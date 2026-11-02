# uc-pxx-installer

A RAM-boot installer for the official firmware of Crestron UC-P10 room
phones. It installs one official firmware onto one A/B slot, and then makes
that slot the next boot. The other slot stays untouched as the fallback.
"Official" means two things: the file is signed by Crestron (the installer
checks the AVB signature, the certificate chain and every hashed image on the
unit, offline), and the installer has a reviewed manifest for it that allows
the install (see `manifests/`). This release offers one file: 1.0.0.94
userdebug. 1.0.6.20 booted on a unit we have but is not offered yet (its stock
bootloader was never written by the installer under supervision). 23 more are
refused until a supervised first install proves their bootloader ("proven
only, then widen"); 12 older ones are refused for good (they would not power
the screen of current boards); a file without a manifest is refused. The installer supports only
boards with the panel supply on PMIC switch 2: before any write the user
confirms that the installer's screen shows on the unit's panel.

The installer runs from RAM (`fastboot boot`). It uses the device screen for
the status and the logs, and the device keys for input. The computer only
starts it, and then acts as a USB drive for the firmware file and as an
optional serial shell.

It is the sister project of
[uc-pxx-linux](../uc-pxx-linux/README.md), which owns the A/B boot-control
tool `uc-pxx-bootctl`.

> **Safety.** Secure boot is on in this phone. If no slot can boot, no
> software can recover it. At every step, at least one slot must stay
> bootable. The installer writes the partitions of one slot only, and it
> changes the A/B metadata only through `uc-pxx-bootctl`.

## Status

| Area | Status |
|---|---|
| Firmware container: decrypt, unpack, check (`internal/pcdfw`) | Done. Unit tests with a made-up container. Byte-identical to the Python reference on the two supported firmwares |
| Key search in the device libraries (`FindKeys`, `p10inst findkey`) | Done. Finds exactly one key/IV pair in both firmwares |
| Manifests (`manifests/`, `internal/manifest`) | Schema 2, one per public firmware file (37), made by `tools/fw-manifest.py`. Each says install, recognise or refuse, the boot chain and its resource status, the nvram model, the AVB facts |
| AVB verifier (`internal/avb`) | The vbmeta signature, the certificate chain to the pinned Crestron PIK, every hash and hashtree image recomputed in pass 1. Go standard library only |
| A/B decoder, read-only (`internal/avbab`) | Done. Tested with captured sectors |
| `p10inst verify` (check a whole firmware file before any write) | Done. About 26 s per firmware on a PC |
| `uc-pxx-bootctl disable` and `arm-trying --slot a\|b` | In uc-pxx-linux, branch `bootctl-installer`. Reviewed as the separate binary `uc-pxx-bootctl-inst` (sha256 `5d83215b…`) |
| `uc-pxx-bootctl repair` and `switch` (the hold repair, the slot switch) | uc-pxx-linux branch `bootctl-repair`; the binary `uc-pxx-bootctl-inst` sha256 `20aa6bc5…` (5d83215b plus the two commands). **Not reviewed**: the device mode pins it, so the device mode needs a new review before any use |
| `p10inst repair`, `p10inst switch` (device mode) | Written, tested on fake units. Not reviewed |
| Status screen, read-only (`p10inst ui`, `p10inst status`) | Written. Unit tests with a fake device. See "The RAM image" |
| Android auto-update check, read-only (`internal/nvram`) | Written. The rule of the stock nvram driver (1.0.0.94 and 1.0.6.20), checked on unit 2. Unit tests with made-up stores. See "Android auto-update" |
| RAM image (`image/`), USB gadget (serial, network, drive) | Written. See "The RAM image" for the state on the unit |
| Install plan (`p10inst plan`, `internal/plan`) | Written. Every write, every A/B transition and every safety check, for humans and as JSON. Read-only |
| Install, dry run only (`p10inst install --dry-run`, `internal/install`) | Written. Writes go to an image file that stands for the eMMC, never to a device. Power-cut sweep passes |
| Install on a device (`p10inst install --device /dev/mmcblk0`, `internal/device`, `internal/devmode`) | Written, in the install build only. Tested on fake units and on loop devices, never on a unit. Needs its release review before any use. See "The device mode" |
| Firmware file on the P10-INSTALL drive, read-only (`internal/fatfs`, `p10inst drive`) | Written. FAT12/16/32, long names, no mount. Tested with real images (busybox and dosfstools mkfs, mtools, the Linux vfat driver) and fuzzed. Not yet used by the on-screen flow |
| Entry CLI `host/p10-enter` (ssh → `uc-pxx-reboot fastboot`, or rockusb → BCB `bootonce-bootloader`, then `fastboot boot`) | Written. Tested with fake rkdeveloptool, fastboot and ssh (`host/tests/`), never on a unit. Not for use before its Opus review and the owner's approval. The BCB allow-list does not hold unit 2's normal BCB yet |
| Entry page (WebUSB) | Open |

## Rules for this repository

- **No decryption key.** The firmware key is not published. The installer
  finds it on the unit, in the updater libraries of the `system` partition,
  or reads a key file that the user gives. `ci/check-no-key.py` fails when
  the key is anywhere in the tree or in the git history. It holds only a
  32-bit fingerprint of the key.
- **No vendor files.** No firmware, no partition image, no vendor library.
  `ci/lint.sh` checks the file names.
- **Only listed images.** The installer writes an image only when its
  sha256 is in a manifest under the same partition name.

## Layout

| Path | What |
|---|---|
| `cmd/p10inst` | The installer program |
| `internal/pcdfw` | The firmware container, and the key search |
| `internal/avbab` | Read-only decoder of the A/B metadata |
| `internal/blk` | Finds the eMMC partitions by GPT name, opens them read-only, sets the kernel read-only flag |
| `internal/ext4`, `internal/bootimg`, `internal/vendorstore` | Read-only readers: one file of an ext4 file system, the boot image header, the serial number |
| `internal/status` | Collects and renders the status: slots, A/B state, unlock state, Android auto-update, identity |
| `internal/nvram` | Read-only reader of the nvram store of stock Android (partition `others`): only the auto-update keys come out. `nvram/nvramtest` makes test stores (install build and tests only) |
| `internal/ui`, `internal/gadget`, `internal/devmem` | The screen and the touch keys, the USB gadget, read-only SoC registers |
| `image/` | The RAM image: build script, initramfs, kernel config fragment |
| `internal/manifest`, `manifests/` | The firmware manifests, embedded in the binary |
| `internal/gpt`, `internal/vbmeta` | Read-only readers: the GPT, the vbmeta header and its AVB key |
| `internal/fatfs` | Read-only FAT reader for the P10-INSTALL drive image: finds the one firmware `.bin` and reads it in place |
| `internal/drivestate` | Read-only: the lun state of the drive and the identity of the drive file (the drive rule) |
| `internal/flow`, `internal/flowio`, `internal/fatmk` | The on-screen install flow: the state machine (no I/O), its runner, and the FAT32 writer of the log bundle. Only in the install build |
| `internal/plan` | The install plan: the ordered writes and all safety checks. Read-only |
| `internal/install`, `internal/bootctl`, `internal/fakeemmc` | The install side: the executor (one for both media), the dry-run medium, the uc-pxx-bootctl runner for sector copies, test images. Only in the install build |
| `internal/device`, `internal/devmode` | The device mode: the checks of the unit, the only eMMC write layer (`writer.go`), uc-pxx-bootctl-inst, the flow. Only in the install build |
| `internal/device/devicetest` | Fake units for the device-mode tests. Only in the devtest build |
| `tools/` | Python reference tools: `pcdfw-unpack.py`, `pcdfw-findkey.py`, `fw-manifest.py` (it checks the AVB data of the firmware offline before it writes a manifest; tests in `test-fw-manifest.py`) |
| `ci/` | `lint.sh`, `check-no-key.py`, `check-no-writer.py`, `dryrun-check.py`, `dryrun-real.py`, `devmode-loop.py`, `nvram-model-diff.py` with `nvram_drvmodel.py` |

## Build and test

```
ci/lint.sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o p10inst-arm64 ./cmd/p10inst
```

The unit tests need no vendor file. To also test the A/B decoder against
captured sectors, set `UC_PXX_SECTORS` to a folder of `*.bin` sector files.

## p10inst

```
p10inst findkey [--raw] [--write KEYFILE] FW.bin LIB...
p10inst verify  [--keys KEYFILE | --lib LIB ...] FW.bin
p10inst unpack  --keys KEYFILE -o DIR FW.bin
p10inst ab      [--offset N] SECTOR.bin
p10inst status  [--root DIR]
p10inst readonly
p10inst ui      [--tty DEV | --stdin] [--leds]
p10inst usbd    [--drive FILE] [--once]
p10inst drive   [FILE]
```

None of these commands write to a device. The install build adds the dry
run on image files (see "The install build") and the device mode (see "The
device mode"). `status`, `readonly`, `ui` and
`usbd` run on the device, in the RAM image. `drive` reads the drive image
(default `/run/p10inst/drive/drive.img`) read-only and names the firmware
file the installer would use, or says why it refuses the drive. `readonly` sets the kernel
read-only flag of the eMMC and its partitions. That changes no byte on the
eMMC. `findkey` never prints the key.
Exit status: 0 OK, 1 a check failed, 2 usage or I/O error.

## The RAM image

`image/build.sh` builds a boot image for `fastboot boot`. It runs on a Linux
build host with docker and the aarch64 cross compiler:

```
image/apk-cache.sh fetch --cache ~/.cache/p10inst-apk     # once, needs the network
image/build.sh --kernel LINUX-UC-PXX --linux UC-PXX-LINUX [--inputs FILE]
```

The kernel is the uc-pxx kernel at the revision that uc-pxx-linux pins. The
build uses `kernel/mkimage.sh` of uc-pxx-linux unchanged.

The build is reproducible: the same inputs give the same image bytes, on any
path, as any user and at any time.

- The Alpine packages are pinned by version and sha256 in `image/apk.lock`.
  The build reads them only from the local cache, and its containers have no
  network. A missing or changed file stops the build. Alpine removes old
  versions from its mirrors, so keep the cache of each release with it.
- An inputs file (`--inputs`, format in `image/build.sh`) gives the token,
  the build time, the kernel user and host, and the hash of every other
  input: this commit, the lock, the kernel revision, `mkimage.sh` and
  `shim.S`, the docker images, the cross compiler, xz, dtc and mkbootimg. The
  build stops at the first hash that differs.
- The initramfs has one file time, sorted names, owner 0:0 and no inode or
  device numbers of the build host. The kernel is built at fixed paths in a
  user and mount namespace of its own (`image/kbuild.sh`), with
  `KBUILD_BUILD_TIMESTAMP`, `KBUILD_BUILD_USER` and `KBUILD_BUILD_HOST`
  from the inputs.
- Without `--inputs` it is a dev build: a new random token and the time of
  the installer commit. `build-info.txt` and `inputs.txt` say "dev build".
  Every build writes `inputs.txt`. With it, the same build can be made again.

A release image is named by its sha256 and by its inputs file. The token is
not unique to an image: two inputs files with the same token give two
different images. Check the sha256 of an image, not only its token.

The image has no Rockchip resource (its "second" is empty), so it holds no
vendor file. U-Boot does not use the second on `fastboot boot`: on unit 2 an
image with no second, one with a resource of our own device tree and one
with the stock resource gave the same U-Boot output and the same working
system (2026-10-25). `--resource FILE` still adds one. The image is for
`fastboot boot` only. Never write it to a boot partition: a `boot_a` image
must carry the stock resource.

The image runs from RAM only. It changes nothing on the eMMC:

- It opens the eMMC read-only and mounts no eMMC file system. Its ext4
  reader reads files without the kernel.
- At start it sets the kernel read-only flag on the eMMC and on each
  partition.
- It has no boot-control tool and no disk tool. `ci/check-no-writer.py`
  checks the sources and the built initramfs.

What it does:

- The screen shows the status: the A/B state of both slots, the next slot
  and the fallback, what each slot holds (U-Boot, boot image, system), the
  unlock state as far as it can be proven, the Android auto-update setting
  (see "Android auto-update"), the model, the serial number and the MAC
  address. The touch keys drive it: Vol+ and Vol- choose, the
  Mic (mute) key is OK, the Speaker key is back. Hold OK for 3 s on "Restart" to
  leave.
- A restart writes the normal boot mode (the reboot-mode node of the device
  tree), so U-Boot boots the slot that the A/B metadata selects.
- After a crash (kernel panic) the unit restarts into fastboot after 30 s,
  because a panic restart does not write the boot mode. This is wanted: the
  user can start the installer again.
- The USB gadget has a serial port with a root shell, a network link
  (the device is 10.99.0.2, with DHCP and ssh) and the drive P10-INSTALL
  (1200 MiB in RAM). It binds only while the USB PHY sees VBUS (the PHY
  quirk of the UC-P10).
- The root login has no password, by the choice of the project owner. Only
  the USB cable reaches it: the USB serial shell and ssh on the USB network
  address. Nothing listens on Ethernet. Keep it that way.

### The install image (`--install`)

**Not for device use yet.** No install image is reviewed. Before one is used on
a unit, an Opus release review must name its sha256 and the user must add it to
RULES.md. Until then the device mode runs only as RULES.md says.

```
image/build.sh --kernel LINUX-UC-PXX --linux UC-PXX-LINUX --install --bootctl uc-pxx-bootctl-inst [--inputs FILE]
```

The same script and the same steps make the install image. It differs from the
status image in these points only:

- `/usr/bin/p10inst` is the release install build (`-tags install`).
- `/usr/libexec/p10inst/uc-pxx-bootctl-inst` is the reviewed A/B tool of
  the device mode (sha256 20aa6bc5..., not reviewed yet), mode 0700. The build refuses any other file.
- `/etc/inittab` and `/etc/profile` come from `image/overlay-install`: tty1
  runs the on-screen install flow (`p10inst flow --leds`) in place of the
  status UI, and the shell banner says that this image can write the eMMC.
  `ci/lint.sh` checks that the two inittabs differ only in the tty1 line.
- `ci/check-no-writer.py --install-image` checks the cpio: exactly one A/B
  tool, at that path, with that sha256 and mode, a release install p10inst
  and no other writer.
- The outputs are `p10inst-install-ucp10.img` and `p10inst-install-arm64`.
- Its inputs file has the format `p10inst-install-image-inputs-1` with one
  more key, `BOOTCTL_INST_SHA256`. A status inputs file does not build an
  install image, and the other way round.
- After the build, the script checks the boot image: header v0, no second,
  the token as the header name, the kernel part = the stub and the Image, the
  initramfs in the Image = the checked cpio, and the device-tree command line
  (`p10inst.ram=1` and `uc-pxx.id=TOKEN` once each, no `root=`,
  `androidboot.`, `skip_initramfs`, `init=`, no quote, printable ASCII).
  `--resource` is refused.

Without `--install` the build is unchanged: the same inputs give the same
status image.

## The install build

`go build -tags install ./cmd/p10inst` adds:

```
p10inst plan      --image FILE --fw FW.bin [--keys F | --lib L ...] --bootctl PATH [--bootctl-exec RUNNER]
                  [--keeper a|b] [--pre-ab HEX] [--prereqs-confirmed] [--autoupdate-unclear-confirmed]
                  [--hash-keeper] [--json OUT]
p10inst install   --dry-run --image FILE (the same options) [--plan PLAN.json] [--log LOG.jsonl]
p10inst fake-emmc --layout FILE -o IMAGE --ab a=P,T,S,b=P,T,S [--fill NAME=FILE ...] [--markers]
                  [--nvram off|on|none]
```

`FILE` is an image file that stands for the eMMC: a regular file with the
GPT of the unit. `install --dry-run` refuses a device node, a symlink and any
path under /dev, /proc or /sys. `install` without `--dry-run` needs
`--device /dev/mmcblk0` (the device mode, below). Without `--keys` or `--lib`, the key comes from
the updater libraries in system_a or system_b of the image, as on the device.

The plan has these steps, in this order:

1. `uc-pxx-bootctl disable --slot T`: the target cannot boot while it is written.
2. Write each partition image of the target. Trust is skipped when it already holds the image.
3. Sync.
4. Re-read every written range and compare its sha256.
5. `uc-pxx-bootctl arm-trying --slot T --lower-keeper`.
6. Read back the A/B sector. Check it with the installer's own decoder and with `uc-pxx-bootctl status`.

A power loss at any point boots the keeper. The plan refuses when it is not sure.
It also refuses while Auto Update Firmware of stock Android is on, and while it
is not clear unless the user confirmed (check `autoupdate`, see "Android
auto-update"). `fake-emmc` puts a
made-up nvram store into `others` (`--nvram off` by default).

uc-pxx-bootctl `--file` works on a 512-byte sector file. So the dry run
copies the A/B sector of the image to a private file, runs the tool on it,
compares the result with the plan's own prediction, and only then puts it
into the image (guarded on the image still holding the old sector).

The status-only RAM image has none of this. `ci/check-no-writer.py`
checks that only files whose build constraint needs `install` import the
install packages, that the import graph of the default build does not hold
them, and that a built p10inst binary does not hold their symbols. The
dry run and the device mode share one executor (`install.Run`); only the
medium differs.

Tests. `ci/lint.sh` runs everything that needs no vendor file. The install
tests and the power-cut sweep need the real uc-pxx-bootctl:
`UC_PXX_BOOTCTL=PATH` (and `UC_PXX_BOOTCTL_EXEC=qemu-aarch64-static` for the
aarch64 binary on a PC). The sweep cuts every install before each write,
as a power loss would, and checks the A/B sector with package avbab after
each cut. `ci/dryrun-real.py` runs the same with a real firmware on a build
host. `ci/dryrun-check.py` is its independent checker.

## Android auto-update

Stock Android can install a firmware by itself (web UI, Settings, Auto
Update, "Auto Update Firmware"). The installer reads that setting and does
not install while it is on or not known.

The setting is nvram P-value 72005 in the nvram store of the Android kernel
(a Grandstream driver). The store is in partition `others`, which both slots
share. Package `internal/nvram` reads it with the rule of the driver (the
same code in the kernels of 1.0.0.94 and 1.0.6.20):

- From byte 0x200000 of `others` there is a ring of 32 parts of 64 KiB. Each
  part has a 0x28-byte header (`FLSH`, the text length, the zlib length,
  flags, a checksum, a 64-bit write counter at 0x18), then a zlib stream of
  NUL-separated `key=value` records. The header is not under the checksum.
- The driver reads absolute sectors (LBA 0x381400 + 0x80 per part), so
  `others` must start at LBA 3671040. The status checks this; the device
  mode checks the whole layout of the unit.
- The package follows the driver step by step (the package comment has the
  code offsets): the scan (magic, the checksum with its signed bound, the
  last part with the highest counter, else the default part 0), the load
  (flags 0: no check and 0xffd8 bytes inflated; success only when the stream
  ends; the text length not used), and the record parse (prefix `:` or 1
  removed, 2 and 3 lock the key and 3 hides it, the later record wins, a
  record without `=` ends the parse, memory after the text is not cleared).
  Where the driver's result depends on memory it reads past the part or the
  text, the package takes every possible result.
- Android reads 72005 with `NvramManager.getBool`: `true` and `1` are true,
  anything else false. The web UI shows "on" only for `true`.
- The state is "on" when a part that Android may load gives 72005 a true
  value: the active part, every part that ties on the highest counter, a
  part whose checksum reads past the part, the default part 0 with flags 0
  when no part is valid, and also any one of several 72005 records, or a
  true value before memory that is not cleared.
- The state is "off" only when the driver certainly loads one part, that
  part is as the driver itself writes parts (flags 1, a zlib length inside
  the part, the right text length, an end record, every record with `=`,
  each of the three keys once, no lock prefix on 72005), and get gives
  72005 the value `false`.
- Otherwise it is "unknown" (not clear), also when `others` is not at LBA
  3671040 (status image) or not there or too small.
- `ci/lint.sh` checks this rule against a model of the driver
  (`ci/nvram_drvmodel.py`, written by the independent release review from
  the driver code, not from this package): `ci/nvram-model-diff.py` runs the
  review's targeted cases and 3000 random stores with fixed seeds (`--long`
  or `NVRAM_MODEL_CASES=30000`: the 60,000 of the review). It fails on "off"
  when the driver does not give exactly `false`, on "unknown" or "off" when
  the driver can give true, and on a targeted case whose state is not the
  reviewed one.

The status shows the state, Auto Update APK (72012) and the URL (72016). The
store also holds secrets (the admin password and hashes): the package
returns only these three keys, and only `true`/`false` of the two flags.
The install plan (check `autoupdate`) follows the rule of the project owner
(INSTALLER-END-USER-PLAN.md §6.1 U2):

| State | Plan |
|---|---|
| off | passes |
| on | refuses, always: no flag passes it |
| unknown (not clear) | refuses, unless the user confirmed to go on: `--autoupdate-unclear-confirmed` (the on-screen flow sets it from its confirmation screen) |

`--prereqs-confirmed` is a separate confirmation and does not set
`--autoupdate-unclear-confirmed`. The plan JSON records what was read and
the confirmation (`"autoupdate": {"state", "active_part", "write_counter",
"valid_parts", "why", "unclear_confirmed"}`), so the plan id binds it: a plan
approved with the confirmation does not run without it. The plan output
has an `autoupdate` line, and the status screen says whether the installer
refuses (on) or needs the confirmation (not clear). In the device mode the
layout check of the unit pins `others` at LBA 3671040; a moved `others` is
refused there, whatever was confirmed. The source of the rule of the driver
is `docs/android-update-settings.md` of the UC-P10 working notes.

## The device mode

> **Not released.** No unit has run it. It needs its own independent review
> (RULES.md rule 9) of the exact binaries before the first use, and that use
> is supervised, with the serial console. The checklist for that review is
> `docs/device-mode-review.md`.

The install build also has the device mode. It runs only inside the
installer RAM image, and writes only the eMMC of a UC-P10:

```
p10inst plan    --device /dev/mmcblk0 --fw FW.bin [--keys F | --lib L ...] [--keeper a|b]
                [--prereqs-confirmed] [--autoupdate-unclear-confirmed] [--json OUT]
p10inst install --device /dev/mmcblk0 (the same options) [--plan PLAN.json] [--yes] [--log LOG.jsonl]
```

It refuses to start unless all of these are true:

1. The program is an install build (`-tags install`).
2. `--device` is exactly `/dev/mmcblk0`.
3. It runs in the installer RAM image: the marker file
   `/etc/p10inst/ram-image`, and a kernel command line with
   `p10inst.ram=1`, exactly one token word of the image,
   `uc-pxx.id=p10inst-XXXXXX` (6 lowercase hex digits: `image/build.sh`
   chooses the token, `kernel/mkimage.sh` adds the word), and no word of a
   booted system (any other `uc-pxx.id=`, `androidboot.`, `root=`,
   `skip_initramfs`, `init=`); the root file system in RAM, no eMMC file
   system mounted, no Android files, and root rights. Before the word checks,
   the command line must be printable ASCII (bytes 0x20 to 0x7e, and tab)
   with no `"`, apart from the newline at its end: the kernel also splits
   words at a 0xA0 byte and joins quoted words, so another byte could hide
   a word such as `root=` from the checks.

   The run prints the token in its `environment:` line. The token goes into
   the plan (`ram_image`, so also into the plan id) and into the first line
   of the run in the JSON-lines log (`"event":"environment"`). So a plan
   made in one RAM image is refused with `--plan` in another. The code does
   not compare the token with a list: the release review pins the sha of the
   image, and the procedure compares the token of the run with the token of
   that image (`token.txt`).
4. The disk is the eMMC of a UC-P10: the device-tree model and compatible, an
   MMC card (not SD) on the eMMC controller `fe330000.mmc`, 512-byte sectors,
   the size and the exact partition layout of the unit in sysfs and in both
   GPT copies, and every device node is the block device that sysfs names.
5. The plan passes every check of the dry run, and also: the keeper is
   hashed, the bootctl binary is the reviewed `uc-pxx-bootctl-inst`.
6. A person confirms: `--yes`, or the word `INSTALL` typed on stdin, after
   the plan and its id are shown.
7. The plan is computed again from scratch, and its id must be the same. With
   `--plan FILE` it must also be the id in the file.

Then the executor runs the plan, with the same steps as a dry run. The
safety layers on the device:

- **One write layer.** `internal/device/writer.go` has the only write open
  of an eMMC node. It opens one target partition node at a time
  (`O_RDWR|O_DIRECT|O_EXCL`), never the whole disk and never misc. It checks
  the range rule of the plan again before every write. After the image it
  runs fsync (that also flushes the eMMC cache) and `BLKFLSBUF`.
- **Kernel read-only flag.** The RAM image sets it on every eMMC device. The
  flag of the disk also applies to every partition (`bdev_read_only`), so a
  write clears the flag of the disk and of its own partition only, and only
  while it runs. At the end every flag is set again.
- **The A/B sector only through `uc-pxx-bootctl-inst`** (sha256
  `20aa6bc5d2146faace461eec0b50f741c681b3c58827788b9b806d9444b00203`, not reviewed yet), at
  `/usr/libexec/p10inst/uc-pxx-bootctl-inst`. Before every call p10inst reads
  the binary, checks its sha256, and runs a private copy of those bytes. It
  runs only `status`, `disable --slot T` and `arm-trying --slot T
  --lower-keeper` (and, in `p10inst repair` and `p10inst switch`, `repair
  --keeper K` and `switch --to T`, each with `--expect-sector` and a dry run
  first). It holds `/run/uc-pxx-abdata.lock` for the whole run. It
  never kills the tool, and the tool runs in its own session with its output
  in files, so a ^C, a hangup or a closed pipe cannot reach it. p10inst never
  writes the A/B sector itself.
- **Signals.** From the first A/B step to the last read-back p10inst ignores
  SIGHUP, SIGINT, SIGTERM and SIGPIPE. Every line also goes to a transcript
  file next to the JSON-lines log (`/run/p10inst/install-ID.out`).
- **Fresh reads.** The A/B sector, the verify pass and the keeper check read
  with `O_DIRECT`. The verify pass also reads each image again with
  `head -c N NODE | sha256sum` after `BLKFLSBUF`. Both must agree.
- **Sync before the arm.** fsync of every target node and of the disk, so the
  data is on the flash before the target becomes the next boot.
- **Stops.** Any failed check stops the install. Up to the arm, the target is
  disabled, so the keeper boots ("safe to power off"). When bootctl ends
  badly (exit 3, a signal), p10inst reads the sector itself: the state before
  or exactly the predicted state is a stop with that state (exit 1). Any
  other sector means the A/B state is not known: p10inst says only "do not
  power off, do not restart" and exits with status 3.
- **Paths.** `--json`, `--log`, `--fw`, `--keys`, `--lib` and `--plan` must
  be regular files outside /dev, /proc and /sys (an output may also be new).
- **The firmware on the drive** (`--fw-drive DRIVE.img [--fw-drive-expect
  ID]`, in place of `--fw`): the one `.bin` of the P10-INSTALL drive file,
  read in place (package fatfs). The drive rule: the lun of the drive is
  empty (the computer does not have the drive) and the drive file keeps one
  identity (size, modification and change time, device, inode; the
  on-screen flow passes the identity it saw) from the first read of the
  firmware to the end. The device mode checks it at the start, at each read
  of the firmware, and before every step up to the arm. A change before the
  first step refuses (nothing written); a later change stops the install
  before the next step, so the target stays disabled and is never armed.
- **After a kill.** A SIGKILL inside a write leaves the read-only flags of
  the disk and of that partition off. The next device-mode run reports this
  and sets them again; `p10inst readonly` does the same.

Tests (no real disk is ever opened):

- `go test -tags install,devtest ./internal/devmode/ ./internal/device/`
  runs the device mode on fake units: device nodes are regular files, sysfs,
  the device tree and /proc are files in a temporary root. The A/B steps run
  the real uc-pxx-bootctl-inst with `--file` on a copy of the sector. It
  covers full installs, wrong devices (SD card, another controller, a loop
  device, another board), a booted system, a wrong layout, a wrong bootctl
  sha256 (at the start and swapped during the install), a changed plan id,
  no confirmation, a bootctl that misbehaves, a power-cut sweep (a cut before
  every write, clean and torn) and a kill sweep (the process SIGKILLs itself
  after every write), each one checked and then resumed.
- `ci/devmode-loop.py` (as root, on a build host) runs the device mode on
  loop devices of a full-size image with a real firmware, with the real
  uc-pxx-bootctl-inst through chroot: the kernel behaviour above, refusals,
  a full install, SIGKILLs, and a partition in use.
- The devtest build (`-tags install,devtest`) adds `--test-root` and the
  test hooks. A release install build has none of it:
  `ci/check-no-writer.py --release-install` checks the binary.
- `ci/check-no-writer.py IMAGE.cpio` refuses every reviewed A/B writer in a
  status image (by name, sha256 and log string); `--install-image` checks an
  install image. `ci/test-check-no-writer.py` tests both.

## The on-screen install flow

> **Not released, not for device use.** It needs the install RAM image
> (not built yet), an Opus release review that names its binary and image
> shas, and a RULES.md change by the project owner.

`p10inst flow [--tty DEV | --stdin] [--leds]` (install build only) is the
screen of the install RAM image: the status (with the menu; Install goes on
only when Crestron auto-update reads as off, and means that the steps of
"Before you start" in [docs/INSTALL.md](docs/INSTALL.md) are done), the
firmware on the drive, the choice of the slot to install to when the
planner asks, the plan, the progress, and the end (Done, Stopped, Hold).
The texts are for an end user: plain words, the details in the log. The
key legend is the last row, one label above each touch key. The flow
itself writes no
byte of the eMMC:

- `internal/flow` is the state machine. It has no I/O at all (ci/goallow
  checks its imports).
- `internal/flowio` does the reads and starts the children. It does not
  import the install side. Its only writes are its own files in
  /run/p10inst, the RAM drive file while the computer does not have the
  drive, the lun of the gadget, and the LEDs (ci/goallow checks each one).
- After the hold of OK on the plan screen it starts the device mode as a
  child, `p10inst install --device /dev/mmcblk0 --fw-drive DRIVE
  --fw-drive-expect ID --plan PLAN --yes --log LOG [--keeper K]
  [--keys F] --prereqs-confirmed [--autoupdate-unclear-confirmed]`, under a
  supervisor in its own session (`p10inst flow-supervise`), so a crash of
  the UI does not stop the install, and a new UI finds it.
- One flow at a time: the UI that holds a flock(2) on
  /run/p10inst/flow/ui.lock (on the install image the screen, tty1) runs
  the flow: the children, the end of an install (readonly, Classify, the
  bundle) and the screens. It writes its screens without ever waiting for
  its terminal, so a terminal that nobody reads cannot stop it. It
  publishes each screen (/run/p10inst/flow/screen.json). Any other UI
  only shows that screen: a second screen UI waits, shows it (in red if it
  is old), and takes over when the flow UI ends; `--stdin` refuses (exit
  4); `--stdin --take-over` sends its keys to the flow UI (one terminal at
  a time, input.lock and remote.in) and shows its screen, while the panel
  shows the state and says where the keys are; q leaves. The kernel drops
  a lock when its process dies, and no child inherits it, so the UI that
  init starts again after a crash takes over at once.
- After the child ended it waits until no uc-pxx-bootctl-inst runs and
  the A/B lock is free, runs `p10inst readonly`, reads the A/B sector
  itself (O_DIRECT), and chooses the screen (package flow, `Classify`):
  exit 0 with the plan's final state is Done; exit 3 is always a hold;
  any other end is Stopped only when the sector is a state of the plan,
  else a hold. Restart is blocked while an install runs and after a hold.
- The drive stays ejected from the read of the firmware to the end, and
  the device mode checks it before every step (`--fw-drive`).
- At the end, the log bundle `p10-install-<plan id>-<UTC>/` (an allowlist
  of files, no serial number, the CID only as a hash, no key) goes into
  /run/p10inst and, on a new FAT32 volume (`internal/fatmk`), onto the
  drive, which then goes back to the computer.
- On a hold, the flow runs `p10inst repair --dry-run` with the plan of the
  install that ended on it (reads only), and offers the repair only after
  its exit 5: at most two `--yes` per hold for the whole boot, never after
  an exit 3 (kept in state.json, and found from the child folders). The
  hold ends only after exit 0 with `REPAIR-SAFE: yes` and the flow's own
  read of the sector (the kept slot successful); a read of the sector on
  the hold never says that the power can be disconnected. Switch (status
  menu) runs `p10inst switch --dry-run`; on its exit 5 a confirm shows the
  change and the sector bytes before and after (the flow's own prediction,
  equal to the tool's), and only its 3 s hold starts the write, when the
  sector is still the one of the check; "Done" needs exit 0,
  `SWITCH-SAFE: yes` and the own read; exit 1 wrote nothing; any other end
  is a hold of its own (keep the unit powered, no repair). The bundle is
  written again after each repair write with its files; a switch that ends
  on a hold gets its own bundle. Yes to "Do you see this screen on the
  unit's panel?" is one press of the panel's OK key; a terminal answers yes
  only after a warning (recorded).

## Design

The design and the open questions are in the UC-P10 working notes,
`fw-installer-design.md`. It moves into `docs/` of this repository before
the first release.
