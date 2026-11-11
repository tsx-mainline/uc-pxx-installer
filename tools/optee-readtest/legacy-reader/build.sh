#!/bin/sh
# build.sh -- build the READ-ONLY legacy-ABI client as a static aarch64 ELF.
#
# Uses a cross gcc (aarch64-linux-gnu-gcc) with -static, so it needs no .so on
# the target. glibc-static is fine here (open/ioctl/printf only). The result
# runs in the RAM installer (root) or on stock (world-rw /dev/opteearmtz00),
# against the forward-ported drivers/tee/optee_legacy driver.
#
# READ ONLY: this source references only command 0; it cannot write lock_state.
set -eu

CC="${CC:-aarch64-linux-gnu-gcc}"
OUT=tee_legacy_read
SRC=tee_legacy_read.c

set -x
"$CC" \
	-std=gnu11 -Os \
	-static -no-pie -fno-pie \
	-Wall -Wextra \
	-o "$OUT" "$SRC"
set +x

echo
echo "built: $OUT"
file "$OUT" 2>/dev/null || true
readelf -h "$OUT" | grep -Ei 'class|machine|type'
# prove there is no write path: no reference to command 1 / WRITE anywhere.
echo "write-path audit (must be empty):"
grep -nE 'CMD_WRITE|cmd *= *1|unlock' "$SRC" || echo "  (none -- read-only confirmed)"
