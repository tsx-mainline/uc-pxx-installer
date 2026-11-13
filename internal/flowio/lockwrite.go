package flowio

// The lock-state write's record, read by the runner after an install child
// (flow review MINOR-B; mainline-rc-7). The device mode's in-RAM lock-state
// writer (internal/devmode/lockwrite.go) writes its record of this boot to
// lockrec.RecordPath, and its start marker to lockrec.StartedPath before the
// write. The runner only reads them, by the device mode's own rule
// (lockrec.Held: one function for both, code review MINOR-C). A STOP of the
// lock-state write is the flow's lock-state hold (never "nothing on the unit
// was changed"); the log bundle carries the record (lockwrite.json: shas,
// flags and the read-back; no p16 byte, no key; the backup security.bak
// stays in the unit's RAM).

import (
	"os"

	"uc-pxx-installer/internal/lockrec"
)

const (
	// LockWriteRecord and LockWriteStarted are the device mode's files.
	LockWriteRecord  = lockrec.RecordPath
	LockWriteStarted = lockrec.StartedPath
)

// lockWriteState reads the record of this boot as the device mode does
// (devmode.LockWriteHeld): kind and why of a lock-state STOP ("" = none);
// done is a verified write (lock_state 0x01 read back); raw is the record
// (nil: none, or not readable).
func (r *Runner) lockWriteState() (kind, why string, done bool, raw []byte) {
	b, err := os.ReadFile(r.path(LockWriteRecord))
	_, serr := os.Stat(r.path(LockWriteStarted))
	kind, why = lockrec.Held(b, err, serr == nil)
	if err != nil {
		b = nil
	}
	return kind, why, err == nil && lockrec.Verified(b), b
}
