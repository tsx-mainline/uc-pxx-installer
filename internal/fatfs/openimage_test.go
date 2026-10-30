package fatfs

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"uc-pxx-installer/internal/fatfs/fattest"
)

// TestOpenImage: a regular image file is read; a FIFO, a character device and
// a symlink are refused at once (review F3: the open of a FIFO blocked).
func TestOpenImage(t *testing.T) {
	d := t.TempDir()
	im := fattest.New(small(16))
	im.Root.File("fw.bin", pattern(1, 3000))
	img := filepath.Join(d, "drive.img")
	if err := os.WriteFile(img, im.B, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := OpenFirmware(img)
	if err != nil {
		t.Fatal(err)
	}
	checkContent(t, f, pattern(1, 3000))
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	fifo := filepath.Join(d, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(d, "link.img")
	if err := os.Symlink(img, link); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, path, want string }{
		{"a FIFO", fifo, "not a regular file"},
		{"a character device", "/dev/null", "not a regular file"},
		{"a symlink to an image", link, "not a regular file"},
		{"a directory", d, "not a regular file"},
		{"no file", filepath.Join(d, "none"), "no such file"},
	} {
		done := make(chan error, 1)
		go func() {
			fs, err := OpenImage(c.path)
			if err == nil {
				fs.Close()
			}
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("%s: err = %v, want %q", c.name, err, c.want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: OpenImage blocks", c.name)
		}
		if _, err := OpenFirmware(c.path); err == nil {
			t.Fatalf("%s: OpenFirmware: no error", c.name)
		}
	}
}
