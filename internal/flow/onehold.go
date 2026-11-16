package flow

// One confirmation (mainline-rc-10, I-5; owner R15, approved 2026-11-15, and
// the owner's reversal of MAJOR-3 of 2026-11-12) and the auto-update check
// for the official path only (I-7; owner R24):
//
//   - no Install press: after the start reads the firmware screen (over USB:
//     the wait for the installer page) opens by itself (autoOpen); the slot
//     table must be readable; Back goes to Status as before;
//   - the auto-update check (on, or not readable: refused) runs when the goal
//     is known: for an official (userdebug) firmware, before its plan
//     (officialGate). The mainline install never runs Android again: no check
//     (the edge: a unit that never gets step 2 starts the Crestron firmware
//     in slot A again, whose updater may write slot B: a reinstall, never a
//     brick). The userdebug-first mainline path keeps its planner's check
//     (Android stays its keeper);
//   - the panel question ("Do you see this screen on the unit's panel?") is
//     not asked when the keys are the unit's own: the plan's 3 s hold of OK
//     on the unit's own key proves the same (the person looks at the unit's
//     screen), so the plan child gets --panel-confirmed as before and the
//     install starts only after that hold; a terminal keeps the question, and
//     a terminal's yes on a plan whose panel answer came from the unit's keys
//     asks it first (askPanelAgain);
//   - the direct path's lock-state screen is gone (it asked nothing: the
//     installer reads lock_state itself; I-1 reads it at the start); its
//     "go on" is recorded as given by the plan hold;
//   - ONE 3 s hold on the plan screen of a direct plan: it records the
//     replace confirmation and, on a locked unit, the agreement to the
//     lock-state write (the screen says "The bootloader is unlocked."), and
//     starts the install: the install child gets exactly the flags it got
//     after the three holds before (plan.ReplaceFlag, plan.LockWriteFlag,
//     LockWriteTerminalFlag from a terminal). A terminal's y counts only
//     PanelWarnWait after the screen was drawn (the rule of the warning);
//   - Done: the unit restarts by itself 10 s after Done (Back = stay here);
//     a terminal keeps the 3 s hold (a supervised session reads back first).
//     A hold or a stopped screen never restarts by itself.

import (
	"fmt"
	"time"
)

// AutoRestart is the time from Done to the restart on the unit's own keys.
const AutoRestart = 10 * time.Second

// oneHold is the I-5 state of the model.
type oneHold struct {
	opened      bool      // autoOpen ran in this UI
	panelByHold bool      // the panel answer is taken from the plan's hold on the unit's keys
	panelReturn bool      // the panel question was asked from the plan screen (a terminal's yes)
	restartAt   time.Time // Done: the restart time ("": not set yet)
	stay        bool      // Done: Back stopped the countdown
	// doneHere: Done came from an install that ended in this UI
	// (InstallEnded); a UI resumed on Done never restarts by itself (RC-10
	// review I-5 MINOR-3): it keeps the restart hold
	doneHere bool
}

func (m *Model) oh() *oneHold {
	if m.one == nil {
		m.one = &oneHold{}
	}
	return m.one
}

// autoOpen opens the firmware screen once after the first status read of a
// UI that is on the status screen with nothing running (no Install press).
func (m *Model) autoOpen() {
	if !m.AutoOpen {
		return
	}
	o := m.oh()
	if o.opened || m.Screen != ScreenStatus || m.running != "" || m.everHeld || m.Outcome != nil || m.Plan != nil {
		o.opened = true
		return
	}
	o.opened = true
	if m.Unit == nil || !m.Unit.ABValid {
		return // the status screen says why (the slot table)
	}
	m.openInstall()
}

// officialGate is why an official firmware is refused before its plan: the
// auto-update check (I-7: the official path only), or "".
func (m *Model) officialGate() string {
	switch {
	case m.Unit == nil:
		return txtNotRead
	case m.Unit.AutoUpdate.State == "on":
		return txtAutoUpdateOn
	case m.Unit.AutoUpdate.State != "off":
		return txtAutoUpdateUnclear
	}
	return ""
}

// panelRule: before a plan, the panel question for a terminal; on the
// unit's own keys the answer is taken from the plan's hold (true: go on to
// the plan now).
func (m *Model) panelRule() bool {
	if m.panelOK {
		return true
	}
	if m.Terminal {
		m.go2(ScreenPanel)
		return false
	}
	m.panelOK, m.panelTerminal = true, false
	m.oh().panelByHold = true
	return true
}

// PanelByHold: the panel answer was taken from the plan's 3 s hold on the
// unit's own keys (state.json, the flow log, the bundle).
func (m *Model) PanelByHold() bool { return m.one != nil && m.one.panelByHold && m.panelOK }

// askPanelAgain: a terminal confirms a plan whose panel answer came from
// the unit's keys: the panel question first; its yes goes back to the plan.
func (m *Model) askPanelAgain() {
	m.panelOK, m.oh().panelByHold, m.oh().panelReturn = false, false, true
	m.go2(ScreenPanel)
}

// panelYesReturn: the yes of the panel question; true when it goes back to
// the plan screen (the plan child had --panel-confirmed already).
func (m *Model) panelYesReturn() bool {
	if m.one == nil || !m.one.panelReturn || m.Plan == nil {
		return false
	}
	m.one.panelReturn = false
	m.go2(ScreenPlan)
	return true
}

// oneHoldStart is the plan screen's one hold of a direct plan: the replace
// confirmation and the lock-state agreement are recorded together.
func (m *Model) oneHoldStart() {
	m.confirmReplace()
	if m.planLockWrite() {
		m.confirmLockWrite()
	}
}

// doneTick: the restart countdown of Done (the unit's own keys only).
func (m *Model) doneTick(now time.Time) bool {
	if m.Screen != ScreenDone || m.Terminal || m.running != "" {
		return false
	}
	o := m.oh()
	if o.stay || !o.doneHere {
		return false
	}
	if o.restartAt.IsZero() {
		o.restartAt = now.Add(AutoRestart)
		return true
	}
	if !now.Before(o.restartAt) {
		o.stay = true // once
		m.fire(ActRestart)
	}
	return true
}

// doneStay: Back on Done stops the countdown (the screen stays).
func (m *Model) doneStay() bool {
	if m.Terminal || m.one == nil || m.one.stay || m.one.restartAt.IsZero() {
		return false
	}
	m.one.stay = true
	m.Msg = txtRestartStay
	return true
}

// restartText is the Done screen's restart line.
func (m *Model) restartText(now time.Time) (string, int) {
	if !m.Terminal && m.one != nil && !m.one.stay && !m.one.restartAt.IsZero() {
		left := m.one.restartAt.Sub(now)
		if left < 0 {
			left = 0
		}
		return fmt.Sprintf(txtRestartIn, HoldSeconds(left)), levelWarn
	}
	return m.holdText("restart", now)
}

// doneBack is the Back key's word on Done.
func (m *Model) doneBack() string {
	if m.Terminal || m.one == nil || m.one.stay || m.one.restartAt.IsZero() {
		return "Status"
	}
	return "Stay"
}

// PlanDetails are the lines that the plan screen of a direct plan showed
// before I-5: they go to the flow log (and so to the Log screen and the log
// bundle) when the plan screen opens.
func (m *Model) PlanDetails() []string {
	if !m.directPlan() {
		return nil
	}
	t := m.Plan.Target
	tn := m.targetNow()
	if tn == "" {
		tn = "slot B holds firmware that the plan does not name"
	}
	now0 := "unknown contents"
	if ti := slotIndex(t); m.Unit != nil && ti >= 0 {
		now0 = orQ(m.Unit.Slots[ti].Summary)
	}
	out := []string{m.mlDriveRow(), upperFirst(tn) + ".",
		fmt.Sprintf("Slot %s gets %s and the boot files of Android userdebug 1.0.0.94 (step 1).", slotWord(t), m.mlVersion()),
		fmt.Sprintf("Slot %s now (status): %s.", slotWord(t), now0), txtMLDirectKeep, txtMLGateDirect, txtMLMarkPlanD}
	if m.planLockWrite() {
		out = append(out, txtLockWarnTitle, txtLockWarnSet, txtLockWarnUndo)
	} else {
		out = append(out, txtLockUnlocked)
	}
	if k := m.sshKeyText(); k != "" {
		out = append(out, "SSH key for root: "+k+".")
	}
	if m.PanelByHold() {
		out = append(out, "The panel question is answered by the 3 s hold of OK on the unit's own key (I-5).")
	}
	return out
}

const (
	txtRestartIn   = "  Restarting in %d s. Back = stay here."
	txtRestartStay = "The unit stays here. Hold OK for 3 seconds to restart."
	// the plan screen of a direct plan (R15 plain words)
	txtOneTitle    = "Install mainline Linux"
	txtOneReplace  = "Slot B is replaced with %s."
	txtOneUnlock   = "The bootloader is unlocked."
	txtOneBackup   = "The Crestron firmware in slot A stays as the backup."
	txtOneKey      = "SSH key: %s"
	txtOneHold     = "Hold OK for 3 seconds to install. Back = Cancel: nothing is written."
	txtOneHoldTerm = "Press Enter, then y, to install. b = Cancel: nothing is written."
	txtOneDone     = "Slot B is installed. The unit restarts."
)
