# Notices: the third-party software in the installer

This file lists every component of the UC-P10 install image and of the
installer page, with its version, its license and the place of its source.
[SOURCES.md](SOURCES.md) has the exact pins, the steps that build the image
again bit for bit, and where the source of the GPL and LGPL parts is
published. The license texts are in [LICENSE](LICENSE) (GPL-2.0) and in
[third_party/licenses/](third_party/README.md).

## The install image

Release v1.2.0 asset `p10inst-install-ucp10-f9ce0a.img`, sha256
`3c522be84ebb8bd59a7cac7b94cf638030937a4bef960b3b6583a1a5c8febb12`
(32382976 bytes), token `p10inst-f9ce0a`. It is an Android boot image
(header v0) for `fastboot boot`. Its kernel part is a boot stub and an arm64
Linux Image. The Image holds the initramfs (an xz-compressed cpio, sha256
`a1eb90c345367bacc3671a9f33c3e8d1e79ecb2b9b3909a8aaf126f4bf64e46d`
uncompressed) and the user space below.

### Programs and files of this project

| Component | Version | In the image | License | Source |
|---|---|---|---|---|
| `p10inst`, the installer (release install build) | uc-pxx-installer `6a888a9d089e` | `/usr/bin/p10inst` (sha256 `8cf358f7…`) | GPL-2.0-or-later | this repository |
| Go standard library and runtime, linked into `p10inst` | Go 1.27.2 | in `/usr/bin/p10inst` | BSD-3-Clause ([text](third_party/licenses/go-LICENSE.txt)) | https://go.dev/dl/ (the `golang:1.27.2-alpine` image pinned in SOURCES.md) |
| The init script, rcS, inittab, profile and `etc/p10inst/*` | uc-pxx-installer `6a888a9d089e` | `/init`, `/etc/…` | GPL-2.0-or-later | `image/overlay/`, `image/overlay-install/` of this repository |
| `uc-pxx-bootctl-inst` r2, the A/B tool | uc-pxx-linux tag `uc-pxx-bootctl-inst-r2` (`fa613ed2d618`) | `/usr/libexec/p10inst/uc-pxx-bootctl-inst` (sha256 `20aa6bc5…`) | GPL-2.0-or-later | https://github.com/tsx-mainline/uc-pxx-linux/tree/uc-pxx-bootctl-inst-r2/tools/uc-pxx-bootctl |
| The boot stub (`bootloader/shim.S`) and `kernel/mkimage.sh` | uc-pxx-linux `ed2a77dbcb99` | the first 2 MiB of the kernel part | GPL-2.0-or-later | https://github.com/tsx-mainline/uc-pxx-linux/tree/ed2a77dbcb991bc8d9082406ef8901cef5ad7c13; release asset `uc-pxx-linux-ed2a77d-source.tar.gz` |

### Third-party software

| Component | Version | In the image | License | Source |
|---|---|---|---|---|
| Linux kernel | 6.18.54, tsx-mainline/linux branch `uc-pxx-lts` commit `d02e92631f28` (tree `c2d31e7011c1`) | the arm64 Image and the device tree in the kernel part | GPL-2.0-only WITH Linux-syscall-note ([COPYING](third_party/licenses/linux-COPYING.txt), [note](third_party/licenses/linux-Linux-syscall-note.txt)); single files may carry other compatible licenses, see their SPDX lines | https://github.com/tsx-mainline/linux/tree/d02e92631f2810a4a43fb6f09824afb9b0044ee2; the configuration is `defconfig` + `uc-pxx.config` + `image/installer.config`; release asset `linux-d02e92631f28-source.tar.gz` |
| BusyBox (Alpine `busybox`, `busybox-binsh`, `busybox-extras`) | 1.37.0-r31 | `/bin/busybox`, `/bin/busybox-extras`, their applet links, `/usr/share/udhcpc/default.script`, `/etc/udhcpc/`, `/etc/securetty`, `/etc/busybox-paths.d/` and other files of the packages | GPL-2.0-only ([LICENSE](LICENSE)) | release asset `busybox-1.37.0-r31-source.tar.gz`; https://busybox.net/downloads/busybox-1.37.0.tar.bz2 with Alpine aports `main/busybox` at `c3ef5d10e6ef` |
| alpine-baselayout, alpine-baselayout-data | 3.7.2-r1 | the base folders and the files of `/etc` (`passwd`, `group`, `profile`, `protocols`, `services`, `modprobe.d/`, …), some of them changed by the build | GPL-2.0-only ([LICENSE](LICENSE)); `protocols` and `services` are from Debian netbase 6.4 (GPL-2.0) | release asset `alpine-baselayout-3.7.2-r1-source.tar.gz`; Alpine aports `main/alpine-baselayout` at `60a7585bbab2` |
| GNU C Library, statically linked into `uc-pxx-bootctl-inst` | 2.41 (Debian `libc6-dev-arm64-cross` 2.41-11cross1, from glibc 2.41-11) | in `/usr/libexec/p10inst/uc-pxx-bootctl-inst` | LGPL-2.1-or-later ([text](third_party/licenses/glibc-COPYING.LIB.txt)) and the notices in [LICENSES](third_party/licenses/glibc-LICENSES.txt) | release asset `glibc-2.41-11-source.tar.gz`; https://snapshot.debian.org/package/glibc/2.41-11/ |
| libgcc, statically linked into `uc-pxx-bootctl-inst` | GCC 14.2.0 (Debian `gcc-14-aarch64-linux-gnu` 14.2.0-19cross1) | in `/usr/libexec/p10inst/uc-pxx-bootctl-inst` | GPL-3.0-or-later WITH GCC-exception-3.1 (the GCC Runtime Library Exception) | https://snapshot.debian.org/package/gcc-14-cross/9/ |
| musl | 1.2.6-r3 | `/lib/ld-musl-aarch64.so.1`, `/lib/libc.musl-aarch64.so.1` | MIT ([COPYRIGHT](third_party/licenses/musl-COPYRIGHT.txt)) | https://musl.libc.org/releases/musl-1.2.6.tar.gz with Alpine aports `main/musl` at `8f79d39391b4` |
| Dropbear SSH | 2026.91-r0 | `/usr/sbin/dropbear`, `/usr/bin/dropbearkey` | MIT and the other notices of its LICENSE ([LICENSE](third_party/licenses/dropbear-LICENSE.txt), [LibTomCrypt](third_party/licenses/dropbear-libtomcrypt-LICENSE.txt), [LibTomMath](third_party/licenses/dropbear-libtommath-LICENSE.txt)) | https://matt.ucc.asn.au/dropbear/releases/dropbear-2026.91.tar.bz2 with Alpine aports `main/dropbear` at `e56a1d711fb2` |
| skalibs (`libskarnet`, Alpine `skalibs-libs`) | 2.15.0.0-r0 | `/usr/lib/libskarnet.so.2.15.0.0` | ISC ([COPYING](third_party/licenses/skalibs-COPYING.txt)) | https://skarnet.org/software/skalibs/skalibs-2.15.0.0.tar.gz with Alpine aports `main/skalibs` at `f148233038fa` |
| utmps (`libutmps`, Alpine `utmps-libs`) | 0.1.3.3-r0 | `/usr/lib/libutmps.so.0.1.3.3` | ISC ([COPYING](third_party/licenses/utmps-COPYING.txt)) | https://skarnet.org/software/utmps/utmps-0.1.3.3.tar.gz with Alpine aports `main/utmps` at `900a4ecef77a` |
| zlib | 1.3.2-r1 | `/usr/lib/libz.so.1.3.2` | Zlib ([LICENSE](third_party/licenses/zlib-LICENSE.txt)) | https://zlib.net/fossils/zlib-1.3.2.tar.gz with Alpine aports `main/zlib` at `0afa2da0e8c8` |
| Terminus Font (Alpine `font-terminus`) | 4.49.1-r4 | `/usr/share/consolefonts/ter-124b.psf`, `ter-128b.psf`, `ter-132b.psf` (the package's fonts, uncompressed) | OFL-1.1 ([text](third_party/licenses/terminus-font-OFL.txt)); Reserved Font Name "Terminus Font" | https://sourceforge.net/projects/terminus-font/files/terminus-font-4.49/terminus-font-4.49.1.tar.gz with Alpine aports `main/font-terminus` at `abec7d40d0a7` |

The Alpine packages are the unchanged binary packages of Alpine Linux v3.24
(aarch64), pinned by version and sha256 in [image/apk.lock](image/apk.lock).
The aports commit of each one is the `commit` of its `.PKGINFO` (and of
`/lib/apk/db/installed` in the image); the aports tree is at
https://gitlab.alpinelinux.org/alpine/aports. The kernel has no built-in
firmware (`CONFIG_EXTRA_FIRMWARE` is empty) and builds no modules into the
image.

The files under `/lib/apk/db/` and `/var/log/apk.log` are records of the
package manager (apk-tools) that installed the packages; apk-tools itself is
not in the image.

## The installer page (`web/site/`)

| Component | License | Source |
|---|---|---|
| The page (`index.html`, `style.css`, `js/*.js`) | GPL-2.0-or-later ([web/site/LICENSE.txt](web/site/LICENSE.txt)) | this repository |
| `js/rockusb.js`: a JavaScript port of a part of rkdeveloptool (commit `304f073752fd`, (C) 2017 Fuzhou Rockchip Electronics Co., Ltd) | GPL-2.0-or-later (rkdeveloptool: GPL-2.0+) | https://github.com/rockchip-linux/rkdeveloptool; the changes are listed in the header of the file |
| The unlock one-liner scripts (`web/site/tools/unlock-linux-macos.sh`, `unlock-windows.ps1`) | GPL-2.0-or-later | this repository |

The page loads no third-party script, style or font.

## The unlock client (`tools/tee-unlock/`, a release asset)

The optional unlock client `tee_unlock.android-arm64` (bundled same-origin from
a release asset by the Pages workflow; pinned by `UNLOCK.client` in
`web/site/js/config.js`, sha256
`6ba5d28c0854f9ca110f22fe5a3a108a1ba068cab40755256061bb83292fc205`) is an Android
arm64 ELF built from `tools/tee-unlock/`.

| Component | License | Source |
|---|---|---|
| `tee_unlock.c` (the client) and `stub/libteec_stub.c` (the link-only stub) | GPL-2.0-or-later | this repository (`tools/tee-unlock/`) |
| `include/tee_client_api.h` (the GlobalPlatform TEE Client API header, from OP-TEE optee_client) | BSD-2-Clause ((C) 2014 STMicroelectronics, (C) 2015 Linaro) | the header's own notice; https://github.com/OP-TEE/optee_client |
| Android libc / libteec, at **run time only** on the unit | — (the unit's own `/vendor/lib64/libteec.so`; never shipped and never linked at build time) | the unit |

The client is linked at build time against the stub above, never the device's
`libteec.so`. `SOURCES.md` has the reproducible build recipe.

## Build tools (not in the image)

The build uses these tools; they are not part of the image or the page: the
Go toolchain (`golang:1.27.2-alpine`), an Alpine v3.24 aarch64 container,
GNU cpio 2.15 (GPL-3.0-or-later; it writes the initramfs archive), the
Debian cross compiler and binutils, xz, dtc, mkbootimg, qemu-user and
docker. If the release carries the Alpine package cache
(`p10inst-apk-cache-v1.2.tar.gz`), that archive holds the binary packages of
the lock, the cpio package among them; its source is the release asset
`cpio-2.15-r0-source.tar.gz`.
