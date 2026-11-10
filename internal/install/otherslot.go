package install

// The executor of an other-slot plan (plan.ComputeOtherSlot, mainline step 2:
// MAINLINE-STEP2-DESIGN.md §3, §11). The running slot K is the keeper; the
// plan writes the other slot T. It runs the steps in order on a Medium, as
// RunMainline does, and reuses unchanged: Run's A/B step (bootctlStep) and
// A/B check (checkAB), the chunker, and the
// mainline executor's byte sources (mlRunner.source: the keeper's partition
// of the same name, the template with the keeper's resource, the rootfs).
// The two data steps (data-format, data-migrate) run through the caller's
// hook OtherSlotOptions.Data; a plan with data steps and no hook is refused
// before anything is written. This file adds code only.

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

// OtherSlotOptions are the options of an other-slot run: the options of Run,
// and the data hook.
type OtherSlotOptions struct {
	Options
	// Data runs one data step of the plan (op plan.OpDataFormat or
	// plan.OpDataMigrate) on the plan's data partition. Its error stops the
	// run: the target is disabled then (the data steps come after the
	// verify and before the arm), so the keeper boots. nil refuses a plan
	// with data steps. data-format must make ext4 d.FormatAs (no slot
	// mounts it) and refuse a mounted partition; data-migrate must relabel
	// it d.Label as its last action.
	Data func(op string, d *plan.DataPlan) error
	// Gate is the payload gate; the precheck checks the payload with it
	// again (review F2). Required.
	Gate *mainline.Gate
	// BootID is /proc/sys/kernel/random/boot_id now (task 4, review F2): a
	// slot plan runs only in the boot it was made in; "" refuses a slot plan.
	BootID string
}

type osRunner struct {
	*runner
	op  *plan.OtherSlotPlan
	ml  *mainline.Payload
	src *mlRunner // only its source(): the bytes of a write
	opt OtherSlotOptions
}

// ExecuteOtherSlot runs an other-slot plan against an image file (the dry run).
func ExecuteOtherSlot(p *plan.OtherSlotPlan, ml *mainline.Payload, im *Image, opt OtherSlotOptions) (*Result, error) {
	return RunOtherSlot(p, ml, &dryMedium{im: im, tool: opt.Bootctl}, opt)
}

func slotIndex(s string) int {
	if s == "b" {
		return 1
	}
	return 0
}

// OtherSlotStructure is the exact shape of a plan of plan.ComputeOtherSlot:
// disable --slot T; the six writes of T in order (the four chain parts may be
// skips); sync; verify of exactly those six; with data, data-format and
// data-migrate; arm-trying --slot T --lower-keeper; check-ab. The A/B
// states are recomputed from the plan's start and must be the plan's: the
// keeper K successful and booting until the arm and T unbootable, after the
// arm T {15,7,0} next with K successful. K must be the plan's running slot.
func OtherSlotStructure(p *plan.OtherSlotPlan) error {
	if p.Running.Slot != "a" && p.Running.Slot != "b" || p.Keeper != p.Running.Slot || p.Target == p.Keeper ||
		(p.Target != "a" && p.Target != "b") {
		return fmt.Errorf("keeper %q target %q running slot %q: the keeper must be the running slot and the target the other", p.Keeper,
			p.Target, p.Running.Slot)
	}
	k, t := slotIndex(p.Keeper), slotIndex(p.Target)
	data := p.Data != nil
	parts := plan.MainlineParts()
	n := len(parts)
	want := n + 5
	if data {
		want += 2
	}
	if len(p.Steps) != want {
		return fmt.Errorf("%d steps, an other-slot plan has %d", len(p.Steps), want)
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
	d := p.Steps[i]
	if !argv(d, "disable", "--slot", p.Target) || !same(d.Bootctl.Before, now) || !same(d.Bootctl.After, s1) || d.Bootctl.NoOp != (s1 == now) {
		return fmt.Errorf("step 1 is not disable --slot %s with the predicted states", p.Target)
	}
	i++
	var names []string
	for _, part := range parts {
		s := p.Steps[i]
		name := part + "_" + p.Target
		chain := part != "boot" && part != "system"
		switch {
		case s.Write == nil || s.Write.Partition != name || s.Write.Offset != 0 || s.Bootctl != nil:
			return fmt.Errorf("step %d is not the write of %s", s.N, name)
		case s.Op == "write" && s.Write.Action == "write":
		case s.Op == "skip" && s.Write.Action == "skip-identical" && chain:
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
		return errors.New("the verify step does not cover exactly the six writes of the target")
	}
	i++
	if data {
		if !plain(p.Steps[i], plan.OpDataFormat) || !plain(p.Steps[i+1], plan.OpDataMigrate) {
			return errors.New("the two steps after the verify are not data-format and data-migrate")
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

// otherSlotSourceRule: an other-slot install writes only the six partitions
// of the target (plan.MainlineParts; never vendor, oem or anything of the
// keeper), uboot, trust, dtbo and vbmeta only from the keeper's partition of
// the same name, boot only assembled, system only from rootfs.ext4.
func otherSlotSourceRule(src *plan.WriteSource, target, keeper string) error {
	part, ok := strings.CutSuffix(src.Partition, "_"+target)
	in := false
	for _, n := range plan.MainlineParts() {
		in = in || ok && part == n
	}
	if !in {
		return fmt.Errorf("%s is not a partition that an other-slot install writes", src.Partition)
	}
	want := "keeper"
	switch part {
	case "boot":
		want = "assembled-boot"
	case "system":
		want = "rootfs"
	}
	if src.From != want || (want == "keeper") != (src.KeeperPartition == part+"_"+keeper) {
		return fmt.Errorf("%s: source %q %q, an other-slot install takes it only from %s", src.Partition, src.From, src.KeeperPartition, want)
	}
	return nil
}

// otherSlotVbmeta (review F4): uboot_T, trust_T and dtbo_T must be the
// payload's required boot chain; vbmeta_T is a byte copy of vbmeta_K, the
// patched vbmeta of contract v2. Its bytes must be the payload's
// vbmeta_patched (the payload the precheck passed through the gate again, not
// the plan's record), the plan's chain patch and boot cap, and, when the gate
// checks the patch (the release gate always does), a reviewed patch of it.
// The write itself checks the sha256 of what it copies.
func otherSlotVbmeta(p *plan.OtherSlotPlan, ml *mainline.Payload, g *mainline.Gate) error {
	src, vp, ch := p.Source("vbmeta_"+p.Target), ml.Manifest.VbmetaPatched, p.Chain
	// uboot, trust and dtbo of T are bound to the payload's required boot
	// chain the same way (review T1 MINOR-1: not only to the plan's record)
	rb := ml.Manifest.RequiresBootchain
	for part, want := range map[string]string{"uboot": rb.UBootSHA256, "trust": rb.TrustSHA256, "dtbo": rb.DtboSHA256} {
		if cs := p.Source(part + "_" + p.Target); cs == nil || cs.SHA256 != want {
			return fmt.Errorf("%s_%s: the plan's source is not the payload's required boot chain (%s)", part, p.Target, want)
		}
	}
	switch {
	case src == nil || ch.VbmetaPatch == nil:
		return errors.New("the plan has no vbmeta source or no vbmeta patch")
	case src.SHA256 != vp.SHA256 || src.Length != vp.Size || ch.VbmetaPatch.SHA256 != vp.SHA256 ||
		ch.VbmetaPatch.SourceSHA256 != vp.SourceSHA256 || ch.BootCap != vp.BootImageSize || int64(ch.VbmetaPatch.NewImageSize) != vp.BootImageSize:
		return fmt.Errorf("vbmeta_%s would be %d bytes sha256 %s (boot cap %d); the payload's patched vbmeta is %d bytes %s (image_size %d)",
			p.Target, src.Length, src.SHA256, ch.BootCap, vp.Size, vp.SHA256, vp.BootImageSize)
	case !g.ChecksVbmeta() && (g.Name() != "test" || p.Gate == nil || p.Gate.Gate != "test"):
		return errors.New("the gate does not check the patched vbmeta, and it is not a test gate")
	case g.ChecksVbmeta() && !mainline.FindVbmetaPatch(g.ReviewedVbmeta(), vp.SourceSHA256, vp.SHA256).Matches(*ch.VbmetaPatch, int(src.Length)):
		return errors.New("the patched vbmeta is not a reviewed patch of the payload gate")
	}
	return nil
}

// otherSlotChecked: a plan runs only with every check passed, the
// confirmation, the kept key and the payload gate passed.
func otherSlotChecked(p *plan.OtherSlotPlan) error {
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
	case p.Key.Copies != 2 || !p.Key.Equal:
		return errors.New("the plan does not show the firmware key kept on the unit")
	case !p.Gate.OK():
		return errors.New("the plan's payload did not pass the payload gate")
	case p.Mode == "slot" && len(p.KeeperSHA256) == 0:
		return errors.New("a slot plan has no keeper hashes")
	case p.Mode == "slot" && p.Running.BootID == "":
		return errors.New("a slot plan has no boot id")
	}
	if len(p.KeeperSHA256) > 0 {
		static := plan.KeeperStaticParts()
		if len(p.KeeperSHA256) != len(static) {
			return fmt.Errorf("the plan hashes %d keeper parts, want exactly %d (%s)", len(p.KeeperSHA256), len(static), strings.Join(static, ", "))
		}
		for _, part := range static {
			if len(p.KeeperSHA256[part+"_"+p.Keeper]) != 64 {
				return fmt.Errorf("the plan has no keeper hash of %s_%s", part, p.Keeper)
			}
		}
	}
	return nil
}

// keeperStatic hashes the keeper's static parts again (plan.KeeperStaticParts):
// the parts that nothing writes while the keeper runs. Run's keeperUnchanged
// hashes all eight, which fails on a running system (review F3).
func (r *osRunner) keeperStatic() error {
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

func (r *osRunner) precheck() error {
	p := r.op
	switch {
	case p.ID == "" || !p.VerifyID():
		return errors.New("the plan has no valid id (a refused or edited plan)")
	case p.Kind != plan.OtherSlotKind || p.Payload != nil || p.Mainline == nil || p.Chain == nil:
		return errors.New("not an other-slot plan")
	case p.Mode != r.m.Kind():
		// a "slot" plan needs the slot medium of the running system (not
		// built yet: MAINLINE-STEP2-DESIGN §13); the dry medium runs only
		// "dry-run" plans
		return fmt.Errorf("plan mode %q: this medium runs only %q plans", p.Mode, r.m.Kind())
	case !r.ml.Verified() || r.ml.ManifestSHA256 != p.Mainline.ManifestSHA256 ||
		mainlineSHA(r.ml.TemplateBytes()) != p.Chain.TemplateSHA:
		return errors.New("the mainline payload is not the one the plan was made with")
	case r.opt.MaxWrite < 0 || r.opt.MaxWrite%gpt.Sector != 0:
		return fmt.Errorf("MaxWrite %d is not a multiple of 512", r.opt.MaxWrite)
	case r.opt.KeyRecord != nil:
		return errors.New("an other-slot install has no keep-key step")
	case p.Data != nil && r.opt.Data == nil:
		return errors.New("the plan has data steps, but no data hook is given")
	case p.Data != nil && (p.Data.Partition != plan.DataPartition || p.Data.Label != plan.DataLabel || p.Data.FSType != "ext4" ||
		p.Data.FormatAs != plan.DataFormatLabel):
		return fmt.Errorf("the data step is for %s %s %s/%s, not %s ext4 %s/%s", p.Data.Partition, p.Data.FSType, p.Data.FormatAs,
			p.Data.Label, plan.DataPartition, plan.DataFormatLabel, plan.DataLabel)
	case r.opt.Gate == nil:
		return errors.New("no payload gate is given")
	case p.Mode == "slot" && (r.opt.BootID == "" || r.opt.BootID != p.Running.BootID):
		return fmt.Errorf("the plan is of boot %q, this boot is %q: make a new plan (review F2)", p.Running.BootID, r.opt.BootID)
	case r.opt.BootID != "" && p.Running.BootID != "" && r.opt.BootID != p.Running.BootID:
		return fmt.Errorf("the plan is of boot %q, this boot is %q: make a new plan", p.Running.BootID, r.opt.BootID)
	}
	// the payload gate and the kept key, again (the plan's records are not
	// trusted: review F2)
	if g := r.opt.Gate.Check(r.ml); !g.OK() || p.Gate == nil || g.Reviewed == nil || p.Gate.Reviewed == nil ||
		*g.Reviewed != *p.Gate.Reviewed || !g.Reviewed.AllowsSlot(p.Target) {
		return errors.New("the payload does not pass the payload gate now, or not as the plan's reviewed payload")
	}
	if err := OtherSlotStructure(p); err != nil {
		return fmt.Errorf("the plan is not the shape of an other-slot plan: %w", err)
	}
	if err := otherSlotChecked(p); err != nil {
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
		if err := otherSlotSourceRule(src, p.Target, p.Keeper); err != nil {
			return fmt.Errorf("step %d: %w", s.N, err)
		}
	}
	if writes != len(p.Sources) {
		return fmt.Errorf("%d write steps, %d write sources", writes, len(p.Sources))
	}
	if err := otherSlotVbmeta(p, r.ml, r.opt.Gate); err != nil {
		return err
	}
	t, err := r.m.Prepare(&p.Plan, r.opt.Hooks)
	if err != nil {
		return err
	}
	r.t = t
	if k := plan.ReadKeyGate(r.m.Reader(), t); k.Copies != 2 || !k.Equal || k.LBAs != p.Key.LBAs {
		return fmt.Errorf("the firmware key is not kept on the unit now: %s", k.Why)
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

// write writes one step from its source, through the chunker, and checks
// the length and the sha256 of what was written (and of the boot resource).
func (r *osRunner) write(s plan.Step) error {
	w := s.Write
	src := r.op.Source(w.Partition)
	if src == nil {
		return fmt.Errorf("%s: no write source", w.Partition)
	}
	if err := otherSlotSourceRule(src, r.p.Target, r.p.Keeper); err != nil {
		return fmt.Errorf("write refused: %w", err)
	}
	if err := plan.CheckWriteRange(r.t, r.p.Target, w.Partition, w.Offset, w.Length); err != nil {
		return fmt.Errorf("write refused: %w", err)
	}
	if w.Offset != 0 || w.Length != src.Length {
		return fmt.Errorf("%s: range %d+%d is not the source's 0+%d", w.Partition, w.Offset, w.Length, src.Length)
	}
	in, err := r.src.source(src) // r.src shares this runner (and its table)
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

// RunOtherSlot runs an other-slot plan against a medium. On any error it
// stops at once: the target then stays disabled (or was never touched), so
// the running slot boots.
func RunOtherSlot(p *plan.OtherSlotPlan, ml *mainline.Payload, m Medium, opt OtherSlotOptions) (res *Result, err error) {
	base := &runner{p: &p.Plan, m: m, opt: opt.Options}
	r := &osRunner{runner: base, op: p, ml: ml, src: &mlRunner{runner: base, ml: ml}, opt: opt}
	defer func() {
		if ferr := m.Finish(); ferr != nil && err == nil {
			err = ferr
		}
	}()
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
			err = fmt.Errorf("step %q is not a step of an other-slot plan", s.Op)
		}
		r.log(map[string]any{"step": s.N, "op": s.Op, "ab_before": before, "ab_after": r.ab32(),
			"writes": m.Writes(), "ms": time.Since(start).Milliseconds(), "error": errString(err)})
		if err != nil {
			return &Result{Writes: m.Writes(), Steps: s.N - 1}, fmt.Errorf("step %d (%s): %w", s.N, s.Op, err)
		}
	}
	if err := r.keeperStatic(); err != nil {
		return &Result{Writes: m.Writes(), Steps: len(p.Steps)}, err
	}
	return &Result{Writes: m.Writes(), FinalAB: p.AB.Final, Steps: len(p.Steps)}, nil
}
