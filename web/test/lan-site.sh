#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# web/test/lan-site.sh OUT URL [STEP2_MANIFEST_SHA256 FINISH_LIST_SHA256]: a
# NON-SERVED test copy of web/site for a LAN test of the page's payload download
# (owner 2026-11-12): it copies web/site to OUT and sets STEP2.PAYLOAD_URL in
# OUT/js/config.js to URL (a folder uc-pxx-mainline served on the LAN, with CORS
# for the test page's origin). With the two shas (mainline-rc-7, the unit-2
# end-user e2e test; owner 2026-11-13: the page is the end-user path) it also
# turns on the step-2 and the finish sections in the copy (STEP2_USB_ENABLED,
# FINISH_USB_ENABLED) and pins exactly that payload manifest and that finish
# list there, as ci/xfer-guest/driver.cjs does in flight. Nothing else changes:
# MAINLINE_INSTALL_APPROVED, the images and every other value are those of
# web/site. The served page (the Pages workflow uploads web/site only) keeps the
# owner's placeholder (null), the flags false and the pin lists empty. Never
# publish OUT.
set -eu
out=${1:?usage: web/test/lan-site.sh OUT URL [STEP2_MANIFEST_SHA256 FINISH_LIST_SHA256]}
url=${2:?usage: web/test/lan-site.sh OUT URL [STEP2_MANIFEST_SHA256 FINISH_LIST_SHA256]}
s2=${3:-} fl=${4:-}
case "$url" in http://*/|https://*/) ;; *) echo "URL must be http(s) and end in /" >&2; exit 2 ;; esac
if [ -n "$s2" ] || [ -n "$fl" ]; then
	for v in "$s2" "$fl"; do
		printf '%s' "$v" | grep -Eqx '[0-9a-f]{64}' || { echo "the two pins must be sha256 hex" >&2; exit 2; }
	done
fi
here=$(cd "$(dirname "$0")/.." && pwd)
[ ! -e "$out" ] || { echo "$out exists" >&2; exit 2; }
cp -a "$here/site" "$out"
c=$out/js/config.js
once() { # once LINE: the line is in config.js exactly once
	n=$(grep -cxF "$1" "$c" || true)
	[ "$n" = 1 ] || { echo "config.js: the line '$1' is not there once" >&2; exit 1; }
}
once '  PAYLOAD_URL: null,'
esc=$(printf '%s' "$url" | sed 's/[\/&|]/\\&/g')
sed -i "s|^  PAYLOAD_URL: null,\$|  PAYLOAD_URL: \"$esc\", // TEST ONLY (web/test/lan-site.sh): never served|" "$c"
if [ -n "$s2" ]; then
	for l in 'export const STEP2_USB_ENABLED = false;' 'export const FINISH_USB_ENABLED = false;' '  PAYLOADS: Object.freeze([]),' \
		'export const FINISH = Object.freeze({ LISTS: Object.freeze([]), STATUS_EVERY_S: 2 });'; do
		once "$l"
	done
	sed -i -e 's|^export const STEP2_USB_ENABLED = false;$|export const STEP2_USB_ENABLED = true; // TEST ONLY (web/test/lan-site.sh): never served|' \
		-e 's|^export const FINISH_USB_ENABLED = false;$|export const FINISH_USB_ENABLED = true; // TEST ONLY (web/test/lan-site.sh): never served|' \
		-e "s|^  PAYLOADS: Object.freeze(\[\]),\$|  PAYLOADS: Object.freeze([{ label: \"TEST ONLY: the end-user payload\", manifest_sha256: \"$s2\" }]),|" \
		-e "s|^export const FINISH = Object.freeze({ LISTS: Object.freeze(\[\]), STATUS_EVERY_S: 2 });\$|export const FINISH = Object.freeze({ LISTS: Object.freeze([{ label: \"TEST ONLY: the finish list\", sha256: \"$fl\" }]), STATUS_EVERY_S: 2 });|" "$c"
fi
grep -nE 'PAYLOAD_URL: "|_USB_ENABLED = true|manifest_sha256: "|LISTS: Object.freeze\(\[\{' "$c"
echo "test site in $out (NOT served; never publish it)"
