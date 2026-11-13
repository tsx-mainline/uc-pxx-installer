#!/bin/bash
# SPDX-License-Identifier: GPL-2.0-or-later
# test/guest/run.sh WORK: the real-USB test of uc-pxx-webdrive's driver (tests only). A KVM guest (the guest image of
# ci/xfer-guest: dummy_hcd + libcomposite + usb_f_fs, the Playwright v1.64.1 root) runs the real uc-pxx-xfer of this
# tree as the unit's transfer interface, and the driver (driver.mjs, inpage.js, the pinned Chromium and policy) as
# the host. No USB device of the host is used: the qemu container gets only /dev/kvm, and the guest has no network.
# Each driver run is made as uc-pxx-webdrive's container makes it: no udevd, a /dev/bus/usb with only the one usbfs
# node (none in a dry run), loopback only, uid 1000 with the node's group, read-only binds of the tool, the page copy,
# the modules and the set. Needs: PAY= the folder that holds uc-pxx-mainline (the RC-7 end-user payload), FIN= the
# uc-pxx-finish folder, WD_HOME= the folder with node_modules and site (uc-pxx-webdrive setup).
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
TOOL=$(cd "$HERE/../.." && pwd)
REPO=$(cd "$TOOL/../.." && pwd)
W=$(mkdir -p "${1:?usage: run.sh WORK}" && cd "$1" && pwd)
PAY=${PAY:?PAY= the folder holding uc-pxx-mainline}
FIN=${FIN:?FIN= the uc-pxx-finish folder}
WD_HOME=${WD_HOME:-$HOME/uc-pxx/webusb-driver/home}
MEM=${MEM:-3584}
if [ ! -d "$W/root/usr" ]; then
	R2="$FIN/uc-pxx-bootctl-inst" "$REPO/ci/xfer-guest/build.sh" "$W"
fi
# the harness in the guest root (root-owned: through the vmhost container)
docker run --rm -v "$W:/w" -v "$TOOL:/t:ro" -v "$WD_HOME:/h:ro" -v "$FIN:/f:ro" uc-pxx-xg-vmhost sh -c '
set -e
rm -rf /w/root/wd && mkdir -p /w/root/wd/out /w/root/wd/finish
cp -a /t /w/root/wd/tool && cp -a /h/node_modules /w/root/wd/modules && cp -a /h/site /w/root/wd/site && cp -a /f /w/root/wd/finish/uc-pxx-finish
cp /t/test/guest/guest-init.sh /t/test/guest/guest.cjs /w/root/wd/
chmod 755 /w/root/wd/guest-init.sh; chmod 777 /w/root/wd/out
# the guest init of ci/xfer-guest starts /xg/guest-init.sh: this test starts its own
printf "#!/bin/sh\nexec /wd/guest-init.sh\n" > /w/root/xg/guest-init.sh; chmod 755 /w/root/xg/guest-init.sh
# only the tool'"'"'s own policy file: no other policy file of the ci guest
rm -f /w/root/etc/chromium/policies/managed/webusb.json /w/root/etc/opt/chrome/policies/managed/webusb.json \
	/w/root/etc/opt/chromium/policies/managed/webusb.json /w/root/etc/opt/chrome_for_testing/policies/managed/webusb.json
mkdir -p /w/root/etc/opt/chrome_for_testing/policies/managed /w/root/tool /w/root/site /w/root/modules /w/root/out /w/root/scratch /w/root/finish
: > /w/root/etc/opt/chrome_for_testing/policies/managed/uc-pxx-webdrive.json
ls -la /w/root/etc/opt/chrome_for_testing/policies/managed /w/root/etc/chromium/policies/managed 2>&1 | head
'
rm -f "$W/out/console-wd.log"
docker run --rm --name xg-vm-wd --device /dev/kvm --network none -v "$W:/w" -v "$PAY:/payload:ro" uc-pxx-xg-vmhost \
	timeout 3600 qemu-system-x86_64 -enable-kvm -cpu host -m "$MEM" -smp 4 -nographic -no-reboot \
	-kernel /w/vmlinuz -initrd /w/initrd.cpio.gz -append "console=ttyS0 panic=-1 quiet uc-pxx.id=ucpxx-5a454728" \
	-virtfs local,path=/w/root,mount_tag=root,security_model=passthrough,id=r0 \
	-virtfs local,path=/payload,mount_tag=payload,security_model=none,readonly=on,id=r1 > "$W/out/console-wd.log" 2>&1 || true
docker run --rm -v "$W:/w" uc-pxx-xg-vmhost sh -c 'chmod -R a+rX /w/root/wd/out'
grep -h "RESULT" "$W/root/wd/out/guest.log" || { echo "no RESULT line"; tail -30 "$W/root/wd/out/guest.log" 2>/dev/null; exit 1; }
