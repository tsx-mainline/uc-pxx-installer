package fatfs

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"uc-pxx-installer/internal/fatfs/fattest"
)

// Synthetic images (package fattest): the layouts and the damage that the
// real tools do not make.

func small(typ int) fattest.Spec {
	switch typ {
	case 12:
		return fattest.Spec{Type: 12, Sectors: 2880, SecPerClus: 1, RootEntries: 224, Label: "F12"}
	case 16:
		return fattest.Spec{Type: 16, Sectors: 8192, SecPerClus: 1, Label: "F16"}
	}
	return fattest.Spec{Type: 32, Sectors: 70000, SecPerClus: 1, Label: "F32"}
}

func mustOpen(t testing.TB, b []byte) *FS {
	t.Helper()
	fs, err := Open(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func TestSynthRoundTrip(t *testing.T) {
	rnd := rand.New(rand.NewSource(7))
	for _, typ := range []int{12, 16, 32} {
		t.Run(fmt.Sprint("FAT", typ), func(t *testing.T) {
			im := fattest.New(small(typ))
			files := map[string][]byte{}
			// names: 8.3, lower case, long, Unicode, 255 units, a name that needs 20 LFN entries
			names := []string{"README.TXT", "lower.txt", "A Long Name With Spaces.dat", "Ünïcödé 日本 😀.bin",
				strings.Repeat("x", 255), strings.Repeat("é", 250) + ".z"}
			for i, n := range names {
				d := pattern(uint64(100+i), rnd.Intn(5000)+1)
				files["/"+n] = d
				im.Root.File(n, d)
			}
			sub := im.Root.Mkdir("Sub Directory")
			deeper := sub.Mkdir("DEEPER")
			files["/Sub Directory/inner file.txt"] = pattern(200, 3000)
			sub.File("inner file.txt", files["/Sub Directory/inner file.txt"])
			files["/Sub Directory/DEEPER/X"] = pattern(201, 10)
			deeper.File("X", files["/Sub Directory/DEEPER/X"])
			// a fragmented file: clusters in a shuffled order
			cs := im.ClusterSize()
			d := pattern(300, 37*cs+5)
			cl := im.Alloc(38)
			rnd.Shuffle(len(cl), func(i, j int) { cl[i], cl[j] = cl[j], cl[i] })
			sub.FileAt("frag.dat", d, cl)
			files["/Sub Directory/frag.dat"] = d
			files["/empty"] = nil
			im.Root.File("empty", nil)

			fs := mustOpen(t, im.B)
			if fs.Info().Type != Type(typ) {
				t.Fatalf("type %v", fs.Info().Type)
			}
			if p := fs.Check(); len(p) != 0 {
				t.Fatalf("check: %v", p)
			}
			for p, want := range files {
				f, err := fs.Open(p)
				if err != nil {
					t.Fatalf("%q: %v", p, err)
				}
				checkContent(t, f, want)
			}
			f, _ := fs.Open("/Sub Directory/frag.dat")
			if f.Fragments() < 10 {
				t.Fatalf("%d fragments", f.Fragments())
			}
			if _, err := FindFirmware(fs); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSynthNames(t *testing.T) {
	im := fattest.New(small(16))
	d := im.Root
	// a long name with a wrong checksum: the 8.3 name is used
	e := d.Entry("Long Name One.txt", 0x20, 0, 0)
	e[0] = 'Q'
	// an orphan long-name entry (no short entry after it), then a deleted entry
	orphan := make([]byte, 32)
	orphan[0], orphan[11], orphan[13] = 0x41, 0x0F, 0x99
	d.Raw(orphan)
	del := make([]byte, 32)
	copy(del, "\xe5ELETED BIN")
	del[11] = 0x20
	d.Raw(del)
	// 0x05 stands for 0xE5 (CP437 "σ"); the NT flags give lower case
	k := make([]byte, 32)
	copy(k, "\x05BC     TXT")
	k[11] = 0x20
	d.Raw(k)
	nt := make([]byte, 32)
	copy(nt, "MIXED   TXT")
	nt[11], nt[12] = 0x20, 0x08
	d.Raw(nt)
	// a volume label in the middle and an end mark: what follows is not read
	lab := make([]byte, 32)
	copy(lab, "LABEL      ")
	lab[11] = 0x08
	d.Raw(lab)
	d.Raw(make([]byte, 32))
	after := make([]byte, 32)
	copy(after, "AFTER   BIN")
	after[11] = 0x20
	d.Raw(after)

	fs := mustOpen(t, im.B)
	es, err := fs.ReadDir("/")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range es {
		got = append(got, e.Name)
	}
	want := "QONGNA~1.TXT|σBC.TXT|mixed.TXT"
	if strings.Join(got, "|") != want {
		t.Fatalf("names %q, want %q", strings.Join(got, "|"), want)
	}
	if _, err := FindFirmware(fs); err == nil {
		t.Fatal("a .bin after the end mark or deleted counted")
	}
}

func TestSynthFirmwareRules(t *testing.T) {
	mk := func(names ...string) *FS {
		im := fattest.New(small(32))
		for i, n := range names {
			if strings.HasSuffix(n, "/") {
				im.Root.Mkdir(strings.TrimSuffix(n, "/"))
				continue
			}
			im.Root.File(n, pattern(uint64(i), 1000))
		}
		return mustOpen(t, im.B)
	}
	for _, c := range []struct {
		names []string
		want  Reason // 0 = found
	}{
		{[]string{"fw.bin"}, 0},
		{[]string{"FW.BIN", "._FW.BIN", "notes.txt", "x.bin/", "fw.bin.txt"}, 0},
		{[]string{"a.bin", "b.Bin"}, TwoOrMore},
		{[]string{"a.bin", "b.bin", "c.BIN"}, TwoOrMore},
		{nil, NoFirmware},
		{[]string{".bin"}, NoFirmware}, // only the extension: not a name
		{[]string{"._fw.bin", "dir.bin/"}, NoFirmware},
	} {
		_, err := FindFirmware(mk(c.names...))
		if c.want == 0 {
			if err != nil {
				t.Fatalf("%v: %v", c.names, err)
			}
			continue
		}
		refusal(t, err, c.want)
	}
	// a control character in a name is quoted in the message
	im := fattest.New(small(32))
	im.Root.File("a\x1b[2J.bin", pattern(1, 10))
	im.Root.File("b.bin", pattern(1, 10))
	_, err := FindFirmware(mustOpen(t, im.B))
	if r := refusal(t, err, TwoOrMore); strings.Contains(r.Error(), "\x1b") {
		t.Fatalf("raw escape in %q", r.Error())
	}
	// an empty .bin
	im = fattest.New(small(32))
	im.Root.File("fw.bin", nil)
	_, err = FindFirmware(mustOpen(t, im.B))
	refusal(t, err, Empty)
}

func TestSynthDirty(t *testing.T) {
	for _, c := range []struct {
		typ  int
		mod  func(im *fattest.Image)
		want string
	}{
		{16, (*fattest.Image).SetDirty, "boot sector"},
		{16, (*fattest.Image).ClearCleanBit, "clean-shutdown"},
		{16, (*fattest.Image).ClearNoErrorBit, "disk error"},
		{32, (*fattest.Image).SetDirty, "boot sector"},
		{32, (*fattest.Image).ClearCleanBit, "clean-shutdown"},
		{32, (*fattest.Image).ClearNoErrorBit, "disk error"},
		{12, (*fattest.Image).SetDirty, "boot sector"},
	} {
		im := fattest.New(small(c.typ))
		im.Root.File("fw.bin", pattern(1, 3000))
		c.mod(im)
		_, err := FindFirmware(mustOpen(t, im.B))
		if r := refusal(t, err, Dirty); !strings.Contains(r.Error(), c.want) {
			t.Fatalf("FAT%d: %v", c.typ, r)
		}
	}
	// FAT12 has no bits in FAT entry 1: a changed entry 1 is not dirty
	im := fattest.New(small(12))
	im.Root.File("fw.bin", pattern(1, 3000))
	im.SetFAT(1, 0)
	if _, err := FindFirmware(mustOpen(t, im.B)); err != nil {
		t.Fatal(err)
	}
}

// TestSynthDamage: every chain and directory error is found, by Open and
// by Check, and FindFirmware refuses the drive.
func TestSynthDamage(t *testing.T) {
	type tc struct {
		name string
		make func(im *fattest.Image) // the image has fw.bin (5 clusters) and other.dat
		want string
	}
	var fw, other []uint32
	base := func(typ int) *fattest.Image {
		im := fattest.New(small(typ))
		cs := im.ClusterSize()
		fw = im.Root.File("fw.bin", pattern(1, 5*cs))
		other = im.Root.File("other.dat", pattern(2, 2*cs))
		return im
	}
	cases := []tc{
		{"a loop", func(im *fattest.Image) { im.SetFAT(fw[3], fw[1]) }, "used twice"},
		{"a cluster that points to itself", func(im *fattest.Image) { im.SetFAT(fw[0], fw[0]) }, "used twice"},
		{"into a free cluster", func(im *fattest.Image) { im.SetFAT(fw[2], 0) }, "a free cluster"},
		{"to a bad cluster", func(im *fattest.Image) { im.SetFAT(fw[2], im.Bad()) }, "a bad cluster"},
		{"to the reserved value 1", func(im *fattest.Image) { im.SetFAT(fw[2], 1) }, "the reserved value 1"},
		{"out of range", func(im *fattest.Image) { im.SetFAT(fw[2], im.Clusters+2) }, "outside the"},
		{"ends early", func(im *fattest.Image) { im.SetFAT(fw[2], im.EOC()) }, "the chain has 3 clusters"},
		{"too long", func(im *fattest.Image) {
			x := im.Alloc(1)
			im.SetFAT(fw[4], x[0])
		}, "longer than the size"},
		{"a cross-link", func(im *fattest.Image) { im.SetFAT(other[0], fw[3]) }, "used twice"},
	}
	for _, typ := range []int{12, 16, 32} {
		for _, c := range cases {
			t.Run(fmt.Sprintf("FAT%d %s", typ, c.name), func(t *testing.T) {
				im := base(typ)
				c.make(im)
				fs := mustOpen(t, im.B)
				p := fs.Check()
				if len(p) == 0 || !strings.Contains(strings.Join(p, "\n"), c.want) {
					t.Fatalf("check: %v, want %q", p, c.want)
				}
				_, err := FindFirmware(fs)
				if r := refusal(t, err, Damaged); !strings.Contains(r.Error(), c.want) {
					t.Fatalf("%v, want %q", r, c.want)
				}
				if c.name != "a cross-link" {
					if _, err := fs.Open("/fw.bin"); !errors.Is(err, ErrDamaged) {
						t.Fatalf("Open: %v", err)
					}
				}
			})
		}
	}
}

func TestSynthDirDamage(t *testing.T) {
	t.Run("a subdirectory that is the root (FAT32)", func(t *testing.T) {
		im := fattest.New(small(32))
		im.Root.File("fw.bin", pattern(1, 100))
		im.Root.Entry("LOOP", 0x10, im.Root.Cluster, 0)
		_, err := FindFirmware(mustOpen(t, im.B))
		refusal(t, err, Damaged)
	})
	t.Run("a directory that contains itself", func(t *testing.T) {
		im := fattest.New(small(16))
		im.Root.File("fw.bin", pattern(1, 100))
		sub := im.Root.Mkdir("SUB")
		sub.Entry("SELF", 0x10, sub.Cluster, 0)
		fs := mustOpen(t, im.B)
		if p := fs.Check(); len(p) == 0 || !strings.Contains(p[0], "used twice") {
			t.Fatalf("check: %v", p)
		}
		// a path through the loop ends at the depth cap, not in a hang
		if _, err := fs.Open("/SUB" + strings.Repeat("/SELF", 40) + "/x"); err == nil {
			t.Fatal("no error")
		}
	})
	t.Run("the root chain loops (FAT32)", func(t *testing.T) {
		im := fattest.New(small(32))
		im.Root.File("fw.bin", pattern(1, 100))
		im.SetFAT(im.Root.Cluster, im.Root.Cluster)
		_, err := FindFirmware(mustOpen(t, im.B))
		refusal(t, err, Damaged)
	})
	t.Run("a directory without a cluster", func(t *testing.T) {
		im := fattest.New(small(16))
		im.Root.File("fw.bin", pattern(1, 100))
		im.Root.Entry("NOCL", 0x10, 0, 0)
		_, err := FindFirmware(mustOpen(t, im.B))
		refusal(t, err, Damaged)
	})
	t.Run("a directory above the size cap", func(t *testing.T) {
		im := fattest.New(fattest.Spec{Type: 32, Sectors: 70000, SecPerClus: 1})
		im.Root.File("fw.bin", pattern(1, 100))
		cl := append([]uint32{im.Root.Cluster}, im.Alloc(MaxDirBytes/512)...)
		im.Link(cl)
		_, err := FindFirmware(mustOpen(t, im.B))
		if r := refusal(t, err, Damaged); !strings.Contains(r.Error(), "a directory of more than") {
			t.Fatal(r)
		}
	})
	t.Run("too deep", func(t *testing.T) {
		im := fattest.New(small(16))
		im.Root.File("fw.bin", pattern(1, 100))
		d := im.Root
		for i := 0; i < MaxDepth+2; i++ {
			d = d.Mkdir("D")
		}
		fs := mustOpen(t, im.B)
		if p := fs.Check(); len(p) != 1 || !strings.Contains(p[0], "levels") {
			t.Fatalf("check: %v", p)
		}
	})
	t.Run("a file with clusters and size 0, one with a size and no cluster", func(t *testing.T) {
		im := fattest.New(small(16))
		im.Root.File("fw.bin", pattern(1, 100))
		x := im.Alloc(1)
		im.Root.Entry("ZERO", 0x20, x[0], 0)
		im.Root.Entry("NOCL", 0x20, 0, 100)
		fs := mustOpen(t, im.B)
		if p := fs.Check(); len(p) != 2 {
			t.Fatalf("check: %v", p)
		}
		if _, err := fs.Open("/ZERO"); !errors.Is(err, ErrDamaged) {
			t.Fatal(err)
		}
		if _, err := fs.Open("/NOCL"); !errors.Is(err, ErrDamaged) {
			t.Fatal(err)
		}
	})
	t.Run("FAT32 without mirroring reads the active FAT", func(t *testing.T) {
		s := small(32)
		s.NoMirrorFAT1 = true
		im := fattest.New(s)
		f := im.Root.File("fw.bin", pattern(1, 3000))
		// FAT copy 1 is not active: damage there is not seen
		im.SetFATCopy(0, f[0], 0)
		fs := mustOpen(t, im.B)
		g, err := FindFirmware(fs)
		if err != nil {
			t.Fatal(err)
		}
		checkContent(t, g, pattern(1, 3000))
		// damage in the active copy is
		im.SetFATCopy(1, f[0], 0)
		_, err = FindFirmware(mustOpen(t, im.B))
		refusal(t, err, Damaged)
	})
}

// TestSynthBootSector: every bad field is refused with an error (never a
// panic, never a volume).
func TestSynthBootSector(t *testing.T) {
	good := func(typ int) []byte {
		im := fattest.New(small(typ))
		im.Root.File("fw.bin", pattern(1, 100))
		return im.B
	}
	set16 := func(o int, v uint16) func([]byte) { return func(b []byte) { le.PutUint16(b[o:], v) } }
	set32 := func(o int, v uint32) func([]byte) { return func(b []byte) { le.PutUint32(b[o:], v) } }
	setb := func(o int, v byte) func([]byte) { return func(b []byte) { b[o] = v } }
	for _, c := range []struct {
		name string
		typ  int
		mod  func([]byte)
		want string
	}{
		{"no signature", 16, setb(510, 0), "signature"},
		{"exFAT", 16, func(b []byte) { copy(b[3:], "EXFAT   ") }, "exFAT"},
		{"NTFS", 16, func(b []byte) { copy(b[3:], "NTFS    ") }, "NTFS"},
		{"0 bytes per sector", 16, set16(11, 0), "bytes per sector"},
		{"300 bytes per sector", 16, set16(11, 300), "bytes per sector"},
		{"0 sectors per cluster", 16, setb(13, 0), "sectors per cluster"},
		{"3 sectors per cluster", 16, setb(13, 3), "sectors per cluster"},
		{"512 KiB clusters", 16, func(b []byte) { le.PutUint16(b[11:], 4096); b[13] = 128 }, "cluster size"},
		{"no reserved sector", 16, set16(14, 0), "reserved"},
		{"no FAT", 16, setb(16, 0), "FATs"},
		{"5 FATs", 16, setb(16, 5), "FATs"},
		{"no sector count", 16, func(b []byte) { le.PutUint16(b[19:], 0); le.PutUint32(b[32:], 0) }, "sector count"},
		{"no root entries", 16, set16(17, 0), "root entries"},
		{"FAT too small", 16, set16(22, 1), "a FAT of"},
		{"metadata larger than the volume", 16, set16(22, 0xFFFF), "metadata"},
		{"volume larger than the image", 16, set16(19, 0xFFFF), "cut short"},
		{"FAT32 with root entries", 32, set16(17, 16), "fixed root"},
		{"FAT32 version", 32, set16(42, 1), "version"},
		{"FAT32 root cluster 0", 32, set32(44, 0), "root cluster"},
		{"FAT32 root cluster past the end", 32, set32(44, 0x0FFFFFF0), "root cluster"},
		{"FAT32 active FAT 3 of 2", 32, set16(40, 0x83), "active FAT"},
		{"FAT32 FAT size 0", 32, set32(36, 0), "FAT size 0"},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := good(c.typ)
			c.mod(b)
			_, err := Open(bytes.NewReader(b), int64(len(b)))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
	if _, err := Open(bytes.NewReader(make([]byte, 100)), 100); !errors.Is(err, ErrNotFAT) {
		t.Fatal(err)
	}
}

// TestSynthMBR: a host that partitions the drive (one FAT partition) is
// read; other tables are refused with a clear reason.
func TestSynthMBR(t *testing.T) {
	vol := fattest.New(small(16))
	vol.Root.File("fw.bin", pattern(1, 3000))
	const start = 2048
	mbr := func(entries ...[3]uint32) []byte { // type, start LBA, sectors
		b := make([]byte, start*512+len(vol.B))
		for i, e := range entries {
			p := b[446+16*i:]
			p[4] = byte(e[0])
			le.PutUint32(p[8:], e[1])
			le.PutUint32(p[12:], e[2])
		}
		b[510], b[511] = 0x55, 0xAA
		copy(b[start*512:], vol.B)
		return b
	}
	n := uint32(len(vol.B) / 512)
	fs := mustOpen(t, mbr([3]uint32{0x0E, start, n}))
	if fs.Info().Offset != start*512 {
		t.Fatalf("offset %d", fs.Info().Offset)
	}
	f, err := FindFirmware(fs)
	if err != nil {
		t.Fatal(err)
	}
	checkContent(t, f, pattern(1, 3000))
	for _, c := range []struct {
		name string
		b    []byte
		want string
	}{
		{"GPT", mbr([3]uint32{0xEE, 1, n}), "GPT"},
		{"NTFS/exFAT partition", mbr([3]uint32{0x07, start, n}), "NTFS or exFAT"},
		{"Linux partition", mbr([3]uint32{0x83, start, n}), "type 0x83"},
		{"two partitions", mbr([3]uint32{0x0C, start, n / 2}, [3]uint32{0x0C, start + n/2, n / 2}), "2 partitions"},
		{"past the end", mbr([3]uint32{0x0C, start, n + 1}), "not inside"},
		{"no partition", mbr(), "no FAT file system"},
	} {
		_, err := Open(bytes.NewReader(c.b), int64(len(c.b)))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}

func TestReadAtEdges(t *testing.T) {
	im := fattest.New(small(16))
	cs := im.ClusterSize()
	d := pattern(1, 3*cs)
	im.Root.File("fw.bin", d)
	f, err := FindFirmware(mustOpen(t, im.B))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ReadAt(make([]byte, 1), -1); err == nil {
		t.Fatal("negative offset")
	}
	if n, err := f.ReadAt(make([]byte, 1), int64(len(d))); n != 0 || err == nil {
		t.Fatal("read at the end")
	}
	p := make([]byte, cs)
	if n, err := f.ReadAt(p, int64(cs)); n != cs || err != nil || !bytes.Equal(p, d[cs:2*cs]) {
		t.Fatalf("one cluster: %d %v", n, err)
	}
	if n, err := f.ReadAt(make([]byte, 0), 5); n != 0 || err != nil {
		t.Fatalf("empty read: %d %v", n, err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
