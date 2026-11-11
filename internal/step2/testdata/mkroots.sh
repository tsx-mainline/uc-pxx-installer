#!/bin/sh
# Makes the root file system fixtures of TestRootHolds (MAINLINE-RC2-LS-REVIEW
# A-1): small ext4 images, each a running root whose own
# /var/lib/tsx/autoupdate/holds is in one state. Run where mke2fs is (e.g. an
# Alpine container with e2fsprogs):
#   docker run --rm -v "$PWD:/w" -w /w ALPINE sh internal/step2/testdata/mkroots.sh
# Made with mke2fs 1.47.4 of Alpine 3.24. Fixed times, UUID and hash seed, but mke2fs -d
# copies the ctime of each source file (the time of the run), so the bytes of a new run differ
# there (and in the inode checksums); the trees, which the tests read, are the same.
set -eu
OUT=$(dirname "$0")/roots
T=$(mktemp -d)
mk() { # NAME: the tree is in $T/NAME (3 MiB: the system_a of the test layout)
	find "$T/$1" -exec touch -h -d @1000000000 {} +
	E2FSPROGS_FAKE_TIME=1000000000 mke2fs -q -F -t ext4 -b 1024 -N 64 -L tsxroot \
		-U 6d2df957-690f-4c1e-9a3b-0123456789ab -E hash_seed=0b5e5b6e-7a5c-4d3f-8a2e-fedcba987654,root_owner=0:0 \
		-d "$T/$1" "$T/$1.img" 3072
	gzip -n -9 -c "$T/$1.img" > "$OUT/$1.img.gz"
}
base() { mkdir -p "$T/$1/etc" "$T/$1/var/lib" && echo uc-pxx > "$T/$1/etc/hostname"; }
base noholds; mkdir -p "$T/noholds/var/lib/tsx/autoupdate"; mk noholds
base empty; mkdir -p "$T/empty/var/lib/tsx/autoupdate/holds"; : > "$T/empty/var/lib/tsx/autoupdate/holds/.step2.tmp"; mk empty
base step2; mkdir -p "$T/step2/var/lib/tsx/autoupdate/holds"; echo "STOP after step 2: test" > "$T/step2/var/lib/tsx/autoupdate/holds/step2"; mk step2
base other; mkdir -p "$T/other/var/lib/tsx/autoupdate/holds"; echo "hold" > "$T/other/var/lib/tsx/autoupdate/holds/other"; mk other
base holdsfile; mkdir -p "$T/holdsfile/var/lib/tsx/autoupdate"; : > "$T/holdsfile/var/lib/tsx/autoupdate/holds"; mk holdsfile
base holdslink; mkdir -p "$T/holdslink/var/lib/tsx/autoupdate"; ln -s /tmp "$T/holdslink/var/lib/tsx/autoupdate/holds"; mk holdslink
base tsxlink; ln -s /data/var/lib/tsx "$T/tsxlink/var/lib/tsx"; mk tsxlink
mkdir -p "$T/novar/etc" && echo uc-pxx > "$T/novar/etc/hostname"; mk novar
rm -rf "$T"
(cd "$OUT" && sha256sum *.img.gz)
