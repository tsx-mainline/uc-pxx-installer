package fatfs

import (
	"bytes"
	"errors"
	"io"
	"math/rand"
	"strings"
	"testing"
)

// The real images (testdata/mkfixtures.sh): made by busybox mkfs.vfat (as
// the RAM image makes the drive), dosfstools, mtools and the Linux vfat
// driver. The content of each file is pattern(seed, size).
const fwSize = 307533

func openFixture(t testing.TB, name string) (*FS, *sparse) {
	t.Helper()
	s := loadSparse(t, name)
	fs, err := Open(s, s.size)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return fs, s
}

// checkContent reads f whole, in random pieces, and through Reader.
func checkContent(t *testing.T, f *File, want []byte) {
	t.Helper()
	if f.Size() != int64(len(want)) {
		t.Fatalf("%q: size %d, want %d", f.Name(), f.Size(), len(want))
	}
	all, err := io.ReadAll(f.Reader())
	if err != nil || !bytes.Equal(all, want) {
		t.Fatalf("%q: content differs (err %v)", f.Name(), err)
	}
	rnd := rand.New(rand.NewSource(1))
	for i := 0; i < 200; i++ {
		off := rnd.Int63n(int64(len(want)) + 1)
		n := rnd.Intn(70000)
		p := make([]byte, n)
		got, err := f.ReadAt(p, off)
		end := min(off+int64(n), int64(len(want)))
		if got != int(end-off) || !bytes.Equal(p[:got], want[off:end]) {
			t.Fatalf("%q: ReadAt(%d, %d) = %d %v", f.Name(), n, off, got, err)
		}
		if (end < off+int64(n)) != (err == io.EOF) {
			t.Fatalf("%q: ReadAt(%d, %d): err %v", f.Name(), n, off, err)
		}
	}
}

func TestRealImages(t *testing.T) {
	for _, c := range []struct {
		fixture string
		typ     Type
		label   string
		cluster int64
		name    string
		seed    uint64
		size    int
		frag    bool // the .bin is in more than one run
	}{
		{"busybox-1200m", FAT32, "UC-PXX-INST", 4096, "UC-P10_1.0.0.94_userdebug.bin", 1, fwSize, false},
		{"dosfs-1200m", FAT32, "UC-PXX-INST", 0, "uc-p10 firmware 1.0.0.94.bin", 2, fwSize, false},
		// mtools wrote U+1F600 as U+F600 (no surrogate pair); linux-clean has a real pair
		{"f16-lfn-frag", FAT16, "FAT16TEST", 2048, "Firmware – ünïcode \uf600 test.bin", 3, fwSize, true},
		{"f12", FAT12, "FAT12TEST", 0, "small firmware.bin", 6, 102407, false},
		{"linux-clean", FAT32, "UC-PXX-INST", 4096, "UC-P10 Firmware 1.0.0.94 (userdebug).bin", 7, fwSize, false},
	} {
		t.Run(c.fixture, func(t *testing.T) {
			fs, _ := openFixture(t, c.fixture)
			in := fs.Info()
			if in.Type != c.typ || in.Label != c.label || (c.cluster != 0 && in.ClusterSize != c.cluster) || in.Offset != 0 {
				t.Fatalf("info %+v", in)
			}
			if d := fs.Dirty(); len(d) != 0 {
				t.Fatalf("dirty: %v", d)
			}
			if p := fs.Check(); len(p) != 0 {
				t.Fatalf("check: %v", p)
			}
			f, err := FindFirmware(fs)
			if err != nil {
				t.Fatal(err)
			}
			if f.Name() != c.name {
				t.Fatalf("firmware %q, want %q", f.Name(), c.name)
			}
			checkContent(t, f, pattern(c.seed, c.size))
			if c.frag && f.Fragments() < 2 {
				t.Fatalf("%d fragments, want a fragmented file", f.Fragments())
			}
			t.Logf("%s: %s %d-byte clusters, %d clusters, %q in %d fragment(s)", c.fixture, in.Type, in.ClusterSize,
				in.Clusters, f.Name(), f.Fragments())
		})
	}
}

func TestRealPaths(t *testing.T) {
	fs, _ := openFixture(t, "f16-lfn-frag")
	for _, p := range []string{"/Sub Dir Ünïcödé/deeper/日本語/a file with a long name.txt",
		"SUB DIR ÜNÏCÖDÉ/DEEPER/日本語/A FILE WITH A LONG NAME.TXT", "//Sub Dir Ünïcödé/./deeper/日本語/a file with a long name.txt"} {
		f, err := fs.Open(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		checkContent(t, f, pattern(10, 5000))
	}
	b, err := fs.ReadFile("/readme.txt", 1000)
	if err != nil || !bytes.Equal(b, pattern(11, 100)) {
		t.Fatalf("readme.txt: %v", err)
	}
	if _, err := fs.ReadFile("/readme.txt", 99); err == nil {
		t.Fatal("ReadFile above the cap")
	}
	if _, err := fs.Open("/nothing.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing file: %v", err)
	}
	if _, err := fs.Open("/readme.txt/x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("file as a directory: %v", err)
	}
	if _, err := fs.Open("/Sub Dir Ünïcödé"); err == nil {
		t.Fatal("opened a directory as a file")
	}
	es, err := fs.ReadDir("/Sub Dir Ünïcödé/deeper")
	if err != nil || len(es) != 1 || es[0].Name != "日本語" || !es[0].Dir {
		t.Fatalf("ReadDir: %+v %v", es, err)
	}
	// the 8.3 name works too
	f, err := FindFirmware(fs)
	if err != nil {
		t.Fatal(err)
	}
	var short string
	root, _ := fs.ReadDir("/")
	for _, e := range root {
		if e.Name == f.Name() {
			short = e.ShortName
		}
	}
	if short == "" || !strings.Contains(short, "~") {
		t.Fatalf("short name %q", short)
	}
	g, err := fs.Open("/" + short)
	if err != nil || g.Size() != f.Size() {
		t.Fatalf("open by 8.3 name %q: %v", short, err)
	}

	fs12, _ := openFixture(t, "f12")
	if b, err := fs12.ReadFile("/docs/notes.txt", 1000); err != nil || !bytes.Equal(b, pattern(11, 100)) {
		t.Fatalf("FAT12 docs/notes.txt: %v", err)
	}
	lc, _ := openFixture(t, "linux-clean")
	if b, err := lc.ReadFile("/System Volume Information/IndexerVolumeGuid", 1<<20); err != nil || !bytes.Equal(b, pattern(12, 3000)) {
		t.Fatalf("linux-clean: %v", err)
	}
	if b, err := lc.ReadFile("/system volume information/EMOJI 😀 NAME.TXT", 100); err != nil || !bytes.Equal(b, pattern(13, 10)) {
		t.Fatalf("linux-clean, a name with a surrogate pair: %v", err)
	}
}

func refusal(t *testing.T, err error, want Reason) *Refusal {
	t.Helper()
	var r *Refusal
	if !errors.As(err, &r) || r.Reason != want {
		t.Fatalf("err = %v, want a refusal %q", err, want)
	}
	return r
}

func TestRealRefusals(t *testing.T) {
	t.Run("two .bin", func(t *testing.T) {
		fs, _ := openFixture(t, "f32-two")
		_, err := FindFirmware(fs)
		r := refusal(t, err, TwoOrMore)
		if strings.Join(r.Detail, " ") != `"B.BIN" "a.bin"` || !strings.Contains(r.Why, "there are 2 firmware files") {
			t.Fatalf("%v", r)
		}
	})
	t.Run("no .bin", func(t *testing.T) {
		fs, _ := openFixture(t, "f32-none")
		_, err := FindFirmware(fs)
		refusal(t, err, NoFirmware)
		es, _ := fs.ReadDir("/")
		var names []string
		for _, e := range es {
			names = append(names, e.Name)
		}
		got := strings.Join(names, "|")
		for _, n := range []string{"firmware.bin.txt", "._firmware.bin", "old.bin", "sub"} {
			if !strings.Contains(got, n) {
				t.Fatalf("root has %s, no %s", got, n)
			}
		}
		if strings.Contains(got, "deleted") {
			t.Fatalf("a deleted entry is listed: %s", got)
		}
	})
	t.Run("dirty, written by Linux, taken while mounted", func(t *testing.T) {
		fs, _ := openFixture(t, "linux-dirty")
		_, err := FindFirmware(fs)
		r := refusal(t, err, Dirty)
		if len(r.Detail) != 1 || !strings.Contains(r.Detail[0], "boot sector") {
			t.Fatalf("%v", r)
		}
	})
}

// TestRealDerived: the busybox drive with the changes a host or a cut copy
// leaves.
func TestRealDerived(t *testing.T) {
	fs0, s := openFixture(t, "busybox-1200m")
	fat1 := fs0.fatOff + 4                             // FAT32 entry 1 of copy 1
	fat2 := fs0.fatOff + fs0.fatSize + 4               // and of copy 2
	root := fs0.clusterOff(fs0.rootCluster) - fs0.base // the root directory
	f0, err := FindFirmware(fs0)
	if err != nil {
		t.Fatal(err)
	}
	// the firmware's short entry: the first entry with attr 0x20 in the root
	b := make([]byte, 4096)
	s.ReadAt(b, root)
	ent := int64(-1)
	for o := 0; o < len(b); o += 32 {
		if b[o+11] == 0x20 {
			ent = root + int64(o)
			break
		}
	}
	if ent < 0 {
		t.Fatal("no file entry")
	}
	size := int64(le.Uint32(b[ent-root+28:]))
	c2 := f0.clusters[1]
	for _, c := range []struct {
		name  string
		patch func(o *overlay)
		size  int64
		want  Reason
		in    string
	}{
		{"Windows/macOS: clean-shutdown bit off", func(o *overlay) { o.set(fat1+3, 0x07); o.set(fat2+3, 0x07) }, 0, Dirty, "clean-shutdown"},
		{"disk error bit off", func(o *overlay) { o.set(fat1+3, 0x0B) }, 0, Dirty, "disk error"},
		{"Linux: boot sector dirty bit", func(o *overlay) { o.set(0x41, 1) }, 0, Dirty, "boot sector"},
		{"FAT copy 2 differs", func(o *overlay) { o.set(fs0.fatOff+fs0.fatSize+4*int64(c2), 0) }, 0, Damaged, "FAT copy 2 differs"},
		{"the size is larger than the chain", func(o *overlay) {
			o.set(ent+28, le.AppendUint32(nil, uint32(size+4096))...)
		}, 0, Damaged, "the chain has"},
		{"the size is smaller than the chain", func(o *overlay) {
			o.set(ent+28, le.AppendUint32(nil, uint32(size-4096))...)
		}, 0, Damaged, "longer than the size"},
		{"a cut FAT: the chain ends in a free cluster", func(o *overlay) {
			for i := 0; i < fs0.numFATs; i++ {
				o.set(fs0.fatOff+int64(i)*fs0.fatSize+4*int64(c2), 0, 0, 0, 0)
			}
		}, 0, Damaged, "a free cluster"},
		{"the file is empty", func(o *overlay) { o.set(ent+28, 0, 0, 0, 0); o.set(ent+26, 0, 0); o.set(ent+20, 0, 0) }, 0, Empty, "is empty"},
		{"the drive is cut short", func(o *overlay) {}, s.size - 512, NotFAT, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := newOverlay(s, s.size)
			c.patch(o)
			sz := s.size
			if c.size != 0 {
				sz = c.size
			}
			o.size = sz
			fs, err := Open(o, sz)
			if c.want == NotFAT {
				if !errors.Is(err, ErrDamaged) || !strings.Contains(err.Error(), "cut short") {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = FindFirmware(fs)
			r := refusal(t, err, c.want)
			if !strings.Contains(r.Error(), c.in) {
				t.Fatalf("%v, want %q", r, c.in)
			}
		})
	}
}
