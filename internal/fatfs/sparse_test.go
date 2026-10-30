package fatfs

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// sparse is a fixture image (testdata/*.fat.gz, see testdata/sparse.py): the
// non-zero runs of an image; everything else reads as zero.
type sparse struct {
	size int64
	runs []run // sorted by offset, not overlapping
}

type run struct {
	off int64
	b   []byte
}

func loadSparse(t testing.TB, name string) *sparse {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name+".fat.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	g, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(g)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(b, []byte("FATSPARSE1\n")) {
		t.Fatalf("%s: not a sparse fixture", name)
	}
	b = b[11:]
	s := &sparse{size: int64(binary.LittleEndian.Uint64(b))}
	b = b[8:]
	for {
		off := binary.LittleEndian.Uint64(b)
		n := binary.LittleEndian.Uint32(b[8:])
		b = b[12:]
		if off == 1<<64-1 {
			break
		}
		s.runs = append(s.runs, run{off: int64(off), b: b[:n]})
		b = b[n:]
	}
	return s
}

func (s *sparse) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("negative offset")
	}
	if off >= s.size {
		return 0, io.EOF
	}
	n := len(p)
	if int64(n) > s.size-off {
		n = int(s.size - off)
	}
	q := p[:n]
	clear(q)
	i := sort.Search(len(s.runs), func(i int) bool { return s.runs[i].off+int64(len(s.runs[i].b)) > off })
	for ; i < len(s.runs) && s.runs[i].off < off+int64(n); i++ {
		r := s.runs[i]
		lo, hi := max(off, r.off), min(off+int64(n), r.off+int64(len(r.b)))
		copy(q[lo-off:hi-off], r.b[lo-r.off:hi-r.off])
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// overlay is an image with some bytes changed.
type overlay struct {
	base  io.ReaderAt
	size  int64
	patch map[int64]byte
}

func newOverlay(base io.ReaderAt, size int64) *overlay {
	return &overlay{base: base, size: size, patch: map[int64]byte{}}
}

func (o *overlay) set(off int64, b ...byte) {
	for i, c := range b {
		o.patch[off+int64(i)] = c
	}
}

func (o *overlay) ReadAt(p []byte, off int64) (int, error) {
	if off >= o.size {
		return 0, io.EOF
	}
	q := p
	if int64(len(q)) > o.size-off {
		q = q[:o.size-off]
	}
	n, err := o.base.ReadAt(q, off)
	if len(o.patch) < len(q) {
		for k, v := range o.patch {
			if k >= off && k < off+int64(n) {
				q[k-off] = v
			}
		}
	} else {
		for i := 0; i < n; i++ {
			if v, ok := o.patch[off+int64(i)]; ok {
				q[i] = v
			}
		}
	}
	if err == nil && n < len(p) {
		err = io.EOF
	}
	return n, err
}

// pattern is the content of the fixture files (sparse.py pattern).
func pattern(seed uint64, size int) []byte {
	out := make([]byte, 0, size+512)
	var v [8]byte
	for i := uint64(0); len(out) < size; i++ {
		binary.LittleEndian.PutUint64(v[:], (seed*1000003+i)*0x9E3779B97F4A7C15)
		for j := 0; j < 64; j++ {
			out = append(out, v[:]...)
		}
	}
	return out[:size]
}

// zeroTail is data followed by zeros up to size (the fuzz images).
type zeroTail struct {
	data []byte
	size int64
}

func (z zeroTail) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("negative offset")
	}
	if off >= z.size {
		return 0, io.EOF
	}
	n := len(p)
	if int64(n) > z.size-off {
		n = int(z.size - off)
	}
	clear(p[:n])
	if off < int64(len(z.data)) {
		copy(p[:n], z.data[off:])
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}
