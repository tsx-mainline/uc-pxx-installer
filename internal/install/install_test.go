package install_test

// These tests run the real uc-pxx-bootctl. Give its path in UC_PXX_BOOTCTL
// and, for an aarch64 binary on a PC, the runner in UC_PXX_BOOTCTL_EXEC
// (qemu-aarch64-static). Without UC_PXX_BOOTCTL they are skipped.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/bootctl"
	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/plan"
)

func realTool(t *testing.T) bootctl.Tool {
	p := os.Getenv("UC_PXX_BOOTCTL")
	if p == "" {
		t.Skip("UC_PXX_BOOTCTL not set: these tests need the real uc-pxx-bootctl")
	}
	return bootctl.Tool{Path: p, Exec: os.Getenv("UC_PXX_BOOTCTL_EXEC"), TmpDir: t.TempDir()}
}

type rig struct {
	t      *testing.T
	fx     *fakeemmc.Fixture
	tool   bootctl.Tool
	unit   fakeemmc.Unit
	dir    string
	path   string
	before []byte // the whole image before the install
	start  []byte // the A/B sector before the install
	keeper string
}

func newRig(t *testing.T, fx *fakeemmc.Fixture, tool bootctl.Tool, u fakeemmc.Unit) *rig {
	r := &rig{t: t, fx: fx, tool: tool, unit: u, dir: t.TempDir()}
	r.reset()
	return r
}

// reset makes a fresh image of the unit.
func (r *rig) reset() {
	os.Remove(r.path)
	p, err := r.fx.Image(r.dir, "emmc.img", r.unit)
	if err != nil {
		r.t.Fatal(err)
	}
	r.path = p
	r.before, _ = os.ReadFile(p)
	r.start = append([]byte(nil), r.before[plan.ABLBA*512:(plan.ABLBA+1)*512]...)
}

func (r *rig) payload() *plan.Payload {
	pl, err := plan.VerifyOfficial(bytes.NewReader(r.fx.FW), int64(len(r.fx.FW)), r.fx.Keys, r.fx.Manifests, nil)
	if err != nil {
		r.t.Fatal(err)
	}
	return pl
}

// makePlan plans against the image as it is now. pre-fastboot = live sector.
func (r *rig) makePlan(im *install.Image, keeper string) (*plan.Plan, *plan.Payload, error) {
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
	pl := r.payload()
	in := plan.Input{Disk: im.Reader(), DiskBytes: im.Size(), Source: "image:" + r.path, Mode: "dry-run", Tool: "test",
		PreFastboot: sec[:32], Keeper: keeper, Payload: pl, Bootctl: info, BootctlStatus: st, PrereqsConfirmed: true,
		ApprovedResources: []string{r.fx.Resource}}
	if keeper != "" {
		in.PreFastboot = nil
	}
	in.HashKeeper = true
	p, err := plan.Compute(in)
	return p, pl, err
}

func (r *rig) open() *install.Image {
	im, err := install.OpenImage(r.path)
	if err != nil {
		r.t.Fatal(err)
	}
	return im
}

// masked is the image with the target's write ranges and the A/B sector
// set to zero: the bytes that no step may change.
func masked(img []byte, p *plan.Plan) []byte {
	m := append([]byte(nil), img...)
	for _, s := range p.Steps {
		if s.Write != nil {
			o := s.Write.StartLBA*512 + s.Write.Offset
			copy(m[o:o+s.Write.Length], make([]byte, s.Write.Length))
		}
	}
	copy(m[plan.ABLBA*512:(plan.ABLBA+1)*512], make([]byte, 512))
	return m
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

type journal struct {
	ev []install.Event
}

func (j *journal) observe(e install.Event) error { j.ev = append(j.ev, e); return nil }

// checkDone checks a finished install: the A/B state, the target contents,
// and that nothing else changed.
func (r *rig) checkDone(p *plan.Plan) {
	t := r.t
	after, err := os.ReadFile(r.path)
	if err != nil {
		t.Fatal(err)
	}
	if sha(masked(after, p)) != sha(masked(r.before, p)) {
		t.Fatal("a byte outside the target's write ranges and the A/B sector changed")
	}
	a, _ := avbab.Parse(after[plan.ABLBA*512:])
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	want, _ := p.AB.Final.Raw()
	if !bytes.Equal(after[plan.ABLBA*512:plan.ABLBA*512+32], want[:]) {
		t.Fatalf("final A/B %x, want %x", after[plan.ABLBA*512:plan.ABLBA*512+32], want)
	}
	if !bytes.Equal(after[plan.ABLBA*512+32:(plan.ABLBA+1)*512], r.start[32:]) {
		t.Fatal("A/B bytes 32..511 changed")
	}
	tb, _ := gpt.Parse(bytes.NewReader(after), int64(len(after)))
	for _, part := range plan.SlotParts {
		e, _ := tb.Find(part + "_" + p.Target)
		img := r.fx.New[part]
		if !bytes.Equal(after[e.Start*512:e.Start*512+int64(len(img))], img) {
			t.Fatalf("%s does not hold the payload image", e.Name)
		}
	}
	cs, err := install.CheckCutState(bytes.NewReader(after), p, r.start)
	if err != nil || cs.Boots != "target" || !cs.Checked {
		t.Fatalf("final state %+v: %v", cs, err)
	}
}

var (
	unitFwd = fakeemmc.Unit{AB: [2][3]byte{{15, 0, 1}, {14, 0, 1}}, A: "old", B: "old"}
	unitRev = fakeemmc.Unit{AB: [2][3]byte{{14, 0, 1}, {15, 0, 1}}, A: "old", B: "old"}
)

func TestDryRunInstall(t *testing.T) {
	tool := realTool(t)
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	for _, c := range []struct {
		name   string
		unit   fakeemmc.Unit
		target string
		final  string
	}{
		{"keeper a prio 15, target b", unitFwd, "b", "A{14,0,1} B{15,7,0}, boots next: B"},
		{"keeper b prio 15, target a", unitRev, "a", "A{15,7,0} B{14,0,1}, boots next: A"},
		{"keeper a prio 14, target b disabled", fakeemmc.Unit{AB: [2][3]byte{{14, 0, 1}, {0, 0, 0}}, A: "old"}, "b",
			"A{14,0,1} B{15,7,0}, boots next: B"},
	} {
		t.Run(c.name, func(t *testing.T) {
			u := c.unit
			if u.B == "" { // the target holds nothing readable: give it an old vbmeta
				u.Tweak = map[string][]byte{"vbmeta_b": fx.Old["vbmeta"]}
			}
			r := newRig(t, fx, tool, u)
			im := r.open()
			defer im.Close()
			p, pl, err := r.makePlan(im, "")
			if err != nil {
				var sb strings.Builder
				p.Render(&sb)
				t.Fatalf("%v\n%s", err, sb.String())
			}
			if p.Target != c.target || p.AB.Final.String() != c.final {
				t.Fatalf("target %s final %s", p.Target, p.AB.Final)
			}
			j := &journal{}
			var log bytes.Buffer
			res, err := install.Execute(p, pl, im, install.Options{Bootctl: tool, Log: &log, Hooks: &install.Hooks{CutAfter: -1, Observe: j.observe}})
			if err != nil {
				t.Fatal(err)
			}
			if res.Steps != len(p.Steps) {
				t.Fatalf("%d steps done", res.Steps)
			}
			r.checkDone(p)
			// the journal: A/B writes first and last, data only in between
			var abAt []int
			for i, e := range j.ev {
				if e.Op == "ab" {
					abAt = append(abAt, i)
				}
			}
			wantAB := 2
			if p.Steps[0].Bootctl.NoOp {
				wantAB = 1
			}
			if len(abAt) != wantAB || abAt[len(abAt)-1] != len(j.ev)-1 || (wantAB == 2 && abAt[0] != 0) {
				t.Fatalf("A/B writes at %v of %d writes", abAt, len(j.ev))
			}
			for _, line := range strings.Split(strings.TrimSpace(log.String()), "\n") {
				var m map[string]any
				if err := json.Unmarshal([]byte(line), &m); err != nil {
					t.Fatalf("log line %q: %v", line, err)
				}
			}
		})
	}
}

// TestPowerCutSweep is the key safety test. For each unit shape it cuts the
// install before every primitive write (and once more with a torn write),
// exactly as a power loss would: no clean-up. After each cut it checks the
// image with CheckCutState (package avbab decodes the A/B sector), and that
// nothing outside the target's write ranges changed. Then it runs the
// installer again on the cut image, as a user would, and checks that the
// install completes, or that a new plan is refused when the target already
// boots next.
// sweepWrite: 32 KiB writes, so the small fixture has about 120 write
// boundaries per install.
const sweepWrite = 32 << 10

func TestPowerCutSweep(t *testing.T) {
	tool := realTool(t)
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	shapes := []struct {
		name string
		unit fakeemmc.Unit
	}{
		{"keeper a 15, target b", unitFwd},
		{"keeper b 15, target a", unitRev},
		{"keeper a 14, target b trust identical", fakeemmc.Unit{AB: [2][3]byte{{14, 0, 1}, {13, 0, 1}}, A: "old", B: "old",
			Tweak: map[string][]byte{"trust_b": fx.New["trust"]}}},
		{"keeper b 15, target a trying", fakeemmc.Unit{AB: [2][3]byte{{3, 5, 0}, {15, 0, 1}}, A: "old", B: "old"}},
	}
	total := 0
	for _, sh := range shapes {
		t.Run(sh.name, func(t *testing.T) {
			r := newRig(t, fx, tool, sh.unit)
			// a full run counts the primitive writes
			im := r.open()
			p0, pl, err := r.makePlan(im, "")
			if err != nil {
				t.Fatal(err)
			}
			j := &journal{}
			if _, err := install.Execute(p0, pl, im, install.Options{Bootctl: tool, MaxWrite: sweepWrite,
				Hooks: &install.Hooks{CutAfter: -1, Observe: j.observe}}); err != nil {
				t.Fatal(err)
			}
			im.Close()
			n := len(j.ev)
			for _, torn := range []bool{false, true} {
				for k := 0; k <= n; k++ {
					r.reset()
					im := r.open()
					p, pl, err := r.makePlan(im, "")
					if err != nil {
						t.Fatal(err)
					}
					if p.ID != p0.ID {
						t.Fatal("the plan of a fresh image differs")
					}
					_, err = install.Execute(p, pl, im, install.Options{Bootctl: tool, MaxWrite: sweepWrite,
						Hooks: &install.Hooks{CutAfter: k, Torn: torn}})
					if k < n && !errors.Is(err, install.ErrPowerCut) {
						t.Fatalf("cut %d: err %v, want a power cut", k, err)
					}
					if k == n && err != nil {
						t.Fatalf("cut %d (no cut): %v", k, err)
					}
					im.Close()
					cut, _ := os.ReadFile(r.path)
					cs, err := install.CheckCutState(bytes.NewReader(cut), p, r.start)
					if err != nil {
						t.Fatalf("cut before write %d (torn %v): %v (A/B %s)", k+1, torn, err, cs.AB)
					}
					if sha(masked(cut, p)) != sha(masked(r.before, p)) {
						t.Fatalf("cut %d: a byte outside the target's write ranges changed", k)
					}
					if k == n && (cs.Boots != "target" || !cs.Checked) {
						t.Fatalf("complete run: %+v", cs)
					}
					if k < n && cs.Boots != "keeper" {
						t.Fatalf("cut %d: %+v: only the last write may make the target the next boot", k, cs)
					}
					r.resume(p, cs, k)
					total++
				}
			}
		})
	}
	t.Logf("power-cut sweep: %d cut points checked (A/B decoded with internal/avbab), each one resumed", total)
}

// resume runs the installer again on a cut image.
func (r *rig) resume(p *plan.Plan, cs install.CutState, k int) {
	t := r.t
	im := r.open()
	defer im.Close()
	p2, pl, err := r.makePlan(im, "")
	if cs.Boots == "target" {
		if err == nil {
			t.Fatalf("cut %d: the target boots next, but a new plan was accepted", k)
		}
		return
	}
	if err != nil {
		var sb strings.Builder
		p2.Render(&sb)
		t.Fatalf("cut %d: resume refused: %v\n%s", k, err, sb.String())
	}
	if p2.Keeper != p.Keeper || p2.AB.Final != p.AB.Final {
		t.Fatalf("cut %d: resume plan keeper %s final %s", k, p2.Keeper, p2.AB.Final)
	}
	if _, err := install.Execute(p2, pl, im, install.Options{Bootctl: r.tool}); err != nil {
		t.Fatalf("cut %d: resume: %v", k, err)
	}
	r.checkDone(p)
}

// fakeTool is a script that runs the real tool for status, and does what
// mode says for the write commands.
func fakeTool(t *testing.T, real bootctl.Tool, mode string) bootctl.Tool {
	runner := real.Path
	if real.Exec != "" {
		runner = real.Exec + " " + real.Path
	}
	var body string
	switch mode {
	case "refuse":
		body = `echo "uc-pxx-bootctl: refusing (test)" >&2; exit 1`
	case "unknown":
		body = `echo "uc-pxx-bootctl: !!! ON-DISK A/B STATE UNKNOWN (test)" >&2; exit 3`
	case "arm-refuse":
		body = `case "$1" in arm-trying) echo "uc-pxx-bootctl: refusing (test)" >&2; exit 1;; esac; exec ` + runner + ` "$@"`
	case "wrong":
		// does the command, then also clears the keeper's successful flag:
		// what a broken bootctl might write
		body = runner + ` "$@" || exit $?
for a in "$@"; do f="$a"; done
printf '\000' | dd of="$f" bs=1 seek=10 conv=notrunc 2>/dev/null
printf '\000' | dd of="$f" bs=1 seek=14 conv=notrunc 2>/dev/null
exit 0`
	case "tail":
		body = runner + ` "$@" || exit $?
for a in "$@"; do f="$a"; done
printf 'X' | dd of="$f" bs=1 seek=300 conv=notrunc 2>/dev/null
exit 0`
	}
	script := "#!/bin/sh\ncase \"$1\" in status) exec " + runner + " \"$@\";; esac\n" + body + "\n"
	p := filepath.Join(t.TempDir(), "bootctl-"+mode)
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bootctl.Tool{Path: p, TmpDir: t.TempDir()}
}

func TestBootctlMisbehaves(t *testing.T) {
	real := realTool(t)
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	for _, c := range []struct {
		mode, want string
		step       int // the step that must stop
		data       bool
	}{
		{"refuse", "exit 1", 1, false},
		{"unknown", "UNKNOWN", 1, false},
		{"wrong", "the plan predicts", 1, false},
		{"tail", "bytes 32..511", 1, false},
		{"arm-refuse", "exit 1", 12, true},
	} {
		t.Run(c.mode, func(t *testing.T) {
			tool := fakeTool(t, real, c.mode)
			r := newRig(t, fx, tool, unitFwd)
			im := r.open()
			defer im.Close()
			p, pl, err := r.makePlan(im, "")
			if err != nil {
				t.Fatal(err)
			}
			j := &journal{}
			_, err = install.Execute(p, pl, im, install.Options{Bootctl: tool, Hooks: &install.Hooks{CutAfter: -1, Observe: j.observe}})
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.HasPrefix(err.Error(), fmt.Sprintf("step %d ", c.step)) {
				t.Fatalf("err = %v", err)
			}
			img, _ := os.ReadFile(r.path)
			cs, err := install.CheckCutState(bytes.NewReader(img), p, r.start)
			if err != nil || cs.Boots != "keeper" {
				t.Fatalf("after the stop: %+v %v", cs, err)
			}
			if !c.data && len(j.ev) != 0 {
				t.Fatalf("%d writes after a refused first step", len(j.ev))
			}
			if c.data && cs.Target != "disabled" {
				t.Fatalf("after a refused arm the target is %s", cs.Target)
			}
			if got := install.SafeState(im, p); c.data && !strings.Contains(got, "safe to power off") {
				t.Fatalf("SafeState: %s", got)
			}
		})
	}
}

func TestExecuteGuards(t *testing.T) {
	tool := realTool(t)
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	run := func(t *testing.T, change func(r *rig, p *plan.Plan, im *install.Image), hooks *install.Hooks, want string) *rig {
		r := newRig(t, fx, tool, unitFwd)
		im := r.open()
		defer im.Close()
		p, pl, err := r.makePlan(im, "")
		if err != nil {
			t.Fatal(err)
		}
		if change != nil {
			change(r, p, im)
		}
		if hooks == nil {
			hooks = &install.Hooks{CutAfter: -1}
		}
		_, err = install.Execute(p, pl, im, install.Options{Bootctl: tool, Hooks: hooks})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want %q", err, want)
		}
		img, _ := os.ReadFile(r.path)
		if cs, err := install.CheckCutState(bytes.NewReader(img), p, r.start); err != nil || cs.Boots != "keeper" {
			t.Fatalf("after the stop: %+v %v", cs, err)
		}
		if sha(masked(img, p)) != sha(masked(r.before, p)) {
			t.Fatal("a byte outside the target's write ranges changed")
		}
		return r
	}
	t.Run("A/B changed after the plan", func(t *testing.T) {
		run(t, func(r *rig, p *plan.Plan, im *install.Image) {
			f, _ := os.OpenFile(r.path, os.O_RDWR, 0)
			f.WriteAt([]byte{0x55}, plan.ABLBA*512+400)
			f.Close()
			// the checks after the stop start from the changed image
			r.before, _ = os.ReadFile(r.path)
			r.start = append([]byte(nil), r.before[plan.ABLBA*512:(plan.ABLBA+1)*512]...)
		}, nil, "A/B sector differs from the plan")
	})
	t.Run("edited plan", func(t *testing.T) {
		run(t, func(r *rig, p *plan.Plan, im *install.Image) { p.Steps[1].Write.Partition = "uboot_a" }, nil, "no valid id")
	})
	t.Run("sealed plan that writes the keeper", func(t *testing.T) {
		run(t, func(r *rig, p *plan.Plan, im *install.Image) {
			p.Steps[1].Write.Partition = "uboot_a"
			p.Seal()
		}, nil, "the plan says uboot_a")
	})
	t.Run("sealed plan with a range outside the partition", func(t *testing.T) {
		run(t, func(r *rig, p *plan.Plan, im *install.Image) {
			for _, s := range p.Steps {
				if s.Write != nil && s.Write.Partition == "dtbo_b" {
					s.Write.Offset = 128 << 10 // pushes the 64 KiB image past the 128 KiB partition end
				}
			}
			p.Seal()
		}, nil, "write refused")
	})
	t.Run("verify pass catches a bad write", func(t *testing.T) {
		r := newRig(t, fx, tool, unitFwd)
		hooks := &install.Hooks{CutAfter: -1, Observe: func(e install.Event) error {
			if e.Op == "data" && e.Partition == "system_b" {
				f, _ := os.OpenFile(r.path, os.O_RDWR, 0)
				f.WriteAt([]byte{0xff, 0xee}, e.Off+1000) // the eMMC "loses" two bytes
				f.Close()
			}
			return nil
		}}
		im := r.open()
		defer im.Close()
		p, pl, err := r.makePlan(im, "")
		if err != nil {
			t.Fatal(err)
		}
		_, err = install.Execute(p, pl, im, install.Options{Bootctl: tool, Hooks: hooks})
		if err == nil || !strings.Contains(err.Error(), "verify: system_b") {
			t.Fatalf("err = %v", err)
		}
		if got := install.SafeState(im, p); !strings.Contains(got, "safe to power off") {
			t.Fatalf("SafeState: %s", got)
		}
	})
	t.Run("keeper check catches a changed keeper", func(t *testing.T) {
		r := newRig(t, fx, tool, unitFwd)
		im := r.open()
		defer im.Close()
		p, pl, err := r.makePlan(im, "")
		if err != nil {
			t.Fatal(err)
		}
		e := p.Steps[1].Write // uboot_b; the keeper's uboot_a is 8192 sectors before it
		hooks := &install.Hooks{CutAfter: -1, Observe: func(ev install.Event) error {
			if ev.Op == "data" && ev.Partition == "oem_b" {
				f, _ := os.OpenFile(r.path, os.O_RDWR, 0)
				f.WriteAt([]byte{0x42}, (e.StartLBA-8192)*512+77) // a stray byte in uboot_a
				f.Close()
			}
			return nil
		}}
		_, err = install.Execute(p, pl, im, install.Options{Bootctl: tool, Hooks: hooks})
		if err == nil || !strings.Contains(err.Error(), "keeper check: uboot_a changed") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("not an image file", func(t *testing.T) {
		for _, p := range []string{"/dev/null", "/proc/self/status"} {
			if _, err := install.OpenImage(p); err == nil {
				t.Fatalf("%s opened", p)
			}
		}
		d := t.TempDir()
		target := filepath.Join(d, "x.img")
		os.WriteFile(target, make([]byte, 4096), 0o644)
		link := filepath.Join(d, "link.img")
		os.Symlink(target, link)
		if _, err := install.OpenImage(link); err == nil {
			t.Fatal("symlink opened")
		}
	})
}

// TestSweepCatchesBadPlans shows that the power-cut check has teeth: a plan
// without the disable step, and a plan that arms the target before writing
// it, are both caught at some cut point.
func TestSweepCatchesBadPlans(t *testing.T) {
	tool := realTool(t)
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	for _, c := range []struct {
		name string
		mut  func(p *plan.Plan)
		want string
	}{
		{"no disable", func(p *plan.Plan) {
			p.Steps = p.Steps[1:]
		}, "partly written"},
		{"arm first", func(p *plan.Plan) {
			n := len(p.Steps)
			arm, check := p.Steps[n-2], p.Steps[n-1]
			arm.Bootctl.Before = p.Steps[0].Bootctl.After
			steps := append([]plan.Step{p.Steps[0], arm}, p.Steps[1:n-2]...)
			p.Steps = append(steps, check)
		}, "armed but does not hold the payload"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, fx, tool, unitFwd)
			caught := ""
			for k := 0; k < 200 && caught == ""; k++ {
				r.reset()
				im := r.open()
				p, pl, err := r.makePlan(im, "")
				if err != nil {
					t.Fatal(err)
				}
				c.mut(p)
				p.Seal()
				_, err = install.Execute(p, pl, im, install.Options{Bootctl: tool, MaxWrite: sweepWrite,
					Hooks: &install.Hooks{CutAfter: k}})
				im.Close()
				cut, _ := os.ReadFile(r.path)
				if _, cerr := install.CheckCutState(bytes.NewReader(cut), p, r.start); cerr != nil {
					caught = fmt.Sprintf("cut %d: %v", k, cerr)
				}
				if err == nil {
					break
				}
			}
			if !strings.Contains(caught, c.want) {
				t.Fatalf("the bad plan was not caught (%q)", caught)
			}
			t.Logf("caught: %s", caught)
		})
	}
}
