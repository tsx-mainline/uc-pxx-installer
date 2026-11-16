package bundle_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"uc-pxx-installer/internal/provision/bundle"
	"uc-pxx-installer/internal/provision/bundle/bundletest"
)

// memSink is a Sink in memory: the files it holds and every create.
type memSink struct {
	files   map[string]*bytes.Buffer
	created []string
	failAt  string // Create of this path fails
}

func newSink() *memSink { return &memSink{files: map[string]*bytes.Buffer{}} }

type memFile struct{ b *bytes.Buffer }

func (f memFile) Write(p []byte) (int, error) { return f.b.Write(p) }
func (f memFile) Close() error                { return nil }

func (s *memSink) Create(p string, size int64) (io.WriteCloser, error) {
	if p == s.failAt {
		return nil, errors.New("no room")
	}
	if _, ok := s.files[p]; ok {
		return nil, errors.New("exists")
	}
	s.files[p] = &bytes.Buffer{}
	s.created = append(s.created, p)
	return memFile{s.files[p]}, nil
}

func (s *memSink) Remove(p string) error { delete(s.files, p); return nil }

func (s *memSink) check(t *testing.T, b *bundletest.Bundle) {
	t.Helper()
	if len(s.files) != len(b.Members) {
		t.Fatalf("%d files in the sink, the bundle has %d", len(s.files), len(b.Members))
	}
	for p, want := range b.Members {
		if got := s.files[p]; got == nil || !bytes.Equal(got.Bytes(), want) {
			t.Fatalf("%s: not the member's bytes", p)
		}
	}
}

func feed(u *bundle.Unpacker, b []byte, rng *rand.Rand) error {
	for len(b) > 0 {
		n := 1 + rng.Intn(70000)
		if n > len(b) {
			n = len(b)
		}
		if _, err := u.Write(b[:n]); err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}

// TestUnpackWhole: the bundle in one write and in random pieces, with Go's
// end of archive and with Python's record padding; every member in the sink,
// bundle.json first, the counts.
func TestUnpackWhole(t *testing.T) {
	for _, pad := range []bool{false, true} {
		b := bundletest.Simple("0.2.0-rc10", pad)
		for _, chunked := range []bool{false, true} {
			s := newSink()
			u, err := bundle.NewUnpacker(b.Pins, s)
			if err != nil {
				t.Fatal(err)
			}
			if chunked {
				err = feed(u, b.Tar, rand.New(rand.NewSource(7)))
			} else {
				_, err = u.Write(b.Tar)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := u.Finish(); err != nil {
				t.Fatal(err)
			}
			s.check(t, b)
			if s.created[0] != bundle.DocName {
				t.Fatalf("the first file written is %s", s.created[0])
			}
			if d, n := u.Members(); d != n || n != len(b.Members) {
				t.Fatalf("members %d of %d", d, n)
			}
		}
	}
}

// TestUnpackPins: a bundle.json other than the pin, a whole file other than
// the pin, a stream longer or shorter than the pin.
func TestUnpackPins(t *testing.T) {
	b := bundletest.Simple("0.2.0-rc10", true)
	try := func(p bundle.Pins, data []byte, want string) {
		t.Helper()
		s := newSink()
		u, err := bundle.NewUnpacker(p, s)
		if err != nil {
			t.Fatal(err)
		}
		_, err = u.Write(data)
		if err == nil {
			err = u.Finish()
		}
		if err == nil || !strings.Contains(err.Error(), want) || !bundle.IsBundle(err) {
			t.Fatalf("want %q, got %v", want, err)
		}
		if len(s.files) > 0 && want == "bundle.json has sha256" {
			t.Fatalf("a file was written before bundle.json matched its pin: %v", s.created)
		}
	}
	p := b.Pins
	p.IndexSHA256 = strings.Repeat("a", 64)
	try(p, b.Tar, "bundle.json has sha256")
	p = b.Pins
	p.SHA256 = strings.Repeat("b", 64)
	try(p, b.Tar, "the bundle has sha256")
	try(b.Pins, append(append([]byte(nil), b.Tar...), 0), "longer than the pinned")
	try(b.Pins, b.Tar[:len(b.Tar)-512], "ended after")
	x := *b.Pins.Expect
	x.Packages = 2
	p = b.Pins
	p.Expect = &x
	try(p, b.Tar, "profiles.json says 2")
	x = *b.Pins.Expect
	x.LVA = true
	p.Expect = &x
	try(p, b.Tar, "lva.tar")
}

// TestUnpackResume: a stream cut inside a member, and a member whose bytes
// are not its line: Resume drops that member's file and returns the offset
// of its header; the rest from there completes the bundle.
func TestUnpackResume(t *testing.T) {
	b := bundletest.Simple("0.2.0-rc10", false)
	for _, cut := range []int{100, 512 + 200, len(b.Doc) + 1024 + 3000, len(b.Tar) - 2000, len(b.Tar) - 600} {
		s := newSink()
		u, _ := bundle.NewUnpacker(b.Pins, s)
		if _, err := u.Write(b.Tar[:cut]); err != nil {
			t.Fatal(err)
		}
		off := u.Resume()
		if off > int64(cut) || off%512 != 0 {
			t.Fatalf("cut %d: resume at %d", cut, off)
		}
		if _, err := u.Write(b.Tar[off:]); err != nil {
			t.Fatalf("cut %d resume %d: %v", cut, off, err)
		}
		if err := u.Finish(); err != nil {
			t.Fatalf("cut %d: %v", cut, err)
		}
		s.check(t, b)
	}
	// cuts in the padding after a complete member (bundle.json and each
	// member with padding): its file goes too, the resume makes it again
	// (provision review MINOR-2)
	ends := []int{512 + len(b.Doc)}
	pos := 512 + (len(b.Doc)+511)/512*512
	for _, m := range b.DocV.Members() {
		ends = append(ends, pos+512+int(m.Size))
		pos += 512 + int(m.Size+511)/512*512
	}
	padCuts := 0
	for _, e := range ends {
		if e%512 == 0 {
			continue
		}
		padCuts++
		s := newSink()
		u, _ := bundle.NewUnpacker(b.Pins, s)
		if _, err := u.Write(b.Tar[:e+1]); err != nil {
			t.Fatal(err)
		}
		off := u.Resume()
		if _, err := u.Write(b.Tar[off:]); err != nil {
			t.Fatalf("a cut in the padding at %d, resume at %d: %v", e+1, off, err)
		}
		if err := u.Finish(); err != nil {
			t.Fatal(err)
		}
		s.check(t, b)
	}
	if padCuts < 3 {
		t.Fatalf("only %d padding cuts", padCuts)
	}
	// one byte of the chromium apk is wrong
	bad := append([]byte(nil), b.Tar...)
	i := bytes.Index(bad, bytes.Repeat([]byte("c"), 1000))
	bad[i+500] = 'x'
	s := newSink()
	u, _ := bundle.NewUnpacker(b.Pins, s)
	_, err := u.Write(bad)
	if !bundle.IsBundle(err) || !strings.Contains(err.Error(), "chromium") {
		t.Fatalf("a wrong member: %v", err)
	}
	for p := range s.files {
		if strings.Contains(p, "chromium") {
			t.Fatal("the wrong member's file was kept")
		}
	}
	if _, err := u.Write([]byte{0}); err == nil {
		t.Fatal("a broken unpacker took a byte")
	}
	off := u.Resume()
	if _, err := u.Write(b.Tar[off:]); err != nil {
		t.Fatal(err)
	}
	if err := u.Finish(); err != nil {
		t.Fatal(err)
	}
	s.check(t, b)
	// Reset starts again at 0
	u.Reset()
	s2 := newSink()
	*s = *s2
	if _, err := u.Write(b.Tar); err != nil || u.Finish() != nil {
		t.Fatalf("after Reset: %v", err)
	}
}

// TestUnpackHeaders: a member that is not the next line, or whose header is
// not the bundle's form (mode, uid, mtime, type, link, user name, a member
// after the last line, garbage in the end of the archive).
func TestUnpackHeaders(t *testing.T) {
	b := bundletest.Simple("0.2.0-rc10", true)
	// the header of the second member (the main index) starts here
	h := 512 + (len(b.Doc)+511)/512*512
	fix := func(blk []byte) {
		for i := 148; i < 156; i++ {
			blk[i] = ' '
		}
		var s int64
		for _, c := range blk {
			s += int64(c)
		}
		copy(blk[148:], []byte(strings.Repeat("0", 6-len(octal(s)))+octal(s)+"\x00"))
	}
	mutate := func(f func(blk []byte), want string) {
		t.Helper()
		x := append([]byte(nil), b.Tar...)
		f(x[h : h+512])
		fix(x[h : h+512])
		u, _ := bundle.NewUnpacker(b.Pins, newSink())
		_, err := u.Write(x)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("want %q, got %v", want, err)
		}
	}
	mutate(func(k []byte) { copy(k[100:], "0000755\x00") }, "mode 0755")
	mutate(func(k []byte) { copy(k[108:], "0000001\x00") }, "uid 1")
	mutate(func(k []byte) { copy(k[136:], "00000000001\x00") }, "mtime 1")
	mutate(func(k []byte) { k[156] = '2' }, "not a regular file")
	mutate(func(k []byte) { copy(k[157:], "x") }, "a link name")
	mutate(func(k []byte) { copy(k[265:], "root") }, "user or group name")
	mutate(func(k []byte) { k[0] = 'w' }, "not the next line")
	mutate(func(k []byte) { copy(k[124:], "00000000001\x00") }, "not the next line")
	// a bad checksum
	x := append([]byte(nil), b.Tar...)
	x[h+1]++
	u, _ := bundle.NewUnpacker(b.Pins, newSink())
	if _, err := u.Write(x); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("checksum: %v", err)
	}
	// garbage in the end of the archive
	x = append([]byte(nil), b.Tar...)
	x[len(x)-10] = 1
	u, _ = bundle.NewUnpacker(b.Pins, newSink())
	if _, err := u.Write(x); err == nil || !strings.Contains(err.Error(), "after the last line") {
		t.Fatalf("trailer: %v", err)
	}
	// a sink that fails is a Sink error, not a bundle error
	s := newSink()
	s.failAt = b.DocV.Packages[0].Path
	u, _ = bundle.NewUnpacker(b.Pins, s)
	_, err := u.Write(b.Tar)
	var be *bundle.Error
	if !errors.As(err, &be) || !be.Sink || bundle.IsBundle(err) {
		t.Fatalf("sink failure: %v", err)
	}
}

func octal(n int64) string {
	const d = "01234567"
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 8 {
		b = append([]byte{d[n%8]}, b...)
	}
	return string(b)
}

// TestDocRules: bundle.json's own rules (bundle.py check) and the profile's
// expectations.
func TestDocRules(t *testing.T) {
	b := bundletest.Simple("0.2.0-rc10", false)
	if _, err := bundle.ParseDoc(b.Doc, b.Pins.Expect); err != nil {
		t.Fatal(err)
	}
	if _, err := bundle.ParseDoc(b.Doc, nil); err != nil {
		t.Fatal(err)
	}
	edit := func(f func(d *bundle.Doc), want string) {
		t.Helper()
		d := b.DocV
		d.Packages = append([]bundle.Package(nil), d.Packages...)
		d.Indexes = append([]bundle.Index(nil), d.Indexes...)
		d.Base = append([]string(nil), d.Base...)
		f(&d)
		js, _ := jsonOf(d)
		if _, err := bundle.ParseDoc(js, b.Pins.Expect); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("want %q, got %v", want, err)
		}
	}
	edit(func(d *bundle.Doc) { d.Packages[0], d.Packages[1] = d.Packages[1], d.Packages[0] }, "not sorted")
	edit(func(d *bundle.Doc) { d.Packages[0].Path = "v3.24/main/aarch64/x-1.apk" }, "want")
	edit(func(d *bundle.Doc) { d.Base = append(d.Base, "zzz-"+d.Packages[0].Name) }, "base line")
	edit(func(d *bundle.Doc) { d.Base = append([]string{"zz=1"}, d.Base...) }, "not sorted")
	edit(func(d *bundle.Doc) {
		d.Base = append(d.Base, d.Packages[2].Name+"=1")
		sort.Strings(d.Base)
	}, "a package of the base")
	edit(func(d *bundle.Doc) { d.Indexes[0], d.Indexes[1] = d.Indexes[1], d.Indexes[0] }, "index 0")
	edit(func(d *bundle.Doc) { d.Release = "0.2.0-rc11" }, "profiles.json says")
	edit(func(d *bundle.Doc) { d.Kind = "x" }, "kind")
	edit(func(d *bundle.Doc) { d.Packages[0].Scripts = []string{"../x"} }, "script")
	edit(func(d *bundle.Doc) { d.Add = nil }, "no names to add")
	if _, err := bundle.ParseDoc(append(append([]byte(nil), b.Doc...), []byte("{}")...), nil); err == nil {
		t.Fatal("data after the object")
	}
	if _, err := bundle.ParseDoc([]byte(strings.Replace(string(b.Doc), `"schema"`, `"x": 1, "schema"`, 1)), nil); err == nil {
		t.Fatal("an unknown key")
	}
}

// TestInstalledSize: the I: fields of the bundle's own indexes.
func TestInstalledSize(t *testing.T) {
	b := bundletest.Make(bundletest.Options{Release: "r", Profile: "kiosk-ha", Base: []string{"musl=1"},
		Pkgs: []bundletest.Pkg{{Name: "a", Version: "1", Repo: "main", Data: []byte("a"), Installed: 1000},
			{Name: "b", Version: "2", Repo: "common", Data: []byte("bb"), Installed: 20}},
		LVA: []byte("lva"), Add: []string{"a"}, World: []string{"a"}})
	open := func(p string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b.Members[p])), nil }
	n, err := bundle.InstalledSize(&b.DocV, open)
	if err != nil || n != 1023 {
		t.Fatalf("%d %v", n, err)
	}
	d := b.DocV
	d.Packages = append([]bundle.Package(nil), d.Packages...)
	d.Packages[0].Version = "9"
	if _, err := bundle.InstalledSize(&d, open); err == nil {
		t.Fatal("a package that no index lists")
	}
}

// TestPythonBundle: a bundle that uc-pxx-linux bundle.py write_bundle made
// (testdata/py-bundle.tar, testdata/gen.py): its tar header bytes are
// Python's, not Go's.
func TestPythonBundle(t *testing.T) {
	tarb, err := os.ReadFile(filepath.Join("testdata", "py-bundle.tar"))
	if err != nil {
		t.Skip("no testdata/py-bundle.tar")
	}
	js, err := os.ReadFile(filepath.Join("testdata", "py-bundle.json"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := bundle.ParseDoc(js, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := bundle.Pins{Size: int64(len(tarb)), SHA256: bundletest.SHA(tarb), IndexSHA256: bundletest.SHA(js),
		Expect: &bundle.Expect{Release: d.Release, Profile: d.Profile, Alpine: d.Alpine, Packages: len(d.Packages), LVA: d.LVA != nil}}
	s := newSink()
	u, err := bundle.NewUnpacker(p, s)
	if err != nil {
		t.Fatal(err)
	}
	if err := feed(u, tarb, rand.New(rand.NewSource(3))); err != nil {
		t.Fatal(err)
	}
	if err := u.Finish(); err != nil {
		t.Fatal(err)
	}
	if len(s.files) != len(d.Members())+1 {
		t.Fatalf("%d files", len(s.files))
	}
	n, err := bundle.InstalledSize(d, func(p string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(s.files[p].Bytes())), nil })
	if err != nil || n <= 0 {
		t.Fatalf("installed size %d %v", n, err)
	}
}

func jsonOf(v any) ([]byte, error) { return json.Marshal(v) }
