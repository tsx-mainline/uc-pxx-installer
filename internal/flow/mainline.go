package flow

// The mainline install in the flow (MAINLINE-INSTALL-PLAN.md, phase 5): the
// drive UC-PXX-INST holds the folder uc-pxx-mainline (payload contract v1) in
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
//   - no repair on a mainline hold (uc-pxx-inst repair refuses mainline plans):
//     repairHold is false, so the hold shows the keep-powered path, and
//     "Check again" reads the A/B sector against the plan (Recheck).
//
// The two drive phases (owner O1/O8, 2026-11-10; MAINLINE-INT-2026-11-10.md
// §2, §4.4) and the direct path (§3):
//   - step 1, the stash: a drive whose top folder holds exactly one firmware
//     .bin and the folder uc-pxx-mainline without the bundle's files (an
//     empty folder: the sign of a mainline step 1) is not an official
//     install. The runner reads it (MainlineDrive.Stash) and the flow runs
//     the stash child in place of the plan child (MainlineStashArgs: the
//     device mode's first drive phase, `uc-pxx-inst plan --mainline-stash`; it
//     writes nothing to the eMMC, only the RAM stash of this boot). It is a
//     child of the plan's kind: the planning screen with its own texts, Back
//     stops it (SIGTERM), the restart is blocked while it runs. Its end
//     (StashEnded): the drive made new and empty (the .bin is not needed any
//     more) and given back, and the firmware screen asks for step 2 (the
//     swap view); a refusal or a cancel writes nothing;
//   - step 2: the bundle drive as before. The flow chooses the path by the
//     slot state that it read (directStart): the BCB-route reset (both slots
//     trying, A the higher priority) or the state after R(K) (A {15,0,1}, B
//     {0,0,0}) is the direct path (--direct: keeper A, the stock slot, by
//     R(K); target B, from the stash); every other state is the
//     userdebug-first path (keeper B, target A), whose planner refuses what
//     is not its start. The direct path without a stash of this boot is
//     refused at once (nothing to check: the plan needs the boot chain of
//     step 1). The userdebug-first path may run without step 1 when the key
//     record is kept on the unit already (its sha256 the release's pin, O3);
//     otherwise its plan refuses and the refused screen asks for step 1;
//   - the unlock (owner O4): the person's yes on the unlock screen is
//     --unlock-confirmed of both paths; nothing else opens the gate.

import (
	"strings"
	"time"

	"uc-pxx-installer/internal/avbab"
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
	// Stash: the drive of step 1 (the first drive phase): exactly one
	// firmware .bin in the top folder and the folder uc-pxx-mainline without
	// the bundle's files. FWName (quoted for the screen), FWSize and KeyFile
	// (the key file p10-keys.txt of the official flow, copied for --keys) are
	// of that .bin; Version and the rest are empty.
	Stash   bool
	FWName  string
	FWSize  int64
	KeyFile bool
	// StashDone: the stash of this boot is there (the runner found
	// /run/uc-pxx-inst/mlstash): for a step-1 drive the flow does not read the
	// .bin again; for a bundle drive the direct path may plan.
	StashDone bool
}

// StashResult is the end of the stash child (step 1).
type StashResult struct {
	OK       bool   // exit 0, and the stash of this boot is there
	Refusal  string // the RESULT text of a refusal
	Canceled bool
	End      End
}

// mlState is the mainline run in the model (nil: not a mainline drive).
type mlState struct {
	drive MainlineDrive
	// the unlock answer: yes (and from a terminal, after its warning)
	unlockOK, unlockTerminal bool
	// hashed: the planning screen saw the end of pass 1 in the child's lines
	hashed bool
	// step 1: the stash child runs (stashing); it ended with the stash and
	// the drive waits for the folder (swap; again: the stash was there
	// already); it was refused (stashFailed: the refused screen's words)
	stashing, swap, again, stashFailed bool
	// direct: the direct path (keeper A by R(K), target B), chosen by the
	// slot state at the read of the bundle (directStart)
	direct bool
	// replaceOK: on the plan screen of a direct plan, the first hold (or
	// Enter, then y) confirmed that what slot B holds now (the plan's
	// target-now check) is replaced (owner F1); for that plan id only
	replaceOK bool
	replaceID string
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
	if r.Stash {
		m.stashDrive(r)
		return
	}
	m.ml = &mlState{drive: r, direct: m.directStart()}
	if m.ml.direct && !r.StashDone {
		// the direct path takes the boot chain and the key from step 1:
		// without it there is nothing to check (the plan would refuse)
		m.refused, m.refusedDetail = txtMLStep1Missing, ""
		m.Drive = nil
		m.ask(Action{Kind: ActGiveBackDrive, Remake: true}) // both do not fit: the drive is made empty for step 1
		m.go2(ScreenRefused)
		return
	}
	m.Drive = &DriveResult{OK: true, Name: `"` + MainlineFolder + `"`, Size: r.Size, Ident: r.Ident}
	// the keeper is always B: the planner's keeper refusal is a refusal
	// here, never the keeper screen (PlanEnded)
	m.keeperAsked, m.Keeper, m.keeperWhy = true, "", ""
	m.go2(ScreenUnlock)
}

// stashDrive: the drive of step 1. With the stash of this boot there, the
// swap view at once (the .bin is not read twice); else the stash child, in
// place of the plan child (the runner gives MainlineStashArgs for it).
func (m *Model) stashDrive(r MainlineDrive) {
	m.ml = &mlState{drive: r}
	m.Drive = nil
	m.refused, m.refusedDetail = "", ""
	if r.StashDone {
		m.ml.swap, m.ml.again = true, true
		m.ask(Action{Kind: ActGiveBackDrive, Remake: true})
		return
	}
	m.ml.stashing = true
	m.Drive = &DriveResult{OK: true, Name: r.FWName, Size: r.FWSize, Ident: r.Ident, KeyFile: r.KeyFile}
	m.keeperAsked, m.Keeper, m.keeperWhy = true, "", ""
	m.startPlan() // the planning screen, ActStartPlan: the stash child
}

// StashEnded is the end of the stash child. It wrote nothing to the eMMC
// whatever its end. With the stash: the drive made new and empty, given
// back, and the firmware screen asks for the folder (step 2). A refusal: the
// refused screen, the drive back as it is. A cancel: the status screen.
func (m *Model) StashEnded(r StashResult) {
	m.running = ""
	m.Msg = ""
	m.Drive = nil
	if m.ml != nil {
		m.ml.stashing = false
	}
	switch {
	case r.Canceled:
		m.ml = nil
		if m.Ejected {
			m.ask(Action{Kind: ActGiveBackDrive})
		}
		m.go2(ScreenStatus)
		m.Msg = "Canceled. Nothing was written."
	case r.OK && m.ml != nil:
		m.ml.swap = true
		m.refused, m.refusedDetail = "", ""
		m.ask(Action{Kind: ActGiveBackDrive, Remake: true})
		m.go2(ScreenFirmware)
	default:
		raw := r.Refusal
		switch {
		case raw == "" && r.End.Lost:
			raw = "the end of step 1 was not recorded; there is no stash" // review N-4
		case raw == "":
			raw = "step 1 ended (" + r.End.String() + ") without the stash"
		}
		if m.ml == nil {
			m.ml = &mlState{}
		}
		m.ml.stashFailed = true
		m.refused, m.refusedDetail = stashPlain(raw), raw
		m.ask(Action{Kind: ActGiveBackDrive})
		m.go2(ScreenRefused)
	}
}

// MainlineStashing: the plan child that the flow asked for is the stash
// child of step 1 (MainlineStashArgs).
func (m *Model) MainlineStashing() bool { return m.ml != nil && m.ml.stashing && m.running == "plan" }

// MainlineDirect: the bundle is for the direct path (--direct).
func (m *Model) MainlineDirect() bool { return m.ml != nil && m.ml.direct && !m.ml.drive.Stash }

// directStart reports a slot state that starts the direct path: the
// BCB-route reset (both slots trying, slot A the higher priority, so slot A
// starts) or the state after R(K) (A {15,0,1}, B {0,0,0}). The direct
// planner checks the same (direct-start, reestablish) on its own read.
func (m *Model) directStart() bool {
	u := m.Unit
	if u == nil || !u.ABValid {
		return false
	}
	a, b := u.Slots[0].AB, u.Slots[1].AB
	trying := func(s avbab.Slot) bool { return s.Successful == 0 && s.Tries > 0 && s.Priority > 0 }
	switch {
	case trying(a) && trying(b) && a.Priority > b.Priority:
		return true
	case a == avbab.Slot{Priority: 15, Successful: 1} && b == avbab.Slot{}:
		return true
	}
	return false
}

// replaceAsk: the plan on the screen is a direct plan whose replacement of
// slot B was not confirmed yet (owner F1, 2026-11-11: show what slot B holds
// and confirm it before anything is written; the flow's confirm: the 3 s
// hold of OK, on a terminal Enter then y; never automatic).
func (m *Model) replaceAsk() bool { return m.directPlan() && !m.ReplaceConfirmed() }

// confirmReplace is the end of that hold: the plan screen then asks for the
// hold that starts the install. Nothing is written by it.
func (m *Model) confirmReplace() {
	if m.ml == nil {
		m.ml = &mlState{} // a UI started again on the plan screen
	}
	m.ml.replaceOK, m.ml.replaceID = true, m.Plan.ID
	m.okDown, m.okFor, m.confirm = time.Time{}, 0, 0
	m.Msg = txtReplaceConfirmed
}

// ReplaceConfirmed: the person confirmed the replacement of slot B for the
// direct plan on the screen (the install child's plan.ReplaceFlag).
func (m *Model) ReplaceConfirmed() bool {
	return m.ml != nil && m.ml.replaceOK && m.Plan != nil && m.ml.replaceID == m.Plan.ID
}

// targetNow is the plan's words for what slot B holds now (its target-now
// check), or "" when the plan has none.
func (m *Model) targetNow() string {
	if m.Plan == nil {
		return ""
	}
	for _, c := range m.Plan.Checks {
		if c.ID == plan.TargetNowCheck {
			t, _, _ := strings.Cut(c.Detail, " (the install needs")
			return t
		}
	}
	return ""
}

// mainlineKind reports a plan kind of the mainline install (either path).
func mainlineKind(k string) bool { return k == plan.MainlineKind || k == plan.MainlineDirectKind }

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
func (m *Model) mainlinePlan() bool { return m.Plan != nil && mainlineKind(m.Plan.Kind) }

// directPlan reports a plan of the direct path on the screen.
func (m *Model) directPlan() bool { return m.Plan != nil && m.Plan.Kind == plan.MainlineDirectKind }

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
// uc-pxx-inst chooses the mainline mode), the drive identity, the plan file, the
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

// MainlineDirectPlanArgs is MainlinePlanArgs with --direct: the direct
// path (keeper A by R(K), target B, the boot chain of the stash).
func MainlineDirectPlanArgs(p Paths, prereqs, panel, unlock bool) []string {
	return append(MainlinePlanArgs(p, prereqs, panel, unlock), "--direct")
}

// MainlineDirectInstallArgs is MainlineInstallArgs with --direct (the
// install child also takes the path from the plan file's kind).
func MainlineDirectInstallArgs(p Paths, prereqs, panel, unlock, replace bool) []string {
	a := append(MainlineInstallArgs(p, prereqs, panel, unlock), "--direct")
	if replace {
		a = append(a, plan.ReplaceFlag)
	}
	return a
}

// MainlineStashArgs is the command line of the stash child of step 1: the
// first drive phase of the device mode (`uc-pxx-inst plan --mainline-stash`, the
// same as `uc-pxx-inst mainline-stash`) with the drive and its identity, and the
// key file of the drive if it had one (the official key search of the
// device mode otherwise). It writes nothing to the eMMC: only the RAM stash
// of this boot. No confirmation flag: it plans nothing and writes nothing.
func MainlineStashArgs(p Paths) []string {
	a := []string{p.Exe, "plan", "--mainline-stash", "--device", "/dev/mmcblk0", "--fw-drive", p.Drive, "--fw-drive-expect", p.DriveIdent}
	if p.Keys != "" {
		a = append(a, "--keys", p.Keys)
	}
	return a
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

// MainlineHeld is the end of a mainline plan child with exit 3: the device
// mode's STOP before any write, the A/B state not known (the in-RAM
// lock_state reader saw security, the A/B sector or the BCB change around
// its read, or its record of this boot says so; MAINLINE-RC2-LS review B-1).
// It is the hold, as after an install's exit 3, with no plan of it (so no
// repair: "Check again" reads the sector), persisted (EverHeld): the restart,
// Install and Switch stay blocked for the rest of this RAM boot, also for the
// official userdebug install of this flow and in a UI started again.
func (m *Model) MainlineHeld(why string) {
	m.running, m.Msg = "", ""
	m.Plan = nil
	m.Outcome = &Outcome{Screen: ScreenHold, State: StateUnknown, Why: "the mainline plan check ended with exit 3 (the A/B state is not known): " + why}
	m.everHeld = true
	m.holdPlan, m.repair, m.repairTries, m.repairWhy, m.deadBy = "", repairNone, 0, "", ""
	m.recheck = txtReaderHeld
	m.Bundle = "the unit: the plan check wrote no log folder (its files are in /run/uc-pxx-inst on the unit)"
	m.go2(ScreenHold)
}
