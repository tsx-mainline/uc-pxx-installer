# web/: the entry page (--plain-entry)

A static page that gets a UC-P10 into the installer RAM image from Chrome or
Edge on a desktop computer. It is the browser front end of the reviewed CLI
`p10-enter rockusb --plain-entry` (uc-pxx-installer `p10enter-plain` f631a8a,
tool sha256 `084394fb…`): it mirrors its `route_rockusb` and `check_plain_entry`
logic, and it is governed by RULES.md "The BCB bootonce-bootloader route into
fastboot" and the R-C end-user clause.

1. In the U-Boot download mode (rockusb) it reads, read only, the primary GPT
   (LBA 0..33) and gates the layout (misc at 49152 ×8192, cfg at 3666944); it
   reads the factory block (LBA 3666944, 8 sectors) and gates the model to
   UC-P10 (a 0xCC-only read is "not readable in the download mode", logged, not
   a stop — the model is then gated by the pinned image and the RAM image's own
   model gate; the factory bytes stay in memory, only the model is shown). It
   reads the A/B sector (LBA 49156, read only) and the boot command block (BCB,
   LBA 49184, 4 sectors).
2. It writes the boot command `bootonce-bootloader` to the BCB, only when the
   A/B sector is valid, the slot that boots next is a keeper and the other slot
   is a keeper or never tried (the factory-stock state), and the BCB command
   field does not hold a pending recovery/OTA command. The BCB gate is the
   command field (bytes 0..31) alone, not a whole-block sha: U-Boot acts, by an
   exact `strcmp`, only on `bootonce-bootloader`, `boot-recovery` and
   `boot_recovery`, and ignores every other field. So an empty field or an
   unrecognised leftover (e.g. the deterministic `b?` left after a BCB-route
   entry) is written over; `bootonce-bootloader` already set skips the write
   (idempotent); a pending `boot-recovery`/`boot_recovery` is **not** clobbered —
   the page stops before the write. (The whole-block allow-list
   `bcb-known-states.json` `ed0824f2…` is the CLI's pin and is no longer the
   page's gate; it refused real post-boot/post-install units.) It reads the BCB
   back, confirms the A/B sector did not change during the write, and resets the
   unit (`rd 0`). The page's only eMMC write is the 2048-byte BCB payload; it
   never writes LBA 49156, the GPT or sector 64.
3. Entering fastboot clears the BCB, which makes U-Boot's libavb reset the A/B
   block to a valid, bootable, **both-slots-trying** state (the benign reset).
   In fastboot the page sends single `getvar`s and boots only when the getvars
   show exactly that benign reset (current slot bootable, neither slot
   successful, the current slot took a try) — mirroring `check_plain_entry`.
   Any other post-entry state keeps the unit in fastboot and asks for help; it
   never boots. `product` is logged but is not a gate (as in the CLI); identity
   across the reset is the USB serial-string match. Then it checks the image
   sha256 against the pinned release image and sends `download` and `boot`. A
   unit already in fastboot is refused (there is no pre-entry read).
4. It opens the installer's USB serial shell (Web Serial) and saves a log. The
   log holds no unit serial, no MAC and no raw BCB/factory bytes.

> **Not for use on a unit.** The page is not reviewed. `js/config.js` has
> `DEVICE_USE_APPROVED = false`, so the page refuses every device action. It
> stays false until the owner's RULES R-C entry, a new Opus review that names
> this commit's shas, and the R4 hardware proof on unit 3.

## Layout

| Path | What |
|---|---|
| `site/` | The page. Only this folder is published. No build step, no minifier, no third-party code |
| `site/js/rockusb.js` | The rockusb client: a port of part of rkdeveloptool (read LBA 49156/49184/GPT/factory, write the BCB, reset) |
| `site/js/gpt.js` | Read-only GPT decoder and the layout gate (port of the CLI `gpt_parts`/`check_layout`) |
| `site/js/factory.js` | Read-only factory-block model decoder and the 0xCC rule (port of `factory_model`/`factory_unserved`) |
| `site/js/avbab.js` | Read-only A/B (AvbABData) decoder and the keeper/never-tried rule |
| `site/js/fastboot.js` | The fastboot client: single getvars from a fixed list, download, boot |
| `site/js/enter.js` | The steps and their guards, including the benign-reset validation (`check_plain_entry`) |
| `site/js/config.js` | The reviewed constants: the approval flag, the model, and the pinned release image |
| `test/fake/fakeusb.js` | A fake unit: WebUSB rockusb and fastboot, Web Serial, and a record of every transfer |
| `test/unit/` | Node unit tests and static source checks (Node built-ins only) |
| `test/e2e/` | Playwright tests of the page in headless Chromium with the fake unit |

## Tests

```
node --test web/test/unit/*.test.mjs     # no dependencies
web/test/run-docker.sh                   # all tests, in the pinned Playwright image
```

The browser tests replace `navigator.usb` and `navigator.serial` with the
fake before the page loads, and the container has no USB device and no
network. No test can reach a real unit.

## Licence

The page is free software under the GNU General Public License, version 2
or (at your option) any later version (`site/LICENSE.txt`, the same text as
the repository's `LICENSE`).

`site/js/rockusb.js` is a port of parts of rkdeveloptool
(<https://github.com/rockchip-linux/rkdeveloptool>, commit
`304f073752fd25c854e1bcf05d8e7f925b1f4e14`: `RKComm.h`, `RKComm.cpp`,
`RKScan.cpp`), (C) 2017 Fuzhou Rockchip Electronics Co., Ltd, licensed
GPL-2.0+. The file header lists the changes.

The page loads no third-party code. The tests use `@playwright/test`
1.64.1 (Apache-2.0), pinned with its integrity hash in
`test/package-lock.json`. It is a test tool only and is not part of the
published page.
