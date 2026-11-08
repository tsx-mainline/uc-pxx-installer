package flow

// The texts of the screens (flow-r3: the owner's rewrite of 2026-10-31,
// drafts until the owner approves them from the renders). For an end user
// who knows the unit: short, plain words, no internal names (no "plan",
// "keeper", "A/B sector", "exit 3", "bootctl", "D1"); the rest goes to the
// log. Rules for every text here:
//   - never "the unit turns off": poweroff in the RAM image restarts the
//     unit (first supervised install, 2026-10-28);
//   - "the power can be disconnected" only for a known slot state: S0 or
//     D1 at the end of an install (the flow's own read), or after the
//     repair's check of a hold (exit 0, REPAIR-SAFE and the flow's own
//     read, review R3 M-3); during the install one line: keep the unit
//     powered until it ends;
//   - the hold says keep the unit powered; a read of the sector on the hold
//     never says more (review R3 L-2);
//   - the slot that is written is always named, and that the other one is
//     not touched.

import (
	"fmt"
	"strings"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/plan"
)

// IssuesURL is the issue tracker of the uc-pxx-installer repository on
// GitHub: the contact of the hold screen and of README.txt in the log bundle
// (U1; the URL confirmed by the owner 2026-10-30). Change it here, and only
// here; "" leaves the URL out of the text.
const IssuesURL = "https://github.com/tsx-mainline/uc-pxx-installer/issues"

// Contact is the support text of the hold screen (after the row with the
// log folder). BundleContact is the same text for README.txt in the folder.
var (
	Contact       = contactText(IssuesURL, "that folder")
	BundleContact = contactText(IssuesURL, "this folder")
)

// contactText is one row of the screen (120 columns): with the URL it does
// not repeat the project's name (the URL names it).
func contactText(url, folder string) string {
	if url == "" {
		return "Open an issue in the uc-pxx-installer project on GitHub and attach " + folder + "."
	}
	return "Open an issue at " + url + " and attach " + folder + "."
}

const (
	txtNotRead           = "The unit is still being read. Try again in a moment."
	txtABInvalid         = "The slot table of this unit cannot be read, so you cannot install. See the log."
	txtAutoUpdateOn      = "Crestron auto-update is on. Turn it off in the Crestron web UI, then start the installer again."
	txtAutoUpdateUnclear = "The installer could not read the Crestron auto-update setting. Check that it is off in the web UI, then start the installer again; if this repeats, open an issue."
	txtStatusInstall     = "Install: first do the steps of \"Before you start\" in the install guide (auto-update off, stable power)."

	txtFirmwareTitle = "Firmware"
	txtFirmwareCopy  = "Copy the firmware file (.bin) to the drive P10-INSTALL on your computer."
	txtFirmwareEject = "Then eject the drive on the computer, or press OK here to eject it."
	txtFirmwareWait  = "Waiting for the drive ..."
	txtFirmwareRead  = "Reading the firmware file ..."
	txtKeyFile       = "Key: from the key file p10-keys.txt"
	txtKeySearch     = "Key: from the Crestron firmware on this unit"

	txtTargetTitle   = "Install to which slot?"
	txtTargetIntro   = "Choose the slot to install to. Its contents are replaced. The other slot is not touched."
	txtTargetRefused = "You cannot install to slot %s: slot %s is %s and cannot be the fallback."

	txtPlanningTitle = "Checking the firmware"

	txtPlanTitle = "Ready to install"

	txtProgressTitle = "Installing"
	txtProgressPower = "Keep the unit powered until the install ends."
	txtCheckAgain    = "Checking the firmware and the unit again before the first write"
	txtProgressKey   = "The install cannot be stopped here. Keep the unit powered until it ends."

	txtDoneTitle    = "Install done"
	txtStoppedTitle = "Install stopped"
	txtHoldTitle    = "KEEP THE UNIT POWERED. DO NOT RESTART IT."
	txtHoldShort    = "Install stopped: keep the unit powered"
	txtHoldWhat     = "The install did not end normally. It is not known which slot would start after a restart."
	txtHoldReread   = "Press OK (Mic) to check the slots again."
	txtRefusedTitle = "Not installed"
	txtRefusedNote  = "Nothing was written to the unit."

	txtRecheckUnknown = "Checked again: the slot state is still not known. Keep the unit powered."

	txtToolRuns       = "Wait: the installer is checking or writing the slot table."
	txtCheckingAgain  = "Checking the slots again ..."
	txtRepairKey      = "The repair cannot be stopped here. Keep the unit powered until it ends."
	txtRepairTitle    = "Repairing the slot table"
	txtRepairChecking = "Checking whether the slot table can be repaired (nothing is written) ..."
	txtRepairOffer    = "The slot table can be repaired: slot %s (the slot that was not touched) is made the slot that starts."
	txtRepairOfferKey = "Hold OK (Mic) for 3 seconds to repair. Vol- checks the slots again."
	txtRepairRefused  = "The slot table cannot be repaired here: %s"
	txtRepairDead     = "The repair did not finish: the slot state is not known. Do not try again yourself."
	txtRepairTwice    = "The repair was tried twice and did not write anything: %s"
	txtOwnReadDiffers = "A second check of the slot table does not show the slot that was not touched as working."
	txtSafeTitle      = "Install stopped: safe"
	txtSafe           = "The slot table was checked. The power can be disconnected."

	txtPanelTitle    = "Check the screen"
	txtPanelQuestion = "Confirm you can read this text on the unit's own screen, then press OK (Mic)."
	txtPanelWhy      = "The installer works only when its screen shows on the unit's panel. Answer No if you see it only on a computer (the serial terminal)."
	txtPanelNo       = "This installer works only when its screen shows on the unit's panel. Nothing was written."
	// txtPanelTerminalWarn: before a terminal's yes (owner 2026-11-01)
	txtPanelTerminalWarn = "You are answering from a terminal. Only answer Yes if you can see this same screen on the unit's own panel. If the panel shows nothing, answer No."

	txtSwitchTitle      = "Switch which slot starts"
	txtSwitchIntro      = "Choose the slot that starts next. The other slot stays as it is."
	txtSwitchAlready    = "Slot %s already starts next."
	txtSwitchNotWorking = "You cannot choose slot %s: it is %s."
	txtSwitchNoFallback = "You cannot choose this slot now: slot %s would be the fallback, and it cannot start."
	txtSwitchingTitle   = "Switching the slot that starts"
	txtSwitchPower      = "Keep the unit powered until the switch ends."
	txtSwitchKey        = "The switch cannot be stopped here. Keep the unit powered until it ends."
	txtSwitchDead       = "The switch did not finish: the slot state is not known. Do not try again yourself."
	txtSwitchHoldShort  = "Switch stopped: keep the unit powered"
	// the confirm before the switch's write (owner 2026-11-01)
	txtSwitchConfirmTitle = "Switch which slot starts: check"
	txtSwitchCheckingKey  = "Wait: the switch is being checked. Nothing is written yet."
	txtSwitchCanceled     = "Not switched. Nothing was written."
	txtSwitchStale        = "The slot table changed after the check. Nothing was written; it is checked again."
	txtSwitchDisagree     = "two checks of the slot table do not agree."

	// statusReadOnly is the status image's safety row (internal/status):
	// the flow's status says only the flag
	statusReadOnly = "  This image only reads. eMMC read-only flag: "
)

func slotWord(s string) string { return strings.ToUpper(s) }

// lowerFirst is s with a small first letter (a sentence after a colon).
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// plainState is a slot's state in plain words.
func plainState(s avbab.Slot) string {
	switch s.State() {
	case "successful":
		return "working"
	case "trying":
		return fmt.Sprintf("not tested yet (%d tries left)", s.Tries)
	case "unbootable":
		return "not bootable"
	}
	return "not valid"
}

// stateSentence is the slot state after an end, for the user.
func stateSentence(st string, keeper, target string) string {
	k, t := slotWord(keeper), slotWord(target)
	switch st {
	case StateS0:
		return fmt.Sprintf("Nothing on the unit was changed. Slot %s starts as before. The power can be disconnected.", k)
	case StateD1:
		return fmt.Sprintf("Slot %s is not finished and will not start; slot %s starts. The power can be disconnected.", t, k)
	case StateD2:
		return fmt.Sprintf("Slot %s is installed and starts next. If it does not start, slot %s starts again after a few restarts.", t, k)
	}
	return "The slot state is not known."
}

// holdSentence is a known state read again on the hold: as stateSentence,
// but never "the power can be disconnected" (review R3 L-2).
func holdSentence(st string, keeper, target string) string {
	k, t := slotWord(keeper), slotWord(target)
	switch st {
	case StateS0:
		return fmt.Sprintf("Nothing on the unit was changed; slot %s starts as before.", k)
	case StateD1:
		return fmt.Sprintf("Slot %s is not finished and will not start; slot %s starts.", t, k)
	case StateD2:
		return fmt.Sprintf("Slot %s is installed and starts next.", t)
	}
	return "The slot state is not known."
}

// SafetyNote is what a power cut would mean now, for the flow log (the
// screen says only "Keep the unit powered until the install ends").
func SafetyNote(p *Progress, keeper, target string) string {
	k, t := slotWord(keeper), slotWord(target)
	switch {
	case p.InBootctl():
		return "power: the slot table is being written now: a power cut now is not safe"
	case p.Armed():
		return fmt.Sprintf("power: a power cut now is safe for the slots: slot %s starts next, slot %s is the fallback", t, k)
	case p.Disabled():
		return fmt.Sprintf("power: a power cut now is safe for the slots: slot %s starts (slot %s is not bootable while it is written)", k, t)
	}
	return "power: nothing is written yet"
}

// plainReason is a device-mode reason (a RESULT or STOP text, or the
// flow's own Why) in plain words. The text itself is shown below it as
// the details.
func plainReason(raw string) string {
	// the gate's own plain notes (fwgen-r4: a boot chain that never booted
	// on a UC-P10, a file this release does not offer) are the screen's text
	for _, n := range []string{plan.NoteBootchainNotTested, plan.NoteNotOffered} {
		if strings.Contains(raw, n) {
			return n
		}
	}
	r := strings.ToLower(raw)
	for _, c := range []struct{ has, say string }{
		{"ended by the signal", "The installer stopped unexpectedly."},
		{"not recorded", "The installer stopped unexpectedly."},
		{"exit 3", "The slot state is not known."},
		{"the install ended (exit", "The installer stopped with an error (the details are below and in the log)."},
		{"the plan now has id", "The unit or the firmware changed after the check."},
		{"not the plan on the screen", "The unit or the firmware changed after the check."},
		{"not the one of the plan", "The slot state changed after the check (something else wrote to the unit)."},
		{"drive rule", "The drive P10-INSTALL went back to the computer, or changed, during the install."},
		{"drive is with the computer", "The drive P10-INSTALL is with the computer again."},
		{"key search", "The firmware key was not found. Copy the key file p10-keys.txt to the drive and try again."},
		{"key file", "The key file p10-keys.txt cannot be used."},
		{"not a uc-p10", "This unit is not a UC-P10."},
		{"is a card of type", "The disk is a memory card, not the unit's eMMC."},
		{"not the installer ram image", "This is not the installer image."},
		{"autoupdate", "Crestron auto-update is on, or it could not be read."},
		{"auto update", "Crestron auto-update is on, or it could not be read."},
		{"bootctl-binary", "A tool of the installer image is not the expected one: use a new copy of the image."},
		{"input/output error", "The eMMC reported a read or write error."},
		{"pass 1", "The firmware file is not a supported Crestron firmware, or it is damaged."},
		{"manifest", "The firmware file is not a supported Crestron firmware, or it is damaged."},
		{"not proven", "The slot that was not touched has changed since the check, so it cannot be made the slot that starts."},
		{"sha256 differs", "The slot that was not touched has changed since the check, so it cannot be made the slot that starts."},
		{"fua", "The eMMC of this unit cannot write the slot table safely here (no reliable write)."},
		{"write cache", "The eMMC of this unit cannot write the slot table safely here (no reliable write)."},
		{"keeper:", "No slot can be kept as the fallback: both slots must be checked."},
	} {
		if strings.Contains(r, c.has) {
			return c.say
		}
	}
	return "The installer found a problem (the details are below and in the log)."
}
