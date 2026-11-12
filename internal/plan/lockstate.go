package plan

// LockStateRead is one run of the in-RAM lock_state reader of the direct path
// (package devmode; MAINLINE-RC-2026-11-11.md §10, MAINLINE-RC2-LS-2026-11-11.md):
// the READ of the TEE object lock_state in this RAM session, at most once per
// boot, with the reads around it. The direct planner takes the unlock only from
// it: lock_state 0x01, no error, nothing changed. A failed read, another byte,
// a change or no read refuses; a person's confirmation never opens the gate.
type LockStateRead struct {
	BootID string `json:"boot_id"`   // the kernel's boot id of the read
	Token  string `json:"ram_token"` // the RAM image token of the read
	// The sha256 of the three files the reader ran (their private copies,
	// each checked against its pin first); "" when it stopped before.
	Module     string `json:"module_sha256"`
	Supplicant string `json:"supplicant_sha256"`
	Client     string `json:"client_sha256"`
	// LockState is the byte the client printed ("0x01"), or "" (none).
	LockState string `json:"lock_state"`
	// Error is why the read gave no usable byte ("" = it gave one).
	Error string `json:"error,omitempty"`
	// Before and After: what the reader read itself (O_DIRECT) before the
	// module was loaded and after the supplicant was stopped.
	Before ReadSums `json:"before"`
	After  ReadSums `json:"after"`
	// Changed: a sum of After differs from Before, an after-read failed, or
	// the kernel read-only flag of an eMMC node was off after the read.
	Changed bool `json:"changed"`
	// RPCLog is the sha256 of the supplicant's RPC log (never a data byte).
	RPCLog string `json:"rpc_log_sha256,omitempty"`
	// Stub names a devtest stub of the TEE part ("" in a release build:
	// only a devtest flag sets it).
	Stub string `json:"test_stub,omitempty"`
}

// ReadSums are the reader's own reads around the TEE read.
type ReadSums struct {
	Security string `json:"security_sha256"` // the whole partition security
	AB32     string `json:"ab32_sha256"`     // the first 32 bytes of the A/B sector (LBA 49156)
	BCB      string `json:"bcb_sha256"`      // the BCB, LBA 49184, 4 sectors
	// review B-3 (hardening): the first 2048 sectors (the primary GPT and
	// the loader area), the backup GPT (the last 33 sectors), the whole misc
	Boot      string `json:"lba0_2047_sha256"`
	GPTBackup string `json:"gpt_backup_sha256"`
	Misc      string `json:"misc_sha256"`
	// ReadOnly: the kernel read-only flag is set on the disk and on every
	// partition (BLKROGET).
	ReadOnly bool `json:"emmc_read_only"`
}

// Unlocked: the read gave 0x01, with no error and nothing changed.
func (r *LockStateRead) Unlocked() bool {
	return r != nil && r.Error == "" && !r.Changed && r.LockState == "0x01"
}

// LockStateWriteIntent is recorded in a direct plan when the in-RAM reader
// read lock_state 0x00 (locked). The installer then writes lock_state 0x01
// (unlocked) with the integrated lock-state writer before any target-slot
// write (owner 2026-11-11 ~23:05): read the security object, write 0x01,
// read it back 0x01. The value is ALWAYS 0x01; no plan, flag or config ever
// makes the installer write 0x00. The write touches only the security
// partition (p16), backed up first and restored on any failure; the keeper
// slot A is a proven Crestron stock slot, so a relock could not strand boot.
// The intent is deterministic (From, To and Security are fixed from the
// 0x00 read), so the plan id is stable across the re-check.
type LockStateWriteIntent struct {
	From     string `json:"from"`     // the lock_state the reader read ("0x00")
	To       string `json:"to"`       // the value written, always "0x01"
	Security string `json:"security"` // the partition the writer touches ("mmcblk0p16")
	Note     string `json:"note"`
}

// LockStateUnlockedValue is the only value the installer's lock-state writer
// ever writes: 0x01 (unlocked / secure-boot verification off). There is no
// code path that writes 0x00 (relock) from the installer.
const (
	LockStateUnlockedValue = "0x01"
	LockStateLockedValue   = "0x00"
	// SecurityPartNode is the eMMC node of the security partition (p16) on
	// the UC-P10 (RULES hard rule 3: the lock-state write touches it only).
	SecurityPartNode = "mmcblk0p16"
)
