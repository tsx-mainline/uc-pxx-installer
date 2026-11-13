//go:build install

package devmode

// The in-RAM lock_state WRITER of the direct path (owner 2026-11-11 ~23:05;
// MAINLINE-UNLOCK-INTEGRATION-2026-11-12.md). It replaces the LS reader's
// "refuse unless 0x01" gate with a write: when the in-RAM reader read
// lock_state 0x00 (locked), the installer writes 0x01 (unlocked) and reads it
// back, BEFORE any target-slot write. If the read was 0x01 it writes nothing;
// anything else, a failed read, a failed read-back or a change around the
// write refuses and writes nothing to the target.
//
//   - The value written is ALWAYS 0x01 (plan.LockStateUnlockedValue). No
//     flag, config or code path makes the installer write 0x00. teeWrite
//     builds a fixed argv for the CA: `--value 0x01 --expect-current 0xNN`.
//   - It touches only the security partition (p16): the reviewed REAL
//     supplicant opens that node O_RDWR (GPT+sysfs-checked, base 0,
//     bounds-checked). A/B (LBA 49156), the BCB, idbloader, the GPT, uboot and
//     trust are unreachable. security is not boot-critical, so a bad p16 write
//     cannot strand boot.
//   - The full security partition is backed up before the write. On any
//     failure (write failed, read-back not 0x01, a change of the A/B sector,
//     the BCB, the loader area, the backup GPT or misc around it, or the
//     read-only flag left off) it restores the backup and STOPs: nothing is
//     written to the target. A security change is expected (the save
//     re-encrypts p16), so it is not counted.
//   - Once per boot, like the reader: the write's record
//     (/run/uc-pxx-inst/optee/lockwrite.json, this boot's id and token)
//     answers a later call; a start marker (O_EXCL) that is left without a
//     verified record is a torn-write STOP that holds every later mainline
//     plan and install of the boot (LockWriteHeld).
//
// Like teeRead, teeWrite (the TEE part: the supplicant and the CA) runs only
// in the installer RAM image on the unit. Tests supply a LockWriter stub and a
// file-backed security node.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/plan"
)

const (
	// The lock-state writer's two files in the install image (root, 0500;
	// baked next to the reader's three, image/mkinitramfs.sh pins them too).
	OpteeRWSupplicant = OpteeDir + "/tee-supplicant-rw-real"
	OpteeSetlock      = OpteeDir + "/tee_legacy_setlock"
	// The pins (ucp10/tools/tee-unlock/unlocker/, each independently Opus-GO'd):
	// the REAL supplicant writes p16 O_RDWR; the setlock CA writes 0x00 or
	// 0x01 (the installer only ever calls it with 0x01).
	OpteeRWSupplicantSHA256 = "2786a2a74f4f8794ece654a97e35a36804af0f77b67fa8e933d4d6c5698fea0a"
	OpteeSetlockSHA256      = "c0a4af8a4755239de0bc860952694f00ca9bb0eda31e3347d413a96857df6768"

	LockWriteRecord  = LockRunDir + "/lockwrite.json"
	LockWriteStarted = LockRunDir + "/write-started"
	LockWriteRPCLog  = LockRunDir + "/writer-rpc.log"
	lockWriteSuppLog = LockRunDir + "/writer-supplicant.log"
	lockBackup       = LockRunDir + "/security.bak"
)

// LockWriteTEE is what the TEE part of the writer gives: the lock_state byte
// it read back after the write (nil: none), why it failed, the sha256 of the
// two files it ran and of the RPC log, and the name of a test stub ("" for the
// real one).
type LockWriteTEE struct {
	ReadBack                   *byte
	Err                        error
	Supplicant, Client, RPCLog string
	Stub                       string
}

// LockWriter is the TEE part of the lock-state writer (MainlineOptions
// .LockWriter; nil: teeWrite, the image's). The brake is lifted by lockWrite
// before it runs and re-protected after. `from` is the lock_state the reader
// read (0x00); the value written is always 0x01. Only a devtest flag gives
// another (a stub).
type LockWriter func(e *device.Env, d *device.Disk, from byte, say func(string)) LockWriteTEE

// LockWriteRecord of this boot, written to lockwrite.json.
type lockWriteRec struct {
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

// LockWriteHeld says why every later mainline plan and install of this boot
// refuses: a write started and left no verified record (a possible torn
// write), or its record says Changed or cannot be read. "" = no hold.
func LockWriteHeld(e *device.Env) string {
	if b, err := os.ReadFile(e.Path(LockWriteRecord)); err == nil {
		var r lockWriteRec
		if json.Unmarshal(b, &r) != nil || r.Changed {
			return "the in-RAM lock_state writer of this boot saw the A/B sector, the BCB, LBA 0..2047, the backup GPT or misc change (or its record cannot be read): keep the unit powered, do not restart, write nothing; ask for help"
		}
		// a write that did not verify and whose p16 backup did not restore
		// cleanly: the security partition may be torn; keep the unit powered
		if r.BackupSHA256 != "" && !r.Verified && !r.Restored {
			return "the in-RAM lock_state write of this boot did not reach 0x01 and the security backup did not restore: keep the unit powered, copy " + lockBackup + " off the unit, do not restart; ask for help"
		}
		return ""
	}
	// no record: a started-but-unfinished write is a torn-write STOP
	if _, err := os.Stat(e.Path(LockWriteStarted)); err == nil {
		return "an in-RAM lock_state write of this boot started and left no record (" + LockWriteStarted + "): keep the unit powered, do not restart, write nothing; ask for help"
	}
	return ""
}

// lockWrite performs the lock-state write of a direct install whose plan says
// a write is needed (p.UnlockDirect.Write != nil): the reader read 0x00 and
// the keeper slot A is a proven Crestron stock slot. It runs before any
// target-slot write. On success the unit reads lock_state 0x01. On any
// failure it restores the security backup and returns an error; the caller
// writes nothing to the target.
func (s *mlSession) lockWrite(p *plan.DirectPlan, say func(string)) error {
	e := s.e
	wr := p.UnlockDirect.Write
	if wr == nil {
		return &device.Refusal{Why: "internal error: lockWrite called for a plan with no lock-state write"}
	}
	if wr.To != plan.LockStateUnlockedValue || wr.From != plan.LockStateLockedValue {
		return &device.Refusal{Why: fmt.Sprintf("the lock-state write must be 0x00 -> 0x01, the plan says %s -> %s", wr.From, wr.To)}
	}
	// the keeper slot A must be a proven Crestron stock slot (the planner's
	// keeper-firmware and lock-write-safe checks); never a both-mainline unit
	if !directLockWriteKeeperOK(p) {
		return &device.Refusal{Why: "the lock-state write is refused: the keeper slot A is not a proven Crestron stock slot (a relock could strand boot); nothing written"}
	}
	// the person's agreement to the lock-state write (owner 2026-11-12): no
	// confirm means no write. The flow's warning screen and the supervised
	// CLI both set it; it is recorded in the write record.
	if !s.o.LockWriteConfirmed {
		return &device.Refusal{Why: "the lock-state write needs the person's agreement (" + plan.LockWriteFlag + "): the unit is locked and the installer would set lock_state 0x01 before it writes anything; nothing written"}
	}
	// once per boot: a verified record answers; a torn start STOPs
	if b, err := os.ReadFile(e.Path(LockWriteRecord)); err == nil {
		var r lockWriteRec
		if json.Unmarshal(b, &r) != nil {
			return fmt.Errorf("%w: the in-RAM lock_state write record cannot be read: keep the unit powered, ask for help", device.ErrABUnknown)
		}
		if r.BootID != s.bootID || r.Token != s.token {
			return &device.Refusal{Why: "the in-RAM lock_state write record is of another boot/token: no write"}
		}
		if r.Changed {
			return errLockWriteChanged(&r)
		}
		if r.Verified && r.ReadBack == plan.LockStateUnlockedValue {
			say("lock_state write: already done in this boot (" + LockWriteRecord + "): lock_state 0x01")
			return nil
		}
		return &device.Refusal{Why: "the in-RAM lock_state write of this boot did not verify (read-back " + orNoneS(r.ReadBack) + "): " + orNoneS(r.Error)}
	} else if !errors.Is(err, os.ErrNotExist) {
		return &device.Refusal{Why: fmt.Sprintf("the in-RAM lock_state write record %s: %v", LockWriteRecord, err)}
	}
	if why := LockWriteHeld(e); why != "" {
		return fmt.Errorf("%w: STOP: %s", device.ErrABUnknown, why)
	}

	// the A/B lock for the restore path (RestoreSecurity needs it)
	if err := s.d.Lock(); err != nil {
		return &device.Refusal{Why: "the A/B lock for the lock-state write: " + err.Error()}
	}
	dir := e.Path(LockRunDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return &device.Refusal{Why: fmt.Sprintf("%s: %v", LockRunDir, err)}
	}
	r := &lockWriteRec{BootID: s.bootID, Token: s.token, From: wr.From, To: plan.LockStateUnlockedValue, Security: wr.Security,
		Confirmed: true, FromTerminal: s.o.LockWriteFromTerminal}
	// the start marker: a crash after it, before the record, is a torn-write STOP
	f, err := os.OpenFile(e.Path(LockWriteStarted), os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("%w: a lock_state write of this boot started before and left no record (%s): keep the unit powered, ask for help", device.ErrABUnknown, LockWriteStarted)
	}
	fmt.Fprintf(f, "boot %s token %s %s\n", s.bootID, s.token, time.Now().UTC().Format(time.RFC3339))
	f.Close()

	say("lock_state write (once in this boot): lock_state 0x00 -> 0x01 on the security partition only, backed up first, read back")
	// the reads before (security, A/B first 32, BCB, loader area, backup GPT, misc)
	before, err := s.lockSums()
	if err != nil {
		r.Error = "the reads before the write: " + err.Error()
		return s.failLockWrite(r, nil, say)
	}
	r.Before = before
	say(fmt.Sprintf("  before: security %s, A/B first 32 %s, BCB %s", short(before.Security), short(before.AB32), short(before.BCB)))

	// the full security backup (a read; the brake is still on)
	backup, err := s.backupSecurity()
	if err != nil {
		r.Error = "the security backup: " + err.Error()
		return s.failLockWrite(r, nil, say)
	}
	bh := sha256.Sum256(backup)
	r.BackupPath, r.BackupSHA256 = e.Path(lockBackup), hex.EncodeToString(bh[:])
	say(fmt.Sprintf("  backup: security -> %s (%d bytes, sha256 %s)", lockBackup, len(backup), short(r.BackupSHA256)))

	// make the security node writable (the whole-disk ro brake); re-protect on
	// every exit
	if err := s.d.LiftSecurityBrake(); err != nil {
		r.Error = "the brake lift: " + err.Error()
		return s.failLockWrite(r, backup, say)
	}
	reprotected := false
	var reprotectErr error
	reprotect := func() {
		if !reprotected {
			reprotected = true
			if err := s.d.ReProtectSecurity(); err != nil {
				reprotectErr = err
				say("  the security brake could not be re-protected (fsync/flush/read-only flag): " + err.Error())
			}
		}
	}
	// defense in depth: a panic inside the TEE part must not leave the
	// whole-disk brake lifted (it is a no-op once reprotect ran normally)
	defer reprotect()

	// the TEE part: the REAL supplicant + the setlock CA (value always 0x01)
	write := s.o.LockWriter
	if write == nil {
		write = teeWrite
	}
	var from byte
	fmt.Sscanf(wr.From, "0x%02x", &from)
	t := write(e, s.d, from, say)
	reprotect()
	r.Supplicant, r.Client, r.RPCLog, r.Stub = t.Supplicant, t.Client, t.RPCLog, t.Stub
	switch {
	case t.Err != nil:
		r.Error = t.Err.Error()
	case t.ReadBack == nil:
		r.Error = "the CA read back no lock_state"
	default:
		r.ReadBack = fmt.Sprintf("0x%02x", *t.ReadBack)
	}

	// the reads after: a change of the A/B sector, the BCB, the loader area,
	// the backup GPT or misc around the write = STOP (a security change is
	// expected: the save re-encrypts p16, so Security is not compared here).
	// A read-only flag still off after the write is a change too (the brake
	// was not re-protected), as the reader counts it.
	after, aerr := s.lockSums()
	r.After = after
	r.Changed = aerr != nil || after.AB32 != before.AB32 || after.BCB != before.BCB ||
		after.Boot != before.Boot || after.GPTBackup != before.GPTBackup || after.Misc != before.Misc || !after.ReadOnly
	if aerr != nil {
		r.Error = strings.TrimPrefix(r.Error+"; ", "; ") + "the reads after the write: " + aerr.Error()
	}
	say(fmt.Sprintf("  after:  security %s, A/B first 32 %s, BCB %s: %s (read-back %s)", short(after.Security), short(after.AB32),
		short(after.BCB), map[bool]string{false: "A/B and BCB SAME", true: "A/B or BCB CHANGED"}[r.Changed], orNoneS(r.ReadBack)))

	if r.Changed {
		// the A/B or BCB moved around a p16 write: this must never happen; STOP
		s.restoreSecurity(r, backup, say)
		s.saveLockWrite(r)
		return errLockWriteChanged(r)
	}
	if reprotectErr != nil {
		r.Error = strings.TrimPrefix(r.Error+"; ", "; ") + "the security brake was not re-protected: " + reprotectErr.Error()
	}
	if r.Error != "" || r.ReadBack != plan.LockStateUnlockedValue {
		return s.failLockWrite(r, backup, say)
	}
	r.Verified = true
	say("  lock_state 0x01 read back: UNLOCKED; the install may write the target")
	if err := s.saveLockWrite(r); err != nil {
		return err
	}
	return nil
}

// failLockWrite restores the security backup (when there is one) and records
// the failure. The install writes nothing to the target. When a backup was
// made but the restore did not verify, it is an exit-3 hold (the p16 bytes may
// be torn and the only backup copy is in tmpfs); otherwise it is a refusal
// (exit 1: nothing was written to p16, or the backup restored cleanly).
func (s *mlSession) failLockWrite(r *lockWriteRec, backup []byte, say func(string)) error {
	if backup != nil {
		s.restoreSecurity(r, backup, say)
	}
	s.saveLockWrite(r)
	why := "the lock-state write did not reach lock_state 0x01 (" + orNoneS(r.ReadBack) + "): " + orNoneS(r.Error) + "; nothing written to the target"
	if backup != nil && !r.Restored {
		return fmt.Errorf("%w: STOP: %s; the security backup did not restore (it is at %s): keep the unit powered, do not restart; ask for help",
			device.ErrABUnknown, why, lockBackup)
	}
	return &device.Refusal{Why: why + map[bool]string{true: "; security restored from the backup", false: ""}[r.Restored]}
}

// restoreSecurity writes the backup back to the security partition and reads
// it back (the restore-on-failure net). It sets r.Restored only when the
// read-back equals the backup sha256 (RULES hard rule 4). A restore that
// fails or does not verify leaves r.Restored false, which failLockWrite turns
// into an exit-3 hold.
func (s *mlSession) restoreSecurity(r *lockWriteRec, backup []byte, say func(string)) {
	say("  STOP: restoring the security partition from the backup")
	if err := s.d.LiftSecurityBrake(); err != nil {
		say("  the brake lift for the restore failed: " + err.Error() + " (the backup is at " + lockBackup + ")")
		return
	}
	defer s.d.ReProtectSecurity()
	if err := s.d.RestoreSecurity(backup, nil); err != nil {
		say("  the security restore failed: " + err.Error() + " (the backup is at " + lockBackup + ")")
		return
	}
	h, err := s.d.HashRange(s.securityOff(), int64(len(backup)))
	if err != nil {
		say("  the read-back of the restored security partition failed: " + err.Error() + " (the backup is at " + lockBackup + ")")
		return
	}
	if h != r.BackupSHA256 {
		say("  the restored security partition reads back " + short(h) + ", not the backup " + short(r.BackupSHA256) + " (the backup is at " + lockBackup + ")")
		return
	}
	r.Restored = true
	say("  security restored from the backup and read back (sha256 " + short(h) + ")")
}

// backupSecurity reads the whole security partition through its node and
// writes it to the backup file.
func (s *mlSession) backupSecurity() ([]byte, error) {
	p, ok := s.d.Part(SecurityPart)
	if !ok {
		return nil, errors.New("no partition security")
	}
	b := make([]byte, p.Bytes())
	if err := s.d.ReadPart(p, b, 0); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(s.e.Path(lockBackup), os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	_, werr := f.Write(b)
	if serr := f.Sync(); werr == nil {
		werr = serr
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return nil, werr
	}
	return b, nil
}

// securityOff is the absolute byte offset of the security partition.
func (s *mlSession) securityOff() int64 {
	p, _ := s.d.Part(SecurityPart)
	return p.Start * 512
}

// SecurityPart is the GPT name of the partition the lock-state write touches.
const SecurityPart = device.SecurityPart

func errLockWriteChanged(r *lockWriteRec) error {
	return fmt.Errorf("%w: STOP: the A/B sector, the BCB, LBA 0..2047, the backup GPT or misc changed around the lock_state write, or the eMMC read-only flag was left off (before %+v, after %+v): keep the unit powered, do not restart, write nothing; ask for help",
		device.ErrABUnknown, r.Before, r.After)
}

// saveLockWrite writes the record (a new file, a rename, a sync).
func (s *mlSession) saveLockWrite(r *lockWriteRec) error {
	b, _ := json.MarshalIndent(r, "", "  ")
	tmp := s.e.Path(LockWriteRecord + ".new")
	os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err == nil {
		_, err = f.Write(append(b, '\n'))
		if serr := f.Sync(); err == nil {
			err = serr
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err == nil {
		err = os.Rename(tmp, s.e.Path(LockWriteRecord))
	}
	if err != nil {
		return fmt.Errorf("%w: the in-RAM lock_state write record could not be written (%v): keep the unit powered, ask for help", device.ErrABUnknown, err)
	}
	return nil
}

// directLockWriteKeeperOK reports that the plan proves the keeper slot A a
// Crestron stock slot (the planner's keeper-firmware and lock-write-safe
// checks passed). The lock-state write runs only then: never on a unit with
// mainline in both slots.
func directLockWriteKeeperOK(p *plan.DirectPlan) bool {
	kf, lw := false, false
	for _, c := range p.Checks {
		switch c.ID {
		case "keeper-firmware":
			kf = c.OK
		case "lock-write-safe":
			lw = c.OK
		}
	}
	return kf && lw
}

// teeWrite is the image's TEE part of the lock-state writer (see the file
// comment). It runs only in the installer RAM image on the unit, after the
// reader loaded optee_legacy.ko and with the security brake lifted by
// lockWrite. It never writes 0x00: the CA argv is fixed to --value 0x01.
func teeWrite(e *device.Env, d *device.Disk, from byte, say func(string)) LockWriteTEE {
	var t LockWriteTEE
	fail := func(f string, a ...any) LockWriteTEE { t.Err = fmt.Errorf(f, a...); return t }
	// the reader must have loaded the module; the writer never insmods
	if _, err := os.Lstat(e.Path(teeModule)); err != nil {
		return fail("optee_legacy is not loaded (the reader loads it; run the read first): no write")
	}
	if pids := procsNamed(e, "tee-supplicant"); len(pids) > 0 {
		return fail("a tee-supplicant runs already (pid %v): no write", pids)
	}
	sec, ok := d.Part(SecurityPart)
	if !ok || sec.Dev != SecurityDev || sec.Index != 16 {
		return fail("the partition security is not %s (GPT index 16): no write", SecurityDev)
	}
	bin := e.Path(LockRunDir + "/wbin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		return fail("%s/wbin: %v", LockRunDir, err)
	}
	supp, _, err := pinnedCopy(e, OpteeRWSupplicant, OpteeRWSupplicantSHA256, bin, 0o500)
	if err != nil {
		return fail("the supplicant: %v", err)
	}
	t.Supplicant = OpteeRWSupplicantSHA256
	cli, _, err := pinnedCopy(e, OpteeSetlock, OpteeSetlockSHA256, bin, 0o500)
	if err != nil {
		return fail("the setlock CA: %v", err)
	}
	t.Client = OpteeSetlockSHA256
	if st, err := os.Stat(e.Path(TEEDevice)); err != nil || st.Mode()&os.ModeCharDevice == 0 {
		return fail("no TEE char device %s: the module bound no OP-TEE", TEEDevice)
	}

	lf, err := os.OpenFile(e.Path(LockWriteRPCLog), os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fail("the RPC log: %v", err)
	}
	defer lf.Close()
	sl, err := os.OpenFile(e.Path(lockWriteSuppLog), os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fail("the supplicant log: %v", err)
	}
	defer sl.Close()
	cmd := exec.Command(supp, "--disk", device.EMMCPath, "--rpc-log-fd", "3")
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	cmd.Dir = "/"
	cmd.Stdout, cmd.Stderr = sl, sl
	cmd.ExtraFiles = []*os.File{lf} // fd 3
	if err := cmd.Start(); err != nil {
		return fail("the supplicant: %v", err)
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	stop := func() {
		cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
			select {
			case <-exited:
			case <-time.After(5 * time.Second):
			}
		}
	}
	defer func() {
		lf.Sync()
		if b, err := os.ReadFile(e.Path(LockWriteRPCLog)); err == nil {
			h := sha256.Sum256(b)
			t.RPCLog = hex.EncodeToString(h[:])
		}
	}()
	// wait for the REAL supplicant: its O_RDWR open of the TEE device and its
	// "O_RDWR, REAL" line
	up := false
	for i := 0; i < 20 && !up; i++ {
		select {
		case <-exited:
			i = 20
			continue
		default:
		}
		fds, _ := os.ReadDir(e.Path(fmt.Sprintf("/proc/%d/fd", cmd.Process.Pid)))
		dev := false
		for _, fd := range fds {
			if l, err := os.Readlink(e.Path(fmt.Sprintf("/proc/%d/fd/%s", cmd.Process.Pid, fd.Name()))); err == nil && l == TEEDevice {
				dev = true
			}
		}
		if b, _ := os.ReadFile(e.Path(lockWriteSuppLog)); dev && hasSub(b, "O_RDWR, REAL") {
			up = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !up {
		stop()
		return fail("the REAL supplicant did not come up (O_RDWR, REAL) on /dev/%s: nothing written", sec.Dev)
	}
	say("  REAL supplicant up (pid " + fmt.Sprint(cmd.Process.Pid) + "): security = /dev/" + sec.Dev + " O_RDWR, REAL")

	// the CA: write 0x01 only, expecting the current 0xNN (from the read). The
	// value is a fixed literal: the installer never writes 0x00.
	rc, out, rerr := runFixed(60*time.Second, cli, "--value", plan.LockStateUnlockedValue, "--expect-current", fmt.Sprintf("0x%02x", from))
	stop()
	after := setlockField(out, "read-after")
	before := setlockField(out, "read-before")
	say(fmt.Sprintf("  setlock: exit %d, read-before %s, read-after %s", rc, orNoneS(before), orNoneS(after)))
	switch {
	case rerr != nil:
		return fail("the setlock CA: %v", rerr)
	case rc == 0 && after == plan.LockStateUnlockedValue:
		var v byte = 1
		t.ReadBack = &v
		return t
	case rc == 6 && before == plan.LockStateUnlockedValue:
		// already unlocked (a prior write in this boot): a no-op success
		var v byte = 1
		t.ReadBack = &v
		return t
	default:
		return fail("the setlock CA did not confirm 0x01 (exit %d, before %s, after %s): %s", rc, orNoneS(before), orNoneS(after), strings.TrimSpace(firstLines(out, 3)))
	}
}

// setlockField parses "key: lock_state = 0xNN ..." from the CA's output.
func setlockField(out, key string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, key+": lock_state = ") {
			rest := strings.TrimPrefix(l, key+": lock_state = ")
			if f := strings.Fields(rest); len(f) > 0 {
				return f[0]
			}
		}
	}
	return ""
}

func hasSub(b []byte, want string) bool { return strings.Contains(string(b), want) }
