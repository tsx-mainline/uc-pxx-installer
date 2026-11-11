package xfer

import "uc-pxx-installer/internal/xfer/handover"

// The hand-over contract lives in internal/xfer/handover (no USB code), so
// that the root step-2 service can import it; these names keep the receiver's
// code short.
type (
	State     = handover.State
	Record    = handover.Record
	ReadyFile = handover.ReadyFile
	Ready     = handover.Ready
)

const (
	StateStarting   = handover.StateStarting
	StateWaiting    = handover.StateWaiting
	StateReceiving  = handover.StateReceiving
	StateComplete   = handover.StateComplete
	StateStatusOnly = handover.StateStatusOnly
	StateFile       = handover.StateFile
	ReadyDir        = handover.ReadyDir
	ReadyRecord     = handover.ReadyRecord
	RecordVersion   = handover.RecordVersion
	incomingStem    = handover.IncomingStem
	maxStateFile    = handover.MaxStateFile
)

func readSmall(p string, max int64) ([]byte, error) { return handover.ReadSmall(p, max) }
func writeAtomic(dir, name string, b []byte) error  { return handover.WriteAtomic(dir, name, b) }
func decodeStrict(b []byte, v any) error            { return handover.DecodeStrict(b, v) }
func pinned(pins []string, s string) bool           { return handover.Pinned(pins, s) }

// CheckReady is handover.CheckReady.
func CheckReady(dir string, uid int, pins []string) (*Ready, error) {
	return handover.CheckReady(dir, uid, pins)
}

// ReadState is handover.ReadState.
func ReadState(dir string) (*State, error) { return handover.ReadState(dir) }
