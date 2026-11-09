#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-2.0-or-later
"""Make a mainline bundle (payload contract v1) for the CI runs, from made-up
parts, with the REAL uc-pxx-linux kernel/mkimage.sh. It shares no code with
p10inst. Run it as a normal user on the build host; it writes only into OUT.

    mainline-fixture.py --mkimage MKIMAGE_SH --out OUT (--fixture FXDIR | --keeper UNPACK_DIR)
                        [--token T] [--version V] [--linux-bytes N] [--rootfs-bytes N] [--seed S]

OUT/uc-pxx-mainline/ gets manifest.json, boot-template.img and rootfs.ext4:
  boot-template.img  kernel/mkimage.sh --resource <empty file> --board TOKEN Image DTB
                     (a made-up arm64 Image: a sha256 chain of the seed with the
                     Image magic; a made-up device tree with /chosen/bootargs)
  boot_assembled     the sha256 and size of kernel/mkimage.sh --resource RES with
                     the same inputs (the image itself is deleted: with --keeper it
                     holds the vendor resource)
  rootfs.ext4        a sha256 chain of the seed with an ext4 superblock magic, the
                     label tsxroot and the blocks count of its size (not a file
                     system: the installer checks the label and the size, and
                     writes the bytes)
The keeper chain (requires_bootchain, vbmeta_boot_cap):
  --fixture FXDIR    the made-up fixture of `p10inst test-fixture -o FXDIR`
                     (devtest): the sha256 of FXDIR/new/{uboot,trust,dtbo,vbmeta}.img,
                     FXDIR/resource.bin, the version and build type of
                     FXDIR/manifest.json and the image_size of its boot descriptor.
                     No vendor byte anywhere.
  --keeper UNPACK    an unpacked 1.0.0.94 userdebug firmware (dryrun-real.py
                     --keeper-fw): its 00_uboot, 01_trust, 02_dtbo, 03_vbmeta
                     images and the resource of 04_boot (read in memory, given
                     to mkimage.sh through a file in a private temporary folder
                     that is deleted); the cap 38029312 of the signed vbmeta.
                     Build host only: the vendor files never leave it.
Exit status: 0 made, 1 a check failed, 2 usage.
"""
import argparse
import hashlib
import json
import os
import shutil
import struct
import subprocess
import sys
import tempfile

KERNEL_REV = "d02e92631f2810a4a43fb6f09824afb9b0044ee2"  # the reviewed uc-pxx-lts pin
MKIMAGE_SHA = "98ef579ce1f152463c8902c53c2f2f8edfbdfd6b8a0d40d650c59844d893703f"
REAL_CAP = 38029312  # the boot descriptor image_size of the signed 1.0.0.94 userdebug vbmeta
PAGE = 2048
KEEPER_FILES = {"uboot": "00_uboot_fd02.img", "trust": "01_trust_fd03.img", "dtbo": "02_dtbo_fe01.img",
                "vbmeta": "03_vbmeta_fe02.img", "boot": "04_boot_fe04.img"}


def sha(b):
    return hashlib.sha256(b).hexdigest()


def chain(seed, tag, n):
    out, s = bytearray(), hashlib.sha256(seed.encode() + tag).digest()
    while len(out) < n:
        out += s
        s = hashlib.sha256(s).digest()
    return bytearray(out[:n])


def second_of(boot):
    """The second part (the resource) of an Android boot image v0."""
    if boot[:8] != b"ANDROID!":
        raise SystemExit("FAIL: not an Android boot image")
    ks, rs, ss, page = struct.unpack_from("<I", boot, 8)[0], struct.unpack_from("<I", boot, 16)[0], \
        struct.unpack_from("<I", boot, 24)[0], struct.unpack_from("<I", boot, 36)[0]
    if ss == 0 or page not in (2048, 4096):
        raise SystemExit("FAIL: the boot image has no second part")
    pages = lambda n: (n + page - 1) // page
    off = page * (1 + pages(ks) + pages(rs))
    return bytes(boot[off:off + ss])


def boot_cap(manifest):
    for d in manifest["avb"]["descriptors"]:
        if d.get("partition") == "boot" and d.get("type") == "hash":
            return int(d["image_size"])
    raise SystemExit("FAIL: the fixture manifest has no boot hash descriptor")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--mkimage", required=True)
    ap.add_argument("--out", required=True)
    g = ap.add_mutually_exclusive_group(required=True)
    g.add_argument("--fixture")
    g.add_argument("--keeper")
    ap.add_argument("--token", default="mlci-1")
    ap.add_argument("--version", default="v0.0.0-ci")
    ap.add_argument("--linux-bytes", type=int, default=300000)
    ap.add_argument("--rootfs-bytes", type=int, default=64 << 20)
    ap.add_argument("--seed", default="ci")
    a = ap.parse_args()
    with open(a.mkimage, "rb") as f:
        if sha(f.read()) != MKIMAGE_SHA:
            raise SystemExit("FAIL: %s is not kernel/mkimage.sh %s" % (a.mkimage, MKIMAGE_SHA[:8]))
    if a.rootfs_bytes % 512 or a.rootfs_bytes < 4096:
        raise SystemExit("FAIL: --rootfs-bytes must be whole sectors")
    if a.fixture:
        m = json.load(open(os.path.join(a.fixture, "manifest.json")))
        img = lambda n: open(os.path.join(a.fixture, "new", n + ".img"), "rb").read()
        res = open(os.path.join(a.fixture, "resource.bin"), "rb").read()
        if sha(res) != m["boot_chain"]["boot_resource_sha256"] or second_of(img("boot")) != res:
            raise SystemExit("FAIL: the fixture's resource.bin is not the resource of its boot image")
        chainv = {"version": m["firmware"]["version"], "build_type": m["build"]["ro.build.type"]}
        cap = boot_cap(m)
    else:
        img = lambda n: open(os.path.join(a.keeper, KEEPER_FILES[n]), "rb").read()
        res = second_of(img("boot"))
        chainv = {"version": "1.0.0.94", "build_type": "userdebug"}
        cap = REAL_CAP
    chainv.update({"uboot_sha256": sha(img("uboot")), "trust_sha256": sha(img("trust")), "dtbo_sha256": sha(img("dtbo")),
                   "vbmeta_sha256": sha(img("vbmeta")), "resource_sha256": sha(res)})
    out = os.path.abspath(a.out)
    bundle = os.path.join(out, "uc-pxx-mainline")
    if os.path.exists(bundle):
        shutil.rmtree(bundle)
    os.makedirs(bundle)
    tmp = tempfile.mkdtemp(prefix="mlfx-", dir=out)
    try:
        os.chmod(tmp, 0o700)
        env = dict(os.environ, TMPDIR=tmp)
        dts = os.path.join(tmp, "k.dts")
        with open(dts, "w") as f:
            f.write('/dts-v1/;\n/ { #address-cells = <2>; #size-cells = <2>; compatible = "made-up,board"; '
                    'chosen { bootargs = "console=ttyS2,115200n8 rdinit=/init"; }; };\n')
        dtb = os.path.join(tmp, "k.dtb")
        subprocess.run(["dtc", "-I", "dts", "-O", "dtb", "-o", dtb, dts], check=True, capture_output=True)
        image = chain(a.seed, b"Image", a.linux_bytes)
        struct.pack_into("<Q", image, 16, a.linux_bytes)
        image[56:60] = b"ARMd"
        ip = os.path.join(tmp, "Image")
        open(ip, "wb").write(image)
        empty, rp = os.path.join(tmp, "empty.bin"), os.path.join(tmp, "res.bin")
        open(empty, "wb").close()
        open(rp, "wb").write(res)
        tp, asm = os.path.join(bundle, "boot-template.img"), os.path.join(tmp, "assembled.img")
        for r, o in ((empty, tp), (rp, asm)):
            subprocess.run(["bash", a.mkimage, "--resource", r, "--board", a.token, ip, dtb, o], check=True,
                           capture_output=True, env=env)
        tb, ab = open(tp, "rb").read(), open(asm, "rb").read()
    finally:
        shutil.rmtree(tmp)  # the assembled image (with --keeper: the vendor resource) is gone here
    if len(ab) > cap:
        raise SystemExit("FAIL: the assembled image of %d bytes is over the boot cap %d" % (len(ab), cap))
    rf = chain(a.seed, b"rootfs", a.rootfs_bytes)
    rf[1024:2048] = bytes(1024)
    struct.pack_into("<H", rf, 1024 + 0x38, 0xEF53)
    rf[1024 + 0x78:1024 + 0x78 + len("tsxroot")] = b"tsxroot"
    bs, lg = (4096, 2) if a.rootfs_bytes % 4096 == 0 else (1024, 0)
    struct.pack_into("<I", rf, 1024 + 0x18, lg)  # s_log_block_size
    struct.pack_into("<I", rf, 1024 + 0x04, a.rootfs_bytes // bs)  # s_blocks_count: the installer checks the size
    open(os.path.join(bundle, "rootfs.ext4"), "wb").write(rf)
    man = {
        "schema": 1, "kind": "uc-pxx-mainline", "version": a.version, "model": "UC-P10",
        "kernel": {"rev": KERNEL_REV}, "token": a.token,
        "boot_template": {"file": "boot-template.img", "size": len(tb), "sha256": sha(tb)},
        "boot_assembled": {"resource_sha256": sha(res), "size": len(ab), "sha256": sha(ab)},
        "rootfs": {"file": "rootfs.ext4", "size": len(rf), "sha256": sha(rf), "label": "tsxroot"},
        "requires_bootchain": chainv,
        "vbmeta_boot_cap": cap,
    }
    with open(os.path.join(bundle, "manifest.json"), "w") as f:
        json.dump(man, f, indent=2)
        f.write("\n")
    print("bundle %s: template %d bytes %s, assembled %d bytes %s (cap %d), rootfs %d bytes %s; chain %s %s" % (
        bundle, len(tb), sha(tb)[:16], len(ab), sha(ab)[:16], cap, len(rf), sha(rf)[:16], chainv["version"],
        chainv["build_type"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
