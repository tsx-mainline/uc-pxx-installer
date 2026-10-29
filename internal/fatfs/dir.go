package fatfs

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf16"
)

const (
	attrReadOnly = 0x01
	attrHidden   = 0x02
	attrSystem   = 0x04
	attrVolume   = 0x08
	attrDir      = 0x10
	attrLFN      = 0x0F
)

// Entry is one file or directory of a directory.
type Entry struct {
	Name      string // the long name, or the 8.3 name when there is no valid long name
	ShortName string // the 8.3 name, for example FIRMWA~1.BIN
	Dir       bool
	Size      int64 // bytes (0 for a directory)
	Attr      byte
	cluster   uint32
}

// cp437 is the upper half of code page 437, the default OEM code page of
// 8.3 names (Linux codepage=437).
var cp437 = []rune("ÇüéâäàåçêëèïîìÄÅÉæÆôöòûùÿÖÜ¢£¥₧ƒáíóúñÑªº¿⌐¬½¼¡«»░▒▓│┤╡╢╖╕╣║╗╝╜╛┐└┴┬├─┼╞╟╚╔╩╦╠═╬╧╨╤╥╙╘╒╓╫╪┘┌█▄▌▐▀αßΓπΣσµτΦΘΩδ∞φε∩≡±≥≤⌠⌡÷≈°∙·√ⁿ²■ ")

func decodeShort(b []byte) string {
	var sb strings.Builder
	for _, c := range []byte(strings.TrimRight(string(b), " ")) {
		if c < 0x80 {
			sb.WriteByte(c)
		} else {
			sb.WriteRune(cp437[c-0x80])
		}
	}
	return sb.String()
}

// shortName makes the 8.3 name of a directory entry, with the NT case flags
// (byte 12: 0x08 base in lower case, 0x10 extension in lower case).
func shortName(e []byte) string {
	n := make([]byte, 11)
	copy(n, e[:11])
	if n[0] == 0x05 {
		n[0] = 0xE5
	}
	base, ext := decodeShort(n[:8]), decodeShort(n[8:11])
	if e[12]&0x08 != 0 {
		base = strings.ToLower(base)
	}
	if e[12]&0x10 != 0 {
		ext = strings.ToLower(ext)
	}
	if ext == "" {
		return base
	}
	return base + "." + ext
}

func lfnChecksum(e []byte) byte {
	var s byte
	for i := 0; i < 11; i++ {
		s = (s&1)<<7 | s>>1
		s += e[i]
	}
	return s
}

// lfn collects the long-name entries before a short entry.
type lfn struct {
	parts [][13]uint16
	next  int // the ordinal expected next; 0 = complete or none
	sum   byte
	ok    bool
}

func (l *lfn) reset() { l.parts, l.next, l.ok = nil, 0, false }

func (l *lfn) add(e []byte) {
	ord := int(e[0])
	var u [13]uint16
	for i, o := range []int{1, 3, 5, 7, 9, 14, 16, 18, 20, 22, 24, 28, 30} {
		u[i] = le.Uint16(e[o:])
	}
	if ord&0x40 != 0 {
		n := ord & 0x1F
		if n == 0 || n > 20 || le.Uint16(e[26:]) != 0 {
			l.reset()
			return
		}
		l.parts, l.sum, l.ok = make([][13]uint16, n), e[13], true
		l.parts[n-1] = u
		l.next = n - 1
		return
	}
	if !l.ok || ord != l.next || ord == 0 || e[13] != l.sum || le.Uint16(e[26:]) != 0 {
		l.reset()
		return
	}
	l.parts[ord-1] = u
	l.next--
}

// name returns the long name for the short entry e, or "" when there is no
// complete long name with the checksum of e.
func (l *lfn) name(e []byte) string {
	defer l.reset()
	if !l.ok || l.next != 0 || lfnChecksum(e) != l.sum {
		return ""
	}
	var u []uint16
	for _, p := range l.parts {
		u = append(u, p[:]...)
	}
	for i, c := range u {
		if c == 0 {
			u = u[:i]
			break
		}
	}
	if len(u) == 0 || len(u) > 255 {
		return ""
	}
	s := string(utf16.Decode(u))
	if s == "." || s == ".." || strings.ContainsAny(s, "/\\\x00") {
		return ""
	}
	return s
}

// parseDir decodes the entries of a directory (its bytes, a multiple of 32).
// It stops at the end mark. Deleted entries, the volume label, "." and ".."
// are left out.
func (fs *FS) parseDir(b []byte) []Entry {
	var out []Entry
	var l lfn
	for o := 0; o+32 <= len(b); o += 32 {
		e := b[o : o+32]
		switch {
		case e[0] == 0x00:
			return out
		case e[0] == 0xE5:
			l.reset()
			continue
		case e[11]&0x3F == attrLFN:
			l.add(e)
			continue
		case e[11]&attrVolume != 0:
			l.reset()
			continue
		case string(e[:11]) == ".          " || string(e[:11]) == "..         ":
			l.reset()
			continue
		}
		en := Entry{ShortName: shortName(e), Attr: e[11], Dir: e[11]&attrDir != 0}
		en.Name = l.name(e)
		if en.Name == "" {
			en.Name = en.ShortName
		}
		en.cluster = uint32(le.Uint16(e[26:]))
		if fs.typ == FAT32 {
			en.cluster |= uint32(le.Uint16(e[20:])) << 16
		}
		if !en.Dir {
			en.Size = int64(le.Uint32(e[28:]))
		}
		out = append(out, en)
	}
	return out
}

// dirBytes reads a directory: the fixed root of FAT12/16 (cluster 0), or the
// clusters of a chain. mark is as for chain.
func (fs *FS) dirBytes(cluster uint32, mark func(uint32) bool) ([]byte, error) {
	if cluster == 0 && fs.typ != FAT32 {
		b := make([]byte, fs.rootBytes)
		if err := readFull(fs.r, b, fs.base+fs.rootOff); err != nil {
			return nil, fmt.Errorf("the root directory: %w", err)
		}
		return b, nil
	}
	if cluster == 0 {
		cluster = fs.rootCluster
	}
	max := int(MaxDirBytes / fs.clusterSize)
	if max < 1 {
		max = 1
	}
	cl, err := fs.chain(cluster, 0, max, mark, true)
	if err != nil {
		return nil, err
	}
	b := make([]byte, int64(len(cl))*fs.clusterSize)
	for i, c := range cl {
		if err := readFull(fs.r, b[int64(i)*fs.clusterSize:int64(i+1)*fs.clusterSize], fs.clusterOff(c)); err != nil {
			return nil, err
		}
	}
	return b, nil
}

func mapMark() func(uint32) bool {
	seen := map[uint32]bool{}
	return func(c uint32) bool {
		if seen[c] {
			return false
		}
		seen[c] = true
		return true
	}
}

// readDir reads the directory that starts at cluster (0 = the root).
func (fs *FS) readDir(cluster uint32) ([]Entry, error) {
	b, err := fs.dirBytes(cluster, mapMark())
	if err != nil {
		return nil, err
	}
	return fs.parseDir(b), nil
}

// lookup finds a path ("/", "/A/B.TXT"; "/" separated, the case does not
// matter, a long or an 8.3 name).
func (fs *FS) lookup(p string) (Entry, error) {
	cur := Entry{Name: "/", Dir: true}
	depth := 0
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." {
			continue
		}
		if !cur.Dir {
			return Entry{}, fmt.Errorf("%q: %q is not a directory: %w", p, cur.Name, ErrNotFound)
		}
		if depth++; depth > MaxDepth {
			return Entry{}, fmt.Errorf("%q: more than %d levels", p, MaxDepth)
		}
		es, err := fs.readDir(cur.cluster)
		if err != nil {
			return Entry{}, err
		}
		found := false
		for _, e := range es {
			if strings.EqualFold(e.Name, part) || strings.EqualFold(e.ShortName, part) {
				cur, found = e, true
				break
			}
		}
		if !found {
			return Entry{}, fmt.Errorf("%q: %w", p, ErrNotFound)
		}
		if cur.Dir && cur.cluster == 0 {
			// ".." of a first-level directory; a real entry never has it
			return Entry{}, damaged("directory %q has no cluster", cur.Name)
		}
	}
	return cur, nil
}

// ReadDir lists a directory.
func (fs *FS) ReadDir(p string) ([]Entry, error) {
	e, err := fs.lookup(p)
	if err != nil {
		return nil, err
	}
	if !e.Dir {
		return nil, fmt.Errorf("%q is not a directory", p)
	}
	return fs.readDir(e.cluster)
}

// File is an open file. It reads the clusters of the file in place.
type File struct {
	fs       *FS
	name     string
	size     int64
	clusters []uint32
	owner    bool // Close closes the image (OpenFirmware)
}

// Name is the name of the file (long or 8.3).
func (f *File) Name() string { return f.name }

// Size is the size of the file in bytes.
func (f *File) Size() int64 { return f.size }

// Fragments is the number of contiguous runs of clusters.
func (f *File) Fragments() int {
	n := 0
	for i, c := range f.clusters {
		if i == 0 || c != f.clusters[i-1]+1 {
			n++
		}
	}
	return n
}

// Reader returns a reader of the whole file.
func (f *File) Reader() *io.SectionReader { return io.NewSectionReader(f, 0, f.size) }

// Close closes the image if the file came from OpenFirmware.
func (f *File) Close() error {
	if f.owner {
		return f.fs.Close()
	}
	return nil
}

// ReadAt reads the file. It reads contiguous clusters with one read.
func (f *File) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("negative offset %d", off)
	}
	if off >= f.size {
		return 0, io.EOF
	}
	want := p
	if int64(len(want)) > f.size-off {
		want = want[:f.size-off]
	}
	cs := f.fs.clusterSize
	n := 0
	for n < len(want) {
		pos := off + int64(n)
		i := pos / cs
		within := pos % cs
		run := int64(1)
		for i+run < int64(len(f.clusters)) && f.clusters[i+run] == f.clusters[i+run-1]+1 && (run*cs-within) < int64(len(want)-n) {
			run++
		}
		k := run*cs - within
		if k > int64(len(want)-n) {
			k = int64(len(want) - n)
		}
		if err := readFull(f.fs.r, want[n:n+int(k)], f.fs.clusterOff(f.clusters[i])+within); err != nil {
			return n, err
		}
		n += int(k)
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// openEntry opens a file entry: its chain must have exactly the clusters
// that its size needs.
func (fs *FS) openEntry(e Entry) (*File, error) {
	if e.Dir {
		return nil, fmt.Errorf("%q is a directory", e.Name)
	}
	f := &File{fs: fs, name: e.Name, size: e.Size}
	want := (e.Size + fs.clusterSize - 1) / fs.clusterSize
	switch {
	case e.Size == 0 && e.cluster == 0:
		return f, nil
	case e.Size == 0:
		return nil, damaged("%q: size 0, but it has cluster %d", e.Name, e.cluster)
	case e.cluster == 0:
		return nil, damaged("%q: %d bytes, but no cluster", e.Name, e.Size)
	case want > int64(fs.clusters):
		return nil, damaged("%q: %d bytes, more than the volume", e.Name, e.Size)
	}
	cl, err := fs.chain(e.cluster, int(want), 0, newBitset(fs.clusters).mark, true)
	if err != nil {
		return nil, fmt.Errorf("%q: %w", e.Name, err)
	}
	f.clusters = cl
	return f, nil
}

// Open opens a file by its path.
func (fs *FS) Open(p string) (*File, error) {
	e, err := fs.lookup(p)
	if err != nil {
		return nil, err
	}
	return fs.openEntry(e)
}

// ReadFile reads a whole file of at most max bytes.
func (fs *FS) ReadFile(p string, max int64) ([]byte, error) {
	f, err := fs.Open(p)
	if err != nil {
		return nil, err
	}
	if f.size > max {
		return nil, fmt.Errorf("%q: %d bytes, more than %d", p, f.size, max)
	}
	b := make([]byte, f.size)
	if _, err := f.ReadAt(b, 0); err != nil && err != io.EOF {
		return nil, err
	}
	return b, nil
}

// Check walks the whole volume like a read-only fsck: every directory and
// every file chain. It returns the problems (at most 10): a chain with a
// free, bad or out-of-range cluster, a chain that does not match the file
// size, a cluster used twice (a loop or a cross-link), a directory that is
// too large, too deep or that cannot be read, and FAT copies that differ.
// The dirty bits are not in it (Dirty). Clusters that are in use but in no
// chain ("lost clusters") are not a problem: they lose space, not data.
func (fs *FS) Check() []string {
	w := &walker{fs: fs, seen: newBitset(fs.clusters)}
	if err := fs.compareFATs(); err != nil {
		w.problem("%v", err)
	}
	w.dir(0, "/", 0)
	return w.problems
}

type walker struct {
	fs       *FS
	seen     bitset
	entries  int
	problems []string
	stop     bool
}

func (w *walker) problem(f string, a ...any) {
	if len(w.problems) < maxProblems {
		w.problems = append(w.problems, fmt.Sprintf(f, a...))
	}
	if len(w.problems) >= maxProblems {
		w.stop = true
	}
}

func (w *walker) dir(cluster uint32, path string, depth int) {
	if w.stop {
		return
	}
	if depth > MaxDepth {
		w.problem("%s: more than %d directory levels", path, MaxDepth)
		return
	}
	b, err := w.fs.dirBytes(cluster, w.seen.mark)
	if err != nil {
		w.problem("directory %s: %v", path, err)
		return
	}
	for _, e := range w.fs.parseDir(b) {
		if w.stop {
			return
		}
		if w.entries++; w.entries > MaxWalkEntries {
			w.problem("more than %d files and directories", MaxWalkEntries)
			return
		}
		p := path + e.Name
		switch {
		case e.Dir && e.cluster == 0:
			w.problem("directory %q has no cluster", p)
		case e.Dir:
			w.dir(e.cluster, p+"/", depth+1)
		case e.Size == 0 && e.cluster == 0:
		case e.Size == 0:
			w.problem("%q: size 0, but it has cluster %d", p, e.cluster)
		case e.cluster == 0:
			w.problem("%q: %d bytes, but no cluster", p, e.Size)
		default:
			want := (e.Size + w.fs.clusterSize - 1) / w.fs.clusterSize
			if want > int64(w.fs.clusters) {
				w.problem("%q: %d bytes, more than the volume", p, e.Size)
				continue
			}
			if _, err := w.fs.chain(e.cluster, int(want), 0, w.seen.mark, false); err != nil {
				w.problem("%q: %v", p, err)
			}
		}
	}
}
