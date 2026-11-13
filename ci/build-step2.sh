#!/bin/sh
# ci/build-step2.sh OUT [FINISH]: the release build of uc-pxx-step2 (mainline
# step 2, MAINLINE-STEP2-REST-2026-11-10.md), the same recipe as the release
# builds of uc-pxx-inst: golang:1.27.2-alpine (sha256:0a761904…), static arm64,
# no cgo, -trimpath, stripped. With FINISH also the finish executor
# uc-pxx-step2-finish (mainline-rc-4, owner Q-T8 = option 5: -tags
# install,step2,finish, and the Go build id blanked: the USB set's finish list
# pins it, so its bytes must not depend on the gate's data;
# ci/check-fixedpoint.sh; its bytes do depend on the release key pin of
# internal/mainline keypin.go: that is no gate data and no cycle, review NIT-3).
# uc-pxx-step2's Go build id is blanked too (mainline-rc-6): the id hashes the
# sources of every package it links, also code it does not hold (a change of
# the direct path in internal/install moved its bytes), and the STEP-2 payload,
# its marker and step2set pin it.
# Run it inside that image, from the repo root, twice
# in two clean clones: the binaries must be byte-identical (reproducible).
# uc-pxx-step2 is checked by ci/check-no-writer.py --step2, the finish
# executor by --step2-finish. uc-pxx-step2 runs only from a slot boot of a
# reviewed STEP-2 root file system; the finish executor only from
# /run/uc-pxx-finish of a normal one (sealed there by uc-pxx-usb, or staged by
# hand in a supervised session); neither is part of a RAM image.
set -eu
out=${1:?usage: ci/build-step2.sh OUT [FINISH]}
export GOOS=linux GOARCH=arm64 CGO_ENABLED=0
go build -tags install,step2 -trimpath -ldflags "-s -w -buildid=" -o "$out" ./cmd/uc-pxx-step2
sha256sum "$out"
if [ -n "${2:-}" ]; then
	go build -tags install,step2,finish -trimpath -ldflags "-s -w -buildid=" -o "$2" ./cmd/uc-pxx-step2-finish
	sha256sum "$2"
fi
