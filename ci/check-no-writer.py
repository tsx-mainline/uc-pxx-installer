#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-2.0-or-later
"""Check that the installer RAM image has no eMMC writer.

    ci/check-no-writer.py              check the sources (image/overlay, the Go packages)
    ci/check-no-writer.py IMAGE.cpio   check a built initramfs (uncompressed newc cpio)
    ci/check-no-writer.py --binary F   check one p10inst binary for the install packages

The status image must not write to the eMMC in any way. This check fails when
  - the image holds a tool that writes boot data or disks: uc-pxx-bootctl and
    the other boot-control tools, e2fsprogs, partition tools, the busybox
    applets that write disks or flash;
  - a script writes to an eMMC device (dd of=/dev/mmcblk..., > /dev/mmcblk...,
    RPMB), makes it writable again (blockdev --setrw), or mounts an eMMC file
    system other than with ro,noload;
  - a Go package that reads the eMMC (blk, status, ext4, bootimg,
    vendorstore, avbab, devmem, gpt, vbmeta, plan) opens a file for writing;
  - the install side leaks into the default build. The install packages
    (internal/install, internal/bootctl, internal/fakeemmc,
    internal/pcdfw/pcdfwtest) may only be imported by Go files that carry the
    build constraint "//go:build install". When the go tool is there, the
    import graph of the default ./cmd/p10inst must not hold them, and the
    graph with -tags install must hold internal/install (so the check sees
    something). In a built initramfs, the p10inst binary must not hold the
    symbols of those packages (Go keeps package paths in its function table,
    also in a stripped binary).
Exit status: 0 clean, 1 a writer found, 2 usage or I/O error.
"""
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
    "tsx-data-restore",
}

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
              "internal/gpt", "internal/vbmeta", "internal/plan"]

# Go: the install side. Only files with the install build constraint may
# import these, and the default binary must not contain them.
MODULE = "uc-pxx-installer"
INSTALL_ONLY = ["internal/install", "internal/bootctl", "internal/fakeemmc", "internal/pcdfw/pcdfwtest"]
BUILD_TAG = re.compile(r"^//go:build\s+install\s*$", re.M)
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
    for top in ("cmd", "internal"):
        for root, _, files in os.walk(os.path.join(REPO, top)):
            rel_dir = os.path.relpath(root, REPO)
            inside = any(rel_dir == p or rel_dir.startswith(p + os.sep) for p in INSTALL_ONLY)
            for f in files:
                if not f.endswith(".go") or f.endswith("_test.go"):
                    continue
                rel = os.path.join(rel_dir, f)
                with open(os.path.join(root, f), encoding="utf-8") as fh:
                    text = fh.read()
                count += 1
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
        for b in sorted(banned & default):
            errors.append(f"the default build of cmd/p10inst depends on the install package {b}")
        if tagged and MODULE + "/internal/install" not in tagged:
            errors.append("the install build of cmd/p10inst does not hold internal/install: the check sees nothing")
    else:
        print("check-no-writer: no go tool, import graph not checked (the file check ran)", file=sys.stderr)
    return count


def check_binary(name, data, errors):
    """A Go binary of the status image must not hold the install packages."""
    for p in INSTALL_ONLY:
        if (MODULE + "/" + p + ".").encode() in data:
            errors.append(f"{name}: holds the install package {MODULE}/{p}")


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


def check_cpio(path, errors):
    count = 0
    names = set()
    for name, mode, data in read_cpio(path):
        names.add(name.lstrip("./"))
        count += 1
        check_name(name, errors)
        if stat.S_ISREG(mode) and is_text(data):
            check_text(name, data.decode("utf-8", "replace"), errors)
        elif stat.S_ISREG(mode) and data[:4] == b"\x7fELF":
            check_binary(name, data, errors)
    for need in ("init", "usr/bin/p10inst", "etc/init.d/rcS"):
        if need not in names:
            errors.append(f"{path}: {need} is missing: not an installer image?")
    return count


def main():
    errors = []
    if len(sys.argv) == 1:
        n = check_sources(errors)
        what = f"{n} source file(s)"
    elif len(sys.argv) == 3 and sys.argv[1] == "--binary":
        try:
            with open(sys.argv[2], "rb") as fh:
                check_binary(sys.argv[2], fh.read(), errors)
        except OSError as e:
            print(f"check-no-writer: {e}", file=sys.stderr)
            return 2
        n, what = 1, f"the binary {sys.argv[2]}"
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
