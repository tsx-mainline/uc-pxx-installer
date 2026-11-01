#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# Runs INSIDE an aarch64 Alpine container (image/build.sh starts it), with
# the repository at /w. It builds the initramfs of the installer RAM image:
#
#   mkinitramfs.sh P10INST OUT.cpio [BOOTCTL]
#
# P10INST is the static aarch64 p10inst. OUT is an uncompressed cpio
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
# The image holds busybox (with busybox-extras for udhcpd), dropbear, the Terminus console font, p10inst and
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
# Env: SOURCE_DATE_EPOCH (required), UIDGID (owner of OUT), MIRROR (default /apk).
set -eu
P10INST=$1 OUT=$2 BOOTCTL=${3:-}
[ "$(apk --print-arch)" = aarch64 ] || { echo "mkinitramfs.sh: this container is $(apk --print-arch), not aarch64"; exit 1; }
[ -x "$P10INST" ] || { echo "mkinitramfs.sh: no p10inst at $P10INST"; exit 1; }
SDE=${SOURCE_DATE_EPOCH:-}
case $SDE in ''|*[!0-9]*) echo "mkinitramfs.sh: SOURCE_DATE_EPOCH must be a number of seconds"; exit 1;; esac
export LC_ALL=C
LOCK=/w/image/apk.lock
BRANCH=$(awk '!/^#/ && NF { print $2; exit }' $LOCK)
MIRROR=${MIRROR:-/apk}
printf '%s/%s/main\n%s/%s/community\n' "$MIRROR" "$BRANCH" "$MIRROR" "$BRANCH" > /etc/apk/repositories
R=/tmp/p10inst-root; rm -rf $R; mkdir -p $R
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
chmod 755 $R/init $R/etc/init.d/rcS $R/etc/p10inst/usb-net-up
install -D -m 755 "$P10INST" $R/usr/bin/p10inst
if [ -n "$BOOTCTL" ]; then
	# The install image: the one A/B tool, at the RULES.md path.
	TOOL=/usr/libexec/p10inst/uc-pxx-bootctl-inst
	[ "$(sha256sum < "$BOOTCTL" | cut -c1-64)" = 20aa6bc5d2146faace461eec0b50f741c681b3c58827788b9b806d9444b00203 ] ||
		{ echo "mkinitramfs.sh: $BOOTCTL is not the reviewed A/B tool (sha256 20aa6bc5...)"; exit 1; }
	install -D -m 700 "$BOOTCTL" "$R$TOOL"
	# The inittab and the banner of the install image, with the overlay modes (664).
	for f in etc/inittab etc/profile; do
		cp /w/image/overlay-install/$f $R/$f
		chmod 664 $R/$f
	done
	grep -q '^tty1::respawn:/usr/bin/p10inst flow --leds$' $R/etc/inittab ||
		{ echo "mkinitramfs.sh: the install inittab does not run the flow on tty1"; exit 1; }
fi

# The console font for setfont (SIL OFL 1.1), uncompressed.
apk add -q --no-cache font-terminus > /dev/null
mkdir -p $R/usr/share/consolefonts
zcat /usr/share/consolefonts/ter-132b.psf.gz > $R/usr/share/consolefonts/ter-132b.psf

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
echo p10inst > $R/etc/hostname
rm -rf $R/var/cache/apk/* $R/lib/apk/db/scripts.tar $R/usr/share/man $R/usr/share/doc $R/etc/init.d/*.apk-new
for f in /init /sbin/init /bin/busybox /usr/bin/p10inst /usr/sbin/dropbear /etc/inittab /etc/init.d/rcS \
	/etc/p10inst/usb-net-up /etc/p10inst/udhcpd.conf /etc/p10inst/ram-image /usr/share/consolefonts/ter-132b.psf /usr/sbin/udhcpd; do
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
