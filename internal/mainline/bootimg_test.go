package mainline_test

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/mainline/mltest"
)

// TestAssembleMatchesModel: Assemble of a template (mkimage.sh with an empty
// resource) and a resource is byte for byte the image that mkbootimg makes
// from the same parts with the resource (mltest.MkBootImg, a model built from
// the parts, not by patching), for resources that end on, before and after
// a page boundary.
func TestAssembleMatchesModel(t *testing.T) {
	for _, linux := range []int{1, 2047, 2048, 9000, 70001} {
		for _, rn := range []int{1, 20, 2047, 2048, 2049, 4096, 5000, 1<<20 + 3} {
			tok := "t" + strings.Repeat("x", linux%14)
			tmpl := mltest.Template(tok, linux, int64(linux))
			res := mltest.Fill(int64(rn), rn)
			want := mltest.Expected(tok, linux, int64(linux), res)
			got, err := mainline.Assemble(tmpl, res)
			if err != nil {
				t.Fatalf("linux %d res %d: %v", linux, rn, err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("linux %d res %d: the assembled image differs from the mkbootimg model (%d vs %d bytes)", linux, rn, len(got), len(want))
			}
			if _, err := mainline.CheckAssembled(got, tok, mltest.SHA(res)); err != nil {
				t.Fatal(err)
			}
			// the template itself is the model without a second part
			if !bytes.Equal(tmpl, mltest.MkBootImg(mltest.Shim(tok, linux, int64(linux)), mltest.EmptyGzip, nil, tok, mltest.Cmdline)) {
				t.Fatal("template model")
			}
		}
	}
}

func put32(b []byte, off int, v uint32) []byte {
	c := append([]byte(nil), b...)
	binary.LittleEndian.PutUint32(c[off:], v)
	return c
}

func TestTemplateRefusals(t *testing.T) {
	const tok = "tok-1"
	tmpl := mltest.Template(tok, 3000, 5)
	if _, err := mainline.ParseTemplate(tmpl); err != nil {
		t.Fatal(err)
	}
	shim := func(args string) []byte {
		k := mltest.Fill(5, mainline.ShimDTB)
		k = append(k, mltest.FDT(args)...)
		return append(k, mltest.Fill(6, 3000)...)
	}
	cases := []struct {
		name string
		b    []byte
		want string
	}{
		{"short", tmpl[:100], "shorter than one page"},
		{"magic", append([]byte("ANDROIX!"), tmpl[8:]...), "magic"},
		{"header version 1", put32(tmpl, 40, 1), "header version 1"},
		{"page size 4096", put32(tmpl, 36, 4096), "page size 4096"},
		{"os version", put32(tmpl, 44, 1<<11), "os_version"},
		{"not the default addresses", put32(tmpl, 20, 0x11000001), "default load addresses"},
		{"kernel addr below the offset", put32(tmpl, 12, 0x100), "below the default"},
		{"has a second part", mltest.Expected(tok, 3000, 5, []byte("res")), "second part already"},
		{"second addr without size", put32(tmpl, 28, mltest.Base+0xf00000), "second part already"},
		{"truncated", tmpl[:len(tmpl)-2048], "bytes, its header gives"},
		{"extended", append(append([]byte(nil), tmpl...), make([]byte, 2048)...), "bytes, its header gives"},
		{"header page junk", func() []byte { c := append([]byte(nil), tmpl...); c[2000] = 1; return c }(), "header page is not zero"},
		{"kernel padding junk", func() []byte {
			c := append([]byte(nil), tmpl...)
			k := binary.LittleEndian.Uint32(c[8:])
			c[2048+int(k)] = 1
			return c
		}(), "padding"},
		{"id wrong", func() []byte { c := append([]byte(nil), tmpl...); c[580] ^= 1; return c }(), "id field"},
		{"kernel changed (id stale)", func() []byte { c := append([]byte(nil), tmpl...); c[2048+100] ^= 1; return c }(), "id field"},
		{"name not a token", mltest.MkBootImg(mltest.Shim(tok, 3000, 5), mltest.EmptyGzip, nil, "a b", mltest.Cmdline), "not a build token"},
		{"name empty", mltest.MkBootImg(mltest.Shim(tok, 3000, 5), mltest.EmptyGzip, nil, "", mltest.Cmdline), "not a build token"},
		{"name trailing junk", func() []byte {
			b := mltest.MkBootImg(mltest.Shim(tok, 3000, 5), mltest.EmptyGzip, nil, tok, mltest.Cmdline)
			b[48+len(tok)+2] = 'x'
			return b
		}(), "one NUL-terminated"},
		{"ramdisk empty", mltest.MkBootImg(mltest.Shim(tok, 3000, 5), nil, nil, tok, mltest.Cmdline), "empty"},
		{"no device tree", mltest.MkBootImg(mltest.Fill(1, 70000), mltest.EmptyGzip, nil, tok, mltest.Cmdline), "no device tree magic"},
		{"kernel too small for the shim", mltest.MkBootImg(mltest.Fill(1, 1000), mltest.EmptyGzip, nil, tok, mltest.Cmdline), "too small"},
		{"token differs", mltest.MkBootImg(shim(mltest.Cmdline+" uc-pxx.id=other"), mltest.EmptyGzip, nil, tok, mltest.Cmdline), "is not the token"},
		{"no token on the command line", mltest.MkBootImg(shim(mltest.Cmdline), mltest.EmptyGzip, nil, tok, mltest.Cmdline), "0 uc-pxx.id="},
		{"two tokens", mltest.MkBootImg(shim(mltest.Cmdline+" uc-pxx.id=tok-1 uc-pxx.id=tok-1"), mltest.EmptyGzip, nil, tok, mltest.Cmdline), "2 uc-pxx.id="},
	}
	for _, c := range cases {
		_, err := mainline.ParseTemplate(c.b)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err %v, want %q", c.name, err, c.want)
		}
		if _, err := mainline.Assemble(c.b, []byte("res")); err == nil {
			t.Errorf("%s: Assemble accepted it", c.name)
		}
	}
	for _, n := range []int{0, mainline.MaxResource + 1} {
		if _, err := mainline.Assemble(tmpl, make([]byte, n)); err == nil || !strings.Contains(err.Error(), "resource of") {
			t.Errorf("resource of %d bytes: %v", n, err)
		}
	}
	img, err := mainline.Assemble(tmpl, []byte("resource"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, tok, res, want string
	}{
		{"other resource", tok, mltest.SHA([]byte("x")), "the resource has sha256"},
		{"other token", "tok-2", mltest.SHA([]byte("resource")), "want \"tok-2\""},
	} {
		if _, err := mainline.CheckAssembled(img, c.tok, c.res); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if _, err := mainline.CheckAssembled(tmpl, tok, mltest.SHA(nil)); err == nil {
		t.Error("CheckAssembled took a template")
	}
	if _, err := mainline.CheckAssembled(put32(img, 28, 0), tok, mltest.SHA([]byte("resource"))); err == nil {
		t.Error("CheckAssembled took a second_addr of 0")
	}
}

func TestFDTChecks(t *testing.T) {
	good := mltest.FDT("a uc-pxx.id=x")
	mk := func(dtb []byte) []byte {
		k := append(mltest.Fill(1, mainline.ShimDTB), dtb...)
		return mltest.MkBootImg(k, mltest.EmptyGzip, nil, "x", "")
	}
	if _, err := mainline.ParseTemplate(mk(good)); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		dtb  []byte
		want string
	}{
		{"two bootargs", mltest.FDT("a uc-pxx.id=x", [2]string{"bootargs", "b\x00"}), "2 /chosen/bootargs"},
		{"bootargs not one string", mltest.FDT("a uc-pxx.id=x\x00b"), "not one string"},
		{"total size too large", func() []byte { c := append([]byte(nil), good...); binary.BigEndian.PutUint32(c[4:], 1<<30); return c }(), "not plausible"},
		{"truncated", good[:len(good)-12], "not plausible"},
		{"bad token", func() []byte {
			c := append([]byte(nil), good...)
			off := binary.BigEndian.Uint32(c[8:])
			binary.BigEndian.PutUint32(c[off:], 7)
			return c
		}(), "bad token"},
	}
	for _, c := range cases {
		_, err := mainline.ParseTemplate(mk(c.dtb))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}

func TestReadResource(t *testing.T) {
	res := mltest.Fill(3, 7777)
	img := mltest.Expected("x", 100, 1, res)
	part := append(append(make([]byte, 4096), img...), make([]byte, 8192)...)
	r := bytes.NewReader(part)
	got, err := mainline.ReadResource(r, 4096, int64(len(img)+8192))
	if err != nil || !bytes.Equal(got, res) {
		t.Fatalf("%v", err)
	}
	for _, c := range []struct {
		name  string
		start int64
		size  int64
		b     []byte
		want  string
	}{
		{"no resource", 0, 1 << 20, mltest.Template("x", 100, 1), "second part of 0"},
		{"ends after the partition", 0, int64(len(img) - 2048), img, "ends after the partition"},
		{"tiny partition", 0, 100, img, "smaller than a header"},
		{"not a boot image", 0, 1 << 20, make([]byte, 1<<20), "magic"},
		{"page size 0", 0, 1 << 20, put32(img, 36, 0), "page size 0"},
		{"page size 3000", 0, 1 << 20, put32(img, 36, 3000), "page size 3000"},
		{"huge second", 0, 1 << 30, put32(img, 24, 1<<30), "second part of"},
	} {
		b := append(append([]byte(nil), c.b...), make([]byte, 1<<20)...)
		if _, err := mainline.ReadResource(bytes.NewReader(b), c.start, c.size); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}

// FuzzTemplate: no input panics ParseTemplate, Assemble or CheckAssembled,
// and an accepted template assembles to an image that checks out.
func FuzzTemplate(f *testing.F) {
	tmpl := mltest.Template("fz", 500, 2)
	f.Add(tmpl, []byte("res"), uint16(0), byte(0))
	f.Add(tmpl, []byte("r"), uint16(40), byte(1))
	f.Add(mltest.Expected("fz", 500, 2, []byte("x")), []byte("res"), uint16(9), byte(0x55))
	f.Fuzz(func(t *testing.T, b, res []byte, at uint16, x byte) {
		c := append([]byte(nil), b...)
		if len(c) > 0 {
			c[int(at)%len(c)] ^= x
		}
		mainline.ParseTemplate(c)
		mainline.CheckAssembled(c, "fz", mltest.SHA(res))
		out, err := mainline.Assemble(c, res)
		if err != nil {
			return
		}
		ti, err := mainline.ParseTemplate(c)
		if err != nil {
			t.Fatal("Assemble took a template that ParseTemplate refuses")
		}
		if _, err := mainline.CheckAssembled(out, ti.Token, mltest.SHA(res)); err != nil {
			t.Fatalf("the assembled image does not check out: %v", err)
		}
	})
}

// FuzzReadResource: no input panics ReadResource.
func FuzzReadResource(f *testing.F) {
	f.Add(mltest.Expected("x", 100, 1, []byte("res")), int64(1<<20))
	f.Fuzz(func(t *testing.T, b []byte, size int64) {
		if size < 0 || size > 1<<24 {
			size = int64(len(b))
		}
		mainline.ReadResource(bytes.NewReader(b), 0, size)
	})
}
