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
	u := fakeemmc.Unit{AB: ab, Tweak: map[string][]byte{"boot_" + k: mltest.Expected(mlToken, mlLinux, 11, res), "backup": b,
		"vbmeta_" + k: mlVbmetaPatched(fx)}}
	if k == "a" {
		u.A, u.B = "new", what
	} else {
		u.A, u.B = what, "new"
	}
	return u
}

// mlVbmetaPatched is the vbmeta of a mainline slot (payload contract v2,
// review F4): the fixture's signed vbmeta with mlBundle's boot image_size.
func mlVbmetaPatched(fx *fakeemmc.Fixture) []byte {
	var cap int64
	for _, d := range fx.Manifests[0].AVB.Descriptors {
		if d.Partition == "boot" {
			cap = int64(d.ImageSize)
		}
	}
	out, _, err := mainline.PatchVbmeta(fx.New["vbmeta"], uint64(cap+64<<10))
	if err != nil {
		panic(err) // the fixture's own signed vbmeta
	}
	return out
}

type osRig struct {
	*rig
	ml     *mainline.Payload
	bundle *mltest.Bundle
	res    []byte
	slot   string // the running slot
	data   bool
	gate   *mainline.Gate // the plan's gate (nil: the bundle's test gate)
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
	if r.gate != nil {
		in.Gate = r.gate
	}
	in.Running.NetworkUp = true
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
	for _, n := range []string{"uboot", "trust", "dtbo"} {
		holds(n+"_"+p.Target, r.fx.New[n])
	}
	holds("vbmeta_"+p.Target, mlVbmetaPatched(r.fx)) // a copy of the keeper's patched vbmeta (contract v2)
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
				Say: func(s string) { said = append(said, s) }, Hooks: &install.Hooks{CutAfter: -1, Observe: j.observe}}, Data: h.run, Gate: b.Gate()})
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
			if !strings.Contains(strings.Join(said, "\n"), "keeper slot "+strings.ToUpper(p.Keeper)+": uboot, trust, dtbo, vbmeta, boot unchanged") {
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
				MaxWrite: sweepWrite, Hooks: &install.Hooks{CutAfter: -1, Observe: j.observe}}, Data: h.run, Gate: b.Gate()}); err != nil {
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
							MaxWrite: sweepWrite, Hooks: &install.Hooks{CutAfter: c, Torn: torn}}, Data: (&dataHook{}).run, Gate: b.Gate()})
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
		Data: (&dataHook{}).run, Gate: r.bundle.Gate()}); err != nil {
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
				Data: (&dataHook{fail: op}).run, Gate: b.Gate()})
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
		if opt.Gate == nil && want != "no payload gate" {
			opt.Gate = b.Gate()
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
		p.Steps = append(p.Steps[:9], p.Steps[10:]...)
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
		p.Steps[6].Write.Partition = "vendor_b"
	})
	reseal("data-format before the writes", "shape", func(p *plan.OtherSlotPlan) {
		f := p.Steps[9]
		p.Steps = append([]plan.Step{p.Steps[0], f}, append(append([]plan.Step{}, p.Steps[1:9]...), p.Steps[10:]...)...)
		for i := range p.Steps {
			p.Steps[i].N = i + 1
		}
	})
	reseal("another format label", "not userdata ext4 tsxdata-new/tsxdata", func(p *plan.OtherSlotPlan) { p.Data.FormatAs = "tsxdata" })
	reseal("all eight keeper parts hashed", "keeper parts", func(p *plan.OtherSlotPlan) { p.KeeperSHA256["system_a"] = strings.Repeat("0", 64) })
	reseal("a keeper hash missing", "keeper", func(p *plan.OtherSlotPlan) { delete(p.KeeperSHA256, "boot_a") })
	reseal("uboot_b from uboot_b", "only from keeper", func(p *plan.OtherSlotPlan) { p.Sources[0].KeeperPartition = "uboot_b" })
	reseal("another data label", "not userdata ext4 tsxdata-new/tsxdata", func(p *plan.OtherSlotPlan) { p.Data.Label = "android" })
	reseal("slot mode on the dry medium", "medium runs only", func(p *plan.OtherSlotPlan) { p.Mode = "slot" })
	// review F4: vbmeta_b is bound to the payload's patched vbmeta, not to the plan's record
	vsrc := func(p *plan.OtherSlotPlan) (*plan.WriteSource, *plan.WriteStep) {
		for _, s := range p.Steps {
			if s.Write != nil && s.Write.Partition == "vbmeta_b" {
				return p.Source("vbmeta_b"), s.Write
			}
		}
		panic("no vbmeta_b step")
	}
	reseal("vbmeta_b is the signed vbmeta (another sha)", "the payload's patched vbmeta", func(p *plan.OtherSlotPlan) {
		src, w := vsrc(p)
		src.SHA256, w.SHA256 = mltest.SHA(fx.New["vbmeta"]), mltest.SHA(fx.New["vbmeta"])
	})
	reseal("the plan's boot cap is not the patch's", "the payload's patched vbmeta", func(p *plan.OtherSlotPlan) { p.Chain.BootCap++ })
	reseal("the plan has no vbmeta patch", "no vbmeta patch", func(p *plan.OtherSlotPlan) { p.Chain.VbmetaPatch = nil })
	reseal("the plan's gate is not a test gate, the gate does not check the patch", "not a test gate", func(p *plan.OtherSlotPlan) {
		p.Gate.Gate = "embedded"
	})
	// review T1 MINOR-1: uboot_b is bound to the payload's required chain (a resealed 512-byte prefix is refused)
	reseal("uboot_b a 512-byte prefix of uboot_a", "required boot chain", func(p *plan.OtherSlotPlan) {
		pre := mltest.SHA(fx.New["uboot"][:512])
		p.Sources[0].Length, p.Sources[0].SHA256 = 512, pre
		p.Steps[1].Write.Length, p.Steps[1].Write.SHA256 = 512, pre
	})
	// review T1 MINOR-2: under a gate that checks the patch, the plan's patch facts must be the reviewed patch
	var signedCap uint64
	for _, d := range fx.Manifests[0].AVB.Descriptors {
		if d.Partition == "boot" {
			signedCap = d.ImageSize
		}
	}
	vgate := b.Gate().WithVbmetaPatches([]mainline.VbmetaPatch{b.VbmetaPatch(signedCap)})
	t.Run("a checking gate: the dry run completes", func(t *testing.T) {
		r := newOSRig(t, fx, tool, u, "a", b, ml, res)
		r.gate = vgate
		im := r.open()
		defer im.Close()
		p, err := r.makePlan(im)
		if err != nil || p.Gate.VbmetaPatch == nil {
			t.Fatalf("plan: %v", err)
		}
		if _, err := install.ExecuteOtherSlot(p, ml, im, install.OtherSlotOptions{Options: install.Options{Bootctl: tool}, Data: hook, Gate: vgate}); err != nil {
			t.Fatal(err)
		}
		r.checkDone(p)
	})
	t.Run("a checking gate: the plan's patch facts are not the reviewed patch", func(t *testing.T) {
		r := newOSRig(t, fx, tool, u, "a", b, ml, res)
		r.gate = vgate
		im := r.open()
		defer im.Close()
		p, err := r.makePlan(im)
		if err != nil {
			t.Fatal(err)
		}
		p.Chain.VbmetaPatch.OldImageSize++
		p.Seal()
		refuse(t, r, im, p, install.OtherSlotOptions{Data: hook, Gate: vgate}, "not a reviewed patch")
	})
	// task 4 (review F2): a plan of another boot is refused
	t.Run("a plan of another boot", func(t *testing.T) {
		r, im, p := fresh(t)
		p.Running.BootID = "boot-1"
		p.Seal()
		refuse(t, r, im, p, install.OtherSlotOptions{Data: hook, BootID: "boot-2"}, "make a new plan")
	})
	t.Run("a gate that checks the patch and lists none", func(t *testing.T) {
		r, im, p := fresh(t)
		refuse(t, r, im, p, install.OtherSlotOptions{Data: hook, Gate: b.Gate().WithVbmetaPatches(nil)}, "payload gate now")
	})
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
	t.Run("no payload gate", func(t *testing.T) {
		r, im, p := fresh(t)
		refuse(t, r, im, p, install.OtherSlotOptions{Data: hook}, "no payload gate")
	})
	t.Run("a gate that does not list the payload", func(t *testing.T) {
		r, im, p := fresh(t)
		other := mltest.NewBundle("other-9", 30001, 1<<20, mltest.Chain{}, nil, 0)
		refuse(t, r, im, p, install.OtherSlotOptions{Data: hook, Gate: other.Gate()}, "payload gate now")
	})
	// the unit changed after the plan (a stale plan): the key record removed, userdata formatted
	changed := func(name, want string, off func(t *testing.T, r *osRig) int64, b []byte) {
		t.Run(name, func(t *testing.T) {
			r, im, p := fresh(t)
			im.Close()
			f, _ := os.OpenFile(r.path, os.O_RDWR, 0)
			f.WriteAt(b, off(t, r))
			f.Close()
			r.before, _ = os.ReadFile(r.path)
			im2 := r.open()
			defer im2.Close()
			refuse(t, r, im2, p, install.OtherSlotOptions{Data: hook}, want)
		})
	}
	part := func(name string) func(t *testing.T, r *osRig) int64 {
		return func(t *testing.T, r *osRig) int64 {
			tb, _ := gpt.Parse(bytes.NewReader(r.before), int64(len(r.before)))
			e, _ := tb.Find(name)
			return e.Start * 512
		}
	}
	changed("the key record removed after the plan", "not kept on the unit now", func(t *testing.T, r *osRig) int64 {
		return part("backup")(t, r) + (4096-keykeep.FromEnd1)*512
	}, make([]byte, 512))
	changed("userdata became a finished /data after the plan", "userdata holds", part("userdata"), mltest.Rootfs(8192, "tsxdata", 3))
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

// A data hook that writes what the real one leaves on userdata: data-format
// an ext4 superblock labelled tsxdata-new, data-migrate the label tsxdata.
// A stale plan (the A/B sector set back to its start, as B's mark and the
// switch back leave it) is refused, and a plan for a userdata that a cut left
// half done (tsxdata-new) is accepted and finishes (review F1, F2).
func TestOtherSlotStaleReplayAndHalfData(t *testing.T) {
	tool := realTool(t)
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	b, res := mlBundle(t, fx)
	ml := mlPayloadOf(t, b)
	r := newOSRig(t, fx, tool, osShapes(fx, res)[1].unit, "a", b, ml, res)
	tb, _ := gpt.Parse(bytes.NewReader(r.before), int64(len(r.before)))
	ud, _ := tb.Find("userdata")
	label := func(l string) {
		f, err := os.OpenFile(r.path, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteAt(mltest.Rootfs(8192, l, 4)[1024:2048], ud.Start*512+1024)
		f.Close()
	}
	real := func(cut string) func(op string, d *plan.DataPlan) error {
		return func(op string, d *plan.DataPlan) error {
			switch op {
			case plan.OpDataFormat:
				label(d.FormatAs)
				if cut == op {
					return errors.New("cut after the format")
				}
			case plan.OpDataMigrate:
				label(d.Label)
			}
			return nil
		}
	}
	run := func(p *plan.OtherSlotPlan, hook func(string, *plan.DataPlan) error) error {
		im := r.open()
		defer im.Close()
		_, err := install.ExecuteOtherSlot(p, ml, im, install.OtherSlotOptions{Options: install.Options{Bootctl: tool}, Data: hook, Gate: b.Gate()})
		return err
	}
	plan0 := func() *plan.OtherSlotPlan {
		im := r.open()
		defer im.Close()
		p, err := r.makePlan(im)
		if err != nil {
			var sb strings.Builder
			p.Render(&sb)
			t.Fatalf("%v\n%s", err, sb.String())
		}
		return p
	}
	// 1. a cut after data-format: B disabled, userdata tsxdata-new
	p1 := plan0()
	if err := run(p1, real(plan.OpDataFormat)); err == nil {
		t.Fatal("no stop")
	}
	// the old plan is stale now (the A/B sector and userdata changed)
	if err := run(p1, real("")); err == nil || !strings.Contains(err.Error(), "differs from the plan") {
		t.Fatalf("the plan of before the format ran again: %v", err)
	}
	// 2. a new plan of the half-done unit finishes
	p2 := plan0()
	if p2.Data.Before != "ext4 label tsxdata-new" {
		t.Fatalf("before %q", p2.Data.Before)
	}
	if err := run(p2, real("")); err != nil {
		t.Fatal(err)
	}
	// 3. the A/B sector set back to the start bytes (B's mark and the switch): the first plan is refused
	f, _ := os.OpenFile(r.path, os.O_RDWR, 0)
	f.WriteAt(r.start, plan.ABLBA*512)
	f.Close()
	// (the plan of the true start: its A/B sector matches again)
	if err := run(p1, real("")); err == nil || !strings.Contains(err.Error(), "userdata holds ext4 label tsxdata now") {
		t.Fatalf("a stale plan ran again: %v", err)
	}
	// and a new plan with the data steps is refused: userdata is a finished /data
	im := r.open()
	defer im.Close()
	if p, err := r.makePlan(im); err == nil {
		t.Fatalf("a data plan on a finished /data:\n%v", p.Steps)
	}
}
