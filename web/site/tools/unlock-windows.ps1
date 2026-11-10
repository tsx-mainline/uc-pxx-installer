# SPDX-License-Identifier: GPL-2.0-or-later
# UC-P10 / UC-Pxx bootloader unlock over LAN adb -- Windows PowerShell.
#
# Copyright (C) 2026 the tsx-mainline contributors. GPL-2.0-or-later.
#
# Run this on your computer, NOT on the unit, AFTER you installed Android
# userdebug and the unit is on your network. It sets the device's authoritative
# unlock bit (lock_state 0x00 -> 0x01), verifies the read-back, reboots, and
# confirms verifiedbootstate=orange. It stops on any mismatch.
#
# It NEVER re-locks: a re-lock would make an unsigned (mainline) slot fail
# verify.
#
# Before you run it:
#   1. The unit runs Android userdebug and is on your LAN. Find its IP.
#   2. Enable adb on the unit; connect it to the same network.
#   3. Download "tee_unlock.android-arm64" from the installer page (same folder
#      as this script) into the folder you run this from.
#
# Usage (PowerShell):  $env:IP="192.168.1.50"; .\unlock-windows.ps1
$ErrorActionPreference = "Stop"
function Fail($m) { Write-Error "STOP: $m"; exit 1 }

$IP = $env:IP
if (-not $IP) { Fail "set `$env:IP to the unit's address, e.g. `$env:IP='192.168.1.50'" }
$D = "${IP}:5555"
$C = if ($env:CLIENT) { $env:CLIENT } else { "tee_unlock.android-arm64" }
$CLIENT_SHA256 = "6ba5d28c0854f9ca110f22fe5a3a108a1ba068cab40755256061bb83292fc205"
$LL = "LD_LIBRARY_PATH=/vendor/lib64:/system/lib64 /data/local/tmp/$C"
$MARKER = "/cache/uc-pxx/unlock-marker"

if (-not (Get-Command adb -ErrorAction SilentlyContinue)) { Fail "adb is not installed (install the Android platform-tools)." }
if (-not (Test-Path $C)) { Fail "the client file '$C' is not in this folder (download it from the installer page)." }

# 1) the client must be the pinned binary.
$got = (Get-FileHash -Algorithm SHA256 $C).Hash.ToLower()
if ($got -ne $CLIENT_SHA256) { Fail "the client sha256 is $got, not the pinned $CLIENT_SHA256. Download it again." }

# Join multi-line native output into one string before matching (adb prints two
# lines for `unlock`; -match on an array returns the matching element, not a bool).
function AdbText([string]$cmd) { return ((adb -s $D shell $cmd 2>&1) -join "`n") }

# 2) connect and gate the device (UC-P10 only; the UC-P8 is not offered).
if ((adb connect $D) -notmatch "connected|already") { Fail "cannot reach the unit at $D." }
adb -s $D wait-for-device
$model = (adb -s $D shell getprop ro.product.model).Trim()
if ($model -ne "UC-P10") { Fail "ro.product.model is '$model', not UC-P10. Wrong device." }
$type = (adb -s $D shell getprop ro.build.type).Trim()
if ($type -ne "userdebug") { Fail "ro.build.type is '$type', not userdebug. Only userdebug honours the unlock bit." }
adb -s $D root 2>$null | Out-Null
Start-Sleep -Seconds 2
adb connect $D 2>$null | Out-Null
adb -s $D wait-for-device
adb -s $D push $C /data/local/tmp/ | Out-Null
adb -s $D shell chmod 755 /data/local/tmp/$C

# 3) read lock_state.
$r = AdbText "$LL read"
$skip = $false
if ($r -match "0x00 \(LOCKED\)") { Write-Host "lock_state is LOCKED (0x00); unlocking." }
elseif ($r -match "0x01 \(UNLOCKED") { Write-Host "already UNLOCKED (0x01); skipping the write."; $skip = $true }
else { Fail "unexpected lock_state read: '$r'. Nothing written; ask for help." }

# 4) write and verify by a FRESH read (not the 'wrote...' echo).
if (-not $skip) {
  $w = AdbText "$LL unlock"
  if ($w -notmatch "wrote lock_state = 0x01") { Fail "the unlock write did not report success. Ask for help." }
  $rb = AdbText "$LL read"
  if ($rb -notmatch "lock_state = 0x01 \(UNLOCKED") { Fail "the read-back is not 0x01. Ask for help; do NOT reboot yet." }
}
adb -s $D shell rm -f /data/local/tmp/$C
adb -s $D shell sync 2>$null | Out-Null

# 5) reboot and confirm orange.
Write-Host "rebooting the unit to confirm the unlock..."
adb -s $D reboot
Start-Sleep -Seconds 40
adb connect $D 2>$null | Out-Null
adb -s $D wait-for-device
$vbs = (adb -s $D shell getprop ro.boot.verifiedbootstate).Trim()
if ($vbs -ne "orange") { Fail "verifiedbootstate is '$vbs', not orange. Ask for help." }

# 6) only NOW, after orange is confirmed, write the unlock marker to /cache
#    (unencrypted; the installer reads it from the 'cache' partition by name).
$stamp = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
$marktext = "lock_state=0x01`nverifiedbootstate=orange`nmodel=$model`nclient_sha256=$CLIENT_SHA256`ntool=$C`nunlocked_at=$stamp`n"
adb -s $D shell "mkdir -p /cache/uc-pxx && printf '%s' '$marktext' > $MARKER" 2>$null | Out-Null
Write-Host "UNLOCK VERIFIED: lock_state 0x01, verifiedbootstate orange. The unit is unlocked."
