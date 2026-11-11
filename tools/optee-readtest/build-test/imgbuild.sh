#!/bin/bash
# imgbuild.sh NAME COMMIT INPUTS: one build of the UC-P10 STATUS RAM image (image/build.sh, no --install,
# --board ucp10) of uc-pxx-installer COMMIT, in its own folder, from nothing (own clones, own HOME, own go cache).
# Offline (the build containers run with --network none); writes only $W/img/NAME.
set -uo pipefail
export LC_ALL=C TZ=UTC
W=${W:-$HOME/uc-pxx/optee-readtest}; N=$1; C=$2; INP=$3; B=$W/img/$N   # W: the work folder (optee-readtest-2/-3: W=~/uc-pxx/optee-readtest-N)
rm -rf "$B"; mkdir -p "$B/home"
git clone -q --no-checkout "$W/src/inst.bundle" "$B/uc-pxx-installer" && git -C "$B/uc-pxx-installer" checkout -q --detach "$C"
git -C "$B/uc-pxx-installer" rev-parse HEAD; [ -z "$(git -C "$B/uc-pxx-installer" status --porcelain)" ] || { echo dirty; exit 1; }
git -c init.defaultBranch=main clone -q --shared --no-checkout "$HOME/uc-pxx/linux-pub" "$B/kernel"
git -C "$B/kernel" checkout -q --detach d02e92631f2810a4a43fb6f09824afb9b0044ee2
git -C "$B/kernel" rev-parse HEAD 'HEAD^{tree}'
git -c init.defaultBranch=main clone -q --no-checkout "$HOME/uc-pxx/mainline-dev-image/src/uc-pxx-linux" "$B/uc-pxx-linux" &&
	git -C "$B/uc-pxx-linux" checkout -q --detach ed2a77dbcb991bc8d9082406ef8901cef5ad7c13
git -C "$B/uc-pxx-linux" rev-parse HEAD; sha256sum "$B/uc-pxx-linux/kernel/mkimage.sh" "$B/uc-pxx-linux/bootloader/shim.S"
cat "$B/uc-pxx-linux/kernel/KERNEL_REV.lts" | grep -v '^#'
cp -a "$HOME/uc-pxx/mainline-dev-image/src/apk-cache" "$B/apk-cache"
cp "$INP" "$B/status.inputs"; sha256sum "$B/status.inputs"
echo "start $(date -u +%FT%TZ)"
HOME=$B/home GO_IMAGE=golang@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d \
ALPINE_IMAGE=arm64v8/alpine@sha256:cc99de154757eb3d8b9200868d0bab96bff581a4f3000e62caed7baf46b41f95 \
nice -n 10 "$B/uc-pxx-installer/image/build.sh" --kernel "$B/kernel" --linux "$B/uc-pxx-linux" --apk-cache "$B/apk-cache" \
	--inputs "$B/status.inputs" --out "$B/out" --jobs 10 > "$B/build.log" 2>&1
rc=$?
echo "rc=$rc end $(date -u +%FT%TZ)"
# keep the kernel .config, release and symvers of the image's own kbuild for the checks, then drop the tree
mkdir -p "$B/kmeta"
for f in .config include/config/kernel.release vmlinux.symvers System.map; do
	[ -f "$B/out/kbuild/$f" ] && cp "$B/out/kbuild/$f" "$B/kmeta/$(basename "$f")"
done
rm -rf "$B/out/kbuild" "$B/kernel"
(cd "$B/out" && sha256sum * | tee "$B/out.sha256")
exit $rc
