# Device mode: checklist for the release review

The device mode (`p10inst install --device /dev/mmcblk0`) writes the eMMC of
a UC-P10. Every write it makes can strand a unit if it is wrong, and a unit
with no bootable slot cannot be recovered by software. So the device mode is
not used until an independent reviewer has checked the exact binaries and
says GO. The reviewer writes their own checks and does not reuse the
author's test code as the proof.

## 1. What is reviewed

Record the sha256 of each item in the review. A different sha256 needs a new
review.

| Item | How to get it |
|---|---|
| The source commit of this repository | `git rev-parse HEAD`, and `git status` must be clean |
| The release install binary, aarch64 | `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -tags install -trimpath -ldflags "-s -w" ./cmd/p10inst`; check that two builds give the same sha256 |
| `uc-pxx-bootctl-inst` | `5d83215bb5cab5b5f68c6db6500af8bea91180338b67e46850c266afb34ee400` (already reviewed as a separate binary; the device mode is a new caller, so this review must name it) |
| The installer RAM image that carries both | Not built yet (section 5) |
| The manifests | `manifests/manifest-*.json` (schema 2, the 37 public files; the gate: `plan.Gate`) |

## 2. Files to read

The write path, in the order it acts:

1. `cmd/p10inst/device_install.go`: the flags. `devtest_off.go` and
   `devtest.go`: the test flags, only in a devtest build.
2. `internal/devmode/devmode.go`: the order of the checks, the confirmation,
   the second plan, the run, the exit codes.
3. `internal/device/env.go`: the pinned facts (model, controller, bootctl
   path and sha256, marker), `Live`, `NewForTest`, and
   `testbuild_off.go` / `testbuild_on.go`.
4. `internal/device/checks.go`: `CheckRAMImage`, `OpenEMMC`.
5. `internal/device/disk.go`: device numbers, `O_DIRECT` reads, the view,
   the read-only flags, fsync, `BLKFLSBUF`, the lock.
6. `internal/device/writer.go`: the only write open of an eMMC node.
7. `internal/device/bootctl.go`: the three command forms, the sha256 check,
   the private copy, no kill.
8. `internal/device/medium.go`: the device as an install medium; the A/B
   pre-check and read-back, the second verify implementation, the sync.
9. `internal/install/install.go` and `medium.go`: the executor that both the
   dry run and the device mode use, and the `Cutter` of the test hooks.
10. `internal/plan/plan.go`: the device mode in the planner (`mode`,
    `device`, `bootctl-binary`, the keeper hashes).

## 3. Questions the reviewer answers

- Can any input (flags, files on the RAM drive, the unit's own data) make
  p10inst write outside the eight target partitions, or write the A/B sector
  other than through `uc-pxx-bootctl-inst` with the plan's commands?
- At every write, is at least one slot bootable, and is it the keeper? Look
  at the order: disable, data, fsync and sync, verify, arm.
- Is the data durable before the arm? The kernel assumptions are in
  section 4.
- Does every stop say the truth about the A/B state? Exit 3 must be the
  only "do not power off" case.
- Can a release install binary use a test root, regular files, a chroot,
  another bootctl sha256, other manifests or the test hooks?
- The open design questions of the install path (keeper without the
  pre-fastboot sector, a non-AVB vbmeta, the key source, rollback) and the
  ones in section 5: accept, or require a change.

## 4. Kernel assumptions

| Assumption | Where it matters | State |
|---|---|---|
| The read-only flag of the disk applies to every partition (`bdev_read_only`), and the flag of a partition only to it | the writer clears the disk flag and its own partition flag | Read in the 6.18 source (`include/linux/blkdev.h`). Tested on loop devices (`ci/devmode-loop.py --kernel`, kernel 6.12). Not tested on a unit |
| fsync of a partition node sends a cache flush to the disk (`blkdev_fsync` -> `blkdev_issue_flush`); the mmc driver flushes the eMMC cache for it (`mmc_blk_issue_flush`) | the data is durable before the arm | Read in the 6.18 source. Tested on loop devices (the flush counter of the disk goes up). The eMMC side is not tested |
| `O_EXCL` on a partition node fails while it is mounted or held | a mounted target is never written | Tested on loop devices (a second exclusive open fails with EBUSY). Mount not tested |
| uc-pxx-bootctl-inst writes with `O_DIRECT\|O_DSYNC` and fsync (FUA or flush) | the disable is durable before the first data write | From its reviewed source |
| A sector write is atomic on the eMMC | a power cut during the A/B write | Not testable here (design §9, remaining risks) |

## 5. Not built, or not decided

- The installer RAM image with the install build and
  `/usr/libexec/p10inst/uc-pxx-bootctl-inst`. RULES.md says the binary is
  never in "the initramfs"; that line was written for the uc-pxx-linux rescue
  initramfs, so the review decides whether the installer RAM image may carry
  it. `ci/check-no-writer.py IMAGE.cpio` refuses such an image as a status
  image: it finds every reviewed A/B writer by name, by sha256 and by its log
  string, and refuses anything under `usr/libexec/p10inst/`.
  `ci/check-no-writer.py --install-image IMAGE.cpio` is the check of the
  install image: exactly one boot-control tool, at that path, with the
  reviewed sha256, a release install p10inst, and no other writer.
  `ci/test-check-no-writer.py` tests both checks with small archives.
- The on-screen flow ("hold OK for 3 s" on the plan screen calls
  `install --device --yes`). Not built.
- The log bundle on the P10-INSTALL drive. The device mode writes the plan
  JSON and the JSON-lines log under `/run/p10inst/` only.
- The keeper when both slots are successful and the pre-fastboot sector is
  not known: the plan refuses until `--keeper` names one. On unit 2 today
  both slots are successful, so `--keeper` is needed.
- With `--keeper`, a user may name a successful keeper while the other slot
  is trying (for example right after an install). The install then runs
  again on the trying slot. It cannot leave zero bootable slots, but the
  review should say whether the UI may offer it.
- The eMMC controller path in sysfs (`fe330000.mmc`) and the CID file come
  from the device tree and the kernel, not from a reading on the unit. If
  they differ there, the device mode refuses (the safe side).

## 6. Evidence to run again

Run each on a build host, never on a unit. The real-firmware runs need the
firmware and the unpacked images, which are not in this repository.

1. `ci/lint.sh` with `UC_PXX_BOOTCTL` set to the reviewed binary (and
   `UC_PXX_BOOTCTL_EXEC=qemu-aarch64-static` on a PC). This runs every Go
   test, including the dry-run sweep and the device-mode power-cut and kill
   sweeps on fake units, and the binary checks of all three builds.
2. `ci/devmode-loop.py --kernel --refusals --kills --busy` for `--shape fwd`
   and `--shape rev`, as root, with a devtest build. It runs the device mode
   on loop devices of a full-size image with the 1.0.0.94 userdebug firmware
   and the real uc-pxx-bootctl-inst, and checks each result with
   `ci/dryrun-check.py`, which shares no code with p10inst.
3. `ci/check-no-writer.py --release-install` on the exact release binary.
4. The reviewer's own checks: at least an own decoder of the A/B sector over
   the logs of step 2, and an own reading of the write path in section 2.

## 7. Fixes after the first review (2026-10-26)

The first review (GO for one supervised install, NO-GO for end users) found
these. Each fix has a test that fails on the reviewed commit:

| Finding | Fix | Test |
|---|---|---|
| F1: the status-image check passed a cpio with uc-pxx-bootctl-inst | name, sha256 and format-string checks (the whole log and status formats: a short prefix appears in the release p10inst by string packing), no file under `usr/libexec/p10inst/`, `--install-image` | `ci/test-check-no-writer.py` (17 cases), and the review's own `cpt/` archives |
| F2: ^C, a hangup or SIGPIPE reached bootctl | bootctl in its own session, output to files; p10inst ignores SIGHUP, SIGINT, SIGTERM and SIGPIPE during the run; a transcript file keeps every line | `TestExecRunnerSession`, `TestDeviceSignals`, `ci/devmode-loop.py --signals` (a pty) |
| F3: a kill inside a write leaves read-only flags off | the next device-mode run reports and sets them (`p10inst readonly` does the same) | `ci/devmode-loop.py --kills` (the resume must report the exact count) |
| F4: `--json`, `--log`, `--lib`, `--keys` could name a device node | refused under /dev, /proc, /sys, and when not a regular file; outputs open with `O_NOFOLLOW` | `TestDevicePaths` |
| F5 (info): a mount under another name, a loop device on the eMMC | refused by device number (or same file) | `TestDeviceInUse` |
| F6 (info): a failed second A/B read-back was a plain stop | exit 3 | `TestReadBackFailureIsUnknown` |
| F8: a signal-ended bootctl gave "do not power off" and "safe to power off" | p10inst classifies by its own read-back: a known state is exit 1 with that state and the right write count; only an unknown state is exit 3, with the hold text only | `TestDeviceBootctlMisbehaves` (10 modes) |

F7 (info) stays: the status image meets the RAM-image gate, so a person can
copy both binaries into it; it is write-free only by its content (F1).

## 8. After a GO

- RULES.md gets a rule-9 entry with the shas of section 1 (the user adds it).
- The first use is one supervised install on unit 2's target slot, with the
  serial console, the A/B sector and the BCB read before and after, and the
  plan JSON kept.
