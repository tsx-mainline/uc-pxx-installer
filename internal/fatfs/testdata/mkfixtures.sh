#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# Makes the real FAT images of the fatfs tests (*.fat.gz, see sparse.py). It
# runs in an x86_64 Alpine 3.24 container IMAGE (busybox 1.37 as in the RAM image,
# dosfstools, mtools), never on a host system:
#
#   docker run --rm -e UIDGID=$(id -u):$(id -g) -v "$PWD/internal/fatfs/testdata:/t" IMAGE sh /t/mkfixtures.sh tools
#   docker run --rm --privileged -e UIDGID=$(id -u):$(id -g) -v "$PWD/internal/fatfs/testdata:/t" IMAGE sh /t/mkfixtures.sh kernel
#
# "tools" uses mkfs (busybox and dosfstools) and mtools only. "kernel" lets
# the Linux vfat driver write a drive (a loop mount inside the container,
# hence --privileged): one copy taken while it is mounted (dirty) and one
# after the unmount (clean). The content of every test file is
# sparse.py pattern SEED SIZE; sparse_test.go knows the seeds and sizes.
set -eu
T=/t
W=/tmp/fx
mkdir -p $W
apk add -q --no-cache dosfstools mtools python3 > /dev/null
export MTOOLS_SKIP_CHECK=1 LANG=C.UTF-8 LC_ALL=C.UTF-8
FW=307533 # 300 KiB + 333 bytes
pat() { python3 -I $T/sparse.py pattern "$1" "$2" "$3"; }
pack() {
	python3 -I $T/sparse.py pack "$1" "$T/$2.fat.gz"
	rm -f "$1"
	[ -z "${UIDGID:-}" ] || chown "$UIDGID" "$T/$2.fat.gz"
	echo "$2: $(stat -c %s "$T/$2.fat.gz") bytes"
}

case ${1:-} in
tools)
	# A. the drive as the RAM image makes it (busybox mkfs.vfat, 1200 MiB)
	truncate -s 1200M $W/a.img
	busybox mkfs.vfat -n P10-INSTALL $W/a.img
	pat 1 $FW $W/fw1
	mcopy -i $W/a.img $W/fw1 "::/UC-P10_1.0.0.94_userdebug.bin"
	pack $W/a.img busybox-1200m

	# B. the drive formatted again by a Linux host (dosfstools defaults), and what macOS adds
	truncate -s 1200M $W/b.img
	mkfs.vfat -n P10-INSTALL $W/b.img > /dev/null
	pat 2 $FW $W/fw2
	mcopy -i $W/b.img $W/fw2 "::/uc-p10 firmware 1.0.0.94.bin"
	printf '\000\005\026\007\000\002\000\000Mac OS X        ' > $W/ad
	truncate -s 4096 $W/ad
	mcopy -i $W/b.img $W/ad "::/._uc-p10 firmware 1.0.0.94.bin"
	printf 'Bud1' > $W/ds
	mcopy -i $W/b.img $W/ds "::/.DS_Store"
	mmd -i $W/b.img "::/.fseventsd"
	mcopy -i $W/b.img $W/ds "::/.fseventsd/fseventsd-uuid"
	pack $W/b.img dosfs-1200m

	# C. FAT16, long Unicode names, subdirectories, a fragmented .bin
	truncate -s 64M $W/c.img
	mkfs.vfat -F 16 -s 4 -n FAT16TEST $W/c.img > /dev/null
	mmd -i $W/c.img "::/Sub Dir Ünïcödé" "::/Sub Dir Ünïcödé/deeper" "::/Sub Dir Ünïcödé/deeper/日本語"
	pat 10 5000 $W/s10
	mcopy -i $W/c.img $W/s10 "::/Sub Dir Ünïcödé/deeper/日本語/a file with a long name.txt"
	pat 11 100 $W/s11
	mcopy -i $W/c.img $W/s11 "::/readme.txt"
	head -c 2048 /dev/zero | tr '\0' 'x' > $W/fill
	i=0
	while [ $i -lt 40 ]; do mcopy -i $W/c.img $W/fill "::/FILL$i.DAT"; i=$((i + 1)); done
	i=1
	while [ $i -lt 40 ]; do mdel -i $W/c.img "::/FILL$i.DAT"; i=$((i + 2)); done
	pat 3 $FW $W/fw3
	# mtools keeps only the low 16 bits of a character above U+FFFF (U+1F600 -> U+F600)
	mcopy -i $W/c.img $W/fw3 "::/Firmware – ünïcode 😀 test.bin"
	pack $W/c.img f16-lfn-frag

	# D. FAT32 with two .bin files
	truncate -s 40M $W/d.img
	mkfs.vfat -F 32 -s 1 -n TWO $W/d.img > /dev/null
	pat 4 $FW $W/fw4
	pat 5 1000 $W/fw5
	mcopy -i $W/d.img $W/fw4 "::/a.bin"
	mcopy -i $W/d.img $W/fw5 "::/B.BIN"
	pack $W/d.img f32-two

	# E. FAT32 with no .bin in the top folder (only things that are not one)
	truncate -s 40M $W/e.img
	mkfs.vfat -F 32 -s 1 -n NONE $W/e.img > /dev/null
	mcopy -i $W/e.img $W/fw4 "::/firmware.bin.txt"
	mcopy -i $W/e.img $W/ad "::/._firmware.bin"
	mmd -i $W/e.img "::/old.bin" "::/sub"
	mcopy -i $W/e.img $W/fw5 "::/sub/inner.bin"
	mcopy -i $W/e.img $W/fw5 "::/deleted.bin"
	mdel -i $W/e.img "::/deleted.bin"
	pack $W/e.img f32-none

	# F. FAT12 (a small volume)
	truncate -s 1M $W/f.img
	mkfs.vfat -F 12 -n FAT12TEST $W/f.img > /dev/null
	pat 6 102407 $W/fw6
	mcopy -i $W/f.img $W/fw6 "::/small firmware.bin"
	mmd -i $W/f.img "::/docs"
	mcopy -i $W/f.img $W/s11 "::/docs/notes.txt"
	pack $W/f.img f12
	;;
kernel)
	# G. the Linux vfat driver writes the busybox drive
	truncate -s 1200M $W/g.img
	busybox mkfs.vfat -n P10-INSTALL $W/g.img
	mkdir -p $W/m
	# a free loop device without a read-only flag left by another job
	LD=
	for d in /sys/block/loop*; do
		[ -e $d/loop/backing_file ] || [ "$(cat $d/ro)" != 0 ] || { LD=/dev/${d##*/}; break; }
	done
	[ -n "$LD" ] || { echo "no free writable loop device"; exit 1; }
	losetup "$LD" $W/g.img
	mount -t vfat -o utf8 "$LD" $W/m
	grep -q " $W/m vfat rw," /proc/mounts || { echo "the vfat mount is not read-write"; umount $W/m; losetup -d "$LD"; exit 1; }
	mkdir -p "$W/m/System Volume Information"
	pat 12 3000 "$W/m/System Volume Information/IndexerVolumeGuid"
	pat 13 10 "$W/m/System Volume Information/emoji 😀 name.txt"
	i=0
	while [ $i -lt 30 ]; do head -c 4096 /dev/zero | tr '\0' 'y' > $W/m/fill$i.dat; i=$((i + 1)); done
	sync
	i=1
	while [ $i -lt 30 ]; do rm $W/m/fill$i.dat; i=$((i + 2)); done
	sync
	pat 7 $FW "$W/m/UC-P10 Firmware 1.0.0.94 (userdebug).bin"
	sync
	cp $W/g.img $W/g-dirty.img
	umount $W/m
	losetup -d "$LD"
	pack $W/g-dirty.img linux-dirty
	pack $W/g.img linux-clean
	;;
*)
	echo "usage: mkfixtures.sh tools|kernel"
	exit 2
	;;
esac
