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
