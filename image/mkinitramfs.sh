#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# Runs INSIDE an aarch64 Alpine container (image/build.sh starts it), with
# the repository at /w. It builds the initramfs of the installer RAM image:
#
#   mkinitramfs.sh P10INST OUT.cpio
#
# P10INST is the static aarch64 p10inst. OUT is an uncompressed cpio
# archive (newc). The kernel build puts it into the Image.
#
# The image holds busybox (with busybox-extras for udhcpd), dropbear, the Terminus console font, p10inst and
# the files of image/overlay. It holds no tool that writes the eMMC: no
# uc-pxx-bootctl, no e2fsprogs, no partition tool. The busybox applets that
# write disks or flash are removed. ci/check-no-writer.py checks the result.
# Env: ALPINE (default v3.24), UIDGID (owner of OUT), MIRROR.
set -eu
P10INST=$1 OUT=$2
[ "$(apk --print-arch)" = aarch64 ] || { echo "mkinitramfs.sh: this container is $(apk --print-arch), not aarch64"; exit 1; }
[ -x "$P10INST" ] || { echo "mkinitramfs.sh: no p10inst at $P10INST"; exit 1; }
MIRROR=${MIRROR:-https://dl-cdn.alpinelinux.org/alpine}
printf '%s/%s/main\n%s/%s/community\n' "$MIRROR" "${ALPINE:-v3.24}" "$MIRROR" "${ALPINE:-v3.24}" > /etc/apk/repositories
R=/tmp/p10inst-root; rm -rf $R; mkdir -p $R
apk add --root $R --initdb --no-cache -q \
	--repositories-file /etc/apk/repositories --keys-dir /etc/apk/keys \
	alpine-baselayout busybox busybox-binsh busybox-extras dropbear
cp -a /w/image/overlay/. $R/
chmod 755 $R/init $R/etc/init.d/rcS $R/etc/p10inst/usb-net-up
install -D -m 755 "$P10INST" $R/usr/bin/p10inst

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
	/etc/p10inst/usb-net-up /etc/p10inst/udhcpd.conf /usr/share/consolefonts/ter-132b.psf /usr/sbin/udhcpd; do
	[ -e "$R$f" ] || [ -L "$R$f" ] || { echo "mkinitramfs.sh: $f is missing"; exit 1; }
done
apk add -q --no-cache cpio > /dev/null
(cd $R && find . | sort | cpio -o -H newc -R 0:0 --quiet) > "$OUT"
[ -n "${UIDGID:-}" ] && chown "$UIDGID" "$OUT"
ls -l "$OUT"
sha256sum "$OUT"
