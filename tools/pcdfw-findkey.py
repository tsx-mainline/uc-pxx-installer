#!/usr/bin/env python3
"""Find the pcdfw head key and IV in the device's own updater libraries.

The repo does not ship the key or the IV. Both are hard-coded in the device's
updater libraries on the system partition (/system/lib64/libwebupdate.so,
libprovdetect.so). This tool takes candidate 16-byte values from those files and
keeps the pair that decrypts a real pcdfw correctly. It never prints the key.

How a candidate is tested (format: see pcdfw-unpack.py):
  - Each seghdr is AES-128-CBC in independent 32-byte chunks with the head key.
    The pack signature sits at +0x10, the first 4 bytes of the SECOND block, which
    depends only on the key and the first ciphertext block, not on the IV. So every
    key candidate is tested alone first.
  - For a key that passes, each IV candidate is tested on the first block:
    size == size_used + 0x200, and seg_id is a known segment.
  - A pair is accepted only if it then walks the whole segment chain to exactly
    EOF, and each segment body starts with its expected magic.

Candidates: every 16-byte window of each printable-ASCII run of >= 16 bytes (the
key may be the start of a longer string). With --raw: every 16-byte window of the
whole file, for when the value is not stored as a string.

Usage:  pcdfw-findkey.py PCDFW LIB [LIB ...] [--raw] [--write-keyfile PATH]
Exit:   0 = exactly one pair | 2 = none | 3 = more than one (ambiguous; use none)
Needs:  pip install pycryptodome
"""
import argparse
import os
import struct
import sys

from Crypto.Cipher import AES

SIG = 0x43A78F39
SIG_LE = struct.pack("<I", SIG)
FIRST_SEG = 0x58
HDR_REGION = 0x200
SEGHDR_LEN = 0x60
IDT_ENTRY = 0x48
KNOWN_SEGS = {0xfd02, 0xfd03, 0xfe01, 0xfe02, 0xfe04, 0xfe06, 0xfe08, 0xfe10}
# expected start of each RAW (idt_len == 0) segment body
RAW_MAGIC = {
    0xfd02: b"LOADER",                  # U-Boot (-> uboot_a/_b)
    0xfd03: b"BL3X",                    # trust (-> trust_a/_b)
    0xfe01: bytes.fromhex("d7b7ab1e"),  # DTBO (big-endian magic)
    0xfe02: b"AVB0",                    # vbmeta
    0xfe04: b"ANDROID!",                # boot
}


def xor(a, b):
    return bytes(x ^ y for x, y in zip(a, b))


def candidates(data, raw):
    """{16-byte value: first offset} from one file."""
    out = {}
    if raw:
        for i in range(len(data) - 15):
            out.setdefault(data[i:i + 16], i)
        return out
    i, n = 0, len(data)
    while i < n:
        j = i
        while j < n and 0x20 <= data[j] < 0x7f:
            j += 1
        if j - i >= 16:
            for k in range(i, j - 15):
                out.setdefault(data[k:k + 16], k)
        i = j + 1
    return out


def dec_head(buf, key, iv):
    out = bytearray()
    for i in range(0, len(buf) - len(buf) % 32, 32):
        out += AES.new(key, AES.MODE_CBC, iv).decrypt(buf[i:i + 32])
    return bytes(out)


def walk(fh, fsz, key, iv):
    """Full check of one (key, iv) pair. Returns (ok, segments, reason)."""
    off, n = FIRST_SEG, 0
    while off + SEGHDR_LEN <= fsz:
        fh.seek(off)
        hdr = dec_head(fh.read(SEGHDR_LEN), key, iv)
        size_used, size = struct.unpack_from("<II", hdr, 0)
        seg_id, = struct.unpack_from("<H", hdr, 0x0a)
        idt_len, = struct.unpack_from("<I", hdr, 0x0c)
        if hdr[0x10:0x14] != SIG_LE:
            break
        if size != size_used + HDR_REGION or seg_id not in KNOWN_SEGS:
            return False, n, "seg %d: bad size/seg_id" % n
        body_key = hdr[0x10:0x20]
        fh.seek(off + HDR_REGION)
        first = AES.new(body_key, AES.MODE_CBC, iv).decrypt(fh.read(16))
        if idt_len == 0:
            magic = RAW_MAGIC.get(seg_id)
            if magic and not first.startswith(magic):
                return False, n, "seg %d (0x%04x): body magic mismatch" % (n, seg_id)
        elif first[:4] != SIG_LE:
            return False, n, "seg %d (0x%04x): index signature mismatch" % (n, seg_id)
        off += size
        n += 1
    if n == 0:
        return False, 0, "no segment"
    if off != fsz:
        return False, n, "walk ended at 0x%x, file is 0x%x" % (off, fsz)
    return True, n, "ok"


def main():
    ap = argparse.ArgumentParser(description="Find the pcdfw head key and IV in device libraries.")
    ap.add_argument("pcdfw")
    ap.add_argument("libs", nargs="+")
    ap.add_argument("--raw", action="store_true", help="test every 16-byte window, not only strings")
    ap.add_argument("--write-keyfile", metavar="PATH", help="save the pair (hex, mode 0600)")
    args = ap.parse_args()

    cands = {}       # value -> "lib@offset" (first place seen)
    for lib in args.libs:
        with open(lib, "rb") as f:
            data = f.read()
        found = candidates(data, args.raw)
        print("%s: %d bytes, %d candidates" % (os.path.basename(lib), len(data), len(found)))
        for v, o in found.items():
            cands.setdefault(v, "%s@0x%x" % (os.path.basename(lib), o))
    print("total distinct candidates: %d" % len(cands))

    fsz = os.path.getsize(args.pcdfw)
    with open(args.pcdfw, "rb") as fh:
        if struct.unpack("<I", fh.read(4))[0] != SIG:
            sys.exit("not a pcdfw (bad pack signature)")
        fh.seek(FIRST_SEG)
        c1 = fh.read(16)
        c2 = fh.read(16)

        keys = []
        for v in cands:
            ecb = AES.new(v, AES.MODE_ECB)
            if xor(ecb.decrypt(c2), c1)[:4] == SIG_LE:
                keys.append(v)
        print("key candidates passing the signature test: %d" % len(keys))

        pairs = []
        for k in keys:
            p1raw = AES.new(k, AES.MODE_ECB).decrypt(c1)
            for iv in cands:
                p1 = xor(p1raw, iv)
                size_used, size = struct.unpack_from("<II", p1, 0)
                seg_id, = struct.unpack_from("<H", p1, 0x0a)
                if size != size_used + HDR_REGION or seg_id not in KNOWN_SEGS:
                    continue
                if FIRST_SEG + size > fsz:
                    continue
                ok, nseg, why = walk(fh, fsz, k, iv)
                print("  pair: key from %s, iv from %s -> %s (%d segments)"
                      % (cands[k], cands[iv], why, nseg))
                if ok:
                    pairs.append((k, iv))

    print("pairs that walk the whole file: %d" % len(pairs))
    if not pairs:
        print("RESULT: no key found")
        sys.exit(2)
    if len(pairs) > 1:
        print("RESULT: ambiguous (%d pairs) - not using any" % len(pairs))
        sys.exit(3)
    print("RESULT: exactly one key/IV pair found")
    if args.write_keyfile:
        k, iv = pairs[0]
        fd = os.open(args.write_keyfile, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
        with os.fdopen(fd, "w") as f:
            f.write("head_key=%s\niv=%s\n" % (k.hex(), iv.hex()))
        os.chmod(args.write_keyfile, 0o600)
        print("key file written: %s" % args.write_keyfile)
    sys.exit(0)


if __name__ == "__main__":
    main()
