package mainline_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"uc-pxx-installer/internal/ext4"
	"uc-pxx-installer/internal/mainline"
)

// FuzzBootControlReads (review of 01b3841, finding 1): the reads of the
// gate's pinned files (Gate.Check: ext4.Open, then ReadFile of each pin
// path) on a mutated root file system never panic. The seed is a small
// ext4 (mke2fs -d) that holds every pinned path, a directory, and symbolic
// links; without mke2fs only the made-up seeds run.
func FuzzBootControlReads(f *testing.F) {
	pins := mainline.EmbeddedBootControlPins()
	f.Add(make([]byte, 8192))
	if mk := mke2fsPath(); mk != "" {
		dir := f.TempDir()
		src := filepath.Join(dir, "root")
		for i, p := range pins {
			full := filepath.Join(src, p.Path)
			os.MkdirAll(filepath.Dir(full), 0o755)
			if i == 1 {
				os.Symlink("/usr/sbin/uc-pxx-bootctl", full) // a symbolic link on the way
				continue
			}
			os.WriteFile(full, bytes.Repeat([]byte{byte(i)}, 100*i+1), 0o755)
		}
		img := filepath.Join(dir, "rootfs.ext4")
		if out, err := exec.Command(mk, "-q", "-F", "-t", "ext4", "-b", "1024", "-O", "^metadata_csum,^has_journal", "-N", "64",
			"-L", mainline.RootfsLabel, "-d", src, img, "512k").CombinedOutput(); err != nil {
			f.Fatalf("mke2fs: %v\n%s", err, out)
		}
		b, _ := os.ReadFile(img)
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, img []byte) {
		fs, err := ext4.Open(bytes.NewReader(img), int64(len(img)))
		if err != nil {
			return
		}
		for _, p := range pins {
			fs.ReadFile(p.Path, 16<<20)
		}
	})
}

func mke2fsPath() string {
	if mk, err := exec.LookPath("mke2fs"); err == nil {
		return mk
	}
	for _, c := range []string{"/sbin/mke2fs", "/usr/sbin/mke2fs"} {
		if _, e := os.Stat(c); e == nil {
			return c
		}
	}
	return ""
}
