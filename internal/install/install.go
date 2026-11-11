package install

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"
	"time"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/bootctl"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/keykeep"
	"uc-pxx-installer/internal/plan"
)

// Options are the run options.
type Options struct {
	Bootctl bootctl.Tool // the dry run only: the tool that works on a sector copy
	Say     func(string) // progress lines for a human (may be nil)
	Log     io.Writer    // JSON lines (may be nil)
	Hooks   *Hooks       // tests only
	// MaxWrite splits the partition data into writes of at most this many
	// bytes (a multiple of 512). 0 = as the decrypter gives them (1 MiB).
	MaxWrite int
	// Guard, if set, runs before every step. It can only stop the run: an
	// error stops it before that step, as a failed step does, so the target
	// stays disabled (or was never touched) and the keeper boots. It is not
	// a test hook: the device mode sets it for the drive rule of the
	// on-screen flow (the UC-PXX-INST drive stays detached and unchanged).
	Guard func(s plan.Step) error
	// KeyRecord is the key record of a plan with a keep-key step (package
	// keykeep): the caller builds it from the key in use. Its sha256 must be
	// the step's. It is never logged.
	KeyRecord []byte
}

// Result is what a run ends with.
type Result struct {
	Writes  int
	FinalAB plan.ABState
	Steps   int
}

type runner struct {
	p   *plan.Plan
	pl  *plan.Payload
	m   Medium
	t   *gpt.Table
	opt Options
}

func (r *runner) say(f string, a ...any) {
	if r.opt.Say != nil {
		r.opt.Say(fmt.Sprintf(f, a...))
	}
}

func (r *runner) log(kv map[string]any) {
	if r.opt.Log == nil {
		return
	}
	kv["time"] = time.Now().UTC().Format(time.RFC3339Nano)
	b, _ := json.Marshal(kv)
	r.opt.Log.Write(append(b, '\n'))
}

func (r *runner) ab32() string {
	b, err := r.m.ReadAB()
	if err != nil {
		return "unreadable: " + err.Error()
	}
	return hex.EncodeToString(b[:32])
}

// SafeState says what the image's A/B sector means for a stop: whether the
// keeper boots next and the target is not bootable.
func SafeState(im *Image, p *plan.Plan) string {
	b, err := im.readAB()
	return SafeStateOf(b, err, p)
}

// SafeStateOf is SafeState for a sector read from any medium.
func SafeStateOf(b []byte, err error, p *plan.Plan) string {
	if err != nil {
		return "A/B sector unreadable: " + err.Error()
	}
	a, _ := avbab.Parse(b)
	if err := a.Validate(); err != nil {
		return "A/B sector NOT VALID: " + err.Error()
	}
	var raw [32]byte
	copy(raw[:], b)
	st := plan.State(raw)
	k := 0
	if p.Keeper == "b" {
		k = 1
	}
	switch {
	case a.Pick() == k && a.Slots[k].Keeper() && !a.Slots[1-k].Bootable():
		return fmt.Sprintf("%s: the target is disabled and the keeper boots; safe to power off", st)
	case a.Pick() == k && a.Slots[k].Keeper():
		return fmt.Sprintf("%s: the keeper boots next; nothing of the target was armed", st)
	case a.Pick() == 1-k && a.Slots[k].Keeper():
		return fmt.Sprintf("%s: the target boots next, the keeper is the fallback", st)
	}
	return fmt.Sprintf("%s: UNEXPECTED state", st)
}

// chunker splits the decrypter's output into writes of at most max bytes
// and hashes what it passes on.
type chunker struct {
	w   PartWriter
	h   hash.Hash
	max int
	n   int64
}

func (c *chunker) Write(b []byte) (int, error) {
	done := 0
	for done < len(b) {
		n := len(b) - done
		if c.max > 0 && n > c.max {
			n = c.max
		}
		k, err := c.w.Write(b[done : done+n])
		c.h.Write(b[done : done+k])
		c.n += int64(k)
		done += k
		if err != nil {
			return done, err
		}
		if k != n {
			return done, io.ErrShortWrite
		}
	}
	return done, nil
}

// precheck checks the plan itself, then lets the medium check that nothing
// changed since the plan was made.
func (r *runner) precheck() error {
	p := r.p
	if p.ID == "" || !p.VerifyID() {
		return errors.New("the plan has no valid id (a refused or edited plan)")
	}
	if p.Mode != r.m.Kind() {
		return fmt.Errorf("plan mode %q: this medium runs only %q plans", p.Mode, r.m.Kind())
	}
	if r.pl == nil || r.pl.File() == nil || r.pl.FirmwareSHA256 != p.Payload.FirmwareSHA256 {
		return errors.New("the payload is not the one the plan was made with")
	}
	if p.Target != "a" && p.Target != "b" || p.Keeper == p.Target || p.Keeper != "a" && p.Keeper != "b" {
		return errors.New("bad target in the plan")
	}
	if r.opt.MaxWrite < 0 || r.opt.MaxWrite%gpt.Sector != 0 {
		return fmt.Errorf("MaxWrite %d is not a multiple of 512", r.opt.MaxWrite)
	}
	t, err := r.m.Prepare(p, r.opt.Hooks)
	if err != nil {
		return err
	}
	r.t = t
	return nil
}

// bootctlStep runs one A/B transition: the sector must be the plan's
// "before" state, the medium runs uc-pxx-bootctl, and the sector read back
// must be valid and exactly the plan's prediction, with bytes 32..511
// unchanged.
func (r *runner) bootctlStep(s plan.Step) error {
	b := s.Bootctl
	cur, err := r.m.ReadAB()
	if err != nil {
		return err
	}
	want0, _ := b.Before.Raw()
	want1, _ := b.After.Raw()
	if !bytes.Equal(cur[:32], want0[:]) {
		return fmt.Errorf("A/B sector is %x, the plan expects %x before this step", cur[:32], want0)
	}
	run, err := r.m.Bootctl(b, cur)
	if run != nil {
		r.log(map[string]any{"step": s.N, "op": "bootctl", "args": b.Args, "rc": run.RC, "stderr": run.Stderr})
	}
	if err != nil {
		return err
	}
	// The medium verified the sector already. A failure of this second
	// read-back means that the state is not known any more.
	back, err := r.m.ReadAB()
	if err != nil {
		return fmt.Errorf("%w: the A/B read-back after uc-pxx-bootctl (verified once by the medium) failed: %v", ErrABUnknown, err)
	}
	a, _ := avbab.Parse(back)
	if err := a.Validate(); err != nil || !bytes.Equal(back[:32], want1[:]) {
		return fmt.Errorf("%w: the A/B read-back %x is not the predicted state %x (%v)", ErrABUnknown, back[:32], want1, err)
	}
	if !bytes.Equal(back[32:], cur[32:]) {
		return fmt.Errorf("%w: A/B read-back: bytes 32..511 of the sector changed", ErrABUnknown)
	}
	if bytes.Equal(back, cur) && !b.NoOp {
		return errors.New("uc-pxx-bootctl changed nothing, but the plan predicts a change")
	}
	if b.NoOp {
		r.say("     no change needed")
	}
	return nil
}

func (r *runner) writeStep(s plan.Step) (err error) {
	w := s.Write
	var seg *int
	segs := r.pl.Segments()
	for i := range segs {
		if segs[i].Index == w.Segment {
			seg = &i
		}
	}
	if seg == nil {
		return fmt.Errorf("segment %d not in the firmware", w.Segment)
	}
	sg := segs[*seg]
	if sg.Name()+"_"+r.p.Target != w.Partition || fmt.Sprintf("0x%04x", sg.SegID) != w.SegID {
		return fmt.Errorf("segment %d is %s, the plan says %s", w.Segment, sg.Name(), w.Partition)
	}
	if err := plan.CheckWriteRange(r.t, r.p.Target, w.Partition, w.Offset, w.Length); err != nil {
		return fmt.Errorf("write refused: %w", err)
	}
	pw, err := r.m.OpenWrite(w)
	if err != nil {
		return fmt.Errorf("%s: %w", w.Partition, err)
	}
	c := &chunker{w: pw, h: sha256.New(), max: r.opt.MaxWrite}
	var dst io.Writer = c
	var rh *plan.ResourceHasher
	if sg.Name() == "boot" {
		// the resource of the boot image as it is written: it must be the
		// one of the plan (an approved stock resource, RULES.md)
		rh = plan.NewResourceHasher()
		dst = io.MultiWriter(c, rh)
	}
	n, werr := r.pl.File().WriteSegment(sg, dst)
	cerr := pw.Close()
	if werr != nil {
		return fmt.Errorf("%s: %w", w.Partition, werr)
	}
	if cerr != nil {
		return fmt.Errorf("%s: %w", w.Partition, cerr)
	}
	got := hex.EncodeToString(c.h.Sum(nil))
	if n != w.Length || c.n != w.Length || got != w.SHA256 {
		return fmt.Errorf("%s: wrote %d bytes sha256 %s, the plan says %d bytes %s", w.Partition, n, got, w.Length, w.SHA256)
	}
	if rh != nil {
		res, err := rh.Sum()
		if err != nil || res != r.pl.BootResource || r.pl.BootResource == "" {
			return fmt.Errorf("%s: the resource of the written boot image is %s (%v), the plan's is %s", w.Partition, res, err, r.pl.BootResource)
		}
	}
	return nil
}

// Execute runs the plan against an image file (the dry run).
func Execute(p *plan.Plan, pl *plan.Payload, im *Image, opt Options) (*Result, error) {
	return Run(p, pl, &dryMedium{im: im, tool: opt.Bootctl}, opt)
}

// Run runs the plan against a medium. On any error it stops at once. The
// target then stays disabled (or was never touched), so the keeper boots.
func Run(p *plan.Plan, pl *plan.Payload, m Medium, opt Options) (res *Result, err error) {
	r := &runner{p: p, pl: pl, m: m, opt: opt}
	defer func() {
		if ferr := m.Finish(); ferr != nil && err == nil {
			err = ferr
		}
	}()
	if err := r.precheck(); err != nil {
		return nil, fmt.Errorf("precheck: %w", err)
	}
	r.log(map[string]any{"event": "start", "plan": p.ID, "mode": p.Mode, "source": p.Disk.Source, "ab": r.ab32()})
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
				return &Result{Writes: m.Writes(), Steps: s.N - 1}, fmt.Errorf("step %d (%s): stopped before the step: %w", s.N, s.Op, gerr)
			}
		}
		start := time.Now()
		before := r.ab32()
		var err error
		switch s.Op {
		case "bootctl":
			r.say("%2d/%d bootctl %s", s.N, len(p.Steps), strings.Join(s.Bootctl.Args, " "))
			err = r.bootctlStep(s)
		case "write":
			r.say("%2d/%d write %s (%d bytes)", s.N, len(p.Steps), s.Write.Partition, s.Write.Length)
			err = r.writeStep(s)
		case "skip":
			r.say("%2d/%d skip %s (already holds the image)", s.N, len(p.Steps), s.Write.Partition)
			var h string
			if h, err = m.Verify(s.Write); err == nil && h != s.Write.SHA256 {
				err = fmt.Errorf("%s changed since the plan: sha256 %s", s.Write.Partition, h)
			}
		case "sync":
			r.say("%2d/%d sync", s.N, len(p.Steps))
			var names []string
			for _, st := range p.Steps {
				if st.Op == "write" {
					names = append(names, st.Write.Partition)
				}
			}
			err = m.Sync(names)
		case "verify":
			r.say("%2d/%d verify", s.N, len(p.Steps))
			for _, name := range s.Verify {
				w := writes[name]
				if w == nil {
					err = fmt.Errorf("verify: %s has no write step", name)
					break
				}
				var h string
				if h, err = m.Verify(w); err == nil && h != w.SHA256 {
					err = fmt.Errorf("verify: %s reads back sha256 %s, want %s", name, h, w.SHA256)
				}
				if err != nil {
					break
				}
				r.say("     %-9s ok  %s", name, h[:16])
			}
		case "check-ab":
			r.say("%2d/%d check the A/B sector", s.N, len(p.Steps))
			err = r.checkAB(s)
		case "keep-key":
			r.say("%2d/%d keep-key: the firmware key on the unit (backup, 2 sectors)", s.N, len(p.Steps))
			err = r.keepKey(s)
		default:
			err = fmt.Errorf("unknown step %q", s.Op)
		}
		r.log(map[string]any{"step": s.N, "op": s.Op, "ab_before": before, "ab_after": r.ab32(),
			"writes": m.Writes(), "ms": time.Since(start).Milliseconds(), "error": errString(err)})
		if err != nil {
			return &Result{Writes: m.Writes(), Steps: s.N - 1}, fmt.Errorf("step %d (%s): %w", s.N, s.Op, err)
		}
	}
	if err := r.keeperUnchanged(); err != nil {
		return &Result{Writes: m.Writes(), Steps: len(p.Steps)}, err
	}
	return &Result{Writes: m.Writes(), FinalAB: p.AB.Final, Steps: len(p.Steps)}, nil
}

// keeperUnchanged re-hashes every keeper partition when the plan recorded
// them (plan --hash-keeper; always in the device mode). It only reads.
func (r *runner) keeperUnchanged() error {
	if len(r.p.KeeperSHA256) == 0 {
		if r.p.Mode == "device" {
			return errors.New("keeper check: the device plan has no keeper hashes")
		}
		return nil
	}
	for _, part := range plan.SlotParts {
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
	r.say("     keeper slot %s: all %d partitions unchanged (sha256)", strings.ToUpper(r.p.Keeper), len(plan.SlotParts))
	r.log(map[string]any{"event": "keeper-unchanged", "slot": r.p.Keeper})
	return nil
}

// keepKey writes the key record to its two sectors of backup, one after the
// other: each sector is read again (O_DIRECT on the device) and must still be
// of the class the plan saw; it is written only when it does not hold the
// record yet, then synced and read back. Only the record's sha256 is logged.
func (r *runner) keepKey(s plan.Step) error {
	k := s.KeepKey
	rec := r.opt.KeyRecord
	if k == nil || len(rec) != 512 || keykeep.SHA256(rec) != k.RecordSHA256 {
		return errors.New("keep-key: the key record is not the plan's (sha256)")
	}
	if r.p.Steps[0].Op != "keep-key" || s.N != 1 {
		return errors.New("keep-key: the step is not the first of the plan (it must come before the disable)")
	}
	for i, lba := range k.LBAs {
		if err := plan.CheckKeepKeyRange(r.t, lba); err != nil {
			return fmt.Errorf("keep-key: write refused: %w", err)
		}
		cur, err := r.m.ReadKeySector(lba)
		if err != nil {
			return fmt.Errorf("keep-key: read LBA %d: %w", lba, err)
		}
		c := keykeep.Classify(cur, rec)
		if c != k.Before[i] || !c.Writable() {
			return fmt.Errorf("keep-key: LBA %d is %s now, the plan saw %s: nothing written there", lba, c, k.Before[i])
		}
		if c != keykeep.Same {
			if err := r.m.WriteKeySector(lba, rec); err != nil {
				return fmt.Errorf("keep-key: LBA %d: %w", lba, err)
			}
		}
		back, err := r.m.ReadKeySector(lba)
		if err != nil || !bytes.Equal(back, rec) {
			return fmt.Errorf("keep-key: LBA %d does not read back as the record (%v)", lba, err)
		}
		r.say("     backup LBA %d: %s -> the record (sha256 %s), read back", lba, c, k.RecordSHA256[:16])
	}
	r.log(map[string]any{"event": "keep-key", "lbas": k.LBAs, "record_sha256": k.RecordSHA256})
	return nil
}

func (r *runner) checkAB(s plan.Step) error {
	back, err := r.m.ReadAB()
	if err != nil {
		return err
	}
	a, _ := avbab.Parse(back)
	if err := a.Validate(); err != nil {
		return fmt.Errorf("the installer's decoder: %w", err)
	}
	want, _ := s.Expect.Raw()
	if !bytes.Equal(back[:32], want[:]) {
		return fmt.Errorf("A/B is %x, want %x", back[:32], want)
	}
	st, err := r.m.BootctlStatus()
	if err != nil {
		return err
	}
	if err := st.Agrees(a); err != nil {
		return fmt.Errorf("bootctl status and the installer's decoder disagree: %w", err)
	}
	k := 0
	if r.p.Keeper == "b" {
		k = 1
	}
	if a.Pick() != 1-k || !a.Slots[k].Keeper() {
		return errors.New("the target does not boot next, or the keeper is not the successful fallback")
	}
	r.say("     A/B %s (decoder and bootctl agree)", plan.State(want))
	return nil
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func joinArgs(a []string) string { return strings.Join(a, " ") }

func trim(s string) string { return strings.TrimSpace(s) }
