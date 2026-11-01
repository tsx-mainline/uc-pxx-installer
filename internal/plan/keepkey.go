package plan

// The keep-key step (KEY-KEEP.md, the owner's answers of 2026-10-31): when the
// install would remove the last copy of the firmware key on the eMMC, the key
// record goes into two sectors of backup BEFORE the disable (a failure there
// is S0). The plan never holds the key or the record bytes: only the record's
// sha256 and the class of each sector before the write.

import (
	"fmt"
	"strings"

	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/keykeep"
)

// KeyKeepInput is what the device mode read for the keep-key step.
type KeyKeepInput struct {
	Gate         keykeep.Gate     // the read-only checks (checks.json); closed: never a step
	RecordSHA256 string           // the record of the key in use
	Classes      [2]keykeep.Class // the two record sectors now, for that record
	Kept         bool             // a record of the key in use is in at least one sector (for the log; the classes decide)
	Copies       []string         // the slots whose system holds the key in use
}

// KeyKeepSector is one record sector of the plan.
type KeyKeepSector struct {
	LBA   int64         `json:"lba"`
	Class keykeep.Class `json:"before"`
}

// KeyKeepInfo is the keep-key part of the plan (no key, no record bytes).
type KeyKeepInfo struct {
	State        string          `json:"state"` // kept, not-needed, gated, not-possible, keep
	Why          string          `json:"why"`
	Gate         keykeep.Gate    `json:"gate"`
	Copies       []string        `json:"system_copies"`
	RecordSHA256 string          `json:"record_sha256"`
	Sectors      []KeyKeepSector `json:"sectors"`
}

// KeepKeyStep writes the record to its two sectors of backup.
type KeepKeyStep struct {
	Partition    string           `json:"partition"`
	GPTIndex     int              `json:"gpt_index"`
	StartLBA     int64            `json:"part_start_lba"`
	PartSectors  int64            `json:"part_sectors"`
	LBAs         [2]int64         `json:"lbas"`
	RecordSHA256 string           `json:"record_sha256"`
	Before       [2]keykeep.Class `json:"before"`
}

// CheckKeepKeyRange is the rule of every keep-key write: exactly one of the
// two record sectors of the backup partition, nothing else.
func CheckKeepKeyRange(t *gpt.Table, lba int64) error {
	e, ok := t.Find(keykeep.Partition)
	if !ok {
		return fmt.Errorf("no partition %s", keykeep.Partition)
	}
	l, err := keykeep.LBAs(e.Start, e.Sectors())
	if err != nil {
		return err
	}
	if lba != l[0] && lba != l[1] {
		return fmt.Errorf("LBA %d is not a key-record sector of %s (%d, %d)", lba, keykeep.Partition, l[0], l[1])
	}
	if lba < ProtectedEnd || lba > t.LastUsable || lba == ABLBA {
		return fmt.Errorf("LBA %d is outside the writable area", lba)
	}
	return nil
}

// keepKey decides the keep-key step of a plan (steps are the plan's steps,
// from the disable on). It returns the steps with the keep-key step first
// (renumbered) when one is needed, and the plan note when the key is not kept.
func keepKey(p *Plan, in Input, t *gpt.Table, c *checker, steps []Step) []Step {
	kk := in.KeyKeep
	if kk == nil {
		return steps
	}
	info := &KeyKeepInfo{Gate: kk.Gate, Copies: append([]string{}, kk.Copies...), RecordSHA256: kk.RecordSHA256}
	p.KeyKeep = info
	e, ok := t.Find(keykeep.Partition)
	lbas, err := keykeep.LBAs(e.Start, e.Sectors())
	if !ok || err != nil || len(kk.RecordSHA256) != 64 {
		info.State, info.Why = "not-possible", fmt.Sprintf("no backup partition with room for the record (%v)", err)
		p.Notes = append(p.Notes, "The key is not kept on the unit: "+info.Why+".")
		return steps
	}
	for i := range lbas {
		info.Sectors = append(info.Sectors, KeyKeepSector{LBA: lbas[i], Class: kk.Classes[i]})
	}
	// a copy survives the plan when it is in a system that the plan does not
	// write: another slot, or the target when its system is skipped
	var survives []string
	for _, s := range kk.Copies {
		written := false
		for _, st := range steps {
			if st.Op == "write" && st.Write != nil && st.Write.Partition == "system_"+s && s == p.Target {
				written = true
			}
		}
		if !written {
			survives = append(survives, "system_"+s)
		}
	}
	same := 0
	for _, c := range kk.Classes {
		if c == keykeep.Same {
			same++
		}
	}
	other := kk.Classes[0]
	if other == keykeep.Same {
		other = kk.Classes[1]
	}
	switch {
	case same == 2:
		info.State, info.Why = "kept", "the key is kept on the unit already (backup, both copies)"
	case same == 1 && !other.Writable():
		info.State, info.Why = "kept", fmt.Sprintf("the key is kept on the unit in one copy; the other sector holds %s and is not written", other)
	case len(survives) > 0:
		info.State, info.Why = "not-needed", strings.Join(survives, " and ")+" keeps the key"
	case !kk.Gate.Open:
		info.State, info.Why = "gated", kk.Gate.Why
		p.Notes = append(p.Notes, "The key is not kept on the unit ("+info.Why+"). If the install stops during the system write, the next install needs the key file.")
	case !kk.Classes[0].Writable() || !kk.Classes[1].Writable():
		info.State = "not-possible"
		info.Why = fmt.Sprintf("the record sectors hold other data (%s, %s): nothing is written there", kk.Classes[0], kk.Classes[1])
		p.Notes = append(p.Notes, "The key is not kept on the unit: "+info.Why+". If the install stops during the system write, the next install needs the key file.")
	default:
		info.State, info.Why = "keep", "the install removes the last copy of the key on the eMMC: it is kept in backup before the first write"
	}
	if info.State != "keep" {
		c.add("keep-key", true, "%s: %s", info.State, info.Why)
		return steps
	}
	for _, l := range lbas {
		if err := CheckKeepKeyRange(t, l); err != nil {
			c.add("keep-key", false, "%v", err)
			return steps
		}
	}
	c.add("keep-key", true, "keep: backup LBA %d and %d (record sha256 %s…), before the disable", lbas[0], lbas[1], kk.RecordSHA256[:16])
	st := Step{N: 1, Op: "keep-key", Why: "keep the firmware key on the unit before the first write: a stop later needs no key file",
		KeepKey: &KeepKeyStep{Partition: e.Name, GPTIndex: e.Index, StartLBA: e.Start, PartSectors: e.Sectors(), LBAs: lbas,
			RecordSHA256: kk.RecordSHA256, Before: kk.Classes}}
	out := []Step{st}
	for _, s := range steps {
		s.N++
		out = append(out, s)
	}
	return out
}
