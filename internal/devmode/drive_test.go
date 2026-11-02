//go:build install && devtest

package devmode_test

// The drive rule of --fw-drive (drive.go; FAT-READER-REVIEW.md F1, user
// 2026-10-29): the lun stays empty and the drive file keeps its identity from
// the first read of the firmware to the end of the install. A change before
// the plan, between the plan and the writes, or during the writes must give a
// refusal (nothing written) or a stop at a step boundary with the target
// disabled and never armed.

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/device/devicetest"
	"uc-pxx-installer/internal/devmode"
	"uc-pxx-installer/internal/drivestate"
	"uc-pxx-installer/internal/fatfs/fattest"
	"uc-pxx-installer/internal/install"
)

// drive is a P10-INSTALL drive file in a fake unit, with its lun file.
type drive struct {
	t       testing.TB
	image   string
	lun     string
	dataOff int64 // first data byte of the FAT volume
}

// newDrive puts a FAT16 drive with the firmware (fragmented, as a host may
// leave it) into the unit at /run/p10inst/drive/drive.img, and an empty lun
// file (the host ejected the drive).
func newDrive(t testing.TB, u *unit, files map[string][]byte) *drive {
	t.Helper()
	total := 0
	for _, b := range files {
		total += len(b)
	}
	im := fattest.New(fattest.Spec{Type: 16, Sectors: 2*total/512 + 40000, SecPerClus: 4, Label: "P10-INSTALL"})
	for name, b := range files {
		cs := im.ClusterSize()
		cl := im.Alloc(2 * ((len(b) + cs - 1) / cs))
		var use []uint32
		for i := 0; i < len(cl); i += 2 {
			use = append(use, cl[i])
		}
		im.Root.FileAt(name, b, use)
	}
	d := &drive{t: t, image: u.root.Path(drivestate.Image), lun: u.root.Path(drivestate.LunFile), dataOff: int64(im.DataOff)}
	for _, p := range []string{d.image, d.lun} {
		os.MkdirAll(filepath.Dir(p), 0o755)
	}
	if err := os.WriteFile(d.image, im.B, 0o644); err != nil {
		t.Fatal(err)
	}
	d.eject()
	return d
}

func (d *drive) ident() string {
	id, err := drivestate.Stat(d.image)
	if err != nil {
		d.t.Fatal(err)
	}
	return id.String()
}

func (d *drive) eject()  { os.WriteFile(d.lun, []byte("\n"), 0o644) }
func (d *drive) attach() { os.WriteFile(d.lun, []byte(d.image+"\n"), 0o644) }

// touch changes only the times of the file (no byte changes).
func (d *drive) touch() {
	t := time.Now().Add(time.Hour)
	if err := os.Chtimes(d.image, t, t); err != nil {
		d.t.Fatal(err)
	}
}

// scribble changes bytes of every data cluster, as a host write would.
func (d *drive) scribble() {
	f, err := os.OpenFile(d.image, os.O_RDWR, 0)
	if err != nil {
		d.t.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()
	for off := d.dataOff + 100; off < st.Size(); off += 2048 {
		f.WriteAt([]byte{0xa5}, off)
	}
}

func (u *unit) driveOpts(keeper string, d *drive) devmode.Options {
	o := u.opts(keeper)
	o.FW, o.FWDrive, o.FWDriveExpect = "", d.image, d.ident()
	return o
}

// TestDriveRefusals: the drive rule refuses before anything, also before the
// eMMC is opened. No bootctl needed.
func TestDriveRefusals(t *testing.T) {
	u := newUnit(t, unitFwd, devicetest.Opts{}, nil, "")
	d := newDrive(t, u, map[string][]byte{"UC-P10 1.0.0.94.bin": u.fx.FW})
	link := filepath.Join(t.TempDir(), "drive-link.img")
	os.Symlink(d.image, link)
	for _, c := range []struct {
		name string
		edit func(o *devmode.Options)
		pre  func()
		want string
	}{
		{"the host has the drive", nil, d.attach, "attached to the computer again"},
		{"no lun file", nil, func() { os.Remove(d.lun) }, "the drive state cannot be read"},
		{"the file changed after the flow read it", nil, d.touch, "the drive file changed"},
		{"--fw and --fw-drive", func(o *devmode.Options) { o.FW = u.fw }, nil, "not both"},
		{"a bad identity", func(o *devmode.Options) { o.FWDriveExpect = "size=1" }, nil, "needs size, mtime"},
		{"another identity", func(o *devmode.Options) { o.FWDriveExpect = strings.Replace(o.FWDriveExpect, "size=", "size=1", 1) }, nil, "the drive file changed"},
		{"the drive is a symlink", func(o *devmode.Options) { o.FWDrive = link }, nil, "not a regular file"},
		{"the drive under /proc", func(o *devmode.Options) { o.FWDrive = "/proc/self/exe" }, nil, "is under /proc"},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := u.driveOpts("a", d)
			if c.edit != nil {
				c.edit(&o)
			}
			if c.pre != nil {
				c.pre()
			}
			for _, run := range []func() error{
				func() error { _, err := devmode.Plan(u.newEnv(), o, io.Discard); return err },
				func() error { _, _, err := u.install(o); return err },
			} {
				err := run()
				if err == nil || !device.IsRefusal(err) || !strings.Contains(err.Error(), c.want) || devmode.ExitCode(err) != 1 {
					t.Fatalf("err = %v, want a refusal with %q", err, c.want)
				}
			}
			// back to a good drive for the next case
			os.Remove(d.image)
			*d = *newDrive(t, u, map[string][]byte{"UC-P10 1.0.0.94.bin": u.fx.FW})
		})
	}
	if len(u.bc.Calls) != 0 {
		t.Fatalf("bootctl ran: %q", u.bc.Calls)
	}
	u.untouched()
}

// TestDriveInstall: from the drive, the plan has the id of the plan from
// the file, the install completes, and the transcript says that the drive
// stayed detached and unchanged.
func TestDriveInstall(t *testing.T) {
	bin, exe := bootctlBin(t)
	u := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe)
	want := u.plan("a")
	d := newDrive(t, u, map[string][]byte{"UC-P10 firmware 1.0.0.94.bin": u.fx.FW, "notes.txt": []byte("x")})
	o := u.driveOpts("a", d)
	var out bytes.Buffer
	p, err := devmode.Plan(u.newEnv(), o, &out)
	if err != nil {
		t.Fatalf("plan %v\n%s", err, out.String())
	}
	// the same plan as from the file, but for the firmware identity: the
	// drive file's (the drive rule's identity), not the .bin file's
	same := *p
	same.FirmwareIdent = want.FirmwareIdent
	same.Seal()
	if !strings.HasPrefix(p.FirmwareIdent, "drive size=") || same.ID != want.ID {
		t.Fatalf("plan id %s (identity %q), the file's plan %s", p.ID, p.FirmwareIdent, want.ID)
	}
	planFile := filepath.Join(t.TempDir(), "plan.json")
	writePlan(t, planFile, p)
	o.PlanFile = planFile
	_, outs, err := u.install(o)
	if err != nil {
		t.Fatalf("%v\n%s", err, outs)
	}
	u.checkDone(p)
	tr := mustRead(t, u.root.Path("/run/install.out"))
	if !strings.Contains(string(tr), "drive: detached and unchanged from the first read to the end") {
		t.Fatalf("transcript:\n%s", tr)
	}
}

// TestDriveChanged: the drive changes at each stage.
func TestDriveChanged(t *testing.T) {
	bin, exe := bootctlBin(t)
	t.Parallel()
	fwName := "UC-P10 1.0.0.94.bin"
	t.Run("before the plan (after the flow read it)", func(t *testing.T) {
		u := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe)
		d := newDrive(t, u, map[string][]byte{fwName: u.fx.FW})
		o := u.driveOpts("a", d)
		d.scribble()
		if _, _, err := u.install(o); err == nil || !device.IsRefusal(err) || !strings.Contains(err.Error(), "the drive file changed") {
			t.Fatalf("err = %v", err)
		}
		u.untouched()
	})
	for _, c := range []struct {
		name   string
		change func(d *drive)
		want   string
	}{
		{"between the plan and the writes: attached", func(d *drive) { d.attach() }, "attached to the computer again"},
		{"between the plan and the writes: touched", func(d *drive) { d.touch() }, "the drive file changed"},
		{"between the plan and the writes: bytes", func(d *drive) { d.scribble() }, "the drive file changed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			u := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe)
			d := newDrive(t, u, map[string][]byte{fwName: u.fx.FW})
			o := u.driveOpts("a", d)
			o.AfterConfirm = func() { c.change(d) }
			u.bc.Calls = nil
			_, out, err := u.install(o)
			if err == nil || !device.IsRefusal(err) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v\n%s", err, out)
			}
			for _, call := range u.bc.Calls {
				if call != "status" {
					t.Fatalf("bootctl wrote: %q", u.bc.Calls)
				}
			}
			u.untouched()
		})
	}
	// During the writes: after every primitive write (the disable, each
	// data write, the arm) the drive changes. The install must stop before
	// the next step at the latest; the target is then disabled and never
	// armed, the keeper boots; a change after the arm is only reported.
	for _, c := range []struct {
		name   string
		change func(d *drive)
	}{
		{"attached", func(d *drive) { d.attach() }},
		{"touched", func(d *drive) { d.touch() }},
		{"bytes", func(d *drive) { d.scribble() }},
	} {
		point(t, "during the writes: "+c.name, func(t *testing.T) {
			n, p0, bounds := countWrites(t, unitFwd, "a", bin, exe)
			var stops atomic.Int64
			t.Cleanup(func() {
				t.Logf("drive changed during the writes (%s): %d stops checked and resumed", c.name, stops.Load())
			})
			for _, k := range sweepPoints(t, n, bounds) {
				if k == 0 {
					continue
				}
				what := fmt.Sprintf("%s after write %d of %d", c.name, k, n)
				point(t, what, func(t *testing.T) {
					sweepList(t, c.name, what)
					u := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe)
					d := newDrive(t, u, map[string][]byte{fwName: u.fx.FW})
					o := u.driveOpts("a", d)
					o.MaxWrite = sweepWrite
					o.Hooks = &install.Hooks{CutAfter: -1, Observe: func(ev install.Event) error {
						if ev.Write == k {
							c.change(d)
						}
						return nil
					}}
					_, out, err := u.install(o)
					cs := u.cutCheck(p0, what)
					if k < n {
						if err == nil {
							t.Fatalf("%s: the install completed\n%s", what, out)
						}
						var st *devmode.Stop
						if !asStop(err, &st) || devmode.ExitCode(err) != 1 {
							t.Fatalf("%s: err %v, want a stop with exit 1", what, err)
						}
						if cs.Boots != "keeper" || cs.Target == "armed" || strings.Contains(cs.Target, "armed") {
							t.Fatalf("%s: %+v: the target must be disabled, never armed", what, cs)
						}
						if !strings.Contains(out, "STOP") {
							t.Fatalf("%s: no STOP line\n%s", what, out)
						}
						stops.Add(1)
					} else {
						// the last write is the arm: after it the drive is not read
						if err != nil || cs.Boots != "target" || !strings.Contains(out, "after the arm") {
							t.Fatalf("%s: err %v, %+v\n%s", what, err, cs, out)
						}
					}
					d.eject()
					u.resume(p0, cs, what)
				})
			}
		})
	}
}

func asStop(err error, st **devmode.Stop) bool {
	s, ok := err.(*devmode.Stop)
	if ok {
		*st = s
	}
	return ok
}
