package plan

// The key check of mainline step 2 (owner answer O3, 2026-11-10,
// MAINLINE-INT-2026-11-10.md §7): after step 2 no system of the unit holds
// the firmware key, so step 2 needs the kept record in both key-record
// sectors of backup, and the record must be the one of a firmware key that
// this release knows by the sha256 of its 512-byte record (package keykeep:
// the record is the key in plain, so only its sha256 is ever stored,
// compared or printed). Today that is only the key of 1.0.0.94 userdebug
// (c9766c0a…), the firmware of every mainline install.
//
// The pin list is a reviewed data change: a new entry needs an Opus review.
// Anything else (no record, one copy, two different records, a record that is
// not pinned) refuses.

import "fmt"

// KeyPin is the sha256 of the key record of one firmware's key.
type KeyPin struct {
	Firmware     string `json:"firmware"`
	RecordSHA256 string `json:"record_sha256"`
}

// The pin sets: the release's own list, or a list a devtest build gives for
// its made-up firmware.
const (
	KeyPinSetRelease = "release"
	KeyPinSetTest    = "test"
)

// releaseKeyPins are the key records this release accepts. 7143b925… is the
// record of the key of 1.0.0.94 userdebug (firmware sha256 c9766c0a…): the
// sha the T2 dry run computed from the real .bin (MAINLINE-INT-REVIEW-T2.md),
// and the record that unit 2's two sectors hold (the S5 install, R2 read of
// 2026-11-10; sha256 only).
var releaseKeyPins = []KeyPin{
	{Firmware: "1.0.0.94 userdebug (c9766c0a)", RecordSHA256: "7143b925e62e867ae6a428fafac17252d4e7b001c96767ee00c40f77f787b08e"},
}

// ReleaseKeyPins is a copy of the release's pin list.
func ReleaseKeyPins() []KeyPin { return append([]KeyPin(nil), releaseKeyPins...) }

// MatchKeyPin sets g.Pin when both copies hold the same record and its sha256
// is in pins (set names the list). It never looks at a byte of the record:
// ReadKeyGate keeps only its sha256.
func MatchKeyPin(g *KeyGate, pins []KeyPin, set string) {
	g.Pin, g.PinSet = "", set
	if g.Copies != 2 || !g.Equal || len(g.RecordSHA256) != 64 {
		return
	}
	for _, p := range pins {
		if len(p.RecordSHA256) == 64 && p.RecordSHA256 == g.RecordSHA256 {
			g.Pin = p.Firmware
			return
		}
	}
}

// KeyPinned is the check text of a key gate after MatchKeyPin.
func (g KeyGate) KeyPinned() (bool, string) {
	switch {
	case g.Copies != 2 || !g.Equal:
		return false, "the key is not kept in both copies (" + g.Why + ")"
	case g.Pin == "":
		return false, fmt.Sprintf("the kept record (sha256 %s) is not the record of a firmware key that this release knows (%s pins)", shortSHA(g.RecordSHA256), g.PinSet)
	}
	return true, fmt.Sprintf("the kept record (sha256 %s) is the record of the key of %s (%s pins)", shortSHA(g.RecordSHA256), g.Pin, g.PinSet)
}
