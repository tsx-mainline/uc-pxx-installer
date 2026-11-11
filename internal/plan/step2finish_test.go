package plan_test

// The finish of step 2 (task 4; reviews T4, T4-2): in T, after T booted, F1
// compares the RKSS record, the keeper's static parts, the layout and the
// kept key (read by the planner from the disk: T4-2 NIT-6) on every boot,
// marked or not; F2 is switch --to a. The result is switch, done, not-yet or
// STOP (T4-2 MINOR-1).

import (
	"errors"
	"os"
	"strings"
	"testing"

	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/keykeep"
	"uc-pxx-installer/internal/plan"
)

// finishWorld: the step-2 plan of K = a from S0, and the finish input in B
// after B armed, booted and marked itself: A{14,0,1} B{15,0,1}.
func finishWorld(t *testing.T) (*plan.OtherSlotPlan, plan.FinishInput, string) {
	t.Helper()
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	w := newOSWorld(t, fx, osUnit(t, fx, osStep2, "a", "new", false), "a")
	p, err := plan.ComputeOtherSlot(w.in)
	if err != nil {
		t.Fatalf("%v\n%s", err, osRender(p))
	}
	if len(p.RKSS) != 4 {
		t.Fatalf("the step-2 plan records %d RKSS ranges", len(p.RKSS))
	}
	in := plan.FinishInput{Running: plan.ParseRunning(osUBootArgs("b", "orange", "unlocked"), "uc-pxx.id="+osToken, "system_b"), Step2: p,
		KeyPins: w.in.KeyPins, KeyPinSet: w.in.KeyPinSet}
	in.Running.BootID = "boot-2"
	plan.AllowDryRunFinish(&in)
	setSector(t, &in, w.path, [2][3]byte{{14, 0, 1}, {15, 0, 1}})
	return p, in, w.path
}

// setSector writes the A/B state into the image (the tail kept) and gives
// the finish the image and that sector.
func setSector(t *testing.T, in *plan.FinishInput, path string, s [2][3]byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	cur := make([]byte, 512)
	f.ReadAt(cur, plan.ABLBA*512)
	sec := fakeemmc.ABSector(s, cur[32:])
	if _, err := f.WriteAt(sec, plan.ABLBA*512); err != nil {
		t.Fatal(err)
	}
	st, _ := f.Stat()
	in.Disk, in.DiskBytes, in.Sector, in.Status = f, st.Size(), sec, fakeStatus(sec)
	t.Cleanup(func() { f.Close() })
}

func poke(t *testing.T, path string, off int64, b []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteAt(b, off); err != nil {
		t.Fatal(err)
	}
}

func partStart(t *testing.T, path, name string) int64 {
	t.Helper()
	f, _ := os.Open(path)
	defer f.Close()
	st, _ := f.Stat()
	tb, err := gpt.Parse(f, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	e, _ := tb.Find(name)
	return e.Start * 512
}

func TestStep2FinishOK(t *testing.T) {
	p, in, _ := finishWorld(t)
	f, err := plan.ComputeStep2Finish(in)
	if err != nil {
		t.Fatalf("%v %+v", err, f.Checks)
	}
	if f.Result != plan.FinishSwitch || f.Switch == nil || strings.Join(f.Switch.Args[:3], " ") != "switch --to a" || f.Switch.Args[3] != "--expect-sector" ||
		f.Switch.After.String() != "A{15,0,1} B{14,0,1}, boots next: A" || !f.VerifyID() || f.Step2ID != p.ID {
		t.Fatalf("finish %+v", f.Switch)
	}
}

func TestStep2FinishResults(t *testing.T) {
	for _, c := range []struct {
		name   string
		edit   func(t *testing.T, in *plan.FinishInput, path string)
		result string // "": a plain refusal
		check  string
	}{
		{"B not marked yet (trying, 6 tries left): not yet", func(t *testing.T, in *plan.FinishInput, path string) {
			setSector(t, in, path, [2][3]byte{{14, 0, 1}, {15, 6, 0}})
		}, plan.FinishNotYet, "b-successful"},
		{"A not successful: STOP", func(t *testing.T, in *plan.FinishInput, path string) {
			setSector(t, in, path, [2][3]byte{{0, 0, 0}, {15, 0, 1}})
		}, plan.FinishStop, "a-successful"},
		{"LBA 1024-1279 changed: STOP", func(t *testing.T, in *plan.FinishInput, path string) {
			poke(t, path, 1100*512, []byte{0x55, 0x66, 0x77, 0x88})
		}, plan.FinishStop, "rkss"},
		{"LBA 1024 changed while B is not marked: STOP, not 'not yet' (T4-2 MINOR-1)", func(t *testing.T, in *plan.FinishInput, path string) {
			setSector(t, in, path, [2][3]byte{{14, 0, 1}, {15, 6, 0}})
			poke(t, path, 1024*512, []byte{0x88, 0x77, 0x66, 0x55})
		}, plan.FinishStop, "rkss"},
		{"the idbloader range changed: STOP", func(t *testing.T, in *plan.FinishInput, path string) {
			poke(t, path, 100*512, []byte{1, 2, 3, 4})
		}, plan.FinishStop, "rkss"},
		{"no RKSS record in the step-2 plan: STOP", func(t *testing.T, in *plan.FinishInput, path string) {
			q := *in.Step2
			q.RKSS = nil
			q.Seal()
			in.Step2 = &q
		}, plan.FinishStop, "rkss"},
		{"the keeper's boot_a changed: STOP", func(t *testing.T, in *plan.FinishInput, path string) {
			poke(t, path, partStart(t, path, "boot_a")+4096, []byte{9, 9, 9, 9})
		}, plan.FinishStop, "keeper-unchanged"},
		{"a key record zeroed: STOP", func(t *testing.T, in *plan.FinishInput, path string) {
			poke(t, path, partStart(t, path, "backup")+(4096-keykeep.FromEnd2)*512, make([]byte, 512))
		}, plan.FinishStop, "key-kept"},
		{"another key pin set: STOP", func(t *testing.T, in *plan.FinishInput, path string) { in.KeyPins, in.KeyPinSet = nil, "" }, plan.FinishStop, "key-kept"},
		{"A{13,0,1}: not the armed state: STOP (T4 MINOR-2)", func(t *testing.T, in *plan.FinishInput, path string) {
			setSector(t, in, path, [2][3]byte{{13, 0, 1}, {15, 0, 1}})
		}, plan.FinishStop, "ab-state"},
		{"the sector given is not the disk's: STOP", func(t *testing.T, in *plan.FinishInput, path string) {
			in.Sector = fakeemmc.ABSector([2][3]byte{{14, 0, 1}, {15, 0, 1}}, nil)
		}, plan.FinishStop, "ab-read"},
		{"no bootctl status: STOP", func(t *testing.T, in *plan.FinishInput, path string) { in.Status = nil }, plan.FinishStop, "bootctl-status"},
		{"running from A", func(t *testing.T, in *plan.FinishInput, path string) {
			in.Running = plan.ParseRunning(osUBootArgs("a", "orange", "unlocked"), "uc-pxx.id="+osToken, "system_a")
			in.Running.BootID = "x"
		}, "", "running-slot"},
		{"another kernel", func(t *testing.T, in *plan.FinishInput, path string) {
			in.Running = plan.ParseRunning(osUBootArgs("b", "orange", "unlocked"), "uc-pxx.id=other", "system_b")
			in.Running.BootID = "x"
		}, "", "running-token"},
		{"a RAM boot", func(t *testing.T, in *plan.FinishInput, path string) {
			in.Running = plan.ParseRunning(nil, "uc-pxx.id="+osToken, "system_b")
		}, "", "running-slot"},
		{"no boot id", func(t *testing.T, in *plan.FinishInput, path string) { in.Running.BootID = "" }, "", "running-boot-id"},
		{"an edited step-2 plan", func(t *testing.T, in *plan.FinishInput, path string) { q := *in.Step2; q.Target = "a"; in.Step2 = &q }, "", "step2-plan"},
		{"a dry-run step-2 plan without the test switch (T4 MINOR-2)", func(t *testing.T, in *plan.FinishInput, path string) {
			*in = plan.FinishInput{Running: in.Running, Disk: in.Disk, DiskBytes: in.DiskBytes, Sector: in.Sector, Status: in.Status, Step2: in.Step2,
				KeyPins: in.KeyPins, KeyPinSet: in.KeyPinSet}
		}, "", "step2-plan"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, in, path := finishWorld(t)
			c.edit(t, &in, path)
			f, err := plan.ComputeStep2Finish(in)
			if err == nil || f.Switch != nil || f.Done || !errors.Is(err, plan.ErrRefused) {
				t.Fatalf("accepted: %v", err)
			}
			if f.Result != c.result || errors.Is(err, plan.ErrFinishStop) != (c.result == plan.FinishStop) ||
				errors.Is(err, plan.ErrFinishNotYet) != (c.result == plan.FinishNotYet) {
				t.Fatalf("result %q (%v), want %q", f.Result, err, c.result)
			}
			found := false
			for _, ch := range f.Checks {
				found = found || ch.ID == c.check && !ch.OK
			}
			if !found {
				t.Fatalf("want %s: %+v", c.check, f.Checks)
			}
		})
	}
}

// The direct path's step 2 (K = b, T = a): the finish in A is the read-only
// F1 check after the first boot of the userdebug U-Boot in A; no switch
// (review T4 MAJOR-1); before A marked itself it is not yet, or a STOP when
// LBA 1024 changed (T4-2 MINOR-1).
func TestStep2FinishKeeperB(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	w := newOSWorld(t, fx, osUnit(t, fx, [2][3]byte{{14, 0, 1}, {15, 0, 1}}, "b", "old", false), "b")
	p, err := plan.ComputeOtherSlot(w.in)
	if err != nil {
		t.Fatalf("%v\n%s", err, osRender(p))
	}
	in := plan.FinishInput{Running: plan.ParseRunning(osUBootArgs("a", "orange", "unlocked"), "uc-pxx.id="+osToken, "system_a"), Step2: p,
		KeyPins: w.in.KeyPins, KeyPinSet: w.in.KeyPinSet}
	in.Running.BootID = "boot-2"
	plan.AllowDryRunFinish(&in)
	setSector(t, &in, w.path, [2][3]byte{{15, 6, 0}, {14, 0, 1}}) // A's first boot, not marked
	if f, err := plan.ComputeStep2Finish(in); !errors.Is(err, plan.ErrFinishNotYet) || f.Result != plan.FinishNotYet {
		t.Fatalf("K = b before the mark: %v %q", err, f.Result)
	}
	setSector(t, &in, w.path, [2][3]byte{{15, 0, 1}, {14, 0, 1}}) // A armed, booted, marked; B the fallback
	f, err := plan.ComputeStep2Finish(in)
	if err != nil || f.Switch != nil || !f.Done || f.Result != plan.FinishDone {
		t.Fatalf("K = b finish: %v %+v", err, f.Switch)
	}
	setSector(t, &in, w.path, [2][3]byte{{15, 6, 0}, {14, 0, 1}})
	poke(t, w.path, 1024*512, []byte{0x55, 0x66, 0x77, 0x88})
	if f, err := plan.ComputeStep2Finish(in); !errors.Is(err, plan.ErrFinishStop) || f.Result != plan.FinishStop {
		t.Fatalf("K = b with LBA 1024 changed, not marked: %v %q", err, f.Result)
	}
}

// A finish that switched already (A{15,0,1} B{14,0,1}, run again): done,
// nothing to write.
func TestStep2FinishAlreadySwitched(t *testing.T) {
	_, in, path := finishWorld(t)
	setSector(t, &in, path, [2][3]byte{{15, 0, 1}, {14, 0, 1}})
	f, err := plan.ComputeStep2Finish(in)
	if err != nil || f.Switch != nil || !f.Done {
		t.Fatalf("already switched: %v %+v", err, f)
	}
}

// The RKSS magic at LBA 1024 stops a plan that writes a different U-Boot
// (review T4 MAJOR-1); a plan that skips an equal U-Boot does not need it.
// Both byte orders of the magic (review T4-2 NIT-2).
func TestOtherSlotRKSSMagic(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	for _, c := range []struct {
		other string
		magic []byte
		ok    bool
	}{{"old", []byte{0x88, 0x77, 0x66, 0x55}, false}, {"old", []byte{0x55, 0x66, 0x77, 0x88}, false}, {"new", []byte{0x88, 0x77, 0x66, 0x55}, true},
		{"old", []byte{0x88, 0x77, 0x66, 0x54}, true}} {
		w := newOSWorld(t, fx, osUnit(t, fx, osStep2, "a", c.other, false), "a")
		poke(t, w.path, 1024*512+16, c.magic)
		p, err := plan.ComputeOtherSlot(w.in)
		if (err == nil) != c.ok {
			t.Fatalf("target %s with %x at LBA 1024: %v\n%s", c.other, c.magic, err, osRender(p))
		}
	}
}
