package install_test

// The step-2 plan kind clone (plan.ComputeClone, install.RunClone;
// DESIGN-R23 §4) on the dry medium with the real uc-pxx-bootctl
// (UC_PXX_BOOTCTL; skipped without it): a full run, the refusals of the
// plan, every stop before the arm (a changed byte in the copy's read-back,
// the difference, the content check, the freeze, the thaw, the thaw guard),
// the power-cut sweep (a cut before every primitive write, clean and torn,
// each checked with CheckCutState and resumed), and the rule that the run
// writes nothing of its own while the root is frozen. The hooks of the
// caller (the freeze, the mount of the difference, the content check) are
// made up here; package step2's tests run the real difference and content
// check on an ext4.

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/bootctl"
	"uc-pxx-installer/internal/clone"
	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/mainline/mltest"
	"uc-pxx-installer/internal/plan"
)

func hexSum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

var (
	cloneRefA   = hexSum([]byte("ref-a"))
	cloneRefB   = hexSum([]byte("ref-b"))
	cloneBundle = hexSum([]byte("bundle"))
)

// cloneGate is a test clone gate whose chain is the fixture's mainline
// chain (slot B's), with one profile.
func cloneGate(fx *fakeemmc.Fixture, res []byte) *clone.Gate {
	g := clone.EmbeddedGate()
	g.Name = "test"
	pin := func(part string, b []byte) clone.ChainPin {
		return clone.ChainPin{Part: part, Size: int64(len(b)), SHA256: hexSum(b)}
	}
	g.Chain = []clone.ChainPin{pin("uboot", fx.New["uboot"]), pin("trust", fx.New["trust"]), pin("dtbo", fx.New["dtbo"]),
		pin("vbmeta", mlVbmetaPatched(fx)), pin("boot", mltest.Expected(mlToken, mlLinux, 11, res))}
	g.Profiles = []clone.Profile{{Name: "kiosk-ha", BuildID: "0.2.0-rc10-kiosk-ha\n", RefA: cloneRefA, RefB: cloneRefB, BundleSHA256: cloneBundle}}
	g.KeeperFiles = []clone.PathPin{{Path: clone.ProvisionBinary, Size: 27, SHA256: clone.SHA256([]byte("the provision binary (test)"))}}
	g.FreezeBound = 2 * time.Second
	return g
}

func doneJSON(profile, refB, bundle string) []byte {
	b, _ := json.Marshal(clone.DoneRecord{Schema: 1, Kind: clone.DoneRecordKind, Release: "0.2.0-rc10", Profile: profile,
		BundleSHA256: bundle, RefBSHA256: refB, InstalledSHA256: hexSum([]byte("installed"))})
	return b
}

// freezer is a made-up freeze of the keeper's root: it records the freeze
// and the thaw, and whether anything of the run was said or logged while
// frozen.
type freezer struct {
	mu                     sync.Mutex
	frozen                 bool
	freezes, thaws         int
	saidFrozen, loggedWhen int
	failFreeze, failThaw   bool
}

func (f *freezer) freeze() (func() error, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failFreeze {
		return nil, errors.New("FIFREEZE /: made-up error")
	}
	f.frozen, f.freezes = true, f.freezes+1
	return func() error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.thaws++
		if f.failThaw {
			return errors.New("FITHAW /: made-up error")
		}
		f.frozen = false
		return nil
	}, nil
}

func (f *freezer) isFrozen() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.frozen }

type frozenLog struct{ f *freezer }

func (l frozenLog) Write(b []byte) (int, error) {
	if l.f.isFrozen() {
		l.f.mu.Lock()
		l.f.loggedWhen++
		l.f.mu.Unlock()
	}
	return len(b), nil
}

type cloneRig struct {
	*rig
	res  []byte
	gate *clone.Gate
	done []byte
	fz   *freezer
	data *dataHook
	// the hooks' faults
	deltaErr   error
	contentErr error // the content-check hook's error (a forbidden file)
	contentSum string
	deltas     int
	out        bytes.Buffer
}

// newCloneRig: slot B runs the mainline chain and holds system_b (random
// bytes), slot A holds what ("old": the fixture's old images; "new": the
// chain already).
func newCloneRig(t *testing.T, tool bootctl.Tool, what string) *cloneRig {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	_, res := mlBundle(t, fx)
	u := osUnitOf(fx, res, [2][3]byte{{14, 0, 1}, {15, 0, 1}}, "b", what)
	sys := make([]byte, 6144*512)
	rand.Read(sys)
	u.Tweak["system_b"] = sys
	r := &cloneRig{rig: newRig(t, fx, tool, u), res: res, gate: cloneGate(fx, res), done: doneJSON("kiosk-ha", cloneRefB, cloneBundle),
		fz: &freezer{}, data: &dataHook{}, contentSum: cloneRefA}
	return r
}

func (r *cloneRig) makePlan(im *install.Image) (*plan.OtherSlotPlan, error) {
	sec := make([]byte, 512)
	im.Reader().ReadAt(sec, plan.ABLBA*512)
	st, _, err := r.tool.Status(sec)
	if err != nil {
		r.t.Fatalf("bootctl status: %v", err)
	}
	info, err := r.tool.Info()
	if err != nil {
		r.t.Fatal(err)
	}
	in := plan.CloneInput{Input: plan.Input{Disk: im.Reader(), DiskBytes: im.Size(), Source: "image:" + r.path, Mode: "dry-run",
		Tool: "test", Bootctl: info, BootctlStatus: st, HashKeeper: true},
		Running: plan.ParseRunning(osUBoot("b"), "console=ttyS2 uc-pxx.id="+mlToken, "system_b"),
		Gate:    r.gate, Done: r.done, Confirm: plan.ConfirmGate{Confirmed: true}, Data: true,
		KeyPins: testKeyPins(), KeyPinSet: plan.KeyPinSetTest}
	return plan.ComputeClone(in)
}

func (r *cloneRig) opts(h *install.Hooks) install.CloneOptions {
	return install.CloneOptions{Options: install.Options{Bootctl: r.tool, Hooks: h, MaxWrite: 64 << 10,
		Say: func(l string) {
			if r.fz.isFrozen() {
				r.fz.mu.Lock()
				r.fz.saidFrozen++
				r.fz.mu.Unlock()
			}
			fmt.Fprintln(&r.out, l)
		}, Log: frozenLog{r.fz}},
		Data: r.data.run, Gate: r.gate, KeyPins: testKeyPins(), KeyPinSet: plan.KeyPinSetTest, Freeze: r.fz.freeze,
		Delta: func(c *plan.ClonePart) error {
			r.deltas++
			if r.deltaErr != nil {
				return r.deltaErr
			}
			// the made-up difference: one sector of system_a changes
			f, err := os.OpenFile(r.path, os.O_WRONLY, 0)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = f.WriteAt(bytes.Repeat([]byte{0xd5}, 512), sysAOff(r.rig)+8192)
			return err
		},
		Content: func(c *plan.ClonePart) (string, error) { return r.contentSum, r.contentErr }}
}

func sysAOff(r *rig) int64 {
	l := fakeemmc.ParseSmallLayout(fakeemmc.SmallLayout)
	for _, p := range l.Entries {
		if p.Name == "system_a" {
			return p.Start * 512
		}
	}
	panic("no system_a")
}

func (r *cloneRig) run(h *install.Hooks) (*plan.OtherSlotPlan, *install.CloneResult, error) {
	im, err := install.OpenImage(r.path)
	if err != nil {
		r.t.Fatal(err)
	}
	defer im.Close()
	p, err := r.makePlan(im)
	if err != nil {
		r.t.Fatalf("plan: %v", err)
	}
	res, err := install.ExecuteClone(p, im, r.opts(h))
	return p, res, err
}

func (r *cloneRig) ab() avbab.AB {
	b, _ := os.ReadFile(r.path)
	a, _ := avbab.Parse(b[plan.ABLBA*512:])
	return a
}

// stoppedBeforeArm: A {0,0,0} (or as before), B successful and the slot
// that boots, the cardinal rule of CheckCutState, no data step after a stop
// before them.
func (r *cloneRig) stoppedBeforeArm(p *plan.OtherSlotPlan, what string) {
	t := r.t
	b, _ := os.ReadFile(r.path)
	cs, err := install.CheckCutState(bytes.NewReader(b), &p.Plan, r.start)
	if err != nil {
		t.Fatalf("%s: the cardinal rule: %v", what, err)
	}
	a := r.ab()
	if cs.Boots != "keeper" || a.Pick() != 1 || !a.Slots[1].Keeper() || a.Slots[0].Bootable() {
		t.Fatalf("%s: A %v B %v boots %s (%s)", what, a.Slots[0], a.Slots[1], cs.Boots, cs.Target)
	}
}

func TestCloneDryRun(t *testing.T) {
	tool := realTool(t)
	for _, what := range []string{"old", "new"} {
		t.Run("A "+what, func(t *testing.T) {
			r := newCloneRig(t, tool, what)
			p, res, err := r.run(nil)
			if err != nil {
				t.Fatalf("%v\n%s", err, r.out.String())
			}
			if p.Kind != plan.CloneKind || p.Clone.AttemptKey != "clone "+cloneRefA || p.Keeper != "b" || p.Target != "a" {
				t.Fatalf("plan %s %+v", p.Kind, p.Clone)
			}
			skips := 0
			for _, s := range p.Steps {
				if s.Op == "skip" {
					skips++
				}
			}
			// "new": uboot, trust and dtbo are the chain already; vbmeta_a is
			// the signed one, boot_a the fixture's boot
			if what == "new" && skips != 3 || what == "old" && skips != 0 {
				t.Fatalf("A %s: %d skips", what, skips)
			}
			a := r.ab()
			if a.Slots[0] != (avbab.Slot{Priority: 15, Tries: 7}) || a.Slots[1] != (avbab.Slot{Priority: 14, Successful: 1}) || a.Pick() != 0 {
				t.Fatalf("after the run A %v B %v", a.Slots[0], a.Slots[1])
			}
			if strings.Join(r.data.ops, " ") != "data-format data-migrate" || r.deltas != 1 {
				t.Fatalf("data %v deltas %d", r.data.ops, r.deltas)
			}
			if r.fz.freezes != 1 || r.fz.thaws != 1 || r.fz.saidFrozen != 0 || r.fz.loggedWhen != 0 {
				t.Fatalf("freezer %+v", r.fz)
			}
			if res.Guard || res.Frozen <= 0 || res.Frozen > r.gate.FreezeBound || !strings.Contains(r.out.String(), "was frozen for") {
				t.Fatalf("frozen %s guard %v\n%s", res.Frozen, res.Guard, r.out.String())
			}
			t.Logf("frozen %s for the copy of %d bytes (the made-up freeze, the dry medium)", res.Frozen, p.Clone.SystemBytes)
			img, _ := os.ReadFile(r.path)
			if !sameOutside(img, r.before, &p.Plan) {
				t.Fatal("a byte outside the target's write ranges and the A/B sector changed")
			}
			sa, sb := sysAOff(r.rig), sysAOff(r.rig)+6144*512
			want := append([]byte(nil), img[sb:sb+6144*512]...)
			copy(want[8192:], bytes.Repeat([]byte{0xd5}, 512))
			if !bytes.Equal(img[sa:sa+6144*512], want) || hexSum(r.before[sb:sb+6144*512]) != res.SystemSHA256 || res.After != hexSum(want) {
				t.Fatal("system_a is not system_b with the made-up difference")
			}
			for _, c := range r.gate.Chain {
				var from []byte
				switch c.Part {
				case "vbmeta":
					from = mlVbmetaPatched(r.fx)
				case "boot":
					from = mltest.Expected(mlToken, mlLinux, 11, r.res)
				default:
					from = r.fx.New[c.Part]
				}
				if hexSum(from) != c.SHA256 {
					t.Fatalf("%s pin", c.Part)
				}
			}
		})
	}
}

// TestCloneContentNotCompared (mainline-rc-10b): system_a's content manifest
// differs from ref_a (as a unit's would, owner 2026-11-16): the clone still
// runs to the arm; the plan holds no keeper content; the digest is logged.
func TestCloneContentNotCompared(t *testing.T) {
	tool := realTool(t)
	r := newCloneRig(t, tool, "old")
	r.contentSum = hexSum([]byte("not ref_a"))
	p, _, err := r.run(nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, r.out.String())
	}
	if p.Clone.KeeperContent != "" || p.Clone.KeeperEntries != 0 {
		t.Fatalf("keeper content %q %d", p.Clone.KeeperContent, p.Clone.KeeperEntries)
	}
	for _, c := range p.Checks {
		if c.ID == "keeper-content" {
			t.Fatalf("a keeper-content check: %+v", c)
		}
	}
	if a := r.ab(); a.Slots[0] != (avbab.Slot{Priority: 15, Tries: 7}) || !a.Slots[1].Keeper() {
		t.Fatalf("after the run A %v B %v", a.Slots[0], a.Slots[1])
	}
	if strings.Join(r.data.ops, " ") != "data-format data-migrate" {
		t.Fatalf("data %v", r.data.ops)
	}
}

// TestClonePlanRefusals: each refusal of the plan writes nothing.
func TestClonePlanRefusals(t *testing.T) {
	tool := realTool(t)
	cases := []struct {
		name, check string
		mod         func(r *cloneRig)
	}{
		{"chain pin mismatch", "keeper-chain", func(r *cloneRig) { r.gate.Chain[4].SHA256 = hexSum([]byte("other boot")) }},
		{"no done record", "clone-done", func(r *cloneRig) { r.done = nil }},
		{"done record malformed", "clone-done", func(r *cloneRig) { r.done = []byte(`{"schema":1}`) }},
		{"done record of another profile", "clone-profile", func(r *cloneRig) { r.done = doneJSON("kiosk", cloneRefB, cloneBundle) }},
		{"done record with another ref_b", "clone-profile", func(r *cloneRig) { r.done = doneJSON("kiosk-ha", cloneRefA, cloneBundle) }},
		{"no profile in the gate (the release today)", "clone-profile", func(r *cloneRig) { r.gate.Profiles = nil }},
		{"a malformed gate", "clone-gate", func(r *cloneRig) { r.gate.FreezeBound = time.Hour }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newCloneRig(t, tool, "old")
			c.mod(r)
			im, err := install.OpenImage(r.path)
			if err != nil {
				t.Fatal(err)
			}
			defer im.Close()
			p, err := r.makePlan(im)
			if !errors.Is(err, plan.ErrRefused) || p == nil || len(p.Steps) != 0 {
				t.Fatalf("not refused: %v", err)
			}
			failed := false
			for _, ch := range p.Checks {
				failed = failed || ch.ID == c.check && !ch.OK
			}
			if !failed {
				t.Fatalf("check %s did not fail: %v", c.check, err)
			}
			if img, _ := os.ReadFile(r.path); !bytes.Equal(img, r.before) {
				t.Fatal("the image changed")
			}
		})
	}
	// system_a of another size
	t.Run("size mismatch", func(t *testing.T) {
		fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
		_, res := mlBundle(t, fx)
		u := osUnitOf(fx, res, [2][3]byte{{14, 0, 1}, {15, 0, 1}}, "b", "old")
		u.Layout = strings.Replace(fakeemmc.SmallLayout, "22 system_a 59648 6144", "22 system_a 59648 6136", 1)
		r := &cloneRig{rig: newRig(t, fx, tool, u), res: res, gate: cloneGate(fx, res), done: doneJSON("kiosk-ha", cloneRefB, cloneBundle),
			fz: &freezer{}, data: &dataHook{}}
		im, err := install.OpenImage(r.path)
		if err != nil {
			t.Fatal(err)
		}
		defer im.Close()
		if _, err := r.makePlan(im); err == nil || !strings.Contains(err.Error(), "must have the same size") {
			t.Fatalf("not refused for the size: %v", err)
		}
	})
}

// TestCloneStopsBeforeArm: every refusal or mismatch of the copy, the
// verify, the difference and the content check stops before the arm (and
// before the data steps): A stays {0,0,0}, B boots.
func TestCloneStopsBeforeArm(t *testing.T) {
	tool := realTool(t)
	cases := []struct {
		name, want string
		mod        func(r *cloneRig) *install.Hooks
	}{
		{"a changed byte in the copy read-back", "verify: system_a reads back", func(r *cloneRig) *install.Hooks {
			return &install.Hooks{CutAfter: -1, Observe: func(ev install.Event) error {
				if ev.Partition == "system_a" && ev.Off+ev.Len == sysAOff(r.rig)+6144*512 {
					f, _ := os.OpenFile(r.path, os.O_WRONLY, 0)
					f.WriteAt([]byte{0x5a}, sysAOff(r.rig)+12345)
					f.Close()
				}
				return nil
			}}
		}},
		{"the difference does not apply", "the difference failed", func(r *cloneRig) *install.Hooks {
			r.deltaErr = errors.New("the difference failed (made up: e2fsck not clean)")
			return nil
		}},
		{"a forbidden file in system_a", "content-check", func(r *cloneRig) *install.Hooks {
			r.contentErr = errors.New("forbidden: /usr/libexec/uc-pxx-step2/uc-pxx-bootctl-inst is uc-pxx-bootctl-inst r2 (made up)")
			return nil
		}},
		{"the freeze fails", "FIFREEZE", func(r *cloneRig) *install.Hooks { r.fz.failFreeze = true; return nil }},
		{"the thaw fails", "the thaw of the root", func(r *cloneRig) *install.Hooks { r.fz.failThaw = true; return nil }},
		{"the freeze hits the guard", "the freeze reached its bound", func(r *cloneRig) *install.Hooks {
			r.gate.FreezeBound = 300 * time.Millisecond
			return &install.Hooks{CutAfter: -1, Observe: func(ev install.Event) error {
				if ev.Partition == "system_a" {
					time.Sleep(20 * time.Millisecond) // 48 writes of 64 KiB: about 1 s
				}
				return nil
			}}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newCloneRig(t, tool, "old")
			h := c.mod(r)
			p, res, err := r.run(h)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want a stop with %q, got %v\n%s", c.want, err, r.out.String())
			}
			r.stoppedBeforeArm(p, c.name)
			if len(r.data.ops) != 0 {
				t.Fatalf("data steps ran: %v", r.data.ops)
			}
			if r.fz.freezes != r.fz.thaws && !r.fz.failThaw {
				t.Fatalf("%d freezes, %d thaws", r.fz.freezes, r.fz.thaws)
			}
			// (after a thaw that fails three times the root may stay frozen: the
			// run's later output would wait there; that case is the risk of
			// the design, a power cycle boots B)
			if (r.fz.saidFrozen != 0 || r.fz.loggedWhen != 0) && !r.fz.failThaw {
				t.Fatalf("the run wrote while frozen: %+v", r.fz)
			}
			if r.fz.failThaw && r.fz.thaws != 3 {
				t.Fatalf("the thaw was tried %d times, want 3", r.fz.thaws)
			}
			if c.name == "the freeze hits the guard" {
				if res == nil || !res.Guard || res.Frozen < 300*time.Millisecond || res.Frozen > 900*time.Millisecond {
					t.Fatalf("the guard: %+v", res)
				}
				t.Logf("the guard thawed after %s (bound 300 ms)", res.Frozen)
			}
		})
	}
}

// TestClonePowerCutSweep: a cut before every primitive write of a clone run
// (clean and torn): B is the only bootable slot until the arm, the cardinal
// rule holds, and a new plan from the cut state runs to the end.
func TestClonePowerCutSweep(t *testing.T) {
	tool := realTool(t)
	r0 := newCloneRig(t, tool, "old")
	n := 0
	if _, _, err := r0.run(&install.Hooks{CutAfter: -1, Observe: func(install.Event) error { n++; return nil }}); err != nil {
		t.Fatal(err)
	}
	var points atomic.Int64
	for k := 0; k < n; k++ {
		for _, torn := range []bool{false, true} {
			if testing.Short() && k%5 != 0 && k < n-3 {
				continue
			}
			k, torn := k, torn
			t.Run(fmt.Sprintf("cut %d torn %v", k, torn), func(t *testing.T) {
				t.Parallel()
				points.Add(1)
				r := newCloneRig(t, tool, "old")
				p, _, err := r.run(&install.Hooks{CutAfter: k, Torn: torn})
				if !errors.Is(err, install.ErrPowerCut) {
					t.Fatalf("not cut: %v", err)
				}
				img, _ := os.ReadFile(r.path)
				cs, err := install.CheckCutState(bytes.NewReader(img), &p.Plan, r.start)
				if err != nil {
					t.Fatalf("the cardinal rule: %v", err)
				}
				if cs.Boots != "keeper" || cs.Target == "armed" {
					t.Fatalf("cut %d: boots %s, target %s", k, cs.Boots, cs.Target)
				}
				// the power comes back: B boots; a new plan resumes
				r.fz, r.data, r.out = &freezer{}, &dataHook{}, bytes.Buffer{}
				if _, _, err := r.run(nil); err != nil {
					t.Fatalf("resume: %v", err)
				}
				if a := r.ab(); a.Slots[0] != (avbab.Slot{Priority: 15, Tries: 7}) || !a.Slots[1].Keeper() {
					t.Fatalf("after the resume A %v B %v", a.Slots[0], a.Slots[1])
				}
			})
		}
	}
	t.Cleanup(func() { t.Logf("clone power-cut sweep: %d primitive writes, %d cut points", n, points.Load()) })
}

// TestCloneExecutorGuards: a plan that is not the gate's, or edited and
// sealed again, is refused before anything is written.
func TestCloneExecutorGuards(t *testing.T) {
	tool := realTool(t)
	cases := []struct {
		name, want string
		mod        func(p *plan.OtherSlotPlan, o *install.CloneOptions)
	}{
		{"another delta", "difference", func(p *plan.OtherSlotPlan, o *install.CloneOptions) { p.Clone.DeltaSHA256 = cloneRefA }},
		{"another ref_a", "profile", func(p *plan.OtherSlotPlan, o *install.CloneOptions) { p.Clone.RefA = cloneRefB }},
		// mainline-rc-10b: a clone plan carries no keeper content
		{"a keeper content", "keeper content", func(p *plan.OtherSlotPlan, o *install.CloneOptions) { p.Clone.KeeperContent = cloneRefB }},
		{"keeper entries", "keeper content", func(p *plan.OtherSlotPlan, o *install.CloneOptions) { p.Clone.KeeperEntries = 7 }},
		{"system from another partition", "is not system_b", func(p *plan.OtherSlotPlan, o *install.CloneOptions) {
			for i := range p.Sources {
				if p.Sources[i].Partition == "system_a" {
					p.Sources[i].From = plan.CloneSourceFrozen
					p.Sources[i].KeeperPartition = "system_a"
				}
			}
		}},
		{"a longer freeze bound", "freeze bound", func(p *plan.OtherSlotPlan, o *install.CloneOptions) { p.Clone.FreezeBoundMS = 181000 }},
		{"no freeze hook", "freeze", func(p *plan.OtherSlotPlan, o *install.CloneOptions) { o.Freeze = nil }},
		{"the release gate for a test plan", "clone gate", func(p *plan.OtherSlotPlan, o *install.CloneOptions) { o.Gate = clone.EmbeddedGate() }},
		{"an other-slot kind", "not a clone plan", func(p *plan.OtherSlotPlan, o *install.CloneOptions) { p.Kind = plan.OtherSlotKind }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newCloneRig(t, tool, "old")
			im, err := install.OpenImage(r.path)
			if err != nil {
				t.Fatal(err)
			}
			defer im.Close()
			p, err := r.makePlan(im)
			if err != nil {
				t.Fatal(err)
			}
			o := r.opts(nil)
			c.mod(p, &o)
			p.Seal()
			_, err = install.ExecuteClone(p, im, o)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want a refusal with %q, got %v", c.want, err)
			}
			if img, _ := os.ReadFile(r.path); !bytes.Equal(img, r.before) {
				t.Fatal("the image changed")
			}
		})
	}
}

// TestCloneKeeperAOnly (clone review MINOR-1): a clone runs from slot B to
// slot A only; a system that runs from slot A gets no clone plan.
func TestCloneKeeperAOnly(t *testing.T) {
	tool := realTool(t)
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	_, res := mlBundle(t, fx)
	u := osUnitOf(fx, res, [2][3]byte{{15, 0, 1}, {14, 0, 1}}, "a", "old")
	r := &cloneRig{rig: newRig(t, fx, tool, u), res: res, gate: cloneGate(fx, res), done: doneJSON("kiosk-ha", cloneRefB, cloneBundle),
		fz: &freezer{}, data: &dataHook{}, contentSum: cloneRefA}
	im, err := install.OpenImage(r.path)
	if err != nil {
		t.Fatal(err)
	}
	defer im.Close()
	sec := make([]byte, 512)
	im.Reader().ReadAt(sec, plan.ABLBA*512)
	st, _, _ := r.tool.Status(sec)
	info, _ := r.tool.Info()
	in := plan.CloneInput{Input: plan.Input{Disk: im.Reader(), DiskBytes: im.Size(), Source: "image:" + r.path, Mode: "dry-run",
		Tool: "test", Bootctl: info, BootctlStatus: st, HashKeeper: true},
		Running: plan.ParseRunning(osUBoot("a"), "console=ttyS2 uc-pxx.id="+mlToken, "system_a"),
		Gate:    r.gate, Done: r.done, Confirm: plan.ConfirmGate{Confirmed: true}, Data: true,
		KeyPins: testKeyPins(), KeyPinSet: plan.KeyPinSetTest}
	if p, err := plan.ComputeClone(in); err == nil || !strings.Contains(err.Error(), "clone-keeper-b") && !strings.Contains(err.Error(), "runs from slot B") {
		t.Fatalf("a clone from slot A: %v (%v)", err, p != nil)
	}
}
