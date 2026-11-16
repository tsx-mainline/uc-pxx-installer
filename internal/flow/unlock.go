package flow

// The unlock status on the Status screen (mainline-rc-10, I-1; owner R7):
// the in-RAM lock_state reader's one read of this boot, run by the runner as
// a read-only child right after the start reads (`uc-pxx-inst lockstate`).
// Plain words on the screen and for the page; the hex value and the status's
// own heuristic (the old Bootloader row) go to the flow log only.

import "fmt"

// The plain words of the unlock status (the page's info "unlock" too).
const (
	UnlockReading  = "reading"
	UnlockUnlocked = "unlocked"
	UnlockLocked   = "locked"
	UnlockUnread   = "could not read"
	UnlockChanged  = "changed"
)

// UnlockOf is the plain word of a read: the value the client printed
// ("0x01", "0x00", "" for none), and whether the reader saw a change.
func UnlockOf(value string, changed bool) string {
	switch {
	case changed:
		return UnlockChanged
	case value == "0x01":
		return UnlockUnlocked
	case value == "0x00":
		return UnlockLocked
	}
	return UnlockUnread
}

// SetUnlock gives the unlock status. A change seen by the reader is the
// hold: the A/B state is not known; nothing more is written in this boot.
func (m *Model) SetUnlock(word, why string) {
	m.unlock = word
	if word == UnlockChanged && !m.everHeld {
		m.MainlineHeld("the lock-state read at the start of the installer: " + why)
	}
}

// Unlock is the unlock status ("" before the read started).
func (m *Model) Unlock() string { return m.unlock }

// unlockRow is the Status screen's row in place of the old Bootloader row.
func (m *Model) unlockRow() (int, string) {
	w := m.unlock
	lvl := levelNormal
	switch w {
	case "":
		w = UnlockReading
	case UnlockReading:
		w = "reading …"
	case UnlockUnlocked:
		lvl = levelGood
	case UnlockChanged:
		w, lvl = "changed during the read: keep the unit powered", levelBad
	case UnlockUnread:
		lvl = levelWarn
	}
	if w == UnlockReading {
		w = "reading …"
	}
	return lvl, fmt.Sprintf("%-30s %s", "Unlock status", w)
}
