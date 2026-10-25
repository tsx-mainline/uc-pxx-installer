#!/bin/bash
# SPDX-License-Identifier: GPL-2.0-or-later
# Build the installer RAM image of the UC-P10: a boot image for
# `fastboot boot`, never for a partition.
#
#   image/build.sh --kernel DIR --linux DIR [--resource FILE] [--out DIR] [--jobs N]
#
#   --kernel    a git checkout of the uc-pxx kernel (branch uc-pxx-lts) at the
#               revision that --linux pins in kernel/KERNEL_REV.lts
#   --linux     a checkout of uc-pxx-linux. The build uses its
#               kernel/mkimage.sh and bootloader/shim.S unchanged
#   --resource  optional: a Rockchip resource for the "second" part of the
#               boot image (kernel/mkimage.sh --resource). Default: none, the
#               second is empty. U-Boot does not use the second on
#               `fastboot boot` (tested on unit 2, 2026-10-25: no second, our
#               own resource and the stock resource boot the same way), so the
#               RAM image needs no vendor file. The image is for `fastboot boot`
#               only: a boot_a image must carry the stock resource
#   --out       output folder (default out/image). The kernel build folder
#               out/image/kbuild stays, so the next build is incremental
#
# Steps:
#   1. p10inst: a static linux/arm64 Go build (docker, GO_IMAGE)
#   2. the initramfs (image/mkinitramfs.sh in an aarch64 Alpine container,
#      ALPINE_IMAGE, qemu-user binfmt), then ci/check-no-writer.py on it
#   3. the kernel: defconfig + uc-pxx.config + image/installer.config, with
#      the initramfs built in. Only Image and the device tree, no modules
#   4. the device tree: the command line puts the console on the screen
#      (fbcon=map:1 removed). The check: the reboot-mode node has mode-normal,
#      so a plain restart of the image does not come back into fastboot
#   5. kernel/mkimage.sh with a new random token p10inst-XXXXXX
# Outputs in --out: p10inst-ucp10.img, p10inst-arm64, initramfs.cpio, Image,
# rk3399-crestron-ucp10.dtb, token.txt, build-info.txt, sha256sums.txt.
#
# Host tools: docker, aarch64-linux-gnu-gcc, make, bc, bison, flex,
# libssl-dev, mkbootimg, fdtget, fdtput, python3, git.
# Env: GO_IMAGE (default golang:1.27.2-alpine), ALPINE_IMAGE (default
# arm64v8/alpine:3.24), CROSS_COMPILE (default aarch64-linux-gnu-).
set -euo pipefail
HERE=$(cd "$(dirname "$0")/.." && pwd)
KSRC= LINUX= RES= OUT=$HERE/out/image JOBS=$(nproc)
usage() { sed -n '5,36p' "$0" >&2; exit 2; }
while [ $# -gt 0 ]; do
	case $1 in
	--kernel) KSRC=${2:?}; shift;;
	--linux) LINUX=${2:?}; shift;;
	--resource) RES=${2:?}; shift;;
	--out) OUT=${2:?}; shift;;
	--jobs) JOBS=${2:?}; shift;;
	*) usage;;
	esac
	shift
done
[ -n "$KSRC" ] && [ -n "$LINUX" ] || usage
KSRC=$(cd "$KSRC" && pwd) LINUX=$(cd "$LINUX" && pwd)
[ -z "$RES" ] || RES=$(readlink -f "$RES")
mkdir -p "$OUT"; OUT=$(cd "$OUT" && pwd)
GO_IMAGE=${GO_IMAGE:-golang:1.27.2-alpine}
ALPINE_IMAGE=${ALPINE_IMAGE:-arm64v8/alpine:3.24}
CROSS=${CROSS_COMPILE:-aarch64-linux-gnu-}
T0=$(date +%s)
step() { echo "== $* ($(( $(date +%s) - T0 )) s)"; }

REV=$(grep -v '^#' "$LINUX/kernel/KERNEL_REV.lts" | head -n 1 | tr -d ' \n')
HEAD=$(git -C "$KSRC" rev-parse HEAD)
[ "$HEAD" = "$REV" ] || { echo "build: $KSRC is at $HEAD, uc-pxx-linux pins $REV" >&2; exit 1; }
[ -f "$KSRC/arch/arm64/configs/uc-pxx.config" ] || { echo "build: $KSRC has no uc-pxx.config" >&2; exit 1; }

step "1. p10inst (linux/arm64, static)"
mkdir -p "$HOME/.cache/p10inst-go"
docker run --rm -u "$(id -u):$(id -g)" -v "$HERE:/src:ro" -v "$OUT:/o" \
	-v "$HOME/.cache/p10inst-go:/cache" -e HOME=/tmp -e GOCACHE=/cache/build \
	-e GOMODCACHE=/cache/mod -e CGO_ENABLED=0 -e GOOS=linux -e GOARCH=arm64 -e GOFLAGS=-buildvcs=false \
	-w /src "$GO_IMAGE" go build -trimpath -ldflags "-s -w" -o /o/p10inst-arm64 ./cmd/p10inst
file "$OUT/p10inst-arm64" 2>/dev/null || true

step "2. initramfs"
[ "$(docker image inspect -f '{{.Architecture}}' "$ALPINE_IMAGE" 2>/dev/null)" = arm64 ] ||
	{ echo "build: $ALPINE_IMAGE is not an arm64 image" >&2; exit 1; }
docker run --rm --platform linux/arm64 -v "$HERE:/w:ro" -v "$OUT:/o" -e UIDGID="$(id -u):$(id -g)" \
	"$ALPINE_IMAGE" /w/image/mkinitramfs.sh /o/p10inst-arm64 /o/initramfs.cpio
python3 -I "$HERE/ci/check-no-writer.py" "$OUT/initramfs.cpio"

step "3. kernel $REV"
KB=$OUT/kbuild
mkdir -p "$KB"
M="make -s -C $KSRC O=$KB ARCH=arm64 CROSS_COMPILE=$CROSS KBUILD_BUILD_USER=build KBUILD_BUILD_HOST=uc-pxx-build"
$M defconfig uc-pxx.config
FRAG=$HERE/image/installer.config
while IFS= read -r l; do
	case $l in
	"# CONFIG_"*" is not set") s=${l#"# CONFIG_"}; "$KSRC/scripts/config" --file "$KB/.config" -k --disable "${s%" is not set"}";;
	CONFIG_*=y) s=${l#CONFIG_}; "$KSRC/scripts/config" --file "$KB/.config" -k --enable "${s%=y}";;
	esac
done < "$FRAG"
"$KSRC/scripts/config" --file "$KB/.config" --set-str INITRAMFS_SOURCE "$OUT/initramfs.cpio" \
	-e RD_XZ -e INITRAMFS_COMPRESSION_XZ
$M olddefconfig
# Kconfig may override a fragment line (select, depends). Check each one.
while IFS= read -r l; do
	case $l in
	"# CONFIG_"*" is not set") s=${l#"# "}; s=${s%" is not set"}
		! grep -q "^$s=" "$KB/.config" || { echo "build: $s is still set" >&2; exit 1; };;
	CONFIG_*=y) grep -qx "$l" "$KB/.config" || { echo "build: $l did not stick" >&2; exit 1; };;
	esac
done < "$FRAG"
$M -j"$JOBS" Image rockchip/rk3399-crestron-ucp10.dtb
KREL=$(cat "$KB/include/config/kernel.release")
cp "$KB/arch/arm64/boot/Image" "$OUT/Image"

step "4. device tree"
DTB=$OUT/rk3399-crestron-ucp10.dtb
cp "$KB/arch/arm64/boot/dts/rockchip/rk3399-crestron-ucp10.dtb" "$DTB"
ARGS=$(fdtget "$DTB" /chosen bootargs)
NEW=
for w in $ARGS; do
	case $w in fbcon=map:*) continue;; esac
	NEW="$NEW $w"
done
NEW="${NEW# } p10inst.ram=1"
fdtput -t s "$DTB" /chosen bootargs "$NEW"
echo "bootargs: $(fdtget "$DTB" /chosen bootargs)"
"$KB/scripts/dtc/dtc" -q -I dtb -O dts "$DTB" | grep -A8 'syscon-reboot-mode' | grep -q 'mode-normal = <0x5242c300>' ||
	{ echo "build: the device tree has no reboot-mode with mode-normal: a restart would loop into fastboot" >&2; exit 1; }
echo "reboot-mode: mode-normal = BOOT_NORMAL"

step "5. boot image"
TOKEN=p10inst-$(od -An -N3 -tx1 /dev/urandom | tr -d ' \n')
echo "$TOKEN" > "$OUT/token.txt"
SECOND=$RES
if [ -z "$SECOND" ]; then
	# no resource: mkimage.sh takes a file, an empty one gives a second of size 0
	SECOND=$OUT/second-empty
	: > "$SECOND"
fi
"$LINUX/kernel/mkimage.sh" --resource "$SECOND" --board "$TOKEN" "$OUT/Image" "$DTB" "$OUT/p10inst-ucp10.img"
# State the second of the result: size and sha256 from the v0 header.
SECINFO=$(python3 -I - "$OUT/p10inst-ucp10.img" <<'PY'
import hashlib, struct, sys
d = open(sys.argv[1], "rb").read()
k, _, r, _, s, _, _, p = struct.unpack_from("<8I", d, 8)
off = p * (1 + (k + p - 1) // p + (r + p - 1) // p)
print("none (size 0)" if s == 0 else "%d bytes, sha256 %s" % (s, hashlib.sha256(d[off:off + s]).hexdigest()))
PY
)
echo "second: $SECINFO"
SZ=$(stat -c %s "$OUT/p10inst-ucp10.img")
[ "$SZ" -le $((128 << 20)) ] || { echo "build: $SZ bytes, more than the fastboot limit of 128 MiB" >&2; exit 1; }
{
	echo "token $TOKEN"
	echo "kernel $REV ($KREL)"
	echo "uc-pxx-linux $(git -C "$LINUX" rev-parse HEAD 2>/dev/null || echo unknown)"
	echo "installer $(git -C "$HERE" rev-parse HEAD 2>/dev/null || echo 'no commit') $(cd "$HERE" && find cmd internal manifests image ci -type f | sort | xargs sha256sum | sha256sum | cut -c1-16)"
	echo "second $SECINFO"
	echo "image $SZ bytes"
} > "$OUT/build-info.txt"
(cd "$OUT" && sha256sum p10inst-ucp10.img p10inst-arm64 initramfs.cpio Image rk3399-crestron-ucp10.dtb > sha256sums.txt)
cat "$OUT/build-info.txt" "$OUT/sha256sums.txt"
step "done"
