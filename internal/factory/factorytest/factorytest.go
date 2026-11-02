// Package factorytest builds made-up factory blocks (package factory) for
// tests and test images. It holds no data of a unit.
//
// Unit2Like has the structure of unit 2's block (read on 2026-11-01,
// UC-P8.md §11: the length 2034, 34 records with the same tags, lengths and
// value lengths, the same header bytes in use), with every value made up:
// the MAC address is 02:00:5e:10:00:01 (a locally administered address),
// the text records are "FAKE…" strings of the same length, and the other
// bytes come from a sha256 chain. Only the model record and the hardware
// revision are what the test asks for.
package factorytest

import (
	"crypto/sha256"
	"encoding/binary"
	"strings"

	"uc-pxx-installer/internal/factory"
)

// rec is one record of unit 2's structure: tag, length in 16-bit words, and
// the kind of its value: 'b' one made-up non-zero byte, 'z' all zero, a
// number n > 0 an ASCII value of n bytes (NUL padded).
type rec struct {
	tag, words int
	kind       byte
	n          int
}

// unit2 is the record list of unit 2, in its order (values not kept).
var unit2 = []rec{
	{600, 1, 'b', 1}, {601, 1, 'b', 1}, {602, 1, 'z', 0}, {603, 1, 'z', 0}, {604, 1, 'z', 0},
	{610, 1, 'a', 1}, {3, 5, 'a', 8}, {611, 14, 'a', 26}, {605, 2, 'a', 3}, {606, 4, 'a', 6},
	{factory.TagModel, 6, 'm', 11},
	{612, 1, 'z', 0}, {613, 1, 'z', 0}, {614, 1, 'z', 0}, {615, 1, 'z', 0}, {616, 1, 'z', 0},
	{617, 1, 'z', 0}, {618, 1, 'z', 0}, {619, 1, 'z', 0}, {620, 1, 'z', 0},
	{700, 1, 'z', 0}, {701, 1, 'z', 0}, {702, 1, 'z', 0},
	{948, 5, 'a', 9},
	{1000, 1, 'b', 1}, {1001, 1, 'b', 1}, {1002, 1, 'z', 0}, {1003, 1, 'z', 0}, {1004, 1, 'z', 0},
	{946, 1, 'z', 0},
	{710, 4, 'a', 6}, {711, 9, 'a', 17}, {712, 9, 'a', 17}, {713, 2, 'a', 2},
}

// FakeMAC is the made-up MAC address in bytes 6..11.
var FakeMAC = []byte{0x02, 0x00, 0x5e, 0x10, 0x00, 0x01}

// Opt changes the block that Build makes.
type Opt struct {
	// Model is the value of record 650 (default "UC-P10-T-HS"). NoModel
	// leaves the record out; TwoModels adds a second one.
	Model              string
	NoModel, TwoModels bool
	// HWRev is the revision "Vx.yz" (default "V1.5A"); bytes 40..42 get
	// x, y and z.
	HWRev string
	// Length overrides L (the checksum is still over the given L, if it fits).
	Length int
	// Count overrides the record count.
	Count int
	// BadSum stores a wrong checksum; ZeroSum stores 0. TrueZeroSum changes
	// two made-up bytes after the records so that the checksum of the block
	// really is 0, and stores 0 (U-Boot: 0 is never valid).
	BadSum, ZeroSum, TrueZeroSum bool
}

func pattern(name string, n int) []byte {
	out := make([]byte, 0, n+32)
	s := sha256.Sum256([]byte("factorytest " + name))
	for len(out) < n {
		for _, c := range s {
			if c != 0 {
				out = append(out, c)
			}
		}
		s = sha256.Sum256(s[:])
	}
	return out[:n]
}

func fakeText(n int, i int) []byte {
	s := strings.Repeat("FAKE", n/4+1)
	b := []byte(s[:n])
	if n > 1 {
		b[n-1] = byte('0' + i%10)
	}
	return b
}

// Build makes a 4096-byte factory block with unit 2's structure.
func Build(o Opt) []byte {
	model := o.Model
	if model == "" {
		model = "UC-P10-T-HS"
	}
	rev := o.HWRev
	if rev == "" {
		rev = "V1.5A"
	}
	blk := make([]byte, factory.Size)
	le := binary.LittleEndian
	// the made-up header bytes, where unit 2 has non-zero bytes
	h := pattern("header", 32)
	for i, off := range []int{13, 15, 30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 53, 86} {
		blk[off] = h[i]
	}
	copy(blk[6:12], FakeMAC)
	if len(rev) == 5 {
		blk[40], blk[41], blk[42] = rev[1], rev[3], rev[4]
	}
	recs := append([]rec(nil), unit2...)
	if o.TwoModels {
		recs = append(recs, rec{factory.TagModel, 6, 'm', 0})
	}
	p, n := 92, 0
	for i, r := range recs {
		if r.kind == 'm' && o.NoModel {
			continue
		}
		words := r.words
		var v []byte
		switch r.kind {
		case 'b':
			v = pattern("rec", i+1)[i : i+1]
		case 'z':
		case 'a':
			v = fakeText(r.n, i)
		case 'm':
			v = []byte(model)
			if 2*words < len(v) {
				words = (len(v) + 1) / 2
			}
		}
		le.PutUint16(blk[p:], uint16(r.tag))
		le.PutUint16(blk[p+2:], uint16(words))
		copy(blk[p+4:], v)
		p += 4 + 2*words
		n++
	}
	l := 2034
	if p > l {
		l = p + 64
	}
	// unit 2 has 1627 non-zero bytes between the records and L
	copy(blk[p:l], pattern("tail", l-p))
	if o.Length != 0 {
		l = o.Length
	}
	le.PutUint32(blk[0:], uint32(l))
	if o.Count != 0 {
		n = o.Count
	}
	le.PutUint16(blk[90:], uint16(n))
	if o.TrueZeroSum {
		// a word after the records that makes the sum of the block 0 mod 2^16
		w := p + (p % 2)
		binary.BigEndian.PutUint16(blk[w:], 0)
		binary.BigEndian.PutUint16(blk[w:], factory.Checksum(blk, l))
	}
	sum := uint16(0x1234)
	if l >= factory.MinLength && l <= factory.Size {
		sum = factory.Checksum(blk, l)
	}
	switch {
	case o.ZeroSum:
		sum = 0
	case o.BadSum:
		sum ^= 0x0101
	}
	binary.BigEndian.PutUint16(blk[4:], sum)
	return blk
}

// Unit2Like is Build with the model and the defaults (V1.5A).
func Unit2Like(model string) []byte { return Build(Opt{Model: model}) }

// WithMAC is blk with another made-up MAC address in bytes 6..11 and the
// checksum made again (for tests that show a MAC of their own).
func WithMAC(blk []byte, mac []byte) []byte {
	b := append([]byte(nil), blk...)
	copy(b[6:12], mac)
	l := int(binary.LittleEndian.Uint32(b))
	if l >= factory.MinLength && l <= factory.Size {
		binary.BigEndian.PutUint16(b[4:], factory.Checksum(b, l))
	}
	return b
}
