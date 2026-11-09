# Third-party license texts

The license texts of the third-party code in the installer image and the
installer page. [NOTICE.md](../NOTICE.md) lists each component, its version
and where its source is. The files in `licenses/` are verbatim copies of
the license files of each upstream release. Each release tarball was checked
against the sha512 in its Alpine APKBUILD (the Debian glibc tarball against
the sha256 in its `.dsc`; the Go license comes from the pinned Go build
image; the kernel files come from the pinned kernel commit).

| File | Component | Taken from |
|---|---|---|
| `licenses/linux-COPYING.txt` | Linux kernel | `COPYING` of tsx-mainline/linux `d02e92631f28` |
| `licenses/linux-Linux-syscall-note.txt` | Linux kernel | `LICENSES/exceptions/Linux-syscall-note` of the same commit |
| `licenses/musl-COPYRIGHT.txt` | musl 1.2.6 (MIT) | `COPYRIGHT` of `musl-1.2.6.tar.gz` |
| `licenses/dropbear-LICENSE.txt` | Dropbear 2026.91 (MIT and others) | `LICENSE` of `dropbear-2026.91.tar.bz2` |
| `licenses/dropbear-libtomcrypt-LICENSE.txt` | LibTomCrypt, in Dropbear | `libtomcrypt/LICENSE` of the same tarball |
| `licenses/dropbear-libtommath-LICENSE.txt` | LibTomMath, in Dropbear | `libtommath/LICENSE` of the same tarball |
| `licenses/skalibs-COPYING.txt` | skalibs 2.15.0.0 (ISC) | `COPYING` of `skalibs-2.15.0.0.tar.gz` |
| `licenses/utmps-COPYING.txt` | utmps 0.1.3.3 (ISC) | `COPYING` of `utmps-0.1.3.3.tar.gz` |
| `licenses/zlib-LICENSE.txt` | zlib 1.3.2 (Zlib) | `LICENSE` of `zlib-1.3.2.tar.gz` |
| `licenses/terminus-font-OFL.txt` | Terminus Font 4.49.1 (OFL-1.1) | `OFL.TXT` of `terminus-font-4.49.1.tar.gz` |
| `licenses/go-LICENSE.txt` | Go 1.27.2 standard library and runtime (BSD-3-Clause), in `p10inst` | `/usr/local/go/LICENSE` of the pinned `golang:1.27.2-alpine` image |
| `licenses/glibc-COPYING.LIB.txt` | GNU C Library 2.41 (LGPL-2.1-or-later), in `uc-pxx-bootctl-inst` | `COPYING.LIB` of `glibc_2.41.orig.tar.xz` |
| `licenses/glibc-LICENSES.txt` | GNU C Library 2.41, the other notices | `LICENSES` of the same tarball |

The GNU General Public License version 2 (BusyBox, alpine-baselayout, the
Linux kernel, and the code of this project) is in [LICENSE](../LICENSE).
