# The KVM guest test of the step-2 USB transport

STEP2-USB-TRANSPORT-2026-11-11 §7 item 2: the real `uc-pxx-xfer` (amd64 build of the release recipe), the real
FunctionFS on `dummy_hcd`, headless Chromium running the real page (`web/site`, `js/step2usb.js`; `config.js`
patched by the driver only to turn the step-2 section on and pin the payload's manifest), and the real normal payload
(read-only). It needs `/dev/kvm` and docker; it touches no USB device of the host, loads no host kernel module and
needs no root on the host (the qemu container gets only `/dev/kvm`; the guest has no network).

    ci/xfer-guest/build.sh WORK            # the images, the binary, the guest root, the kernel and initrd
    ci/xfer-guest/run.sh WORK main         # descriptors, refusals, cut sweep, tab close, resync, the full payload, the seal
    ci/xfer-guest/run.sh WORK powercut     # a VM kill during the rootfs, then a second boot that finds nothing

`PAY=` names the payload folder (holding `uc-pxx-mainline/`). The results are in `WORK/root/xg/out/`
(`results-*.json`, the driver, guest, receiver and page logs) and `WORK/out/` (console logs, the root-write check).
