// Package vendorstore reads the serial number from the Rockchip vendor
// storage of the eMMC, the same value that U-Boot reports as the fastboot
// serialno. The storage holds four copies of 128 sectors from sector 7168,
// before the first partition. This package only reads, and it returns only
// item 1 (the serial number). The storage holds other items too, and they are
// never decoded or shown.
package vendorstore

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	// Start is the first sector of the vendor storage on the eMMC.
	Start = 7168
	// CopySectors is the size of one copy.
	CopySectors = 128
	// Copies is the number of copies.
	Copies     = 4
	tag        = 0x524B5644 // the vendor storage tag of Rockchip
	itemsMax   = 126
	dataOffset = 1024
	idSerial   = 1
)

// Serial finds item 1 in the newest valid copy. area is Copies*CopySectors
// sectors from Start.
func Serial(area []byte) (string, error) {
	size := CopySectors * 512
	if len(area) < size {
		return "", errors.New("vendor storage area too short")
	}
	le := binary.LittleEndian
	best, bestVer := -1, uint32(0)
	for c := 0; c < Copies && (c+1)*size <= len(area); c++ {
		b := area[c*size : (c+1)*size]
		if le.Uint32(b[0:]) != tag {
			continue
		}
		ver := le.Uint32(b[4:])
		if le.Uint32(b[size-4:]) != ver {
			continue // a copy that was not written to the end
		}
		if best < 0 || ver > bestVer {
			best, bestVer = c, ver
		}
	}
	if best < 0 {
		return "", errors.New("no valid vendor storage copy")
	}
	b := area[best*size : (best+1)*size]
	n := int(le.Uint16(b[10:]))
	if n > itemsMax {
		return "", fmt.Errorf("vendor storage: %d items", n)
	}
	for i := 0; i < n; i++ {
		it := b[16+i*8:]
		if le.Uint16(it[0:]) != idSerial {
			continue
		}
		off := dataOffset + int(le.Uint16(it[2:]))
		sz := int(le.Uint16(it[4:]))
		// The item is padded with zeros: unit 2 has a 512-byte item.
		if sz == 0 || sz > 512 || off+sz > size-8 {
			return "", errors.New("vendor storage: bad serial item")
		}
		// a C string: the serial ends at the first zero byte
		s := b[off : off+sz]
		if i := bytes.IndexByte(s, 0); i >= 0 {
			s = s[:i]
		}
		if len(s) == 0 || len(s) > 64 {
			return "", errors.New("vendor storage: bad serial length")
		}
		for _, ch := range s {
			if ch < 0x20 || ch > 0x7e {
				return "", errors.New("vendor storage: serial is not printable")
			}
		}
		return string(s), nil
	}
	return "", errors.New("vendor storage: no serial item")
}
