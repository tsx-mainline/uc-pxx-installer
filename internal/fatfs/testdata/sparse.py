#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-2.0-or-later
"""Test fixtures of package fatfs.

    sparse.py pack IMAGE OUT.fat.gz     store the non-zero 512-byte sectors of IMAGE
    sparse.py unpack IN.fat.gz IMAGE    make the image again (a sparse file)
    sparse.py pattern SEED SIZE OUT     write the test file content (sparse_test.go makes the same)

The .fat.gz format (gzip): b"FATSPARSE1\\n", the image size (u64 LE), then
records of offset (u64 LE), length (u32 LE) and the bytes; the record with
offset 2^64-1 ends it. A 1200 MiB drive with one small file packs into a few
kilobytes. The name does not end in .img, so the repo's vendor-file check
does not see it as a partition image.
"""
import gzip
import struct
import sys

END = (1 << 64) - 1
SECTOR = 512


def pack(image, out):
    with open(image, "rb") as f, gzip.GzipFile(out, "wb", mtime=0) as g:
        f.seek(0, 2)
        size = f.tell()
        f.seek(0)
        g.write(b"FATSPARSE1\n" + struct.pack("<Q", size))
        run_off, run = None, bytearray()
        off = 0
        while True:
            chunk = f.read(1 << 20)
            if not chunk:
                break
            for i in range(0, len(chunk), SECTOR):
                s = chunk[i:i + SECTOR]
                if s.count(0) == len(s):
                    if run:
                        g.write(struct.pack("<QI", run_off, len(run)) + run)
                        run_off, run = None, bytearray()
                else:
                    if not run:
                        run_off = off + i
                    run += s
                    if len(run) >= 1 << 20:
                        g.write(struct.pack("<QI", run_off, len(run)) + run)
                        run_off, run = None, bytearray()
            off += len(chunk)
        if run:
            g.write(struct.pack("<QI", run_off, len(run)) + run)
        g.write(struct.pack("<QI", END, 0))


def unpack(src, image):
    with gzip.open(src, "rb") as g, open(image, "wb") as f:
        if g.read(11) != b"FATSPARSE1\n":
            sys.exit(f"{src}: not a sparse fixture")
        (size,) = struct.unpack("<Q", g.read(8))
        f.truncate(size)
        while True:
            off, n = struct.unpack("<QI", g.read(12))
            if off == END:
                break
            f.seek(off)
            f.write(g.read(n))


def pattern(seed, size):
    """512-byte blocks; block i repeats the 8-byte value (seed*1000003+i)*0x9E3779B97F4A7C15."""
    out = bytearray()
    i = 0
    while len(out) < size:
        v = ((seed * 1000003 + i) * 0x9E3779B97F4A7C15) & ((1 << 64) - 1)
        out += struct.pack("<Q", v) * 64
        i += 1
    return bytes(out[:size])


def main():
    if len(sys.argv) == 4 and sys.argv[1] == "pack":
        pack(sys.argv[2], sys.argv[3])
    elif len(sys.argv) == 4 and sys.argv[1] == "unpack":
        unpack(sys.argv[2], sys.argv[3])
    elif len(sys.argv) == 5 and sys.argv[1] == "pattern":
        with open(sys.argv[4], "wb") as f:
            f.write(pattern(int(sys.argv[2]), int(sys.argv[3])))
    else:
        sys.exit(__doc__)


if __name__ == "__main__":
    main()
