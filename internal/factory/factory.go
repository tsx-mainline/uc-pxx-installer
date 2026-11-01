// Package factory reads the factory data block of a UC-P10 or UC-P8: the
// first 4 KiB of the cfg partition (GPT index 20, LBA 3666944 on the unit
// layout). The stock U-Boot reads the same block at the same absolute LBA,
// takes the model from its record 650 ("hwmodelbase") and chooses the panel
// with it: "UC-P8" in the first 5 bytes gives the 800x1280 panel, else
// "UC-P10" in the first 6 bytes gives the 1200x1920 panel (UC-P8.md §2.2,
// from the U-Boot disassembly; the format was checked on unit 2, UC-P8.md
// §11).
//
// The package works on bytes only: the caller reads the block. It returns
// the model, the hardware revision and the checksum result, and never the
// other records (the block also holds the MAC address and other data of the
// unit).
//
// The format:
//
//	bytes 0..3    the length L of the block (u32, little-endian)
//	bytes 4..5    the checksum (big-endian): the sum of the big-endian
//	              16-bit words of bytes [0, L) without the word at 4,
//	              negated, mod 2^16 (U-Boot: 0 is never valid)
//	bytes 40..42  the hardware revision ("V%c.%c%c" in the stock kernel)
//	bytes 90..91  the number of records (u16, little-endian)
//	bytes 92..    the records: u16 tag, u16 length in 16-bit words, data
//
// This package is stricter than U-Boot: U-Boot parses the records even when
// the checksum is wrong, and maps every other model value to the 800x1280
// panel. Here a wrong checksum, a block that does not fit, a missing or a
// second model record, and a model that is neither UC-P8 nor UC-P10 are
// errors.
package factory

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

const (
	// Partition is the GPT name of the partition that holds the block.
	Partition = "cfg"
	// Size is the number of bytes that U-Boot reads (8 sectors).
	Size = 4096
	// TagModel is the record of the model ("hwmodelbase").
	TagModel = 650
	// MinLength: U-Boot reads the records only when L > 0x56.
	MinLength = 0x56 + 1
	recCount  = 90
	recStart  = 92
	revOffset = 40
	sumOffset = 4
	maxModel  = 64 // U-Boot copies at most 64 bytes of the model
	modelP8   = "UC-P8"
	modelP10  = "UC-P10"
)

// The model bases.
const (
	UCP10 = "UC-P10"
	UCP8  = "UC-P8"
)

// Block is what the installer uses of a factory block.
type Block struct {
	Length   int    // L
	Stored   uint16 // the checksum in the block
	Computed uint16 // the checksum of bytes [0, L)
	Records  int    // the number of records
	// Model is the value of record 650 without its trailing NUL bytes
	// (for example "UC-P10-T-HS"), "" when the record is missing.
	Model string
	// HWRev is the hardware revision ("V1.5A"), or "" when bytes 40..42
	// are not printable.
	HWRev string
}

// ChecksumOK reports a valid checksum.
func (b *Block) ChecksumOK() bool { return b.Stored != 0 && b.Stored == b.Computed }

// Checksum is the checksum of the first l bytes of blk, as U-Boot computes
// it (blk must hold l bytes, and one more when l is odd).
func Checksum(blk []byte, l int) uint16 {
	var sum uint16
	for i := 0; i < l; i += 2 {
		if i == sumOffset {
			continue
		}
		sum += uint16(blk[i])<<8 | uint16(blk[i+1])
	}
	return -sum
}

// Parse reads a factory block. It returns an error when the block cannot be
// read as a whole: a short buffer, L out of range, records that do not fit
// into L. A wrong checksum or a missing model is not an error of Parse
// (Identify refuses them): the status shows what was found.
func Parse(blk []byte) (*Block, error) {
	if len(blk) < Size {
		return nil, fmt.Errorf("factory data: %d bytes, want %d", len(blk), Size)
	}
	blk = blk[:Size]
	le := binary.LittleEndian
	l := le.Uint32(blk)
	if l < MinLength || l > Size {
		return nil, fmt.Errorf("factory data: length %d out of range (%d..%d)", l, MinLength, Size)
	}
	b := &Block{Length: int(l), Stored: binary.BigEndian.Uint16(blk[sumOffset:]), Computed: Checksum(blk, int(l))}
	if rev := blk[revOffset : revOffset+3]; printable(rev) {
		b.HWRev = fmt.Sprintf("V%c.%c%c", rev[0], rev[1], rev[2])
	}
	n := int(le.Uint16(blk[recCount:]))
	b.Records = n
	p := recStart
	found := 0
	for i := 0; i < n; i++ {
		if p+4 > b.Length {
			return nil, fmt.Errorf("factory data: record %d of %d starts at byte %d, past the length %d", i+1, n, p, b.Length)
		}
		tag, words := le.Uint16(blk[p:]), int(le.Uint16(blk[p+2:]))
		end := p + 4 + 2*words
		if end > b.Length {
			return nil, fmt.Errorf("factory data: record %d (tag %d, %d bytes) ends at byte %d, past the length %d", i+1, tag, 2*words, end, b.Length)
		}
		if tag == TagModel {
			found++
			v := blk[p+4 : end]
			if len(v) > maxModel {
				return nil, fmt.Errorf("factory data: the model record has %d bytes, more than %d", len(v), maxModel)
			}
			b.Model = strings.TrimRight(string(v), "\x00")
		}
		p = end
	}
	if found > 1 {
		return nil, fmt.Errorf("factory data: %d model records (tag %d), want one", found, TagModel)
	}
	return b, nil
}

func printable(b []byte) bool {
	for _, c := range b {
		if c < 0x21 || c > 0x7e {
			return false
		}
	}
	return len(b) > 0
}

// Model is the unit model that the factory data names.
type Model struct {
	Base   string // UCP10 or UCP8
	Value  string // the whole value of the record ("UC-P10-T-HS")
	Suffix string // what follows the base ("-T-HS"); shown, never decided on
	HWRev  string
}

// ErrNoModel is the error of Identify for every block that does not name a
// UC-P10 or a UC-P8.
var ErrNoModel = errors.New("the factory data names no UC-P10 or UC-P8")

// Identify decides the model of a parsed block, the way the stock U-Boot
// chooses the panel: "UC-P8" in the first 5 bytes is a UC-P8; else "UC-P10"
// in the first 6 bytes is a UC-P10. Every other value, a missing record, a
// value that is not printable ASCII and a wrong checksum are refused.
func (b *Block) Identify() (Model, error) {
	m := Model{Value: b.Model, HWRev: b.HWRev}
	if !b.ChecksumOK() {
		return m, fmt.Errorf("%w: its checksum is wrong (stored %04x, computed %04x)", ErrNoModel, b.Stored, b.Computed)
	}
	if b.Model == "" {
		return m, fmt.Errorf("%w: it has no model record (tag %d)", ErrNoModel, TagModel)
	}
	if !printable([]byte(strings.ReplaceAll(b.Model, " ", "_"))) {
		return m, fmt.Errorf("%w: the model %q is not printable text", ErrNoModel, b.Model)
	}
	switch {
	case strings.HasPrefix(b.Model, modelP8):
		m.Base = UCP8
	case strings.HasPrefix(b.Model, modelP10):
		m.Base = UCP10
	default:
		return m, fmt.Errorf("%w: it says %q", ErrNoModel, b.Model)
	}
	m.Suffix = b.Model[len(m.Base):]
	return m, nil
}

// Read parses blk and identifies the model (Parse, then Identify).
func Read(blk []byte) (*Block, Model, error) {
	b, err := Parse(blk)
	if err != nil {
		return nil, Model{}, err
	}
	m, err := b.Identify()
	return b, m, err
}
