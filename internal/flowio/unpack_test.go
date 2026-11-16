package flowio

// The drive form of a compressed bundle (unpack.go, mainline-rc-10 R21/R22):
// the read of a schema-3 drive folder unpacks rootfs.ext4.zst once into the
// RAM folder DriveBundleDir, and the mainline children take that folder
// (--mainline DIR) in place of the drive. Every refusal leaves no folder and
// starts no child. The fixture is the step-1 receiver's (internal/xfer
// testdata: zstd -19 --zstd=wlog=23 of 3 MiB of numbered lines).

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"uc-pxx-installer/internal/drivestate"
	"uc-pxx-installer/internal/fatfs/fattest"
	"uc-pxx-installer/internal/flow"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/mainline/mltest"
)

func zstPlain(t *testing.T) (plain, packed []byte) {
	t.Helper()
	var b bytes.Buffer
	for i := 0; b.Len() < 3<<20; i++ {
		fmt.Fprintf(&b, "rootfs line %d: uc-pxx step-1 compressed fixture %x %d\n", i, (uint64(i)*2654435761)&0xffffffff, i%97)
	}
	plain = b.Bytes()[:3<<20]
	packed, err := os.ReadFile("../xfer/testdata/rootfs-text-3m.zst")
	if err != nil {
		t.Fatal(err)
	}
	return plain, packed
}

func hx(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// zstDrive is the drive of a compressed bundle: the bundle of mlBundle with
// schema 3, its root file system the fixture's text and rootfs_zst its
// packed form (edit: other values or other files).
func zstDrive(t *testing.T, g *rig, edit func(m map[string]any, d *fattest.Dir, packed []byte)) {
	plain, packed := zstPlain(t)
	{
		b := mlBundle()
		b.Manifest["schema"] = mainline.SchemaZst
		b.Manifest["rootfs"].(map[string]any)["size"] = len(plain)
		b.Manifest["rootfs"].(map[string]any)["sha256"] = hx(plain)
		b.Manifest["rootfs_zst"] = map[string]any{"file": mainline.RootfsZstFile, "size": len(packed), "sha256": hx(packed)}
		im := fattest.New(fattest.Spec{Type: 16, Sectors: 70000, SecPerClus: 4, Label: "UC-PXX-INST"})
		d := im.Root.Mkdir(mainline.Dir)
		if edit != nil {
			edit(b.Manifest, d, packed)
		}
		d.File(mainline.ManifestFile, b.ManifestJSON())
		d.File(mainline.TemplateFile, b.Template)
		d.File(mainline.RootfsZstFile, packed)
		if err := os.WriteFile(filepath.Join(g.root, drivestate.Image), im.B, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestDriveZstUnpack: a compressed drive bundle is unpacked into RAM, the
// folder holds the three files of the manifest (the unpacked root file
// system byte for byte), and both children take --mainline of that folder,
// not the drive.
func TestDriveZstUnpack(t *testing.T) {
	g, _ := newMLRig(t, mlD2, "exit 0", nil)
	zstDrive(t, g, nil)
	g.toMLInstall()
	g.end()
	dir := filepath.Join(g.root, DriveBundleDir)
	args := g.mlArgs()
	if len(args) != 2 {
		t.Fatalf("children %q", args)
	}
	for _, a := range args {
		if !strings.Contains(a, " --mainline "+dir+" ") || strings.Contains(a, "--fw-drive") {
			t.Fatalf("child %q", a)
		}
	}
	plain, _ := zstPlain(t)
	got, err := os.ReadFile(filepath.Join(dir, mainline.RootfsFile))
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("the unpacked rootfs: %v, %d bytes", err, len(got))
	}
	es, _ := os.ReadDir(dir)
	var names []string
	for _, e := range es {
		fi, _ := e.Info()
		if fi.Mode().Perm() != 0o400 {
			t.Fatalf("%s: mode %v", e.Name(), fi.Mode())
		}
		names = append(names, e.Name())
	}
	if strings.Join(names, " ") != "boot-template.img manifest.json rootfs.ext4" {
		t.Fatalf("the folder holds %q", names)
	}
	if log := strings.Join(g.r.log, "\n"); !strings.Contains(log, "drive: the compressed bundle (rootfs.ext4.zst) unpacked into RAM: "+dir) {
		t.Fatalf("no unpack line in the flow log:\n%s", log)
	}
}

// TestDriveZstRefusals: a refused compressed folder leaves no RAM folder and
// starts no child; the refusal names the cause.
func TestDriveZstRefusals(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(m map[string]any, d *fattest.Dir, packed []byte)
		ram  string // the meminfo of the unit ("": the host's)
		want string
	}{
		{"a changed byte", func(m map[string]any, d *fattest.Dir, packed []byte) { packed[len(packed)/2] ^= 1 },
			"", "The drive was refused: rootfs.ext4.zst: 358950 bytes"},
		{"the zst pin of other bytes", func(m map[string]any, d *fattest.Dir, packed []byte) {
			m["rootfs_zst"].(map[string]any)["sha256"] = strings.Repeat("0", 64)
		}, "", "copy the folder again"},
		{"a larger rootfs pin than the stream", func(m map[string]any, d *fattest.Dir, packed []byte) {
			m["rootfs"].(map[string]any)["size"] = 3<<20 + 512
		}, "", "unpacks to 3145728 bytes"},
		{"a smaller rootfs pin than the stream", func(m map[string]any, d *fattest.Dir, packed []byte) {
			m["rootfs"].(map[string]any)["size"] = 3<<20 - 512
		}, "", "more bytes than the manifest says"},
		{"rootfs.ext4 too", func(m map[string]any, d *fattest.Dir, packed []byte) { d.File(mainline.RootfsFile, []byte("x")) },
			"", "holds rootfs.ext4 and rootfs.ext4.zst"},
		{"an other file", func(m map[string]any, d *fattest.Dir, packed []byte) { d.File("notes.txt", []byte("x")) },
			"", "other entries (\"notes.txt\")"},
		{"a schema 2 manifest", func(m map[string]any, d *fattest.Dir, packed []byte) {
			m["schema"] = mainline.Schema
			delete(m, "rootfs_zst")
		}, "", "its manifest is of schema 2"},
		{"too little RAM", nil, "MemAvailable:      200000 kB\n", "the unpacked bundle needs 3 MiB and 256 MiB stay free"},
	} {
		t.Run(c.name, func(t *testing.T) {
			g, _ := newMLRig(t, mlD2, "exit 0", nil)
			zstDrive(t, g, c.edit)
			if c.ram != "" {
				proc := filepath.Join(filepath.Dir(g.root), "proc")
				os.MkdirAll(proc, 0o755)
				os.WriteFile(filepath.Join(proc, "meminfo"), []byte("MemTotal:  2000000 kB\n"+c.ram), 0o644)
				g.r.c.Proc = proc
			}
			g.r.Start()
			g.pump(func() bool { return g.m.Unit != nil }, 20*time.Second)
			g.key(flow.KeyNext)
			g.key(flow.KeyOK)
			g.key(flow.KeyOK)
			g.pump(func() bool { return strings.Contains(bodyText(g.m), "The drive was refused: ") }, 30*time.Second)
			if b := bodyText(g.m); !strings.Contains(b, c.want) || len(g.mlArgs()) != 0 {
				t.Fatalf("%s\n%q", b, g.mlArgs())
			}
			if _, err := os.Lstat(filepath.Join(g.root, DriveBundleDir)); !os.IsNotExist(err) {
				t.Fatalf("the RAM folder is there after a refusal: %v", err)
			}
		})
	}
}

// TestDriveZstFolderGoes: the RAM folder of an earlier read goes with the
// next read of the drive (here an official drive: the RAM is given back).
func TestDriveZstFolderGoes(t *testing.T) {
	g, _ := newMLRig(t, mlD2, "exit 0", nil)
	dir := filepath.Join(g.root, DriveBundleDir)
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, mainline.RootfsFile), []byte("old"), 0o400)
	g.r.st.DriveBundle = dir
	// an official drive: the earlier folder is removed by the read
	im := fattest.New(fattest.Spec{Type: 16, Sectors: 70000, SecPerClus: 4, Label: "UC-PXX-INST"})
	im.Root.File("fw.bin", []byte(strings.Repeat("firmware", 1000)))
	os.WriteFile(filepath.Join(g.root, drivestate.Image), im.B, 0o644)
	g.r.Start()
	g.pump(func() bool { return g.m.Unit != nil }, 20*time.Second)
	g.key(flow.KeyNext)
	g.key(flow.KeyOK)
	g.key(flow.KeyOK)
	g.pump(func() bool { return g.m.Drive != nil || strings.Contains(bodyText(g.m), "refused") }, 30*time.Second)
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("the earlier RAM folder is still there: %v", err)
	}
	if g.r.ramBundle() != "" {
		t.Fatalf("ramBundle %q", g.r.ramBundle())
	}
}

// TestUnpackDriveBundleGuards: the writer writes only a folder of the run
// folder, never the run folder itself or a kernel file system.
func TestUnpackDriveBundleGuards(t *testing.T) {
	base := filepath.Join(t.TempDir(), "run", "uc-pxx-inst")
	os.MkdirAll(base, 0o755)
	var m mainline.Manifest
	json.Unmarshal(mltest.NewBundle("t", 100, 4096, mltest.Chain{}, nil, 0).ManifestJSON(), &m)
	for _, dir := range []string{base, filepath.Dir(base), "/proc/x", "/tmp/x"} {
		if err := unpackDriveBundle(base, dir, nil, nil, nil, &m); err == nil || !strings.Contains(err.Error(), "is not a folder of the run folder") {
			t.Fatalf("%s: %v", dir, err)
		}
		if err := removeDriveBundle(base, dir); err == nil {
			t.Fatalf("remove %s: no error", dir)
		}
	}
}
