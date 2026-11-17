package install

// The executor of a clone plan (plan.ComputeClone, DESIGN-R23 §4.3). The
// running slot K is the keeper; the plan makes the other slot T a copy of
// K. It runs the steps in order on a Medium and reuses unchanged: Run's A/B
// step (bootctlStep) and check (checkAB), the chunker. What it adds:
//
//   - the chain writes take the first bytes of K's partition of the same
//     name (the clone gate's pins, checked again here and while written);
//   - copy-frozen: the run's own output (Say, Log) goes to a memory buffer,
//     CloneOptions.Freeze freezes K's root file system, system_K is read in
//     clone.BlockSize blocks through the medium's reader (O_DIRECT on a
//     unit), hashed and written to system_T, then the root is thawed and
//     the buffer flushed. A thaw guard thaws after the plan's bound (at most
//     clone.FreezeBound) or on any error; when it fires, the copy stops with
//     an error (a copy past the thaw is not of a frozen file system). The
//     frozen time is measured, said and logged;
//   - the verify of system_T compares with the sha256 of the copy's reads;
//   - delta and content-check run through the caller's hooks (the mount of
//     system_T and the e2fsck are the caller's: this package has no code
//     path to a block device); a forbidden file in system_T stops the run
//     (mainline-rc-10b: the content digest is logged, not compared with ref_a).
//
// Any error stops at once, before the arm: T stays {0,0,0} (or untouched
// before the disable), so the keeper boots and keeps everything.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/clone"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/plan"
)

// CloneOptions are the options of a clone run.
type CloneOptions struct {
	Options
	// Data runs data-format and data-migrate (as OtherSlotOptions.Data).
	Data func(op string, d *plan.DataPlan) error
	// Gate is the clone gate; the precheck checks the plan against it again.
	Gate *clone.Gate
	// BootID: as OtherSlotOptions.BootID.
	BootID    string
	KeyPins   []plan.KeyPin
	KeyPinSet string
	// Freeze freezes the keeper's root file system and returns the thaw. The
	// executor calls the thaw once (the guard or the end of the copy).
	Freeze func() (thaw func() error, err error)
	// Delta mounts system_T, applies the plan's difference, unmounts it and
	// runs e2fsck -fn (exit 0 required).
	Delta func(c *plan.ClonePart) error
	// Content builds the content manifest of system_T (read only) and
	// returns its sha256; a forbidden file is its error (mainline-rc-10b: the
	// sha256 is logged, not compared with ref_a).
	Content func(c *plan.ClonePart) (string, error)
	// Now is the clock of the freeze measurement (nil: time.Now).
	Now func() time.Time
}

// CloneResult is the end of a clone run.
type CloneResult struct {
	*Result
	// SystemSHA256 is the sha256 of the copy's reads of system_K; After the
	// sha256 of system_T after the difference.
	SystemSHA256 string
	After        string
	// Frozen is how long K's root was frozen; Guard: the thaw guard fired.
	Frozen time.Duration
	Guard  bool
}

type cloneRunner struct {
	*runner
	cp     *plan.OtherSlotPlan
	opt    CloneOptions
	sysSHA string
	frozen time.Duration
	guard  bool
	after  string
}

// CloneStructure is the exact shape of a plan of plan.ComputeClone:
// disable --slot T; the five chain writes of T (write or skip); copy-frozen
// system_T; sync; verify of the six; delta; content-check; with data,
// data-format and data-migrate; arm-trying --slot T --lower-keeper;
// check-ab; the A/B states as OtherSlotStructure's.
func CloneStructure(p *plan.OtherSlotPlan) error {
	if p.Running.Slot != "a" && p.Running.Slot != "b" || p.Keeper != p.Running.Slot || p.Target == p.Keeper ||
		(p.Target != "a" && p.Target != "b") {
		return fmt.Errorf("keeper %q target %q running slot %q: the keeper must be the running slot and the target the other", p.Keeper,
			p.Target, p.Running.Slot)
	}
	k, t := slotIndex(p.Keeper), slotIndex(p.Target)
	data := p.Data != nil
	parts := plan.CloneParts()
	want := len(parts) + 7
	if data {
		want += 2
	}
	if len(p.Steps) != want {
		return fmt.Errorf("%d steps, a clone plan has %d", len(p.Steps), want)
	}
	for i, s := range p.Steps {
		if s.N != i+1 || s.KeepKey != nil {
			return fmt.Errorf("step %d: number %d or a keep-key part", i+1, s.N)
		}
	}
	now, err := p.AB.Now.Raw()
	if err != nil {
		return fmt.Errorf("the plan's A/B start: %v", err)
	}
	s1, err1 := plan.PredictDisable(now, t)
	s2, err2 := plan.PredictArm(s1, t, true)
	if err1 != nil || err2 != nil {
		return fmt.Errorf("the A/B predictions: %v %v", err1, err2)
	}
	a0, _ := avbab.Parse(now[:])
	a1, _ := avbab.Parse(s1[:])
	a2, _ := avbab.Parse(s2[:])
	switch {
	case a0.Validate() != nil || !a0.Slots[k].Keeper() || a0.Tie() || a0.Pick() != k:
		return errors.New("at the start the keeper is not the successful slot that boots now")
	case a1.Validate() != nil || !a1.Slots[k].Keeper() || a1.Slots[t].Bootable() || a1.Pick() != k:
		return errors.New("after the disable the keeper would not be the only bootable slot")
	case a2.Validate() != nil || a2.Slots[t] != (avbab.Slot{Priority: 15, Tries: 7}) || !a2.Slots[k].Keeper() || a2.Pick() != t:
		return errors.New("after the arm the target would not be {15,7,0} next with the keeper successful")
	}
	same := func(st plan.ABState, w [32]byte) bool {
		raw, err := st.Raw()
		return err == nil && raw == w
	}
	argv := func(s plan.Step, w ...string) bool {
		return s.Op == "bootctl" && s.Bootctl != nil && s.Write == nil && strings.Join(s.Bootctl.Args, " ") == strings.Join(w, " ")
	}
	plain := func(s plan.Step, op string) bool {
		return s.Op == op && s.Bootctl == nil && s.Write == nil && s.Expect == nil && len(s.Verify) == 0
	}
	i := 0
	if d := p.Steps[i]; !argv(d, "disable", "--slot", p.Target) || !same(d.Bootctl.Before, now) || !same(d.Bootctl.After, s1) || d.Bootctl.NoOp != (s1 == now) {
		return fmt.Errorf("step 1 is not disable --slot %s with the predicted states", p.Target)
	}
	i++
	var names []string
	for _, part := range parts {
		s := p.Steps[i]
		name := part + "_" + p.Target
		switch {
		case s.Write == nil || s.Write.Partition != name || s.Write.Offset != 0 || s.Bootctl != nil:
			return fmt.Errorf("step %d is not the write of %s", s.N, name)
		case part == "system" && s.Op == plan.OpCopyFrozen && s.Write.Action == plan.OpCopyFrozen && s.Write.SHA256 == "":
		case part != "system" && s.Op == "write" && s.Write.Action == "write":
		case part != "system" && s.Op == "skip" && s.Write.Action == "skip-identical":
		default:
			return fmt.Errorf("step %d (%s): op %q action %q", s.N, name, s.Op, s.Write.Action)
		}
		names = append(names, name)
		i++
	}
	if !plain(p.Steps[i], "sync") {
		return errors.New("no sync after the writes")
	}
	i++
	if v := p.Steps[i]; v.Op != "verify" || strings.Join(v.Verify, " ") != strings.Join(names, " ") {
		return errors.New("the verify step does not cover exactly the six partitions of the target")
	}
	i++
	if !plain(p.Steps[i], plan.OpDelta) || !plain(p.Steps[i+1], plan.OpContentCheck) {
		return errors.New("the two steps after the verify are not delta and content-check")
	}
	i += 2
	if data {
		if !plain(p.Steps[i], plan.OpDataFormat) || !plain(p.Steps[i+1], plan.OpDataMigrate) {
			return errors.New("the two steps after the content check are not data-format and data-migrate")
		}
		i += 2
	}
	if a := p.Steps[i]; !argv(a, "arm-trying", "--slot", p.Target, "--lower-keeper") || !same(a.Bootctl.Before, s1) ||
		!same(a.Bootctl.After, s2) || a.Bootctl.NoOp {
		return fmt.Errorf("the step before the last is not arm-trying --slot %s --lower-keeper with the predicted states", p.Target)
	}
	i++
	if c := p.Steps[i]; c.Op != "check-ab" || c.Expect == nil || !same(*c.Expect, s2) || !same(p.AB.Final, s2) {
		return errors.New("the last step is not check-ab of the predicted final state")
	}
	return nil
}

// cloneSourceRule: a clone writes only the six partitions of the target,
// uboot … boot only from the keeper's partition of the same name with the
// gate's pin, system only from the whole system_K (copy-frozen).
func cloneSourceRule(src *plan.WriteSource, target, keeper string, g *clone.Gate, sysBytes int64) error {
	part, ok := strings.CutSuffix(src.Partition, "_"+target)
	in := false
	for _, n := range plan.CloneParts() {
		in = in || ok && part == n
	}
	if !in {
		return fmt.Errorf("%s is not a partition that a clone writes", src.Partition)
	}
	if src.KeeperPartition != part+"_"+keeper {
		return fmt.Errorf("%s: the source %s is not %s_%s", src.Partition, src.KeeperPartition, part, keeper)
	}
	if part == "system" {
		if src.From != plan.CloneSourceFrozen || src.SHA256 != "" || src.Length != sysBytes || sysBytes <= 0 {
			return fmt.Errorf("%s: source %q of %d bytes, a clone takes the whole %s (%d bytes) while it is frozen", src.Partition, src.From,
				src.Length, src.KeeperPartition, sysBytes)
		}
		return nil
	}
	pin, ok := g.Pin(part)
	if src.From != "keeper" || !ok || src.Length != pin.Size || src.SHA256 != pin.SHA256 {
		return fmt.Errorf("%s: source %q %d bytes %s, the clone gate pins %d bytes %s", src.Partition, src.From, src.Length, src.SHA256, pin.Size, pin.SHA256)
	}
	return nil
}

func (r *cloneRunner) precheck() error {
	p, cp, g := r.cp, r.cp.Clone, r.opt.Gate
	switch {
	case p.ID == "" || !p.VerifyID():
		return errors.New("the plan has no valid id (a refused or edited plan)")
	case p.Kind != plan.CloneKind || cp == nil || p.Payload != nil || p.Mainline != nil:
		return errors.New("not a clone plan")
	case p.Mode != r.m.Kind():
		return fmt.Errorf("plan mode %q: this medium runs only %q plans", p.Mode, r.m.Kind())
	case r.opt.MaxWrite < 0 || r.opt.MaxWrite%gpt.Sector != 0:
		return fmt.Errorf("MaxWrite %d is not a multiple of 512", r.opt.MaxWrite)
	case r.opt.KeyRecord != nil:
		return errors.New("a clone has no keep-key step")
	case g == nil:
		return errors.New("no clone gate is given")
	case r.opt.Freeze == nil || r.opt.Delta == nil || r.opt.Content == nil:
		return errors.New("a clone needs the freeze, the difference and the content check of its caller")
	case p.Data != nil && r.opt.Data == nil:
		return errors.New("the plan has data steps, but no data hook is given")
	case p.Data != nil && (p.Data.Partition != plan.DataPartition || p.Data.Label != plan.DataLabel || p.Data.FSType != "ext4" ||
		p.Data.FormatAs != plan.DataFormatLabel):
		return fmt.Errorf("the data step is for %s %s %s/%s", p.Data.Partition, p.Data.FSType, p.Data.FormatAs, p.Data.Label)
	case p.Mode == "slot" && (r.opt.BootID == "" || r.opt.BootID != p.Running.BootID):
		return fmt.Errorf("the plan is of boot %q, this boot is %q: make a new plan", p.Running.BootID, r.opt.BootID)
	case r.opt.BootID != "" && p.Running.BootID != "" && r.opt.BootID != p.Running.BootID:
		return fmt.Errorf("the plan is of boot %q, this boot is %q: make a new plan", p.Running.BootID, r.opt.BootID)
	}
	// the plan against the gate again (the plan's records are not trusted)
	if err := g.Check(); err != nil {
		return fmt.Errorf("the clone gate: %w", err)
	}
	pr, ok := g.Profile(cp.Profile)
	switch {
	case cp.Gate != g.Name || g.Name != "embedded" && g.Name != "test":
		return fmt.Errorf("the plan was made with the clone gate %q, this run has %q", cp.Gate, g.Name)
	case !ok || pr.RefA != cp.RefA || pr.RefB != cp.RefB || pr.BuildID != cp.BuildID || cp.Done.RefB != pr.RefB || cp.Done.BundleSHA256 != pr.BundleSHA256:
		return fmt.Errorf("the plan's profile %q is not the clone gate's", cp.Profile)
	case cp.KeeperContent != "" || cp.KeeperEntries != 0:
		return errors.New("the plan has a keeper content: not a clone plan of this release (mainline-rc-10b)")
	case cp.DeltaSHA256 != g.DeltaSHA256() || cp.Delta == nil || !cp.Delta.Equal(g.Delta):
		return errors.New("the plan's difference is not the clone gate's")
	case cp.ExcludeSHA256 != g.ExcludeSHA256() || strings.Join(cp.Exclude, "\n") != strings.Join(g.Exclude, "\n"):
		return errors.New("the plan's exclusion list is not the clone gate's")
	case cp.FreezeBoundMS != g.FreezeBound.Milliseconds() || cp.FreezeBoundMS <= 0 || time.Duration(cp.FreezeBoundMS)*time.Millisecond > clone.FreezeBound:
		return fmt.Errorf("the plan's freeze bound %d ms is not the clone gate's %s (at most %s)", cp.FreezeBoundMS, g.FreezeBound, clone.FreezeBound)
	case cp.BlockSize != clone.BlockSize || cp.MountPoint != clone.MountPoint || cp.AttemptKey != clone.AttemptKey(cp.RefA):
		return errors.New("the plan's block size, mount point or attempt key is not this release's")
	}
	for _, f := range g.Forbidden {
		found := false
		for _, q := range cp.Forbidden {
			found = found || q == f
		}
		if !found {
			return fmt.Errorf("the plan does not forbid %s", f.What)
		}
	}
	if err := CloneStructure(p); err != nil {
		return fmt.Errorf("the plan is not the shape of a clone plan: %w", err)
	}
	if err := cloneChecked(p); err != nil {
		return err
	}
	writes := 0
	for _, s := range p.Steps {
		if s.Write == nil {
			continue
		}
		writes++
		src := p.Source(s.Write.Partition)
		if src == nil || src.Length != s.Write.Length || src.SHA256 != s.Write.SHA256 || src.From != s.Write.SegID {
			return fmt.Errorf("step %d (%s): no matching write source in the plan", s.N, s.Write.Partition)
		}
		if err := cloneSourceRule(src, p.Target, p.Keeper, g, cp.SystemBytes); err != nil {
			return fmt.Errorf("step %d: %w", s.N, err)
		}
	}
	if writes != len(p.Sources) {
		return fmt.Errorf("%d write steps, %d write sources", writes, len(p.Sources))
	}
	t, err := r.m.Prepare(&p.Plan, r.opt.Hooks)
	if err != nil {
		return err
	}
	r.t = t
	sk, ok1 := t.Find("system_" + p.Keeper)
	st, ok2 := t.Find("system_" + p.Target)
	if !ok1 || !ok2 || sk.Bytes() != cp.SystemBytes || st.Bytes() != cp.SystemBytes {
		return fmt.Errorf("system_%s and system_%s are not both %d bytes", p.Keeper, p.Target, cp.SystemBytes)
	}
	pins, set := r.opt.KeyPins, r.opt.KeyPinSet
	if pins == nil {
		pins, set = plan.ReleaseKeyPins(), plan.KeyPinSetRelease
	}
	k := plan.ReadKeyGate(r.m.Reader(), t)
	plan.MatchKeyPin(&k, pins, set)
	if k.Copies != 2 || !k.Equal || k.LBAs != p.Key.LBAs {
		return fmt.Errorf("the firmware key is not kept on the unit now: %s", k.Why)
	}
	if k.Pin == "" || k.RecordSHA256 != p.Key.RecordSHA256 || k.Pin != p.Key.Pin || k.PinSet != p.Key.PinSet {
		return errors.New("the kept key record now is not the plan's pinned record")
	}
	if p.Data != nil {
		e, ok := t.Find(plan.DataPartition)
		if !ok || e.Index != p.Data.GPTIndex || e.Start != p.Data.StartLBA || e.Sectors() != p.Data.Sectors {
			return errors.New("the data partition is not the one the plan names")
		}
		if now := plan.DataState(r.m.Reader(), e.Start*gpt.Sector); now != p.Data.Before || now == "ext4 label "+plan.DataLabel {
			return fmt.Errorf("userdata holds %s now, the plan saw %s: make a new plan", now, p.Data.Before)
		}
	}
	sec, err := r.m.ReadAB()
	if err != nil {
		return err
	}
	now, _ := p.AB.Now.Raw()
	if !bytes.Equal(sec[:32], now[:]) {
		return fmt.Errorf("the A/B sector is %x, the plan starts from %x", sec[:32], now)
	}
	return nil
}

// cloneChecked: every check passed, the confirmation, the kept key, the
// keeper hashes and the boot id of a slot plan.
func cloneChecked(p *plan.OtherSlotPlan) error {
	if len(p.Checks) == 0 {
		return errors.New("the plan has no checks")
	}
	for _, c := range p.Checks {
		if !c.OK {
			return fmt.Errorf("the plan's check %s failed: %s", c.ID, c.Detail)
		}
	}
	switch {
	case !p.Confirm.Confirmed:
		return errors.New("the plan has no confirmation")
	case p.Key.Copies != 2 || !p.Key.Equal || p.Key.Pin == "" || len(p.Key.RecordSHA256) != 64:
		return errors.New("the plan does not show the firmware key kept on the unit as a record this release knows")
	case p.Mode == "slot" && len(p.KeeperSHA256) != len(plan.KeeperStaticParts()):
		return errors.New("a slot plan has no keeper hashes")
	case p.Mode == "slot" && p.Running.BootID == "":
		return errors.New("a slot plan has no boot id")
	}
	for _, part := range plan.KeeperStaticParts() {
		if len(p.KeeperSHA256) > 0 && len(p.KeeperSHA256[part+"_"+p.Keeper]) != 64 {
			return fmt.Errorf("the plan has no keeper hash of %s_%s", part, p.Keeper)
		}
	}
	return nil
}

// keeperPart reads the first n bytes of the keeper's partition.
func (r *cloneRunner) keeperPart(name string, n int64) (io.Reader, error) {
	e, ok := r.t.Find(name)
	if !ok || n > e.Bytes() {
		return nil, fmt.Errorf("no keeper partition %s of at least %d bytes", name, n)
	}
	return io.NewSectionReader(r.m.Reader(), e.Start*gpt.Sector, n), nil
}

// write writes one chain part from the keeper's partition (through the
// chunker), with the length and the sha256 checked.
func (r *cloneRunner) write(s plan.Step) error {
	w := s.Write
	src := r.cp.Source(w.Partition)
	if src == nil {
		return fmt.Errorf("%s: no write source", w.Partition)
	}
	if err := cloneSourceRule(src, r.p.Target, r.p.Keeper, r.opt.Gate, r.cp.Clone.SystemBytes); err != nil || src.From != "keeper" {
		return fmt.Errorf("write refused: %v (source %q)", err, src.From)
	}
	if err := plan.CheckWriteRange(r.t, r.p.Target, w.Partition, w.Offset, w.Length); err != nil {
		return fmt.Errorf("write refused: %w", err)
	}
	in, err := r.keeperPart(src.KeeperPartition, src.Length)
	if err != nil {
		return err
	}
	pw, err := r.m.OpenWrite(w)
	if err != nil {
		return fmt.Errorf("%s: %w", w.Partition, err)
	}
	max := r.opt.MaxWrite
	if max == 0 {
		max = 1 << 20
	}
	c := &chunker{w: pw, h: sha256.New(), max: max}
	n, werr := io.CopyBuffer(c, io.LimitReader(in, w.Length), make([]byte, 1<<20))
	cerr := pw.Close()
	if werr != nil {
		return fmt.Errorf("%s: %w", w.Partition, werr)
	}
	if cerr != nil {
		return fmt.Errorf("%s: %w", w.Partition, cerr)
	}
	if got := hex.EncodeToString(c.h.Sum(nil)); n != w.Length || c.n != w.Length || got != w.SHA256 {
		return fmt.Errorf("%s: wrote %d bytes sha256 %s, the plan says %d bytes %s", w.Partition, n, got, w.Length, w.SHA256)
	}
	return nil
}

// frozenOut holds the run's output while the root is frozen.
type frozenOut struct {
	mu    sync.Mutex
	lines []string
	log   bytes.Buffer
}

func (f *frozenOut) say(l string) { f.mu.Lock(); f.lines = append(f.lines, l); f.mu.Unlock() }
func (f *frozenOut) Write(b []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.log.Write(b)
}

// copyFrozen is the copy-frozen step (see the file comment).
func (r *cloneRunner) copyFrozen(s plan.Step) (err error) {
	w, cp := s.Write, r.cp.Clone
	src := r.cp.Source(w.Partition)
	if src == nil {
		return fmt.Errorf("%s: no write source", w.Partition)
	}
	if err := cloneSourceRule(src, r.p.Target, r.p.Keeper, r.opt.Gate, cp.SystemBytes); err != nil || src.From != plan.CloneSourceFrozen {
		return fmt.Errorf("write refused: %v (source %q)", err, src.From)
	}
	if err := plan.CheckWriteRange(r.t, r.p.Target, w.Partition, 0, w.Length); err != nil {
		return fmt.Errorf("write refused: %w", err)
	}
	in, err := r.keeperPart(src.KeeperPartition, w.Length)
	if err != nil {
		return err
	}
	now := r.opt.Now
	if now == nil {
		now = time.Now
	}
	bound := time.Duration(cp.FreezeBoundMS) * time.Millisecond
	if bound <= 0 || bound > clone.FreezeBound {
		return fmt.Errorf("freeze bound %s (0 < bound <= %s)", bound, clone.FreezeBound)
	}
	pw, err := r.m.OpenWrite(w)
	if err != nil {
		return fmt.Errorf("%s: %w", w.Partition, err)
	}
	// from here no write of this run goes to the keeper's root: the output
	// waits in memory until the thaw
	buf := &frozenOut{}
	// the embedded runner's own options: r.say and r.log read them (clone
	// review MINOR-3: r.opt is the clone options, a separate copy)
	say, log := r.runner.opt.Say, r.runner.opt.Log
	r.runner.opt.Say, r.runner.opt.Log = buf.say, buf
	var fired atomic.Bool
	var thawOnce sync.Once
	var thawErr error
	var thaw func() error
	var start, thawedAt time.Time
	frozen := false
	doThaw := func() {
		thawOnce.Do(func() {
			// a frozen root stops the system: try again (as the journal
			// checkpoint of step 2 does)
			for i := 0; thaw != nil && i < 3; i++ {
				if thawErr = thaw(); thawErr == nil {
					break
				}
			}
			thawedAt = now()
		})
	}
	defer func() {
		if frozen {
			doThaw()
			r.frozen = thawedAt.Sub(start)
		}
		cerr := pw.Close()
		r.runner.opt.Say, r.runner.opt.Log = say, log
		for _, l := range buf.lines {
			r.say("%s", l)
		}
		if log != nil && buf.log.Len() > 0 {
			log.Write(buf.log.Bytes())
		}
		r.guard = fired.Load()
		r.say("     the root file system of slot %s was frozen for %.1f s (bound %s, thaw guard fired %v)", strings.ToUpper(r.p.Keeper),
			r.frozen.Seconds(), bound, r.guard)
		r.log(map[string]any{"event": "freeze", "frozen_ms": r.frozen.Milliseconds(), "bound_ms": bound.Milliseconds(), "guard": r.guard,
			"thaw_error": errString(thawErr), "copy_sha256": r.sysSHA})
		if err == nil && cerr != nil {
			err = fmt.Errorf("%s: %w", w.Partition, cerr)
		}
		if thawErr != nil {
			err = fmt.Errorf("the thaw of the root file system failed three times (%v): the root of slot %s may stay frozen; a power cycle boots slot %s (slot %s is disabled): %v",
				thawErr, strings.ToUpper(r.p.Keeper), strings.ToUpper(r.p.Keeper), strings.ToUpper(r.p.Target), err)
		}
		if r.guard {
			err = fmt.Errorf("the freeze reached its bound of %s: the thaw guard thawed the root; the copy is not of a frozen file system (%v)", bound, err)
		}
	}()
	t0 := now() // the frozen time counts from the FIFREEZE call (its sync included; clone review NOTE-6)
	th, ferr := r.opt.Freeze()
	if ferr != nil {
		return fmt.Errorf("the freeze of the root file system of slot %s: %w", strings.ToUpper(r.p.Keeper), ferr)
	}
	thaw, frozen, start = th, true, t0
	timer := time.AfterFunc(bound, func() { fired.Store(true); doThaw() })
	defer timer.Stop()
	max := r.opt.MaxWrite
	if max == 0 {
		max = 1 << 20
	}
	c := &chunker{w: pw, h: sha256.New(), max: max}
	rh := sha256.New()
	block := make([]byte, cp.BlockSize)
	var done int64
	for done < w.Length {
		if fired.Load() {
			return errors.New("stopped at the freeze bound")
		}
		k := int64(len(block))
		if left := w.Length - done; left < k {
			k = left
		}
		if _, err := io.ReadFull(in, block[:k]); err != nil {
			return fmt.Errorf("read %s at %d: %w", src.KeeperPartition, done, err)
		}
		rh.Write(block[:k])
		if _, err := c.Write(block[:k]); err != nil {
			return fmt.Errorf("%s at %d: %w", w.Partition, done, err)
		}
		done += k
	}
	timer.Stop()
	if fired.Load() {
		return errors.New("stopped at the freeze bound")
	}
	doThaw()
	r.frozen = thawedAt.Sub(start)
	frozen = false
	read, wrote := hex.EncodeToString(rh.Sum(nil)), hex.EncodeToString(c.h.Sum(nil))
	if c.n != w.Length || read != wrote {
		return fmt.Errorf("%s: wrote %d bytes sha256 %s, read %d bytes %s", w.Partition, c.n, wrote, done, read)
	}
	r.sysSHA = read
	return nil
}

// RunClone runs a clone plan against a medium. On any error it stops at
// once: the target then stays disabled (or was never touched), so the
// running slot boots.
func RunClone(p *plan.OtherSlotPlan, m Medium, opt CloneOptions) (res *CloneResult, err error) {
	base := &runner{p: &p.Plan, m: m, opt: opt.Options}
	r := &cloneRunner{runner: base, cp: p, opt: opt}
	out := func(steps int) *CloneResult {
		return &CloneResult{Result: &Result{Writes: m.Writes(), Steps: steps}, SystemSHA256: r.sysSHA, After: r.after, Frozen: r.frozen, Guard: r.guard}
	}
	defer func() {
		if ferr := m.Finish(); ferr != nil && err == nil {
			err = ferr
		}
	}()
	if err := r.precheck(); err != nil {
		return nil, fmt.Errorf("precheck: %w", err)
	}
	r.log(map[string]any{"event": "start", "plan": p.ID, "kind": p.Kind, "mode": p.Mode, "source": p.Disk.Source, "ab": r.ab32(),
		"keeper": p.Keeper, "target": p.Target, "profile": p.Clone.Profile, "ref_a": p.Clone.RefA})
	writes := map[string]*plan.WriteStep{}
	for _, s := range p.Steps {
		if s.Write != nil {
			writes[s.Write.Partition] = s.Write
		}
	}
	for _, s := range p.Steps {
		m.SetStep(s.N)
		if opt.Guard != nil {
			if gerr := opt.Guard(s); gerr != nil {
				r.say("%2d/%d STOP before %s: %v", s.N, len(p.Steps), s.Op, gerr)
				r.log(map[string]any{"step": s.N, "op": s.Op, "guard": gerr.Error(), "ab_before": r.ab32(), "writes": m.Writes()})
				return out(s.N - 1), fmt.Errorf("step %d (%s): stopped before the step: %w", s.N, s.Op, gerr)
			}
		}
		start := time.Now()
		before := r.ab32()
		var err error
		switch s.Op {
		case "bootctl":
			r.say("%2d/%d bootctl %s", s.N, len(p.Steps), strings.Join(s.Bootctl.Args, " "))
			err = r.bootctlStep(s)
		case plan.OpDataFormat, plan.OpDataMigrate:
			if p.Data == nil || opt.Data == nil {
				err = errors.New("a data step without the plan's data part or the hook")
				break
			}
			r.say("%2d/%d %s %s", s.N, len(p.Steps), s.Op, p.Data.Partition)
			err = opt.Data(s.Op, p.Data)
		case "write":
			r.say("%2d/%d write %s from %s (%d bytes)", s.N, len(p.Steps), s.Write.Partition, r.cp.Source(s.Write.Partition).KeeperPartition, s.Write.Length)
			err = r.write(s)
		case "skip":
			r.say("%2d/%d skip %s (already holds the image)", s.N, len(p.Steps), s.Write.Partition)
			var h string
			if h, err = m.Verify(s.Write); err == nil && h != s.Write.SHA256 {
				err = fmt.Errorf("%s changed since the plan: sha256 %s", s.Write.Partition, h)
			}
		case plan.OpCopyFrozen:
			r.say("%2d/%d copy-frozen %s from %s (%d bytes, the root of slot %s frozen)", s.N, len(p.Steps), s.Write.Partition,
				r.cp.Source(s.Write.Partition).KeeperPartition, s.Write.Length, strings.ToUpper(p.Keeper))
			err = r.copyFrozen(s)
		case "sync":
			r.say("%2d/%d sync", s.N, len(p.Steps))
			var names []string
			for _, st := range p.Steps {
				if st.Op == "write" || st.Op == plan.OpCopyFrozen {
					names = append(names, st.Write.Partition)
				}
			}
			err = m.Sync(names)
		case "verify":
			r.say("%2d/%d verify", s.N, len(p.Steps))
			for _, name := range s.Verify {
				w := writes[name]
				want := ""
				switch {
				case w == nil:
				case w.Action == plan.OpCopyFrozen:
					want = r.sysSHA
				default:
					want = w.SHA256
				}
				if want == "" {
					err = fmt.Errorf("verify: %s has no known sha256", name)
					break
				}
				var h string
				if h, err = m.Verify(w); err == nil && h != want {
					err = fmt.Errorf("verify: %s reads back sha256 %s, want %s", name, h, want)
				}
				if err != nil {
					break
				}
				r.say("     %-9s ok  %s", name, h[:16])
			}
		case plan.OpDelta:
			r.say("%2d/%d delta: clone-delta.json %.16s on system_%s", s.N, len(p.Steps), p.Clone.DeltaSHA256, p.Target)
			if err = opt.Delta(p.Clone); err == nil {
				e, _ := r.t.Find("system_" + p.Target)
				if r.after, err = m.HashRange(e.Start*gpt.Sector, e.Bytes()); err == nil {
					r.say("     system_%s after the difference: sha256 %s", p.Target, r.after)
					r.log(map[string]any{"event": "delta", "system_after_sha256": r.after})
				}
			}
		case plan.OpContentCheck:
			// mainline-rc-10b: the forbidden-file search of system_T; its
			// content manifest is logged, not compared with ref_a
			r.say("%2d/%d content-check system_%s: no forbidden file (the content manifest is recorded, not compared)", s.N, len(p.Steps), p.Target)
			var got string
			if got, err = opt.Content(p.Clone); err == nil {
				r.log(map[string]any{"event": "content", "system_content_sha256": got, "ref_a": p.Clone.RefA})
			}
		case "check-ab":
			r.say("%2d/%d check the A/B sector", s.N, len(p.Steps))
			err = r.checkAB(s)
		default:
			err = fmt.Errorf("step %q is not a step of a clone plan", s.Op)
		}
		r.log(map[string]any{"step": s.N, "op": s.Op, "ab_before": before, "ab_after": r.ab32(),
			"writes": m.Writes(), "ms": time.Since(start).Milliseconds(), "error": errString(err)})
		if err != nil {
			return out(s.N - 1), fmt.Errorf("step %d (%s): %w", s.N, s.Op, err)
		}
	}
	if err := r.keeperStatic(); err != nil {
		return out(len(p.Steps)), err
	}
	res = out(len(p.Steps))
	res.FinalAB = p.AB.Final
	return res, nil
}

// keeperStatic hashes the keeper's static parts again (as osRunner's).
func (r *cloneRunner) keeperStatic() error {
	if len(r.p.KeeperSHA256) == 0 {
		return nil
	}
	for _, part := range plan.KeeperStaticParts() {
		name := part + "_" + r.p.Keeper
		e, ok := r.t.Find(name)
		want := r.p.KeeperSHA256[name]
		if !ok || want == "" {
			return fmt.Errorf("keeper check: no %s", name)
		}
		got, err := r.m.HashRange(e.Start*gpt.Sector, e.Bytes())
		if err != nil {
			return fmt.Errorf("keeper check: %s: %w", name, err)
		}
		if got != want {
			return fmt.Errorf("keeper check: %s changed (sha256 %s, was %s)", name, got, want)
		}
	}
	r.say("     keeper slot %s: %s unchanged (sha256)", strings.ToUpper(r.p.Keeper), strings.Join(plan.KeeperStaticParts(), ", "))
	r.log(map[string]any{"event": "keeper-unchanged", "slot": r.p.Keeper, "parts": plan.KeeperStaticParts()})
	return nil
}

// ExecuteClone runs a clone plan against an image file (the dry run).
func ExecuteClone(p *plan.OtherSlotPlan, im *Image, opt CloneOptions) (*CloneResult, error) {
	return RunClone(p, &dryMedium{im: im, tool: opt.Bootctl}, opt)
}
