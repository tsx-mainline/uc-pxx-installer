package mainline_test

import (
	"bytes"
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
