// Package fattest makes small FAT12/16/32 images in memory, for the tests of
// package fatfs and of the users of the firmware drive. It builds a byte
// slice; it opens no file. The tests use it for the layouts and damage that
// mkfs.vfat and mtools do not make (a loop, a cross-link, a bad cluster).
package fattest

import (
	"encoding/binary"
	"fmt"
	"strings"
	"unicode/utf16"
)

var le = binary.LittleEndian

// Spec is the layout of an image.
type Spec struct {
	Type         int // 12, 16 or 32
	SectorSize   int // default 512
	SecPerClus   int // default 1
	Sectors      int // total sectors
	Reserved     int // default 1 (FAT12/16) or 32 (FAT32)
	NumFATs      int // default 2
	RootEntries  int // FAT12/16, default 512
	Label        string
	NoMirrorFAT1 bool // FAT32: ext_flags = 0x80|1 (only FAT 2 is active)
}

// Image is an image being built.
type Image struct {
	Spec
	B        []byte
	FATOff   int // byte offset of FAT copy 0
	FATBytes int // bytes per FAT copy
	RootOff  int // FAT12/16 fixed root
	DataOff  int
	Clusters uint32
	Root     *Dir
	free     uint32
}

// Dir is a directory being built.
type Dir struct {
	im       *Image
	off      []int // byte offsets of its slots in B, in order
	used     int
	Cluster  uint32 // 0 = the fixed root of FAT12/16
	Clusters []uint32
	short    map[string]bool
}

// New formats an image.
func New(s Spec) *Image {
	if s.SectorSize == 0 {
		s.SectorSize = 512
	}
	if s.SecPerClus == 0 {
		s.SecPerClus = 1
	}
	if s.NumFATs == 0 {
		s.NumFATs = 2
	}
	if s.Reserved == 0 {
		s.Reserved = 1
		if s.Type == 32 {
			s.Reserved = 32
		}
	}
	if s.RootEntries == 0 && s.Type != 32 {
		s.RootEntries = 512
	}
	if s.Type == 32 {
		s.RootEntries = 0
	}
	bps := s.SectorSize
	rootSecs := (s.RootEntries*32 + bps - 1) / bps
	// FAT size: iterate until the cluster count fits.
	fatSecs := 1
	var n int
	for {
		n = (s.Sectors - s.Reserved - s.NumFATs*fatSecs - rootSecs) / s.SecPerClus
		need := 0
		switch s.Type {
		case 12:
			need = (n+2)*3/2 + 2
		case 16:
			need = (n + 2) * 2
		case 32:
			need = (n + 2) * 4
		}
		if fatSecs*bps >= need {
			break
		}
		fatSecs++
	}
	im := &Image{Spec: s, B: make([]byte, s.Sectors*bps), Clusters: uint32(n), free: 2}
	im.FATOff = s.Reserved * bps
	im.FATBytes = fatSecs * bps
	im.RootOff = im.FATOff + s.NumFATs*im.FATBytes
	im.DataOff = im.RootOff + rootSecs*bps
	b := im.B
	b[0], b[1], b[2] = 0xEB, 0x58, 0x90
	copy(b[3:11], "FATTEST ")
	le.PutUint16(b[11:], uint16(bps))
	b[13] = byte(s.SecPerClus)
	le.PutUint16(b[14:], uint16(s.Reserved))
	b[16] = byte(s.NumFATs)
	le.PutUint16(b[17:], uint16(s.RootEntries))
	if s.Sectors < 65536 && s.Type != 32 {
		le.PutUint16(b[19:], uint16(s.Sectors))
	} else {
		le.PutUint32(b[32:], uint32(s.Sectors))
	}
	b[21] = 0xF8
	label := []byte(fmt.Sprintf("%-11s", strings.ToUpper(s.Label)))[:11]
	if s.Label == "" {
		copy(label, "NO NAME    ")
	}
	if s.Type == 32 {
		le.PutUint32(b[36:], uint32(fatSecs))
		if s.NoMirrorFAT1 {
			le.PutUint16(b[40:], 0x81)
		}
		le.PutUint32(b[44:], 2)
		le.PutUint16(b[48:], 1)
		b[0x42] = 0x29
		copy(b[0x47:0x52], label)
		copy(b[0x52:0x5A], "FAT32   ")
	} else {
		le.PutUint16(b[22:], uint16(fatSecs))
		b[0x26] = 0x29
		copy(b[0x2B:0x36], label)
		copy(b[0x36:0x3E], fmt.Sprintf("FAT%d   ", s.Type))
	}
	b[510], b[511] = 0x55, 0xAA
	switch s.Type {
	case 12:
		im.SetFAT(0, 0xFF8)
		im.SetFAT(1, 0xFFF)
	case 16:
		im.SetFAT(0, 0xFFF8)
		im.SetFAT(1, 0xFFFF)
	case 32:
		im.SetFAT(0, 0x0FFFFFF8)
		im.SetFAT(1, 0x0FFFFFFF)
	}
	im.Root = &Dir{im: im, short: map[string]bool{}}
	if s.Type == 32 {
		c := im.alloc(1)
		im.SetFAT(c[0], im.EOC())
		im.Root.Cluster, im.Root.Clusters = c[0], c
		im.Root.addSlots(c[0])
	} else {
		for i := 0; i < s.RootEntries; i++ {
			im.Root.off = append(im.Root.off, im.RootOff+32*i)
		}
	}
	return im
}

// EOC is the end mark of the type.
func (im *Image) EOC() uint32 {
	switch im.Type {
	case 12:
		return 0xFFF
	case 16:
		return 0xFFFF
	}
	return 0x0FFFFFFF
}

// Bad is the bad-cluster mark of the type.
func (im *Image) Bad() uint32 {
	switch im.Type {
	case 12:
		return 0xFF7
	case 16:
		return 0xFFF7
	}
	return 0x0FFFFFF7
}

// ClusterSize is the bytes per cluster.
func (im *Image) ClusterSize() int { return im.SectorSize * im.SecPerClus }

// SetFAT sets entry c in every FAT copy.
func (im *Image) SetFAT(c, v uint32) {
	for i := 0; i < im.NumFATs; i++ {
		im.SetFATCopy(i, c, v)
	}
}

// SetFATCopy sets entry c in FAT copy i only.
func (im *Image) SetFATCopy(i int, c, v uint32) {
	f := im.B[im.FATOff+i*im.FATBytes:]
	switch im.Type {
	case 12:
		o := int(c) + int(c)/2
		w := le.Uint16(f[o:])
		if c&1 == 1 {
			w = w&0x000F | uint16(v&0xFFF)<<4
		} else {
			w = w&0xF000 | uint16(v&0xFFF)
		}
		le.PutUint16(f[o:], w)
	case 16:
		le.PutUint16(f[2*int(c):], uint16(v))
	case 32:
		old := le.Uint32(f[4*int(c):])
		le.PutUint32(f[4*int(c):], old&0xF0000000|v&0x0FFFFFFF)
	}
}

// FAT returns entry c of FAT copy 0.
func (im *Image) FAT(c uint32) uint32 {
	f := im.B[im.FATOff:]
	switch im.Type {
	case 12:
		o := int(c) + int(c)/2
		w := uint32(le.Uint16(f[o:]))
		if c&1 == 1 {
			return w >> 4
		}
		return w & 0xFFF
	case 16:
		return uint32(le.Uint16(f[2*int(c):]))
	}
	return le.Uint32(f[4*int(c):]) & 0x0FFFFFFF
}

// ClusterOff is the byte offset of data cluster c.
func (im *Image) ClusterOff(c uint32) int { return im.DataOff + int(c-2)*im.ClusterSize() }

func (im *Image) alloc(n int) []uint32 {
	var out []uint32
	for c := im.free; len(out) < n; c++ {
		if c-2 >= im.Clusters {
			panic("fattest: image full")
		}
		if im.FAT(c) == 0 {
			out = append(out, c)
			im.SetFAT(c, im.EOC()) // reserve
		}
		im.free = c + 1
	}
	return out
}

// Link writes the chain of clusters into the FAT (the last gets the end mark).
func (im *Image) Link(cl []uint32) {
	for i, c := range cl {
		if i+1 < len(cl) {
			im.SetFAT(c, cl[i+1])
		} else {
			im.SetFAT(c, im.EOC())
		}
	}
}

// WriteData writes data into the clusters, in order.
func (im *Image) WriteData(cl []uint32, data []byte) {
	cs := im.ClusterSize()
	for i, c := range cl {
		if i*cs >= len(data) {
			break
		}
		end := (i + 1) * cs
		if end > len(data) {
			end = len(data)
		}
		copy(im.B[im.ClusterOff(c):], data[i*cs:end])
	}
}

func (d *Dir) addSlots(c uint32) {
	cs := d.im.ClusterSize()
	for i := 0; i < cs/32; i++ {
		d.off = append(d.off, d.im.ClusterOff(c)+32*i)
	}
}

// slot returns the next free 32-byte slot, growing a cluster directory.
func (d *Dir) slot() []byte {
	if d.used == len(d.off) {
		if d.Cluster == 0 {
			panic("fattest: root directory full")
		}
		c := d.im.alloc(1)
		d.Clusters = append(d.Clusters, c[0])
		d.im.Link(d.Clusters)
		d.addSlots(c[0])
	}
	o := d.off[d.used]
	d.used++
	return d.im.B[o : o+32]
}

// Raw appends a raw 32-byte entry.
func (d *Dir) Raw(e []byte) { copy(d.slot(), e) }

// Checksum is the LFN checksum of an 11-byte short name.
func Checksum(n []byte) byte {
	var s byte
	for i := 0; i < 11; i++ {
		s = (s&1)<<7 | s>>1
		s += n[i]
	}
	return s
}

// short83 returns the 11-byte short name for name, and whether name needs a
// long name.
func (d *Dir) short83(name string) ([]byte, bool) {
	base, ext := name, ""
	if i := strings.LastIndex(name, "."); i > 0 {
		base, ext = name[:i], name[i+1:]
	}
	valid := func(s string, n int) bool {
		if len(s) == 0 && n == 8 || len(s) > n {
			return false
		}
		for _, c := range s {
			if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_-$~!#%&{}()@'^`", c)) {
				return false
			}
		}
		return true
	}
	n := []byte("           ")
	if valid(base, 8) && valid(ext, 3) {
		copy(n, base)
		copy(n[8:], ext)
		return n, false
	}
	clean := func(s string, max int) string {
		var b strings.Builder
		for _, c := range strings.ToUpper(s) {
			if b.Len() >= max {
				break
			}
			if c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
				b.WriteRune(c)
			}
		}
		return b.String()
	}
	b6, e3 := clean(base, 6), clean(ext, 3)
	for i := 1; ; i++ {
		s := fmt.Sprintf("%s~%d", b6, i)
		copy(n, "        ")
		copy(n, s)
		copy(n[8:], "   ")
		copy(n[8:], e3)
		if !d.short[string(n)] {
			return n, true
		}
	}
}

// Entry writes a directory entry (with long-name entries when the name is
// not a valid upper-case 8.3 name) and returns the short entry.
func (d *Dir) Entry(name string, attr byte, cluster uint32, size uint32) []byte {
	sn, long := d.short83(name)
	d.short[string(sn)] = true
	if long {
		u := utf16.Encode([]rune(name))
		if len(u)%13 != 0 {
			u = append(u, 0)
		}
		for len(u)%13 != 0 {
			u = append(u, 0xFFFF)
		}
		k := len(u) / 13
		sum := Checksum(sn)
		for i := k; i >= 1; i-- {
			e := d.slot()
			e[0] = byte(i)
			if i == k {
				e[0] |= 0x40
			}
			e[11], e[13] = 0x0F, sum
			part := u[(i-1)*13 : i*13]
			for j, o := range []int{1, 3, 5, 7, 9, 14, 16, 18, 20, 22, 24, 28, 30} {
				le.PutUint16(e[o:], part[j])
			}
		}
	}
	e := d.slot()
	copy(e, sn)
	e[11] = attr
	le.PutUint16(e[20:], uint16(cluster>>16))
	le.PutUint16(e[26:], uint16(cluster))
	le.PutUint32(e[28:], size)
	return e
}

// File writes a file into contiguous free clusters.
func (d *Dir) File(name string, data []byte) []uint32 {
	cs := d.im.ClusterSize()
	n := (len(data) + cs - 1) / cs
	var cl []uint32
	if n > 0 {
		cl = d.im.alloc(n)
	}
	return d.FileAt(name, data, cl)
}

// FileAt writes a file into the given clusters (any order: fragmented).
func (d *Dir) FileAt(name string, data []byte, cl []uint32) []uint32 {
	if len(cl) > 0 {
		d.im.Link(cl)
		d.im.WriteData(cl, data)
	}
	first := uint32(0)
	if len(cl) > 0 {
		first = cl[0]
	}
	d.Entry(name, 0x20, first, uint32(len(data)))
	return cl
}

// Alloc reserves n free clusters (marked as end of chain until linked).
func (im *Image) Alloc(n int) []uint32 { return im.alloc(n) }

// Mkdir makes a subdirectory of one cluster with "." and "..".
func (d *Dir) Mkdir(name string) *Dir {
	c := d.im.alloc(1)
	d.Entry(name, 0x10, c[0], 0)
	sub := &Dir{im: d.im, Cluster: c[0], Clusters: c, short: map[string]bool{}}
	sub.addSlots(c[0])
	dot := sub.slot()
	copy(dot, ".          ")
	dot[11] = 0x10
	le.PutUint16(dot[26:], uint16(c[0]))
	le.PutUint16(dot[20:], uint16(c[0]>>16))
	dd := sub.slot()
	copy(dd, "..         ")
	dd[11] = 0x10
	parent := d.Cluster
	if d.im.Type == 32 && d == d.im.Root {
		parent = 0
	}
	le.PutUint16(dd[26:], uint16(parent))
	le.PutUint16(dd[20:], uint16(parent>>16))
	return sub
}

// SetDirty sets the dirty bit of the boot sector (as Linux does on mount).
func (im *Image) SetDirty() {
	if im.Type == 32 {
		im.B[0x41] |= 1
	} else {
		im.B[0x25] |= 1
	}
}

// ClearCleanBit clears the clean-shutdown bit of FAT entry 1 (as Windows and
// macOS do on mount).
func (im *Image) ClearCleanBit() {
	switch im.Type {
	case 16:
		im.SetFAT(1, im.FAT(1)&^0x8000)
	case 32:
		im.SetFAT(1, im.FAT(1)&^0x08000000)
	}
}

// ClearNoErrorBit clears the "no disk error" bit of FAT entry 1.
func (im *Image) ClearNoErrorBit() {
	switch im.Type {
	case 16:
		im.SetFAT(1, im.FAT(1)&^0x4000)
	case 32:
		im.SetFAT(1, im.FAT(1)&^0x04000000)
	}
}
