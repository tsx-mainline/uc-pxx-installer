#!/bin/sh
# ci/build-step2.sh OUT: the release build of uc-pxx-step2 (mainline step 2,
# MAINLINE-STEP2-REST-2026-11-10.md), the same recipe as the release builds of
# p10inst: golang:1.27.2-alpine (sha256:0a761904…), static arm64, no cgo,
# -trimpath, stripped. Run it inside that image, from the repo root, twice in
# two clean clones: the two binaries must be byte-identical (reproducible).
# The binary is checked by ci/check-no-writer.py --step2. It runs only from
# a slot boot of a reviewed both-slot root file system; it is never part of a
# RAM image.
set -eu
out=${1:?usage: ci/build-step2.sh OUT}
export GOOS=linux GOARCH=arm64 CGO_ENABLED=0
go build -tags install,step2 -trimpath -ldflags "-s -w" -o "$out" ./cmd/uc-pxx-step2
sha256sum "$out"
