package flow

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"

	"uc-pxx-installer/internal/avbab"
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
	// Panel: the model's screen and key row (nil: the UC-P10)
	Panel *Panel `json:",omitempty"`
}

// panel is the frame's panel (the UC-P10 when none is set).
func (f Frame) panel() Panel {
	if f.Panel == nil || f.Panel.Px <= 0 {
		return PanelUCP10
	}
	return *f.Panel
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
				p = append(p, "Enter, then y = "+strings.TrimPrefix(k.ok, "Hold: "))
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
	f.Keys[PlaceSpeaker], f.Keys[PlaceVolDown], f.Keys[PlaceVolUp], f.Keys[PlaceMic] = k.back, k.prev, k.next, k.ok
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
		return fmt.Sprintf("  Keep holding OK: %s in %d s  %s", what, HoldSeconds(left), strings.Repeat("#", int(left.Seconds()*4)+1)), levelWarn
	}
	return "  Hold OK (Mic) for 3 seconds to " + what + ".", levelNormal
}

// HoldSeconds is the time of a hold that is still needed, in whole seconds
// for the screen: 3, 2, 1 (owner feedback R3 item 3: no tenths). It never
// shows 0: the action starts at 0.
func HoldSeconds(left time.Duration) int {
	n := int(math.Ceil(left.Seconds()))
	if n < 1 {
		n = 1
	}
	return n
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
	if m.Panel.Px > 0 {
		p := m.Panel
		f.Panel = &p
	}
	var body []status.Line
	add := func(level int, format string, a ...any) {
		body = append(body, status.Line{Level: level, Text: fmt.Sprintf(format, a...)})
	}
	scr := m.Screen
	if m.mainlineView(&f, &body, now) {
		scr = 0 // a screen of the mainline install (mainline_render.go)
	}
	switch scr {
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
				next = "  (starts next)"
			}
			if keep.AB.Keeper() {
				add(levelHeading, "%sSlot %s: now %s%s", mark, slotWord(s.Name), orQ(s.Summary), next)
				add(levelNormal, "      Install here: slot %s is replaced; slot %s is not touched and stays the fallback.",
					slotWord(s.Name), slotWord(keep.Name))
			} else {
				add(levelNormal, "%sSlot %s: now %s%s", mark, slotWord(s.Name), orQ(s.Summary), next)
				add(levelWarn, "      You cannot choose this slot: slot %s is %s and cannot be the fallback.", slotWord(keep.Name), plainState(keep.AB))
			}
			add(levelNormal, "")
		}
		add(levelNormal, "%s", m.targetWhy())
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
		m.setKeys(&f, keys{back: "Cancel", ok: "Hold: start", hold: true})
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
		// only a smaller console (the UC-P8 at 91 x 28) needs it: its empty
		// rows go before a row of a step would (W-P6)
		f.Squeeze = true
	case ScreenDone, ScreenStopped, ScreenHold:
		f.Clip = m.endRows(&body, &f, now)
	case ScreenPanel:
		f.Title += "  -  " + txtPanelTitle
		add(levelHeading, "%s", txtPanelQuestion)
		add(levelNormal, "")
		add(levelNormal, "%s", txtPanelWhy)
		add(levelNormal, "")
		pk := keys{back: "No", ok: "Yes"}
		switch {
		case !m.Terminal:
			add(levelNormal, "  OK (Mic) = Yes, Speaker = No.")
		case m.panelWarn:
			// a terminal: the warning first (owner 2026-11-01)
			add(levelWarn, "%s", txtPanelTerminalWarn)
			add(levelNormal, "")
			add(levelNormal, "  y = Yes (I see this screen on the unit's panel), b = No.")
		default:
			add(levelNormal, "  Enter (or y) = Yes, b = No. Then confirm the Yes with y.")
		}
		m.setKeys(&f, pk)
		if m.Terminal && m.panelWarn {
			f.Footer = "y = Yes   b = No" // Enter does not answer here
		}
	case ScreenRefused:
		f.Title += "  -  " + txtRefusedTitle
		f.LED = "red"
		if !strings.Contains(m.refused, "Nothing was written") {
			add(levelGood, "%s", txtRefusedNote)
		}
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
	case ScreenRepair:
		f.Title += "  -  " + txtRepairTitle
		f.LED = "red"
		keeper := "?"
		if m.Plan != nil {
			keeper = slotWord(m.Plan.Keeper)
		}
		add(levelNormal, "Repairing the slot table: slot %s (the slot that was not touched) is made the slot that starts.", keeper)
		add(levelWarn, "Keep the unit powered until the repair ends.")
		add(levelNormal, "")
		p := m.Prog
		if p == nil {
			p = NewProgress(false, time.Time{})
		}
		n := len(plan.SlotParts)
		for _, st := range []struct {
			label       string
			done, doing bool
		}{
			{fmt.Sprintf("Checking slot %s (%d of %d parts)", keeper, min(p.RepairKept, n), n), p.RepairKept >= n || p.RepairWrites > 0, p.RepairKept < n && p.RepairWrites == 0},
			{"Writing the slot table", p.RepairWrites > 0 && p.Result != "", p.RepairWrites > 0 && p.Result == ""},
			{"Checking the result", p.Result != "", false},
		} {
			mark, lvl := "   ", levelNormal
			switch {
			case st.done:
				mark, lvl = "ok ", levelGood
			case st.doing:
				mark, lvl = ">> ", levelHeading
			}
			add(lvl, "%s%s", mark, st.label)
		}
		m.setKeys(&f, keys{})
	case ScreenSafe:
		f.Title += "  -  " + txtSafeTitle
		f.LED = "green"
		add(levelGood, "%s", m.safeText)
		add(levelGood, "%s", txtSafe)
		add(levelNormal, "")
		m.bundleRow(&body)
		add(levelNormal, "Press OK to install again, or Back for the status.")
		m.setKeys(&f, keys{back: "Status", ok: "Install again"})
	case ScreenSwitch:
		f.Title += "  -  " + txtSwitchTitle
		add(levelNormal, "%s", txtSwitchIntro)
		add(levelNormal, "")
		for i := 0; i < 2 && m.Unit != nil; i++ {
			s := m.Unit.Slots[i]
			mark := "  "
			if i == m.Sel {
				mark = "> "
			}
			now := ""
			if m.Unit.Next == i {
				now = "  (starts next)"
			}
			why := m.switchGate(s.Name)
			lvl := levelHeading
			if why != "" {
				lvl = levelNormal
			}
			add(lvl, "%sSlot %s: %s, %s%s", mark, slotWord(s.Name), orQ(s.Summary), plainState(s.AB), now)
			if why == "" {
				add(levelNormal, "      %s", otherAfterSwitch(m.Unit.Slots[1-i].Name, m.Unit.Slots[1-i].AB))
			} else {
				add(levelNormal, "      %s", why)
			}
			add(levelNormal, "")
		}
		// OK starts the check (a dry run: it reads only); the 3 s hold comes
		// on the next screen, with the predicted change (owner 2026-11-01)
		if m.Terminal {
			add(levelNormal, "  Press Enter to check the switch. Nothing is written yet.")
		} else {
			add(levelNormal, "  Press OK (Mic) to check the switch. Nothing is written yet.")
		}
		m.setKeys(&f, keys{back: "Back", next: "Next", prev: "Previous", ok: "Check"})
	case ScreenSwitchConfirm:
		f.Title += "  -  " + txtSwitchConfirmTitle
		if m.switchState != switchConfirm {
			add(levelNormal, "Checking the switch to slot %s. Nothing is written.", slotWord(m.switchTo))
			add(levelNormal, "")
			add(levelNormal, "Checking the slot table ...")
			m.setKeys(&f, keys{})
			break
		}
		add(levelNormal, "Now:              %s.", abSentence(m.switchBefore, false))
		add(levelHeading, "After the switch: %s.", abSentence(m.switchAfter, true))
		add(levelNormal, "")
		l, lvl := m.holdText("switch", now)
		add(lvl, "%s", l)
		add(levelNormal, "  Back cancels the switch: nothing is written.")
		add(levelNormal, "")
		// for the check of a supervised session (rule 9): the bytes the
		// dry run read and the bytes it would write, and the sha256 of each
		sb, sa := sha256.Sum256(m.switchBefore), sha256.Sum256(m.switchAfter)
		add(levelNormal, "Details of the slot table (A/B sector, LBA 49156):")
		add(levelNormal, "  bytes 0..31 now:    %s", hex.EncodeToString(m.switchBefore[:32]))
		add(levelNormal, "  bytes 0..31 after:  %s", hex.EncodeToString(m.switchAfter[:32]))
		add(levelNormal, "  sha256 now:         %s", hex.EncodeToString(sb[:]))
		add(levelNormal, "  sha256 after:       %s", hex.EncodeToString(sa[:]))
		m.setKeys(&f, keys{back: "Cancel", ok: "Hold: switch", hold: true})
	case ScreenSwitching:
		// the switch runs: a fixed power row, no key (review R3 L-3)
		f.Title += "  -  " + txtSwitchingTitle
		f.LED = "red"
		if to := slotIndex(m.switchTo); to >= 0 && m.Unit != nil && !m.Unit.Slots[1-to].AB.Keeper() {
			// a never-tried (trying) other slot is not a fallback (review
			// P10-ENTER-CC2 I-2)
			o := m.Unit.Slots[1-to]
			add(levelNormal, "Slot %s is made the slot that starts next; slot %s stays %s.",
				slotWord(m.switchTo), slotWord(o.Name), plainState(o.AB))
		} else if to >= 0 {
			add(levelNormal, "Slot %s is made the slot that starts next; slot %s becomes the fallback.",
				slotWord(m.switchTo), slotWord(otherSlot(m.switchTo)))
		} else {
			add(levelNormal, "The slot that starts next is being switched.")
		}
		add(levelWarn, "%s", txtSwitchPower)
		add(levelNormal, "")
		add(levelNormal, "Checking, then writing the slot table ...")
		m.setKeys(&f, keys{})
	case ScreenRestart:
		f.Title += "  -  Restart"
		m.restartRows(&body, now)
		m.setKeys(&f, keys{back: "Back", ok: "Hold: restart", hold: true})
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

// abSentence says which slot starts next and what the other slot is, from
// a valid A/B sector (the switch's confirm).
func abSentence(sec []byte, after bool) string {
	a, err := avbab.Parse(sec)
	if err != nil || a.Validate() != nil || a.Pick() < 0 {
		return "the slot state is not known"
	}
	n := a.Pick()
	next, other := slotWord(avbab.SlotName(n)), slotWord(avbab.SlotName(1-n))
	switch {
	case !a.Slots[1-n].Bootable():
		return fmt.Sprintf("slot %s starts next; slot %s does not start", next, other)
	case !a.Slots[1-n].Keeper():
		// bootable but never marked successful (trying): not a fallback
		// (review P10-ENTER-CC2 I-2)
		return fmt.Sprintf("slot %s starts next; slot %s is %s, so it is not a proven fallback", next, other, plainState(a.Slots[1-n]))
	case after:
		return fmt.Sprintf("slot %s starts next; slot %s is the fallback and stays bootable", next, other)
	}
	return fmt.Sprintf("slot %s starts next; slot %s is the fallback", next, other)
}

// otherAfterSwitch is the row under a slot of the switch screen: what the
// other slot is after the switch. Only a successful slot is called the
// fallback; a trying slot (for example the never-tried slot of a stock
// unit) is not (review P10-ENTER-CC2 I-2).
func otherAfterSwitch(name string, ab avbab.Slot) string {
	if ab.Keeper() {
		return fmt.Sprintf("Start this slot next: slot %s becomes the fallback.", slotWord(name))
	}
	return fmt.Sprintf("Start this slot next: slot %s stays %s, so it is not a proven fallback.", slotWord(name), plainState(ab))
}

func otherSlot(s string) string {
	if s == "a" {
		return "b"
	}
	return "a"
}

// targetWhy is why the screen asks for the target, in plain words, with
// the slot letters (owner, 2026-11-01).
func (m *Model) targetWhy() string {
	switch {
	case strings.Contains(m.keeperWhy, "must be successful") && m.Unit != nil:
		// the slot that is not tested yet (trying) cannot be kept
		x := 0
		if m.Unit.Slots[0].AB.Keeper() {
			x = 1
		}
		X, Y := slotWord(m.Unit.Slots[x].Name), slotWord(m.Unit.Slots[1-x].Name)
		return fmt.Sprintf("Slot %s is not tested yet. You can install only to slot %s; slot %s stays as the fallback.", X, X, Y)
	case strings.Contains(m.keeperWhy, "both slots are successful"):
		return "Both slots work. Choose the one to replace; the other stays as the fallback."
	}
	return "Choose the slot to replace; the other stays as the fallback."
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
	n := len(plan.SlotParts)
	if p.Checked >= n {
		// the firmware is checked; now reading the unit's slots to compute
		// the plan — a multi-minute step with no sub-progress. Show a moving
		// indicator, not a full bar that reads as frozen (R4 finding 3).
		add(levelNormal, "%s   %s", Pulse(now.Sub(p.Start), 40), dur(now.Sub(p.Start)))
	} else {
		add(levelNormal, "%s   %s", Bar(p.Fraction(now), 40), dur(now.Sub(p.Start)))
	}
	add(levelNormal, "")
	key := "Finding the key"
	if m.Drive != nil && m.Drive.KeyFile {
		key = "Reading the key file"
	}
	stages := []struct {
		label       string
		done, doing bool
	}{
		{key, p.Computes > 0, p.Computes == 0},
		{fmt.Sprintf("Checking the firmware (sha256): %d of %d parts", min(p.Checked, n), n), p.Checked >= n, p.Computes > 0 && p.Checked < n},
		{"Checking the unit (reading the slots; this can take a few minutes)", false, p.Checked >= n},
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
			// the device mode prints no progress inside a step. A one-shot
			// flash makes a "time left" estimate misleading (R4 finding 4), so
			// the detail says only the size written and the time so far; the
			// read-back (verify) is a multi-minute step with no sub-progress,
			// so it says to expect that rather than reading as frozen (R4
			// finding 3).
			switch {
			case s.Op == "write":
				detail(fmt.Sprintf("%d MB to write, %s so far", s.Bytes/1e6, in))
			case s.Op == "verify":
				detail("reading everything back; this can take a few minutes (" + in + " so far)")
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
	// lead each line with the action, so which slot is overwritten and which
	// is kept is unambiguous even when both slots hold the same firmware
	// (a stock first install); the target is always the slot being written,
	// the keeper the slot kept as the fallback (R4 finding 2)
	add(levelWarn, "Install to slot %s: now %s. After the install: %s.", slotWord(p.Target), now, fw)
	keep := "unknown contents"
	if m.Unit != nil {
		keep = orQ(m.Unit.Slots[ki].Summary)
	}
	add(levelGood, "Keep slot %s: not touched (%s). It stays the fallback.", slotWord(p.Keeper), keep)
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
	// the screen decides, not the install's Outcome: a hold after a switch
	// has an Outcome of an earlier install, or none (review R3 H-1)
	if o == nil && m.Screen != ScreenHold {
		add(levelBad, "no result")
		return 0
	}
	switch m.Screen {
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
		m.setKeys(f, keys{back: "Status", ok: "Hold: restart", hold: true})
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
		add(levelBad, "Why: %s", m.plain(why))
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
	folder := "The log folder is being written to the drive UC-PXX-INST ..."
	if m.Bundle != "" {
		folder = "Take the log folder from " + m.Bundle + "."
	}
	keeper := "?"
	if m.Plan != nil {
		keeper = slotWord(m.Plan.Keeper)
	}
	hk := keys{back: "Log", ok: "Check again"}
	switch {
	case m.repair == repairDead:
		// the repair or a switch wrote, and the state is not known: no
		// more repair; ask for help
		title, what := txtHoldShort, txtRepairDead
		if m.deadBy == "switch" {
			title, what = txtSwitchHoldShort, txtSwitchDead
		}
		f.Title += "  -  " + title
		add(levelBad, "!!! %s", txtHoldTitle)
		add(levelNormal, "%s", what)
		add(levelNormal, "")
		add(levelNormal, "What to do:")
		add(levelNormal, "  1. Leave the unit on.")
		add(levelNormal, "  2. %s", folder)
		add(levelNormal, "  3. %s", Contact)
		add(levelNormal, "  4. Wait for help before you disconnect the power.")
		if m.recheck != "" {
			add(levelWarn, "%s", m.recheck)
		}
	case m.lockStop():
		// a STOP of the lock-state write (flow review MINOR-B): the security
		// partition may be torn and its only backup is in RAM; no repair (a
		// mainline plan never offers one)
		f.Title += "  -  " + txtLockHoldShort
		add(levelBad, "!!! %s", txtHoldTitle)
		add(levelBad, "%s", txtLockHoldWhat)
		add(levelWarn, "%s", txtLockHoldCopy)
		add(levelNormal, "")
		add(levelNormal, "What to do:")
		add(levelNormal, "  1. Leave the unit on.")
		add(levelNormal, "  2. %s", txtLockHoldHelp)
		add(levelNormal, "  3. %s", folder)
		add(levelNormal, "  4. %s", Contact)
		if m.recheck != "" {
			add(levelWarn, "%s", m.recheck)
		}
	default:
		f.Title += "  -  " + txtHoldShort
		add(levelBad, "!!! %s", txtHoldTitle)
		add(levelNormal, "%s", txtHoldWhat)
		add(levelNormal, "")
		switch m.repair {
		case repairChecking:
			add(levelNormal, "%s", txtRepairChecking)
		case repairOffer:
			add(levelWarn, txtRepairOffer, keeper)
			if m.repairWhy != "" && m.repairTries > 0 {
				add(levelNormal, "The first try did not write anything: %s", m.repairWhy)
			}
			add(levelWarn, "%s", txtRepairOfferKey)
			// "Check again" stays on Vol- (the RULES entry names that key)
			hk = keys{back: "Log", prev: "Check again", ok: "Hold: repair", hold: true}
		case repairRefused:
			if m.repairTries >= MaxRepairTries {
				add(levelNormal, txtRepairTwice, m.repairWhy)
			} else {
				add(levelNormal, txtRepairRefused, m.repairWhy)
			}
		}
		add(levelNormal, "")
		add(levelNormal, "What to do:")
		add(levelNormal, "  1. Leave the unit on.")
		if m.repair == repairOffer {
			add(levelNormal, "  2. Repair the slot table (above), or press Vol- to check the slots again.")
		} else {
			add(levelNormal, "  2. %s", txtHoldReread)
		}
		add(levelNormal, "  3. %s", folder)
		add(levelNormal, "  4. %s", Contact)
		if m.recheck != "" {
			add(levelWarn, "%s", m.recheck)
		}
	}
	if m.running != "" {
		hk.ok, hk.prev = "", "" // a check runs
	}
	m.setKeys(f, hk)
	return 0
}

func (m *Model) bundleRow(body *[]status.Line) {
	if m.Bundle == "" {
		*body = append(*body, status.Line{Level: levelNormal, Text: "The log folder is being written to the drive UC-PXX-INST ..."})
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
	case m.abChanged:
		// a switch wrote the A/B sector (and nothing else)
		add(levelNormal, "The installer changed the slot table of the unit (the A/B sector). It wrote no partition.")
	default:
		add(levelNormal, "The installer changed nothing on the unit.")
	}
	add(levelNormal, "")
	l, lvl := m.holdText("restart", now)
	add(lvl, "%s", l)
}
