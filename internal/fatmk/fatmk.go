// Package fatmk makes a new, empty FAT32 volume and puts a few small files
// into it: the log bundle that the on-screen flow gives back to the computer
// on the UC-PXX-INST drive (INSTALLER-END-USER-PLAN.md §2.6), and the empty
// drive after a refusal of a dirty or damaged drive (FAT-READER.md §6.2).
//
// It writes only to the io.WriterAt it is given, which must read as zeros
// (a new file of the drive's size). It opens no file. The volume is the one
// that busybox mkfs.vfat makes for the drive (FAT32, 512-byte sectors,
// 32 reserved sectors, 2 FATs, the root at cluster 2), with every file in
// one run of clusters and a long name for every entry. Package fatfs reads
// it back before the drive goes to the computer.
package fatmk

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf16"
)

// File is one file of the volume. Path is "name" or "folder/name"
// ("/"-separated, no "." or ".." parts).
type File struct {
	Path string
	Data []byte
}

// Options are the label, the time stamp of every entry, and the volume id.
type Options struct {
	Label    string // at most 11 characters, A-Z 0-9 - _ and space
	Time     time.Time
	VolumeID uint32
}

const (
	bps      = 512
	reserved = 32
	numFATs  = 2
	minFAT32 = 65525      // the fewest clusters of a FAT32 volume
	maxFAT32 = 0x0FFFFFF5 // the most
	eoc      = 0x0FFFFFFF
	maxFiles = 4096
	maxBytes = 256 << 20 // the bundle is small: refuse more
)

var le = binary.LittleEndian

type node struct {
	name     string
	dir      bool
	data     []byte
	kids     []*node
	cluster  uint32
	clusters uint32
	parent   *node
}

// Make writes the volume into w (size bytes, all zero before).
func Make(w io.WriterAt, size int64, files []File, o Options) error {
	if err := checkLabel(o.Label); err != nil {
		return err
	}
	total := size / bps
	spc := int64(0)
	var fatSecs, clusters int64
	for _, s := range []int64{8, 4, 2, 1} {
		f, c := geometry(total, s)
		if c >= minFAT32 && c <= maxFAT32 {
			spc, fatSecs, clusters = s, f, c
			break
		}
	}
	if spc == 0 {
		return fmt.Errorf("%d bytes is too small or too large for FAT32", size)
	}
	root, err := tree(files)
	if err != nil {
		return err
	}
	cs := spc * bps
	// allocate: the root first (cluster 2), then the folders, then the files
	next := uint32(2)
	var dirs, regs []*node
	var walk func(n *node)
	walk = func(n *node) {
		if n.dir {
			dirs = append(dirs, n)
		} else {
			regs = append(regs, n)
		}
		for _, k := range n.kids {
			walk(k)
		}
	}
	walk(root)
	alloc := func(n *node, bytes int64) {
		c := uint32((bytes + cs - 1) / cs)
		if c == 0 {
			if n.dir {
				c = 1
			} else {
				return // an empty file has no cluster
			}
		}
		n.cluster, n.clusters = next, c
		next += c
	}
	for _, d := range dirs {
		alloc(d, int64(len(dirEntries(d, o)))*32)
	}
	for _, f := range regs {
		alloc(f, int64(len(f.data)))
	}
	used := int64(next - 2)
	if used > clusters {
		return fmt.Errorf("the files need %d clusters, the volume has %d", used, clusters)
	}
	fatOff := int64(reserved) * bps
	dataOff := fatOff + numFATs*fatSecs*bps
	clusterOff := func(c uint32) int64 { return dataOff + int64(c-2)*cs }

	// the FAT: entries 0 and 1 (clean, no error), then the chains
	fat := make([]byte, int64(next)*4)
	le.PutUint32(fat[0:], 0x0FFFFFF8)
	le.PutUint32(fat[4:], eoc)
	chain := func(n *node) {
		for i := uint32(0); i < n.clusters; i++ {
			v := uint32(eoc)
			if i+1 < n.clusters {
				v = n.cluster + i + 1
			}
			le.PutUint32(fat[4*(n.cluster+i):], v)
		}
	}
	for _, d := range dirs {
		chain(d)
	}
	for _, f := range regs {
		chain(f)
	}
	for i := int64(0); i < numFATs; i++ {
		if _, err := w.WriteAt(fat, fatOff+i*fatSecs*bps); err != nil {
			return err
		}
	}
	// the folders, every cluster written in full
	for _, d := range dirs {
		b := make([]byte, int64(d.clusters)*cs)
		for i, e := range dirEntries(d, o) {
			copy(b[i*32:], e)
		}
		if _, err := w.WriteAt(b, clusterOff(d.cluster)); err != nil {
			return err
		}
	}
	for _, f := range regs {
		if len(f.data) > 0 {
			if _, err := w.WriteAt(f.data, clusterOff(f.cluster)); err != nil {
				return err
			}
		}
	}
	// the boot sector, FSInfo, and their backups at 6 and 7
	bs := bootSector(total, spc, fatSecs, o)
	fsi := fsInfo(uint32(clusters-used), next)
	for _, s := range []struct {
		b   []byte
		lba int64
	}{{bs, 0}, {fsi, 1}, {sig(), 2}, {bs, 6}, {fsi, 7}, {sig(), 8}} {
		if _, err := w.WriteAt(s.b, s.lba*bps); err != nil {
			return err
		}
	}
	return nil
}

// geometry gives the FAT sectors and the clusters for spc sectors per cluster.
func geometry(total, spc int64) (fatSecs, clusters int64) {
	fatSecs = 1
	for i := 0; i < 64; i++ {
		clusters = (total - reserved - numFATs*fatSecs) / spc
		if clusters <= 0 {
			return fatSecs, 0
		}
		need := ((clusters+2)*4 + bps - 1) / bps
		if need <= fatSecs {
			return fatSecs, clusters
		}
		fatSecs = need
	}
	return fatSecs, 0
}

func checkLabel(l string) error {
	if len(l) == 0 || len(l) > 11 {
		return fmt.Errorf("label %q: 1 to 11 characters", l)
	}
	for _, c := range l {
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == ' ') {
			return fmt.Errorf("label %q: only A-Z, 0-9, -, _ and space", l)
		}
	}
	return nil
}

func validName(n string) bool {
	if n == "" || n == "." || n == ".." || len(n) > 200 || strings.HasSuffix(n, ".") || strings.HasSuffix(n, " ") {
		return false
	}
	for _, c := range n {
		if c < 0x20 || c == 0x7f || strings.ContainsRune(`/\:*?"<>|`, c) {
			return false
		}
	}
	return true
}

// tree builds the folders and files, sorted by name.
func tree(files []File) (*node, error) {
	if len(files) > maxFiles {
		return nil, fmt.Errorf("%d files: at most %d", len(files), maxFiles)
	}
	root := &node{dir: true}
	var total int64
	for _, f := range files {
		total += int64(len(f.Data))
		if total > maxBytes {
			return nil, errors.New("the files are too large for a log bundle")
		}
		parts := strings.Split(f.Path, "/")
		cur := root
		for i, p := range parts {
			if !validName(p) {
				return nil, fmt.Errorf("bad path %q", f.Path)
			}
			var hit *node
			for _, k := range cur.kids {
				if strings.EqualFold(k.name, p) {
					hit = k
				}
			}
			last := i == len(parts)-1
			switch {
			case hit != nil && (last || !hit.dir):
				return nil, fmt.Errorf("%q is there twice", f.Path)
			case hit == nil && last:
				cur.kids = append(cur.kids, &node{name: p, data: f.Data, parent: cur})
			case hit == nil:
				hit = &node{name: p, dir: true, parent: cur}
				cur.kids = append(cur.kids, hit)
				cur = hit
			default:
				cur = hit
			}
		}
	}
	var srt func(n *node)
	srt = func(n *node) {
		sort.Slice(n.kids, func(i, j int) bool { return n.kids[i].name < n.kids[j].name })
		for _, k := range n.kids {
			srt(k)
		}
	}
	srt(root)
	return root, nil
}

func fatTime(t time.Time) (date, tm uint16) {
	t = t.UTC()
	if t.Year() < 1980 {
		t = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	if t.Year() > 2107 {
		t = time.Date(2107, 12, 31, 23, 59, 58, 0, time.UTC)
	}
	date = uint16((t.Year()-1980)<<9 | int(t.Month())<<5 | t.Day())
	tm = uint16(t.Hour()<<11 | t.Minute()<<5 | t.Second()/2)
	return
}

// shortName makes the 8.3 name NAME~N.EXT (upper case), unique in the folder.
func shortName(name string, taken map[string]bool) [11]byte {
	clean := func(s string, n int) string {
		var b strings.Builder
		for _, c := range strings.ToUpper(s) {
			if c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' {
				b.WriteRune(c)
			}
			if b.Len() == n {
				break
			}
		}
		return b.String()
	}
	base, ext := name, ""
	if i := strings.LastIndex(name, "."); i > 0 {
		base, ext = name[:i], name[i+1:]
	}
	b, e := clean(base, 6), clean(ext, 3)
	if b == "" {
		b = "FILE"
	}
	for n := 1; ; n++ {
		tail := fmt.Sprintf("~%d", n)
		bb := b
		if len(bb)+len(tail) > 8 {
			bb = bb[:8-len(tail)]
		}
		var s [11]byte
		copy(s[:], fmt.Sprintf("%-8s%-3s", bb+tail, e))
		if !taken[string(s[:])] {
			taken[string(s[:])] = true
			return s
		}
	}
}

func checksum(s [11]byte) byte {
	var c byte
	for _, b := range s {
		c = (c>>1 | c<<7) + b
	}
	return c
}

func shortEntry(s [11]byte, attr byte, cluster uint32, size uint32, o Options) []byte {
	e := make([]byte, 32)
	copy(e, s[:])
	e[11] = attr
	d, t := fatTime(o.Time)
	le.PutUint16(e[14:], t)
	le.PutUint16(e[16:], d)
	le.PutUint16(e[18:], d)
	le.PutUint16(e[20:], uint16(cluster>>16))
	le.PutUint16(e[22:], t)
	le.PutUint16(e[24:], d)
	le.PutUint16(e[26:], uint16(cluster))
	le.PutUint32(e[28:], size)
	return e
}

// lfnEntries are the long-name entries of name, last part first.
func lfnEntries(name string, sum byte) [][]byte {
	u := utf16.Encode([]rune(name))
	if len(u)%13 != 0 {
		u = append(u, 0)
		for len(u)%13 != 0 {
			u = append(u, 0xFFFF)
		}
	}
	n := len(u) / 13
	var out [][]byte
	for i := n; i >= 1; i-- {
		e := make([]byte, 32)
		e[0] = byte(i)
		if i == n {
			e[0] |= 0x40
		}
		e[11], e[13] = 0x0F, sum
		part := u[(i-1)*13 : i*13]
		for j, off := range []int{1, 3, 5, 7, 9, 14, 16, 18, 20, 22, 24, 28, 30} {
			le.PutUint16(e[off:], part[j])
		}
		out = append(out, e)
	}
	return out
}

// dirEntries are the 32-byte entries of a folder: the label (root), "."
// and ".." (a subfolder), then each child with its long name.
func dirEntries(d *node, o Options) [][]byte {
	var out [][]byte
	taken := map[string]bool{}
	if d.parent == nil {
		var l [11]byte
		copy(l[:], fmt.Sprintf("%-11s", o.Label))
		out = append(out, shortEntry(l, 0x08, 0, 0, o))
	} else {
		var dot, dotdot [11]byte
		copy(dot[:], ".          ")
		copy(dotdot[:], "..         ")
		out = append(out, shortEntry(dot, 0x10, d.cluster, 0, o))
		pc := d.parent.cluster
		if d.parent.parent == nil {
			pc = 0 // ".." of a folder in the root points to cluster 0
		}
		out = append(out, shortEntry(dotdot, 0x10, pc, 0, o))
	}
	for _, k := range d.kids {
		s := shortName(k.name, taken)
		out = append(out, lfnEntries(k.name, checksum(s))...)
		attr, size := byte(0x20), uint32(len(k.data))
		if k.dir {
			attr, size = 0x10, 0
		}
		out = append(out, shortEntry(s, attr, k.cluster, size, o))
	}
	return out
}

func bootSector(total, spc, fatSecs int64, o Options) []byte {
	b := make([]byte, bps)
	copy(b, []byte{0xEB, 0x58, 0x90})
	copy(b[3:], "P10INST ")
	le.PutUint16(b[11:], bps)
	b[13] = byte(spc)
	le.PutUint16(b[14:], reserved)
	b[16] = numFATs
	b[21] = 0xF8
	le.PutUint16(b[24:], 32) // sectors per track
	le.PutUint16(b[26:], 64) // heads
	le.PutUint32(b[32:], uint32(total))
	le.PutUint32(b[36:], uint32(fatSecs))
	le.PutUint32(b[44:], 2) // root cluster
	le.PutUint16(b[48:], 1) // FSInfo
	le.PutUint16(b[50:], 6) // backup boot sector
	b[64] = 0x80
	b[66] = 0x29
	le.PutUint32(b[67:], o.VolumeID)
	copy(b[71:], fmt.Sprintf("%-11s", o.Label))
	copy(b[82:], "FAT32   ")
	b[510], b[511] = 0x55, 0xAA
	return b
}

func fsInfo(free, next uint32) []byte {
	b := make([]byte, bps)
	le.PutUint32(b[0:], 0x41615252)
	le.PutUint32(b[484:], 0x61417272)
	le.PutUint32(b[488:], free)
	le.PutUint32(b[492:], next)
	le.PutUint32(b[508:], 0xAA550000)
	return b
}

func sig() []byte {
	b := make([]byte, bps)
	b[510], b[511] = 0x55, 0xAA
	return b
}
