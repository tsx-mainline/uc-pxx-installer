package flow

import (
	"strings"
	"testing"
	"time"

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
		StateS0:      "Nothing on the unit changed. Slot A starts as before. It is safe to disconnect the power.",
		StateD1:      "Slot B is disabled and slot A starts. It is safe to disconnect the power.",
		StateD2:      "Slot B is complete and starts next. If it does not start, slot A starts again after a few restarts.",
		StateUnknown: "The state of the slots is not known.",
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
			"Slot B is complete and starts next. If it does not start, slot A starts again after a few restarts."}, levelGood},
		{"stopped S0", s0Sec, End{Exited: true, Code: 1}, []string{
			"Why: the install ended (exit 1); the A/B sector reads as S0: as before the install",
			"Nothing on the unit changed. Slot A starts as before. It is safe to disconnect the power."}, levelBad},
		{"stopped D1", d1Sec, End{Exited: true, Code: 1}, []string{
			"Why: the install ended (exit 1); the A/B sector reads as D1: the target is disabled; the keeper boots",
			"Slot B is disabled and slot A starts. It is safe to disconnect the power."}, levelBad},
		{"stopped D2", d2Sec, End{Signal: "killed"}, []string{
			"Why: the install ended (ended by the signal killed); the A/B sector reads as D2: the target is armed and boots next; the keeper is the fallback",
			"Slot B is complete and starts next. If it does not start, slot A starts again after a few restarts."}, levelBad},
		{"hold", d1Sec, End{Exited: true, Code: 3}, []string{
			"!!! DO NOT DISCONNECT THE POWER. DO NOT RESTART.",
			"Keep the unit powered. Restart is removed from this installer.",
			"Why: the install ended with exit 3: the installer did not know the A/B state"}, levelBad},
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
		if c.name == "hold" {
			if !strings.HasSuffix(f.Title, "  -  Hold: keep the unit powered") || !hasRow(f, Contact) ||
				!hasRow(f, "OK reads the A/B sector again and says whether it is now safe to disconnect the power.") {
				t.Errorf("hold: %q %q", f.Title, got)
			}
		}
	}
	d := newDrive(t, false, "off")
	d.m.InstallRefused("the A/B sector is not the one of the plan any more")
	f := d.m.View(d.now)
	if got := rowsOf(f); got[0] != "Nothing was written to the unit." || got[1] != "Why: the A/B sector is not the one of the plan any more" {
		t.Errorf("refused: %q", got)
	}
}

// TestHoldReread: after OK on the hold, a known state replaces the warning
// (no "DO NOT DISCONNECT" next to "safe to disconnect", review L-5); an
// unknown state keeps it. The hold is never left.
func TestHoldReread(t *testing.T) {
	d := newDrive(t, false, "off")
	d.toProgress()
	d.m.InstallEnded(Classify(testPlan(s0Sec), s0Sec, d1Sec, nil, End{Exited: true, Code: 3}))
	d.m.Rechecked(StateUnknown, "the A/B sector is none of the plan's states")
	f := d.m.View(d.now)
	if got := rowsOf(f); got[0] != "!!! DO NOT DISCONNECT THE POWER. DO NOT RESTART." ||
		!hasRow(f, "The A/B sector still does not read as a known state (the A/B sector is none of the plan's states). Keep the unit powered.") {
		t.Errorf("unknown: %q", got)
	}
	d.m.Rechecked(StateD1, "")
	f = d.m.View(d.now)
	got := rowsOf(f)
	if got[0] != "Read again: the A/B sector now reads as D1: Slot B is disabled and slot A starts. It is safe to disconnect the power." ||
		got[1] != "This is the newest read of the A/B sector. Restart stays removed from this installer: keep the log folder." ||
		f.Body[0].Level != levelWarn || !strings.HasSuffix(f.Title, "  -  Hold: the install did not end normally") {
		t.Errorf("known: %q / %q", f.Title, got)
	}
	all := strings.ToUpper(f.Title + strings.Join(got, " "))
	if strings.Contains(all, "DO NOT DISCONNECT") || d.m.Screen != ScreenHold || d.m.RestartBlocked() == "" {
		t.Errorf("known: the warning stays, or the hold was left: %q", got)
	}
	d.m.Rechecked(StateUnknown, "x")
	if got := rowsOf(d.m.View(d.now)); got[0] != "!!! DO NOT DISCONNECT THE POWER. DO NOT RESTART." {
		t.Errorf("unknown again: %q", got)
	}
}

func TestLiveSafetyExact(t *testing.T) {
	pr := NewProgress(true, time.Unix(0, 0))
	pr.SetPlan(StepsOf(testPlan(s0Sec)))
	at := func(want string, lvl int) {
		t.Helper()
		if got, l := liveSafety(pr, "a", "b"); got != want || l != lvl {
			t.Errorf("%q %d, want %q %d", got, l, want, lvl)
		}
	}
	at("Nothing is written yet. Safe to disconnect the power.", levelGood)
	pr.Line(" 1/7 bootctl disable --slot b", time.Unix(1, 0))
	at("Do not disconnect the power now: the boot slot record is being written.", levelBad)
	pr.Line(" 2/7 write uboot_b (4194304 bytes)", time.Unix(2, 0))
	at("Safe to disconnect the power: slot A starts (slot B is disabled while it is written).", levelGood)
	pr.Line(" 6/7 bootctl arm-trying --slot b --lower-keeper", time.Unix(3, 0))
	at("Do not disconnect the power now: the boot slot record is being written.", levelBad)
	pr.Line(" 7/7 check the A/B sector", time.Unix(4, 0))
	at("Safe to disconnect the power: slot B starts next, slot A is the fallback.", levelGood)
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
		d.want(ScreenPlanning)
		f := d.m.View(d.now)
		file, search := hasRow(f, "Key: from the key file p10-keys.txt on the drive"), hasRow(f, "Key: searched in the system of the slots (no key file on the drive)")
		if file != kf || search == kf || !hasRow(f, `Firmware file: "fw.bin", 973 bytes`) {
			t.Errorf("key file %v: rows %q", kf, rowsOf(f))
		}
		p := testPlan(s0Sec)
		p.KeySource = map[bool]string{true: "key file", false: "updater libraries in system_b"}[kf]
		d.m.PlanEnded(PlanResult{Plan: p})
		f = d.m.View(d.now)
		if !hasRow(f, "Key: "+p.KeySource+" (it is never shown)") {
			t.Errorf("plan: %q", rowsOf(f))
		}
		p.KeySource = ""
		for _, r := range rowsOf(d.m.View(d.now)) {
			if strings.HasPrefix(r, "Key:") {
				t.Errorf("a key row without a source: %q", r)
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
	if lay.Hidden != 8 || lay.Problem(ScreenStatus) == "" || lay.Problem(ScreenLog) != "" ||
		lay.Body[len(lay.Body)-1].Text != "!! 9 more rows do not fit on this screen." || lay.Body[len(lay.Body)-1].Level != levelBad {
		t.Errorf("hidden %d %q %q", lay.Hidden, lay.Problem(ScreenStatus), lay.Body[len(lay.Body)-1].Text)
	}
	f.ScrollKeys = "Vol+ / Vol- scroll"
	if lay := f.Layout(120, 37); lay.Problem(ScreenPlan) == "" || !strings.Contains(lay.Footer, "rows 1-32 of 40") {
		t.Errorf("scroll: %q %q", lay.Problem(ScreenPlan), lay.Footer)
	}
	m := Frame{Msg: strings.Repeat("message ", 80)}
	lay = m.Layout(120, 37)
	if len(lay.Msg) != 3 || lay.Cut == 0 || lay.Problem(ScreenStatus) == "" || !strings.HasSuffix(lay.Msg[2], " ...") || Cells(lay.Msg[2]) > 120 {
		t.Errorf("message: %d rows, cut %d, %q", len(lay.Msg), lay.Cut, lay.Msg)
	}
	if lay := (Frame{Msg: strings.Repeat("message ", 40)}).Layout(120, 37); lay.Cut != 0 || len(lay.Msg) != 3 {
		t.Errorf("a 3-row message: %d rows, cut %d", len(lay.Msg), lay.Cut)
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
// on the screen (the reason is cut with a sign, review L-3); Vol- on the
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
				key = []string{"!!! " + txtHoldTitle, txtHoldKeep, Contact, txtHoldReread}
			}
			for _, k := range key {
				if !onScreen(lay, k) {
					t.Errorf("why x%d %s: %q is not on the screen", n, m.Screen, k)
				}
			}
			if lay.Problem(m.Screen) != "" || (n > 1 && !onScreen(lay, "the whole text is in the log folder")) {
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
	if l, _ := liveSafety(m.Prog, "a", "b"); !onScreen(lay, l) || lay.Problem(m.Screen) != "" {
		t.Errorf("progress after Vol-: the live line %q is not on the screen (%q)", l, lay.Problem(m.Screen))
	}
}
