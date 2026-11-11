#!/usr/bin/env python3
"""cpiodiff.py A.cpio B.cpio: list the entries of two newc cpios that differ (added, removed, changed:
mode, size, sha256, rdev, link target). Read only."""
import hashlib, sys

def read(p):
    d = open(p, "rb").read(); off = 0; out = {}
    while True:
        h = d[off:off + 110]
        if h[:6] != b"070701":
            sys.exit("bad header in %s at %d" % (p, off))
        f = [int(h[6 + 8 * i:14 + 8 * i], 16) for i in range(13)]
        mode, size, rmaj, rmin, nsz = f[1], f[6], f[9], f[10], f[11]
        name = d[off + 110:off + 110 + nsz - 1].decode()
        off = (off + 110 + nsz + 3) & ~3
        data = d[off:off + size]
        off = (off + size + 3) & ~3
        if name == "TRAILER!!!":
            return out
        out[name] = (oct(mode), size, hashlib.sha256(data).hexdigest(), (rmaj, rmin))

a, b = read(sys.argv[1]), read(sys.argv[2])
print("A %s: %d entries; B %s: %d entries" % (sys.argv[1], len(a), sys.argv[2], len(b)))
for n in sorted(set(a) | set(b)):
    if n not in b:
        print("REMOVED  %s %s" % (n, a[n]))
    elif n not in a:
        print("ADDED    %s mode %s size %d sha256 %s" % (n, b[n][0], b[n][1], b[n][2]))
    elif a[n] != b[n]:
        print("CHANGED  %s\n   A %s\n   B %s" % (n, a[n], b[n]))
