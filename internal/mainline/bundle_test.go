package mainline_test

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/mainline/mltest"
)

func verifyDir(t *testing.T, d string) (*mainline.Payload, error) {
	t.Helper()
	src, err := mainline.DirSource(d)
	if err != nil {
		return nil, err
	}
	return mainline.Verify(src)
}

func TestVerifyDir(t *testing.T) {
	b := newBundle()
	d, err := b.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// a host's AppleDouble file and .DS_Store are ignored
	os.WriteFile(filepath.Join(d, "._rootfs.ext4"), []byte("mac"), 0o644)
	os.WriteFile(filepath.Join(d, ".DS_Store"), []byte("mac"), 0o644)
	p, err := verifyDir(t, d)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if !p.Verified() || p.Kind != mainline.Kind || p.Manifest.Token != "tok-test.1" || p.ManifestSHA256 != mltest.SHA(b.ManifestJSON()) ||
		!bytes.Equal(p.TemplateBytes(), b.Template) || !strings.HasPrefix(p.Source, "folder ") {
		t.Fatalf("%+v", p)
	}
	got := make([]byte, len(b.Rootfs))
	if _, err := p.RootfsReader().ReadAt(got, 0); err != nil || !bytes.Equal(got, b.Rootfs) {
		t.Fatal("rootfs reader")
	}
}

func TestVerifyRefusals(t *testing.T) {
	cases := []struct {
		name string
		f    func(t *testing.T, d string, b *mltest.Bundle)
		want string
	}{
		{"extra file", func(t *testing.T, d string, b *mltest.Bundle) {
			os.WriteFile(filepath.Join(d, "README.txt"), []byte("x"), 0o644)
		}, "other entries"},
		{"extra folder", func(t *testing.T, d string, b *mltest.Bundle) { os.Mkdir(filepath.Join(d, "more"), 0o755) }, "other entries"},
		{"symlink in place of a file", func(t *testing.T, d string, b *mltest.Bundle) {
			p := filepath.Join(d, mainline.RootfsFile)
			os.Rename(p, p+".real")
			os.Symlink(p+".real", p)
		}, "other entries"},
		{"no rootfs", func(t *testing.T, d string, b *mltest.Bundle) { os.Remove(filepath.Join(d, mainline.RootfsFile)) }, "has no rootfs.ext4"},
		{"no manifest", func(t *testing.T, d string, b *mltest.Bundle) { os.Remove(filepath.Join(d, mainline.ManifestFile)) }, "has no manifest.json"},
		{"two names of one file", func(t *testing.T, d string, b *mltest.Bundle) {
			os.WriteFile(filepath.Join(d, "ROOTFS.EXT4"), b.Rootfs, 0o644)
		}, "only one of them"},
		{"bad manifest", func(t *testing.T, d string, b *mltest.Bundle) {
			b.Manifest["model"] = "UC-P8"
			os.WriteFile(filepath.Join(d, mainline.ManifestFile), b.ManifestJSON(), 0o644)
		}, "only on a UC-P10"},
		{"empty manifest", func(t *testing.T, d string, b *mltest.Bundle) {
			os.WriteFile(filepath.Join(d, mainline.ManifestFile), nil, 0o644)
		}, "manifest.json: 0 bytes"},
		{"template size", func(t *testing.T, d string, b *mltest.Bundle) {
			os.WriteFile(filepath.Join(d, mainline.TemplateFile), append(b.Template, make([]byte, 2048)...), 0o644)
		}, "boot-template.img: "},
		{"template sha", func(t *testing.T, d string, b *mltest.Bundle) {
			c := append([]byte(nil), b.Template...)
			c[len(c)-1] ^= 1
			os.WriteFile(filepath.Join(d, mainline.TemplateFile), c, 0o644)
		}, "boot-template.img: sha256"},
		{"template token is not the manifest's", func(t *testing.T, d string, b *mltest.Bundle) {
			b.Manifest["token"] = "other"
			os.WriteFile(filepath.Join(d, mainline.ManifestFile), b.ManifestJSON(), 0o644)
		}, "token \"tok-test.1\", the manifest says \"other\""},
		{"template not a template", func(t *testing.T, d string, b *mltest.Bundle) {
			img := mltest.Expected("tok-test.1", 9000, 11, []byte("r"))
			b.Sub("boot_template")["size"], b.Sub("boot_template")["sha256"] = len(img), mltest.SHA(img)
			b.Sub("boot_assembled")["size"] = len(img) + 2048
			os.WriteFile(filepath.Join(d, mainline.ManifestFile), b.ManifestJSON(), 0o644)
			os.WriteFile(filepath.Join(d, mainline.TemplateFile), img, 0o644)
		}, "second part already"},
		{"rootfs size", func(t *testing.T, d string, b *mltest.Bundle) {
			os.WriteFile(filepath.Join(d, mainline.RootfsFile), b.Rootfs[:len(b.Rootfs)-512], 0o644)
		}, "rootfs.ext4: "},
		{"rootfs sha", func(t *testing.T, d string, b *mltest.Bundle) {
			c := append([]byte(nil), b.Rootfs...)
			c[5000] ^= 1
			os.WriteFile(filepath.Join(d, mainline.RootfsFile), c, 0o644)
		}, "rootfs.ext4: sha256"},
		{"rootfs label", func(t *testing.T, d string, b *mltest.Bundle) {
			rf := mltest.Rootfs(len(b.Rootfs), "other", 12)
			b.Sub("rootfs")["sha256"] = mltest.SHA(rf)
			os.WriteFile(filepath.Join(d, mainline.ManifestFile), b.ManifestJSON(), 0o644)
			os.WriteFile(filepath.Join(d, mainline.RootfsFile), rf, 0o644)
		}, "label is \"other\""},
		{"rootfs not ext4", func(t *testing.T, d string, b *mltest.Bundle) {
			rf := mltest.Fill(4, len(b.Rootfs))
			b.Sub("rootfs")["sha256"] = mltest.SHA(rf)
			os.WriteFile(filepath.Join(d, mainline.ManifestFile), b.ManifestJSON(), 0o644)
			os.WriteFile(filepath.Join(d, mainline.RootfsFile), rf, 0o644)
		}, "no ext2/3/4 superblock"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newBundle()
			d, err := b.Write(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			c.f(t, d, b)
			p, err := verifyDir(t, d)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v, want %q", err, c.want)
			}
			if p != nil {
				t.Fatal("a payload with the error")
			}
		})
	}
	for _, d := range []string{"/dev/shm", "/proc/self", "/sys/kernel"} {
		if _, err := mainline.DirSource(d); err == nil {
			t.Errorf("DirSource(%s) accepted", d)
		}
	}
	f := filepath.Join(t.TempDir(), "file")
	os.WriteFile(f, nil, 0o644)
	if _, err := mainline.DirSource(f); err == nil {
		t.Error("DirSource of a file")
	}
}

// TestReopen: the bundle of a plan opened again without a second hash of the
// root file system. Every other part of pass 1 runs again and must be the
// plan's; a changed root file system is not seen here (the executor hashes
// it while it writes it, install.TestMainlineReopenedRootfsChanged).
func TestReopen(t *testing.T) {
	b := newBundle()
	d, err := b.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := verifyDir(t, d)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if !p.RootfsHashed() {
		t.Fatal("Verify: the root file system is not marked hashed")
	}
	src, _ := mainline.DirSource(d)
	q, err := mainline.Reopen(src, p)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if !q.Verified() || q.RootfsHashed() || q.Pass1 != p.Pass1 || q.ManifestSHA256 != p.ManifestSHA256 ||
		q.Template != p.Template || q.Source != p.Source || !bytes.Equal(q.TemplateBytes(), p.TemplateBytes()) {
		t.Fatalf("reopened %+v", q)
	}
	// the JSON of the payload in a plan is the same
	if a, b := mustJSON(t, p), mustJSON(t, q); a != b {
		t.Fatalf("plan JSON differs:\n%s\n%s", a, b)
	}
	// a changed root file system of the same size: Reopen does not hash it
	c := append([]byte(nil), b.Rootfs...)
	c[len(c)-1] ^= 1
	os.WriteFile(filepath.Join(d, mainline.RootfsFile), c, 0o644)
	if r, err := mainline.Reopen(src, p); err != nil {
		t.Fatalf("reopen of a changed rootfs (same size): %v", err)
	} else {
		r.Close()
	}
	if _, err := mainline.Verify(src); err == nil || !strings.Contains(err.Error(), "rootfs.ext4: sha256") {
		t.Fatalf("Verify of the changed rootfs: %v", err)
	}
	os.WriteFile(filepath.Join(d, mainline.RootfsFile), b.Rootfs, 0o644)

	for _, c := range []struct {
		name string
		f    func(d string, w *mainline.Payload) *mainline.Payload
		want string
	}{
		{"no plan payload", func(string, *mainline.Payload) *mainline.Payload { return nil }, "the plan has no mainline payload"},
		{"another manifest", func(d string, w *mainline.Payload) *mainline.Payload {
			w.ManifestSHA256 = strings.Repeat("0", 64)
			return w
		}, "manifest.json: sha256"},
		{"another template", func(d string, w *mainline.Payload) *mainline.Payload {
			w.Template.Size++
			return w
		}, "boot-template.img: "},
		{"another source", func(d string, w *mainline.Payload) *mainline.Payload {
			w.Source = "folder /elsewhere"
			return w
		}, "the plan's is folder /elsewhere"},
		{"rootfs size", func(d string, w *mainline.Payload) *mainline.Payload {
			os.WriteFile(filepath.Join(d, mainline.RootfsFile), append(b.Rootfs, make([]byte, 512)...), 0o644)
			return w
		}, "rootfs.ext4: "},
		{"rootfs label", func(d string, w *mainline.Payload) *mainline.Payload {
			c := append([]byte(nil), b.Rootfs...)
			copy(c[1024+0x78:1024+0x88], "other\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00")
			os.WriteFile(filepath.Join(d, mainline.RootfsFile), c, 0o644)
			return w
		}, "the file system label"},
		{"template changed", func(d string, w *mainline.Payload) *mainline.Payload {
			c := append([]byte(nil), b.Template...)
			c[len(c)-1] ^= 1
			os.WriteFile(filepath.Join(d, mainline.TemplateFile), c, 0o644)
			return w
		}, "boot-template.img: sha256"},
		{"extra file", func(d string, w *mainline.Payload) *mainline.Payload {
			os.WriteFile(filepath.Join(d, "x"), nil, 0o644)
			return w
		}, "other entries"},
	} {
		t.Run(c.name, func(t *testing.T) {
			d2, err := b.Write(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			w := *p // the plan's payload, with this copy's source
			w.Source = "folder " + d2
			want := c.f(d2, &w)
			s2, _ := mainline.DirSource(d2)
			if _, err := mainline.Reopen(s2, want); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
		})
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestRootfsSize: the superblock's blocks count x block size must be the
// file size (finding 5 of the review of c0fb5c5).
func TestRootfsSize(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(sb []byte)
		want string
	}{
		{"blocks count too small", func(sb []byte) { binary.LittleEndian.PutUint32(sb[4:], binary.LittleEndian.Uint32(sb[4:])-1) }, "the superblock gives a file system of"},
		{"no blocks", func(sb []byte) { binary.LittleEndian.PutUint32(sb[4:], 0) }, "gives 0 blocks"},
		{"64-bit high part", func(sb []byte) {
			binary.LittleEndian.PutUint32(sb[0x60:], 0x80)
			binary.LittleEndian.PutUint32(sb[0x150:], 1)
		}, "gives"},
		{"bad block size", func(sb []byte) { binary.LittleEndian.PutUint32(sb[0x18:], 9) }, "block size"},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := newBundle()
			c.edit(b.Rootfs[1024:2048])
			b.Sub("rootfs")["sha256"] = mltest.SHA(b.Rootfs)
			d, _ := b.Write(t.TempDir())
			if _, err := verifyDir(t, d); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("%v, want %q", err, c.want)
			}
		})
	}
}
