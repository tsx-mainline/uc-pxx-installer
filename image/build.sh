#!/bin/bash
# SPDX-License-Identifier: GPL-2.0-or-later
# Build the installer RAM image of the UC-P10: a boot image for
# `fastboot boot`, never for a partition.
#
#   image/build.sh --kernel DIR --linux DIR [--inputs FILE] [--apk-cache DIR]
#                  [--resource FILE] [--out DIR] [--jobs N]
#                  [--install --bootctl FILE] [--board ucp10|ucp8|both]
#
#   --kernel    a git checkout of the uc-pxx kernel (branch uc-pxx-lts) at the
#               revision that --linux pins in kernel/KERNEL_REV.lts. That pin
#               must be REVIEWED_KERNEL_REV (below): the reviewed public kernel
#   --linux     a checkout of uc-pxx-linux. The build uses its
#               kernel/mkimage.sh and bootloader/shim.S unchanged
#   --inputs    the inputs file of a reproducible build (format below): the
#               token, the build time, the kernel user and host, and the hash
#               of every other input. The build checks each hash and stops at
#               the first one that differs. --out must be new or empty.
#               Without --inputs it is a dev build: a new random token, the
#               time of the installer commit, and the hashes as found. The
#               outputs say "dev build"
#   --apk-cache the local copy of the Alpine files of image/apk.lock
#               (default ~/.cache/p10inst-apk). Fill it with
#               image/apk-cache.sh fetch --cache DIR. The build never fetches
#   --resource  optional: a Rockchip resource for the "second" part of the
#               boot image (kernel/mkimage.sh --resource). Default: none, the
#               second is empty. U-Boot does not use the second on
#               `fastboot boot` (tested on unit 2, 2026-10-25: no second, our
#               own resource and the stock resource boot the same way), so the
#               RAM image needs no vendor file. The image is for `fastboot boot`
#               only: a boot_a image must carry the stock resource
#   --out       output folder (default out/image). In a dev build the kernel
#               build folder out/image/kbuild stays, so the next build is
#               incremental
#   --install   the INSTALL image (INSTALLER-END-USER-PLAN.md section 1), not
#               the status image. Its differences, and nothing else:
#               - /usr/bin/p10inst is the release install build (go build -tags
#                 install), checked with ci/check-no-writer.py --release-install
#               - the one A/B tool --bootctl at /usr/libexec/p10inst/ (the path
#                 of RULES.md), owner root, mode 0700
#               - /etc/inittab and /etc/profile from image/overlay-install: the
#                 install flow on tty1 (p10inst flow --leds; the status image
#                 keeps p10inst ui --leds) and the banner of the install image
#               - the cpio is checked with ci/check-no-writer.py --install-image
#                 (the status check refuses an A/B tool)
#               - the outputs are p10inst-install-ucp10.img and
#                 p10inst-install-arm64, so the two images are never confused
#               - the inputs file has the FORMAT p10inst-install-image-inputs-1
#                 and one more key, BOOTCTL_INST_SHA256. A status inputs file
#                 does not build an install image, and the other way round
#               - after the build: the checks of the plan's section 1.3 (header
#                 v0, no second, the token, the device-tree command line, the
#                 Image and the initramfs in the boot image = the checked ones)
#               --resource is refused: the install image has no resource
#   --board     the model of the image (UC-P8.md section 3): ucp10 (the
#               default: the outputs, the inputs file and every byte as
#               before), ucp8 (p10inst-ucp8.img / p10inst-install-ucp8.img
#               with rk3399-crestron-ucp8.dtb), or both: the two images from
#               this one kernel Image and this one initramfs, with two tokens
#               (TOKEN for the UC-P10 image, TOKEN_UCP8 for the UC-P8 image),
#               and a check that the two images differ only in the device tree
#               of the shim and the token (the header name and id), and the
#               two device trees only in the model properties
#               (internal/device Models: model, compatible, the panel, the
#               backlight table, the touch keys, the sound card name) and the
#               token. The initramfs chooses the console font at boot from the
#               device tree (image/overlay/etc/init.d/rcS)
#   --bootctl   with --install: uc-pxx-bootctl-inst, the reviewed static
#               aarch64 binary, sha256 20aa6bc5d2146faace461eec0b50f741c681b3c58827788b9b806d9444b00203
#               (uc-pxx-linux bootctl-repair: 5d83215b + repair, switch;
#               NOT REVIEWED YET, DEVMODE-R4.md). Any other file stops the build
#
# Steps:
#   1. p10inst: a static linux/arm64 Go build (docker, GO_IMAGE, no network)
#   2. the initramfs (image/mkinitramfs.sh in an aarch64 Alpine container,
#      ALPINE_IMAGE, qemu-user binfmt, no network; the packages come from a
#      checked copy of the apk cache), then ci/check-no-writer.py on it
#   3. the kernel (image/kbuild.sh, in a user and mount namespace with fixed
#      paths): defconfig + uc-pxx.config + image/installer.config, with the
#      initramfs built in. Only Image and the device tree, no modules
#   4. the device tree: the command line puts the console on the screen
#      (fbcon=map:1 removed). The check: the reboot-mode node has mode-normal,
#      so a plain restart of the image does not come back into fastboot
#   5. kernel/mkimage.sh with the token of the inputs (dev build: a new random
#      token p10inst-XXXXXX)
# Outputs in --out: p10inst-ucp10.img, p10inst-arm64, initramfs.cpio, Image,
# rk3399-crestron-ucp10.dtb, token.txt, inputs.txt (the inputs of this build,
# in the format below), build-info.txt, sha256sums.txt. With --install the
# image and the binary are p10inst-install-ucp10.img and p10inst-install-arm64.
#
# Reproducible: the same inputs file, kernel, uc-pxx-linux files and apk
# cache give the same bytes, on any path, as any user, at any time. The
# inputs file has one KEY=VALUE per line, "#" lines are comments, each key
# once:
#   FORMAT                  p10inst-image-inputs-1
#   KIND                    release, or dev (the record of a dev build)
#   TOKEN                   p10inst- and 6 lowercase hex digits
#   SOURCE_DATE_EPOCH       seconds: every file time in the initramfs, the
#                           apk.log time and the shadow day
#   KBUILD_BUILD_TIMESTAMP  the time in the kernel version string
#   KBUILD_BUILD_USER, KBUILD_BUILD_HOST   the user@host of the version string
#   INSTALLER_COMMIT        this repository: HEAD, with a clean tree
#   APK_LOCK_SHA256         sha256 of image/apk.lock
#   KERNEL_REV              HEAD of --kernel (clean), = kernel/KERNEL_REV.lts
#                           = REVIEWED_KERNEL_REV
#   MKIMAGE_SHA256          kernel/mkimage.sh of --linux
#   SHIM_SHA256             bootloader/shim.S of --linux
#   RESOURCE_SHA256         none, or the sha256 of --resource
#   GO_IMAGE_ID, ALPINE_IMAGE_ID   docker image ids (sha256:...)
#   CROSS_CC_VERSION, CROSS_LD_VERSION, XZ_VERSION, DTC_VERSION
#                           the first line of --version (ld: -v; dtc: fdtput)
#   MKBOOTIMG_SHA256        sha256 of the mkbootimg program
# The install image (--install): FORMAT is p10inst-install-image-inputs-1, and
# one more key:
#   BOOTCTL_INST_SHA256     sha256 of --bootctl (always the RULES.md binary)
# Two keys only for --board ucp8 or both (a UC-P10 inputs file has neither):
#   BOARD                   ucp8 or both (= --board; absent = ucp10)
#   TOKEN_UCP8              with BOARD=both: the token of the UC-P8 image
#
# Host tools: docker, unshare (util-linux; user namespaces must be allowed),
# aarch64-linux-gnu-gcc, make, bc, bison, flex, libssl-dev, mkbootimg,
# fdtget, fdtput, xz, python3, git.
# Env: GO_IMAGE (default golang:1.27.2-alpine), ALPINE_IMAGE (default
# arm64v8/alpine:3.24), CROSS_COMPILE (default aarch64-linux-gnu-).
set -euo pipefail
export LC_ALL=C TZ=UTC
umask 022
HERE=$(cd "$(dirname "$0")/.." && pwd)
KSRC= LINUX= RES= OUT=$HERE/out/image JOBS=$(nproc) INPUTS= APKCACHE=$HOME/.cache/p10inst-apk
INSTALL=0 BOOTCTL= BOARDSEL=ucp10
usage() { sed -n '5,7p' "$0" >&2; exit 2; }
while [ $# -gt 0 ]; do
	case $1 in
	--kernel) KSRC=${2:?}; shift;;
	--linux) LINUX=${2:?}; shift;;
	--inputs) INPUTS=${2:?}; shift;;
	--apk-cache) APKCACHE=${2:?}; shift;;
	--resource) RES=${2:?}; shift;;
	--out) OUT=${2:?}; shift;;
	--jobs) JOBS=${2:?}; shift;;
	--install) INSTALL=1;;
	--bootctl) BOOTCTL=${2:?}; shift;;
	--board) BOARDSEL=${2:?}; shift;;
	*) usage;;
	esac
	shift
done
[ -n "$KSRC" ] && [ -n "$LINUX" ] || usage
die() { echo "build: $*" >&2; exit 1; }
case $BOARDSEL in ucp10|ucp8|both) ;; *) die "--board must be ucp10, ucp8 or both";; esac
KSRC=$(cd "$KSRC" && pwd) LINUX=$(cd "$LINUX" && pwd)
[ -z "$RES" ] || RES=$(readlink -f "$RES")
[ -z "$INPUTS" ] || INPUTS=$(readlink -f "$INPUTS")
# The install image (--install): the reviewed A/B tool of RULES.md, nothing else.
BOOTCTL_INST_SHA=20aa6bc5d2146faace461eec0b50f741c681b3c58827788b9b806d9444b00203
FMT=p10inst-image-inputs-1 IMG=p10inst-ucp10.img BIN=p10inst-arm64 GOTAGS=
if [ "$INSTALL" = 1 ]; then
	[ -n "$BOOTCTL" ] || die "--install needs --bootctl FILE"
	[ -z "$RES" ] || die "--install: the install image has no resource (no --resource)"
	BOOTCTL=$(readlink -f "$BOOTCTL")
	[ -f "$BOOTCTL" ] || die "--bootctl $BOOTCTL is not a file"
	FMT=p10inst-install-image-inputs-1 IMG=p10inst-install-ucp10.img BIN=p10inst-install-arm64 GOTAGS="-tags install"
else
	[ -z "$BOOTCTL" ] || die "--bootctl needs --install: the status image has no A/B tool"
fi
# the image of each board: the UC-P10 names as before, the UC-P8 ones with ucp8
IMG8=${IMG/ucp10/ucp8}
[ "$BOARDSEL" != ucp8 ] || IMG=$IMG8
if [ -n "$INPUTS" ] && [ -e "$OUT" ] && [ -n "$(ls -A "$OUT")" ]; then
	die "--out $OUT is not empty: a build with --inputs starts from nothing"
fi
mkdir -p "$OUT"; OUT=$(cd "$OUT" && pwd)
GO_IMAGE=${GO_IMAGE:-golang:1.27.2-alpine}
ALPINE_IMAGE=${ALPINE_IMAGE:-arm64v8/alpine:3.24}
CROSS=${CROSS_COMPILE:-aarch64-linux-gnu-}
# image/kbuild.sh puts a tmpfs on /mnt in its own namespace: nothing it needs may be there.
for d in "$KSRC" "$OUT" "$HERE"; do
	case $d/ in /mnt/*) die "$d is under /mnt, which image/kbuild.sh covers";; esac
done
T0=$(date +%s)
step() { echo "== $* ($(( $(date +%s) - T0 )) s)"; }

# ---- the inputs ---------------------------------------------------------
KEYS="FORMAT KIND TOKEN SOURCE_DATE_EPOCH KBUILD_BUILD_TIMESTAMP KBUILD_BUILD_USER KBUILD_BUILD_HOST
INSTALLER_COMMIT APK_LOCK_SHA256 KERNEL_REV MKIMAGE_SHA256 SHIM_SHA256 RESOURCE_SHA256 GO_IMAGE_ID
ALPINE_IMAGE_ID CROSS_CC_VERSION CROSS_LD_VERSION XZ_VERSION DTC_VERSION MKBOOTIMG_SHA256"
[ "$INSTALL" = 0 ] || KEYS="$KEYS BOOTCTL_INST_SHA256"
# BOARD and TOKEN_UCP8: only in the inputs of a UC-P8 image or of both images
OPTKEYS="BOARD TOKEN_UCP8"
declare -A IN=() GOT=()
if [ -n "$INPUTS" ]; then
	n=0
	while IFS= read -r l || [ -n "$l" ]; do
		n=$((n + 1))
		case $l in ''|'#'*) continue;; esac
		case $l in *[![:print:]]*) die "$INPUTS line $n: a byte that is not printable ASCII";; esac
		k=${l%%=*} v=${l#*=}
		[ "$k" != "$l" ] || die "$INPUTS line $n: not KEY=VALUE"
		case " $(echo $KEYS $OPTKEYS) " in *" $k "*) ;; *) die "$INPUTS line $n: unknown key $k";; esac
		[ -z "${IN[$k]+x}" ] || die "$INPUTS line $n: $k twice"
		IN[$k]=$v
	done < "$INPUTS"
	for k in $KEYS; do [ -n "${IN[$k]+x}" ] || die "$INPUTS has no $k"; done
	[ "${IN[FORMAT]}" = $FMT ] || die "$INPUTS: FORMAT is not $FMT"
	case ${IN[KIND]} in release|dev) ;; *) die "$INPUTS: KIND must be release or dev";; esac
	[ "${IN[BOARD]:-ucp10}" = "$BOARDSEL" ] || die "$INPUTS: BOARD is ${IN[BOARD]:-ucp10 (no BOARD key)}, --board is $BOARDSEL"
	[ "${IN[BOARD]-x}" != ucp10 ] || die "$INPUTS: a UC-P10 inputs file has no BOARD key"
	if [ "$BOARDSEL" = both ]; then
		[ -n "${IN[TOKEN_UCP8]+x}" ] || die "$INPUTS: BOARD=both needs TOKEN_UCP8"
	else
		[ -z "${IN[TOKEN_UCP8]+x}" ] || die "$INPUTS: TOKEN_UCP8 only with BOARD=both"
	fi
fi
# pin KEY VALUE: the value found here. With --inputs it must be the value of the file.
pin() {
	GOT[$1]=$2
	if [ -n "$INPUTS" ] && [ "${IN[$1]}" != "$2" ]; then
		die "$1 is '$2', the inputs file says '${IN[$1]}'"
	fi
}
# choose KEY DEFAULT: the inputs file gives it; a dev build takes DEFAULT.
choose() {
	if [ -n "$INPUTS" ]; then GOT[$1]=${IN[$1]}; else GOT[$1]=$2; fi
}

choose FORMAT $FMT
choose KIND dev
REV=$(grep -v '^#' "$LINUX/kernel/KERNEL_REV.lts" | head -n 1 | tr -d ' \n')
HEAD=$(git -C "$KSRC" rev-parse HEAD)
[ "$HEAD" = "$REV" ] || die "$KSRC is at $HEAD, uc-pxx-linux pins $REV"
# The kernel of every image is the reviewed public one: tsx-mainline/linux,
# branch uc-pxx-lts, pinned by uc-pxx-linux b41d86b and later. A stale or
# local uc-pxx-linux (another kernel/KERNEL_REV.lts) or a kernel checkout of a
# commit that is not public is refused, in a dev build too (DEVMODE-R4.md §15:
# an old uc-pxx-linux snapshot gave a local, unpublished kernel).
REVIEWED_KERNEL_REV=d02e92631f2810a4a43fb6f09824afb9b0044ee2
[ "$REV" = "$REVIEWED_KERNEL_REV" ] ||
	die "uc-pxx-linux pins the kernel $REV, not the reviewed public kernel $REVIEWED_KERNEL_REV (tsx-mainline/linux uc-pxx-lts): use uc-pxx-linux b41d86b or later"
[ -f "$KSRC/arch/arm64/configs/uc-pxx.config" ] || die "$KSRC has no uc-pxx.config"
pin KERNEL_REV "$HEAD"
if [ -n "$(git -C "$KSRC" status --porcelain)" ]; then
	[ -z "$INPUTS" ] || die "$KSRC has changes or untracked files"
	echo "build: warning: $KSRC has changes or untracked files (dev build)" >&2
fi
if ICOMMIT=$(git -C "$HERE" rev-parse HEAD 2>/dev/null); then
	[ -z "$(git -C "$HERE" status --porcelain)" ] || ICOMMIT=$ICOMMIT-dirty
else
	ICOMMIT=none
fi
pin INSTALLER_COMMIT "$ICOMMIT"
sha() { sha256sum < "$1" | cut -c1-64; }
pin APK_LOCK_SHA256 "$(sha "$HERE/image/apk.lock")"
pin MKIMAGE_SHA256 "$(sha "$LINUX/kernel/mkimage.sh")"
pin SHIM_SHA256 "$(sha "$LINUX/bootloader/shim.S")"
pin RESOURCE_SHA256 "$([ -z "$RES" ] && echo none || sha "$RES")"
GO_ID=$(docker image inspect -f '{{.Id}}' "$GO_IMAGE") || die "no docker image $GO_IMAGE"
ALPINE_ID=$(docker image inspect -f '{{.Id}}' "$ALPINE_IMAGE") || die "no docker image $ALPINE_IMAGE"
[ "$(docker image inspect -f '{{.Architecture}}' "$ALPINE_ID")" = arm64 ] || die "$ALPINE_IMAGE is not an arm64 image"
pin GO_IMAGE_ID "$GO_ID"
pin ALPINE_IMAGE_ID "$ALPINE_ID"
pin CROSS_CC_VERSION "$("${CROSS}gcc" --version | head -n 1)"
pin CROSS_LD_VERSION "$("${CROSS}ld" -v | head -n 1)"
pin XZ_VERSION "$(xz --version | head -n 1)"
pin DTC_VERSION "$(fdtput --version 2>&1 | head -n 1)"
pin MKBOOTIMG_SHA256 "$(sha "$(command -v mkbootimg)")"
if [ "$INSTALL" = 1 ]; then
	pin BOOTCTL_INST_SHA256 "$(sha "$BOOTCTL")"
	[ "${GOT[BOOTCTL_INST_SHA256]}" = $BOOTCTL_INST_SHA ] ||
		die "--bootctl $BOOTCTL has sha256 ${GOT[BOOTCTL_INST_SHA256]}, not the reviewed A/B tool $BOOTCTL_INST_SHA"
fi

# The token, the time, the kernel user and host.
choose TOKEN "p10inst-$(od -An -N3 -tx1 /dev/urandom | tr -d ' \n')"
TOKEN8=
if [ "$BOARDSEL" = both ]; then
	choose TOKEN_UCP8 "p10inst-$(od -An -N3 -tx1 /dev/urandom | tr -d ' \n')"
	TOKEN8=${GOT[TOKEN_UCP8]}
	[[ $TOKEN8 =~ ^p10inst-[0-9a-f]{6}$ ]] || die "TOKEN_UCP8 '$TOKEN8' is not p10inst- and 6 lowercase hex digits"
	[ "$TOKEN8" != "${GOT[TOKEN]}" ] || die "TOKEN_UCP8 is TOKEN: each image needs its own token"
fi
[ "$BOARDSEL" = ucp10 ] || GOT[BOARD]=$BOARDSEL
SDE0=$(git -C "$HERE" log -1 --format=%ct 2>/dev/null || date +%s)
choose SOURCE_DATE_EPOCH "$SDE0"
choose KBUILD_BUILD_TIMESTAMP "$(date -u -d "@${GOT[SOURCE_DATE_EPOCH]}" 2>/dev/null || echo bad)"
choose KBUILD_BUILD_USER build
choose KBUILD_BUILD_HOST uc-pxx-build
TOKEN=${GOT[TOKEN]} SDE=${GOT[SOURCE_DATE_EPOCH]} KTIME=${GOT[KBUILD_BUILD_TIMESTAMP]}
KUSER=${GOT[KBUILD_BUILD_USER]} KHOST=${GOT[KBUILD_BUILD_HOST]}
[[ $TOKEN =~ ^p10inst-[0-9a-f]{6}$ ]] || die "TOKEN '$TOKEN' is not p10inst- and 6 lowercase hex digits"
[[ $SDE =~ ^[1-9][0-9]{0,10}$ ]] && [ "$SDE" -lt 253402300800 ] || die "SOURCE_DATE_EPOCH '$SDE' is not a time in seconds"
[[ $KTIME =~ ^[A-Za-z0-9\ :+.,-]{1,40}$ ]] || die "KBUILD_BUILD_TIMESTAMP '$KTIME': only letters, digits, space and : + . , - (1 to 40)"
[[ $KUSER =~ ^[A-Za-z0-9._-]{1,32}$ ]] || die "KBUILD_BUILD_USER '$KUSER': only letters, digits, . _ -"
[[ $KHOST =~ ^[A-Za-z0-9._-]{1,32}$ ]] || die "KBUILD_BUILD_HOST '$KHOST': only letters, digits, . _ -"
{
	echo "# The inputs of a build of the installer RAM image (image/build.sh$([ "$INSTALL" = 0 ] || echo " --install"))."
	[ "${GOT[KIND]}" = release ] || echo "# DEV BUILD: not a release. The token was chosen at random."
	for k in $KEYS $OPTKEYS; do [ -z "${GOT[$k]+x}" ] || echo "$k=${GOT[$k]}"; done
} > "$OUT/inputs.txt"
if [ -n "$INPUTS" ]; then
	echo "inputs: $INPUTS (sha256 $(sha "$INPUTS")), KIND ${GOT[KIND]}, all pins match"
else
	echo "inputs: DEV BUILD (no --inputs): random token $TOKEN, SOURCE_DATE_EPOCH $SDE (installer commit time)"
fi

# The Alpine files: checked against the lock, then a copy of exactly those.
APKREPO=$OUT/apk-repo
rm -rf "$APKREPO"
"$HERE/image/apk-cache.sh" verify --cache "$APKCACHE" --lock "$HERE/image/apk.lock" --stage "$APKREPO"

step "1. p10inst (linux/arm64, static$([ "$INSTALL" = 0 ] || echo ", the release install build"))"
mkdir -p "$HOME/.cache/p10inst-go"
docker run --rm --network none -u "$(id -u):$(id -g)" -v "$HERE:/src:ro" -v "$OUT:/o" \
	-v "$HOME/.cache/p10inst-go:/cache" -e HOME=/tmp -e GOCACHE=/cache/build \
	-e GOMODCACHE=/cache/mod -e CGO_ENABLED=0 -e GOOS=linux -e GOARCH=arm64 -e GOFLAGS=-buildvcs=false \
	-e GOTOOLCHAIN=local -w /src "$GO_ID" go build $GOTAGS -trimpath -ldflags "-s -w" -o /o/$BIN ./cmd/p10inst
file "$OUT/$BIN" 2>/dev/null || true
[ "$INSTALL" = 0 ] || python3 -I "$HERE/ci/check-no-writer.py" --release-install "$OUT/$BIN"

step "2. initramfs"
TOOLARG=
if [ "$INSTALL" = 1 ]; then
	# a private copy for the container, checked again there; removed after the step
	cp "$BOOTCTL" "$OUT/bootctl-inst.bin"
	[ "$(sha "$OUT/bootctl-inst.bin")" = $BOOTCTL_INST_SHA ] || die "the copy of --bootctl differs"
	TOOLARG=/o/bootctl-inst.bin
fi
docker run --rm --network none --platform linux/arm64 -v "$HERE:/w:ro" -v "$OUT:/o" \
	-v "$APKREPO:/apk:ro" -e MIRROR=/apk -e SOURCE_DATE_EPOCH="$SDE" -e UIDGID="$(id -u):$(id -g)" \
	"$ALPINE_ID" /w/image/mkinitramfs.sh /o/$BIN /o/initramfs.cpio $TOOLARG
rm -rf "$APKREPO"
rm -f "$OUT/bootctl-inst.bin"
if [ "$INSTALL" = 1 ]; then
	python3 -I "$HERE/ci/check-no-writer.py" --install-image "$OUT/initramfs.cpio"
else
	python3 -I "$HERE/ci/check-no-writer.py" "$OUT/initramfs.cpio"
fi
# The cpio as mkinitramfs.sh makes it: sorted names, one time, owner 0:0,
# inode numbers 0..N-1, no device numbers (the device nodes keep their rdev).
python3 -I - "$OUT/initramfs.cpio" "$SDE" <<'PY'
import sys
d = open(sys.argv[1], "rb").read()
sde = int(sys.argv[2])
off, names, inos = 0, [], set()
while True:
    h = d[off:off + 110]
    if h[:6] != b"070701":
        sys.exit("build: cpio: no newc header at %d" % off)
    f = [int(h[6 + 8 * i:14 + 8 * i], 16) for i in range(13)]
    ino, mode, uid, gid, nlink, mtime, size, dmaj, dmin, rmaj, rmin, nsz, chk = f
    name = d[off + 110:off + 110 + nsz - 1]
    off = (off + 110 + nsz + 3) & ~3
    off = (off + size + 3) & ~3
    if name == b"TRAILER!!!":
        break
    if (uid, gid, mtime, dmaj, dmin) != (0, 0, sde, 0, 0):
        sys.exit("build: cpio: %r has uid %d gid %d mtime %d dev %d:%d" % (name, uid, gid, mtime, dmaj, dmin))
    names.append(name)
    inos.add(ino)
if names != sorted(names) or len(set(names)) != len(names):
    sys.exit("build: cpio: the names are not sorted and unique")
if inos != set(range(len(inos))):
    sys.exit("build: cpio: the inode numbers are not 0..N-1")
print("cpio: %d entries, sorted, mtime %d, owner 0:0, inodes 0..%d, no device numbers" % (len(names), sde, len(inos) - 1))
PY

step "3. kernel $REV"
KB=$OUT/kbuild
mkdir -p "$KB"
unshare --user --map-root-user --mount --propagation private -- "$HERE/image/kbuild.sh" \
	"$KSRC" "$OUT" "$HERE/image/installer.config" "$JOBS" "$CROSS" "$KUSER" "$KHOST" "$KTIME"
KREL=$(cat "$KB/include/config/kernel.release")
cp "$KB/arch/arm64/boot/Image" "$OUT/Image"

# board_image BOARD TOKEN IMG: steps 4 and 5 for one board. The Image and the
# initramfs are the same for every board; only the device tree of the board
# (and so the token in its command line and in the header) differs.
board_image() {
	local BOARD=$1 TOKEN=$2 IMG=$3
	step "4. device tree ($BOARD)"
	DTB=$OUT/rk3399-crestron-$BOARD.dtb
	cp "$KB/arch/arm64/boot/dts/rockchip/rk3399-crestron-$BOARD.dtb" "$DTB"
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
		die "the device tree has no reboot-mode with mode-normal: a restart would loop into fastboot"
	echo "reboot-mode: mode-normal = BOOT_NORMAL"

	step "5. boot image ($BOARD)"
	SECOND=$RES
	if [ -z "$SECOND" ]; then
		# no resource: mkimage.sh takes a file, an empty one gives a second of size 0
		SECOND=$OUT/second-empty
		: > "$SECOND"
	fi
	"$LINUX/kernel/mkimage.sh" --resource "$SECOND" --board "$TOKEN" "$OUT/Image" "$DTB" "$OUT/$IMG"
	# State the second of the result: size and sha256 from the v0 header.
	SECINFO=$(python3 -I - "$OUT/$IMG" <<'PY'
import hashlib, struct, sys
d = open(sys.argv[1], "rb").read()
k, _, r, _, s, _, _, p = struct.unpack_from("<8I", d, 8)
off = p * (1 + (k + p - 1) // p + (r + p - 1) // p)
print("none (size 0)" if s == 0 else "%d bytes, sha256 %s" % (s, hashlib.sha256(d[off:off + s]).hexdigest()))
PY
	)
	echo "second: $SECINFO"
	SZ=$(stat -c %s "$OUT/$IMG")
	[ "$SZ" -le $((128 << 20)) ] || die "$SZ bytes, more than the fastboot limit of 128 MiB"
	if [ "$INSTALL" = 1 ]; then
		# The checks of INSTALLER-END-USER-PLAN.md section 1.3 on the boot image itself.
		SHIMDTB=$OUT/shim.dtb.tmp
		python3 -I - "$OUT/$IMG" "$OUT/Image" "$OUT/initramfs.cpio" "$TOKEN" "$SHIMDTB" <<'PY'
import lzma, struct, sys
img, image, cpio, token, dtbout = sys.argv[1:6]
d = open(img, "rb").read()
fail = lambda m: sys.exit("build: install image: " + m)
if d[:8] != b"ANDROID!":
    fail("no ANDROID! magic")
k, _, r, _, s, _, _, p, v = struct.unpack_from("<9I", d, 8)
if v != 0 or p != 2048:
    fail("header version %d page %d, want 0 and 2048" % (v, p))
if s != 0:
    fail("second_size %d, want 0 (no resource)" % s)
name = d[48:64].rstrip(b"\0").decode()
if name != token:
    fail("header name %r, want the token %r" % (name, token))
kern = d[p:p + k]
li = open(image, "rb").read()
stub = 0x200000
if kern[stub:] != li:
    fail("the kernel part is not the stub and the built Image")
# the device tree of the stub (the command line of the running kernel)
i = kern.find(b"\xd0\x0d\xfe\xed", 0, stub)
if i < 0:
    fail("no device tree in the stub")
n = struct.unpack_from(">I", kern, i + 4)[0]
open(dtbout, "wb").write(kern[i:i + n])
# the initramfs built into the Image: an xz stream that is the checked cpio
want = open(cpio, "rb").read()
found, pos = 0, li.find(b"\xfd7zXZ\x00")
while pos >= 0:
    try:
        if lzma.LZMADecompressor().decompress(li[pos:], max_length=len(want) + 1) == want:
            found += 1
    except lzma.LZMAError:
        pass
    pos = li.find(b"\xfd7zXZ\x00", pos + 1)
if found != 1:
    fail("%d xz streams in the Image are the checked initramfs.cpio, want 1" % found)
print("install image: header v0, page 2048, second 0, name %s, kernel = stub + Image, the initramfs in the Image = initramfs.cpio" % token)
PY
		BA=$(fdtget "$SHIMDTB" /chosen bootargs)
		rm -f "$SHIMDTB"
		# The rule of the device mode's RAM image check (internal/device, CheckRAMImage), and more.
		case $BA in *[!\ -~]*) die "install image: the command line has a byte that is not printable ASCII";; esac
		case $BA in *\"*) die "install image: the command line has a quote";; esac
		nram=0 nid=0
		for w in $BA; do
			case $w in
			p10inst.ram=1) nram=$((nram + 1));;
			uc-pxx.id=*) [ "$w" = "uc-pxx.id=$TOKEN" ] || die "install image: the command line has $w, the token is $TOKEN"
				nid=$((nid + 1));;
			root=*|androidboot.*|skip_initramfs*|init=*) die "install image: the command line has $w";;
			esac
		done
		[ "$nram" = 1 ] && [ "$nid" = 1 ] || die "install image: p10inst.ram=1 $nram times, uc-pxx.id= $nid times, want once each"
		echo "install image: command line ok ($(echo $BA | wc -w) words, p10inst.ram=1 once, uc-pxx.id=$TOKEN once)"
	fi
}

# The boards: every image from this one Image and this one initramfs.
case $BOARDSEL in
ucp10|ucp8)
	board_image "$BOARDSEL" "$TOKEN" "$IMG"
	echo "$TOKEN" > "$OUT/token.txt"
	DTBS=rk3399-crestron-$BOARDSEL.dtb IMGS=$IMG
	;;
both)
	board_image ucp10 "$TOKEN" "$IMG"
	SECINFO10=$SECINFO SZ10=$SZ
	board_image ucp8 "$TOKEN8" "$IMG8"
	[ "$SECINFO" = "$SECINFO10" ] || die "the two images have another second ($SECINFO10, $SECINFO)"
	echo "$TOKEN" > "$OUT/token.txt"
	echo "$TOKEN8" > "$OUT/token-ucp8.txt"
	DTBS="rk3399-crestron-ucp10.dtb rk3399-crestron-ucp8.dtb" IMGS="$IMG $IMG8"
	step "6. the two images differ only in the device tree and the token"
	for b in ucp10 ucp8; do
		# the device tree that mkimage.sh puts into the shim: the board's, with the token word
		t=$TOKEN; [ $b = ucp10 ] || t=$TOKEN8
		cp "$OUT/rk3399-crestron-$b.dtb" "$OUT/shim-$b.dtb.tmp"
		fdtput -t s "$OUT/shim-$b.dtb.tmp" /chosen bootargs "$(fdtget "$OUT/rk3399-crestron-$b.dtb" /chosen bootargs) uc-pxx.id=$t"
	done
	python3 -I - "$OUT/$IMG" "$OUT/$IMG8" "$OUT/Image" "$OUT/shim-ucp10.dtb.tmp" "$OUT/shim-ucp8.dtb.tmp" "$TOKEN" "$TOKEN8" <<'PY'
import struct, sys
a, b, image, da, db, ta, tb = sys.argv[1:8]
A, B = open(a, "rb").read(), open(b, "rb").read()
fail = lambda m: sys.exit("build: the two images: " + m)
PAGE, NAME, ID, STUB, DTB_OFF = 2048, (48, 64), (576, 608), 0x200000, 0x10000
ha, hb = struct.unpack_from("<10I", A, 8), struct.unpack_from("<10I", B, 8)
if A[:8] != b"ANDROID!" or B[:8] != b"ANDROID!" or ha != hb:
    fail("the header fields differ: %s %s" % (ha, hb))
k, page = ha[0], ha[7]
if page != PAGE or len(A) != len(B):
    fail("page %d, sizes %d %d" % (page, len(A), len(B)))
def outside(x, ranges):
    out, last = b"", 0
    for lo, hi in ranges:
        out += x[last:lo]
        last = hi
    return out + x[last:]
# the header: equal but for the name (the token) and the id (a sha of the parts)
if outside(A[:PAGE], [NAME, ID]) != outside(B[:PAGE], [NAME, ID]):
    fail("the headers differ outside the name and the id")
if A[48:64].rstrip(b"\0").decode() != ta or B[48:64].rstrip(b"\0").decode() != tb:
    fail("the header names are not the tokens")
ka, kb = A[PAGE:PAGE + k], B[PAGE:PAGE + k]
li = open(image, "rb").read()
if ka[STUB:] != li or kb[STUB:] != li:
    fail("the Linux Image in a shim is not the built Image")
want = {}
for name, kern, dfile in (("UC-P10", ka, da), ("UC-P8", kb, db)):
    if kern[DTB_OFF:DTB_OFF + 4] != b"\xd0\x0d\xfe\xed":
        fail("no device tree at the shim offset in the %s image" % name)
    n = struct.unpack_from(">I", kern, DTB_OFF + 4)[0]
    if kern[DTB_OFF:DTB_OFF + n] != open(dfile, "rb").read():
        fail("the device tree in the %s shim is not the board's device tree with its token" % name)
    want[name] = n
n = max(want.values())
if ka[:DTB_OFF] != kb[:DTB_OFF] or ka[DTB_OFF + n:STUB] != kb[DTB_OFF + n:STUB]:
    fail("the shims differ outside the device tree")
if A[PAGE + k:] != B[PAGE + k:]:
    fail("the images differ after the kernel part")
# the two device trees: property by property
def props(d):
    off_struct, off_str = struct.unpack_from(">I", d, 8)[0], struct.unpack_from(">I", d, 12)[0]
    p, path, out = off_struct, [], {}
    while True:
        tok = struct.unpack_from(">I", d, p)[0]
        p += 4
        if tok == 1:
            e = d.index(b"\0", p)
            path.append(d[p:e].decode())
            p = (e + 4) & ~3
        elif tok == 2:
            path.pop()
        elif tok == 3:
            ln, no = struct.unpack_from(">II", d, p)
            name = d[off_str + no:d.index(b"\0", off_str + no)].decode()
            out["/".join(path) or "/", name] = d[p + 8:p + 8 + ln]
            p = (p + 8 + ln + 3) & ~3
        elif tok == 4:
            continue
        elif tok == 9:
            return out
        else:
            fail("a bad device tree token %d" % tok)
pa, pb = props(open(da, "rb").read()), props(open(db, "rb").read())
allowed = {("/", "model"), ("/", "compatible"), ("/chosen", "bootargs")}
diff = sorted(set(k for k in set(pa) | set(pb) if pa.get(k) != pb.get(k)))
bad = []
for path, prop in diff:
    leaf = path.rsplit("/", 1)[-1]
    ok = (path, prop) in allowed or (leaf == "panel@0" and prop == "compatible") or \
        (leaf == "backlight" and prop == "brightness-levels") or \
        (leaf.startswith("touchscreen@") and prop == "linux,keycodes") or (leaf == "sound" and prop == "label")
    if not ok:
        bad.append("%s:%s" % (path, prop))
if bad:
    fail("the device trees differ in more than the model properties: %s" % ", ".join(bad))
ba = pa["/chosen", "bootargs"].rstrip(b"\0").decode().split()
bb = pb["/chosen", "bootargs"].rstrip(b"\0").decode().split()
if [w for w in ba if not w.startswith("uc-pxx.id=")] != [w for w in bb if not w.startswith("uc-pxx.id=")]:
    fail("the command lines differ in more than the token")
print("two images: the same header but the name and the id, the same shim but the device tree, the same Image; "
      "the device trees differ only in %s" % ", ".join("%s:%s" % d for d in diff))
PY
	rm -f "$OUT"/shim-*.dtb.tmp
	;;
esac
{
	if [ "${GOT[KIND]}" = release ]; then
		echo "build release, inputs sha256 $(sha "$INPUTS")"
	elif [ -n "$INPUTS" ]; then
		echo "build dev (the inputs of a dev build, sha256 $(sha "$INPUTS")): not a release"
	else
		echo "build dev (no --inputs, random token): not a release"
	fi
	[ "$INSTALL" = 0 ] || echo "variant INSTALL image: release install p10inst, the A/B tool $BOOTCTL_INST_SHA at the RULES.md path, tty1 p10inst flow --leds"
	[ "$BOARDSEL" = ucp10 ] || echo "board $BOARDSEL (UC-P8.md section 3)"
	echo "token $TOKEN"
	[ "$BOARDSEL" != both ] || echo "token-ucp8 $TOKEN8"
	echo "time $SDE ($(date -u -d "@$SDE" +%FT%TZ)), kernel \"$KTIME\" $KUSER@$KHOST"
	echo "kernel $REV ($KREL)"
	echo "uc-pxx-linux $(git -C "$LINUX" rev-parse HEAD 2>/dev/null || echo unknown), mkimage.sh ${GOT[MKIMAGE_SHA256]}"
	echo "installer $ICOMMIT $(cd "$HERE" && find cmd internal manifests image ci -type f | sort | xargs sha256sum | sha256sum | cut -c1-16)"
	echo "second $SECINFO"
	if [ "$BOARDSEL" = both ]; then
		echo "image $SZ10 bytes ($IMG), $SZ bytes ($IMG8)"
	else
		echo "image $SZ bytes"
	fi
} > "$OUT/build-info.txt"
(cd "$OUT" && sha256sum $IMGS $BIN initramfs.cpio Image $DTBS inputs.txt > sha256sums.txt)
cat "$OUT/build-info.txt" "$OUT/sha256sums.txt"
step "done"
