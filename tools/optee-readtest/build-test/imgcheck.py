#!/usr/bin/env python3
"""imgcheck.py IMG Image initramfs.cpio: own read-only checks of a status RAM image (not the build's):
v0 header, page 2048, no ramdisk, no second, header name = token; kernel part = shim + the Image; the shim
DTB at kernel+64 KiB: /model, /compatible, /chosen/bootargs; the Image holds the cpio as exactly one xz
stream; prints the kernel version string of the Image."""
import hashlib, lzma, re, struct, sys

img, image, cpio = sys.argv[1:4]
d = open(img, "rb").read(); li = open(image, "rb").read(); want = open(cpio, "rb").read()
assert d[:8] == b"ANDROID!", "magic"
k, ka, r, ra, s, sa, ta, p, v = struct.unpack_from("<9I", d, 8)
print("header: kernel %d B, ramdisk %d B, second %d B, page %d, version %d, name %r" %
      (k, r, s, p, v, d[48:64].rstrip(b"\0").decode()))
assert p == 2048 and v == 0 and s == 0
roff = p * (1 + (k + p - 1) // p)
print("ramdisk part: %d B, hex %s (mkimage.sh's placeholder)" % (r, d[roff:roff + r].hex()))
kern = d[p:p + k]
assert kern[0x200000:] == li, "kernel part is not shim + Image"
print("kernel part = shim (2 MiB) + Image (sha256 %s)" % hashlib.sha256(li).hexdigest())

def fdt_props(dtb):
    off_struct, off_str = struct.unpack_from(">I", dtb, 8)[0], struct.unpack_from(">I", dtb, 12)[0]
    q, path, out = off_struct, [], {}
    while True:
        tok = struct.unpack_from(">I", dtb, q)[0]; q += 4
        if tok == 1:
            e = dtb.index(b"\0", q); path.append(dtb[q:e].decode()); q = (e + 4) & ~3
        elif tok == 2:
            path.pop()
        elif tok == 3:
            ln, no = struct.unpack_from(">II", dtb, q)
            name = dtb[off_str + no:dtb.index(b"\0", off_str + no)].decode()
            out["/".join(path) or "/", name] = dtb[q + 8:q + 8 + ln]; q = (q + 8 + ln + 3) & ~3
        elif tok == 4:
            continue
        elif tok == 9:
            return out
        else:
            sys.exit("bad fdt token")

dt = kern[0x10000:]
assert dt[:4] == b"\xd0\x0d\xfe\xed", "no DTB at kernel+64KiB"
n = struct.unpack_from(">I", dt, 4)[0]; dtb = dt[:n]
pr = fdt_props(dtb)
print("shim DTB: %d B, sha256 %s" % (n, hashlib.sha256(dtb).hexdigest()))
print("  /model      = %r" % pr["/", "model"].rstrip(b"\0").decode())
print("  /compatible = %r" % pr["/", "compatible"].split(b"\0")[:-1])
print("  bootargs    = %r" % pr["/chosen", "bootargs"].rstrip(b"\0").decode())
opt = [kk for kk in pr if "optee" in kk[0]]
for kk in opt:
    print("  %s:%s = %r" % (kk[0], kk[1], pr[kk]))
found, pos = 0, li.find(b"\xfd7zXZ\x00")
while pos >= 0:
    try:
        if lzma.LZMADecompressor().decompress(li[pos:], max_length=len(want) + 1) == want:
            found += 1
    except lzma.LZMAError:
        pass
    pos = li.find(b"\xfd7zXZ\x00", pos + 1)
print("xz streams in the Image equal to the cpio: %d (want 1)" % found)
m = re.search(rb"Linux version [ -~]+", li)
print("Image version string: %s" % (m.group(0).decode() if m else "not found (compressed?)"))
