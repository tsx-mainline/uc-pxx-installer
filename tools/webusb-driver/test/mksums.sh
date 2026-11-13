#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# test/mksums.sh (development only): write SHA256SUMS for the runtime files and
# put its sha256 into the wrapper (SUMS_SHA256). Run after any change of a
# runtime file; the result is part of the reviewed tool.
set -eu
cd "$(dirname "$0")/.."
sha256sum driver.mjs inpage.js package-lock.json package.json pins.json policy.json > SHA256SUMS
s=$(sha256sum < SHA256SUMS | cut -c1-64)
sed -i "s/^SUMS_SHA256 = \"[0-9a-fA-FTBD]*\"\$/SUMS_SHA256 = \"$s\"/" uc-pxx-webdrive
grep -n '^SUMS_SHA256 = ' uc-pxx-webdrive
