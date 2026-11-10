#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# UC-P10 / UC-Pxx bootloader unlock over LAN adb -- Linux and macOS.
#
# Copyright (C) 2026 the tsx-mainline contributors. GPL-2.0-or-later.
#
# Run this on your computer, NOT on the unit, AFTER you installed Android
# userdebug on the unit and the unit is on your network. It sets the device's
# authoritative unlock bit (lock_state 0x00 -> 0x01) through the OP-TEE
# storedata pseudo-TA, verifies the read-back, reboots, and confirms
# verifiedbootstate=orange. It stops on any mismatch.
#
# It NEVER re-locks. "lock" is deliberately not offered: a re-lock makes an
# unsigned slot (mainline) fail verify.
#
# Before you run it:
#   1. The unit runs Android userdebug and is on your LAN. Find its IP in the
#      unit's network settings (or your router).
#   2. Enable adb on the unit and connect it to the same network.
#   3. Download the unlock client "tee_unlock.android-arm64" from the installer
#      page (same folder as this script) into the folder you run this from.
#   4. Set IP below to the unit's address.
#
# Usage:  IP=192.168.1.50 sh unlock-linux-macos.sh
set -eu

: "${IP:?set IP to the unit's address, e.g. IP=192.168.1.50 sh unlock-linux-macos.sh}"
D="$IP:5555"
C="${CLIENT:-tee_unlock.android-arm64}"
CLIENT_SHA256=6ba5d28c0854f9ca110f22fe5a3a108a1ba068cab40755256061bb83292fc205
# The unit-side run line: the device's own libteec provides the TEEC_* symbols.
LL="LD_LIBRARY_PATH=/vendor/lib64:/system/lib64 /data/local/tmp/$C"
MARKER=/cache/uc-pxx/unlock-marker

fail() { echo "STOP: $*" >&2; exit 1; }

command -v adb >/dev/null 2>&1 || fail "adb is not installed (install the Android platform-tools)."
[ -f "$C" ] || fail "the client file '$C' is not in this folder (download it from the installer page)."

# 1) the client must be the pinned, reviewed binary (byte for byte).
if command -v sha256sum >/dev/null 2>&1; then GOT=$(sha256sum "$C" | cut -c1-64)
else GOT=$(shasum -a 256 "$C" | cut -c1-64); fi
[ "$GOT" = "$CLIENT_SHA256" ] || fail "the client sha256 is $GOT, not the pinned $CLIENT_SHA256. Download it again."

# 2) connect and gate the device: UC-P10, userdebug, rooted. (This page is
#    UC-P10 only; the UC-P8 is not offered.)
adb connect "$D" >/dev/null || fail "cannot reach the unit at $D."
adb -s "$D" wait-for-device
MODEL=$(adb -s "$D" shell getprop ro.product.model | tr -d '\r')
[ "$MODEL" = UC-P10 ] || fail "ro.product.model is '$MODEL', not UC-P10. Wrong device."
TYPE=$(adb -s "$D" shell getprop ro.build.type | tr -d '\r')
[ "$TYPE" = userdebug ] || fail "ro.build.type is '$TYPE', not userdebug. Only userdebug honours the unlock bit."
adb -s "$D" root >/dev/null 2>&1 || true
sleep 2
adb connect "$D" >/dev/null 2>&1 || true
adb -s "$D" wait-for-device
adb -s "$D" push "$C" /data/local/tmp/ >/dev/null || fail "could not push the client."
adb -s "$D" shell chmod 755 /data/local/tmp/"$C"

# 3) read lock_state. Proceed ONLY on exactly 0x00 LOCKED; 0x01 = already
#    unlocked (skip the write); anything else = stop.
R=$(adb -s "$D" shell "$LL read" | tr -d '\r')
SKIP=
case "$R" in
  *"0x00 (LOCKED)"*)   echo "lock_state is LOCKED (0x00); unlocking." ;;
  *"0x01 (UNLOCKED"*)  echo "lock_state is already UNLOCKED (0x01); skipping the write."; SKIP=1 ;;
  *)                   fail "unexpected lock_state read: '$R'. Nothing written; ask for help." ;;
esac

# 4) write 0x01 and verify by a FRESH read (not the 'wrote...' echo).
if [ -z "$SKIP" ]; then
  adb -s "$D" shell "$LL unlock" | tr -d '\r' | grep -q "wrote lock_state = 0x01" \
    || fail "the unlock write did not report success. Ask for help."
  adb -s "$D" shell "$LL read" | tr -d '\r' | grep -q "lock_state = 0x01 (UNLOCKED" \
    || fail "the read-back is not 0x01. Ask for help; do NOT reboot yet."
fi
adb -s "$D" shell rm -f /data/local/tmp/"$C"
adb -s "$D" shell sync || true

# 5) reboot and confirm orange.
echo "rebooting the unit to confirm the unlock..."
adb -s "$D" reboot
sleep 40
adb connect "$D" >/dev/null 2>&1 || true
adb -s "$D" wait-for-device
VBS=$(adb -s "$D" shell getprop ro.boot.verifiedbootstate | tr -d '\r')
[ "$VBS" = orange ] || fail "verifiedbootstate is '$VBS', not orange. Ask for help."

# 6) only NOW, after orange is confirmed, write the unlock marker to /cache
#    (unencrypted ext4; the installer reads it from the 'cache' partition by
#    name). It is a claim, not the authority.
STAMP=$(date -u +%Y-%m-%dT%H:%M:%SZ)
adb -s "$D" shell "mkdir -p /cache/uc-pxx && printf 'lock_state=0x01\nverifiedbootstate=orange\nmodel=%s\nclient_sha256=%s\ntool=%s\nunlocked_at=%s\n' '$MODEL' '$CLIENT_SHA256' '$C' '$STAMP' > $MARKER" \
  || echo "note: could not write the marker to $MARKER (not fatal)."
echo "UNLOCK VERIFIED: lock_state 0x01, verifiedbootstate orange. The unit is unlocked."
