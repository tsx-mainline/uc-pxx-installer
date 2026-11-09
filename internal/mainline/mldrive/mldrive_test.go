package mldrive_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uc-pxx-installer/internal/fatfs"
	"uc-pxx-installer/internal/fatfs/fattest"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/mainline/mldrive"
	"uc-pxx-installer/internal/mainline/mltest"
)

func newBundle() *mltest.Bundle {
	res := mltest.Fill(77, 5000)
	ch := mltest.Chain{Version: "1.0.0.94", BuildType: "userdebug", UBoot: strings.Repeat("1", 64), Trust: strings.Repeat("2", 64),
		Dtbo: strings.Repeat("3", 64), Vbmeta: strings.Repeat("4", 64), Resource: mltest.SHA(res)}
	return mltest.NewBundle("tok-drive", 9000, 64<<10, ch, res, 38029312)
}

func TestIdent(t *testing.T) {
	d, err := newBundle().Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if id, err := mldrive.Ident(d); err != nil || strings.Count(id, "ino=") != 3 {
		t.Fatalf("ident %q %v", id, err)
	}
	os.Remove(filepath.Join(d, mainline.RootfsFile))
	if _, err := mldrive.Ident(d); err == nil {
		t.Fatal("ident of a folder without rootfs.ext4")
	}
}

// driveImage is a FAT32 drive with the bundle in uc-pxx-mainline/.
func driveImage(b *mltest.Bundle, extra func(d *fattest.Dir)) []byte {
	im := fattest.New(fattest.Spec{Type: 32, Sectors: 70000, SecPerClus: 1, Label: "P10-INSTALL"})
	im.Root.File("README.txt", []byte("hello"))
	d := im.Root.Mkdir(mainline.Dir)
	d.File(mainline.ManifestFile, b.ManifestJSON())
	d.File(mainline.TemplateFile, b.Template)
	d.File(mainline.RootfsFile, b.Rootfs)
	if extra != nil {
		extra(d)
	}
	return im.B
}

func TestVerifyDrive(t *testing.T) {
	b := newBundle()
	img := driveImage(b, nil)
	fs, err := fatfs.Open(bytes.NewReader(img), int64(len(img)))
	if err != nil {
		t.Fatal(err)
	}
	src, err := mldrive.DriveSource(fs)
	if err != nil {
		t.Fatal(err)
	}
	p, err := mainline.Verify(src)
	if err != nil {
		t.Fatal(err)
	}
	if p.ManifestSHA256 != mltest.SHA(b.ManifestJSON()) || !bytes.Equal(p.TemplateBytes(), b.Template) {
		t.Fatal("drive payload")
	}
	// an extra file in the folder
	img = driveImage(b, func(d *fattest.Dir) { d.File("x.bin", []byte("x")) })
	fs, _ = fatfs.Open(bytes.NewReader(img), int64(len(img)))
	src, _ = mldrive.DriveSource(fs)
	if _, err := mainline.Verify(src); err == nil || !strings.Contains(err.Error(), "other entries") {
		t.Fatalf("extra file: %v", err)
	}
	// a drive without the folder
	im := fattest.New(fattest.Spec{Type: 32, Sectors: 70000, SecPerClus: 1})
	im.Root.File("fw.bin", []byte("x"))
	fs, _ = fatfs.Open(bytes.NewReader(im.B), int64(len(im.B)))
	src, _ = mldrive.DriveSource(fs)
	if _, err := mainline.Verify(src); err == nil || !strings.Contains(err.Error(), "no readable folder") {
		t.Fatalf("no folder: %v", err)
	}
	// a drive that was not ejected (FAT32 entry 1: clean-shutdown bit off)
	dirty := fattest.New(fattest.Spec{Type: 32, Sectors: 70000, SecPerClus: 1})
	dd := dirty.Root.Mkdir(mainline.Dir)
	dd.File(mainline.ManifestFile, b.ManifestJSON())
	dirty.SetFAT(1, dirty.FAT(1)&^0x08000000)
	fs, _ = fatfs.Open(bytes.NewReader(dirty.B), int64(len(dirty.B)))
	if _, err := mldrive.DriveSource(fs); err == nil || !strings.Contains(err.Error(), "not ejected cleanly") {
		t.Fatalf("dirty drive: %v", err)
	}
	// a damaged file system: the FAT copies differ
	dmg := fattest.New(fattest.Spec{Type: 32, Sectors: 70000, SecPerClus: 1})
	dm := dmg.Root.Mkdir(mainline.Dir)
	cl := dm.File(mainline.TemplateFile, b.Template)
	dmg.SetFATCopy(1, cl[3], 0)
	fs, _ = fatfs.Open(bytes.NewReader(dmg.B), int64(len(dmg.B)))
	if _, err := mldrive.DriveSource(fs); err == nil || !strings.Contains(err.Error(), "has errors") {
		t.Fatalf("damaged drive: %v", err)
	}
}

func TestTop(t *testing.T) {
	b := newBundle()
	top := func(img []byte) mldrive.TopFolder {
		t.Helper()
		fs, err := fatfs.Open(bytes.NewReader(img), int64(len(img)))
		if err != nil {
			t.Fatal(err)
		}
		tf, err := mldrive.Top(fs)
		if err != nil {
			t.Fatal(err)
		}
		return tf
	}
	if tf := top(driveImage(b, nil)); !tf.Bundle || len(tf.Bins) != 0 {
		t.Fatalf("bundle drive: %+v", tf)
	}
	// the firmware of the official install, and a .bin inside the folder (not in the top folder)
	im := fattest.New(fattest.Spec{Type: 32, Sectors: 70000, SecPerClus: 1})
	im.Root.File("fw.bin", []byte("x"))
	im.Root.File("._fw.bin", []byte("mac"))
	im.Root.Mkdir("other").File("y.bin", []byte("y"))
	if tf := top(im.B); tf.Bundle || len(tf.Bins) != 1 || tf.Bins[0] != "fw.bin" {
		t.Fatalf("firmware drive: %+v", tf)
	}
	// both, the folder name in other case; a file named like the folder is not the folder
	both := fattest.New(fattest.Spec{Type: 32, Sectors: 70000, SecPerClus: 1})
	both.Root.Mkdir("UC-PXX-MAINLINE")
	both.Root.File("FW.BIN", []byte("x"))
	if tf := top(both.B); !tf.Bundle || len(tf.Bins) != 1 {
		t.Fatalf("both: %+v", tf)
	}
	file := fattest.New(fattest.Spec{Type: 32, Sectors: 70000, SecPerClus: 1})
	file.Root.File(mainline.Dir, []byte("not a folder"))
	if tf := top(file.B); tf.Bundle {
		t.Fatalf("a file named %s: %+v", mainline.Dir, tf)
	}
	// TopOfImage reads a drive file
	p := filepath.Join(t.TempDir(), "drive.img")
	os.WriteFile(p, driveImage(b, nil), 0o644)
	if tf, err := mldrive.TopOfImage(p); err != nil || !tf.Bundle {
		t.Fatalf("TopOfImage: %+v %v", tf, err)
	}
	if _, err := mldrive.TopOfImage(filepath.Join(t.TempDir(), "none.img")); err == nil {
		t.Fatal("TopOfImage of a missing file")
	}
}
