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
#   - the nvram reader against the model of the Android driver: "off" only
#     when the driver gives exactly "false", never "not clear" or "off" when
#     it can give true (ci/nvram-model-diff.py; NVRAM_MODEL_CASES=30000 for
#     the long run of the review)
set -uo pipefail
cd "$(dirname "$0")/.."
fail=0

echo "== gofmt =="
out=$(gofmt -l .)
[ -z "$out" ] || { echo "FAIL: not formatted:"; echo "$out"; fail=1; }

echo "== go vet (default, install and devtest build) =="
go vet ./... || { echo "FAIL: go vet"; fail=1; }
go vet -tags install ./... || { echo "FAIL: go vet -tags install"; fail=1; }
go vet -tags install,devtest ./... || { echo "FAIL: go vet -tags install,devtest"; fail=1; }

echo "== go build (default, install and devtest build) =="
go build -o /dev/null ./cmd/p10inst || { echo "FAIL: go build"; fail=1; }
go build -tags install -o /dev/null ./cmd/p10inst || { echo "FAIL: go build -tags install"; fail=1; }
go build -tags install,devtest -o /dev/null ./cmd/p10inst || { echo "FAIL: go build -tags install,devtest"; fail=1; }

echo "== go test =="
# The install and device-mode tests need the real uc-pxx-bootctl: set
# UC_PXX_BOOTCTL (and UC_PXX_BOOTCTL_EXEC=qemu-aarch64-static for the aarch64
# binary). Without it they are skipped, and the power-cut sweeps do not run.
# LINT_SHORT=1 runs the device-mode sweeps on every 13th write point only.
short=
[ -n "${LINT_SHORT:-}" ] && short=-short
go test $short -timeout 60m -tags install,devtest ./... || { echo "FAIL: go test -tags install,devtest"; fail=1; }
go test -tags install ./internal/device/ || { echo "FAIL: go test -tags install (release build)"; fail=1; }
[ -n "${UC_PXX_BOOTCTL:-}" ] || echo "note: UC_PXX_BOOTCTL not set: the install tests and the power-cut sweeps were skipped"

echo "== python3 -m py_compile =="
while IFS= read -r f; do
	python3 -I -m py_compile "$f" || { echo "FAIL: py_compile $f"; fail=1; }
done < <(find . -name '*.py' -not -path './.git/*')
find . -name __pycache__ -not -path './.git/*' -exec rm -rf {} + 2>/dev/null

echo "== bash -n =="
for f in ci/*.sh image/build.sh image/kbuild.sh image/apk-cache.sh; do
	bash -n "$f" || { echo "FAIL: bash -n $f"; fail=1; }
done

echo "== sh -n (image scripts) =="
for f in image/mkinitramfs.sh image/overlay/init image/overlay/etc/init.d/rcS image/overlay/etc/p10inst/usb-net-up; do
	sh -n "$f" || { echo "FAIL: sh -n $f"; fail=1; }
done

echo "== the two inittabs: the status image keeps the status UI, the install image runs the flow =="
# Apart from comments they differ in the tty1 line only (image/overlay-install, image/build.sh --install).
d=$(diff <(grep -v '^#' image/overlay/etc/inittab) <(grep -v '^#' image/overlay-install/etc/inittab) | grep '^[<>]')
want=$(printf '%s\n' '< tty1::respawn:/usr/bin/p10inst ui --leds' '> tty1::respawn:/usr/bin/p10inst flow --leds')
[ "$d" = "$want" ] || { echo "FAIL: the inittabs differ in more than tty1:"; echo "$d"; fail=1; }
ls image/overlay-install/etc | tr '\n' ' ' | grep -qx 'inittab profile ' ||
	{ echo "FAIL: image/overlay-install holds more than etc/inittab and etc/profile"; fail=1; }

echo "== no decryption key =="
python3 -I ci/check-no-key.py || fail=1

echo "== no eMMC writer in the RAM image sources =="
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
rm -f "$bin"

echo "== nvram reader against the Android driver model =="
python3 -I ci/nvram-model-diff.py || { echo "FAIL: nvram model"; fail=1; }

echo "== check-no-writer: its own failing-input tests =="
python3 -I ci/test-check-no-writer.py || fail=1
find ci -name __pycache__ -exec rm -rf {} + 2>/dev/null

echo "== no vendor files =="
vendor=$(git ls-files --cached --others --exclude-standard |
	grep -E -i '\.(pcdfw|puf|img|simg|ext4)$|(^|/)pcdfw-[^/]*\.bin$|(^|/)lib(webupdate|provdetect|jni_upgrade)\.so$' || true)
[ -z "$vendor" ] || { echo "FAIL: vendor file(s):"; echo "$vendor"; fail=1; }

[ $fail -eq 0 ] && echo "lint OK" || echo "lint FAILED"
exit $fail
