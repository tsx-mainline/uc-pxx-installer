package gpt

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"strconv"
	"strings"
	"unicode/utf16"
)

// LayoutEntry is one partition of a layout file: the GPT index, the name, the
// first LBA and the size in sectors.
type LayoutEntry struct {
	Index       int
	Name        string
	Start, Size int64
}

// Layout is a partition layout without any GUID or raw GPT byte. Test images
// are built from it.
type Layout struct {
	DiskSectors int64
	Entries     []LayoutEntry
}

// ReadLayout reads a layout file:
//
//	disk SECTORS
//	INDEX NAME START SIZE      (one line per partition, sectors)
//
// Lines that start with # are comments.
func ReadLayout(r io.Reader) (*Layout, error) {
	l := &Layout{}
	sc := bufio.NewScanner(r)
	n := 0
	for sc.Scan() {
		n++
		s := strings.TrimSpace(sc.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		f := strings.Fields(s)
		if f[0] == "disk" && len(f) == 2 {
			v, err := strconv.ParseInt(f[1], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("line %d: %v", n, err)
			}
			l.DiskSectors = v
			continue
		}
		if len(f) != 4 {
			return nil, fmt.Errorf("line %d: want INDEX NAME START SIZE", n)
		}
		idx, e1 := strconv.Atoi(f[0])
		start, e2 := strconv.ParseInt(f[2], 10, 64)
		size, e3 := strconv.ParseInt(f[3], 10, 64)
		if e1 != nil || e2 != nil || e3 != nil || idx < 1 || idx > NumEntries || size <= 0 {
			return nil, fmt.Errorf("line %d: bad numbers", n)
		}
		l.Entries = append(l.Entries, LayoutEntry{idx, f[1], start, size})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if l.DiskSectors == 0 {
		return nil, fmt.Errorf("no disk line")
	}
	return l, nil
}

func guid(seed string) [16]byte {
	s := sha256.Sum256([]byte(seed))
	var g [16]byte
	copy(g[:], s[:16])
	g[7] = g[7]&0x0f | 0x40 // version 4 style
	g[8] = g[8]&0x3f | 0x80
	return g
}

// Build makes the bytes of a protective MBR plus primary table (LBA 0..33)
// and of the backup table (the last 33 sectors) for a layout. The GUIDs are
// derived from the names, so the same layout always gives the same bytes. It
// writes nothing itself.
func Build(l *Layout) (primary, backup []byte, err error) {
	last := l.DiskSectors - 1
	first := int64(2 + EntrySectors)
	lastUsable := last - 1 - EntrySectors
	ents := make([]byte, EntrySectors*Sector)
	linuxData := guid("linux-data")
	for _, e := range l.Entries {
		if e.Start < first || e.Start+e.Size-1 > lastUsable {
			return nil, nil, fmt.Errorf("%s is outside the usable range", e.Name)
		}
		b := ents[(e.Index-1)*EntrySize:]
		copy(b[0:16], linuxData[:])
		g := guid("part-" + e.Name)
		copy(b[16:32], g[:])
		binary.LittleEndian.PutUint64(b[32:], uint64(e.Start))
		binary.LittleEndian.PutUint64(b[40:], uint64(e.Start+e.Size-1))
		u := utf16.Encode([]rune(e.Name))
		if len(u) > 36 {
			return nil, nil, fmt.Errorf("name %s too long", e.Name)
		}
		for i, c := range u {
			binary.LittleEndian.PutUint16(b[56+2*i:], c)
		}
	}
	disk := guid("disk")
	mkHeader := func(my, alt, entries int64) []byte {
		h := make([]byte, Sector)
		copy(h, signature)
		binary.LittleEndian.PutUint32(h[8:], 0x00010000)
		binary.LittleEndian.PutUint32(h[12:], HeaderSize)
		binary.LittleEndian.PutUint64(h[24:], uint64(my))
		binary.LittleEndian.PutUint64(h[32:], uint64(alt))
		binary.LittleEndian.PutUint64(h[40:], uint64(first))
		binary.LittleEndian.PutUint64(h[48:], uint64(lastUsable))
		copy(h[56:72], disk[:])
		binary.LittleEndian.PutUint64(h[72:], uint64(entries))
		binary.LittleEndian.PutUint32(h[80:], NumEntries)
		binary.LittleEndian.PutUint32(h[84:], EntrySize)
		binary.LittleEndian.PutUint32(h[88:], crc32.ChecksumIEEE(ents))
		binary.LittleEndian.PutUint32(h[16:], crc32.ChecksumIEEE(h[:HeaderSize]))
		return h
	}
	mbr := make([]byte, Sector)
	// one protective partition of type 0xEE over the whole disk
	p := mbr[446:]
	p[1], p[2], p[3] = 0, 2, 0
	p[4] = 0xee
	p[5], p[6], p[7] = 0xff, 0xff, 0xff
	binary.LittleEndian.PutUint32(p[8:], 1)
	sz := last
	if sz > 0xffffffff {
		sz = 0xffffffff
	}
	binary.LittleEndian.PutUint32(p[12:], uint32(sz))
	mbr[510], mbr[511] = 0x55, 0xaa
	primary = append(append(mbr, mkHeader(1, last, 2)...), ents...)
	backup = append(append([]byte(nil), ents...), mkHeader(last, 1, last-EntrySectors)...)
	return primary, backup, nil
}
