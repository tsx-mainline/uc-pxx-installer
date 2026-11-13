#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# web/test/lan-site.sh OUT URL: a NON-SERVED test copy of web/site for a LAN
# test of the page's payload download (owner 2026-11-12): it copies web/site to
# OUT and sets STEP2.PAYLOAD_URL in OUT/js/config.js to URL (a folder
# uc-pxx-mainline served on the LAN, with CORS for the test page's origin).
# Nothing else changes: the flags, the pins and every other value are those of
# web/site. The served page (the Pages workflow uploads web/site only) keeps
# the owner's placeholder (null). Never publish OUT.
set -eu
out=${1:?usage: web/test/lan-site.sh OUT URL}
url=${2:?usage: web/test/lan-site.sh OUT URL}
case "$url" in http://*/|https://*/) ;; *) echo "URL must be http(s) and end in /" >&2; exit 2 ;; esac
here=$(cd "$(dirname "$0")/.." && pwd)
[ ! -e "$out" ] || { echo "$out exists" >&2; exit 2; }
cp -a "$here/site" "$out"
n=$(grep -c '^  PAYLOAD_URL: null,$' "$out/js/config.js")
[ "$n" = 1 ] || { echo "config.js: PAYLOAD_URL line not found once" >&2; exit 1; }
esc=$(printf '%s' "$url" | sed 's/[\/&|]/\\&/g')
sed -i "s|^  PAYLOAD_URL: null,\$|  PAYLOAD_URL: \"$esc\", // TEST ONLY (web/test/lan-site.sh): never served|" "$out/js/config.js"
grep -n 'PAYLOAD_URL: "' "$out/js/config.js"
echo "test site in $out (NOT served; never publish it)"
