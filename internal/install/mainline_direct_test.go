package install_test

// The direct path (task 3) with the real uc-pxx-bootctl-inst r2 under qemu:
// a factory-stock unit after the BCB-route entry (both slots trying, A 15)
// gets keep-key -> R(K) -> disable b (no-op) -> the six writes of B from the
// stash and the payload -> sync -> verify -> arm b. The power-cut sweep cuts
// before every primitive write (clean and torn): at every cut the keeper A
// is bootable and boots, B is never selectable while partly written, only
// the arm makes B the next boot, and a new plan resumes to the end state.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/keykeep"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/mainline/mltest"
	"uc-pxx-installer/internal/plan"
)

var dUnit = fakeemmc.Unit{AB: [2][3]byte{{15, 6, 0}, {14, 7, 0}}, A: "new", B: "old"}

type mapStash map[string][]byte

func (m mapStash) Part(p string) []byte { return m[p] }

func dStashOf(fx *fakeemmc.Fixture, res []byte) (mapStash, *plan.StashInput) {
	ms := mapStash{"uboot": fx.New["uboot"], "trust": fx.New["trust"], "dtbo": fx.New["dtbo"], "vbmeta": fx.New["vbmeta"], "resource": res}
	m := fx.Manifests[0]
	return ms, &plan.StashInput{IndexSHA256: strings.Repeat("5a", 32), FirmwareName: m.Name(), FirmwareSHA256: m.Firmware.SHA256,
		BuildType: "userdebug", UBootBuild: "userdebug", Parts: ms}
}

type dRig struct {
	*rig
	ml     *mainline.Payload
	bundle *mltest.Bundle
	res    []byte
	stash  mapStash
	noRead bool // plan with no in-RAM lock_state read (TestDirectNoReadRefused)
	locked bool // the in-RAM read is 0x00: the plan holds the lock-state write intent (TestDirectLockWriteOrder)
}

// dReadOne is an in-RAM lock_state read of 0x01 with nothing changed: the
// direct planner takes the unlock only from the reader (MAINLINE-RC2-LS;
// review B-8: the confirmation alone opens nothing since e5daffb).
func dReadOne() *plan.LockStateRead {
	z := plan.ReadSums{Security: "5ec", AB32: "ab", BCB: "bcb", Boot: "b00", GPTBackup: "69b", Misc: "m15c", ReadOnly: true}
	return &plan.LockStateRead{BootID: "test", Token: "pxx-inst-000000", Module: "m", Supplicant: "s", Client: "c", LockState: "0x01",
		Before: z, After: z, Stub: "install test"}
}

func (r *dRig) makePlan(im *install.Image) (*plan.DirectPlan, error) {
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
	_, si := dStashOf(r.fx, r.res)
	in := plan.MainlineDirectInput{Input: plan.Input{Disk: im.Reader(), DiskBytes: im.Size(), Source: "image:" + r.path, Mode: "dry-run",
		Tool: "test", Bootctl: info, BootctlStatus: st, PrereqsConfirmed: true, HashKeeper: true},
		Mainline: r.ml, Manifests: r.fx.Manifests, Gate: r.bundle.Gate(), Stash: si, Unlock: plan.DirectUnlockInput{Confirmed: true, Reader: dReadOne()}}
	if r.noRead {
		in.Unlock.Reader = nil
	}
	if r.locked {
		in.Unlock.Reader.LockState = plan.LockStateLockedValue
	}
	in.MainlineKey = plan.ReadMainlineKey(im.Reader(), im.Size(), mlRecord, keykeep.Gate{Open: true, Why: "test", SHA256: strings.Repeat("ab", 32)}, "test")
	return plan.ComputeMainlineDirect(in)
}

func (r *dRig) opts(o install.Options) install.DirectOptions {
	o.KeyRecord = mlRecord
	if o.Bootctl.Path == "" {
		o.Bootctl = r.tool
	}
	return install.DirectOptions{Options: o, Stash: r.stash, Gate: r.bundle.Gate()}
}

func newDRig(t *testing.T, u fakeemmc.Unit) *dRig {
	tool := realTool(t)
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	b, res := mlBundle(t, fx)
	ms, _ := dStashOf(fx, res)
	return &dRig{rig: newRig(t, fx, tool, u), ml: mlPayloadOf(t, b), bundle: b, res: res, stash: ms}
}

// dDone: B holds the stash's chain, the patched vbmeta, the assembled boot
// image and the rootfs; A is as before; the record is in both sectors; the
// final A/B state.
func (r *dRig) dDone(p *plan.DirectPlan) {
	t := r.t
	t.Helper()
	after, err := os.ReadFile(r.path)
	if err != nil {
		t.Fatal(err)
	}
	if !sameOutside(after, r.before, &p.Plan) {
		t.Fatal("a byte outside B's write ranges, the key-record sectors and the A/B sector changed")
	}
	a, _ := avbab.Parse(after[plan.ABLBA*512:])
	if a.Validate() != nil || a.Pick() != 1 || a.Slots[1] != (avbab.Slot{Priority: 15, Tries: 7}) || a.Slots[0] != (avbab.Slot{Priority: 14, Successful: 1}) {
		t.Fatalf("final A/B %v %v", a.Slots[0], a.Slots[1])
	}
	tb, _ := gpt.Parse(bytes.NewReader(after), int64(len(after)))
	holds := func(name string, img []byte) {
		e, _ := tb.Find(name)
		if !bytes.Equal(after[e.Start*512:e.Start*512+int64(len(img))], img) {
			t.Fatalf("%s does not hold its image", name)
		}
	}
	for _, n := range []string{"uboot", "trust", "dtbo"} {
		holds(n+"_b", r.fx.New[n])
	}
	holds("vbmeta_b", mlVbmetaPatched(r.fx))
	holds("boot_b", mltest.Expected(mlToken, mlLinux, 11, r.res))
	holds("system_b", r.bundle.Rootfs)
	for _, part := range plan.SlotParts {
		e, _ := tb.Find(part + "_a")
		if !bytes.Equal(after[e.Start*512:(e.End+1)*512], r.before[e.Start*512:(e.End+1)*512]) {
			t.Fatalf("the keeper's %s changed", e.Name)
		}
	}
	e, _ := tb.Find("backup")
	l, _ := keykeep.LBAs(e.Start, e.Sectors())
	for _, x := range l {
		if !bytes.Equal(after[x*512:(x+1)*512], mlRecord) {
			t.Fatalf("backup LBA %d does not hold the record", x)
		}
	}
}

// TestDirectNoReadRefused: with no in-RAM lock_state read, the confirmation
// alone opens nothing: the plan refuses the unlock (review B-8).
func TestDirectNoReadRefused(t *testing.T) {
	r := newDRig(t, dUnit)
	r.noRead = true
	im := r.open()
	defer im.Close()
	p, err := r.makePlan(im)
	if err == nil || !strings.Contains(err.Error(), "unlock") || p.Unlock.Confirmed || len(p.Steps) != 0 {
		t.Fatalf("no read: %v", err)
	}
}

func TestDirectDryRunInstall(t *testing.T) {
	r := newDRig(t, dUnit)
	im := r.open()
	defer im.Close()
	p, err := r.makePlan(im)
	if err != nil {
		var sb strings.Builder
		p.Render(&sb)
		t.Fatalf("%v\n%s", err, sb.String())
	}
	j := &journal{}
	res, err := install.ExecuteMainlineDirect(p, r.ml, im, r.opts(install.Options{Hooks: &install.Hooks{CutAfter: -1, Observe: j.observe}}))
	if err != nil {
		t.Fatal(err)
	}
	im.Close()
	r.dDone(p)
	// the order: the two key writes, then R(K), the data of B only, the arm last
	var ops []string
	for _, e := range j.ev {
		if e.Op == "data" && !strings.HasSuffix(e.Partition, "_b") {
			t.Fatalf("data write to %s", e.Partition)
		}
		if len(ops) == 0 || ops[len(ops)-1] != e.Op {
			ops = append(ops, e.Op)
		}
	}
	if strings.Join(ops, " ") != "key ab data ab" || j.ev[0].Op != "key" || j.ev[1].Op != "key" || res.Writes != len(j.ev) {
		t.Fatalf("write order %v (%d writes)", ops, len(j.ev))
	}
}

func TestDirectPowerCutSweep(t *testing.T) {
	r0 := newDRig(t, dUnit)
	im := r0.open()
	p0, err := r0.makePlan(im)
	if err != nil {
		t.Fatal(err)
	}
	j := &journal{}
	if _, err := install.ExecuteMainlineDirect(p0, r0.ml, im, r0.opts(install.Options{MaxWrite: sweepWrite,
		Hooks: &install.Hooks{CutAfter: -1, Observe: j.observe}})); err != nil {
		t.Fatal(err)
	}
	im.Close()
	n := len(j.ev)
	t.Parallel()
	var total atomic.Int64
	t.Cleanup(func() { t.Logf("direct power-cut sweep: %d cut points checked, each one resumed", total.Load()) })
	for _, torn := range []bool{false, true} {
		for _, c := range sweepPoints(n, j.ev) {
			point(t, fmt.Sprintf("cut %d torn %v", c, torn), func(t *testing.T) {
				r := &dRig{rig: newRig(t, r0.fx, r0.tool, dUnit), ml: r0.ml, bundle: r0.bundle, res: r0.res, stash: r0.stash}
				im := r.open()
				p, err := r.makePlan(im)
				if err != nil {
					t.Fatal(err)
				}
				_, err = install.ExecuteMainlineDirect(p, r.ml, im, r.opts(install.Options{MaxWrite: sweepWrite,
					Hooks: &install.Hooks{CutAfter: c, Torn: torn}}))
				if c < n && !errors.Is(err, install.ErrPowerCut) {
					t.Fatalf("cut %d: err %v, want a power cut", c, err)
				}
				if c == n && err != nil {
					t.Fatalf("no cut: %v", err)
				}
				im.Close()
				cut, _ := os.ReadFile(r.path)
				boots := reestablishCutOK(t, cut, &p.Plan)
				if !sameOutside(cut, r.before, &p.Plan) {
					t.Fatalf("cut %d: a byte outside the plan's ranges changed", c)
				}
				if c < n && boots != "keeper" {
					t.Fatalf("cut %d: boots %s; only the arm may make B boot next", c, boots)
				}
				total.Add(1)
				// resume: a new plan from the cut image finishes (or is refused when B boots next)
				im2 := r.open()
				defer im2.Close()
				p2, err := r.makePlan(im2)
				if boots == "target" {
					if err == nil {
						t.Fatal("B boots next, but a new plan was accepted")
					}
					return
				}
				if err != nil {
					var sb strings.Builder
					p2.Render(&sb)
					t.Fatalf("resume refused: %v\n%s", err, sb.String())
				}
				if _, err := install.ExecuteMainlineDirect(p2, r.ml, im2, r.opts(install.Options{})); err != nil {
					t.Fatalf("resume: %v", err)
				}
				im2.Close()
				r.dDone(p)
			})
		}
	}
}

func TestDirectExecuteGuards(t *testing.T) {
	r0 := newDRig(t, dUnit)
	refuse := func(t *testing.T, edit func(p *plan.DirectPlan), opt func(o *install.DirectOptions), want string) {
		t.Helper()
		r := &dRig{rig: newRig(t, r0.fx, r0.tool, dUnit), ml: r0.ml, bundle: r0.bundle, res: r0.res, stash: r0.stash}
		im := r.open()
		defer im.Close()
		p, err := r.makePlan(im)
		if err != nil {
			t.Fatal(err)
		}
		if edit != nil {
			edit(p)
			p.Seal()
		}
		o := r.opts(install.Options{})
		if opt != nil {
			opt(&o)
		}
		j := &journal{}
		o.Hooks = &install.Hooks{CutAfter: -1, Observe: j.observe}
		_, err = install.ExecuteMainlineDirect(p, r.ml, im, o)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err %v, want %q", err, want)
		}
		if after, _ := os.ReadFile(r.path); len(j.ev) != 0 || !bytes.Equal(after, r.before) {
			t.Fatalf("%d writes; the image changed", len(j.ev))
		}
	}
	src := func(p *plan.DirectPlan, part string) *plan.WriteSource { return p.Source(part + "_b") }
	cases := []struct {
		name string
		edit func(p *plan.DirectPlan)
		opt  func(o *install.DirectOptions)
		want string
	}{
		{"no stash", nil, func(o *install.DirectOptions) { o.Stash = nil }, "no stash"},
		{"a stashed uboot changed", nil, func(o *install.DirectOptions) {
			m := mapStash{}
			for k, v := range r0.stash {
				m[k] = v
			}
			m["uboot"] = r0.fx.Old["uboot"]
			o.Stash = m
		}, "the stash's uboot is not the plan's"},
		{"no gate", nil, func(o *install.DirectOptions) { o.Gate = nil }, "no payload gate"},
		{"no key record", nil, func(o *install.DirectOptions) { o.KeyRecord = nil }, "do not fit"},
		{"uboot_b from the keeper", func(p *plan.DirectPlan) {
			s := src(p, "uboot")
			s.From, s.KeeperPartition = "keeper", "uboot_a"
			p.Steps[3].Write.SegID = "keeper"
		},
			nil, "takes it only from stash"},
		{"uboot_b of the old firmware", func(p *plan.DirectPlan) {
			h := mltest.SHA(r0.fx.Old["uboot"])
			src(p, "uboot").SHA256, p.Steps[3].Write.SHA256 = h, h
		}, nil, "not the payload's"},
		{"another boot image", func(p *plan.DirectPlan) {
			h := strings.Repeat("0", 64)
			src(p, "boot").SHA256, p.Steps[7].Write.SHA256 = h, h
		}, nil, "not the payload's"},
		{"the keeper is b", func(p *plan.DirectPlan) { p.Keeper, p.Target = "b", "a" }, nil, "keeper a and target b"},
		{"R(K) of keeper b", func(p *plan.DirectPlan) { p.Steps[1].Bootctl.Args[2] = "b" }, nil, "repair --keeper a"},
		{"R(K) removed", func(p *plan.DirectPlan) {
			p.Steps = append(p.Steps[:1], p.Steps[2:]...)
			for i := range p.Steps {
				p.Steps[i].N = i + 1
			}
		}, nil, "keeper A is not the successful slot"},
		{"no unlock", func(p *plan.DirectPlan) { p.Unlock.Confirmed = false }, nil, "no unlock basis"},
		// a plan that needs the lock-state write (the reader read 0x00) runs only after the device
		// mode wrote 0x01 and read it back (LockWritten): the image mode has no TEE (mainline RC-6)
		{"a lock-state write intent, not written", func(p *plan.DirectPlan) {
			p.UnlockDirect.LockState = plan.LockStateLockedValue
			p.UnlockDirect.Write = &plan.LockStateWriteIntent{From: plan.LockStateLockedValue, To: plan.LockStateUnlockedValue,
				Security: plan.SecurityPartNode}
		}, nil, "needs the lock-state write first"},
		{"a write of vendor_b", func(p *plan.DirectPlan) {
			src(p, "system").Partition = "vendor_b"
			p.Steps[8].Write.Partition = "vendor_b"
		}, nil, "shape"},
		{"a mainline (userdebug-first) kind", func(p *plan.DirectPlan) { p.Kind = plan.MainlineKind }, nil, "not a direct"},
		{"the plan's gate exempts the patch check", func(p *plan.DirectPlan) { p.Gate.Gate = "embedded" }, nil, "not a test gate"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { refuse(t, c.edit, c.opt, c.want) })
	}
	t.Run("an edited plan (no reseal)", func(t *testing.T) {
		r := &dRig{rig: newRig(t, r0.fx, r0.tool, dUnit), ml: r0.ml, bundle: r0.bundle, res: r0.res, stash: r0.stash}
		im := r.open()
		defer im.Close()
		p, _ := r.makePlan(im)
		p.Steps[3].Write.SHA256 = strings.Repeat("0", 64)
		if _, err := install.ExecuteMainlineDirect(p, r.ml, im, r.opts(install.Options{})); err == nil || !strings.Contains(err.Error(), "no valid id") {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("the mainline executor refuses a direct plan", func(t *testing.T) {
		r := &dRig{rig: newRig(t, r0.fx, r0.tool, dUnit), ml: r0.ml, bundle: r0.bundle, res: r0.res, stash: r0.stash}
		im := r.open()
		defer im.Close()
		p, _ := r.makePlan(im)
		if _, err := install.ExecuteMainline(&p.MainlinePlan, r.ml, im, install.Options{KeyRecord: mlRecord, Bootctl: r.tool}); err == nil {
			t.Fatal("the userdebug-first executor ran a direct plan")
		}
		if _, err := install.Execute(&p.Plan, nil, im, install.Options{Bootctl: r.tool}); err == nil {
			t.Fatal("the official executor ran a direct plan")
		}
	})
}

// abAt is the decoded A/B slot table of the image at its path.
func abAt(t *testing.T, path string) avbab.AB {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	a, err := avbab.Parse(b[plan.ABLBA*512:])
	if err != nil || a.Validate() != nil {
		t.Fatalf("A/B not valid: %v", err)
	}
	return a
}

// TestDirectLockWriteOrder (MAINLINE-RC6 R-D item: the order): a direct plan
// of a locked unit runs keep-key, then R(K) (read back), then the lock-state
// writer once, with the live A/B state A{15,0,1} B{0,0,0} (the keeper A
// successful and booting, B not bootable), then the disable and the writes
// of B. A writer that fails (restored) or is torn (exit 3) stops the run there:
// the keeper A successful, B not bootable and not written. A plan with the
// intent and no writer, or a writer with no intent, refuses before any write.
func TestDirectLockWriteOrder(t *testing.T) {
	keeperRK := func(t *testing.T, a avbab.AB) {
		t.Helper()
		if a.Slots[0] != (avbab.Slot{Priority: 15, Successful: 1}) || a.Slots[1] != (avbab.Slot{}) || a.Pick() != 0 {
			t.Fatalf("A/B %v %v: not the R(K) state", a.Slots[0], a.Slots[1])
		}
	}
	t.Run("the order", func(t *testing.T) {
		r := newDRig(t, dUnit)
		r.locked = true
		im := r.open()
		defer im.Close()
		p, err := r.makePlan(im)
		if err != nil || p.UnlockDirect.Write == nil {
			t.Fatalf("plan: %v, write %v", err, p.UnlockDirect.Write)
		}
		j := &journal{}
		calls, at := 0, 0
		o := r.opts(install.Options{Hooks: &install.Hooks{CutAfter: -1, Observe: j.observe}})
		o.LockWrite = func(say func(string)) error {
			calls++
			at = len(j.ev)
			keeperRK(t, abAt(t, r.path))
			return nil
		}
		if _, err := install.ExecuteMainlineDirect(p, r.ml, im, o); err != nil {
			t.Fatal(err)
		}
		im.Close()
		r.dDone(p)
		var before []string
		for _, e := range j.ev[:at] {
			before = append(before, e.Op)
		}
		if calls != 1 || strings.Join(before, " ") != "key key ab" {
			t.Fatalf("the writer ran %d times, after the writes %v (want key key ab: keep-key, R(K))", calls, before)
		}
		for _, e := range j.ev[at:] {
			if e.Op == "key" {
				t.Fatal("a key write after the lock-state write")
			}
		}
	})
	for _, c := range []struct {
		name    string
		err     error
		unknown bool
	}{
		{"a failed write (restored)", errors.New("the stub's write failed; restored and verified"), false},
		{"a torn write (not restored)", fmt.Errorf("%w: torn security partition", install.ErrABUnknown), true},
		{"a power cut during the write", install.ErrPowerCut, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newDRig(t, dUnit)
			r.locked = true
			im := r.open()
			p, err := r.makePlan(im)
			if err != nil {
				t.Fatal(err)
			}
			o := r.opts(install.Options{})
			o.LockWrite = func(func(string)) error { return c.err }
			_, err = install.ExecuteMainlineDirect(p, r.ml, im, o)
			im.Close()
			if err == nil || !strings.Contains(err.Error(), "the lock-state write (before step") || errors.Is(err, install.ErrABUnknown) != c.unknown {
				t.Fatalf("err %v", err)
			}
			keeperRK(t, abAt(t, r.path))
			after, _ := os.ReadFile(r.path)
			tb, _ := gpt.Parse(bytes.NewReader(after), int64(len(after)))
			for _, part := range plan.SlotParts {
				for _, sl := range []string{"_a", "_b"} {
					e, _ := tb.Find(part + sl)
					if !bytes.Equal(after[e.Start*512:(e.End+1)*512], r.before[e.Start*512:(e.End+1)*512]) {
						t.Fatalf("%s changed before the lock-state write ended", e.Name)
					}
				}
			}
		})
	}
	t.Run("no writer, or a writer with no intent: refused before any write", func(t *testing.T) {
		for _, locked := range []bool{true, false} {
			r := newDRig(t, dUnit)
			r.locked = locked
			im := r.open()
			p, err := r.makePlan(im)
			if err != nil {
				t.Fatal(err)
			}
			o := r.opts(install.Options{})
			if !locked {
				o.LockWrite = func(func(string)) error { t.Fatal("the writer ran"); return nil }
			}
			if _, err := install.ExecuteMainlineDirect(p, r.ml, im, o); err == nil || !strings.Contains(err.Error(), "precheck") {
				t.Fatalf("locked %v: %v", locked, err)
			}
			im.Close()
			after, _ := os.ReadFile(r.path)
			if !bytes.Equal(after, r.before) {
				t.Fatalf("locked %v: the image changed", locked)
			}
		}
	})
}

// TestDirectLockWritePowerCutSweep: the power-cut sweep of a direct plan of a
// locked unit (the lock-state writer stubbed; the security partition is not in
// the image): every cut point, also torn, leaves a valid A/B sector whose
// keeper A boots until the arm; the writer never runs before R(K) is read
// back; each cut resumes to the end.
func TestDirectLockWritePowerCutSweep(t *testing.T) {
	r0 := newDRig(t, dUnit)
	r0.locked = true
	im := r0.open()
	p0, err := r0.makePlan(im)
	if err != nil {
		t.Fatal(err)
	}
	j := &journal{}
	o0 := r0.opts(install.Options{MaxWrite: sweepWrite, Hooks: &install.Hooks{CutAfter: -1, Observe: j.observe}})
	o0.LockWrite = func(func(string)) error { return nil }
	if _, err := install.ExecuteMainlineDirect(p0, r0.ml, im, o0); err != nil {
		t.Fatal(err)
	}
	im.Close()
	n := len(j.ev)
	t.Parallel()
	var total atomic.Int64
	t.Cleanup(func() {
		t.Logf("direct lock-write power-cut sweep: %d cut points checked, each one resumed", total.Load())
	})
	for _, torn := range []bool{false, true} {
		for _, c := range sweepPoints(n, j.ev) {
			point(t, fmt.Sprintf("cut %d torn %v", c, torn), func(t *testing.T) {
				r := &dRig{rig: newRig(t, r0.fx, r0.tool, dUnit), ml: r0.ml, bundle: r0.bundle, res: r0.res, stash: r0.stash, locked: true}
				im := r.open()
				p, err := r.makePlan(im)
				if err != nil {
					t.Fatal(err)
				}
				o := r.opts(install.Options{MaxWrite: sweepWrite, Hooks: &install.Hooks{CutAfter: c, Torn: torn}})
				o.LockWrite = func(func(string)) error {
					a := abAt(t, r.path)
					if a.Slots[0] != (avbab.Slot{Priority: 15, Successful: 1}) || a.Slots[1] != (avbab.Slot{}) {
						t.Fatalf("cut %d: the writer ran at A/B %v %v", c, a.Slots[0], a.Slots[1])
					}
					return nil
				}
				_, err = install.ExecuteMainlineDirect(p, r.ml, im, o)
				if c < n && !errors.Is(err, install.ErrPowerCut) {
					t.Fatalf("cut %d: err %v, want a power cut", c, err)
				}
				im.Close()
				cut, _ := os.ReadFile(r.path)
				boots := reestablishCutOK(t, cut, &p.Plan)
				if !sameOutside(cut, r.before, &p.Plan) {
					t.Fatalf("cut %d: a byte outside the plan's ranges changed", c)
				}
				if c < n && boots != "keeper" {
					t.Fatalf("cut %d: boots %s; only the arm may make B boot next", c, boots)
				}
				total.Add(1)
				im2 := r.open()
				defer im2.Close()
				p2, err := r.makePlan(im2)
				if boots == "target" {
					if err == nil {
						t.Fatal("B boots next, but a new plan was accepted")
					}
					return
				}
				if err != nil {
					t.Fatalf("resume refused: %v", err)
				}
				o2 := r.opts(install.Options{})
				o2.LockWrite = func(func(string)) error { return nil }
				if _, err := install.ExecuteMainlineDirect(p2, r.ml, im2, o2); err != nil {
					t.Fatalf("resume: %v", err)
				}
				im2.Close()
				r.dDone(p)
			})
		}
	}
}
