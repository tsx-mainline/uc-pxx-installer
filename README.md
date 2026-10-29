# uc-pxx-installer

A RAM-boot installer for the official firmware of Crestron UC-P10 room
phones. It installs one official firmware (usually the userdebug build) onto
one A/B slot, and then makes that slot the next boot. The other slot stays
untouched as the fallback.

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
| Manifests (`manifests/`, `internal/manifest`) | Done for 1.0.0.94 userdebug and 1.0.6.20 stock |
| A/B decoder, read-only (`internal/avbab`) | Done. Tested with captured sectors |
| `p10inst verify` (check a whole firmware file before any write) | Done. About 26 s per firmware on a PC |
| `uc-pxx-bootctl disable` and `arm-trying --slot a\|b` | In uc-pxx-linux, branch `bootctl-installer`. Reviewed as the separate binary `uc-pxx-bootctl-inst` (sha256 `5d83215b…`) |
| Status screen, read-only (`p10inst ui`, `p10inst status`) | Written. Unit tests with a fake device. See "The RAM image" |
| Android auto-update check, read-only (`internal/nvram`) | Written. The rule of the stock nvram driver (1.0.0.94 and 1.0.6.20), checked on unit 2. Unit tests with made-up stores. See "Android auto-update" |
| RAM image (`image/`), USB gadget (serial, network, drive) | Written. See "The RAM image" for the state on the unit |
| Install plan (`p10inst plan`, `internal/plan`) | Written. Every write, every A/B transition and every safety check, for humans and as JSON. Read-only |
| Install, dry run only (`p10inst install --dry-run`, `internal/install`) | Written. Writes go to an image file that stands for the eMMC, never to a device. Power-cut sweep passes |
| Install on a device (`p10inst install --device /dev/mmcblk0`, `internal/device`, `internal/devmode`) | Written, in the install build only. Tested on fake units and on loop devices, never on a unit. Needs its release review before any use. See "The device mode" |
| Entry page (WebUSB) and CLI | Open |

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
| `internal/plan` | The install plan: the ordered writes and all safety checks. Read-only |
| `internal/install`, `internal/bootctl`, `internal/fakeemmc` | The install side: the executor (one for both media), the dry-run medium, the uc-pxx-bootctl runner for sector copies, test images. Only in the install build |
| `internal/device`, `internal/devmode` | The device mode: the checks of the unit, the only eMMC write layer (`writer.go`), uc-pxx-bootctl-inst, the flow. Only in the install build |
| `internal/device/devicetest` | Fake units for the device-mode tests. Only in the devtest build |
| `tools/` | Python reference tools: `pcdfw-unpack.py`, `pcdfw-findkey.py`, `fw-manifest.py` |
| `ci/` | `lint.sh`, `check-no-key.py`, `check-no-writer.py`, `dryrun-check.py`, `dryrun-real.py`, `devmode-loop.py` |

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
```

None of these commands write to a device. The install build adds the dry
run on image files (see "The install build") and the device mode (see "The
device mode"). `status`, `readonly`, `ui` and
`usbd` run on the device, in the RAM image. `readonly` sets the kernel
read-only flag of the eMMC and its partitions. That changes no byte on the
eMMC. `findkey` never prints the key.
Exit status: 0 OK, 1 a check failed, 2 usage or I/O error.

## The RAM image

`image/build.sh` builds a boot image for `fastboot boot`. It runs on a Linux
build host with docker and the aarch64 cross compiler:

```
image/build.sh --kernel LINUX-UC-PXX --linux UC-PXX-LINUX
```

The kernel is the uc-pxx kernel at the revision that uc-pxx-linux pins. The
build uses `kernel/mkimage.sh` of uc-pxx-linux unchanged.

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
   `/etc/p10inst/ram-image`, `p10inst.ram=1` on the kernel command line, no
   word of a booted system there (`uc-pxx.id=`, `androidboot.`, `root=`;
   the one exception is the image's own token `uc-pxx.id=p10inst-XXXXXX`,
   6 lowercase hex digits, which `kernel/mkimage.sh` adds), the
   root file system in RAM, no eMMC file system mounted, no Android files,
   and root rights.
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
  `5d83215bb5cab5b5f68c6db6500af8bea91180338b67e46850c266afb34ee400`), at
  `/usr/libexec/p10inst/uc-pxx-bootctl-inst`. Before every call p10inst reads
  the binary, checks its sha256, and runs a private copy of those bytes. It
  runs only `status`, `disable --slot T` and `arm-trying --slot T
  --lower-keeper`. It holds `/run/uc-pxx-abdata.lock` for the whole run. It
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

## Design

The design and the open questions are in the UC-P10 working notes,
`fw-installer-design.md`. It moves into `docs/` of this repository before
the first release.
