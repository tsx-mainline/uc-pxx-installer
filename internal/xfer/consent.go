package xfer

import (
	"encoding/json"
	"fmt"

	"uc-pxx-installer/internal/xfer/handover"
)

// consent answers the COMMIT of a consent receiver: taken only with the
// unit's plan id, once; the record (consent.json) first, then the state.
// auto false is taken too and starts nothing: the panel's hold decides.
func (s *session) consent(auto bool, planID string) commitAnswer {
	r := s.r
	a := commitAnswer{Auto: auto}
	switch {
	case r.Complete():
		a.Reason = "the consent of this plan was given already"
		return a
	case planID != r.cfg.PlanID:
		a.Reason = fmt.Sprintf("plan_id %q is not the plan the unit shows (%s): read HELLO again", planID, r.cfg.PlanID)
		return a
	}
	rec := handover.Consent{Version: RecordVersion, Kind: handover.RecordKindConsent, PlanID: planID, Auto: auto, Session: s.n,
		Time: r.cfg.Now().Unix()}
	b, _ := json.MarshalIndent(rec, "", " ")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dead {
		a.Reason = "the session was ended"
		return a
	}
	if err := writeAtomic(r.cfg.Dir, handover.ConsentRecord, append(b, '\n')); err != nil {
		a.Reason = "record: " + err.Error()
		return a
	}
	r.mu.Lock()
	r.complete = true
	r.st = State{State: StateConsentComplete, Session: s.n, Detail: "consent of plan " + planID + ": the panel's hold decides"}
	if auto {
		r.st.Detail = "consent of plan " + planID + ": continue by itself (the panel's countdown, Back stops it)"
	}
	r.publish()
	r.mu.Unlock()
	r.cfg.Logf("COMMIT: session %d, consent of plan %s, auto %v", s.n, planID, auto)
	s.notifyCommitted = true
	a.OK = true
	return a
}
