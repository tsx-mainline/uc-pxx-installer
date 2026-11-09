package flow

// The screens of the mainline install (mainline.go) and their texts, in the
// rules of texts.go: short, plain words; the slot that is written is named,
// and that the other one is not touched; "the power can be disconnected"
// only where the official screen says it (the same state sentences).

import (
	"fmt"
	"strings"
	"time"

	"uc-pxx-installer/internal/status"
)

const (
	txtUnlockTitle    = "Is the unit unlocked?"
	txtUnlockQuestion = "Confirm that the TEE unlock tool read the lock state back as UNLOCKED (lock_state = 0x01)."
	txtUnlockWhy      = "Mainline Linux starts only on an unlocked unit. On a locked unit slot A does not start: the unit tries it up to 7 times, then starts slot B (Android) again."
	txtUnlockReads    = "The installer cannot read the lock state itself (its status shows: Bootloader %s)."
	txtUnlockNoHint   = "Answer No if you did not unlock this unit, or if the tool did not show UNLOCKED."
	// txtUnlockTerminalWarn: before a terminal's yes (the rule of the panel question)
	txtUnlockTerminalWarn = "You are answering from a terminal. Only answer Yes if the TEE unlock tool showed UNLOCKED for this unit."
	txtUnlockNo           = "Mainline Linux needs an unlocked unit. Unlock it with the TEE unlock tool, then start the install again. Nothing was written."

	txtMLPlanningTitle = "Checking the mainline files"
	txtMLHashing       = "Checking the mainline files (sha256): manifest, boot image, root file system"
	txtMLUnit          = "Checking the unit (reading the slots; this can take a few minutes)"

	txtMLGate  = "Checked: a reviewed mainline build; you confirmed that the unit is unlocked."
	txtMLCable = "Before the restart, connect the unit to the network: the Ethernet port (eth0) or USB networking (usb0)."
	txtMLMark  = "Mainline Linux marks slot A as working only with a network link and sshd within 300 s of its start. Otherwise the unit starts slot B again after the tries of slot A."
	// txtMLMarkPlan: the same rule on the plan screen, before the install
	txtMLMarkPlan = "After the install, mainline Linux marks slot A as working only with a network link (eth0 or usb0) and sshd within 300 s; otherwise slot B starts again."

	// the plain words of the mainline refusals (mainlinePlain)
	txtMLDamaged  = "The folder " + MainlineFolder + " is not a valid mainline build, or it is damaged. Copy it again."
	txtMLChanged  = "The folder " + MainlineFolder + " changed after the check. Copy it again."
	txtMLBoth     = "The drive holds the folder " + MainlineFolder + " and a firmware file (.bin). Leave only one of them on the drive."
	txtMLNoFolder = "The folder " + MainlineFolder + " is not on the drive."
	txtMLGeneric  = "The installer found a problem (the details are below and in the log)."
)

// mlDriveRow names the bundle of the drive (the version, the size).
func (m *Model) mlDriveRow() string {
	if m.ml == nil {
		return "Mainline Linux: the folder " + MainlineFolder + " on the drive"
	}
	return fmt.Sprintf("Mainline Linux %s: the folder %s (%d MB)", m.ml.drive.Version, MainlineFolder, m.ml.drive.Size/1e6)
}

// mlVersion is "mainline Linux V" (without V when the drive was not read in
// this UI: a UI started again after the plan).
func (m *Model) mlVersion() string {
	if m.ml == nil || m.ml.drive.Version == "" {
		return "mainline Linux"
	}
	return "mainline Linux " + m.ml.drive.Version
}

// mlChain is the Android build that slot B keeps ("Android userdebug
// 1.0.0.94"): the boot chain the bundle needs in slot B, which the plan
// checked in slot B (plan check keeper-chain).
func (m *Model) mlChain() string {
	if m.ml == nil || m.ml.drive.ChainVersion == "" {
		return "Android"
	}
	return "Android " + m.ml.drive.ChainBuild + " " + m.ml.drive.ChainVersion
}

// mainlineView renders the screens of the mainline install into f and body.
// It reports false for every screen that View renders as for the official
// install: every screen of an official run, and the firmware, panel,
// progress, hold, restart, log and switch screens of a mainline run.
func (m *Model) mainlineView(f *Frame, body *[]status.Line, now time.Time) bool {
	add := func(level int, format string, a ...any) {
		*body = append(*body, status.Line{Level: level, Text: fmt.Sprintf(format, a...)})
	}
	k, t := "?", "?"
	if m.Plan != nil {
		k, t = m.Plan.Keeper, m.Plan.Target
	}
	switch {
	case m.Screen == ScreenUnlock:
		f.Title += "  -  " + txtUnlockTitle
		add(levelNormal, "%s", m.mlDriveRow()+". It is for slot A.")
		add(levelNormal, "")
		add(levelHeading, "%s", txtUnlockQuestion)
		add(levelNormal, "")
		add(levelNormal, "%s", txtUnlockWhy)
		add(levelNormal, txtUnlockReads, m.bootloaderRow())
		add(levelNormal, "%s", txtUnlockNoHint)
		add(levelNormal, "")
		switch {
		case !m.Terminal:
			add(levelNormal, "  OK (Mic) = Yes, Speaker = No.")
		case m.panelWarn:
			add(levelWarn, "%s", txtUnlockTerminalWarn)
			add(levelNormal, "")
			add(levelNormal, "  y = Yes (the unit is unlocked), b = No.")
		default:
			add(levelNormal, "  Enter (or y) = Yes, b = No. Then confirm the Yes with y.")
		}
		m.setKeys(f, keys{back: "No", ok: "Yes"})
		if m.Terminal && m.panelWarn {
			f.Footer = "y = Yes   b = No" // Enter does not answer here
		}
	case m.Screen == ScreenPlanning && m.ml != nil:
		f.Title += "  -  " + txtMLPlanningTitle
		add(levelNormal, "%s", m.mlDriveRow())
		add(levelNormal, "Unlocked: yes (your answer on the unlock screen)")
		add(levelNormal, "Install to slot A. Slot B is not touched.")
		m.mlPlanningRows(body, now)
		m.setKeys(f, keys{back: "Cancel"})
	case m.Screen == ScreenPlan && m.mainlinePlan():
		f.Title += "  -  " + txtPlanTitle
		ti := slotIndex(t)
		now0 := "unknown contents"
		if m.Unit != nil && ti >= 0 {
			now0 = orQ(m.Unit.Slots[ti].Summary)
		}
		add(levelNormal, "%s", m.mlDriveRow())
		if m.ml != nil {
			add(levelNormal, "  kernel %s, boot token %s", shortRev(m.ml.drive.Kernel), m.ml.drive.Token)
		} else {
			add(levelNormal, "")
		}
		add(levelNormal, "")
		add(levelWarn, "Slot %s gets %s; slot %s keeps %s (fallback).", slotWord(t), m.mlVersion(), slotWord(k), m.mlChain())
		add(levelNormal, "Slot %s now: %s. It is replaced. Slot %s is not touched.", slotWord(t), now0, slotWord(k))
		add(levelNormal, "")
		add(levelGood, "%s", txtMLGate)
		add(levelNormal, "%s", txtMLMarkPlan)
		add(levelNormal, "")
		l, lvl := m.holdText("start the install", now)
		add(lvl, "%s", l)
		m.setKeys(f, keys{back: "Cancel", ok: "Hold: start", hold: true})
	case m.Screen == ScreenDone && m.mainlinePlan() && m.Outcome != nil:
		f.Title += "  -  " + txtDoneTitle
		f.LED = "green"
		add(levelGood, "%s", stateSentence(StateD2, k, t))
		add(levelNormal, "Slot %s was not touched.", slotWord(k))
		add(levelNormal, "")
		add(levelWarn, "%s", txtMLCable)
		add(levelNormal, "%s", txtMLMark)
		add(levelNormal, "")
		m.bundleRow(body)
		add(levelNormal, "")
		l, lvl := m.holdText("restart", now)
		add(lvl, "%s", l)
		m.setKeys(f, keys{back: "Status", ok: "Hold: restart", hold: true})
	case m.Screen == ScreenStopped && m.mainlinePlan() && m.Outcome != nil:
		// the official stopped screen, with the mainline words of the reason
		f.Title += "  -  " + txtStoppedTitle
		f.LED = "red"
		o := m.Outcome
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
		add(levelBad, "Why: %s", mainlinePlain(why))
		m.bundleRow(body)
		add(levelNormal, "Press OK to try again, or Back for the status.")
		m.setKeys(f, keys{back: "Status", ok: "Try again"})
		if detail != "" {
			add(levelNormal, "")
			add(levelNormal, "Details: %s", detail)
			f.Clip = len(*body)
		}
	case m.Screen == ScreenRefused && m.ml != nil:
		// the official refused screen, with the mainline words of the reason
		f.Title += "  -  " + txtRefusedTitle
		f.LED = "red"
		why := m.refused
		if m.refusedDetail != "" {
			why = mainlinePlain(m.refusedDetail)
		}
		if !strings.Contains(why, "Nothing was written") {
			add(levelGood, "%s", txtRefusedNote)
		}
		add(levelBad, "Why: %s", why)
		add(levelNormal, "")
		add(levelNormal, "Press OK to try again. The drive is back on the computer.")
		if m.refusedDetail != "" {
			add(levelNormal, "")
			add(levelNormal, "Details: %s", m.refusedDetail)
			f.Clip = len(*body)
		}
		m.setKeys(f, keys{back: "Status", ok: "Try again"})
	default:
		return false
	}
	return true
}

// shortRev is the first 12 hex of a kernel pin.
func shortRev(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// mlPlanningRows: the checks of the mainline plan child, one row each, in a
// fixed place: the mainline files (pass 1: the manifest, the boot template
// and the root file system hashed), then the unit. Pass 1 prints no progress
// inside a file: a moving indicator, not a bar.
func (m *Model) mlPlanningRows(body *[]status.Line, now time.Time) {
	add := func(level int, format string, a ...any) {
		*body = append(*body, status.Line{Level: level, Text: fmt.Sprintf(format, a...)})
	}
	add(levelNormal, "")
	p := m.Prog
	if p == nil {
		add(levelNormal, "Starting ...")
		return
	}
	add(levelNormal, "%s   %s", Pulse(now.Sub(p.Start), 40), dur(now.Sub(p.Start)))
	add(levelNormal, "")
	if m.ml != nil && !m.ml.hashed {
		// the end of pass 1: "  manifest manifest.json; ... match the
		// manifest (sha256); template token T" (mainline.Verify). Kept once
		// seen: the log keeps only the last lines.
		for _, l := range p.Lines {
			if strings.Contains(l, " match the manifest (sha256)") {
				m.ml.hashed = true
			}
		}
	}
	hashed := m.ml != nil && m.ml.hashed
	for _, st := range []struct {
		label       string
		done, doing bool
	}{
		{txtMLHashing, hashed, !hashed},
		{txtMLUnit, false, hashed},
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
}

// mainlinePlain is a refusal or a stop of the mainline device mode in plain
// words: the first failed check of the mainline planner (plan.ComputeMainline,
// "install refused: ID: DETAIL; ID: DETAIL") that it names; else the device
// mode's own refusals of a mainline bundle; else the official words
// (plainReason), but not those that are about a firmware file or its key.
func mainlinePlain(raw string) string {
	best, say := -1, ""
	for _, c := range []struct{ id, say string }{
		{"unlock", "You did not confirm that the unit is unlocked."},
		{"mainline-model", "Mainline Linux is only for a UC-P10. This unit is another model."},
		{"unit-model", "The factory data of this unit does not match this installer image."},
		{"no-reestablish", "The slot table was reset when the installer was started (both slots are not tested yet). For mainline Linux, start the installer from Android in slot B (see the install guide)."},
		{"keeper", "Slot B must be a working Android userdebug slot (the fallback). It is not."},
		{"keeper-boots", "Slot B must be the slot that starts now. Start the unit in Android (slot B), then start the installer from there."},
		{"keeper-chain", "Slot B does not hold the Android userdebug boot files that this mainline build needs."},
		{"chain-manifest", "This mainline build needs boot files that this installer does not support."},
		{"chain-userdebug", "This mainline build needs boot files that this installer does not support."},
		{"vbmeta-verify", "The boot check data of slot B is not the one that this mainline build needs."},
		{"boot-cap", "The boot check data of slot B is not the one that this mainline build needs."},
		{"rollback", "The boot check data of slot B is not the one that this mainline build needs."},
		{"payload", txtMLDamaged},
		{"mainline-manifest", txtMLDamaged},
		{"payload-reviewed", "This mainline build is not on this installer's list of reviewed builds."},
		{"kernel-reviewed", "The kernel of this mainline build is not a reviewed kernel."},
		{"boot-control-files", "The boot control files of this mainline build are not the reviewed ones."},
		{"boot-assembled", "The boot image of this mainline build does not fit this unit (it is not as its manifest says, or it is too large)."},
		{"boot-fits", "The boot image of this mainline build does not fit this unit (it is not as its manifest says, or it is too large)."},
		{"autoupdate", "Crestron auto-update is on, or it could not be read."},
	} {
		for _, lead := range []string{"install refused: ", "; "} {
			if i := strings.Index(raw, lead+c.id+": "); i >= 0 && (best < 0 || i < best) {
				best, say = i, c.say
			}
		}
	}
	if best >= 0 {
		return say
	}
	for _, c := range []struct{ has, say string }{
		{" and a firmware file (", txtMLBoth},
		{"has no folder " + MainlineFolder, txtMLNoFolder},
		{"the mainline bundle changed", txtMLChanged},
		{"is not the one that pass 1 checked", txtMLChanged},
		{"the plan's mainline bundle", txtMLChanged},
		{"pass 1:", txtMLDamaged},
	} {
		if strings.Contains(raw, c.has) {
			return c.say
		}
	}
	switch r := plainReason(raw); {
	case strings.Contains(r, "The firmware file is not"):
		return txtMLDamaged
	case strings.Contains(r, "The unit or the firmware changed"):
		return "The unit or the mainline files changed after the check."
	case strings.Contains(r, "firmware"), strings.Contains(r, "key file"), strings.Contains(r, "slot that was not touched"),
		strings.Contains(r, "both slots must be checked"):
		// the key, the firmware gate's notes, the repair: not a mainline text
		return txtMLGeneric
	default:
		return r
	}
}
