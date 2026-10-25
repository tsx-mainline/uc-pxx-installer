#!/usr/bin/env python3
"""Build the installer manifest for one official UC-P10 firmware.

Input: the pcdfw file and the directory that pcdfw-unpack.py wrote for it.
Output (JSON): the pcdfw sha256, the build identity (from system build.prop), the
vbmeta facts the installer's preflight needs (AVB public-key hash, rollback
index), and one entry per partition image: target partition, size, sha256, action.

The installer accepts an image only if its sha256 is in a manifest under the same
partition name. A sha256 is a fingerprint, not a key: nothing secret is in here.

Usage:  p10-fw-manifest.py PCDFW UNPACK_DIR -o manifest.json [--debugfs PATH]
"""
import argparse
import hashlib
import json
import os
import re
import struct
import subprocess
import sys
import tempfile

# seg_id -> A/B partition base name (see pcdfw-unpack.py for the evidence)
PARTITION = {
    0xfd02: "uboot",
    0xfd03: "trust",
    0xfe01: "dtbo",
    0xfe02: "vbmeta",
    0xfe04: "boot",
    0xfe06: "vendor",
    0xfe08: "oem",
    0xfe10: "system",
}
# trust is version-independent (1.0.0.94 == 1.0.6.20); write it only if it differs
ACTION = {"trust": "write-if-different"}
BUILD_KEYS = ("ro.build.fingerprint", "ro.build.display.id", "ro.build.type",
              "ro.build.version.release", "ro.build.version.incremental", "ro.build.date")


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for b in iter(lambda: f.read(8 << 20), b""):
            h.update(b)
    return h.hexdigest()


def vbmeta_info(path):
    """AvbVBMetaImageHeader (big-endian, 256 bytes) + the public key it carries."""
    with open(path, "rb") as f:
        blob = f.read()
    if blob[:4] != b"AVB0":
        raise SystemExit("%s: not a vbmeta image" % path)
    auth_size, aux_size = struct.unpack_from(">QQ", blob, 12)
    algorithm, = struct.unpack_from(">I", blob, 28)
    pk_off, pk_size = struct.unpack_from(">QQ", blob, 64)
    rollback_index, = struct.unpack_from(">Q", blob, 112)
    flags, = struct.unpack_from(">I", blob, 120)
    release = blob[128:176].split(b"\0")[0].decode("ascii", "replace")
    aux = 256 + auth_size
    pubkey = blob[aux + pk_off: aux + pk_off + pk_size]
    return {
        "image_bytes": 256 + auth_size + aux_size,
        "algorithm": algorithm,
        "rollback_index": rollback_index,
        "flags": flags,
        "release_string": release,
        "public_key_sha1": hashlib.sha1(pubkey).hexdigest(),
        "public_key_sha256": hashlib.sha256(pubkey).hexdigest(),
    }


def build_prop(system_img, debugfs):
    """Read build.prop from the system ext4 image with debugfs (read-only)."""
    for path in ("/system/build.prop", "/build.prop"):
        with tempfile.NamedTemporaryFile(delete=False) as t:
            tmp = t.name
        try:
            subprocess.run([debugfs, "-R", "dump %s %s" % (path, tmp), system_img],
                           check=True, capture_output=True)
            with open(tmp, "rb") as f:
                text = f.read().decode("utf-8", "replace")
        finally:
            os.unlink(tmp)
        if text.strip():
            props = {}
            for line in text.splitlines():
                if "=" in line and not line.lstrip().startswith("#"):
                    k, v = line.split("=", 1)
                    props[k.strip()] = v.strip()
            return path, {k: props.get(k) for k in BUILD_KEYS}
    return None, {}


def main():
    ap = argparse.ArgumentParser(description="Build the installer manifest for one firmware.")
    ap.add_argument("pcdfw")
    ap.add_argument("unpack_dir")
    ap.add_argument("-o", "--out", required=True)
    ap.add_argument("--debugfs", default="debugfs")
    ap.add_argument("--fw-version", required=True,
                    help="Crestron version, e.g. 1.0.6.20 (only in the encrypted segment "
                         "headers: pcdfw-unpack.py prints it). build.prop does not carry it; "
                         "the installer maps a slot to this version by ro.build.version.incremental")
    args = ap.parse_args()

    images = {}
    for name in sorted(os.listdir(args.unpack_dir)):
        m = re.fullmatch(r"(\d\d)_[a-z]+_([0-9a-f]{4})\.img", name)
        if not m:
            continue
        seg_id = int(m.group(2), 16)
        part = PARTITION.get(seg_id)
        if part is None:
            sys.exit("%s: unknown segment id 0x%04x" % (name, seg_id))
        if part in images:
            sys.exit("two images for partition %s" % part)
        images[part] = (int(m.group(1)), seg_id, os.path.join(args.unpack_dir, name))
    missing = sorted(set(PARTITION.values()) - set(images))
    if missing:
        sys.exit("missing partition images: %s" % ", ".join(missing))

    partitions = []
    for part, (idx, seg_id, path) in sorted(images.items(), key=lambda kv: kv[1][0]):
        size = os.path.getsize(path)
        digest = sha256_file(path)
        print("%-7s 0x%04x %12d  %s" % (part, seg_id, size, digest))
        partitions.append({
            "segment": idx,
            "seg_id": "0x%04x" % seg_id,
            "partition": part,
            "image": os.path.basename(path),
            "size": size,
            "sha256": digest,
            "action": ACTION.get(part, "write"),
        })

    prop_path, build = build_prop(images["system"][2], args.debugfs)
    manifest = {
        "schema": 1,
        "firmware": {
            "version": args.fw_version,
            "file": os.path.basename(args.pcdfw),
            "size": os.path.getsize(args.pcdfw),
            "sha256": sha256_file(args.pcdfw),
        },
        "build": build,
        "build_prop_path": prop_path,
        "vbmeta": vbmeta_info(images["vbmeta"][2]),
        "partitions": partitions,
    }
    with open(args.out, "w") as f:
        json.dump(manifest, f, indent=2)
        f.write("\n")
    print("build: %s (%s)" % (build.get("ro.build.display.id"), build.get("ro.build.type")))
    v = manifest["vbmeta"]
    print("vbmeta: rollback_index %d, public key sha1 %s" % (v["rollback_index"], v["public_key_sha1"]))
    print("wrote %s" % args.out)


if __name__ == "__main__":
    main()
