package fatmk

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"uc-pxx-installer/internal/fatfs"
)

func image(t *testing.T, size int64) (*os.File, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "drive.img")
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	return f, p
}

var opt = Options{Label: "P10-INSTALL", Time: time.Date(2026, 10, 29, 21, 30, 4, 0, time.UTC), VolumeID: 0x10293021}

// readBack opens the volume with package fatfs and checks every file.
func readBack(t *testing.T, p string, files []File) *fatfs.FS {
	t.Helper()
	fs, err := fatfs.OpenImage(p)
	if err != nil {
		t.Fatal(err)
	}
	if d := fs.Dirty(); len(d) > 0 {
		t.Fatalf("dirty: %v", d)
	}
	if pr := fs.Check(); len(pr) > 0 {
		t.Fatalf("check: %v", pr)
	}
	for _, f := range files {
		b, err := fs.ReadFile("/"+f.Path, 1<<30)
		if err != nil {
			t.Fatalf("%s: %v", f.Path, err)
		}
		if !bytes.Equal(b, f.Data) {
			t.Fatalf("%s: %d bytes back, want %d", f.Path, len(b), len(f.Data))
		}
	}
	return fs
}

// fsck runs fsck.fat -n when the host has it (dosfstools).
func fsck(t *testing.T, p string) {
	t.Helper()
	tool, err := exec.LookPath("fsck.fat")
	if err != nil {
		t.Log("no fsck.fat on this host: skipped")
		return
	}
	out, err := exec.Command(tool, "-n", "-v", p).CombinedOutput()
	if err != nil {
		t.Fatalf("fsck.fat -n: %v\n%s", err, out)
	}
	l := strings.Split(strings.TrimSpace(string(out)), "\n")
	t.Logf("fsck.fat -n: clean: %s", l[len(l)-1])
}

func TestBundleOnDriveSize(t *testing.T) {
	f, p := image(t, 1200<<20)
	files := []File{
		{"p10-install-0123456789ab-20261029T213004Z/plan.json", []byte(`{"id":"x"}` + "\n")},
		{"p10-install-0123456789ab-20261029T213004Z/install-0123456789ab.jsonl", bytes.Repeat([]byte(`{"step":1}`+"\n"), 5000)},
		{"p10-install-0123456789ab-20261029T213004Z/install-0123456789ab.out", []byte("RESULT: install done\n")},
		{"p10-install-0123456789ab-20261029T213004Z/empty.txt", nil},
		{"p10-install-0123456789ab-20261029T213004Z/sub/ab-sector.txt", []byte("00414230\n")},
		{"README.TXT", []byte("the log of the UC-P10 installer\n")},
	}
	if err := Make(f, 1200<<20, files, opt); err != nil {
		t.Fatal(err)
	}
	f.Close()
	fs := readBack(t, p, files)
	in := fs.Info()
	if in.Type != 32 || in.Label != "P10-INSTALL" || in.ClusterSize != 4096 || in.Bytes != 1200<<20 {
		t.Fatalf("%+v", in)
	}
	es, _ := fs.ReadDir("/")
	var names []string
	for _, e := range es {
		names = append(names, e.Name)
	}
	if strings.Join(names, ",") != "README.TXT,p10-install-0123456789ab-20261029T213004Z" {
		t.Fatalf("top folder %v", names)
	}
	// no firmware file: the drive does not look like a copied firmware
	if _, err := fatfs.FindFirmware(fs); err == nil || err.(*fatfs.Refusal).Reason != fatfs.NoFirmware {
		t.Fatalf("FindFirmware: %v", err)
	}
	fsck(t, p)
	st, _ := os.Stat(p)
	if st.Size() != 1200<<20 {
		t.Fatal("size changed")
	}
}

func TestShapes(t *testing.T) {
	big := bytes.Repeat([]byte("0123456789abcdef"), 1<<16) // 1 MiB
	var many []File
	for i := 0; i < 300; i++ { // a folder of many clusters of entries
		many = append(many, File{fmt.Sprintf("dir/a file with a long name %03d.txt", i), []byte(fmt.Sprint(i))})
	}
	for _, c := range []struct {
		name  string
		size  int64
		files []File
	}{
		{"empty drive", 1200 << 20, nil},
		{"the smallest FAT32 size", 33 << 20, []File{{"a.txt", []byte("a")}}},
		{"64 MiB", 64 << 20, []File{{"x/y/z/deep.txt", big}}},
		{"unicode names", 256 << 20, []File{{"日本語/ファイル.txt", []byte("x")}, {"😀.log", []byte("y")}, {"exactly13char", []byte("z")}}},
		{"same short names", 128 << 20, []File{{"longname-one.txt", []byte("1")}, {"longname-two.txt", []byte("2")}, {"LONGNA~1.TXT.x", []byte("3")}}},
		{"many entries", 128 << 20, many},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, p := image(t, c.size)
			if err := Make(f, c.size, c.files, opt); err != nil {
				t.Fatal(err)
			}
			f.Close()
			readBack(t, p, c.files)
			fsck(t, p)
		})
	}
}

func TestRefusals(t *testing.T) {
	for _, c := range []struct {
		name  string
		size  int64
		files []File
		o     Options
		want  string
	}{
		{"too small", 16 << 20, nil, opt, "too small"},
		{"bad label", 64 << 20, nil, Options{Label: "p10/install"}, "label"},
		{"no label", 64 << 20, nil, Options{}, "label"},
		{"a path with ..", 64 << 20, []File{{"../x", nil}}, opt, "bad path"},
		{"a control character", 64 << 20, []File{{"a\nb", nil}}, opt, "bad path"},
		{"twice", 64 << 20, []File{{"a", nil}, {"A", nil}}, opt, "twice"},
		{"a file and a folder", 64 << 20, []File{{"a", nil}, {"a/b", nil}}, opt, "twice"},
		{"too large", 64 << 20, []File{{"a", make([]byte, 300<<20)}}, opt, "too large"},
		{"more than the volume", 40 << 20, []File{{"a", make([]byte, 100<<20)}}, opt, "clusters"},
	} {
		w := &recorder{}
		err := Make(w, c.size, c.files, c.o)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
		if w.n != 0 {
			t.Errorf("%s: %d writes before the refusal", c.name, w.n)
		}
	}
}

type recorder struct{ n int }

func (r *recorder) WriteAt(b []byte, off int64) (int, error) { r.n++; return len(b), nil }
