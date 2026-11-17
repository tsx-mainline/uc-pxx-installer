# web/: the installer page (RC-10, end user)

A static page that installs a UC-P10 from Google Chrome, Microsoft Edge or
Chromium on a desktop computer (Windows, macOS or Linux), in the steps of
`plan/uc-pxx/enduser-page/DESIGN.md` v3 and `DESIGN-R23.md` (not in this
repository): 1 Prepare, 2 Files, 3 Connect, 4 Start, 5 Install, then for
Mainline Linux 6 System and 7 Finish (Android userdebug: 6 Done). The screen
shows one plain line per step; every detail (slots, the A/B state, the boot
command, the safety reasons) goes to the log (Details).

The entry is the reviewed `--plain-entry` route and is unchanged: in the
download mode the page reads (read only) the GPT, the factory block, the A/B
sector and the boot command block (BCB), and gates the layout, the model, the
A/B rule (the slot that boots next successful; when slot B runs, slot A
successful too), the optional `expect-ab` binding and the BCB command field.
"Start the installer" is the consent to its only eMMC write: the boot command
(LBA 49184, 4 sectors, sha256 `892627cb…`), read back, then `rd 0`. In fastboot
the page checks the same USB serial string and the exact measured benign
reset, then sends the pinned image (sha256 again in memory, at most
max-download-size) with `download` and `boot` only.

Then the RAM installer takes the step-1 files over the USB cable (I-6, the
step-2 transport's identity 1d6b:0105): the Crestron firmware file (its size
and sha256 checked by the page first), for mainline the optional (Console
only: required) ssh key and the base (`base/uc-pxx-mainline/` of this site,
the manifest pinned first). The unit checks every byte again; nothing is
written before the one hold on the unit's plan screen. The drive UC-PXX-INST is
the fallback. For mainline, slot B then installs the chosen system (the unit
fetches the pinned bundle; the cable is the fallback), the page gives the
step-2 consent (the unit clones B into A), and sends the pinned finish set.

Next to a greyed Next (Prepare, Files, Connect) the page says what is
missing, one line for each thing (R25). The Install card shows the
bootloader unlock as its own step when the installer reports the phase word
`unlock` (RC-10c, R26). The System card shows a bar for step 2's copy of the
system while the unit's step-2 code is `started <id12> copy <D> of <T>`
(R32). On Linux the page shows the udev lines at the steps that use the
devices, with the ModemManager ignore for `1d6b:0105` at Start, and during a
step-1 send it asks the person not to press keys on the unit (R27).

Release 1.3.0 (mainline RC-10b) pinned its image, base, bundles and finish
list and turned the three flags on. RC-10c (local test) (`PAGE_VERSION`
"1.4.0-rc10c-local (2026-11-17): mainline RC-10c (local test), install image
pxx-inst-804423") pins the RC-10c install image, base, bundles and finish list
and turns the three flags on, for the owner's end-to-end run from a local copy
of the page. It is not the public page: the Pages workflow deploys only `main`.
`web/test/lan-site.sh` patches only the placeholder lines of the dev state, so
it refuses this config.

## Layout

| Path | What |
|---|---|
| `site/` | The page. Only this folder is published. No build step, no minifier, no third-party code |
| `site/js/main.js` | The steps, one action at a time; the loops of the transfer link (step 1, the system, the finish) |
| `site/js/steps.js`, `site/js/texts.js` | The step lists and the status maps; every text the script puts on the screen, the stop box |
| `site/js/progress.js` | The one progress bar (amount, total, the time left computed from the last 10 s) |
| `site/js/sha256stream.js` | An incremental SHA-256 for large files (crypto.subtle has no streaming digest) |
| `site/js/download.js` | The image and the finish set from this site, the base (manifest first), the check of a chosen file against its pin |
| `site/js/step1usb.js` | Step 1 over the cable: firmware.bin, SSH-KEY.PUB, COMMIT {mode}; the base, COMMIT {}; DRIVE |
| `site/js/provisionusb.js` | The system install: the choice, the cable bundle, the step-2 consent (the installer's Page interface §6.10) |
| `site/js/step2usb.js`, `site/js/finishusb.js` | The PXX1 frames and session (Step2Link); the finish set |
| `site/js/rockusb.js` | The rockusb client: a port of part of rkdeveloptool (read LBA 49156/49184/GPT/factory, write the BCB, reset) |
| `site/js/partmap.js` | Read-only GPT decoder and the layout gate. Named `partmap.js`, not `gpt.js`, because ad blockers block the path `js/gpt.js` |
| `site/js/factory.js` | Read-only factory-block model decoder and the 0xCC rule |
| `site/js/avbab.js` | Read-only A/B (AvbABData) decoder and the keeper/never-tried rule |
| `site/js/fastboot.js` | The fastboot client: single getvars from a fixed list, download, boot |
| `site/js/enter.js` | The entry's steps and their guards, including the benign-reset validation (`check_plain_entry`) |
| `site/js/config.js` | The reviewed constants: the approval flags, the model, the pins |
| `site/js/drivecopy.js` | The drive fallback: host-side file I/O only (File System Access API), every file read back, nothing deleted |
| `site/js/sshkey.js` | The check of the optional ssh public key (pure, no DOM); the RAM installer applies the same rule |
| `test/unit/sshkey-vectors.json` | The shared test vectors of that rule |
| `test/unit/mockup-v3-texts.json` | The texts of the mock-up v3's normal-path frames (b83684f4…), for the text test |
| `test/fake/fakeusb.js` | A fake unit: rockusb, fastboot, the RAM installer's step-1 link, slot B's system link, the finish, Web Serial |
| `test/unit/` | Node unit tests and static source checks (Node built-ins only) |
| `test/e2e/` | Playwright tests of the page in headless Chromium with the fake unit |

## The optional ssh public key

For Mainline Linux the person may give one ssh public key for the new system
(on the Files step; optional for the Kiosk systems, required for Console only). The page checks it (`js/sshkey.js`),
shows its type, size and fingerprint, and needs the box "This is my key
(fingerprint …)" ticked. It sends it to the RAM installer as `SSH-KEY.PUB` (the
normalised line + one LF) with the firmware file, or writes it to the drive in
the fallback. The Android userdebug goal never sends a key.

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
