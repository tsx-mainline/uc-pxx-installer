#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# Run the page tests in the pinned Playwright image (Node 24, Chromium 1248).
# Step 1 (npm ci) needs the npm registry. Step 2 (the tests) runs with no
# network and no USB device in the container: the tests use the fake unit of
# fake/fakeusb.js only.
set -eu
cd "$(dirname "$0")"
IMG=mcr.microsoft.com/playwright@sha256:47b510dd9230bf0b9bfc9b20102241e577ea7a99334ab4c3ca19f19770e9b8ea
U="$(id -u):$(id -g)"
docker run --rm -u "$U" -e HOME=/tmp -v "$PWD/..:/w" -w /w/test "$IMG" \
	npm ci --ignore-scripts --no-audit --no-fund
docker run --rm --network none -u "$U" -e HOME=/tmp -v "$PWD/..:/w" -w /w/test "$IMG" \
	sh -c 'node --test unit/*.test.mjs && npx playwright test'
