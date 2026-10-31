package flow

import (
	"fmt"
	"strings"
	"time"

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
	Footer string
	Msg    string
	LED    string // "blue", "green", "red": working, done, stop
	// Tail: the newest output lines of a child, after the body: as many
	// (whole, wrapped) as fit below it; the log screen has them all.
	Tail []status.Line
	// Squeeze: empty body rows may be dropped when the body does not fit.
	Squeeze bool
	// Scroll: rows scrolled (FromEnd: up from the end, as on the log).
	Scroll  int
	FromEnd bool
	// ScrollKeys: the keys that scroll, shown in the footer when rows are
	// not on the screen.
	ScrollKeys string
}

// legend of the device keys and of a terminal. choose: the screen has
// something to choose with the up and down keys.
func (m *Model) legend(choose bool, ok, back string) string {
	var p []string
	if m.Terminal {
		if choose {
			p = append(p, "Up/Down choose")
		}
		if ok != "" {
			p = append(p, "Enter = "+ok)
		}
		if back != "" {
			p = append(p, "b = "+back)
		}
		return strings.Join(p, "   ")
	}
	if choose {
		p = append(p, "Vol+ / Vol- choose")
	}
	if ok != "" {
		p = append(p, "Mic key = "+ok)
	}
	if back != "" {
		p = append(p, "Speaker key = "+back)
	}
	return strings.Join(p, "   ")
}

// scrollKeys are the keys that scroll a screen whose rows do not all fit.
func (m *Model) scrollKeys() string {
	if m.Terminal {
		return "Up/Down scroll"
	}
	return "Vol+ / Vol- scroll"
}

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
	return "  Hold the OK key (Mic) for 3 seconds to " + what + ".", levelNormal
}

func (m *Model) slotLine(i int) string {
	if m.Unit == nil {
		return "?"
	}
	s := m.Unit.Slots[i]
	return fmt.Sprintf("%s, %s", orQ(s.Summary), s.AB.State())
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
		if m.Unit == nil {
			add(levelNormal, "Reading the eMMC (read-only) ...")
		} else {
			body = append(body, m.Unit.Lines...)
		}
		add(levelNormal, "")
		add(levelHeading, "This is the install image. Nothing is written before the hold of OK on the plan screen.")
		// the menu is in the footer (as on the status image): it is
		// always on the screen, however long the status is
		var mb strings.Builder
		for i, it := range statusMenu {
			if i == m.Sel {
				mb.WriteString("[ " + strings.ToUpper(it) + " ]  ")
			} else {
				mb.WriteString("  " + it + "    ")
			}
		}
		f.Footer = mb.String() + "    " + m.legend(true, "open", "")
		f.Squeeze = true
	case ScreenChecklist:
		f.Title += "  -  " + txtChecklistTitle
		add(levelNormal, "%s", txtChecklistIntro)
		add(levelNormal, "")
		for i, it := range m.items {
			mark := "  "
			if i == m.Sel {
				mark = "> "
			}
			box, lvl := "[ ]", levelNormal
			text := it.text
			var more []string
			switch it.kind {
			case itemAutoUpdate:
				state, todo, l := autoUpdateLine(m.Unit.AutoUpdate, m.unclearOK)
				text, lvl = it.text+": "+state, l
				if todo != "" {
					more = append(more, todo)
				}
				if d := m.Unit.AutoUpdate.Detail; d != "" {
					more = append(more, "read: "+d)
				}
				if m.Unit.AutoUpdate.State == "off" || m.unclearOK {
					box = "[x]"
				}
				if m.Unit.AutoUpdate.State == "on" {
					box = "[!]"
				}
			case itemConfirm:
				if m.confirmed[it.id] {
					box, lvl = "[x]", levelGood
				}
			case itemInfo:
				box = " - "
			}
			add(lvl, "%s%s %s", mark, box, text)
			for _, x := range more {
				add(lvl, "        %s", x)
			}
			add(levelNormal, "        why: %s", it.why)
		}
		add(levelNormal, "")
		mark := "  "
		if m.Sel == len(m.items) {
			mark = "> "
		}
		if ok, _ := m.checklistDone(); ok {
			add(levelGood, "%s[ Continue ]", mark)
		} else {
			add(levelNormal, "%s[ Continue ]  (every item first)", mark)
		}
		f.Footer = m.legend(true, "confirm", "cancel")
	case ScreenFirmware:
		f.Title += "  -  " + txtFirmwareTitle
		add(levelNormal, "%s", txtFirmwareCopy)
		add(levelNormal, "%s", txtFirmwareEject)
		add(levelNormal, "")
		switch {
		case m.reading:
			add(levelNormal, "%s", txtFirmwareRead)
		case m.refused != "":
			add(levelBad, "The drive was refused: %s", m.refused)
			add(levelNormal, "The drive is back on the computer: fix it there, then eject it again.")
		default:
			add(levelNormal, "%s", txtFirmwareWait)
			if !m.copyStart.IsZero() && now.Sub(m.copyLast) < 5*time.Second {
				el := m.copyLast.Sub(m.copyStart)
				f := el.Seconds() / measuredCopySecs
				if f > 0.99 {
					f = 0.99
				}
				add(levelNormal, "The computer writes to the drive: %s  %s of %s", Bar(f, 30), dur(el), minutes(measuredCopySecs))
			}
		}
		f.Footer = m.legend(false, "eject now", "cancel")
	case ScreenKeeper:
		f.Title += "  -  " + txtKeeperTitle
		add(levelNormal, "%s", txtKeeperIntro)
		add(levelWarn, "The check asked for your choice: %s", m.keeperWhy)
		add(levelNormal, "")
		for i := 0; i < 2; i++ {
			s := m.Unit.Slots[i]
			mark := "  "
			if i == m.Sel {
				mark = "> "
			}
			next := ""
			lvl := levelNormal
			if m.Unit.Next == i {
				next, lvl = "   << BOOTS NEXT", levelHeading // U7
			}
			add(lvl, "%sKeep slot %s: %s%s", mark, slotWord(s.Name), m.slotLine(i), next)
			o := m.Unit.Slots[1-i]
			warn := ""
			if !o.AB.Keeper() && o.AB.Bootable() {
				warn = " (it is still trying: its new contents are replaced)"
			}
			add(levelNormal, "      then slot %s is written and its contents are lost%s", slotWord(o.Name), warn)
			if !s.AB.Keeper() {
				add(levelWarn, "      slot %s is %s: it cannot be kept as the fallback", slotWord(s.Name), s.AB.State())
			}
		}
		add(levelNormal, "")
		add(levelNormal, "A wrong choice loses the contents of a slot. It cannot leave the unit without a slot that starts:")
		add(levelNormal, "the slot you keep must be successful, and it is never written.")
		f.Footer = m.legend(true, "keep this slot", "cancel")
	case ScreenPlanning:
		f.Title += "  -  " + txtPlanningTitle
		add(levelNormal, "%s", txtPlanningIntro)
		if m.Drive != nil {
			// the drive read went straight here: the file and the key
			// source are shown on this screen
			add(levelNormal, "Firmware file: %s, %d bytes", m.Drive.Name, m.Drive.Size)
			if m.Drive.KeyFile {
				add(levelNormal, "%s", txtKeyFile)
			} else {
				add(levelNormal, "%s", txtKeySearch)
			}
		}
		if m.Keeper != "" {
			add(levelNormal, "Slot kept (your choice): %s", slotWord(m.Keeper))
		}
		m.progressRows(&body, &f, now, false)
		f.Footer = m.legend(false, "", "cancel")
	case ScreenPlan:
		f.Title += "  -  " + txtPlanTitle
		m.planRows(&body)
		add(levelNormal, "")
		l, lvl := m.holdText("start the install", now)
		add(lvl, "%s", l)
		f.Footer = m.legend(false, "hold 3 s = start", "cancel")
	case ScreenProgress:
		f.Title += "  -  " + txtProgressTitle
		if m.Plan != nil {
			add(levelNormal, "Writing slot %s. Slot %s is not written.", slotWord(m.Plan.Target), slotWord(m.Plan.Keeper))
		}
		m.progressRows(&body, &f, now, true)
		add(levelWarn, "%s", txtProgressNote)
		f.Footer = m.legend(false, "", "")
	case ScreenDone, ScreenStopped, ScreenHold:
		m.endRows(&body, &f, now)
	case ScreenRefused:
		f.Title += "  -  " + txtRefusedTitle
		f.LED = "red"
		add(levelGood, "%s", txtRefusedNote)
		add(levelBad, "Why: %s", m.refused)
		add(levelNormal, "")
		add(levelNormal, "OK goes back to the firmware screen. The drive is back on the computer.")
		f.Footer = m.legend(false, "try again", "status")
	case ScreenLog:
		f.Title += "  -  Log"
		if m.Prog != nil {
			for _, l := range m.Prog.Lines {
				add(levelNormal, "  %s", l)
			}
		} else {
			add(levelNormal, "  (no install output yet)")
		}
		f.FromEnd = true // it opens at the end (the last lines: STOP, RESULT)
		f.Footer = m.legend(false, "back", "back")
	case ScreenRestart:
		f.Title += "  -  Restart"
		m.restartRows(&body, now)
		f.Footer = m.legend(false, "hold 3 s = restart", "back")
	}
	switch m.Screen {
	case ScreenLog, ScreenPlanning, ScreenProgress, ScreenPlan, ScreenDone, ScreenStopped, ScreenHold, ScreenRefused:
		f.Scroll, f.ScrollKeys = m.Scroll, m.scrollKeys()
	}
	f.Body = body
	return f
}

func (m *Model) progressRows(body *[]status.Line, f *Frame, now time.Time, install bool) {
	add := func(level int, format string, a ...any) {
		*body = append(*body, status.Line{Level: level, Text: fmt.Sprintf(format, a...)})
	}
	p := m.Prog
	if p == nil {
		add(levelNormal, "Starting ...")
		return
	}
	add(levelNormal, "")
	add(levelNormal, "%s   %s of %s", Bar(p.Fraction(now), 40), dur(now.Sub(p.Start)), minutes(int(p.Estimate().Seconds())))
	if install && m.Plan != nil {
		l, lvl := liveSafety(p, m.Plan.Keeper, m.Plan.Target)
		add(lvl, "%s", l)
	}
	if mb := p.MBps(); mb > 0 {
		add(levelNormal, "Write speed %.1f MB/s", mb)
	}
	add(levelNormal, "")
	if install {
		if p.Cur < 0 {
			add(levelNormal, "Checking the plan again, %d of 2 (nothing is written yet) ...", max(1, min(p.Computes, 2)))
		}
		for i, s := range p.Steps {
			mark := "   "
			lvl := levelNormal
			switch {
			case s.Done:
				mark, lvl = "ok ", levelGood
			case i == p.Cur:
				mark, lvl = ">> ", levelHeading
			}
			add(lvl, "%s%2d %s", mark, s.N, s.Label)
		}
	} else {
		add(levelNormal, "Plan computations: %d", p.Computes)
	}
	if p.Stop != "" {
		add(levelBad, "%s", p.Stop)
	}
	n := len(p.Lines)
	if n > 6 {
		n = 6
	}
	f.Tail = []status.Line{} // the newest lines that fit, below the body
	for _, l := range p.Lines[len(p.Lines)-n:] {
		f.Tail = append(f.Tail, status.Line{Level: levelNormal, Text: "  | " + l})
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
	add(levelWarn, "Write slot %s (now: %s). Its contents are lost.", slotWord(p.Target), m.slotLine(ti))
	add(levelGood, "Slot %s stays untouched (%s) and is your fallback.", slotWord(p.Keeper), m.slotLine(ki))
	if p.Payload != nil {
		add(levelNormal, "Firmware: %s %s (%s)", p.Payload.Version, p.Payload.BuildType, p.Payload.FirmwareFile)
	}
	if p.KeySource != "" {
		add(levelNormal, "Key: %s (it is never shown)", p.KeySource)
	}
	add(levelNormal, "")
	var total int64
	for _, s := range p.Steps {
		switch s.Op {
		case "write":
			total += s.Write.Length
			add(levelNormal, "  write %-9s %11d bytes", s.Write.Partition, s.Write.Length)
		case "skip":
			add(levelNormal, "  skip  %-9s (it holds the image already)", s.Write.Partition)
		}
	}
	add(levelNormal, "")
	add(levelNormal, "Time: %s (the plan is checked again twice, then %d MB are written and read back).",
		minutes(2*measuredPlanSecs+measuredStepsSecs), total/1e6)
	add(levelNormal, "After the install slot %s starts next. Slot %s stays the fallback.", slotWord(p.Target), slotWord(p.Keeper))
	if p.AutoUpdate != nil {
		add(levelNormal, "Android auto-update: %s%s", p.AutoUpdate.State, map[bool]string{true: " (you confirmed it is off)"}[p.AutoUpdate.UnclearConfirmed])
	}
	if p.RAMImage != nil {
		add(levelNormal, "Installer image %s, plan %s", p.RAMImage.Token, short(p.ID))
	}
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func (m *Model) endRows(body *[]status.Line, f *Frame, now time.Time) {
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
		return
	}
	switch o.Screen {
	case ScreenDone:
		f.Title += "  -  " + txtDoneTitle
		f.LED = "green"
		add(levelGood, "%s", stateSentence(StateD2, k, t))
		add(levelNormal, "A/B now: %s (read by the installer itself)", o.Decode)
		if m.Prog != nil && m.Prog.Agree {
			add(levelNormal, "uc-pxx-bootctl status agrees.")
		}
		m.bundleRow(body)
		add(levelNormal, "")
		l, lvl := m.holdText("restart", now)
		add(lvl, "%s", l)
		f.Footer = m.legend(false, "hold 3 s = restart", "status")
	case ScreenStopped:
		f.Title += "  -  " + txtStoppedTitle
		f.LED = "red"
		why := o.Why
		if m.Prog != nil && m.Prog.Stop != "" {
			why = m.Prog.Stop
		} else if m.Prog != nil && m.Prog.Result != "" {
			why = m.Prog.Result
		}
		add(levelBad, "Why: %s", why)
		add(levelGood, "%s", stateSentence(o.State, k, t))
		add(levelNormal, "A/B now: %s (read by the installer itself)", o.Decode)
		m.bundleRow(body)
		add(levelNormal, "")
		add(levelNormal, "OK goes back to the firmware screen to try again. Restart is allowed from the status menu.")
		f.Footer = m.legend(false, "try again", "status")
	case ScreenHold:
		f.Title += "  -  " + txtHoldShort
		f.LED = "red"
		add(levelBad, "!!! %s", txtHoldTitle)
		add(levelBad, "Keep the unit powered. Restart is removed from this installer.")
		add(levelNormal, "Why: %s", o.Why)
		raw := o.Raw32
		if raw == "" {
			raw = "unreadable"
		}
		add(levelNormal, "A/B sector, first 32 bytes: %s", raw)
		if o.Decode != "" {
			add(levelNormal, "Decoded (not trusted): %s", o.Decode)
		}
		m.bundleRow(body)
		add(levelNormal, "%s", Contact)
		add(levelNormal, "")
		if m.recheck != "" {
			add(levelWarn, "%s", m.recheck)
		}
		add(levelNormal, "OK reads the A/B sector again and says whether it is now safe to disconnect the power.")
		f.Footer = m.legend(false, "read the A/B sector again", "log")
	}
}

func (m *Model) bundleRow(body *[]status.Line) {
	if m.Bundle == "" {
		*body = append(*body, status.Line{Level: levelNormal, Text: "The log folder is being written to the drive P10-INSTALL ..."})
		return
	}
	*body = append(*body, status.Line{Level: levelNormal, Text: "Log: " + m.Bundle})
}

func (m *Model) restartRows(body *[]status.Line, now time.Time) {
	add := func(level int, format string, a ...any) {
		*body = append(*body, status.Line{Level: level, Text: fmt.Sprintf(format, a...)})
	}
	if why := m.RestartBlocked(); why != "" {
		add(levelBad, "Restart is blocked: %s.", why)
		return
	}
	next := "the slot that the A/B metadata selects"
	if u := m.Unit; u != nil && u.Next >= 0 {
		next = "slot " + slotWord(u.Slots[u.Next].Name) + " (" + orQ(u.Slots[u.Next].Summary) + ")"
	}
	if o := m.Outcome; o != nil && m.Plan != nil && (o.Screen == ScreenDone || o.State == StateD2) {
		next = "slot " + slotWord(m.Plan.Target) + " (the new install); if it does not start, slot " + slotWord(m.Plan.Keeper)
	}
	add(levelNormal, "A restart starts %s.", next)
	switch {
	case m.Outcome != nil:
		add(levelNormal, "The installer wrote to this unit (see the log folder on the drive).")
	default:
		add(levelNormal, "The installer changed nothing on the unit.")
	}
	add(levelNormal, "The restart writes the normal boot mode, so the unit does not come back into fastboot.")
	add(levelNormal, "If the installer crashes, the unit restarts into fastboot after 30 s.")
	add(levelNormal, "")
	l, lvl := m.holdText("restart", now)
	add(lvl, "%s", l)
}
