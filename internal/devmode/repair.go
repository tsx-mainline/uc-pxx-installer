//go:build install

package devmode

// The A/B repair of the hold (owner decision 2026-10-31, DEVMODE-R4.md §2):
//
//	uc-pxx-inst repair --device /dev/mmcblk0 --plan PLAN.json --log DIR [--yes | --dry-run]
//
// An install that ends with the A/B state not known leaves the unit on the
// hold screen: do not power off, do not restart. If the sector really is not
// valid, a power cut then is a brick. The repair writes, through
// uc-pxx-bootctl-inst `repair`, the one fixed state R(K) in which only the
// keeper K boots (K successful {15,0,1}, the target {0,0,0}), and only when
// all of this holds:
//
//  1. the installer RAM image, the eMMC of a UC-P10, the A/B lock, and no
//     uc-pxx-bootctl-inst still running (after a killed install the tool may
//     finish in its own session);
//  2. the plan file is a device plan whose content matches its id, of this
//     eMMC, this GPT, this RAM image token and THIS BOOT (the boot id): then
//     nothing has booted a slot since the plan hashed the keeper, so oem_K
//     and the rollback indexes cannot have changed;
//  3. the sector reads (an unreadable sector is never written) and needs a
//     write (plan.ClassifyRepair: not valid, or the target boots next while
//     the keeper is not successful). A valid sector in which the keeper is
//     successful or boots next is left as it is;
//  4. bootctl status says the same as the installer's decoder (valid or not);
//  5. the keeper is proven intact: all eight partitions of slot K have the
//     sha256 the plan recorded when K was a successful slot;
//  6. the tool's dry run would write exactly the installer's own prediction
//     plan.PredictRepair, and the sector is still the one read in 3.
//
// Then one write (the tool's), a fresh read-back (page caches dropped,
// O_DIRECT) compared with the prediction byte for byte, and bootctl status
// again. On a mismatch: at most one more write, and only when the read-back
// itself reads and still needs a write by both decoders (it then has no
// state that boots the keeper to lose) and the keeper proof holds; then a
// stop. Never a third write, never a write over an unreadable sector.

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/plan"
)

// Exit codes of uc-pxx-inst repair.
const (
	// RepairExitSafe: the A/B sector is valid now and the keeper boots
	// (successful, or next): no repair was needed, or R(K) was written and
	// read back. Safe to power off.
	RepairExitSafe = 0
	// RepairExitRefused: nothing was written; the sector is as it was (it may
	// still be not valid): the unit stays on the hold.
	RepairExitRefused = 1
	// RepairExitUnknown: a write was made and the sector is not the
	// prediction, or the tool did not end with a known state: the hold, do not
	// power off.
	RepairExitUnknown = 3
	// RepairExitWouldRepair: --dry-run only: a repair is needed, and every
	// check passed. Nothing was written.
	RepairExitWouldRepair = 5
)

// RepairOptions are the options of the repair.
type RepairOptions struct {
	Device   string    // must be /dev/mmcblk0
	PlanFile string    // the plan of the install that ended on the hold
	LogDir   string    // the log folder
	Yes      bool      // the confirmation is given
	Confirm  io.Reader // else the word REPAIR typed
	DryRun   bool      // every check and the tool's dry run, no write
	// Tests only:
	Hooks       *install.Hooks // CutAfter n: a power cut before write n+1; Observe after each write
	BeforeWrite func()         // right before each write
}

// RepairResult is what the repair did.
type RepairResult struct {
	Class  plan.RepairClass
	Why    string
	Writes int    // runs of the write form of uc-pxx-bootctl-inst repair (0, 1 or 2)
	Before []byte // the sector read first (nil: unreadable)
	After  []byte // the sector read last
	Would  []byte // --dry-run: the sector the repair would write
	Log    string // the JSON-lines log
}

// RepairExitCode is the exit status of uc-pxx-inst repair.
func RepairExitCode(res *RepairResult, err error) int {
	switch {
	case err == nil && res != nil && res.Would != nil:
		return RepairExitWouldRepair
	case err == nil:
		return RepairExitSafe
	case errors.Is(err, device.ErrABUnknown):
		return RepairExitUnknown
	}
	return RepairExitRefused
}

type repairRun struct {
	kind string // "repair" or "switch"
	s    *session
	o    RepairOptions
	p    *plan.Plan
	k    int // the keeper: 0 = a, 1 = b
	lf   *os.File
	dir  string
	tag  string
	cut  install.Cutter
	res  *RepairResult
}

func (r *repairRun) log(kv map[string]any) {
	if r.lf == nil {
		return
	}
	kv["time"] = time.Now().UTC().Format(time.RFC3339Nano)
	b, _ := json.Marshal(kv)
	r.lf.Write(append(b, '\n'))
}

func (r *repairRun) refuse(f string, a ...any) error {
	why := fmt.Sprintf(f, a...)
	r.log(map[string]any{"event": "refused", "why": why})
	return &device.Refusal{Why: r.kind + " refused, nothing written: " + why}
}

// saveSector keeps a raw copy of the sector in the log folder.
func (r *repairRun) saveSector(name string, b []byte) {
	if b == nil {
		return
	}
	p := filepath.Join(r.dir, r.tag+"-"+name+".bin")
	if CheckOutputPath(p) != nil {
		return
	}
	if f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o644); err == nil {
		f.Write(b)
		f.Close()
	}
}

// Repair runs the repair. See the package text of this file for the rules.
func Repair(e *device.Env, o RepairOptions, out io.Writer) (*RepairResult, error) {
	res := &RepairResult{}
	if o.PlanFile == "" || o.LogDir == "" {
		return res, &device.Refusal{Why: "repair needs --plan (the plan of the install) and --log DIR"}
	}
	if err := CheckInputFile(o.PlanFile); err != nil {
		return res, err
	}
	r, done, err := startABRun(e, o.Device, o.LogDir, "repair", out, res)
	if err != nil {
		return res, err
	}
	r.o = o
	r.cut.Hooks = o.Hooks
	r.log(map[string]any{"event": "environment", "ram_token": r.s.token, "boot_id": r.s.bootID, "plan_file": o.PlanFile,
		"dry_run": o.DryRun})
	return res, done(r.run())
}

// startABRun makes the log folder and its files (TAG-TIME.jsonl, .out), runs
// the checks of the device mode before any plan (the RAM image, the eMMC with
// the read-only flags set, the A/B lock), and returns the run. done ends it:
// the last sector into the folder, the read-only flags again, the end line.
func startABRun(e *device.Env, dev, dir, kind string, out io.Writer, res *RepairResult) (*repairRun, func(error) error, error) {
	if err := outsideKernelFS(dir); err != nil {
		return nil, nil, err
	}
	if st, err := os.Lstat(dir); err == nil && !st.IsDir() {
		return nil, nil, &device.Refusal{Why: fmt.Sprintf("%s is not a directory", dir)}
	} else if err != nil {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, nil, &device.Refusal{Why: err.Error()}
		}
	}
	r := &repairRun{dir: dir, tag: kind + "-" + time.Now().UTC().Format("20060102T150405.000Z"), res: res, kind: kind}
	logPath := filepath.Join(r.dir, r.tag+".jsonl")
	trPath := filepath.Join(r.dir, r.tag+".out")
	for _, p := range []string{logPath, trPath} {
		if err := CheckOutputPath(p); err != nil {
			return nil, nil, err
		}
	}
	var err error
	if r.lf, err = os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o644); err != nil {
		return nil, nil, &device.Refusal{Why: err.Error()}
	}
	res.Log = logPath
	tr, err := os.OpenFile(trPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		r.lf.Close()
		return nil, nil, &device.Refusal{Why: err.Error()}
	}
	s, err := open(e, Options{Device: dev}, io.MultiWriter(out, tr))
	if err != nil {
		r.log(map[string]any{"event": "refused", "why": err.Error()})
		r.lf.Close()
		tr.Close()
		return nil, nil, err
	}
	r.s = s
	s.say("%s: log %s, transcript %s", kind, logPath, trPath)
	done := func(err error) error {
		if res.After != nil {
			r.saveSector("ab-after", res.After)
		}
		if ferr := s.d.ReadOnlyAll(); ferr != nil {
			// Never a change of the exit: it says what happened to the A/B
			// sector (a refusal would claim "nothing written" after a write).
			s.say("WARNING: the kernel read-only flags could not be set again (%v); the A/B result stands: %d write(s) of the tool", ferr, res.Writes)
			r.log(map[string]any{"event": "readonly-flags", "error": ferr.Error(), "writes": res.Writes})
		}
		r.log(map[string]any{"event": "end", "class": string(res.Class), "writes": res.Writes, "error": errString(err),
			"exit": RepairExitCode(res, err)})
		s.close()
		signal.Reset(syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGPIPE)
		r.lf.Close()
		tr.Close()
		return err
	}
	return r, done, nil
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (r *repairRun) run() error {
	s, d, res := r.s, r.s.d, r.res
	// 2. the plan: this eMMC, this GPT, this RAM image, this boot
	p, err := ReadPlanFile(r.o.PlanFile)
	if err != nil {
		return r.refuse("%v", err)
	}
	r.p = p
	switch {
	case p.Keeper == "a" && p.Target == "b":
		r.k = 0
	case p.Keeper == "b" && p.Target == "a":
		r.k = 1
	default:
		return r.refuse("the plan has keeper %q and target %q", p.Keeper, p.Target)
	}
	r.log(map[string]any{"event": "plan", "plan": p.ID, "keeper": p.Keeper, "target": p.Target})
	s.say("repair: the plan %s (id %s): keeper slot %s, target slot %s", r.o.PlanFile, p.ID, strings.ToUpper(p.Keeper), strings.ToUpper(p.Target))
	switch {
	case p.Disk.Source != "device:"+device.EMMCPath || p.Device == nil || *p.Device != d.ID || p.Disk.Sectors != d.Sectors:
		return r.refuse("the plan is not for this eMMC")
	case p.Disk.LayoutSHA256 != d.Table.LayoutSHA256():
		return r.refuse("the GPT layout differs from the plan")
	case p.RAMImage == nil || p.RAMImage.Token != s.token:
		return r.refuse("the plan was made in another RAM image (token %v, this one %s)", p.RAMImage, s.token)
	case p.RAMImage.BootID != s.bootID:
		return r.refuse("the plan was made in another boot (boot id %s, now %s): after a boot the keeper hashes of the plan prove nothing (oem changes while Android runs)",
			p.RAMImage.BootID, s.bootID)
	case len(p.KeeperSHA256) != len(plan.SlotParts):
		return r.refuse("the plan has no keeper hashes")
	}
	v := d.View()
	t, err := gpt.Parse(v, d.Bytes())
	v.Close()
	if err != nil || t.LayoutSHA256() != p.Disk.LayoutSHA256 {
		return r.refuse("the GPT read now is not the plan's (%v)", err)
	}
	now0, _ := p.AB.Now.Raw()
	if a, _ := avbab.Parse(now0[:]); a.Validate() != nil || !a.Slots[r.k].Keeper() {
		return r.refuse("in the plan's A/B state the keeper %s is not successful", p.Keeper)
	}
	// 1. no bootctl may still run
	if pids, err := s.e.BootctlRuns(); err != nil || len(pids) > 0 {
		return r.refuse("uc-pxx-bootctl-inst still runs (pid %s; %v): wait until it ends, then run the repair again", strings.Join(pids, " "), err)
	}
	// 3. the sector, and what it needs
	cur, rerr := d.ReadABFresh()
	if rerr == nil {
		res.Before = cur
		r.saveSector("ab-before", cur)
	}
	res.Class, res.Why = plan.ClassifyRepair(cur, rerr, r.k)
	ev := map[string]any{"event": "sector", "class": string(res.Class), "why": res.Why, "sector": hexOrNil(cur)}
	if cur != nil {
		ev["sha256"] = sha256hex(cur)
	}
	r.log(ev)
	s.say("REPAIR-CLASS: %s: %s", res.Class, res.Why)
	if rerr != nil {
		return r.refuse("the A/B sector cannot be read (%v): an unreadable sector is never written", rerr)
	}
	s.say("repair: A/B sector now %x (decoded, not trusted: %s)", cur[:32], plan.State(first32(cur)))
	// 4. bootctl status must say the same as the decoder
	if err := r.statusAgrees(cur); err != nil {
		return r.refuse("%v", err)
	}
	// 5. the keeper
	if err := r.proveKeeper(t); err != nil {
		s.say("KEEPER: NOT PROVEN: %v", err)
		return r.refuse("the keeper slot %s is not proven intact: %v", strings.ToUpper(r.p.Keeper), err)
	}
	if !res.Class.NeedsWrite() {
		res.After = cur
		s.say("REPAIR-SAFE: yes: %s", plan.State(first32(cur)))
		s.say("")
		s.say("RESULT: no repair needed: %s. Slot %s boots; it is safe to power off.", res.Why, strings.ToUpper(p.Keeper))
		return nil
	}
	// 6. the prediction and the tool's dry run
	return r.repair(cur)
}

func hexOrNil(b []byte) any {
	if b == nil {
		return nil
	}
	return hex.EncodeToString(b)
}

// statusAgrees: bootctl status and the installer's decoder say the same of
// the sector: both valid with the same slots, CRC and pick, or both not valid.
func (r *repairRun) statusAgrees(cur []byte) error {
	a, _ := avbab.Parse(cur)
	verr := a.Validate()
	br, err := r.s.d.Bootctl([]string{"status"})
	if err != nil {
		return fmt.Errorf("uc-pxx-bootctl-inst status: %v", err)
	}
	r.log(map[string]any{"event": "bootctl", "args": []string{"status"}, "rc": br.RC, "stdout": br.Stdout, "stderr": br.Stderr})
	switch {
	case verr == nil && br.RC == 0:
		st, err := plan.ParseBootctlStatus(br.Stderr, br.Stdout)
		if err != nil {
			return err
		}
		if err := st.Agrees(a); err != nil {
			return fmt.Errorf("bootctl status and the installer's decoder disagree: %w", err)
		}
	case verr != nil && br.RC == 1 && strings.Contains(br.Stderr, "current AvbABData invalid"):
	default:
		return fmt.Errorf("bootctl status (exit %d: %s) and the installer's decoder (%v) disagree", br.RC, strings.TrimSpace(br.Stderr), verr)
	}
	r.s.say("repair: uc-pxx-bootctl-inst status agrees with the installer's decoder (exit %d)", br.RC)
	return nil
}

// proveKeeper hashes all eight partitions of the keeper (O_DIRECT) and
// compares them with the plan.
func (r *repairRun) proveKeeper(t *gpt.Table) error {
	for _, part := range plan.SlotParts {
		name := part + "_" + r.p.Keeper
		e, ok := t.Find(name)
		want := r.p.KeeperSHA256[name]
		if !ok || len(want) != 64 {
			return fmt.Errorf("no %s in the GPT or in the plan", name)
		}
		got, err := r.s.d.HashRange(e.Start*gpt.Sector, e.Bytes())
		r.log(map[string]any{"event": "keeper", "partition": name, "sha256": got, "want": want, "error": errString(err)})
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if got != want {
			if part == "oem" {
				return fmt.Errorf("%s has sha256 %s, the plan %s (oem changes only while Android runs; in this boot nothing ran)", name, got, want)
			}
			return fmt.Errorf("%s has sha256 %s, the plan %s", name, got, want)
		}
		r.s.say("repair: keeper %-9s ok  %s", name, got[:16])
	}
	r.s.say("KEEPER: proven: all %d partitions of slot %s have the sha256 of the plan", len(plan.SlotParts), strings.ToUpper(r.p.Keeper))
	return nil
}

// dryRun runs the tool's dry run on cur and checks that it would write
// exactly the prediction want.
func (r *repairRun) dryRun(cur, want []byte) error {
	args := device.RepairArgs(r.p.Keeper, cur, true)
	br, err := r.s.d.Bootctl(args)
	if err != nil {
		return fmt.Errorf("uc-pxx-bootctl-inst repair --dry-run: %v", err)
	}
	r.log(map[string]any{"event": "bootctl", "args": "repair --keeper " + r.p.Keeper + " --dry-run", "rc": br.RC, "stdout": br.Stdout, "stderr": br.Stderr})
	line := "repair: would-write " + hex.EncodeToString(want[:32]) + "\n"
	if br.RC != 0 || br.Stdout != line {
		return fmt.Errorf("the tool's dry run (exit %d, %q) is not the installer's prediction %x: the two disagree", br.RC, strings.TrimSpace(br.Stdout), want[:32])
	}
	r.s.say("repair: uc-pxx-bootctl-inst repair --dry-run would write %x (= the installer's prediction)", want[:32])
	return nil
}

// repair makes the write (at most twice, see the file text).
func (r *repairRun) repair(cur []byte) error {
	s, d, res := r.s, r.s.d, r.res
	orig := append([]byte(nil), cur...) // the first sector: "nothing written" means the sector is this one
	changed := false                    // a read-back after a write differed from orig
	// beforeSecond ends the run on a failure after write 1 and before write
	// 2 (review RR-L1): exit 1 only when no read-back changed and a fresh
	// (flushed) read is still byte for byte orig; else exit 3.
	beforeSecond := func(f string, a ...any) error {
		why := fmt.Sprintf(f, a...)
		if !changed {
			if back, err := d.ReadABFresh(); err == nil && bytes.Equal(back, orig) {
				res.After = back
				return r.refuse("the first write did not take (the sector is the one read first), and before the second write: %s", why)
			}
		}
		return r.unknown("before the second write: %s", why)
	}
	for attempt := 1; ; attempt++ {
		want, err := plan.PredictRepair(cur, r.k)
		if err != nil {
			if attempt == 1 {
				return r.refuse("%v", err)
			}
			return beforeSecond("%v", err)
		}
		if err := r.dryRun(cur, want); err != nil {
			if attempt == 1 {
				return r.refuse("%v", err)
			}
			return beforeSecond("%v", err)
		}
		if r.o.DryRun {
			res.Would, res.After = want, cur
			s.say("")
			s.say("RESULT: repair possible (dry run, nothing written): it would write %x: only slot %s boots (%s)",
				want[:32], strings.ToUpper(r.p.Keeper), plan.State(first32(want)))
			return nil
		}
		if attempt == 1 {
			if err := r.confirm(); err != nil {
				return err
			}
		}
		// the sector must still be the one that was checked
		again, err := d.ReadABFresh()
		if err != nil || !bytes.Equal(again, cur) {
			if attempt == 1 {
				return r.refuse("the A/B sector changed after it was checked (%v)", err)
			}
			return beforeSecond("the A/B sector changed (%v)", err)
		}
		if pids, err := s.e.BootctlRuns(); err != nil || len(pids) > 0 {
			if attempt == 1 {
				return r.refuse("uc-pxx-bootctl-inst runs (pid %s; %v)", strings.Join(pids, " "), err)
			}
			// a write was made already: never "nothing written" here
			return beforeSecond("uc-pxx-bootctl-inst runs (pid %s; %v)", strings.Join(pids, " "), err)
		}
		if err := d.CanWriteAB(); err != nil {
			if attempt == 1 {
				return r.refuse("the tool cannot write: %v", err)
			}
			return beforeSecond("the tool cannot write: %v", err)
		}
		if r.o.BeforeWrite != nil {
			r.o.BeforeWrite()
		}
		if err := r.cut.Before(nil); err != nil {
			return err // tests: a power cut before the write
		}
		s.say("REPAIR-WRITE: %d: uc-pxx-bootctl-inst repair --keeper %s (the A/B sector is written now: do not power off)", attempt, r.p.Keeper)
		br, rerr := d.Bootctl(device.RepairArgs(r.p.Keeper, cur, false))
		res.Writes++
		if errors.Is(rerr, install.ErrPowerCut) {
			return rerr // tests: a power cut during the write
		}
		if cerr := r.cut.After(install.Event{Op: "ab", Partition: "misc", Off: plan.ABLBA * gpt.Sector, Len: gpt.Sector}); cerr != nil {
			return cerr
		}
		rc, sig, stdout, stderr := -1, "", "", ""
		if rerr != nil {
			stderr = rerr.Error()
		} else {
			rc, sig, stdout, stderr = br.RC, br.Signal, br.Stdout, br.Stderr
		}
		back, berr := d.ReadABFresh()
		res.After = back
		r.log(map[string]any{"event": "write", "attempt": attempt, "rc": rc, "signal": sig, "stdout": stdout, "stderr": stderr,
			"want": hex.EncodeToString(want), "read_back": hexOrNil(back), "read_error": errString(berr)})
		switch {
		case berr != nil:
			return r.unknown("the read-back after the write failed (%v): never a write over an unreadable sector", berr)
		case bytes.Equal(back, want):
			if err := r.statusAgrees(back); err != nil {
				return r.unknown("the sector is the prediction, but %v", err)
			}
			if rc != 0 {
				s.say("repair: the tool ended with exit %d %s, but the read-back is exactly the prediction", rc, sig)
			}
			s.say("REPAIR-WRITE: %d: read back %x = the prediction (eMMC cache flushed, page caches dropped, O_DIRECT)", attempt, back[:32])
			s.say("REPAIR-SAFE: yes: %s", plan.State(first32(back)))
			s.say("")
			s.say("RESULT: repaired: A/B now %s: only slot %s boots; it is safe to power off.", plan.State(first32(back)), strings.ToUpper(r.p.Keeper))
			return nil
		}
		s.say("REPAIR-WRITE: %d: the tool ended with exit %d %s; read back %x, the prediction %x", attempt, rc, sig, back[:32], want[:32])
		c, why := plan.ClassifyRepair(back, nil, r.k)
		// A write that did not take: the first sector reads back byte for
		// byte, after a cache flush, whatever the tool's exit says.
		notTaken := bytes.Equal(back, orig)
		if !bytes.Equal(back, orig) {
			changed = true
		}
		if attempt == 2 {
			if notTaken && !changed {
				// both writes did not take: the sector is the first one
				return r.refuse("the write did not take twice (the tool's exit %d %s); the sector is the one read first: %s", rc, sig, why)
			}
			return r.unknown("the read-back %x is not the prediction %x (%s: %s); no more writes", back[:32], want[:32], c, why)
		}
		if !notTaken && !c.Retryable() {
			// a read-back that boots the keeper (successful, or trying
			// and next), but is not the prediction: stop, write nothing more
			return r.unknown("the read-back %x is not the prediction %x (%s: %s); no more writes", back[:32], want[:32], c, why)
		}
		// One more write, the only one: either the first write did not take
		// (the sector is the first one, which this run decided to repair),
		// or the read-back is not valid or boots the target (Retryable), so a
		// write cannot lose a state that boots the keeper. The keeper proof of
		// this run still holds: the tool writes LBA 49156 only.
		if notTaken {
			s.say("repair: the write did not take (the tool's exit %d %s, the first sector read back): one more write, the last one", rc, sig)
		} else {
			s.say("repair: the read-back still needs a repair (%s: %s): one more write, the last one", c, why)
		}
		r.log(map[string]any{"event": "retry", "class": string(c), "why": why})
		if err := r.statusAgrees(back); err != nil {
			return beforeSecond("%v", err)
		}
		cur = back
	}
}

func (r *repairRun) unknown(f string, a ...any) error {
	why := fmt.Sprintf(f, a...)
	r.log(map[string]any{"event": "stop", "why": why})
	s := r.s
	s.say("")
	s.say("STOP: %s", why)
	s.say("!!! DO NOT POWER OFF AND DO NOT RESTART. The A/B state is not known.")
	s.say("!!! Keep the unit powered and ask for help. The log: %s", r.res.Log)
	return fmt.Errorf("%w: %s: %s", device.ErrABUnknown, r.kind, why)
}

// confirm: --yes, or REPAIR typed. From here to the end, ^C, a hangup,
// SIGTERM and SIGPIPE are ignored (as in the install): only SIGKILL and a
// power cut can stop uc-pxx-inst while the tool writes.
func (r *repairRun) confirm() error {
	s := r.s
	if !r.o.Yes {
		s.say("")
		s.say("This writes the A/B sector: only slot %s will boot. Slot %s is disabled.", strings.ToUpper(r.p.Keeper), strings.ToUpper(r.p.Target))
		fmt.Fprintf(s.out, "Type REPAIR and press Enter to write, anything else to stop: ")
		if !confirmed(r.o.Confirm, "REPAIR") {
			return r.refuse("not confirmed")
		}
	}
	sigs := []os.Signal{syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGPIPE}
	signal.Ignore(sigs...)
	return nil
}

// confirmed reads one line and compares it with the word.
func confirmed(in io.Reader, word string) bool {
	line := ""
	if in != nil {
		line, _ = bufio.NewReader(in).ReadString('\n')
	}
	return strings.TrimSpace(line) == word
}

// sha256hex of a sector, for the log.
func sha256hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
