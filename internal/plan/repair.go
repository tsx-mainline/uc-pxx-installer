package plan

import (
	"fmt"

	"uc-pxx-installer/internal/avbab"
)

// The A/B repair of the hold (DEVMODE-R4.md §2). An install that ends with the
// A/B state not known leaves the unit on the hold screen. The repair brings the
// sector back to one fixed state in which only the keeper boots:
//
//	R(K): magic "\0AB0", version 1.0, the keeper K successful {15,0,1}, the
//	      target {0,0,0}, the reserved bytes zero, the big-endian CRC; bytes
//	      32..511 as they are.
//
// uc-pxx-bootctl-inst `repair --keeper K --expect-sector HEX` writes it; this
// code only predicts it and says when it is needed, so the executor can compare
// the tool's dry run and the read-back with its own prediction (as for the
// install's disable and arm).

// RepairClass is what the repair finds in the sector.
type RepairClass string

const (
	// RepairUnreadable: the sector cannot be read (an I/O error). Never a write.
	RepairUnreadable RepairClass = "unreadable"
	// RepairKeeperOK: the sector is valid and the keeper is successful. No write.
	RepairKeeperOK RepairClass = "keeper-successful"
	// RepairKeeperNext: the sector is valid and libavb boots the keeper next,
	// but the keeper is only trying (not successful). The repair writes R(K)
	// after the keeper proof (the owner's Q1, DEVMODE-R4.md §11): a keeper
	// that never marks itself would run out of tries.
	RepairKeeperNext RepairClass = "keeper-boots-next"
	// RepairInvalid: bad magic, version, reserved bytes or CRC, an illegal slot,
	// or no bootable slot. The repair writes R(K).
	RepairInvalid RepairClass = "invalid"
	// RepairTargetNext: valid, but the target boots next and the keeper is not
	// successful (not bootable, or only trying). The repair writes R(K).
	RepairTargetNext RepairClass = "target-boots-next"
)

// NeedsWrite reports a class that the repair writes R(K) for.
func (c RepairClass) NeedsWrite() bool {
	return c == RepairInvalid || c == RepairTargetNext || c == RepairKeeperNext
}

// Retryable reports a read-back after a failed write that may get the one
// more write: it has no state that boots the keeper (not valid, or the target
// boots next), so a second write cannot lose a boot path to the keeper. A
// keeper that boots next (trying) is not retried.
func (c RepairClass) Retryable() bool { return c == RepairInvalid || c == RepairTargetNext }

// Safe reports the class in which the keeper boots, now or as the
// successful fallback: valid, the keeper successful and so bootable (the
// owner's Q3: any such sector is safe).
func (c RepairClass) Safe() bool { return c == RepairKeeperOK }

// ClassifyRepair decodes a sector for the repair with the installer's own
// decoder (package avbab). k is the keeper (0 = a, 1 = b). why explains the
// class.
func ClassifyRepair(sector []byte, readErr error, k int) (c RepairClass, why string) {
	if readErr != nil {
		return RepairUnreadable, "the sector cannot be read: " + readErr.Error()
	}
	if len(sector) != 512 {
		return RepairUnreadable, fmt.Sprintf("the sector has %d bytes", len(sector))
	}
	a, _ := avbab.Parse(sector)
	if err := a.Validate(); err != nil {
		return RepairInvalid, err.Error()
	}
	ks := a.Slots[k]
	switch {
	case ks.Keeper():
		return RepairKeeperOK, fmt.Sprintf("valid; the keeper %s is successful %v", avbab.SlotName(k), ks)
	case a.Pick() == k:
		return RepairKeeperNext, fmt.Sprintf("valid; libavb boots the keeper %s next %v", avbab.SlotName(k), ks)
	}
	return RepairTargetNext, fmt.Sprintf("valid, but slot %s boots next and the keeper %s is not successful %v",
		avbab.SlotName(1-k), avbab.SlotName(k), ks)
}

// PredictRepair is the 512-byte sector that `uc-pxx-bootctl-inst repair
// --keeper K` writes over cur: R(K) in bytes 0..31, cur in bytes 32..511. It is
// built byte by byte here, not taken from the tool.
func PredictRepair(cur []byte, k int) ([]byte, error) {
	if len(cur) != 512 || k < 0 || k > 1 {
		return nil, fmt.Errorf("PredictRepair: sector of %d bytes, keeper %d", len(cur), k)
	}
	next := append([]byte(nil), cur...)
	var raw [32]byte
	raw[1], raw[2], raw[3] = 'A', 'B', '0'
	raw[4] = 1
	raw[8+4*k], raw[9+4*k], raw[10+4*k] = 15, 0, 1
	raw = withCRC(raw)
	copy(next, raw[:])
	// the model's own checks of R(K)
	a, _ := avbab.Parse(next)
	if a.Validate() != nil || !a.Slots[k].Keeper() || a.Slots[1-k] != (avbab.Slot{}) || a.Pick() != k {
		return nil, fmt.Errorf("PredictRepair: R(%s) is not keeper-only bootable", avbab.SlotName(k))
	}
	return next, nil
}

// ErrSwitchRefused is a switch that the rule does not allow.
var ErrSwitchRefused = fmt.Errorf("switch refused")

// PredictSwitch is what `uc-pxx-bootctl-inst switch --to T` writes over cur
// (DEVMODE-R4.md §4): the sector must be valid, T successful, the other slot
// O bootable. T gets priority 15, O priority min(p, 14); nothing else
// changes. It returns nil (and no error) when T boots next already with a
// strictly higher priority: nothing to do.
func PredictSwitch(cur []byte, to int) ([]byte, error) {
	if len(cur) != 512 || to < 0 || to > 1 {
		return nil, fmt.Errorf("%w: sector of %d bytes, slot %d", ErrSwitchRefused, len(cur), to)
	}
	a, _ := avbab.Parse(cur)
	if err := a.Validate(); err != nil {
		return nil, fmt.Errorf("%w: the A/B sector is not valid: %v", ErrSwitchRefused, err)
	}
	t, o := a.Slots[to], a.Slots[1-to]
	if !t.Keeper() {
		return nil, fmt.Errorf("%w: slot %s is not successful %v: switch only to a slot that booted and was marked", ErrSwitchRefused, avbab.SlotName(to), t)
	}
	if !o.Bootable() {
		return nil, fmt.Errorf("%w: slot %s is not bootable %v: it would not be a fallback", ErrSwitchRefused, avbab.SlotName(1-to), o)
	}
	if a.Pick() == to && t.Priority > o.Priority {
		return nil, nil
	}
	var raw [32]byte
	copy(raw[:], cur)
	raw[8+4*to] = 15
	if raw[8+4*(1-to)] > 14 {
		raw[8+4*(1-to)] = 14
	}
	raw = withCRC(raw)
	next := append([]byte(nil), cur...)
	copy(next, raw[:])
	n, _ := avbab.Parse(next)
	if n.Validate() != nil || n.Pick() != to || !n.Slots[to].Keeper() || !n.Slots[1-to].Bootable() {
		return nil, fmt.Errorf("PredictSwitch: the new state is not T next with a bootable fallback")
	}
	return next, nil
}
