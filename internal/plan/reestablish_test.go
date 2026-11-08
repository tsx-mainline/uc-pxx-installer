package plan_test

// The pre-install "re-establish the keeper" step (KEEPER-REESTABLISH.md): a
// stock unit entered through the BCB `bootonce-bootloader` route reaches the
// installer with a valid, both-slots-trying A/B block (libavb's reset
// A{15,·,0} B{14,·,0}). The plan prepends R(K) (the reviewed repair write) so
// the keeper is successful, then the normal disable/write/arm run. These tests
// check the plan shape, the cardinal rule (the keeper boots at every A/B state
// the plan passes through), and that every gated-out state still refuses.

import (
	"encoding/hex"
	"strings"
	"testing"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/plan"
)

// bothTrying is the libavb reset state the BCB route leaves on a stock unit:
// slot A priority 15 (one try taken booting into fastboot), slot B priority 14,
// neither successful.
var bothTrying = fakeemmc.Unit{AB: [2][3]byte{{15, 6, 0}, {14, 7, 0}}, A: "old", B: "old"}

func ops(p *plan.Plan) string {
	var o []string
	for _, s := range p.Steps {
		o = append(o, s.Op)
	}
	return strings.Join(o, " ")
}

// decode is the 32-byte A/B state of an ABState.
func decode(t *testing.T, s plan.ABState) avbab.AB {
	t.Helper()
	r, err := s.Raw()
	if err != nil {
		t.Fatalf("bad ABState %v: %v", s, err)
	}
	a, _ := avbab.Parse(r[:])
	return a
}

func TestComputeReestablish(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	w := newWorld(t, fx, bothTrying)
	p, err := plan.Compute(w.in)
	if err != nil {
		var sb strings.Builder
		p.Render(&sb)
		t.Fatalf("%v\n%s", err, sb.String())
	}
	if p.Keeper != "a" || p.Target != "b" || !p.VerifyID() {
		t.Fatalf("keeper %s target %s", p.Keeper, p.Target)
	}
	// R(K) prepended: repair, then the normal disable/write/arm.
	want := "bootctl bootctl write write write write write write write write sync verify bootctl check-ab"
	if ops(p) != want {
		t.Fatalf("steps %q", ops(p))
	}
	// step 1 is the repair of the keeper, with the live sector as --expect-sector.
	s0 := p.Steps[0].Bootctl
	live := make([]byte, 512)
	if _, err := w.f.ReadAt(live, plan.ABLBA*512); err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"repair", "--keeper", "a", "--expect-sector", hex.EncodeToString(live)}
	if strings.Join(s0.Args, " ") != strings.Join(wantArgs, " ") {
		t.Fatalf("repair args %q", s0.Args)
	}
	rk := raw([3]byte{15, 0, 1}, [3]byte{})
	if s0.Before.Hex32 != hex.EncodeToString(live[:32]) || s0.After.Hex32 != hex.EncodeToString(rk[:]) {
		t.Fatalf("R(K) before %v after %v", s0.Before, s0.After)
	}
	// step 2 is the disable, and it is a no-op (R(K) already set the target to {0,0,0}).
	d := p.Steps[1].Bootctl
	if d.Args[0] != "disable" || d.Args[2] != "b" || !d.NoOp {
		t.Fatalf("disable step %v no-op %v", d.Args, d.NoOp)
	}
	// the normal end state.
	fin := raw([3]byte{14, 0, 1}, [3]byte{15, 7, 0})
	if p.AB.Final.Hex32 != hex.EncodeToString(fin[:]) {
		t.Fatalf("final %v", p.AB.Final)
	}
	// p.AB.Now is still the real live sector (not R(K)); the executor guards it.
	if p.AB.Now.Hex32 != hex.EncodeToString(live[:32]) {
		t.Fatalf("AB.Now %v, want the live both-trying sector", p.AB.Now)
	}
	if !strings.Contains(p.KeeperWhy, "re-established") {
		t.Fatalf("keeper why %q", p.KeeperWhy)
	}
	// only slot b is ever written.
	for _, s := range p.Steps {
		if s.Write != nil && !strings.HasSuffix(s.Write.Partition, "_b") {
			t.Fatalf("write to %s", s.Write.Partition)
		}
	}
	// the same inputs give the same plan id.
	if p2, err := plan.Compute(w.in); err != nil || p2.ID != p.ID {
		t.Fatal("plan id not stable")
	}

	// The cardinal rule: the keeper (slot a) is bootable and libavb boots the
	// keeper at every A/B state the plan passes through up to the arm, and the
	// target is unbootable from R(K) through the writes (a half-written target
	// is never selectable). Only the final arm makes the target boot next, and
	// it runs after the verify pass.
	k := 0 // keeper a
	seq := []struct {
		name string
		st   plan.ABState
	}{
		{"start (both trying, cut before R(K))", p.Steps[0].Bootctl.Before},
		{"after R(K) (cut during disable/writes)", p.Steps[0].Bootctl.After},
		{"after disable (cut during writes)", p.Steps[1].Bootctl.After},
		{"after arm (final)", p.AB.Final},
	}
	for i, s := range seq {
		a := decode(t, s.st)
		if err := a.Validate(); err != nil {
			t.Fatalf("%s: not valid: %v", s.name, err)
		}
		if !a.Slots[k].Bootable() {
			t.Fatalf("%s: the keeper (slot a) is not bootable: %v", s.name, a.Slots[k])
		}
		if a.Pick() < 0 {
			t.Fatalf("%s: no slot boots", s.name)
		}
		last := i == len(seq)-1
		if !last {
			// up to the arm the keeper boots and the target is unbootable.
			if a.Pick() != k {
				t.Fatalf("%s: libavb would not boot the keeper (picks %s)", s.name, avbab.SlotName(a.Pick()))
			}
			if i >= 1 && a.Slots[1-k].Bootable() {
				t.Fatalf("%s: the target is bootable while it is written", s.name)
			}
		} else {
			// after the arm the target boots next; the keeper stays the bootable fallback.
			if a.Pick() != 1-k || !a.Slots[k].Keeper() {
				t.Fatalf("final: target does not boot next with the keeper as the successful fallback: %v", a)
			}
		}
	}
}

// TestComputeReestablishReverse: the same when slot B is the higher-priority
// (keeper) slot of the reset.
func TestComputeReestablishReverse(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	u := fakeemmc.Unit{AB: [2][3]byte{{14, 7, 0}, {15, 6, 0}}, A: "old", B: "old"}
	p, err := plan.Compute(newWorld(t, fx, u).in)
	if err != nil {
		t.Fatal(err)
	}
	if p.Keeper != "b" || p.Target != "a" || p.Steps[0].Bootctl.Args[2] != "b" {
		t.Fatalf("keeper %s target %s repair %v", p.Keeper, p.Target, p.Steps[0].Bootctl.Args)
	}
	rk := raw([3]byte{}, [3]byte{15, 0, 1})
	fin := raw([3]byte{15, 7, 0}, [3]byte{14, 0, 1})
	if p.Steps[0].Bootctl.After.Hex32 != hex.EncodeToString(rk[:]) || p.AB.Final.Hex32 != hex.EncodeToString(fin[:]) {
		t.Fatalf("R(K) %v final %v", p.Steps[0].Bootctl.After, p.AB.Final)
	}
}

// TestComputeReestablishKeeperFlag: --keeper names the higher-priority slot
// (the one the gate would choose). The install proceeds the same way.
func TestComputeReestablishKeeperFlag(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	w := newWorld(t, fx, bothTrying)
	w.in.Keeper = "a"
	w.in.PreFastboot = nil
	p, err := plan.Compute(w.in)
	if err != nil {
		t.Fatal(err)
	}
	if p.Keeper != "a" || p.Target != "b" || p.Steps[0].Bootctl.Args[0] != "repair" {
		t.Fatalf("keeper %s target %s step0 %v", p.Keeper, p.Target, p.Steps[0].Bootctl.Args)
	}
}

// TestComputeReestablishGatedOut: every state that is not the valid,
// both-trying, distinct-priority start keeps its existing behaviour. No R(K)
// step is ever emitted for them; the refusals stand.
func TestComputeReestablishGatedOut(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	cases := []struct {
		name    string
		u       fakeemmc.Unit
		keeper  string
		noPre   bool
		wantErr string
	}{
		// equal priorities: a tie, not re-established; the existing tie refusal.
		{"both trying, equal priorities (tie)", fakeemmc.Unit{AB: [2][3]byte{{15, 6, 0}, {15, 7, 0}}, A: "old", B: "old"}, "", false, "both slots have priority 15"},
		// one slot already successful (the post-install / stale-arm state): not
		// both-trying, so no R(K); the keeper gate refuses the trying slot.
		{"one trying, one successful", fakeemmc.Unit{AB: [2][3]byte{{14, 0, 1}, {15, 7, 0}}, A: "old", B: "old"}, "", false, "must be successful"},
		// both-trying but the user named the lower-priority slot as keeper: not
		// re-established (the chosen slot is not the libavb pick); refused.
		{"both trying, --keeper the lower slot", bothTrying, "b", false, "keeper chosen is b"},
		// both successful and the pre-fastboot sector is not known: unchanged
		// ("choose the keeper"); this is not a both-trying start.
		{"both successful, pre-fastboot unknown", unit2, "", true, "choose the keeper"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t, fx, c.u)
			w.in.Keeper = c.keeper
			if c.noPre {
				w.in.PreFastboot = nil
			}
			p, err := plan.Compute(w.in)
			if err == nil {
				t.Fatalf("expected a refusal, got a plan (keeper %s target %s)", p.Keeper, p.Target)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("refusal %q, want it to contain %q", err.Error(), c.wantErr)
			}
			if p.Steps != nil {
				t.Fatalf("a refused plan has no steps, got %d", len(p.Steps))
			}
			for _, ch := range p.Checks {
				if ch.ID == "reestablish" {
					t.Fatalf("a gated-out state must emit no reestablish check: %+v", ch)
				}
			}
		})
	}
}
