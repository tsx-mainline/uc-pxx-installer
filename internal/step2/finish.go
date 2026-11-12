//go:build install && step2

package step2

// The finish of step 2 (design §3 rows 11-12; review T4 MAJOR-1, MINOR-2,
// MINOR-4). It runs in the step-2 target T after T booted and marked itself:
//
//  1. the step-2 record (the plan of the start) is read from the keeper's
//     root file system system_K (package ext4, read only through the
//     partition node; T does not mount it);
//  2. every input of plan.ComputeStep2Finish is read live, just before the
//     decision: the A/B sector (fresh, the caches flushed), bootctl's status,
//     the four RKSS ranges, the keeper's static parts, the GPT layout, the
//     two key-record sectors with the pin, the running facts and the boot id;
//  3. K = b, or a switch made before: nothing to write (the read-only F1
//     compare is the whole finish);
//  4. K = a: `uc-pxx-bootctl-inst switch --to a --expect-sector <the sector
//     read in 2>` (the reviewed form; r2 refuses any other live sector), with
//     both locks held and the plan computed under them, then the fresh
//     read-back must be the predicted A{15,0,1} B{14,0,1} with bytes 32..511
//     unchanged.
//
// A supervised finish runs only with ExpectID, the id of the finish plan of
// a dry run that a fresh Opus subagent checked (rule 9): the plan computed
// under the locks must have that id (the id holds the sector and the boot
// id).

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/ext4"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/plan"
)

// ErrStop is a finish that found a change (an RKSS range, the keeper, the
// layout, the key, an A/B state step 2 cannot leave): keep the unit powered,
// do not restart, write nothing more, ask the owner (exit 4). ErrNotYet: the
// target has not marked itself yet (exit 1, ask again at the next boot).
var (
	ErrStop   = errors.New("STOP: keep the unit powered, do not restart, write nothing more, ask the owner")
	ErrNotYet = errors.New("not yet: the step-2 target has not marked itself")
)

// FinishResult is the end of a finish.
type FinishResult struct {
	Plan     *plan.FinishPlan
	Switched bool
}

// readFileExt4 reads one file of the step-2 state folder of slot k's root
// file system (system_k), read only.
func readFileExt4(d *device.Disk, k, name string) ([]byte, error) {
	r, size, closeFn, err := d.PartReader("system_" + k)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	fs, err := ext4.Open(r, size)
	if err != nil {
		return nil, fmt.Errorf("system_%s: %w", k, err)
	}
	return fs.ReadFile(path.Join(StateDir, name), maxRecord)
}

// finishInput reads the inputs of the finish that the planner does not read
// itself: the step-2 record from system_K, the A/B sector (fresh, the caches
// flushed) and bootctl's status. The planner reads the GPT, the RKSS ranges,
// the keeper's static parts and the key records through the view (O_DIRECT);
// close the view after the planner.
func (s *session) finishInput() (plan.FinishInput, *device.View, []byte, error) {
	T := s.ru.Slot
	K := other(T)
	var in plan.FinishInput
	if err := s.mounts(false); err != nil {
		return in, nil, nil, err
	}
	rr := s.o.RecordReader
	if rr == nil {
		rr = readFileExt4
	}
	b, err := rr(s.d, K, RecordFile)
	if err != nil {
		return in, nil, nil, refuse("the step-2 record on system_%s (%s/%s): %v", K, StateDir, RecordFile, err)
	}
	var s2 plan.OtherSlotPlan
	if err := json.Unmarshal(b, &s2); err != nil {
		return in, nil, nil, refuse("the step-2 record: %v", err)
	}
	if s2.Kind != plan.OtherSlotKind || !s2.VerifyID() {
		return in, nil, nil, refuse("the step-2 record is not a sealed other-slot plan")
	}
	sec, err := s.d.ReadABFresh()
	if err != nil {
		return in, nil, nil, refuse("the A/B sector: %v", err)
	}
	st, err := s.d.SlotBootctlStatus()
	if err != nil {
		return in, nil, nil, refuse("%v", err)
	}
	v := s.d.View()
	in = plan.FinishInput{Running: s.ru, Disk: v, DiskBytes: s.d.Bytes(), Sector: sec, Status: st, Step2: &s2}
	if s.o.KeyPins != nil {
		in.KeyPins, in.KeyPinSet = s.o.KeyPins, plan.KeyPinSetTest
	}
	return in, v, sec, nil
}

func (s *session) renderFinish(p *plan.FinishPlan) {
	s.say("finish of step-2 plan %s (running slot %s, boot id %s)", p.Step2ID, strings.ToUpper(p.Running.Slot), p.Running.BootID)
	for _, c := range p.Checks {
		mark := "ok  "
		if !c.OK {
			mark = "FAIL"
		}
		s.say("  %s %-16s %s", mark, c.ID, c.Detail)
	}
	if p.Switch != nil {
		s.say("  switch    uc-pxx-bootctl-inst switch --to a --expect-sector <the sector, sha256 %s>: %s -> %s", p.SectorSHA256,
			p.Switch.Before, p.Switch.After)
	}
	if p.Done {
		s.say("  done      the end state: slot A boots first, slot B successful; nothing to write")
	}
	s.say("finish plan id: %s", p.ID)
}

// Finish runs the finish. See the file comment.
func Finish(e *device.Env, o Options) (*FinishResult, error) {
	s, err := open(e, o)
	if err != nil {
		return nil, err
	}
	defer s.close()
	if o.Auto && s.o.Supervised {
		return nil, refuse("a supervised finish never runs by itself")
	}
	if !o.DryRun {
		unlock, err := s.live.lockAutoupdate()
		if err != nil {
			return nil, err
		}
		defer unlock()
		if err := s.d.Lock(); err != nil {
			return nil, refuse("the A/B lock: %v", err)
		}
		if err := s.live.quiet(); err != nil {
			return nil, err
		}
	}
	in, view, sec, err := s.finishInput()
	if err != nil {
		return nil, err
	}
	fp, ferr := plan.ComputeStep2Finish(in)
	view.Close()
	s.renderFinish(fp)
	if ferr != nil {
		if errors.Is(ferr, plan.ErrFinishStop) {
			// a STOP is recorded (on this slot's root) and shown; nothing is
			// written to the eMMC
			appendLine(filepath.Join(e.Path(StateDir), StateFile), "stop "+fp.Step2ID+" "+fp.Running.BootID+"\n")
			return &FinishResult{Plan: fp}, fmt.Errorf("%w: %v", ErrStop, ferr)
		}
		if errors.Is(ferr, plan.ErrFinishNotYet) {
			return &FinishResult{Plan: fp}, fmt.Errorf("%w: %v", ErrNotYet, ferr)
		}
		return &FinishResult{Plan: fp}, refuse("the finish: %v", ferr)
	}
	if o.JSONOut != "" {
		if err := writeJSON(o.JSONOut, fp, false); err != nil {
			return &FinishResult{Plan: fp}, err
		}
	}
	dir := e.Path(StateDir)
	if fp.Done {
		if !o.DryRun {
			if err := writeJSON(filepath.Join(dir, DoneFile), fp, true); err != nil {
				return &FinishResult{Plan: fp}, err
			}
		}
		return &FinishResult{Plan: fp}, nil
	}
	if o.DryRun {
		return &FinishResult{Plan: fp}, nil
	}
	switch {
	case s.o.Supervised && o.ExpectID == "":
		return &FinishResult{Plan: fp}, refuse("a supervised finish runs only with --expect-id: the id of the dry run that the rule-9 GO checked")
	case o.ExpectID != "" && o.ExpectID != fp.ID:
		return &FinishResult{Plan: fp}, refuse("the finish plan has id %s now, the reviewed dry run had %s: nothing written", fp.ID, o.ExpectID)
	case !o.Auto && o.ExpectID == "":
		// mainline-rc-4 review MAJOR-1: a normal build (the finish executor
		// of a step-2 target) never has the supervised marker, so a finish
		// by hand must always name the reviewed dry run; only the service's
		// automatic finish (o.Auto, checked below against the keeper's
		// start line) runs without an id
		return &FinishResult{Plan: fp}, refuse("a finish by hand runs only with --expect-id: the id of the dry run that the rule-9 GO checked (run `%s finish --dry-run` first)", finishCmd)
	case o.Auto:
		// the automatic switch only after an unsupervised, armed start of
		// this plan (the keeper's own start and arm lines), checked here
		// again, not only by the caller
		st, _ := s.readOtherState(other(s.ru.Slot))
		if _, armed, sup := stateOf(st, fp.Step2ID); !armed || sup != "false" {
			return &FinishResult{Plan: fp}, refuse("the automatic finish needs the keeper's start line supervised=false and its arm line of plan %s (armed %v, supervised %q): run the supervised finish (--dry-run, the rule-9 GO, --expect-id)", fp.Step2ID, armed, sup)
		}
	}
	if err := s.d.CanWriteABSlot(); err != nil {
		return &FinishResult{Plan: fp}, refuse("the switch cannot run: %v", err)
	}
	if err := writeJSON(filepath.Join(dir, "finish-"+fp.ID[:16]+".json"), fp, true); err != nil {
		return &FinishResult{Plan: fp}, refuse("the finish record: %v", err)
	}
	s.say("switch: uc-pxx-bootctl-inst %s …", strings.Join(fp.Switch.Args[:4], " "))
	run, err := s.d.SlotSwitch(fp.Switch, sec, o.FinishCut)
	if run != nil {
		s.say("switch: exit %d %s", run.RC, strings.TrimSpace(run.Stderr))
	}
	if err != nil {
		return &FinishResult{Plan: fp}, err
	}
	st, err := s.d.SlotBootctlStatus()
	if err != nil {
		return &FinishResult{Plan: fp, Switched: true}, fmt.Errorf("%w: the switch wrote the predicted sector, but uc-pxx-bootctl-inst status failed: %v", install.ErrABUnknown, err)
	}
	back, err := s.d.ReadABFresh()
	if err != nil {
		return &FinishResult{Plan: fp, Switched: true}, fmt.Errorf("%w: %v", install.ErrABUnknown, err)
	}
	if err := checkEnd(back, st); err != nil {
		return &FinishResult{Plan: fp, Switched: true}, fmt.Errorf("%w: %v", install.ErrABUnknown, err)
	}
	if err := writeJSON(filepath.Join(dir, DoneFile), fp, true); err != nil {
		return &FinishResult{Plan: fp, Switched: true}, fmt.Errorf("switched (A{15,0,1} B{14,0,1}), but the done record was not written: %v", err)
	}
	s.say("finish done: A{15,0,1} B{14,0,1}, slot A boots next, slot B successful. SWITCH-SAFE: yes")
	return &FinishResult{Plan: fp, Switched: true}, nil
}

// checkEnd: the end state A{15,0,1} B{14,0,1}, A next, and bootctl's own
// status of it agrees with the installer's decoder.
func checkEnd(sec []byte, st *plan.BootctlStatus) error {
	a, _ := avbab.Parse(sec)
	if a.Validate() != nil || a.Slots[0] != (avbab.Slot{Priority: 15, Successful: 1}) ||
		a.Slots[1] != (avbab.Slot{Priority: 14, Successful: 1}) || a.Tie() || a.Pick() != 0 {
		var raw [32]byte
		copy(raw[:], sec)
		return fmt.Errorf("after the switch the A/B state is %s, not A{15,0,1} B{14,0,1} with A next", plan.State(raw))
	}
	if st == nil {
		return fmt.Errorf("no bootctl status after the switch")
	}
	return st.Agrees(a)
}
