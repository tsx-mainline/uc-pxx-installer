package pcdfw

import (
	"bytes"
	"crypto/aes"
	"encoding/binary"
	"fmt"
	"io"
)

// Lib is one file to take key candidates from, normally the device's
// /system/lib64/libwebupdate.so and libprovdetect.so (read from a system
// partition mounted read-only).
type Lib struct {
	Name string
	Data []byte
}

// Found is the result of FindKeys.
type Found struct {
	Candidates int      // distinct 16-byte values tested
	KeysPassed int      // key candidates that passed the signature test
	Pairs      []Keys   // key/IV pairs that pass Check on the whole file
	Notes      []string // one line per pair that got to the full check
}

// candidates returns every 16-byte window of every printable-ASCII run of at
// least 16 bytes (the key may be the start of a longer string), or with raw
// every 16-byte window of the data. The value maps to "lib@offset".
func candidates(libs []Lib, raw bool) (vals [][16]byte, where map[[16]byte]string) {
	where = map[[16]byte]string{}
	add := func(l Lib, i int) {
		var v [16]byte
		copy(v[:], l.Data[i:i+16])
		if _, ok := where[v]; !ok {
			where[v] = fmt.Sprintf("%s@0x%x", l.Name, i)
			vals = append(vals, v)
		}
	}
	for _, l := range libs {
		d := l.Data
		if raw {
			for i := 0; i+16 <= len(d); i++ {
				add(l, i)
			}
			continue
		}
		for i := 0; i < len(d); {
			j := i
			for j < len(d) && d[j] >= 0x20 && d[j] < 0x7f {
				j++
			}
			if j-i >= 16 {
				for k := i; k+16 <= j; k++ {
					add(l, k)
				}
			}
			i = j + 1
		}
	}
	return vals, where
}

// FindKeys finds the head key and IV among the candidates from libs, using
// the container r as the test. The key alone decides the second block of the
// first seghdr chunk (the signature at +0x10), so keys are tested first
// without an IV. An IV must then give a sane first block, and the pair must
// pass Check on the whole file. Use the result only when len(Pairs) == 1.
func FindKeys(r io.ReaderAt, size int64, libs []Lib, raw bool) (Found, error) {
	var res Found
	if size < FirstSeg+32 {
		return res, fmt.Errorf("file too small for a pcdfw container")
	}
	hdr := make([]byte, FirstSeg+32)
	if _, err := r.ReadAt(hdr, 0); err != nil {
		return res, err
	}
	if !bytes.Equal(hdr[:4], sigLE) {
		return res, fmt.Errorf("not a pcdfw container (bad pack signature)")
	}
	c1, c2 := hdr[FirstSeg:FirstSeg+16], hdr[FirstSeg+16:FirstSeg+32]
	vals, where := candidates(libs, raw)
	res.Candidates = len(vals)

	var p [16]byte
	for _, k := range vals {
		block, _ := aes.NewCipher(k[:])
		block.Decrypt(p[:], c2)
		ok := true
		for i := 0; i < 4; i++ {
			if p[i]^c1[i] != sigLE[i] {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		res.KeysPassed++
		var p1raw [16]byte
		block.Decrypt(p1raw[:], c1)
		for _, iv := range vals {
			var p1 [16]byte
			for i := range p1 {
				p1[i] = p1raw[i] ^ iv[i]
			}
			sizeUsed := binary.LittleEndian.Uint32(p1[0:])
			sz := binary.LittleEndian.Uint32(p1[4:])
			segID := binary.LittleEndian.Uint16(p1[0x0a:])
			if sz != sizeUsed+HdrRegion || int64(FirstSeg)+int64(sz) > size {
				continue
			}
			if _, known := Partition[segID]; !known {
				continue
			}
			keys := Keys{Head: k, IV: iv}
			f, err := Open(r, size, keys)
			if err != nil {
				return res, err
			}
			segs, err := f.Check()
			if err != nil {
				res.Notes = append(res.Notes, fmt.Sprintf("key from %s, iv from %s: %v", where[k], where[iv], err))
				continue
			}
			res.Notes = append(res.Notes, fmt.Sprintf("key from %s, iv from %s: ok (%d segments)", where[k], where[iv], len(segs)))
			res.Pairs = append(res.Pairs, keys)
		}
	}
	return res, nil
}
