// Package lockrec is the record of the in-RAM lock-state write of one boot
// (internal/devmode lockwrite.go) and the one rule that reads it: whether a
// STOP of the lock-state write holds the boot (Held) and whether the write
// verified (Verified). The device mode and the flow's runner (flowio) both use
// it, so the two can never read the same files differently (mainline-rc-7
// code review MINOR-C). It only decodes bytes; the callers read the files.
package lockrec

import (
	"encoding/json"

	"uc-pxx-installer/internal/plan"
)

const (
	// Dir is the RAM folder of the lock-state reader and writer.
	Dir = "/run/uc-pxx-inst/optee"
	// RecordPath is the writer's record of this boot; StartedPath its start
	// marker (made before the write); BackupPath the security backup.
	RecordPath  = Dir + "/lockwrite.json"
	StartedPath = Dir + "/write-started"
	BackupPath  = Dir + "/security.bak"
)

// Record is the lock-state write's record of this boot (lockwrite.json).
type Record struct {
	BootID       string        `json:"boot_id"`
	Token        string        `json:"ram_token"`
	From         string        `json:"from"`
	To           string        `json:"to"` // always "0x01"
	Security     string        `json:"security"`
	BackupPath   string        `json:"backup_path"`
	BackupSHA256 string        `json:"backup_sha256"`
	ReadBack     string        `json:"read_back"`
	Verified     bool          `json:"verified"`
	Error        string        `json:"error,omitempty"`
	Changed      bool          `json:"changed"`
	Restored     bool          `json:"restored"`
	Before       plan.ReadSums `json:"before"`
	After        plan.ReadSums `json:"after"`
	Supplicant   string        `json:"supplicant_sha256,omitempty"`
	Client       string        `json:"client_sha256,omitempty"`
	RPCLog       string        `json:"rpc_log_sha256,omitempty"`
	Stub         string        `json:"test_stub,omitempty"`
	Confirmed    bool          `json:"confirmed"`
	FromTerminal bool          `json:"confirm_from_terminal,omitempty"`
}

// The kinds of a STOP of the lock-state write.
const (
	KindChanged    = "changed"    // a change around the write, or the record cannot be read
	KindUnrestored = "unrestored" // a security backup that neither verified nor restored
	KindStarted    = "started"    // a start marker and no record
)

// Held is the rule of a STOP of the lock-state write of this boot: b and err
// are the read of RecordPath, started whether StartedPath exists. It gives
// the kind and the reason, or "" and "" when there is no STOP.
func Held(b []byte, err error, started bool) (kind, why string) {
	if err == nil {
		var r Record
		if json.Unmarshal(b, &r) != nil || r.Changed {
			return KindChanged, "the in-RAM lock_state writer of this boot saw the A/B sector, the BCB, LBA 0..2047, the backup GPT or misc change (or its record cannot be read): keep the unit powered, do not restart, write nothing; ask for help"
		}
		// a write that did not verify and whose p16 backup did not restore
		// cleanly: the security partition may be torn; keep the unit powered
		if r.BackupSHA256 != "" && !r.Verified && !r.Restored {
			return KindUnrestored, "the in-RAM lock_state write of this boot did not reach 0x01 and the security backup did not restore: keep the unit powered, copy " + BackupPath + " off the unit, do not restart; ask for help"
		}
		return "", ""
	}
	// no record: a started-but-unfinished write is a torn-write STOP
	if started {
		return KindStarted, "an in-RAM lock_state write of this boot started and left no record (" + StartedPath + "): keep the unit powered, do not restart, write nothing; ask for help"
	}
	return "", ""
}

// Verified reports a record that says the write verified (lock_state 0x01
// read back) and is no STOP.
func Verified(b []byte) bool {
	var r Record
	if json.Unmarshal(b, &r) != nil {
		return false
	}
	if k, _ := Held(b, nil, false); k != "" {
		return false
	}
	return r.Verified && r.ReadBack == plan.LockStateUnlockedValue
}
