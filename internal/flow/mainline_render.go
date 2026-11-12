package flow

// The screens of the mainline install (mainline.go) and their texts, in the
// rules of texts.go: short, plain words; the slot that is written is named,
// and that the other one is not touched; "the power can be disconnected"
// only where the official screen says it (the same state sentences).

import (
	"fmt"
	"strings"
	"time"

	"uc-pxx-installer/internal/plan"
	"uc-pxx-installer/internal/status"
)

const (
	txtUnlockTitle    = "Is the unit unlocked?"
	txtUnlockQuestion = "Confirm that the TEE unlock tool read the lock state back as UNLOCKED (lock_state = 0x01)."
	txtUnlockWhy      = "Mainline Linux starts only on an unlocked unit. On a locked unit slot A does not start, and the unit starts slot B (Android) again."
	txtUnlockReads    = "The installer cannot read the lock state itself (its status shows: Bootloader %s)."
	// the direct path reads lock_state itself (the in-RAM reader, MAINLINE-RC2-LS; review B-2)
	txtUnlockReadsDirect = "The installer also reads the lock state itself before its check, once in this start of the installer: only lock_state 0x01 (unlocked) lets it install. Your answer alone does not."
	txtMLUnlockDirect    = "Unlocked: the installer reads lock_state itself (once in this start); your answer: yes"
	// txtReaderHeld: the hold after the reader saw a change (MainlineHeld, review B-1)
	txtReaderHeld   = "The lock-state check saw the security partition, the slot table or the BCB change during its read. The installer wrote nothing, but it does not know why the bytes changed. Nothing more is written in this start of the installer."
	txtUnlockNoHint = "Answer No if you did not unlock this unit, or if the tool did not show UNLOCKED."
	// txtUnlockTerminalWarn: before a terminal's yes (the rule of the panel question)
	txtUnlockTerminalWarn = "You are answering from a terminal. Only answer Yes if the TEE unlock tool showed UNLOCKED for this unit."
	txtUnlockNo           = "Mainline Linux needs an unlocked unit. Unlock it with the TEE unlock tool, then start the install again. Nothing was written."

	txtMLPlanningTitle = "Checking the mainline files"
	txtMLHashing       = "Checking the mainline files (sha256): manifest, boot image, root file system"
	txtMLUnit          = "Checking the unit (reading the slots; this can take a few minutes)"

	txtMLGate  = "Checked: a reviewed mainline build; you confirmed that the unit is unlocked."
	txtMLCable = "Before the restart, connect the unit to a network that gives it an IPv4 address (DHCP) on the Ethernet port (eth0), or use USB networking (usb0)."
	txtMLMark  = "Mainline Linux marks slot A as working only when it has an IPv4 address on eth0 or usb0 and sshd runs within 300 s of its start. Otherwise the unit starts slot B again after the tries of slot A."
	// txtMLMarkPlan: the same rule on the plan screen, before the install
	txtMLMarkPlan = "After the install, mainline Linux marks slot A as working only after a healthy start (up 60 s, the services started, no kernel oops); otherwise slot B starts again."

	// the plain words of the mainline refusals (mainlinePlain)
	txtMLDamaged  = "The folder " + MainlineFolder + " is not a valid mainline build, or it is damaged. Copy it again."
	txtMLChanged  = "The folder " + MainlineFolder + " changed after the check. Copy it again."
	txtMLBoth     = "The drive holds the folder " + MainlineFolder + " and a firmware file (.bin). Leave only one of them on the drive."
	txtMLNoFolder = "The folder " + MainlineFolder + " is not on the drive."
	txtMLGeneric  = "The installer found a problem (the details are below and in the log)."

	// txtMLFirmwareCopy: the line that the firmware screen of an image with
	// the mainline install adds after the official txtFirmwareCopy
	txtMLFirmwareCopy = "For mainline Linux instead: step 1, the 1.0.0.94 userdebug .bin and an empty folder named " + MainlineFolder +
		"; step 2, when this screen asks, the folder " + MainlineFolder + " with its files (not both at once)."

	// step 1, the stash (the first drive phase)
	txtMLStashTitle   = "Mainline Linux, step 1 of 2: the .bin"
	txtMLStashWhat    = "The installer reads the boot files of Android userdebug 1.0.0.94 and the firmware key from this file."
	txtMLStashNothing = "Nothing is written to the unit. The installer keeps them in its memory (RAM) until the unit restarts or loses power."
	txtMLStashKeep    = "Keeping the boot files and the firmware key record in the installer's memory"
	// step 2, the swap of the drive
	txtMLSwapTitle    = "Mainline Linux, step 2 of 2: the folder"
	txtMLSwapDone     = "Step 1 is done: the installer has the boot files and the firmware key of %s in its memory (RAM). Nothing was written to the unit."
	txtMLSwapAgain    = "Step 1 was done already after this start of the installer. Nothing was written to the unit."
	txtMLSwapCopy     = "Now copy the folder " + MainlineFolder + " (the whole folder, with its files) to the drive UC-PXX-INST on your computer."
	txtMLSwapEmpty    = "The drive is empty again: the .bin is not needed any more."
	txtMLSwapPower    = "Step 1 is kept only until the unit restarts or loses power. After that nothing on the unit has changed: start again with step 1."
	txtMLStep1Missing = "Step 1 is not done after this start of the installer: copy the 1.0.0.94 userdebug .bin and an empty folder named " +
		MainlineFolder + " to the drive UC-PXX-INST, and eject it. The installer reads the .bin, then asks for the folder."
	txtMLStashFailed = "The installer could not read the boot files and the firmware key from the .bin (the details are below)."
	// txtMLStep1Full: the plan refused for want of step 1; the drive still
	// holds the folder (review M-2)
	txtMLStep1Full = "Step 1 is needed first (the firmware key is not kept on the unit yet). On the computer, delete the folder " +
		MainlineFolder + " from the drive UC-PXX-INST (the .bin does not fit next to it), copy the 1.0.0.94 userdebug .bin and make an empty folder named " +
		MainlineFolder + ", then eject the drive. The installer reads the .bin, then asks for the folder again."

	// the direct path (keeper A, the Crestron firmware; target B)
	txtReplaceConfirmed = "Confirmed: slot B, as it is now, is replaced. Nothing is written yet."
	txtUnlockWhyDirect  = "Mainline Linux starts only on an unlocked unit. On a locked unit slot B does not start, and the unit starts slot A (the Crestron firmware) again."
	txtMLGateDirect     = "Checked: a reviewed mainline build; you confirmed that the unit is unlocked; the boot files of step 1 are the reviewed 1.0.0.94 userdebug ones."
	txtMLDirectKeep     = "First the firmware key is kept on the unit, and slot A is made the fallback (only the slot table changes). The partitions of slot A are not written."
	txtMLMarkPlanD      = "After the install, mainline Linux marks slot B as working only after a healthy start (up 60 s, the services started, no kernel oops); otherwise slot A starts again."
	txtMLMarkD          = "Mainline Linux marks slot B as working only when it has an IPv4 address on eth0 or usb0 and sshd runs within 300 s of its start. Otherwise the unit starts slot A again after the tries of slot B."
)

// firmwareView is the firmware screen of View (render.go, case ScreenFirmware),
// line for line, with txtMLFirmwareCopy after txtFirmwareCopy.
// TestFirmwareViewIsViews holds the two equal but for that line.
func (m *Model) firmwareView(f *Frame, add func(level int, format string, a ...any), now time.Time) {
	f.Title += "  -  " + txtFirmwareTitle
	add(levelNormal, "%s", txtFirmwareCopy)
	add(levelNormal, "%s", txtMLFirmwareCopy)
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
	m.setKeys(f, keys{back: "Cancel", ok: "Eject"})
}

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
// install: every screen of an official run but the firmware screen, and the
// panel, progress, hold, restart, log and switch screens of a mainline run.
// The firmware screen (owner 2026-11-09) is View's, with one line more after
// txtFirmwareCopy: txtMLFirmwareCopy (firmwareView). So View itself, and the
// official texts, stay as they are.
func (m *Model) mainlineView(f *Frame, body *[]status.Line, now time.Time) bool {
	add := func(level int, format string, a ...any) {
		*body = append(*body, status.Line{Level: level, Text: fmt.Sprintf(format, a...)})
	}
	k, t := "?", "?"
	if m.Plan != nil {
		k, t = m.Plan.Keeper, m.Plan.Target
	}
	switch {
	case m.Screen == ScreenFirmware && m.ml != nil && m.ml.swap:
		m.swapView(f, add, now)
	case m.Screen == ScreenFirmware:
		m.firmwareView(f, add, now)
	case m.Screen == ScreenUnlock:
		f.Title += "  -  " + txtUnlockTitle
		why, slot := txtUnlockWhy, "A"
		if m.MainlineDirect() {
			why, slot = txtUnlockWhyDirect, "B"
		}
		add(levelNormal, "%s", m.mlDriveRow()+". It is for slot "+slot+".")
		add(levelNormal, "")
		add(levelHeading, "%s", txtUnlockQuestion)
		add(levelNormal, "")
		add(levelNormal, "%s", why)
		if m.MainlineDirect() {
			add(levelNormal, "%s", txtUnlockReadsDirect)
		} else {
			add(levelNormal, txtUnlockReads, m.bootloaderRow())
		}
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
	case m.Screen == ScreenPlanning && m.ml != nil && m.ml.stashing:
		f.Title += "  -  " + txtMLStashTitle
		if m.Drive != nil {
			add(levelNormal, "Firmware file: %s (%d MB)", m.Drive.Name, m.Drive.Size/1e6)
		} else {
			add(levelNormal, "")
		}
		add(levelNormal, "%s", txtMLStashWhat)
		add(levelGood, "%s", txtMLStashNothing)
		m.stashRows(body, now)
		m.setKeys(f, keys{back: "Cancel"})
	case m.Screen == ScreenPlanning && m.ml != nil:
		f.Title += "  -  " + txtMLPlanningTitle
		add(levelNormal, "%s", m.mlDriveRow())
		if m.MainlineDirect() {
			add(levelNormal, "%s", txtMLUnlockDirect)
		} else {
			add(levelNormal, "Unlocked: yes (your answer on the unlock screen)")
		}
		if m.MainlineDirect() {
			add(levelNormal, "Install to slot B. Slot A (the Crestron firmware) stays the fallback; its partitions are not written.")
		} else {
			add(levelNormal, "Install to slot A. Slot B is not touched.")
		}
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
		if m.directPlan() {
			tn := m.targetNow()
			if tn == "" {
				tn = "slot B holds firmware that the plan does not name"
			}
			add(levelWarn, "%s.", upperFirst(tn))
			add(levelNormal, "Slot %s gets %s and the boot files of Android userdebug 1.0.0.94 (step 1).", slotWord(t), m.mlVersion())
			add(levelNormal, "Slot %s now (status): %s.", slotWord(t), now0)
			add(levelNormal, "%s", txtMLDirectKeep)
			add(levelGood, "%s", txtMLGateDirect)
			add(levelNormal, "%s", txtMLMarkPlanD)
			add(levelNormal, "")
			if !m.ReplaceConfirmed() {
				l, lvl := m.holdText("confirm that slot B is replaced", now)
				add(lvl, "%s", l)
				m.setKeys(f, keys{back: "Cancel", ok: "Hold: yes", hold: true})
				break
			}
			add(levelGood, "%s", txtReplaceConfirmed)
		} else {
			add(levelWarn, "Slot %s gets %s; slot %s keeps %s (fallback).", slotWord(t), m.mlVersion(), slotWord(k), m.mlChain())
			add(levelNormal, "Slot %s now: %s. It is replaced. Slot %s is not touched.", slotWord(t), now0, slotWord(k))
			add(levelNormal, "")
			add(levelGood, "%s", txtMLGate)
			add(levelNormal, "%s", txtMLMarkPlan)
		}
		add(levelNormal, "")
		l, lvl := m.holdText("start the install", now)
		add(lvl, "%s", l)
		m.setKeys(f, keys{back: "Cancel", ok: "Hold: start", hold: true})
	case m.Screen == ScreenDone && m.mainlinePlan() && m.Outcome != nil:
		f.Title += "  -  " + txtDoneTitle
		f.LED = "green"
		add(levelGood, "%s", stateSentence(StateD2, k, t))
		mark := txtMLMark
		if m.directPlan() {
			add(levelNormal, "The partitions of slot %s were not written; it is the fallback.", slotWord(k))
			mark = txtMLMarkD
		} else {
			add(levelNormal, "Slot %s was not touched.", slotWord(k))
		}
		add(levelNormal, "")
		add(levelWarn, "%s", txtMLCable)
		add(levelNormal, "%s", mark)
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
		add(lvl, "%s", m.mlStateSentence(o.State, k, t))
		add(levelNormal, "")
		add(levelBad, "Why: %s", m.mlPlain(why))
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
		if m.refusedDetail != "" && !m.ml.stashFailed {
			why = m.mlPlain(m.refusedDetail)
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
	if strings.Contains(raw, "key-keep: ") && strings.Contains(raw, "no key from the first drive phase") {
		return txtMLStep1Full // no stash of this boot and no record kept on the unit: step 1 first
	}
	best, say := -1, ""
	for _, c := range []struct{ id, say string }{
		{"key-keep", "The firmware key cannot be kept on the unit by this install (the details are below and in the log)."},
		{"key-pin", "The firmware key on the unit is not the key of the reviewed 1.0.0.94 userdebug firmware. Nothing is written."},
		{"stash", txtMLStep1Full},
		{"stash-chain", "The boot files of step 1 are not the ones that this mainline build needs."},
		{"direct-start", "The slot state is not one that the direct install starts from (the reset of the page's entry, or a stop after it). Start the installer from the page again."},
		{"keeper-firmware", "Slot A does not hold a Crestron firmware that this installer knows, byte for byte. It is not a factory-stock unit."},
		{"payload-slot", "This mainline build is not reviewed for this slot."},
		{"reestablish", "The slot table cannot be made safe before the install (slot A cannot be made the fallback)."},
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

// swapView is the firmware screen after step 1 (the stash): the drive is
// empty again and waits for the folder (step 2). The rows of the drive's
// state (reading, refused, waiting, copying) are those of firmwareView.
func (m *Model) swapView(f *Frame, add func(level int, format string, a ...any), now time.Time) {
	f.Title += "  -  " + txtMLSwapTitle
	if m.ml.again || m.ml.drive.FWName == "" {
		add(levelGood, "%s", txtMLSwapAgain)
	} else {
		add(levelGood, txtMLSwapDone, m.ml.drive.FWName)
	}
	add(levelNormal, "")
	add(levelHeading, "%s", txtMLSwapCopy)
	add(levelNormal, "%s", txtMLSwapEmpty)
	add(levelNormal, "%s", txtFirmwareEject)
	add(levelNormal, "")
	add(levelWarn, "%s", txtMLSwapPower)
	add(levelNormal, "")
	switch {
	case m.reading:
		add(levelNormal, "Reading the drive ...")
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
	m.setKeys(f, keys{back: "Cancel", ok: "Eject"})
}

// stashRows: the checks of the stash child (step 1), one row each, in a
// fixed place: the key, the firmware's parts (pass 1, the bar), then the
// stash. They follow the lines of the device mode, as planningRows does.
func (m *Model) stashRows(body *[]status.Line, now time.Time) {
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
		add(levelNormal, "%s   %s", Pulse(now.Sub(p.Start), 40), dur(now.Sub(p.Start)))
	} else {
		add(levelNormal, "%s   %s", Bar(p.Fraction(now), 40), dur(now.Sub(p.Start)))
	}
	add(levelNormal, "")
	key := "Finding the key"
	if m.Drive != nil && m.Drive.KeyFile {
		key = "Reading the key file"
	}
	for _, st := range []struct {
		label       string
		done, doing bool
	}{
		{key, p.Computes > 0, p.Computes == 0},
		{fmt.Sprintf("Checking the firmware (sha256): %d of %d parts", min(p.Checked, n), n), p.Checked >= n, p.Computes > 0 && p.Checked < n},
		{txtMLStashKeep, false, p.Checked >= n},
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

// stashPlain is a refusal of the stash child in plain words.
func stashPlain(raw string) string {
	for _, c := range []struct{ has, say string }{
		{"the drive", "The drive UC-PXX-INST went back to the computer, or changed, while the .bin was read. Eject it again."},
		{"key search", "The installer did not find the firmware key on the unit. Copy the key file " + "p10-keys.txt" + " next to the .bin (see the install guide)."},
		{"pass 1", "The .bin is not the reviewed 1.0.0.94 userdebug firmware, or it is damaged. Copy the right file."},
		{"userdebug boot chain", "The .bin is not the 1.0.0.94 userdebug firmware. A mainline install needs that file in step 1."},
		{"exists already", "Step 1 was done already after this start of the installer."},
	} {
		if strings.Contains(raw, c.has) {
			return c.say
		}
	}
	return txtMLStashFailed
}

// upperFirst is s with its first letter in upper case.
func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// mlStateSentence is stateSentence for a mainline plan: a stop with the slot
// table as before (S0) after the keep-key step may have written the key
// record (two sectors of backup; review M-1), so it does not say "nothing
// was changed".
func (m *Model) mlStateSentence(st, k, t string) string {
	if st == StateS0 && m.Plan != nil && len(m.Plan.Steps) > 0 && m.Plan.Steps[0].Op == "keep-key" {
		return fmt.Sprintf("The slots are as before: slot %s starts as before. The power can be disconnected. Only the firmware key record may have been written (two sectors of the backup partition: the first step of this install).", slotWord(k))
	}
	return stateSentence(st, k, t)
}

// mlPlain is mainlinePlain with the words of the direct path where the
// userdebug-first words would name the wrong slot (review N-1).
func (m *Model) mlPlain(raw string) string {
	if m.MainlineDirect() || m.directPlan() {
		for _, c := range []struct{ id, say string }{
			{"keeper", "Slot A must be a working Crestron firmware slot (the fallback). It is not."},
			{"keeper-boots", "Slot A must be the slot that starts now (the reset of the page's entry). It is not."},
			{"vbmeta-verify", "The boot check data of step 1 is not the one that this mainline build needs."},
			{"boot-cap", "The boot check data of step 1 is not the one that this mainline build needs."},
			{"rollback", "The boot check data of slot A and of step 1 do not match (rollback index or key versions)."},
		} {
			if i := strings.Index(raw, "install refused: "+c.id+": "); i >= 0 && i == firstID(raw) {
				return c.say
			}
		}
	}
	return mainlinePlain(raw)
}

// firstID is the position of the first failed check of a refusal
// ("install refused: ID: ..."), or -1.
func firstID(raw string) int { return strings.Index(raw, "install refused: ") }
