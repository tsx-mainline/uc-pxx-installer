package blk_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"uc-pxx-installer/internal/blk"
	"uc-pxx-installer/internal/fakedev"
)

func TestFindAndRead(t *testing.T) {
	root := t.TempDir()
	parts := append([]fakedev.Part{}, fakedev.Layout...)
	parts[fakedev.PartNum(parts, "cfg")-1].Data = []byte("0123456789abcdef")
	dev, err := fakedev.New(root, parts)
	if err != nil {
		t.Fatal(err)
	}
	dev.WriteDisk(49156, []byte("sector-49156"))
	d, err := blk.Find(root)
	if err != nil {
		t.Fatal(err)
	}
	if d.Dev != "mmcblk0" || len(d.Parts) != len(parts) {
		t.Fatalf("disk %s with %d partitions", d.Dev, len(d.Parts))
	}
	if d.Parts[0].Name != "uboot_a" {
		t.Errorf("not sorted by start: %v", d.Parts[0])
	}
	p, err := d.Part("misc")
	if err != nil || p.Start != 49152 || p.Size != 8192 {
		t.Fatalf("misc %v %v", p, err)
	}
	b, err := d.ReadPart("cfg", 6, 6)
	if err != nil || string(b) != "6789ab" {
		t.Errorf("cfg read %q %v", b, err)
	}
	if _, err := d.ReadPart("cfg", 4096*512-2, 6); err == nil {
		t.Error("read past the end of cfg")
	}
	s, err := d.ReadSectors(49156, 1)
	if err != nil || !bytes.HasPrefix(s, []byte("sector-49156")) {
		t.Errorf("sector read %q %v", s[:12], err)
	}
	if _, err := d.Part("nothere"); err == nil {
		t.Error("missing partition found")
	}
}

func TestDuplicateName(t *testing.T) {
	root := t.TempDir()
	parts := append([]fakedev.Part{}, fakedev.Layout...)
	parts = append(parts, fakedev.Part{Name: "boot_a", Start: 99999999, Size: 8})
	if _, err := fakedev.New(root, parts); err != nil {
		t.Fatal(err)
	}
	d, err := blk.Find(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Part("boot_a"); err == nil {
		t.Error("two partitions named boot_a accepted")
	}
}

func TestNoEMMC(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "sys/class/block/mmcblk1"), 0o755)
	if _, err := blk.Find(root); err == nil {
		t.Error("a disk without the UC-P10 partitions accepted")
	}
}

// TestOpenIsReadOnly checks that the opened files refuse a write.
func TestOpenIsReadOnly(t *testing.T) {
	root := t.TempDir()
	if _, err := fakedev.New(root, fakedev.Layout); err != nil {
		t.Fatal(err)
	}
	d, _ := blk.Find(root)
	f, _, err := d.OpenPart("misc")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteAt([]byte("x"), 0); err == nil {
		t.Fatal("write through OpenPart worked")
	}
	g, err := d.OpenDisk()
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if _, err := g.WriteAt([]byte("x"), 0); err == nil {
		t.Fatal("write through OpenDisk worked")
	}
}
