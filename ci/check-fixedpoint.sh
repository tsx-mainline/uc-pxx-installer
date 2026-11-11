#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# ci/check-fixedpoint.sh: the gate pins uc-pxx-xfer and uc-pxx-step2-finish (the
# USB set and its finish list, mainline-rc-4), so their bytes must not depend on
# the gate's data (internal/mainline gate.go gateDataNote, step2set): else no pin
# could exist. Build both (the release recipe, ci/build-xfer.sh and
# ci/build-step2.sh), change the data of every list in a copy of the tree (a pin
# of each both-slot list, the USB set, the kernels, a reviewed payload, the
# step-2 set), build again and require the same bytes. Both recipes blank the
# Go build id (-buildid=): it hashes every source file of the build, also the
# gate's data that the binary does not hold. Run it inside
# golang:1.27.2-alpine from the repo root: sh ci/check-fixedpoint.sh WORK
set -eu
W=${1:?usage: ci/check-fixedpoint.sh WORK}
mkdir -p "$W"
export GOOS=linux GOARCH=arm64 CGO_ENABLED=0 GOFLAGS=-buildvcs=false
b() { # b TREE OUTDIR
	(cd "$1" && go build -tags xfer -trimpath -ldflags "-s -w -buildid=" -o "$2/uc-pxx-xfer" ./cmd/uc-pxx-xfer &&
		go build -tags install,step2,finish -trimpath -ldflags "-s -w -buildid=" -o "$2/uc-pxx-step2-finish" ./cmd/uc-pxx-step2-finish)
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
	s/40c913500d29e26601fbb6d14ac9feb2b8b200b9b3b2a2c2d5d69c42180391ab/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb/;
	s/15a610e7079ddad643bdacc0f744f348bef0ee965d2fb4e6d912e848f6e1ced2/cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc/g;
	s/85f5a417e12feabb3c8a52b246ce8b89d6f9c70d2e6994525f69ff307f13f3d7/dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd/;
	s/73867d92083f2f8ab899a26ccda7ef63dfaa0032a938620eda605558958a8041/eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee/' "$G"
# the USB set: every pin of it (the finish list's sha256 is one)
awk 'BEGIN{u=0} /^var bothSlotUSB = /{u=1} u&&/^}/{u=0} {if(u) gsub(/"[0-9a-f]{64}"/, "\"1111111111111111111111111111111111111111111111111111111111111111\""); print}' "$G" > "$G.new" && mv "$G.new" "$G"
# a reviewed payload and a reviewed kernel
sed -i 's|^var reviewedPayloads = \[\]Reviewed{}|var reviewedPayloads = []Reviewed{{Version: "x", ManifestSHA256: "2222222222222222222222222222222222222222222222222222222222222222", TemplateSHA256: "3333333333333333333333333333333333333333333333333333333333333333", RootfsSHA256: "4444444444444444444444444444444444444444444444444444444444444444", Slots: "ab"}}|' "$G"
sed -i 's|func reviewedKernels() map\[string\]string { return map\[string\]string{} }|func reviewedKernels() map[string]string { return map[string]string{"x": "5555555555555555555555555555555555555555555555555555555555555555"} }|' "$G"
sed -i 's/[0-9a-f]\{64\}/6666666666666666666666666666666666666666666666666666666666666666/' "$S"
if cmp -s internal/mainline/gate.go "$G" || cmp -s internal/mainline/step2set/step2set.go "$S"; then
	echo "FAIL: the data of the copy did not change"; exit 1
fi
grep -c '"1111111111111111' "$G" > /dev/null || { echo "FAIL: the USB set was not changed"; exit 1; }
grep -q '2222222222222222' "$G" || { echo "FAIL: no reviewed payload added"; exit 1; }
b "$W/tree" "$W/b"
rc=0
for f in uc-pxx-xfer uc-pxx-step2-finish; do
	sa=$(sha256sum < "$W/a/$f" | cut -d' ' -f1) sb=$(sha256sum < "$W/b/$f" | cut -d' ' -f1)
	if [ "$sa" = "$sb" ]; then
		echo "ok   $f $sa: the same bytes with the gate's data changed"
	else
		echo "FAIL $f: $sa with the data, $sb with the data changed: its bytes depend on the gate (a fixed point)"
		rc=1
	fi
done
exit $rc
