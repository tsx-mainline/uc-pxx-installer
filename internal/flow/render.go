package flow

import (
	"fmt"
	"strings"
	"time"

	"uc-pxx-installer/internal/plan"
	"uc-pxx-installer/internal/status"
)

// Row levels (the same as status.Line).
const (
	levelNormal  = status.Normal
	levelHeading = status.Heading
	levelGood    = status.Good
	levelWarn    = status.Warn
	levelBad     = status.Bad
)

// Frame is one screen as text rows: the title, the body, the footer. Its
// Layout fits it to the screen (rows wrapped at the width).
type Frame struct {
	Title  string
	Body   []status.Line
	Footer string // the key legend of a terminal (TextKeys), or a note
	Msg    string
	LED    string // "blue", "green", "red": working, done, stop
	// Keys: the label of each touch key, from the left (KeyPlaces); the
	// footer shows each label above its key. TextKeys: the frame is for a
	// terminal, the footer is the Footer text.
	Keys     [KeyPlaces]string
	TextKeys bool
	// Tail: the newest output lines of a child, after the body: as many
	// (whole, wrapped) as fit below it; the log screen has them all.
	Tail []status.Line
	// Squeeze: empty body rows may be dropped when the body does not fit.
	Squeeze bool
	// Clip: 1 + the index of the body row (a long reason) that is
	// shortened, with a sign, when the body does not fit; 0 = none.
	Clip int
	// Scroll: rows scrolled (FromEnd: up from the end, as on the log).
	Scroll  int
	FromEnd bool
	// ScrollKeys: the screen scrolls (the rule above the message then says
	// which rows are on the screen).
	ScrollKeys string
}

// keys are the functions of the keys on one screen ("" = none).
type keys struct {
	back, next, prev, ok string
	hold                 bool // ok is a 3 s hold (a terminal: Enter, then y)
}

// setKeys puts the key legend into the frame: on the panel one label above
// each touch key; on a terminal the terminal keys as text.
func (m *Model) setKeys(f *Frame, k keys) {
	if m.Terminal {
		f.TextKeys = true
		var p []string
		if k.prev != "" {
			p = append(p, "Up = "+k.prev)
		}
		if k.next != "" {
			p = append(p, "Down = "+k.next)
		}
		if k.ok != "" {
			if k.hold {
				p = append(p, "Enter, then y = "+strings.TrimPrefix(k.ok, "Hold 3 s: "))
			} else {
				p = append(p, "Enter = "+k.ok)
			}
		}
		if k.back != "" {
			p = append(p, "b = "+k.back)
		}
		f.Footer = strings.Join(p, "   ")
		return
	}
	f.Keys[PlaceSpeaker], f.Keys[PlaceVolDown], f.Keys[PlaceVolUp], f.Keys[PlaceMic] = k.back, k.next, k.prev, k.ok
}

// Dur is a time for the screen: "40 s", "6 min 40 s" (never a Go
// duration such as "1m0s").
func Dur(d time.Duration) string { return dur(d) }

// dur is a time for the screen: "40 s", "6 min 40 s".
func dur(d time.Duration) string {
	sec := int(d.Round(time.Second).Seconds())
	if sec < 60 {
		return fmt.Sprintf("%d s", sec)
	}
	return fmt.Sprintf("%d min %02d s", sec/60, sec%60)
}

func (m *Model) holdText(what string, now time.Time) (string, int) {
	if m.Terminal {
		if m.confirm != 0 {
			return "  Press y to " + what + " now. Any other key cancels.", levelWarn
		}
		return "  Press Enter, then y, to " + what + ".", levelNormal
	}
	if !m.okDown.IsZero() {
		left := HoldOK - now.Sub(m.okDown)
		if left < 0 {
			left = 0
		}
		return fmt.Sprintf("  Keep holding OK: %s in %.1f s  %s", what, left.Seconds(), strings.Repeat("#", int(left.Seconds()*4)+1)), levelWarn
	}
	return "  Hold OK (Mic) for 3 seconds to " + what + ".", levelNormal
}

func orQ(s string) string {
	if s == "" {
		return "unknown contents"
	}
	return s
}

// View renders the model.
func (m *Model) View(now time.Time) Frame {
	f := Frame{Title: m.Title, Msg: m.Msg, LED: "blue"}
	var body []status.Line
	add := func(level int, format string, a ...any) {
		body = append(body, status.Line{Level: level, Text: fmt.Sprintf(format, a...)})
	}
	switch m.Screen {
	case ScreenStatus:
		f.LED = ""
		// the menu first: it is always in the same place
		var mb strings.Builder
		for i, it := range statusMenu {
			if i == m.Sel {
				mb.WriteString("[ " + strings.ToUpper(it) + " ]  ")
			} else {
				mb.WriteString("  " + it + "    ")
			}
		}
		add(levelHeading, "%s", strings.TrimRight(mb.String(), " "))
		add(levelNormal, "%s", txtStatusInstall)
		add(levelNormal, "")
		if m.Unit == nil {
			add(levelNormal, "Reading the unit (read-only) ...")
		} else {
			body = append(body, m.Unit.Lines...)
		}
		f.Squeeze = true
		m.setKeys(&f, keys{next: "Next", prev: "Previous", ok: "Open"})
	case ScreenFirmware:
		f.Title += "  -  " + txtFirmwareTitle
		add(levelNormal, "%s", txtFirmwareCopy)
		add(levelNormal, "%s", txtFirmwareEject)
		add(levelNormal, "")
		switch {
		case m.reading:
			add(levelNormal, "%s", txtFirmwareRead)
			add(levelNormal, "")
		case m.refused != "":
			add(levelBad, "The drive was refused: %s", m.refused)
			add(levelNormal, "Fix it on the computer, then eject the drive again.")
		default:
			add(levelNormal, "%s", txtFirmwareWait)
			if !m.copyStart.IsZero() && now.Sub(m.copyLast) < 5*time.Second {
				add(levelNormal, "The computer is writing to the drive: %s", dur(m.copyLast.Sub(m.copyStart)))
			} else {
				add(levelNormal, "")
			}
		}
		m.setKeys(&f, keys{back: "Cancel", ok: "Eject"})
	case ScreenTarget:
		f.Title += "  -  " + txtTargetTitle
		add(levelNormal, "%s", txtTargetIntro)
		add(levelNormal, "")
		for i := 0; i < 2; i++ {
			s, keep := m.Unit.Slots[i], m.Unit.Slots[1-i]
			mark := "  "
			if i == m.Sel {
				mark = "> "
			}
			next := ""
			if m.Unit.Next == i {
				next = "  (starts now)"
			}
			if keep.AB.Keeper() {
				add(levelHeading, "%sSlot %s: now %s%s", mark, slotWord(s.Name), orQ(s.Summary), next)
				add(levelNormal, "      Install here: slot %s is replaced; slot %s is not touched and stays the fallback.",
					slotWord(s.Name), slotWord(keep.Name))
			} else {
				add(levelNormal, "%sSlot %s: now %s%s", mark, slotWord(s.Name), orQ(s.Summary), next)
				add(levelWarn, "      Cannot be chosen: slot %s would be the fallback, and it is %s.", slotWord(keep.Name), plainState(keep.AB))
			}
			add(levelNormal, "")
		}
		add(levelNormal, "%s", targetWhy(m.keeperWhy))
		m.setKeys(&f, keys{back: "Cancel", next: "Next", prev: "Previous", ok: "Install here"})
	case ScreenPlanning:
		f.Title += "  -  " + txtPlanningTitle
		if m.Drive != nil {
			add(levelNormal, "Firmware file: %s (%d MB)", m.Drive.Name, m.Drive.Size/1e6)
			if m.Drive.KeyFile {
				add(levelNormal, "%s", txtKeyFile)
			} else {
				add(levelNormal, "%s", txtKeySearch)
			}
		} else {
			add(levelNormal, "")
			add(levelNormal, "")
		}
		if m.Keeper != "" {
			add(levelNormal, "Install to slot %s (your choice). Slot %s is not touched.", slotWord(otherSlot(m.Keeper)), slotWord(m.Keeper))
		} else {
			add(levelNormal, "")
		}
		m.planningRows(&body, now)
		m.setKeys(&f, keys{back: "Cancel"})
	case ScreenPlan:
		f.Title += "  -  " + txtPlanTitle
		m.planRows(&body)
		add(levelNormal, "")
		l, lvl := m.holdText("start the install", now)
		add(lvl, "%s", l)
		m.setKeys(&f, keys{back: "Cancel", ok: "Hold 3 s: start", hold: true})
	case ScreenProgress:
		f.Title += "  -  " + txtProgressTitle
		if m.Plan != nil {
			add(levelNormal, "Installing to slot %s. Slot %s is not touched.", slotWord(m.Plan.Target), slotWord(m.Plan.Keeper))
		} else {
			add(levelNormal, "Installing.")
		}
		add(levelWarn, "%s", txtProgressPower)
		m.progressRows(&body, now)
		m.setKeys(&f, keys{})
	case ScreenDone, ScreenStopped, ScreenHold:
		f.Clip = m.endRows(&body, &f, now)
	case ScreenRefused:
		f.Title += "  -  " + txtRefusedTitle
		f.LED = "red"
		add(levelGood, "%s", txtRefusedNote)
		add(levelBad, "Why: %s", m.refused)
		add(levelNormal, "")
		add(levelNormal, "Press OK to try again. The drive is back on the computer.")
		if m.refusedDetail != "" {
			add(levelNormal, "")
			add(levelNormal, "Details: %s", m.refusedDetail)
			f.Clip = len(body)
		}
		m.setKeys(&f, keys{back: "Status", ok: "Try again"})
	case ScreenLog:
		f.Title += "  -  Log"
		if m.Prog != nil {
			for _, l := range m.Prog.Lines {
				add(levelNormal, "  %s", l)
			}
		} else {
			add(levelNormal, "  (nothing yet)")
		}
		f.FromEnd = true // it opens at the end (the last lines)
		m.setKeys(&f, keys{back: "Back", next: "Scroll down", prev: "Scroll up", ok: "Back"})
	case ScreenRestart:
		f.Title += "  -  Restart"
		m.restartRows(&body, now)
		m.setKeys(&f, keys{back: "Back", ok: "Hold 3 s: restart", hold: true})
	}
	switch m.Screen {
	// the hold does not scroll: its Vol keys stay free (a repair action
	// may come there, owner 2026-10-31)
	case ScreenLog, ScreenPlan, ScreenDone, ScreenStopped, ScreenRefused:
		f.Scroll, f.ScrollKeys = m.Scroll, "scroll"
	}
	f.Body = body
	return f
}

func otherSlot(s string) string {
	if s == "a" {
		return "b"
	}
	return "a"
}

// targetWhy is why the installer asks for the target, in plain words.
func targetWhy(why string) string {
	switch {
	case strings.Contains(why, "must be successful"):
		return "The installer asks because the slot that starts now is not tested yet, so it cannot be the fallback."
	case strings.Contains(why, "both slots are successful"):
		return "The installer asks because both slots work: choose the one to replace."
	}
	return "The installer asks which slot to replace."
}

// planningRows: the checks of the plan child, one row each, in a fixed
// place: the key, the sha256 of the firmware's partition images, the unit.
func (m *Model) planningRows(body *[]status.Line, now time.Time) {
	add := func(level int, format string, a ...any) {
		*body = append(*body, status.Line{Level: level, Text: fmt.Sprintf(format, a...)})
	}
	add(levelNormal, "")
	p := m.Prog
	if p == nil {
		add(levelNormal, "Starting ...")
		return
	}
	add(levelNormal, "%s   %s", Bar(p.Fraction(now), 40), dur(now.Sub(p.Start)))
	add(levelNormal, "")
	key := "Finding the key"
	if m.Drive != nil && m.Drive.KeyFile {
		key = "Reading the key file"
	}
	n := len(plan.SlotParts)
	stages := []struct {
		label       string
		done, doing bool
	}{
		{key, p.Computes > 0, p.Computes == 0},
		{fmt.Sprintf("Checking the firmware (sha256): %d of %d parts", min(p.Checked, n), n), p.Checked >= n, p.Computes > 0 && p.Checked < n},
		{"Checking the unit", false, p.Checked >= n},
	}
	for _, st := range stages {
		mark, lvl := "   ", levelNormal
		switch {
		case st.done:
			mark, lvl = "ok ", levelGood
		case st.doing:
			mark, lvl = ">> ", levelHeading
		}
		add(lvl, "%s%s", mark, st.label)
	}
}

func (m *Model) progressRows(body *[]status.Line, now time.Time) {
	add := func(level int, format string, a ...any) {
		*body = append(*body, status.Line{Level: level, Text: fmt.Sprintf(format, a...)})
	}
	add(levelNormal, "")
	p := m.Prog
	if p == nil {
		add(levelNormal, "Starting ...")
		return
	}
	add(levelNormal, "%s   %s", Bar(p.Fraction(now), 40), dur(now.Sub(p.Start)))
	add(levelNormal, "")
	// one row per item, and one detail row under the item that runs: the
	// rows below move by that one row only
	detail := func(s string) { add(levelNormal, "            %s", s) }
	// the check(s) before the first write: one row, however many checks
	// the install child makes (one or two; owner 2026-10-31)
	if p.Cur < 0 {
		add(levelHeading, ">> %s", txtCheckAgain)
		detail("nothing is written yet; " + dur(now.Sub(p.Start)) + " so far")
	} else {
		add(levelGood, "ok %s", txtCheckAgain)
	}
	for i, s := range p.Steps {
		mark, lvl := "   ", levelNormal
		switch {
		case s.Done:
			mark, lvl = "ok ", levelGood
		case i == p.Cur:
			mark, lvl = ">> ", levelHeading
		}
		add(lvl, "%s%s", mark, s.Label)
		if i == p.Cur && !s.Done {
			in := dur(now.Sub(p.stepStart))
			// the device mode prints no progress inside a step: the size,
			// the time so far, and an estimate of this step only, from the
			// speed of the earlier writes of this install
			switch {
			case s.Op == "write" && p.StepEstimate(s) > 0:
				detail(fmt.Sprintf("%d MB to write, %s so far; about %s at %.1f MB/s, the speed of the earlier writes",
					s.Bytes/1e6, in, dur(p.StepEstimate(s)), p.MBps()))
			case s.Op == "write":
				detail(fmt.Sprintf("%d MB to write, %s so far", s.Bytes/1e6, in))
			default:
				detail(in + " so far")
			}
		}
	}
	add(levelNormal, "")
	if p.Stop != "" {
		add(levelBad, "Stopped: %s", plainReason(p.Stop))
	} else {
		add(levelNormal, "")
	}
}

func (m *Model) planRows(body *[]status.Line) {
	add := func(level int, format string, a ...any) {
		*body = append(*body, status.Line{Level: level, Text: fmt.Sprintf(format, a...)})
	}
	p := m.Plan
	if p == nil {
		add(levelBad, "no plan")
		return
	}
	ti, ki := 0, 0
	if p.Target == "b" {
		ti = 1
	} else {
		ki = 1
	}
	fw := "the firmware on the drive"
	if p.Payload != nil {
		fw = fmt.Sprintf("%s %s", p.Payload.Version, p.Payload.BuildType)
		add(levelNormal, "Firmware: %s (%s)", fw, fileName(p.Payload.FirmwareFile))
	}
	add(levelNormal, "")
	now := "unknown contents"
	if m.Unit != nil {
		now = orQ(m.Unit.Slots[ti].Summary)
	}
	add(levelWarn, "Slot %s (install to): now %s. After the install: %s.", slotWord(p.Target), now, fw)
	keep := "unknown contents"
	if m.Unit != nil {
		keep = orQ(m.Unit.Slots[ki].Summary)
	}
	add(levelGood, "Slot %s: not touched (%s). It stays the fallback.", slotWord(p.Keeper), keep)
}

// fileName is the last part of a firmware file path (the plan names the
// drive file and the name in it).
func fileName(p string) string {
	if i := strings.LastIndexAny(p, "/:"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// endRows: Done, Stopped, Hold. It returns Frame.Clip (the details row).
func (m *Model) endRows(body *[]status.Line, f *Frame, now time.Time) int {
	add := func(level int, format string, a ...any) {
		*body = append(*body, status.Line{Level: level, Text: fmt.Sprintf(format, a...)})
	}
	o := m.Outcome
	k, t := "?", "?"
	if m.Plan != nil {
		k, t = m.Plan.Keeper, m.Plan.Target
	}
	if o == nil {
		add(levelBad, "no result")
		return 0
	}
	switch o.Screen {
	case ScreenDone:
		f.Title += "  -  " + txtDoneTitle
		f.LED = "green"
		add(levelGood, "%s", stateSentence(StateD2, k, t))
		add(levelNormal, "Slot %s was not touched.", slotWord(k))
		add(levelNormal, "")
		m.bundleRow(body)
		add(levelNormal, "")
		l, lvl := m.holdText("restart", now)
		add(lvl, "%s", l)
		m.setKeys(f, keys{back: "Status", ok: "Hold 3 s: restart", hold: true})
		return 0
	case ScreenStopped:
		f.Title += "  -  " + txtStoppedTitle
		f.LED = "red"
		// the device mode's own text (STOP, RESULT) is shown as the
		// details; the flow's own reason (an exit or a signal) only in plain
		// words (it is in the flow log)
		why, detail := o.Why, ""
		if m.Prog != nil && m.Prog.Stop != "" {
			why, detail = m.Prog.Stop, m.Prog.Stop
		} else if m.Prog != nil && m.Prog.Result != "" {
			why, detail = m.Prog.Result, m.Prog.Result
		}
		lvl := levelGood
		if o.State == StateD1 {
			lvl = levelWarn
		}
		add(lvl, "%s", stateSentence(o.State, k, t))
		add(levelNormal, "")
		add(levelBad, "Why: %s", plainReason(why))
		m.bundleRow(body)
		add(levelNormal, "Press OK to try again, or Back for the status.")
		m.setKeys(f, keys{back: "Status", ok: "Try again"})
		if detail == "" {
			return 0
		}
		add(levelNormal, "")
		add(levelNormal, "Details: %s", detail)
		return len(*body)
	}
	// the hold
	f.LED = "red"
	folder := "the log folder is being written to the drive P10-INSTALL ..."
	if m.Bundle != "" {
		folder = "Take the log folder from " + m.Bundle + "."
	}
	if m.recheckKnown {
		// the newest check is a known state: it replaces the warning (L-5)
		f.Title += "  -  " + txtHoldRead
		add(levelWarn, "%s", m.recheck)
		add(levelNormal, "%s", txtHoldReadNote)
		add(levelNormal, "")
		add(levelNormal, "What to do:")
		add(levelNormal, "  1. %s", folder)
		add(levelNormal, "  2. %s", Contact)
		add(levelNormal, "  3. %s", txtHoldReread)
		add(levelNormal, "")
	} else {
		f.Title += "  -  " + txtHoldShort
		add(levelBad, "!!! %s", txtHoldTitle)
		add(levelNormal, "%s", txtHoldWhat)
		add(levelNormal, "")
		add(levelNormal, "What to do:")
		add(levelNormal, "  1. Leave the unit on.")
		add(levelNormal, "  2. %s", txtHoldReread)
		add(levelNormal, "  3. %s", folder)
		add(levelNormal, "  4. %s", Contact)
		if m.recheck != "" {
			add(levelWarn, "%s", m.recheck)
		} else {
			add(levelNormal, "")
		}
	}
	// the reason of the hold is in the flow log and the log folder; the
	// screen says what to do
	m.setKeys(f, keys{back: "Log", ok: "Check again"})
	return 0
}

func (m *Model) bundleRow(body *[]status.Line) {
	if m.Bundle == "" {
		*body = append(*body, status.Line{Level: levelNormal, Text: "The log folder is being written to the drive P10-INSTALL ..."})
		return
	}
	*body = append(*body, status.Line{Level: levelNormal, Text: "Log folder: " + m.Bundle})
}

func (m *Model) restartRows(body *[]status.Line, now time.Time) {
	add := func(level int, format string, a ...any) {
		*body = append(*body, status.Line{Level: level, Text: fmt.Sprintf(format, a...)})
	}
	if why := m.RestartBlocked(); why != "" {
		add(levelBad, "Restart is blocked: %s.", why)
		return
	}
	next := "the slot that the slot table selects"
	if u := m.Unit; u != nil && u.Next >= 0 {
		next = "slot " + slotWord(u.Slots[u.Next].Name) + " (" + orQ(u.Slots[u.Next].Summary) + ")"
	}
	if o := m.Outcome; o != nil && m.Plan != nil && (o.Screen == ScreenDone || o.State == StateD2) {
		next = "slot " + slotWord(m.Plan.Target) + " (the new install); if it does not start, slot " + slotWord(m.Plan.Keeper)
	}
	add(levelNormal, "A restart starts %s.", next)
	switch {
	case m.Outcome != nil && m.Plan != nil:
		add(levelNormal, "The installer wrote slot %s (see the log folder on the drive).", slotWord(m.Plan.Target))
	case m.Outcome != nil:
		add(levelNormal, "The installer wrote to this unit (see the log folder on the drive).")
	default:
		add(levelNormal, "The installer changed nothing on the unit.")
	}
	add(levelNormal, "")
	l, lvl := m.holdText("restart", now)
	add(lvl, "%s", l)
}
