package vendorstore

import (
	"encoding/binary"
	"testing"
)

// copyOf makes one copy with the given version and items {id, data}.
func copyOf(version uint32, items map[uint16]string) []byte {
	size := CopySectors * 512
	b := make([]byte, size)
	le := binary.LittleEndian
	le.PutUint32(b[0:], tag)
	le.PutUint32(b[4:], version)
	le.PutUint32(b[size-4:], version)
	off := 0
	i := 0
	for id, data := range items {
		it := b[16+i*8:]
		le.PutUint16(it[0:], id)
		le.PutUint16(it[2:], uint16(off))
		le.PutUint16(it[4:], uint16(len(data)))
		copy(b[dataOffset+off:], data)
		off += (len(data) + 63) &^ 63
		i++
	}
	le.PutUint16(b[10:], uint16(i))
	return b
}

func TestSerial(t *testing.T) {
	size := CopySectors * 512
	area := make([]byte, Copies*size)
	copy(area[0:], copyOf(5, map[uint16]string{1: "OLDSERIAL", 2: "secret"}))
	copy(area[size:], copyOf(7, map[uint16]string{3: "macmac", 1: "NEWSERIAL\x00junk after the string" + string(make([]byte, 480))}))
	// copy 3: newer, but not written to the end (version2 differs)
	c := copyOf(9, map[uint16]string{1: "TORN"})
	binary.LittleEndian.PutUint32(c[size-4:], 8)
	copy(area[2*size:], c)
	s, err := Serial(area)
	if err != nil || s != "NEWSERIAL" {
		t.Fatalf("%q %v", s, err)
	}
}

func TestSerialErrors(t *testing.T) {
	size := CopySectors * 512
	if _, err := Serial(make([]byte, Copies*size)); err == nil {
		t.Error("empty area accepted")
	}
	if _, err := Serial(copyOf(1, map[uint16]string{2: "x"})); err == nil {
		t.Error("no serial item accepted")
	}
	if _, err := Serial(copyOf(1, map[uint16]string{1: "bad\x01"})); err == nil {
		t.Error("unprintable serial accepted")
	}
	if _, err := Serial(copyOf(1, map[uint16]string{1: string(make([]byte, 16))})); err == nil {
		t.Error("empty serial accepted")
	}
	if _, err := Serial(make([]byte, 100)); err == nil {
		t.Error("short area accepted")
	}
}
