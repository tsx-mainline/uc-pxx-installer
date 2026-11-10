package install_test

// The mainline install (plan.ComputeMainline, install.RunMainline) on the
// dry medium with the real uc-pxx-bootctl (UC_PXX_BOOTCTL; skipped without
// it): a full run, the power-cut sweep (a cut before every primitive write,
// clean and torn, each point checked with CheckCutState and resumed), the
// proof that the sweep catches a bad mainline plan, and the executor's guards.

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
	"uc-pxx-installer/internal/pcdfw/pcdfwtest"
	"uc-pxx-installer/internal/plan"
)

var mlUnit = fakeemmc.Unit{AB: [2][3]byte{{14, 0, 1}, {15, 0, 1}}, A: "old", B: "new"}

// mlUnitKept is mlUnit with the key record in both sectors of backup (the
// fixture's 4096-sector backup): the plan's key state is "kept", no
// keep-key step.
var mlUnitKept = func() fakeemmc.Unit {
	u := mlUnit
	b := make([]byte, 4096*512)
	rec := keykeep.Encode(pcdfwtest.Keys())
	copy(b[(4096-keykeep.FromEnd1)*512:], rec)
	copy(b[(4096-keykeep.FromEnd2)*512:], rec)
	u.Tweak = map[string][]byte{"backup": b}
	return u
}()

const mlToken, mlLinux = "mltest-1", 30000

// mlBundle is a mainline bundle for the fixture: the required chain is the
// fixture payload's (slot B holds it), the template fits its boot cap.
func mlBundle(t *testing.T, fx *fakeemmc.Fixture) (*mltest.Bundle, []byte) {
	t.Helper()
	res, err := mainline.ReadResource(bytes.NewReader(fx.New["boot"]), 0, int64(len(fx.New["boot"])))
	if err != nil {
		t.Fatal(err)
	}
	m := fx.Manifests[0]
	var cap int64
	for _, d := range m.AVB.Descriptors {
		if d.Partition == "boot" {
			cap = int64(d.ImageSize)
		}
	}
	ch := mltest.Chain{Version: m.Firmware.Version, BuildType: m.Build["ro.build.type"], UBoot: fx.ManifestPartSHA("uboot"),
		Trust: fx.ManifestPartSHA("trust"), Dtbo: fx.ManifestPartSHA("dtbo"), Vbmeta: fx.ManifestPartSHA("vbmeta"),
		Resource: mltest.SHA(res)}
	// contract v2: vbmeta_a is the patch of the keeper's vbmeta. cap is the
	// signed boot image_size; the patched one is 64 KiB more (it must be
	// larger than the signed one and fit the fixture's boot_a)
	ps := cap + 64<<10
	b := mltest.NewBundle(mlToken, mlLinux, 1<<20, ch, res, ps)
	if _, err := b.PatchVbmeta(fx.New["vbmeta"], ps); err != nil {
		panic(err) // the fixture's own signed vbmeta
	}
	return b, res
}

func mlPayloadOf(t *testing.T, b *mltest.Bundle) *mainline.Payload {
	t.Helper()
	d, err := b.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	src, err := mainline.DirSource(d)
	if err != nil {
		t.Fatal(err)
	}
	p, err := mainline.Verify(src)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

type mlRig struct {
	*rig
	ml       *mainline.Payload
	bundle   *mltest.Bundle
	expected []byte // boot_a after the install
}

func newMLRig(t *testing.T, fx *fakeemmc.Fixture, tool bootctl.Tool, u fakeemmc.Unit, b *mltest.Bundle, ml *mainline.Payload, res []byte) *mlRig {
	return &mlRig{rig: newRig(t, fx, tool, u), ml: ml, bundle: b, expected: mltest.Expected(mlToken, mlLinux, 11, res)}
}

func (r *mlRig) makePlan(im *install.Image) (*plan.MainlinePlan, error) {
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
	in := plan.MainlineInput{Input: plan.Input{Disk: im.Reader(), DiskBytes: im.Size(), Source: "image:" + r.path, Mode: "dry-run",
		Tool: "test", PreFastboot: sec[:32], Bootctl: info, BootctlStatus: st, PrereqsConfirmed: true, HashKeeper: true},
		Mainline: r.ml, Manifests: r.fx.Manifests, UnlockConfirmed: true, Gate: r.bundle.Gate()}
	in.MainlineKey = plan.ReadMainlineKey(im.Reader(), im.Size(), keykeep.Encode(r.fx.Keys),
		keykeep.Gate{Open: true, Why: "test", SHA256: strings.Repeat("ab", 32)}, "test firmware")
	return plan.ComputeMainline(in)
}

// TestMainlineKeepKeyGuards (task 2): a keep-key plan runs only with its
// record, the record of another key is refused, and a keep-key step that is
// not the first step is refused; nothing is written.
func TestMainlineKeepKeyGuards(t *testing.T) {
	tool := realTool(t)
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	b, res := mlBundle(t, fx)
	ml := mlPayloadOf(t, b)
	other := pcdfwtest.Keys()
	other.IV[0] ^= 1
	for _, c := range []struct {
		name string
		rec  []byte
		mut  func(p *plan.MainlinePlan)
		want string
	}{
		{"no record given", nil, nil, "do not fit"},
		{"the record of another key", keykeep.Encode(other), nil, "do not fit"},
		{"the keep-key step after the disable", mlRecord, func(p *plan.MainlinePlan) {
			p.Steps[0], p.Steps[1] = p.Steps[1], p.Steps[0]
			p.Steps[0].N, p.Steps[1].N = 1, 2
		}, "do not fit"},
		{"a second keep-key step in place of the sync", mlRecord, func(p *plan.MainlinePlan) {
			n := len(p.Steps)
			kk := p.Steps[0]
			kk.N = p.Steps[n-4].N
			p.Steps[n-4] = kk
		}, "not the first step"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newMLRig(t, fx, tool, mlUnit, b, ml, res)
			im := r.open()
			p, err := r.makePlan(im)
			if err != nil || p.Steps[0].Op != "keep-key" || p.KeyKeep.State != "keep" {
				t.Fatalf("plan: %v", err)
			}
			if c.mut != nil {
				c.mut(p)
				p.Seal()
			}
			j := &journal{}
			_, err = install.ExecuteMainline(p, ml, im, install.Options{KeyRecord: c.rec, Bootctl: tool, Hooks: &install.Hooks{CutAfter: -1, Observe: j.observe}})
			im.Close()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			if after, _ := os.ReadFile(r.path); len(j.ev) != 0 || !bytes.Equal(after, r.before) {
				t.Fatalf("%d writes; the image changed", len(j.ev))
			}
		})
	}
}

// mlRecord is the key record of the fixture's key (task 2: every mainline
// plan of a unit without the record has the keep-key step).
var mlRecord = keykeep.Encode(pcdfwtest.Keys())

// checkDone checks a finished mainline install: nothing outside slot A's
// write ranges and the A/B sector changed, the final A/B state, slot A holds
// the keeper's chain, the assembled boot image and the root file system, and
// vendor_a and oem_a are as before.
func (r *mlRig) checkDone(p *plan.MainlinePlan) {
	t := r.t
	after, err := os.ReadFile(r.path)
	if err != nil {
		t.Fatal(err)
	}
	if !sameOutside(after, r.before, &p.Plan) {
		t.Fatal("a byte outside slot A's write ranges and the A/B sector changed")
	}
	a, _ := avbab.Parse(after[plan.ABLBA*512:])
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	want, _ := p.AB.Final.Raw()
	if !bytes.Equal(after[plan.ABLBA*512:plan.ABLBA*512+32], want[:]) || a.Pick() != 0 || !a.Slots[1].Keeper() {
		t.Fatalf("final A/B %x, want %x", after[plan.ABLBA*512:plan.ABLBA*512+32], want)
	}
	if !bytes.Equal(after[plan.ABLBA*512+32:(plan.ABLBA+1)*512], r.start[32:]) {
		t.Fatal("A/B bytes 32..511 changed")
	}
	tb, _ := gpt.Parse(bytes.NewReader(after), int64(len(after)))
	holds := func(name string, img []byte) {
		e, _ := tb.Find(name)
		if !bytes.Equal(after[e.Start*512:e.Start*512+int64(len(img))], img) {
			t.Fatalf("%s does not hold its image", name)
		}
	}
	for _, n := range []string{"uboot", "trust", "dtbo"} {
		holds(n+"_a", r.fx.New[n])
	}
	// contract v2: vbmeta_a is the patch of the keeper's vbmeta
	pv, _, err := mainline.PatchVbmeta(r.fx.New["vbmeta"], uint64(p.Chain.BootCap))
	if err != nil || bytes.Equal(pv, r.fx.New["vbmeta"]) {
		t.Fatalf("the patch of the fixture's vbmeta: %v", err)
	}
	holds("vbmeta_a", pv)
	holds("boot_a", r.expected)
	holds("system_a", r.bundle.Rootfs)
	for _, n := range []string{"vendor_a", "oem_a"} {
		e, _ := tb.Find(n)
		if !bytes.Equal(after[e.Start*512:(e.End+1)*512], r.before[e.Start*512:(e.End+1)*512]) {
			t.Fatalf("%s changed", n)
		}
	}
	cs, err := install.CheckCutState(bytes.NewReader(after), &p.Plan, r.start)
	if err != nil || cs.Boots != "target" || !cs.Checked {
		t.Fatalf("final state %+v: %v", cs, err)
	}
}

func TestMainlineDryRunInstall(t *testing.T) {
	tool := realTool(t)
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	b, res := mlBundle(t, fx)
	ml := mlPayloadOf(t, b)
	r := newMLRig(t, fx, tool, mlUnit, b, ml, res)
	im := r.open()
	defer im.Close()
	p, err := r.makePlan(im)
	if err != nil {
		var sb strings.Builder
		p.Render(&sb)
		t.Fatalf("%v\n%s", err, sb.String())
	}
	j := &journal{}
	var log bytes.Buffer
	var said []string
	res2, err := install.ExecuteMainline(p, ml, im, install.Options{KeyRecord: mlRecord, Bootctl: tool, Log: &log, Say: func(s string) { said = append(said, s) },
		Hooks: &install.Hooks{CutAfter: -1, Observe: j.observe}})
	if err != nil {
		t.Fatalf("%v\n%s", err, strings.Join(said, "\n"))
	}
	if res2.Steps != len(p.Steps) || res2.FinalAB != p.AB.Final {
		t.Fatalf("%d steps", res2.Steps)
	}
	r.checkDone(p)
	// A/B writes first and last, data only in between, and data only in slot A
	var abAt []int
	for i, e := range j.ev {
		if e.Op == "ab" {
			abAt = append(abAt, i)
		}
		if e.Op == "data" && !strings.HasSuffix(e.Partition, "_a") {
			t.Fatalf("data write to %s", e.Partition)
		}
	}
	// task 2: the two key-record writes of the keep-key step come first, before every A/B write
	if len(j.ev) < 2 || j.ev[0].Op != "key" || j.ev[1].Op != "key" || p.Steps[0].Op != "keep-key" {
		t.Fatalf("the install does not start with the two key-record writes: %+v", j.ev[:min(2, len(j.ev))])
	}
	if len(abAt) != 2 || abAt[0] != 2 || abAt[1] != len(j.ev)-1 {
		t.Fatalf("A/B writes at %v of %d writes", abAt, len(j.ev))
	}
	for _, line := range strings.Split(strings.TrimSpace(log.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
	}
	if !strings.Contains(strings.Join(said, "\n"), "keeper slot B: all 8 partitions unchanged") {
		t.Fatalf("no keeper check:\n%s", strings.Join(said, "\n"))
	}
	// the official executor refuses the mainline plan (and its inner plan)
	im2 := r.open()
	defer im2.Close()
	if _, err := install.Execute(&p.Plan, nil, im2, install.Options{Bootctl: tool}); err == nil || !strings.Contains(err.Error(), "no valid id") {
		t.Fatalf("the official executor ran the inner plan of a mainline plan: %v", err)
	}
}

// mlShapes are the start states of the mainline sweep.
func mlShapes(fx *fakeemmc.Fixture) []struct {
	name string
	unit fakeemmc.Unit
} {
	return []struct {
		name string
		unit fakeemmc.Unit
	}{
		{"A 14 successful, keeper B 15", mlUnit},
		{"trust_a identical", fakeemmc.Unit{AB: mlUnit.AB, A: "old", B: "new", Tweak: map[string][]byte{"trust_a": fx.New["trust"]}}},
		{"keeper B 14, A 13", fakeemmc.Unit{AB: [2][3]byte{{13, 0, 1}, {14, 0, 1}}, A: "old", B: "new"}},
		{"A disabled, keeper B 15", fakeemmc.Unit{AB: [2][3]byte{{0, 0, 0}, {15, 0, 1}}, A: "old", B: "new"}},
	}
}

// mlSamePlan: plan b of a fresh image at pb is plan a of the image at pa
// but for the path.
func mlSamePlan(a, b *plan.MainlinePlan, pa, pb string) bool {
	if pa == pb {
		return false
	}
	ca, cb := *a, *b
	ca.ID, cb.ID = "", ""
	ja, err1 := json.Marshal(&ca)
	jb, err2 := json.Marshal(&cb)
	return err1 == nil && err2 == nil && bytes.Equal(ja, bytes.ReplaceAll(jb, []byte(pb), []byte(pa)))
}

// TestMainlinePowerCutSweep: for each start shape, the mainline install is
// cut before every primitive write (and once more with a torn write), as a
// power loss would: no clean-up. At every point CheckCutState must pass (the
// keeper B successful and booting; slot A bootable only with all of the old
// or all of the new bytes), nothing outside slot A's write ranges and the A/B
// sector may differ, slot A must be unbootable from its first written byte
// until the arm, and only the last write (the arm) may make A the next boot.
// Then the installer runs again on the cut image and must finish, or refuse
// when A boots next.
func TestMainlinePowerCutSweep(t *testing.T) {
	tool := realTool(t)
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	b, res := mlBundle(t, fx)
	ml := mlPayloadOf(t, b)
	t.Parallel()
	var total atomic.Int64
	t.Cleanup(func() {
		t.Logf("mainline power-cut sweep: %d cut points checked (A/B decoded with internal/avbab), each one resumed", total.Load())
	})
	for _, sh := range mlShapes(fx) {
		point(t, sh.name, func(t *testing.T) {
			r := newMLRig(t, fx, tool, sh.unit, b, ml, res)
			im := r.open()
			p0, err := r.makePlan(im)
			if err != nil {
				t.Fatal(err)
			}
			path0 := r.path
			j := &journal{}
			if _, err := install.ExecuteMainline(p0, ml, im, install.Options{KeyRecord: mlRecord, Bootctl: tool, MaxWrite: sweepWrite,
				Hooks: &install.Hooks{CutAfter: -1, Observe: j.observe}}); err != nil {
				t.Fatal(err)
			}
			im.Close()
			n := len(j.ev)
			for _, torn := range []bool{false, true} {
				for _, k := range sweepPoints(n, j.ev) {
					point(t, fmt.Sprintf("cut %d torn %v", k, torn), func(t *testing.T) {
						sweepList(t, sh.name, fmt.Sprintf("cut %d torn %v", k, torn))
						r := newMLRig(t, fx, tool, sh.unit, b, ml, res)
						im := r.open()
						p, err := r.makePlan(im)
						if err != nil {
							t.Fatal(err)
						}
						if !mlSamePlan(p0, p, path0, r.path) {
							t.Fatal("the plan of a fresh image differs")
						}
						_, err = install.ExecuteMainline(p, ml, im, install.Options{KeyRecord: mlRecord, Bootctl: tool, MaxWrite: sweepWrite,
							Hooks: &install.Hooks{CutAfter: k, Torn: torn}})
						if k < n && !errors.Is(err, install.ErrPowerCut) {
							t.Fatalf("cut %d: err %v, want a power cut", k, err)
						}
						if k == n && err != nil {
							t.Fatalf("cut %d (no cut): %v", k, err)
						}
						im.Close()
						cut, _ := os.ReadFile(r.path)
						cs, err := install.CheckCutState(bytes.NewReader(cut), &p.Plan, r.start)
						if err != nil {
							t.Fatalf("cut before write %d (torn %v): %v (A/B %s)", k+1, torn, err, cs.AB)
						}
						if !sameOutside(cut, r.before, &p.Plan) {
							t.Fatalf("cut %d: a byte outside slot A's write ranges changed", k)
						}
						a, _ := avbab.Parse(cut[plan.ABLBA*512:])
						if !a.Slots[1].Keeper() || !a.Slots[1].Bootable() {
							t.Fatalf("cut %d: the keeper B is not successful: %v", k, a.Slots[1])
						}
						// slot A is unbootable from its first written byte until the arm
						dataDone := torn && k < n && j.ev[k].Op == "data"
						for _, e := range j.ev[:k] {
							dataDone = dataDone || e.Op == "data"
						}
						if k < n && dataDone && a.Slots[0] != (avbab.Slot{}) {
							t.Fatalf("cut %d (torn %v): slot A has written bytes but is %v (%s)", k, torn, a.Slots[0], cs.Target)
						}
						if k == n && (cs.Boots != "target" || !cs.Checked) {
							t.Fatalf("complete run: %+v", cs)
						}
						if k < n && cs.Boots != "keeper" {
							t.Fatalf("cut %d: %+v: only the last write may make slot A the next boot", k, cs)
						}
						r.resume(p, cs, k)
						total.Add(1)
					})
				}
			}
		})
	}
}

// resume runs the mainline installer again on a cut image.
func (r *mlRig) resume(p *plan.MainlinePlan, cs install.CutState, k int) {
	t := r.t
	im := r.open()
	defer im.Close()
	p2, err := r.makePlan(im)
	if cs.Boots == "target" {
		if err == nil {
			t.Fatalf("cut %d: slot A boots next, but a new plan was accepted", k)
		}
		return
	}
	if err != nil {
		var sb strings.Builder
		p2.Render(&sb)
		t.Fatalf("cut %d: resume refused: %v\n%s", k, err, sb.String())
	}
	if p2.Keeper != "b" || p2.AB.Final != p.AB.Final {
		t.Fatalf("cut %d: resume plan keeper %s final %s", k, p2.Keeper, p2.AB.Final)
	}
	if _, err := install.ExecuteMainline(p2, r.ml, im, install.Options{KeyRecord: mlRecord, Bootctl: r.tool}); err != nil {
		t.Fatalf("cut %d: resume: %v", k, err)
	}
	r.checkDone(p)
}

// TestMainlineSweepCatchesBadPlans: the bad mainline plans of phase 3 (a
// plan without the disable, a plan that arms slot A before writing it, and
// one without verify and sync that arms before the last write), each resealed,
// were caught by the cut check at some cut point. Since the review of
// c0fb5c5 (finding 2) the executor's structural check refuses each of them
// in the precheck, before anything is written.
func TestMainlineSweepCatchesBadPlans(t *testing.T) {
	tool := realTool(t)
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	b, res := mlBundle(t, fx)
	ml := mlPayloadOf(t, b)
	for _, c := range []struct {
		name string
		mut  func(p *plan.MainlinePlan)
		want string
	}{
		{"no disable", func(p *plan.MainlinePlan) { p.Steps = p.Steps[1:] }, "steps, a mainline plan has"},
		{"arm first", func(p *plan.MainlinePlan) {
			n := len(p.Steps)
			arm, check := p.Steps[n-2], p.Steps[n-1]
			arm.Bootctl.Before = p.Steps[0].Bootctl.After
			steps := append([]plan.Step{p.Steps[0], arm}, p.Steps[1:n-2]...)
			p.Steps = append(steps, check)
		}, "step 2: number 10"},
		{"verify and sync dropped, arm before the last write", func(p *plan.MainlinePlan) {
			n := len(p.Steps)
			arm, check := p.Steps[n-2], p.Steps[n-1]
			w := p.Steps[1 : n-4]
			steps := append([]plan.Step{p.Steps[0]}, w[:len(w)-1]...)
			p.Steps = append(append(steps, arm, w[len(w)-1]), check)
		}, "steps, a mainline plan has"},
		{"renumbered without the disable, a write in its place", func(p *plan.MainlinePlan) {
			p.Steps[0] = p.Steps[1]
			p.Steps[0].N = 1
		}, "step 1 is not disable --slot a"},
		{"the arm without --lower-keeper", func(p *plan.MainlinePlan) {
			a := &p.Steps[len(p.Steps)-2]
			a.Bootctl = &plan.BootctlStep{Args: []string{"arm-trying", "--slot", "a"}, Before: a.Bootctl.Before, After: a.Bootctl.After}
		}, "is not arm-trying --slot a --lower-keeper"},
		{"verify without system_a", func(p *plan.MainlinePlan) {
			v := &p.Steps[len(p.Steps)-3]
			v.Verify = v.Verify[:len(v.Verify)-1]
		}, "does not cover exactly the six writes"},
		{"another final state", func(p *plan.MainlinePlan) { p.AB.Final = p.AB.Now }, "check-ab of the predicted final state"},
		{"a failed check", func(p *plan.MainlinePlan) { p.Checks[3].OK = false }, "the plan's check"},
		{"no unlock confirmation", func(p *plan.MainlinePlan) { p.Unlock.Confirmed = false }, "no unlock confirmation"},
		{"no payload gate", func(p *plan.MainlinePlan) { p.Gate = nil }, "payload gate"},
		// task 2: the key state is part of the plan's shape
		{"no keep-key part", func(p *plan.MainlinePlan) { p.KeyKeep = nil }, "without its keep-key part"},
		{"the key state not-possible", func(p *plan.MainlinePlan) { p.KeyKeep.State = "not-possible" }, "do not fit"},
		{"kept, but a keep-key step after the disable", func(p *plan.MainlinePlan) {
			kk := plan.Step{N: 2, Op: "keep-key", KeepKey: &plan.KeepKeyStep{Partition: "backup", RecordSHA256: p.KeyKeep.RecordSHA256}}
			steps := append([]plan.Step{p.Steps[0], kk}, p.Steps[1:]...)
			for i := range steps {
				steps[i].N = i + 1
			}
			p.Steps = steps
		}, "steps, a mainline plan has"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newMLRig(t, fx, tool, mlUnitKept, b, ml, res) // no keep-key step: the indexes below are the disable's
			im := r.open()
			p, err := r.makePlan(im)
			if err != nil || p.Steps[0].Op != "bootctl" || p.KeyKeep.State != "kept" {
				t.Fatalf("plan: %v", err)
			}
			c.mut(p)
			p.Seal()
			j := &journal{}
			_, err = install.ExecuteMainline(p, ml, im, install.Options{KeyRecord: mlRecord, Bootctl: tool, Hooks: &install.Hooks{CutAfter: -1, Observe: j.observe}})
			im.Close()
			if err == nil || !strings.HasPrefix(err.Error(), "precheck: ") || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want a precheck refusal with %q", err, c.want)
			}
			if after, _ := os.ReadFile(r.path); len(j.ev) != 0 || !bytes.Equal(after, r.before) {
				t.Fatalf("%d writes; the image changed", len(j.ev))
			}
		})
	}
}

func TestMainlineExecuteGuards(t *testing.T) {
	tool := realTool(t)
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	b, res := mlBundle(t, fx)
	ml := mlPayloadOf(t, b)
	// another bundle (another token): not the plan's payload
	b2 := mltest.NewBundle("other-2", mlLinux, 1<<20, mltest.Chain{}, res, 0)
	for _, k := range []string{"schema", "kind", "version", "model", "kernel", "requires_bootchain", "vbmeta_patched", "vbmeta_boot_cap"} {
		b2.Manifest[k] = b.Manifest[k]
	}
	b2.Sub("boot_assembled")["resource_sha256"] = b.Sub("boot_assembled")["resource_sha256"]
	run := func(t *testing.T, change func(r *mlRig, p *plan.MainlinePlan), pl *mainline.Payload, hooks *install.Hooks, want string, wantData bool) {
		t.Helper()
		r := newMLRig(t, fx, tool, mlUnitKept, b, ml, res) // no keep-key step: the step indexes below are the disable's
		im := r.open()
		defer im.Close()
		p, err := r.makePlan(im)
		if err != nil {
			t.Fatal(err)
		}
		if change != nil {
			change(r, p)
		}
		if hooks == nil {
			hooks = &install.Hooks{CutAfter: -1}
		}
		if pl == nil {
			pl = ml
		}
		j := &journal{}
		hooks.Observe = j.observe
		_, err = install.ExecuteMainline(p, pl, im, install.Options{KeyRecord: mlRecord, Bootctl: tool, Hooks: hooks})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want %q", err, want)
		}
		img, _ := os.ReadFile(r.path)
		cs, err := install.CheckCutState(bytes.NewReader(img), &p.Plan, r.start)
		if err != nil || cs.Boots != "keeper" {
			t.Fatalf("after the stop: %+v %v", cs, err)
		}
		data := false
		for _, e := range j.ev {
			data = data || e.Op == "data"
		}
		if data != wantData {
			t.Fatalf("data written: %v, want %v", data, wantData)
		}
		if data && (cs.Target != "disabled" || !strings.Contains(install.SafeState(im, &p.Plan), "safe to power off")) {
			t.Fatalf("after a stop with data written, slot A is %s", cs.Target)
		}
	}
	reseal := func(f func(p *plan.MainlinePlan)) func(r *mlRig, p *plan.MainlinePlan) {
		return func(r *mlRig, p *plan.MainlinePlan) { f(p); p.Seal() }
	}
	poke := func(r *mlRig, name string, off int64) {
		tb, _ := gpt.Parse(bytes.NewReader(r.before), int64(len(r.before)))
		e, _ := tb.Find(name)
		f, _ := os.OpenFile(r.path, os.O_RDWR, 0)
		f.WriteAt([]byte{0x5a}, e.Start*512+off)
		f.Close()
	}
	t.Run("edited plan", func(t *testing.T) {
		run(t, func(r *mlRig, p *plan.MainlinePlan) { p.Steps[1].Write.Partition = "uboot_b" }, nil, nil, "no valid id", false)
	})
	t.Run("sealed plan that writes the keeper", func(t *testing.T) {
		run(t, reseal(func(p *plan.MainlinePlan) { p.Steps[1].Write.Partition = "uboot_b"; p.Sources[0].Partition = "uboot_b" }),
			nil, nil, "step 2 is not the write of uboot_a", false)
	})
	t.Run("sealed plan that writes vendor_a", func(t *testing.T) {
		run(t, reseal(func(p *plan.MainlinePlan) {
			p.Steps[6].Write.Partition = "vendor_a"
			p.Sources[5].Partition = "vendor_a"
		}), nil, nil, "step 7 is not the write of system_a", false)
	})
	t.Run("sealed plan with uboot_a from the rootfs", func(t *testing.T) {
		run(t, reseal(func(p *plan.MainlinePlan) { p.Steps[1].Write.SegID = "rootfs"; p.Sources[0].From = "rootfs" }),
			nil, nil, "takes it only from keeper", false)
	})
	t.Run("sealed plan with vbmeta_a copied from the keeper", func(t *testing.T) {
		run(t, reseal(func(p *plan.MainlinePlan) { p.Steps[4].Write.SegID = "keeper"; p.Sources[3].From = "keeper" }),
			nil, nil, "takes it only from patched-vbmeta", false)
	})
	t.Run("sealed plan with uboot_a as a patched vbmeta", func(t *testing.T) {
		run(t, reseal(func(p *plan.MainlinePlan) {
			p.Steps[1].Write.SegID = "patched-vbmeta"
			p.Sources[0].From = "patched-vbmeta"
		}), nil, nil, "takes it only from keeper", false)
	})
	t.Run("sealed plan with a patched vbmeta of no image_size", func(t *testing.T) {
		run(t, reseal(func(p *plan.MainlinePlan) { p.Sources[3].BootImageSize = 0 }), nil, nil, "without its source sha256", false)
	})
	t.Run("sealed plan with another patched image_size", func(t *testing.T) {
		run(t, reseal(func(p *plan.MainlinePlan) { p.Sources[3].BootImageSize += 2048 }), nil, nil, "the patched vbmeta is", true)
	})
	t.Run("sealed plan whose chain names another patch", func(t *testing.T) {
		run(t, reseal(func(p *plan.MainlinePlan) {
			v := *p.Chain.VbmetaPatch
			v.SHA256 = strings.Repeat("7", 64)
			p.Chain.VbmetaPatch = &v
		}), nil, nil, "not the plan's boot chain patch", true)
	})
	t.Run("sealed plan under the release gate with an unreviewed patch", func(t *testing.T) {
		run(t, reseal(func(p *plan.MainlinePlan) { p.Gate.Gate = "embedded" }), nil, nil, "not a reviewed patch of this release", true)
	})
	t.Run("sealed plan with a write source missing", func(t *testing.T) {
		run(t, reseal(func(p *plan.MainlinePlan) { p.Sources = p.Sources[:5] }), nil, nil, "no matching write source", false)
	})
	t.Run("sealed plan without the chain", func(t *testing.T) {
		run(t, reseal(func(p *plan.MainlinePlan) { p.Chain = nil }), nil, nil, "not a mainline plan", false)
	})
	t.Run("sealed plan with a keep-key step (the key is kept already)", func(t *testing.T) {
		run(t, reseal(func(p *plan.MainlinePlan) {
			p.Steps = append([]plan.Step{{N: 1, Op: "keep-key", KeepKey: &plan.KeepKeyStep{}}}, p.Steps...)
		}), nil, nil, "do not fit", false)
	})
	t.Run("another payload", func(t *testing.T) {
		run(t, nil, mlPayloadOf(t, b2), nil, "not the one the plan was made with", false)
	})
	t.Run("A/B changed after the plan", func(t *testing.T) {
		run(t, func(r *mlRig, p *plan.MainlinePlan) {
			f, _ := os.OpenFile(r.path, os.O_RDWR, 0)
			f.WriteAt([]byte{0x55}, plan.ABLBA*512+400)
			f.Close()
			r.before, _ = os.ReadFile(r.path)
			r.start = append([]byte(nil), r.before[plan.ABLBA*512:(plan.ABLBA+1)*512]...)
		}, nil, nil, "A/B sector differs from the plan", false)
	})
	t.Run("keeper uboot_b changed after the plan", func(t *testing.T) {
		run(t, func(r *mlRig, p *plan.MainlinePlan) { poke(r, "uboot_b", 1000) }, nil, nil, "uboot_a: wrote", true)
	})
	t.Run("keeper vbmeta_b changed after the plan", func(t *testing.T) {
		run(t, func(r *mlRig, p *plan.MainlinePlan) { poke(r, "vbmeta_b", 4000) }, nil, nil, "vbmeta_b is sha256", true)
	})
	t.Run("keeper resource changed after the plan", func(t *testing.T) {
		run(t, func(r *mlRig, p *plan.MainlinePlan) {
			off := int64(2048 * (1 + 50 + 25)) // the fixture boot image: its resource after 50 kernel and 25 ramdisk pages
			poke(r, "boot_b", off+7)
		}, nil, nil, "the resource of boot_b is sha256", true)
	})
	t.Run("verify pass catches a bad write", func(t *testing.T) {
		r := newMLRig(t, fx, tool, mlUnit, b, ml, res)
		im := r.open()
		defer im.Close()
		p, err := r.makePlan(im)
		if err != nil {
			t.Fatal(err)
		}
		hooks := &install.Hooks{CutAfter: -1, Observe: func(e install.Event) error {
			if e.Op == "data" && e.Partition == "system_a" {
				f, _ := os.OpenFile(r.path, os.O_RDWR, 0)
				f.WriteAt([]byte{0xff, 0xee}, e.Off+1000) // the eMMC "loses" two bytes
				f.Close()
			}
			return nil
		}}
		_, err = install.ExecuteMainline(p, ml, im, install.Options{KeyRecord: mlRecord, Bootctl: tool, Hooks: hooks})
		if err == nil || !strings.Contains(err.Error(), "verify: system_a") {
			t.Fatalf("err = %v", err)
		}
		if got := install.SafeState(im, &p.Plan); !strings.Contains(got, "safe to power off") {
			t.Fatalf("SafeState: %s", got)
		}
	})
	t.Run("keeper check catches a changed keeper", func(t *testing.T) {
		r := newMLRig(t, fx, tool, mlUnit, b, ml, res)
		im := r.open()
		defer im.Close()
		p, err := r.makePlan(im)
		if err != nil {
			t.Fatal(err)
		}
		hooks := &install.Hooks{CutAfter: -1, Observe: func(e install.Event) error {
			if e.Op == "data" && e.Partition == "system_a" {
				poke(r, "oem_b", 77) // a stray byte in a keeper partition
			}
			return nil
		}}
		_, err = install.ExecuteMainline(p, ml, im, install.Options{KeyRecord: mlRecord, Bootctl: tool, Hooks: hooks})
		if err == nil || !strings.Contains(err.Error(), "keeper check: oem_b changed") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("bootctl refuses the arm", func(t *testing.T) {
		ft := fakeTool(t, tool, "arm-refuse")
		r := newMLRig(t, fx, ft, mlUnit, b, ml, res)
		im := r.open()
		defer im.Close()
		p, err := r.makePlan(im)
		if err != nil {
			t.Fatal(err)
		}
		_, err = install.ExecuteMainline(p, ml, im, install.Options{KeyRecord: mlRecord, Bootctl: ft})
		if err == nil || !strings.Contains(err.Error(), "exit 1") || !strings.HasPrefix(err.Error(), fmt.Sprintf("step %d ", len(p.Steps)-1)) {
			t.Fatalf("err = %v", err)
		}
		img, _ := os.ReadFile(r.path)
		if cs, err := install.CheckCutState(bytes.NewReader(img), &p.Plan, r.start); err != nil || cs.Boots != "keeper" || cs.Target != "disabled" {
			t.Fatalf("after a refused arm: %+v %v", cs, err)
		}
	})
	t.Run("bootctl writes a wrong sector", func(t *testing.T) {
		ft := fakeTool(t, tool, "wrong")
		r := newMLRig(t, fx, ft, mlUnitKept, b, ml, res) // no keep-key step: the disable is step 1
		im := r.open()
		defer im.Close()
		p, err := r.makePlan(im)
		if err != nil {
			t.Fatal(err)
		}
		_, err = install.ExecuteMainline(p, ml, im, install.Options{KeyRecord: mlRecord, Bootctl: ft})
		if err == nil || !strings.Contains(err.Error(), "the plan predicts") || !strings.HasPrefix(err.Error(), "step 1 ") {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestMainlineReopenedRootfsChanged: the install run of a plan opens the
// bundle again without a second hash of rootfs.ext4 (mainline.Reopen). A
// root file system that changed after the plan (same size and label) is
// caught while system_a is written: the install stops before the sync, slot
// A stays disabled, the keeper B boots, nothing is armed.
func TestMainlineReopenedRootfsChanged(t *testing.T) {
	tool := realTool(t)
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	b, res := mlBundle(t, fx)
	d, err := b.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	src, _ := mainline.DirSource(d)
	ml, err := mainline.Verify(src)
	if err != nil {
		t.Fatal(err)
	}
	defer ml.Close()
	r := newMLRig(t, fx, tool, mlUnit, b, ml, res)
	im := r.open()
	defer im.Close()
	p, err := r.makePlan(im)
	if err != nil {
		t.Fatal(err)
	}
	c := append([]byte(nil), b.Rootfs...)
	c[len(c)/2] ^= 0x40
	if err := os.WriteFile(d+"/"+mainline.RootfsFile, c, 0o644); err != nil {
		t.Fatal(err)
	}
	re, err := mainline.Reopen(src, ml)
	if err != nil {
		t.Fatal(err)
	}
	defer re.Close()
	j := &journal{}
	_, err = install.ExecuteMainline(p, re, im, install.Options{KeyRecord: mlRecord, Bootctl: tool, Hooks: &install.Hooks{CutAfter: -1, Observe: j.observe}})
	if err == nil || !strings.Contains(err.Error(), "system_a: wrote") {
		t.Fatalf("err = %v", err)
	}
	img, _ := os.ReadFile(r.path)
	cs, err := install.CheckCutState(bytes.NewReader(img), &p.Plan, r.start)
	if err != nil || cs.Boots != "keeper" || cs.Target != "disabled" {
		t.Fatalf("after the stop: %+v %v", cs, err)
	}
	ab := 0
	for _, e := range j.ev {
		if e.Op == "ab" {
			ab++
		}
	}
	if ab != 1 {
		t.Fatalf("%d A/B writes, want only the disable", ab)
	}
	// with the unchanged file the reopened payload installs
	os.WriteFile(d+"/"+mainline.RootfsFile, b.Rootfs, 0o644)
	re2, err := mainline.Reopen(src, ml)
	if err != nil {
		t.Fatal(err)
	}
	defer re2.Close()
	r2 := newMLRig(t, fx, tool, mlUnit, b, ml, res)
	im2 := r2.open()
	defer im2.Close()
	p2, err := r2.makePlan(im2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := install.ExecuteMainline(p2, re2, im2, install.Options{KeyRecord: mlRecord, Bootctl: tool}); err != nil {
		t.Fatal(err)
	}
	r2.checkDone(p2)
}
