#!/bin/bash
# Lint and tests for the whole repo. They need no hardware, no vendor file and
# no key. Run them locally the same way as CI does: ci/lint.sh
#   - gofmt, go vet and the Go unit tests (the tests build their own firmware
#     container with a made-up key)
#   - every *.py file must byte-compile, every bash script must pass bash -n
#   - the image scripts must pass sh -n (they run in busybox sh)
#   - the repo holds no decryption key (ci/check-no-key.py)
#   - the RAM image sources hold no eMMC writer, the install side is
#     reachable only in the install build, and the devtest code only in the
#     devtest build (ci/check-no-writer.py, also on the three built binaries)
#   - the repo holds no vendor file (firmware, partition images, vendor libraries)
#   - tools/fw-manifest.py: its offline AVB checks refuse every changed,
#     truncated or wrongly signed firmware (tools/test-fw-manifest.py, with
#     its own test signer and test keys)
#   - host/p10-enter (the entry CLI) against fake rkdeveloptool, fastboot and
#     ssh: no write but the BCB, never LBA 49156, no transfer cut short. The
#     tests pass the fakes as explicit paths and give the tool a PATH with
#     only the fakes. On a host where a unit is connected, run this script in
#     a container without USB devices anyway (no --device, no --privileged).
#     The mutation check of the guards (host/tests/mutate.py) is slow and is
#     not part of this script; run it before a review.
#   - the nvram reader against the model of the Android driver: "off" only
#     when the driver gives exactly "false", never "not clear" or "off" when
#     it can give true (ci/nvram-model-diff.py; NVRAM_MODEL_CASES=30000 for
#     the long run of the review)
#
# Two tiers (FASTTEST.md):
#   ci/lint.sh             the full tier: every cut, kill and fault point of
#                          every sweep. For review heads and the release.
#   LINT_TIER=fast ci/lint.sh   the fast tier (go test -short), for the commits
#                          in between: every check of the full tier, and every
#                          unit test, but the device-mode, install and flow
#                          sweeps cut only at a fixed sample of points (every
#                          13th write, both ends, the writes around each step
#                          change and each A/B write) and the repair contract
#                          runs one write-2 fault per case. LINT_SHORT=1 is the
#                          old name of the fast tier.
# The sweep points are parallel subtests: LINT_PARALLEL (default 8) of them
# run at once in each package (LINT_FLOW_PARALLEL, default 16, in
# cmd/p10inst, whose points mostly wait for the UI). go test runs in the
# background while the other checks run. The fake units go to a RAM folder under
# /dev/shm when it has LINT_RAM_MIN_GB (default 4) free and allows exec
# (LINT_RAMTMP=0: never; the folder is removed at the end); go test runs with
# GOMEMLIMIT (default 2GiB per test process) so a package of parallel points
# stays in a known memory budget.
set -uo pipefail
cd "$(dirname "$0")/.."
fail=0

t0=$(date +%s)
# each stage line shows the seconds since the start
stage() { echo "== $* == ($(($(date +%s) - t0)) s)"; }

tier=${LINT_TIER:-full}
[ -n "${LINT_SHORT:-}" ] && tier=fast
case "$tier" in
full | fast) ;;
*) echo "FAIL: LINT_TIER must be full or fast, not $tier"; exit 2 ;;
esac
stage "tier: $tier"
ramtmp=
if [ "${LINT_RAMTMP:-1}" != 0 ] && [ -d /dev/shm ] && [ -w /dev/shm ]; then
	free_kb=$(df -Pk /dev/shm | awk 'NR == 2 { print $4 }')
	if [ "${free_kb:-0}" -ge $(( ${LINT_RAM_MIN_GB:-4} * 1024 * 1024 )) ] &&
		! findmnt -no OPTIONS --target /dev/shm 2>/dev/null | tr , '\n' | grep -qx noexec; then
		ramtmp=$(mktemp -d /dev/shm/p10inst-lint.XXXXXX) || ramtmp=
	fi
fi
if [ -n "$ramtmp" ]; then
	trap 'rm -rf "$ramtmp"' EXIT # after the go test of the background has ended (wait below)
	echo "temporary folder of the tests: $ramtmp (RAM)"
else
	echo "temporary folder of the tests: ${TMPDIR:-/tmp}"
fi

stage "gofmt"
out=$(gofmt -l .)
[ -z "$out" ] || { echo "FAIL: not formatted:"; echo "$out"; fail=1; }

stage "go vet (default, install and devtest build)"
go vet ./... || { echo "FAIL: go vet"; fail=1; }
go vet -tags install ./... || { echo "FAIL: go vet -tags install"; fail=1; }
go vet -tags install,devtest ./... || { echo "FAIL: go vet -tags install,devtest"; fail=1; }
# the step-2 binary (cmd/uc-pxx-step2: go build -tags install,step2) and its devtest build
go vet -tags install,step2 ./... || { echo "FAIL: go vet -tags install,step2"; fail=1; }
go vet -tags install,step2,devtest ./... || { echo "FAIL: go vet -tags install,step2,devtest"; fail=1; }

stage "go build (default, install and devtest build)"
go build -o /dev/null ./cmd/p10inst || { echo "FAIL: go build"; fail=1; }
go build -tags install -o /dev/null ./cmd/p10inst || { echo "FAIL: go build -tags install"; fail=1; }
go build -tags install,devtest -o /dev/null ./cmd/p10inst || { echo "FAIL: go build -tags install,devtest"; fail=1; }
go build -tags install,step2 -o /dev/null ./cmd/uc-pxx-step2 || { echo "FAIL: go build -tags install,step2 ./cmd/uc-pxx-step2"; fail=1; }

stage "go test ($tier tier)"
# The install and device-mode tests need the real uc-pxx-bootctl: set
# UC_PXX_BOOTCTL (and UC_PXX_BOOTCTL_EXEC=qemu-aarch64-static for the aarch64
# binary). Without it they are skipped, and the power-cut sweeps do not run.
short=
[ "$tier" = fast ] && short=-short
gotest() {
	if [ -n "$ramtmp" ]; then
		TMPDIR=$ramtmp GOMEMLIMIT=${GOMEMLIMIT:-2GiB} go test "$@"
	else
		GOMEMLIMIT=${GOMEMLIMIT:-2GiB} go test "$@"
	fi
}
# go test runs in the background while the checks below run; its output is
# printed (and its result counted) at the stage "go test: the result".
gtlog=$(mktemp)
# 120m, not 60m: on the build host the lints of several agents run in parallel (the
# owner's policy, 2026-11-01).
# cmd/p10inst (the on-screen flow) runs on its own with more parallel points
# (LINT_FLOW_PARALLEL, default 16): its sweep points mostly wait for the UI.
(
	pkgs=$(go list -tags install,devtest ./... | grep -v '/cmd/p10inst$')
	gotest $short -parallel "${LINT_FLOW_PARALLEL:-16}" -timeout 120m -tags install,devtest ./cmd/p10inst/ >"$gtlog.flow" 2>&1 &
	fpid=$!
	gotest $short -parallel "${LINT_PARALLEL:-8}" -timeout 120m -tags install,devtest $pkgs || { echo "FAIL: go test -tags install,devtest"; echo 1 >"$gtlog.rc"; }
	wait "$fpid" || { cat "$gtlog.flow"; echo "FAIL: go test -tags install,devtest ./cmd/p10inst/"; echo 1 >"$gtlog.rc"; }
	grep -E '^(ok|FAIL|---)' "$gtlog.flow" | head -5
	rm -f "$gtlog.flow"
	gotest -tags install ./internal/device/ || { echo "FAIL: go test -tags install (release build)"; echo 1 >"$gtlog.rc"; }
	# step 2 (internal/step2, the slot side of internal/device): the devtest build with the sweeps, and the
	# release build's own test (the test options are refused)
	gotest $short -parallel "${LINT_PARALLEL:-8}" -timeout 120m -tags install,step2,devtest ./internal/step2/ ./internal/device/ ||
		{ echo "FAIL: go test -tags install,step2,devtest"; echo 1 >"$gtlog.rc"; }
	gotest -tags install,step2 ./internal/step2/ || { echo "FAIL: go test -tags install,step2 (release build)"; echo 1 >"$gtlog.rc"; }
	# the install build's own tests of cmd/p10inst (mainline_dispatch_test.go: the dispatch and the flag scanner without devtest)
	gotest -tags install ./cmd/p10inst/ || { echo "FAIL: go test -tags install ./cmd/p10inst/ (release build)"; echo 1 >"$gtlog.rc"; }
	# the status build's own tests of cmd/p10inst (loader_test.go: the download-mode restart)
	gotest ./cmd/p10inst/ || { echo "FAIL: go test ./cmd/p10inst/ (status build)"; echo 1 >"$gtlog.rc"; }
) >"$gtlog" 2>&1 &
gtpid=$!

stage "python3 -m py_compile"
while IFS= read -r f; do
	python3 -I -m py_compile "$f" || { echo "FAIL: py_compile $f"; fail=1; }
done < <(find . -name '*.py' -not -path './.git/*')
find . -name __pycache__ -not -path './.git/*' -exec rm -rf {} + 2>/dev/null

stage "bash -n"
for f in ci/*.sh image/build.sh image/kbuild.sh image/apk-cache.sh; do
	bash -n "$f" || { echo "FAIL: bash -n $f"; fail=1; }
done

stage "sh -n (image scripts)"
for f in image/mkinitramfs.sh image/overlay/init image/overlay/etc/init.d/rcS image/overlay/etc/p10inst/usb-net-up; do
	sh -n "$f" || { echo "FAIL: sh -n $f"; fail=1; }
done

stage "the two inittabs: the status image keeps the status UI, the install image runs the flow"
# Apart from comments they differ in the tty1 line only (image/overlay-install, image/build.sh --install).
# No diff output format: the busybox diff of the CI image is unified, GNU diff is not.
st=$(grep -v -e '^#' -e '^$' image/overlay/etc/inittab)
in=$(grep -v -e '^#' -e '^$' image/overlay-install/etc/inittab)
[ "$(echo "$st" | grep -v '^tty1:')" = "$(echo "$in" | grep -v '^tty1:')" ] &&
	[ "$(echo "$st" | grep '^tty1:')" = 'tty1::respawn:/usr/bin/p10inst ui --leds' ] &&
	[ "$(echo "$in" | grep '^tty1:')" = 'tty1::respawn:/usr/bin/p10inst flow --leds' ] ||
	{ echo "FAIL: the inittabs differ in more than tty1:"; diff image/overlay/etc/inittab image/overlay-install/etc/inittab; fail=1; }
ls image/overlay-install/etc | tr '\n' ' ' | grep -qx 'inittab profile ' ||
	{ echo "FAIL: image/overlay-install holds more than etc/inittab and etc/profile"; fail=1; }

stage "the image kernel is the reviewed public one"
# image/build.sh refuses any other kernel (DEVMODE-R4.md §15); the pin is one full sha, used by the check.
[ "$(grep -cE '^REVIEWED_KERNEL_REV=[0-9a-f]{40}$' image/build.sh)" = 1 ] &&
	grep -q '\[ "\$REV" = "\$REVIEWED_KERNEL_REV" \]' image/build.sh ||
	{ echo "FAIL: image/build.sh must pin REVIEWED_KERNEL_REV (one full sha) and check it"; fail=1; }

stage "no decryption key"
python3 -I ci/check-no-key.py || fail=1

stage "no eMMC writer in the RAM image sources"
python3 -I ci/check-no-writer.py || fail=1
bin=$(mktemp)
if go build -trimpath -ldflags "-s -w" -o "$bin" ./cmd/p10inst; then
	python3 -I ci/check-no-writer.py --binary "$bin" || fail=1
fi
if go build -tags install -trimpath -ldflags "-s -w" -o "$bin" ./cmd/p10inst; then
	python3 -I ci/check-no-writer.py --release-install "$bin" || fail=1
fi
if go build -tags install,devtest -trimpath -ldflags "-s -w" -o "$bin" ./cmd/p10inst; then
	python3 -I ci/check-no-writer.py --devtest "$bin" || fail=1
fi
if go build -tags install,step2 -trimpath -ldflags "-s -w" -o "$bin" ./cmd/uc-pxx-step2; then
	python3 -I ci/check-no-writer.py --step2 "$bin" || fail=1
fi
rm -f "$bin"

stage "nvram reader against the Android driver model"
python3 -I ci/nvram-model-diff.py || { echo "FAIL: nvram model"; fail=1; }

stage "check-no-writer: its own failing-input tests"
python3 -I ci/test-check-no-writer.py || fail=1
find ci -name __pycache__ -exec rm -rf {} + 2>/dev/null

stage "fw-manifest.py: its AVB checks (made-up firmwares; the real ones only with UC_PXX_UNPACK and UC_PXX_FW)"
python3 -I tools/test-fw-manifest.py || { echo "FAIL: tools/test-fw-manifest.py"; fail=1; }
stage "host/p10-enter with fake rkdeveloptool, fastboot and ssh"
python3 -I -c 'import sys; compile(open(sys.argv[1]).read(), sys.argv[1], "exec")' host/p10-enter ||
	{ echo "FAIL: host/p10-enter does not compile"; fail=1; }
python3 -I host/tests/test_p10_enter.py || { echo "FAIL: host/p10-enter tests"; fail=1; }

stage "go test: the result"
wait "$gtpid"
cat "$gtlog"
[ -e "$gtlog.rc" ] && fail=1
rm -f "$gtlog" "$gtlog.rc"
[ -n "${UC_PXX_BOOTCTL:-}" ] || echo "note: UC_PXX_BOOTCTL not set: the install tests and the power-cut sweeps were skipped"

stage "no vendor files"
vendor=$(git ls-files --cached --others --exclude-standard |
	grep -E -i '\.(pcdfw|puf|img|simg|ext4)$|(^|/)pcdfw-[^/]*\.bin$|(^|/)lib(webupdate|provdetect|jni_upgrade)\.so$' || true)
[ -z "$vendor" ] || { echo "FAIL: vendor file(s):"; echo "$vendor"; fail=1; }

stage "end"
[ $fail -eq 0 ] && echo "lint OK" || echo "lint FAILED"
exit $fail
