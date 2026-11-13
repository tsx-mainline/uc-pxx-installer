#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# ci/build-send.sh OUT-AMD64 [OUT-ARM64]: the release build of the host sender
# uc-pxx-send (cmd/uc-pxx-send + internal/sender), the recipe of the other
# release builds: golang:1.27.2-alpine (sha256:0a761904…), static, no cgo,
# -trimpath, stripped, no VCS stamp. linux/amd64 (the build host) and,
# with a second argument, linux/arm64. Run it inside that
# image from the repo root, twice in two clean clones: the binaries must be
# byte-identical (reproducible). The sender is a host tool: it is in no RAM
# image, no payload and no package.
set -eu
out=${1:?usage: ci/build-send.sh OUT-AMD64 [OUT-ARM64]}
export GOOS=linux CGO_ENABLED=0 GOFLAGS=-buildvcs=false
GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o "$out" ./cmd/uc-pxx-send
sha256sum "$out"
if [ -n "${2:-}" ]; then
	GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o "$2" ./cmd/uc-pxx-send
	sha256sum "$2"
fi
