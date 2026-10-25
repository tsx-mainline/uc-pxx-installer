// Package vbmeta reads the header of an AVB vbmeta image (AvbVBMetaImageHeader,
// big-endian, 256 bytes) and the public key that it carries. It only reads.
//
// The installer uses it for two preflight checks: the payload vbmeta must be
// signed with the same key as the vbmeta that the unit already boots, and its
// rollback index must not be lower than the index of either current vbmeta.
package vbmeta

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
)

// HeaderSize is the size of the fixed header.
const HeaderSize = 256

// MaxRead is enough bytes for a vbmeta image of the UC-P10 firmwares (6528).
const MaxRead = 64 << 10

// Info is the decoded part of a vbmeta image.
type Info struct {
	Algorithm       uint32
	RollbackIndex   uint64
	Flags           uint32
	Release         string
	ImageBytes      int64 // header + authentication block + auxiliary block
	PublicKeySHA1   string
	PublicKeySHA256 string
}

// ErrNoMagic means the data does not start with "AVB0".
var ErrNoMagic = errors.New("no AVB0 magic")

// Parse decodes a vbmeta image from its first bytes (at least the whole
// image). It checks that every block lies inside the data.
func Parse(b []byte) (Info, error) {
	var in Info
	if len(b) < HeaderSize {
		return in, errors.New("shorter than the vbmeta header")
	}
	if string(b[:4]) != "AVB0" {
		return in, ErrNoMagic
	}
	be := binary.BigEndian
	if be.Uint32(b[4:]) != 1 {
		return in, fmt.Errorf("required libavb major version %d", be.Uint32(b[4:]))
	}
	auth := be.Uint64(b[12:])
	aux := be.Uint64(b[20:])
	in.Algorithm = be.Uint32(b[28:])
	pkOff := be.Uint64(b[64:])
	pkLen := be.Uint64(b[72:])
	in.RollbackIndex = be.Uint64(b[112:])
	in.Flags = be.Uint32(b[120:])
	rel := b[128:176]
	for i, c := range rel {
		if c == 0 {
			rel = rel[:i]
			break
		}
	}
	in.Release = string(rel)
	if auth > MaxRead || aux > MaxRead || auth%64 != 0 || aux%64 != 0 {
		return in, fmt.Errorf("block sizes %d/%d are not valid", auth, aux)
	}
	total := uint64(HeaderSize) + auth + aux
	if total > uint64(len(b)) {
		return in, fmt.Errorf("image of %d bytes, only %d read", total, len(b))
	}
	in.ImageBytes = int64(total)
	if pkLen == 0 {
		return in, errors.New("no public key in the vbmeta")
	}
	if pkOff > aux || pkLen > aux-pkOff {
		return in, errors.New("public key lies outside the auxiliary block")
	}
	start := uint64(HeaderSize) + auth + pkOff
	pk := b[start : start+pkLen]
	s1 := sha1.Sum(pk)
	s2 := sha256.Sum256(pk)
	in.PublicKeySHA1 = hex.EncodeToString(s1[:])
	in.PublicKeySHA256 = hex.EncodeToString(s2[:])
	return in, nil
}
