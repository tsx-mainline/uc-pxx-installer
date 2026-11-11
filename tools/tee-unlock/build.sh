#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# build.sh -- build tee_unlock.c into an Android arm64 dynamic ELF, reproducibly.
#
# Copyright (C) 2026 the tsx-mainline contributors. GPL-2.0-or-later.
#
# It links against a STUB libteec built here from the public BSD optee_client
# header (include/tee_client_api.h). It NEVER uses the device's libteec.so. At
# run time on the unit the device's own /vendor/lib64/libteec.so provides the
# TEEC_* symbols.
#
# Reproducible: set SOURCE_DATE_EPOCH, no build-id, fixed toolchain (NDK r26d).
# Two runs give byte-identical output. Intended to run in the pinned docker
# recipe of SOURCES.md; NDK is found through $NDK (the toolchain root), e.g.
#   NDK=/opt/android-ndk-r26d sh build.sh
set -eu

HERE="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
: "${NDK:?set NDK to the android-ndk-r26d root}"
: "${API:=30}"
: "${SOURCE_DATE_EPOCH:=1700000000}"
export SOURCE_DATE_EPOCH

TC="$NDK/toolchains/llvm/prebuilt/linux-x86_64"
CC="$TC/bin/aarch64-linux-android${API}-clang"
OUT="${OUT:-$HERE/out}"
mkdir -p "$OUT"

# Common, deterministic flags. -ffile-prefix-map keeps the build path out of the
# output; --build-id=none and -Wl,--no-rosegment keep the layout fixed.
CFLAGS="-O2 -fPIE -std=gnu11 -Wall -Wextra -Werror \
	-ffile-prefix-map=${HERE}=. -ffile-prefix-map=${TC}=. \
	-no-canonical-prefixes"
LDFLAGS="-Wl,--build-id=none -Wl,-z,noexecstack -Wl,--hash-style=gnu"

# 1) the link-only stub libteec.so (SONAME libteec.so), from the public header.
"$CC" $CFLAGS -I"$HERE/include" -fPIC -shared \
	-Wl,-soname,libteec.so $LDFLAGS \
	-nostdlib \
	-o "$OUT/libteec.so" "$HERE/stub/libteec_stub.c"

# 2) the client, linked against the stub (pie).
"$CC" $CFLAGS -I"$HERE/include" -pie \
	$LDFLAGS \
	-o "$OUT/tee_unlock.android-arm64" \
	"$HERE/tee_unlock.c" -L"$OUT" -lteec

"$TC/bin/llvm-strip" --strip-unneeded "$OUT/tee_unlock.android-arm64"

echo "built: $OUT/tee_unlock.android-arm64"
sha256sum "$OUT/tee_unlock.android-arm64"
"$TC/bin/llvm-readelf" -dh "$OUT/tee_unlock.android-arm64" | grep -Ei 'class|machine|type|NEEDED'
