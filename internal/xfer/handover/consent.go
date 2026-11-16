package handover

// The step-2 consent of the end-user path (MAINLINE-RC10-2026-11-15 §6.10;
// DESIGN-R23 §4): no payload; the page's COMMIT {auto, plan_id} for the
// clone plan that the unit shows. The receiver (uid 990) writes consent.json
// into its folder; the root step-2 service reads it with ReadConsent and
// takes it only for its own plan id. The record only says "the person chose
// on the page: continue by itself" (auto true) or "the panel's hold decides"
// (auto false): what step 2 runs is the plan that the root service made.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
)

// ConsentRecord is the file of a consent receiver.
const ConsentRecord = "consent.json"

// RecordKindConsent is its kind.
const RecordKindConsent = "consent"

// The states of a consent receiver (HELLO/STATUS "state").
const (
	StateWaitingConsent  = "waiting-consent"
	StateConsentComplete = "consent-complete"
)

// PlanIDForm is the form of a plan id on the link: the first 12 hex digits.
var PlanIDForm = regexp.MustCompile(`^[0-9a-f]{12}$`)

// Consent is consent.json.
type Consent struct {
	Version int    `json:"version"`
	Kind    string `json:"kind"`
	PlanID  string `json:"plan_id"`
	Auto    bool   `json:"auto"`
	Session int    `json:"session"`
	Time    int64  `json:"time"`
}

// ReadConsent reads consent.json of the receiver's folder dir: a regular
// file of the receiver's user uid (not a link, at most MaxStateFile bytes),
// strictly parsed, of the kind consent and of the plan id planID. No record:
// os.ErrNotExist.
func ReadConsent(dir string, uid int, planID string) (*Consent, error) {
	p := filepath.Join(dir, ConsentRecord)
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.Mode().IsRegular() || !ok || int(st.Uid) != uid {
		return nil, fmt.Errorf("%s: not a regular file of the receiver's user", p)
	}
	b, err := ReadSmall(p, MaxStateFile)
	if err != nil {
		return nil, err
	}
	var c Consent
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %v", p, err)
	}
	switch {
	case c.Version != RecordVersion || c.Kind != RecordKindConsent:
		return nil, fmt.Errorf("%s: version %d kind %q", p, c.Version, c.Kind)
	case !PlanIDForm.MatchString(planID) || c.PlanID != planID:
		return nil, errors.New("the consent is for plan " + c.PlanID + ", not " + planID)
	}
	return &c, nil
}
