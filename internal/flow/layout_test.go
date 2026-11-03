package flow

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/nvram"
	"uc-pxx-installer/internal/plan"
	"uc-pxx-installer/internal/status"
)

// checkFit fails when a screen does not fit the panel (120 x 37): a row
// wider than the screen, more rows than the screen, or body rows that are
// not on the screen (only the log scrolls).
func checkFit(t testing.TB, m *Model, now time.Time, what string) Rows {
	t.Helper()
	return checkFitAt(t, m, now, what, PanelCols, PanelRows)
}

// checkFitAt is checkFit on a screen of cols x rows with the model's panel
// (W-P6): also every key label wholly on its key, as the label or one of
// its short forms, never cut.
func checkFitAt(t testing.TB, m *Model, now time.Time, what string, PanelCols, PanelRows int) Rows {
	t.Helper()
	f := m.View(now)
	pn := f.panel()
	lay := f.Layout(PanelCols, PanelRows)
	if p := lay.Problem(m.Screen); p != "" {
		t.Fatalf("%s: screen %s does not fit %dx%d: %s\n%s", what, m.Screen, PanelCols, PanelRows, p, dump(f))
	}
	if !f.TextKeys {
		for i, l := range f.Keys {
			if l == "" {
				continue
			}
			from, to := pn.KeyCells(i, PanelCols)
			got := LabelFor(l, to-from)
			ok := got == l
			for _, sh := range shortLabels[l] {
				ok = ok || got == sh
			}
			r := []rune(lay.Footer + strings.Repeat(" ", PanelCols))
			if !ok || !strings.Contains(string(r[from:to]), got) {
				t.Fatalf("%s: screen %s at %d cols: the label %q of key %d is cut or off its key (%d..%d): %q", what, m.Screen, PanelCols, l, i+1, from, to, lay.Footer)
			}
		}
	}
	rows := append([]string{lay.Title, lay.Footer}, lay.Msg...)
	for _, b := range lay.Body {
		rows = append(rows, b.Text)
	}
	for _, r := range rows {
		if n := utf8.RuneCountInString(r); n > PanelCols {
			t.Fatalf("%s: screen %s: a row of %d characters: %q", what, m.Screen, n, r)
		}
	}
	// no key label over the Crestron logo (720..1200 px: cells 45..74)
	if f.Keys != ([KeyPlaces]string{}) && !f.TextKeys {
		r := []rune(lay.Footer + strings.Repeat(" ", PanelCols))
		if logo := string(r[int(pn.LogoFromPx*float64(PanelCols)/pn.Px):int(pn.LogoToPx*float64(PanelCols)/pn.Px)]); strings.TrimSpace(logo) != "" {
			t.Fatalf("%s: screen %s: a key label over the logo: %q", what, m.Screen, lay.Footer)
		}
	}
	if n := 4 + len(lay.Body) + len(lay.Msg); n > PanelRows || len(lay.Body) > lay.Room {
		t.Fatalf("%s: screen %s: %d rows (body %d, room %d)", what, m.Screen, n, len(lay.Body), lay.Room)
	}
	return lay
}

func dump(f Frame) string {
	var b strings.Builder
	fmt.Fprintf(&b, "title %q\n", f.Title)
	for _, l := range f.Body {
		fmt.Fprintf(&b, "| %s\n", l.Text)
	}
	for _, l := range f.Tail {
		fmt.Fprintf(&b, "tail %s\n", l.Text)
	}
	fmt.Fprintf(&b, "footer %q\nmsg %q\n", f.Footer, f.Msg)
	return b.String()
}

func TestWrap(t *testing.T) {
	long := "Why: refused, nothing written: install refused: bootctl-status: uc-pxx-bootctl status was not run; bootctl-binary: " +
		"/usr/libexec/p10inst/uc-pxx-bootctl-inst sha256 af1e19999a46fea4d63b323ef91fc29253e70d5d4a9b0e8a61214de15c86cf5d must be " +
		"the reviewed binary 5d83215bb5cab5b5f68c6db6500af8bea91180338b67e46850c266afb34ee400"
	for _, c := range []struct {
		in   string
		w    int
		cont string
	}{
		{long, 120, "    "},
		{"  | drive: the firmware is the .bin on the P10-INSTALL drive file /run/p10inst/drive/drive.img (size=1258291200,mtime=1793339637152651113,ctime=1793339637168651228,dev=65024,ino=25840818); the drive is detached", 120, "  |     "},
		{"        why: " + strings.Repeat("word ", 40), 60, "            "},
		{strings.Repeat("x", 300), 120, "    "},
	} {
		rows := Wrap(c.in, c.w)
		if len(rows) < 2 {
			t.Fatalf("%q: %d rows", c.in, len(rows))
		}
		var words []string
		for i, r := range rows {
			if utf8.RuneCountInString(r) > c.w {
				t.Fatalf("row %d: %d > %d: %q", i, utf8.RuneCountInString(r), c.w, r)
			}
			if i > 0 && !strings.HasPrefix(r, c.cont) {
				t.Fatalf("row %d %q does not start with %q", i, r, c.cont)
			}
			if i > 0 {
				r = r[len(c.cont):]
			}
			words = append(words, r)
		}
		join := strings.Join(words, " ")
		if strings.Join(strings.Fields(join), "") != strings.Join(strings.Fields(c.in), "") {
			t.Fatalf("text lost:\n%q\n%q", c.in, join)
		}
	}
	if r := Wrap("short", 120); len(r) != 1 || r[0] != "short" {
		t.Fatal(r)
	}
	if r := Wrap("", 120); r != nil {
		t.Fatal(r)
	}
}

// unit2Report is a status report like unit 2's (made-up serial and MAC).
func unit2Report() *status.Report {
	a, _ := avbab.Parse(s0Sec)
	r := &status.Report{Model: "Crestron UC-P10", Serial: "FAKE0SERIAL", MAC: "00:00:5e:00:53:01", Kernel: "6.18.54-gd02e92631f28",
		ImageToken: "p10inst-280c5d", ABValid: true, AB: a, Next: 0, Fallback: 1,
		AutoUpdate: nvram.AutoUpdate{State: nvram.Off, Part: 3, Counter: 7}, ReadOnly: "yes (the kernel refuses writes to the eMMC)",
		Unlock: "unlocked", UnlockWhy: "slot a booted an unsigned image with the userdebug U-Boot and is marked successful"}
	r.Slots[0] = status.Slot{Name: "a", AB: a.Slots[0], Summary: "uc-pxx mainline, build v3-ha-202610250209, token ucpxx-df13653d"}
	r.Slots[1] = status.Slot{Name: "b", AB: a.Slots[1], Summary: "Crestron 1.0.0.94 (userdebug)"}
	return r
}

// unit2Lines are the flow's status rows of a unit like unit 2, and
// noEMMCLines those of a unit without an eMMC (every value unknown, the
// slot table not valid, a read error).
func unit2Lines() []status.Line { return UnitFrom(unit2Report()).Lines }

func noEMMCLines() []status.Line {
	r := &status.Report{ABErr: "the A/B sector cannot be read: no eMMC with the partitions misc and boot_a", Next: -1, Fallback: -1,
		AutoUpdate: nvram.AutoUpdate{State: nvram.Unknown, Part: -1}, Errs: []string{"no eMMC with the partitions misc and boot_a"}}
	r.Slots[0].Name, r.Slots[1].Name = "a", "b"
	return UnitFrom(r).Lines
}

const (
	longWhy = "refused, nothing written: install refused: bootctl-status: uc-pxx-bootctl status was not run; bootctl-binary: " +
		"/usr/libexec/p10inst/uc-pxx-bootctl-inst sha256 af1e19999a46fea4d63b323ef91fc29253e70d5d4a9b0e8a61214de15c86cf5d must be " +
		"the reviewed binary 5d83215bb5cab5b5f68c6db6500af8bea91180338b67e46850c266afb34ee400"
	longBundle = "the drive P10-INSTALL, folder p10-install-f1699c150d62-20261030T142405Z (also in /run/p10inst/p10-install-f1699c150d62-20261030T142405Z)"
	longChild  = "drive: the firmware is the .bin on the P10-INSTALL drive file /run/p10inst/drive/drive.img (size=1258291200,mtime=1793339637152651113," +
		"ctime=1793339637168651228,dev=65024,ino=25840818); the drive is detached from the computer and must stay so, unchanged, to the end"
)

// longUnit is the unit with the longest texts the flow shows.
func longUnit(au AutoUpdateView, lines []status.Line) Unit {
	u := Unit{ABValid: true, Next: 0, AutoUpdate: au, Lines: lines}
	u.Slots[0] = SlotView{Name: "a", AB: avbab.Slot{Priority: 15, Successful: 1}, Summary: "uc-pxx mainline, build v3-ha-202610250209, token ucpxx-df13653d"}
	u.Slots[1] = SlotView{Name: "b", AB: avbab.Slot{Priority: 14, Successful: 1}, Summary: "Crestron 1.0.0.94 (userdebug)"}
	return u
}

// longPlan is the plan of 13 steps of the real install, with the longest
// texts.
func longPlan() *plan.Plan {
	p := testPlan(s0Sec)
	p.Steps = []plan.Step{p.Steps[0]}
	for i, part := range []string{"uboot_b", "trust_b", "dtbo_b", "vbmeta_b", "boot_b", "system_b", "vendor_b", "oem_b"} {
		op := "write"
		if part == "trust_b" {
			op = "skip"
		}
		p.Steps = append(p.Steps, plan.Step{N: i + 2, Op: op, Write: &plan.WriteStep{Partition: part, Length: 1879048192}})
	}
	t := testPlan(s0Sec).Steps
	p.Steps = append(p.Steps, plan.Step{N: 10, Op: "sync"}, plan.Step{N: 11, Op: "verify"}, t[5], plan.Step{N: 13, Op: "check-ab"})
	p.Steps[11].N = 12
	p.Payload = &plan.Payload{Version: "1.0.0.94", BuildType: "userdebug", FirmwareFile: "/run/p10inst/drive/drive.img:UC-P10_1.0.0.94_userdebug_firmware.bin"}
	p.KeySource = "updater libraries in system_b"
	p.AutoUpdate = &plan.AutoUpdate{State: "unknown", UnclearConfirmed: true}
	p.RAMImage = &plan.RAMImage{Token: "p10inst-280c5d"}
	p.ID = strings.Repeat("f1699c150d62", 6)[:64]
	return p
}

// TestScreensFit: every screen with the longest texts it can show (the
// texts of the screen renders of 284cbe2, real device-mode lines) fits the
// panel. A row that would be cut or a body row below the footer fails.
func TestScreensFit(t *testing.T) { screensFit(t, PanelUCP10, PanelCols, PanelRows) }

// screensFit is TestScreensFit on a panel and a console size (W-P6).
func screensFit(t *testing.T, panel Panel, cols, rows int) {
	checkFit := func(t testing.TB, m *Model, now time.Time, what string) Rows {
		t.Helper()
		m.Panel = panel
		return checkFitAt(t, m, now, what, cols, rows)
	}
	now := time.Unix(100000, 0)
	auOn := AutoUpdateView{State: "on", URL: "https://fw-update.example.invalid/ucp10/", Detail: "nvram part 3, write counter 8"}
	auUnclear := AutoUpdateView{State: "unknown", URL: "https://fw-update.example.invalid/ucp10/", Detail: "parts 3, 5 have the same highest write counter 7"}
	for _, terminal := range []bool{false, true} {
		name := func(s string) string { return fmt.Sprintf("%s (terminal %v)", s, terminal) }
		mk := func(au AutoUpdateView, lines []status.Line) *Model {
			m := New("UC-P10 installer  -  install image  -  p10inst", terminal)
			m.SetUnit(longUnit(au, lines))
			return m
		}
		// the status: unit 2, every menu item; no eMMC with the long message
		m := mk(auOn, unit2Lines())
		for i := 0; i < 4; i++ {
			m.Sel = i
			lay := checkFit(t, m, now, name("status unit 2"))
			if !strings.Contains(lay.Body[0].Text, "[ "+strings.ToUpper(statusMenu[i])+" ]") {
				t.Fatalf("the menu is not the first row: %q", lay.Body[0].Text)
			}
		}
		m.Msg = "The A/B metadata is not valid: the installer does not install (the A/B sector cannot be read: no eMMC with the partitions misc and boot_a)."
		checkFit(t, m, now, name("status unit 2 with a message"))
		m = mk(auUnclear, noEMMCLines())
		m.Unit.ABValid, m.Unit.ABErr = false, "the A/B sector cannot be read: no eMMC with the partitions misc and boot_a"
		m.Sel = 1
		m.Handle(Event{KeyOK, true}, now)
		if m.Screen != ScreenStatus || m.Msg == "" {
			t.Fatalf("no eMMC: %s %q", m.Screen, m.Msg)
		}
		checkFit(t, m, now, name("status without an eMMC"))
		// the Install item in every auto-update state that refuses: the
		// whole reason is in the message rows
		for _, au := range []AutoUpdateView{auOn, auUnclear} {
			m = mk(au, unit2Lines())
			m.Sel = 1
			m.Handle(Event{KeyOK, true}, now)
			lay := checkFit(t, m, now, name("install refused, auto-update "+au.State))
			if got := strings.Join(strings.Fields(strings.Join(lay.Msg, " ")), " "); m.Screen != ScreenStatus || got != m.Msg {
				t.Fatalf("auto-update %s: the message %q is not whole on the screen: %q", au.State, m.Msg, got)
			}
		}
		// the firmware screen: the drive refusals
		for _, why := range []string{
			"there is no firmware file (*.bin) in the top folder of the P10-INSTALL drive: copy the firmware .bin there, then eject the drive",
			"the P10-INSTALL drive was not ejected cleanly, so the copy can be incomplete: copy the firmware again, then eject the drive on the computer"} {
			m = mk(auUnclear, unit2Lines())
			m.Screen, m.refused = ScreenFirmware, why
			checkFit(t, m, now, name("firmware refused"))
		}
		// the planning screen with long child lines; the keeper screen
		m = mk(auUnclear, unit2Lines())
		m.Screen, m.running = ScreenPlanning, "plan"
		m.Drive = &DriveResult{OK: true, Name: `"UC-P10_1.0.0.94_userdebug_firmware.bin"`, Size: 1020215296, KeyFile: false}
		m.Prog = NewProgress(false, now.Add(-150*time.Second))
		for i := 0; i < 20; i++ {
			m.ChildLine(longChild, now)
		}
		lay := checkFit(t, m, now, name("planning"))
		if !onScreen(lay, "Finding the key") || !onScreen(lay, "Checking the firmware (sha256)") || onScreen(lay, "drive.img") {
			t.Fatalf("planning: the checks are not shown, or the child's lines are: %q", lay.Body)
		}
		m.Screen, m.running = ScreenTarget, ""
		m.keeperWhy = "both slots are successful and the pre-fastboot A/B sector is not known; choose the keeper"
		checkFit(t, m, now, name("keeper"))
		// the plan screen; the progress in every phase
		m.Plan = longPlan()
		m.Screen = ScreenPlan
		checkFit(t, m, now, name("plan"))
		m.Screen = ScreenProgress
		m.running = "install"
		m.Prog = NewProgress(true, now.Add(-400*time.Second))
		m.Prog.SetPlan(StepsOf(m.Plan))
		for i := 0; i < 10; i++ {
			m.ChildLine("note: No pre-fastboot A/B sector was given. Fastboot entry through the boot-mode register wrote nothing to misc when it was measured, but only with slot A active (design §10 item 1).", now)
		}
		checkFit(t, m, now, name("progress, before the steps"))
		for i := 1; i <= 13; i++ {
			// a minute a step: the detail row of the running step has the
			// estimate from the speed of the earlier writes (its longest form)
			at := now.Add(time.Duration(i) * time.Minute)
			m.ChildLine(fmt.Sprintf(" %d/13 write system_b (1879048192 bytes)", i), at)
			m.ChildLine("  "+longChild, at)
			checkFit(t, m, at.Add(30*time.Second), name(fmt.Sprintf("progress, step %d", i)))
		}
		m.ChildLine("STOP: step 10 (sync): stopped before the step: drive rule: the P10-INSTALL drive is attached to the computer again (the lun holds \"/run/p10inst/drive/drive.img\")", now)
		m.Handle(Event{KeyBack, true}, now)
		checkFit(t, m, now, name("progress, a key pressed"))
		// the end screens with the longest reason and log folder
		for _, c := range []struct {
			after []byte
			end   End
		}{{d2Sec, End{Exited: true}}, {d1Sec, End{Exited: true, Code: 1}}, {d1Sec, End{Exited: true, Code: 3}},
			{sector([3]byte{14, 0, 1}, [3]byte{15, 6, 0}, 0), End{Signal: "killed"}}} {
			o := Classify(testPlan(s0Sec), s0Sec, c.after, nil, c.end)
			o.Why = longWhy
			m.Prog.Result = "refused, nothing written: the plan now has id d7f65b98501b3e4af2e5577bdb8542d70ee37a1e2e98107cc78f452a10b4bbf4, /run/p10inst/flow/plan.json has f1699c150d6295e0d0525fef46280f761e6c4e669e6a272925bbf83a075231d8"
			m.running = "install"
			m.Msg = txtProgressKey
			m.InstallEnded(o)
			if m.Msg != "" {
				t.Fatalf("%s: the message of the install stays: %q", m.Screen, m.Msg)
			}
			m.BundleDone(longBundle)
			checkFit(t, m, now, name("end "+m.Screen.String()))
			if m.Screen == ScreenHold {
				m.Rechecked(StateUnknown, "the A/B sector is none of the plan's states")
				checkFit(t, m, now, name("hold, read again"))
			}
		}
		// refused, restart, and the log (it scrolls, opens at its end)
		m.everHeld = false
		m.Screen, m.refused = ScreenRefused, longWhy
		checkFit(t, m, now, name("refused"))
		m.Screen = ScreenRestart
		checkFit(t, m, now, name("restart"))
		m.Outcome = nil
		checkFit(t, m, now, name("restart, nothing changed"))
		m.Screen, m.Scroll = ScreenLog, 0
		for i := 0; i < 110; i++ {
			m.ChildLine(fmt.Sprintf("log line %d", i), now)
		}
		m.ChildLine("RESULT: install done in 5m38s", now)
		lay = checkFit(t, m, now, name("log"))
		if lay.Hidden == 0 || !strings.Contains(lay.Body[len(lay.Body)-1].Text, "RESULT") || !strings.Contains(lay.Rule, "rows ") {
			t.Fatalf("the log does not open at its end: hidden %d, last %q, rule %q", lay.Hidden, lay.Body[len(lay.Body)-1].Text, lay.Rule)
		}
		m.Handle(Event{KeyPrev, true}, now) // up: back in time
		if lay2 := checkFit(t, m, now, name("log up")); lay2.Body[0].Text == lay.Body[0].Text {
			t.Fatal("the log does not scroll up")
		}
	}
}

// TestTitlesAndLegends: titles start with a capital letter; the Vol keys
// choose only where something can be chosen; times are not Go
// durations; the hold says its warning once.
func TestTitlesAndLegends(t *testing.T) {
	d := newDrive(t, false, "off")
	for _, s := range []Screen{ScreenLog, ScreenRestart} {
		d.m.go2(s)
		f := d.m.View(d.now)
		i := strings.LastIndex(f.Title, "  -  ")
		if i < 0 || f.Title[i+5] < 'A' || f.Title[i+5] > 'Z' {
			t.Errorf("title %q", f.Title)
		}
		if f.Keys[PlaceVolUp] == "Next" {
			t.Errorf("%s: keys %q", s, f.Keys)
		}
	}
	d.m.go2(ScreenStatus)
	if f := d.m.View(d.now); f.Keys[PlaceVolDown] != "Previous" || f.Keys[PlaceVolUp] != "Next" || f.Keys[PlaceMic] != "Open" {
		t.Errorf("status keys %q", f.Keys)
	}
	if Dur(12*time.Second) != "12 s" || dur(60*time.Second) != "1 min 00 s" || dur(563*time.Second) != "9 min 23 s" || dur(40*time.Second) != "40 s" {
		t.Error(dur(60*time.Second), dur(563*time.Second))
	}
	d.toProgress()
	d.m.InstallEnded(Classify(testPlan(s0Sec), s0Sec, d1Sec, nil, End{Exited: true, Code: 3}))
	f := d.m.View(d.now)
	n := strings.Count(f.Title, txtHoldTitle)
	for _, l := range f.Body {
		n += strings.Count(l.Text, txtHoldTitle)
	}
	if n != 1 || f.Body[0].Level != levelBad {
		t.Errorf("the hold says its warning %d times: %q / %+v", n, f.Title, f.Body[0])
	}
}
