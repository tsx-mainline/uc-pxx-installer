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
			"What to do:",
			"  1. Leave the unit on.",
			"  2. Press OK (Mic) to check the slots again.",
			"  3. the log folder is being written to the drive P10-INSTALL ...",
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
			f.Keys[PlaceMic] != "Check again" || f.Keys[PlaceSpeaker] != "Log" || f.Keys[PlaceVolUp] != "" || f.Keys[PlaceVolDown] != "") {
			t.Errorf("hold: %q %q", f.Title, f.Keys)
		}
	}
	d := newDrive(t, false, "off")
	d.m.InstallRefused("The slot state changed after the check (something else wrote to the unit). Nothing was written. Start again.")
	f := d.m.View(d.now)
	if got := rowsOf(f); got[0] != "Nothing was written to the unit." ||
		got[1] != "Why: The slot state changed after the check (something else wrote to the unit). Nothing was written. Start again." {
		t.Errorf("refused: %q", got)
	}
}

// TestHoldReread: after OK on the hold, a known state replaces the warning
// (review L-5); an unknown state keeps it. The hold is never left.
func TestHoldReread(t *testing.T) {
	d := newDrive(t, false, "off")
	d.toProgress()
	d.m.InstallEnded(Classify(testPlan(s0Sec), s0Sec, d1Sec, nil, End{Exited: true, Code: 3}))
	d.m.Rechecked(StateUnknown, "the A/B sector is none of the plan's states")
	f := d.m.View(d.now)
	if got := rowsOf(f); got[0] != "!!! KEEP THE UNIT POWERED. DO NOT RESTART IT." ||
		!hasRow(f, "Checked again: the slot state is still not known. Keep the unit powered.") {
		t.Errorf("unknown: %q", got)
	}
	d.m.Rechecked(StateD1, "")
	f = d.m.View(d.now)
	got := rowsOf(f)
	if got[0] != "Checked again: Slot B is not finished and will not start; slot A starts. The power can be disconnected." ||
		got[1] != "The install did not end normally. Send the log folder (below) before you use the unit again." ||
		f.Body[0].Level != levelWarn || !strings.HasSuffix(f.Title, "  -  Install stopped") {
		t.Errorf("known: %q / %q", f.Title, got)
	}
	all := strings.ToUpper(f.Title + strings.Join(got, " "))
	if strings.Contains(all, "KEEP THE UNIT POWERED") || d.m.Screen != ScreenHold || d.m.RestartBlocked() == "" {
		t.Errorf("known: the warning stays, or the hold was left: %q", got)
	}
	d.m.Rechecked(StateUnknown, "x")
	if got := rowsOf(d.m.View(d.now)); got[0] != "!!! KEEP THE UNIT POWERED. DO NOT RESTART IT." {
		t.Errorf("unknown again: %q", got)
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
		"            2147 MB, 28 s so far; the earlier writes ran at 4.2 MB/s")
	if f := pr.Fraction(d.now); f <= 0 || f >= 0.01 {
		t.Errorf("fraction %v after U-Boot only", f)
	}
	d.m.ChildLine(" 4/7 sync", d.now)
	d.m.ChildLine(" 5/7 verify", d.now)
	d.m.ChildLine(" 6/7 bootctl arm-trying --slot b --lower-keeper", d.now)
	d.m.ChildLine(" 7/7 check the A/B sector", d.now)
	at("ok Verify (read everything back)", "ok Make slot B the next boot", ">> Check the slot table")
	if f := pr.Fraction(d.now); f < 0.99 {
		t.Errorf("fraction %v before the last check", f)
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
	k := Frame{Keys: [KeyPlaces]string{PlaceSpeaker: "Back", PlaceVolDown: "Next", PlaceVolUp: "Previous", PlaceMic: "OK"}}
	lay = k.Layout(120, 37)
	for _, c := range []struct {
		label string
		at    int // the middle of the key's column: place*20 + (20-len)/2
	}{{"Back", 48}, {"Next", 68}, {"Previous", 86}, {"OK", 109}} {
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
		t.Errorf("progress after Vol-: the power line %q is not on the screen (%q)", l, lay.Problem(m.Screen))
	}
}
