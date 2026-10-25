// Package avbab decodes the A/B metadata (AvbABData) of the UC-P10: the first
// 32 bytes of eMMC LBA 49156 (misc partition start 49152 + 4).
//
//	0..3   magic "\0AB0"      4 version_major (1)   5 version_minor   6..7 reserved
//	8..11  slot A {priority, tries_remaining, successful_boot, reserved}
//	12..15 slot B {same}
//	16..27 reserved           28..31 crc32 of bytes 0..27, BIG-endian
//
// This package only reads. The one program that writes the sector is
// uc-pxx-bootctl (repository uc-pxx-linux). The installer decodes the sector
// again with this code after each bootctl write, so two independent
// implementations must agree before the installer offers a reboot.
package avbab

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
)

// LBA is the eMMC sector that holds the A/B metadata.
const LBA = 49156

// Slot is one slot entry.
type Slot struct {
	Priority, Tries, Successful, Reserved byte
}

// Legal: libavb can hold only successful {tries 0, succ 1}, trying
// {tries > 0, succ 0} or unbootable {tries 0, succ 0}. Successful with
// tries > 0 is the state that bricked a unit.
func (s Slot) Legal() bool {
	if s.Priority > 15 || s.Tries > 7 || s.Reserved != 0 {
		return false
	}
	if s.Successful == 1 {
		return s.Tries == 0
	}
	return s.Successful == 0
}

// Bootable is libavb slot_is_bootable().
func (s Slot) Bootable() bool {
	return s.Priority > 0 && (s.Successful == 1 || s.Tries > 0)
}

// Keeper: a slot that can be the fallback while the other slot is written.
func (s Slot) Keeper() bool {
	return s.Successful == 1 && s.Tries == 0 && s.Priority >= 1
}

// State is a short word for the screen.
func (s Slot) State() string {
	switch {
	case !s.Legal():
		return "ILLEGAL"
	case !s.Bootable():
		return "unbootable"
	case s.Successful == 1:
		return "successful"
	default:
		return "trying"
	}
}

func (s Slot) String() string {
	return fmt.Sprintf("{prio %d tries %d succ %d} %s", s.Priority, s.Tries, s.Successful, s.State())
}

// AB is a decoded sector.
type AB struct {
	Raw         [32]byte
	Slots       [2]Slot
	CRCStored   uint32
	CRCComputed uint32
}

// Parse decodes the first 32 bytes of a sector. It does not validate.
func Parse(sector []byte) (AB, error) {
	var a AB
	if len(sector) < 32 {
		return a, errors.New("sector shorter than 32 bytes")
	}
	copy(a.Raw[:], sector[:32])
	for i := range a.Slots {
		b := a.Raw[8+4*i:]
		a.Slots[i] = Slot{b[0], b[1], b[2], b[3]}
	}
	a.CRCStored = binary.BigEndian.Uint32(a.Raw[28:])
	a.CRCComputed = crc32.ChecksumIEEE(a.Raw[:28])
	return a, nil
}

// Validate applies the same rules as uc-pxx-bootctl: magic, version, reserved
// bytes, big-endian CRC, both slots legal, at least one slot bootable.
func (a AB) Validate() error {
	r := a.Raw
	if r[0] != 0 || r[1] != 'A' || r[2] != 'B' || r[3] != '0' {
		return errors.New("bad magic")
	}
	if r[4] != 1 {
		return fmt.Errorf("unsupported version_major %d", r[4])
	}
	if r[6] != 0 || r[7] != 0 {
		return errors.New("reserved bytes 6..7 not zero")
	}
	for i := 16; i < 28; i++ {
		if r[i] != 0 {
			return errors.New("reserved bytes 16..27 not zero")
		}
	}
	if a.CRCStored != a.CRCComputed {
		if binary.LittleEndian.Uint32(r[28:]) == a.CRCComputed {
			return errors.New("crc32 stored little-endian (must be big-endian)")
		}
		return errors.New("crc32 mismatch")
	}
	for i, s := range a.Slots {
		if !s.Legal() {
			return fmt.Errorf("slot %c in illegal state %v", "AB"[i], s)
		}
	}
	if !a.Slots[0].Bootable() && !a.Slots[1].Bootable() {
		return errors.New("no bootable slot")
	}
	return nil
}

// Pick is the libavb choice: the bootable slot with the higher priority,
// slot A on a tie, -1 if none. The installer never relies on the tie rule:
// the miniloader's own choice on a tie is not proven.
func (a AB) Pick() int {
	ba, bb := a.Slots[0].Bootable(), a.Slots[1].Bootable()
	switch {
	case ba && bb:
		if a.Slots[1].Priority > a.Slots[0].Priority {
			return 1
		}
		return 0
	case ba:
		return 0
	case bb:
		return 1
	}
	return -1
}

// Tie reports two bootable slots with the same priority.
func (a AB) Tie() bool {
	return a.Slots[0].Bootable() && a.Slots[1].Bootable() && a.Slots[0].Priority == a.Slots[1].Priority
}

// SlotName is "a" or "b".
func SlotName(i int) string {
	if i == 0 {
		return "a"
	}
	if i == 1 {
		return "b"
	}
	return "-"
}
