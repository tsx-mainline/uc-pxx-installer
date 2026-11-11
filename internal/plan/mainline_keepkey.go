package plan

// The keep-key step of a mainline install (MAINLINE-INT-2026-11-10.md task 2;
// MAINLINE-STEP2-DESIGN.md Q-S6, review N2). The official trigger (keepKey,
// KEY-KEEP.md §4.1) adds the step only when the plan removes the last copy of
// the key on the eMMC. A mainline step 1 never does that (stock system_a or
// userdebug system_b keeps a copy), but step 2 then overwrites the last
// Android system and refuses unless the key record is in backup (P6). So the
// mainline rule is stricter: the key must be kept by the end of step 1.
//
//   - "kept": a record of the key in use is in both sectors: no step (one
//     copy with foreign data in the other sector refuses: step 2 needs both);
//   - "keep": the gate (checks.json) is open and both sectors are writable
//     (empty, this record, or a torn write of it): the keep-key step, first,
//     before every A/B write (keep-key -> [R(K) ->] disable -> write -> sync
//     -> verify -> arm);
//   - anything else refuses the plan: without the record a later step 2
//     refuses on every unit (fails closed), so step 1 does not start.
//
// The key in use comes from the stash of the first drive phase (package
// mlstash): the 1.0.0.94 userdebug firmware proved it (the key decrypts the
// container and passes pass 1). The plan holds only the record's sha256 and
// the class of each sector, never the key.

import (
	"fmt"
	"io"

	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/keykeep"
	"uc-pxx-installer/internal/mainline"
)

// MainlineKeyInput is the keep-key input of a mainline plan: the classes of
// the two record sectors now for the record of the key in use, and where the
// key was proved.
type MainlineKeyInput struct {
	KeyKeepInput
	// Proof names the firmware that the key decrypts (the stash: the
	// firmware's name and sha256) and the stash index sha256.
	Proof string
	// Pin is the release's pin of the record's sha256 (owner O3:
	// mainline.Gate.KeyRecordPin, the 1.0.0.94 userdebug key's record):
	// when it is set, the record must have exactly this sha256, whether it
	// comes from the stash or is the one kept on the unit. "" (a test gate):
	// no pin check.
	Pin string
}

// ReadMainlineKey makes the keep-key input of a mainline plan from the disk
// (read only: the two record sectors of backup) and the record of the key in
// use (rec, from the stash; never kept here). gate is the keep-key gate
// (keykeep.TheGate() on the unit), proof names the firmware that proved the
// key. A read error gives the class Unread (never written).
func ReadMainlineKey(r io.ReaderAt, size int64, rec []byte, gate keykeep.Gate, proof string) *MainlineKeyInput {
	in := &MainlineKeyInput{KeyKeepInput: KeyKeepInput{Gate: gate, RecordSHA256: keykeep.SHA256(rec)}, Proof: proof}
	in.Classes = [2]keykeep.Class{keykeep.Unread, keykeep.Unread}
	k, kerr := keykeep.Decode(rec)
	t, err := gpt.Parse(r, size)
	if err != nil || kerr != nil {
		return in
	}
	e, ok := t.Find(keykeep.Partition)
	if !ok {
		return in
	}
	lbas, err := keykeep.LBAs(e.Start, e.Sectors())
	if err != nil {
		return in
	}
	for i, l := range lbas {
		sec := make([]byte, gpt.Sector)
		if _, err := r.ReadAt(sec, l*gpt.Sector); err != nil {
			continue
		}
		in.Classes[i] = keykeep.Classify(sec, rec)
		if got, derr := keykeep.Decode(sec); derr == nil && got == k {
			in.Kept = true
		}
	}
	return in
}

// pinnedKey is the keep-key input with the gate's pin (review M-3): the
// release's gate pins the record by construction, whatever the caller set;
// only a gate without a pin (a test gate) keeps the caller's Pin.
func pinnedKey(kk *MainlineKeyInput, g *mainline.Gate) *MainlineKeyInput {
	gp := g.KeyRecordPin()
	if kk == nil || gp == "" {
		return kk
	}
	c := *kk
	c.Pin = gp
	return &c
}

// mainlineKeepKey decides the keep-key part of a mainline plan and returns the
// steps with the keep-key step first when it is needed. ok is false when the
// plan must be refused.
func mainlineKeepKey(p *Plan, kk *MainlineKeyInput, t *gpt.Table, c *checker, steps []Step) ([]Step, bool) {
	if kk == nil {
		c.add("key-keep", false, "the firmware key must be kept on the unit by this install (step 2 refuses without the record, KEY-KEEP.md): no key from the first drive phase (put the 1.0.0.94 userdebug .bin on the UC-PXX-INST drive first)")
		return steps, false
	}
	info := &KeyKeepInfo{Gate: kk.Gate, Copies: append([]string{}, kk.Copies...), RecordSHA256: kk.RecordSHA256}
	p.KeyKeep = info
	p.KeySource = "the stash of the first drive phase: " + kk.Proof
	if kk.Pin != "" {
		// owner O3: the record is the reviewed key's, by the release's pin;
		// any other record (or an empty pin of a wrong length) refuses
		if len(kk.Pin) != 64 || kk.RecordSHA256 != kk.Pin {
			info.State = "not-possible"
			info.Why = fmt.Sprintf("the key record (sha256 %s) is not the record of the reviewed 1.0.0.94 userdebug key that this release pins (sha256 %s)",
				kk.RecordSHA256, kk.Pin)
			c.add("key-pin", false, "%s", info.Why)
			return steps, false
		}
		c.add("key-pin", true, "the key record is the reviewed 1.0.0.94 userdebug key's: its sha256 is the release's pin %s…", kk.Pin[:16])
		p.KeySource += "; the record's sha256 is the release's pin"
	}
	e, ok := t.Find(keykeep.Partition)
	lbas, err := keykeep.LBAs(e.Start, e.Sectors())
	if !ok || err != nil || len(kk.RecordSHA256) != 64 {
		info.State, info.Why = "not-possible", fmt.Sprintf("no backup partition with room for the record (%v)", err)
		c.add("key-keep", false, "%s", info.Why)
		return steps, false
	}
	for i := range lbas {
		info.Sectors = append(info.Sectors, KeyKeepSector{LBA: lbas[i], Class: kk.Classes[i]})
	}
	same := 0
	for _, cl := range kk.Classes {
		if cl == keykeep.Same {
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
		// step 2 needs both copies, equal (ReadKeyGate): one copy is not enough (review T2 MINOR-1)
		info.State = "not-possible"
		info.Why = fmt.Sprintf("the key is kept in one copy only; the other sector holds %s and is never written, and step 2 needs both copies", other)
	case !kk.Gate.Open:
		info.State, info.Why = "gated", kk.Gate.Why
	case !kk.Classes[0].Writable() || !kk.Classes[1].Writable():
		info.State = "not-possible"
		info.Why = fmt.Sprintf("the record sectors hold other data (%s, %s): nothing is written there", kk.Classes[0], kk.Classes[1])
	default:
		info.State, info.Why = "keep", "a mainline install keeps the key before its first write: step 2 overwrites the last Android system"
	}
	switch info.State {
	case "kept":
		c.add("key-keep", true, "%s: %s", info.State, info.Why)
		return steps, true
	case "keep":
	default:
		c.add("key-keep", false, "the firmware key must be kept on the unit by this install (step 2 refuses without the record): %s: %s", info.State, info.Why)
		return steps, false
	}
	for _, l := range lbas {
		if err := CheckKeepKeyRange(t, l); err != nil {
			c.add("key-keep", false, "%v", err)
			return steps, false
		}
	}
	c.add("key-keep", true, "keep: backup LBA %d and %d (record sha256 %s…), the first step, before every A/B write", lbas[0], lbas[1],
		kk.RecordSHA256[:16])
	st := Step{N: 1, Op: "keep-key", Why: "keep the firmware key on the unit before the first write: step 2 overwrites the last Android system and needs the record",
		KeepKey: &KeepKeyStep{Partition: e.Name, GPTIndex: e.Index, StartLBA: e.Start, PartSectors: e.Sectors(), LBAs: lbas,
			RecordSHA256: kk.RecordSHA256, Before: kk.Classes}}
	out := []Step{st}
	for _, s := range steps {
		s.N++
		out = append(out, s)
	}
	return out, true
}

// MainlineKeepKeyNeverWritten is the never-written line of backup in a
// mainline plan.
const MainlineKeepKeyNeverWritten = "backup, except the two key-record sectors (only in a plan with the keep-key step)"
