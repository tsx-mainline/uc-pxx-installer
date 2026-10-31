package flow

// The texts of the screens. They are drafts: the project owner decides the
// final words (INSTALLER-END-USER-PLAN.md W8, "screen texts"). Rules for
// every text here:
//   - never "the unit turns off": poweroff in the RAM image restarts the
//     unit (first supervised install, 2026-10-28);
//   - "safe to disconnect the power" only for a known A/B state (S0, D1, D2);
//   - the hold text never says it is safe; only the re-read can;
//   - the slot whose contents are lost is always named.

import (
	"fmt"
	"strings"
)

// IssuesURL is the issue tracker of the uc-pxx-installer repository on
// GitHub: the contact of the hold screen and of README.txt in the log bundle
// (U1, owner 2026-10-30). The URL is not final yet. Fill it in here, and
// only here; "" leaves the URL out of the text.
const IssuesURL = ""

// Contact is the support text of the hold screen (after the row with the
// log folder). BundleContact is the same text for README.txt in the folder.
var (
	Contact       = contactText(IssuesURL, "that folder")
	BundleContact = contactText(IssuesURL, "this folder")
)

func contactText(url, folder string) string {
	where := "the uc-pxx-installer project on GitHub"
	if url != "" {
		where += " (" + url + ")"
	}
	return "Open an issue in " + where + " and attach " + folder + "."
}

// Measured times (INSTALLER-END-USER-PLAN.md §4.5, unit 2, 2026-10-28).
const (
	measuredCopySecs  = 135 // copy of the 973 MB firmware to the drive over USB
	measuredPlanSecs  = 256 // one plan computation (pass 1 and the keeper hashing)
	measuredStepsSecs = 338 // the install steps: 3275 writes, all read back
)

const (
	txtChecklistTitle = "Before the install"
	txtChecklistIntro = "Check each item, then choose Continue. Nothing is written before the last screen."

	txtFirmwareTitle = "Firmware"
	txtFirmwareCopy  = "Copy the official firmware .bin file to the drive P10-INSTALL on the computer (about 2 minutes)."
	txtFirmwareEject = "Then eject the drive on the computer. OK ejects it from here instead."
	txtFirmwareWait  = "Waiting for the eject ..."
	txtFirmwareRead  = "Reading the firmware file on the drive (read-only) ..."

	txtKeeperTitle = "Which slot do you keep?"
	txtKeeperIntro = "The installer writes one slot. The other slot is kept as it is and is the fallback."

	txtPlanningTitle = "Checking the firmware and the unit"
	txtPlanningIntro = "Nothing is written. The whole firmware and the slot you keep are read and checked."

	txtPlanTitle = "The install plan"

	txtProgressTitle = "Installing"
	txtProgressNote  = "Do not press Restart. The install cannot be stopped from here."

	txtDoneTitle    = "Install done"
	txtStoppedTitle = "Install stopped"
	txtHoldTitle    = "DO NOT DISCONNECT THE POWER. DO NOT RESTART."
	txtRefusedTitle = "Not installed"
	txtRefusedNote  = "Nothing was written to the unit."
)

// checklist items (§2.3). The ids go into the log.
type itemKind int

const (
	itemAutoUpdate itemKind = iota // hard gate: off goes, on refuses, unknown needs the confirmation (U2)
	itemConfirm                    // confirm with OK (U3)
	itemInfo                       // information only
)

type item struct {
	id, text, why string
	kind          itemKind
}

func checklistItems(au AutoUpdateView) []item {
	url := au.URL
	if url == "" {
		url = "not set or not read"
	}
	return []item{
		{id: "autoupdate", kind: itemAutoUpdate, text: "Automatic firmware update of Android is off",
			why: "Android must not install a firmware by itself into the slot that the installer writes"},
		{id: "url", kind: itemConfirm, text: "Update server URL: " + url,
			why: "the URL that Android would load a firmware from; change it to an address that does not answer"},
		{id: "xio", kind: itemConfirm, text: "XiO Cloud does not push a firmware to this unit",
			why: "a firmware push from the cloud could start an update"},
		{id: "internet", kind: itemInfo, text: "Optional: block the Internet for this unit at the router",
			why: "a second guard against an update"},
		{id: "power", kind: itemConfirm, text: "Stable power: no PoE switch cycle during the install",
			why: "a power cut is safe for the slots, but it can damage the eMMC while it writes"},
	}
}

func autoUpdateLine(au AutoUpdateView, unclearOK bool) (string, int) {
	switch au.State {
	case "off":
		return "off" + au.where(), levelGood
	case "on":
		return "ON" + au.where() + ": turn it off in the web UI of Android (Settings, Auto Update), then start the installer again", levelBad
	}
	if unclearOK {
		return "not clear" + au.where() + "; you confirmed that it is off in the web UI", levelWarn
	}
	return "not clear" + au.where() + ": check in the web UI of Android that Auto Update Firmware is off, then press OK to confirm", levelWarn
}

func (a AutoUpdateView) where() string {
	if a.Detail == "" {
		return ""
	}
	return " (" + a.Detail + ")"
}

func slotWord(s string) string { return strings.ToUpper(s) }

// stateSentence is the A/B state after an end, for the user.
func stateSentence(st string, keeper, target string) string {
	k, t := slotWord(keeper), slotWord(target)
	switch st {
	case StateS0:
		return fmt.Sprintf("Nothing on the unit changed. Slot %s starts as before. It is safe to disconnect the power.", k)
	case StateD1:
		return fmt.Sprintf("Slot %s is disabled and slot %s starts. It is safe to disconnect the power.", t, k)
	case StateD2:
		return fmt.Sprintf("Slot %s is complete and starts next. If it does not start, slot %s starts again after a few restarts.", t, k)
	}
	return "The state of the slots is not known."
}

// liveSafety is the live line of the progress screen.
func liveSafety(p *Progress, keeper, target string) (string, int) {
	k, t := slotWord(keeper), slotWord(target)
	switch {
	case p.InBootctl():
		return "Do not disconnect the power now: the boot slot record is being written.", levelBad
	case p.Armed():
		return fmt.Sprintf("Safe to disconnect the power: slot %s starts next, slot %s is the fallback.", t, k), levelGood
	case p.Disabled():
		return fmt.Sprintf("Safe to disconnect the power: slot %s starts (slot %s is disabled while it is written).", k, t), levelGood
	}
	return "Nothing is written yet. Safe to disconnect the power.", levelGood
}

func minutes(secs int) string {
	m := (secs + 30) / 60
	if m < 1 {
		m = 1
	}
	if m == 1 {
		return "about 1 minute"
	}
	return fmt.Sprintf("about %d minutes", m)
}
