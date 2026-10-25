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
| `uc-pxx-bootctl disable` and `arm-trying --slot a\|b` | Written in uc-pxx-linux, branch `bootctl-installer`. Needs its review |
| Status screen, read-only (`p10inst ui`, `p10inst status`) | Written. Unit tests with a fake device. See "The RAM image" |
| RAM image (`image/`), USB gadget (serial, network, drive) | Written. See "The RAM image" for the state on the unit |
| Install plan (`p10inst plan`, `internal/plan`) | Written. Every write, every A/B transition and every safety check, for humans and as JSON. Read-only |
| Install, dry run only (`p10inst install --dry-run`, `internal/install`) | Written. Writes go to an image file that stands for the eMMC, never to a device. Power-cut sweep passes |
| Install on a device | Not built. See "The install build" |
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
| `internal/status` | Collects and renders the status: slots, A/B state, unlock state, identity |
| `internal/ui`, `internal/gadget`, `internal/devmem` | The screen and the touch keys, the USB gadget, read-only SoC registers |
| `image/` | The RAM image: build script, initramfs, kernel config fragment |
| `internal/manifest`, `manifests/` | The firmware manifests, embedded in the binary |
| `internal/gpt`, `internal/vbmeta` | Read-only readers: the GPT, the vbmeta header and its AVB key |
| `internal/plan` | The install plan: the ordered writes and all safety checks. Read-only |
| `internal/install`, `internal/bootctl`, `internal/fakeemmc` | The install side: the dry-run executor, the uc-pxx-bootctl runner, test images. Only in the install build |
| `tools/` | Python reference tools: `pcdfw-unpack.py`, `pcdfw-findkey.py`, `fw-manifest.py` |
| `ci/` | `lint.sh`, `check-no-key.py`, `check-no-writer.py`, `dryrun-check.py`, `dryrun-real.py` |

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

None of these commands write to a device. The install build adds three
commands that work on image files only (see "The install build"). `status`, `readonly`, `ui` and
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
  unlock state as far as it can be proven, the model, the serial number and
  the MAC address. The touch keys drive it: Vol+ and Vol- choose, the
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
                  [--keeper a|b] [--pre-ab HEX] [--prereqs-confirmed] [--hash-keeper] [--json OUT]
p10inst install   --dry-run --image FILE (the same options) [--plan PLAN.json] [--log LOG.jsonl]
p10inst fake-emmc --layout FILE -o IMAGE --ab a=P,T,S,b=P,T,S [--fill NAME=FILE ...] [--markers]
```

`FILE` is an image file that stands for the eMMC: a regular file with the
GPT of the unit. `install` refuses to run without `--dry-run`, and refuses a
device node, a symlink and any path under /dev, /proc or /sys. There is no
code path to a block device. Without `--keys` or `--lib`, the key comes from
the updater libraries in system_a or system_b of the image, as on the device.

The plan has these steps, in this order:

1. `uc-pxx-bootctl disable --slot T`: the target cannot boot while it is written.
2. Write each partition image of the target. Trust is skipped when it already holds the image.
3. Sync.
4. Re-read every written range and compare its sha256.
5. `uc-pxx-bootctl arm-trying --slot T --lower-keeper`.
6. Read back the A/B sector. Check it with the installer's own decoder and with `uc-pxx-bootctl status`.

A power loss at any point boots the keeper. The plan refuses when it is not sure.

uc-pxx-bootctl `--file` works on a 512-byte sector file. So the dry run
copies the A/B sector of the image to a private file, runs the tool on it,
compares the result with the plan's own prediction, and only then puts it
into the image (guarded on the image still holding the old sector).

The status-only RAM image has none of this. `ci/check-no-writer.py`
checks that only files with `//go:build install` import the install
packages, that the import graph of the default build does not hold them, and
that a built p10inst binary does not hold their symbols.

Tests. `ci/lint.sh` runs everything that needs no vendor file. The install
tests and the power-cut sweep need the real uc-pxx-bootctl:
`UC_PXX_BOOTCTL=PATH` (and `UC_PXX_BOOTCTL_EXEC=qemu-aarch64-static` for the
aarch64 binary on a PC). The sweep cuts every install before each write,
as a power loss would, and checks the A/B sector with package avbab after
each cut. `ci/dryrun-real.py` runs the same with a real firmware on a build
host. `ci/dryrun-check.py` is its independent checker.

## Design

The design and the open questions are in the UC-P10 working notes,
`fw-installer-design.md`. It moves into `docs/` of this repository before
the first release.
