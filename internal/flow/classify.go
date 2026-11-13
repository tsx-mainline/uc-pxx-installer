package flow

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/plan"
)

// End is how the install child ended, as its supervisor recorded it.
type End struct {
	Exited bool   // ended by exit (Code is valid)
	Code   int    // the exit status
	Signal string // the signal that ended it, when not Exited
	Lost   bool   // no record of the end (the UI was started again and found none)
}

func (e End) String() string {
	switch {
	case e.Lost:
		return "the end of the install was not recorded"
	case e.Exited:
		return fmt.Sprintf("exit %d", e.Code)
	}
	return "ended by the signal " + e.Signal
}

// The A/B states of a plan that a stop can leave (DEVICE-MODE-REVIEW.md §8):
// S0 = as before the install, D1 = the target disabled, D2 = the target
// armed (trying) and the keeper the fallback.
const (
	StateS0      = "S0"
	StateD1      = "D1"
	StateD2      = "D2"
	StateUnknown = "unknown"
)

// Outcome is what the flow shows after the install child ended.
type Outcome struct {
	Screen Screen // ScreenDone, ScreenStopped or ScreenHold
	State  string // StateS0, StateD1, StateD2 or StateUnknown
	Raw32  string // hex of the first 32 bytes of the A/B sector, "" if unreadable
	Decode string // the sector decoded (not trusted when State is unknown)
	Why    string // why this screen (for the log and the screen)
	End    End
	// LockStop: the lock-state write of this boot stopped (its record says
	// a change around the write, or a security backup that neither verified
	// nor restored, or a start marker with no record): the runner reads it
	// after the install child (flowio, flow review MINOR-B). The screen is
	// then the lock-state hold: the security partition may not be as before.
	LockStop string `json:",omitempty"`
	// LockDone: the lock-state write of this boot verified (lock_state 0x01
	// read back): a stop after it says so (flow re-look NIT-R3).
	LockDone bool `json:",omitempty"`
}

// isBootctl reports a step that runs uc-pxx-bootctl(-inst) with arg0 as its
// first argument (for example "disable", "arm-trying" or "repair").
func isBootctl(s plan.Step, arg0 string) bool {
	return s.Op == "bootctl" && s.Bootctl != nil && len(s.Bootctl.Args) > 0 && s.Bootctl.Args[0] == arg0
}

// planStates returns S0, D1 and D2 of a plan (32 bytes each): S0 is the sector
// before the install, D2 the final sector (the target armed and booting next,
// the keeper the successful fallback), and D1 the intermediate in which the
// target is disabled and only the keeper boots.
//
// The disable is normally the plan's first A/B step. A keeper-reestablish plan
// (KEEPER-REESTABLISH.md) prepends one repair (R(K)) step before it: R(K) makes
// the keeper successful {15,0,1} and the target {0,0,0}, so the disable that
// follows is a no-op and its ab_after is the R(K) state. D1 is therefore the
// disable step's ab_after in both shapes (the plain disabled state, or the R(K)
// state when R(K) is present). Only one keep-key step (KEY-KEEP.md: two sectors
// of backup, never the A/B sector) may come before either.
func planStates(p *plan.Plan) (s0, d1, d2 [32]byte, err error) {
	if s0, err = p.AB.Now.Raw(); err != nil {
		return
	}
	if d2, err = p.AB.Final.Raw(); err != nil {
		return
	}
	i := 0
	for i < len(p.Steps) && p.Steps[i].Op == "keep-key" {
		i++
	}
	if i > 1 {
		err = fmt.Errorf("the plan has more than one keep-key step before the first A/B step")
		return
	}
	// A keeper-reestablish plan starts its A/B steps with one repair (R(K))
	// step; skip it so the disable follows in both plan shapes.
	if i < len(p.Steps) && isBootctl(p.Steps[i], "repair") {
		i++
	}
	if i >= len(p.Steps) || !isBootctl(p.Steps[i], "disable") {
		err = fmt.Errorf("the plan does not start with the disable step (optionally after one keep-key step and/or one keeper-reestablish repair step)")
		return
	}
	d1, err = p.Steps[i].Bootctl.After.Raw()
	return
}

// StateOf names a sector read after the install against the plan: S0, D1 or
// D2 when the first 32 bytes are exactly that state, the sector is valid,
// and bytes 32..511 are those of before (the sector the flow read before it
// started the install, which must be the plan's sector). Anything else is
// unknown.
func StateOf(p *plan.Plan, before, after []byte, readErr error) (string, string) {
	if p == nil {
		return StateUnknown, "no plan"
	}
	if readErr != nil {
		return StateUnknown, "the A/B sector cannot be read: " + readErr.Error()
	}
	if len(before) != 512 || len(after) != 512 {
		return StateUnknown, "the A/B sector is not 512 bytes"
	}
	sum := sha256.Sum256(before)
	if hex.EncodeToString(sum[:]) != p.AB.SectorSHA256 {
		return StateUnknown, "the A/B sector before the install is not the one of the plan"
	}
	if !bytes.Equal(before[32:], after[32:]) {
		return StateUnknown, "bytes 32..511 of the A/B sector changed"
	}
	a, _ := avbab.Parse(after)
	if err := a.Validate(); err != nil {
		return StateUnknown, "the A/B sector is not valid: " + err.Error()
	}
	s0, d1, d2, err := planStates(p)
	if err != nil {
		return StateUnknown, err.Error()
	}
	var raw [32]byte
	copy(raw[:], after)
	switch raw {
	case d2:
		return StateD2, "the target is armed and boots next; the keeper is the fallback"
	case d1:
		if d1 == s0 {
			return StateS0, "as before the install (the target was already disabled)"
		}
		return StateD1, "the target is disabled; the keeper boots"
	case s0:
		return StateS0, "as before the install"
	}
	return StateUnknown, "the A/B sector is none of the plan's states"
}

// Classify chooses the screen after the install child ended
// (INSTALLER-END-USER-PLAN.md §2.5, with the rule of review F8):
//
//   - exit 0 and the sector is D2: Done. Exit 0 with any other sector: Hold
//     (the child and the sector disagree).
//   - exit 3 (the child says the A/B state is not known): Hold, always. The
//     hold screen can read the sector again (U1) and say whether it reads as
//     a known state now, but it never leaves the hold by itself.
//   - every other end (exit 1, exit 2, a signal, a Go panic, a lost record):
//     the sector is S0, D1 or D2 of the plan with bytes 32..511 unchanged:
//     Stopped with that state; anything else: Hold.
func Classify(p *plan.Plan, before, after []byte, readErr error, e End) Outcome {
	st, why := StateOf(p, before, after, readErr)
	o := Outcome{State: st, End: e}
	if readErr == nil && len(after) >= 32 {
		var raw [32]byte
		copy(raw[:], after)
		o.Raw32 = hex.EncodeToString(raw[:])
		o.Decode = plan.State(raw).String()
	}
	switch {
	case e.Exited && e.Code == 0 && st == StateD2:
		o.Screen, o.Why = ScreenDone, "the install ended with exit 0 and the A/B sector is the plan's final state"
	case e.Exited && e.Code == 0:
		o.Screen, o.Why = ScreenHold, "the install ended with exit 0, but the A/B sector is not the plan's final state: "+why
	case e.Exited && e.Code == 3:
		o.Screen, o.Why = ScreenHold, "the install ended with exit 3: the installer did not know the A/B state"
	case st == StateUnknown:
		o.Screen, o.Why = ScreenHold, fmt.Sprintf("the install ended (%s) and the A/B state is not known: %s", e, why)
	default:
		o.Screen, o.Why = ScreenStopped, fmt.Sprintf("the install ended (%s); the A/B sector reads as %s: %s", e, st, why)
	}
	return o
}

// Recheck is the "read the A/B sector again" action of the hold screen (U1).
// It never changes the screen: it only says what the sector reads as now.
func Recheck(p *plan.Plan, before, after []byte, readErr error) (string, string) {
	st, why := StateOf(p, before, after, readErr)
	return st, strings.TrimSpace(why)
}
