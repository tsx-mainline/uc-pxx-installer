package flow

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"reflect"
	"strings"
	"testing"
	"time"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/plan"
)

// sector is an A/B sector with the slots {prio, tries, succ} and a tail.
func sector(a, b [3]byte, tail byte) []byte {
	s := make([]byte, 512)
	copy(s, []byte{0, 'A', 'B', '0', 1, 0})
	copy(s[8:], []byte{a[0], a[1], a[2], 0, b[0], b[1], b[2], 0})
	binary.BigEndian.PutUint32(s[28:], crc32.ChecksumIEEE(s[:28]))
	for i := 32; i < 512; i++ {
		s[i] = tail
	}
	return s
}

func raw32(s []byte) [32]byte {
	var r [32]byte
	copy(r[:], s)
	return r
}

// testPlan is a plan with keeper a, target b: S0 A{15,0,1} B{14,0,1},
// D1 A{15,0,1} B{0,0,0}, D2 A{14,0,1} B{15,7,0}.
func testPlan(before []byte) *plan.Plan {
	s0 := raw32(before)
	d1 := raw32(sector([3]byte{15, 0, 1}, [3]byte{0, 0, 0}, 0))
	d2 := raw32(sector([3]byte{14, 0, 1}, [3]byte{15, 7, 0}, 0))
	sum := sha256.Sum256(before)
	return &plan.Plan{ID: "0123456789abcdef0123", Keeper: "a", Target: "b",
		AB: plan.AB{Now: plan.State(s0), Final: plan.State(d2), SectorSHA256: hex.EncodeToString(sum[:])},
		Steps: []plan.Step{
			{N: 1, Op: "bootctl", Bootctl: &plan.BootctlStep{Args: []string{"disable", "--slot", "b"}, Before: plan.State(s0), After: plan.State(d1)}},
			{N: 2, Op: "write", Write: &plan.WriteStep{Partition: "uboot_b", Length: 4 << 20}},
			{N: 3, Op: "write", Write: &plan.WriteStep{Partition: "system_b", Length: 2 << 30}},
			{N: 4, Op: "sync"},
			{N: 5, Op: "verify"},
			{N: 6, Op: "bootctl", Bootctl: &plan.BootctlStep{Args: []string{"arm-trying", "--slot", "b", "--lower-keeper"}, Before: plan.State(d1), After: plan.State(d2)}},
			{N: 7, Op: "check-ab"},
		}}
}

var (
	s0Sec = sector([3]byte{15, 0, 1}, [3]byte{14, 0, 1}, 0)
	d1Sec = sector([3]byte{15, 0, 1}, [3]byte{0, 0, 0}, 0)
	d2Sec = sector([3]byte{14, 0, 1}, [3]byte{15, 7, 0}, 0)
)

// TestClassifyKeepKeyFirst: a plan whose first step is the keep-key step
// (KEY-KEEP.md: backup, never the A/B sector), then the disable, classifies as
// the same plan without it. Anything else before the disable is not a plan of
// the device mode: hold.
func TestClassifyKeepKeyFirst(t *testing.T) {
	kk := plan.Step{N: 1, Op: "keep-key", KeepKey: &plan.KeepKeyStep{Partition: "backup"}}
	withKK := func(extra ...plan.Step) *plan.Plan {
		p := testPlan(s0Sec)
		p.Steps = append(append(extra, kk), p.Steps...)
		return p
	}
	ok0, sig := End{Exited: true}, End{Signal: "killed"}
	for _, c := range []struct {
		name   string
		after  []byte
		end    End
		screen Screen
		state  string
	}{
		{"exit 0, D2", d2Sec, ok0, ScreenDone, StateD2},
		{"SIGKILL, S0 (in the keep-key step)", s0Sec, sig, ScreenStopped, StateS0},
		{"SIGKILL, D1", d1Sec, sig, ScreenStopped, StateD1},
	} {
		if o := Classify(withKK(), s0Sec, c.after, nil, c.end); o.Screen != c.screen || o.State != c.state {
			t.Errorf("%s: screen %s state %s (%s), want %s %s", c.name, o.Screen, o.State, o.Why, c.screen, c.state)
		}
	}
	for name, p := range map[string]*plan.Plan{
		"two keep-key steps":         withKK(kk),
		"a write before the disable": withKK(plan.Step{N: 1, Op: "write", Write: &plan.WriteStep{Partition: "uboot_b"}}),
		"only keep-key":              {AB: testPlan(s0Sec).AB, Steps: []plan.Step{kk}},
		"keep-key, then the arm":     {AB: testPlan(s0Sec).AB, Steps: []plan.Step{kk, testPlan(s0Sec).Steps[5]}},
	} {
		if o := Classify(p, s0Sec, d2Sec, nil, ok0); o.Screen != ScreenHold || o.State != StateUnknown {
			t.Errorf("%s: %+v", name, o)
		}
	}
}

func TestClassify(t *testing.T) {
	p := testPlan(s0Sec)
	other := sector([3]byte{14, 0, 1}, [3]byte{15, 6, 0}, 0) // valid, but none of the plan's states
	tail := append([]byte(nil), d1Sec...)
	tail[300] = 1
	bad := append([]byte(nil), d1Sec...)
	bad[29] ^= 0xff // CRC
	ok0, ex1, ex2, ex3 := End{Exited: true}, End{Exited: true, Code: 1}, End{Exited: true, Code: 2}, End{Exited: true, Code: 3}
	sig, lost := End{Signal: "killed"}, End{Lost: true}
	for _, c := range []struct {
		name   string
		after  []byte
		err    error
		end    End
		screen Screen
		state  string
	}{
		{"exit 0, D2", d2Sec, nil, ok0, ScreenDone, StateD2},
		{"exit 0, D1 (disagree)", d1Sec, nil, ok0, ScreenHold, StateD1},
		{"exit 0, S0 (disagree)", s0Sec, nil, ok0, ScreenHold, StateS0},
		{"exit 1, S0", s0Sec, nil, ex1, ScreenStopped, StateS0},
		{"exit 1, D1", d1Sec, nil, ex1, ScreenStopped, StateD1},
		{"exit 1, D2", d2Sec, nil, ex1, ScreenStopped, StateD2},
		{"exit 1, another state", other, nil, ex1, ScreenHold, StateUnknown},
		{"exit 1, tail changed", tail, nil, ex1, ScreenHold, StateUnknown},
		{"exit 1, bad CRC", bad, nil, ex1, ScreenHold, StateUnknown},
		{"exit 1, unreadable", nil, errors.New("EIO"), ex1, ScreenHold, StateUnknown},
		{"exit 2, S0", s0Sec, nil, ex2, ScreenStopped, StateS0},
		{"exit 2 (a Go panic), D1", d1Sec, nil, ex2, ScreenStopped, StateD1},
		{"exit 3, D1: always hold", d1Sec, nil, ex3, ScreenHold, StateD1},
		{"exit 3, unknown", other, nil, ex3, ScreenHold, StateUnknown},
		{"SIGKILL, D1", d1Sec, nil, sig, ScreenStopped, StateD1},
		{"SIGKILL, D2", d2Sec, nil, sig, ScreenStopped, StateD2},
		{"SIGKILL, unknown", other, nil, sig, ScreenHold, StateUnknown},
		{"lost, S0", s0Sec, nil, lost, ScreenStopped, StateS0},
		{"lost, unknown", other, nil, lost, ScreenHold, StateUnknown},
	} {
		o := Classify(p, s0Sec, c.after, c.err, c.end)
		if o.Screen != c.screen || o.State != c.state {
			t.Errorf("%s: screen %s state %s (%s), want %s %s", c.name, o.Screen, o.State, o.Why, c.screen, c.state)
		}
	}
	// the sector before the install must be the plan's
	o := Classify(p, d1Sec, d1Sec, nil, ex1)
	if o.Screen != ScreenHold || !strings.Contains(o.Why, "not the one of the plan") {
		t.Errorf("a wrong sector before: %+v", o)
	}
	// a target that was already disabled: S0 = D1
	p2 := testPlan(d1Sec)
	if st, _ := StateOf(p2, d1Sec, d1Sec, nil); st != StateS0 {
		t.Errorf("S0 = D1: %s", st)
	}
}

// reestablishPlan is a keeper-reestablish plan (keeper a, target b): the
// BCB-route both-trying reset S0 A{15,6,0} B{14,7,0}; R(K) makes the keeper A
// successful {15,0,1} and the target {0,0,0} (= the no-op disable's ab_after,
// here d1Sec), then the slot-b writes, then the arm to D2 A{14,0,1} B{15,7,0}.
// This is the plan shape that falsely reported exit-3 "A/B state unknown"
// before the fix (the post-install check assumed the plan started with the
// disable step; a keeper-reestablish plan starts with the R(K) repair step).
func reestablishPlan(before []byte) *plan.Plan {
	s0 := raw32(before)
	rk := raw32(d1Sec) // R(K) / the no-op disable state: A{15,0,1} B{0,0,0}
	d2 := raw32(d2Sec)
	sum := sha256.Sum256(before)
	return &plan.Plan{ID: "0123456789abcdef0123", Keeper: "a", Target: "b",
		AB: plan.AB{Now: plan.State(s0), Final: plan.State(d2), SectorSHA256: hex.EncodeToString(sum[:])},
		Steps: []plan.Step{
			{N: 1, Op: "bootctl", Bootctl: &plan.BootctlStep{Args: []string{"repair", "--keeper", "a", "--expect-sector", hex.EncodeToString(before)}, Before: plan.State(s0), After: plan.State(rk)}},
			{N: 2, Op: "bootctl", Bootctl: &plan.BootctlStep{Args: []string{"disable", "--slot", "b"}, Before: plan.State(rk), After: plan.State(rk), NoOp: true}},
			{N: 3, Op: "write", Write: &plan.WriteStep{Partition: "uboot_b", Length: 4 << 20}},
			{N: 4, Op: "write", Write: &plan.WriteStep{Partition: "system_b", Length: 2 << 30}},
			{N: 5, Op: "sync"},
			{N: 6, Op: "verify"},
			{N: 7, Op: "bootctl", Bootctl: &plan.BootctlStep{Args: []string{"arm-trying", "--slot", "b", "--lower-keeper"}, Before: plan.State(rk), After: plan.State(d2)}},
			{N: 8, Op: "check-ab"},
		}}
}

// TestClassifyReestablish: a keeper-reestablish plan (R(K) first) classifies the
// same way a disable-first plan does. The bug R4 caught on hardware was that a
// successful keeper-reestablish install (exit 0, the final D2 sector) falsely
// held with "A/B state unknown". After the fix exit 0 with the final sector is
// Done; the intermediate R(K)/disable state and the both-trying start are known
// stop states; and the genuine exit-3 safety (a real mismatch, or any exit 3)
// still holds.
func TestClassifyReestablish(t *testing.T) {
	reS0 := sector([3]byte{15, 6, 0}, [3]byte{14, 7, 0}, 0) // the both-trying reset, before R(K)
	rkSec := d1Sec                                          // after R(K) / the no-op disable: A{15,0,1} B{0,0,0}
	p := reestablishPlan(reS0)
	other := sector([3]byte{14, 0, 1}, [3]byte{15, 6, 0}, 0) // valid, but none of the plan's states
	ok0, ex1, ex3 := End{Exited: true}, End{Exited: true, Code: 1}, End{Exited: true, Code: 3}
	sig := End{Signal: "killed"}
	for _, c := range []struct {
		name   string
		after  []byte
		end    End
		screen Screen
		state  string
	}{
		// the real bug: a successful keeper-reestablish install is Done, not a hold.
		{"exit 0, D2 (success): Done, not exit-3 unknown", d2Sec, ok0, ScreenDone, StateD2},
		// the intermediate R(K)/disable state and the start are known stop states.
		{"SIGKILL after R(K) (target disabled): D1", rkSec, sig, ScreenStopped, StateD1},
		{"exit 1 after R(K): D1", rkSec, ex1, ScreenStopped, StateD1},
		{"SIGKILL before R(K) (both trying): S0", reS0, sig, ScreenStopped, StateS0},
		{"exit 1 before R(K): S0", reS0, ex1, ScreenStopped, StateS0},
		{"exit 1, D2", d2Sec, ex1, ScreenStopped, StateD2},
		// the genuine exit-3 safety is not weakened: a real mismatch still holds.
		{"exit 0, a different valid state (mismatch): hold", other, ok0, ScreenHold, StateUnknown},
		{"exit 1, a different valid state: hold", other, ex1, ScreenHold, StateUnknown},
		{"exit 3, D2: always hold", d2Sec, ex3, ScreenHold, StateD2},
	} {
		if o := Classify(p, reS0, c.after, nil, c.end); o.Screen != c.screen || o.State != c.state {
			t.Errorf("%s: screen %s state %s (%s), want %s %s", c.name, o.Screen, o.State, o.Why, c.screen, c.state)
		}
	}
	// the full shape [keep-key, R(K), disable, ...] is also recognized.
	kk := plan.Step{N: 1, Op: "keep-key", KeepKey: &plan.KeepKeyStep{Partition: "backup"}}
	pKK := reestablishPlan(reS0)
	pKK.Steps = append([]plan.Step{kk}, pKK.Steps...)
	if o := Classify(pKK, reS0, d2Sec, nil, ok0); o.Screen != ScreenDone || o.State != StateD2 {
		t.Errorf("keep-key + R(K) + disable, exit 0 D2: screen %s state %s (%s)", o.Screen, o.State, o.Why)
	}
}

// drive runs a model through keys and events.
type drive struct {
	t   *testing.T
	m   *Model
	now time.Time
}

func newDrive(t *testing.T, terminal bool, au string) *drive {
	m := New("test", terminal)
	u := Unit{ABValid: true, Next: 0, AutoUpdate: AutoUpdateView{State: au, URL: "http://example.invalid"}}
	u.Slots[0] = SlotView{Name: "a", AB: avbab.Slot{Priority: 15, Successful: 1}, Summary: "mainline"}
	u.Slots[1] = SlotView{Name: "b", AB: avbab.Slot{Priority: 14, Successful: 1}, Summary: "1.0.0.94 userdebug"}
	m.SetUnit(u)
	return &drive{t: t, m: m, now: time.Unix(1000, 0)}
}

// Every screen that a test reaches through press, hold or want must fit
// the panel (checkFit, layout_test.go).
func (d *drive) press(k Key) {
	d.t.Helper()
	d.m.Handle(Event{k, true}, d.now)
	checkFit(d.t, d.m, d.now, "after a key")
	d.m.Handle(Event{k, false}, d.now)
}

func (d *drive) hold(dur time.Duration) {
	d.t.Helper()
	d.m.Handle(Event{KeyOK, true}, d.now)
	d.now = d.now.Add(dur)
	d.m.Tick(d.now)
	d.m.Handle(Event{KeyOK, false}, d.now)
	checkFit(d.t, d.m, d.now, "after a hold")
}

func (d *drive) want(s Screen) {
	d.t.Helper()
	checkFit(d.t, d.m, d.now, "want "+s.String())
	if d.m.Screen != s {
		d.t.Fatalf("screen %s, want %s (msg %q)", d.m.Screen, s, d.m.Msg)
	}
}

func (d *drive) acts() []ActionKind {
	var k []ActionKind
	for _, a := range d.m.Take() {
		k = append(k, a.Kind)
	}
	return k
}

func (d *drive) wantActs(want ...ActionKind) []Action {
	d.t.Helper()
	got := d.m.Take()
	var k []ActionKind
	for _, a := range got {
		k = append(k, a.Kind)
	}
	if !reflect.DeepEqual(k, want) && !(len(k) == 0 && len(want) == 0) {
		d.t.Fatalf("actions %v, want %v", k, want)
	}
	return got
}

// toChecklist (the name of earlier rounds): Status → Install → the
// firmware screen (auto-update off; the checklist is gone since flow-r3).
func (d *drive) toChecklist() {
	d.t.Helper()
	d.press(KeyNext) // Install
	d.press(KeyOK)
	d.want(ScreenFirmware)
}

// confirmAll: nothing to confirm any more (kept for the old call sites).
func (d *drive) confirmAll() {}

func TestStatusMenuAndBack(t *testing.T) {
	d := newDrive(t, false, "off")
	d.want(ScreenStatus)
	d.press(KeyPrev) // wraps to Restart
	d.press(KeyOK)
	d.want(ScreenRestart)
	d.press(KeyBack)
	d.want(ScreenStatus)
	d.m.Sel = 3 // Log
	d.press(KeyOK)
	d.want(ScreenLog)
	d.press(KeyBack)
	d.want(ScreenStatus)
	d.m.Sel = 0
	d.toChecklist()
	d.press(KeyBack)
	d.want(ScreenStatus)
	d.wantActs()
}

// TestInstallGates: the Install item of the status screen (owner,
// 2026-10-31): auto-update off goes on; on, and not clear, are refused
// with a plain reason, with no confirmation; an invalid slot table is
// refused. --prereqs-confirmed comes from choosing Install;
// --autoupdate-unclear-confirmed is never given.
func TestInstallGates(t *testing.T) {
	for _, c := range []struct {
		au, msg string
	}{
		{"on", "Crestron auto-update is on. Turn it off in the Crestron web UI, then start the installer again."},
		{"unknown", "The installer could not read the Crestron auto-update setting. Check that it is off in the web UI, then start the installer again; if this repeats, open an issue."},
	} {
		d := newDrive(t, false, c.au)
		d.press(KeyNext)
		for i := 0; i < 3; i++ { // OK again does not get past it
			d.press(KeyOK)
			d.want(ScreenStatus)
			if d.m.Msg != c.msg || d.m.PrereqsConfirmed() || d.m.UnclearConfirmed() {
				t.Fatalf("%s: %q", c.au, d.m.Msg)
			}
		}
		d.wantActs()
	}
	d := newDrive(t, false, "off")
	d.toChecklist()
	if !d.m.PrereqsConfirmed() || d.m.UnclearConfirmed() {
		t.Fatal("off: prereqs or unclear wrong")
	}
	d = newDrive(t, false, "off")
	d.m.Unit.ABValid, d.m.Unit.ABErr = false, "crc32 mismatch"
	d.press(KeyNext)
	d.press(KeyOK)
	d.want(ScreenStatus)
	if d.m.Msg != "The slot table of this unit cannot be read, so you cannot install. See the log." {
		t.Fatal(d.m.Msg)
	}
}

// toPlanning: Install → firmware → eject → read → plan child.
func (d *drive) toPlanning() {
	d.toChecklist()
	d.confirmAll()
	d.want(ScreenFirmware)
	d.wantActs()
	d.press(KeyOK) // force the eject
	d.wantActs(ActEject)
	d.m.DriveEjected(true)
	d.wantActs(ActReadDrive)
	d.m.DriveRead(DriveResult{OK: true, Name: `"fw.bin"`, Size: 973, Ident: "size=1,mtime=1,ctime=1,dev=1,ino=1"})
	if d.m.Screen == ScreenPanel {
		d.press(KeyOK) // yes, the screen shows on the panel
		if d.m.Terminal {
			// a terminal: y after the warning was drawn PanelWarnWait ago
			d.m.Drawn(d.now)
			d.now = d.now.Add(PanelWarnWait)
			d.press(KeyYes)
		}
	}
	d.want(ScreenPlanning)
	a := d.wantActs(ActStartPlan)
	if a[0].Keeper != "" {
		d.t.Fatalf("a keeper %q before the planner asked", a[0].Keeper)
	}
}

func TestFirmwareAndPlan(t *testing.T) {
	t.Run("the host ejects", func(t *testing.T) {
		d := newDrive(t, false, "off")
		d.toChecklist()
		d.confirmAll()
		d.m.DriveEjected(true)
		d.wantActs(ActReadDrive)
	})
	t.Run("the copy progress", func(t *testing.T) {
		d := newDrive(t, false, "off")
		d.toChecklist()
		d.confirmAll()
		for i := 0; i <= 60; i++ { // the poll sees the file change every second
			d.m.DriveWrites(d.now)
			d.now = d.now.Add(time.Second)
		}
		d.now = d.now.Add(-time.Second)
		found := false
		for _, l := range d.m.View(d.now).Body {
			found = found || l.Text == "The computer is writing to the drive: 1 min 00 s"
		}
		if !found {
			t.Fatal("no copy progress")
		}
	})
	t.Run("a refused drive goes back, dirty ones made new", func(t *testing.T) {
		d := newDrive(t, false, "off")
		d.toChecklist()
		d.confirmAll()
		d.m.DriveEjected(true)
		d.wantActs(ActReadDrive)
		d.m.DriveRead(DriveResult{Refusal: "two .bin", Remake: false})
		a := d.wantActs(ActGiveBackDrive)
		if a[0].Remake || d.m.Ejected {
			t.Fatal(a)
		}
		d.want(ScreenFirmware)
		d.m.DriveEjected(true)
		d.wantActs(ActReadDrive)
		d.m.DriveRead(DriveResult{Refusal: "dirty", Remake: true})
		if a := d.wantActs(ActGiveBackDrive); !a[0].Remake {
			t.Fatal("a dirty drive must be made new")
		}
	})
	t.Run("cancel on the firmware screen gives the drive back", func(t *testing.T) {
		d := newDrive(t, false, "off")
		d.toChecklist()
		d.confirmAll()
		d.press(KeyOK)
		d.wantActs(ActEject)
		d.m.DriveEjected(true)
		d.m.Take()
		d.press(KeyBack)
		d.wantActs(ActGiveBackDrive)
		d.want(ScreenStatus)
	})
	t.Run("plan, then the 3 s hold starts the install", func(t *testing.T) {
		d := newDrive(t, false, "off")
		d.toPlanning()
		d.m.PlanEnded(PlanResult{Plan: testPlan(s0Sec)})
		d.want(ScreenPlan)
		d.hold(2 * time.Second) // too short
		d.want(ScreenPlan)
		d.wantActs()
		d.press(KeyNext)
		d.hold(HoldOK)
		d.want(ScreenProgress)
		d.wantActs(ActStartInstall)
		if d.m.RestartBlocked() == "" {
			t.Fatal("restart allowed during the install")
		}
	})
	t.Run("terminal: Enter, then y", func(t *testing.T) {
		d := newDrive(t, true, "off")
		d.toPlanning()
		d.m.PlanEnded(PlanResult{Plan: testPlan(s0Sec)})
		d.press(KeyYes) // y alone does nothing
		d.want(ScreenPlan)
		d.press(KeyOK)
		d.press(KeyNext) // another key cancels the y
		d.press(KeyYes)
		d.want(ScreenPlan)
		d.press(KeyOK)
		d.press(KeyYes)
		d.want(ScreenProgress)
		d.wantActs(ActStartInstall)
	})
	t.Run("back on the plan screen cancels", func(t *testing.T) {
		d := newDrive(t, false, "off")
		d.toPlanning()
		d.m.PlanEnded(PlanResult{Plan: testPlan(s0Sec)})
		d.press(KeyBack)
		d.want(ScreenStatus)
		d.wantActs(ActGiveBackDrive)
		if !strings.Contains(d.m.Msg, "Nothing was written") {
			t.Fatal(d.m.Msg)
		}
	})
	t.Run("back while the plan runs stops the plan child", func(t *testing.T) {
		d := newDrive(t, false, "off")
		d.toPlanning()
		d.press(KeyBack)
		d.wantActs(ActCancelPlan)
		d.m.PlanEnded(PlanResult{Canceled: true})
		d.want(ScreenStatus)
		d.wantActs(ActGiveBackDrive)
	})
	t.Run("a plan refusal: nothing written", func(t *testing.T) {
		d := newDrive(t, false, "off")
		d.toPlanning()
		d.m.PlanEnded(PlanResult{Refusal: "install refused: vbmeta-key: no match", End: End{Exited: true, Code: 1}})
		d.want(ScreenRefused)
		d.wantActs(ActGiveBackDrive)
		d.press(KeyOK)
		d.want(ScreenFirmware)
	})
}

// TestKeeperScreen: the target screen ("Install to which slot?"), only
// when the planner asks; the choice is the slot to install to, and the
// other slot is kept (it must be working).
func TestKeeperScreen(t *testing.T) {
	t.Run("only when the planner asks", func(t *testing.T) {
		d := newDrive(t, false, "off")
		d.toPlanning() // no target screen before the plan
		d.m.PlanEnded(PlanResult{Refusal: "install refused: keeper: both slots are successful and the pre-fastboot A/B sector is not known; choose the keeper"})
		d.want(ScreenTarget)
		if d.m.Sel != 1 {
			t.Fatalf("the slot that does not start now (B) is not selected: %d", d.m.Sel)
		}
		v := d.m.View(d.now)
		var text []string
		for _, l := range v.Body {
			text = append(text, l.Text)
		}
		all := strings.Join(text, "\n")
		for _, w := range []string{"  Slot A: now mainline  (starts next)", "> Slot B: now 1.0.0.94 userdebug",
			"      Install here: slot B is replaced; slot A is not touched and stays the fallback.",
			"Both slots work. Choose the one to replace; the other stays as the fallback."} {
			if !strings.Contains(all, w) {
				t.Fatalf("no %q on the target screen:\n%s", w, all)
			}
		}
		for _, bad := range []string{"keep", "Keep", "keeper", "pre-fastboot"} {
			if strings.Contains(all, bad) {
				t.Fatalf("the target screen says %q:\n%s", bad, all)
			}
		}
		d.press(KeyNext) // to A
		d.press(KeyOK)
		a := d.wantActs(ActStartPlan)
		if a[0].Keeper != "b" {
			t.Fatalf("keeper %q", a[0].Keeper)
		}
		d.want(ScreenPlanning)
		// the planner refuses again: no second target screen
		d.m.PlanEnded(PlanResult{Refusal: "install refused: keeper: slot b must be successful"})
		d.want(ScreenRefused)
	})
	t.Run("the next slot is trying: it cannot be kept, so it cannot be chosen away", func(t *testing.T) {
		d := newDrive(t, false, "off")
		d.m.Unit.Slots[1].AB = avbab.Slot{Priority: 15, Tries: 6}
		d.m.Unit.Next = 1
		d.toPlanning()
		d.m.PlanEnded(PlanResult{Refusal: "install refused: keeper: slot b must be successful {prio>=1, tries 0, succ 1}: {prio 15 tries 6 succ 0} trying"})
		d.want(ScreenTarget)
		if d.m.Sel != 0 {
			t.Fatal("not on the slot that does not start now")
		}
		d.press(KeyOK) // install to A: B (trying) would be the fallback: refused
		d.want(ScreenTarget)
		if d.m.Msg != "You cannot install to slot A: slot B is not tested yet (6 tries left) and cannot be the fallback." {
			t.Fatal(d.m.Msg)
		}
		d.wantActs()
		v := d.m.View(d.now)
		found := false
		for _, l := range v.Body {
			found = found || l.Text == "      You cannot choose this slot: slot B is not tested yet (6 tries left) and cannot be the fallback."
		}
		if !found {
			t.Fatalf("no reason row: %q", rowsOf(v))
		}
		if !hasRow(v, "Slot B is not tested yet. You can install only to slot B; slot A stays as the fallback.") {
			t.Fatalf("the bottom line: %q", rowsOf(v))
		}
		d.press(KeyNext) // B: replaces the trying slot, A (working) stays
		d.press(KeyOK)
		if a := d.wantActs(ActStartPlan); a[0].Keeper != "a" {
			t.Fatal(a)
		}
	})
	t.Run("other refusals do not ask", func(t *testing.T) {
		if ok, _ := keeperRefusal("install refused: autoupdate: on"); ok {
			t.Fatal("asked")
		}
		if ok, _ := keeperRefusal("install refused: gpt: x; keeper: both slots have priority 15: the installer never relies on the tie rule; choose the keeper"); !ok {
			t.Fatal("a tie did not ask")
		}
	})
}

// toProgress runs to the progress screen.
func (d *drive) toProgress() {
	d.toPlanning()
	d.m.PlanEnded(PlanResult{Plan: testPlan(s0Sec)})
	d.hold(HoldOK)
	d.want(ScreenProgress)
	d.wantActs(ActStartInstall)
}

func TestEnds(t *testing.T) {
	p := testPlan(s0Sec)
	for _, c := range []struct {
		name  string
		after []byte
		end   End
		want  Screen
	}{
		{"done", d2Sec, End{Exited: true}, ScreenDone},
		{"stopped D1", d1Sec, End{Exited: true, Code: 1}, ScreenStopped},
		{"stopped S0 after a kill", s0Sec, End{Signal: "killed"}, ScreenStopped},
		{"exit 2 S0", s0Sec, End{Exited: true, Code: 2}, ScreenStopped},
		{"hold exit 3", d1Sec, End{Exited: true, Code: 3}, ScreenHold},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := newDrive(t, false, "off")
			d.toProgress()
			d.press(KeyBack) // does nothing during the install
			d.want(ScreenProgress)
			d.m.Sel = 4
			d.go2status()
			d.m.InstallEnded(Classify(p, s0Sec, c.after, nil, c.end))
			d.want(c.want)
			switch c.want {
			case ScreenDone:
				if d.m.RestartBlocked() != "" {
					t.Fatal("restart blocked after done")
				}
				d.hold(HoldOK)
				d.wantActs(ActRestart)
			case ScreenStopped:
				if d.m.RestartBlocked() != "" {
					t.Fatal("restart blocked after a stop")
				}
				d.press(KeyOK)
				d.want(ScreenFirmware)
			case ScreenHold:
				if d.m.RestartBlocked() == "" {
					t.Fatal("restart allowed in a hold")
				}
				// the repair check runs at the hold start; it refuses here
				d.wantActs(ActRepairCheck)
				d.m.ToolEnded(ToolResult{Tool: "repair", DryRun: true, End: End{Exited: true, Code: 1}, Result: "repair refused, nothing written: x"})
				// OK checks again with the repair's dry run (it reads only;
				// review R3 L-2): its refusal keeps the hold
				d.press(KeyOK)
				d.wantActs(ActRepairCheck)
				d.m.ToolEnded(ToolResult{Tool: "repair", DryRun: true, End: End{Exited: true, Code: 1}, Result: "repair refused, nothing written: x"})
				d.want(ScreenHold)
				v := d.m.View(d.now)
				all := ""
				for _, l := range v.Body {
					all += l.Text + "\n"
				}
				if !strings.Contains(all, "KEEP THE UNIT POWERED") || strings.Contains(all, "can be disconnected") {
					t.Fatalf("hold text:\n%s", all)
				}
				// the hold stays: the restart stays blocked, the menu Install comes back to it
				d.press(KeyBack)
				d.want(ScreenLog)
				d.press(KeyBack)
				d.want(ScreenHold)
				d.m.go2(ScreenStatus)
				d.m.Sel = 4
				d.press(KeyOK)
				d.want(ScreenRestart)
				d.hold(HoldOK)
				d.wantActs()
				d.press(KeyBack)
				d.m.Sel = 1
				d.press(KeyOK)
				d.want(ScreenHold)
			}
		})
	}
}

// go2status: while the install runs, the menu works, but Install comes back
// to the progress screen and Restart is blocked.
func (d *drive) go2status() {
	d.m.go2(ScreenStatus)
	d.m.Sel = 4
	d.press(KeyOK)
	d.want(ScreenRestart)
	d.hold(HoldOK)
	d.wantActs()
	if !strings.Contains(d.m.Msg, "blocked") {
		d.t.Fatalf("msg %q", d.m.Msg)
	}
	d.press(KeyBack)
	d.m.Sel = 1
	d.press(KeyOK)
	d.want(ScreenProgress)
}

func TestResume(t *testing.T) {
	p := testPlan(s0Sec)
	d := newDrive(t, false, "off")
	d.toProgress()
	per := d.m.Persist()
	if per.Running != "install" {
		t.Fatal(per)
	}
	// a new UI process
	m2 := New("test", false)
	m2.SetUnit(*d.m.Unit)
	m2.Resume(per, p)
	if m2.Screen != ScreenProgress || m2.RestartBlocked() == "" {
		t.Fatalf("resume: %s", m2.Screen)
	}
	m2.InstallEnded(Classify(p, s0Sec, d1Sec, nil, End{Exited: true, Code: 3}))
	per = m2.Persist()
	m3 := New("test", false)
	m3.Resume(per, p)
	if m3.Screen != ScreenHold || m3.RestartBlocked() == "" {
		t.Fatalf("a hold did not survive: %s", m3.Screen)
	}
}

func TestArgs(t *testing.T) {
	ps := Paths{Exe: "/usr/bin/p10inst", Drive: "/run/p10inst/drive/drive.img", DriveIdent: "size=1,mtime=2,ctime=3,dev=4,ino=5",
		PlanJSON: "/run/p10inst/flow/plan.json", Log: "/run/p10inst/flow/install.jsonl"}
	got := strings.Join(InstallArgs(ps, "b", true, false, true), " ")
	want := "/usr/bin/p10inst install --device /dev/mmcblk0 --fw-drive /run/p10inst/drive/drive.img --fw-drive-expect size=1,mtime=2,ctime=3,dev=4,ino=5 --plan /run/p10inst/flow/plan.json --yes --log /run/p10inst/flow/install.jsonl --keeper b --prereqs-confirmed --panel-confirmed"
	if got != want {
		t.Fatalf("install args\n%s\nwant\n%s", got, want)
	}
	got = strings.Join(PlanArgs(ps, "", true, true, true), " ")
	want = "/usr/bin/p10inst plan --device /dev/mmcblk0 --fw-drive /run/p10inst/drive/drive.img --fw-drive-expect size=1,mtime=2,ctime=3,dev=4,ino=5 --json /run/p10inst/flow/plan.json --prereqs-confirmed --panel-confirmed --autoupdate-unclear-confirmed"
	if got != want {
		t.Fatalf("plan args\n%s\nwant\n%s", got, want)
	}
	if Transcript(ps.Log) != "/run/p10inst/flow/install.out" {
		t.Fatal(Transcript(ps.Log))
	}
	ps.Keys = "/run/p10inst/flow/keys.txt"
	if got := strings.Join(PlanArgs(ps, "a", true, false, false), " "); !strings.HasSuffix(got, " --keys /run/p10inst/flow/keys.txt --keeper a --prereqs-confirmed") {
		t.Fatalf("plan args with a key file: %s", got)
	}
	for _, a := range InstallArgs(ps, "x", false, false, false) {
		if a == "--keeper" || a == "--prereqs-confirmed" || a == "--panel-confirmed" {
			t.Fatal("a bad keeper or no confirmation passed through")
		}
	}
}

func TestProgress(t *testing.T) {
	p := testPlan(s0Sec)
	t0 := time.Unix(0, 0)
	pr := NewProgress(true, t0)
	pr.SetPlan(StepsOf(p))
	tick := func(l string, s int) { pr.Line(l, t0.Add(time.Duration(s)*time.Second)) }
	if l := SafetyNote(pr, "a", "b"); !strings.Contains(l, "nothing is written yet") {
		t.Fatal(l)
	}
	tick("pass 1: check the whole firmware", 1)
	for _, part := range []string{"uboot", "trust", "dtbo", "vbmeta", "boot", "system", "vendor", "oem"} {
		tick("  pass 1: "+part+"   4194304 bytes  sha256 0123456789abcdef  matches", 100) // not a new computation
	}
	tick("  ok   payload  1.0.0.94 userdebug; pass 1: 8 of 8 partition images match the manifest", 200)
	if pr.Computes != 1 {
		t.Fatalf("computations after the first pass 1: %d, want 1", pr.Computes)
	}
	tick("computing the plan again from the unit and the firmware (nothing is written yet)", 250)
	if pr.Computes != 2 {
		t.Fatalf("computations after 'computing the plan again': %d, want 2", pr.Computes)
	}
	tick(" 1/7 bootctl disable --slot b", 512)
	if l := SafetyNote(pr, "a", "b"); !strings.Contains(l, "not safe") {
		t.Fatal(l)
	}
	tick(" 2/7 write uboot_b (4194304 bytes)", 513)
	if l := SafetyNote(pr, "a", "b"); !strings.Contains(l, "slot A starts (slot B is not bootable") {
		t.Fatal(l)
	}
	tick(" 3/7 write system_b (2147483648 bytes)", 514)
	f1 := pr.Fraction(t0.Add(600 * time.Second))
	tick(" 4/7 sync", 800)
	tick(" 5/7 verify", 801)
	tick(" 6/7 bootctl arm-trying --slot b --lower-keeper", 850)
	if l := SafetyNote(pr, "a", "b"); !strings.Contains(l, "not safe") {
		t.Fatal(l)
	}
	tick(" 7/7 check the A/B sector", 851)
	if l := SafetyNote(pr, "a", "b"); !strings.Contains(l, "slot B starts next, slot A is the fallback") {
		t.Fatal(l)
	}
	tick("     A/B A{14,0,1} B{15,7,0}, boots next: B (decoder and bootctl agree)", 852)
	tick("RESULT: install done in 5m38s", 853)
	if !pr.Agree || pr.Fraction(t0.Add(900*time.Second)) != 1 || f1 != 2.0/7 || pr.MBps() <= 0 {
		t.Fatalf("agree %v, f1 %.2f, MB/s %.1f", pr.Agree, f1, pr.MBps())
	}
	if pr.Computes != 2 || len(pr.Steps) != 7 || !pr.Steps[6].Done {
		t.Fatalf("%+v", pr)
	}
}

// TestTexts: no screen says that the unit turns off (poweroff restarts it,
// §4.5), and the hold never says "safe" by itself.
func TestTexts(t *testing.T) {
	p := testPlan(s0Sec)
	screens := map[string]Frame{}
	for _, c := range []struct {
		name  string
		after []byte
		end   End
	}{{"done", d2Sec, End{Exited: true}}, {"stopped", d1Sec, End{Exited: true, Code: 1}}, {"hold", d1Sec, End{Exited: true, Code: 3}}} {
		d := newDrive(t, false, "off")
		d.toProgress()
		screens["progress"] = d.m.View(d.now)
		d.m.InstallEnded(Classify(p, s0Sec, c.after, nil, c.end))
		screens[c.name] = d.m.View(d.now)
	}
	d := newDrive(t, false, "off")
	for _, s := range []Screen{ScreenStatus, ScreenRestart, ScreenLog} {
		d.m.go2(s)
		screens[s.String()] = d.m.View(d.now)
	}
	d.m.go2(ScreenStatus)
	d.toChecklist()
	screens["checklist"] = d.m.View(d.now)
	d.confirmAll()
	screens["firmware"] = d.m.View(d.now)
	for name, f := range screens {
		all := strings.ToLower(f.Title + "\n" + f.Footer + "\n" + f.Msg)
		for _, l := range f.Body {
			all += strings.ToLower(l.Text) + "\n"
		}
		for _, bad := range []string{"turns off", "turn off the unit", "switches off", "powers off"} {
			if strings.Contains(all, bad) {
				t.Errorf("%s says %q:\n%s", name, bad, all)
			}
		}
		if name == "hold" && (strings.Contains(all, "it is safe to disconnect") || strings.Contains(all, "safe to disconnect the power:") || f.LED != "red") {
			t.Errorf("the hold screen says it is safe, or its LED is not red:\n%s", all)
		}
	}
	if screens["done"].LED != "green" || screens["stopped"].LED != "red" || screens["progress"].LED != "blue" {
		t.Error("LEDs")
	}
}

// TestContact: the contact of the hold and of README.txt (U1): the GitHub
// issues of uc-pxx-installer, with the URL from the one constant IssuesURL.
func TestContact(t *testing.T) {
	if got := contactText("", "that folder"); got != "Open an issue in the uc-pxx-installer project on GitHub and attach that folder." {
		t.Fatalf("%q", got)
	}
	u := "https://github.example/o/uc-pxx-installer/issues"
	if got := contactText(u, "this folder"); got != "Open an issue at "+u+" and attach this folder." {
		t.Fatalf("%q", got)
	}
	if IssuesURL != "https://github.com/tsx-mainline/uc-pxx-installer/issues" ||
		Contact != "Open an issue at https://github.com/tsx-mainline/uc-pxx-installer/issues and attach that folder." {
		t.Fatalf("the contact (owner 2026-10-30): %q", Contact)
	}
	if len(Contact) > PanelCols || len(BundleContact) > PanelCols {
		t.Fatalf("the contact is wider than the screen: %d", len(Contact))
	}
	if Contact != contactText(IssuesURL, "that folder") || BundleContact != contactText(IssuesURL, "this folder") {
		t.Fatal("Contact and BundleContact must come from IssuesURL")
	}
	p := testPlan(s0Sec)
	d := newDrive(t, false, "off")
	d.toProgress()
	d.m.InstallEnded(Classify(p, s0Sec, d1Sec, nil, End{Exited: true, Code: 3}))
	f := d.m.View(d.now)
	found := false
	for _, l := range f.Body {
		found = found || strings.HasSuffix(l.Text, ". "+Contact)
	}
	if d.m.Screen != ScreenHold || !found {
		t.Fatalf("the hold screen does not show the contact: %+v", f.Body)
	}
}
