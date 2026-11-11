#!/bin/bash
# modstep.sh KSRC OUT CROSS USER HOST TIMESTAMP -- step 2 of modbuild.sh, in a user +
# mount namespace of its own, with the SAME mounts as image/kbuild.sh
# (/mnt/p10inst/linux = KSRC read-only, /mnt/p10inst/out = OUT). Builds the external
# module /mnt/p10inst/out/modsrc/drivers/tee/optee_legacy against the kbuild output
# /mnt/p10inst/out/kbuild that the unmodified image/kbuild.sh just made. modpost
# resolves every import against vmlinux.symvers of that kernel (copied to
# Module.symvers: the tree has no in-tree modules built; the module needs none).
set -euo pipefail
[ $# = 6 ] || { echo "modstep.sh: 6 arguments" >&2; exit 2; }
KSRC=$1 OUT=$2 CROSS=$3 KUSER=$4 KHOST=$5 KTIME=$6
read -r u0 u1 un < /proc/self/uid_map
[ "$(id -u)" = 0 ] && [ "$u0" = 0 ] && [ "$u1" != 0 ] && [ "$un" = 1 ] || { echo "modstep.sh: own user ns only" >&2; exit 2; }
F=/mnt/p10inst
mount -t tmpfs -o mode=0755,size=1m p10inst-build /mnt
mkdir -p $F/linux $F/out
mount --bind "$KSRC" $F/linux
mount -o remount,bind,ro $F/linux
mount --bind "$OUT" $F/out
cd $F/out
K=$F/linux KB=$F/out/kbuild MD=$F/out/modsrc/drivers/tee/optee_legacy
[ -f "$KB/vmlinux.symvers" ] || { echo "modstep.sh: no vmlinux.symvers" >&2; exit 1; }
cp "$KB/vmlinux.symvers" "$KB/Module.symvers"
M=(make -s -C "$K" O="$KB" ARCH=arm64 CROSS_COMPILE="$CROSS" KBUILD_BUILD_USER="$KUSER"
	KBUILD_BUILD_HOST="$KHOST" KBUILD_BUILD_TIMESTAMP="$KTIME")
# scripts/module.lds and the other module-build files (Image needs none of them)
"${M[@]}" modules_prepare
make -s -C "$K" O="$KB" ARCH=arm64 CROSS_COMPILE="$CROSS" KBUILD_BUILD_USER="$KUSER" \
	KBUILD_BUILD_HOST="$KHOST" KBUILD_BUILD_TIMESTAMP="$KTIME" M="$MD" CONFIG_OPTEE_LEGACY=m -j20 modules
ls -l "$MD/optee_legacy.ko"
