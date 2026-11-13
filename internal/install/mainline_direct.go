package install

// The executor of a direct-path plan (plan.ComputeMainlineDirect;
// MAINLINE-INT-2026-11-10.md task 3). It runs the steps in order on a Medium
// as Run and RunMainline do, and reuses unchanged: Run's keep-key step
// (keepKey), A/B step (bootctlStep, also for R(K)'s repair form), A/B check
// (checkAB), keeper check (keeperUnchanged: all eight partitions of A), the
// chunker, and the rootfs source of the mainline executor. Only the source
// of the boot-chain bytes differs: the stash of the first drive phase
// (DirectOptions.Stash), never the keeper and never a firmware container:
//
//   - "stash": the stash's uboot, trust or dtbo image;
//   - "stash-patched-vbmeta": the stash's signed vbmeta, patched by
//     mainline.PatchVbmeta to the plan's boot image_size;
//   - "assembled-boot-stash": the payload's template with the stash's boot
//     resource (mainline.Assemble).
//
// Every source is bound to the payload (re-checked through the gate) and to
// the reviewed firmware's shas of the payload's requires_bootchain, not only
// to the plan's record. This file adds code only.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/plan"
)

// StashParts gives the stashed parts (mlstash.Loaded has this method).
type StashParts interface {
	Part(part string) []byte
}

// DirectOptions are the options of a direct run: the options of Run (with
// KeyRecord for the keep-key step), the stash and the payload gate.
type DirectOptions struct {
	Options
	Stash StashParts
	// Gate is the payload gate; the precheck checks the payload with it
	// again. Required.
	Gate *mainline.Gate
	// LockWritten: the caller wrote lock_state 0x01 and read it back 0x01
	// before this run (devmode lockWrite, the in-RAM lock-state writer). A
	// plan that needs the lock-state write (UnlockDirect.Write, the reader
	// read 0x00) runs only with it: the image mode has no TEE, so it never
	// sets it, and such a plan refuses there before any write.
	LockWritten bool
}

type dRunner struct {
	*runner
	dp  *plan.DirectPlan
	ml  *mainline.Payload
	src *mlRunner // only its source() of "rootfs"
	opt DirectOptions
}

// ExecuteMainlineDirect runs a direct plan against an image file (the dry run).
func ExecuteMainlineDirect(p *plan.DirectPlan, ml *mainline.Payload, im *Image, opt DirectOptions) (*Result, error) {
	return RunMainlineDirect(p, ml, &dryMedium{im: im, tool: opt.Bootctl}, opt)
}

// DirectStructure is the exact shape of a plan of plan.ComputeMainlineDirect:
// [keep-key] [R(K) repair --keeper a] disable --slot b; the six writes of B in
// order (trust may be a skip); sync; verify of exactly those six; arm-trying
// --slot b --lower-keeper; check-ab. The A/B states are recomputed from the
// plan's start: R(K) only from the valid both-trying start with A the higher
// priority, to the reviewed R(K) state; then the keeper A successful and
// booting until the arm and B unbootable; after the arm B {15,7,0} next with
// A successful.
func DirectStructure(p *plan.DirectPlan) error {
	if p.Keeper != plan.DirectKeeper || p.Target != plan.DirectTarget {
		return errors.New("a direct plan must have keeper a and target b")
	}
	parts := plan.MainlineParts()
	n := len(parts)
	i := 0
	if len(p.Steps) > 0 && p.Steps[0].Op == "keep-key" {
		i = 1
	}
	now, err := p.AB.Now.Raw()
	if err != nil {
		return fmt.Errorf("the plan's A/B start: %v", err)
	}
	a0, _ := avbab.Parse(now[:])
	eff := now
	rk := len(p.Steps) > i && p.Steps[i].Op == "bootctl" && p.Steps[i].Bootctl != nil && len(p.Steps[i].Bootctl.Args) > 0 &&
		p.Steps[i].Bootctl.Args[0] == "repair"
	want := n + 5 + i
	if rk {
		want++
	}
	if len(p.Steps) != want {
		return fmt.Errorf("%d steps, a direct plan of this start has %d", len(p.Steps), want)
	}
	for j, s := range p.Steps {
		kk := j == 0 && i == 1
		if s.N != j+1 || (s.KeepKey != nil) != kk || kk && (s.Op != "keep-key" || s.Bootctl != nil || s.Write != nil) ||
			!kk && s.Op == "keep-key" {
			return fmt.Errorf("step %d: number %d, or a keep-key part that is not the first step", j+1, s.N)
		}
	}
	same := func(st plan.ABState, w [32]byte) bool {
		raw, err := st.Raw()
		return err == nil && raw == w
	}
	argv := func(s plan.Step, w ...string) bool {
		return s.Op == "bootctl" && s.Bootctl != nil && s.Write == nil && strings.Join(s.Bootctl.Args, " ") == strings.Join(w, " ")
	}
	if rk {
		r := p.Steps[i]
		if a0.Validate() != nil || a0.Slots[0].Successful != 0 || a0.Slots[1].Successful != 0 || a0.Tie() || a0.Pick() != 0 ||
			a0.Slots[0].Tries == 0 || a0.Slots[1].Tries == 0 {
			return errors.New("R(K) from a start that is not the both-trying reset with A the higher priority")
		}
		sec := make([]byte, gpt.Sector)
		copy(sec, now[:])
		if len(r.Bootctl.Args) != 5 || r.Bootctl.Args[1] != "--keeper" || r.Bootctl.Args[2] != plan.DirectKeeper ||
			r.Bootctl.Args[3] != "--expect-sector" || len(r.Bootctl.Args[4]) != 1024 {
			return errors.New("the R(K) step is not repair --keeper a --expect-sector <the start sector>")
		}
		es, err := hex.DecodeString(r.Bootctl.Args[4])
		if err != nil || !bytes.Equal(es[:32], now[:]) || len(es) != gpt.Sector {
			return errors.New("the R(K) step's --expect-sector is not the plan's start")
		}
		rs, err := plan.PredictRepair(es, 0)
		if err != nil {
			return fmt.Errorf("R(K): %v", err)
		}
		copy(eff[:], rs)
		if !same(r.Bootctl.Before, now) || !same(r.Bootctl.After, eff) || r.Bootctl.NoOp {
			return errors.New("the R(K) step does not have the predicted states")
		}
		i++
	}
	ae, _ := avbab.Parse(eff[:])
	if ae.Validate() != nil || !ae.Slots[0].Keeper() || ae.Tie() || ae.Pick() != 0 {
		return errors.New("before the disable the keeper A is not the successful slot that boots")
	}
	s1, err1 := plan.PredictDisable(eff, 1)
	s2, err2 := plan.PredictArm(s1, 1, true)
	if err1 != nil || err2 != nil {
		return fmt.Errorf("the A/B predictions: %v %v", err1, err2)
	}
	a1, _ := avbab.Parse(s1[:])
	a2, _ := avbab.Parse(s2[:])
	switch {
	case a1.Validate() != nil || !a1.Slots[0].Keeper() || a1.Slots[1].Bootable() || a1.Pick() != 0:
		return errors.New("after the disable the keeper A would not be the only bootable slot")
	case a2.Validate() != nil || a2.Slots[1] != (avbab.Slot{Priority: 15, Tries: 7}) || !a2.Slots[0].Keeper() || a2.Pick() != 1:
		return errors.New("after the arm slot B would not be {15,7,0} next with the keeper A successful")
	}
	d := p.Steps[i]
	if !argv(d, "disable", "--slot", plan.DirectTarget) || !same(d.Bootctl.Before, eff) || !same(d.Bootctl.After, s1) || d.Bootctl.NoOp != (s1 == eff) {
		return errors.New("the disable is not disable --slot b with the predicted states")
	}
	i++
	var names []string
	for _, part := range parts {
		s := p.Steps[i]
		name := part + "_" + plan.DirectTarget
		switch {
		case s.Write == nil || s.Write.Partition != name || s.Write.Offset != 0 || s.Bootctl != nil:
			return fmt.Errorf("step %d is not the write of %s", s.N, name)
		case s.Op == "write" && s.Write.Action == "write":
		case s.Op == "skip" && s.Write.Action == "skip-identical" && part == "trust":
		default:
			return fmt.Errorf("step %d (%s): op %q action %q", s.N, name, s.Op, s.Write.Action)
		}
		names = append(names, name)
		i++
	}
	if p.Steps[i].Op != "sync" || p.Steps[i].Bootctl != nil || p.Steps[i].Write != nil {
		return errors.New("no sync after the writes")
	}
	i++
	if v := p.Steps[i]; v.Op != "verify" || strings.Join(v.Verify, " ") != strings.Join(names, " ") {
		return errors.New("the verify step does not cover exactly the six writes of slot B")
	}
	i++
	if a := p.Steps[i]; !argv(a, "arm-trying", "--slot", plan.DirectTarget, "--lower-keeper") || !same(a.Bootctl.Before, s1) ||
		!same(a.Bootctl.After, s2) || a.Bootctl.NoOp {
		return errors.New("the step after the verify is not arm-trying --slot b --lower-keeper with the predicted states")
	}
	i++
	if c := p.Steps[i]; c.Op != "check-ab" || c.Expect == nil || !same(*c.Expect, s2) || !same(p.AB.Final, s2) {
		return errors.New("the last step is not check-ab of the predicted final state")
	}
	return nil
}

// directSourceRule: a direct install writes only the six partitions of B,
// uboot/trust/dtbo only from the stash, vbmeta only as the patch of the
// stash's vbmeta, boot only assembled with the stash's resource, system only
// from rootfs.ext4; never a keeper source.
func directSourceRule(src *plan.WriteSource) error {
	part, ok := strings.CutSuffix(src.Partition, "_"+plan.DirectTarget)
	in := false
	for _, n := range plan.MainlineParts() {
		in = in || ok && part == n
	}
	if !in {
		return fmt.Errorf("%s is not a partition that a direct install writes", src.Partition)
	}
	want := plan.SrcStash
	switch part {
	case "boot":
		want = plan.SrcAssembledStash
	case "system":
		want = "rootfs"
	case "vbmeta":
		want = plan.SrcStashPatchedVbmet
	}
	if src.From != want || src.KeeperPartition != "" {
		return fmt.Errorf("%s: source %q %q, a direct install takes it only from %s", src.Partition, src.From, src.KeeperPartition, want)
	}
	if want == plan.SrcStashPatchedVbmet && (len(src.SourceSHA256) != 64 || src.BootImageSize <= 0) {
		return fmt.Errorf("%s: a patched vbmeta source without its source sha256 and boot image_size", src.Partition)
	}
	return nil
}

// directBinding: every source is the payload's (the payload the precheck
// passed through the gate again): uboot, trust, dtbo the requires_bootchain
// shas; vbmeta the payload's vbmeta_patched (and, with a checking gate, a
// reviewed patch) of the required signed vbmeta; boot the payload's
// boot_assembled with the required resource; system the payload's rootfs.
func directBinding(p *plan.DirectPlan, ml *mainline.Payload, g *mainline.Gate) error {
	m := ml.Manifest
	rb, vp := m.RequiresBootchain, m.VbmetaPatched
	for _, s := range p.Sources {
		part, _ := strings.CutSuffix(s.Partition, "_"+plan.DirectTarget)
		ok := false
		switch part {
		case "uboot":
			ok = s.SHA256 == rb.UBootSHA256
		case "trust":
			ok = s.SHA256 == rb.TrustSHA256
		case "dtbo":
			ok = s.SHA256 == rb.DtboSHA256
		case "vbmeta":
			ok = s.SHA256 == vp.SHA256 && s.Length == vp.Size && s.SourceSHA256 == vp.SourceSHA256 && vp.SourceSHA256 == rb.VbmetaSHA256 &&
				s.BootImageSize == vp.BootImageSize && p.Chain.VbmetaPatch != nil && p.Chain.VbmetaPatch.SHA256 == vp.SHA256 &&
				p.Chain.BootCap == vp.BootImageSize
		case "boot":
			ok = s.SHA256 == m.BootAssembled.SHA256 && s.Length == m.BootAssembled.Size && s.ResourceSHA256 == rb.ResourceSHA256 &&
				s.TemplateSHA256 == m.BootTemplate.SHA256
		case "system":
			ok = s.SHA256 == m.Rootfs.SHA256 && s.Length == m.Rootfs.Size
		}
		if !ok {
			return fmt.Errorf("%s: the plan's source is not the payload's (requires_bootchain, vbmeta_patched, boot_assembled, rootfs)", s.Partition)
		}
	}
	switch {
	case !g.ChecksVbmeta() && (g.Name() != "test" || p.Gate == nil || p.Gate.Gate != "test"):
		return errors.New("the gate does not check the patched vbmeta, and it is not a test gate")
	case g.ChecksVbmeta() && (p.Chain.VbmetaPatch == nil ||
		!mainline.FindVbmetaPatch(g.ReviewedVbmeta(), vp.SourceSHA256, vp.SHA256).Matches(*p.Chain.VbmetaPatch, int(vp.Size))):
		return errors.New("the patched vbmeta is not a reviewed patch of the payload gate")
	}
	return nil
}

func (r *dRunner) precheck() error {
	p := r.dp
	switch {
	case p.ID == "" || !p.VerifyID():
		return errors.New("the plan has no valid id (a refused or edited plan)")
	case p.Kind != plan.MainlineDirectKind || p.Payload != nil || p.Mainline == nil || p.Chain == nil || p.Stash == nil:
		return errors.New("not a direct mainline plan")
	case p.Mode != r.m.Kind():
		return fmt.Errorf("plan mode %q: this medium runs only %q plans", p.Mode, r.m.Kind())
	case !r.ml.Verified() || r.ml.ManifestSHA256 != p.Mainline.ManifestSHA256 || mainlineSHA(r.ml.TemplateBytes()) != p.Chain.TemplateSHA:
		return errors.New("the mainline payload is not the one the plan was made with")
	case r.opt.MaxWrite < 0 || r.opt.MaxWrite%gpt.Sector != 0:
		return fmt.Errorf("MaxWrite %d is not a multiple of 512", r.opt.MaxWrite)
	case r.opt.Stash == nil:
		return errors.New("no stash is given")
	case r.opt.Gate == nil:
		return errors.New("no payload gate is given")
	}
	if g := r.opt.Gate.Check(r.ml); !g.OK() || p.Gate == nil || g.Reviewed == nil || p.Gate.Reviewed == nil || *g.Reviewed != *p.Gate.Reviewed || !g.Reviewed.AllowsSlot(p.Target) {
		return errors.New("the payload does not pass the payload gate now, or not as the plan's reviewed payload")
	}
	if err := mainlineKeyRule(&p.MainlinePlan, r.opt.KeyRecord); err != nil {
		return err
	}
	if err := DirectStructure(p); err != nil {
		return fmt.Errorf("the plan is not the shape of a direct plan: %w", err)
	}
	if len(p.Checks) == 0 {
		return errors.New("the plan has no checks")
	}
	for _, c := range p.Checks {
		if !c.OK {
			return fmt.Errorf("the plan's check %s failed: %s", c.ID, c.Detail)
		}
	}
	if !p.Unlock.Confirmed || p.UnlockDirect.Basis == "" {
		return errors.New("the plan has no unlock basis")
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
		if err := directSourceRule(src); err != nil {
			return fmt.Errorf("step %d: %w", s.N, err)
		}
	}
	if writes != len(p.Sources) || writes != len(plan.MainlineParts()) {
		return fmt.Errorf("%d write steps, %d write sources", writes, len(p.Sources))
	}
	if err := directBinding(p, r.ml, r.opt.Gate); err != nil {
		return err
	}
	// the stash now: each part the plan's
	for _, cp := range p.Stash.Parts {
		if b := r.opt.Stash.Part(cp.Part); int64(len(b)) != cp.Size || mainlineSHA(b) != cp.SHA256 || cp.SHA256 != cp.Want {
			return fmt.Errorf("the stash's %s is not the plan's", cp.Part)
		}
	}
	t, err := r.m.Prepare(&p.Plan, r.opt.Hooks)
	if err != nil {
		return err
	}
	r.t = t
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

// source returns the bytes of one write of the stash, or of the rootfs.
func (r *dRunner) source(src *plan.WriteSource) (io.Reader, error) {
	part, _ := strings.CutSuffix(src.Partition, "_"+plan.DirectTarget)
	switch src.From {
	case plan.SrcStash:
		b := r.opt.Stash.Part(part)
		if int64(len(b)) != src.Length || mainlineSHA(b) != src.SHA256 {
			return nil, fmt.Errorf("%s: the stash's %s is %d bytes sha256 %s, the plan's %d bytes %s", src.Partition, part, len(b), mainlineSHA(b), src.Length, src.SHA256)
		}
		return bytes.NewReader(b), nil
	case plan.SrcStashPatchedVbmet:
		signed := r.opt.Stash.Part("vbmeta")
		if mainlineSHA(signed) != src.SourceSHA256 {
			return nil, fmt.Errorf("the stash's vbmeta is sha256 %s, the plan's source is %s", mainlineSHA(signed), src.SourceSHA256)
		}
		out, pf, err := mainline.PatchVbmeta(signed, uint64(src.BootImageSize))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", src.Partition, err)
		}
		if int64(len(out)) != src.Length || pf.SHA256 != src.SHA256 || r.dp.Chain.VbmetaPatch == nil || r.dp.Chain.VbmetaPatch.SHA256 != pf.SHA256 {
			return nil, fmt.Errorf("the patched vbmeta is %d bytes sha256 %s, the plan's %d bytes %s", len(out), pf.SHA256, src.Length, src.SHA256)
		}
		return bytes.NewReader(out), nil
	case plan.SrcAssembledStash:
		res := r.opt.Stash.Part("resource")
		if mainlineSHA(res) != src.ResourceSHA256 {
			return nil, fmt.Errorf("the stash's resource is sha256 %s, the plan's is %s", mainlineSHA(res), src.ResourceSHA256)
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
		return r.src.source(src)
	}
	return nil, fmt.Errorf("%s: unknown source %q", src.Partition, src.From)
}

func (r *dRunner) write(s plan.Step) error {
	w := s.Write
	src := r.dp.Source(w.Partition)
	if src == nil {
		return fmt.Errorf("%s: no write source", w.Partition)
	}
	if err := directSourceRule(src); err != nil {
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
		max = 1 << 20
	}
	c := &chunker{w: pw, h: sha256.New(), max: max}
	var dst io.Writer = c
	var rh *plan.ResourceHasher
	if src.From == plan.SrcAssembledStash {
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

// RunMainlineDirect runs a direct plan against a medium. On any error it
// stops at once: slot B then stays disabled (or was never touched), and the
// keeper A boots (before R(K): A is the higher-priority trying slot).
func RunMainlineDirect(p *plan.DirectPlan, ml *mainline.Payload, m Medium, opt DirectOptions) (res *Result, err error) {
	base := &runner{p: &p.Plan, m: m, opt: opt.Options}
	r := &dRunner{runner: base, dp: p, ml: ml, src: &mlRunner{runner: base, mp: &p.MainlinePlan, ml: ml}, opt: opt}
	defer func() {
		if ferr := m.Finish(); ferr != nil && err == nil {
			err = ferr
		}
	}()
	if p.UnlockDirect.Write != nil && !opt.LockWritten {
		return nil, fmt.Errorf("precheck: the plan needs the lock-state write first (lock_state %s -> %s): only the device mode in the installer RAM image writes it, before any target write; nothing was written",
			p.UnlockDirect.Write.From, p.UnlockDirect.Write.To)
	}
	if err := r.precheck(); err != nil {
		return nil, fmt.Errorf("precheck: %w", err)
	}
	r.log(map[string]any{"event": "start", "plan": p.ID, "kind": p.Kind, "mode": p.Mode, "source": p.Disk.Source, "ab": r.ab32(),
		"keeper": p.Keeper, "target": p.Target})
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
		case "keep-key":
			r.say("%2d/%d keep-key: the firmware key on the unit (backup, 2 sectors)", s.N, len(p.Steps))
			err = r.keepKey(s)
		case "bootctl":
			r.say("%2d/%d bootctl %s", s.N, len(p.Steps), strings.Join(argsShown(s.Bootctl.Args), " "))
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
			err = fmt.Errorf("step %q is not a step of a direct plan", s.Op)
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

// argsShown is the argv for the screen: the long --expect-sector hex of R(K)
// shortened (the log keeps it whole).
func argsShown(a []string) []string {
	out := append([]string(nil), a...)
	for i := range out {
		if len(out[i]) > 64 {
			out[i] = out[i][:16] + "…"
		}
	}
	return out
}
