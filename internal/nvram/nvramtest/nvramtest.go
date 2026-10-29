// Package nvramtest builds made-up nvram stores for tests, in the format that
// package nvram reads. Only tests and test images use it. It holds no data of
// a unit: every key and value comes from the test.
package nvramtest

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"

	"uc-pxx-installer/internal/nvram"
)

// Part is one part of the ring.
type Part struct {
	Index   int // 0..31
	Counter uint64
	// Records are "key=value" strings, in this order.
	Records []string
	// BadSum stores a wrong checksum. BadMagic leaves out the magic.
	// BadZlib breaks the zlib stream (with a correct checksum of the broken
	// bytes).
	BadSum, BadMagic, BadZlib bool
	// ZlibLen > 0 stores this zlib length instead of the real one; -1
	// stores 0. The checksum is computed for the stored length when it fits.
	ZlibLen int
	// TextLen > 0 stores this text length instead of the real one.
	TextLen int
	// FlagsZero stores flags 0 (the driver then checks no checksum at load
	// and inflates 0xffd8 bytes). NoEnd leaves out the empty record that ends
	// the list. Sum, when set, is stored as the checksum.
	FlagsZero, NoEnd bool
	Sum              *uint16
	// Zlib, when set, is stored instead of the compressed text.
	Zlib []byte
}

// Encode returns one part of nvram.PartSize bytes.
func Encode(p Part) []byte {
	var text []byte
	for _, r := range p.Records {
		text = append(append(text, r...), 0)
	}
	if !p.NoEnd {
		text = append(text, 0) // the empty record that ends the list
	}
	var z bytes.Buffer
	w, _ := zlib.NewWriterLevel(&z, zlib.BestCompression)
	w.Write(text)
	w.Close()
	zb := z.Bytes()
	if p.Zlib != nil {
		zb = append([]byte(nil), p.Zlib...)
	}
	if p.BadZlib {
		for i := len(zb) / 2; i < len(zb); i++ {
			zb[i] ^= 0x5a
		}
	}
	b := make([]byte, nvram.PartSize)
	copy(b, nvram.Magic)
	if p.BadMagic {
		copy(b, "XXXX")
	}
	le := binary.LittleEndian
	tlen := len(text)
	if p.TextLen > 0 {
		tlen = p.TextLen
	}
	le.PutUint32(b[4:], uint32(tlen))
	le.PutUint32(b[8:], 8)
	zlen := len(zb)
	switch {
	case p.ZlibLen > 0:
		zlen = p.ZlibLen
	case p.ZlibLen < 0:
		zlen = 0
	}
	le.PutUint32(b[12:], uint32(zlen))
	if !p.FlagsZero {
		le.PutUint16(b[0x10:], 1)
	}
	copy(b[nvram.HeaderSize:], zb)
	// the driver's bound: m = (zlen + 1) &^ 1 as a signed 32-bit number
	var sum uint16
	if m := int32(uint32(zlen)+1) &^ 1; m > 0 {
		sum = nvram.Checksum(b[nvram.HeaderSize:], int(min(m, nvram.DataSize)))
	}
	if p.Sum != nil {
		sum = *p.Sum
	}
	if p.BadSum {
		sum ^= 0x0101
	}
	binary.BigEndian.PutUint16(b[0x12:], sum)
	le.PutUint64(b[0x18:], p.Counter)
	return b
}

// Ring returns the ring (nvram.RingSize bytes) with the parts; the other
// parts are zero.
func Ring(parts ...Part) []byte {
	r := make([]byte, nvram.RingSize)
	for _, p := range parts {
		copy(r[p.Index*nvram.PartSize:], Encode(p))
	}
	return r
}

// Partition returns the start of others up to the end of the ring
// (nvram.RingOffset + nvram.RingSize bytes): zeros, then the ring.
func Partition(parts ...Part) []byte {
	return append(make([]byte, nvram.RingOffset), Ring(parts...)...)
}

// Records are made-up settings with the auto-update keys. fw is the value of
// 72005.
func Records(fw string) []string {
	return []string{"1=test", "100=made-up", nvram.KeyFirmware + "=" + fw, nvram.KeyAPK + "=true",
		"72013=0", "72014=02:00", "72015=0", nvram.KeyURL + "=https://example.invalid/fw.txt"}
}

// Off is a store whose active part has auto update firmware off.
func Off() []byte {
	return Partition(Part{Index: 3, Counter: 7, Records: Records("false")})
}

// On is a store whose active part has auto update firmware on.
func On() []byte {
	return Partition(Part{Index: 3, Counter: 7, Records: Records("true")})
}
