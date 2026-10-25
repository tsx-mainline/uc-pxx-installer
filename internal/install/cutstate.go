package install

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/plan"
)

// CutState is what CheckCutState found.
type CutState struct {
	AB      plan.ABState
	Boots   string // "keeper" or "target"
	Target  string // "as before", "disabled" or "armed"
	Checked bool   // the target is bootable and holds the whole payload (hashed)
}

// CheckCutState is the power-cut rule. It looks only at the bytes that a
// power cut would leave on the disk (r), decodes the A/B sector with
// package avbab (not with bootctl and not with the executor), and checks:
//
//   - the sector is valid: magic, version, big-endian CRC, legal slots;
//   - the keeper is successful, and only its priority may differ from the
//     start (15 -> 14);
//   - bytes 32..511 of the sector are unchanged;
//   - the target is as before, disabled {0,0,0}, or armed {15,7,0};
//   - a bootable target holds none of the payload or all of it (every
//     write range hashed here, from r): a half-written target is never
//     selectable, not even as the fallback;
//   - libavb boots the keeper, or the target only when the target is armed
//     and holds the whole payload.
//
// start is the A/B sector before the install.
func CheckCutState(r io.ReaderAt, p *plan.Plan, start []byte) (CutState, error) {
	var cs CutState
	sec := make([]byte, gpt.Sector)
	if _, err := r.ReadAt(sec, plan.ABLBA*gpt.Sector); err != nil {
		return cs, err
	}
	var raw [32]byte
	copy(raw[:], sec)
	cs.AB = plan.State(raw)
	a, _ := avbab.Parse(sec)
	if err := a.Validate(); err != nil {
		return cs, fmt.Errorf("A/B sector not valid: %w", err)
	}
	if !bytes.Equal(sec[32:], start[32:]) {
		return cs, fmt.Errorf("bytes 32..511 of the A/B sector changed")
	}
	if !bytes.Equal(sec[:8], start[:8]) || !bytes.Equal(sec[16:28], start[16:28]) {
		return cs, fmt.Errorf("A/B bytes outside the slots changed")
	}
	k := 0
	if p.Keeper == "b" {
		k = 1
	}
	t := 1 - k
	s0, _ := avbab.Parse(start)
	ks, ks0 := a.Slots[k], s0.Slots[k]
	if !ks.Keeper() {
		return cs, fmt.Errorf("the keeper %s is not successful: %v", p.Keeper, ks)
	}
	if ks.Tries != ks0.Tries || ks.Successful != ks0.Successful || ks.Reserved != ks0.Reserved ||
		!(ks.Priority == ks0.Priority || ks0.Priority == 15 && ks.Priority == 14) {
		return cs, fmt.Errorf("the keeper changed: %v, was %v", ks, ks0)
	}
	ts := a.Slots[t]
	switch {
	case ts == s0.Slots[t]:
		cs.Target = "as before"
	case ts == avbab.Slot{}:
		cs.Target = "disabled"
	case ts == avbab.Slot{Priority: 15, Tries: 7}:
		cs.Target = "armed"
	default:
		return cs, fmt.Errorf("the target is in an unexpected state %v", ts)
	}
	if ts == s0.Slots[t] && ts == (avbab.Slot{Priority: 15, Tries: 7}) {
		cs.Target = "as before (armed)"
	}
	// A bootable target must hold either nothing of this install or all of
	// it: a half-written target must never be selectable, not even as the
	// fallback (design §5.3, "disable while writing").
	if ts.Bootable() {
		allOld, allNew := true, true
		for _, st := range p.Steps {
			if st.Write == nil {
				continue
			}
			w := st.Write
			h := sha256.New()
			if _, err := io.Copy(h, io.NewSectionReader(r, w.StartLBA*gpt.Sector+w.Offset, w.Length)); err != nil {
				return cs, err
			}
			got := hex.EncodeToString(h.Sum(nil))
			allOld = allOld && got == w.BeforeSHA256
			allNew = allNew && got == w.SHA256
		}
		if !allOld && !allNew {
			return cs, fmt.Errorf("the target %v is bootable while its partitions are partly written", ts)
		}
		if cs.Target == "armed" && !allNew {
			return cs, fmt.Errorf("the target is armed but does not hold the payload")
		}
		cs.Checked = allNew
	}
	switch a.Pick() {
	case k:
		cs.Boots = "keeper"
	case t:
		cs.Boots = "target"
		if bytes.Equal(sec, start) {
			// The sector is exactly as before the install: the target was
			// already the next boot. Nothing of this install is armed.
			cs.Boots = "target (as before the install)"
			return cs, nil
		}
		if cs.Target != "armed" || !cs.Checked {
			return cs, fmt.Errorf("the target boots next, but it was not armed by this install with the whole payload (%s)", cs.Target)
		}
	default:
		return cs, fmt.Errorf("no slot boots")
	}
	return cs, nil
}
