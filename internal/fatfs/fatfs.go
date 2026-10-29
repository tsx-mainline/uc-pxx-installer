// Package fatfs reads files from a FAT12, FAT16 or FAT32 file system through
// an io.ReaderAt. It has no write path: it never writes, never mounts, and
// opens an image file read-only. The installer uses it for the P10-INSTALL
// drive (a FAT image file in RAM that the host sees as USB mass storage): it
// finds the one firmware .bin that the user copied into the top folder and
// gives the firmware code an io.ReaderAt over the clusters of that file, so
// the file is read where it is and is not copied.
//
// It reads what a host writes onto a drive of that size: the volume at byte
// 0 (as the RAM image formats it) or in the one FAT partition of an MBR, 512
// to 4096 bytes per sector, FAT12/16 with the fixed root directory, FAT32
// with the root directory in clusters, long file names (VFAT, UTF-16) and 8.3
// names (code page 437, with the Windows NT lower-case flags), and
// subdirectories.
//
// The input may be hostile. Every offset and length is checked against the
// volume, every cluster number against the cluster count, every chain is
// checked for loops, and the sizes of the FAT, a directory, the walk and a
// file are capped. No input makes it panic (FuzzOpen, FuzzMutate).
package fatfs

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

// Type is FAT12, FAT16 or FAT32.
type Type int

const (
	FAT12 Type = 12
	FAT16 Type = 16
	FAT32 Type = 32
)

func (t Type) String() string { return fmt.Sprintf("FAT%d", int(t)) }

// Limits. The P10-INSTALL drive is 1200 MiB (FAT32, 4 KiB clusters, a FAT of
// 1.2 MB). The caps allow a host to format it again with any cluster size.
const (
	MaxFATBytes    = 16 << 20   // one copy of the FAT, in memory (4 Mi FAT32 clusters)
	MaxDirBytes    = 65536 * 32 // one directory: the FAT limit of 65536 entries
	MaxWalkEntries = 1 << 20    // directory entries in the whole volume (Check)
	MaxDepth       = 32         // directory depth (Check, a path)
	MaxClusterSize = 256 << 10  // bytes per cluster
	maxProblems    = 10
)

var (
	// ErrNotFAT: no FAT boot sector at byte 0 and no MBR with one FAT
	// partition.
	ErrNotFAT = errors.New("no FAT file system")
	// ErrDamaged wraps every structure error that a host's clean write
	// never leaves: a bad chain, a loop, a cross-link, a size that does not
	// match the chain, a bad boot sector field.
	ErrDamaged = errors.New("the file system is damaged")
	// ErrNotFound: no such file or directory.
	ErrNotFound = errors.New("not found")
)

func damaged(f string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrDamaged, fmt.Sprintf(f, a...))
}

// FS is an opened FAT volume.
type FS struct {
	r           io.ReaderAt
	base        int64 // byte offset of the volume in r (0, or the MBR partition)
	volBytes    int64
	typ         Type
	bps         int64 // bytes per sector
	clusterSize int64
	clusters    uint32 // data clusters; valid numbers are 2..clusters+1
	numFATs     int
	activeFAT   int
	mirrored    bool
	fatOff      int64 // byte offset (in the volume) of FAT copy 0
	fatSize     int64 // bytes per FAT copy (from the boot sector)
	fatNeed     int64 // bytes of a FAT copy that hold entries 0..clusters+1
	rootOff     int64 // FAT12/16: the fixed root directory
	rootBytes   int64
	rootCluster uint32 // FAT32
	dataOff     int64
	fat         []byte // the active FAT, fatNeed bytes
	state       byte   // the state byte of the boot sector (bit 0: dirty)
	label       string
	media       byte
	closer      io.Closer
}

// Info describes a volume.
type Info struct {
	Type        Type
	Label       string // the boot sector label, spaces trimmed ("" if none)
	Offset      int64  // byte offset of the volume (0 = no partition table)
	Bytes       int64  // size of the volume
	ClusterSize int64
	Clusters    uint32
}

// Info returns the facts of the volume.
func (fs *FS) Info() Info {
	return Info{Type: fs.typ, Label: fs.label, Offset: fs.base, Bytes: fs.volBytes, ClusterSize: fs.clusterSize,
		Clusters: fs.clusters}
}

// Close closes the image file of OpenImage. For Open it does nothing.
func (fs *FS) Close() error {
	if fs.closer != nil {
		return fs.closer.Close()
	}
	return nil
}

// OpenImage opens an image file read-only (a regular file, not a symlink, not
// a device node) and the FAT volume in it.
func OpenImage(path string) (*FS, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !st.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("%s is not a regular file (%v)", path, st.Mode())
	}
	fs, err := Open(f, st.Size())
	if err != nil {
		f.Close()
		return nil, err
	}
	fs.closer = f
	return fs, nil
}

// Open reads the boot sector and the active FAT. size is the size of the
// image or device.
func Open(r io.ReaderAt, size int64) (*FS, error) {
	if size < 512 {
		return nil, fmt.Errorf("%w: only %d bytes", ErrNotFAT, size)
	}
	b0 := make([]byte, 512)
	if err := readFull(r, b0, 0); err != nil {
		return nil, err
	}
	fs, err := parseVolume(r, 0, size, b0)
	if err == nil {
		return fs, nil
	}
	off, n, perr := mbrPartition(b0, size)
	if perr != nil {
		// Sector 0 is not an MBR: the boot sector error says more.
		if errors.Is(perr, errNotMBR) && !errors.Is(err, errNoBoot) {
			return nil, err
		}
		return nil, perr
	}
	b := make([]byte, 512)
	if err := readFull(r, b, off); err != nil {
		return nil, err
	}
	fs, err = parseVolume(r, off, n, b)
	if err != nil {
		return nil, fmt.Errorf("the MBR partition at sector %d: %w", off/512, err)
	}
	return fs, nil
}

var (
	errNoBoot = fmt.Errorf("%w: no boot sector signature", ErrNotFAT)
	errNotMBR = fmt.Errorf("%w: no FAT boot sector, and sector 0 is not an MBR with a partition", ErrNotFAT)
)

// readFull reads len(p) bytes at off. A short read is an error.
func readFull(r io.ReaderAt, p []byte, off int64) error {
	n, err := r.ReadAt(p, off)
	if n == len(p) {
		return nil
	}
	if err == nil || err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return fmt.Errorf("read %d bytes at %d: %w", len(p), off, err)
}

var le = binary.LittleEndian

// parseVolume checks the boot sector b of a volume at base with size bytes,
// then reads the active FAT.
func parseVolume(r io.ReaderAt, base, size int64, b []byte) (*FS, error) {
	switch string(b[3:11]) {
	case "EXFAT   ":
		return nil, fmt.Errorf("%w: the drive is exFAT; it must be FAT32", ErrNotFAT)
	case "NTFS    ":
		return nil, fmt.Errorf("%w: the drive is NTFS; it must be FAT32", ErrNotFAT)
	}
	if b[510] != 0x55 || b[511] != 0xAA {
		return nil, errNoBoot
	}
	bad := func(f string, a ...any) error {
		return fmt.Errorf("%w: boot sector: %s", ErrNotFAT, fmt.Sprintf(f, a...))
	}
	bps := int64(le.Uint16(b[11:]))
	switch bps {
	case 512, 1024, 2048, 4096:
	default:
		return nil, bad("%d bytes per sector", bps)
	}
	spc := int64(b[13])
	if spc == 0 || spc&(spc-1) != 0 {
		return nil, bad("%d sectors per cluster", spc)
	}
	cs := bps * spc
	if cs > MaxClusterSize {
		return nil, bad("cluster size %d bytes (more than %d)", cs, MaxClusterSize)
	}
	rsvd := int64(le.Uint16(b[14:]))
	if rsvd == 0 {
		return nil, bad("no reserved sector")
	}
	nf := int(b[16])
	if nf < 1 || nf > 4 {
		return nil, bad("%d FATs", nf)
	}
	rootEnt := int64(le.Uint16(b[17:]))
	tot := int64(le.Uint16(b[19:]))
	if tot == 0 {
		tot = int64(le.Uint32(b[32:]))
	}
	if tot == 0 {
		return nil, bad("no sector count")
	}
	fs := &FS{r: r, base: base, bps: bps, clusterSize: cs, numFATs: nf, media: b[21], mirrored: true}
	fatSz := int64(le.Uint16(b[22:]))
	fat32 := fatSz == 0
	if fat32 {
		// The FAT32 layout (as Linux decides it: a 16-bit FAT size of 0).
		fatSz = int64(le.Uint32(b[36:]))
		if rootEnt != 0 {
			return nil, bad("FAT32 with %d fixed root entries", rootEnt)
		}
		if v := le.Uint16(b[42:]); v != 0 {
			return nil, bad("FAT32 version %#x", v)
		}
		if ext := le.Uint16(b[40:]); ext&0x80 != 0 {
			fs.mirrored = false
			fs.activeFAT = int(ext & 0x0F)
			if fs.activeFAT >= nf {
				return nil, bad("active FAT %d of %d", fs.activeFAT, nf)
			}
		}
		fs.rootCluster = le.Uint32(b[44:])
		fs.state = b[0x41]
		if b[0x42] == 0x29 {
			fs.label = trimLabel(b[0x47:0x52])
		}
	} else {
		if rootEnt == 0 {
			return nil, bad("FAT12/16 without root entries")
		}
		fs.state = b[0x25]
		if b[0x26] == 0x29 {
			fs.label = trimLabel(b[0x2B:0x36])
		}
	}
	if fatSz == 0 {
		return nil, bad("FAT size 0")
	}
	rootSecs := (rootEnt*32 + bps - 1) / bps
	// All in int64: tot < 2^32, fatSz < 2^32, nf <= 4, so no overflow.
	meta := rsvd + int64(nf)*fatSz + rootSecs
	if meta >= tot {
		return nil, bad("%d metadata sectors of %d", meta, tot)
	}
	fs.volBytes = tot * bps
	if fs.volBytes > size {
		return nil, fmt.Errorf("%w: the volume is %d bytes, the drive only %d (cut short)", ErrDamaged, fs.volBytes, size)
	}
	n := (tot - meta) / spc
	if n < 1 {
		return nil, bad("no data cluster")
	}
	switch {
	case fat32:
		fs.typ = FAT32
		if n > 0x0FFFFFF5 {
			return nil, bad("%d clusters", n)
		}
	case n <= 4084:
		fs.typ = FAT12
	case n <= 65524:
		fs.typ = FAT16
	default:
		return nil, bad("FAT16 with %d clusters", n)
	}
	fs.clusters = uint32(n)
	switch fs.typ {
	case FAT12:
		// entry c is in bytes c+c/2 and c+c/2+1; the last entry is n+1
		fs.fatNeed = (n + 1) + (n+1)/2 + 2
	case FAT16:
		fs.fatNeed = (int64(n) + 2) * 2
	case FAT32:
		fs.fatNeed = (int64(n) + 2) * 4
	}
	fs.fatSize = fatSz * bps
	if fs.fatNeed > fs.fatSize {
		return nil, bad("a FAT of %d bytes for %d clusters", fs.fatSize, n)
	}
	if fs.fatNeed > MaxFATBytes {
		return nil, bad("a FAT of %d bytes (more than %d)", fs.fatNeed, MaxFATBytes)
	}
	fs.fatOff = rsvd * bps
	fs.rootOff = (rsvd + int64(nf)*fatSz) * bps
	fs.rootBytes = rootEnt * 32
	fs.dataOff = meta * bps
	if fat32 && !fs.valid(fs.rootCluster) {
		return nil, bad("root cluster %d of %d", fs.rootCluster, n)
	}
	fs.fat = make([]byte, fs.fatNeed)
	if err := readFull(r, fs.fat, base+fs.fatOff+int64(fs.activeFAT)*fs.fatSize); err != nil {
		return nil, fmt.Errorf("the FAT: %w", err)
	}
	return fs, nil
}

func trimLabel(b []byte) string {
	s := decodeShort(b)
	if s == "NO NAME" {
		return ""
	}
	return s
}

// mbrPartition finds the one FAT partition of an MBR in sector 0.
func mbrPartition(b []byte, size int64) (int64, int64, error) {
	if b[510] != 0x55 || b[511] != 0xAA {
		return 0, 0, errNoBoot
	}
	var off, n int64
	found := 0
	for i := 0; i < 4; i++ {
		e := b[446+16*i : 446+16*i+16]
		typ := e[4]
		if typ == 0 {
			continue
		}
		if e[0] != 0 && e[0] != 0x80 {
			return 0, 0, errNotMBR
		}
		switch typ {
		case 0x01, 0x04, 0x06, 0x0B, 0x0C, 0x0E:
		case 0xEE:
			return 0, 0, fmt.Errorf("%w: the drive has a GPT; it must be one FAT32 volume", ErrNotFAT)
		case 0x07:
			return 0, 0, fmt.Errorf("%w: the drive has an NTFS or exFAT partition; it must be FAT32", ErrNotFAT)
		default:
			return 0, 0, fmt.Errorf("%w: the drive has a partition of type %#02x; it must be FAT32", ErrNotFAT, typ)
		}
		found++
		off = int64(le.Uint32(e[8:])) * 512
		n = int64(le.Uint32(e[12:])) * 512
	}
	switch {
	case found == 0:
		return 0, 0, errNotMBR
	case found > 1:
		return 0, 0, fmt.Errorf("%w: %d partitions; the drive must have one FAT32 volume", ErrNotFAT, found)
	case off == 0 || n < 512 || off > size || n > size-off:
		return 0, 0, fmt.Errorf("%w: the partition (%d bytes at %d) is not inside the drive (%d bytes)", ErrNotFAT, n, off, size)
	}
	return off, n, nil
}

// valid reports whether c is a data cluster number.
func (fs *FS) valid(c uint32) bool { return c >= 2 && c-2 < fs.clusters }

// entry returns FAT entry c of the active FAT. c must be < clusters+2.
func (fs *FS) entry(c uint32) uint32 {
	return fs.entryOf(fs.fat, c)
}

func (fs *FS) entryOf(fat []byte, c uint32) uint32 {
	switch fs.typ {
	case FAT12:
		o := int(c) + int(c)/2
		v := uint32(le.Uint16(fat[o:]))
		if c&1 == 1 {
			return v >> 4
		}
		return v & 0xFFF
	case FAT16:
		return uint32(le.Uint16(fat[2*int(c):]))
	default:
		return le.Uint32(fat[4*int(c):]) & 0x0FFFFFFF
	}
}

func (fs *FS) eoc(v uint32) bool {
	switch fs.typ {
	case FAT12:
		return v >= 0xFF8
	case FAT16:
		return v >= 0xFFF8
	default:
		return v >= 0x0FFFFFF8
	}
}

func (fs *FS) badMark(v uint32) bool {
	switch fs.typ {
	case FAT12:
		return v == 0xFF7
	case FAT16:
		return v == 0xFFF7
	default:
		return v == 0x0FFFFFF7
	}
}

// clusterOff is the byte offset of data cluster c in r.
func (fs *FS) clusterOff(c uint32) int64 {
	return fs.base + fs.dataOff + int64(c-2)*fs.clusterSize
}

// describe says what a FAT value that is not a next cluster means.
func (fs *FS) describe(v uint32) string {
	switch {
	case v == 0:
		return "a free cluster"
	case v == 1:
		return "the reserved value 1"
	case fs.badMark(v):
		return "a bad cluster"
	case fs.eoc(v):
		return "the end mark"
	}
	return fmt.Sprintf("cluster %d, outside the %d clusters", v, fs.clusters)
}

// chain follows the cluster chain that starts at first.
//
// want > 0: the chain must have exactly want clusters (a file of that size):
// a shorter or longer chain is damage. want == 0: up to the end mark, at most
// max clusters (a directory). mark is called with each cluster and returns
// false if the cluster was seen before (a loop or a cross-link). With
// collect, the clusters are returned.
func (fs *FS) chain(first uint32, want, max int, mark func(uint32) bool, collect bool) ([]uint32, error) {
	if !fs.valid(first) {
		return nil, damaged("the first cluster is %s", fs.describe(first))
	}
	var out []uint32
	if collect && want > 0 {
		out = make([]uint32, 0, want)
	}
	c := first
	for i := 0; ; i++ {
		if want == 0 && i >= max {
			return nil, damaged("a directory of more than %d bytes", int64(max)*fs.clusterSize)
		}
		if !mark(c) {
			return nil, damaged("cluster %d is used twice (a loop or a cross-link)", c)
		}
		if collect {
			out = append(out, c)
		}
		v := fs.entry(c)
		if fs.eoc(v) {
			if want > 0 && i+1 != want {
				return nil, damaged("the chain has %d clusters, the size needs %d", i+1, want)
			}
			return out, nil
		}
		if !fs.valid(v) {
			return nil, damaged("cluster %d is followed by %s", c, fs.describe(v))
		}
		if want > 0 && i+1 >= want {
			return nil, damaged("the chain is longer than the size (%d clusters)", want)
		}
		c = v
	}
}

// bitset marks clusters.
type bitset []uint64

func newBitset(n uint32) bitset { return make(bitset, (uint64(n)+2+63)/64) }

// mark sets bit c and returns false if it was set already.
func (b bitset) mark(c uint32) bool {
	w, m := c/64, uint64(1)<<(c%64)
	if b[w]&m != 0 {
		return false
	}
	b[w] |= m
	return true
}

// Dirty returns why the volume is not clean: the dirty bit of the boot
// sector (Linux and Windows set it while the volume is mounted), the
// clean-shutdown and the no-error bits of FAT entry 1 (FAT16 and FAT32:
// Windows and macOS clear them while it is mounted). An empty list means
// clean. FAT12 has only the boot sector bit.
func (fs *FS) Dirty() []string {
	var why []string
	if fs.state&1 != 0 {
		why = append(why, "the boot sector says the volume was not unmounted (dirty bit)")
	}
	e1 := fs.entry(1)
	switch fs.typ {
	case FAT16:
		if e1&0x8000 == 0 {
			why = append(why, "FAT entry 1 says the volume was not unmounted (clean-shutdown bit off)")
		}
		if e1&0x4000 == 0 {
			why = append(why, "FAT entry 1 records a disk error")
		}
	case FAT32:
		if e1&0x08000000 == 0 {
			why = append(why, "FAT entry 1 says the volume was not unmounted (clean-shutdown bit off)")
		}
		if e1&0x04000000 == 0 {
			why = append(why, "FAT entry 1 records a disk error")
		}
	}
	return why
}

// compareFATs compares every other FAT copy with the active one (without
// entries 0 and 1, which hold the media byte and the state bits). A host
// writes every copy; a copy that differs is an unfinished write.
func (fs *FS) compareFATs() error {
	if !fs.mirrored || fs.numFATs < 2 {
		return nil
	}
	skip := int64(4)
	switch fs.typ {
	case FAT12:
		skip = 3
	case FAT32:
		skip = 8
	}
	buf := make([]byte, 64<<10)
	for i := 0; i < fs.numFATs; i++ {
		if i == fs.activeFAT {
			continue
		}
		at := fs.base + fs.fatOff + int64(i)*fs.fatSize
		for o := skip; o < fs.fatNeed; o += int64(len(buf)) {
			n := int64(len(buf))
			if o+n > fs.fatNeed {
				n = fs.fatNeed - o
			}
			if err := readFull(fs.r, buf[:n], at+o); err != nil {
				return fmt.Errorf("FAT copy %d: %w", i+1, err)
			}
			for j := int64(0); j < n; j++ {
				if buf[j] != fs.fat[o+j] {
					return damaged("FAT copy %d differs from FAT copy %d at byte %d", i+1, fs.activeFAT+1, o+j)
				}
			}
		}
	}
	return nil
}
