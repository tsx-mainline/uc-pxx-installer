#!/bin/bash
# SPDX-License-Identifier: GPL-2.0-or-later
# ci/xfer-guest/run.sh WORK main|powercut: boot the guest (README.md). powercut boots twice: the first boot is killed
# (SIGKILL of qemu) during the rootfs transfer, the second one checks what is left.
set -euo pipefail
W=$(cd "${1:?usage: run.sh WORK main|powercut}" && pwd)
PHASE=${2:?usage: run.sh WORK main|powercut}
PAY=${PAY:-$HOME/uc-pxx/mainline-rc/payload-rebuild/payload-normal/A}
MEM=${MEM:-3584}
boot() { # boot PHASE
	rm -f "$W/out/console-$1.log"
	docker run --rm -v "$W:/w" uc-pxx-xg-vmhost sh -c 'touch /w/stamp-'"$1"'; sleep 1'
	docker run --rm --name "xg-vm-$1" --device /dev/kvm --network none -v "$W:/w" -v "$PAY:/payload:ro" uc-pxx-xg-vmhost \
		timeout 3000 qemu-system-x86_64 -enable-kvm -cpu host -m "$MEM" -smp 4 -nographic -no-reboot \
		-kernel /w/vmlinuz -initrd /w/initrd.cpio.gz -append "console=ttyS0 panic=-1 quiet xg.phase=$1" \
		-virtfs local,path=/w/root,mount_tag=root,security_model=passthrough,id=r0 \
		-virtfs local,path=/payload,mount_tag=payload,security_model=none,readonly=on,id=r1 > "$W/out/console-$1.log" 2>&1 || true
}
rootwrites() { # the files of the guest root (9p, a disk of the host) that changed during the boot: only /xg/out may
	docker run --rm -v "$W:/w" uc-pxx-xg-vmhost sh -c 'cd /w/root && find . -newer /w/stamp-'"$1"' ! -path "./xg/out" ! -path "./xg/out/*" -print' \
		> "$W/out/rootwrites-$1.txt"
	echo "root writes of $1: $(wc -l < "$W/out/rootwrites-$1.txt") (out/rootwrites-$1.txt; the harness: fontconfig, /run, /payload)"
	if grep -E 'xfer|incoming|complete|\.ext4$|\.img$|manifest' "$W/out/rootwrites-$1.txt"; then
		echo "FAIL: a staged file on the guest's root disk"
	else
		echo "ok: no staged file on the guest's root disk"
	fi
}
case "$PHASE" in
main)
	boot main
	rootwrites main
	;;
powercut)
	rm -f "$W/out/console-powercut.log" # never a marker of an earlier run
	boot powercut &
	bp=$!
	for i in $(seq 1200); do
		grep -q XG-POWERCUT-NOW "$W/out/console-powercut.log" 2>/dev/null && break
		sleep 0.5
	done
	if grep -q XG-POWERCUT-NOW "$W/out/console-powercut.log"; then
		if docker kill -s KILL xg-vm-powercut > /dev/null; then
			echo "powercut: qemu killed at $(date -u +%T)"
		else
			echo "powercut: FAIL: the kill did not reach the VM"
		fi
	else
		echo "powercut: the guest never reached the cut point"
	fi
	wait "$bp" || true
	rootwrites powercut
	boot after-powercut
	rootwrites after-powercut
	;;
*)
	echo "unknown phase $PHASE"
	exit 2
	;;
esac
docker run --rm -v "$W:/w" uc-pxx-xg-vmhost sh -c 'chmod -R a+rX /w/root/xg/out; ls -la /w/root/xg/out'
echo "run ended: $PHASE"
