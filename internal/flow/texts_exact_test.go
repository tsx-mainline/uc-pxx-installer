package flow

import (
	"math"
	"strings"
	"testing"
	"time"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/plan"
	"uc-pxx-installer/internal/status"
)

// The safety sentences of every end state and of the live line, to the
// letter (review L-2: a mutation of any of them must fail a test).

func rowsOf(f Frame) []string {
	var r []string
	for _, l := range f.Body {
		r = append(r, l.Text)
	}
	return r
}

func hasRow(f Frame, want string) bool {
	for _, l := range f.Body {
		if l.Text == want {
			return true
		}
	}
	return false
}

func onScreen(lay Rows, want string) bool {
	all := ""
	for _, b := range lay.Body {
		all += b.Text + " "
	}
	return strings.Contains(strings.Join(strings.Fields(all), " "), want)
}

func TestStateSentencesExact(t *testing.T) {
	for st, want := range map[string]string{
		StateS0:      "Nothing on the unit was changed. Slot A starts as before. The power can be disconnected.",
		StateD1:      "Slot B is not finished and will not start; slot A starts. The power can be disconnected.",
		StateD2:      "Slot B is installed and starts next. If it does not start, slot A starts again after a few restarts.",
		StateUnknown: "The slot state is not known.",
	} {
		if got := stateSentence(st, "a", "b"); got != want {
			t.Errorf("%s: %q, want %q", st, got, want)
		}
	}
}

func TestEndScreensExact(t *testing.T) {
	p := testPlan(s0Sec)
	for _, c := range []struct {
		name  string
		after []byte
		end   End
		rows  []string // the first rows of the body, exactly
		level int      // the level of the first row
	}{
		{"done", d2Sec, End{Exited: true}, []string{
			"Slot B is installed and starts next. If it does not start, slot A starts again after a few restarts.",
			"Slot A was not touched."}, levelGood},
		{"stopped S0", s0Sec, End{Exited: true, Code: 1}, []string{
			"Nothing on the unit was changed. Slot A starts as before. The power can be disconnected.", "",
			"Why: The installer stopped with an error (the details are below and in the log)."}, levelGood},
		{"stopped D1", d1Sec, End{Exited: true, Code: 1}, []string{
			"Slot B is not finished and will not start; slot A starts. The power can be disconnected.", "",
			"Why: The installer stopped with an error (the details are below and in the log)."}, levelWarn},
		{"stopped D2", d2Sec, End{Signal: "killed"}, []string{
			"Slot B is installed and starts next. If it does not start, slot A starts again after a few restarts.", "",
			"Why: The installer stopped unexpectedly."}, levelGood},
		{"hold", d1Sec, End{Exited: true, Code: 3}, []string{
			"!!! KEEP THE UNIT POWERED. DO NOT RESTART IT.",
			"The install did not end normally, and the installer cannot tell which slot the unit would start.",
			"",
			"Checking whether the slot table can be repaired (nothing is written) ...",
			"",
			"What to do:",
			"  1. Leave the unit on.",
			"  2. Press OK (Mic) to check the slots again.",
			"  3. The log folder is being written to the drive P10-INSTALL ...",
			"  4. " + Contact}, levelBad},
	} {
		d := newDrive(t, false, "off")
		d.toProgress()
		d.m.InstallEnded(Classify(p, s0Sec, c.after, nil, c.end))
		f := d.m.View(d.now)
		got := rowsOf(f)
		if len(got) < len(c.rows) {
			t.Fatalf("%s: %q", c.name, got)
		}
		for i, w := range c.rows {
			if got[i] != w {
				t.Errorf("%s row %d: %q\nwant %q", c.name, i, got[i], w)
			}
		}
		if f.Body[0].Level != c.level {
			t.Errorf("%s: level %d", c.name, f.Body[0].Level)
		}
		if c.name == "hold" && (!strings.HasSuffix(f.Title, "  -  Install stopped: keep the unit powered") ||
			f.Keys[PlaceMic] != "" || f.Keys[PlaceSpeaker] != "Log" || f.Keys[PlaceVolUp] != "" || f.Keys[PlaceVolDown] != "") {
			t.Errorf("hold, while the repair check runs: %q %q", f.Title, f.Keys)
		}
	}
	d := newDrive(t, false, "off")
	d.m.InstallRefused("The slot state changed after the check (something else wrote to the unit). Nothing was written. Start again.")
	f := d.m.View(d.now)
	// (the reason says itself that nothing was written: no second row for it)
	if got := rowsOf(f); got[0] != "Why: The slot state changed after the check (something else wrote to the unit). Nothing was written. Start again." {
		t.Errorf("refused: %q", got)
	}
}

// TestHoldReread: a read of the sector on the hold never says that the
// power can be disconnected, and never replaces the warning (review R3 L-2;
// it replaced the round-2 rule L-5): only the repair's check ends a hold.
// On a hold where the repair may run, OK runs the repair's dry run; where
// it may not (after an exit 3), OK reads the sector.
func TestHoldReread(t *testing.T) {
	d := newDrive(t, false, "off")
	d.toHold()
	d.m.ToolEnded(ToolResult{Tool: "repair", DryRun: true, End: End{Exited: true, Code: 1}, Result: "x"})
	d.press(KeyOK)
	d.wantActs(ActRepairCheck) // the repair may run: its dry run decides
	d.m.ToolEnded(ToolResult{Tool: "repair", DryRun: true, End: End{Exited: true, Code: 5}})
	d.hold(HoldOK)
	d.wantActs(ActRepair)
	d.m.ToolEnded(ToolResult{Tool: "repair", End: End{Exited: true, Code: 3}, Stop: "STOP: x"})
	d.press(KeyOK)
	d.wantActs(ActReadAB) // dead: a read only
	for _, st := range []string{StateS0, StateD1, StateD2, StateUnknown} {
		d.m.Rechecked(st, "")
		f := d.m.View(d.now)
		got := rowsOf(f)
		all := strings.Join(got, " ")
		if got[0] != "!!! KEEP THE UNIT POWERED. DO NOT RESTART IT." || strings.Contains(all, "can be disconnected") ||
			d.m.Screen != ScreenHold || d.m.RestartBlocked() == "" || f.LED != "red" {
			t.Errorf("%s: %q", st, got)
		}
		want := map[string]string{
			StateS0:      "Checked again: Nothing on the unit was changed; slot A starts as before. Keep the unit powered.",
			StateD1:      "Checked again: Slot B is not finished and will not start; slot A starts. Keep the unit powered.",
			StateD2:      "Checked again: Slot B is installed and starts next. Keep the unit powered.",
			StateUnknown: "Checked again: the slot state is still not known. Keep the unit powered.",
		}[st]
		if !hasRow(f, want) {
			t.Errorf("%s: no row %q: %q", st, want, got)
		}
	}
}

// TestProgressScreenExact: the progress screen has one power line, one row
// for the check(s) before the first write, the steps in plain words, and
// one detail row under the item that runs.
func TestProgressScreenExact(t *testing.T) {
	d := newDrive(t, false, "off")
	d.toProgress()
	pr := NewProgress(true, d.now)
	pr.SetPlan(StepsOf(testPlan(s0Sec)))
	d.m.ChildStarted(pr)
	at := func(want ...string) {
		t.Helper()
		f := d.m.View(d.now)
		for _, w := range want {
			if !hasRow(f, w) {
				t.Errorf("no row %q in %q", w, rowsOf(f))
			}
		}
		for _, r := range rowsOf(f) {
			for _, bad := range []string{"bootctl", "Safe to disconnect", "Restart", "1/7", "plan", "A/B"} {
				if strings.Contains(r, bad) {
					t.Errorf("the progress says %q: %q", bad, r)
				}
			}
		}
	}
	at("Installing to slot B. Slot A is not touched.", "Keep the unit powered until the install ends.",
		">> Checking the firmware and the unit again before the first write")
	d.m.ChildLine("pass 1: check the whole firmware", d.now)
	d.m.ChildLine("computing the plan again from the unit and the firmware (nothing is written yet)", d.now)
	d.m.ChildLine(" 1/7 bootctl disable --slot b", d.now)
	at("ok Checking the firmware and the unit again before the first write", ">> Mark slot B as not bootable")
	d.m.ChildLine(" 2/7 write uboot_b (4194304 bytes)", d.now.Add(time.Second))
	d.m.ChildLine(" 3/7 write system_b (2147483648 bytes)", d.now.Add(2*time.Second))
	d.now = d.now.Add(30 * time.Second)
	at("ok Mark slot B as not bootable", "ok Write U-Boot", ">> Write the system",
		"            2147 MB to write, 28 s so far; about 8 min 32 s at 4.2 MB/s, the speed of the earlier writes")
	if f := pr.Fraction(d.now); f != 2.0/7 {
		t.Errorf("fraction %v after 2 of 7 steps", f)
	}
	d.m.ChildLine(" 4/7 sync", d.now)
	d.m.ChildLine(" 5/7 verify", d.now)
	d.m.ChildLine(" 6/7 bootctl arm-trying --slot b --lower-keeper", d.now)
	d.m.ChildLine(" 7/7 check the A/B sector", d.now)
	at("ok Verify (read everything back)", "ok Make slot B the next boot", ">> Check the slot table")
	if f := pr.Fraction(d.now); f != 6.0/7 {
		t.Errorf("fraction %v before the last check (6 of 7 steps done)", f)
	}
}

// TestKeySourceRows: the planning screen names where the key comes from
// (the key file, or the search in the slots), the plan screen the plan's
// source; never both rows, never a row without a source.
func TestKeySourceRows(t *testing.T) {
	for _, kf := range []bool{true, false} {
		d := newDrive(t, false, "off")
		d.toChecklist()
		d.confirmAll()
		d.m.DriveEjected(true)
		d.m.DriveRead(DriveResult{OK: true, Name: `"fw.bin"`, Size: 973, KeyFile: kf})
		d.want(ScreenPanel)
		d.press(KeyOK)
		d.want(ScreenPlanning)
		f := d.m.View(d.now)
		file, search := hasRow(f, "Key: from the key file p10-keys.txt"), hasRow(f, "Key: from the Crestron firmware on this unit")
		if file != kf || search == kf || !hasRow(f, `Firmware file: "fw.bin" (0 MB)`) {
			t.Errorf("key file %v: rows %q", kf, rowsOf(f))
		}
		p := testPlan(s0Sec)
		p.KeySource = map[bool]string{true: "key file", false: "updater libraries in system_b"}[kf]
		d.m.PlanEnded(PlanResult{Plan: p})
		f = d.m.View(d.now)
		for _, r := range rowsOf(f) { // the plan screen shows only the firmware and the slots (owner r3)
			if strings.HasPrefix(r, "Key:") || strings.Contains(r, "auto") || strings.Contains(r, "twice") {
				t.Errorf("plan: %q", r)
			}
		}
	}
}

// TestMessagesCleared: a message of the planning screen (or of a key in
// the install) is not shown on the next screen.
func TestMessagesCleared(t *testing.T) {
	for _, next := range []string{"plan", "refused", "keeper"} {
		d := newDrive(t, false, "off")
		d.toPlanning()
		d.press(KeyBack)
		if d.m.Msg == "" {
			t.Fatal("no message on Back")
		}
		switch next {
		case "plan":
			d.m.PlanEnded(PlanResult{Plan: testPlan(s0Sec)})
		case "refused":
			d.m.PlanEnded(PlanResult{Refusal: "refused, nothing written: x", End: End{Exited: true, Code: 1}})
		case "keeper":
			d.m.PlanEnded(PlanResult{Refusal: "install refused: keeper: both slots are successful; choose the keeper", End: End{Exited: true, Code: 1}})
		}
		if d.m.Msg != "" {
			t.Errorf("%s: the planning message stays: %q (screen %s)", next, d.m.Msg, d.m.Screen)
		}
	}
	d := newDrive(t, false, "off")
	d.toProgress()
	d.press(KeyBack)
	d.m.InstallRefused("x")
	if d.m.Msg != "" {
		t.Errorf("refused after the install key: %q", d.m.Msg)
	}
}

// TestLayoutAccounting: Problem counts hidden rows (but not on the log); a
// message of more than 3 rows is cut with a sign and counted; Squeeze drops
// only empty rows; a screen that does not scroll shows a sign in its last
// row; Cells counts wide characters twice.
func TestLayoutAccounting(t *testing.T) {
	var body []status.Line
	for i := 0; i < 40; i++ {
		body = append(body, status.Line{Text: "row"})
	}
	f := Frame{Title: "t", Body: body}
	lay := f.Layout(120, 37)
	if lay.Hidden != 9 || lay.Problem(ScreenStatus) == "" || lay.Problem(ScreenLog) != "" ||
		lay.Body[len(lay.Body)-1].Text != "!! 10 more rows do not fit on this screen." || lay.Body[len(lay.Body)-1].Level != levelBad {
		t.Errorf("hidden %d %q %q", lay.Hidden, lay.Problem(ScreenStatus), lay.Body[len(lay.Body)-1].Text)
	}
	f.ScrollKeys = "Vol+ / Vol- scroll"
	if lay := f.Layout(120, 37); lay.Problem(ScreenPlan) == "" || lay.Rule != " rows 1-31 of 40 " {
		t.Errorf("scroll: %q %q", lay.Problem(ScreenPlan), lay.Rule)
	}
	m := Frame{Msg: strings.Repeat("message ", 80)}
	lay = m.Layout(120, 37)
	if len(lay.Msg) != MsgRows || lay.Cut == 0 || lay.Problem(ScreenStatus) == "" || !strings.HasSuffix(lay.Msg[1], " ...") || Cells(lay.Msg[1]) > 120 {
		t.Errorf("message: %d rows, cut %d, %q", len(lay.Msg), lay.Cut, lay.Msg)
	}
	if lay := (Frame{Msg: strings.Repeat("message ", 25)}).Layout(120, 37); lay.Cut != 0 || len(lay.Msg) != MsgRows {
		t.Errorf("a 2-row message: %d rows, cut %d", len(lay.Msg), lay.Cut)
	}
	if lay := (Frame{Msg: "short"}).Layout(120, 37); len(lay.Msg) != MsgRows || lay.Msg[1] != "" || lay.Room != 37-4-MsgRows {
		t.Errorf("a short message: %q, room %d (the message area is fixed)", lay.Msg, lay.Room)
	}
	// the key legend: one column per touch key, each label above its key
	k := Frame{Keys: [KeyPlaces]string{PlaceSpeaker: "Back", PlaceVolDown: "Previous", PlaceVolUp: "Next", PlaceMic: "OK"}}
	lay = k.Layout(120, 37)
	for _, c := range []struct {
		label string
		at    int // centred on the key: its centre in px / 16 px per cell, minus half the label
	}{{"Back", 36}, {"Previous", 78}, {"Next", 94}, {"OK", 109}} { // the centres 609, 1312, 1535, 1759 px
		if strings.Index(lay.Footer, c.label) != c.at {
			t.Errorf("legend %q: %q at %d, want %d", lay.Footer, c.label, strings.Index(lay.Footer, c.label), c.at)
		}
	}
	// Squeeze: 30 text rows and 6 empty rows on 32: the empty rows go, the
	// text rows stay, in order
	var sq []status.Line
	for i := 0; i < 36; i++ {
		txt := "text " + strings.Repeat("x", i)
		if i%6 == 5 {
			txt = ""
		}
		sq = append(sq, status.Line{Text: txt})
	}
	lay = Frame{Body: sq, Squeeze: true}.Layout(120, 37)
	var texts []string
	for _, b := range lay.Body {
		if b.Text != "" {
			texts = append(texts, b.Text)
		}
	}
	if lay.Hidden != 0 || len(texts) != 30 || texts[0] != "text " || texts[29] != "text "+strings.Repeat("x", 34) {
		t.Errorf("squeeze: hidden %d, %d text rows", lay.Hidden, len(texts))
	}
	// cells
	if Cells("ab") != 2 || Cells("漢字") != 4 || Cells("é") != 1 || Cells("é") != 1 {
		t.Errorf("cells %d %d %d %d", Cells("ab"), Cells("漢字"), Cells("é"), Cells("é"))
	}
	for _, r := range Wrap(strings.Repeat("漢字 ", 50), 40) {
		if Cells(r) > 40 {
			t.Errorf("a row of %d cells", Cells(r))
		}
	}
	if lay := (Frame{Title: strings.Repeat("漢", 70)}).Layout(120, 37); Cells(lay.Title) > 120 || lay.Cut != 1 {
		t.Errorf("wide title %d cells, cut %d", Cells(lay.Title), lay.Cut)
	}
}

// TestLongReasons: however long the reason, the safety sentence of the
// stopped screen, and the warning, contact and re-read rows of the hold are
// on the screen (the reason is cut with a sign, review L-3); Next (Vol+) on the
// progress does not scroll the live line away.
func TestLongReasons(t *testing.T) {
	now := time.Unix(100000, 0)
	for _, n := range []int{1, 10, 40} {
		why := strings.TrimSpace(strings.Repeat(longWhy+" ", n))
		for _, c := range []struct {
			after []byte
			end   End
		}{{d1Sec, End{Exited: true, Code: 1}}, {s0Sec, End{Exited: true, Code: 1}}, {d2Sec, End{Signal: "killed"}}, {d1Sec, End{Exited: true, Code: 3}}} {
			m := New("UC-P10 installer  -  install image  -  p10inst", false)
			m.SetUnit(longUnit(AutoUpdateView{State: "off"}, unit2Lines()))
			m.Plan = longPlan()
			m.Prog = NewProgress(true, now.Add(-400*time.Second))
			m.Prog.Stop = "STOP " + why
			m.running = "install"
			o := Classify(testPlan(s0Sec), s0Sec, c.after, nil, c.end)
			o.Why = why
			m.InstallEnded(o)
			m.BundleDone(longBundle)
			lay := m.View(now).Layout(PanelCols, PanelRows)
			var key []string
			switch m.Screen {
			case ScreenStopped:
				key = []string{stateSentence(o.State, "a", "b")}
			case ScreenHold:
				key = []string{"!!! " + txtHoldTitle, "1. Leave the unit on.", Contact, txtHoldReread}
			}
			for _, k := range key {
				if !onScreen(lay, k) {
					t.Errorf("why x%d %s: %q is not on the screen", n, m.Screen, k)
				}
			}
			if lay.Problem(m.Screen) != "" || (n > 1 && m.Screen == ScreenStopped && !onScreen(lay, "(cut: the whole text is in the log)")) {
				t.Errorf("why x%d %s: %q", n, m.Screen, lay.Problem(m.Screen))
			}
		}
	}
	m := New("t", false)
	m.SetUnit(longUnit(AutoUpdateView{State: "off"}, unit2Lines()))
	m.Plan = longPlan()
	m.Screen, m.running = ScreenProgress, "install"
	m.Prog = NewProgress(true, now.Add(-400*time.Second))
	m.Prog.SetPlan(StepsOf(m.Plan))
	m.ChildLine(" 1/13 bootctl disable --slot b", now)
	m.ChildLine(" 2/13 write uboot_b (4194304 bytes)", now)
	m.ChildLine("STOP: "+strings.Repeat(longWhy+" ", 6), now)
	for i := 0; i < 8; i++ {
		m.Handle(Event{Key: KeyNext, Down: true}, now)
	}
	lay := m.View(now).Layout(PanelCols, PanelRows)
	if l := txtProgressPower; !onScreen(lay, l) || lay.Problem(m.Screen) != "" {
		t.Errorf("progress after Next: the power line %q is not on the screen (%q)", l, lay.Problem(m.Screen))
	}
}

// TestProgressOneCheck: the install child of devmode-r4 checks the
// firmware once (no "pass 1" and no "computing the plan again" rows, only
// "re-check:" rows): the progress shows the same one row before the first
// write, then the steps.
func TestProgressOneCheck(t *testing.T) {
	d := newDrive(t, false, "off")
	d.toProgress()
	pr := NewProgress(true, d.now)
	pr.SetPlan(StepsOf(testPlan(s0Sec)))
	d.m.ChildStarted(pr)
	d.m.ChildLine("the plan of /run/p10inst/flow/plan.json: its pass 1 checked the whole firmware against the manifests; the firmware is not hashed again", d.now)
	d.m.ChildLine("re-check: the plan again from the unit, the firmware not hashed again (its identity, the manifest, the container; nothing is written yet)", d.now)
	if f := d.m.View(d.now); !hasRow(f, ">> Checking the firmware and the unit again before the first write") {
		t.Fatalf("%q", rowsOf(f))
	}
	d.m.ChildLine("re-check: the plan has the same id 0123; firmware source drive", d.now)
	d.m.ChildLine(" 1/7 bootctl disable --slot b", d.now)
	if f := d.m.View(d.now); !hasRow(f, "ok Checking the firmware and the unit again before the first write") || !hasRow(f, ">> Mark slot B as not bootable") {
		t.Fatalf("%q", rowsOf(f))
	}
}

// TestKeepKeyStepFirst: a plan of devmode-r4 with the keep-key step before
// the disable: the steps are taken as the child prints them (the disable
// is step 2), and the end of the install is classified as before.
func TestKeepKeyStepFirst(t *testing.T) {
	p := testPlan(s0Sec)
	steps := []plan.Step{{N: 1, Op: "keep-key"}}
	for _, st := range p.Steps {
		st.N++
		steps = append(steps, st)
	}
	p.Steps = steps
	for _, c := range []struct {
		after []byte
		end   End
		st    string
		scr   Screen
	}{{d1Sec, End{Exited: true, Code: 1}, StateD1, ScreenStopped}, {d2Sec, End{Exited: true}, StateD2, ScreenDone}, {s0Sec, End{Signal: "killed"}, StateS0, ScreenStopped}} {
		if o := Classify(p, s0Sec, c.after, nil, c.end); o.State != c.st || o.Screen != c.scr {
			t.Errorf("%s: %+v", c.st, o)
		}
	}
	pr := NewProgress(true, time.Unix(0, 0))
	pr.SetPlan(StepsOf(p))
	if pr.Steps[0].Label != "Keep the firmware key on the unit" || pr.Steps[1].Label != "Mark slot B as not bootable" {
		t.Fatalf("%+v", pr.Steps[:2])
	}
	pr.Line(" 1/8 keep-key: the firmware key on the unit (backup, 2 sectors)", time.Unix(1, 0))
	if pr.Cur != 0 || pr.Disabled() {
		t.Fatalf("keep-key: cur %d, disabled %v", pr.Cur, pr.Disabled())
	}
	pr.Line(" 2/8 bootctl disable --slot b", time.Unix(2, 0))
	if pr.Cur != 1 || !pr.Steps[0].Done || !pr.Disabled() {
		t.Fatalf("disable: cur %d", pr.Cur)
	}
}

// TestTryAgainNewUI: "try again" on the stopped screen of a UI that was
// started again (Resume) gives --prereqs-confirmed as the first try did,
// and passes the same gates as Install.
func TestTryAgainNewUI(t *testing.T) {
	d := newDrive(t, false, "off")
	d.m.Resume(Persisted{Screen: "stopped", Outcome: &Outcome{Screen: ScreenStopped, State: StateD1}}, testPlan(s0Sec))
	d.want(ScreenStopped)
	if d.m.PrereqsConfirmed() {
		t.Fatal("confirmed before the try")
	}
	d.press(KeyOK)
	d.want(ScreenFirmware)
	if !d.m.PrereqsConfirmed() {
		t.Fatal("try again: not confirmed")
	}
	d = newDrive(t, false, "on")
	d.m.Resume(Persisted{Screen: "stopped", Outcome: &Outcome{Screen: ScreenStopped, State: StateD1}}, testPlan(s0Sec))
	d.press(KeyOK)
	d.want(ScreenStopped)
	if d.m.Msg != "Crestron auto-update is on. Turn it off in the Crestron web UI, then start the installer again." || d.m.PrereqsConfirmed() {
		t.Fatalf("on: %q", d.m.Msg)
	}
}

// TestProgressBar pins the bar of the install (13 steps of the real plan)
// at the points of the renders: the check before the first write, after
// the disable, in the middle of the system write, at the verify, at the
// last check, and at the end. It counts the steps done.
func TestProgressBar(t *testing.T) {
	p := longPlan()
	t0 := time.Unix(0, 0)
	pr := NewProgress(true, t0)
	pr.SetPlan(StepsOf(p))
	if len(pr.Steps) != 13 {
		t.Fatalf("%d steps", len(pr.Steps))
	}
	at := func(want float64, bar string) {
		t.Helper()
		if f := pr.Fraction(t0); f != want || !strings.Contains(Bar(f, 40), bar) {
			t.Errorf("fraction %v %q, want %v (%s)", f, Bar(f, 40), want, bar)
		}
	}
	line := func(l string, s int) { pr.Line(l, t0.Add(time.Duration(s)*time.Second)) }
	line("pass 1: check the whole firmware against the manifests (nothing is written)", 1)
	line("re-check: the plan again from the unit", 300)
	at(0, "  0 %")
	line(" 1/13 bootctl disable --slot b", 500)
	at(0, "  0 %")
	line(" 2/13 write uboot_b (4194304 bytes)", 501)
	at(1.0/13, "  8 %")
	for i, l := range []string{" 3/13 skip trust_b (already holds the image)", " 4/13 write dtbo_b (4194304 bytes)",
		" 5/13 write vbmeta_b (8192 bytes)", " 6/13 write boot_b (67108864 bytes)", " 7/13 write system_b (1879048192 bytes)"} {
		line(l, 502+i)
	}
	at(6.0/13, " 46 %") // in the middle of the system write: 6 of 13 done
	if e := pr.StepEstimate(pr.Steps[pr.Cur]); e <= 0 {
		t.Errorf("no estimate of the system write: %v", e)
	}
	for i, l := range []string{" 8/13 write vendor_b (402653184 bytes)", " 9/13 write oem_b (1073741824 bytes)", "10/13 sync", "11/13 verify"} {
		line(l, 600+i*60)
	}
	at(10.0/13, " 77 %") // at the verify
	line("12/13 bootctl arm-trying --slot b --lower-keeper", 900)
	line("13/13 check the A/B sector", 901)
	at(12.0/13, " 92 %") // the last check runs
	line("RESULT: install done in 5m38s", 905)
	at(1, "100 %")
}

// toHold: an install that ended on the hold (exit 3); the repair check of
// the hold start is asked.
func (d *drive) toHold() {
	d.t.Helper()
	d.toProgress()
	d.m.InstallEnded(Classify(testPlan(s0Sec), s0Sec, d1Sec, nil, End{Exited: true, Code: 3}))
	d.want(ScreenHold)
	d.wantActs(ActRepairCheck)
}

// rOnlyA is the sector of a repair: only slot A boots (B not bootable).
var rOnlyA = sector([3]byte{15, 0, 1}, [3]byte{0, 0, 0}, 0)

// TestRepairFlow: the hold's repair (owner, DEVMODE-R4 §11): the dry run at
// the hold start; safe → the hold ends; a repair needed → Repair behind the
// 3 s hold; exit 1 → one more try, then no more; exit 3 → keep it powered,
// ask for help, no Repair; exit 0 → safe.
func TestRepairFlow(t *testing.T) {
	dry := func(code int, safe bool) ToolResult {
		return ToolResult{Tool: "repair", DryRun: true, End: End{Exited: true, Code: code}, Safe: safe, After: rOnlyA,
			Result: "repair refused, nothing written: keeper: system_a sha256 differs from the plan"}
	}
	yes := func(code int, safe bool) ToolResult {
		r := dry(code, safe)
		r.DryRun = false
		return r
	}
	t.Run("the check reads safe: the hold ends", func(t *testing.T) {
		d := newDrive(t, false, "off")
		d.toHold()
		if d.m.RestartBlocked() == "" {
			t.Fatal("restart allowed while the check runs")
		}
		d.m.ToolEnded(dry(0, true))
		d.want(ScreenSafe)
		f := d.m.View(d.now)
		if got := rowsOf(f); got[0] != "Slot A starts next. Slot B will not start until it is installed again." ||
			got[1] != "The slot table was checked. The power can be disconnected." || d.m.RestartBlocked() != "" || f.LED != "green" {
			t.Fatalf("safe: %q, blocked %q", got, d.m.RestartBlocked())
		}
		d.press(KeyOK) // install again
		d.want(ScreenFirmware)
	})
	t.Run("a repair is needed: Repair, a refusal, one more try, then no more", func(t *testing.T) {
		d := newDrive(t, false, "off")
		d.toHold()
		d.m.ToolEnded(dry(5, false))
		d.want(ScreenHold)
		f := d.m.View(d.now)
		if f.Keys[PlaceMic] != "Hold: repair" || f.Keys[PlaceVolDown] != "Check again" ||
			!hasRow(f, "The slot table can be repaired: slot A (the slot that was not touched) is made the slot that starts.") {
			t.Fatalf("offer: %q %q", f.Keys, rowsOf(f))
		}
		d.press(KeyPrev) // Vol-, check again: the dry run again (it reads only)
		d.wantActs(ActRepairCheck)
		if f := d.m.View(d.now); f.Keys[PlaceMic] != "" || hasRow(f, "The slot table can be repaired: slot A (the slot that was not touched) is made the slot that starts.") {
			t.Fatalf("the offer stays while the check runs: %q %q", f.Keys, rowsOf(f))
		}
		d.m.ToolEnded(dry(5, false))
		d.hold(2 * time.Second) // 2 s is not enough
		d.wantActs()
		d.want(ScreenHold)
		d.hold(HoldOK)
		d.wantActs(ActRepair)
		d.want(ScreenRepair)
		if d.m.RestartBlocked() != "the repair runs" {
			t.Fatal(d.m.RestartBlocked())
		}
		d.m.ChildLine("REPAIR-CLASS: invalid: the CRC is wrong", d.now)
		for _, p := range []string{"uboot", "trust", "dtbo"} {
			d.m.ChildLine("repair: keeper "+p+"_a ok sha256 0123", d.now)
		}
		f = d.m.View(d.now)
		if !hasRow(f, ">> Checking slot A (3 of 8 parts)") || !hasRow(f, "Keep the unit powered until the repair ends.") {
			t.Fatalf("repair: %q", rowsOf(f))
		}
		d.m.ChildLine("REPAIR-WRITE: 1: the A/B sector is written now", d.now)
		if f = d.m.View(d.now); !hasRow(f, "ok Checking slot A (3 of 8 parts)") || !hasRow(f, ">> Writing the slot table") {
			t.Fatalf("repair write: %q", rowsOf(f))
		}
		d.m.ToolEnded(yes(1, false)) // nothing written: a new dry run (review R3 L-1)
		d.want(ScreenHold)
		d.wantActs(ActRepairCheck)
		if f := d.m.View(d.now); f.Keys[PlaceMic] != "" {
			t.Fatalf("an offer before the new dry run: %q", f.Keys)
		}
		d.m.ToolEnded(dry(5, false)) // its exit 5: one more try
		if f := d.m.View(d.now); f.Keys[PlaceMic] != "Hold: repair" ||
			!hasRow(f, "The first try did not write anything: The slot that was not touched has changed since the check, so it cannot be made the slot that starts.") {
			t.Fatalf("after a refusal: %q %q", f.Keys, rowsOf(f))
		}
		d.hold(HoldOK)
		d.wantActs(ActRepair)
		d.m.ToolEnded(yes(1, false)) // the second refusal: no more
		d.want(ScreenHold)
		d.wantActs()
		f = d.m.View(d.now)
		twice := "The repair was tried twice and did not write anything: The slot that was not touched has changed since the check, so it cannot be made the slot that starts."
		if f.Keys[PlaceMic] != "Check again" || !hasRow(f, twice) {
			t.Fatalf("after two: %q %q", f.Keys, rowsOf(f))
		}
		d.hold(HoldOK) // the dry run (it reads only), but no third offer
		d.wantActs(ActRepairCheck)
		d.m.ToolEnded(dry(5, false))
		if f = d.m.View(d.now); f.Keys[PlaceMic] != "Check again" || !hasRow(f, twice) {
			t.Fatalf("a third offer: %q %q", f.Keys, rowsOf(f))
		}
		d.hold(HoldOK)
		d.wantActs(ActRepairCheck)
		if d.m.repairTries != MaxRepairTries {
			t.Fatal(d.m.repairTries)
		}
	})
	t.Run("exit 3: keep it powered, ask for help, no Repair", func(t *testing.T) {
		d := newDrive(t, false, "off")
		d.toHold()
		d.m.ToolEnded(dry(5, false))
		d.hold(HoldOK)
		d.wantActs(ActRepair)
		d.m.ToolEnded(yes(3, false))
		d.want(ScreenHold)
		f := d.m.View(d.now)
		got := rowsOf(f)
		if got[0] != "!!! KEEP THE UNIT POWERED. DO NOT RESTART IT." ||
			got[1] != "The repair did not finish: the slot state is not known. Do not try again yourself." ||
			!hasRow(f, "  3. "+Contact) || !hasRow(f, "  4. Wait for help before you disconnect the power.") ||
			f.Keys[PlaceMic] == "Hold: repair" || d.m.RestartBlocked() == "" {
			t.Fatalf("dead: %q %q", got, f.Keys)
		}
		d.hold(HoldOK) // OK only reads
		d.wantActs(ActReadAB)
	})
	t.Run("exit 0 after the repair: safe", func(t *testing.T) {
		d := newDrive(t, false, "off")
		d.toHold()
		d.m.ToolEnded(dry(5, false))
		d.hold(HoldOK)
		d.wantActs(ActRepair)
		d.m.ToolEnded(yes(0, true))
		d.want(ScreenSafe)
	})
	t.Run("a hold without the plan: no repair check", func(t *testing.T) {
		d := newDrive(t, false, "off")
		d.m.Resume(Persisted{EverHeld: true}, nil)
		d.want(ScreenHold)
		d.wantActs()
	})
}

// TestSwitchFlow: "Switch which slot starts" from the status menu, only to
// a working slot with the other slot bootable: OK checks (the dry run), the
// confirm shows the predicted change and its bytes, and only its 3 s hold
// writes (owner 2026-11-01).
func TestSwitchFlow(t *testing.T) {
	d := newDrive(t, false, "off")
	d.m.Sel = 2 // Switch
	d.press(KeyOK)
	d.want(ScreenSwitch)
	f := d.m.View(d.now)
	if !hasRow(f, "> Slot B: 1.0.0.94 userdebug, working") || !hasRow(f, "      Start this slot next: slot A becomes the fallback.") ||
		!hasRow(f, "      Slot A starts next already.") || f.Keys[PlaceMic] != "Check" ||
		!hasRow(f, "  Press OK (Mic) to check the switch. Nothing is written yet.") {
		t.Fatalf("switch: %q %q", rowsOf(f), f.Keys)
	}
	d.press(KeyPrev) // A: it starts already
	d.press(KeyOK)
	if d.m.Msg != "Slot A starts next already." {
		t.Fatal(d.m.Msg)
	}
	d.wantActs()
	d.press(KeyNext)
	d.toSwitchRunFromSelection()
	d.m.ToolEnded(ToolResult{Tool: "switch", End: End{Exited: true}, Safe: true, After: sector([3]byte{14, 0, 1}, [3]byte{15, 0, 1}, 0)})
	d.want(ScreenStatus)
	if d.m.Msg != "Done. Slot B starts next. Slot A is the fallback." {
		t.Fatal(d.m.Msg)
	}
	// a slot that is not tested yet cannot be chosen
	d = newDrive(t, false, "off")
	d.m.Unit.Slots[1].AB = avbab.Slot{Priority: 14, Tries: 7}
	d.m.Sel = 2
	d.press(KeyOK)
	d.press(KeyOK)
	if d.m.Msg != "Slot B cannot be chosen: it is not tested yet (7 tries left)." {
		t.Fatal(d.m.Msg)
	}
	d.hold(HoldOK)
	d.wantActs()
	// a refusal: nothing written, the switch screen; exit 3: the hold
	for _, c := range []struct {
		code int
		want Screen
	}{{1, ScreenSwitch}, {3, ScreenHold}} {
		d = newDrive(t, false, "off")
		d.toSwitchRun()
		d.m.ToolEnded(ToolResult{Tool: "switch", End: End{Exited: true, Code: c.code}, Result: "switch refused, nothing written: x"})
		d.want(c.want)
		if c.code == 3 && (d.m.RestartBlocked() == "" || d.m.View(d.now).Keys[PlaceMic] == "Hold: repair") {
			t.Fatal("exit 3 of the switch: no hold, or a repair offered")
		}
	}
}

// TestKeyGeometry: the edges and centres of the six keys from the owner's
// measurement (M 50, the logo 720..1200, six equal keys of 670/3 px).
func TestKeyGeometry(t *testing.T) {
	want := []float64{162, 385, 609, 1312, 1535, 1759}
	for i := 0; i < KeyPlaces; i++ {
		l, r := KeyEdgesPx(i)
		// the owner's centres are rounded to whole px
		if c := (l + r) / 2; c < want[i]-1 || c > want[i]+1 || math.Abs(r-l-670.0/3) > 1e-9 {
			t.Errorf("key %d: %v..%v (centre %v, want %v)", i+1, l, r, c, want[i])
		}
	}
	if l, _ := KeyEdgesPx(0); l != 50 {
		t.Error("the left margin")
	}
	if _, r := KeyEdgesPx(2); r != 720 {
		t.Errorf("key 3 ends at %v, the logo starts at 720", r)
	}
	if l, _ := KeyEdgesPx(3); l != 1200 {
		t.Error("key 4 does not start at the logo's end")
	}
	if _, r := KeyEdgesPx(5); r != 1870 {
		t.Errorf("the right margin: %v", r)
	}
	// every label of the flow fits its key (13 cells of 16 px) unclipped
	for _, l := range []string{"Back", "Cancel", "Log", "Status", "Next", "Previous", "Check again", "Scroll down", "Scroll up",
		"Open", "Eject", "Try again", "Install here", "Install again", "Hold: start", "Hold: restart", "Hold: repair", "Hold: switch", "Check"} {
		k := [KeyPlaces]string{PlaceMic: l}
		if f := keyColumns(k, PanelCols); !strings.HasSuffix(f, l) {
			t.Errorf("label %q is cut: %q", l, f)
		}
	}
}

// TestPanelQuestion: before the first plan the flow asks whether its
// screen shows on the unit's panel (FIRMWARE-GENERAL §11.5): yes gives
// --panel-confirmed and is kept (also by a UI started again); no stops,
// nothing written, the drive goes back.
func TestPanelQuestion(t *testing.T) {
	d := newDrive(t, false, "off")
	d.toChecklist()
	d.m.DriveEjected(true)
	d.wantActs(ActReadDrive)
	d.m.DriveRead(DriveResult{OK: true, Name: `"fw.bin"`, Size: 973})
	d.want(ScreenPanel)
	f := d.m.View(d.now)
	if got := rowsOf(f); got[0] != "Do you see this screen on the unit's panel?" || f.Keys[PlaceMic] != "Yes" || f.Keys[PlaceSpeaker] != "No" {
		t.Fatalf("%q %q", got, f.Keys)
	}
	d.wantActs()
	d.press(KeyBack) // no
	d.want(ScreenRefused)
	d.wantActs(ActGiveBackDrive)
	if got := rowsOf(d.m.View(d.now)); got[0] != "Why: This installer works only when its screen shows on the unit's panel. Nothing was written." || d.m.PanelConfirmed() {
		t.Fatalf("no: %q", got)
	}
	d.press(KeyOK) // try again: asked again
	d.m.DriveEjected(true)
	d.m.Take()
	d.m.DriveRead(DriveResult{OK: true, Name: `"fw.bin"`, Size: 973})
	d.want(ScreenPanel)
	d.press(KeyOK) // yes
	d.want(ScreenPlanning)
	d.wantActs(ActStartPlan)
	if !d.m.PanelConfirmed() || !d.m.Persist().Panel {
		t.Fatal("yes is not kept")
	}
	// a UI started again keeps the answer: no second question
	m2 := New("t", false)
	m2.SetUnit(*d.m.Unit)
	m2.Resume(Persisted{Panel: true, Outcome: &Outcome{Screen: ScreenStopped, State: StateS0}}, testPlan(s0Sec))
	m2.Handle(Event{KeyOK, true}, d.now) // try again
	m2.DriveEjected(true)
	m2.Take()
	m2.DriveRead(DriveResult{OK: true, Name: `"fw.bin"`, Size: 973})
	if m2.Screen != ScreenPlanning {
		t.Fatalf("asked again after a restart of the UI: %s", m2.Screen)
	}
}

// TestGateNotes (fwgen-r4 c2addb2): the gate's refusals of a firmware whose
// boot chain never booted on a UC-P10, or that this release does not offer,
// are the refused screen's reason word for word; the device mode's own text
// is the details row.
func TestGateNotes(t *testing.T) {
	for _, c := range []struct {
		note  string
		panel Panel
		want  string
	}{
		{plan.NoteNotOffered, PanelUCP10, plan.NoteNotOffered},
		{plan.NoteNotOffered, PanelUCP8, plan.NoteNotOffered},
		// the bootloader note names the unit's model (RELEASE-1-REVIEW N-1)
		{plan.NoteBootchainNotTested, Panel{}, "This firmware's bootloader has not been tested on a UC-P10 yet. Nothing was written."},
		{plan.NoteBootchainNotTested, PanelUCP10, "This firmware's bootloader has not been tested on a UC-P10 yet. Nothing was written."},
		{plan.NoteBootchainNotTested, PanelUCP8, "This firmware's bootloader has not been tested on a UC-P8 yet. Nothing was written."},
	} {
		d := newDrive(t, false, "off")
		d.m.Panel = c.panel
		d.toPlanning()
		raw := "refused, nothing written: pass 1: firmware 1.0.6.20: " + c.note
		d.m.PlanEnded(PlanResult{Refusal: raw, End: End{Exited: true, Code: 1}})
		d.want(ScreenRefused)
		f := d.m.View(d.now)
		got := rowsOf(f)
		if got[0] != "Why: "+c.want || hasRow(f, "Nothing was written to the unit.") || !hasRow(f, "Details: "+raw) {
			t.Errorf("%q on %q: %q", c.note, c.panel.Model, got)
		}
	}
	if strings.Contains(plan.NoteBootchainNotTested, "UC-P10") || strings.Contains(plan.NoteBootchainNotTested, "UC-P8") {
		t.Errorf("the device mode's note names a model: %q", plan.NoteBootchainNotTested)
	}
}
