package plan

import (
	"fmt"
	"io"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/manifest"
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

// PredictSwitchKnown is the prediction of the switch to a slot T that is not
// tested yet (mainline-rc-10 I-2; uc-pxx-bootctl-inst r3 switch --untested):
// T trying {prio >= 1, tries >= 1, succ 0}, the other slot O bootable. The
// write is PredictSwitch's: T priority 15, O min(p, 14), the big-endian CRC,
// nothing else; T stays trying (its tries and successful byte are kept), so
// libavb counts its tries down and boots O when they run out. nil, nil: T
// boots next already. The caller proves that T (and O when it is not
// successful) holds exactly one known firmware's boot chain (KnownChain).
func PredictSwitchKnown(cur []byte, to int) ([]byte, error) {
	if len(cur) != 512 || to < 0 || to > 1 {
		return nil, fmt.Errorf("%w: sector of %d bytes, slot %d", ErrSwitchRefused, len(cur), to)
	}
	a, _ := avbab.Parse(cur)
	if err := a.Validate(); err != nil {
		return nil, fmt.Errorf("%w: the A/B sector is not valid: %v", ErrSwitchRefused, err)
	}
	t, o := a.Slots[to], a.Slots[1-to]
	if !(t.Priority >= 1 && t.Tries >= 1 && t.Successful == 0) {
		return nil, fmt.Errorf("%w: slot %s is not trying %v: an untested switch goes only to a slot with tries left (never one that U-Boot gave up on)", ErrSwitchRefused, avbab.SlotName(to), t)
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
	nt := n.Slots[to]
	if n.Validate() != nil || n.Pick() != to || nt.Tries != t.Tries || nt.Successful != 0 || !nt.Bootable() || !n.Slots[1-to].Bootable() {
		return nil, fmt.Errorf("PredictSwitchKnown: the new state is not T next, still trying, with a bootable fallback")
	}
	return next, nil
}

// KnownChain is the one known firmware whose uboot, trust, dtbo, vbmeta and
// boot images slot holds at the start of its partitions (ms: the
// installer's manifests), or nil and why not: the check of I-2's switch to a
// slot that is not tested yet (the direct planner's keeper-firmware read).
// The match must be exactly one manifest, and that manifest must be one the
// installer would install on this board with a boot chain that booted on a
// unit when it is the target (SafeChain; RC-10 review I-2 MAJOR-1: a
// firmware the manifests mark "refuse", a no-go boot resource or a test build
// is never "known" here). target: the slot that is to start next (its boot
// chain must have booted on a unit); false: the trying fallback.
func KnownChain(r io.ReaderAt, t *gpt.Table, slot string, ms []*manifest.Manifest, target bool) (*manifest.Manifest, string) {
	m, why := keeperFirmware(r, t, slot, ms)
	if m == nil {
		return nil, why
	}
	if bad := SafeChain(m, target); bad != "" {
		return nil, fmt.Sprintf("it is %s, which %s", m.Name(), bad)
	}
	return m, why
}

// SafeChain says why a known firmware is not one whose boot chain a switch
// may start or keep as the fallback ("" when it may): its status must be
// install, its boot resource not no-go, and it is no test build; for the
// target also its boot chain booted on a unit (bootchain_status booted).
func SafeChain(m *manifest.Manifest, target bool) string {
	switch {
	case m.Status != manifest.StatusInstall:
		return fmt.Sprintf("the installer's manifests mark %q (%s)", m.Status, m.RefuseReason)
	case m.BootChain.ResourceStatus == manifest.ResourceNoGo:
		return "has a boot resource that must never be installed (no-go)"
	case target && m.BootChain.BootchainStatus != manifest.BootchainBooted:
		return fmt.Sprintf("has a boot chain that did not boot on a unit yet (%s)", m.BootChain.BootchainStatus)
	case m.TestBuild:
		return "is a Crestron test build"
	}
	return ""
}
