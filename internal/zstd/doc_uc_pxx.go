// Package zstd is the zstd decompressor of the Go standard library
// (src/internal/zstd of Go 1.27.2, from the golang:1.27.2-alpine build
// image), copied so that the installer can import it. The step-1 receiver of
// the RAM installer (internal/xfer, kind step1) unpacks with it the
// compressed root file system of a mainline payload (manifest schema 3, the
// rootfs_zst file) while the bytes arrive; the unpacked bytes are checked
// against the manifest's rootfs size and sha256.
//
// Files: bits.go, block.go, fse.go, huff.go, literals.go, window.go,
// xxhash.go, zstd.go, fse_test.go, fuzz_test.go, window_test.go and
// testdata/ are byte-identical to the Go tree (BSD license:
// third_party/licenses/go-LICENSE.txt). zstd_test.go has two changes (its
// header says which). xxhash_test.go is left out (it reads the Go tree's
// Opticks text). This file and uc_pxx_test.go are new.
//
// What the decoder does not do (RFC 8878): dictionaries. Its window is at
// most 8 MiB (zstd.go maxWindowSize), so a payload is compressed with a
// window of at most 2^23 bytes (zstd -19 --zstd=wlog=23; never --long).
package zstd
