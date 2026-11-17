# Notices: the third-party software in the installer

This file lists every component of the UC-P10 install image and of the
installer page, with its version, its license and the place of its source.
[SOURCES.md](SOURCES.md) has the exact pins, the steps that build the image
again bit for bit, and where the source of the GPL and LGPL parts is
published. The license texts are in [LICENSE](LICENSE) (GPL-2.0) and in
[third_party/licenses/](third_party/README.md).

## Release 1.3.0 (the mainline installer, RC-10b)

Release 1.3.0 ships these files. The page starts the install image only after its sha256 matches the pin in
`web/site/js/config.js`; the unit checks every other file against the pins built into the install image.

| Release asset | sha256 | Bytes | What it is |
|---|---|---|---|
| `uc-pxx-inst-ucp10-d876e5.img` | `f7069de1e983a7f839e30ab6514d467c3fce3c6e49a7ad0b42155155942ac934` | 34480128 | the install image (Android boot image v0, run from RAM by `fastboot boot`), token `pxx-inst-d876e5` |
| `uc-pxx-base-manifest.json` | `04f4f064608a0336bfdada9a08163d2b8187659e055ebc0496e5a9123f792839` | 1753 | the base system `0.2.0-rc10b-base`: its manifest (schema 3) |
| `uc-pxx-base-boot-template.img` | `0498d4d70c57ddcf54ee559b1bd104a2f863654046c09afc9254c3c905f03fc8` | 34521088 | the base system: the boot image template (kernel `6.18.54-gd02e92631f28`) |
| `uc-pxx-base-rootfs.ext4.zst` | `8b713dd24c409b7693e78c494a6764a2415bde02fcfddc5c809d75aea6d82c1b` | 52532293 | the base system: the root file system (zstd; unpacked `rootfs.ext4` `b4fa39fe…`, 229638144 B) |
| `finish.json`, `uc-pxx-step2-finish`, `uc-pxx-bootctl-inst` | `66dfb112a23b3d31846015d3fdd23557098e15e5694af407c559537e14fadd74`, `446a35be70f170f44aeb5ed006827b542999f1db5b6cdf89976c8f298bd42025`, `20aa6bc5d2146faace461eec0b50f741c681b3c58827788b9b806d9444b00203` | 290, 3866748, 777976 | the finish set of step 2 |

The two package bundles that the unit downloads on its first start (`uc-pxx-profile-kiosk-ha-0.2.0-rc10.tar`
`5e2b78dc…`, `uc-pxx-profile-kiosk-0.2.0-rc10.tar` `ac7fa681…`) are assets of the release `mainline-rc-10`, the
address that the base system's `profiles.json` names.

### The install image of release 1.3.0

The same layout as the image of v1.2.0 below (the same kernel commit, the same Alpine packages of
[image/apk.lock](image/apk.lock) `bc1d158d…`, the same boot stub), with these programs in place of `p10inst` and r2:

| Component | Version | In the image | License | Source |
|---|---|---|---|---|
| `uc-pxx-inst`, the installer (release install build) | this repository, commit `bf9396642371` (the images commit; release 1.3.0 contains it) | `/usr/bin/uc-pxx-inst` (sha256 `65f09622…`) | GPL-2.0-or-later; Go 1.27.2 linked in (BSD-3-Clause) | this repository |
| `uc-pxx-xfer`, the step-1 USB service | this repository | `/usr/libexec/uc-pxx-xfer/uc-pxx-xfer` (sha256 `62421c48…`) | GPL-2.0-or-later; Go (BSD-3-Clause) | this repository |
| `uc-pxx-bootctl-inst` r3, the A/B tool | uc-pxx-linux tag `uc-pxx-bootctl-inst-r3` (`de744a5`) | `/usr/libexec/uc-pxx-inst/uc-pxx-bootctl-inst` (sha256 `639b6514…`) | GPL-2.0-or-later; glibc 2.41 and libgcc linked in statically (as r2 below) | https://github.com/tsx-mainline/uc-pxx-linux/tree/uc-pxx-bootctl-inst-r3/tools/uc-pxx-bootctl |
| `optee_legacy.ko`, the legacy OP-TEE driver (a forward port of the Rockchip 4.4 driver) | tsx-mainline/linux tag `uc-pxx-optee-legacy-r1` (`26dd90965af1`), built against the kernel above | `/usr/lib/uc-pxx-inst/optee/optee_legacy.ko` (sha256 `5870e0fd…`) | GPL-2.0 (STMicroelectronics, Rockchip) | https://github.com/tsx-mainline/linux/tree/uc-pxx-optee-legacy-r1/drivers/tee/optee_legacy; release asset `optee_legacy-26dd909-source.tar.gz` |
| The read-only supplicant and the lock-state reader (`tee-supplicant-ro`, `tee_legacy_read`) | this repository, `tools/optee-readtest/` | `/usr/lib/uc-pxx-inst/optee/` (sha256 `64c139f6…`, `1424ac1e…`) | GPL-2.0-or-later; glibc and libgcc linked in statically | this repository; release asset `uc-pxx-inst-optee-tools-source.tar.gz` |
| The secure-storage supplicant and the lock-state client of the unlock step (`tee-supplicant-rw-real`, `tee_legacy_setlock`) | the tee-unlock tool set | `/usr/lib/uc-pxx-inst/optee/` (sha256 `2786a2a7…`, `c0a4af8a…`) | GPL-2.0-or-later; glibc and libgcc linked in statically | release asset `uc-pxx-inst-optee-tools-source.tar.gz` |
| GNU C Library and libgcc, in the five static tools above | glibc 2.41 (Debian `libc6-dev-arm64-cross` 2.41-11cross1), GCC 14.2.0 (Debian 14.2.0-19) | in each static tool | LGPL-2.1-or-later; GPL-3.0-or-later WITH GCC-exception-3.1 | release asset `glibc-2.41-11-source.tar.gz` |

The third-party packages of the image are those of v1.2.0 (the table below): BusyBox, alpine-baselayout, musl,
Dropbear, skalibs, utmps, zlib, the Terminus font, all at the versions of `image/apk.lock`.

### The base system and the bundles

The base system (`uc-pxx-base-rootfs.ext4.zst` and `uc-pxx-base-boot-template.img`) and the two bundles are
Alpine Linux v3.24 (aarch64) systems: unchanged binary packages of Alpine and of
https://github.com/tsx-mainline/tsx-aports, the programs of this project and of uc-pxx-linux, and (Kiosk and
Home Assistant only) the voice assistant folder `lva.tar` below. Every package, its version, its aports commit and
its license is listed in
[third_party/release-1.3.0-packages.md](third_party/release-1.3.0-packages.md) (144 packages in the base, 277 in
the Kiosk and Home Assistant bundle, 207 in the Kiosk bundle). The programs of the projects:

| Component | In | License | Source |
|---|---|---|---|
| `uc-pxx-step2` (`86d305ec…`), `uc-pxx-provision` (`6dfc8268…`), `uc-pxx-xfer` (`62421c48…`), `uc-pxx-step2-finish` (`446a35be…`) | the base system; the finish set | GPL-2.0-or-later; Go (BSD-3-Clause) | this repository, built at commit `3f06cc2` (the same bytes at `b6b0d14`; release 1.3.0 contains both) |
| `uc-pxx-bootctl-inst` r2 (`20aa6bc5…`) | the base system; the finish set | GPL-2.0-or-later; glibc, libgcc static | uc-pxx-linux tag `uc-pxx-bootctl-inst-r2` |
| The base system's build, its boot files, init scripts and `profiles.json` | the base system | GPL-2.0-or-later | uc-pxx-linux tag `r23-base-rc10b` (`a93819a`) |
| The kernel (Linux 6.18.54) in the boot template and the package `uc-pxx-kernel-lts` | the base system | GPL-2.0-only WITH Linux-syscall-note | tsx-mainline/linux `d02e92631f28`; release asset `linux-d02e92631f28-source.tar.gz` |
| The `tsx-*` and `uc-pxx-*` packages | the base system; the bundles | GPL-2.0-or-later | https://github.com/tsx-mainline/tsx-aports and https://github.com/tsx-mainline/tsx-linux-common (tags `v0.3.13_pre20261113`, `v0.3.13_pre20261114`) |

The Kiosk and Home Assistant bundle also holds `lva.tar` (sha256
`abca01e8bc5ba1442d1085cf4c3125c07b53ae5ebae82e85454c3e5855b709ce`, 19722240 bytes), unpacked to `/opt/lva/`. It is
not an Alpine package:

| Component | Version | License | Source |
|---|---|---|---|
| linux-voice-assistant (`/opt/lva/app`, with its `sounds/`) | 1.1.15 | Apache-2.0 (its `LICENSE.md`; `sounds/LICENSE.md`) | https://github.com/OHF-Voice/linux-voice-assistant |
| aioesphomeapi | 45.3.1 | MIT | https://pypi.org/project/aioesphomeapi/ |
| async_interrupt | 1.2.2 | Apache-2.0 | https://pypi.org/project/async-interrupt/ |
| chacha20poly1305-reuseable | 0.13.2 | Apache-2.0 OR BSD-3-Clause | https://pypi.org/project/chacha20poly1305-reuseable/ |
| getmac | 0.9.5 | MIT | https://pypi.org/project/getmac/ |
| netifaces2 | 0.0.22 | MIT | https://pypi.org/project/netifaces2/ |
| noiseprotocol | 0.3.1 | MIT | https://pypi.org/project/noiseprotocol/ |
| pymicro-features | 2.0.2 | Apache-2.0 | https://pypi.org/project/pymicro-features/ |
| pymicro-wakeword | 2.5.0 | Apache-2.0 | https://pypi.org/project/pymicro-wakeword/ |
| pyopen-wakeword | 1.1.0 | Apache-2.0 | https://pypi.org/project/pyopen-wakeword/ |
| websockets | 12.0 | BSD-3-Clause | https://pypi.org/project/websockets/ |
| The wake-word models (`wakewords/`, `pyopen_wakeword/models/`) | as shipped by the packages above | the licenses of their packages and of the upstream model files (openWakeWord, microWakeWord) | the packages above |

The license files of each are in `/opt/lva/` (each package's `*.dist-info/`).

The source of each copyleft Alpine package (the aports folder at the commit of the list, with its patches, and
the upstream source files that its APKBUILD names) is a release asset `<origin>-<version>-source.tar.gz`
([SOURCES.md](SOURCES.md)).

## The install image (release v1.2.0; v1.2.1 ships the same file)

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
