#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# Runs INSIDE an aarch64 Alpine container (image/build.sh starts it), with
# the repository at /w. It builds the initramfs of the installer RAM image:
#
#   mkinitramfs.sh P10INST OUT.cpio [BOOTCTL]
#
# P10INST is the static aarch64 uc-pxx-inst. OUT is an uncompressed cpio
# archive (newc). The kernel build puts it into the Image.
#
# With BOOTCTL it makes the INSTALL image (image/build.sh --install,
# INSTALLER-END-USER-PLAN.md section 1.1): P10INST is then the release install
# build, BOOTCTL is the reviewed A/B tool (sha256 20aa6bc5..., checked here
# again), installed at the path of RULES.md, owner root, mode 0700, and
# image/overlay-install replaces /etc/inittab (the install flow on tty1) and
# /etc/profile (the banner of the install image). Without BOOTCTL (the status
# image) nothing of this is done: the status image has no A/B tool.
#
# The image holds busybox (with busybox-extras for udhcpd), dropbear, the Terminus console font, uc-pxx-inst and
# the files of image/overlay. It holds no tool that writes the eMMC: no
# uc-pxx-bootctl, no e2fsprogs, no partition tool. The busybox applets that
# write disks or flash are removed. ci/check-no-writer.py checks the result.
#
# The same inputs give the same bytes (docs: image/build.sh, "Reproducible"):
#   - the packages come only from MIRROR, the local copy of the files of
#     image/apk.lock (the container has no network). The script checks that it
#     installed exactly the versions of the lock
#   - the modes of the overlay files are fixed here, not by the umask of the
#     checkout
#   - the two build times in the files are set to SOURCE_DATE_EPOCH: the time
#     of the apk add line in var/log/apk.log, and the day of the last password
#     change in etc/shadow (klogd, added by busybox)
#   - every file time is SOURCE_DATE_EPOCH; the cpio has the sorted names,
#     owner 0:0, and (cpio --reproducible) inode numbers from 0, no device
#     numbers and a link count of 2 for each folder, so the file system
#     under docker does not matter. The device nodes keep their numbers
# Env: SOURCE_DATE_EPOCH (required), UIDGID (owner of OUT), MIRROR (default /apk), DEVMARK
# (a dev build: the file that becomes /NOT-FOR-DEVICE.txt; empty for a release build).
set -eu
P10INST=$1 OUT=$2 BOOTCTL=${3:-}
[ "$(apk --print-arch)" = aarch64 ] || { echo "mkinitramfs.sh: this container is $(apk --print-arch), not aarch64"; exit 1; }
[ -x "$P10INST" ] || { echo "mkinitramfs.sh: no uc-pxx-inst at $P10INST"; exit 1; }
SDE=${SOURCE_DATE_EPOCH:-}
case $SDE in ''|*[!0-9]*) echo "mkinitramfs.sh: SOURCE_DATE_EPOCH must be a number of seconds"; exit 1;; esac
export LC_ALL=C
LOCK=/w/image/apk.lock
BRANCH=$(awk '!/^#/ && NF { print $2; exit }' $LOCK)
MIRROR=${MIRROR:-/apk}
printf '%s/%s/main\n%s/%s/community\n' "$MIRROR" "$BRANCH" "$MIRROR" "$BRANCH" > /etc/apk/repositories
R=/tmp/uc-pxx-inst-root; rm -rf $R; mkdir -p $R
apk add --root $R --initdb --no-cache -q \
	--repositories-file /etc/apk/repositories --keys-dir /etc/apk/keys \
	alpine-baselayout busybox busybox-binsh busybox-extras dropbear
# Exactly the root packages of the lock, at their versions.
want=$(awk '$1 == "root" { print $3 " " $4 }' $LOCK | sort)
have=$(awk -F: '/^P:/ { p = substr($0, 3) } /^V:/ { print p " " substr($0, 3) }' $R/lib/apk/db/installed | sort)
[ "$want" = "$have" ] || { printf 'mkinitramfs.sh: installed packages differ from image/apk.lock:\n%s\n' "$have"; exit 1; }
cp -a /w/image/overlay/. $R/
# The modes of the overlay are set here, so the umask of the checkout does not
# matter. They are the modes of the reviewed images 117439ee and b6a14404
# (built from a checkout with umask 002): folders 775, files 664, the three
# scripts 755. Owner and group are 0 in the image.
(cd /w/image/overlay && find . -type d) | while IFS= read -r d; do chmod 775 "$R/$d"; done
(cd /w/image/overlay && find . -type f) | while IFS= read -r f; do chmod 664 "$R/$f"; done
chmod 755 $R/init $R/etc/init.d/rcS $R/etc/uc-pxx-inst/usb-net-up
install -D -m 755 "$P10INST" $R/usr/bin/uc-pxx-inst
if [ -n "$BOOTCTL" ]; then
	# The install image: the one A/B tool, at the RULES.md path.
	TOOL=/usr/libexec/uc-pxx-inst/uc-pxx-bootctl-inst
	[ "$(sha256sum < "$BOOTCTL" | cut -c1-64)" = 20aa6bc5d2146faace461eec0b50f741c681b3c58827788b9b806d9444b00203 ] ||
		{ echo "mkinitramfs.sh: $BOOTCTL is not the reviewed A/B tool (sha256 20aa6bc5...)"; exit 1; }
	install -D -m 700 "$BOOTCTL" "$R$TOOL"
	# The inittab and the banner of the install image, with the overlay modes (664).
	for f in etc/inittab etc/profile; do
		cp /w/image/overlay-install/$f $R/$f
		chmod 664 $R/$f
	done
	grep -q '^tty1::respawn:/usr/bin/uc-pxx-inst flow --leds$' $R/etc/inittab ||
		{ echo "mkinitramfs.sh: the install inittab does not run the flow on tty1"; exit 1; }
	# The in-RAM lock_state reader of the direct path (MAINLINE-RC2-LS-2026-11-11.md): the legacy
	# OP-TEE module, the round-4 read-only supplicant and the READ-only client, each pinned here
	# and again in uc-pxx-inst (internal/devmode/lockstate.go), root, 0500. Only uc-pxx-inst
	# runs them, once per boot, on the direct path. None of them writes the eMMC.
	# The lock-state writer of the direct path (MAINLINE-UNLOCK-INTEGRATION-2026-11-12.md): the
	# real supplicant (it writes the security partition p16 only, O_RDWR, bounds-checked) and
	# the setlock client, each pinned here and again in uc-pxx-inst (internal/devmode/lockwrite.go),
	# root, 0500. Only uc-pxx-inst runs them, on the direct path, only after the reader read
	# 0x00, before any target write, and only with the value 0x01.
	OT=/w/image/overlay-optee-install/usr/lib/uc-pxx-inst/optee
	OD=usr/lib/uc-pxx-inst/optee
	for p in "optee_legacy.ko 5870e0fd31816310518e6576e30aeaf38e3c11a21a5cec2cfad4ec1176b2e02d" \
		"tee-supplicant-ro 64c139f6a062776e00a7810d4a06e9e96eaa1fc787754efc5df6acc9427e2e0c" \
		"tee_legacy_read 1424ac1e34f913e2068d579afa87cafb5fc4e0bd2360f8d4fe0284999500c220" \
		"tee-supplicant-rw-real 2786a2a74f4f8794ece654a97e35a36804af0f77b67fa8e933d4d6c5698fea0a" \
		"tee_legacy_setlock c0a4af8a4755239de0bc860952694f00ca9bb0eda31e3347d413a96857df6768"; do
		[ "$(sha256sum < $OT/${p% *} | cut -c1-64)" = "${p#* }" ] ||
			{ echo "mkinitramfs.sh: $OD/${p% *} is not the pinned file (sha256 ${p#* })"; exit 1; }
		install -D -m 500 $OT/${p% *} $R/$OD/${p% *}
	done
	chmod 500 $R/$OD
	[ -L $R/sbin/insmod ] || [ -x $R/sbin/insmod ] || { echo "mkinitramfs.sh: no /sbin/insmod for the lock_state reader"; exit 1; }
	echo "mkinitramfs.sh: the lock_state reader and writer added ($OD: module, read-only supplicant, read client, real supplicant, setlock client)"
fi

# The dev mark (image/build.sh, KIND=dev only): /NOT-FOR-DEVICE.txt, 0644. RULES.md knows a
# dev build by it; a release build (no DEVMARK) has none.
if [ -n "${DEVMARK:-}" ]; then
	[ -f "$DEVMARK" ] || { echo "mkinitramfs.sh: no dev mark at $DEVMARK"; exit 1; }
	install -m 644 "$DEVMARK" $R/NOT-FOR-DEVICE.txt
	echo "mkinitramfs.sh: the dev mark /NOT-FOR-DEVICE.txt added (KIND=dev)"
elif [ -e $R/NOT-FOR-DEVICE.txt ]; then
	echo "mkinitramfs.sh: a release build must not hold /NOT-FOR-DEVICE.txt"; exit 1
fi

# The console font for setfont (SIL OFL 1.1), uncompressed.
apk add -q --no-cache font-terminus > /dev/null
mkdir -p $R/usr/share/consolefonts
# ter-132b for the UC-P10 panel; ter-128b for the UC-P8 panel (the owner, 2026-11-02;
# ter-124b stays as the other size of UC-P8.md 5.2). rcS chooses by the device tree.
for f in ter-132b ter-124b ter-128b; do
	zcat /usr/share/consolefonts/$f.psf.gz > $R/usr/share/consolefonts/$f.psf
done

# Busybox applets that write disks, flash or registers: remove the links.
# A person on the shell still has dd. The image itself never writes the eMMC.
for a in fdisk sfdisk mkfs.ext2 mke2fs tune2fs e2fsck fsck fsck.minix mkfs.minix mkswap swapon \
	blkdiscard hdparm devmem flashcp flash_eraseall flash_lock flash_unlock nandwrite nanddump \
	ubiattach ubidetach ubimkvol ubirmvol ubirsvol ubiupdatevol losetup fstrim partprobe; do
	for d in bin sbin usr/bin usr/sbin; do
		[ -L "$R/$d/$a" ] && rm -f "$R/$d/$a"
	done
done
# mkfs.vfat stays: it formats the RAM drive.
[ -L $R/sbin/mkfs.vfat ] || [ -L $R/usr/sbin/mkfs.vfat ] || { echo "mkinitramfs.sh: busybox has no mkfs.vfat"; exit 1; }

# root: no password. Only the USB cable and the debug UART reach a shell
# (dropbear -B allows the empty password, on the USB address only).
sed -i 's|^root:[^:]*:|root::|' $R/etc/shadow
echo uc-pxx > $R/etc/hostname  # = rcS (`hostname uc-pxx`): the flow title fits the UC-P8 console (MAINLINE-RC-REVIEW NIT-4)
rm -rf $R/var/cache/apk/* $R/lib/apk/db/scripts.tar $R/usr/share/man $R/usr/share/doc $R/etc/init.d/*.apk-new
for f in /init /sbin/init /bin/busybox /usr/bin/uc-pxx-inst /usr/sbin/dropbear /etc/inittab /etc/init.d/rcS \
	/etc/uc-pxx-inst/usb-net-up /etc/uc-pxx-inst/udhcpd.conf /etc/uc-pxx-inst/ram-image /usr/share/consolefonts/ter-132b.psf /usr/share/consolefonts/ter-124b.psf \
	/usr/share/consolefonts/ter-128b.psf /usr/sbin/udhcpd; do
	[ -e "$R$f" ] || [ -L "$R$f" ] || { echo "mkinitramfs.sh: $f is missing"; exit 1; }
done
[ -z "$BOOTCTL" ] || [ -f "$R$TOOL" ] || { echo "mkinitramfs.sh: $TOOL is missing"; exit 1; }
apk add -q --no-cache cpio > /dev/null
# The build tools at the versions of the lock.
for t in $(awk '$1 == "tool" { print $3 "=" $4 }' $LOCK); do
	v=$(awk -F: -v n="${t%%=*}" '/^P:/ { p = substr($0, 3) } /^V:/ && p == n { print substr($0, 3) }' /lib/apk/db/installed)
	[ "$v" = "${t#*=}" ] || { echo "mkinitramfs.sh: build tool ${t%%=*} is ${v:-not installed}, image/apk.lock says ${t#*=}"; exit 1; }
done

# The build times in files. apk.log: the time of its one "Running `apk add
# ...` at TIME" line. shadow: the day of the last password change of each
# account that has one (only klogd: its password is "!", so it cannot log in,
# and its maximum age is 99999 days).
STAMP=$(date -u -d "@$SDE" '+%Y-%m-%d %H:%M:%S')
L=$R/var/log/apk.log
[ "$(grep -c '^Running `apk add .*` at [0-9-]* [0-9:]*$' $L)" = 1 ] || { echo "mkinitramfs.sh: $L is not one apk add line"; cat $L; exit 1; }
sed -i "s/^\(Running \`apk add .*\` at \)[0-9-]* [0-9:]*\$/\1$STAMP/" $L
! grep -E '[0-9]{4}-[0-9]{2}-[0-9]{2}|[0-9]{2}:[0-9]{2}:[0-9]{2}' $L | grep -v -F " at $STAMP" | grep -q . ||
	{ echo "mkinitramfs.sh: $L has another time"; cat $L; exit 1; }
awk -F: -v OFS=: -v d=$((SDE / 86400)) '$3 != "" && $3 != "0" { $3 = d } { print }' $R/etc/shadow > /tmp/shadow
cat /tmp/shadow > $R/etc/shadow
rm -f /tmp/shadow

# Every file time is SOURCE_DATE_EPOCH (the last change to the tree is above).
find $R -exec touch -h -d "@$SDE" {} +
(cd $R && find . | sort | cpio -o -H newc -R 0:0 --reproducible --quiet) > "$OUT"
[ -n "${UIDGID:-}" ] && chown "$UIDGID" "$OUT"
ls -l "$OUT"
sha256sum "$OUT"
