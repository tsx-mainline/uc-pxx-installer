package plan_test

// The finish of step 2 (task 4): in B, after B marked itself, F1 compares the
// RKSS record of the step-2 plan, F2 is switch --to a.

import (
	"strings"
	"testing"

	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/plan"
)

func finishWorld(t *testing.T) (*plan.OtherSlotPlan, plan.FinishInput) {
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
	// after step 2: B armed, booted and marked itself: A{14,0,1} B{15,0,1}
	sec := fakeemmc.ABSector([2][3]byte{{14, 0, 1}, {15, 0, 1}}, nil)
	in := plan.FinishInput{Running: plan.ParseRunning(osUBootArgs("b", "orange", "unlocked"), "uc-pxx.id="+osToken, "system_b"),
		Sector: sec, Status: fakeStatus(sec), Step2: p, RKSSNow: map[string]string{}}
	for k, v := range p.RKSS {
		in.RKSSNow[k] = v
	}
	return p, in
}

func TestStep2FinishOK(t *testing.T) {
	p, in := finishWorld(t)
	f, err := plan.ComputeStep2Finish(in)
	if err != nil {
		t.Fatalf("%v %+v", err, f.Checks)
	}
	if f.Switch == nil || strings.Join(f.Switch.Args[:3], " ") != "switch --to a" || f.Switch.Args[3] != "--expect-sector" ||
		f.Switch.After.String() != "A{15,0,1} B{14,0,1}, boots next: A" || !f.VerifyID() || f.Step2ID != p.ID {
		t.Fatalf("finish %+v", f.Switch)
	}
}

func TestStep2FinishRefusals(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(in *plan.FinishInput)
		want string
	}{
		{"B not marked (trying)", func(in *plan.FinishInput) {
			in.Sector = fakeemmc.ABSector([2][3]byte{{14, 0, 1}, {15, 6, 0}}, nil)
			in.Status = fakeStatus(in.Sector)
		}, "b-successful"},
		{"A not successful", func(in *plan.FinishInput) {
			in.Sector = fakeemmc.ABSector([2][3]byte{{0, 0, 0}, {15, 0, 1}}, nil)
			in.Status = fakeStatus(in.Sector)
		}, "a-successful"},
		{"LBA 1024-1279 changed", func(in *plan.FinishInput) { in.RKSSNow["1024-1279"] = strings.Repeat("0", 64) }, "rkss"},
		{"the idbloader range changed", func(in *plan.FinishInput) { in.RKSSNow["64-392"] = strings.Repeat("0", 64) }, "rkss"},
		{"no RKSS record in the step-2 plan", func(in *plan.FinishInput) {
			q := *in.Step2
			q.RKSS = nil
			q.Seal()
			in.Step2 = &q
		}, "rkss"},
		{"running from A", func(in *plan.FinishInput) {
			in.Running = plan.ParseRunning(osUBootArgs("a", "orange", "unlocked"), "uc-pxx.id="+osToken, "system_a")
		}, "running-slot"},
		{"another kernel", func(in *plan.FinishInput) {
			in.Running = plan.ParseRunning(osUBootArgs("b", "orange", "unlocked"), "uc-pxx.id=other", "system_b")
		}, "running-token"},
		{"a RAM boot", func(in *plan.FinishInput) { in.Running = plan.ParseRunning(nil, "uc-pxx.id="+osToken, "system_b") }, "running-slot"},
		{"an edited step-2 plan", func(in *plan.FinishInput) { q := *in.Step2; q.Target = "a"; in.Step2 = &q }, "step2-plan"},
		{"no bootctl status", func(in *plan.FinishInput) { in.Status = nil }, "bootctl-status"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, in := finishWorld(t)
			c.edit(&in)
			f, err := plan.ComputeStep2Finish(in)
			if err == nil || f.Switch != nil {
				t.Fatal("accepted")
			}
			found := false
			for _, ch := range f.Checks {
				found = found || ch.ID == c.want && !ch.OK
			}
			if !found {
				t.Fatalf("want %s: %+v", c.want, f.Checks)
			}
		})
	}
}
