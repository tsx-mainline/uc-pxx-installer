# Sources: how to build the install image again, bit for bit

This file covers the UC-P10 install image of the release:

- `p10inst-install-ucp10-f9ce0a.img`, sha256
  `3c522be84ebb8bd59a7cac7b94cf638030937a4bef960b3b6583a1a5c8febb12`,
  32382976 bytes, token `p10inst-f9ce0a` (release v1.2.0; release v1.2.1
  ships the same file).

It gives every pin of that build and the steps that make the same bytes from
public inputs only. [NOTICE.md](NOTICE.md) lists the components and their
licenses. Where the source of the GPL and LGPL parts is published is at the
end of this file.

## The pins

| Input | Pin | Where to get it |
|---|---|---|
| This repository | commit `6a888a9d089ef436a5965e8a1155e0bea63d4a98` built the release. No image input (`cmd/`, `internal/`, `manifests/`, `go.mod`, the build files in `image/`) changed between that commit and v1.2.1 (only `image/release-inputs/`, a copy of the build inputs, was added), so v1.2.1 gives the same image | https://github.com/tsx-mainline/uc-pxx-installer |
| The inputs file | [image/release-inputs/v1.2.0-install.inputs](image/release-inputs/v1.2.0-install.inputs): the same values as the inputs of the release build (only the comment lines differ) | this repository |
| Kernel | tsx-mainline/linux, branch `uc-pxx-lts`, commit `d02e92631f2810a4a43fb6f09824afb9b0044ee2`, tree `c2d31e7011c1aacc5eaf9debc166ad86db49b024` (Linux 6.18.54) | https://github.com/tsx-mainline/linux |
| uc-pxx-linux (`kernel/mkimage.sh`, `bootloader/shim.S`, `kernel/KERNEL_REV.lts`) | commit `ed2a77dbcb991bc8d9082406ef8901cef5ad7c13`; `mkimage.sh` sha256 `98ef579ce1f152463c8902c53c2f2f8edfbdfd6b8a0d40d650c59844d893703f`, `shim.S` sha256 `eb29ea22597be1ece763b614d6e9327f0fcf2f0350c74b9ed7d1d7350c5b9268` | https://github.com/tsx-mainline/uc-pxx-linux |
| `uc-pxx-bootctl-inst` r2 | built from tag `uc-pxx-bootctl-inst-r2` (commit `fa613ed2d6185905d64b9ccc73220826ae0fbbbd`, `tools/uc-pxx-bootctl/uc-pxx-bootctl.c` sha256 `84cc3d94770e5d25794e365b7cf9711473c765a7d982f6412407aa9ab1a8db09`); the binary is sha256 `20aa6bc5d2146faace461eec0b50f741c681b3c58827788b9b806d9444b00203` | https://github.com/tsx-mainline/uc-pxx-linux |
| Alpine packages | [image/apk.lock](image/apk.lock), sha256 `bc1d158dc091cb5a8bb099bb7107ef50cd8ef57a670488a64cc2fda09e106fd6`: Alpine v3.24 aarch64, the two index files and 12 packages, each by sha256 | the release asset `p10inst-apk-cache-v1.2.tar.gz` (Alpine has changed its index files since, so the mirror does not serve these exact files any more) |
| Go build image | `golang:1.27.2-alpine`, linux/amd64, image id `sha256:0a761904b78c29eba4ec83bd83b7312db1e113dcd92da7fc669843ad9bca56a1` | `docker pull golang@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d` (the index; its linux/amd64 manifest is `sha256:3b15ba438c60ab8fabe5fee3b11025c0049e358b31feff384648f11fc8e335f8`) |
| Alpine build image | `arm64v8/alpine:3.24`, linux/arm64, image id `sha256:33bee74c45f307e3268adc2010c0f55c48e7a6041e12cd12432bb1a46e498e43` | `docker pull --platform linux/arm64 arm64v8/alpine@sha256:cc99de154757eb3d8b9200868d0bab96bff581a4f3000e62caed7baf46b41f95` |
| Cross compiler | `aarch64-linux-gnu-gcc (Debian 14.2.0-19) 14.2.0`: Debian 13 `gcc-14-aarch64-linux-gnu` 14.2.0-19cross1, with `libc6-dev-arm64-cross` 2.41-11cross1 (for `uc-pxx-bootctl-inst`) | Debian 13 (trixie); old versions at https://snapshot.debian.org |
| Linker | `GNU ld (GNU Binutils for Debian) 2.44`: `binutils-aarch64-linux-gnu` 2.44-3 | Debian 13 |
| xz | `xz (XZ Utils) 5.8.1`: `xz-utils` 5.8.1-1+deb13u1 | Debian 13 |
| dtc | `Version: DTC 1.7.2` (from `fdtput --version`): `device-tree-compiler` 1.7.2-2+b1 | Debian 13 |
| mkbootimg | `/usr/bin/mkbootimg` sha256 `b682574d42fd5c5cb852e7e76996b0e460808f10f47d81fc01e7f6db1697da4e`: `mkbootimg` 1:34.0.5-12 | Debian 13 |
| Time | `SOURCE_DATE_EPOCH=1794180514`, `KBUILD_BUILD_TIMESTAMP=Sun Nov  8 23:28:34 UTC 2026` | the inputs file |
| Kernel user and host | `KBUILD_BUILD_USER=build`, `KBUILD_BUILD_HOST=uc-pxx-build` (fixed values, not the build machine's) | the inputs file |
| Tokens | `TOKEN=p10inst-f9ce0a` (UC-P10), `TOKEN_UCP8=p10inst-697a85` (UC-P8) | the inputs file |

`image/build.sh` checks each of these pins and stops at the first one that
differs. The release build is `--board both`: one kernel and one initramfs
give the UC-P10 image and a UC-P8 image. Only the UC-P10 image is part of
the release.

## What the build needs

- A Linux host on **x86_64 (amd64)**: the Go build image is pinned by its
  linux/amd64 image id. The release was built on Debian 13 (trixie) with the
  package versions above.
- Docker, with qemu-user binfmt for linux/arm64 containers
  (`qemu-user-static` or `qemu-user-binfmt`).
- Unprivileged user namespaces (`unshare --user`), for the kernel build at
  fixed paths (`image/kbuild.sh`). Some distributions limit them (on Ubuntu:
  `kernel.apparmor_restrict_unprivileged_userns`).
- `make`, `bc`, `bison`, `flex`, `libssl-dev`, `python3`, `git`, `file`.
- About 3 minutes with 16 cores, and no network during the build (the
  containers run with `--network none`).

On Debian 13:

```
sudo apt install docker.io qemu-user-static util-linux git file make bc bison flex libssl-dev python3 \
    gcc-14-aarch64-linux-gnu gcc-aarch64-linux-gnu binutils-aarch64-linux-gnu libc6-dev-arm64-cross \
    xz-utils device-tree-compiler mkbootimg
```

## The steps

All the commands run in one empty folder, as a normal user.

```
# 1. The docker images, by digest
docker pull golang@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d
docker pull --platform linux/arm64 arm64v8/alpine@sha256:cc99de154757eb3d8b9200868d0bab96bff581a4f3000e62caed7baf46b41f95
export GO_IMAGE=golang@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d
export ALPINE_IMAGE=arm64v8/alpine@sha256:cc99de154757eb3d8b9200868d0bab96bff581a4f3000e62caed7baf46b41f95
docker image inspect -f '{{.Id}}' "$GO_IMAGE"       # sha256:0a761904b78c...
docker image inspect -f '{{.Id}}' "$ALPINE_IMAGE"   # sha256:33bee74c45f3...

# 2. The sources
git clone https://github.com/tsx-mainline/uc-pxx-installer
git -C uc-pxx-installer checkout --detach v1.2.1
git init -q linux && git -C linux fetch -q --depth 1 https://github.com/tsx-mainline/linux d02e92631f2810a4a43fb6f09824afb9b0044ee2
git -C linux checkout -q --detach FETCH_HEAD
git -C linux rev-parse HEAD 'HEAD^{tree}'             # d02e9263..., c2d31e70...
git clone https://github.com/tsx-mainline/uc-pxx-linux
git -C uc-pxx-linux checkout --detach ed2a77dbcb991bc8d9082406ef8901cef5ad7c13

# 3. The A/B tool, from its source tag
mkdir bootctl-src
git -C uc-pxx-linux archive uc-pxx-bootctl-inst-r2 tools/uc-pxx-bootctl | tar -x -C bootctl-src
(cd bootctl-src/tools/uc-pxx-bootctl &&
 aarch64-linux-gnu-gcc -O2 -Wall -Wextra -Werror -static -o ../../../uc-pxx-bootctl-inst uc-pxx-bootctl.c)
sha256sum uc-pxx-bootctl-inst                        # 20aa6bc5...

# 4. The Alpine packages (the release asset), checked against the lock
mkdir apk-cache
tar -xzf p10inst-apk-cache-v1.2.tar.gz --strip-components=1 -C apk-cache
uc-pxx-installer/image/apk-cache.sh verify --cache apk-cache   # 14 files match

# 5. The inputs file. The build checks INSTALLER_COMMIT against the commit
#    it runs from, so a build from v1.2.1 sets it to that commit. The copy
#    stays outside the repository: the build refuses a tree with changes.
sed "s/^INSTALLER_COMMIT=.*/INSTALLER_COMMIT=$(git -C uc-pxx-installer rev-parse HEAD)/" \
    uc-pxx-installer/image/release-inputs/v1.2.0-install.inputs > install.inputs

# 6. The build
uc-pxx-installer/image/build.sh --kernel linux --linux uc-pxx-linux --apk-cache apk-cache \
    --install --bootctl uc-pxx-bootctl-inst --board both --inputs install.inputs --out out
sha256sum out/p10inst-install-ucp10.img              # 3c522be8...
```

To build from the release commit itself, copy
`image/release-inputs/v1.2.0-install.inputs` out of the repository, then
`git checkout --detach 6a888a9d089ef436a5965e8a1155e0bea63d4a98` and use the
copy unchanged in step 6 (its `INSTALLER_COMMIT` is that commit).

The build runs `ci/check-no-writer.py` on the binary and the initramfs, and
it checks the boot image (header v0, no second part, the token, the kernel
command line, the initramfs in the Image = the checked cpio).

## The expected outputs

These files of `out/` are the same for every commit that builds this image:

| File | sha256 |
|---|---|
| `p10inst-install-ucp10.img` (the release asset `p10inst-install-ucp10-f9ce0a.img`) | `3c522be84ebb8bd59a7cac7b94cf638030937a4bef960b3b6583a1a5c8febb12` |
| `p10inst-install-ucp8.img` (not released) | `c9f0ca570aaea00706d462befe02f97c9362d7943a1f4419c8bd9b449a00e054` |
| `initramfs.cpio` | `a1eb90c345367bacc3671a9f33c3e8d1e79ecb2b9b3909a8aaf126f4bf64e46d` |
| `Image` | `998b9f1adbeb6722e433e72528d0f8247dc448cb621775fd656767f6b8fd5568` |
| `p10inst-install-arm64` | `8cf358f7979638b1f61b6dc2d1a121580a841f4b76d426834c5256dc8c92dbbb` |
| `rk3399-crestron-ucp10.dtb` | `8651b03beba40b51c7e1e01ca715f7358837fc4b5fc8e4bbbf8db9aed2d0b374` |
| `rk3399-crestron-ucp8.dtb` | `7c9840c6c8d634e663c6a7c97d39ff369331d97ea69cc9e8aba8a7102ab9d56c` |

`inputs.txt`, `build-info.txt` and `sha256sums.txt` name the installer
commit and the inputs file, so they differ from one commit to the next.

## The source of each part

- **This project** (`p10inst`, the image scripts and files, the page): this
  repository, GPL-2.0-or-later.
- **The kernel**: https://github.com/tsx-mainline/linux at
  `d02e92631f2810a4a43fb6f09824afb9b0044ee2`, configured by
  `arch/arm64/configs/defconfig`, `arch/arm64/configs/uc-pxx.config` (in that
  tree) and `image/installer.config`, built by `image/kbuild.sh`. The same
  tree is the release asset `linux-d02e92631f28-source.tar.gz` (a `git
  archive` of that commit).
- **The boot stub, `mkimage.sh` and `uc-pxx-bootctl-inst`**:
  https://github.com/tsx-mainline/uc-pxx-linux at `ed2a77dbcb99` (also the
  release asset `uc-pxx-linux-ed2a77d-source.tar.gz`) and at the tag
  `uc-pxx-bootctl-inst-r2`.
- **BusyBox 1.37.0-r31** (GPL-2.0-only): the release asset
  `busybox-1.37.0-r31-source.tar.gz`. It holds `busybox-1.37.0.tar.bz2`
  (sha512 as in the APKBUILD) and the Alpine aports folder `main/busybox` at
  `c3ef5d10e6ef6528852c51f0564963e2f8c1be19` (APKBUILD, patches,
  `busyboxconfig`, `busyboxconfig-extras`).
- **alpine-baselayout 3.7.2-r1** (GPL-2.0-only): the release asset
  `alpine-baselayout-3.7.2-r1-source.tar.gz` (aports `main/alpine-baselayout`
  at `60a7585bbab2fa0f762504eb617dbca90216e31f` and the two netbase 6.4 files).
- **The GNU C Library in `uc-pxx-bootctl-inst`** (LGPL-2.1-or-later): the
  release asset `glibc-2.41-11-source.tar.gz` (the Debian source packages
  glibc 2.41-11 and cross-toolchain-base 77, and the source of the tool with
  the command that links it again).
- **The permissive packages** (musl, Dropbear, skalibs, utmps, zlib, the
  Terminus font): the upstream release tarballs and the aports commits in
  [NOTICE.md](NOTICE.md).
- **GNU cpio 2.15** (a build tool, not in the image; in the apk cache
  asset): the release asset `cpio-2.15-r0-source.tar.gz`.

## Source of the GPL and LGPL parts

The install image contains software under the GNU General Public License
version 2 (the Linux kernel, BusyBox, alpine-baselayout and the code of this
project) and the GNU Lesser General Public License version 2.1 (the GNU C
Library in `uc-pxx-bootctl-inst`). The complete corresponding source is
published with the release, as the release assets named above, next to the
image, and in the repositories named above.

Questions about the source: open an issue at
https://github.com/tsx-mainline/uc-pxx-installer/issues and name the release
and the image sha256.
