package flowio

// The lock-state write's record, read by the runner after an install child
// (flow review MINOR-B; mainline-rc-7). The device mode's in-RAM lock-state
// writer (internal/devmode/lockwrite.go) writes its record of this boot to
// LockWriteRecord, and its start marker to LockWriteStarted before the
// write. The runner only reads them, by the device mode's own rule
// (devmode.LockWriteHeld; a test checks the two agree): the record cannot be
// read, or says a change around the write, or has a security backup that
// neither verified nor restored, or there is a start marker and no record:
// a STOP of the lock-state write. The flow then shows the lock-state hold
// (never "nothing on the unit was changed"), and the log bundle carries the
// record (lockwrite.json: shas, the read-back and the flags; no p16 byte, no
// key; the backup security.bak stays in the unit's RAM).

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"syscall"
)

const (
	// LockWriteRecord and LockWriteStarted are the device mode's files
	// (devmode.LockWriteRecord, devmode.LockWriteStarted).
	LockWriteRecord  = "/run/uc-pxx-inst/optee/lockwrite.json"
	LockWriteStarted = "/run/uc-pxx-inst/optee/write-started"
	maxLockRecord    = 1 << 20
)

// lockWriteState reads the record of this boot: stop is the reason of a
// lock-state STOP ("" = none); done is a verified write (lock_state 0x01 read
// back); raw is the record (nil: none).
func (r *Runner) lockWriteState() (stop string, done bool, raw []byte) {
	b, err := readSmall(r.path(LockWriteRecord))
	if err == nil {
		var rec struct {
			Verified     bool   `json:"verified"`
			Changed      bool   `json:"changed"`
			Restored     bool   `json:"restored"`
			ReadBack     string `json:"read_back"`
			BackupSHA256 string `json:"backup_sha256"`
		}
		switch {
		case json.Unmarshal(b, &rec) != nil:
			return "the record of the lock-state write cannot be read", false, b
		case rec.Changed:
			return "the A/B sector, the BCB, LBA 0..2047, the backup GPT or misc changed around the lock-state write", false, b
		case rec.BackupSHA256 != "" && !rec.Verified && !rec.Restored:
			return "the lock-state write did not reach 0x01 and the security backup did not restore", false, b
		}
		return "", rec.Verified && rec.ReadBack == "0x01", b
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "the record of the lock-state write cannot be read: " + err.Error(), false, nil
	}
	if _, err := os.Lstat(r.path(LockWriteStarted)); err == nil {
		return "a lock-state write started and left no record", false, nil
	}
	return "", false, nil
}

// readSmall reads a regular file of at most maxLockRecord bytes, without
// following a link.
func readSmall(p string) ([]byte, error) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > maxLockRecord {
		return nil, errors.New("not a small regular file")
	}
	b := make([]byte, st.Size())
	_, err = f.ReadAt(b, 0)
	return b, err
}
