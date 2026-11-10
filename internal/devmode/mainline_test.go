//go:build install && devtest

package devmode_test

// The mainline device mode (mainline.go) against a fake unit (package
// devicetest): slot A holds old images (successful, priority 14), slot B the
// fixture's payload images as the keeper's boot chain (successful, priority
// 15, booting), the stand-in for unit 3 after the userdebug install. The
// bundle is a made-up one (package mltest; no vendor byte) in a folder
// (--mainline) or in the folder uc-pxx-mainline/ of a FAT drive file
// (--fw-drive). The A/B steps run the real uc-pxx-bootctl-inst r2
// (UC_PXX_BOOTCTL; skipped without it).

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/device/devicetest"
	"uc-pxx-installer/internal/devmode"
	"uc-pxx-installer/internal/drivestate"
	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/fatfs/fattest"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/keykeep"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/mainline/mltest"
	"uc-pxx-installer/internal/mlstash"
	"uc-pxx-installer/internal/plan"
)

// mlUnit3 is the unit-3-like start: A stock (successful, 14), B userdebug
// (the fixture's chain; successful, 15, booting).
var mlUnit3 = fakeemmc.Unit{AB: [2][3]byte{{14, 0, 1}, {15, 0, 1}}, A: "old", B: "new"}

const mlToken, mlLinux = "mltest-dev", 30000

// mlFixtureBundle is the made-up bundle whose required chain is the
// fixture's payload (the images in slot B) and whose assembled image uses
// the resource of the fixture's boot image.
func mlFixtureBundle(t testing.TB) (*mltest.Bundle, []byte) {
	fx := fixture
	res, err := mainline.ReadResource(bytes.NewReader(fx.New["boot"]), 0, int64(len(fx.New["boot"])))
	if err != nil {
		panic(err) // the fixture's own boot image
	}
	m := fx.Manifests[0]
	var cap int64
	for _, d := range m.AVB.Descriptors {
		if d.Partition == "boot" {
			cap = int64(d.ImageSize)
		}
	}
	ch := mltest.Chain{Version: m.Firmware.Version, BuildType: m.Build["ro.build.type"], UBoot: fx.ManifestPartSHA("uboot"),
		Trust: fx.ManifestPartSHA("trust"), Dtbo: fx.ManifestPartSHA("dtbo"), Vbmeta: fx.ManifestPartSHA("vbmeta"),
		Resource: mltest.SHA(res)}
	// contract v2: vbmeta_a is the patch of the keeper's vbmeta. cap is the
	// signed boot image_size; the patched one is 64 KiB more (it must be
	// larger than the signed one and fit the fixture's boot_a)
	ps := cap + 64<<10
	b := mltest.NewBundle(mlToken, mlLinux, 1<<20, ch, res, ps)
	if _, err := b.PatchVbmeta(fx.New["vbmeta"], ps); err != nil {
		panic(err) // the fixture's own signed vbmeta
	}
	return b, res
}

// mlu is a fake unit with a mainline bundle.
type mlu struct {
	*unit
	b        *mltest.Bundle
	res      []byte
	folder   string // the bundle folder (--mainline)
	drive    *drive // or the drive (--fw-drive)
	expected []byte // boot_a after the install
}

func newMLU(t testing.TB, u fakeemmc.Unit, onDrive bool, bin []byte, exe string) *mlu {
	un := newUnit(t, u, devicetest.Opts{}, bin, exe)
	b, res := mlFixtureBundle(t)
	m := &mlu{unit: un, b: b, res: res, expected: mltest.Expected(mlToken, mlLinux, 11, res)}
	// task 2: the first drive phase of this boot (the stash of the fixture's userdebug firmware and its key record)
	if _, err := devmode.MainlineStash(un.newEnv(), devmode.Options{Device: "/dev/mmcblk0", FW: un.fw, Keys: un.keys}, io.Discard); err != nil {
		t.Fatalf("mainline stash: %v", err)
	}
	un.before = un.image() // the stash writes nothing to the unit
	if onDrive {
		m.drive = mlDrive(t, un, b, nil)
	} else {
		d, err := b.Write(filepath.Join(un.root.Dir, "run/p10inst"))
		if err != nil {
			t.Fatal(err)
		}
		m.folder = d
	}
	return m
}

// mlDrive puts a FAT16 drive with the bundle folder into the unit, and an
// empty lun file (the host ejected the drive). extra adds to the top folder.
func mlDrive(t testing.TB, u *unit, b *mltest.Bundle, extra func(im *fattest.Image)) *drive {
	t.Helper()
	total := len(b.Template) + len(b.Rootfs) + 65536
	im := fattest.New(fattest.Spec{Type: 16, Sectors: 2*total/512 + 40000, SecPerClus: 4, Label: "P10-INSTALL"})
	dir := im.Root.Mkdir(mainline.Dir)
	dir.File(mainline.ManifestFile, b.ManifestJSON())
	dir.File(mainline.TemplateFile, b.Template)
	dir.File(mainline.RootfsFile, b.Rootfs)
	if extra != nil {
		extra(im)
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

func (m *mlu) mlOpts() devmode.MainlineOptions {
	o := m.opts("")
	o.FW, o.Keys = "", ""
	mo := devmode.MainlineOptions{Options: o, UnlockConfirmed: true, Gate: m.b.Gate()}
	if m.drive != nil {
		mo.FWDrive, mo.FWDriveExpect = m.drive.image, m.drive.ident()
	} else {
		mo.Mainline = m.folder
	}
	return mo
}

func (m *mlu) mlPlan(o devmode.MainlineOptions) (*plan.MainlinePlan, string, error) {
	var out bytes.Buffer
	var p *plan.MainlinePlan
	var err error
	m.lockRetry(func() error {
		out.Reset()
		p, err = devmode.PlanMainline(m.newEnv(), o, &out)
		return err
	})
	return p, out.String(), err
}

func (m *mlu) mlInstall(o devmode.MainlineOptions) (*install.Result, string, error) {
	var out bytes.Buffer
	var res *install.Result
	var err error
	m.lockRetry(func() error {
		out.Reset()
		res, err = devmode.InstallMainline(m.env, o, &out)
		return err
	})
	return res, out.String(), err
}

// mlDone checks a finished mainline install from the disk bytes.
func (m *mlu) mlDone(p *plan.MainlinePlan) {
	t := m.t
	t.Helper()
	after := m.image()
	if !sameOutside(after, m.before, &p.Plan) {
		t.Fatal("a byte outside slot A's write ranges and the A/B sector changed")
	}
	want, _ := p.AB.Final.Raw()
	if !bytes.Equal(after[plan.ABLBA*512:plan.ABLBA*512+32], want[:]) {
		t.Fatalf("final A/B %x, want %x", after[plan.ABLBA*512:plan.ABLBA*512+32], want)
	}
	if !bytes.Equal(after[plan.ABLBA*512+32:(plan.ABLBA+1)*512], m.start[32:]) {
		t.Fatal("A/B bytes 32..511 changed")
	}
	a, _ := avbab.Parse(after[plan.ABLBA*512:])
	if a.Pick() != 0 || a.Slots[0] != (avbab.Slot{Priority: 15, Tries: 7}) || !a.Slots[1].Keeper() {
		t.Fatalf("final A/B %v %v", a.Slots[0], a.Slots[1])
	}
	// task 2: the firmware key is kept: both record sectors hold the fixture key's record
	rec := keykeep.Encode(m.fx.Keys)
	if p.KeyKeep == nil || (p.KeyKeep.State != "keep" && p.KeyKeep.State != "kept") {
		t.Fatalf("key state %+v", p.KeyKeep)
	}
	for _, sec := range p.KeyKeep.Sectors {
		if !bytes.Equal(after[sec.LBA*512:(sec.LBA+1)*512], rec) {
			t.Fatalf("backup LBA %d does not hold the key record after the install", sec.LBA)
		}
	}
	tb, _ := gpt.Parse(bytes.NewReader(after), int64(len(after)))
	holds := func(name string, img []byte) {
		e, _ := tb.Find(name)
		if !bytes.Equal(after[e.Start*512:e.Start*512+int64(len(img))], img) {
			t.Fatalf("%s does not hold its image", name)
		}
	}
	for _, n := range []string{"uboot", "trust", "dtbo"} {
		holds(n+"_a", m.fx.New[n])
	}
	// contract v2: vbmeta_a is the patch of the keeper's vbmeta
	pv, _, err := mainline.PatchVbmeta(m.fx.New["vbmeta"], uint64(p.Chain.BootCap))
	if err != nil || bytes.Equal(pv, m.fx.New["vbmeta"]) {
		t.Fatalf("the patch of the fixture's vbmeta: %v", err)
	}
	holds("vbmeta_a", pv)
	holds("boot_a", m.expected)
	holds("system_a", m.b.Rootfs)
	for _, n := range []string{"vendor_a", "oem_a"} {
		e, _ := tb.Find(n)
		if !bytes.Equal(after[e.Start*512:(e.End+1)*512], m.before[e.Start*512:(e.End+1)*512]) {
			t.Fatalf("%s changed", n)
		}
	}
	cs, err := install.CheckCutState(bytes.NewReader(after), &p.Plan, m.start)
	if err != nil || cs.Boots != "target" || !cs.Checked {
		t.Fatalf("final state %+v: %v", cs, err)
	}
}

func TestMainlineDeviceInstall(t *testing.T) {
	bin, exe := bootctlBin(t)
	for _, onDrive := range []bool{false, true} {
		for _, withPlan := range []bool{false, true} {
			t.Run(fmt.Sprintf("drive %v, plan file %v", onDrive, withPlan), func(t *testing.T) {
				m := newMLU(t, mlUnit3, onDrive, bin, exe)
				o := m.mlOpts()
				o.JSONOut = filepath.Join(m.root.Dir, "run/ml-plan.json")
				p, out, err := m.mlPlan(o)
				if err != nil {
					t.Fatalf("%v\n%s", err, out)
				}
				if p.Kind != plan.MainlineKind || p.Mode != "device" || p.Target != "a" || p.Keeper != "b" || p.RAMImage == nil ||
					p.RAMImage.Token != fakeToken || !p.Unlock.Confirmed || p.Tool != devmode.MainlineVersion ||
					p.AB.Final.String() != "A{15,7,0} B{14,0,1}, boots next: A" {
					t.Fatalf("plan: kind %s mode %s target %s keeper %s final %s\n%s", p.Kind, p.Mode, p.Target, p.Keeper, p.AB.Final, out)
				}
				wantIdent := "folder "
				if onDrive {
					wantIdent = "drive "
				}
				if !strings.HasPrefix(p.FirmwareIdent, wantIdent) || !strings.Contains(out, "pass 1: check the mainline bundle") ||
					!strings.Contains(out, "plan id: "+p.ID) {
					t.Fatalf("plan ident %q\n%s", p.FirmwareIdent, out)
				}
				m.untouched()
				// the plan file reads back with its id
				if q, err := devmode.ReadMainlinePlanFile(o.JSONOut); err != nil || q.ID != p.ID {
					t.Fatalf("read the plan file: %v", err)
				}
				if withPlan {
					o.PlanFile = o.JSONOut
				}
				o.JSONOut = ""
				m.bc.Calls = nil
				m.env = m.newEnv()
				res, out, err := m.mlInstall(o)
				if err != nil {
					t.Fatalf("%v\n%s", err, out)
				}
				if res.Steps != len(p.Steps) || !strings.Contains(out, "re-check: the plan has the same id "+p.ID) ||
					!strings.Contains(out, "RESULT: install done") || !strings.Contains(out, "keeper slot B: all 8 partitions unchanged") {
					t.Fatalf("steps %d\n%s", res.Steps, out)
				}
				if withPlan && !strings.Contains(out, "the bundle is not hashed again") {
					t.Fatalf("no plan-file line\n%s", out)
				}
				if onDrive && !strings.Contains(out, "drive: detached and unchanged from the first read to the end") {
					t.Fatalf("no drive rule line\n%s", out)
				}
				checkLogToken(t, o.LogPath, p.ID)
				m.mlDone(p)
				var writes []string
				for _, call := range m.bc.Calls {
					if call != "status" {
						writes = append(writes, call)
					}
				}
				if strings.Join(writes, "|") != "disable --slot a|arm-trying --slot a --lower-keeper" {
					t.Fatalf("bootctl write calls %q", writes)
				}
				// a second install is refused: slot A boots next, B is not the slot that boots
				m.env = m.newEnv()
				o.PlanFile = ""
				if _, out, err := m.mlInstall(o); err == nil || !device.IsRefusal(err) || !strings.Contains(err.Error(), "keeper-boots") {
					t.Fatalf("second install: %v\n%s", err, out)
				}
			})
		}
	}
}

// TestMainlineDeviceRefusals: every refusal writes nothing and runs no A/B
// write of the tool. Most need no bootctl binary (the refusal comes first).
func TestMainlineDeviceRefusals(t *testing.T) {
	bin, exe := bootctlBin(t)
	for _, c := range []struct {
		name    string
		onDrive bool
		edit    func(t *testing.T, m *mlu, o *devmode.MainlineOptions)
		want    string
		install bool // the plan run passes; only the install refuses
	}{
		{"not unlocked", false, func(t *testing.T, m *mlu, o *devmode.MainlineOptions) { o.UnlockConfirmed = false }, "unlock", false},
		{"the release gate: no payload reviewed", false, func(t *testing.T, m *mlu, o *devmode.MainlineOptions) { o.Gate = nil }, "payload-reviewed", false},
		{"a firmware file too", false, func(t *testing.T, m *mlu, o *devmode.MainlineOptions) { o.FW = m.fw }, "takes no firmware file (--fw)", false},
		{"a key file", false, func(t *testing.T, m *mlu, o *devmode.MainlineOptions) { o.Keys = m.keys }, "takes no firmware key (--keys, --lib)", false},
		{"keeper a", false, func(t *testing.T, m *mlu, o *devmode.MainlineOptions) { o.Keeper = "a" }, "always keeps slot B", false},
		{"folder and drive", false, func(t *testing.T, m *mlu, o *devmode.MainlineOptions) {
			d := mlDrive(t, m.unit, m.b, nil)
			o.FWDrive = d.image
		}, "one of them", false},
		{"no bundle", false, func(t *testing.T, m *mlu, o *devmode.MainlineOptions) { o.Mainline = "" }, "one of them", false},
		{"folder under /dev", false, func(t *testing.T, m *mlu, o *devmode.MainlineOptions) { o.Mainline = "/dev/shm" }, "never read under /dev", false},
		{"folder with an extra file", false, func(t *testing.T, m *mlu, o *devmode.MainlineOptions) {
			os.WriteFile(filepath.Join(m.folder, "notes.txt"), []byte("x"), 0o644)
		}, "other entries", false},
		{"rootfs changed", false, func(t *testing.T, m *mlu, o *devmode.MainlineOptions) {
			c := append([]byte(nil), m.b.Rootfs...)
			c[9999] ^= 1
			os.WriteFile(filepath.Join(m.folder, mainline.RootfsFile), c, 0o644)
		}, "rootfs.ext4: sha256", false},
		{"drive with a firmware .bin too", true, func(t *testing.T, m *mlu, o *devmode.MainlineOptions) {
			d := mlDrive(t, m.unit, m.b, func(im *fattest.Image) { im.Root.File("fw.bin", []byte("x")) })
			o.FWDriveExpect = d.ident()
		}, "and a firmware file (fw.bin)", false},
		{"drive without the folder", true, func(t *testing.T, m *mlu, o *devmode.MainlineOptions) {
			d := newDrive(t, m.unit, map[string][]byte{"fw.bin": m.fx.FW})
			o.FWDriveExpect = d.ident()
		}, "has no folder uc-pxx-mainline", false},
		{"drive attached", true, func(t *testing.T, m *mlu, o *devmode.MainlineOptions) { m.drive.attach() }, "attached to the computer again", false},
		{"drive of another identity", true, func(t *testing.T, m *mlu, o *devmode.MainlineOptions) { m.drive.touch() }, "drive: ", false},
		{"an official plan file", false, func(t *testing.T, m *mlu, o *devmode.MainlineOptions) {
			pf := filepath.Join(m.root.Dir, "run/official.json")
			writePlan(t, pf, &plan.Plan{Schema: plan.Schema, ID: "x"})
			o.PlanFile = pf
		}, "not a mainline plan", true},
		{"both trying (the BCB reset shape): no keeper", false, nil, "slot B must be successful", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			u := mlUnit3
			if c.edit == nil {
				u = fakeemmc.Unit{AB: [2][3]byte{{15, 7, 0}, {15, 7, 0}}, A: "old", B: "new"}
			}
			m := newMLU(t, u, c.onDrive, bin, exe)
			o := m.mlOpts()
			if c.edit != nil {
				c.edit(t, m, &o)
			}
			m.bc.Calls = nil
			for _, run := range []string{"plan", "install"} {
				if run == "plan" && c.install {
					continue
				}
				var err error
				var out string
				if run == "plan" {
					_, out, err = m.mlPlan(o)
				} else {
					m.env = m.newEnv()
					_, out, err = m.mlInstall(o)
				}
				if err == nil || !device.IsRefusal(err) || !strings.Contains(err.Error(), c.want) || devmode.ExitCode(err) != 1 {
					t.Fatalf("%s: %v, want a refusal with %q\n%s", run, err, c.want, out)
				}
			}
			m.untouched()
			for _, call := range m.bc.Calls {
				if call != "status" {
					t.Fatalf("bootctl ran %q", call)
				}
			}
		})
	}
}

// TestMainlinePlanFiles: the plan kinds do not mix. A mainline plan file is
// refused by the official install (its id is not the id of the plan inside
// it), an edited mainline plan and a plan of another bundle are refused by
// the mainline install, and a plan file of a changed folder too.
func TestMainlinePlanFiles(t *testing.T) {
	bin, exe := bootctlBin(t)
	m := newMLU(t, mlUnit3, false, bin, exe)
	o := m.mlOpts()
	o.JSONOut = filepath.Join(m.root.Dir, "run/ml-plan.json")
	p, out, err := m.mlPlan(o)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	pf := o.JSONOut
	o.JSONOut = ""
	if k, err := devmode.PlanKind(pf); err != nil || k != plan.MainlineKind {
		t.Fatalf("PlanKind %q %v", k, err)
	}
	// the official install with the mainline plan file
	oo := m.opts("")
	oo.PlanFile = pf
	if _, out, err := m.install(oo); err == nil || !strings.Contains(err.Error(), "does not match its id") {
		t.Fatalf("official install of a mainline plan: %v\n%s", err, out)
	}
	m.untouched()
	// an edited mainline plan (the target)
	raw, _ := os.ReadFile(pf)
	ed := filepath.Join(m.root.Dir, "run/ml-edited.json")
	os.WriteFile(ed, bytes.Replace(raw, []byte(`"target": "a"`), []byte(`"target": "b"`), 1), 0o644)
	o.PlanFile = ed
	m.env = m.newEnv()
	if _, out, err := m.mlInstall(o); err == nil || !strings.Contains(err.Error(), "does not match its id") {
		t.Fatalf("edited plan: %v\n%s", err, out)
	}
	// a resealed plan with another value: the re-check gives another id
	q := *p
	q.Notes = append([]string{"another note"}, q.Notes...)
	q.Seal()
	other := filepath.Join(m.root.Dir, "run/ml-other.json")
	writeMLPlan(t, other, &q)
	o.PlanFile = other
	m.env = m.newEnv()
	if _, out, err := m.mlInstall(o); err == nil || !strings.Contains(err.Error(), "the plan now has id") {
		t.Fatalf("resealed plan: %v\n%s", err, out)
	}
	// the folder's files got another identity after the plan
	os.Chtimes(filepath.Join(m.folder, mainline.RootfsFile), m.fixedTime(), m.fixedTime())
	o.PlanFile = pf
	m.env = m.newEnv()
	if _, out, err := m.mlInstall(o); err == nil || !strings.Contains(err.Error(), "is not the one that pass 1 checked") {
		t.Fatalf("changed folder: %v\n%s", err, out)
	}
	// the plan of a dry run
	q = *p
	q.Mode = "dry-run"
	q.Seal()
	dr := filepath.Join(m.root.Dir, "run/ml-dry.json")
	writeMLPlan(t, dr, &q)
	if _, err := devmode.ReadMainlinePlanFile(dr); err == nil || !strings.Contains(err.Error(), "not a device plan") {
		t.Fatalf("dry-run plan: %v", err)
	}
	m.untouched()
}

func (m *mlu) fixedTime() time.Time { return time.Unix(1700000000, 0) }

func writeMLPlan(t testing.TB, path string, p *plan.MainlinePlan) {
	t.Helper()
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestMainlineDeviceGuard: the bundle rule before every step up to the arm.
// A change of the folder (or of the drive) after the disable stops the
// install before the next step: slot A disabled, never armed, the keeper B
// boots. A run again completes. A change after the arm is only reported.
func TestMainlineDeviceGuard(t *testing.T) {
	bin, exe := bootctlBin(t)
	for _, onDrive := range []bool{false, true} {
		t.Run(fmt.Sprintf("drive %v", onDrive), func(t *testing.T) {
			m := newMLU(t, mlUnit3, onDrive, bin, exe)
			p, out, err := m.mlPlan(m.mlOpts())
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			o := m.mlOpts()
			changed := false
			o.Hooks = &install.Hooks{CutAfter: -1, Observe: func(ev install.Event) error {
				if ev.Op == "ab" && !changed { // right after the disable
					changed = true
					if onDrive {
						m.drive.attach()
					} else {
						os.Chtimes(filepath.Join(m.folder, mainline.TemplateFile), m.fixedTime(), m.fixedTime())
					}
				}
				return nil
			}}
			_, out, err = m.mlInstall(o)
			var st *devmode.Stop
			if !errors.As(err, &st) || devmode.ExitCode(err) != 1 || !strings.Contains(err.Error(), "stopped before the step") {
				t.Fatalf("err %v\n%s", err, out)
			}
			cs := m.mlCut(p, "after the change")
			if cs.Target != "disabled" || cs.Boots != "keeper" {
				t.Fatalf("after the stop: %+v", cs)
			}
			// a run again (the drive detached; the folder as it is now) completes
			if onDrive {
				m.drive.eject()
			}
			m.env = m.newEnv()
			o = m.mlOpts()
			if _, out, err := m.mlInstall(o); err != nil {
				t.Fatalf("resume: %v\n%s", err, out)
			}
			m.mlDone(p)
		})
	}
}

// mlCut checks the disk after a stop or a cut: the power-cut rule, nothing
// outside slot A's write ranges and the A/B sector changed, the keeper B
// successful.
func (m *mlu) mlCut(p *plan.MainlinePlan, what string) install.CutState {
	m.t.Helper()
	img := m.image()
	cs, err := install.CheckCutState(bytes.NewReader(img), &p.Plan, m.start)
	if err != nil {
		m.t.Fatalf("%s: %v (A/B %s)", what, err, cs.AB)
	}
	if !sameOutside(img, m.before, &p.Plan) {
		m.t.Fatalf("%s: a byte outside slot A's write ranges changed", what)
	}
	a, _ := avbab.Parse(img[plan.ABLBA*512:])
	if !a.Slots[1].Keeper() {
		m.t.Fatalf("%s: the keeper B is not successful: %v", what, a.Slots[1])
	}
	return cs
}

// mlResume runs the mainline device mode again on a cut unit: it completes,
// or, when slot A boots next (after the arm), the plan is refused.
func (m *mlu) mlResume(p *plan.MainlinePlan, cs install.CutState, what string) {
	m.t.Helper()
	m.env = m.newEnv()
	if m.drive != nil {
		m.drive.eject()
	}
	o := m.mlOpts()
	_, out, err := m.mlInstall(o)
	if cs.Boots == "target" {
		if err == nil || !device.IsRefusal(err) {
			m.t.Fatalf("%s: slot A boots next, but the install ran again: %v\n%s", what, err, out)
		}
		return
	}
	if err != nil {
		m.t.Fatalf("%s: resume: %v\n%s", what, err, out)
	}
	m.mlDone(p)
}

func mlCountWrites(t *testing.T, sh fakeemmc.Unit, bin []byte, exe string) (int, *plan.MainlinePlan, []int) {
	m := newMLU(t, sh, false, bin, exe)
	p, out, err := m.mlPlan(m.mlOpts())
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	o := m.mlOpts()
	o.MaxWrite = sweepWrite
	n, step := 0, -1
	var bounds []int
	o.Hooks = &install.Hooks{CutAfter: -1, Observe: func(ev install.Event) error {
		if ev.Step != step {
			bounds = append(bounds, n, ev.Write-1)
			step = ev.Step
		}
		if ev.Op == "ab" {
			bounds = append(bounds, ev.Write-1, ev.Write)
		}
		n = ev.Write
		return nil
	}}
	if _, out, err := m.mlInstall(o); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return n, p, append(bounds, n)
}

var mlSweepShapes = []struct {
	name string
	unit fakeemmc.Unit
}{
	{"unit 3: A stock 14, keeper B userdebug 15", mlUnit3},
	{"A disabled (a stopped install), keeper B 15", fakeemmc.Unit{AB: [2][3]byte{{0, 0, 0}, {15, 0, 1}}, A: "old", B: "new"}},
}

// TestMainlineDevicePowerCutSweep cuts the mainline device-mode install
// before every primitive write (clean and torn), with no clean-up. After
// each cut: the power-cut rule, nothing else changed, the keeper B
// successful; then the device mode runs again and completes (or, after the
// arm, refuses: slot A boots next).
func TestMainlineDevicePowerCutSweep(t *testing.T) {
	bin, exe := bootctlBin(t)
	t.Parallel()
	var total atomic.Int64
	t.Cleanup(func() {
		t.Logf("mainline device-mode power-cut sweep: %d cut points checked, each resumed", total.Load())
	})
	for _, sh := range mlSweepShapes {
		point(t, sh.name, func(t *testing.T) {
			n, p0, bounds := mlCountWrites(t, sh.unit, bin, exe)
			for _, torn := range []bool{false, true} {
				for _, k := range sweepPoints(t, n, bounds) {
					what := fmt.Sprintf("cut before write %d (torn %v)", k+1, torn)
					point(t, what, func(t *testing.T) {
						sweepList(t, sh.name, what)
						m := newMLU(t, sh.unit, false, bin, exe)
						o := m.mlOpts()
						o.MaxWrite = sweepWrite
						o.Hooks = &install.Hooks{CutAfter: k, Torn: torn}
						_, out, err := m.mlInstall(o)
						if k < n && !errors.Is(err, install.ErrPowerCut) {
							t.Fatalf("%s: err %v, want a power cut\n%s", what, err, out)
						}
						if k == n && err != nil {
							t.Fatalf("no cut: %v\n%s", err, out)
						}
						cs := m.mlCut(p0, what)
						if k < n && cs.Boots != "keeper" {
							t.Fatalf("%s: %+v: only the last write may make slot A the next boot", what, cs)
						}
						m.mlResume(p0, cs, what)
						total.Add(1)
					})
				}
			}
		})
	}
}

// The kill sweep runs the mainline device mode in a child process (this
// test binary, started by init before TestMain) that SIGKILLs itself right
// after primitive write k.
const mlChildEnv = "P10INST_MAINLINE_CHILD"

func init() {
	if os.Getenv(mlChildEnv) != "" {
		os.Exit(mlChild())
	}
}

func mlChild() int {
	f := strings.Split(os.Getenv(mlChildEnv), "|")
	root, folder := f[0], f[1]
	k, _ := strconv.Atoi(f[2])
	r := devicetest.Reopen(root, small)
	bc := &devicetest.Bootctl{Root: r, Exec: os.Getenv("UC_PXX_BOOTCTL_EXEC"), Tmp: filepath.Dir(root)}
	e, err := device.NewForTest(device.TestOptions{Root: root, RegularFiles: true, Layout: small, Runner: bc,
		Manifests: fixture.Manifests})
	if err != nil {
		fmt.Println(err)
		return 2
	}
	o := devmode.MainlineOptions{Options: devmode.Options{Device: device.EMMCPath, PrereqsConfirmed: true, PanelConfirmed: true, Yes: true,
		MaxWrite: sweepWrite, LogPath: filepath.Join(root, "run/install.jsonl")}, Mainline: folder, UnlockConfirmed: true}
	b, _ := mlFixtureBundle(nil)
	o.Gate = b.Gate()
	o.Hooks = &install.Hooks{CutAfter: -1, Observe: func(ev install.Event) error {
		if ev.Write == k {
			syscall.Kill(os.Getpid(), syscall.SIGKILL)
			select {}
		}
		return nil
	}}
	var out bytes.Buffer
	_, err = devmode.InstallMainline(e, o, &out)
	fmt.Print(out.String())
	if err != nil {
		fmt.Println(err)
		return 1
	}
	return 0
}

func TestMainlineDeviceKillSweep(t *testing.T) {
	bin, exe := bootctlBin(t)
	t.Parallel()
	var total atomic.Int64
	t.Cleanup(func() {
		t.Logf("mainline device-mode kill sweep: %d kill points checked, each resumed", total.Load())
	})
	sh := mlSweepShapes[0]
	n, p0, bounds := mlCountWrites(t, sh.unit, bin, exe)
	for _, k := range sweepPoints(t, n, bounds) {
		if k == 0 {
			continue
		}
		what := fmt.Sprintf("SIGKILL after write %d", k)
		point(t, what, func(t *testing.T) {
			sweepList(t, sh.name, what)
			m := newMLU(t, sh.unit, false, bin, exe)
			cmd := exec.Command(os.Args[0], "-test.run=^$")
			cmd.Env = append(os.Environ(), mlChildEnv+"="+strings.Join([]string{m.root.Dir, m.folder, strconv.Itoa(k)}, "|"))
			out, err := cmd.CombinedOutput()
			var ee *exec.ExitError
			killed := errors.As(err, &ee) && ee.Sys().(syscall.WaitStatus).Signaled() &&
				ee.Sys().(syscall.WaitStatus).Signal() == syscall.SIGKILL
			if !killed {
				t.Fatalf("%s: the child was not killed: %v\n%s", what, err, out)
			}
			cs := m.mlCut(p0, what)
			if k < n && cs.Boots != "keeper" {
				t.Fatalf("%s: %+v", what, cs)
			}
			m.mlResume(p0, cs, what)
			total.Add(1)
		})
	}
}

// TestMainlineBootctlMisbehaves: the exit codes of the mainline device mode
// are the official ones. A known A/B state after a failed bootctl run is exit
// 1 with "safe to power off"; an unknown one is exit 3 with the hold text
// only ("do not power off"). A refused arm leaves slot A disabled.
func TestMainlineBootctlMisbehaves(t *testing.T) {
	bin, exe := bootctlBin(t)
	for _, c := range []struct {
		mode, want string
		code       int
		target     string // after a known stop
	}{
		{"refuse", "exit 1 (refused, the A/B sector is verified unchanged)", 1, "as before"},
		{"unknown", "ended with exit 3; the installer read the A/B sector back: it is unchanged", 1, "as before"},
		{"unknown-wrote", "it is the plan's next state", 1, "disabled"},
		{"signal-wrote", "was ended by the signal interrupt", 1, "disabled"},
		{"garbage", "A/B STATE UNKNOWN", 3, ""},
		{"lie", "A/B STATE UNKNOWN", 3, ""},
		{"wrong", "A/B STATE UNKNOWN", 3, ""},
		{"tail", "A/B STATE UNKNOWN", 3, ""},
		{"arm-refuse", "exit 1 (refused", 1, "disabled"},
	} {
		t.Run(c.mode, func(t *testing.T) {
			m := newMLU(t, mlUnit3, false, bin, exe)
			p, out, err := m.mlPlan(m.mlOpts())
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			m.bc.Mode = c.mode
			m.env = m.newEnv()
			_, out, err = m.mlInstall(m.mlOpts())
			if err == nil || !strings.Contains(err.Error(), c.want) || devmode.ExitCode(err) != c.code {
				t.Fatalf("err = %v (exit %d)\n%s", err, devmode.ExitCode(err), out)
			}
			hold := strings.Contains(out, "DO NOT POWER OFF AND DO NOT RESTART")
			safe := strings.Contains(out, "safe to power off")
			if c.code == 3 && (!hold || safe) || c.code == 1 && (hold || c.target == "disabled" && !safe) {
				t.Fatalf("exit %d: hold text %v, safe text %v\n%s", c.code, hold, safe, out)
			}
			if c.code == 1 {
				cs := m.mlCut(p, c.mode)
				if cs.Boots != "keeper" || cs.Target != c.target {
					t.Fatalf("after the stop: %+v", cs)
				}
			}
		})
	}
}

// TestMainlineStash (task 2): the first drive phase writes only the stash of
// this boot (nothing on the unit); a second stash, a stash of another boot,
// a removed stash and a keeper option are refused; without the stash the
// mainline plan refuses (the key cannot be kept).
func TestMainlineStash(t *testing.T) {
	bin, exe := bootctlBin(t)
	m := newMLU(t, mlUnit3, false, bin, exe)
	if after := m.image(); !bytes.Equal(after, m.before) {
		t.Fatal("the stash changed the unit")
	}
	dir := m.root.Path(mlstash.Dir)
	l, err := mlstash.Load(dir)
	if err != nil || l.Firmware.SHA256 != m.fx.Manifests[0].Firmware.SHA256 || l.Key.RecordSHA256 != keykeep.SHA256(keykeep.Encode(m.fx.Keys)) {
		t.Fatalf("stash %+v %v", l, err)
	}
	for _, p := range []string{"uboot", "trust", "dtbo", "vbmeta"} {
		if !bytes.Equal(l.Part(p), m.fx.New[p]) {
			t.Fatalf("stash %s is not the firmware's image", p)
		}
	}
	if _, err := devmode.MainlineStash(m.newEnv(), devmode.Options{Device: "/dev/mmcblk0", FW: m.fw, Keys: m.keys}, io.Discard); err == nil ||
		!strings.Contains(err.Error(), "once per boot") {
		t.Fatalf("a second stash: %v", err)
	}
	if _, err := devmode.MainlineStash(m.newEnv(), devmode.Options{Device: "/dev/mmcblk0", FW: m.fw, Keys: m.keys, Keeper: "a"}, io.Discard); err == nil {
		t.Fatal("a stash with a keeper option")
	}
	// the plan with the stash: the keep-key step first
	p, out, err := m.mlPlan(m.mlOpts())
	if err != nil || p.Steps[0].Op != "keep-key" || !strings.Contains(out, "stash: "+mlstash.Dir) {
		t.Fatalf("plan: %v\n%s", err, out)
	}
	// a stash of another boot: refused
	b, _ := os.ReadFile(filepath.Join(dir, mlstash.Index))
	os.WriteFile(filepath.Join(dir, mlstash.Index), bytes.Replace(b, []byte(l.BootID), []byte("another-boot-id-0000000000000000000"), 1), 0o600)
	if _, out, err := m.mlPlan(m.mlOpts()); err == nil || !strings.Contains(out, "the stash is of boot") {
		t.Fatalf("a stash of another boot: %v\n%s", err, out)
	}
	os.RemoveAll(dir)
	if _, out, err := m.mlPlan(m.mlOpts()); err == nil || !strings.Contains(err.Error(), "key-keep") {
		t.Fatalf("no stash: %v\n%s", err, out)
	}
}

// TestMainlineKeptNoStash (review T2 MINOR-2): a unit whose two record
// sectors hold the same record (an earlier install kept the key) plans
// without a stash: the key state is "kept", no keep-key step; one copy only
// refuses (step 2 needs both).
func TestMainlineKeptNoStash(t *testing.T) {
	bin, exe := bootctlBin(t)
	rec := keykeep.Encode(fixture.Keys)
	backup := func(copies int) []byte {
		b := make([]byte, 4096*512)
		if copies > 0 {
			copy(b[(4096-keykeep.FromEnd1)*512:], rec)
		}
		if copies > 1 {
			copy(b[(4096-keykeep.FromEnd2)*512:], rec)
		}
		return b
	}
	for _, c := range []struct {
		copies int
		ok     bool
	}{{2, true}, {1, false}} {
		u := mlUnit3
		u.Tweak = map[string][]byte{"backup": backup(c.copies)}
		m := newMLU(t, u, false, bin, exe)
		os.RemoveAll(m.root.Path(mlstash.Dir))
		p, out, err := m.mlPlan(m.mlOpts())
		if (err == nil) != c.ok {
			t.Fatalf("%d copies: %v\n%s", c.copies, err, out)
		}
		if c.ok && (p.KeyKeep == nil || p.KeyKeep.State != "kept" || p.Steps[0].Op == "keep-key" || !strings.Contains(out, "kept on the unit already")) {
			t.Fatalf("%d copies: key state %+v\n%s", c.copies, p.KeyKeep, out)
		}
	}
}

// dUnitReset is a factory-stock unit after the BCB-route entry: both slots
// trying (libavb's reset, A 15), both slots stock.
var dUnitReset = fakeemmc.Unit{AB: [2][3]byte{{15, 6, 0}, {14, 7, 0}}, A: "old", B: "old"}

func (m *mlu) dPlan(o devmode.MainlineOptions) (*plan.DirectPlan, string, error) {
	var out bytes.Buffer
	var p *plan.DirectPlan
	var err error
	m.lockRetry(func() error {
		out.Reset()
		p, err = devmode.PlanMainlineDirect(m.newEnv(), o, &out)
		return err
	})
	return p, out.String(), err
}

func (m *mlu) dInstall(o devmode.MainlineOptions) (*install.Result, string, error) {
	var out bytes.Buffer
	var res *install.Result
	var err error
	m.lockRetry(func() error {
		out.Reset()
		res, err = devmode.InstallMainlineDirect(m.newEnv(), o, &out)
		return err
	})
	return res, out.String(), err
}

// dDone: B holds the stash's chain, the patched vbmeta, the assembled image
// and the rootfs; A unchanged; both key-record sectors hold the record; A/B
// A{14,0,1} B{15,7,0}.
func (m *mlu) dDone(p *plan.DirectPlan) {
	t := m.t
	t.Helper()
	after := m.image()
	if !sameOutside(after, m.before, &p.Plan) {
		t.Fatal("a byte outside B's write ranges, the key-record sectors and the A/B sector changed")
	}
	a, _ := avbab.Parse(after[plan.ABLBA*512:])
	if a.Validate() != nil || a.Pick() != 1 || a.Slots[1] != (avbab.Slot{Priority: 15, Tries: 7}) || a.Slots[0] != (avbab.Slot{Priority: 14, Successful: 1}) {
		t.Fatalf("final A/B %v %v", a.Slots[0], a.Slots[1])
	}
	tb, _ := gpt.Parse(bytes.NewReader(after), int64(len(after)))
	for _, n := range []string{"uboot", "trust", "dtbo"} {
		e, _ := tb.Find(n + "_b")
		if img := m.fx.New[n]; !bytes.Equal(after[e.Start*512:e.Start*512+int64(len(img))], img) {
			t.Fatalf("%s_b does not hold the stash's image", n)
		}
	}
	e, _ := tb.Find("boot_b")
	if !bytes.Equal(after[e.Start*512:e.Start*512+int64(len(m.expected))], m.expected) {
		t.Fatal("boot_b does not hold the assembled image")
	}
	rec := keykeep.Encode(m.fx.Keys)
	for _, sec := range p.KeyKeep.Sectors {
		if !bytes.Equal(after[sec.LBA*512:(sec.LBA+1)*512], rec) {
			t.Fatalf("backup LBA %d does not hold the record", sec.LBA)
		}
	}
}

// TestMainlineDirectDevice (task 3): the direct path on a fake factory-stock
// unit after the BCB-route entry: the plan (keep-key, R(K), the six writes of
// B from the stash, the arm), the install of the plan file, the end state;
// without the stash, without the unlock, with a locked lock_state read, and
// with the userdebug-first mode, refused.
func TestMainlineDirectDevice(t *testing.T) {
	bin, exe := bootctlBin(t)
	m := newMLU(t, dUnitReset, true, bin, exe)
	o := m.mlOpts()
	o.JSONOut = filepath.Join(m.root.Dir, "run/d-plan.json")
	p, out, err := m.dPlan(o)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if mlOpsD(p) != "keep-key bootctl bootctl write write write write write write sync verify bootctl check-ab" ||
		!strings.Contains(out, "cache") || p.UnlockDirect.Marker == nil || p.UnlockDirect.Marker.Found {
		t.Fatalf("plan %s\n%s", mlOpsD(p), out)
	}
	if after := m.image(); !bytes.Equal(after, m.before) {
		t.Fatal("the plan run changed the unit")
	}
	// the userdebug-first planner refuses this start (no keeper B, the BCB reset)
	if _, _, err := m.mlPlan(m.mlOpts()); err == nil {
		t.Fatal("the userdebug-first plan accepted the BCB reset of a stock unit")
	}
	// a lock_state read of 0x00 refuses whatever was confirmed
	zero := byte(0)
	lo := m.mlOpts()
	lo.LockState = &zero
	if _, out, err := m.dPlan(lo); err == nil || !strings.Contains(out, "FAIL unlock") {
		t.Fatalf("lock_state 0x00: %v\n%s", err, out)
	}
	no := m.mlOpts()
	no.UnlockConfirmed = false
	if _, out, err := m.dPlan(no); err == nil || !strings.Contains(out, "FAIL unlock") {
		t.Fatalf("no unlock: %v\n%s", err, out)
	}
	ins := m.mlOpts()
	ins.PlanFile, ins.Yes = o.JSONOut, true
	ins.JSONOut = ""
	if _, out, err := m.dInstall(ins); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	m.dDone(p)
}

func mlOpsD(p *plan.DirectPlan) string {
	var ops []string
	for _, s := range p.Steps {
		ops = append(ops, s.Op)
	}
	return strings.Join(ops, " ")
}

// TestMainlineDirectDeviceCutSweep (task 3): the device mode's direct install
// cut before every primitive write (clean and torn): at every cut the keeper
// A is bootable and boots (reestablishCut), and a new install resumes to the
// end state.
func TestMainlineDirectDeviceCutSweep(t *testing.T) {
	bin, exe := bootctlBin(t)
	m0 := newMLU(t, dUnitReset, false, bin, exe)
	o0 := m0.mlOpts()
	o0.Yes, o0.MaxWrite = true, sweepWrite
	o0.Hooks = &install.Hooks{CutAfter: -1}
	cnt := &countHooks{}
	o0.Hooks.Observe = cnt.observe
	if _, out, err := m0.dInstall(o0); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	n := cnt.n
	t.Parallel()
	var total atomic.Int64
	t.Cleanup(func() {
		t.Logf("direct device-mode power-cut sweep: %d cut points checked, each resumed", total.Load())
	})
	for _, torn := range []bool{false, true} {
		for _, k := range sweepPoints(t, n, nil) {
			what := fmt.Sprintf("cut before write %d (torn %v)", k+1, torn)
			point(t, what, func(t *testing.T) {
				m := newMLU(t, dUnitReset, false, bin, exe)
				ref, out0, err := m.dPlan(m.mlOpts()) // the plan of this fresh unit: the write ranges of dDone
				if err != nil {
					t.Fatalf("plan: %v\n%s", err, out0)
				}
				o := m.mlOpts()
				o.Yes, o.MaxWrite = true, sweepWrite
				o.Hooks = &install.Hooks{CutAfter: k, Torn: torn}
				_, out, err := m.dInstall(o)
				if k < n && !errors.Is(err, install.ErrPowerCut) {
					t.Fatalf("%s: err %v, want a power cut\n%s", what, err, out)
				}
				img := m.image()
				a, _ := avbab.Parse(img[plan.ABLBA*512:])
				if a.Validate() != nil || !a.Slots[0].Bootable() || (k < n && a.Pick() != 0) {
					t.Fatalf("%s: A/B %v %v: the keeper A must be bootable and boot until the arm", what, a.Slots[0], a.Slots[1])
				}
				ro := m.mlOpts()
				ro.Yes = true
				if k < n {
					if _, out, err := m.dInstall(ro); err != nil {
						t.Fatalf("%s: resume: %v\n%s", what, err, out)
					}
				}
				m.dDone(ref)
				total.Add(1)
			})
		}
	}
}

type countHooks struct{ n int }

func (c *countHooks) observe(install.Event) error { c.n++; return nil }
