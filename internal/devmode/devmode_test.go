//go:build install && devtest

package devmode_test

// The device mode against a fake unit (package devicetest): every device
// node is a regular file, sysfs, the device tree and /proc are files in a
// temporary root. No real disk is opened. The A/B transitions run the real
// uc-pxx-bootctl-inst (UC_PXX_BOOTCTL, under UC_PXX_BOOTCTL_EXEC on a PC)
// with --file on a copy of the sector; the result goes into the misc node
// file. Tests that need it are skipped without UC_PXX_BOOTCTL.
//
// Run: go test -tags install,devtest ./internal/devmode/

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
	"syscall"
	"testing"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/device/devicetest"
	"uc-pxx-installer/internal/devmode"
	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/nvram/nvramtest"
	"uc-pxx-installer/internal/pcdfw"
	"uc-pxx-installer/internal/plan"
)

var (
	unitFwd = fakeemmc.Unit{AB: [2][3]byte{{15, 0, 1}, {14, 0, 1}}, A: "old", B: "old"}
	unitRev = fakeemmc.Unit{AB: [2][3]byte{{14, 0, 1}, {15, 0, 1}}, A: "old", B: "old"}
	small   = fakeemmc.ParseSmallLayout(fakeemmc.SmallLayout)
)

// sweepWrite is the size of each data write in the sweeps: about 120 write
// points per install with the made-up firmware.
const sweepWrite = 32 << 10

func bootctlBin(t testing.TB) ([]byte, string) {
	p := os.Getenv("UC_PXX_BOOTCTL")
	if p == "" {
		t.Skip("UC_PXX_BOOTCTL not set: this test runs the real uc-pxx-bootctl-inst")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if devicetest.SHA256(b) != device.BootctlSHA256 {
		t.Fatalf("%s is not the reviewed binary %s", p, device.BootctlSHA256)
	}
	return b, os.Getenv("UC_PXX_BOOTCTL_EXEC")
}

// unit is one fake unit with its firmware file and key file.
type unit struct {
	t      testing.TB
	fx     *fakeemmc.Fixture
	root   *devicetest.Root
	bc     *devicetest.Bootctl
	env    *device.Env
	fw     string
	keys   string
	before []byte // the disk before the install
	start  []byte // the A/B sector before the install
}

var fixture = fakeemmc.NewFixture(fakeemmc.FixtureOpt{})

func newUnit(t testing.TB, u fakeemmc.Unit, o devicetest.Opts, bin []byte, exe string) *unit {
	return newUnitIn(t, t.TempDir(), u, o, bin, exe)
}

func newUnitIn(t testing.TB, dir string, u fakeemmc.Unit, o devicetest.Opts, bin []byte, exe string) *unit {
	fx := fixture
	src, err := fx.Image(dir, "src.img", u)
	if err != nil {
		t.Fatal(err)
	}
	l := small
	if u.Layout != "" {
		l = fakeemmc.ParseSmallLayout(u.Layout)
	}
	if bin == nil {
		bin = []byte("not the tool")
	}
	root, err := devicetest.New(filepath.Join(dir, "root"), src, l, bin, o)
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(src)
	un := &unit{t: t, fx: fx, root: root, fw: filepath.Join(dir, "fw.bin"), keys: filepath.Join(dir, "keys.txt")}
	if err := os.WriteFile(un.fw, fx.FW, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := pcdfw.WriteKeyFile(un.keys, fx.Keys); err != nil {
		t.Fatal(err)
	}
	un.bc = &devicetest.Bootctl{Root: root, Exec: exe, Tmp: dir}
	un.env = un.newEnv()
	un.before = un.image()
	un.start = append([]byte(nil), un.before[plan.ABLBA*512:(plan.ABLBA+1)*512]...)
	return un
}

// newEnv makes a fresh environment (a new process would have one).
func (u *unit) newEnv() *device.Env {
	e, err := device.NewForTest(device.TestOptions{Root: u.root.Dir, RegularFiles: true, Layout: small, Runner: u.bc,
		Manifests: u.fx.Manifests, ApprovedResources: []string{u.fx.Resource}})
	if err != nil {
		u.t.Fatal(err)
	}
	return e
}

func (u *unit) image() []byte {
	b, err := u.root.Image()
	if err != nil {
		u.t.Fatal(err)
	}
	return b
}

func (u *unit) opts(keeper string) devmode.Options {
	return devmode.Options{Device: device.EMMCPath, FW: u.fw, Keys: u.keys, Keeper: keeper, PrereqsConfirmed: true, Yes: true,
		LogPath: filepath.Join(u.root.Dir, "run/install.jsonl")}
}

func (u *unit) install(o devmode.Options) (*install.Result, string, error) {
	var out bytes.Buffer
	res, err := devmode.Install(u.env, o, &out)
	return res, out.String(), err
}

func (u *unit) plan(keeper string) *plan.Plan {
	var out bytes.Buffer
	p, err := devmode.Plan(u.newEnv(), u.opts(keeper), &out)
	if err != nil {
		u.t.Fatalf("plan: %v\n%s", err, out.String())
	}
	return p
}

// masked is the disk with the target's write ranges and the A/B sector set
// to zero: the bytes that no step may change.
func masked(img []byte, p *plan.Plan) []byte {
	m := append([]byte(nil), img...)
	for _, s := range p.Steps {
		if s.Write != nil {
			o := s.Write.StartLBA*512 + s.Write.Offset
			copy(m[o:o+s.Write.Length], make([]byte, s.Write.Length))
		}
	}
	copy(m[plan.ABLBA*512:(plan.ABLBA+1)*512], make([]byte, 512))
	return m
}

// untouched checks that nothing at all changed on the unit.
func (u *unit) untouched() {
	u.t.Helper()
	if !bytes.Equal(u.image(), u.before) {
		u.t.Fatal("the unit changed, but nothing may be written")
	}
}

// checkDone checks a finished install from the disk bytes.
func (u *unit) checkDone(p *plan.Plan) {
	t := u.t
	t.Helper()
	after := u.image()
	if !bytes.Equal(masked(after, p), masked(u.before, p)) {
		t.Fatal("a byte outside the target's write ranges and the A/B sector changed")
	}
	want, _ := p.AB.Final.Raw()
	if !bytes.Equal(after[plan.ABLBA*512:plan.ABLBA*512+32], want[:]) {
		t.Fatalf("final A/B %x, want %x", after[plan.ABLBA*512:plan.ABLBA*512+32], want)
	}
	if !bytes.Equal(after[plan.ABLBA*512+32:(plan.ABLBA+1)*512], u.start[32:]) {
		t.Fatal("A/B bytes 32..511 changed")
	}
	tb, _ := gpt.Parse(bytes.NewReader(after), int64(len(after)))
	for _, part := range plan.SlotParts {
		e, _ := tb.Find(part + "_" + p.Target)
		img := u.fx.New[part]
		if !bytes.Equal(after[e.Start*512:e.Start*512+int64(len(img))], img) {
			t.Fatalf("%s does not hold the payload image", e.Name)
		}
	}
	cs, err := install.CheckCutState(bytes.NewReader(after), p, u.start)
	if err != nil || cs.Boots != "target" || !cs.Checked {
		t.Fatalf("final state %+v: %v", cs, err)
	}
}

func TestDeviceInstall(t *testing.T) {
	bin, exe := bootctlBin(t)
	for _, c := range []struct {
		name, keeper, target, final string
		unit                        fakeemmc.Unit
	}{
		{"keeper a prio 15, target b", "a", "b", "A{14,0,1} B{15,7,0}, boots next: B", unitFwd},
		{"keeper b prio 15, target a", "b", "a", "A{15,7,0} B{14,0,1}, boots next: A", unitRev},
		{"keeper a prio 14, target b disabled, no --keeper", "", "b", "A{14,0,1} B{15,7,0}, boots next: B",
			fakeemmc.Unit{AB: [2][3]byte{{14, 0, 1}, {0, 0, 0}}, A: "old",
				Tweak: map[string][]byte{"vbmeta_b": fixture.Old["vbmeta"]}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			u := newUnit(t, c.unit, devicetest.Opts{}, bin, exe)
			p := u.plan(c.keeper)
			if p.Mode != "device" || p.Target != c.target || p.AB.Final.String() != c.final || p.Device == nil ||
				len(p.KeeperSHA256) != len(plan.SlotParts) || p.Bootctl.SHA256 != device.BootctlSHA256 {
				t.Fatalf("plan: mode %s target %s final %s device %v keeper hashes %d bootctl %s", p.Mode, p.Target,
					p.AB.Final, p.Device, len(p.KeeperSHA256), p.Bootctl.SHA256)
			}
			u.bc.Calls = nil
			res, out, err := u.install(u.opts(c.keeper))
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if res.Steps != len(p.Steps) || !strings.Contains(out, "plan id: "+p.ID) ||
				!strings.Contains(out, "the new plan has the same id "+p.ID) || !strings.Contains(out, "RESULT: install done") {
				t.Fatalf("steps %d\n%s", res.Steps, out)
			}
			u.checkDone(p)
			// uc-pxx-bootctl-inst ran only status and the two write forms of the plan, in this order
			var writes []string
			for _, call := range u.bc.Calls {
				if call != "status" {
					writes = append(writes, call)
				}
			}
			want := []string{"disable --slot " + c.target, "arm-trying --slot " + c.target + " --lower-keeper"}
			if p.Steps[0].Bootctl.NoOp {
				// a target that is already {0,0,0}: the tool says "nothing to do"
				want = []string{"disable --slot " + c.target, "arm-trying --slot " + c.target + " --lower-keeper"}
			}
			if strings.Join(writes, "|") != strings.Join(want, "|") {
				t.Fatalf("bootctl write calls %q, want %q", writes, want)
			}
			// a second install is refused without --keeper: the target boots
			// next and is not successful yet. (With --keeper the user names a
			// successful keeper, and the planner allows the install again.)
			if _, out, err := u.install(u.opts("")); err == nil || !strings.Contains(err.Error(), "keeper: slot "+c.target+" must be successful") {
				t.Fatalf("second install: %v\n%s", err, out)
			}
		})
	}
}

// TestDeviceRefusals: the device mode refuses everything that is not the
// eMMC of a UC-P10 in the installer RAM image. Nothing is written, and
// uc-pxx-bootctl-inst never runs. These need no bootctl binary.
func TestDeviceRefusals(t *testing.T) {
	moved := strings.Replace(fakeemmc.SmallLayout, "14 boot_b 59008 512", "14 boot_b 59008 511", 1)
	real, tok := devicetest.RAMCmdline, "uc-pxx.id=p10inst-4f3f5e"
	if !strings.Contains(real, " "+tok) {
		t.Fatalf("the fixture has no token: %q", real)
	}
	cases := []struct {
		name   string
		device string
		o      devicetest.Opts
		layout string // the GPT on the disk ("" = the expected layout)
		want   string
	}{
		{"another device: an SD card node", "/dev/mmcblk1", devicetest.Opts{}, "", "must be exactly /dev/mmcblk0"},
		{"another device: a USB disk", "/dev/sda", devicetest.Opts{}, "", "must be exactly"},
		{"another device: a partition", "/dev/mmcblk0p13", devicetest.Opts{}, "", "must be exactly"},
		{"another device: a path trick", "/dev/../dev/mmcblk0", devicetest.Opts{}, "", "must be exactly"},
		{"an SD card as mmcblk0", "", devicetest.Opts{CardType: "SD"}, "", "not the eMMC (MMC): an SD card"},
		{"mmcblk0 on the SD controller", "", devicetest.Opts{Host: "fe320000.mmc"}, "", "not on the eMMC controller"},
		{"a loop device", "", devicetest.Opts{Host: "virtual"}, "", "not on the eMMC controller"},
		{"another board", "", devicetest.Opts{Model: "Rockchip RK3399 Evaluation Board"}, "", "not a UC-P10"},
		{"another compatible", "", devicetest.Opts{Compat: "rockchip,rk3399-evb\x00rockchip,rk3399"}, "", "does not list crestron,uc-p10"},
		{"booted mainline (token)", "", devicetest.Opts{Cmdline: "console=ttyS2 uc-pxx.id=ucpxx-12345678 p10inst.ram=1"}, "", "booted system"},
		{"booted Android", "", devicetest.Opts{Cmdline: "androidboot.slot_suffix=_b p10inst.ram=1"}, "", "booted system"},
		{"root on the eMMC", "", devicetest.Opts{Cmdline: "root=/dev/mmcblk0p22 rw p10inst.ram=1"}, "", "booted system"},
		{"no p10inst.ram=1", "", devicetest.Opts{Cmdline: "console=ttyS2 rdinit=/init"}, "", "no p10inst.ram=1"},
		// the real command line of the RAM image (with its token), changed
		{"real line: a mainline token", "", devicetest.Opts{Cmdline: strings.Replace(real, tok, "uc-pxx.id=ucpxx-dfdcdfd8", 1)}, "", "booted system"},
		{"real line: the token in uppercase", "", devicetest.Opts{Cmdline: strings.Replace(real, tok, "uc-pxx.id=p10inst-4F3F5E", 1)}, "", "booted system"},
		{"real line: a token of 7 digits", "", devicetest.Opts{Cmdline: strings.Replace(real, tok, tok+"0", 1)}, "", "booted system"},
		{"real line: a token with a suffix", "", devicetest.Opts{Cmdline: strings.Replace(real, tok, tok+".1", 1)}, "", "booted system"},
		{"real line: two tokens", "", devicetest.Opts{Cmdline: real + " uc-pxx.id=p10inst-000000"}, "", "second uc-pxx.id="},
		{"real line: no p10inst.ram=1", "", devicetest.Opts{Cmdline: strings.Replace(real, " p10inst.ram=1", "", 1)}, "", "no p10inst.ram=1"},
		{"real line: androidboot.", "", devicetest.Opts{Cmdline: real + " androidboot.slot_suffix=_a"}, "", "booted system"},
		{"real line: root=", "", devicetest.Opts{Cmdline: real + " root=/dev/mmcblk0p22"}, "", "booted system"},
		{"no marker file", "", devicetest.Opts{NoMarker: true}, "", "no marker file"},
		{"root file system on the eMMC", "", devicetest.Opts{Mounts: "/dev/root / ext4 rw 0 0\n"}, "", "not in RAM"},
		{"an eMMC partition mounted", "", devicetest.Opts{Mounts: "rootfs / rootfs rw 0 0\n/dev/mmcblk0p23 /mnt ext4 ro 0 0\n"}, "", "is mounted"},
		{"Android files", "", devicetest.Opts{Android: true}, "", "this is Android"},
		{"sysfs layout: a start moved", "", devicetest.Opts{SysfsEdit: func(n string, e *gpt.LayoutEntry) {
			if n == "system_b" {
				e.Start++
			}
		}}, "", "partition system_b: sysfs has"},
		{"sysfs layout: a name changed", "", devicetest.Opts{SysfsEdit: func(n string, e *gpt.LayoutEntry) {
			if n == "boot_b" {
				e.Name = "boot_c"
			}
		}}, "", "partition boot_b: sysfs has"},
		{"the GPT differs from sysfs", "", devicetest.Opts{}, moved, "the GPT has"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u := newUnit(t, fakeemmc.Unit{Layout: c.layout, AB: unitFwd.AB, A: "old", B: "old"}, c.o, nil, "")
			if c.layout != "" {
				// sysfs shows the expected layout, the GPT on the disk the moved one
				dir := t.TempDir()
				src, _ := u.fx.Image(dir, "x.img", fakeemmc.Unit{Layout: c.layout, AB: unitFwd.AB, A: "old", B: "old"})
				r2, err := devicetest.New(filepath.Join(dir, "root"), src, small, []byte("x"), c.o)
				if err != nil {
					t.Fatal(err)
				}
				u.root, u.bc.Root = r2, r2
				u.env = u.newEnv()
				u.before = u.image()
			}
			o := u.opts("a")
			if c.device != "" {
				o.Device = c.device
			}
			_, out, err := u.install(o)
			if err == nil || !device.IsRefusal(err) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want a refusal with %q\n%s", err, c.want, out)
			}
			if devmode.ExitCode(err) != 1 {
				t.Fatalf("exit code %d", devmode.ExitCode(err))
			}
			if len(u.bc.Calls) != 0 {
				t.Fatalf("uc-pxx-bootctl-inst ran: %q", u.bc.Calls)
			}
			u.untouched()
		})
	}
	t.Run("the A/B lock is held", func(t *testing.T) {
		u := newUnit(t, unitFwd, devicetest.Opts{}, nil, "")
		os.MkdirAll(u.root.Path("/run"), 0o755)
		f, err := os.OpenFile(u.root.Path(device.ABLockPath), os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		// another open file description holds the lock, as another process would
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			t.Fatal(err)
		}
		if _, _, err := u.install(u.opts("a")); err == nil || !strings.Contains(err.Error(), "held by another A/B writer") {
			t.Fatalf("err = %v", err)
		}
		u.untouched()
	})
	t.Run("a release build has no test environment", func(t *testing.T) {
		if device.Live().IsTest() {
			t.Fatal("Live is a test environment")
		}
		if _, err := device.NewForTest(device.TestOptions{Root: "/"}); err == nil {
			t.Fatal("a test environment on /")
		}
	})
}

// TestDeviceKeeperRule: both slots successful and no pre-fastboot sector:
// the device mode refuses unless the user names the keeper.
func TestDeviceKeeperRule(t *testing.T) {
	bin, exe := bootctlBin(t)
	u := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe)
	_, out, err := u.install(u.opts(""))
	if err == nil || !strings.Contains(err.Error(), "choose the keeper") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	u.untouched()
}

// TestDeviceAutoUpdate: the device mode refuses while Auto Update Firmware of
// stock Android is on or not known (the plan check autoupdate), before any
// write; bootctl runs only its status.
func TestDeviceAutoUpdate(t *testing.T) {
	bin, exe := bootctlBin(t)
	stores := map[string][]byte{
		"on":                          nvramtest.On(),
		"no store":                    {0},
		"the newer part on":           nvramtest.Partition(nvramtest.Part{Index: 31, Counter: 9, Records: nvramtest.Records("false")}, nvramtest.Part{Index: 0, Counter: 10, Records: nvramtest.Records("true")}),
		"the only part, bad checksum": nvramtest.Partition(nvramtest.Part{Index: 2, Counter: 9, Records: nvramtest.Records("false"), BadSum: true}),
	}
	for name, store := range stores {
		t.Run(name, func(t *testing.T) {
			un := unitFwd
			un.Tweak = map[string][]byte{"others": store}
			u := newUnit(t, un, devicetest.Opts{}, bin, exe)
			_, out, err := u.install(u.opts("a"))
			if err == nil || !strings.Contains(err.Error(), "autoupdate: Auto Update Firmware") {
				t.Fatalf("err = %v\n%s", err, out)
			}
			for _, c := range u.bc.Calls {
				if !strings.HasPrefix(c, "status") {
					t.Fatalf("uc-pxx-bootctl-inst ran %q", c)
				}
			}
			u.untouched()
		})
	}
}

// TestDeviceBootctlSHA: a binary that is not the reviewed one never runs:
// at the start (the plan refuses) and in the middle (the arm step stops;
// the target stays disabled, so the keeper boots).
func TestDeviceBootctlSHA(t *testing.T) {
	bin, exe := bootctlBin(t)
	t.Run("at the start", func(t *testing.T) {
		u := newUnit(t, unitFwd, devicetest.Opts{}, append(append([]byte(nil), bin...), 0), exe)
		_, out, err := u.install(u.opts("a"))
		if err == nil || !device.IsRefusal(err) || !strings.Contains(err.Error(), "bootctl-binary") {
			t.Fatalf("err = %v\n%s", err, out)
		}
		if !strings.Contains(out, "not the reviewed binary") || len(u.bc.Calls) != 0 {
			t.Fatalf("calls %q\n%s", u.bc.Calls, out)
		}
		u.untouched()
	})
	t.Run("swapped during the install", func(t *testing.T) {
		u := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe)
		o := u.opts("a")
		swapped := false
		o.Hooks = &install.Hooks{CutAfter: -1, Observe: func(ev install.Event) error {
			if ev.Op == "data" && !swapped {
				swapped = true
				return os.WriteFile(u.root.Path(device.BootctlPath), append(append([]byte(nil), bin...), 0x90), 0o755)
			}
			return nil
		}}
		_, out, err := u.install(o)
		var st *devmode.Stop
		if !errors.As(err, &st) || !strings.Contains(err.Error(), "not the reviewed binary") || devmode.ExitCode(err) != 1 {
			t.Fatalf("err = %v\n%s", err, out)
		}
		for _, c := range u.bc.Calls {
			if strings.HasPrefix(c, "arm-trying") {
				t.Fatal("arm-trying ran with a swapped binary")
			}
		}
		if !strings.Contains(st.Safe, "safe to power off") {
			t.Fatalf("A/B after the stop: %s", st.Safe)
		}
		cs, err := install.CheckCutState(bytes.NewReader(u.image()), u.plan0(), u.start)
		if err != nil || cs.Boots != "keeper" || cs.Target != "disabled" {
			t.Fatalf("after the stop: %+v %v", cs, err)
		}
	})
}

// plan0 is the plan of the unit as it was before the install (from the
// saved bytes, so it does not depend on the state now).
func (u *unit) plan0() *plan.Plan {
	dir := u.t.TempDir()
	img := filepath.Join(dir, "before.img")
	if err := os.WriteFile(img, u.before, 0o644); err != nil {
		u.t.Fatal(err)
	}
	r, err := devicetest.New(filepath.Join(dir, "root"), img, small, mustRead(u.t, u.root.Path(device.BootctlPath)), devicetest.Opts{})
	if err != nil {
		u.t.Fatal(err)
	}
	u2 := &unit{t: u.t, fx: u.fx, root: r, fw: u.fw, keys: u.keys, bc: &devicetest.Bootctl{Root: r, Exec: u.bc.Exec, Tmp: dir}}
	u2.env = u2.newEnv()
	bin, _ := bootctlBin(u.t)
	os.WriteFile(r.Path(device.BootctlPath), bin, 0o755)
	return u2.plan("a")
}

func mustRead(t testing.TB, p string) []byte {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestDevicePlanID: what a person approved is what runs.
func TestDevicePlanID(t *testing.T) {
	bin, exe := bootctlBin(t)
	t.Run("another plan file", func(t *testing.T) {
		u := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe)
		p := u.plan("a")
		p.KeeperWhy = "edited"
		p.Seal()
		f := filepath.Join(t.TempDir(), "plan.json")
		writePlan(t, f, p)
		o := u.opts("a")
		o.PlanFile = f
		_, out, err := u.install(o)
		if err == nil || !strings.Contains(err.Error(), "the plan now has id") {
			t.Fatalf("err = %v\n%s", err, out)
		}
		u.untouched()
	})
	t.Run("an edited plan file with the old id", func(t *testing.T) {
		u := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe)
		p := u.plan("a")
		p.KeeperWhy = "edited" // the id field stays as it was
		f := filepath.Join(t.TempDir(), "plan.json")
		writePlan(t, f, p)
		o := u.opts("a")
		o.PlanFile = f
		_, out, err := u.install(o)
		if err == nil || !strings.Contains(err.Error(), "does not match its id") {
			t.Fatalf("err = %v\n%s", err, out)
		}
		u.untouched()
	})
	t.Run("the approved plan file", func(t *testing.T) {
		u := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe)
		p := u.plan("a")
		f := filepath.Join(t.TempDir(), "plan.json")
		writePlan(t, f, p)
		o := u.opts("a")
		o.PlanFile = f
		if _, out, err := u.install(o); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		u.checkDone(p)
	})
	t.Run("the unit changes after the confirmation", func(t *testing.T) {
		u := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe)
		o := u.opts("a")
		o.AfterConfirm = func() {
			// a byte of the keeper's system changes: its hash is in the plan
			f, _ := os.OpenFile(u.root.Node("system_a"), os.O_RDWR, 0)
			f.WriteAt([]byte{0x5a}, 4096)
			f.Close()
		}
		_, out, err := u.install(o)
		if err == nil || !strings.Contains(err.Error(), "the plan changed after it was shown") {
			t.Fatalf("err = %v\n%s", err, out)
		}
		after := u.image()
		e, _ := gpt.Parse(bytes.NewReader(after), int64(len(after)))
		sa, _ := e.Find("system_a")
		after[sa.Start*512+4096] = u.before[sa.Start*512+4096]
		if !bytes.Equal(after, u.before) {
			t.Fatal("something was written")
		}
	})
	t.Run("not confirmed", func(t *testing.T) {
		u := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe)
		for _, answer := range []string{"", "yes\n", "install\n", "INSTAL\n"} {
			o := u.opts("a")
			o.Yes, o.Confirm = false, strings.NewReader(answer)
			if _, _, err := u.install(o); err == nil || !strings.Contains(err.Error(), "not confirmed") {
				t.Fatalf("answer %q: err = %v", answer, err)
			}
		}
		u.untouched()
		o := u.opts("a")
		o.Yes, o.Confirm = false, strings.NewReader("INSTALL\n")
		if _, out, err := u.install(o); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	})
}

func writePlan(t testing.TB, path string, p *plan.Plan) {
	b, _ := jsonMarshal(p)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDeviceBootctlMisbehaves: whatever uc-pxx-bootctl-inst does, the
// device mode stops. When the tool ends badly (exit 3, a signal), p10inst
// reads the sector itself: a known state (before, or exactly the prediction)
// is a stop with exit 1 and that state; anything else is exit 3, and then the
// output never says "safe to power off" (review F8).
func TestDeviceBootctlMisbehaves(t *testing.T) {
	bin, exe := bootctlBin(t)
	for _, c := range []struct {
		mode, want string
		code       int
		armed      bool // the stop is at the arm (the data is written)
		writes     int  // primitive writes reported (-1 = do not check)
		target     string
	}{
		{"refuse", "exit 1 (refused, the A/B sector is verified unchanged)", 1, false, 0, "as before"},
		{"unknown", "ended with exit 3; the installer read the A/B sector back: it is unchanged", 1, false, 0, "as before"},
		{"unknown-wrote", "ended with exit 3; the installer read the A/B sector back: it is the plan's next state", 1, false, 1, "disabled"},
		{"signal", "was ended by the signal hangup; the installer read the A/B sector back: it is unchanged", 1, false, 0, "as before"},
		{"signal-wrote", "was ended by the signal interrupt; the installer read the A/B sector back: it is the plan's next state", 1, false, 1, "disabled"},
		{"garbage", "A/B STATE UNKNOWN", 3, false, -1, ""},
		{"lie", "A/B STATE UNKNOWN", 3, false, -1, ""},
		{"wrong", "A/B STATE UNKNOWN", 3, false, -1, ""},
		{"tail", "A/B STATE UNKNOWN", 3, false, -1, ""},
		{"arm-refuse", "exit 1 (refused", 1, true, -1, "disabled"},
	} {
		t.Run(c.mode, func(t *testing.T) {
			u := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe)
			u.bc.Mode = c.mode
			_, out, err := u.install(u.opts("a"))
			if err == nil || !strings.Contains(err.Error(), c.want) || devmode.ExitCode(err) != c.code {
				t.Fatalf("err = %v (exit %d)\n%s", err, devmode.ExitCode(err), out)
			}
			hold := strings.Contains(out, "DO NOT POWER OFF AND DO NOT RESTART")
			safe := strings.Contains(out, "safe to power off")
			if c.code == 3 && (!hold || safe) || c.code == 1 && hold {
				t.Fatalf("exit %d: hold text %v, safe text %v: the two must never both show\n%s", c.code, hold, safe, out)
			}
			if c.writes >= 0 && !strings.Contains(out, fmt.Sprintf("writes done: %d\n", c.writes)) {
				t.Fatalf("want writes done: %d\n%s", c.writes, out)
			}
			img := u.image()
			if c.code == 1 {
				cs, err := install.CheckCutState(bytes.NewReader(img), u.plan0(), u.start)
				if err != nil || cs.Boots != "keeper" || cs.Target != c.target {
					t.Fatalf("after the stop: %+v %v", cs, err)
				}
			}
			if !c.armed {
				// no data was written: only the A/B sector may differ
				p := u.plan0()
				for _, s := range p.Steps {
					if s.Write != nil {
						o := s.Write.StartLBA * 512
						if !bytes.Equal(img[o:o+s.Write.Length], u.before[o:o+s.Write.Length]) {
							t.Fatalf("%s was written after a failed first step", s.Write.Partition)
						}
					}
				}
			}
		})
	}
}

// TestDeviceSignals (review F2): a ^C, a hangup, SIGTERM and SIGPIPE during
// the install do not stop it, and a closed output loses nothing (the
// transcript file keeps the lines). The device mode runs in a child process
// (this test binary) that sends the signals to itself after the disable,
// closes its stdout during the data, and must still end with a complete
// install. Without the fix the first SIGINT ends the child.
const signalChildEnv = "P10INST_DEVMODE_SIGNALS"

func TestDeviceSignals(t *testing.T) {
	bin, exe := bootctlBin(t)
	u := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe)
	p0 := u.plan("a")
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), signalChildEnv+"="+strings.Join([]string{u.root.Dir, u.fw, u.keys}, "|"))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the child: %v\n%s", err, out)
	}
	tr, err := os.ReadFile(filepath.Join(u.root.Dir, "run/install.out"))
	if err != nil || !strings.Contains(string(tr), "RESULT: install done") || !strings.Contains(string(tr), "13/13 check the A/B sector") {
		t.Fatalf("transcript: %v\n%s", err, tr)
	}
	if strings.Contains(string(out), "RESULT: install done") {
		t.Fatal("the child still wrote to its closed stdout")
	}
	u.checkDone(p0)
}

func signalChild() int {
	f := strings.Split(os.Getenv(signalChildEnv), "|")
	root, fw, keys := f[0], f[1], f[2]
	r := devicetest.Reopen(root, small)
	bc := &devicetest.Bootctl{Root: r, Exec: os.Getenv("UC_PXX_BOOTCTL_EXEC"), Tmp: filepath.Dir(root)}
	e, err := device.NewForTest(device.TestOptions{Root: root, RegularFiles: true, Layout: small, Runner: bc,
		Manifests: fixture.Manifests, ApprovedResources: []string{fixture.Resource}})
	if err != nil {
		fmt.Println(err)
		return 2
	}
	o := devmode.Options{Device: device.EMMCPath, FW: fw, Keys: keys, Keeper: "a", PrereqsConfirmed: true, Yes: true,
		LogPath: filepath.Join(root, "run/install.jsonl")}
	me := os.Getpid()
	o.Hooks = &install.Hooks{CutAfter: -1, Observe: func(ev install.Event) error {
		switch ev.Write {
		case 1: // after the disable
			for _, s := range []syscall.Signal{syscall.SIGINT, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGPIPE} {
				syscall.Kill(me, s)
				syscall.Kill(-syscall.Getpgrp(), s) // the whole group, as a terminal does
			}
		case 3: // during the data: the terminal goes away
			os.Stdout.Close()
			os.Stderr.Close()
		}
		return nil
	}}
	_, err = devmode.Install(e, o, os.Stdout)
	if err != nil {
		return 1
	}
	return 0
}

// TestDevicePaths (review F4): no output onto, and no input from, a device
// node, a kernel file system or a symlink.
func TestDevicePaths(t *testing.T) {
	u := newUnit(t, unitFwd, devicetest.Opts{}, nil, "")
	d := t.TempDir()
	link := filepath.Join(d, "plan.json")
	os.Symlink("/dev/null", link)
	fwLink := filepath.Join(d, "fw.bin")
	os.Symlink(u.fw, fwLink)
	for _, c := range []struct {
		name string
		edit func(o *devmode.Options)
		want string
	}{
		{"--json /dev/null", func(o *devmode.Options) { o.JSONOut = "/dev/null" }, "is under /dev"},
		{"--json a disk node", func(o *devmode.Options) { o.JSONOut = "/dev/mmcblk0" }, "is under /dev"},
		{"--json a symlink to /dev/null", func(o *devmode.Options) { o.JSONOut = link }, "not a regular file"},
		{"--log under /proc", func(o *devmode.Options) { o.LogPath = "/proc/self/fd/1" }, "is under /proc"},
		{"--lib /dev/zero", func(o *devmode.Options) { o.Keys, o.Libs = "", []string{"/dev/zero"} }, "is under /dev"},
		{"--keys under /sys", func(o *devmode.Options) { o.Keys = "/sys/kernel/notes" }, "is under /sys"},
		{"--fw a symlink", func(o *devmode.Options) { o.FW = fwLink }, "not a regular file"},
		{"--plan a directory", func(o *devmode.Options) { o.PlanFile = d }, "not a regular file"},
	} {
		o := u.opts("a")
		c.edit(&o)
		if _, _, err := u.install(o); err == nil || !device.IsRefusal(err) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
		if _, err := devmode.Plan(u.newEnv(), o, io.Discard); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("plan %s: err = %v", c.name, err)
		}
	}
	if len(u.bc.Calls) != 0 {
		t.Fatalf("bootctl ran: %q", u.bc.Calls)
	}
	u.untouched()
}

// TestDeviceInUse (review F5): an eMMC partition mounted under another name,
// and a loop device backed by an eMMC partition, are refused.
func TestDeviceInUse(t *testing.T) {
	for _, c := range []struct {
		name string
		o    devicetest.Opts
		want string
	}{
		{"mounted through /dev/disk/by-partlabel", devicetest.Opts{
			Mounts: "rootfs / rootfs rw 0 0\n/dev/disk/by-partlabel/system_b /mnt ext4 ro 0 0\n",
			Links:  map[string]string{"/dev/disk/by-partlabel/system_b": "../../mmcblk0p23"}}, "mmcblk0p23 (system_b) is mounted on /mnt"},
		{"a loop device on system_a", devicetest.Opts{
			Files: map[string]string{"/sys/block/loop0/loop/backing_file": "/dev/mmcblk0p22\n"}}, "loop0 is backed by /dev/mmcblk0p22"},
		{"a loop device on a link to the disk", devicetest.Opts{
			Files: map[string]string{"/sys/block/loop3/loop/backing_file": "/run/emmc-link\n"},
			Links: map[string]string{"/run/emmc-link": "../dev/mmcblk0"}}, "loop3 is backed by /run/emmc-link (mmcblk0)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			u := newUnit(t, unitFwd, c.o, nil, "")
			_, out, err := u.install(u.opts("a"))
			if err == nil || !device.IsRefusal(err) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q\n%s", err, c.want, out)
			}
			u.untouched()
		})
	}
	t.Run("a loop device on another file is fine", func(t *testing.T) {
		u := newUnit(t, unitFwd, devicetest.Opts{Files: map[string]string{"/sys/block/loop1/loop/backing_file": "/run/p10inst/drive/drive.img\n"}}, nil, "")
		if _, err := u.env.OpenEMMC(device.EMMCPath); err != nil {
			t.Fatal(err)
		}
	})
}

// cutCheck checks the disk after a cut: the power-cut rule, and nothing
// outside the target's write ranges and the A/B sector changed.
func (u *unit) cutCheck(p *plan.Plan, what string) install.CutState {
	u.t.Helper()
	img := u.image()
	cs, err := install.CheckCutState(bytes.NewReader(img), p, u.start)
	if err != nil {
		u.t.Fatalf("%s: %v (A/B %s)", what, err, cs.AB)
	}
	if !bytes.Equal(masked(img, p), masked(u.before, p)) {
		u.t.Fatalf("%s: a byte outside the target's write ranges changed", what)
	}
	return cs
}

// resume runs the device mode again on a cut unit, as a user would.
func (u *unit) resume(p *plan.Plan, cs install.CutState, what string) {
	u.t.Helper()
	u.env = u.newEnv()
	if cs.Boots == "target" {
		// the target boots next (trying): without --keeper the planner must refuse
		if _, out, err := u.install(u.opts("")); err == nil || !device.IsRefusal(err) {
			u.t.Fatalf("%s: the target boots next, but the install ran again: %v\n%s", what, err, out)
		}
		return
	}
	_, out, err := u.install(u.opts(p.Keeper))
	if err != nil {
		u.t.Fatalf("%s: resume: %v\n%s", what, err, out)
	}
	u.checkDone(p)
}

func sweepPoints(t *testing.T, n int) []int {
	var ks []int
	step := 1
	if testing.Short() {
		step = 13
	}
	for k := 0; k <= n; k += step {
		ks = append(ks, k)
	}
	if ks[len(ks)-1] != n {
		ks = append(ks, n)
	}
	return ks
}

func countWrites(t *testing.T, sh fakeemmc.Unit, keeper string, bin []byte, exe string) (int, *plan.Plan) {
	u := newUnit(t, sh, devicetest.Opts{}, bin, exe)
	p := u.plan(keeper)
	o := u.opts(keeper)
	o.MaxWrite = sweepWrite
	n := 0
	o.Hooks = &install.Hooks{CutAfter: -1, Observe: func(ev install.Event) error { n = ev.Write; return nil }}
	if _, out, err := u.install(o); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return n, p
}

var sweepShapes = []struct {
	name, keeper string
	unit         fakeemmc.Unit
}{
	{"keeper a 15, target b", "a", unitFwd},
	{"keeper b 15, target a", "b", unitRev},
	{"keeper a 14, target b trust identical", "a", fakeemmc.Unit{AB: [2][3]byte{{14, 0, 1}, {13, 0, 1}}, A: "old", B: "old",
		Tweak: map[string][]byte{"trust_b": fixture.New["trust"]}}},
}

// TestDevicePowerCutSweep cuts the device-mode install before every
// primitive write (clean, and torn: half of the write first), with no
// clean-up, as a power loss would. After each cut: the power-cut rule on the
// disk bytes (CheckCutState, A/B decoded with package avbab), nothing else
// changed; then the device mode runs again and completes.
func TestDevicePowerCutSweep(t *testing.T) {
	bin, exe := bootctlBin(t)
	total := 0
	for _, sh := range sweepShapes {
		t.Run(sh.name, func(t *testing.T) {
			n, p0 := countWrites(t, sh.unit, sh.keeper, bin, exe)
			for _, torn := range []bool{false, true} {
				for _, k := range sweepPoints(t, n) {
					u := newUnit(t, sh.unit, devicetest.Opts{}, bin, exe)
					o := u.opts(sh.keeper)
					o.MaxWrite = sweepWrite
					o.Hooks = &install.Hooks{CutAfter: k, Torn: torn}
					_, out, err := u.install(o)
					what := fmt.Sprintf("cut before write %d (torn %v)", k+1, torn)
					if k < n && !errors.Is(err, install.ErrPowerCut) {
						t.Fatalf("%s: err %v, want a power cut\n%s", what, err, out)
					}
					if k == n && err != nil {
						t.Fatalf("no cut: %v\n%s", err, out)
					}
					cs := u.cutCheck(p0, what)
					if k < n && cs.Boots != "keeper" {
						t.Fatalf("%s: %+v: only the last write may make the target the next boot", what, cs)
					}
					u.resume(p0, cs, what)
					total++
				}
			}
		})
	}
	t.Logf("device-mode power-cut sweep: %d cut points checked and resumed", total)
}

// The kill sweep runs the device mode in a child process (this test binary)
// that SIGKILLs itself right after primitive write k: a real process death,
// with no deferred clean-up.
const childEnv = "P10INST_DEVMODE_CHILD"

func TestMain(m *testing.M) {
	if os.Getenv(childEnv) != "" {
		os.Exit(child())
	}
	if os.Getenv(signalChildEnv) != "" {
		os.Exit(signalChild())
	}
	os.Exit(m.Run())
}

func child() int {
	f := strings.Split(os.Getenv(childEnv), "|")
	root, fw, keys, keeper := f[0], f[1], f[2], f[3]
	k, _ := strconv.Atoi(f[4])
	r := devicetest.Reopen(root, small)
	bc := &devicetest.Bootctl{Root: r, Exec: os.Getenv("UC_PXX_BOOTCTL_EXEC"), Tmp: filepath.Dir(root)}
	e, err := device.NewForTest(device.TestOptions{Root: root, RegularFiles: true, Layout: small, Runner: bc,
		Manifests: fixture.Manifests, ApprovedResources: []string{fixture.Resource}})
	if err != nil {
		fmt.Println(err)
		return 2
	}
	o := devmode.Options{Device: device.EMMCPath, FW: fw, Keys: keys, Keeper: keeper, PrereqsConfirmed: true, Yes: true,
		MaxWrite: sweepWrite, LogPath: filepath.Join(root, "run/install.jsonl")}
	o.Hooks = &install.Hooks{CutAfter: -1, Observe: func(ev install.Event) error {
		if ev.Write == k {
			syscall.Kill(os.Getpid(), syscall.SIGKILL)
			select {}
		}
		return nil
	}}
	var out bytes.Buffer
	_, err = devmode.Install(e, o, &out)
	fmt.Print(out.String())
	if err != nil {
		fmt.Println(err)
		return 1
	}
	return 0
}

func TestDeviceKillSweep(t *testing.T) {
	bin, exe := bootctlBin(t)
	total := 0
	for _, sh := range sweepShapes[:2] {
		t.Run(sh.name, func(t *testing.T) {
			n, p0 := countWrites(t, sh.unit, sh.keeper, bin, exe)
			for _, k := range sweepPoints(t, n) {
				if k == 0 {
					continue
				}
				u := newUnit(t, sh.unit, devicetest.Opts{}, bin, exe)
				cmd := exec.Command(os.Args[0], "-test.run=^$")
				cmd.Env = append(os.Environ(), childEnv+"="+strings.Join([]string{u.root.Dir, u.fw, u.keys, sh.keeper, strconv.Itoa(k)}, "|"))
				out, err := cmd.CombinedOutput()
				what := fmt.Sprintf("SIGKILL after write %d", k)
				var ee *exec.ExitError
				killed := errors.As(err, &ee) && ee.Sys().(syscall.WaitStatus).Signaled() &&
					ee.Sys().(syscall.WaitStatus).Signal() == syscall.SIGKILL
				if k < n && !killed {
					t.Fatalf("%s: the child was not killed: %v\n%s", what, err, out)
				}
				if k == n && !killed {
					t.Fatalf("%s (the last write): %v\n%s", what, err, out)
				}
				cs := u.cutCheck(p0, what)
				if k < n && cs.Boots != "keeper" {
					t.Fatalf("%s: %+v", what, cs)
				}
				u.resume(p0, cs, what)
				total++
			}
		})
	}
	t.Logf("device-mode kill sweep: %d kill points checked and resumed", total)
}

func jsonMarshal(p *plan.Plan) ([]byte, error) { return json.MarshalIndent(p, "", "  ") }
