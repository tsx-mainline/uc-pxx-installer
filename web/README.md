# web/: the entry page (--plain-entry)

A static page that gets a UC-P10 into the installer RAM image from Google
Chrome, Microsoft Edge or Chromium on a desktop computer (Windows, macOS or
Linux). It is the browser front end of the reviewed CLI
`p10-enter rockusb --plain-entry` (uc-pxx-installer `p10enter-plain-cmdfield`
4934986, tool sha256 `6fb3acd7…`): it mirrors its `route_rockusb` and
`check_plain_entry` logic, plus one rule of its own (step 1, slot A), and it
is governed by RULES.md "The BCB bootonce-bootloader route into fastboot" and
the R-C end-user clause.

1. In the U-Boot download mode (rockusb) it reads, read only, the primary GPT
   (LBA 0..33) and gates the layout (misc at 49152 ×8192, cfg at 3666944); it
   reads the factory block (LBA 3666944, 8 sectors) and gates the model to
   UC-P10 (a 0xCC-only read is "not readable in the download mode", logged, not
   a stop — the model is then gated by the pinned image and the RAM image's own
   model gate; the factory bytes stay in memory, only the model is shown). It
   reads the A/B sector (LBA 49156, read only) and the boot command block (BCB,
   LBA 49184, 4 sectors). The A/B sector must be valid, the slot that boots
   next a keeper (successful), and the other slot a keeper or never tried (the
   CLI's `other_slot_kind`). The page is **stricter** than the CLI here: when
   slot B runs, slot A must be successful too. The entry's A/B reset always
   makes slot A the installer's keeper, so a unit that runs slot B while slot A
   is not marked as successful is refused before any write.
2. It writes the boot command `bootonce-bootloader` to the BCB unless the
   command field holds a pending recovery/OTA command. The BCB gate is the
   command field (bytes 0..31), not a whole-block sha: U-Boot acts, by an exact
   `strcmp`, only on `bootonce-bootloader`, `boot-recovery` and
   `boot_recovery`, and ignores every other field. So an empty field or an
   unrecognised leftover is written over (the leftover is RAM garbage that
   varies from boot to boot, for example `b?` or the bytes `d8 85 00 …`); a
   block that is already the whole payload skips the write (idempotent); a
   pending `boot-recovery`/`boot_recovery` is **not** clobbered — the page
   stops before the write. (The whole-block allow-list `bcb-known-states.json`
   `ed0824f2…` is the CLI's pin and is not the page's gate.) It reads the BCB
   back, confirms the A/B sector did not change during the write, and resets
   the unit (`rd 0`). The page's only eMMC write is the 2048-byte BCB payload
   (sha256 `892627cb…`); it never writes LBA 49156, the GPT or sector 64.
3. Entering fastboot clears the BCB, which makes U-Boot's libavb reset the A/B
   block to its defaults, both slots trying (the benign reset). In fastboot the
   page sends single `getvar`s and boots only when they show exactly the
   measured reset: current-slot "a"; slot-unbootable a and b both "no";
   slot-successful a and b both "no"; slot-retry-count a == 6 and b == 7 (the
   same rule as `check_plain_entry`). Any other post-entry state, or a getvar
   that does not answer, keeps the unit in fastboot and asks for help; it never
   boots. `product` is logged but is not a gate (as in the CLI). Identity across
   the reset is the USB serial string: when the download-mode device has one,
   the fastboot device must have the same one (none or another one stops).
   Then it checks the image sha256 against the pinned release image and sends
   `download` and `boot`. A unit already in fastboot is refused (there is no
   pre-entry read).
4. It opens the installer's USB serial shell (Web Serial) and saves a log. The
   log holds no unit serial, no MAC and no raw BCB/factory bytes.

The page is approved for end users (owner 2026-11-08): `js/config.js` has
`DEVICE_USE_APPROVED = true` and pins the release-1.2 UC-P10 install image
sha256 `3c522be8…` (token `p10inst-f9ce0a`). The image is not in the git repo:
it is a GitHub release asset, and the Pages workflow copies it into the
site's `images/`, so the page fetches it from its own origin. If that fails, the
user downloads it from the Releases page and chooses the file; the sha256 gate
is the same for both.

## Layout

| Path | What |
|---|---|
| `site/` | The page. Only this folder is published. No build step, no minifier, no third-party code |
| `site/js/rockusb.js` | The rockusb client: a port of part of rkdeveloptool (read LBA 49156/49184/GPT/factory, write the BCB, reset) |
| `site/js/partmap.js` | Read-only GPT decoder and the layout gate (port of the CLI `gpt_parts`/`check_layout`). Named `partmap.js`, not `gpt.js`, because ad blockers block the path `js/gpt.js` (it collides with Google Publisher Tag's `gpt.js`) |
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
