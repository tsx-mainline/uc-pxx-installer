package install_test

// The other-slot install of mainline step 2 (plan.ComputeOtherSlot,
// install.RunOtherSlot; MAINLINE-STEP2-DESIGN.md §3) on the dry medium with
// the real uc-pxx-bootctl (UC_PXX_BOOTCTL; skipped without it): a full run,
// the power-cut sweep (a cut before every primitive write, clean and torn,
// each point checked with CheckCutState and resumed), the data hook, and the
// executor's guards.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/bootctl"
	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/keykeep"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/mainline/mltest"
	"uc-pxx-installer/internal/plan"
)

func osUBoot(slot string) []byte {
	return []byte("storagemedia=emmc androidboot.vbmeta.device_state=unlocked androidboot.verifiedbootstate=orange " +
		"androidboot.slot_suffix=_" + slot + " console=ttyFIQ0\x00")
}

// osUnitOf: slot k runs mainline (the reviewed chain, boot_k = the assembled
// image), the other slot holds what, backup holds the kept key.
func osUnitOf(fx *fakeemmc.Fixture, res []byte, ab [2][3]byte, k, what string) fakeemmc.Unit {
	b := make([]byte, 4096*512)
	rec := keykeep.Encode(fx.Keys)
	copy(b[(4096-keykeep.FromEnd1)*512:], rec)
	copy(b[(4096-keykeep.FromEnd2)*512:], rec)
	u := fakeemmc.Unit{AB: ab, Tweak: map[string][]byte{"boot_" + k: mltest.Expected(mlToken, mlLinux, 11, res), "backup": b}}
	if k == "a" {
		u.A, u.B = "new", what
	} else {
		u.A, u.B = what, "new"
	}
	return u
}

type osRig struct {
	*rig
	ml     *mainline.Payload
	bundle *mltest.Bundle
	res    []byte
	slot   string // the running slot
	data   bool
}

func newOSRig(t *testing.T, fx *fakeemmc.Fixture, tool bootctl.Tool, u fakeemmc.Unit, slot string, b *mltest.Bundle, ml *mainline.Payload, res []byte) *osRig {
	return &osRig{rig: newRig(t, fx, tool, u), ml: ml, bundle: b, res: res, slot: slot, data: true}
}

func (r *osRig) makePlan(im *install.Image) (*plan.OtherSlotPlan, error) {
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
	in := plan.OtherSlotInput{Input: plan.Input{Disk: im.Reader(), DiskBytes: im.Size(), Source: "image:" + r.path, Mode: "dry-run",
		Tool: "test", Bootctl: info, BootctlStatus: st, HashKeeper: true},
		Running:  plan.ParseRunning(osUBoot(r.slot), "console=ttyS2 uc-pxx.id="+mlToken, "system_"+r.slot),
		Mainline: r.ml, Manifests: r.fx.Manifests, Gate: r.bundle.Gate(), Confirm: plan.ConfirmGate{Confirmed: true}, Data: r.data}
	return plan.ComputeOtherSlot(in)
}

// dataHook records the data steps; fail makes the step of that op fail.
type dataHook struct {
	ops  []string
	fail string
}

func (h *dataHook) run(op string, d *plan.DataPlan) error {
	h.ops = append(h.ops, op)
	if d == nil || d.Partition != "userdata" || d.Label != "tsxdata" {
		return fmt.Errorf("data part %+v", d)
	}
	if op == h.fail {
		return errors.New("the made-up data step failed")
	}
	return nil
}

// checkDone: nothing outside the target's write ranges and the A/B sector
// changed, the final A/B state, the target holds the keeper's chain, the
// assembled boot image and the root file system, and every keeper partition
// is as before.
func (r *osRig) checkDone(p *plan.OtherSlotPlan) {
	t := r.t
	after, err := os.ReadFile(r.path)
	if err != nil {
		t.Fatal(err)
	}
	if !sameOutside(after, r.before, &p.Plan) {
		t.Fatal("a byte outside the target's write ranges and the A/B sector changed")
	}
	a, _ := avbab.Parse(after[plan.ABLBA*512:])
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	k := 0
	if p.Keeper == "b" {
		k = 1
	}
	want, _ := p.AB.Final.Raw()
	if !bytes.Equal(after[plan.ABLBA*512:plan.ABLBA*512+32], want[:]) || a.Pick() != 1-k || !a.Slots[k].Keeper() {
		t.Fatalf("final A/B %x, want %x", after[plan.ABLBA*512:plan.ABLBA*512+32], want)
	}
	tb, _ := gpt.Parse(bytes.NewReader(after), int64(len(after)))
	holds := func(name string, img []byte) {
		e, _ := tb.Find(name)
		if !bytes.Equal(after[e.Start*512:e.Start*512+int64(len(img))], img) {
			t.Fatalf("%s does not hold its image", name)
		}
	}
	for _, n := range []string{"uboot", "trust", "dtbo", "vbmeta"} {
		holds(n+"_"+p.Target, r.fx.New[n])
	}
	holds("boot_"+p.Target, mltest.Expected(mlToken, mlLinux, 11, r.res))
	holds("system_"+p.Target, r.bundle.Rootfs)
	for _, part := range plan.SlotParts {
		e, _ := tb.Find(part + "_" + p.Keeper)
		if !bytes.Equal(after[e.Start*512:(e.End+1)*512], r.before[e.Start*512:(e.End+1)*512]) {
			t.Fatalf("keeper %s changed", e.Name)
		}
	}
	cs, err := install.CheckCutState(bytes.NewReader(after), &p.Plan, r.start)
	if err != nil || cs.Boots != "target" || !cs.Checked {
		t.Fatalf("final state %+v: %v", cs, err)
	}
}

func TestOtherSlotDryRunInstall(t *testing.T) {
	tool := realTool(t)
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	b, res := mlBundle(t, fx)
	ml := mlPayloadOf(t, b)
	for _, sh := range osShapes(fx, res) {
		t.Run(sh.name, func(t *testing.T) {
			r := newOSRig(t, fx, tool, sh.unit, sh.slot, b, ml, res)
			im := r.open()
			defer im.Close()
			p, err := r.makePlan(im)
			if err != nil {
				var sb strings.Builder
				p.Render(&sb)
				t.Fatalf("%v\n%s", err, sb.String())
			}
			j := &journal{}
			h := &dataHook{}
			var said []string
			res2, err := install.ExecuteOtherSlot(p, ml, im, install.OtherSlotOptions{Options: install.Options{Bootctl: tool,
				Say: func(s string) { said = append(said, s) }, Hooks: &install.Hooks{CutAfter: -1, Observe: j.observe}}, Data: h.run})
			if err != nil {
				t.Fatalf("%v\n%s", err, strings.Join(said, "\n"))
			}
			if res2.Steps != len(p.Steps) || res2.FinalAB != p.AB.Final {
				t.Fatalf("%d steps", res2.Steps)
			}
			if strings.Join(h.ops, " ") != "data-format data-migrate" {
				t.Fatalf("data steps %v", h.ops)
			}
			r.checkDone(p)
			// A/B writes first and last, data only in between, only in the target
			var abAt []int
			for i, e := range j.ev {
				if e.Op == "ab" {
					abAt = append(abAt, i)
				}
				if e.Op == "data" && !strings.HasSuffix(e.Partition, "_"+p.Target) {
					t.Fatalf("data write to %s", e.Partition)
				}
			}
			if len(abAt) < 1 || abAt[len(abAt)-1] != len(j.ev)-1 || (len(abAt) == 2 && abAt[0] != 0) {
				t.Fatalf("A/B writes at %v of %d writes", abAt, len(j.ev))
			}
			if !strings.Contains(strings.Join(said, "\n"), "keeper slot "+strings.ToUpper(p.Keeper)+": all 8 partitions unchanged") {
				t.Fatalf("no keeper check:\n%s", strings.Join(said, "\n"))
			}
		})
	}
}

type osShape struct {
	name string
	unit fakeemmc.Unit
	slot string
}

func osShapes(fx *fakeemmc.Fixture, res []byte) []osShape {
	return []osShape{
		{"step 2: A mainline 15, B Android 14 (equal chain: skips)", osUnitOf(fx, res, [2][3]byte{{15, 0, 1}, {14, 0, 1}}, "a", "new"), "a"},
		{"step 2: B old images (chain written)", osUnitOf(fx, res, [2][3]byte{{15, 0, 1}, {14, 0, 1}}, "a", "old"), "a"},
		{"resume: B disabled", osUnitOf(fx, res, [2][3]byte{{15, 0, 1}, {0, 0, 0}}, "a", "old"), "a"},
		{"keeper A 14, B 13", osUnitOf(fx, res, [2][3]byte{{14, 0, 1}, {13, 0, 1}}, "a", "old"), "a"},
		{"from B: B mainline 15, A old 14", osUnitOf(fx, res, [2][3]byte{{14, 0, 1}, {15, 0, 1}}, "b", "old"), "b"},
	}
}

// TestOtherSlotPowerCutSweep: for each start shape the install is cut before
// every primitive write (and once more torn), as a power loss would. At
// every point CheckCutState must pass (the keeper successful and booting;
// the target bootable only with all of the old or all of the new bytes),
// nothing outside the target's write ranges and the A/B sector may differ,
// the target must be unbootable from its first written byte until the arm,
// and only the last write (the arm) may make the target the next boot. Then
// the installer runs again on the cut image and must finish, or refuse when
// the target boots next.
func TestOtherSlotPowerCutSweep(t *testing.T) {
	tool := realTool(t)
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	b, res := mlBundle(t, fx)
	ml := mlPayloadOf(t, b)
	t.Parallel()
	var total atomic.Int64
	t.Cleanup(func() {
		t.Logf("other-slot power-cut sweep: %d cut points checked (A/B decoded with internal/avbab), each one resumed", total.Load())
	})
	for _, sh := range osShapes(fx, res) {
		point(t, sh.name, func(t *testing.T) {
			r := newOSRig(t, fx, tool, sh.unit, sh.slot, b, ml, res)
			im := r.open()
			p0, err := r.makePlan(im)
			if err != nil {
				t.Fatal(err)
			}
			path0 := r.path
			j := &journal{}
			h := &dataHook{}
			if _, err := install.ExecuteOtherSlot(p0, ml, im, install.OtherSlotOptions{Options: install.Options{Bootctl: tool,
				MaxWrite: sweepWrite, Hooks: &install.Hooks{CutAfter: -1, Observe: j.observe}}, Data: h.run}); err != nil {
				t.Fatal(err)
			}
			im.Close()
			n := len(j.ev)
			k := 0
			if sh.slot == "b" {
				k = 1
			}
			for _, torn := range []bool{false, true} {
				for _, c := range sweepPoints(n, j.ev) {
					point(t, fmt.Sprintf("cut %d torn %v", c, torn), func(t *testing.T) {
						sweepList(t, sh.name, fmt.Sprintf("cut %d torn %v", c, torn))
						r := newOSRig(t, fx, tool, sh.unit, sh.slot, b, ml, res)
						im := r.open()
						p, err := r.makePlan(im)
						if err != nil {
							t.Fatal(err)
						}
						if !osSamePlan(p0, p, path0, r.path) {
							t.Fatal("the plan of a fresh image differs")
						}
						_, err = install.ExecuteOtherSlot(p, ml, im, install.OtherSlotOptions{Options: install.Options{Bootctl: tool,
							MaxWrite: sweepWrite, Hooks: &install.Hooks{CutAfter: c, Torn: torn}}, Data: (&dataHook{}).run})
						if c < n && !errors.Is(err, install.ErrPowerCut) {
							t.Fatalf("cut %d: err %v, want a power cut", c, err)
						}
						if c == n && err != nil {
							t.Fatalf("cut %d (no cut): %v", c, err)
						}
						im.Close()
						cut, _ := os.ReadFile(r.path)
						cs, err := install.CheckCutState(bytes.NewReader(cut), &p.Plan, r.start)
						if err != nil {
							t.Fatalf("cut before write %d (torn %v): %v (A/B %s)", c+1, torn, err, cs.AB)
						}
						if !sameOutside(cut, r.before, &p.Plan) {
							t.Fatalf("cut %d: a byte outside the target's write ranges changed", c)
						}
						a, _ := avbab.Parse(cut[plan.ABLBA*512:])
						if !a.Slots[k].Keeper() || !a.Slots[k].Bootable() {
							t.Fatalf("cut %d: the keeper is not successful: %v", c, a.Slots[k])
						}
						dataDone := torn && c < n && j.ev[c].Op == "data"
						for _, e := range j.ev[:c] {
							dataDone = dataDone || e.Op == "data"
						}
						if c < n && dataDone && a.Slots[1-k] != (avbab.Slot{}) {
							t.Fatalf("cut %d (torn %v): the target has written bytes but is %v (%s)", c, torn, a.Slots[1-k], cs.Target)
						}
						if c == n && (cs.Boots != "target" || !cs.Checked) {
							t.Fatalf("complete run: %+v", cs)
						}
						if c < n && cs.Boots != "keeper" {
							t.Fatalf("cut %d: %+v: only the last write may make the target the next boot", c, cs)
						}
						r.resume(p, cs, c)
						total.Add(1)
					})
				}
			}
		})
	}
}

func osSamePlan(a, b *plan.OtherSlotPlan, pa, pb string) bool {
	if pa == pb {
		return false
	}
	ca, cb := *a, *b
	ca.ID, cb.ID = "", ""
	ja, err1 := json.Marshal(&ca)
	jb, err2 := json.Marshal(&cb)
	return err1 == nil && err2 == nil && bytes.Equal(ja, bytes.ReplaceAll(jb, []byte(pb), []byte(pa)))
}

// resume runs the installer again on a cut image.
func (r *osRig) resume(p *plan.OtherSlotPlan, cs install.CutState, c int) {
	t := r.t
	im := r.open()
	defer im.Close()
	p2, err := r.makePlan(im)
	if cs.Boots == "target" {
		if err == nil {
			t.Fatalf("cut %d: the target boots next, but a new plan was accepted", c)
		}
		return
	}
	if err != nil {
		var sb strings.Builder
		p2.Render(&sb)
		t.Fatalf("cut %d: resume refused: %v\n%s", c, err, sb.String())
	}
	if p2.Keeper != p.Keeper || p2.AB.Final != p.AB.Final {
		t.Fatalf("cut %d: resume plan keeper %s final %s", c, p2.Keeper, p2.AB.Final)
	}
	if _, err := install.ExecuteOtherSlot(p2, r.ml, im, install.OtherSlotOptions{Options: install.Options{Bootctl: r.tool},
		Data: (&dataHook{}).run}); err != nil {
		t.Fatalf("cut %d: resume: %v", c, err)
	}
	r.checkDone(p)
}

// A failed data step stops the run with the target disabled (the keeper
// boots); a new run finishes.
func TestOtherSlotDataStepFails(t *testing.T) {
	tool := realTool(t)
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	b, res := mlBundle(t, fx)
	ml := mlPayloadOf(t, b)
	for _, op := range []string{plan.OpDataFormat, plan.OpDataMigrate} {
		t.Run(op, func(t *testing.T) {
			r := newOSRig(t, fx, tool, osShapes(fx, res)[1].unit, "a", b, ml, res)
			im := r.open()
			p, err := r.makePlan(im)
			if err != nil {
				t.Fatal(err)
			}
			_, err = install.ExecuteOtherSlot(p, ml, im, install.OtherSlotOptions{Options: install.Options{Bootctl: tool},
				Data: (&dataHook{fail: op}).run})
			im.Close()
			if err == nil || !strings.Contains(err.Error(), "the made-up data step failed") {
				t.Fatalf("err %v", err)
			}
			cut, _ := os.ReadFile(r.path)
			a, _ := avbab.Parse(cut[plan.ABLBA*512:])
			cs, cerr := install.CheckCutState(bytes.NewReader(cut), &p.Plan, r.start)
			if cerr != nil || a.Slots[1] != (avbab.Slot{}) || a.Pick() != 0 || cs.Boots != "keeper" {
				t.Fatalf("after the failed %s: %v %v %+v", op, a.Slots, cerr, cs)
			}
			r.resume(p, cs, -1)
		})
	}
}

func TestOtherSlotExecuteGuards(t *testing.T) {
	tool := realTool(t)
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	b, res := mlBundle(t, fx)
	ml := mlPayloadOf(t, b)
	u := osShapes(fx, res)[1].unit
	fresh := func(t *testing.T) (*osRig, *install.Image, *plan.OtherSlotPlan) {
		r := newOSRig(t, fx, tool, u, "a", b, ml, res)
		im := r.open()
		t.Cleanup(func() { im.Close() })
		p, err := r.makePlan(im)
		if err != nil {
			t.Fatal(err)
		}
		return r, im, p
	}
	refuse := func(t *testing.T, r *osRig, im *install.Image, p *plan.OtherSlotPlan, opt install.OtherSlotOptions, want string) {
		t.Helper()
		if opt.Bootctl.Path == "" {
			opt.Bootctl = tool
		}
		_, err := install.ExecuteOtherSlot(p, ml, im, opt)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err %v, want %q", err, want)
		}
		after, _ := os.ReadFile(r.path)
		if !bytes.Equal(after, r.before) {
			t.Fatal("a refused run wrote something")
		}
	}
	hook := (&dataHook{}).run
	t.Run("no data hook", func(t *testing.T) {
		r, im, p := fresh(t)
		refuse(t, r, im, p, install.OtherSlotOptions{}, "no data hook")
	})
	t.Run("an edited plan (no reseal)", func(t *testing.T) {
		r, im, p := fresh(t)
		p.Steps[2].Write.SHA256 = strings.Repeat("0", 64)
		refuse(t, r, im, p, install.OtherSlotOptions{Data: hook}, "no valid id")
	})
	reseal := func(name, want string, edit func(p *plan.OtherSlotPlan)) {
		t.Run(name, func(t *testing.T) {
			r, im, p := fresh(t)
			edit(p)
			p.Seal()
			refuse(t, r, im, p, install.OtherSlotOptions{Data: hook}, want)
		})
	}
	reseal("the arm before the data-migrate", "shape", func(p *plan.OtherSlotPlan) {
		n := len(p.Steps)
		p.Steps[n-3], p.Steps[n-2] = p.Steps[n-2], p.Steps[n-3]
		p.Steps[n-3].N, p.Steps[n-2].N = n-2, n-1
	})
	reseal("no data-format", "shape", func(p *plan.OtherSlotPlan) {
		p.Steps = append(p.Steps[:1], p.Steps[2:]...)
		for i := range p.Steps {
			p.Steps[i].N = i + 1
		}
	})
	reseal("the data part removed (the steps stay)", "shape", func(p *plan.OtherSlotPlan) { p.Data = nil })
	reseal("the keeper is not the running slot", "keeper must be the running slot", func(p *plan.OtherSlotPlan) { p.Running.Slot = "b" })
	reseal("no confirmation", "no confirmation", func(p *plan.OtherSlotPlan) { p.Confirm.Confirmed = false })
	reseal("no kept key", "firmware key kept", func(p *plan.OtherSlotPlan) { p.Key.Copies = 1 })
	reseal("a failed check", "check", func(p *plan.OtherSlotPlan) { p.Checks[3].OK = false })
	reseal("a write of vendor_b", "shape", func(p *plan.OtherSlotPlan) {
		p.Sources[5].Partition = "vendor_b"
		p.Steps[7].Write.Partition = "vendor_b"
	})
	reseal("uboot_b from uboot_b", "only from keeper", func(p *plan.OtherSlotPlan) { p.Sources[0].KeeperPartition = "uboot_b" })
	reseal("another data label", "not userdata ext4 tsxdata", func(p *plan.OtherSlotPlan) { p.Data.Label = "android" })
	reseal("slot mode on the dry medium", "medium runs only", func(p *plan.OtherSlotPlan) { p.Mode = "slot" })
	t.Run("the A/B sector changed after the plan", func(t *testing.T) {
		r, im, p := fresh(t)
		im.Close()
		f, _ := os.OpenFile(r.path, os.O_RDWR, 0)
		f.WriteAt(fakeemmc.ABSector([2][3]byte{{15, 0, 1}, {13, 0, 1}}, nil), plan.ABLBA*512)
		f.Close()
		r.before, _ = os.ReadFile(r.path)
		im2 := r.open()
		defer im2.Close()
		refuse(t, r, im2, p, install.OtherSlotOptions{Data: hook}, "differs from the plan")
	})
	t.Run("the official executor refuses the inner plan", func(t *testing.T) {
		r, im, p := fresh(t)
		_, err := install.Execute(&p.Plan, nil, im, install.Options{Bootctl: tool})
		if err == nil || !strings.Contains(err.Error(), "no valid id") {
			t.Fatalf("the official executor ran the inner plan of an other-slot plan: %v", err)
		}
		after, _ := os.ReadFile(r.path)
		if !bytes.Equal(after, r.before) {
			t.Fatal("written")
		}
	})
}
