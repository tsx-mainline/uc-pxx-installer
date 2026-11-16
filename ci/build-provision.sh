#!/bin/sh
# ci/build-provision.sh OUT: the release build of uc-pxx-provision (the
# first-boot provisioning of an R23 base slot, DESIGN-R23 §3; mainline-rc-10),
# the recipe of ci/build-xfer.sh: golang:1.27.2-alpine (sha256:0a761904…),
# static arm64, no cgo, -trimpath, stripped, and the Go build id blanked
# (-buildid=): uc-pxx-step2's clone gate pins this binary (internal/step2
# gate-keeper.json), so its bytes must not depend on the data that is made
# after it (the mainline gate, step2set, the keeper pin: ci/check-fixedpoint.sh).
# Its bytes DO depend on internal/clone gate-profiles.json (the release's
# profiles and the uc-pxx-xfer pin, made before it). Run it inside that
# image, from the repo root, twice in two clean clones: the two binaries must
# be byte-identical. The binary is checked by ci/check-no-writer.py
# --provision. It belongs only in an R23 base STEP-2 rootfs at
# /usr/libexec/uc-pxx-provision/uc-pxx-provision (root, 0700 folder).
set -eu
out=${1:?usage: ci/build-provision.sh OUT}
export GOOS=linux GOARCH=${GOARCH:-arm64} CGO_ENABLED=0
go build -trimpath -ldflags "-s -w -buildid=" -o "$out" ./cmd/uc-pxx-provision
sha256sum "$out"
