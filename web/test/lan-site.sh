#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# web/test/lan-site.sh OUT DIR: a NON-SERVED test copy of web/site for a
# supervised unit-2 run of the RC-10 page (DESIGN v3 §10 step 3) with test
# files: it copies web/site to OUT, copies the files of DIR into it, sets in
# OUT/js/config.js the pins of those files and turns on the mainline goal and
# the system and finish steps (each changed line ends in a TEST ONLY comment).
# DIR holds:
#   images/<file>.img                   the install image (one)
#   base/uc-pxx-mainline/               manifest.json, boot-template.img, rootfs.ext4.zst
#   finish/                             finish.json, uc-pxx-step2-finish, uc-pxx-bootctl-inst
#   pins.env                            IMAGE_SHA256 IMAGE_SIZE IMAGE_TOKEN BASE_MANIFEST_SHA256
#                                       FINISH_LIST_SHA256 (and, optional, PROVISION_URL; for
#                                       the cable mode BUNDLE_RELEASE with BUNDLE_<ID>_SHA256 and
#                                       _SIZE: the bundle uc-pxx-profile-<id>-<release>.tar)
# The page then fetches everything from its own origin (connect-src 'self').
# The served page (the Pages workflow uploads web/site only) keeps the
# placeholders and the flags false (RULES item G). Never publish OUT.
set -eu
out=${1:?usage: web/test/lan-site.sh OUT DIR}
dir=${2:?usage: web/test/lan-site.sh OUT DIR}
here=$(cd "$(dirname "$0")/.." && pwd)
[ ! -e "$out" ] || { echo "$out exists" >&2; exit 2; }
[ -f "$dir/pins.env" ] || { echo "$dir/pins.env is missing" >&2; exit 2; }
IMAGE_SHA256='' IMAGE_SIZE='' IMAGE_TOKEN='' BASE_MANIFEST_SHA256='' FINISH_LIST_SHA256='' PROVISION_URL='' BUNDLE_RELEASE=''
BUNDLE_KIOSK_HA_SHA256='' BUNDLE_KIOSK_HA_SIZE='' BUNDLE_KIOSK_SHA256='' BUNDLE_KIOSK_SIZE=''
# pins.env is read as KEY=VALUE lines (no shell code); a value has no quote,
# no backslash, no space
while IFS= read -r line || [ -n "$line" ]; do
	case "$line" in ''|'#'*) continue ;; esac
	k=${line%%=*} v=${line#*=}
	v=${v#\'}; v=${v%\'}
	case "$v" in *[\"\'\\\ \|\&\;\$\`]*) echo "pins.env: $k has a character that is not allowed" >&2; exit 2 ;; esac
	case "$k" in
	IMAGE_SHA256|IMAGE_SIZE|IMAGE_TOKEN|BASE_MANIFEST_SHA256|FINISH_LIST_SHA256|PROVISION_URL|BUNDLE_RELEASE|BUNDLE_KIOSK_HA_SHA256|BUNDLE_KIOSK_HA_SIZE|BUNDLE_KIOSK_SHA256|BUNDLE_KIOSK_SIZE)
		eval "$k=\$v" ;;
	*) echo "pins.env: unknown key $k" >&2; exit 2 ;;
	esac
done < "$dir/pins.env"
for b in KIOSK_HA KIOSK; do
	eval "bs=\$BUNDLE_${b}_SHA256 bz=\$BUNDLE_${b}_SIZE"
	[ -z "$bs$bz" ] && continue
	printf '%s' "$BUNDLE_RELEASE" | grep -Eqx '[A-Za-z0-9][A-Za-z0-9._+-]{0,63}' || { echo "a bundle pin needs BUNDLE_RELEASE (the release in uc-pxx-profile-<id>-<release>.tar)" >&2; exit 2; }
	printf '%s' "$bs" | grep -Eqx '[0-9a-f]{64}' || { echo "BUNDLE_${b}_SHA256 must be sha256 hex" >&2; exit 2; }
	printf '%s' "$bz" | grep -Eqx '[1-9][0-9]*' || { echo "BUNDLE_${b}_SIZE must be a number" >&2; exit 2; }
done
for v in "$IMAGE_SHA256" "$BASE_MANIFEST_SHA256" "$FINISH_LIST_SHA256"; do
	printf '%s' "$v" | grep -Eqx '[0-9a-f]{64}' || { echo "the pins must be sha256 hex" >&2; exit 2; }
done
printf '%s' "$IMAGE_SIZE" | grep -Eqx '[1-9][0-9]*' || { echo "IMAGE_SIZE must be a number" >&2; exit 2; }
printf '%s' "$IMAGE_TOKEN" | grep -Eqx '[A-Za-z0-9._-]{1,15}' || { echo "bad IMAGE_TOKEN" >&2; exit 2; }
case "$PROVISION_URL" in ''|http://*/|https://*/) ;; *) echo "PROVISION_URL must be http(s) and end in /" >&2; exit 2 ;; esac
# the installer's url limits (record §6.10 "Built", handover.CheckBaseURL): a
# host, no user info, no query or fragment, printable ASCII, at most 512 bytes
if [ -n "$PROVISION_URL" ]; then
	printf '%s' "$PROVISION_URL" | LC_ALL=C grep -Eqx '[!-~]+' || { echo "PROVISION_URL must be printable ASCII without spaces" >&2; exit 2; }
	case "$PROVISION_URL" in *'?'*|*'#'*|*'\'*) echo "PROVISION_URL has a query, a fragment or a backslash" >&2; exit 2 ;; esac
	! printf '%s' "$PROVISION_URL" | grep -Eq '%([^0-9A-Fa-f]|[0-9A-Fa-f][^0-9A-Fa-f]|[0-9A-Fa-f]?$)' || { echo "PROVISION_URL has a % that is not an escape" >&2; exit 2; }
	auth=${PROVISION_URL#*://}; auth=${auth%%/*}
	case "$auth" in ''|*@*) echo "PROVISION_URL needs a host and no user info" >&2; exit 2 ;; esac
	[ "$(printf '%s' "$PROVISION_URL" | wc -c)" -le 512 ] || { echo "PROVISION_URL is longer than 512 bytes" >&2; exit 2; }
fi
img=$(cd "$dir/images" && ls -- *.img)
[ "$(printf '%s\n' "$img" | wc -l)" = 1 ] || { echo "$dir/images must hold one .img" >&2; exit 2; }
printf '%s' "$img" | grep -Eqx '[A-Za-z0-9][A-Za-z0-9._-]*\.img' || { echo "bad image file name" >&2; exit 2; }
sha() { sha256sum "$1" | cut -d' ' -f1; }
[ "$(sha "$dir/images/$img")" = "$IMAGE_SHA256" ] || { echo "the image is not IMAGE_SHA256" >&2; exit 1; }
[ "$(sha "$dir/base/uc-pxx-mainline/manifest.json")" = "$BASE_MANIFEST_SHA256" ] || { echo "the base manifest is not its pin" >&2; exit 1; }
[ "$(sha "$dir/finish/finish.json")" = "$FINISH_LIST_SHA256" ] || { echo "finish.json is not its pin" >&2; exit 1; }
cp -a "$here/site" "$out"
mkdir -p "$out/images" "$out/base/uc-pxx-mainline" "$out/finish"
cp "$dir/images/$img" "$out/images/"
for f in manifest.json boot-template.img rootfs.ext4.zst; do cp "$dir/base/uc-pxx-mainline/$f" "$out/base/uc-pxx-mainline/"; done
for f in finish.json uc-pxx-step2-finish uc-pxx-bootctl-inst; do cp "$dir/finish/$f" "$out/finish/"; done
c=$out/js/config.js
once() { # once LINE: the line is in config.js exactly once
	n=$(grep -cxF "$1" "$c" || true)
	[ "$n" = 1 ] || { echo "config.js: the line '$1' is not there once" >&2; exit 1; }
}
for l in '    sha256: "RC10-IMAGE-SHA256-PLACEHOLDER",' '    size: null,' '    token: "RC10-PLACEHOLDER",' \
	'    file: "uc-pxx-inst-ucp10-rc10.img",' '  manifest_sha256: "BASE-MANIFEST-SHA256-PLACEHOLDER",' \
	'export const MAINLINE_INSTALL_APPROVED = false;' 'export const STEP2_USB_ENABLED = false;' \
	'export const FINISH_USB_ENABLED = false;' 'export const PROVISION = Object.freeze({ PROVISION_URL: null, STATUS_EVERY_S: 1 });' \
	'    Object.freeze({ label: "the RC-9 finish set", sha256: "b6ed2a624dfd1af115f589de1a0b4a22d24f2df14d2ee0aceca2f3375d4b5ca2" }),'; do
	once "$l"
done
T='// TEST ONLY (web/test/lan-site.sh): never served'
sed -i -e "s|^    sha256: \"RC10-IMAGE-SHA256-PLACEHOLDER\",\$|    sha256: \"$IMAGE_SHA256\", $T|" \
	-e "s|^    size: null,\$|    size: $IMAGE_SIZE, $T|" \
	-e "s|^    token: \"RC10-PLACEHOLDER\",\$|    token: \"$IMAGE_TOKEN\", $T|" \
	-e "s|^    file: \"uc-pxx-inst-ucp10-rc10.img\",\$|    file: \"$img\", $T|" \
	-e "s|^  manifest_sha256: \"BASE-MANIFEST-SHA256-PLACEHOLDER\",\$|  manifest_sha256: \"$BASE_MANIFEST_SHA256\", $T|" \
	-e "s|^export const MAINLINE_INSTALL_APPROVED = false;\$|export const MAINLINE_INSTALL_APPROVED = true; $T|" \
	-e "s|^export const STEP2_USB_ENABLED = false;\$|export const STEP2_USB_ENABLED = true; $T|" \
	-e "s|^export const FINISH_USB_ENABLED = false;\$|export const FINISH_USB_ENABLED = true; $T|" \
	-e "s|^    Object.freeze({ label: \"the RC-9 finish set\", sha256: \"b6ed2a624dfd1af115f589de1a0b4a22d24f2df14d2ee0aceca2f3375d4b5ca2\" }),\$|    Object.freeze({ label: \"TEST ONLY: the finish set\", sha256: \"$FINISH_LIST_SHA256\" }), $T|" "$c"
if [ -n "$PROVISION_URL" ]; then
	esc=$(printf '%s' "$PROVISION_URL" | sed 's/[\/&|]/\\&/g')
	sed -i "s|^export const PROVISION = Object.freeze({ PROVISION_URL: null, STATUS_EVERY_S: 1 });\$|export const PROVISION = Object.freeze({ PROVISION_URL: \"$esc\", STATUS_EVERY_S: 1 }); $T|" "$c"
fi
bundle() { # bundle ID SHA SIZE: the bundle pin of one system (bundle.py's name)
	old=$(grep -n "bundle: Object.freeze({ file: \"uc-pxx-profile-$1-RELEASE-PLACEHOLDER.tar\", size: null, sha256: \"BUNDLE-" "$c" | cut -d: -f1)
	[ "$(printf '%s\n' "$old" | grep -c .)" = 1 ] || { echo "config.js: the bundle line of $1 is not there once" >&2; exit 1; }
	sed -i "${old}s|file: \"uc-pxx-profile-$1-RELEASE-PLACEHOLDER.tar\", size: null, sha256: \"BUNDLE-[A-Z-]*-SHA256-PLACEHOLDER\", url: null }) }),\$|file: \"uc-pxx-profile-$1-$BUNDLE_RELEASE.tar\", size: $3, sha256: \"$2\", url: null }) }), $T|" "$c"
}
[ -z "$BUNDLE_KIOSK_HA_SHA256" ] || bundle kiosk-ha "$BUNDLE_KIOSK_HA_SHA256" "$BUNDLE_KIOSK_HA_SIZE"
[ -z "$BUNDLE_KIOSK_SHA256" ] || bundle kiosk "$BUNDLE_KIOSK_SHA256" "$BUNDLE_KIOSK_SIZE"
grep -n 'TEST ONLY' "$c"
echo "test site in $out (NOT served; never publish it)"
