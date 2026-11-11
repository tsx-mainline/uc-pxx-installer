#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-2.0-or-later
"""Check that the installer RAM image has no eMMC writer.

    ci/check-no-writer.py              check the sources (image/overlay, image/overlay-install,
                                       the image scripts, the Go packages)
    ci/check-no-writer.py IMAGE.cpio   check a built status initramfs (uncompressed newc cpio)
    ci/check-no-writer.py --install-image IMAGE.cpio
                                       check an install initramfs: the one allowed A/B
                                       writer (mode 0700), a release install uc-pxx-inst,
                                       nothing else
    ci/check-no-writer.py --binary F   check one uc-pxx-inst binary for the install packages
    ci/check-no-writer.py --release-install F
                                       check an install build of uc-pxx-inst: it has the
                                       device mode, but no devtest code and no test-only
                                       package (internal/mainline/mltest)
    ci/check-no-writer.py --mainline-install F
                                       the same, and it has the mainline install
                                       (internal/mainline, internal/mainline/mldrive)
    ci/check-no-writer.py --devtest F  check a devtest build: it holds the devtest marker
    ci/check-no-writer.py --step2 F    check a release build of uc-pxx-step2 (go build -tags
                                       install,step2 ./cmd/uc-pxx-step2): the engine and the slot
                                       side of the device layer; no device mode (devmode), no
                                       firmware decrypter (pcdfw), no RAM flow, no drive reader,
                                       no devtest code, no test gate
    ci/check-no-writer.py --xfer F     check a build of uc-pxx-xfer (go build -tags xfer
                                       ./cmd/uc-pxx-xfer): the step-2 USB transport holds the
                                       receiver and the gadget, and no eMMC code at all: no
                                       device layer, install engine, A/B tool, step-2 engine,
                                       plan, partition or block reader, no /dev/mmcblk string

The status image must not write to the eMMC in any way. This check fails when
  - the image holds a tool that writes boot data or disks: uc-pxx-bootctl,
    uc-pxx-bootctl-inst and the other boot-control tools, e2fsprogs,
    partition tools, the busybox applets that write disks or flash. A tool is
    found by its name, by its sha256 (every reviewed A/B writer binary), and
    in any ELF file by the log prefix of the boot-control tools; and nothing
    may be under usr/libexec/uc-pxx-inst/ (the place of the install image's tool);
  - a script writes to an eMMC device (dd of=/dev/mmcblk..., > /dev/mmcblk...,
    RPMB), makes it writable again (blockdev --setrw), or mounts an eMMC file
    system other than with ro,noload;
  - a Go package that reads the eMMC (blk, status, ext4, bootimg,
    vendorstore, avbab, devmem, gpt, vbmeta, nvram, plan, rebootmode) or the UC-PXX-INST
    drive (fatfs, fatfs/fattest, drivestate, mainline, mainline/mldrive) opens a file for writing;
  - the install side leaks into the default build. The install packages
    (internal/install, internal/bootctl, internal/fakeemmc,
    internal/pcdfw/pcdfwtest, internal/nvram/nvramtest, internal/device,
    internal/devmode) may only be
    imported by Go files whose build constraint needs "install". When the go
    tool is there, the import graph of the default ./cmd/uc-pxx-inst must not hold
    them, and the graph with -tags install must hold internal/install and
    internal/device (so the check sees something). In a built initramfs, the
    uc-pxx-inst binary must not hold the symbols of those packages (Go keeps
    package paths in its function table, also in a stripped binary);
  - a test-only package (internal/mainline/mltest) is imported by a file that
    is not a _test.go file, or a build of cmd/uc-pxx-inst holds it;
  - the devtest code leaks into an install build: internal/device/devicetest
    and the test flags may only be in files whose constraint needs "devtest",
    the -tags install graph must not hold devicetest, and a release install
    binary must not hold the devtest marker string;
  - the restart into the download mode (internal/rebootmode) is in the
    status build only: the -tags install graph and a release install binary
    must not hold it (the install flow's holds forbid a restart; DLMODE.md);
  - a file of internal/device other than writer.go opens a file for writing,
    except the A/B lock file (disk.go) and the private copy of the bootctl
    binary (bootctl.go). writer.go is the only write open of an eMMC node;
  - the step-2 code (internal/step2, cmd/uc-pxx-step2, internal/device/slot.go)
    is only in files whose constraint needs both "install" and "step2", so no
    build of cmd/uc-pxx-inst compiles it; the graphs of cmd/uc-pxx-inst must not hold
    internal/step2, and the graph of cmd/uc-pxx-step2 must not hold the device
    mode, the flow, the drive reader or the download-mode restart;
  - the step-2 USB transport (cmd/uc-pxx-xfer, internal/xfer): the files of cmd/uc-pxx-xfer
    need //go:build xfer and the xfer tag is nowhere else; only cmd/uc-pxx-xfer imports
    internal/xfer; the sources of both never name an eMMC node, O_DIRECT or a block ioctl;
    with the go tool, no build of cmd/uc-pxx-inst or cmd/uc-pxx-step2 holds internal/xfer,
    and the graph of cmd/uc-pxx-xfer holds internal/xfer and none of the eMMC packages;
  - with the go tool: ci/goallow (go/types) finds a write path in a reader
    package (a raw system call, an ioctl, a mount, a chmod, a write method
    or a file given to a writer interface, a write open flag), an import of
    the pure flow package outside its list, or a write of the flow runner
    (internal/flowio) outside the functions named for it, or its import of
    the install side (FAT-READER-REVIEW.md F5). The flow packages are
    install-only: the status build must not hold them.
Exit status: 0 clean, 1 a writer found, 2 usage or I/O error.
"""
import hashlib
import os
import re
import shutil
import stat
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(HERE)

# Tools that must not be in the image (base names).
FORBIDDEN_NAMES = {
    "uc-pxx-bootctl", "uc-pxx-flash-boot", "uc-pxx-arm-slot", "uc-pxx-mark-boot.sh", "uc-pxx-mark-boot",
    "uc-pxx-boot-ok", "uc-pxx-install-rootfs", "uc-pxx-force-rescue", "p10-bootctl", "p10-flash-boot-a",
    "p10-mark-boot.sh", "rkdeveloptool", "fastboot", "bootctl",
    "mke2fs", "mkfs.ext2", "mkfs.ext3", "mkfs.ext4", "e2fsck", "fsck.ext4", "resize2fs", "tune2fs", "debugfs",
    "e2image", "fdisk", "sfdisk", "gdisk", "sgdisk", "parted", "partprobe", "blkdiscard", "hdparm",
    "mmc", "devmem", "flashcp", "flash_eraseall", "nandwrite", "mkswap", "losetup", "fstrim",
    "tsx-data-restore", "uc-pxx-bootctl-inst",
}

# The sha256 of every reviewed binary that writes the A/B sector (RULES.md rule 9):
# uc-pxx-bootctl-inst, uc-pxx-bootctl, p10-bootctl. A renamed copy is still found.
WRITER_SHAS = {
    "5d83215bb5cab5b5f68c6db6500af8bea91180338b67e46850c266afb34ee400": "uc-pxx-bootctl-inst",
    "abbffc392615b1db708884262d7e18d5a5e480716e1d2fec4f26b32dd137f1c3": "uc-pxx-bootctl-inst (repair, switch; superseded)",
    "391ee645b4e183d2f072fc4ece068800444d4f1724af413627bfbfcb222d299d": "uc-pxx-bootctl-inst (repair, switch; superseded)",
    "20aa6bc5d2146faace461eec0b50f741c681b3c58827788b9b806d9444b00203": "uc-pxx-bootctl-inst (repair, switch)",
    "38fea6d32412e7977ebc5845b138dc1c8c2df8555dae0b64b47f8a851b45e2d7": "uc-pxx-bootctl",
    "d979ee70fd15ff3892c3f59b4e2f9dbc02b13b9b176a6881f9d3f4eba630e4be": "p10-bootctl",
}
# The format strings of the boot-control tools (their log line and their status line), with
# the newline. A Go binary packs its strings without separators, so a short prefix such as
# "uc-pxx-bootctl:" can appear in uc-pxx-inst by chance; these whole format strings do not.
WRITER_ELF_STRINGS = [b"uc-pxx-bootctl: %s\n", b"p10-bootctl: %s\n", b"libavb-would-boot: %c\n"]
INSTALL_TOOL_DIR = "usr/libexec/uc-pxx-inst/"
INSTALL_TOOL = INSTALL_TOOL_DIR + "uc-pxx-bootctl-inst"
INSTALL_TOOL_SHA = "20aa6bc5d2146faace461eec0b50f741c681b3c58827788b9b806d9444b00203"
INSTALL_TOOL_MODE = 0o700  # owner root (the cpio is 0:0), read and run by root only
# The one script line that may name the tool: image/mkinitramfs.sh puts it at the RULES.md
# path in the install image (image/build.sh --install). The built cpio is checked anyway.
INSTALL_TOOL_LINE = ("image/mkinitramfs.sh", "TOOL=/" + INSTALL_TOOL)

# Script lines that write the eMMC or undo the read-only flag.
BAD_LINES = [
    (re.compile(r"\bof=/dev/mmcblk"), "dd to an eMMC device"),
    (re.compile(r">>?\s*/dev/mmcblk"), "redirect to an eMMC device"),
    (re.compile(r"mmcblk\d*rpmb"), "RPMB access"),
    (re.compile(r"blockdev\s+--setrw"), "clears the read-only flag"),
    (re.compile(r"/sys/block/mmcblk[^ ]*/force_ro"), "boot partition force_ro"),
    (re.compile(r"\b(uc-pxx-bootctl|uc-pxx-flash-boot|uc-pxx-arm-slot|uc-pxx-mark-boot)\b"), "boot-control tool"),
]
MOUNT = re.compile(r"\bmount\b[^\n]*(/dev/mmcblk|LABEL=|UUID=|PARTLABEL=)")
MOUNT_OK = re.compile(r"-o\s*\S*\bro\b\S*\bnoload\b|-o\s*\S*\bnoload\b\S*\bro\b")

# Go: the packages that read the eMMC must only read.
GO_READERS = ["internal/blk", "internal/status", "internal/ext4", "internal/bootimg",
              "internal/vendorstore", "internal/avbab", "internal/devmem", "internal/rebootmode",
              "internal/gpt", "internal/vbmeta", "internal/avb", "internal/nvram", "internal/plan", "internal/factory",
              "internal/fatfs", "internal/fatfs/fattest", "internal/drivestate", "internal/mainline",
              "internal/mainline/mldrive"]

# Go: the install side. Only files with the install build constraint may
# import these, and the default binary must not contain them.
MODULE = "uc-pxx-installer"
INSTALL_ONLY = ["internal/install", "internal/bootctl", "internal/fakeemmc", "internal/pcdfw/pcdfwtest",
                "internal/nvram/nvramtest", "internal/device", "internal/devmode", "internal/flow", "internal/flowio",
                "internal/step2"]
# The step-2 code: only in files whose constraint needs install and step2.
STEP2_TAG = re.compile(r"^//go:build\s+install\s*&&\s*step2(\s*&&\s*!?[\w.]+)*\s*$", re.M)
STEP2_FILES = ["internal/step2", "cmd/uc-pxx-step2", "internal/device/slot.go"]
# What a build of uc-pxx-step2 must not hold (the RAM side of the installer).
STEP2_FORBIDDEN = ["internal/devmode", "internal/flow", "internal/flowio", "internal/fatfs", "internal/mainline/mldrive",
                   "internal/rebootmode", "internal/fakeemmc", "internal/device/devicetest"]
# pcdfw in uc-pxx-step2: only the type and its init (plan.Payload names a segment); never the
# decrypter, the key search or a key file.
STEP2_PCDFW_BAD = [b"internal/pcdfw.Open", b"internal/pcdfw.(*File)", b"internal/pcdfw.FindKeys",
                   b"internal/pcdfw.LoadKeyFile", b"internal/pcdfw.WriteKeyFile", b"internal/pcdfw.decHead",
                   b"internal/pcdfw.decBody", b"internal/pcdfw.candidates"]
DEVTEST_ONLY = ["internal/device/devicetest"]
# Test support that only test files (*_test.go) may import: no build of
# cmd/uc-pxx-inst may hold it (the made-up mainline payloads of the tests).
TEST_ONLY = ["internal/mainline/mltest"]
# A constraint that needs "install": install, or install && other terms.
BUILD_TAG = re.compile(r"^//go:build\s+install(\s*&&\s*!?[\w.]+)*\s*$", re.M)
DEVTEST_TAG = re.compile(r"^//go:build\s+install\s*&&\s*devtest\s*$", re.M)
DEVTEST_MARKER = b"uc-pxx-inst devtest build, test roots allowed"
# The restart into the download mode: the status build only.
STATUS_ONLY = ["internal/rebootmode"]
# internal/device: the only files that may open a file for writing, and what for.
DEVICE_WRITES = {
    "writer.go": "the write open of a target partition node",
    "disk.go": "the A/B lock file",
    "bootctl.go": "the private copy of uc-pxx-bootctl-inst and its two new output files",
}
GO_BAD = [
    (re.compile(r"\bO_(WRONLY|RDWR|APPEND|CREATE|TRUNC)\b"), "opens for writing"),
    (re.compile(r"\bos\.(Create|WriteFile|Truncate|Remove|Rename|Mkdir|MkdirAll)\b"), "writes files"),
    (re.compile(r"\.(WriteAt|Truncate)\("), "writes into a file"),
]


# The step-2 USB transport (STEP2-USB-TRANSPORT-2026-11-11.md §5): no eMMC code may link into it.
XFER_TAG = re.compile(r"^//go:build\s+xfer\s*$", re.M)
XFER_CMD = "cmd/uc-pxx-xfer"
XFER_PKG = "internal/xfer"
XFER_NEED = ["internal/xfer", "internal/gadget"]
XFER_FORBIDDEN = ["internal/device", "internal/device/devicetest", "internal/install", "internal/bootctl",
                  "internal/avbab", "internal/devmode", "internal/flow", "internal/flowio", "internal/step2",
                  "internal/plan", "internal/fakeemmc", "internal/blk", "internal/gpt", "internal/vbmeta",
                  "internal/nvram", "internal/keykeep", "internal/pcdfw", "internal/status", "internal/vendorstore",
                  "internal/fatfs", "internal/fatmk", "internal/drivestate", "internal/rebootmode",
                  "internal/mainline/mldrive", "internal/mainline/step2set", "internal/mainline/mltest", "internal/mlstash",
                  "internal/factory", "internal/ui", "internal/step2ui"]
XFER_BAD_SRC = [
    (re.compile(r"/dev/(mmcblk|block|sd[a-z]|nvme|mtd)"), "names a block device node"),
    (re.compile(r"\bO_DIRECT\b"), "O_DIRECT"),
    (re.compile(r"\bBLK[A-Z0-9_]+\b"), "a block ioctl"),
    (re.compile(r"\bSYS_IOCTL\b|\bIoctl"), "an ioctl"),
]
XFER_BAD_BYTES = [b"/dev/mmcblk", b"/dev/block/"]


def check_xfer_split(errors):
    """cmd/uc-pxx-xfer and internal/xfer: the build tag, the importers, the sources, the graphs."""
    count = 0
    for top in ("cmd", "internal"):
        for root, _, files in os.walk(os.path.join(REPO, top)):
            rel_dir = os.path.relpath(root, REPO)
            for f in files:
                if not f.endswith(".go"):
                    continue
                rel = os.path.join(rel_dir, f)
                with open(os.path.join(root, f), encoding="utf-8") as fh:
                    text = fh.read()
                count += 1
                in_cmd = rel_dir == XFER_CMD
                in_pkg = rel_dir == XFER_PKG
                tagline = re.search(r"^//go:build[^\n]*", text, re.M)
                if in_cmd and not f.endswith("_test.go") and not XFER_TAG.search(text):
                    errors.append(f"{rel}: uc-pxx-xfer code without //go:build xfer")
                if tagline and re.search(r"\bxfer\b", tagline.group(0)) and not in_cmd:
                    errors.append(f"{rel}: the xfer build tag outside cmd/uc-pxx-xfer")
                if MODULE + "/" + XFER_PKG in imports_of(text) and not (in_cmd or in_pkg):
                    errors.append(f"{rel}: imports {XFER_PKG} (only cmd/uc-pxx-xfer may)")
                if (in_cmd or in_pkg) and not f.endswith("_test.go"):
                    for n, line in enumerate(text.splitlines(), 1):
                        if line.strip().startswith("//"):
                            continue
                        for rx, why in XFER_BAD_SRC:
                            if rx.search(line):
                                errors.append(f"{rel}:{n}: {why}: {line.strip()}")
    go = shutil.which("go")
    if not go:
        print("check-no-writer: no go tool, the uc-pxx-xfer graphs not checked", file=sys.stderr)
        return count
    def deps(pkg, *tags):
        r = subprocess.run([go, "list", *tags, "-deps", pkg], cwd=REPO, capture_output=True, text=True)
        if r.returncode != 0:
            errors.append(f"go list {' '.join(tags)} {pkg}: {r.stderr.strip()}")
            return None
        return set(r.stdout.split())
    xp = MODULE + "/" + XFER_PKG
    for pkg, tags in (("./cmd/uc-pxx-inst", ()), ("./cmd/uc-pxx-inst", ("-tags", "install")),
                      ("./cmd/uc-pxx-inst", ("-tags", "install,devtest")), ("./cmd/uc-pxx-inst", ("-tags", "install,xfer")),
                      ("./cmd/uc-pxx-step2", ("-tags", "install,step2")), ("./cmd/uc-pxx-step2", ("-tags", "install,step2,xfer"))):
        g = deps(pkg, *tags)
        if g is not None and xp in g:
            errors.append(f"the build {' '.join(tags)} of {pkg[2:]} holds {XFER_PKG}")
    g = deps("./" + XFER_CMD, "-tags", "xfer")
    if g is not None:
        for p in XFER_NEED:
            if MODULE + "/" + p not in g:
                errors.append(f"the build of {XFER_CMD} does not hold {p}: the check sees nothing")
        for p in XFER_FORBIDDEN:
            if MODULE + "/" + p in g:
                errors.append(f"the build of {XFER_CMD} depends on {p}")
    return count


def check_xfer(name, data, errors):
    """A build of uc-pxx-xfer: the receiver and the gadget are in it; no eMMC code, no A/B tool."""
    for p in XFER_NEED:
        if (MODULE + "/" + p + ".").encode() not in data:
            errors.append(f"{name}: does not hold {MODULE}/{p}: not a build of uc-pxx-xfer?")
    for p in XFER_FORBIDDEN:
        if (MODULE + "/" + p + ".").encode() in data:
            errors.append(f"{name}: holds {MODULE}/{p}")
    for b in XFER_BAD_BYTES:
        if b in data:
            errors.append(f"{name}: holds the string {b.decode()!r}")
    sha = hashlib.sha256(data).hexdigest()
    if sha in WRITER_SHAS:
        errors.append(f"{name}: is the A/B writer {WRITER_SHAS[sha]}")
    for w in WRITER_ELF_STRINGS:
        if w in data:
            errors.append(f"{name}: holds the boot-control string {w.decode()!r}")
    if DEVTEST_MARKER in data or any(s in data for s in DEVTEST_STRINGS):
        errors.append(f"{name}: holds devtest code (the marker or a test flag)")
    if TEST_GATE_SYM in data:
        errors.append(f"{name}: holds mainline.TestGate")


def check_text(name, text, errors):
    for n, line in enumerate(text.splitlines(), 1):
        s = line.strip()
        if s.startswith("#") or (name, s) == INSTALL_TOOL_LINE:
            continue
        for rx, why in BAD_LINES:
            if rx.search(s):
                errors.append(f"{name}:{n}: {why}: {s}")
        if MOUNT.search(s) and not MOUNT_OK.search(s):
            errors.append(f"{name}:{n}: mounts an eMMC file system without -o ro,noload: {s}")


def is_text(data):
    return b"\0" not in data[:8192]


def check_name(name, errors):
    base = os.path.basename(name.rstrip("/"))
    if base in FORBIDDEN_NAMES:
        errors.append(f"{name}: forbidden tool in the image")


def check_sources(errors):
    count = 0
    walk = [w for o in ("overlay", "overlay-install") for w in os.walk(os.path.join(REPO, "image", o))]
    for root, _, files in walk:
        for f in files:
            p = os.path.join(root, f)
            rel = os.path.relpath(p, REPO)
            check_name(rel, errors)
            with open(p, "rb") as fh:
                data = fh.read()
            if is_text(data):
                check_text(rel, data.decode("utf-8", "replace"), errors)
                count += 1
    for script in ("image/mkinitramfs.sh", "image/build.sh", "image/kbuild.sh", "image/apk-cache.sh"):
        p = os.path.join(REPO, script)
        if os.path.exists(p):
            with open(p, encoding="utf-8") as fh:
                check_text(script, fh.read(), errors)
            count += 1
    for pkg in GO_READERS:
        d = os.path.join(REPO, pkg)
        for f in sorted(os.listdir(d)):
            if not f.endswith(".go") or f.endswith("_test.go"):
                continue
            rel = os.path.join(pkg, f)
            with open(os.path.join(d, f), encoding="utf-8") as fh:
                for n, line in enumerate(fh, 1):
                    s = line.strip()
                    if s.startswith("//"):
                        continue
                    for rx, why in GO_BAD:
                        if rx.search(s):
                            errors.append(f"{rel}:{n}: {why}: {s}")
            count += 1
    count += check_install_split(errors)
    count += check_device_writes(errors)
    count += check_xfer_split(errors)
    return count


def check_device_writes(errors):
    """In internal/device, only writer.go opens an eMMC node for writing."""
    d = os.path.join(REPO, "internal", "device")
    count = 0
    write_opens = 0
    for f in sorted(os.listdir(d)):
        if not f.endswith(".go") or f.endswith("_test.go"):
            continue
        count += 1
        with open(os.path.join(d, f), encoding="utf-8") as fh:
            for n, line in enumerate(fh, 1):
                s = line.strip()
                if s.startswith("//"):
                    continue
                for rx, why in GO_BAD:
                    if rx.search(s) and f not in DEVICE_WRITES:
                        errors.append(f"internal/device/{f}:{n}: {why}: {s}")
                if f == "writer.go" and re.search(r"\bO_RDWR\b", s):
                    write_opens += 1
                if f in ("disk.go",) and re.search(r"\bO_(RDWR|WRONLY)\b", s) and "O_CREATE" not in s:
                    errors.append(f"internal/device/{f}:{n}: a write open that is not the lock file: {s}")
                if f == "bootctl.go" and (re.search(r"\bO_RDWR\b|\.WriteAt\(", s) or
                                          (re.search(r"\bO_WRONLY\b", s) and not ("O_CREATE" in s and "O_EXCL" in s))):
                    errors.append(f"internal/device/{f}:{n}: bootctl.go writes only new files in its private dir: {s}")
    if write_opens != 1:
        errors.append(f"internal/device/writer.go: {write_opens} O_RDWR opens, want exactly one")
    return count


def imports_of(text):
    """Import paths of a Go file (single and grouped imports)."""
    out = re.findall(r'^\s*import\s+(?:\w+\s+)?"([^"]+)"', text, re.M)
    for block in re.findall(r"^\s*import\s*\((.*?)\)", text, re.M | re.S):
        out += re.findall(r'"([^"]+)"', block)
    return out


def check_install_split(errors):
    """The install packages are reachable only from install-tagged files."""
    count = 0
    banned = {MODULE + "/" + p for p in INSTALL_ONLY}
    devtest = {MODULE + "/" + p for p in DEVTEST_ONLY}
    testonly = {MODULE + "/" + p for p in TEST_ONLY}
    for top in ("cmd", "internal"):
        for root, _, files in os.walk(os.path.join(REPO, top)):
            rel_dir = os.path.relpath(root, REPO)
            inside = any(rel_dir == p or rel_dir.startswith(p + os.sep) for p in INSTALL_ONLY)
            in_devtest = any(rel_dir == p or rel_dir.startswith(p + os.sep) for p in DEVTEST_ONLY)
            for f in files:
                if not f.endswith(".go") or f.endswith("_test.go"):
                    continue
                rel = os.path.join(rel_dir, f)
                with open(os.path.join(root, f), encoding="utf-8") as fh:
                    text = fh.read()
                count += 1
                if in_devtest and not DEVTEST_TAG.search(text):
                    errors.append(f"{rel}: a devtest package file without //go:build install && devtest")
                if rel_dir.startswith(os.path.join("internal", "device")) or rel_dir.startswith(os.path.join("internal", "devmode")):
                    if not BUILD_TAG.search(text):
                        errors.append(f"{rel}: a device-mode file without //go:build install")
                in_step2 = any(rel == p or rel_dir == p or rel_dir.startswith(p + os.sep) for p in STEP2_FILES)
                if in_step2 and not STEP2_TAG.search(text):
                    errors.append(f"{rel}: step-2 code without //go:build install && step2")
                tagline = re.search(r"^//go:build[^\n]*", text, re.M)
                if tagline and re.search(r"\bstep2\b", tagline.group(0)) and not in_step2:
                    errors.append(f"{rel}: the step2 build tag outside the step-2 files")
                for imp in imports_of(text):
                    if imp in devtest and not in_devtest and not DEVTEST_TAG.search(text):
                        errors.append(f"{rel}: imports the devtest package {imp} without //go:build install && devtest")
                if "--test-root" in text and not DEVTEST_TAG.search(text) and not in_devtest:
                    errors.append(f"{rel}: has the devtest flag --test-root without //go:build install && devtest")
                in_testonly = any(rel_dir == p or rel_dir.startswith(p + os.sep) for p in TEST_ONLY)
                # the test payload gate of the mainline install: only tests, mltest and devtest files use it
                if TEST_GATE_USE.search(re.sub(r"//[^\n]*", "", text)) and not (in_testonly or DEVTEST_TAG.search(text) or
                                                      rel == os.path.join("internal", "mainline", "gate.go")):
                    errors.append(f"{rel}: uses mainline.TestGate (the test payload gate) outside a test, mltest or a devtest file")
                for imp in imports_of(text):
                    if imp in testonly and not in_testonly:
                        errors.append(f"{rel}: imports the test-only package {imp} outside a _test.go file")
                if inside or BUILD_TAG.search(text):
                    continue
                for imp in imports_of(text):
                    if imp in banned:
                        errors.append(f"{rel}: imports the install package {imp} without //go:build install")
    go = shutil.which("go")
    if go:
        def deps(*tags):
            r = subprocess.run([go, "list", *tags, "-deps", "./cmd/uc-pxx-inst"], cwd=REPO,
                               capture_output=True, text=True)
            if r.returncode != 0:
                errors.append(f"go list {' '.join(tags)}: {r.stderr.strip()}")
                return set()
            return set(r.stdout.split())
        default, tagged = deps(), deps("-tags", "install")
        dt = deps("-tags", "install,devtest")
        for b in sorted(banned & default):
            errors.append(f"the default build of cmd/uc-pxx-inst depends on the install package {b}")
        for need in ("/internal/install", "/internal/device", "/internal/devmode", "/internal/mainline",
                     "/internal/mainline/mldrive"):
            if tagged and MODULE + need not in tagged:
                errors.append(f"the install build of cmd/uc-pxx-inst does not hold {need[1:]}: the check sees nothing")
        for b in sorted(devtest & tagged):
            errors.append(f"the install build of cmd/uc-pxx-inst depends on the devtest package {b}")
        s2 = {MODULE + "/internal/step2"}
        for name, g in (("install", tagged), ("install,devtest", dt), ("install,step2", deps("-tags", "install,step2"))):
            for b in sorted(s2 & g):
                errors.append(f"the {name} build of cmd/uc-pxx-inst depends on the step-2 package {b}")
        r = subprocess.run([go, "list", "-tags", "install,step2", "-deps", "./cmd/uc-pxx-step2"], cwd=REPO,
                           capture_output=True, text=True)
        if r.returncode != 0:
            errors.append(f"go list -tags install,step2 ./cmd/uc-pxx-step2: {r.stderr.strip()}")
        else:
            g = set(r.stdout.split())
            if MODULE + "/internal/step2" not in g or MODULE + "/internal/device" not in g:
                errors.append("the build of cmd/uc-pxx-step2 does not hold internal/step2 and internal/device: the check sees nothing")
            for p in STEP2_FORBIDDEN:
                if MODULE + "/" + p in g:
                    errors.append(f"the build of cmd/uc-pxx-step2 depends on {p}")
        for b in sorted(testonly & (default | tagged | dt)):
            errors.append(f"a build of cmd/uc-pxx-inst depends on the test-only package {b}")
        for p in STATUS_ONLY:
            if tagged and MODULE + "/" + p in tagged:
                errors.append(f"the install build of cmd/uc-pxx-inst depends on {p}: the download-mode restart is status-only")
            if default and MODULE + "/" + p not in default:
                errors.append(f"the default build of cmd/uc-pxx-inst does not hold {p}: the check sees nothing")
        # FAT-READER-REVIEW F5: the typed check of the readers and the flow
        r = subprocess.run([go, "run", "./ci/goallow", "."], cwd=REPO, capture_output=True, text=True)
        out = (r.stdout + r.stderr).strip()
        if r.returncode != 0:
            fails = [l[len("FAIL: "):] for l in out.splitlines() if l.startswith("FAIL: ")]
            errors.extend(f"goallow: {l}" for l in fails)
            if not fails:
                errors.append(f"goallow did not run: {out}")
        else:
            print("check-no-writer: " + out, file=sys.stderr)
    else:
        print("check-no-writer: no go tool, import graph not checked (the file check ran)", file=sys.stderr)
    return count


def check_binary(name, data, errors):
    """A Go binary of the status image must not hold the install packages."""
    for p in INSTALL_ONLY:
        if (MODULE + "/" + p + ".").encode() in data:
            errors.append(f"{name}: holds the install package {MODULE}/{p}")


# The test payload gate (internal/mainline/gate.go TestGate): never in a release build.
TEST_GATE_USE = re.compile(r"\bmainline\.TestGate\b|\bfunc TestGate\b|\bTestGate\(")
TEST_GATE_SYM = MODULE.encode() + b"/internal/mainline.TestGate"
# The device mode of the mainline install: in every install build.
MAINLINE_PKGS = ("internal/mainline", "internal/mainline/mldrive")
# The devtest flags and commands (cmd/uc-pxx-inst/devtest.go): never in a release build.
DEVTEST_STRINGS = (b"--test-root", b"test-kill-after", b"test-fixture-manifests", b"uc-pxx-inst test-fixture",
                   b"test-review-bundle")


def check_release_install(name, data, errors):
    """An install build: the device mode is in it, the devtest code and the
    test-only packages are not. (A build of a release before the mainline
    install has no mainline package; --mainline-install requires them.)"""
    for p in ("internal/install", "internal/device", "internal/devmode"):
        if (MODULE + "/" + p + ".").encode() not in data:
            errors.append(f"{name}: does not hold {MODULE}/{p}: not an install build?")
    for p in DEVTEST_ONLY + TEST_ONLY:
        if (MODULE + "/" + p + ".").encode() in data:
            errors.append(f"{name}: holds the devtest or test-only package {MODULE}/{p}")
    if DEVTEST_MARKER in data or any(s in data for s in DEVTEST_STRINGS):
        errors.append(f"{name}: holds devtest code (the marker or a test flag)")
    if TEST_GATE_SYM in data:
        errors.append(f"{name}: holds mainline.TestGate: a release build has only the release's payload gate")
    for p in STATUS_ONLY:
        if (MODULE + "/" + p + ".").encode() in data:
            errors.append(f"{name}: holds {MODULE}/{p}: the download-mode restart is in the status build only")


def check_mainline_install(name, data, errors):
    """An install build with the mainline install: check_release_install, and
    the mainline packages are in it (so the check of the test-only package
    sees something)."""
    check_release_install(name, data, errors)
    for p in MAINLINE_PKGS:
        if (MODULE + "/" + p + ".").encode() not in data:
            errors.append(f"{name}: does not hold {MODULE}/{p}: not an install build with the mainline install")


def check_step2(name, data, errors):
    """A release build of uc-pxx-step2: the step-2 session, the slot side of the
    device layer and the engine are in it; the RAM side, the decrypter and the
    devtest code are not."""
    for p in ("internal/step2", "internal/device", "internal/install", "internal/plan"):
        if (MODULE + "/" + p + ".").encode() not in data:
            errors.append(f"{name}: does not hold {MODULE}/{p}: not a build of uc-pxx-step2?")
    for p in STEP2_FORBIDDEN + DEVTEST_ONLY + TEST_ONLY + STATUS_ONLY:
        if (MODULE + "/" + p + ".").encode() in data:
            errors.append(f"{name}: holds {MODULE}/{p}")
    for b in STEP2_PCDFW_BAD:
        if MODULE.encode() + b"/" + b in data:
            errors.append(f"{name}: holds the firmware decrypter or key code {b.decode()}")
    if DEVTEST_MARKER in data or any(s in data for s in DEVTEST_STRINGS):
        errors.append(f"{name}: holds devtest code (the marker or a test flag)")
    if TEST_GATE_SYM in data:
        errors.append(f"{name}: holds mainline.TestGate")


def check_devtest(name, data, errors):
    """A devtest build holds the marker, so check_release_install sees something."""
    if DEVTEST_MARKER not in data:
        errors.append(f"{name}: a devtest build without the devtest marker: the release check sees nothing")


def read_cpio(path):
    """Yield (name, mode, data) of an uncompressed newc cpio archive."""
    with open(path, "rb") as fh:
        buf = fh.read()
    off = 0
    while off + 110 <= len(buf):
        hdr = buf[off:off + 110]
        if hdr[:6] not in (b"070701", b"070702"):
            raise ValueError(f"bad cpio header at {off}")
        fields = [int(hdr[6 + 8 * i:14 + 8 * i], 16) for i in range(13)]
        mode, filesize, namesize = fields[1], fields[6], fields[11]
        name_start = off + 110
        name = buf[name_start:name_start + namesize - 1].decode("utf-8", "replace")
        data_start = (name_start + namesize + 3) & ~3
        data = buf[data_start:data_start + filesize]
        off = (data_start + filesize + 3) & ~3
        if name == "TRAILER!!!":
            return
        yield name, mode, data


def writer_in(name, mode, data):
    """Why a cpio entry is an A/B writer, or None."""
    if stat.S_ISREG(mode):
        sha = hashlib.sha256(data).hexdigest()
        if sha in WRITER_SHAS:
            return f"is the A/B writer {WRITER_SHAS[sha]} (sha256 {sha[:16]}...)"
        if data[:4] == b"\x7fELF":
            for w in WRITER_ELF_STRINGS:
                if w in data:
                    return f"is an ELF file with the boot-control string {w.decode()!r}"
    return None


def check_cpio(path, errors, install_image=False):
    count = 0
    names = set()
    tools = []
    for name, mode, data in read_cpio(path):
        rel = name.lstrip("./")
        names.add(rel)
        count += 1
        is_tool = install_image and rel == INSTALL_TOOL
        if not is_tool:
            check_name(name, errors)
            why = writer_in(name, mode, data)
            if why:
                errors.append(f"{name}: {why}")
            if rel.startswith(INSTALL_TOOL_DIR):
                if not stat.S_ISDIR(mode):  # an empty directory holds no writer
                    errors.append(f"{name}: nothing may be under {INSTALL_TOOL_DIR} in {'an install' if install_image else 'the status'} image")
        else:
            tools.append((name, mode, data))
        if stat.S_ISREG(mode) and is_text(data):
            check_text(name, data.decode("utf-8", "replace"), errors)
        elif stat.S_ISREG(mode) and data[:4] == b"\x7fELF" and not is_tool:
            if install_image and rel == "usr/bin/uc-pxx-inst":
                check_release_install(name, data, errors)
            else:
                check_binary(name, data, errors)
    for need in ("init", "usr/bin/uc-pxx-inst", "etc/init.d/rcS"):
        if need not in names:
            errors.append(f"{path}: {need} is missing: not an installer image?")
    if install_image:
        if len(tools) != 1:
            errors.append(f"{path}: an install image needs exactly one {INSTALL_TOOL}, it has {len(tools)}")
        for name, mode, data in tools:
            sha = hashlib.sha256(data).hexdigest()
            if not stat.S_ISREG(mode) or sha != INSTALL_TOOL_SHA:
                errors.append(f"{name}: not the reviewed uc-pxx-bootctl-inst (sha256 {sha})")
            elif stat.S_IMODE(mode) != INSTALL_TOOL_MODE:
                errors.append(f"{name}: mode {stat.S_IMODE(mode):04o}, the install image needs {INSTALL_TOOL_MODE:04o}")
    return count


def main():
    errors = []
    if len(sys.argv) == 1:
        n = check_sources(errors)
        what = f"{n} source file(s)"
    elif len(sys.argv) == 3 and sys.argv[1] in ("--binary", "--release-install", "--mainline-install", "--devtest", "--step2",
                                                 "--xfer"):
        fn = {"--binary": check_binary, "--release-install": check_release_install,
              "--mainline-install": check_mainline_install, "--devtest": check_devtest, "--step2": check_step2,
              "--xfer": check_xfer}[sys.argv[1]]
        try:
            with open(sys.argv[2], "rb") as fh:
                fn(sys.argv[2], fh.read(), errors)
        except OSError as e:
            print(f"check-no-writer: {e}", file=sys.stderr)
            return 2
        n, what = 1, f"the binary {sys.argv[2]} ({sys.argv[1][2:]})"
    elif len(sys.argv) == 3 and sys.argv[1] == "--install-image":
        try:
            n = check_cpio(sys.argv[2], errors, install_image=True)
        except (OSError, ValueError) as e:
            print(f"check-no-writer: {e}", file=sys.stderr)
            return 2
        what = f"{n} entries of the install image {sys.argv[2]} (outside {INSTALL_TOOL})"
    elif len(sys.argv) == 2:
        try:
            n = check_cpio(sys.argv[1], errors)
        except (OSError, ValueError) as e:
            print(f"check-no-writer: {e}", file=sys.stderr)
            return 2
        what = f"{n} entries of {sys.argv[1]}"
    else:
        print(__doc__, file=sys.stderr)
        return 2
    if errors:
        for e in errors:
            print("FAIL:", e)
        return 1
    print(f"ok: no eMMC writer in {what}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
