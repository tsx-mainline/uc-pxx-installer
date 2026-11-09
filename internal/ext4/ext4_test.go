package ext4

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// mkfs builds an image from a folder with mke2fs -d. The test is skipped
// when mke2fs is not installed.
func mkfs(t *testing.T, opts []string, sizeKB int, fill func(dir string)) (*os.File, int64) {
	t.Helper()
	mk, err := exec.LookPath("mke2fs")
	if err != nil {
		for _, p := range []string{"/sbin/mke2fs", "/usr/sbin/mke2fs"} {
			if _, e := os.Stat(p); e == nil {
				mk, err = p, nil
			}
		}
	}
	if err != nil {
		t.Skip("mke2fs not installed")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	os.Mkdir(src, 0o755)
	fill(src)
	img := filepath.Join(dir, "fs.img")
	args := append([]string{"-q", "-F", "-L", "testlabel", "-d", src}, opts...)
	args = append(args, img, fmt.Sprintf("%dk", sizeKB))
	cmd := exec.Command(mk, args...)
	cmd.Env = append(os.Environ(), "E2FSPROGS_FAKE_TIME=1000000000")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mke2fs %v: %v\n%s", args, err, out)
	}
	f, err := os.Open(img)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	st, _ := f.Stat()
	return f, st.Size()
}

func put(t *testing.T, p string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + i/4096)
	}
	return b
}

func fillTree(t *testing.T) func(string) {
	return func(d string) {
		put(t, filepath.Join(d, "system/build.prop"), []byte("# begin\nro.build.version.incremental=210836\nro.build.type=userdebug\n"))
		put(t, filepath.Join(d, "etc/tsx/build-id"), []byte("v9-test-1\n"))
		put(t, filepath.Join(d, "big.bin"), pattern(300*1024+123))
		put(t, filepath.Join(d, "usr/lib/os-release"), []byte("ID=alpine\n"))
		os.Symlink("../usr/lib/os-release", filepath.Join(d, "etc/os-release"))
		os.Symlink("/usr/lib", filepath.Join(d, "libdir"))
		long := strings.Repeat("x", 100)
		os.MkdirAll(filepath.Join(d, "deep", long), 0o755)
		put(t, filepath.Join(d, "deep", long, "f"), []byte("deep"))
		os.Symlink(filepath.Join("/deep", long, "f"), filepath.Join(d, "slowlink"))
		// a directory with many entries becomes a hashed directory
		for i := 0; i < 600; i++ {
			put(t, filepath.Join(d, "many", fmt.Sprintf("entry-%04d", i)), []byte(fmt.Sprint(i)))
		}
		// a sparse file: a hole at the start
		f, _ := os.Create(filepath.Join(d, "sparse.bin"))
		f.WriteAt([]byte("end"), 200*1024)
		f.Close()
	}
}

func check(t *testing.T, fs *FS) {
	t.Helper()
	if fs.Label != "testlabel" {
		t.Errorf("label %q", fs.Label)
	}
	b, err := fs.ReadFile("/system/build.prop", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if p := Props(b); p["ro.build.version.incremental"] != "210836" || p["ro.build.type"] != "userdebug" {
		t.Errorf("props %v", p)
	}
	for path, want := range map[string]string{
		"/etc/tsx/build-id":  "v9-test-1\n",
		"etc/os-release":     "ID=alpine\n",
		"/libdir/os-release": "ID=alpine\n",
		"/slowlink":          "deep",
		"/many/entry-0599":   "599",
		"/many/entry-0000":   "0",
	} {
		got, err := fs.ReadFile(path, 4096)
		if err != nil || string(got) != want {
			t.Errorf("%s: %q, %v", path, got, err)
		}
	}
	big, err := fs.ReadFile("/big.bin", 1<<20)
	if err != nil || !bytes.Equal(big, pattern(300*1024+123)) {
		t.Errorf("big.bin: %d bytes, %v", len(big), err)
	}
	sp, err := fs.ReadFile("/sparse.bin", 1<<20)
	if err != nil || len(sp) != 200*1024+3 || string(sp[200*1024:]) != "end" || sp[0] != 0 {
		t.Errorf("sparse.bin: %d bytes, %v", len(sp), err)
	}
	if _, err := fs.ReadFile("/big.bin", 1000); err == nil {
		t.Error("size limit not applied")
	}
	if _, err := fs.ReadFile("/nothere", 1000); err == nil {
		t.Error("missing file found")
	}
	if _, err := fs.ReadFile("/system", 1000); err == nil {
		t.Error("a directory read as a file")
	}
}

func TestExt4Variants(t *testing.T) {
	for name, opts := range map[string][]string{
		"ext4-64bit-csum": {"-t", "ext4", "-b", "4096", "-O", "64bit,metadata_csum"},
		"ext4-android":    {"-t", "ext4", "-b", "4096", "-O", "^64bit,^metadata_csum,uninit_bg", "-I", "256"},
		"ext4-1k":         {"-t", "ext4", "-b", "1024"},
		"ext2-blockmap":   {"-t", "ext2", "-b", "1024"},
		"ext3-4k":         {"-t", "ext3", "-b", "4096"},
	} {
		t.Run(name, func(t *testing.T) {
			f, size := mkfs(t, opts, 16384, fillTree(t))
			fs, err := Open(f, size)
			if err != nil {
				t.Fatal(err)
			}
			check(t, fs)
		})
	}
}

func TestNotExt4(t *testing.T) {
	if _, err := Open(bytes.NewReader(make([]byte, 8192)), 8192); !errors.Is(err, ErrNotExt4) {
		t.Fatalf("got %v", err)
	}
}

// TestImage reads a real image when UC_PXX_EXT4_IMAGE names one, for example
// the system image of a firmware: UC_PXX_EXT4_IMAGE=05_system_fe10.img
// UC_PXX_EXT4_FILE=/system/build.prop.
func TestImage(t *testing.T) {
	img := os.Getenv("UC_PXX_EXT4_IMAGE")
	if img == "" {
		t.Skip("UC_PXX_EXT4_IMAGE not set")
	}
	f, err := os.Open(img)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()
	fs, err := Open(f, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	p := os.Getenv("UC_PXX_EXT4_FILE")
	b, err := fs.ReadFile(p, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("label %q, %s: %d bytes, incremental %q", fs.Label, p, len(b), Props(b)["ro.build.version.incremental"])
	if want := os.Getenv("UC_PXX_EXT4_SHA256"); want != "" {
		t.Logf("compare with debugfs by hand: %s", want)
	}
}

// rawInodeOffset is where inode ino is in the image (the arithmetic of
// FS.inode), for the tests that break one inode.
func rawInodeOffset(t *testing.T, fs *FS, img []byte, ino uint32) int64 {
	t.Helper()
	group := (ino - 1) / fs.inodesPer
	index := int64((ino - 1) % fs.inodesPer)
	desc := img[fs.gdtBlock*fs.blockSize+int64(group)*fs.descSize:]
	table := int64(binary.LittleEndian.Uint32(desc[0x8:]))
	if fs.descSize >= 64 {
		table |= int64(binary.LittleEndian.Uint32(desc[0x28:])) << 32
	}
	return table*fs.blockSize + index*fs.inodeSize
}

// TestHostileSize (review of 01b3841, finding 1): bit 31 of i_size_high
// makes the size negative; a size past the image is no size of a file of it.
// On a file, a directory and a symbolic link inode the read is an error,
// never a panic (make() or a slice with that size).
func TestHostileSize(t *testing.T) {
	f, size := mkfs(t, []string{"-t", "ext4", "-b", "4096", "-O", "^metadata_csum"}, 16384, fillTree(t))
	img, err := io.ReadAll(io.NewSectionReader(f, 0, size))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, broken, read string }{
		{"file", "/system/build.prop", "/system/build.prop"},
		{"directory", "/etc/tsx", "/etc/tsx/build-id"},
		{"short symbolic link", "/libdir", "/libdir/os-release"},
		{"long symbolic link", "/slowlink", "/slowlink"},
	} {
		for _, high := range []uint32{0x80000000, 0xffffffff, 1} { // negative twice; 4 GiB, past the 16 MiB image
			t.Run(fmt.Sprintf("%s %#x", c.name, high), func(t *testing.T) {
				fs, err := Open(bytes.NewReader(img), size)
				if err != nil {
					t.Fatal(err)
				}
				in, err := fs.resolveNoFollowLast(c.broken)
				if err != nil {
					t.Fatal(err)
				}
				b := append([]byte(nil), img...)
				off := rawInodeOffset(t, fs, b, in)
				binary.LittleEndian.PutUint32(b[off+0x6C:], high)
				fs2, err := Open(bytes.NewReader(b), size)
				if err != nil {
					t.Fatal(err)
				}
				if got, err := fs2.ReadFile(c.read, 1<<20); err == nil {
					t.Fatalf("read %q of an inode of size high %#x", got, high)
				}
				// the other files stay readable
				if got, err := fs2.ReadFile("/big.bin", 1<<20); err != nil || len(got) != 300*1024+123 {
					t.Fatalf("big.bin: %d %v", len(got), err)
				}
			})
		}
	}
}

// resolveNoFollowLast is the inode number of p, the last part not followed
// (tests only).
func (fs *FS) resolveNoFollowLast(p string) (uint32, error) {
	dir, base := path.Split(strings.TrimSuffix(p, "/"))
	d, err := fs.resolve(dir, 0)
	if err != nil {
		return 0, err
	}
	return fs.lookup(d, base)
}
