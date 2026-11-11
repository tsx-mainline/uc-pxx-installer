package ext4

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// the root file system fixtures of uc-pxx-step2 (internal/step2/testdata/mkroots.sh)
func fixtureFS(t *testing.T, name string) *FS {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "step2", "testdata", "roots", name+".img.gz"))
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
	fs, err := Open(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func TestReadDirNoFollow(t *testing.T) {
	const holds = "/var/lib/tsx/autoupdate/holds"
	for _, c := range []struct {
		img, path string
		names     string // joined by ","; "-" = not found
		err       string
	}{
		{"noholds", holds, "-", ""},
		{"noholds", "/var/lib/tsx/autoupdate", "", ""},
		{"noholds", "/var/lib", "tsx", ""},
		{"noholds", "/", "lost+found,etc,var", ""},
		{"empty", holds, ".step2.tmp", ""},
		{"step2", holds, "step2", ""},
		{"other", holds, "other", ""},
		{"novar", holds, "-", ""},
		{"holdsfile", holds, "", "/var/lib/tsx/autoupdate/holds is not a directory (a regular file)"},
		{"holdslink", holds, "", "/var/lib/tsx/autoupdate/holds is not a directory (a symbolic link)"},
		{"tsxlink", holds, "", "/var/lib/tsx is not a directory (a symbolic link)"},
		{"step2", "/etc/hostname", "", "/etc/hostname is not a directory (a regular file)"},
		{"step2", "var/lib/../lib/tsx/autoupdate/holds", "step2", ""},
	} {
		fs := fixtureFS(t, c.img)
		names, found, err := fs.ReadDirNoFollow(c.path, 100)
		switch {
		case c.err != "":
			if err == nil || err.Error() != c.err {
				t.Errorf("%s %s: error %v, want %q", c.img, c.path, err, c.err)
			}
		case err != nil:
			t.Errorf("%s %s: %v", c.img, c.path, err)
		case c.names == "-" && found:
			t.Errorf("%s %s: found %v", c.img, c.path, names)
		case c.names != "-" && (!found || strings.Join(names, ",") != c.names):
			t.Errorf("%s %s: %v %v, want %s", c.img, c.path, found, names, c.names)
		}
	}
	if _, _, err := fixtureFS(t, "noholds").ReadDirNoFollow("/", 2); err == nil || !strings.Contains(err.Error(), "more than 2 entries") {
		t.Errorf("max: %v", err)
	}
}
