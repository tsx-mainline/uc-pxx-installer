#!/usr/bin/env python3
"""mkgptdisk.py OUT: a 64 MiB test disk for the qemu smoke test: GPT with 16 partitions, number 16 named
"security" (4 MiB of fixed pseudo-random bytes), the others p1..p15 small. Synthetic: no unit data."""
import hashlib, struct, sys, uuid, zlib

out = sys.argv[1]
SECT = 512; SIZE = 64 << 20; N = SIZE // SECT
names = ["p%d" % i for i in range(1, 16)] + ["security"]
parts, lba = [], 2048
for i, nm in enumerate(names):
    sz = 8192 if nm == "security" else 2048
    parts.append((nm, lba, lba + sz - 1)); lba += sz
disk = bytearray(SIZE)
# fill with a fixed pattern so sha256 before/after means something
seed = b"optee-readtest qemu disk"
for off in range(0, SIZE, 32):
    disk[off:off + 32] = hashlib.sha256(seed + off.to_bytes(8, "little")).digest()
# protective MBR
disk[0:SECT] = bytes(SECT)
disk[446:462] = struct.pack("<BBBBBBBBII", 0, 0, 2, 0, 0xEE, 0xFF, 0xFF, 0xFF, 1, min(N - 1, 0xFFFFFFFF))
disk[510:512] = b"\x55\xaa"
tguid = uuid.UUID("0fc63daf-8483-4772-8e79-3d69d8477de4").bytes_le
ents = bytearray(128 * 128)
for i, (nm, a, b) in enumerate(parts):
    e = tguid + uuid.UUID(int=i + 1).bytes_le + struct.pack("<QQQ", a, b, 0) + nm.encode("utf-16-le").ljust(72, b"\0")
    ents[i * 128:(i + 1) * 128] = e
ecrc = zlib.crc32(bytes(ents)) & 0xffffffff
def hdr(cur, alt, entlba):
    h = struct.pack("<8sIIIIQQQQ16sQIII", b"EFI PART", 0x10000, 92, 0, 0, cur, alt, 34, N - 34,
                    uuid.UUID(int=0x5c95a8).bytes_le, entlba, 128, 128, ecrc)
    c = zlib.crc32(h) & 0xffffffff
    return h[:16] + struct.pack("<I", c) + h[20:]
disk[SECT:2 * SECT] = hdr(1, N - 1, 2).ljust(SECT, b"\0")
disk[2 * SECT:34 * SECT] = ents
disk[(N - 33) * SECT:(N - 1) * SECT] = ents
disk[(N - 1) * SECT:N * SECT] = hdr(N - 1, 1, N - 33).ljust(SECT, b"\0")
open(out, "wb").write(disk)
s = parts[15]
print("disk %s: %d sectors; security = partition 16, LBA %d..%d, sha256 %s" %
      (out, N, s[1], s[2], hashlib.sha256(disk[s[1] * SECT:(s[2] + 1) * SECT]).hexdigest()))
print("disk sha256 %s" % hashlib.sha256(disk).hexdigest())
