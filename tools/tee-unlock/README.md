<!-- SPDX-License-Identifier: GPL-2.0-or-later -->
# tee-unlock — the UC-Pxx bootloader-unlock client

`tee_unlock.c` reads and sets the authoritative OP-TEE unlock bit `lock_state`
through the no-policy storedata pseudo-TA. It is the client the installer page's
optional tier-A unlock guide offers (over LAN adb, on an installed Android
userdebug). See `../../web/site/tools/unlock-linux-macos.sh` and
`unlock-windows.ps1` for the one-liners that drive it.

## Build (reproducible, Android arm64)

```sh
NDK=/path/to/android-ndk-r26d sh build.sh
# out/tee_unlock.android-arm64
```

It links against a **stub** `libteec.so` built here from the public BSD
`include/tee_client_api.h` (OP-TEE optee_client) — never the device's
`libteec.so`. At run time on the unit the device's own `/vendor/lib64/libteec.so`
provides the `TEEC_*` symbols. The recipe, the pins (NDK sha, container digest,
`SOURCE_DATE_EPOCH`) and the output sha256
(`6ba5d28c0854f9ca110f22fe5a3a108a1ba068cab40755256061bb83292fc205`) are in
`../../SOURCES.md`.

## Files

| File | What | License |
|---|---|---|
| `tee_unlock.c` | the client (read / unlock / lock) | GPL-2.0-or-later |
| `stub/libteec_stub.c` | the link-only stub libteec | GPL-2.0-or-later |
| `include/tee_client_api.h` | the GP TEE Client API header | BSD-2-Clause (OP-TEE) |
| `build.sh` | the reproducible build driver | GPL-2.0-or-later |

## Safety

- Always `read` before and after an `unlock`; confirm the byte changed.
- **Never `lock`** a unit that runs mainline from a slot: a re-lock makes the
  unsigned system fail verify. The one-liners and the page never offer `lock`.
- Real device use is owner-gated (RULES.md). The reproducible binary here is
  byte-different from the hardware-proven client `fc6eea50…`; the owner decides
  whether to prove it on a unit or keep the proven one (SOURCES.md).
