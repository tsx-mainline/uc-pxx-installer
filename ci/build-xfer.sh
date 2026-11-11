#!/bin/sh
# ci/build-xfer.sh OUT: the release build of uc-pxx-xfer (the USB interface of
# every mainline build, STEP2-USB-TRANSPORT-2026-11-11.md; mainline-rc-4), the
# recipe of ci/build-step2.sh: golang:1.27.2-alpine (sha256:0a761904…), static
# arm64, no cgo, -trimpath, stripped, and the Go build id blanked (-buildid=):
# the gate pins this binary (the USB set), so its bytes must not depend on the
# gate's data, and the build id hashes every source file of the build
# (ci/check-fixedpoint.sh). Run it inside that image, from the repo root,
# twice in two clean clones: the two binaries must be byte-identical. The
# binary is checked by ci/check-no-writer.py --xfer. It belongs in every
# both-slot payload at /usr/libexec/uc-pxx-xfer/uc-pxx-xfer (the USB set).
set -eu
out=${1:?usage: ci/build-xfer.sh OUT}
export GOOS=linux GOARCH=${GOARCH:-arm64} CGO_ENABLED=0
go build -tags xfer -trimpath -ldflags "-s -w -buildid=" -o "$out" ./cmd/uc-pxx-xfer
sha256sum "$out"
