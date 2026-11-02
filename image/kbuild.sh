#!/bin/bash
# SPDX-License-Identifier: GPL-2.0-or-later
# Step 3 of image/build.sh: the kernel, at fixed paths. image/build.sh starts
# it in a new user and mount namespace of its own:
#
#   unshare --user --map-root-user --mount --propagation private -- \
#       image/kbuild.sh KSRC OUT FRAG JOBS CROSS USER HOST TIMESTAMP
#
# In that namespace only, it mounts a small tmpfs on /mnt, binds KSRC
# read-only to /mnt/p10inst/linux and OUT to /mnt/p10inst/out, and builds
# there (O=/mnt/p10inst/out/kbuild, INITRAMFS_SOURCE
# /mnt/p10inst/out/initramfs.cpio). So the paths of the build host are not in
# the Image: the kernel keeps the INITRAMFS_SOURCE path in its config
# (ikconfig), and the build-id note hashes the debug information, which
# holds the build paths. The mounts end with the namespace. Nothing changes
# on the host but OUT/kbuild.
#
# KBUILD_BUILD_USER, KBUILD_BUILD_HOST and KBUILD_BUILD_TIMESTAMP come from
# the arguments (image/build.sh takes them from the inputs), so the version
# string of the kernel does not depend on the build host or the time.
set -euo pipefail
[ $# = 8 ] || { echo "kbuild.sh: 8 arguments expected" >&2; exit 2; }
KSRC=$1 OUT=$2 FRAG=$3 JOBS=$4 CROSS=$5 KUSER=$6 KHOST=$7 KTIME=$8
# Only in a user namespace of its own (uid 0 there, the user outside), never as
# the real root: the tmpfs on /mnt must not reach the host.
read -r u0 u1 un < /proc/self/uid_map
[ "$(id -u)" = 0 ] && [ "$u0" = 0 ] && [ "$u1" != 0 ] && [ "$un" = 1 ] ||
	{ echo "kbuild.sh: run me in a user namespace of my own (image/build.sh does)" >&2; exit 2; }
F=/mnt/p10inst
mount -t tmpfs -o mode=0755,size=1m p10inst-build /mnt
mkdir -p $F/linux $F/out
mount --bind "$KSRC" $F/linux
mount -o remount,bind,ro $F/linux
mount --bind "$OUT" $F/out
cd $F/out

K=$F/linux KB=$F/out/kbuild
mkdir -p "$KB"
M=(make -s -C "$K" O="$KB" ARCH=arm64 CROSS_COMPILE="$CROSS" KBUILD_BUILD_USER="$KUSER"
	KBUILD_BUILD_HOST="$KHOST" KBUILD_BUILD_TIMESTAMP="$KTIME")
"${M[@]}" defconfig uc-pxx.config
while IFS= read -r l; do
	case $l in
	"# CONFIG_"*" is not set") s=${l#"# CONFIG_"}; "$K/scripts/config" --file "$KB/.config" -k --disable "${s%" is not set"}";;
	CONFIG_*=y) s=${l#CONFIG_}; "$K/scripts/config" --file "$KB/.config" -k --enable "${s%=y}";;
	esac
done < "$FRAG"
"$K/scripts/config" --file "$KB/.config" --set-str INITRAMFS_SOURCE "$F/out/initramfs.cpio" \
	-e RD_XZ -e INITRAMFS_COMPRESSION_XZ
"${M[@]}" olddefconfig
# Kconfig may override a fragment line (select, depends). Check each one.
while IFS= read -r l; do
	case $l in
	"# CONFIG_"*" is not set") s=${l#"# "}; s=${s%" is not set"}
		! grep -q "^$s=" "$KB/.config" || { echo "build: $s is still set" >&2; exit 1; };;
	CONFIG_*=y) grep -qx "$l" "$KB/.config" || { echo "build: $l did not stick" >&2; exit 1; };;
	esac
done < "$FRAG"
"${M[@]}" -j"$JOBS" Image rockchip/rk3399-crestron-ucp10.dtb rockchip/rk3399-crestron-ucp8.dtb
