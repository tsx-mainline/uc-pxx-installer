#!/bin/bash
# modbuild.sh RUN: build optee_legacy.ko against EXACTLY the kernel tree that the
# uc-pxx-installer image recipe builds (d02e9263 + defconfig + uc-pxx.config +
# image/installer.config), as an external module (M=), at the fixed namespace paths
# of image/kbuild.sh. Step 1 runs the UNMODIFIED image/kbuild.sh of 80239f7 (the same
# call as image/build.sh step 3). Step 2 (modstep.sh) builds the module in a new
# namespace with the same mounts against that kbuild output.
# Offline; touches only ~/uc-pxx/optee-readtest/mod/RUN.
set -euo pipefail
export LC_ALL=C TZ=UTC
umask 022
W=$HOME/uc-pxx/optee-readtest
RUN=${1:?run name}
B=$W/mod/$RUN
KREV=d02e92631f2810a4a43fb6f09824afb9b0044ee2
PORT=26dd90965af1d01ffa08da28908d7918efe2e80a
INST=80239f744c6265f0f8c8f8af58b671b54443ac7a
# the same kernel version-string inputs as the image inputs file
KUSER=build KHOST=uc-pxx-build
KTIME=${KTIME:?KTIME}
rm -rf "$B"; mkdir -p "$B/out"
git -c init.defaultBranch=main clone -q --shared --no-checkout "$HOME/uc-pxx/linux-pub" "$B/kernel"
git -C "$B/kernel" checkout -q --detach $KREV
[ -z "$(git -C "$B/kernel" status --porcelain)" ] || { echo "kernel dirty"; exit 1; }
git -C "$B/kernel" rev-parse HEAD 'HEAD^{tree}'
# the installer recipe files of 80239f7 (kbuild.sh, installer.config)
mkdir -p "$B/inst"
git -C "$W/src/uc-pxx-installer" archive $INST image/kbuild.sh image/installer.config | tar -x -C "$B/inst"
sha256sum "$B/inst/image/kbuild.sh" "$B/inst/image/installer.config"
# the module source = drivers/tee/optee_legacy of the port commit, nothing else
mkdir -p "$B/out/modsrc"
git -C "$HOME/uc-pxx/optee-legacy-port" archive $PORT drivers/tee/optee_legacy | tar -x -C "$B/out/modsrc"
(cd "$B/out/modsrc" && find . -type f | sort | xargs sha256sum | sha256sum)
# kbuild needs an initramfs file at the fixed path; the module does not depend on it
cp "$W/mod/stand-in-initramfs.cpio" "$B/out/initramfs.cpio"
mkdir -p "$B/out/kbuild"
echo "== kbuild.sh (unmodified, 80239f7) $(date -u +%FT%TZ)"
unshare --user --map-root-user --mount --propagation private -- "$B/inst/image/kbuild.sh" \
	"$B/kernel" "$B/out" "$B/inst/image/installer.config" 20 aarch64-linux-gnu- "$KUSER" "$KHOST" "$KTIME"
echo "== module $(date -u +%FT%TZ)"
unshare --user --map-root-user --mount --propagation private -- "$W/scripts/modstep.sh" \
	"$B/kernel" "$B/out" aarch64-linux-gnu- "$KUSER" "$KHOST" "$KTIME"
cp "$B/out/modsrc/drivers/tee/optee_legacy/optee_legacy.ko" "$B/optee_legacy.ko"
cp "$B/out/kbuild/include/config/kernel.release" "$B/kernel.release"
cp "$B/out/kbuild/.config" "$B/kernel.config"
cp "$B/out/kbuild/vmlinux.symvers" "$B/vmlinux.symvers"
cp "$B/out/kbuild/System.map" "$B/System.map"
cp "$B/out/kbuild/arch/arm64/boot/Image" "$B/Image-standin"
(cd "$B" && sha256sum optee_legacy.ko kernel.release kernel.config vmlinux.symvers System.map Image-standin)
echo "== done $(date -u +%FT%TZ)"
