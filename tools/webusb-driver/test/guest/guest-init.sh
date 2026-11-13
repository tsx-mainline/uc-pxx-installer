#!/bin/bash
# SPDX-License-Identifier: GPL-2.0-or-later
# PID 1 of the guest of test/guest/run.sh: the mounts and the USB gadget stack. No udevd (as in the container of
# uc-pxx-webdrive), the tmpfs stage as the step-2 service makes it; then guest.cjs; then power off.
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin PLAYWRIGHT_BROWSERS_PATH=/ms-playwright
mount -t tmpfs tmpfs /run
mount -t tmpfs tmpfs /tmp
mkdir -p /dev/shm /dev/pts /tmp/home
mount -t tmpfs tmpfs /dev/shm
mount -t devpts devpts /dev/pts
export HOME=/tmp/home
exec > /wd/out/guest-init.log 2>&1
set -x
mkdir -p /payload
mount -t 9p -o trans=virtio,version=9p2000.L,msize=524288,ro payload /payload
ip link set lo up
modprobe configfs
modprobe dummy_hcd
modprobe libcomposite
modprobe usb_f_fs
mountpoint -q /sys/kernel/config || mount -t configfs none /sys/kernel/config
ls /run/udev 2>&1
mkdir -p /run/uc-pxx-step2/stage
mount -t tmpfs -o size=1300m,mode=0711,nosuid,nodev,noexec uc-pxx-stage /run/uc-pxx-step2/stage
free -m
set +x
cd /wd && timeout 3300 node guest.cjs > /wd/out/guest.log 2>&1
echo "guest.cjs exit $?"
sync
echo o > /proc/sysrq-trigger
