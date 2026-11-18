#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# ci/check-fixedpoint.sh: the gate pins uc-pxx-xfer and uc-pxx-step2-finish (the
# USB set and its finish list, mainline-rc-4), so their bytes must not depend on
# the gate's data (internal/mainline gate.go gateDataNote, step2set): else no pin
# could exist. Build both (the release recipe, ci/build-xfer.sh and
# ci/build-step2.sh), change the data of every list in a copy of the tree (a pin
# of each both-slot list, the USB set, the kernels, a reviewed payload, the
# step-2 sets, the end-user payloads), build again and require the same bytes. Both recipes blank the
# Go build id (-buildid=): it hashes every source file of the build, also the
# gate's data that the binary does not hold. Run it inside
# golang:1.27.2-alpine from the repo root: sh ci/check-fixedpoint.sh WORK
#
# mainline-rc-10 R23: uc-pxx-step2's clone gate pins uc-pxx-provision (internal/step2 gate-keeper.json), so
# uc-pxx-provision is built here too: its bytes must not depend on the gate's data, step2set or the keeper pin
# (all made after it). It does hold the clone gate's profiles (internal/clone gate-profiles.json, made before
# it); uc-pxx-xfer and the finish executor must not depend on those either (the second copy, OUTDIR c).
set -eu
W=${1:?usage: ci/check-fixedpoint.sh WORK}
mkdir -p "$W"
export GOOS=linux GOARCH=arm64 CGO_ENABLED=0 GOFLAGS=-buildvcs=false
b() { # b TREE OUTDIR
	(cd "$1" && go build -tags xfer -trimpath -ldflags "-s -w -buildid=" -o "$2/uc-pxx-xfer" ./cmd/uc-pxx-xfer &&
		go build -tags install,step2,finish -trimpath -ldflags "-s -w -buildid=" -o "$2/uc-pxx-step2-finish" ./cmd/uc-pxx-step2-finish &&
		go build -trimpath -ldflags "-s -w -buildid=" -o "$2/uc-pxx-provision" ./cmd/uc-pxx-provision)
}
mkdir -p "$W/a" "$W/b" "$W/tree"
b . "$W/a"
rm -rf "$W/tree" && mkdir -p "$W/tree" && tar --exclude=./.git -cf - . | tar -x -C "$W/tree"
G=$W/tree/internal/mainline/gate.go S=$W/tree/internal/mainline/step2set/step2set.go
# every list of the gate's data changes: one sha of each
for f in bothSlotBootTools bothSlotAutoupdate bothSlotBoard bothSlotUSB bootControlPins bothSlotAPKKeys; do
	grep -q "^var $f = " "$G" || { echo "FAIL: $f is not in gate.go"; exit 1; }
done
sed -i 's/11c2afcd0272f9c989f9825074186a2341d0a33a34e0fe9ebf92cf0081f9efa7/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/;
	s/c706ca5f03bd6e5a6b926e3ec78bfc3c487e7a1ebaf410f41087c7903e3fa346/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb/;
	s/15a610e7079ddad643bdacc0f744f348bef0ee965d2fb4e6d912e848f6e1ced2/cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc/g;
	s/27c62b8516a26535a49a80d0acd8905caa5aee64654f41fadb7b8dc4228b1cd9/9999999999999999999999999999999999999999999999999999999999999999/;
	s/85f5a417e12feabb3c8a52b246ce8b89d6f9c70d2e6994525f69ff307f13f3d7/dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd/;
	s/73867d92083f2f8ab899a26ccda7ef63dfaa0032a938620eda605558958a8041/eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee/' "$G"
# the USB set: every pin of it (the finish list's sha256 is one)
awk 'BEGIN{u=0} /^var bothSlotUSB = /{u=1} u&&/^}/{u=0} {if(u) gsub(/"[0-9a-f]{64}"/, "\"1111111111111111111111111111111111111111111111111111111111111111\""); print}' "$G" > "$G.new" && mv "$G.new" "$G"
# the end-user payloads (mainline-rc-7): every sha of the list
grep -q "^var endUserPayloads = " "$G" || { echo "FAIL: endUserPayloads is not in gate.go"; exit 1; }
# (one more entry first: the list may be empty, mainline-rc-10)
sed -i 's|^var endUserPayloads = \[\]string{|var endUserPayloads = []string{"7777777777777777777777777777777777777777777777777777777777777777", |' "$G"
awk 'BEGIN{u=0} /^var endUserPayloads = /{u=1} u&&/^}/{u=0} {if(u) gsub(/"[0-9a-f]{64}"/, "\"7777777777777777777777777777777777777777777777777777777777777777\""); print}' "$G" > "$G.new" && mv "$G.new" "$G"
grep -q '"7777777777777777' "$G" || { echo "FAIL: the end-user payloads were not changed"; exit 1; }
# a reviewed payload and a reviewed kernel
# (one more entry first: the list may already hold the release's payloads)
sed -i 's|^var reviewedPayloads = \[\]Reviewed{|var reviewedPayloads = []Reviewed{{Version: "x", ManifestSHA256: "2222222222222222222222222222222222222222222222222222222222222222", TemplateSHA256: "3333333333333333333333333333333333333333333333333333333333333333", RootfsSHA256: "4444444444444444444444444444444444444444444444444444444444444444", Slots: "ab"}, |' "$G"
sed -i 's|func reviewedKernels() map\[string\]string { return map\[string\]string{} }|func reviewedKernels() map[string]string { return map[string]string{"x": "5555555555555555555555555555555555555555555555555555555555555555"} }|' "$G"
sed -i 's/[0-9a-f]\{64\}/6666666666666666666666666666666666666666666666666666666666666666/' "$S"
if cmp -s internal/mainline/gate.go "$G" || cmp -s internal/mainline/step2set/step2set.go "$S"; then
	echo "FAIL: the data of the copy did not change"; exit 1
fi
grep -c '"1111111111111111' "$G" > /dev/null || { echo "FAIL: the USB set was not changed"; exit 1; }
grep -q '2222222222222222' "$G" || { echo "FAIL: no reviewed payload added"; exit 1; }
# the clone gate's keeper pin (mainline-rc-10 R23, internal/step2 gate-keeper.json: the pin of uc-pxx-provision,
# made after it): uc-pxx-provision, uc-pxx-xfer and the finish executor must not hold it
K=$W/tree/internal/step2/gate-keeper.json
printf '{\n "keeper_files": [{"path": "/usr/libexec/uc-pxx-provision/uc-pxx-provision", "sha256": "%s", "size": 1234}],\n "kind": "uc-pxx-clone-gate-keeper",\n "schema": 1\n}\n' \
	8888888888888888888888888888888888888888888888888888888888888888 > "$K"
cmp -s internal/step2/gate-keeper.json "$K" && { echo "FAIL: the keeper pin of the copy did not change"; exit 1; }
b "$W/tree" "$W/b"
rc=0
for f in uc-pxx-xfer uc-pxx-step2-finish uc-pxx-provision; do
	sa=$(sha256sum < "$W/a/$f" | cut -d' ' -f1) sb=$(sha256sum < "$W/b/$f" | cut -d' ' -f1)
	if [ "$sa" = "$sb" ]; then
		echo "ok   $f $sa: the same bytes with the gate's data changed"
	else
		echo "FAIL $f: $sa with the data, $sb with the data changed: its bytes depend on the gate (a fixed point)"
		rc=1
	fi
done
# the clone gate's profiles (internal/clone gate-profiles.json, made before uc-pxx-provision): uc-pxx-provision may
# change with them (it holds them), uc-pxx-xfer and the finish executor must not
P=$W/tree/internal/clone/gate-profiles.json
cat > "$P" <<'JSON'
{
 "kind": "uc-pxx-clone-gate-profiles",
 "profiles": [{"build_id": "0.0.0-x-console\n", "bundle_sha256": "", "name": "console", "ref_a_sha256": "1212121212121212121212121212121212121212121212121212121212121212", "ref_b_sha256": "3434343434343434343434343434343434343434343434343434343434343434"}],
 "release": "0.0.0-x",
 "schema": 1,
 "xfer_sha256": "5656565656565656565656565656565656565656565656565656565656565656"
}
JSON
mkdir -p "$W/c"
b "$W/tree" "$W/c"
for f in uc-pxx-xfer uc-pxx-step2-finish; do
	sa=$(sha256sum < "$W/a/$f" | cut -d' ' -f1) sc=$(sha256sum < "$W/c/$f" | cut -d' ' -f1)
	if [ "$sa" = "$sc" ]; then
		echo "ok   $f $sa: the same bytes with the clone gate's profiles changed"
	else
		echo "FAIL $f: $sa, $sc with the clone gate's profiles changed: its bytes depend on them"
		rc=1
	fi
done
exit $rc
