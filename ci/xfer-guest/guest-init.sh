#!/bin/bash
# SPDX-License-Identifier: GPL-2.0-or-later
# PID 1 of the guest (README.md): the mounts, the USB gadget stack (dummy_hcd + libcomposite + usb_f_fs), the tmpfs
# stage as the step-2 service makes it, then the driver of the phase; then power off.
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin PLAYWRIGHT_BROWSERS_PATH=/ms-playwright
mount -t tmpfs tmpfs /run
mount -t tmpfs tmpfs /tmp
mkdir -p /dev/shm /dev/pts /tmp/home
mount -t tmpfs tmpfs /dev/shm
mount -t devpts devpts /dev/pts
export HOME=/tmp/home
PHASE=$(sed -n 's/.*xg\.phase=\([a-z-]*\).*/\1/p' /proc/cmdline)
exec > "/xg/out/guest-$PHASE.log" 2>&1
set -x
mkdir -p /payload
mount -t 9p -o trans=virtio,version=9p2000.L,msize=524288,ro payload /payload
ip link set lo up
/lib/systemd/systemd-udevd --daemon
udevadm trigger
udevadm settle
modprobe configfs
modprobe dummy_hcd
modprobe libcomposite
modprobe usb_f_fs
mountpoint -q /sys/kernel/config || mount -t configfs none /sys/kernel/config
ls /sys/class/udc
free -m
# the stage: a root tmpfs of 1300 MiB, 0711 (the step-2 service's mount, C2)
mkdir -p /run/uc-pxx-step2/stage
mount -t tmpfs -o size=1300m,mode=0711,nosuid,nodev,noexec uc-pxx-stage /run/uc-pxx-step2/stage
ls -la /run/uc-pxx-step2/stage
set +x
cd /xg && timeout 2700 node driver.cjs "$PHASE" > "/xg/out/driver-$PHASE.log" 2>&1
echo "driver exit $?"
ls -la /run/uc-pxx-step2/stage /run/uc-pxx-step2/stage/* 2>&1
sync
echo o > /proc/sysrq-trigger
