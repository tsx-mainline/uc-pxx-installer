package install

// The executor of a mainline plan (plan.ComputeMainline). It runs the steps
// of the plan in order on a Medium, as Run does for an official plan, and
// reuses Run's own A/B step (bootctlStep), A/B check (checkAB) and keeper
// check (keeperUnchanged) unchanged. Only the source of the written bytes
// differs: a write takes its bytes from the plan's write source, never from a
// firmware container:
//
//   - "keeper": the first bytes of the keeper's partition of the same name
//     (uboot_b, trust_b, dtbo_b, vbmeta_b), read from the medium;
//   - "assembled-boot": the payload's template with the resource of the
//     keeper's boot_b (read from the medium), mainline.Assemble;
//   - "rootfs": rootfs.ext4 of the payload, read in place.
//
// Every write is hashed while it is written and must be the plan's sha256
// and length; the boot write's resource is hashed as written and must be the
// plan's. This file adds code only; Run and every function it uses are
// unchanged. (Phase 3: the dry run and the power-cut sweeps run it. The
// device mode does not call it yet.)

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/plan"
)

type mlRunner struct {
	*runner
	mp *plan.MainlinePlan
	ml *mainline.Payload
}

// ExecuteMainline runs a mainline plan against an image file (the dry run).
func ExecuteMainline(p *plan.MainlinePlan, ml *mainline.Payload, im *Image, opt Options) (*Result, error) {
	return RunMainline(p, ml, &dryMedium{im: im, tool: opt.Bootctl}, opt)
}

// precheck checks the mainline plan itself, then lets the medium check that
// nothing changed since the plan was made.
func (r *mlRunner) precheck() error {
	p := r.mp
	switch {
	case p.ID == "" || !p.VerifyID():
		return errors.New("the plan has no valid id (a refused or edited plan)")
	case p.Kind != plan.MainlineKind || p.Payload != nil || p.Mainline == nil || p.Chain == nil:
		return errors.New("not a mainline plan")
	case p.Mode != r.m.Kind():
		return fmt.Errorf("plan mode %q: this medium runs only %q plans", p.Mode, r.m.Kind())
	case !r.ml.Verified() || r.ml.ManifestSHA256 != p.Mainline.ManifestSHA256 ||
		mainlineSHA(r.ml.TemplateBytes()) != p.Chain.TemplateSHA:
		return errors.New("the mainline payload is not the one the plan was made with")
	case p.Target != plan.MainlineTarget || p.Keeper != plan.MainlineKeeper:
		return errors.New("a mainline plan must have target a and keeper b")
	case r.opt.MaxWrite < 0 || r.opt.MaxWrite%gpt.Sector != 0:
		return fmt.Errorf("MaxWrite %d is not a multiple of 512", r.opt.MaxWrite)
	case r.opt.KeyRecord != nil:
		return errors.New("a mainline install has no keep-key step")
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
		if err := mainlineSourceRule(src); err != nil {
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
	return nil
}

// mainlineSourceRule: a mainline install writes only the six partitions of
// slot A (plan.MainlineParts; never vendor_a, oem_a or anything of B), uboot,
// trust, dtbo and vbmeta only from the keeper's partition of the same name,
// boot_a only assembled, system_a only from rootfs.ext4.
func mainlineSourceRule(src *plan.WriteSource) error {
	part, ok := strings.CutSuffix(src.Partition, "_"+plan.MainlineTarget)
	in := false
	for _, n := range plan.MainlineParts() {
		in = in || ok && part == n
	}
	if !in {
		return fmt.Errorf("%s is not a partition that a mainline install writes", src.Partition)
	}
	want := "keeper"
	switch part {
	case "boot":
		want = "assembled-boot"
	case "system":
		want = "rootfs"
	}
	if src.From != want || (want == "keeper") != (src.KeeperPartition == part+"_"+plan.MainlineKeeper) {
		return fmt.Errorf("%s: source %q %q, a mainline install takes it only from %s", src.Partition, src.From, src.KeeperPartition, want)
	}
	return nil
}

func mainlineSHA(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// source returns the bytes of one write: a reader of exactly src.Length
// bytes. The keeper's bytes are read from the medium here; their sha256 is
// checked as they are written.
func (r *mlRunner) source(src *plan.WriteSource) (io.Reader, error) {
	switch src.From {
	case "keeper":
		name := strings.TrimSuffix(src.Partition, "_"+r.p.Target) + "_" + r.p.Keeper
		e, ok := r.t.Find(src.KeeperPartition)
		if !ok || src.KeeperPartition != name || src.Length > e.Bytes() {
			return nil, fmt.Errorf("%s: the keeper source %s is not %s of the keeper", src.Partition, src.KeeperPartition, name)
		}
		return io.NewSectionReader(r.m.Reader(), e.Start*gpt.Sector, src.Length), nil
	case "assembled-boot":
		e, ok := r.t.Find("boot_" + r.p.Keeper)
		if !ok {
			return nil, errors.New("no boot partition of the keeper")
		}
		res, err := mainline.ReadResource(r.m.Reader(), e.Start*gpt.Sector, e.Bytes())
		if err != nil {
			return nil, fmt.Errorf("the keeper's resource: %w", err)
		}
		if mainlineSHA(res) != src.ResourceSHA256 {
			return nil, fmt.Errorf("the resource of boot_%s is sha256 %s now, the plan's is %s", r.p.Keeper, mainlineSHA(res), src.ResourceSHA256)
		}
		tmpl := r.ml.TemplateBytes()
		if mainlineSHA(tmpl) != src.TemplateSHA256 {
			return nil, errors.New("the template is not the plan's")
		}
		img, err := mainline.Assemble(tmpl, res)
		if err != nil {
			return nil, err
		}
		if int64(len(img)) != src.Length || mainlineSHA(img) != src.SHA256 {
			return nil, fmt.Errorf("the assembled boot image is %d bytes sha256 %s, the plan's %d bytes %s", len(img), mainlineSHA(img), src.Length, src.SHA256)
		}
		return bytes.NewReader(img), nil
	case "rootfs":
		return io.NewSectionReader(r.ml.RootfsReader(), 0, src.Length), nil
	}
	return nil, fmt.Errorf("%s: unknown write source %q", src.Partition, src.From)
}

// write writes one step from its source, through the chunker, and checks
// the length and the sha256 of what was written (and of the boot resource).
func (r *mlRunner) write(s plan.Step) error {
	w := s.Write
	src := r.mp.Source(w.Partition)
	if src == nil {
		return fmt.Errorf("%s: no write source", w.Partition)
	}
	if err := mainlineSourceRule(src); err != nil {
		return fmt.Errorf("write refused: %w", err)
	}
	if err := plan.CheckWriteRange(r.t, r.p.Target, w.Partition, w.Offset, w.Length); err != nil {
		return fmt.Errorf("write refused: %w", err)
	}
	if w.Offset != 0 || w.Length != src.Length {
		return fmt.Errorf("%s: range %d+%d is not the source's 0+%d", w.Partition, w.Offset, w.Length, src.Length)
	}
	in, err := r.source(src)
	if err != nil {
		return err
	}
	pw, err := r.m.OpenWrite(w)
	if err != nil {
		return fmt.Errorf("%s: %w", w.Partition, err)
	}
	max := r.opt.MaxWrite
	if max == 0 {
		max = 1 << 20 // as the decrypter of an official firmware gives its data
	}
	c := &chunker{w: pw, h: sha256.New(), max: max}
	var dst io.Writer = c
	var rh *plan.ResourceHasher
	if src.From == "assembled-boot" {
		rh = plan.NewResourceHasher()
		dst = io.MultiWriter(c, rh)
	}
	n, werr := io.CopyBuffer(dst, io.LimitReader(in, w.Length), make([]byte, 1<<20))
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
		if err != nil || res != src.ResourceSHA256 {
			return fmt.Errorf("%s: the resource of the written boot image is %s (%v), the plan's is %s", w.Partition, res, err, src.ResourceSHA256)
		}
	}
	return nil
}

// RunMainline runs a mainline plan against a medium. On any error it stops
// at once: slot A then stays disabled (or was never touched), so the keeper B
// boots. The step loop is Run's, without the keep-key step.
func RunMainline(p *plan.MainlinePlan, ml *mainline.Payload, m Medium, opt Options) (res *Result, err error) {
	r := &mlRunner{runner: &runner{p: &p.Plan, m: m, opt: opt}, mp: p, ml: ml}
	defer func() {
		if ferr := m.Finish(); ferr != nil && err == nil {
			err = ferr
		}
	}()
	if err := r.precheck(); err != nil {
		return nil, fmt.Errorf("precheck: %w", err)
	}
	r.log(map[string]any{"event": "start", "plan": p.ID, "kind": p.Kind, "mode": p.Mode, "source": p.Disk.Source, "ab": r.ab32()})
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
			r.say("%2d/%d write %s from %s (%d bytes)", s.N, len(p.Steps), s.Write.Partition, s.Write.SegID, s.Write.Length)
			err = r.write(s)
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
		default:
			err = fmt.Errorf("step %q is not a step of a mainline plan", s.Op)
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
