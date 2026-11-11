#!/bin/sh
# ci/build-xfer.sh OUT: the release build of uc-pxx-xfer (the step-2 USB
# transport, STEP2-USB-TRANSPORT-2026-11-11.md), the recipe of ci/build-step2.sh:
# golang:1.27.2-alpine (sha256:0a761904…), static arm64, no cgo, -trimpath,
# stripped. Run it inside that image, from the repo root, twice in two clean
# clones: the two binaries must be byte-identical (reproducible). The binary
# is checked by ci/check-no-writer.py --xfer. It belongs only in a STEP-2
# payload (the both-slot rules refuse it elsewhere).
set -eu
out=${1:?usage: ci/build-xfer.sh OUT}
export GOOS=linux GOARCH=${GOARCH:-arm64} CGO_ENABLED=0
go build -tags xfer -trimpath -ldflags "-s -w" -o "$out" ./cmd/uc-pxx-xfer
sha256sum "$out"
