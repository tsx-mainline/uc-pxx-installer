package clone

// The content manifest (DESIGN-R23 §4.6; format "uc-pxx-content-manifest-1",
// the same for the release build's contentmanifest.py and this code):
//
//	PATH TYPE MODE UID GID SIZE DATA\n
//
// one line per entry of the tree that the exclusion list keeps, the root "/"
// included, sorted by the bytes of PATH (unescaped), fields separated by one
// space, no times:
//
//   - PATH: absolute; every byte outside 0x21..0x7e, and "\", as \xHH
//     (two lowercase hex digits);
//   - TYPE: f (regular file), d (folder), l (symbolic link), c, b (device
//     nodes), p (FIFO), s (socket);
//   - MODE: the low 12 bits of the mode, 4 octal digits (0755, 4755, 0777);
//   - UID, GID: decimal;
//   - SIZE: the bytes of a regular file, the length of a link's target, 0
//     for every other type;
//   - DATA: f the sha256 of the content (64 lowercase hex), l the target
//     (escaped as PATH), c and b MAJOR:MINOR, otherwise "-".
//
// Hard links are listed once per name. The manifest's sha256 is ref_a /
// ref_b.
//
// The exclusion list: one rule per entry; an entry is left out when a rule
// matches it, unless a "!" rule names exactly its path:
//
//   - "/x/y": /x/y and everything under it;
//   - "/x/y/*": everything under /x/y (the folder itself stays);
//   - "/x/pre*": every entry of /x whose name starts with pre, with
//     everything under it (one "*", at the end of the last component);
//   - "**/name": every entry whose last component is name, with everything
//     under it;
//   - "!/x/y": /x/y itself is kept (what is under it follows the other
//     rules).

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"syscall"

	"uc-pxx-installer/internal/ext4"
)

// ManifestFormat names the format of this file.
const ManifestFormat = "uc-pxx-content-manifest-1"

// MaxEntries is the most entries a tree may have (the RC-9 trees have about
// 11,150).
const MaxEntries = 400000

// Entry is one line of the manifest.
type Entry struct {
	Path string
	Type byte // 'f', 'd', 'l', 'c', 'b', 'p', 's'
	Mode uint32
	UID  uint32
	GID  uint32
	Size int64
	Data string // the sha256, the link target (raw), "MAJOR:MINOR" or "-"
}

// Line is the manifest line of e (with its newline).
func (e Entry) Line() string {
	data := e.Data
	if e.Type == 'l' {
		data = Escape(data)
	}
	return fmt.Sprintf("%s %c %04o %d %d %d %s\n", Escape(e.Path), e.Type, e.Mode&0o7777, e.UID, e.GID, e.Size, data)
}

// Escape escapes a path or link target for a manifest line.
func Escape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x21 || c > 0x7e || c == '\\' {
			fmt.Fprintf(&b, "\\x%02x", c)
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// Excluder is a parsed exclusion list.
type Excluder struct {
	subtree  []string    // "/x": x and under
	children [][2]string // "/x/pre*": the entries of x whose name starts with pre, and under them
	names    []string    // "**/n"
	keep     map[string]bool
}

// NewExcluder parses an exclusion list.
func NewExcluder(rules []string) (*Excluder, error) {
	x := &Excluder{keep: map[string]bool{}}
	for _, r := range rules {
		switch {
		case strings.HasPrefix(r, "!"):
			if !cleanAbs(r[1:]) {
				return nil, fmt.Errorf("rule %q", r)
			}
			x.keep[r[1:]] = true
		case strings.HasPrefix(r, "**/"):
			n := r[3:]
			if n == "" || strings.ContainsAny(n, "/*\x00") || n == "." || n == ".." {
				return nil, fmt.Errorf("rule %q", r)
			}
			x.names = append(x.names, n)
		case strings.HasSuffix(r, "*"):
			i := strings.LastIndex(r, "/")
			if i < 0 { // clone review MINOR-5: "*" alone
				return nil, fmt.Errorf("rule %q", r)
			}
			d, pre := r[:i], strings.TrimSuffix(r[i+1:], "*")
			if d != "" && !cleanAbs(d) || strings.ContainsAny(pre, "*\x00") || pre == "." || pre == ".." {
				return nil, fmt.Errorf("rule %q", r)
			}
			x.children = append(x.children, [2]string{d, pre})
		default:
			if !cleanAbs(r) || strings.Contains(r, "*") {
				return nil, fmt.Errorf("rule %q", r)
			}
			x.subtree = append(x.subtree, r)
		}
	}
	return x, nil
}

func under(p, d string) bool { return strings.HasPrefix(p, d+"/") }

// matched: a rule (not "!") matches p.
func (x *Excluder) matched(p string) bool {
	for _, s := range x.subtree {
		if p == s || under(p, s) {
			return true
		}
	}
	for _, c := range x.children {
		if under(p, c[0]) {
			first, _, _ := strings.Cut(strings.TrimPrefix(p, c[0]+"/"), "/")
			if strings.HasPrefix(first, c[1]) {
				return true
			}
		}
	}
	for _, n := range x.names {
		for _, c := range strings.Split(p, "/") {
			if c == n {
				return true
			}
		}
	}
	return false
}

// Excluded: p is left out of the manifest.
func (x *Excluder) Excluded(p string) bool { return x.matched(p) && !x.keep[p] }

// keptBelow: a "!" rule names a path under p.
func (x *Excluder) keptBelow(p string) bool {
	for k := range x.keep {
		if under(k, p) || p == "/" {
			return true
		}
	}
	return false
}

// Stat is what a Source tells of one entry.
type Stat struct {
	Type         byte
	Mode         uint32
	UID, GID     uint32
	Size         int64
	Major, Minor uint32
	// Dev is the device of the file system that holds the entry (0: not
	// known, as in an ext4 image).
	Dev uint64
}

// Source is a tree to list: the live root of B (OSSource) or the ext4 file
// system of a partition (Ext4Source). No method follows a symbolic link.
type Source interface {
	Lstat(p string) (Stat, error)
	ReadDir(p string) ([]string, error)
	Readlink(p string) (string, error)
	// Hash is the size and sha256 of a regular file.
	Hash(p string) (int64, string, error)
}

// Options of Build.
type Options struct {
	// Full: list every entry, also under an excluded folder (for the
	// forbidden search of A); otherwise an excluded folder is not entered
	// unless a "!" rule names a path under it.
	Full bool
	// Forbidden: a regular file anywhere in the tree (excluded or not) with
	// one of these sizes and sha256s is an error.
	Forbidden []Pin
	// OneFS: every listed entry must be on the root's device (a live root:
	// another file system mounted on a kept path is an error).
	OneFS bool
}

// Result is a built manifest.
type Result struct {
	Manifest []byte
	SHA256   string
	Entries  int
}

// Build lists the tree of src from "/" and makes its manifest.
func Build(src Source, x *Excluder, o Options) (*Result, error) {
	var lines []struct{ p, l string }
	root, err := src.Lstat("/")
	if err != nil {
		return nil, fmt.Errorf("/: %w", err)
	}
	if root.Type != 'd' {
		return nil, errors.New("the root is not a folder")
	}
	sizes := map[int64]bool{}
	for _, f := range o.Forbidden {
		sizes[f.Size] = true
	}
	n := 0
	var walk func(p string, st Stat, depth int) error
	walk = func(p string, st Stat, depth int) error {
		if n++; n > MaxEntries {
			return fmt.Errorf("more than %d entries", MaxEntries)
		}
		if depth > 64 {
			return fmt.Errorf("%s: deeper than 64 folders", p)
		}
		excl := p != "/" && x.Excluded(p)
		e := Entry{Path: p, Type: st.Type, Mode: st.Mode, UID: st.UID, GID: st.GID, Data: "-"}
		switch st.Type {
		case 'f':
			if !excl || sizes[st.Size] {
				size, sum, err := src.Hash(p)
				if err != nil {
					return fmt.Errorf("%s: %w", p, err)
				}
				if size != st.Size {
					return fmt.Errorf("%s: %d bytes read, the stat says %d (it changed)", p, size, st.Size)
				}
				for _, f := range o.Forbidden {
					if f.Size == size && f.SHA256 == sum {
						return fmt.Errorf("%s is %s (%d bytes, sha256 %.16s…): it must not be in this tree", p, f.What, size, sum)
					}
				}
				e.Size, e.Data = size, sum
			}
		case 'l':
			if !excl {
				t, err := src.Readlink(p)
				if err != nil {
					return fmt.Errorf("%s: %w", p, err)
				}
				e.Size, e.Data = int64(len(t)), t
			}
		case 'c', 'b':
			e.Data = fmt.Sprintf("%d:%d", st.Major, st.Minor)
		case 'd', 'p', 's':
		default:
			return fmt.Errorf("%s: unknown type %q", p, st.Type)
		}
		if !excl {
			lines = append(lines, struct{ p, l string }{p, e.Line()})
		}
		if st.Type != 'd' {
			return nil
		}
		if excl && !o.Full && !x.keptBelow(p) {
			return nil
		}
		names, err := src.ReadDir(p)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		sort.Strings(names)
		for _, nm := range names {
			if nm == "" || nm == "." || nm == ".." || strings.ContainsAny(nm, "/\x00") {
				return fmt.Errorf("%s: the name %q", p, nm)
			}
			c := path.Join(p, nm)
			cs, err := src.Lstat(c)
			if err != nil {
				return fmt.Errorf("%s: %w", c, err)
			}
			if o.OneFS && cs.Dev != root.Dev {
				if x.Excluded(c) {
					continue // a mount on an excluded path (/proc, /sys, /dev, /run): not entered
				}
				return fmt.Errorf("%s is on another file system (device %#x, the root's %#x): a file system is mounted there", c, cs.Dev, root.Dev)
			}
			if err := walk(c, cs, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk("/", root, 0); err != nil {
		return nil, err
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i].p < lines[j].p })
	var b bytes.Buffer
	for _, l := range lines {
		b.WriteString(l.l)
	}
	s := sha256.Sum256(b.Bytes())
	return &Result{Manifest: b.Bytes(), SHA256: hex.EncodeToString(s[:]), Entries: len(lines)}, nil
}

// Diff is the first lines in which two manifests differ (for the log):
// "-" lines only in want, "+" only in got, at most max of them.
func Diff(want, got []byte, max int) []string {
	set := func(b []byte) map[string]bool {
		m := map[string]bool{}
		sc := bufio.NewScanner(bytes.NewReader(b))
		sc.Buffer(make([]byte, 1<<16), 1<<20)
		for sc.Scan() {
			m[sc.Text()] = true
		}
		return m
	}
	w, g := set(want), set(got)
	var out []string
	for l := range w {
		if !g[l] {
			out = append(out, "- "+l)
		}
	}
	for l := range g {
		if !w[l] {
			out = append(out, "+ "+l)
		}
	}
	sort.Strings(out)
	if len(out) > max {
		out = out[:max]
	}
	return out
}

// OSSource is the live tree under dir (B's running root: "/", or a test
// root). No link is followed: Build enters only folders that lstat found,
// and every open has O_NOFOLLOW.
type OSSource struct{ dir string }

// NewOSSource is the tree under dir (a folder).
func NewOSSource(dir string) (*OSSource, error) {
	fi, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", dir)
	}
	return &OSSource{dir: strings.TrimSuffix(dir, "/")}, nil
}

// Close implements io.Closer (nothing to close).
func (s *OSSource) Close() error { return nil }

func (s *OSSource) at(p string) string {
	if p == "/" {
		if s.dir == "" {
			return "/"
		}
		return s.dir
	}
	return s.dir + p
}

// Lstat implements Source.
func (s *OSSource) Lstat(p string) (Stat, error) {
	fi, err := os.Lstat(s.at(p))
	if err != nil {
		return Stat{}, err
	}
	return osStat(fi)
}

func osStat(fi os.FileInfo) (Stat, error) {
	sys, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return Stat{}, errors.New("no stat")
	}
	st := Stat{Mode: uint32(sys.Mode) & 0o7777, UID: sys.Uid, GID: sys.Gid, Dev: uint64(sys.Dev)}
	switch sys.Mode & syscall.S_IFMT {
	case syscall.S_IFREG:
		st.Type, st.Size = 'f', sys.Size
	case syscall.S_IFDIR:
		st.Type = 'd'
	case syscall.S_IFLNK:
		st.Type = 'l'
	case syscall.S_IFCHR, syscall.S_IFBLK:
		st.Type = 'c'
		if sys.Mode&syscall.S_IFMT == syscall.S_IFBLK {
			st.Type = 'b'
		}
		rd := uint64(sys.Rdev)
		st.Major = uint32((rd>>32)&0xfffff000 | (rd>>8)&0xfff)
		st.Minor = uint32((rd>>12)&0xffffff00 | rd&0xff)
	case syscall.S_IFIFO:
		st.Type = 'p'
	case syscall.S_IFSOCK:
		st.Type = 's'
	default:
		return Stat{}, fmt.Errorf("unknown file type %#o", sys.Mode&syscall.S_IFMT)
	}
	return st, nil
}

// ReadDir implements Source.
func (s *OSSource) ReadDir(p string) ([]string, error) {
	f, err := os.OpenFile(s.at(p), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Readdirnames(-1)
}

// Readlink implements Source.
func (s *OSSource) Readlink(p string) (string, error) { return os.Readlink(s.at(p)) }

// Hash implements Source: the file opened without following a link, checked
// to be the regular file of the stat, read to its end.
func (s *OSSource) Hash(p string) (int64, string, error) {
	f, err := os.OpenFile(s.at(p), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 0, "", err
	}
	if !fi.Mode().IsRegular() {
		return 0, "", errors.New("not a regular file")
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

// Ext4Source is the ext4 file system of a partition (A after the
// difference), read through package ext4: no mount, no journal replay, no
// link followed.
type Ext4Source struct{ fs *ext4.FS }

// NewExt4Source opens the file system on r (size bytes). The reads go
// through a small block cache (the folders are read again for every path).
func NewExt4Source(r io.ReaderAt, size int64) (*Ext4Source, error) {
	fs, err := ext4.Open(NewBlockCache(r, 4096, 4096), size)
	if err != nil {
		return nil, err
	}
	return &Ext4Source{fs: fs}, nil
}

// Lstat implements Source.
func (s *Ext4Source) Lstat(p string) (Stat, error) {
	st, err := ext4.LstatNoFollow(s.fs, p)
	if err != nil {
		return Stat{}, err
	}
	k, err := ext4.KindNoFollow(s.fs, p)
	if err != nil {
		return Stat{}, err
	}
	out := Stat{Type: k[0], Mode: uint32(st.Perm), UID: st.UID, GID: st.GID}
	switch out.Type {
	case 'f':
		out.Size = st.Size
	case 'c', 'b':
		if out.Major, out.Minor, err = ext4.DeviceNoFollow(s.fs, p); err != nil {
			return Stat{}, err
		}
	}
	return out, nil
}

// ReadDir implements Source.
func (s *Ext4Source) ReadDir(p string) ([]string, error) {
	names, found, err := s.fs.ReadDirNoFollow(p, MaxEntries)
	if err == nil && !found {
		err = ext4.ErrNotFound
	}
	return names, err
}

// Readlink implements Source.
func (s *Ext4Source) Readlink(p string) (string, error) { return ext4.ReadlinkNoFollow(s.fs, p) }

// Hash implements Source.
func (s *Ext4Source) Hash(p string) (int64, string, error) { return ext4.HashFileNoFollow(s.fs, p) }

// BlockCache is an io.ReaderAt that keeps the last n blocks of bs bytes
// (FIFO): the ext4 reader reads each folder again for every path under it.
type BlockCache struct {
	r     io.ReaderAt
	bs    int64
	n     int
	m     map[int64][]byte
	order []int64
}

// NewBlockCache makes the cache.
func NewBlockCache(r io.ReaderAt, bs int64, n int) *BlockCache {
	return &BlockCache{r: r, bs: bs, n: n, m: map[int64][]byte{}}
}

// ReadAt implements io.ReaderAt (whole cached blocks; a read of more than
// one block goes to the reader).
func (c *BlockCache) ReadAt(p []byte, off int64) (int, error) {
	if int64(len(p)) > c.bs || off/c.bs != (off+int64(len(p))-1)/c.bs {
		return c.r.ReadAt(p, off)
	}
	blk := off / c.bs
	b, ok := c.m[blk]
	if !ok {
		b = make([]byte, c.bs)
		k, err := c.r.ReadAt(b, blk*c.bs)
		if err != nil && !(errors.Is(err, io.EOF) && k > 0) {
			return 0, err
		}
		b = b[:k]
		if len(c.order) >= c.n {
			delete(c.m, c.order[0])
			c.order = c.order[1:]
		}
		c.m[blk], c.order = b, append(c.order, blk)
	}
	in := off - blk*c.bs
	if in >= int64(len(b)) {
		return 0, io.EOF
	}
	k := copy(p, b[in:])
	if k < len(p) {
		return k, io.EOF
	}
	return k, nil
}
