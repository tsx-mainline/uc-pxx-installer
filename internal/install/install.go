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
	"uc-pxx-installer/internal/plan"
)

// Options are the run options.
type Options struct {
	Bootctl bootctl.Tool
	Say     func(string) // progress lines for a human (may be nil)
	Log     io.Writer    // JSON lines (may be nil)
	Hooks   *Hooks       // tests only
	// MaxWrite splits the partition data into writes of at most this many
	// bytes (a multiple of 512). 0 = as the decrypter gives them (1 MiB).
	MaxWrite int
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
	im  *Image
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
	b, err := r.im.readAB()
	if err != nil {
		return "unreadable: " + err.Error()
	}
	return hex.EncodeToString(b[:32])
}

// SafeState says what the image's A/B sector means for a stop: whether the
// keeper boots next and the target is not bootable.
func SafeState(im *Image, p *plan.Plan) string {
	b, err := im.readAB()
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

type partWriter struct {
	im   *Image
	name string
	off  int64
	h    hash.Hash
	max  int
}

func (w *partWriter) Write(b []byte) (int, error) {
	done := 0
	for done < len(b) {
		n := len(b) - done
		if w.max > 0 && n > w.max {
			n = w.max
		}
		if err := w.im.writeData(w.name, w.off, b[done:done+n]); err != nil {
			return done, err
		}
		w.off += int64(n)
		w.h.Write(b[done : done+n])
		done += n
	}
	return done, nil
}

func (r *runner) hashPart(w *plan.WriteStep) (string, error) {
	h := sha256.New()
	_, err := io.Copy(h, io.NewSectionReader(r.im.f, w.StartLBA*gpt.Sector+w.Offset, w.Length))
	return hex.EncodeToString(h.Sum(nil)), err
}

// precheck binds the image to the plan and checks that nothing changed
// since the plan was made.
func (r *runner) precheck() error {
	p := r.p
	if p.ID == "" || !p.VerifyID() {
		return errors.New("the plan has no valid id (a refused or edited plan)")
	}
	if p.Mode != "dry-run" {
		return fmt.Errorf("plan mode %q: only dry-run exists", p.Mode)
	}
	if r.im.size != p.Disk.Sectors*gpt.Sector {
		return fmt.Errorf("image has %d bytes, the plan is for %d sectors", r.im.size, p.Disk.Sectors)
	}
	t, err := gpt.Parse(r.im.f, r.im.size)
	if err != nil {
		return fmt.Errorf("GPT: %w", err)
	}
	if t.LayoutSHA256() != p.Disk.LayoutSHA256 {
		return errors.New("the GPT layout differs from the plan")
	}
	sec, err := r.im.readAB()
	if err != nil {
		return err
	}
	if s := sha256.Sum256(sec); hex.EncodeToString(s[:]) != p.AB.SectorSHA256 {
		return errors.New("the A/B sector differs from the plan")
	}
	if r.pl == nil || r.pl.File() == nil || r.pl.FirmwareSHA256 != p.Payload.FirmwareSHA256 {
		return errors.New("the payload is not the one the plan was made with")
	}
	sha, err := r.opt.Bootctl.SHA256()
	if err != nil || sha != p.Bootctl.SHA256 || r.opt.Bootctl.Path != p.Bootctl.Path {
		return errors.New("the bootctl binary is not the one the plan names")
	}
	if p.Target != "a" && p.Target != "b" || p.Keeper == p.Target {
		return errors.New("bad target in the plan")
	}
	r.im.t, r.im.target = t, p.Target
	return nil
}

func (r *runner) bootctlStep(s plan.Step) error {
	b := s.Bootctl
	cur, err := r.im.readAB()
	if err != nil {
		return err
	}
	want0, _ := b.Before.Raw()
	want1, _ := b.After.Raw()
	if !bytes.Equal(cur[:32], want0[:]) {
		return fmt.Errorf("A/B sector is %x, the plan expects %x before this step", cur[:32], want0)
	}
	res, err := r.opt.Bootctl.Apply(cur, b.Args)
	if err != nil {
		return err
	}
	r.log(map[string]any{"step": s.N, "op": "bootctl", "args": b.Args, "rc": res.RC, "stderr": res.Stderr})
	if res.RC != bootctl.RCOK {
		what := "refused, sector unchanged"
		switch res.RC {
		case bootctl.RCIOSame:
			what = "I/O error, sector verified unchanged"
		case bootctl.RCUnknown:
			what = "ON A DEVICE THE A/B STATE WOULD BE UNKNOWN: do not reboot"
		}
		return fmt.Errorf("uc-pxx-bootctl %s: exit %d (%s): %s", strings.Join(b.Args, " "), res.RC, what, strings.TrimSpace(res.Stderr))
	}
	if !bytes.Equal(res.Sector[32:], cur[32:]) {
		return errors.New("uc-pxx-bootctl changed bytes 32..511 of the sector")
	}
	if !bytes.Equal(res.Sector[:32], want1[:]) {
		return fmt.Errorf("uc-pxx-bootctl wrote %x, the plan predicts %x: stopping, nothing put into the image", res.Sector[:32], want1)
	}
	if bytes.Equal(res.Sector, cur) {
		if !b.NoOp {
			return errors.New("uc-pxx-bootctl changed nothing, but the plan predicts a change")
		}
		r.say("     no change needed")
	} else if err := r.im.writeAB(cur, res.Sector); err != nil {
		return err
	}
	back, err := r.im.readAB()
	if err != nil {
		return err
	}
	a, _ := avbab.Parse(back)
	if err := a.Validate(); err != nil || !bytes.Equal(back[:32], want1[:]) {
		return fmt.Errorf("A/B read-back %x is not the predicted state (%v)", back[:32], err)
	}
	return nil
}

func (r *runner) writeStep(s plan.Step) error {
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
	if r.opt.MaxWrite < 0 || r.opt.MaxWrite%gpt.Sector != 0 {
		return fmt.Errorf("MaxWrite %d is not a multiple of 512", r.opt.MaxWrite)
	}
	pw := &partWriter{im: r.im, name: w.Partition, off: w.Offset, h: sha256.New(), max: r.opt.MaxWrite}
	n, err := r.pl.File().WriteSegment(sg, pw)
	if err != nil {
		return fmt.Errorf("%s: %w", w.Partition, err)
	}
	got := hex.EncodeToString(pw.h.Sum(nil))
	if n != w.Length || got != w.SHA256 {
		return fmt.Errorf("%s: wrote %d bytes sha256 %s, the plan says %d bytes %s", w.Partition, n, got, w.Length, w.SHA256)
	}
	return nil
}

// Execute runs the plan against the image. On any error it stops at once.
// The target then stays disabled (or was never touched), so the keeper boots.
func Execute(p *plan.Plan, pl *plan.Payload, im *Image, opt Options) (*Result, error) {
	r := &runner{p: p, pl: pl, im: im, opt: opt}
	im.hooks = opt.Hooks
	if err := r.precheck(); err != nil {
		return nil, fmt.Errorf("precheck: %w", err)
	}
	r.log(map[string]any{"event": "start", "plan": p.ID, "image": im.path, "ab": r.ab32()})
	writes := map[string]*plan.WriteStep{}
	for _, s := range p.Steps {
		if s.Write != nil {
			writes[s.Write.Partition] = s.Write
		}
	}
	for _, s := range p.Steps {
		im.step = s.N
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
			if h, err = r.hashPart(s.Write); err == nil && h != s.Write.SHA256 {
				err = fmt.Errorf("%s changed since the plan: sha256 %s", s.Write.Partition, h)
			}
		case "sync":
			r.say("%2d/%d sync", s.N, len(p.Steps))
			err = im.f.Sync()
		case "verify":
			r.say("%2d/%d verify", s.N, len(p.Steps))
			for _, name := range s.Verify {
				w := writes[name]
				if w == nil {
					err = fmt.Errorf("verify: %s has no write step", name)
					break
				}
				var h string
				if h, err = r.hashPart(w); err == nil && h != w.SHA256 {
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
		default:
			err = fmt.Errorf("unknown step %q", s.Op)
		}
		r.log(map[string]any{"step": s.N, "op": s.Op, "ab_before": before, "ab_after": r.ab32(),
			"writes": im.writes, "ms": time.Since(start).Milliseconds(), "error": errString(err)})
		if err != nil {
			return &Result{Writes: im.writes, Steps: s.N - 1}, fmt.Errorf("step %d (%s): %w", s.N, s.Op, err)
		}
	}
	if err := r.keeperUnchanged(); err != nil {
		return &Result{Writes: im.writes, Steps: len(p.Steps)}, err
	}
	return &Result{Writes: im.writes, FinalAB: p.AB.Final, Steps: len(p.Steps)}, nil
}

// keeperUnchanged re-hashes every keeper partition when the plan recorded
// them (plan --hash-keeper). It only reads.
func (r *runner) keeperUnchanged() error {
	if len(r.p.KeeperSHA256) == 0 {
		return nil
	}
	for _, part := range plan.SlotParts {
		name := part + "_" + r.p.Keeper
		e, ok := r.im.t.Find(name)
		want := r.p.KeeperSHA256[name]
		if !ok || want == "" {
			return fmt.Errorf("keeper check: no %s", name)
		}
		h := sha256.New()
		if _, err := io.Copy(h, io.NewSectionReader(r.im.f, e.Start*gpt.Sector, e.Bytes())); err != nil {
			return err
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != want {
			return fmt.Errorf("keeper check: %s changed (sha256 %s, was %s)", name, got, want)
		}
	}
	r.say("     keeper slot %s: all %d partitions unchanged (sha256)", strings.ToUpper(r.p.Keeper), len(plan.SlotParts))
	r.log(map[string]any{"event": "keeper-unchanged", "slot": r.p.Keeper})
	return nil
}

func (r *runner) checkAB(s plan.Step) error {
	back, err := r.im.readAB()
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
	st, _, err := r.opt.Bootctl.Status(back)
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
