package flow

// The findings of FLOW-R3-REVIEW, each tested with the reviewer's probe
// scenario (P1..P16): the hold after a switch (H-1), the repair state of a
// UI that is started again (M-1), the repair only for the hold of its
// install (M-2), the safe screen only with the flow's own read (M-3), a new
// dry run after a refusal (L-1), a read on the hold (L-2), the switch
// screen while it runs (L-3), the switch's Done (L-4), the safe end kept
// (L-5), the panel question from a terminal (L-6).

import (
	"strings"
	"testing"

	"uc-pxx-installer/internal/plan"
)

func bodyOf(f Frame) string {
	return strings.Join(rowsOf(f), "\n")
}

// newUI is a UI process that is started again: the persisted state of m,
// and the plan that the runner reads from state.json.
func newUI(t *testing.T, m *Model, pl *plan.Plan) *drive {
	d := newDrive(t, false, "off")
	d.m.Resume(m.Persist(), pl)
	return d
}

// toSwitchRun: Status → Switch → B → the 3 s hold: the switch runs.
func (d *drive) toSwitchRun() {
	d.t.Helper()
	d.m.Sel = 2
	d.press(KeyOK)
	d.want(ScreenSwitch)
	d.hold(HoldOK)
	if a := d.wantActs(ActSwitch); a[0].Slot != "b" {
		d.t.Fatalf("switch to %q", a[0].Slot)
	}
	d.want(ScreenSwitching)
}

var switch3 = ToolResult{Tool: "switch", End: End{Exited: true, Code: 3}, Stop: "STOP: the A/B state is not known",
	Result: "switch: the state is not known"}

// checkSwitchHold: the hold of a switch whose end is not known.
func checkSwitchHold(t *testing.T, name string, d *drive) {
	t.Helper()
	d.want(ScreenHold)
	f := d.m.View(d.now)
	got := rowsOf(f)
	if got[0] != "!!! KEEP THE UNIT POWERED. DO NOT RESTART IT." ||
		got[1] != "The switch did not finish: the slot state is not known. Do not try again yourself." ||
		!hasRow(f, "  4. Wait for help before you disconnect the power.") || !hasRow(f, "  3. "+Contact) ||
		strings.Contains(bodyOf(f), "can be disconnected") || strings.Contains(bodyOf(f), "repaired") ||
		!strings.HasSuffix(f.Title, "  -  Switch stopped: keep the unit powered") || f.LED != "red" ||
		f.Keys != [KeyPlaces]string{PlaceSpeaker: "Log", PlaceMic: "Check again"} || d.m.RestartBlocked() == "" {
		t.Errorf("%s: the hold after a switch exit 3: %q %q LED %q keys %q blocked %q", name, f.Title, got, f.LED, f.Keys, d.m.RestartBlocked())
	}
	// OK reads the sector only: never the repair (no install ended on it)
	d.hold(HoldOK)
	d.wantActs(ActReadAB)
	d.m.Rechecked(StateUnknown, "no plan")
	d.want(ScreenHold)
}

// TestR3SwitchHold (H-1; the probes P3, P15, RP3): the hold after a switch
// that ended exit 3 is the keep-powered screen whatever an install earlier
// in this RAM image showed: none, Stopped S0, Stopped D1, Done.
func TestR3SwitchHold(t *testing.T) {
	p := testPlan(s0Sec)
	for _, c := range []struct {
		name  string
		after []byte
		end   End
	}{
		{"no install", nil, End{}},
		{"stopped S0", s0Sec, End{Exited: true, Code: 1}},
		{"stopped D1", d1Sec, End{Exited: true, Code: 1}},
		{"done", d2Sec, End{Exited: true}},
	} {
		d := newDrive(t, false, "off")
		if c.after != nil {
			d.m.Plan, d.m.running, d.m.Screen = p, "install", ScreenProgress
			d.m.InstallEnded(Classify(p, s0Sec, c.after, nil, c.end))
			d.wantActs()
			d.press(KeyBack)
			d.want(ScreenStatus)
		}
		d.toSwitchRun()
		d.m.ToolEnded(switch3)
		checkSwitchHold(t, c.name, d)
		// a UI started again (with the plan of state.json): the same hold
		d2 := newUI(t, d.m, p)
		d2.wantActs()
		checkSwitchHold(t, c.name+", new UI", d2)
		// and a UI that lost state.json, but finds the switch write on disk
		d3 := newDrive(t, false, "off")
		d3.m.Resume(Persisted{EverHeld: true, Repair: "dead", DeadBy: "switch"}, p)
		d3.wantActs()
		checkSwitchHold(t, c.name+", from the disk", d3)
	}
}

// TestR3RepairStateKept (M-1; the probes P1, P2, RP1): after a repair
// --yes exit 3, a UI that is started again runs no dry run and offers no
// repair; the tries are per hold, not per UI process.
func TestR3RepairStateKept(t *testing.T) {
	d := newDrive(t, false, "off")
	d.toHold()
	d.m.ToolEnded(ToolResult{Tool: "repair", DryRun: true, End: End{Exited: true, Code: 5}})
	d.hold(HoldOK)
	d.wantActs(ActRepair)
	d.m.ToolEnded(ToolResult{Tool: "repair", End: End{Exited: true, Code: 3}, Stop: "STOP: y", Result: "x"})
	per := d.m.Persist()
	if per.Repair != "dead" || per.DeadBy != "repair" || per.RepairTries != 1 || !per.EverHeld {
		t.Fatalf("persisted: %+v", per)
	}
	d2 := newUI(t, d.m, testPlan(s0Sec))
	d2.wantActs() // no repair --dry-run again
	d2.want(ScreenHold)
	f := d2.m.View(d2.now)
	if got := rowsOf(f); got[1] != "The repair did not finish: the slot state is not known. Do not try again yourself." ||
		f.Keys[PlaceMic] != "Check again" {
		t.Fatalf("new UI after exit 3: %q %q", got, f.Keys)
	}
	d2.hold(HoldOK)
	d2.wantActs(ActReadAB)
	// a dry-run result that comes anyway (an old child) offers nothing
	d2.m.ToolEnded(ToolResult{Tool: "repair", DryRun: true, End: End{Exited: true, Code: 5}})
	d2.hold(HoldOK)
	if a := d2.acts(); len(a) != 1 || a[0] != ActReadAB {
		t.Fatalf("after exit 3 and a dry run: %v", a)
	}

	// P2: a new UI after each refusal: at most MaxRepairTries --yes runs
	d = newDrive(t, false, "off")
	d.toHold()
	runs := 0
	for ui := 0; ui < 4; ui++ {
		if ui > 0 {
			d = newUI(t, d.m, testPlan(s0Sec))
			if a := d.acts(); len(a) != 1 || a[0] != ActRepairCheck {
				t.Fatalf("new UI %d: %v", ui, a)
			}
		}
		d.m.ToolEnded(ToolResult{Tool: "repair", DryRun: true, End: End{Exited: true, Code: 5}})
		d.hold(HoldOK)
		a := d.acts()
		if len(a) != 1 || a[0] != ActRepair {
			// no offer: OK only checks again (the dry run)
			if ui < MaxRepairTries || len(a) != 1 || a[0] != ActRepairCheck {
				t.Fatalf("UI %d: %v", ui, a)
			}
			break
		}
		runs++
		d.m.ToolEnded(ToolResult{Tool: "repair", End: End{Exited: true, Code: 1}, Result: "repair refused, nothing written: x"})
		d.m.Take() // the dry run after the refusal (L-1); the next UI runs its own
	}
	if runs != MaxRepairTries {
		t.Fatalf("%d repair --yes runs across new UIs, want %d", runs, MaxRepairTries)
	}
}

// TestR3RepairOnlyForItsHold (M-2; the probes P7, P16, RP7): the repair
// runs only in a hold that an install of that plan ended on: never after
// a plan that was only shown, never after Done or Stopped, never after a
// switch hold, never with another plan; and never --yes from a screen that
// shows no offer.
func TestR3RepairOnlyForItsHold(t *testing.T) {
	p := testPlan(s0Sec)
	// P7, P16: a plan of this boot on file, a switch hold, a new UI
	d := newDrive(t, false, "off")
	d.toSwitchRun()
	d.m.ToolEnded(switch3)
	d2 := newUI(t, d.m, p)
	d2.wantActs()
	d2.m.ToolEnded(ToolResult{Tool: "repair", DryRun: true, End: End{Exited: true, Code: 5}}) // as the old UI could have
	f := d2.m.View(d2.now)
	d2.hold(HoldOK)
	if a := d2.acts(); len(a) != 1 || a[0] != ActReadAB || f.Keys[PlaceMic] == "Hold: repair" || strings.Contains(bodyOf(f), "can be repaired") {
		t.Fatalf("P16: %v %q %q", a, f.Keys, rowsOf(f))
	}
	// a plan shown, then Back (never installed), then a hold of a switch
	d = newDrive(t, false, "off")
	d.toPlanning()
	d.m.PlanEnded(PlanResult{Plan: p})
	d.press(KeyBack)
	d.want(ScreenStatus)
	d.m.Take()
	if d.m.Persist().HoldPlan != "" {
		t.Fatal("a plan that was only shown is a hold plan")
	}
	// Done and Stopped are no hold of a plan
	for _, c := range []struct {
		after []byte
		code  int
	}{{d2Sec, 0}, {s0Sec, 1}, {d1Sec, 1}} {
		d = newDrive(t, false, "off")
		d.m.Plan, d.m.running, d.m.Screen = p, "install", ScreenProgress
		d.m.InstallEnded(Classify(p, s0Sec, c.after, nil, End{Exited: true, Code: c.code}))
		if d.m.Persist().HoldPlan != "" || len(d.m.Take()) != 0 {
			t.Fatalf("%s: a hold plan", d.m.Screen)
		}
	}
	// the hold of plan X in a UI that has plan Y (another id): no repair
	d = newDrive(t, false, "off")
	d.toHold()
	other := testPlan(s0Sec)
	other.ID = "fedcba9876543210fedc"
	d2 = newUI(t, d.m, other)
	d2.wantActs()
	d2.want(ScreenHold)
	d2.hold(HoldOK)
	d2.wantActs(ActReadAB)
	// and with that hold's own plan: the dry run
	d3 := newUI(t, d.m, p)
	d3.wantActs(ActRepairCheck)
}

// TestR3SafeNeedsOwnRead (M-3; the probe P5): the safe screen ("The power
// can be disconnected.") exactly for exit 0 with the SAFE line and the
// flow's own read showing the kept slot successful; every other end stays
// on the hold, with no "disconnect".
func TestR3SafeNeedsOwnRead(t *testing.T) {
	for _, dry := range []bool{true, false} {
		for _, code := range []int{-1, 0, 1, 2, 3, 4, 5, 9} {
			for _, safe := range []bool{false, true} {
				d := newDrive(t, false, "off")
				d.toHold()
				if !dry {
					d.m.ToolEnded(ToolResult{Tool: "repair", DryRun: true, End: End{Exited: true, Code: 5}})
					d.hold(HoldOK)
					d.wantActs(ActRepair)
				}
				r := ToolResult{Tool: "repair", DryRun: dry, End: End{Exited: true, Code: code}, Safe: safe, After: d1Sec,
					Result: "x", Stop: "STOP: y"}
				if code == -1 {
					r.End = End{Signal: "killed"}
				}
				d.m.ToolEnded(r)
				f := d.m.View(d.now)
				says := strings.Contains(bodyOf(f), "can be disconnected")
				if want := code == 0 && safe; (d.m.Screen == ScreenSafe) != want || says != want || (d.m.RestartBlocked() == "") != want {
					t.Errorf("dry %v exit %d safe %v: screen %s, says disconnect %v, blocked %q", dry, code, safe, d.m.Screen, says, d.m.RestartBlocked())
				}
				if !dry && code != 0 && code != 1 && (d.m.repair != repairDead || f.Keys[PlaceMic] != "Check again") {
					t.Errorf("--yes exit %d: not dead (%d), keys %q", code, d.m.repair, f.Keys)
				}
			}
		}
	}
	// exit 0 + SAFE, but the flow's own read: unreadable, not valid, the
	// kept slot (A) not successful
	for name, after := range map[string][]byte{"unreadable": nil, "not valid": make([]byte, 512),
		"A trying": sector([3]byte{15, 7, 0}, [3]byte{0, 0, 0}, 0), "B only": sector([3]byte{0, 0, 0}, [3]byte{15, 0, 1}, 0)} {
		for _, dry := range []bool{true, false} {
			d := newDrive(t, false, "off")
			d.toHold()
			if !dry {
				d.m.ToolEnded(ToolResult{Tool: "repair", DryRun: true, End: End{Exited: true, Code: 5}})
				d.hold(HoldOK)
				d.wantActs(ActRepair)
			}
			d.m.ToolEnded(ToolResult{Tool: "repair", DryRun: dry, End: End{Exited: true}, Safe: true, After: after})
			d.want(ScreenHold)
			f := d.m.View(d.now)
			if strings.Contains(bodyOf(f), "can be disconnected") || d.m.RestartBlocked() == "" {
				t.Errorf("own read %s, dry %v: %q", name, dry, rowsOf(f))
			}
			// a dry run: the repair cannot run here; after a write: not known
			want := "The slot table cannot be repaired here: The installer's own check of the slot table does not show the kept slot as working."
			if !dry {
				want = "The repair did not finish: the slot state is not known. Do not try again yourself."
			}
			if !hasRow(f, want) || !dry && d.m.repair != repairDead || d.m.repairWhy != txtOwnReadDiffers {
				t.Errorf("own read %s, dry %v: %q", name, dry, rowsOf(f))
			}
		}
	}
}

// TestR3RecheckClearsOffer (L-2; the probe P4): a read of the sector while
// the repair is offered clears the offer; the OK label then says what OK
// does, and the 3 s hold starts no repair.
func TestR3RecheckClearsOffer(t *testing.T) {
	d := newDrive(t, false, "off")
	d.toHold()
	d.m.ToolEnded(ToolResult{Tool: "repair", DryRun: true, End: End{Exited: true, Code: 5}})
	d.m.Rechecked(StateD2, "the target is armed")
	f := d.m.View(d.now)
	if f.Keys[PlaceMic] != "Check again" || strings.Contains(bodyOf(f), "Hold OK (Mic) for 3 seconds to repair") ||
		!hasRow(f, "Checked again: Slot B is installed and starts next. Keep the unit powered.") {
		t.Fatalf("after the read: %q %q", f.Keys, rowsOf(f))
	}
	d.m.Handle(Event{KeyOK, true}, d.now)
	d.now = d.now.Add(HoldOK)
	d.m.Tick(d.now)
	d.m.Handle(Event{KeyOK, false}, d.now)
	if a := d.acts(); len(a) != 1 || a[0] != ActRepairCheck {
		t.Fatalf("OK held after the read: %v", a)
	}
}

// TestR3SwitchRunning (L-3; the probe P14): while the switch runs, no key
// changes the screen or removes its power row; a new UI shows the same.
func TestR3SwitchRunning(t *testing.T) {
	d := newDrive(t, false, "off")
	d.toSwitchRun()
	check := func(name string, d *drive) {
		t.Helper()
		f := d.m.View(d.now)
		if got := rowsOf(f); f.LED != "red" || got[1] != "Keep the unit powered until the switch ends." ||
			f.Keys != [KeyPlaces]string{} || d.m.RestartBlocked() != "the switch runs" {
			t.Errorf("%s: %q %q %q", name, got, f.LED, f.Keys)
		}
	}
	check("switch runs", d)
	if got := rowsOf(d.m.View(d.now)); got[0] != "Slot B is made the slot that starts next; slot A becomes the fallback." {
		t.Errorf("%q", got)
	}
	for _, k := range []Key{KeyNext, KeyPrev, KeyBack, KeyOK} {
		d.press(k)
		d.want(ScreenSwitching)
		if d.m.Msg != "The switch cannot be stopped here. Keep the unit powered until it ends." || d.m.switchTo != "b" {
			t.Errorf("key %d: %q %q", k, d.m.Msg, d.m.switchTo)
		}
		check("after a key", d)
	}
	d.wantActs()
	d2 := newUI(t, d.m, nil)
	d2.want(ScreenSwitching)
	check("new UI", d2)
	// the probe's own form: no slot known
	d3 := newDrive(t, false, "off")
	d3.m.Resume(Persisted{Running: "switch"}, nil)
	check("new UI, no slot", d3)
}

// TestR3SwitchDone (L-4; the probe P13): Done only after exit 0 with
// SWITCH-SAFE and the flow's own read (the new slot successful and next).
func TestR3SwitchDone(t *testing.T) {
	bOK := sector([3]byte{14, 0, 1}, [3]byte{15, 0, 1}, 0)
	for _, c := range []struct {
		name  string
		safe  bool
		after []byte
		done  bool
	}{
		{"SAFE and the own read", true, bOK, true},
		{"no SAFE line", false, bOK, false},
		{"own read unreadable", true, nil, false},
		{"own read: A still next", true, sector([3]byte{15, 0, 1}, [3]byte{14, 0, 1}, 0), false},
		{"own read: B trying", true, sector([3]byte{14, 0, 1}, [3]byte{15, 7, 0}, 0), false},
	} {
		d := newDrive(t, false, "off")
		d.toSwitchRun()
		d.m.ToolEnded(ToolResult{Tool: "switch", End: End{Exited: true}, Safe: c.safe, After: c.after})
		if c.done {
			d.want(ScreenStatus)
			if d.m.Msg != "Done. Slot B starts next. Slot A is the fallback." {
				t.Errorf("%s: %q", c.name, d.m.Msg)
			}
			continue
		}
		d.want(ScreenHold)
		if f := d.m.View(d.now); !strings.Contains(bodyOf(f), "KEEP THE UNIT POWERED") || d.m.RestartBlocked() == "" {
			t.Errorf("%s: %q", c.name, rowsOf(f))
		}
	}
}

// TestR3SafeKept (L-5; the probe P6): a UI started again after the safe end
// shows the safe screen, and after Back the status (never the hold again).
func TestR3SafeKept(t *testing.T) {
	d := newDrive(t, false, "off")
	d.toHold()
	d.m.ToolEnded(ToolResult{Tool: "repair", DryRun: true, End: End{Exited: true}, Safe: true, After: d1Sec})
	d.want(ScreenSafe)
	d2 := newUI(t, d.m, testPlan(s0Sec))
	d2.wantActs()
	d2.want(ScreenSafe)
	if f := d2.m.View(d2.now); rowsOf(f)[0] != "Slot A starts next. Slot B will not start until it is installed again." ||
		d2.m.RestartBlocked() != "" || f.LED != "green" {
		t.Fatalf("new UI after the safe end: %q %q", rowsOf(f), d2.m.RestartBlocked())
	}
	d2.press(KeyBack)
	d2.want(ScreenStatus)
	d3 := newUI(t, d2.m, testPlan(s0Sec))
	d3.wantActs()
	d3.want(ScreenStatus)
	if d3.m.RestartBlocked() != "" {
		t.Fatal(d3.m.RestartBlocked())
	}
}

// TestR3PanelTerminal (L-6; the probe P11): only the panel's own OK (Mic)
// answers yes. A terminal (a --stdin UI, or the take-over of the panel's
// UI) answers No only.
func TestR3PanelTerminal(t *testing.T) {
	toPanel := func(d *drive) {
		d.toChecklist()
		d.m.DriveEjected(true)
		d.wantActs(ActReadDrive)
		d.m.DriveRead(DriveResult{OK: true, Name: `"fw.bin"`, Size: 973})
		d.want(ScreenPanel)
	}
	d := newDrive(t, true, "off")
	d.m.TerminalAnswersPanel = false // a release build
	toPanel(d)
	f := d.m.View(d.now)
	if !strings.Contains(f.Footer, "b = No") || strings.Contains(f.Footer, "Enter") ||
		!hasRow(f, "  Answer Yes on the unit's panel: leave this terminal (q), then press OK (Mic) on the panel. b = No.") {
		t.Fatalf("terminal: %q %q", f.Footer, rowsOf(f))
	}
	for _, k := range []Key{KeyOK, KeyYes} {
		d.press(k)
		d.want(ScreenPanel)
		d.wantActs()
		if d.m.PanelConfirmed() || d.m.Msg != "Answer Yes on the unit's panel: leave this terminal (q), then press OK (Mic) on the panel." {
			t.Fatalf("terminal key %d: %v %q", k, d.m.PanelConfirmed(), d.m.Msg)
		}
	}
	// the panel's UI with a take-over terminal: the same; after the take-over the Mic key answers
	d = newDrive(t, false, "off")
	toPanel(d)
	d.m.SetTerminal(true)
	d.press(KeyOK)
	d.press(KeyYes)
	d.want(ScreenPanel)
	d.wantActs()
	d.m.SetTerminal(false)
	d.press(KeyYes) // y is no panel key
	d.want(ScreenPanel)
	d.press(KeyOK)
	d.want(ScreenPlanning)
	d.wantActs(ActStartPlan)
	if !d.m.PanelConfirmed() {
		t.Fatal("the panel's Mic key")
	}
	// No from the terminal
	d = newDrive(t, true, "off")
	d.m.TerminalAnswersPanel = false
	toPanel(d)
	d.press(KeyBack)
	d.want(ScreenRefused)
	d.wantActs(ActGiveBackDrive)
}
