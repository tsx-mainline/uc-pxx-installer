//go:build install && devtest

package devmode_test

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/device/devicetest"
	"uc-pxx-installer/internal/devmode"
	"uc-pxx-installer/internal/fatfs"
	"uc-pxx-installer/internal/fatfs/fattest"
)

// driveWith makes a P10-INSTALL drive (FAT16, 2 KiB clusters) that holds
// the given files, each in every other cluster (fragmented). The clusters in
// between stay allocated to no file, as lost clusters, which Check allows.
func driveWith(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	total := 0
	for _, b := range files {
		total += len(b)
	}
	sectors := 2*total/512 + 40000
	im := fattest.New(fattest.Spec{Type: 16, Sectors: sectors, SecPerClus: 4, Label: "P10-INSTALL"})
	im.Root.File("FILLER.DAT", make([]byte, 2048))
	for name, b := range files {
		cs := im.ClusterSize()
		n := (len(b) + cs - 1) / cs
		cl := im.Alloc(2 * n)
		var use []uint32
		for i := 0; i < len(cl); i += 2 {
			use = append(use, cl[i]) // every other cluster: n fragments
		}
		im.Root.FileAt(name, b, use)
	}
	return im.B
}

func fatSource(img []byte) func() (devmode.FWFile, error) {
	return func() (devmode.FWFile, error) {
		fs, err := fatfs.Open(bytes.NewReader(img), int64(len(img)))
		if err != nil {
			return nil, err
		}
		f, err := fatfs.FindFirmware(fs)
		if err != nil {
			return nil, err
		}
		return f, nil
	}
}

// TestDeviceFWSource: the firmware read in place on the drive gives the same
// plan (the same id) as the file, and the install from it completes.
func TestDeviceFWSource(t *testing.T) {
	bin, exe := bootctlBin(t)
	u := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe)
	want := u.plan("a")
	img := driveWith(t, map[string][]byte{"UC-P10 firmware 1.0.0.94.bin": u.fx.FW})
	o := u.opts("a")
	o.FW, o.FWSource = "P10-INSTALL drive", fatSource(img)
	var out bytes.Buffer
	p, err := devmode.Plan(u.newEnv(), o, &out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if p.ID != want.ID {
		t.Fatalf("plan id %s from the drive, %s from the file", p.ID, want.ID)
	}
	o.PlanFile = ""
	res, outs, err := u.install(o)
	if err != nil {
		t.Fatalf("%v\n%s", err, outs)
	}
	if res.Steps != len(p.Steps) {
		t.Fatalf("%d steps", res.Steps)
	}
	u.checkDone(p)
}

// TestDeviceFWSourceRefused: a refusal of the drive is a refusal of the
// device mode, before anything is written. No bootctl is needed.
func TestDeviceFWSourceRefused(t *testing.T) {
	u := newUnit(t, unitFwd, devicetest.Opts{}, nil, "")
	for _, c := range []struct {
		name  string
		files map[string][]byte
		want  string
	}{
		{"two .bin", map[string][]byte{"a.bin": u.fx.FW, "b.bin": []byte("x")}, "there are 2 firmware files"},
		{"no .bin", map[string][]byte{"notes.txt": []byte("x")}, "no firmware file"},
		{"a .bin that is not the firmware", map[string][]byte{"fw.bin": bytes.Repeat([]byte("x"), 100000)}, "firmware"},
	} {
		o := u.opts("a")
		o.FW, o.FWSource = "P10-INSTALL drive", fatSource(driveWith(t, c.files))
		_, err := devmode.Plan(u.newEnv(), o, io.Discard)
		if err == nil || !device.IsRefusal(err) || !strings.Contains(err.Error(), c.want) || devmode.ExitCode(err) != 1 {
			t.Fatalf("%s: err = %v", c.name, err)
		}
		if _, _, err := u.install(o); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: install: err = %v", c.name, err)
		}
	}
	if len(u.bc.Calls) != 0 {
		t.Fatalf("bootctl ran: %q", u.bc.Calls)
	}
	u.untouched()
}
