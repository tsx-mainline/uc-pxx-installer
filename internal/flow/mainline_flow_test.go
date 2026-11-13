package flow

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/plan"
)

// The two drive phases and the direct path in the flow (owner O1/O8,
// MAINLINE-FLOW-2026-11-10.md): step 1 (the stash child in place of the
// plan child, the swap of the drive), the choice of the path by the slot
// state, the direct path's screens, its hold without a repair.

// The A/B states of a direct install: the BCB-route reset (both trying, A
// 15), the state after R(K) (A {15,0,1}, B {0,0,0}; the disable after it is
// a no-op, so it is D1), and the end (B armed, A the successful fallback).
var (
	dS0 = sector([3]byte{15, 6, 0}, [3]byte{14, 7, 0}, 0)
	dRK = sector([3]byte{15, 0, 1}, [3]byte{0, 0, 0}, 0)
	dD2 = sector([3]byte{14, 0, 1}, [3]byte{15, 7, 0}, 0)
)

// dTestPlan is the embedded Plan of a direct plan (plan.ComputeMainlineDirect):
// kind MainlineDirectKind, keeper a, target b; keep-key, R(K), the disable,
// the six writes of B, sync, verify, the arm, check-ab.
func dTestPlan() *plan.Plan {
	s0, rk, d2 := raw32(dS0), raw32(dRK), raw32(dD2)
	sum := sha256.Sum256(dS0)
	w := func(n int, part string, l int64) plan.Step {
		return plan.Step{N: n, Op: "write", Write: &plan.WriteStep{Partition: part, Length: l, Action: "write"}}
	}
	return &plan.Plan{Schema: 1, Kind: plan.MainlineDirectKind, ID: strings.Repeat("d1ec", 16), Mode: "device", Keeper: "a", Target: "b",
		AB:     plan.AB{Now: plan.State(s0), Final: plan.State(d2), SectorSHA256: hex.EncodeToString(sum[:])},
		Checks: []plan.Check{{ID: plan.TargetNowCheck, OK: true, Detail: "slot B holds the Crestron firmware 1.0.4.22 (user) (uboot, trust, dtbo, vbmeta and boot as its manifest); it is replaced by mainline Linux; slot A stays the fallback (the install needs the person's confirmation of it, --replace-target-confirmed)"}},
		Steps: []plan.Step{
			{N: 1, Op: "keep-key", KeepKey: &plan.KeepKeyStep{Partition: "backup", LBAs: [2]int64{477184, 479231}}},
			{N: 2, Op: "bootctl", Bootctl: &plan.BootctlStep{Args: []string{"repair", "--keeper", "a", "--expect-sector", "00"}, Before: plan.State(s0), After: plan.State(rk)}},
			{N: 3, Op: "bootctl", Bootctl: &plan.BootctlStep{Args: []string{"disable", "--slot", "b"}, Before: plan.State(rk), After: plan.State(rk)}},
			w(4, "uboot_b", 4<<20), w(5, "trust_b", 4<<20), w(6, "dtbo_b", 1<<20), w(7, "vbmeta_b", 4096), w(8, "boot_b", 30<<20), w(9, "system_b", 900<<20),
			{N: 10, Op: "sync"},
			{N: 11, Op: "verify"},
			{N: 12, Op: "bootctl", Bootctl: &plan.BootctlStep{Args: []string{"arm-trying", "--slot", "b", "--lower-keeper"}, Before: plan.State(rk), After: plan.State(d2)}},
			{N: 13, Op: "check-ab"},
		}}
}

// dLockPlan is dTestPlan of a locked unit: the in-RAM reader read 0x00, so
// the plan holds the lock-state write intent and its check
// (LockWriteCheck), with another id.
func dLockPlan() *plan.Plan {
	p := dTestPlan()
	p.ID = strings.Repeat("10c4", 16)
	p.Checks = append(p.Checks, plan.Check{ID: LockWriteCheck, OK: true, Detail: "the keeper slot A is a proven Crestron stock slot"})
	return p
}

// mlStashDrive is the runner's read of a step-1 drive.
var mlStashDrive = MainlineDrive{OK: true, Stash: true, FWName: `"UC-P10 1.0.0.94 userdebug.bin"`, FWSize: 972963471,
	Ident: "size=2,mtime=2,ctime=2,dev=2,ino=2"}

// newDDrive is newDrive on a factory-stock unit after the BCB-route entry
// (the libavb reset: both slots trying, A 15).
func newDDrive(t *testing.T, terminal bool) *drive {
	d := newMLDrive(t, terminal)
	u := *d.m.Unit
	u.Next = 0
	u.Slots[0] = SlotView{Name: "a", AB: avbab.Slot{Priority: 15, Tries: 6}, Summary: "Crestron 1.0.4.22 (user)"}
	u.Slots[1] = SlotView{Name: "b", AB: avbab.Slot{Priority: 14, Tries: 7}, Summary: "Crestron 1.0.4.22 (user)"}
	d.m.SetUnit(u)
	return d
}

// toStepOne: Status → Install → the eject → the read of a step-1 drive →
// the stash child (the plan action) on the planning screen.
func (d *drive) toStepOne() {
	d.t.Helper()
	d.toChecklist()
	d.press(KeyOK) // eject
	d.wantActs(ActEject)
	d.m.DriveEjected(true)
	d.wantActs(ActReadDrive)
	d.m.MainlineDriveRead(mlStashDrive)
	d.want(ScreenPlanning)
	a := d.wantActs(ActStartPlan)
	if a[0].Keeper != "" || a[0].Unclear || !d.m.MainlineStashing() || d.m.Running() != "plan" || d.m.Mainline() == false {
		d.t.Fatalf("step 1: action %+v, stashing %v, running %q", a[0], d.m.MainlineStashing(), d.m.Running())
	}
}

func bodyText(f Frame) string {
	var b strings.Builder
	for _, l := range f.Body {
		b.WriteString(l.Text + "\n")
	}
	return f.Title + "\n" + b.String()
}

func TestMainlineStepOne(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "panel keys", true: "terminal"}[terminal], func(t *testing.T) {
			d := newMLDrive(t, terminal)
			d.toStepOne()
			v := bodyText(d.m.View(d.now))
			for _, want := range []string{txtMLStashTitle, txtMLStashWhat, txtMLStashNothing, `"UC-P10 1.0.0.94 userdebug.bin" (972 MB)`, "Starting ..."} {
				if !strings.Contains(v, want) {
					t.Fatalf("no %q on the step-1 screen:\n%s", want, v)
				}
			}
			if d.m.RestartBlocked() == "" {
				t.Fatal("the restart is not blocked while step 1 runs")
			}
			// the stash child's lines move the rows as the plan child's do
			d.m.ChildStarted(NewProgress(false, d.now.Add(-30*time.Second)))
			if v := bodyText(d.m.View(d.now)); !strings.Contains(v, ">> Finding the key") {
				t.Fatalf("rows at the start:\n%s", v)
			}
			d.m.ChildLine("key search: system_b: exactly one key/IV pair", d.now)
			d.m.ChildLine("pass 1: check the whole firmware against the manifests (nothing is written)", d.now)
			for _, p := range plan.SlotParts {
				d.m.ChildLine("  pass 1: "+p+" 4096 bytes sha256 matches", d.now)
			}
			if v := bodyText(d.m.View(d.now)); !strings.Contains(v, "ok Checking the firmware (sha256)") || !strings.Contains(v, ">> "+txtMLStashKeep) {
				t.Fatalf("rows after pass 1:\n%s", v)
			}
			// the stash: the drive made new and empty, the swap view
			d.m.StashEnded(StashResult{OK: true, End: End{Exited: true}})
			d.want(ScreenFirmware)
			a := d.wantActs(ActGiveBackDrive)
			if !a[0].Remake || d.m.Ejected || d.m.Running() != "" || d.m.Drive != nil {
				t.Fatalf("after step 1: %+v, ejected %v, running %q", a[0], d.m.Ejected, d.m.Running())
			}
			v = bodyText(d.m.View(d.now))
			for _, want := range []string{txtMLSwapTitle, "Step 1 is done", `"UC-P10 1.0.0.94 userdebug.bin"`, txtMLSwapCopy, txtMLSwapEmpty,
				txtMLSwapPower, txtFirmwareEject, txtFirmwareWait} {
				if !strings.Contains(v, want) {
					t.Fatalf("no %q on the swap screen:\n%s", want, v)
				}
			}
			if d.m.RestartBlocked() != "" {
				t.Fatalf("the swap screen blocks the restart: %s", d.m.RestartBlocked())
			}
			// the host ejects the drive with the folder: the bundle read, step 2
			d.m.DriveEjected(true)
			d.wantActs(ActReadDrive)
			if v := bodyText(d.m.View(d.now)); !strings.Contains(v, "Reading the drive ...") {
				t.Fatalf("reading:\n%s", v)
			}
			r := mlDriveOK
			r.StashDone = true
			d.m.MainlineDriveRead(r)
			d.want(ScreenUnlock)
			if d.m.MainlineDirect() || d.m.MainlineStashing() {
				t.Fatal("step 2 of the userdebug-first unit: not the direct path, no stash child")
			}
			d.unlockYes()
			d.want(ScreenPanel)
			d.panelYes()
			d.want(ScreenPlanning)
			d.wantActs(ActStartPlan)
			if d.m.MainlineStashing() {
				t.Fatal("the plan child of step 2 is not the stash child")
			}
		})
	}
	t.Run("cancel", func(t *testing.T) {
		d := newMLDrive(t, false)
		d.toStepOne()
		d.press(KeyBack)
		d.wantActs(ActCancelPlan)
		d.m.StashEnded(StashResult{Canceled: true, End: End{Signal: "terminated"}})
		d.want(ScreenStatus)
		d.wantActs(ActGiveBackDrive)
		if d.m.Msg != "Canceled. Nothing was written." || d.m.Mainline() {
			t.Fatalf("msg %q, mainline %v", d.m.Msg, d.m.Mainline())
		}
	})
	t.Run("refused", func(t *testing.T) {
		d := newMLDrive(t, false)
		d.toStepOne()
		raw := "the firmware 1.0.6.20 is a user build with a user U-Boot: a mainline install needs the userdebug boot chain (a user U-Boot forces locked)"
		d.m.StashEnded(StashResult{Refusal: raw, End: End{Exited: true, Code: 1}})
		d.want(ScreenRefused)
		a := d.wantActs(ActGiveBackDrive)
		if a[0].Remake {
			t.Fatal("a refused .bin is given back as it is")
		}
		v := bodyText(d.m.View(d.now))
		if !strings.Contains(v, "Why: The .bin is not the 1.0.0.94 userdebug firmware.") || !strings.Contains(v, "Details: "+raw) ||
			!strings.Contains(v, txtRefusedNote) {
			t.Fatalf("refused:\n%s", v)
		}
		d.press(KeyOK) // try again
		d.want(ScreenFirmware)
	})
	t.Run("the stash is there already", func(t *testing.T) {
		d := newMLDrive(t, false)
		d.toChecklist()
		d.press(KeyOK)
		d.wantActs(ActEject)
		d.m.DriveEjected(true)
		d.wantActs(ActReadDrive)
		r := mlStashDrive
		r.StashDone = true
		d.m.MainlineDriveRead(r)
		d.want(ScreenFirmware)
		a := d.wantActs(ActGiveBackDrive)
		if !a[0].Remake || d.m.Running() != "" {
			t.Fatalf("%+v %q", a[0], d.m.Running())
		}
		if v := bodyText(d.m.View(d.now)); !strings.Contains(v, txtMLSwapAgain) || !strings.Contains(v, txtMLSwapCopy) {
			t.Fatalf("swap again:\n%s", v)
		}
		d.press(KeyBack) // Cancel: the status; the stash stays for this boot
		d.want(ScreenStatus)
	})
	t.Run("plain words", func(t *testing.T) {
		for raw, want := range map[string]string{
			"drive: the drive is with the computer again":                                       "went back to the computer",
			"key search: no updater library with the key in system_a or system_b":               "did not find the firmware key",
			"pass 1: segment boot: sha256 differs":                                              "not the reviewed 1.0.0.94 userdebug firmware",
			"the stash: /run/uc-pxx-inst/mlstash exists already: a stash is made once per boot": "Step 1 was done already",
			"something else": txtMLStashFailed,
		} {
			if got := stashPlain(raw); !strings.Contains(got, want) {
				t.Fatalf("stashPlain(%q) = %q, want %q", raw, got, want)
			}
		}
	})
}

func TestMainlineDirectChoice(t *testing.T) {
	slot := func(p, tr, s byte) avbab.Slot { return avbab.Slot{Priority: p, Tries: tr, Successful: s} }
	for _, c := range []struct {
		name   string
		a, b   avbab.Slot
		valid  bool
		direct bool
	}{
		{"the BCB-route reset", slot(15, 6, 0), slot(14, 7, 0), true, true},
		{"the reset, other tries", slot(15, 7, 0), slot(14, 7, 0), true, true},
		{"after R(K)", slot(15, 0, 1), slot(0, 0, 0), true, true},
		{"both trying, B higher", slot(14, 7, 0), slot(15, 6, 0), true, false},
		{"both trying, a tie", slot(15, 7, 0), slot(15, 7, 0), true, false},
		{"unit 2 (both successful)", slot(15, 0, 1), slot(14, 0, 1), true, false},
		{"userdebug B booting", slot(14, 0, 1), slot(15, 0, 1), true, false},
		{"A successful, B trying (after the arm)", slot(14, 0, 1), slot(15, 7, 0), true, false},
		{"after R(K), A 14", slot(14, 0, 1), slot(0, 0, 0), true, false},
		{"not valid", slot(15, 6, 0), slot(14, 7, 0), false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := newMLDrive(t, false)
			u := *d.m.Unit
			u.ABValid, u.Slots[0].AB, u.Slots[1].AB = c.valid, c.a, c.b
			d.m.SetUnit(u)
			if got := d.m.directStart(); got != c.direct {
				t.Fatalf("directStart %v, want %v", got, c.direct)
			}
		})
	}
	t.Run("direct without step 1: refused at once", func(t *testing.T) {
		d := newDDrive(t, false)
		d.toChecklist()
		d.press(KeyOK)
		d.wantActs(ActEject)
		d.m.DriveEjected(true)
		d.wantActs(ActReadDrive)
		d.m.MainlineDriveRead(mlDriveOK) // StashDone false
		d.want(ScreenRefused)
		a := d.wantActs(ActGiveBackDrive)
		if !a[0].Remake || d.m.Running() != "" {
			t.Fatalf("%+v %q", a[0], d.m.Running())
		}
		if v := bodyText(d.m.View(d.now)); !strings.Contains(v, "Why: "+txtMLStep1Missing) || !strings.Contains(v, txtRefusedNote) {
			t.Fatalf("refused:\n%s", v)
		}
	})
	t.Run("direct with step 1: the unlock for slot B", func(t *testing.T) {
		d := newDDrive(t, false)
		d.toChecklist()
		d.press(KeyOK)
		d.wantActs(ActEject)
		d.m.DriveEjected(true)
		d.wantActs(ActReadDrive)
		r := mlDriveOK
		r.StashDone = true
		d.m.MainlineDriveRead(r)
		d.want(ScreenUnlock)
		if !d.m.MainlineDirect() {
			t.Fatal("not the direct path")
		}
		v := bodyText(d.m.View(d.now))
		if !strings.Contains(v, "It is for slot B.") || !strings.Contains(v, txtUnlockWhyDirect) || strings.Contains(v, txtUnlockWhy) {
			t.Fatalf("unlock:\n%s", v)
		}
		d.unlockYes()
		d.panelYes()
		d.want(ScreenPlanning)
		d.wantActs(ActStartPlan)
		if v := bodyText(d.m.View(d.now)); !strings.Contains(v, "Install to slot B. Slot A (the Crestron firmware) stays the fallback") {
			t.Fatalf("planning:\n%s", v)
		}
	})
}

// TestMainlineDirectEnds: the direct plan's screens and every end of its
// install child (Classify on the direct plan's states): Done only with exit
// 0 and the plan's end state; a stop before the arm is Stopped with S0 or
// D1 (the R(K) state); exit 3 is the hold, with no repair check (a direct
// plan is a mainline plan), and Check again reads the sector.
func TestMainlineDirectEnds(t *testing.T) {
	toPlan := func(t *testing.T) *drive {
		d := newDDrive(t, false)
		d.toChecklist()
		d.press(KeyOK)
		d.wantActs(ActEject)
		d.m.DriveEjected(true)
		d.wantActs(ActReadDrive)
		r := mlDriveOK
		r.StashDone = true
		d.m.MainlineDriveRead(r)
		d.unlockYes()
		d.panelYes()
		d.wantActs(ActStartPlan)
		d.m.PlanEnded(PlanResult{Plan: dTestPlan()})
		d.want(ScreenPlan)
		return d
	}
	d := toPlan(t)
	v := bodyText(d.m.View(d.now))
	for _, want := range []string{"Slot B holds the Crestron firmware 1.0.4.22 (user) (uboot, trust, dtbo, vbmeta and boot as its manifest); it is replaced by mainline Linux; slot A stays the fallback.",
		"Slot B gets mainline Linux v1.0.0-rc1 and the boot files of Android userdebug 1.0.0.94 (step 1).",
		"Slot B now (status): Crestron 1.0.4.22 (user).", txtMLDirectKeep, txtMLGateDirect, txtMLMarkPlanD,
		"Hold OK (Mic) for 3 seconds to confirm that slot B is replaced."} {
		if !strings.Contains(v, want) {
			t.Fatalf("no %q on the plan screen:\n%s", want, v)
		}
	}
	if strings.Contains(v, "start the install") || d.m.ReplaceConfirmed() {
		t.Fatalf("the install can start before the confirmation:\n%s", v)
	}
	for _, c := range []struct {
		name   string
		after  []byte
		end    End
		screen Screen
		state  string
	}{
		{"done", dD2, End{Exited: true}, ScreenDone, StateD2},
		{"killed before R(K)", dS0, End{Signal: "killed"}, ScreenStopped, StateS0},
		{"stopped after R(K)", dRK, End{Exited: true, Code: 1}, ScreenStopped, StateD1},
		{"killed after the arm", dD2, End{Signal: "killed"}, ScreenStopped, StateD2},
		{"exit 3", dRK, End{Exited: true, Code: 3}, ScreenHold, StateD1},
		{"exit 0, not the end state", dRK, End{Exited: true}, ScreenHold, StateD1},
		{"a sector of no plan state", mlS0, End{Exited: true, Code: 1}, ScreenHold, StateUnknown},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := toPlan(t)
			d.hold(HoldOK) // confirms the replacement of slot B
			d.want(ScreenPlan)
			d.hold(HoldOK)
			d.want(ScreenProgress)
			d.wantActs(ActStartInstall)
			o := Classify(dTestPlan(), dS0, c.after, nil, c.end)
			if o.Screen != c.screen || o.State != c.state {
				t.Fatalf("classify: %s %s, want %s %s (%s)", o.Screen, o.State, c.screen, c.state, o.Why)
			}
			d.m.InstallEnded(o)
			d.want(c.screen)
			d.wantActs() // never the repair's dry run
			if d.m.repairHold() {
				t.Fatal("a direct hold is a repair hold")
			}
			v := bodyText(d.m.View(d.now))
			switch c.screen {
			case ScreenDone:
				for _, want := range []string{"The partitions of slot A were not written; it is the fallback.", txtMLCable, txtMLMarkD} {
					if !strings.Contains(v, want) {
						t.Fatalf("no %q on Done:\n%s", want, v)
					}
				}
			case ScreenHold:
				d.press(KeyOK) // Check again: a read of the sector, never the repair
				d.wantActs(ActReadAB)
				if d.m.RestartBlocked() == "" {
					t.Fatal("the hold does not block the restart")
				}
			}
		})
	}
}

// TestMainlineReplaceConfirm (owner F1): on the plan screen of a direct
// plan the first hold of OK (a terminal: Enter, then y) confirms that slot
// B, as the plan names it, is replaced; nothing is asked of the runner and
// nothing is written; only a second hold starts the install child with the
// confirmation. A short hold, a key between Enter and y, Back, and a new
// plan id each leave it unconfirmed. Never automatic.
func TestMainlineReplaceConfirm(t *testing.T) {
	toPlan := func(t *testing.T, terminal bool) *drive {
		d := newDDrive(t, terminal)
		d.toChecklist()
		d.press(KeyOK)
		d.wantActs(ActEject)
		d.m.DriveEjected(true)
		d.wantActs(ActReadDrive)
		r := mlDriveOK
		r.StashDone = true
		d.m.MainlineDriveRead(r)
		d.unlockYes()
		d.panelYes()
		d.wantActs(ActStartPlan)
		d.m.PlanEnded(PlanResult{Plan: dTestPlan()})
		d.want(ScreenPlan)
		return d
	}
	t.Run("panel keys", func(t *testing.T) {
		d := toPlan(t, false)
		d.hold(HoldOK - time.Second) // too short
		if d.m.ReplaceConfirmed() {
			t.Fatal("a short hold confirmed")
		}
		for i := 0; i < 20; i++ { // time alone never confirms
			d.now = d.now.Add(time.Second)
			d.m.Tick(d.now)
		}
		if d.m.ReplaceConfirmed() {
			t.Fatal("confirmed without a hold")
		}
		d.hold(HoldOK)
		d.want(ScreenPlan)
		d.wantActs()
		if !d.m.ReplaceConfirmed() || d.m.Running() != "" || d.m.Msg != txtReplaceConfirmed {
			t.Fatalf("after the hold: confirmed %v, running %q, msg %q", d.m.ReplaceConfirmed(), d.m.Running(), d.m.Msg)
		}
		v := bodyText(d.m.View(d.now))
		if !strings.Contains(v, txtReplaceConfirmed) || !strings.Contains(v, "Hold OK (Mic) for 3 seconds to start the install.") {
			t.Fatalf("after the confirmation:\n%s", v)
		}
		d.hold(HoldOK)
		d.want(ScreenProgress)
		d.wantActs(ActStartInstall)
	})
	t.Run("terminal", func(t *testing.T) {
		d := toPlan(t, true)
		d.press(KeyOK)
		d.press(KeyNext) // another key cancels the Enter
		d.press(KeyYes)
		if d.m.ReplaceConfirmed() {
			t.Fatal("y after another key confirmed")
		}
		d.press(KeyOK)
		d.press(KeyYes)
		d.want(ScreenPlan)
		d.wantActs()
		if !d.m.ReplaceConfirmed() {
			t.Fatal("Enter then y did not confirm")
		}
		d.press(KeyOK)
		d.press(KeyYes)
		d.want(ScreenProgress)
		d.wantActs(ActStartInstall)
	})
	t.Run("Back, and a new plan", func(t *testing.T) {
		d := toPlan(t, false)
		d.hold(HoldOK)
		if !d.m.ReplaceConfirmed() {
			t.Fatal("not confirmed")
		}
		p2 := dTestPlan()
		p2.ID = strings.Repeat("e2e2", 16)
		d.m.Plan = p2 // a plan of another id on the screen (a new check)
		if d.m.ReplaceConfirmed() {
			t.Fatal("the confirmation carried over to another plan")
		}
		d.m.Plan = dTestPlan()
		d.press(KeyBack)
		d.want(ScreenStatus)
	})
	t.Run("the userdebug-first plan asks no confirmation", func(t *testing.T) {
		d := newMLDrive(t, false)
		d.toMLProgress() // one hold starts the install
	})
}

// TestMainlineLockWriteConfirm (owner MAJOR-3, 2026-11-12): on the plan
// screen of a direct plan of a locked unit (the lock-state write intent),
// after the replace confirmation, the screen shows the plain warning (the
// unit is locked; lock_state 0x01 before anything is written to slot B; not
// undone), and only the next hold of OK (a terminal: Enter, then y) agrees;
// it writes nothing and asks nothing of the runner. Only a third hold starts
// the install. A short hold, time alone, a key between Enter and y, Back and
// a new plan id each leave it not agreed. The agreement is recorded
// (Persist().LockWrite) and never restored as an agreement in a new UI.
func TestMainlineLockWriteConfirm(t *testing.T) {
	toPlan := func(t *testing.T, terminal bool, p *plan.Plan) *drive {
		d := newDDrive(t, terminal)
		d.toChecklist()
		d.press(KeyOK)
		d.wantActs(ActEject)
		d.m.DriveEjected(true)
		d.wantActs(ActReadDrive)
		r := mlDriveOK
		r.StashDone = true
		d.m.MainlineDriveRead(r)
		d.unlockYes()
		d.panelYes()
		d.wantActs(ActStartPlan)
		d.m.PlanEnded(PlanResult{Plan: p})
		d.want(ScreenPlan)
		return d
	}
	warn := []string{txtLockWarnTitle, txtLockWarnSet, txtLockWarnUndo, txtLockWarnBack}
	t.Run("panel keys", func(t *testing.T) {
		d := toPlan(t, false, dLockPlan())
		v := bodyText(d.m.View(d.now))
		if strings.Contains(v, txtLockWarnTitle) || strings.Contains(v, txtLockUnlocked) || !strings.Contains(v, txtLockLockedNote) ||
			!strings.Contains(v, "to confirm that slot B is replaced.") {
			t.Fatalf("before the replace confirmation:\n%s", v)
		}
		d.hold(HoldOK) // the replace confirmation (owner F1)
		d.want(ScreenPlan)
		d.wantActs()
		v = bodyText(d.m.View(d.now))
		for _, w := range append(warn, txtReplaceConfirmed, "Hold OK (Mic) for 3 seconds to agree to the lock-state write.") {
			if !strings.Contains(v, w) {
				t.Fatalf("no %q on the warning:\n%s", w, v)
			}
		}
		if strings.Contains(v, "start the install") || d.m.LockWriteConfirmed() || d.m.LockWriteAgreed() != nil {
			t.Fatalf("the install can start before the agreement:\n%s", v)
		}
		d.hold(HoldOK - time.Second) // too short
		for i := 0; i < 20; i++ {
			d.now = d.now.Add(time.Second)
			d.m.Tick(d.now)
		}
		if d.m.LockWriteConfirmed() {
			t.Fatal("agreed without a full hold")
		}
		d.hold(HoldOK)
		d.want(ScreenPlan)
		d.wantActs()
		if !d.m.LockWriteConfirmed() || d.m.LockWriteFromTerminal() || d.m.Running() != "" || d.m.Msg != txtLockWriteAgreed {
			t.Fatalf("after the hold: agreed %v, terminal %v, running %q, msg %q", d.m.LockWriteConfirmed(), d.m.LockWriteFromTerminal(), d.m.Running(), d.m.Msg)
		}
		if a := d.m.Persist().LockWrite; a == nil || !a.Confirmed || a.FromTerminal || a.Plan != dLockPlan().ID {
			t.Fatalf("the record %+v", a)
		}
		v = bodyText(d.m.View(d.now))
		if !strings.Contains(v, txtLockWriteAgreed) || strings.Contains(v, txtLockWarnTitle) || !strings.Contains(v, "Hold OK (Mic) for 3 seconds to start the install.") {
			t.Fatalf("after the agreement:\n%s", v)
		}
		d.hold(HoldOK)
		d.want(ScreenProgress)
		d.wantActs(ActStartInstall)
		d.m.InstallEnded(Classify(dLockPlan(), dS0, dD2, nil, End{Exited: true}))
		d.want(ScreenDone)
		if v := bodyText(d.m.View(d.now)); !strings.Contains(v, txtMLLockDone) {
			t.Fatalf("Done of a lock-write plan:\n%s", v)
		}
	})
	t.Run("terminal", func(t *testing.T) {
		d := toPlan(t, true, dLockPlan())
		d.press(KeyOK)
		d.press(KeyYes) // the replace confirmation
		if !d.m.ReplaceConfirmed() || d.m.LockWriteConfirmed() {
			t.Fatal("the replace confirmation")
		}
		d.press(KeyOK)
		d.press(KeyNext) // another key cancels the Enter
		d.press(KeyYes)
		if d.m.LockWriteConfirmed() {
			t.Fatal("y after another key agreed")
		}
		d.press(KeyYes) // y alone
		if d.m.LockWriteConfirmed() {
			t.Fatal("y alone agreed")
		}
		d.press(KeyOK)
		if v := bodyText(d.m.View(d.now)); !strings.Contains(v, "Press y to agree to the lock-state write now.") {
			t.Fatalf("after Enter:\n%s", v)
		}
		d.press(KeyYes)
		d.want(ScreenPlan)
		d.wantActs()
		if !d.m.LockWriteConfirmed() || !d.m.LockWriteFromTerminal() {
			t.Fatal("Enter then y did not agree, or not from the terminal")
		}
		if a := d.m.Persist().LockWrite; a == nil || !a.Confirmed || !a.FromTerminal {
			t.Fatalf("the record %+v", a)
		}
		d.press(KeyOK)
		d.press(KeyYes)
		d.want(ScreenProgress)
		d.wantActs(ActStartInstall)
	})
	t.Run("Back on the warning: nothing written", func(t *testing.T) {
		d := toPlan(t, false, dLockPlan())
		d.hold(HoldOK)
		d.press(KeyBack)
		d.want(ScreenStatus)
		if d.m.LockWriteConfirmed() || d.m.Running() != "" || d.m.Msg != "Canceled. Nothing was written." {
			t.Fatalf("after Back: agreed %v, running %q, msg %q", d.m.LockWriteConfirmed(), d.m.Running(), d.m.Msg)
		}
		for _, a := range d.m.Take() {
			if a.Kind == ActStartInstall {
				t.Fatal("Back started the install")
			}
		}
	})
	t.Run("a new plan id: not agreed", func(t *testing.T) {
		d := toPlan(t, false, dLockPlan())
		d.hold(HoldOK)
		d.hold(HoldOK)
		if !d.m.LockWriteConfirmed() {
			t.Fatal("not agreed")
		}
		p2 := dLockPlan()
		p2.ID = strings.Repeat("e2e2", 16)
		d.m.Plan = p2
		if d.m.LockWriteConfirmed() {
			t.Fatal("the agreement carried over to another plan")
		}
	})
	t.Run("a plan with no write intent: no warning, no flag", func(t *testing.T) {
		d := toPlan(t, false, dTestPlan())
		if v := bodyText(d.m.View(d.now)); !strings.Contains(v, txtLockUnlocked) {
			t.Fatalf("no lock-state line:\n%s", v)
		}
		d.hold(HoldOK)
		if v := bodyText(d.m.View(d.now)); strings.Contains(v, txtLockWarnTitle) || !strings.Contains(v, "to start the install.") {
			t.Fatalf("a warning with no write intent:\n%s", v)
		}
		d.hold(HoldOK)
		d.want(ScreenProgress)
		d.wantActs(ActStartInstall)
		if d.m.LockWriteConfirmed() || d.m.Persist().LockWrite != nil {
			t.Fatal("an agreement with no write intent")
		}
		d.m.InstallEnded(Classify(dTestPlan(), dS0, dD2, nil, End{Exited: true}))
		if v := bodyText(d.m.View(d.now)); strings.Contains(v, txtMLLockDone) {
			t.Fatalf("Done says the lock state was set:\n%s", v)
		}
	})
	t.Run("a failed lock-write check: no warning", func(t *testing.T) {
		p := dLockPlan()
		p.Checks[len(p.Checks)-1].OK = false
		d := toPlan(t, false, p)
		d.hold(HoldOK)
		if d.m.planLockWrite() || d.m.lockWriteAsk() {
			t.Fatal("a failed check counts as the intent")
		}
	})
	t.Run("the userdebug-first plan has no lock-state write", func(t *testing.T) {
		d := newMLDrive(t, false)
		d.toMLProgress()
		if d.m.planLockWrite() || d.m.LockWriteConfirmed() {
			t.Fatal("a lock-state write on the userdebug-first path")
		}
	})
	t.Run("resume: the record, not the agreement", func(t *testing.T) {
		d := toPlan(t, false, dLockPlan())
		d.hold(HoldOK)
		d.hold(HoldOK)
		p := d.m.Persist()
		m := New(d.m.Title, false)
		m.Resume(p, dLockPlan())
		if m.LockWriteConfirmed() {
			t.Fatal("a new UI restored the agreement itself")
		}
		if a := m.Persist().LockWrite; a == nil || !a.Confirmed || a.Plan != dLockPlan().ID {
			t.Fatalf("a new UI lost the record: %+v", a)
		}
	})
}

// TestMainlineUnlockDirectStatement (owner MAJOR-3): on the direct path the
// unlock screen is a statement, not a question: the installer reads the
// lock state itself; a locked unit gets the warning on the plan screen.
// Back cancels (nothing written); a terminal goes on only with y after its
// warning, as the panel question.
func TestMainlineUnlockDirectStatement(t *testing.T) {
	toUnlock := func(t *testing.T, terminal bool) *drive {
		d := newDDrive(t, terminal)
		d.toChecklist()
		d.press(KeyOK)
		d.wantActs(ActEject)
		d.m.DriveEjected(true)
		d.wantActs(ActReadDrive)
		r := mlDriveOK
		r.StashDone = true
		d.m.MainlineDriveRead(r)
		d.want(ScreenUnlock)
		return d
	}
	d := toUnlock(t, false)
	v := bodyText(d.m.View(d.now))
	for _, w := range []string{txtUnlockTitleDirect, txtUnlockStatementDirect, txtUnlockWhyDirect, txtUnlockValuesDirect, "OK (Mic) = Continue, Speaker = Cancel."} {
		if !strings.Contains(v, w) {
			t.Fatalf("no %q:\n%s", w, v)
		}
	}
	for _, w := range []string{txtUnlockQuestion, txtUnlockNoHint, "TEE unlock tool"} {
		if strings.Contains(v, w) {
			t.Fatalf("the question %q on the direct path:\n%s", w, v)
		}
	}
	d.press(KeyBack)
	d.want(ScreenRefused)
	d.wantActs(ActGiveBackDrive)
	if v := bodyText(d.m.View(d.now)); !strings.Contains(v, txtUnlockCancelDirect) || strings.Contains(v, txtUnlockNo) {
		t.Fatalf("Back:\n%s", v)
	}
	d = toUnlock(t, true)
	d.press(KeyOK)
	if v := bodyText(d.m.View(d.now)); !strings.Contains(v, txtUnlockTermWarnDirect) || !strings.Contains(v, "y = Continue, b = Cancel.") {
		t.Fatalf("the terminal's warning:\n%s", v)
	}
	d.press(KeyYes) // too early
	d.want(ScreenUnlock)
	d.m.Drawn(d.now)
	d.now = d.now.Add(PanelWarnWait)
	d.press(KeyYes)
	if !d.m.UnlockConfirmed() || !d.m.UnlockFromTerminal() {
		t.Fatal("y after the warning did not go on")
	}
}

func TestMainlineStepArgs(t *testing.T) {
	ps := Paths{Exe: "/usr/bin/uc-pxx-inst", Drive: "/run/uc-pxx-inst/drive/drive.img", DriveIdent: "size=1,mtime=1,ctime=1,dev=1,ino=1",
		PlanJSON: "/run/uc-pxx-inst/flow/plan.json", Log: "/run/uc-pxx-inst/flow/install-d1ecd1ecd1ec.jsonl", Keys: "/run/uc-pxx-inst/flow/keys.txt"}
	if got := MainlineStashArgs(ps); !reflect.DeepEqual(got, []string{"/usr/bin/uc-pxx-inst", "plan", "--mainline-stash", "--device", "/dev/mmcblk0",
		"--fw-drive", "/run/uc-pxx-inst/drive/drive.img", "--fw-drive-expect", "size=1,mtime=1,ctime=1,dev=1,ino=1", "--keys", "/run/uc-pxx-inst/flow/keys.txt"}) {
		t.Fatalf("stash args %q", got)
	}
	ps.Keys = ""
	if got := strings.Join(MainlineStashArgs(ps), " "); strings.Contains(got, "--keys") || strings.Contains(got, "confirmed") ||
		strings.Contains(got, "--yes") || strings.Contains(got, "--plan") || strings.Contains(got, "--json") {
		t.Fatalf("stash args %q", got)
	}
	if got := MainlineDirectPlanArgs(ps, true, true, true); !reflect.DeepEqual(got, append(MainlinePlanArgs(ps, true, true, true), "--direct")) {
		t.Fatalf("direct plan args %q", got)
	}
	if got := MainlineDirectInstallArgs(ps, true, true, false, false, false, false); !reflect.DeepEqual(got, append(MainlineInstallArgs(ps, true, true, false), "--direct")) ||
		strings.Contains(strings.Join(got, " "), "--unlock") || strings.Contains(strings.Join(got, " "), "--replace") ||
		strings.Contains(strings.Join(got, " "), "lockwrite") {
		t.Fatalf("direct install args %q", got)
	}
	if got := MainlineDirectInstallArgs(ps, true, true, true, true, false, true); !reflect.DeepEqual(got, append(MainlineInstallArgs(ps, true, true, true), "--direct", plan.ReplaceFlag)) {
		t.Fatalf("direct install args %q (no agreement: no lockwrite flag, also not the terminal one)", got)
	}
	if got := MainlineDirectInstallArgs(ps, true, true, true, true, true, false); !reflect.DeepEqual(got, append(MainlineInstallArgs(ps, true, true, true), "--direct", plan.ReplaceFlag, plan.LockWriteFlag)) {
		t.Fatalf("direct install args %q", got)
	}
	if got := MainlineDirectInstallArgs(ps, true, true, true, true, true, true); !reflect.DeepEqual(got, append(MainlineInstallArgs(ps, true, true, true), "--direct", plan.ReplaceFlag, plan.LockWriteFlag, LockWriteTerminalFlag)) {
		t.Fatalf("direct install args %q", got)
	}
}

// TestMainlineFlowScreensFit: the new screens fit the UC-P10 panel and the
// UC-P8 console, on the panel keys and on a terminal, with long texts.
func TestMainlineFlowScreensFit(t *testing.T) {
	long := mlDriveOK
	long.Version, long.Token, long.StashDone = "v10.20.30-rc12+build.20261109T235959", "ucpxx-0123456789abcdef", true
	sd := mlStashDrive
	sd.FWName = `"UC-P10 1.0.0.94 userdebug - a very long name that a user gave the file on the computer.bin"`
	for _, sz := range []struct {
		panel      Panel
		cols, rows int
	}{{PanelUCP10, PanelCols, PanelRows}, {PanelUCP8, 91, 28}} {
		for _, terminal := range []bool{false, true} {
			check := func(m *Model, what string) {
				t.Helper()
				m.Panel = sz.panel
				checkFitAt(t, m, time.Unix(100000, 0), what, sz.cols, sz.rows)
			}
			mk := func(s Screen, direct bool) *Model {
				m := New("UC-P10 installer  -  install image  -  uc-pxx", terminal)
				u := longUnit(AutoUpdateView{State: "off"}, unit2Lines())
				if direct {
					u.Slots[0].AB, u.Slots[1].AB = avbab.Slot{Priority: 15, Tries: 6}, avbab.Slot{Priority: 14, Tries: 7}
				}
				m.SetUnit(u)
				m.ml = &mlState{drive: long, unlockOK: true, direct: direct}
				m.Screen = s
				return m
			}
			m := mk(ScreenPlanning, false)
			m.ml = &mlState{drive: sd, stashing: true}
			m.Drive = &DriveResult{OK: true, Name: sd.FWName, Size: sd.FWSize, KeyFile: true}
			m.Prog = NewProgress(false, time.Unix(100000-200, 0))
			for i := 0; i < 30; i++ {
				m.ChildLine(longChild, time.Unix(100000, 0))
			}
			check(m, "step 1")
			for _, st := range []func(m *Model){func(m *Model) {}, func(m *Model) { m.reading = true },
				func(m *Model) { m.refused = longWhy }, func(m *Model) {
					m.copyStart, m.copyLast = time.Unix(100000-60, 0), time.Unix(100000-1, 0)
				}} {
				for _, again := range []bool{false, true} {
					m = mk(ScreenFirmware, false)
					m.ml = &mlState{drive: sd, swap: true, again: again}
					st(m)
					check(m, "swap")
				}
			}
			m = mk(ScreenUnlock, true)
			check(m, "unlock, direct")
			m.panelWarn = true
			check(m, "unlock, direct, the terminal's warning")
			m = mk(ScreenPlanning, true)
			m.Prog = NewProgress(false, time.Unix(100000-200, 0))
			check(m, "planning, direct")
			m = mk(ScreenPlan, true)
			m.Plan = dTestPlan()
			check(m, "plan, direct")
			m.ml.replaceOK, m.ml.replaceID = true, m.Plan.ID
			check(m, "plan, direct, confirmed")
			m = mk(ScreenPlan, true)
			m.Plan = dLockPlan()
			check(m, "plan, direct, locked")
			m.ml.replaceOK, m.ml.replaceID = true, m.Plan.ID
			check(m, "plan, direct, locked, the warning")
			m.okDown, m.okFor = time.Unix(100000-1, 0), ActStartInstall
			check(m, "plan, direct, locked, the warning, holding")
			m.okDown, m.okFor = time.Time{}, 0
			m.confirm = ActStartInstall
			check(m, "plan, direct, locked, the warning, Enter")
			m.confirm = 0
			m.ml.lockOK, m.ml.lockID = true, m.Plan.ID
			check(m, "plan, direct, locked, agreed")
			m = mk(ScreenDone, true)
			m.Plan = dLockPlan()
			m.Outcome = &Outcome{Screen: ScreenDone, State: StateD2}
			m.BundleDone(longBundle)
			check(m, "done, direct, locked")
			for _, c := range []struct {
				after []byte
				end   End
			}{{dD2, End{Exited: true}}, {dRK, End{Exited: true, Code: 1}}, {dRK, End{Exited: true, Code: 3}}} {
				m = mk(ScreenProgress, true)
				m.Plan, m.running = dTestPlan(), "install"
				m.Prog = NewProgress(true, time.Unix(100000-600, 0))
				m.Prog.SetPlan(StepsOf(m.Plan))
				check(m, "progress, direct")
				m.Prog.Stop = "STOP: " + longWhy
				m.InstallEnded(Classify(m.Plan, dS0, c.after, nil, c.end))
				m.BundleDone(longBundle)
				check(m, "end, direct, "+m.Screen.String())
			}
			m = mk(ScreenRefused, true)
			m.refused = txtMLStep1Missing
			check(m, "refused, step 1 missing")
			m = mk(ScreenRefused, false)
			m.ml.stashFailed = true
			m.refused, m.refusedDetail = txtMLStashFailed, "pass 1: "+longWhy
			check(m, "refused, step 1 failed")
			m = mk(ScreenRefused, true)
			m.refused, m.refusedDetail = "x", "install refused: key-pin: the key record (sha256 "+strings.Repeat("ab", 32)+") is not the record of the reviewed key; "+longWhy
			check(m, "refused, key pin")
		}
	}
}

// TestMainlineFlowRenders draws the new screens at 120 x 37 (the UC-P10
// panel) on the panel keys; with P10INST_RENDER_DIR set it writes each into
// that folder (the screens of the record and of the review).
func TestMainlineFlowRenders(t *testing.T) {
	now := time.Unix(100000, 0)
	frames := map[string]Frame{}
	d := newMLDrive(t, false)
	d.m.Title = "UC-P10 installer  -  install image  -  p10inst-xxxxxx"
	d.now = now
	d.toStepOne()
	d.m.ChildStarted(NewProgress(false, now.Add(-70*time.Second)))
	d.m.ChildLine("pass 1: check the whole firmware against the manifests (nothing is written)", now)
	d.m.ChildLine("  pass 1: uboot 4194304 bytes sha256 matches", now)
	frames["step1-reading"] = d.m.View(now)
	d.m.StashEnded(StashResult{OK: true, End: End{Exited: true}})
	frames["step2-swap"] = d.m.View(now)
	d.m.DriveEjected(true)
	frames["step2-reading"] = d.m.View(now)
	f := newMLDrive(t, false)
	f.m.Title, f.now = d.m.Title, now
	f.toStepOne()
	f.m.StashEnded(StashResult{Refusal: "key search: no updater library with the key in system_a or system_b, and no key kept on the unit; give a key file", End: End{Exited: true, Code: 1}})
	frames["step1-refused"] = f.m.View(now)
	x := newDDrive(t, false)
	x.m.Title, x.now = d.m.Title, now
	x.toChecklist()
	x.press(KeyOK)
	x.m.DriveEjected(true)
	x.m.MainlineDriveRead(mlDriveOK)
	frames["direct-no-step1"] = x.m.View(now)
	g := newDDrive(t, false)
	g.m.Title, g.now = d.m.Title, now
	g.toChecklist()
	g.press(KeyOK)
	g.m.DriveEjected(true)
	r := mlDriveOK
	r.StashDone = true
	g.m.MainlineDriveRead(r)
	frames["direct-unlock"] = g.m.View(now)
	g.unlockYes()
	g.panelYes()
	g.m.ChildStarted(NewProgress(false, now.Add(-95*time.Second)))
	frames["direct-planning"] = g.m.View(now)
	g.m.PlanEnded(PlanResult{Plan: dTestPlan()})
	frames["direct-plan"] = g.m.View(now)
	g.hold(HoldOK)
	frames["direct-plan-confirmed"] = g.m.View(g.now)
	g.hold(HoldOK)
	g.m.InstallEnded(Classify(dTestPlan(), dS0, dD2, nil, End{Exited: true}))
	g.m.BundleDone("the drive UC-PXX-INST, folder p10-install-d1ecd1ecd1ec-20261110T120000Z")
	frames["direct-done"] = g.m.View(now)
	// a locked unit (owner MAJOR-3): the warning, the agreement, Done
	l := newDDrive(t, false)
	l.m.Title, l.now = d.m.Title, now
	l.toChecklist()
	l.press(KeyOK)
	l.m.DriveEjected(true)
	l.m.MainlineDriveRead(r)
	l.unlockYes()
	l.panelYes()
	l.m.PlanEnded(PlanResult{Plan: dLockPlan()})
	frames["direct-lock-plan"] = l.m.View(l.now)
	l.hold(HoldOK)
	frames["direct-lock-warning"] = l.m.View(l.now)
	l.hold(HoldOK)
	frames["direct-lock-agreed"] = l.m.View(l.now)
	l.hold(HoldOK)
	l.m.InstallEnded(Classify(dLockPlan(), dS0, dD2, nil, End{Exited: true}))
	l.m.BundleDone("the drive UC-PXX-INST, folder p10-install-10c410c410c4-20261112T120000Z")
	frames["direct-lock-done"] = l.m.View(l.now)
	tl := newDDrive(t, true)
	tl.m.Title, tl.now = d.m.Title, now
	tl.toChecklist()
	tl.press(KeyOK)
	tl.m.DriveEjected(true)
	tl.m.MainlineDriveRead(r)
	frames["direct-unlock-terminal"] = tl.m.View(tl.now)
	tl.unlockYes()
	tl.panelYes()
	tl.m.PlanEnded(PlanResult{Plan: dLockPlan()})
	tl.press(KeyOK)
	tl.press(KeyYes)
	frames["direct-lock-warning-terminal"] = tl.m.View(tl.now)
	tl.press(KeyOK)
	frames["direct-lock-warning-terminal-enter"] = tl.m.View(tl.now)
	out := os.Getenv("P10INST_RENDER_DIR")
	for name, f := range frames {
		txt := screenText(f, PanelCols, PanelRows)
		if lay := f.Layout(PanelCols, PanelRows); lay.Problem(ScreenStatus) != "" {
			t.Fatalf("%s: %s", name, lay.Problem(ScreenStatus))
		}
		if out != "" {
			if err := os.WriteFile(filepath.Join(out, "mainline-"+name+".txt"), []byte(txt), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}
