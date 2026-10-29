#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-2.0-or-later
"""Check that the installer RAM image has no eMMC writer.

    ci/check-no-writer.py              check the sources (image/overlay, the Go packages)
    ci/check-no-writer.py IMAGE.cpio   check a built status initramfs (uncompressed newc cpio)
    ci/check-no-writer.py --install-image IMAGE.cpio
                                       check an install initramfs: the one allowed A/B
                                       writer, a release install p10inst, nothing else
    ci/check-no-writer.py --binary F   check one p10inst binary for the install packages
    ci/check-no-writer.py --release-install F
                                       check an install build of p10inst: it has the
                                       device mode, but no devtest code
    ci/check-no-writer.py --devtest F  check a devtest build: it holds the devtest marker

The status image must not write to the eMMC in any way. This check fails when
  - the image holds a tool that writes boot data or disks: uc-pxx-bootctl,
    uc-pxx-bootctl-inst and the other boot-control tools, e2fsprogs,
    partition tools, the busybox applets that write disks or flash. A tool is
    found by its name, by its sha256 (every reviewed A/B writer binary), and
    in any ELF file by the log prefix of the boot-control tools; and nothing
    may be under usr/libexec/p10inst/ (the place of the install image's tool);
  - a script writes to an eMMC device (dd of=/dev/mmcblk..., > /dev/mmcblk...,
    RPMB), makes it writable again (blockdev --setrw), or mounts an eMMC file
    system other than with ro,noload;
  - a Go package that reads the eMMC (blk, status, ext4, bootimg,
    vendorstore, avbab, devmem, gpt, vbmeta, plan) or the P10-INSTALL drive
    (fatfs, fatfs/fattest) opens a file for writing;
  - the install side leaks into the default build. The install packages
    (internal/install, internal/bootctl, internal/fakeemmc,
    internal/pcdfw/pcdfwtest, internal/device, internal/devmode) may only be
    imported by Go files whose build constraint needs "install". When the go
    tool is there, the import graph of the default ./cmd/p10inst must not hold
    them, and the graph with -tags install must hold internal/install and
    internal/device (so the check sees something). In a built initramfs, the
    p10inst binary must not hold the symbols of those packages (Go keeps
    package paths in its function table, also in a stripped binary);
  - the devtest code leaks into an install build: internal/device/devicetest
    and the test flags may only be in files whose constraint needs "devtest",
    the -tags install graph must not hold devicetest, and a release install
    binary must not hold the devtest marker string;
  - a file of internal/device other than writer.go opens a file for writing,
    except the A/B lock file (disk.go) and the private copy of the bootctl
    binary (bootctl.go). writer.go is the only write open of an eMMC node.
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
    "38fea6d32412e7977ebc5845b138dc1c8c2df8555dae0b64b47f8a851b45e2d7": "uc-pxx-bootctl",
    "d979ee70fd15ff3892c3f59b4e2f9dbc02b13b9b176a6881f9d3f4eba630e4be": "p10-bootctl",
}
# The format strings of the boot-control tools (their log line and their status line), with
# the newline. A Go binary packs its strings without separators, so a short prefix such as
# "uc-pxx-bootctl:" can appear in p10inst by chance; these whole format strings do not.
WRITER_ELF_STRINGS = [b"uc-pxx-bootctl: %s\n", b"p10-bootctl: %s\n", b"libavb-would-boot: %c\n"]
INSTALL_TOOL_DIR = "usr/libexec/p10inst/"
INSTALL_TOOL = INSTALL_TOOL_DIR + "uc-pxx-bootctl-inst"
INSTALL_TOOL_SHA = "5d83215bb5cab5b5f68c6db6500af8bea91180338b67e46850c266afb34ee400"

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
              "internal/vendorstore", "internal/avbab", "internal/devmem",
              "internal/gpt", "internal/vbmeta", "internal/plan",
              "internal/fatfs", "internal/fatfs/fattest"]

# Go: the install side. Only files with the install build constraint may
# import these, and the default binary must not contain them.
MODULE = "uc-pxx-installer"
INSTALL_ONLY = ["internal/install", "internal/bootctl", "internal/fakeemmc", "internal/pcdfw/pcdfwtest",
                "internal/device", "internal/devmode"]
DEVTEST_ONLY = ["internal/device/devicetest"]
# A constraint that needs "install": install, or install && other terms.
BUILD_TAG = re.compile(r"^//go:build\s+install(\s*&&\s*!?[\w.]+)*\s*$", re.M)
DEVTEST_TAG = re.compile(r"^//go:build\s+install\s*&&\s*devtest\s*$", re.M)
DEVTEST_MARKER = b"p10inst devtest build, test roots allowed"
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


def check_text(name, text, errors):
    for n, line in enumerate(text.splitlines(), 1):
        s = line.strip()
        if s.startswith("#"):
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
    overlay = os.path.join(REPO, "image", "overlay")
    count = 0
    for root, _, files in os.walk(overlay):
        for f in files:
            p = os.path.join(root, f)
            rel = os.path.relpath(p, REPO)
            check_name(rel, errors)
            with open(p, "rb") as fh:
                data = fh.read()
            if is_text(data):
                check_text(rel, data.decode("utf-8", "replace"), errors)
                count += 1
    for script in ("image/mkinitramfs.sh", "image/build.sh"):
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
                for imp in imports_of(text):
                    if imp in devtest and not in_devtest and not DEVTEST_TAG.search(text):
                        errors.append(f"{rel}: imports the devtest package {imp} without //go:build install && devtest")
                if "--test-root" in text and not DEVTEST_TAG.search(text) and not in_devtest:
                    errors.append(f"{rel}: has the devtest flag --test-root without //go:build install && devtest")
                if inside or BUILD_TAG.search(text):
                    continue
                for imp in imports_of(text):
                    if imp in banned:
                        errors.append(f"{rel}: imports the install package {imp} without //go:build install")
    go = shutil.which("go")
    if go:
        def deps(*tags):
            r = subprocess.run([go, "list", *tags, "-deps", "./cmd/p10inst"], cwd=REPO,
                               capture_output=True, text=True)
            if r.returncode != 0:
                errors.append(f"go list {' '.join(tags)}: {r.stderr.strip()}")
                return set()
            return set(r.stdout.split())
        default, tagged = deps(), deps("-tags", "install")
        dt = deps("-tags", "install,devtest")
        for b in sorted(banned & default):
            errors.append(f"the default build of cmd/p10inst depends on the install package {b}")
        for need in ("/internal/install", "/internal/device", "/internal/devmode"):
            if tagged and MODULE + need not in tagged:
                errors.append(f"the install build of cmd/p10inst does not hold {need[1:]}: the check sees nothing")
        for b in sorted(devtest & tagged):
            errors.append(f"the install build of cmd/p10inst depends on the devtest package {b}")
    else:
        print("check-no-writer: no go tool, import graph not checked (the file check ran)", file=sys.stderr)
    return count


def check_binary(name, data, errors):
    """A Go binary of the status image must not hold the install packages."""
    for p in INSTALL_ONLY:
        if (MODULE + "/" + p + ".").encode() in data:
            errors.append(f"{name}: holds the install package {MODULE}/{p}")


def check_release_install(name, data, errors):
    """An install build: the device mode is in it, the devtest code is not."""
    for p in ("internal/install", "internal/device", "internal/devmode"):
        if (MODULE + "/" + p + ".").encode() not in data:
            errors.append(f"{name}: does not hold {MODULE}/{p}: not an install build?")
    for p in DEVTEST_ONLY:
        if (MODULE + "/" + p + ".").encode() in data:
            errors.append(f"{name}: holds the devtest package {MODULE}/{p}")
    if DEVTEST_MARKER in data or b"--test-root" in data or b"test-kill-after" in data:
        errors.append(f"{name}: holds devtest code (the marker or a test flag)")


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
            if install_image and rel == "usr/bin/p10inst":
                check_release_install(name, data, errors)
            else:
                check_binary(name, data, errors)
    for need in ("init", "usr/bin/p10inst", "etc/init.d/rcS"):
        if need not in names:
            errors.append(f"{path}: {need} is missing: not an installer image?")
    if install_image:
        if len(tools) != 1:
            errors.append(f"{path}: an install image needs exactly one {INSTALL_TOOL}, it has {len(tools)}")
        for name, mode, data in tools:
            sha = hashlib.sha256(data).hexdigest()
            if not stat.S_ISREG(mode) or sha != INSTALL_TOOL_SHA:
                errors.append(f"{name}: not the reviewed uc-pxx-bootctl-inst (sha256 {sha})")
    return count


def main():
    errors = []
    if len(sys.argv) == 1:
        n = check_sources(errors)
        what = f"{n} source file(s)"
    elif len(sys.argv) == 3 and sys.argv[1] in ("--binary", "--release-install", "--devtest"):
        fn = {"--binary": check_binary, "--release-install": check_release_install, "--devtest": check_devtest}[sys.argv[1]]
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
