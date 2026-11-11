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
	b := fixtureBytes(t, name)
	fs, err := Open(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func fixtureBytes(t *testing.T, name string) []byte {
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
	return b
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

// TestReadDirNoFollowStrict (MAINLINE-RC3-REVIEW C3-2): a malformed record
// in the hold folder is an error; the lenient parser (dirEntries) skips it.
func TestReadDirNoFollowStrict(t *testing.T) {
	const holds = "/var/lib/tsx/autoupdate/holds"
	img := fixtureBytes(t, "step2")
	// the hold folder's block: the one with the record "step2" (inode != 0,
	// name_len 5) after "." and ".."
	i := bytes.Index(img, []byte("\x05\x01step2"))
	if i < 6 {
		t.Fatal("no record step2 in the fixture")
	}
	rec := i - 6 // the record starts 6 bytes before name_len
	for _, c := range []struct {
		name string
		edit func(b []byte)
	}{
		{"rec_len 0", func(b []byte) { b[rec+4], b[rec+5] = 0, 0 }},
		{"rec_len 6", func(b []byte) { b[rec+4], b[rec+5] = 6, 0 }},
		{"rec_len not a multiple of 4", func(b []byte) { b[rec+4]++ }},
		{"rec_len past the block", func(b []byte) { b[rec+4], b[rec+5] = 0xfc, 0xff }},
		{"name_len past rec_len", func(b []byte) { b[rec+6] = 0xff }},
		{"a / in the name", func(b []byte) { b[rec+9] = '/' }},
	} {
		b := append([]byte(nil), img...)
		c.edit(b)
		fs, err := Open(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			t.Fatal(err)
		}
		if names, found, err := fs.ReadDirNoFollow(holds, 100); err == nil {
			t.Errorf("%s: no error (%v %v)", c.name, found, names)
		}
	}
	// control: the lenient parser of the same tree silently gives no "step2"
	b := append([]byte(nil), img...)
	b[rec+4], b[rec+5] = 6, 0
	fs, _ := Open(bytes.NewReader(b), int64(len(b)))
	in, err := fs.resolve(holds, 0)
	if err != nil {
		t.Fatal(err)
	}
	ents, err := fs.dirEntries(in)
	for _, e := range ents {
		if e.name == "step2" {
			t.Fatal("control: the lenient parser still saw step2")
		}
	}
	if err != nil {
		t.Fatalf("control: the lenient parser gave an error: %v", err)
	}
}
