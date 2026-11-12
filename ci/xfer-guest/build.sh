#!/bin/bash
# SPDX-License-Identifier: GPL-2.0-or-later
# ci/xfer-guest/build.sh WORK: the guest test of the step-2 USB transport (README.md).
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
REPO=$(cd "$HERE/../.." && pwd)
W=${1:?usage: ci/xfer-guest/build.sh WORK}
mkdir -p "$W/out" "$W/gocache"
W=$(cd "$W" && pwd)
docker build -q -t uc-pxx-xg-vmhost -f "$HERE/Dockerfile.vmhost" "$HERE" > /dev/null
docker build -q -t uc-pxx-xg-guest -f "$HERE/Dockerfile.guest" "$HERE" > /dev/null
# the receiver: the real uc-pxx-xfer, the release recipe for amd64
docker run --rm --network none -u "$(id -u):$(id -g)" -v "$REPO:/src:ro" -v "$W:/w" -w /src -e GOCACHE=/w/gocache -e HOME=/tmp \
	-e CGO_ENABLED=0 -e GOFLAGS=-buildvcs=false golang:1.27.2 go build -tags xfer -trimpath -ldflags "-s -w -buildid=" -o /w/uc-pxx-xfer ./cmd/uc-pxx-xfer
sha256sum "$W/uc-pxx-xfer" | tee "$W/out/uc-pxx-xfer.sha256"
# the finish set of the finish phases: the finish executor (the release recipe for amd64) and the real r2 (aarch64:
# only bytes in the guest, never run; R2= its file, sha256 20aa6bc5...)
docker run --rm --network none -u "$(id -u):$(id -g)" -v "$REPO:/src:ro" -v "$W:/w" -w /src -e GOCACHE=/w/gocache -e HOME=/tmp \
	-e CGO_ENABLED=0 -e GOFLAGS=-buildvcs=false golang:1.27.2 go build -tags install,step2,finish -trimpath -ldflags "-s -w -buildid=" -o /w/uc-pxx-step2-finish ./cmd/uc-pxx-step2-finish
sha256sum "$W/uc-pxx-step2-finish" | tee "$W/out/uc-pxx-step2-finish.sha256"
R2=${R2:?R2= the reviewed uc-pxx-bootctl-inst r2 file}
[ "$(sha256sum < "$R2" | cut -c1-64)" = 20aa6bc5d2146faace461eec0b50f741c681b3c58827788b9b806d9444b00203 ] || { echo "R2 is not r2 20aa6bc5"; exit 1; }
cp "$R2" "$W/uc-pxx-bootctl-inst"
# the guest root (once): the Playwright image with udev, kmod, usbutils
if [ ! -d "$W/root/usr" ]; then
	cid=$(docker create uc-pxx-xg-guest)
	docker export "$cid" > "$W/guest.tar"
	docker rm "$cid" > /dev/null
	docker run --rm -v "$W:/w" uc-pxx-xg-vmhost sh -c 'mkdir -p /w/root && tar -x -C /w/root -f /w/guest.tar && rm /w/guest.tar'
fi
# the kernel of the vmhost image (dummy_hcd, libcomposite, usb_f_fs as modules), its modules in the guest root, an initrd for the 9p root
docker run --rm -v "$W:/w" -v "$HERE:/g:ro" uc-pxx-xg-vmhost bash -c '
set -e
K=$(ls /lib/modules | head -1); echo "kernel $K"
rm -rf /w/root/lib/modules/$K; mkdir -p /w/root/lib/modules; cp -a /lib/modules/$K /w/root/lib/modules/
cp /boot/vmlinuz-$K /w/vmlinuz
rm -rf /tmp/i; mkdir -p /tmp/i/bin /tmp/i/mods /tmp/i/proc /tmp/i/sys /tmp/i/dev
cp /bin/busybox /tmp/i/bin/; cp /g/initramfs-init /tmp/i/init; chmod 755 /tmp/i/init
: > /tmp/i/modlist
modprobe -S $K -a --show-depends 9p 9pnet_virtio virtio_pci 2>/dev/null | awk "/^insmod/{print \$2}" | while read f; do
	n=$(basename $f); n=${n%.xz}; n=${n%.zst}; n=${n%.ko}
	case $f in *.xz) xz -dc $f > /tmp/i/mods/$n.ko;; *.zst) zstd -dc $f > /tmp/i/mods/$n.ko;; *) cp $f /tmp/i/mods/$n.ko;; esac
	grep -qx $n /tmp/i/modlist || echo $n >> /tmp/i/modlist
done
cp /tmp/i/modlist /tmp/i/mods/
(cd /tmp/i && find . | cpio -o -H newc 2>/dev/null | gzip -1) > /w/initrd.cpio.gz
'
# the harness, the real page and the binary in the guest root; the receiver's user; the WebUSB policy for the test origin
docker run --rm -v "$W:/w" -v "$REPO:/r:ro" uc-pxx-xg-vmhost sh -c '
set -e
rm -rf /w/root/xg && mkdir -p /w/root/xg/out /w/root/xg/site /w/root/xg/test /w/root/xg/bin
cp -a /r/web/site/. /w/root/xg/site/
cp /r/web/test/serve.mjs /w/root/xg/test/
cp /r/ci/xfer-guest/guest-init.sh /r/ci/xfer-guest/driver.cjs /r/ci/xfer-guest/finish.cjs /r/ci/xfer-guest/msos.py /w/root/xg/
cp /w/uc-pxx-step2-finish /w/uc-pxx-bootctl-inst /w/root/xg/bin/ && chmod 755 /w/root/xg/bin/uc-pxx-step2-finish && chmod 644 /w/root/xg/bin/uc-pxx-bootctl-inst
chmod 755 /w/root/xg/guest-init.sh
cp /w/uc-pxx-xfer /w/root/xg/bin/ && chmod 755 /w/root/xg/bin/uc-pxx-xfer
chmod 777 /w/root/xg/out
grep -q "^uc-pxx-xfer:" /w/root/etc/passwd || echo "uc-pxx-xfer:x:990:990:step-2 transfer:/nonexistent:/usr/sbin/nologin" >> /w/root/etc/passwd
grep -q "^uc-pxx-xfer:" /w/root/etc/group || echo "uc-pxx-xfer:x:990:" >> /w/root/etc/group
POL="{\"WebUsbAllowDevicesForUrls\":[{\"devices\":[{\"vendor_id\":7531,\"product_id\":261}],\"urls\":[\"http://127.0.0.1:8137\"]}]}"
for d in /etc/chromium/policies/managed /etc/opt/chrome/policies/managed /etc/opt/chrome_for_testing/policies/managed /etc/opt/chromium/policies/managed; do
	mkdir -p /w/root$d; echo "$POL" > /w/root$d/webusb.json
done
'
echo "build ok: $W"
