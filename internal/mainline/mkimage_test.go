package mainline_test

// The proof that Assemble is kernel/mkimage.sh --resource: the template and
// the resources of testdata/mkimage were made by the real script
// (testdata/mkimage-pair.sh; the provenance is in golden.json), and
// Assemble(template, resource) must give exactly the sha256 and size of the
// script's own image with that resource. With UC_PXX_MKIMAGE set to a
// kernel/mkimage.sh (and its tools on PATH), TestAssembleLiveMkimage runs the
// script again on new made-up inputs and compares byte for byte.

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/mainline/mltest"
)

func gunzip(t *testing.T, p string) []byte {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(z)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAssembleGoldenMkimage(t *testing.T) {
	var g struct {
		Token          string `json:"token"`
		TemplateSHA256 string `json:"template_sha256"`
		Expected       map[string]struct {
			Size   int    `json:"size"`
			SHA256 string `json:"sha256"`
		} `json:"expected"`
	}
	b, err := os.ReadFile("testdata/mkimage/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	tmpl := gunzip(t, "testdata/mkimage/template.gz")
	if mltest.SHA(tmpl) != g.TemplateSHA256 {
		t.Fatal("template.gz is not the golden template")
	}
	ti, err := mainline.ParseTemplate(tmpl)
	if err != nil || ti.Token != g.Token {
		t.Fatalf("the real template: %+v %v", ti, err)
	}
	if len(g.Expected) != 3 {
		t.Fatalf("%d golden images", len(g.Expected))
	}
	for name, want := range g.Expected {
		res := gunzip(t, "testdata/mkimage/res-"+name+".bin.gz")
		img, err := mainline.Assemble(tmpl, res)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(img) != want.Size || mltest.SHA(img) != want.SHA256 {
			t.Fatalf("%s: assembled %d bytes sha256 %s, kernel/mkimage.sh --resource made %d bytes %s", name, len(img),
				mltest.SHA(img), want.Size, want.SHA256)
		}
		if _, err := mainline.CheckAssembled(img, g.Token, mltest.SHA(res)); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %d bytes sha256 %s = kernel/mkimage.sh --resource res-%s.bin", name, len(img), want.SHA256, name)
	}
}

// TestAssembleLiveMkimage runs the real kernel/mkimage.sh (UC_PXX_MKIMAGE)
// with made-up inputs of several sizes and compares Assemble byte for byte.
func TestAssembleLiveMkimage(t *testing.T) {
	mk := os.Getenv("UC_PXX_MKIMAGE")
	if mk == "" {
		t.Skip("UC_PXX_MKIMAGE not set: the live comparison with kernel/mkimage.sh does not run (the golden one does)")
	}
	script, _ := filepath.Abs("testdata/mkimage-pair.sh")
	n := 0
	for i, c := range []struct {
		token string
		linux int
	}{{"a", 1 << 16}, {"live-test.2", 1<<20 + 13}, {"x_y-z.0123456", 300000}, {"tok", 2048}} {
		out := t.TempDir()
		cmd := exec.Command("bash", script, mk, out, c.token, fmt.Sprint(c.linux), fmt.Sprintf("live%d", i))
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("mkimage-pair.sh: %v\n%s", err, b)
		}
		tmpl, err := os.ReadFile(filepath.Join(out, "template.img"))
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range []string{"rsce", "page", "odd"} {
			res, _ := os.ReadFile(filepath.Join(out, "res-"+r+".bin"))
			want, err := os.ReadFile(filepath.Join(out, "expected-"+r+".img"))
			if err != nil {
				t.Fatal(err)
			}
			got, err := mainline.Assemble(tmpl, res)
			if err != nil {
				t.Fatalf("%s/%s: %v", c.token, r, err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("%s/%s: Assemble differs from kernel/mkimage.sh --resource (%d vs %d bytes)", c.token, r, len(got), len(want))
			}
			n++
		}
	}
	t.Logf("%d images of the real kernel/mkimage.sh --resource equal Assemble byte for byte", n)
}
