# Release 1.3.0: the packages of the mainline system

Generated from the package database of the base system (`/lib/apk/db/installed` of the base
`rootfs.ext4`, sha256 `b4fa39fe09fb00475526201acbe0203a7c1c0b177886e6bea07ec8ce49e3d046`) and from the
package indexes of the two bundles. Every package is an unchanged binary package: Alpine Linux v3.24
(aarch64; the aports commit is the `commit` column, https://gitlab.alpinelinux.org/alpine/aports), or a
package of https://github.com/tsx-mainline/tsx-aports (no aports commit; see SOURCES.md). The license is
the package's own `license` field. Copyleft: GPL, LGPL, AGPL, MPL, EPL or CDDL in that field.

## the base system (`uc-pxx-base-rootfs.ext4.zst`, slot B and then slot A)

144 packages.

| package | version | origin | aports commit | license | copyleft |
|---|---|---|---|---|---|
| acl-libs | 2.3.2-r1 | acl | 8bac74716d06 | LGPL-2.1-or-later AND GPL-2.0-or-later | yes |
| alpine-baselayout | 3.7.2-r1 | alpine-baselayout | 60a7585bbab2 | GPL-2.0-only | yes |
| alpine-baselayout-data | 3.7.2-r1 | alpine-baselayout | 60a7585bbab2 | GPL-2.0-only | yes |
| alpine-keys | 2.6-r0 | alpine-keys | b9f23becced4 | MIT |  |
| alpine-release | 3.24.4-r0 | alpine-base | 105b835acd99 | MIT |  |
| alsa-lib | 1.2.15.3-r0 | alsa-lib | 2939b0cfc6ac | LGPL-2.1-or-later | yes |
| alsa-ucm-conf | 1.2.16.1-r0 | alsa-ucm-conf | c6af12e98010 | BSD-3-Clause |  |
| alsa-utils | 1.2.15.2-r1 | alsa-utils | 30b1881b4432 | GPL-2.0-or-later | yes |
| alsa-utils-openrc | 1.2.15.2-r1 | alsa-utils | 30b1881b4432 | GPL-2.0-or-later | yes |
| alsa-utils-udev | 1.2.15.2-r1 | alsa-utils | 30b1881b4432 | GPL-2.0-or-later | yes |
| apk-tools | 3.0.9-r0 | apk-tools | 13685e8f2cf1 | GPL-2.0-only | yes |
| avahi | 0.9_rc4-r0 | avahi | c8147a847998 | LGPL-2.1-or-later | yes |
| avahi-libs | 0.9_rc4-r0 | avahi | c8147a847998 | LGPL-2.1-or-later | yes |
| avahi-openrc | 0.9_rc4-r0 | avahi | c8147a847998 | LGPL-2.1-or-later | yes |
| bluez-btmon | 5.86-r2 | bluez | 89c57e4f72fc | GPL-2.0-or-later AND BSD-2-Clause AND MIT | yes |
| bluez-deprecated | 5.86-r2 | bluez | 89c57e4f72fc | GPL-2.0-or-later AND BSD-2-Clause AND MIT | yes |
| bluez-deprecated-openrc | 5.86-r2 | bluez | 89c57e4f72fc | GPL-2.0-or-later AND BSD-2-Clause AND MIT | yes |
| bridge | 1.5-r5 | bridge | 8bac74716d06 | GPL-2.0-or-later | yes |
| brightnessctl | 0.5.1-r9 | brightnessctl | 42dda71aadc8 | MIT |  |
| brightnessctl-openrc | 0.5.1-r9 | brightnessctl | 42dda71aadc8 | MIT |  |
| brightnessctl-udev | 0.5.1-r9 | brightnessctl | 42dda71aadc8 | MIT |  |
| brotli-libs | 1.2.1-r0 | brotli | 831335212799 | MIT |  |
| busybox | 1.37.0-r31 | busybox | c3ef5d10e6ef | GPL-2.0-only | yes |
| busybox-binsh | 1.37.0-r31 | busybox | c3ef5d10e6ef | GPL-2.0-only | yes |
| busybox-openrc | 1.37.0-r31 | busybox | c3ef5d10e6ef | GPL-2.0-only | yes |
| busybox-suid | 1.37.0-r31 | busybox | c3ef5d10e6ef | GPL-2.0-only | yes |
| c-ares | 1.34.9-r0 | c-ares | 50f178d7a4c3 | MIT |  |
| ca-certificates | 20261020-r0 | ca-certificates | e78f5987166c | MPL-2.0 AND MIT | yes |
| ca-certificates-bundle | 20261020-r0 | ca-certificates | e78f5987166c | MPL-2.0 AND MIT | yes |
| chrony | 4.8-r7 | chrony | 933158c24d6c | GPL-2.0-or-later | yes |
| chrony-common | 4.8-r7 | chrony | 933158c24d6c | GPL-2.0-or-later | yes |
| chrony-openrc | 4.8-r7 | chrony | 933158c24d6c | GPL-2.0-or-later | yes |
| curl | 8.23.0-r0 | curl | c65a7b3f6d6b | curl |  |
| dbus | 1.16.2-r2 | dbus | 0dac2d7bf760 | AFL-2.1 OR GPL-2.0-or-later | yes |
| dbus-daemon-launch-helper | 1.16.2-r2 | dbus | 0dac2d7bf760 | AFL-2.1 OR GPL-2.0-or-later | yes |
| dbus-libs | 1.16.2-r2 | dbus | 0dac2d7bf760 | AFL-2.1 OR GPL-2.0-or-later | yes |
| dbus-openrc | 1.16.2-r2 | dbus | 0dac2d7bf760 | AFL-2.1 OR GPL-2.0-or-later | yes |
| device-mapper | 2.03.35-r3 | device-mapper | a9f462dee63c | GPL-2.0-or-later AND LGPL-2.1-or-later | yes |
| device-mapper-event-libs | 2.03.35-r3 | device-mapper | a9f462dee63c | GPL-2.0-or-later AND LGPL-2.1-or-later | yes |
| device-mapper-libs | 2.03.35-r3 | device-mapper | a9f462dee63c | GPL-2.0-or-later AND LGPL-2.1-or-later | yes |
| device-mapper-udev | 2.03.35-r3 | device-mapper | a9f462dee63c | GPL-2.0-or-later AND LGPL-2.1-or-later | yes |
| dialog | 1.3.20260107-r1 | dialog | a9e2a2dc46f3 | LGPL-2.1-only | yes |
| dosfstools | 4.2-r2 | dosfstools | 9024026ee683 | GPL-3.0-or-later | yes |
| e2fsprogs | 1.47.4-r0 | e2fsprogs | 8683a6972cb2 | GPL-2.0-or-later AND LGPL-2.0-or-later AND BSD-3-Clause AND MIT | yes |
| e2fsprogs-libs | 1.47.4-r0 | e2fsprogs | 8683a6972cb2 | GPL-2.0-or-later AND LGPL-2.0-or-later AND BSD-3-Clause AND MIT | yes |
| eudev | 3.2.14-r6 | eudev | 4133df77f8cc | GPL-2.0-or-later | yes |
| eudev-libs | 3.2.14-r6 | eudev | 4133df77f8cc | GPL-2.0-or-later | yes |
| eudev-openrc | 3.2.14-r6 | eudev | 4133df77f8cc | GPL-2.0-or-later | yes |
| evtest | 1.36-r0 | evtest | d3a95ea716b5 | GPL-2.0 | yes |
| fftw-single-libs | 3.3.11-r0 | fftw | 961fdd748f46 | GPL-2.0-or-later | yes |
| gdbm | 1.26-r0 | gdbm | 1ea03990396d | GPL-3.0-or-later | yes |
| glib | 2.88.1-r1 | glib | bd96e2db11c9 | LGPL-2.1-or-later | yes |
| htop | 3.5.3-r0 | htop | abd41c49b2dd | GPL-2.0-or-later | yes |
| i2c-tools | 4.4-r3 | i2c-tools | cdbc8fbffcd7 | GPL-2.0-or-later AND LGPL-2.1-or-later | yes |
| ifupdown-ng | 0.13.0-r0 | ifupdown-ng | 2c26ffc5e83f | ISC |  |
| iw | 6.17-r0 | iw | a035abb86237 | ISC |  |
| kmod | 34.2-r1 | kmod | 206e7b39f788 | GPL-2.0-or-later | yes |
| kmod-libs | 34.2-r1 | kmod | 206e7b39f788 | LGPL-2.1-or-later | yes |
| libaio | 0.3.113-r2 | libaio | 4efcc59469af | LGPL-2.1-or-later | yes |
| libapk | 3.0.9-r0 | apk-tools | 13685e8f2cf1 | GPL-2.0-only | yes |
| libblkid | 2.42.3-r1 | util-linux | d98c55af5905 | LGPL-2.1-or-later | yes |
| libbz2 | 1.0.8-r6 | bzip2 | 33283848034c | bzip2-1.0.6 |  |
| libcap2 | 2.78-r0 | libcap | 0c35848136c4 | BSD-3-Clause OR GPL-2.0-only | yes |
| libcom_err | 1.47.4-r0 | e2fsprogs | 8683a6972cb2 | GPL-2.0-or-later AND LGPL-2.0-or-later AND BSD-3-Clause AND MIT | yes |
| libcrypto3 | 3.5.9-r0 | openssl | 29b9ec24b1b5 | Apache-2.0 |  |
| libcurl | 8.23.0-r0 | curl | c65a7b3f6d6b | curl |  |
| libdaemon | 0.14-r6 | libdaemon | f8d62aa6b1bf | MIT |  |
| libeconf | 0.8.3-r0 | libeconf | 62db33049768 | MIT |  |
| libedit | 20260508.3.1-r1 | libedit | 7c805161e588 | BSD-3-Clause |  |
| libevent | 2.1.13-r0 | libevent | 7c5a7049c577 | BSD-3-Clause |  |
| libexpat | 2.10.0-r0 | expat | af0fa42036cd | MIT |  |
| libfdisk | 2.42.3-r1 | util-linux | d98c55af5905 | LGPL-2.1-or-later | yes |
| libffi | 3.5.2-r1 | libffi | 2c2a7bb4a8b1 | MIT |  |
| libformw | 6.6_p20260516-r0 | ncurses | 2cee8a7328d0 | X11 |  |
| libgcc | 15.2.0-r5 | gcc | 423a8ad043d0 | GPL-2.0-or-later AND LGPL-2.1-or-later | yes |
| libidn2 | 2.3.8-r0 | libidn2 | 8240e09aa2fd | GPL-2.0-or-later OR LGPL-3.0-or-later | yes |
| libintl | 1.0-r0 | gettext | a0b12dbf67fb | LGPL-2.1-or-later | yes |
| libmenuw | 6.6_p20260516-r0 | ncurses | 2cee8a7328d0 | X11 |  |
| libmount | 2.42.3-r1 | util-linux | d98c55af5905 | LGPL-2.1-or-later | yes |
| libncursesw | 6.6_p20260516-r0 | ncurses | 2cee8a7328d0 | X11 |  |
| libnl3 | 3.11.0-r0 | libnl3 | 4084996615c2 | LGPL-2.1-or-later | yes |
| libpanelw | 6.6_p20260516-r0 | ncurses | 2cee8a7328d0 | X11 |  |
| libproc2 | 4.0.6-r0 | procps-ng | 99d7511b57e5 | GPL-2.0-or-later AND LGPL-2.1-or-later | yes |
| libpsl | 0.21.5-r3 | libpsl | 6bd45b53fb31 | MIT |  |
| libseccomp | 2.6.1-r0 | libseccomp | 3e05bdc04e1c | LGPL-2.1-or-later | yes |
| libsmartcols | 2.42.3-r1 | util-linux | d98c55af5905 | LGPL-2.1-or-later | yes |
| libssl3 | 3.5.9-r0 | openssl | 29b9ec24b1b5 | Apache-2.0 |  |
| libstdc++ | 15.2.0-r5 | gcc | 423a8ad043d0 | GPL-2.0-or-later AND LGPL-2.1-or-later | yes |
| libunistring | 1.4.2-r0 | libunistring | 30d9f1934038 | GPL-2.0-or-later OR LGPL-3.0-or-later | yes |
| libuuid | 2.42.3-r1 | util-linux | d98c55af5905 | BSD-3-Clause |  |
| logrotate | 3.22.0-r0 | logrotate | 37870fe268df | GPL-2.0-or-later | yes |
| logrotate-openrc | 3.22.0-r0 | logrotate | 37870fe268df | GPL-2.0-or-later | yes |
| lsof | 4.99.6-r0 | lsof | 0910b6707b36 | custom |  |
| lvm2 | 2.03.35-r2 | lvm2 | c7d038205165 | GPL-2.0-or-later AND LGPL-2.1-or-later AND BSD-2-Clause | yes |
| lvm2-libs | 2.03.35-r2 | lvm2 | c7d038205165 | GPL-2.0-or-later AND LGPL-2.1-or-later AND BSD-2-Clause | yes |
| lvm2-openrc | 2.03.35-r2 | lvm2 | c7d038205165 | GPL-2.0-or-later AND LGPL-2.1-or-later AND BSD-2-Clause | yes |
| lvm2-udev | 2.03.35-r2 | lvm2 | c7d038205165 | GPL-2.0-or-later AND LGPL-2.1-or-later AND BSD-2-Clause | yes |
| mpdecimal | 4.0.1-r0 | mpdecimal | c0519b85456e | BSD-2-Clause |  |
| musl | 1.2.6-r3 | musl | 8f79d39391b4 | MIT |  |
| musl-utils | 1.2.6-r3 | musl | 8f79d39391b4 | MIT AND BSD-2-Clause AND GPL-2.0-or-later | yes |
| ncurses-terminfo-base | 6.6_p20260516-r0 | ncurses | 2cee8a7328d0 | X11 |  |
| nghttp2-libs | 1.70.0-r0 | nghttp2 | d5a34efc88bb | MIT |  |
| openrc | 0.63.2-r0 | openrc | 30c61338b7a0 | BSD-2-Clause |  |
| openrc-user | 0.63.2-r0 | openrc | 30c61338b7a0 | BSD-2-Clause |  |
| openssh-client-common | 10.3_p1-r2 | openssh | 384e25e6bd1f | SSH-OpenSSH |  |
| openssh-client-default | 10.3_p1-r2 | openssh | 384e25e6bd1f | SSH-OpenSSH |  |
| openssh-keygen | 10.3_p1-r2 | openssh | 384e25e6bd1f | SSH-OpenSSH |  |
| openssh-server | 10.3_p1-r2 | openssh | 384e25e6bd1f | SSH-OpenSSH |  |
| openssh-server-common | 10.3_p1-r2 | openssh | 384e25e6bd1f | SSH-OpenSSH |  |
| openssh-server-common-openrc | 10.3_p1-r2 | openssh | 384e25e6bd1f | SSH-OpenSSH |  |
| openssh-sftp-server | 10.3_p1-r2 | openssh | 384e25e6bd1f | SSH-OpenSSH |  |
| pcre2 | 10.49-r0 | pcre2 | 3d497cbb3217 | BSD-3-Clause |  |
| popt | 1.19-r4 | popt | d3abe30d4352 | MIT |  |
| procps-ng | 4.0.6-r0 | procps-ng | 99d7511b57e5 | GPL-2.0-or-later AND LGPL-2.1-or-later | yes |
| pyc | 3.14.10-r1 | python3 | 831cfeb7301d | PSF-2.0 |  |
| python3 | 3.14.10-r1 | python3 | 831cfeb7301d | PSF-2.0 |  |
| python3-pyc | 3.14.10-r1 | python3 | 831cfeb7301d | PSF-2.0 |  |
| python3-pycache-pyc0 | 3.14.10-r1 | python3 | 831cfeb7301d | PSF-2.0 |  |
| readline | 8.3.3-r1 | readline | a854c03acdac | GPL-3.0-or-later | yes |
| scanelf | 1.3.9-r1 | pax-utils | c61801eeacb3 | GPL-2.0-only | yes |
| setarch | 2.42.3-r1 | util-linux | d98c55af5905 | GPL-3.0-or-later AND GPL-2.0-or-later AND GPL-2.0-only AND GPL-1.0-only AND LGPL-2.1-or-later AND BSD-1-Clause AND BSD-3-Clause AND BSD-4-Clause-UC AND MIT AND Public-Domain | yes |
| skalibs-libs | 2.15.0.0-r0 | skalibs | f148233038fa | ISC |  |
| sqlite-libs | 3.53.4-r0 | sqlite | a9c3ba8b9129 | blessing |  |
| ssl_client | 1.37.0-r31 | busybox | c3ef5d10e6ef | GPL-2.0-only | yes |
| tsx-autoupdate | 0.3.13_pre20261114-r0 | tsx-autoupdate | — | GPL-2.0-or-later | yes |
| tsx-base | 0.3.13_pre20261113-r0 | tsx-base | — | GPL-2.0-or-later | yes |
| tsx-buttons | 0.3.13_pre20261113-r0 | tsx-buttons | — | GPL-2.0-or-later | yes |
| tsx-idled | 0.3.13_pre20261113-r0 | tsx-idled | — | GPL-2.0-or-later | yes |
| tsx-keys | 1-r0 | tsx-keys | — | GPL-2.0-or-later | yes |
| tsx-splash | 0.3.13_pre20261113-r0 | tsx-splash | — | GPL-2.0-or-later | yes |
| tzdata | 2026f-r0 | tzdata | 63faba7f731a | Public-Domain |  |
| uc-pxx-board | 0.2.0-r0 | uc-pxx-board | — | GPL-2.0-or-later | yes |
| uc-pxx-boot-tools | 0.2.0-r1 | uc-pxx-boot-tools | — | GPL-2.0-or-later | yes |
| uc-pxx-console | 0.1.0-r1 | uc-pxx-console | — | GPL-2.0-or-later | yes |
| uc-pxx-kernel-lts | 6.18.54_git20261110-r0 | uc-pxx-kernel-lts | — | GPL-2.0-only | yes |
| uc-pxx-leds | 0.2.0-r0 | uc-pxx-leds | — | GPL-2.0-or-later | yes |
| udev-init-scripts | 35-r1 | udev-init-scripts | 7501172a3124 | GPL-2.0-only | yes |
| udev-init-scripts-openrc | 35-r1 | udev-init-scripts | 7501172a3124 | GPL-2.0-only | yes |
| util-linux-misc | 2.42.3-r1 | util-linux | d98c55af5905 | GPL-3.0-or-later AND GPL-2.0-or-later AND GPL-2.0-only AND GPL-1.0-only AND LGPL-2.1-or-later AND BSD-1-Clause AND BSD-3-Clause AND BSD-4-Clause-UC AND MIT AND Public-Domain | yes |
| utmps-libs | 0.1.3.3-r0 | utmps | 900a4ecef77a | ISC |  |
| wireless-regdb | 2025.10.07-r0 | wireless-regdb | b622fe09a3d2 | ISC |  |
| xz-libs | 5.8.4-r0 | xz | 0c088d609f9f | GPL-2.0-or-later AND 0BSD AND Public-Domain AND LGPL-2.1-or-later | yes |
| zlib | 1.4-r0 | zlib | bdf501c870a9 | Zlib |  |
| zstd-libs | 1.5.7-r2 | zstd | 3c6e2ee2b16f | BSD-3-Clause OR GPL-2.0-or-later | yes |

## the bundle `uc-pxx-profile-kiosk-ha-0.2.0-rc10.tar` (Kiosk and Home Assistant)

277 packages.

| package | version | origin | aports commit | license | copyleft |
|---|---|---|---|---|---|
| abseil-cpp-raw-logging-internal | 20250814.1-r0 | abseil-cpp | 68b166903c94 | Apache-2.0 |  |
| abseil-cpp-strings | 20250814.1-r0 | abseil-cpp | 68b166903c94 | Apache-2.0 |  |
| abseil-cpp-strings-internal | 20250814.1-r0 | abseil-cpp | 68b166903c94 | Apache-2.0 |  |
| aom-libs | 3.14.1-r0 | aom | ca6a710df98a | BSD-2-Clause AND custom |  |
| at-spi2-core | 2.60.5-r0 | at-spi2-core | 127aabdda292 | LGPL-2.1-or-later | yes |
| at-spi2-core-libs | 2.60.5-r0 | at-spi2-core | 127aabdda292 | LGPL-2.1-or-later | yes |
| avahi-compat-libdns_sd | 0.9_rc4-r0 | avahi | c8147a847998 | LGPL-2.1-or-later | yes |
| basu | 0.2.1-r2 | basu | 6bd45b53fb31 | LGPL-2.1-or-later | yes |
| bubblewrap | 0.12.0-r0 | bubblewrap | 21ce0edacfd8 | LGPL-2.1-or-later | yes |
| cairo | 1.18.4-r1 | cairo | e54632f6ef65 | LGPL-2.1-or-later OR MPL-1.1 | yes |
| cairo-gobject | 1.18.4-r1 | cairo | e54632f6ef65 | LGPL-2.1-or-later OR MPL-1.1 | yes |
| chromium | 156.0.8078.79-r0 | chromium | bf3db421d5bb | BSD-3-Clause |  |
| chromium-angle | 156.0.8078.79-r0 | chromium | bf3db421d5bb | BSD-3-Clause |  |
| chromium-common | 156.0.8078.79-r0 | chromium | bf3db421d5bb | BSD-3-Clause |  |
| cjson | 1.7.19-r1 | cjson | 635e41d25a76 | MIT |  |
| crc32c | 1.1.2-r3 | crc32c | b72d47c7fa33 | BSD-3-Clause |  |
| cups-libs | 2.4.19-r1 | cups | 636370bb6f28 | Apache-2.0 |  |
| double-conversion | 3.4.0-r0 | double-conversion | f5a68812ed21 | BSD-3-Clause |  |
| encodings | 1.1.0-r0 | encodings | c690d2215724 | Public Domain |  |
| feedbackd | 0.8.9-r2 | feedbackd | 1e14fb21989b | GPL-3.0-or-later | yes |
| feedbackd-device-themes | 0.8.9-r0 | feedbackd-device-themes | 549e84793b0f | GPL-3.0-or-later | yes |
| feedbackd-udev | 0.8.9-r2 | feedbackd | 1e14fb21989b | GPL-3.0-or-later | yes |
| ffmpeg-libavcodec | 8.1.3-r0 | ffmpeg | 66cae15cc197 | GPL-2.0-or-later AND LGPL-2.1-or-later | yes |
| ffmpeg-libavdevice | 8.1.3-r0 | ffmpeg | 66cae15cc197 | GPL-2.0-or-later AND LGPL-2.1-or-later | yes |
| ffmpeg-libavfilter | 8.1.3-r0 | ffmpeg | 66cae15cc197 | GPL-2.0-or-later AND LGPL-2.1-or-later | yes |
| ffmpeg-libavformat | 8.1.3-r0 | ffmpeg | 66cae15cc197 | GPL-2.0-or-later AND LGPL-2.1-or-later | yes |
| ffmpeg-libavutil | 8.1.3-r0 | ffmpeg | 66cae15cc197 | GPL-2.0-or-later AND LGPL-2.1-or-later | yes |
| ffmpeg-libswresample | 8.1.3-r0 | ffmpeg | 66cae15cc197 | GPL-2.0-or-later AND LGPL-2.1-or-later | yes |
| ffmpeg-libswscale | 8.1.3-r0 | ffmpeg | 66cae15cc197 | GPL-2.0-or-later AND LGPL-2.1-or-later | yes |
| fftw-double-libs | 3.3.11-r0 | fftw | 961fdd748f46 | GPL-2.0-or-later | yes |
| file | 5.47-r2 | file | a05d36fa8641 | BSD-2-Clause |  |
| font-dejavu | 2.37-r6 | font-dejavu | 59420f440b2e | Bitstream-Vera |  |
| font-opensans | 0_git20210927-r1 | font-opensans | ec70c07cd709 | Apache-2.0 |  |
| fontconfig | 2.17.1-r1 | fontconfig | 07061cb5a288 | MIT |  |
| freetype | 2.14.3-r0 | freetype | ad16b0afc1a3 | FTL OR GPL-2.0-or-later | yes |
| fribidi | 1.0.16-r3 | fribidi | 5c9b8291bfb3 | LGPL-2.1-or-later | yes |
| gdk-pixbuf | 2.44.7-r1 | gdk-pixbuf | a21007f809f4 | LGPL-2.1-or-later | yes |
| glslang-libs | 1.4.341.0-r0 | glslang | 805ebd1e5465 | BSD-3-Clause AND BSD-2-Clause AND MIT AND Apache-2.0 AND GPL-3.0-or-later | yes |
| glycin-image-rs | 2.1.5-r0 | glycin | 78ed74f69dc1 | MPL-2.0 OR LGPL-2.0-or-later | yes |
| glycin-svg | 2.1.5-r0 | glycin | 78ed74f69dc1 | MPL-2.0 OR LGPL-2.0-or-later | yes |
| gmobile | 0.7.2-r0 | gmobile | e5e49f8c66f4 | GPL-3.0-only | yes |
| gmobile-udev | 0.7.2-r0 | gmobile | e5e49f8c66f4 | GPL-3.0-only | yes |
| gmp | 6.3.0-r4 | gmp | 206e7b39f788 | LGPL-3.0-or-later OR GPL-2.0-or-later | yes |
| gnutls | 3.8.13-r0 | gnutls | c09f2e94a4b0 | LGPL-2.1-or-later | yes |
| gobject-introspection | 1.86.0-r1 | gobject-introspection | f48701aa97c7 | LGPL-2.0-or-later AND GPL-2.0-or-later AND MIT | yes |
| graphite2 | 1.3.14-r6 | graphite2 | 0016c47e5316 | LGPL-2.1-or-later OR MPL-1.1 | yes |
| grim | 1.5.0-r0 | grim | 63e0874f4b1c | MIT |  |
| gsound | 1.0.3-r3 | gsound | 2941f70a8645 | LGPL-2.1-or-later | yes |
| gtk+3.0 | 3.24.52-r0 | gtk+3.0 | 81ac3343fbf0 | LGPL-2.1-or-later | yes |
| harfbuzz | 13.2.1-r0 | harfbuzz | 0e23da21a00f | MIT |  |
| harfbuzz-subset | 13.2.1-r0 | harfbuzz | 0e23da21a00f | MIT |  |
| hwdata-pci | 0.408-r0 | hwdata | 6f27b0b36ae7 | GPL-2.0-or-later OR XFree86-1.1 | yes |
| icu-data-full | 78.1-r0 | icu | 6755fdb21483 | ICU |  |
| jack | 1.9.22-r4 | jack | f55d8d2dd5f2 | GPL-2.0-or-later | yes |
| json-c | 0.18-r1 | json-c | 8e0faa63c91a | MIT |  |
| json-glib | 1.10.8-r0 | json-glib | 2941f70a8645 | LGPL-2.1-or-later | yes |
| kmscube | 0_git20220902-r1 | kmscube | 2caff6799201 | MIT |  |
| lame-libs | 3.100-r5 | lame | ffd76e0bae99 | LGPL-2.0-or-later | yes |
| lcms2 | 2.19-r0 | lcms2 | b103184b887b | MIT |  |
| libSvtAv1Enc | 4.1.0-r0 | svt-av1 | a75bcf7a688f | BSD-3-Clause-Clear |  |
| libarchive | 3.8.11-r0 | libarchive | 931582d4d89b | BSD-2-Clause AND BSD-3-Clause AND Public-Domain |  |
| libass | 0.17.4-r1 | libass | 18b069a4afa2 | ISC |  |
| libasyncns | 0.8-r5 | libasyncns | a9e2a2dc46f3 | LGPL-2.0-or-later | yes |
| libatk-1.0 | 2.60.5-r0 | at-spi2-core | 127aabdda292 | LGPL-2.1-or-later | yes |
| libatk-bridge-2.0 | 2.60.5-r0 | at-spi2-core | 127aabdda292 | LGPL-2.1-or-later | yes |
| libbluray | 1.4.0-r0 | libbluray | 7d7242f055a0 | LGPL-2.1-or-later | yes |
| libbsd | 0.12.2-r0 | libbsd | 02365e78d654 | BSD-3-Clause |  |
| libcaca | 0.99_beta20-r5 | libcaca | 176fb8daf23f | WTFPL |  |
| libcamera | 0.7.1-r1 | libcamera | c47555ebb1c7 | LGPL-2.1-or-later AND GPL-2.0-or-later | yes |
| libcamera-ipa | 0.7.1-r1 | libcamera | c47555ebb1c7 | LGPL-2.1-or-later AND GPL-2.0-or-later | yes |
| libcanberra | 0.30-r12 | libcanberra | bbbd9f6db611 | LGPL-2.1-or-later | yes |
| libcanberra-alsa | 0.30-r12 | libcanberra | bbbd9f6db611 | LGPL-2.1-or-later | yes |
| libcdio | 2.3.0-r0 | libcdio | bca1e6ac02e2 | GPL-3.0-or-later | yes |
| libcdio-paranoia | 10.2.2.0.2_git20260227-r0 | libcdio-paranoia | 10c1499ee441 | GPL-2.0-or-later AND LGPL-2.0-or-later | yes |
| libdav1d | 1.5.3-r0 | dav1d | 6a13170ddff8 | BSD-2-Clause |  |
| libdisplay-info | 0.3.0-r0 | libdisplay-info | 5c472425b844 | MIT |  |
| libdovi | 3.3.2-r0 | libdovi | 80739e9ec068 | MIT |  |
| libdrm | 2.4.134-r0 | libdrm | 2015800769b7 | MIT |  |
| libdrm-tests | 2.4.134-r0 | libdrm | 2015800769b7 | MIT |  |
| libdvdcss | 1.4.3-r0 | libdvdcss | 3add37f3a9a8 | GPL-2.0-or-later | yes |
| libdvdnav | 6.1.1-r1 | libdvdnav | be0f852b9283 | GPL-2.0-or-later | yes |
| libdvdread | 6.1.3-r2 | libdvdread | 4f8157b2e125 | GPL-2.0-or-later | yes |
| libelf | 0.195-r0 | elfutils | 8f1329420688 | GPL-3.0-or-later AND ( GPL-2.0-or-later OR LGPL-3.0-or-later ) | yes |
| libelogind | 255.24-r0 | elogind | f7e4c2c1ed47 | GPL-2.0-or-later LGPL-2.1-or-later | yes |
| libepoxy | 1.5.10-r1 | libepoxy | 6bd45b53fb31 | MIT |  |
| libevdev | 1.13.6-r0 | libevdev | 2af4dd008295 | MIT |  |
| libflac | 1.4.3-r2 | flac | 253ee9b07f76 | BSD-3-Clause AND GPL-2.0-or-later | yes |
| libfontenc | 1.1.9-r0 | libfontenc | 9667f1121e20 | MIT |  |
| libgfortran | 15.2.0-r5 | gcc | 423a8ad043d0 | GPL-2.0-or-later AND LGPL-2.1-or-later | yes |
| libglycin | 2.1.5-r0 | libglycin | ce376ccfea45 | MPL-2.0 OR LGPL-2.0-or-later | yes |
| libgnome-desktop-3 | 44.5-r0 | gnome-desktop | f77596a3f338 | GPL-2.0-or-later AND LGPL-2.1-or-later | yes |
| libgomp | 15.2.0-r5 | gcc | 423a8ad043d0 | GPL-2.0-or-later AND LGPL-2.1-or-later | yes |
| libgudev | 238-r1 | libgudev | c97799aa548f | LGPL-2.1-or-later | yes |
| libhwy | 1.3.0-r0 | highway | 880de784cce2 | Apache-2.0 |  |
| libice | 1.1.2-r0 | libice | d4941e833167 | X11 |  |
| libinput | 1.31.3-r0 | libinput | 3241f28bdb55 | MIT |  |
| libinput-libs | 1.31.3-r0 | libinput | 3241f28bdb55 | MIT |  |
| libinput-udev | 1.31.3-r0 | libinput | 3241f28bdb55 | MIT |  |
| libjpeg-turbo | 3.1.3-r0 | libjpeg-turbo | 5fe6bebf850f | BSD-3-Clause AND IJG AND Zlib |  |
| libjxl | 0.11.2-r1 | libjxl | 9ec8d4495d91 | Apache-2.0 |  |
| libliftoff | 0.5.0-r0 | libliftoff | 6bd45b53fb31 | MIT |  |
| libltdl | 2.6.0-r1 | libtool | 21b05914806e | LGPL-2.0-or-later AND GPL-2.0-or-later | yes |
| libmagic | 5.47-r2 | file | a05d36fa8641 | BSD-2-Clause |  |
| libmd | 1.2.0-r0 | libmd | 87b78bc68906 | BSD-3-Clause AND BSD-2-Clause AND ISC AND Beerware AND Public Domain |  |
| libogg | 1.3.6-r0 | libogg | c17e8cd839de | BSD-3-Clause |  |
| libopenmpt | 0.8.10-r0 | libopenmpt | 58a88f3b7c7e | BSD-3-Clause |  |
| libpciaccess | 0.19-r0 | libpciaccess | d5de88aad809 | X11 |  |
| libpisp | 1.5.0-r0 | libpisp | 06dcfbef45b4 | BSD-2-Clause |  |
| libplacebo | 7.360.1-r0 | libplacebo | ce0bd2062780 | LGPL-2.1-or-later | yes |
| libpng | 1.6.59-r0 | libpng | 8f222b50cd6e | Libpng |  |
| libpulse | 17.0-r7 | pulseaudio | e4821ce2dcff | LGPL-2.1-or-later | yes |
| librist | 0.2.15-r0 | librist | d9adfca901be | BSD-2-Clause |  |
| librsvg | 2.62.3-r0 | librsvg | c760246e8554 | LGPL-2.1-or-later | yes |
| libsamplerate | 0.2.2-r3 | libsamplerate | 33283848034c | BSD-2-Clause |  |
| libseat | 0.9.3-r0 | seatd | 493aed103009 | MIT |  |
| libsharpyuv | 1.6.0-r0 | libwebp | af8b48fabd19 | BSD-3-Clause |  |
| libsixel | 1.10.5-r0 | libsixel | e87270af5dd0 | MIT |  |
| libsm | 1.2.6-r0 | libsm | 4eaa95753776 | MIT |  |
| libsndfile | 1.2.2-r2 | libsndfile | 3acdcc403bf3 | LGPL-2.1-or-later | yes |
| libsodium | 1.0.22-r0 | libsodium | 43738991b7e8 | ISC |  |
| libsrt | 1.5.3-r1 | libsrt | 2d9f0a6490c4 | MPL-2.0 | yes |
| libssh | 0.12.2-r0 | libssh | 16037a8794a4 | LGPL-2.1-or-later BSD-2-Clause | yes |
| libtasn1 | 4.21.0-r0 | libtasn1 | 771f180774aa | LGPL-2.1-or-later | yes |
| libtheora | 1.2.0-r1 | libtheora | 5e1b822309fb | BSD-3-Clause |  |
| libudfread | 1.2.0-r1 | libudfread | 5c9b8291bfb3 | LGPL-2.1-or-later | yes |
| libunibreak | 6.1-r0 | libunibreak | 80739e9ec068 | Zlib |  |
| libunwind | 1.8.3-r0 | libunwind | 1789f1e1e73c | MIT |  |
| libuv | 1.52.1-r0 | libuv | 8d19451845ed | MIT |  |
| libva | 2.23.0-r0 | libva | 217e378f7609 | MIT |  |
| libvdpau | 1.5-r4 | libvdpau | 6bd45b53fb31 | MIT |  |
| libvorbis | 1.3.7-r2 | libvorbis | fc95c5999828 | BSD-3-Clause |  |
| libvpx | 1.15.2-r1 | libvpx | c4a9322622d7 | BSD-3-Clause |  |
| libwebp | 1.6.0-r0 | libwebp | af8b48fabd19 | BSD-3-Clause |  |
| libwebpdemux | 1.6.0-r0 | libwebp | af8b48fabd19 | BSD-3-Clause |  |
| libwebpmux | 1.6.0-r0 | libwebp | af8b48fabd19 | BSD-3-Clause |  |
| libx11 | 1.8.14-r0 | libx11 | e129d974905f | X11 |  |
| libxau | 1.0.12-r0 | libxau | 6bd45b53fb31 | MIT |  |
| libxcb | 1.17.0-r2 | libxcb | 5e1b822309fb | MIT |  |
| libxcomposite | 0.4.7-r0 | libxcomposite | c0827d537025 | MIT |  |
| libxcursor | 1.2.3-r0 | libxcursor | 1d6d12ed6f5c | MIT |  |
| libxdamage | 1.1.7-r0 | libxdamage | 5ef54777a856 | MIT |  |
| libxdmcp | 1.1.5-r1 | libxdmcp | 614e9166706c | MIT |  |
| libxext | 1.3.7-r0 | libxext | 4ec252fa3e6e | MIT |  |
| libxfixes | 6.0.2-r0 | libxfixes | 9d33b3af389f | MIT |  |
| libxft | 2.3.9-r0 | libxft | 9a4062b0f278 | MIT |  |
| libxi | 1.8.4-r0 | libxi | d7f98a7160b7 | MIT AND X11 |  |
| libxinerama | 1.1.6-r0 | libxinerama | 4b83e9a57e62 | MIT |  |
| libxkbcommon | 1.13.1-r0 | libxkbcommon | 76aa0b70442b | MIT AND MIT-open-group AND HPND AND HPND-sell-variant AND LicenseRef-digital-equipment-corporation |  |
| libxml2 | 2.13.9-r2 | libxml2 | f1bd5b6767a0 | MIT |  |
| libxmu | 1.3.1-r0 | libxmu | 697576337178 | MIT |  |
| libxpresent | 1.0.2-r0 | libxpresent | 98ac3e31d3e9 | MIT |  |
| libxrandr | 1.5.5-r0 | libxrandr | 4a608248a9e0 | MIT |  |
| libxrender | 0.9.13-r0 | libxrender | 5ccdccc7a1db | MIT |  |
| libxscrnsaver | 1.2.5-r0 | libxscrnsaver | 7af1732d369e | MIT |  |
| libxshmfence | 1.3.3-r0 | libxshmfence | 8bac74716d06 | MIT |  |
| libxslt | 1.1.43-r3 | libxslt | 730f2f5bdc97 | X11 |  |
| libxt | 1.3.1-r0 | libxt | 60222ebeadec | MIT |  |
| libxtst | 1.2.6-r0 | libxtst | 9b53c2882fa6 | MIT |  |
| libxv | 1.0.13-r0 | libxv | 9ce32dfd15fa | MIT |  |
| libxxf86vm | 1.1.7-r0 | libxxf86vm | ac89fbb35d37 | MIT |  |
| libyuv | 0.0.1928-r0 | libyuv | 6d055fa770a2 | BSD-3-Clause |  |
| libzmq | 4.3.5-r2 | zeromq | 03fe9a0a6194 | MPL-2.0 | yes |
| lilv-libs | 0.24.26-r1 | lilv | d70e64dd3579 | ISC |  |
| llvm22-libs | 22.1.3-r1 | llvm22 | 7d10bc6cb2ec | Apache-2.0 |  |
| luajit | 2.1_p20251030-r0 | luajit | 3d4f2edabc7c | MIT |  |
| lz4-libs | 1.10.0-r1 | lz4 | 1f16962f3423 | BSD-2-Clause AND GPL-2.0-or-later | yes |
| mbedtls3 | 3.6.7-r0 | mbedtls3 | a4cda3e2ff52 | Apache-2.0 OR GPL-2.0-or-later | yes |
| mesa | 26.1.6-r0 | mesa | b94e73658216 | MIT AND SGI-B-2.0 AND BSL-1.0 |  |
| mesa-dri-gallium | 26.1.6-r0 | mesa | b94e73658216 | MIT AND SGI-B-2.0 AND BSL-1.0 |  |
| mesa-egl | 26.1.6-r0 | mesa | b94e73658216 | MIT AND SGI-B-2.0 AND BSL-1.0 |  |
| mesa-gbm | 26.1.6-r0 | mesa | b94e73658216 | MIT AND SGI-B-2.0 AND BSL-1.0 |  |
| mesa-gl | 26.1.6-r0 | mesa | b94e73658216 | MIT AND SGI-B-2.0 AND BSL-1.0 |  |
| mesa-gles | 26.1.6-r0 | mesa | b94e73658216 | MIT AND SGI-B-2.0 AND BSL-1.0 |  |
| mesa-utils | 9.0.0-r6 | mesa-demos | 6bd45b53fb31 | custom |  |
| minizip | 1.3.2-r1 | minizip | e22dc13fc625 | Zlib |  |
| mkfontscale | 1.2.4-r0 | mkfontscale | e4301c788920 | MIT |  |
| mosquitto-clients | 2.1.2-r1 | mosquitto | 63d56ef133da | EPL-1.0 OR EPL-2.0 | yes |
| mosquitto-libs | 2.1.2-r1 | mosquitto | 63d56ef133da | EPL-1.0 OR EPL-2.0 | yes |
| mpg123-libs | 1.33.5-r0 | mpg123 | 017919108b43 | LGPL-2.1-only | yes |
| mpv-libs | 0.41.0-r2 | mpv | 58165463f1a1 | GPL-2.0-or-later | yes |
| mtdev | 1.1.7-r0 | mtdev | 6867e384dbfb | MIT |  |
| nettle | 3.10.2-r0 | nettle | bf898e6d067c | GPL-2.0-or-later OR LGPL-3.0-or-later | yes |
| nspr | 4.38.2-r0 | nspr | 4e10628e0ece | MPL-2.0 | yes |
| nss | 3.124-r0 | nss | 422faa8d3631 | MPL-2.0 | yes |
| numactl | 2.0.19-r0 | numactl | ac972e48bfad | LGPL-2.1-only | yes |
| openblas | 0.3.30-r2 | openblas | 2999d76115d6 | BSD-3-Clause |  |
| openh264 | 2.6.0-r0 | openh264 | 3e3fa0ef3b55 | BSD-2-Clause |  |
| opus | 1.6.1-r0 | opus | 6664b23b1c7a | BSD-3-Clause |  |
| orc | 0.4.41-r0 | orc | 35fe4add84d7 | BSD-2-Clause |  |
| p11-kit | 0.26.2-r0 | p11-kit | 922ca1ffc9cc | BSD-3-Clause |  |
| pango | 1.57.1-r0 | pango | 18857621f16b | LGPL-2.1-or-later | yes |
| pipewire-libs | 1.6.8-r4 | pipewire | ad5814b3ee6f | MIT AND LGPL-2.1-or-later | yes |
| pixman | 0.46.4-r0 | pixman | 9337ac809a43 | MIT |  |
| pkgconf | 2.5.1-r0 | pkgconf | c6f80aa8e59c | ISC |  |
| py3-aiohappyeyeballs | 2.6.1-r1 | py3-aiohappyeyeballs | 28ea5ee98cfe | Apache-2.0 |  |
| py3-aiohappyeyeballs-pyc | 2.6.1-r1 | py3-aiohappyeyeballs | 28ea5ee98cfe | Apache-2.0 |  |
| py3-attrs | 26.1.0-r0 | py3-attrs | d86e2f56cbf9 | MIT |  |
| py3-attrs-pyc | 26.1.0-r0 | py3-attrs | d86e2f56cbf9 | MIT |  |
| py3-cffi | 2.0.0-r0 | py3-cffi | 9964385871c8 | MIT |  |
| py3-cffi-pyc | 2.0.0-r0 | py3-cffi | 9964385871c8 | MIT |  |
| py3-cparser | 3.0-r1 | py3-cparser | 0b6a4f25cc3a | BSD-3-Clause |  |
| py3-cparser-pyc | 3.0-r1 | py3-cparser | 0b6a4f25cc3a | BSD-3-Clause |  |
| py3-cryptography | 47.0.0-r0 | py3-cryptography | 4fc0c823f4f7 | Apache-2.0 OR BSD-3-Clause |  |
| py3-cryptography-pyc | 47.0.0-r0 | py3-cryptography | 4fc0c823f4f7 | Apache-2.0 OR BSD-3-Clause |  |
| py3-ifaddr | 0.2.0-r5 | py3-ifaddr | 4d9794fab047 | MIT |  |
| py3-ifaddr-pyc | 0.2.0-r5 | py3-ifaddr | 4d9794fab047 | MIT |  |
| py3-mpv | 1.0.8-r1 | py3-mpv | ab96d5ffbef5 | MIT |  |
| py3-mpv-pyc | 1.0.8-r1 | py3-mpv | ab96d5ffbef5 | MIT |  |
| py3-numpy | 2.4.6-r0 | py3-numpy | 5c3024984bfd | BSD-3-Clause |  |
| py3-numpy-pyc | 2.4.6-r0 | py3-numpy | 5c3024984bfd | BSD-3-Clause |  |
| py3-numpy-tests | 2.4.6-r0 | py3-numpy | 5c3024984bfd | BSD-3-Clause |  |
| py3-protobuf | 6.33.5-r1 | py3-protobuf | 37a9135d8939 | BSD-3-Clause |  |
| py3-protobuf-pyc | 6.33.5-r1 | py3-protobuf | 37a9135d8939 | BSD-3-Clause |  |
| py3-tzdata | 2026.4-r0 | py3-tzdata | 0d43a5a5ce3d | Apache-2.0 |  |
| py3-tzdata-pyc | 2026.4-r0 | py3-tzdata | 0d43a5a5ce3d | Apache-2.0 |  |
| py3-tzlocal | 5.3.1-r1 | py3-tzlocal | e8b901ba9f44 | MIT |  |
| py3-tzlocal-pyc | 5.3.1-r1 | py3-tzlocal | e8b901ba9f44 | MIT |  |
| py3-zeroconf | 0.147.2-r1 | py3-zeroconf | 1cdb3ef97091 | LGPL-2.0-or-later | yes |
| py3-zeroconf-pyc | 0.147.2-r1 | py3-zeroconf | 1cdb3ef97091 | LGPL-2.0-or-later | yes |
| rav1e-libs | 0.8.1-r0 | rav1e | 0f7061c11f34 | BSD-2-Clause custom |  |
| roc-toolkit-libs | 0.4.0-r1 | roc-toolkit | 9ea23937b2c6 | MPL-2.0 | yes |
| rubberband-libs | 4.0.0-r0 | rubberband | b282397859aa | GPL-2.0-only | yes |
| seatd | 0.9.3-r0 | seatd | 493aed103009 | MIT |  |
| seatd-openrc | 0.9.3-r0 | seatd | 493aed103009 | MIT |  |
| sendspin-cli | 0.3.0-r2 | sendspin-cli | — | MIT |  |
| serd-libs | 0.32.8-r0 | serd | 72f72e6d97be | ISC |  |
| shaderc | 2026.1-r0 | shaderc | 31b84d5dd129 | Apache-2.0 |  |
| shared-mime-info | 2.4-r7 | shared-mime-info | 74df86bcbb3a | GPL-2.0-or-later | yes |
| sndio-libs | 1.10.0-r4 | sndio | a9e2a2dc46f3 | ISC |  |
| sord-libs | 0.16.22-r0 | sord | 15a91cfc65d2 | ISC |  |
| sound-theme-freedesktop | 0.8-r2 | sound-theme-freedesktop | 619f65318054 | CC-BY-SA-3.0 AND GPL-2.0-or-later AND GPL-2.0-only AND CC-BY-3.0 | yes |
| soxr | 0.1.3-r7 | soxr | a7ec97e438d9 | LGPL-2.1-or-later | yes |
| speexdsp | 1.2.1-r2 | speexdsp | 33283848034c | BSD-3-Clause |  |
| spirv-tools | 1.4.341.0-r0 | spirv-tools | 5115d4d26db3 | Apache-2.0 |  |
| squeekboard | 1.43.1-r2 | squeekboard | 3faf0a5afd7a | GPL-3.0-or-later | yes |
| sratom | 0.6.20-r0 | sratom | 4a87b165685f | ISC |  |
| sway | 1.12_rc3-r0 | sway | 9c2aa7281ad6 | MIT |  |
| sway-openrc | 1.12_rc3-r0 | sway | 9c2aa7281ad6 | MIT |  |
| sway-wallpapers | 1.12_rc3-r0 | sway | 9c2aa7281ad6 | CC0-1.0 |  |
| swaybar | 1.12_rc3-r0 | sway | 9c2aa7281ad6 | MIT |  |
| swaybg | 1.2.2-r0 | swaybg | 680e3af93865 | MIT |  |
| swaybg-openrc | 1.2.2-r0 | swaybg | 680e3af93865 | MIT |  |
| swaynag | 1.12_rc3-r0 | sway | 9c2aa7281ad6 | MIT |  |
| tdb-libs | 1.4.15-r1 | tdb | 138cce73b696 | LGPL-3.0-or-later | yes |
| tensorflow-lite-c | 2.17.1-r1 | tensorflow-lite-c | — | Apache-2.0 |  |
| tsx-ha | 0.3.13_pre20261113-r0 | tsx-ha | — | GPL-2.0-or-later | yes |
| tsx-kiosk | 0.3.13_pre20261113-r0 | tsx-kiosk | — | GPL-2.0-or-later | yes |
| tsx-panel-app | 0.3.13_pre20261113-r0 | tsx-panel-app | — | GPL-2.0-or-later | yes |
| tsx-sendspin | 0.3.13_pre20261113-r0 | tsx-sendspin | — | GPL-2.0-or-later | yes |
| tsx-setup | 0.3.13_pre20261113-r0 | tsx-setup | — | GPL-2.0-or-later | yes |
| uc-pxx-ha | 0.1.0-r1 | uc-pxx-ha | — | GPL-2.0-or-later | yes |
| uc-pxx-kiosk | 0.1.0-r1 | uc-pxx-kiosk | — | GPL-2.0-or-later | yes |
| uchardet-libs | 0.0.8-r2 | uchardet | 9dba0c523e5d | MPL-1.1 | yes |
| v4l-utils-libs | 1.32.0-r1 | v4l-utils | 5e1b822309fb | LGPL-2.0-or-later | yes |
| vidstab | 1.1.1-r0 | vidstab | 24eb3bcfeb27 | GPL-2.0-or-later | yes |
| vulkan-loader | 1.4.347-r0 | vulkan-loader | 07bac49555d7 | Apache-2.0 |  |
| wayland-libs-client | 1.25.0-r0 | wayland | 5960a9697428 | MIT |  |
| wayland-libs-cursor | 1.25.0-r0 | wayland | 5960a9697428 | MIT |  |
| wayland-libs-egl | 1.25.0-r0 | wayland | 5960a9697428 | MIT |  |
| wayland-libs-server | 1.25.0-r0 | wayland | 5960a9697428 | MIT |  |
| wayland-utils | 1.3.0-r0 | wayland-utils | 18aa555d1028 | MIT |  |
| webrtc-audio-processing-2 | 2.1-r1 | webrtc-audio-processing-2 | f6f70577078e | custom |  |
| wlr-randr | 0.5.0-r1 | wlr-randr | 281864040170 | MIT |  |
| wlroots0.20 | 0.20.2-r0 | wlroots0.20 | 7ba24e6c28ed | MIT |  |
| wvkbd-mobintl | 0.19.4-r0 | wvkbd | 725a2646cc8b | GPL-3.0-or-later | yes |
| x264-libs | 0.164.3108-r1 | x264 | 21efe0fb007d | GPL-2.0-or-later | yes |
| x265-libs | 4.1-r0 | x265 | d3b7a03815c4 | GPL-2.0-or-later | yes |
| xcb-util-renderutil | 0.3.10-r0 | xcb-util-renderutil | 72959c97a7f1 | MIT |  |
| xcb-util-wm | 0.4.2-r0 | xcb-util-wm | 6867e384dbfb | MIT |  |
| xdg-utils | 1.2.1-r1 | xdg-utils | 6867e384dbfb | MIT |  |
| xkeyboard-config | 2.47-r0 | xkeyboard-config | ec89b244a8f1 | MIT |  |
| xprop | 1.2.8-r0 | xprop | 6867e384dbfb | MIT |  |
| xset | 1.2.5-r1 | xset | e1b77cbba827 | MIT |  |
| xvidcore | 1.3.7-r2 | xvidcore | 2e85ba2de76e | GPL-2.0-or-later | yes |
| yaml | 0.2.5-r2 | yaml | 8bac74716d06 | MIT |  |
| zimg | 3.0.6-r0 | zimg | 44823d5e5cf4 | WTFPL |  |
| zix-libs | 0.8.0-r0 | zix | 50bcdef49328 | ISC |  |

## the bundle `uc-pxx-profile-kiosk-0.2.0-rc10.tar` (Kiosk only)

207 packages.

| package | version | origin | aports commit | license | copyleft |
|---|---|---|---|---|---|
| abseil-cpp-raw-logging-internal | 20250814.1-r0 | abseil-cpp | 68b166903c94 | Apache-2.0 |  |
| abseil-cpp-strings | 20250814.1-r0 | abseil-cpp | 68b166903c94 | Apache-2.0 |  |
| abseil-cpp-strings-internal | 20250814.1-r0 | abseil-cpp | 68b166903c94 | Apache-2.0 |  |
| aom-libs | 3.14.1-r0 | aom | ca6a710df98a | BSD-2-Clause AND custom |  |
| at-spi2-core | 2.60.5-r0 | at-spi2-core | 127aabdda292 | LGPL-2.1-or-later | yes |
| at-spi2-core-libs | 2.60.5-r0 | at-spi2-core | 127aabdda292 | LGPL-2.1-or-later | yes |
| basu | 0.2.1-r2 | basu | 6bd45b53fb31 | LGPL-2.1-or-later | yes |
| bubblewrap | 0.12.0-r0 | bubblewrap | 21ce0edacfd8 | LGPL-2.1-or-later | yes |
| cairo | 1.18.4-r1 | cairo | e54632f6ef65 | LGPL-2.1-or-later OR MPL-1.1 | yes |
| cairo-gobject | 1.18.4-r1 | cairo | e54632f6ef65 | LGPL-2.1-or-later OR MPL-1.1 | yes |
| chromium | 156.0.8078.79-r0 | chromium | bf3db421d5bb | BSD-3-Clause |  |
| chromium-angle | 156.0.8078.79-r0 | chromium | bf3db421d5bb | BSD-3-Clause |  |
| chromium-common | 156.0.8078.79-r0 | chromium | bf3db421d5bb | BSD-3-Clause |  |
| cjson | 1.7.19-r1 | cjson | 635e41d25a76 | MIT |  |
| crc32c | 1.1.2-r3 | crc32c | b72d47c7fa33 | BSD-3-Clause |  |
| cups-libs | 2.4.19-r1 | cups | 636370bb6f28 | Apache-2.0 |  |
| double-conversion | 3.4.0-r0 | double-conversion | f5a68812ed21 | BSD-3-Clause |  |
| encodings | 1.1.0-r0 | encodings | c690d2215724 | Public Domain |  |
| feedbackd | 0.8.9-r2 | feedbackd | 1e14fb21989b | GPL-3.0-or-later | yes |
| feedbackd-device-themes | 0.8.9-r0 | feedbackd-device-themes | 549e84793b0f | GPL-3.0-or-later | yes |
| feedbackd-udev | 0.8.9-r2 | feedbackd | 1e14fb21989b | GPL-3.0-or-later | yes |
| ffmpeg-libavcodec | 8.1.3-r0 | ffmpeg | 66cae15cc197 | GPL-2.0-or-later AND LGPL-2.1-or-later | yes |
| ffmpeg-libavformat | 8.1.3-r0 | ffmpeg | 66cae15cc197 | GPL-2.0-or-later AND LGPL-2.1-or-later | yes |
| ffmpeg-libavutil | 8.1.3-r0 | ffmpeg | 66cae15cc197 | GPL-2.0-or-later AND LGPL-2.1-or-later | yes |
| ffmpeg-libswresample | 8.1.3-r0 | ffmpeg | 66cae15cc197 | GPL-2.0-or-later AND LGPL-2.1-or-later | yes |
| file | 5.47-r2 | file | a05d36fa8641 | BSD-2-Clause |  |
| font-dejavu | 2.37-r6 | font-dejavu | 59420f440b2e | Bitstream-Vera |  |
| font-opensans | 0_git20210927-r1 | font-opensans | ec70c07cd709 | Apache-2.0 |  |
| fontconfig | 2.17.1-r1 | fontconfig | 07061cb5a288 | MIT |  |
| freetype | 2.14.3-r0 | freetype | ad16b0afc1a3 | FTL OR GPL-2.0-or-later | yes |
| fribidi | 1.0.16-r3 | fribidi | 5c9b8291bfb3 | LGPL-2.1-or-later | yes |
| gdk-pixbuf | 2.44.7-r1 | gdk-pixbuf | a21007f809f4 | LGPL-2.1-or-later | yes |
| glycin-image-rs | 2.1.5-r0 | glycin | 78ed74f69dc1 | MPL-2.0 OR LGPL-2.0-or-later | yes |
| glycin-svg | 2.1.5-r0 | glycin | 78ed74f69dc1 | MPL-2.0 OR LGPL-2.0-or-later | yes |
| gmobile | 0.7.2-r0 | gmobile | e5e49f8c66f4 | GPL-3.0-only | yes |
| gmobile-udev | 0.7.2-r0 | gmobile | e5e49f8c66f4 | GPL-3.0-only | yes |
| gmp | 6.3.0-r4 | gmp | 206e7b39f788 | LGPL-3.0-or-later OR GPL-2.0-or-later | yes |
| gnutls | 3.8.13-r0 | gnutls | c09f2e94a4b0 | LGPL-2.1-or-later | yes |
| gobject-introspection | 1.86.0-r1 | gobject-introspection | f48701aa97c7 | LGPL-2.0-or-later AND GPL-2.0-or-later AND MIT | yes |
| graphite2 | 1.3.14-r6 | graphite2 | 0016c47e5316 | LGPL-2.1-or-later OR MPL-1.1 | yes |
| grim | 1.5.0-r0 | grim | 63e0874f4b1c | MIT |  |
| gsound | 1.0.3-r3 | gsound | 2941f70a8645 | LGPL-2.1-or-later | yes |
| gtk+3.0 | 3.24.52-r0 | gtk+3.0 | 81ac3343fbf0 | LGPL-2.1-or-later | yes |
| harfbuzz | 13.2.1-r0 | harfbuzz | 0e23da21a00f | MIT |  |
| harfbuzz-subset | 13.2.1-r0 | harfbuzz | 0e23da21a00f | MIT |  |
| hwdata-pci | 0.408-r0 | hwdata | 6f27b0b36ae7 | GPL-2.0-or-later OR XFree86-1.1 | yes |
| icu-data-full | 78.1-r0 | icu | 6755fdb21483 | ICU |  |
| json-c | 0.18-r1 | json-c | 8e0faa63c91a | MIT |  |
| json-glib | 1.10.8-r0 | json-glib | 2941f70a8645 | LGPL-2.1-or-later | yes |
| kmscube | 0_git20220902-r1 | kmscube | 2caff6799201 | MIT |  |
| lame-libs | 3.100-r5 | lame | ffd76e0bae99 | LGPL-2.0-or-later | yes |
| lcms2 | 2.19-r0 | lcms2 | b103184b887b | MIT |  |
| libSvtAv1Enc | 4.1.0-r0 | svt-av1 | a75bcf7a688f | BSD-3-Clause-Clear |  |
| libasyncns | 0.8-r5 | libasyncns | a9e2a2dc46f3 | LGPL-2.0-or-later | yes |
| libatk-1.0 | 2.60.5-r0 | at-spi2-core | 127aabdda292 | LGPL-2.1-or-later | yes |
| libatk-bridge-2.0 | 2.60.5-r0 | at-spi2-core | 127aabdda292 | LGPL-2.1-or-later | yes |
| libbluray | 1.4.0-r0 | libbluray | 7d7242f055a0 | LGPL-2.1-or-later | yes |
| libbsd | 0.12.2-r0 | libbsd | 02365e78d654 | BSD-3-Clause |  |
| libcamera | 0.7.1-r1 | libcamera | c47555ebb1c7 | LGPL-2.1-or-later AND GPL-2.0-or-later | yes |
| libcamera-ipa | 0.7.1-r1 | libcamera | c47555ebb1c7 | LGPL-2.1-or-later AND GPL-2.0-or-later | yes |
| libcanberra | 0.30-r12 | libcanberra | bbbd9f6db611 | LGPL-2.1-or-later | yes |
| libcanberra-alsa | 0.30-r12 | libcanberra | bbbd9f6db611 | LGPL-2.1-or-later | yes |
| libdav1d | 1.5.3-r0 | dav1d | 6a13170ddff8 | BSD-2-Clause |  |
| libdisplay-info | 0.3.0-r0 | libdisplay-info | 5c472425b844 | MIT |  |
| libdrm | 2.4.134-r0 | libdrm | 2015800769b7 | MIT |  |
| libdrm-tests | 2.4.134-r0 | libdrm | 2015800769b7 | MIT |  |
| libdvdcss | 1.4.3-r0 | libdvdcss | 3add37f3a9a8 | GPL-2.0-or-later | yes |
| libdvdnav | 6.1.1-r1 | libdvdnav | be0f852b9283 | GPL-2.0-or-later | yes |
| libdvdread | 6.1.3-r2 | libdvdread | 4f8157b2e125 | GPL-2.0-or-later | yes |
| libelf | 0.195-r0 | elfutils | 8f1329420688 | GPL-3.0-or-later AND ( GPL-2.0-or-later OR LGPL-3.0-or-later ) | yes |
| libelogind | 255.24-r0 | elogind | f7e4c2c1ed47 | GPL-2.0-or-later LGPL-2.1-or-later | yes |
| libepoxy | 1.5.10-r1 | libepoxy | 6bd45b53fb31 | MIT |  |
| libevdev | 1.13.6-r0 | libevdev | 2af4dd008295 | MIT |  |
| libflac | 1.4.3-r2 | flac | 253ee9b07f76 | BSD-3-Clause AND GPL-2.0-or-later | yes |
| libfontenc | 1.1.9-r0 | libfontenc | 9667f1121e20 | MIT |  |
| libglycin | 2.1.5-r0 | libglycin | ce376ccfea45 | MPL-2.0 OR LGPL-2.0-or-later | yes |
| libgnome-desktop-3 | 44.5-r0 | gnome-desktop | f77596a3f338 | GPL-2.0-or-later AND LGPL-2.1-or-later | yes |
| libgomp | 15.2.0-r5 | gcc | 423a8ad043d0 | GPL-2.0-or-later AND LGPL-2.1-or-later | yes |
| libgudev | 238-r1 | libgudev | c97799aa548f | LGPL-2.1-or-later | yes |
| libhwy | 1.3.0-r0 | highway | 880de784cce2 | Apache-2.0 |  |
| libice | 1.1.2-r0 | libice | d4941e833167 | X11 |  |
| libinput | 1.31.3-r0 | libinput | 3241f28bdb55 | MIT |  |
| libinput-libs | 1.31.3-r0 | libinput | 3241f28bdb55 | MIT |  |
| libinput-udev | 1.31.3-r0 | libinput | 3241f28bdb55 | MIT |  |
| libjpeg-turbo | 3.1.3-r0 | libjpeg-turbo | 5fe6bebf850f | BSD-3-Clause AND IJG AND Zlib |  |
| libjxl | 0.11.2-r1 | libjxl | 9ec8d4495d91 | Apache-2.0 |  |
| libliftoff | 0.5.0-r0 | libliftoff | 6bd45b53fb31 | MIT |  |
| libltdl | 2.6.0-r1 | libtool | 21b05914806e | LGPL-2.0-or-later AND GPL-2.0-or-later | yes |
| libmagic | 5.47-r2 | file | a05d36fa8641 | BSD-2-Clause |  |
| libmd | 1.2.0-r0 | libmd | 87b78bc68906 | BSD-3-Clause AND BSD-2-Clause AND ISC AND Beerware AND Public Domain |  |
| libogg | 1.3.6-r0 | libogg | c17e8cd839de | BSD-3-Clause |  |
| libopenmpt | 0.8.10-r0 | libopenmpt | 58a88f3b7c7e | BSD-3-Clause |  |
| libpciaccess | 0.19-r0 | libpciaccess | d5de88aad809 | X11 |  |
| libpisp | 1.5.0-r0 | libpisp | 06dcfbef45b4 | BSD-2-Clause |  |
| libpng | 1.6.59-r0 | libpng | 8f222b50cd6e | Libpng |  |
| libpulse | 17.0-r7 | pulseaudio | e4821ce2dcff | LGPL-2.1-or-later | yes |
| librist | 0.2.15-r0 | librist | d9adfca901be | BSD-2-Clause |  |
| librsvg | 2.62.3-r0 | librsvg | c760246e8554 | LGPL-2.1-or-later | yes |
| libseat | 0.9.3-r0 | seatd | 493aed103009 | MIT |  |
| libsharpyuv | 1.6.0-r0 | libwebp | af8b48fabd19 | BSD-3-Clause |  |
| libsm | 1.2.6-r0 | libsm | 4eaa95753776 | MIT |  |
| libsndfile | 1.2.2-r2 | libsndfile | 3acdcc403bf3 | LGPL-2.1-or-later | yes |
| libsodium | 1.0.22-r0 | libsodium | 43738991b7e8 | ISC |  |
| libsrt | 1.5.3-r1 | libsrt | 2d9f0a6490c4 | MPL-2.0 | yes |
| libssh | 0.12.2-r0 | libssh | 16037a8794a4 | LGPL-2.1-or-later BSD-2-Clause | yes |
| libtasn1 | 4.21.0-r0 | libtasn1 | 771f180774aa | LGPL-2.1-or-later | yes |
| libtheora | 1.2.0-r1 | libtheora | 5e1b822309fb | BSD-3-Clause |  |
| libudfread | 1.2.0-r1 | libudfread | 5c9b8291bfb3 | LGPL-2.1-or-later | yes |
| libunwind | 1.8.3-r0 | libunwind | 1789f1e1e73c | MIT |  |
| libuv | 1.52.1-r0 | libuv | 8d19451845ed | MIT |  |
| libva | 2.23.0-r0 | libva | 217e378f7609 | MIT |  |
| libvdpau | 1.5-r4 | libvdpau | 6bd45b53fb31 | MIT |  |
| libvorbis | 1.3.7-r2 | libvorbis | fc95c5999828 | BSD-3-Clause |  |
| libvpx | 1.15.2-r1 | libvpx | c4a9322622d7 | BSD-3-Clause |  |
| libwebp | 1.6.0-r0 | libwebp | af8b48fabd19 | BSD-3-Clause |  |
| libwebpdemux | 1.6.0-r0 | libwebp | af8b48fabd19 | BSD-3-Clause |  |
| libwebpmux | 1.6.0-r0 | libwebp | af8b48fabd19 | BSD-3-Clause |  |
| libx11 | 1.8.14-r0 | libx11 | e129d974905f | X11 |  |
| libxau | 1.0.12-r0 | libxau | 6bd45b53fb31 | MIT |  |
| libxcb | 1.17.0-r2 | libxcb | 5e1b822309fb | MIT |  |
| libxcomposite | 0.4.7-r0 | libxcomposite | c0827d537025 | MIT |  |
| libxcursor | 1.2.3-r0 | libxcursor | 1d6d12ed6f5c | MIT |  |
| libxdamage | 1.1.7-r0 | libxdamage | 5ef54777a856 | MIT |  |
| libxdmcp | 1.1.5-r1 | libxdmcp | 614e9166706c | MIT |  |
| libxext | 1.3.7-r0 | libxext | 4ec252fa3e6e | MIT |  |
| libxfixes | 6.0.2-r0 | libxfixes | 9d33b3af389f | MIT |  |
| libxft | 2.3.9-r0 | libxft | 9a4062b0f278 | MIT |  |
| libxi | 1.8.4-r0 | libxi | d7f98a7160b7 | MIT AND X11 |  |
| libxinerama | 1.1.6-r0 | libxinerama | 4b83e9a57e62 | MIT |  |
| libxkbcommon | 1.13.1-r0 | libxkbcommon | 76aa0b70442b | MIT AND MIT-open-group AND HPND AND HPND-sell-variant AND LicenseRef-digital-equipment-corporation |  |
| libxml2 | 2.13.9-r2 | libxml2 | f1bd5b6767a0 | MIT |  |
| libxmu | 1.3.1-r0 | libxmu | 697576337178 | MIT |  |
| libxrandr | 1.5.5-r0 | libxrandr | 4a608248a9e0 | MIT |  |
| libxrender | 0.9.13-r0 | libxrender | 5ccdccc7a1db | MIT |  |
| libxshmfence | 1.3.3-r0 | libxshmfence | 8bac74716d06 | MIT |  |
| libxslt | 1.1.43-r3 | libxslt | 730f2f5bdc97 | X11 |  |
| libxt | 1.3.1-r0 | libxt | 60222ebeadec | MIT |  |
| libxtst | 1.2.6-r0 | libxtst | 9b53c2882fa6 | MIT |  |
| libxxf86vm | 1.1.7-r0 | libxxf86vm | ac89fbb35d37 | MIT |  |
| libyuv | 0.0.1928-r0 | libyuv | 6d055fa770a2 | BSD-3-Clause |  |
| libzmq | 4.3.5-r2 | zeromq | 03fe9a0a6194 | MPL-2.0 | yes |
| llvm22-libs | 22.1.3-r1 | llvm22 | 7d10bc6cb2ec | Apache-2.0 |  |
| mbedtls3 | 3.6.7-r0 | mbedtls3 | a4cda3e2ff52 | Apache-2.0 OR GPL-2.0-or-later | yes |
| mesa | 26.1.6-r0 | mesa | b94e73658216 | MIT AND SGI-B-2.0 AND BSL-1.0 |  |
| mesa-dri-gallium | 26.1.6-r0 | mesa | b94e73658216 | MIT AND SGI-B-2.0 AND BSL-1.0 |  |
| mesa-egl | 26.1.6-r0 | mesa | b94e73658216 | MIT AND SGI-B-2.0 AND BSL-1.0 |  |
| mesa-gbm | 26.1.6-r0 | mesa | b94e73658216 | MIT AND SGI-B-2.0 AND BSL-1.0 |  |
| mesa-gl | 26.1.6-r0 | mesa | b94e73658216 | MIT AND SGI-B-2.0 AND BSL-1.0 |  |
| mesa-gles | 26.1.6-r0 | mesa | b94e73658216 | MIT AND SGI-B-2.0 AND BSL-1.0 |  |
| mesa-utils | 9.0.0-r6 | mesa-demos | 6bd45b53fb31 | custom |  |
| minizip | 1.3.2-r1 | minizip | e22dc13fc625 | Zlib |  |
| mkfontscale | 1.2.4-r0 | mkfontscale | e4301c788920 | MIT |  |
| mpg123-libs | 1.33.5-r0 | mpg123 | 017919108b43 | LGPL-2.1-only | yes |
| mtdev | 1.1.7-r0 | mtdev | 6867e384dbfb | MIT |  |
| nettle | 3.10.2-r0 | nettle | bf898e6d067c | GPL-2.0-or-later OR LGPL-3.0-or-later | yes |
| nspr | 4.38.2-r0 | nspr | 4e10628e0ece | MPL-2.0 | yes |
| nss | 3.124-r0 | nss | 422faa8d3631 | MPL-2.0 | yes |
| numactl | 2.0.19-r0 | numactl | ac972e48bfad | LGPL-2.1-only | yes |
| openh264 | 2.6.0-r0 | openh264 | 3e3fa0ef3b55 | BSD-2-Clause |  |
| opus | 1.6.1-r0 | opus | 6664b23b1c7a | BSD-3-Clause |  |
| orc | 0.4.41-r0 | orc | 35fe4add84d7 | BSD-2-Clause |  |
| p11-kit | 0.26.2-r0 | p11-kit | 922ca1ffc9cc | BSD-3-Clause |  |
| pango | 1.57.1-r0 | pango | 18857621f16b | LGPL-2.1-or-later | yes |
| pipewire-libs | 1.6.8-r4 | pipewire | ad5814b3ee6f | MIT AND LGPL-2.1-or-later | yes |
| pixman | 0.46.4-r0 | pixman | 9337ac809a43 | MIT |  |
| pkgconf | 2.5.1-r0 | pkgconf | c6f80aa8e59c | ISC |  |
| rav1e-libs | 0.8.1-r0 | rav1e | 0f7061c11f34 | BSD-2-Clause custom |  |
| roc-toolkit-libs | 0.4.0-r1 | roc-toolkit | 9ea23937b2c6 | MPL-2.0 | yes |
| seatd | 0.9.3-r0 | seatd | 493aed103009 | MIT |  |
| seatd-openrc | 0.9.3-r0 | seatd | 493aed103009 | MIT |  |
| shared-mime-info | 2.4-r7 | shared-mime-info | 74df86bcbb3a | GPL-2.0-or-later | yes |
| sound-theme-freedesktop | 0.8-r2 | sound-theme-freedesktop | 619f65318054 | CC-BY-SA-3.0 AND GPL-2.0-or-later AND GPL-2.0-only AND CC-BY-3.0 | yes |
| soxr | 0.1.3-r7 | soxr | a7ec97e438d9 | LGPL-2.1-or-later | yes |
| speexdsp | 1.2.1-r2 | speexdsp | 33283848034c | BSD-3-Clause |  |
| spirv-tools | 1.4.341.0-r0 | spirv-tools | 5115d4d26db3 | Apache-2.0 |  |
| squeekboard | 1.43.1-r2 | squeekboard | 3faf0a5afd7a | GPL-3.0-or-later | yes |
| sway | 1.12_rc3-r0 | sway | 9c2aa7281ad6 | MIT |  |
| sway-openrc | 1.12_rc3-r0 | sway | 9c2aa7281ad6 | MIT |  |
| sway-wallpapers | 1.12_rc3-r0 | sway | 9c2aa7281ad6 | CC0-1.0 |  |
| swaybar | 1.12_rc3-r0 | sway | 9c2aa7281ad6 | MIT |  |
| swaybg | 1.2.2-r0 | swaybg | 680e3af93865 | MIT |  |
| swaybg-openrc | 1.2.2-r0 | swaybg | 680e3af93865 | MIT |  |
| swaynag | 1.12_rc3-r0 | sway | 9c2aa7281ad6 | MIT |  |
| tdb-libs | 1.4.15-r1 | tdb | 138cce73b696 | LGPL-3.0-or-later | yes |
| tsx-kiosk | 0.3.13_pre20261113-r0 | tsx-kiosk | — | GPL-2.0-or-later | yes |
| tsx-setup | 0.3.13_pre20261113-r0 | tsx-setup | — | GPL-2.0-or-later | yes |
| uc-pxx-kiosk | 0.1.0-r1 | uc-pxx-kiosk | — | GPL-2.0-or-later | yes |
| vulkan-loader | 1.4.347-r0 | vulkan-loader | 07bac49555d7 | Apache-2.0 |  |
| wayland-libs-client | 1.25.0-r0 | wayland | 5960a9697428 | MIT |  |
| wayland-libs-cursor | 1.25.0-r0 | wayland | 5960a9697428 | MIT |  |
| wayland-libs-egl | 1.25.0-r0 | wayland | 5960a9697428 | MIT |  |
| wayland-libs-server | 1.25.0-r0 | wayland | 5960a9697428 | MIT |  |
| wayland-utils | 1.3.0-r0 | wayland-utils | 18aa555d1028 | MIT |  |
| webrtc-audio-processing-2 | 2.1-r1 | webrtc-audio-processing-2 | f6f70577078e | custom |  |
| wlr-randr | 0.5.0-r1 | wlr-randr | 281864040170 | MIT |  |
| wlroots0.20 | 0.20.2-r0 | wlroots0.20 | 7ba24e6c28ed | MIT |  |
| wvkbd-mobintl | 0.19.4-r0 | wvkbd | 725a2646cc8b | GPL-3.0-or-later | yes |
| x264-libs | 0.164.3108-r1 | x264 | 21efe0fb007d | GPL-2.0-or-later | yes |
| x265-libs | 4.1-r0 | x265 | d3b7a03815c4 | GPL-2.0-or-later | yes |
| xcb-util-renderutil | 0.3.10-r0 | xcb-util-renderutil | 72959c97a7f1 | MIT |  |
| xcb-util-wm | 0.4.2-r0 | xcb-util-wm | 6867e384dbfb | MIT |  |
| xdg-utils | 1.2.1-r1 | xdg-utils | 6867e384dbfb | MIT |  |
| xkeyboard-config | 2.47-r0 | xkeyboard-config | ec89b244a8f1 | MIT |  |
| xprop | 1.2.8-r0 | xprop | 6867e384dbfb | MIT |  |
| xset | 1.2.5-r1 | xset | e1b77cbba827 | MIT |  |
| xvidcore | 1.3.7-r2 | xvidcore | 2e85ba2de76e | GPL-2.0-or-later | yes |
| yaml | 0.2.5-r2 | yaml | 8bac74716d06 | MIT |  |
