#!/bin/bash
# SPDX-License-Identifier: GPL-2.0-or-later
# Make a boot template and the images with a resource with the REAL
# uc-pxx-linux kernel/mkimage.sh, from made-up inputs (no vendor byte):
#
#   mkimage-pair.sh MKIMAGE_SH OUT TOKEN LINUX_BYTES SEED
#
# OUT gets:
#   template.img           mkimage.sh --resource <an empty file>  (the payload template)
#   res-<name>.bin         the made-up resources: "rsce" (kernel/mkresource.py packs a
#                          made-up device tree), "page" (4096 bytes), "odd" (3001 bytes)
#   expected-<name>.img    mkimage.sh --resource res-<name>.bin   (what Assemble must give)
#
# Tools: those of kernel/mkimage.sh (the cross binutils and gcc, mkbootimg, fdtget,
# fdtput), dtc and python3. The script writes only into OUT (mkimage.sh's own
# temporary folder is made under OUT through TMPDIR).
set -euo pipefail
MK=${1:?usage: mkimage-pair.sh MKIMAGE_SH OUT TOKEN LINUX_BYTES SEED}
OUT=${2:?}
TOKEN=${3:?}
LINUX=${4:?}
SEED=${5:?}
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)
export TMPDIR="$OUT/tmp"
mkdir -p "$TMPDIR"
HERE=$(cd "$(dirname "$MK")" && pwd)

cat > "$TMPDIR/k.dts" <<'DTS'
/dts-v1/;
/ { #address-cells = <2>; #size-cells = <2>; compatible = "made-up,board"; chosen { bootargs = "console=ttyS2,115200n8 rdinit=/init"; }; };
DTS
dtc -I dts -O dtb -o "$TMPDIR/k.dtb" "$TMPDIR/k.dts" 2>/dev/null
# a made-up arm64 Image: pseudo-random bytes (a sha256 chain of the seed), image_size
# at byte 16, the magic "ARM\x64" at byte 56
python3 -I - "$TMPDIR/Image" "$LINUX" "$SEED" <<'PY'
import hashlib, struct, sys
n, seed = int(sys.argv[2]), sys.argv[3].encode()
out, s = bytearray(), hashlib.sha256(seed).digest()
while len(out) < n:
    out += s
    s = hashlib.sha256(s).digest()
b = bytearray(out[:n])
struct.pack_into("<Q", b, 16, n)
b[56:60] = b"ARMd"
open(sys.argv[1], "wb").write(b)
PY
printf 'a made-up device tree for u-boot %s' "$SEED" > "$TMPDIR/uboot.dtb"
python3 -I "$HERE/mkresource.py" "$OUT/res-rsce.bin" rk-kernel.dtb="$TMPDIR/uboot.dtb" >/dev/null
python3 -I - "$OUT" "$SEED" <<'PY'
import hashlib, sys
d, seed = sys.argv[1], sys.argv[2].encode()
def chain(n, tag):
    out, s = bytearray(), hashlib.sha256(seed + tag).digest()
    while len(out) < n:
        out += s
        s = hashlib.sha256(s).digest()
    return bytes(out[:n])
open(d + "/res-page.bin", "wb").write(chain(4096, b"page"))
open(d + "/res-odd.bin", "wb").write(chain(3001, b"odd"))
PY
: > "$TMPDIR/empty.bin"
bash "$MK" --resource "$TMPDIR/empty.bin" --board "$TOKEN" "$TMPDIR/Image" "$TMPDIR/k.dtb" "$OUT/template.img" >/dev/null
for r in rsce page odd; do
	bash "$MK" --resource "$OUT/res-$r.bin" --board "$TOKEN" "$TMPDIR/Image" "$TMPDIR/k.dtb" "$OUT/expected-$r.img" >/dev/null
done
rm -rf "$TMPDIR"
