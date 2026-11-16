package flow

// RC-10 I-5 (owner R15: one confirmation; the reversal of MAJOR-3) and I-7
// (owner R24: the auto-update check for the official path only).

import (
	"strings"
	"testing"
	"time"

	"uc-pxx-installer/internal/plan"
)

// toDPlan: a direct plan on the plan screen (I-5: no lock-state screen, no
// panel question on the unit's keys; a terminal answers the question).
func toDPlan(t *testing.T, terminal bool, p *plan.Plan) *drive {
	d := newDDrive(t, terminal)
	d.toChecklist()
	d.press(KeyOK)
	d.wantActs(ActEject)
	d.m.DriveEjected(true)
	d.wantActs(ActReadDrive)
	r := mlDriveOK
	r.StashDone = true
	d.m.MainlineDriveRead(r)
	if terminal {
		d.want(ScreenPanel)
		d.panelYes()
	}
	d.want(ScreenPlanning)
	d.wantActs(ActStartPlan)
	if !d.m.UnlockConfirmed() || !d.m.UnlockByHold() {
		t.Fatal("the direct path's go-on is not recorded as the plan hold's")
	}
	d.m.PlanEnded(PlanResult{Plan: p})
	d.want(ScreenPlan)
	return d
}

// TestI5OneHold: the plan screen of a direct plan needs exactly one 3 s
// hold; it records the replace confirmation and, on a locked unit, the
// lock-state agreement, and starts the install; the install child then gets
// the flags it got after the three holds of RC-9. Short holds, time alone,
// Back and another plan id never confirm.
func TestI5OneHold(t *testing.T) {
	for _, locked := range []bool{false, true} {
		p := dTestPlan()
		if locked {
			p = dLockPlan()
		}
		d := toDPlan(t, false, p)
		v := bodyText(d.m.View(d.now))
		if strings.Contains(v, txtOneUnlock) != locked || !strings.Contains(v, txtOneHold) || strings.Contains(v, "0x0") {
			t.Fatalf("locked %v: the plan screen\n%s", locked, v)
		}
		d.hold(HoldOK - time.Second)
		for i := 0; i < 20; i++ {
			d.now = d.now.Add(time.Second)
			d.m.Tick(d.now)
		}
		if d.m.ReplaceConfirmed() || d.m.LockWriteConfirmed() || d.m.Running() != "" {
			t.Fatal("a short hold or time confirmed")
		}
		d.wantActs()
		d.hold(HoldOK)
		d.want(ScreenProgress)
		d.wantActs(ActStartInstall)
		if !d.m.ReplaceConfirmed() || d.m.LockWriteConfirmed() != locked || d.m.LockWriteFromTerminal() {
			t.Fatalf("locked %v: replace %v, lock %v", locked, d.m.ReplaceConfirmed(), d.m.LockWriteConfirmed())
		}
		if a := d.m.Persist().LockWrite; (a != nil) != locked || locked && (a.Plan != p.ID || !a.Confirmed || a.FromTerminal) {
			t.Fatalf("the agreement record %+v", a)
		}
		// the install child's flags: those of the three RC-9 holds
		got := strings.Join(MainlineDirectInstallArgs(Paths{Exe: "x", MainlineDir: "/d", PlanJSON: "/p", Log: "/l"}, true, d.m.PanelConfirmed(),
			d.m.UnlockConfirmed(), d.m.ReplaceConfirmed(), d.m.LockWriteConfirmed(), d.m.LockWriteFromTerminal()), " ")
		want := "x install --device /dev/mmcblk0 --mainline /d --plan /p --yes --log /l --prereqs-confirmed --panel-confirmed --unlock-confirmed --direct " + plan.ReplaceFlag
		if locked {
			want += " " + plan.LockWriteFlag
		}
		if got != want {
			t.Fatalf("args\n%s\nwant\n%s", got, want)
		}
	}
	// Back: nothing written, no agreement recorded
	d := toDPlan(t, false, dLockPlan())
	d.press(KeyBack)
	d.want(ScreenStatus)
	if d.m.Persist().LockWrite != nil || d.m.ReplaceConfirmed() {
		t.Fatal("a record after Back")
	}
	// another plan id on the screen: nothing carries over
	d = toDPlan(t, false, dLockPlan())
	d.hold(HoldOK - time.Millisecond)
	p2 := dLockPlan()
	p2.ID = strings.Repeat("e2e2", 16)
	d.m.Plan = p2
	if d.m.ReplaceConfirmed() || d.m.LockWriteConfirmed() {
		t.Fatal("carried over")
	}
}

// TestI5Terminal: a terminal confirms with Enter, then y; on a locked unit
// the y counts only PanelWarnWait after the screen (its unlock line) was
// drawn; a burst never agrees.
func TestI5Terminal(t *testing.T) {
	d := toDPlan(t, true, dLockPlan())
	d.press(KeyOK)
	d.press(KeyYes) // before any draw
	if d.m.Running() != "" || d.m.LockWriteConfirmed() {
		t.Fatal("a y before the draw agreed")
	}
	d.m.Drawn(d.now)
	d.now = d.now.Add(PanelWarnWait - time.Millisecond)
	d.press(KeyYes)
	if d.m.Running() != "" {
		t.Fatal("a y before PanelWarnWait agreed")
	}
	d.now = d.now.Add(time.Millisecond)
	d.press(KeyYes)
	d.want(ScreenProgress)
	d.wantActs(ActStartInstall)
	if !d.m.LockWriteConfirmed() || !d.m.LockWriteFromTerminal() || !d.m.Persist().LockWrite.FromTerminal {
		t.Fatal("the terminal's agreement")
	}
	// an unlocked unit: Enter, then y, only PanelWarnWait after the plan
	// was drawn (review I-5 MINOR-1)
	d = toDPlan(t, true, dTestPlan())
	d.press(KeyOK)
	d.press(KeyYes)
	d.want(ScreenPlan)
	d.wantActs()
	d.read()
	d.press(KeyOK)
	d.press(KeyYes)
	d.want(ScreenProgress)
}

// read: the UI drew the screen, and PanelWarnWait passed.
func (d *drive) read() {
	d.m.Drawn(d.now)
	d.now = d.now.Add(PanelWarnWait)
}

// TestI5PanelByHold: on the unit's keys the plan's hold answers the panel
// question; a terminal that takes over and confirms is asked the question
// first, and its yes goes back to the same plan (no new plan child).
func TestI5PanelByHold(t *testing.T) {
	d := toDPlan(t, false, dTestPlan())
	if !d.m.PanelByHold() || !d.m.Persist().PanelByHold || d.m.PanelFromTerminal() {
		t.Fatal("the panel answer is not taken from the hold")
	}
	if det := strings.Join(d.m.PlanDetails(), "\n"); !strings.Contains(det, "answered by the 3 s hold") {
		t.Fatalf("details:\n%s", det)
	}
	d.m.SetTerminal(true)
	d.read()
	d.press(KeyOK)
	d.press(KeyYes)
	d.want(ScreenPanel)
	d.wantActs()
	if d.m.Running() != "" || d.m.PanelConfirmed() {
		t.Fatal("a terminal confirmed a plan whose panel answer came from the keys")
	}
	d.panelYes()
	d.want(ScreenPlan)
	d.wantActs() // the same plan: no new plan child
	if !d.m.PanelFromTerminal() || d.m.PanelByHold() {
		t.Fatal("the terminal's panel answer")
	}
	d.read()
	d.press(KeyOK)
	d.press(KeyYes)
	d.want(ScreenProgress)
	d.wantActs(ActStartInstall)
}

// TestI5AutoOpen: with AutoOpen (the image's UI) the first status read
// opens the firmware screen (no Install press; --prereqs-confirmed as the
// press gave it); never over a hold, a run or an unreadable slot table, and
// once per UI.
func TestI5AutoOpen(t *testing.T) {
	m := New("t", false)
	m.AutoOpen = true
	u := Unit{ABValid: true, Next: 0, AutoUpdate: AutoUpdateView{State: "on"}}
	m.SetUnit(u)
	if m.Screen != ScreenFirmware || !m.PrereqsConfirmed() {
		t.Fatalf("auto-open: %s", m.Screen)
	}
	m.Handle(Event{KeyBack, true}, time.Now())
	m.SetUnit(u)
	if m.Screen != ScreenStatus {
		t.Fatalf("opened twice: %s", m.Screen)
	}
	m = New("t", false)
	m.AutoOpen = true
	m.SetUnit(Unit{ABValid: false, ABErr: "crc"})
	if m.Screen != ScreenStatus {
		t.Fatal("opened with an unreadable slot table")
	}
	m = New("t", false)
	m.AutoOpen = true
	m.Resume(Persisted{EverHeld: true}, nil)
	m.SetUnit(u)
	if m.Screen != ScreenHold {
		t.Fatalf("a hold was left: %s", m.Screen)
	}
}

// TestI5DoneRestart: Done restarts by itself 10 s later on the unit's keys
// (Back = stay); a terminal keeps the hold; a take-over stops the countdown.
func TestI5DoneRestart(t *testing.T) {
	toDone := func(terminal bool) *drive {
		d := toDPlan(t, terminal, dTestPlan())
		if terminal {
			d.read()
			d.press(KeyOK)
			d.press(KeyYes)
		} else {
			d.hold(HoldOK)
		}
		d.wantActs(ActStartInstall)
		d.m.InstallEnded(Classify(dTestPlan(), dS0, dD2, nil, End{Exited: true}))
		d.want(ScreenDone)
		return d
	}
	d := toDone(false)
	d.m.Tick(d.now)
	if v := bodyText(d.m.View(d.now)); !strings.Contains(v, "Restarting in 10 s. Back = stay here.") || !strings.Contains(v, txtOneDone) {
		t.Fatalf("Done:\n%s", v)
	}
	d.now = d.now.Add(9 * time.Second)
	d.m.Tick(d.now)
	d.wantActs()
	d.now = d.now.Add(time.Second)
	d.m.Tick(d.now)
	d.wantActs(ActRestart)
	d.now = d.now.Add(time.Minute)
	d.m.Tick(d.now)
	d.wantActs() // once
	// Back stays
	d = toDone(false)
	d.m.Tick(d.now)
	d.press(KeyBack)
	d.want(ScreenDone)
	d.now = d.now.Add(time.Minute)
	d.m.Tick(d.now)
	d.wantActs()
	d.hold(HoldOK) // the hold still restarts
	d.wantActs(ActRestart)
	// a terminal: no countdown
	d = toDone(true)
	d.now = d.now.Add(time.Minute)
	d.m.Tick(d.now)
	d.wantActs()
	// a take-over during the countdown stops it
	d = toDone(false)
	d.m.Tick(d.now)
	d.m.SetTerminal(true)
	d.m.SetTerminal(false)
	d.now = d.now.Add(time.Minute)
	d.m.Tick(d.now)
	d.wantActs()
	// a UI resumed on Done (review I-5 MINOR-3): no countdown, the hold stays
	p := toDone(false).m.Persist()
	r := New("test", true)
	r.Resume(p, nil)
	if r.Screen != ScreenDone {
		t.Fatalf("resumed on %s", r.Screen)
	}
	now := time.Now()
	for i := 0; i < 30; i++ {
		now = now.Add(time.Second)
		r.Tick(now)
	}
	if a := r.Take(); len(a) != 0 {
		t.Fatalf("a resumed Done acted by itself: %v", a)
	}
}

// TestI7AutoUpdateOfficialOnly: the auto-update check refuses an official
// firmware (drive or USB) and never a mainline step 1.
func TestI7AutoUpdateOfficialOnly(t *testing.T) {
	for _, au := range []string{"on", "unknown"} {
		// a mainline step 1: the stash child
		d := newDrive(t, false, au)
		d.toChecklist()
		d.press(KeyOK)
		d.wantActs(ActEject)
		d.m.DriveEjected(true)
		d.wantActs(ActReadDrive)
		d.m.MainlineDriveRead(mlStashDrive)
		d.want(ScreenPlanning)
		d.wantActs(ActStartPlan)
		// over USB: official refused, mainline goes on
		for _, mode := range []string{"official", "mainline"} {
			m := New("t", false)
			m.SetUSB()
			m.SetUnit(Unit{ABValid: true, AutoUpdate: AutoUpdateView{State: au}})
			m.Sel = 1
			m.Handle(Event{KeyOK, true}, time.Now())
			m.Take()
			m.USBFirmwareSealed(USBFirmware{Mode: mode, Size: 973, Ident: "size=1,mtime=1,ctime=1,dev=1,ino=1"})
			acts := m.Take()
			if mode == "official" && (m.Screen != ScreenRefused || len(acts) != 1 || acts[0].Kind != ActUSB || acts[0].Want != "firmware") {
				t.Fatalf("%s official over USB: %s %+v", au, m.Screen, acts)
			}
			if mode == "mainline" && (m.Screen != ScreenPlanning || !m.MainlineStashing()) {
				t.Fatalf("%s mainline over USB: %s", au, m.Screen)
			}
		}
	}
}

// TestI4DriveTexts: in the drive mode (the fallback) the drive screens say
// first that the installer page copies the files; over USB they never show.
func TestI4DriveTexts(t *testing.T) {
	d := newMLDrive(t, false)
	d.toStepOne()
	d.m.StashEnded(StashResult{OK: true, End: End{Exited: true}})
	d.want(ScreenFirmware)
	if r := rowsOf(d.m.View(d.now)); !strings.Contains(strings.Join(r, "\n"), txtDrivePage+"\n"+txtMLSwapCopy) {
		t.Fatalf("swap view: %q", r)
	}
	m := New("t", false)
	m.SetUSB()
	m.SetUnit(Unit{ABValid: true, AutoUpdate: AutoUpdateView{State: "off"}})
	m.Sel = 1
	m.Handle(Event{KeyOK, true}, time.Now())
	if v := bodyText(m.View(time.Now())); strings.Contains(v, txtDrivePage) || !strings.Contains(v, txtUSBDriveKey) {
		t.Fatalf("over USB:\n%s", v)
	}
	m.USBDrive()
	if v := bodyText(m.View(time.Now())); !strings.Contains(v, txtDrivePage) {
		t.Fatalf("the drive mode:\n%s", v)
	}
}
