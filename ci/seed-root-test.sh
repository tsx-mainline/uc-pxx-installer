#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# ci/seed-root-test.sh ROOTFS E2FS-TREE [ROOTFS-A]: the ssh key seed's read-write mount on a
# real kernel (internal/keywrite/mount/root_test.go and internal/device/seed_root_test.go, build tags
# seedroot,install,devtest), as root in a
# privileged container with loop devices, on a copy of a real payload root file
# system ROOTFS. Never on a unit. E2FS-TREE: a musl x86_64 tree with sbin/e2fsck
# (e2fsprogs). Run on the build host from the repo root.
set -eu
R=$(readlink -f "$1"); E=$(readlink -f "$2"); A=${3:+$(readlink -f "$3")}
# with ROOTFS-A (the normal payload of slot A) also the first boots (firstboot_test.go): ROOTFS and ROOTFS-A must be
# built with tsx-linux-common's setup-done rule
FB=""; [ -n "$A" ] && FB="-v $A:/in/rootfs-a.ext4:ro -e FIRSTBOOT_B=/in/rootfs.ext4 -e FIRSTBOOT_A=/in/rootfs-a.ext4"
docker run --rm --privileged --network none --tmpfs /tmp:rw,size=8g,exec $FB -v "$PWD":/src:ro -w /src -v "$R":/in/rootfs.ext4:ro -v "$E":/e2fs:ro \
	-e GOCACHE=/tmp/gc -e GOFLAGS=-buildvcs=false -e SEED_ROOTFS=/in/rootfs.ext4 -e SEED_E2FSCK=/tmp/e2fsck \
	golang:1.27.2-alpine sh -c '
	printf "#!/bin/sh\nexec /e2fs/lib/ld-musl-x86_64.so.1 --library-path /e2fs/lib:/e2fs/usr/lib /e2fs/sbin/e2fsck \"\$@\"\n" > /tmp/e2fsck
	chmod 755 /tmp/e2fsck
	go test -p 1 -timeout 60m -tags seedroot,install,devtest -count=1 -v -run "TestSeedRoot|TestSeedNodeReadOnlyFlags|TestSeedFirstBoot" ./internal/keywrite/mount/ ./internal/device/ 2>&1 | tee /tmp/seed.out
	rc=$?
	# the run must have run both tests (RC-8 key code review MAJOR-3: no test, no pass)
	grep -q "^--- PASS: TestSeedRoot " /tmp/seed.out && grep -q "^--- PASS: TestSeedNodeReadOnlyFlags " /tmp/seed.out || { echo "FAIL: a seed test did not run or did not pass"; exit 1; }
	if [ -n "${FIRSTBOOT_B:-}" ]; then grep -q "^--- PASS: TestSeedFirstBoot " /tmp/seed.out || { echo "FAIL: the first-boot test did not run or did not pass"; exit 1; }; fi
	grep -q "^FAIL" /tmp/seed.out && exit 1
	exit 0'
