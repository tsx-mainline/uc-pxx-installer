package flow

// The mainline install in the flow (MAINLINE-INSTALL-PLAN.md, phase 5): the
// drive P10-INSTALL holds the folder uc-pxx-mainline (payload contract v1) in
// place of a firmware .bin. Slot A gets mainline Linux, slot B keeps Android
// userdebug as the fallback (Q1). The flow adds one screen, the unlock
// confirmation (before the plan, Q4), and its own texts for the planning,
// plan, done, stopped and refused screens; everything else is the official
// flow unchanged (the progress, the hold, the restart, the switch, the log).
//
// What makes a run a mainline run:
//   - before the plan: the runner's drive read found the folder and gave it
//     to MainlineDriveRead (m.ml is set); every other drive read clears it
//     first (LeaveMainline);
//   - from the plan on: the plan's kind is plan.MainlineKind (the runner reads
//     a mainline plan file with its own id check and gives the model the
//     embedded Plan, whose Kind is MainlineKind). A UI that is started again
//     knows a mainline run only from that, so the end screens and the hold
//     decide by the plan.
//
// The differences from the official flow:
//   - the unlock screen comes after the drive read and before the panel
//     question and the plan; its yes is --unlock-confirmed of the children
//     (MainlinePlanArgs); its confirm protocol is the panel question's (the
//     panel's OK answers in one step; a terminal answers only with y, at
//     least PanelWarnWait after its warning was drawn);
//   - no keeper screen: the keeper is always B (keeperAsked is set, so a
//     keeper refusal of the planner is a refusal);
//   - no repair on a mainline hold (p10inst repair refuses mainline plans):
//     repairHold is false, so the hold shows the keep-powered path, and
//     "Check again" reads the A/B sector against the plan (Recheck).

import (
	"strings"
	"time"

	"uc-pxx-installer/internal/plan"
)

// MainlineFolder is the bundle folder in the top folder of the drive
// (mainline.Dir; the pure flow does not import package mainline, a test of
// the runner holds the two equal).
const MainlineFolder = "uc-pxx-mainline"

// The name of the unlock screen (the logs, screen.json): registered here, so
// the map of flow.go stays as it is.
func init() { screenNames[ScreenUnlock] = "unlock" }

// MainlineDrive is the runner's read of a drive whose top folder holds the
// folder uc-pxx-mainline: its manifest, read and checked strictly (the plan
// child checks the whole bundle, pass 1). Nothing of it is a secret.
type MainlineDrive struct {
	OK      bool
	Refusal string // why the drive is refused (also: a .bin next to the folder)
	Remake  bool   // the drive must be made again before it goes back (dirty or damaged)
	Ident   string // drivestate.Ident of the drive file at the read
	Size    int64  // the bytes of the boot template and the root file system
	// the manifest: the mainline version, the boot token, the kernel pin,
	// the boot chain that slot B must hold, its sha256
	Version, Token, Kernel   string
	ChainVersion, ChainBuild string
	ManifestSHA256           string
}

// mlState is the mainline run in the model (nil: not a mainline drive).
type mlState struct {
	drive MainlineDrive
	// the unlock answer: yes (and from a terminal, after its warning)
	unlockOK, unlockTerminal bool
	// hashed: the planning screen saw the end of pass 1 in the child's lines
	hashed bool
}

// MainlineDriveRead is the result of ActReadDrive for a mainline drive (in
// place of DriveRead): a refusal is shown on the firmware screen as a
// refused firmware file is; a bundle goes to the unlock screen.
func (m *Model) MainlineDriveRead(r MainlineDrive) {
	m.reading = false
	if m.Screen != ScreenFirmware {
		return
	}
	if !r.OK {
		m.ml = nil
		m.refused = r.Refusal
		m.Drive = nil
		m.ask(Action{Kind: ActGiveBackDrive, Remake: r.Remake})
		return
	}
	m.ml = &mlState{drive: r}
	m.Drive = &DriveResult{OK: true, Name: `"` + MainlineFolder + `"`, Size: r.Size, Ident: r.Ident}
	// the keeper is always B: the planner's keeper refusal is a refusal
	// here, never the keeper screen (PlanEnded)
	m.keeperAsked, m.Keeper, m.keeperWhy = true, "", ""
	m.go2(ScreenUnlock)
}

// LeaveMainline: the drive that was read is not a mainline drive (the runner
// says so before it gives the official result of the read).
func (m *Model) LeaveMainline() { m.ml = nil }

// Mainline reports a mainline drive (the plan child is the mainline one).
func (m *Model) Mainline() bool { return m.ml != nil }

// UnlockConfirmed: the user answered yes on the unlock screen for this drive
// (--unlock-confirmed).
func (m *Model) UnlockConfirmed() bool { return m.ml != nil && m.ml.unlockOK }

// UnlockFromTerminal: that yes came from a terminal, after its warning.
func (m *Model) UnlockFromTerminal() bool { return m.UnlockConfirmed() && m.ml.unlockTerminal }

// mainlinePlan reports a plan of the mainline kind on the screen.
func (m *Model) mainlinePlan() bool { return m.Plan != nil && m.Plan.Kind == plan.MainlineKind }

// keyUnlock: the panel's OK (Mic) answers yes in one step; a terminal answers
// yes only after its warning: Enter (or y) shows it, and a y answers only
// when the warning was drawn and PanelWarnWait has passed since (the rule of
// the panel question, keyPanel). Back (b) is No at any time: nothing was
// written, the drive goes back. After the yes: the panel question if it was
// not answered in this run, then the plan.
func (m *Model) keyUnlock(k Key, now time.Time) {
	switch k {
	case KeyOK, KeyYes:
		switch {
		case m.ml == nil:
			return
		case k == KeyYes && !m.Terminal:
			return // y is a terminal key
		case m.Terminal && !m.panelWarn:
			m.panelWarn, m.panelWarnSeen = true, time.Time{}
			return
		case m.Terminal && (k != KeyYes || m.panelWarnSeen.IsZero() || now.Sub(m.panelWarnSeen) < PanelWarnWait):
			return // Enter, or a y too early: the warning stays
		}
		m.ml.unlockOK, m.ml.unlockTerminal = true, m.Terminal
		m.panelWarn, m.panelWarnSeen = false, time.Time{}
		if !m.panelOK {
			m.go2(ScreenPanel)
			return
		}
		m.startPlan()
	case KeyBack:
		m.panelWarn, m.panelWarnSeen = false, time.Time{}
		if m.ml != nil {
			m.ml.unlockOK = false
		}
		m.refused, m.refusedDetail = txtUnlockNo, ""
		m.ask(Action{Kind: ActGiveBackDrive})
		m.go2(ScreenRefused)
	}
}

// bootloaderRow is the value of the status's Bootloader row ("unknown" when
// the status has none): the unlock screen says what the installer itself
// found (the C3 heuristic of the status).
func (m *Model) bootloaderRow() string {
	if m.Unit != nil {
		for _, l := range m.Unit.Lines {
			if strings.HasPrefix(l.Text, "Bootloader ") {
				if v := strings.TrimSpace(strings.TrimPrefix(l.Text, "Bootloader")); v != "" {
					return v
				}
			}
		}
	}
	return "unknown"
}

// MainlinePlanArgs is the command line of the mainline plan child: the
// device mode with the drive (its top folder holds uc-pxx-mainline, so
// p10inst chooses the mainline mode), the drive identity, the plan file, the
// confirmations. No --keys (a mainline install reads no firmware key) and no
// --keeper (the keeper is always B); --unlock-confirmed only after the yes
// of the unlock screen. It writes nothing to the eMMC (devmode.PlanMainline).
func MainlinePlanArgs(p Paths, prereqs, panel, unlock bool) []string {
	a := []string{p.Exe, "plan", "--device", "/dev/mmcblk0", "--fw-drive", p.Drive, "--fw-drive-expect", p.DriveIdent,
		"--json", p.PlanJSON}
	return append(a, mlCommon(prereqs, panel, unlock)...)
}

// MainlineInstallArgs is the command line of the mainline install child:
// the plan that was shown (--plan), the hold of OK (--yes), the log, and the
// confirmations of the plan child (the same plan id comes out).
func MainlineInstallArgs(p Paths, prereqs, panel, unlock bool) []string {
	a := []string{p.Exe, "install", "--device", "/dev/mmcblk0", "--fw-drive", p.Drive, "--fw-drive-expect", p.DriveIdent,
		"--plan", p.PlanJSON, "--yes", "--log", p.Log}
	return append(a, mlCommon(prereqs, panel, unlock)...)
}

func mlCommon(prereqs, panel, unlock bool) []string {
	var a []string
	if prereqs {
		a = append(a, "--prereqs-confirmed")
	}
	if panel {
		a = append(a, "--panel-confirmed")
	}
	if unlock {
		a = append(a, plan.UnlockFlag)
	}
	return a
}
