package plan_test

import (
	"strings"
	"testing"

	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/keykeep"
	"uc-pxx-installer/internal/plan"
)

func TestKeepKeyTrigger(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	open := keykeep.Gate{Open: true, Why: "test", SHA256: strings.Repeat("ab", 32)}
	closed := keykeep.Gate{Why: "checks open", SHA256: strings.Repeat("cd", 32)}
	e, f := keykeep.Empty, keykeep.Foreign
	for _, c := range []struct {
		name    string
		in      plan.KeyKeepInput
		state   string
		keyStep bool
	}{
		{"key only in the target", plan.KeyKeepInput{Gate: open, Copies: []string{"b"}, Classes: [2]keykeep.Class{e, e}}, "keep", true},
		{"key file, no copy (Q5)", plan.KeyKeepInput{Gate: open, Classes: [2]keykeep.Class{e, e}}, "keep", true},
		{"key in the keeper", plan.KeyKeepInput{Gate: open, Copies: []string{"a"}, Classes: [2]keykeep.Class{e, e}}, "not-needed", false},
		{"key in both", plan.KeyKeepInput{Gate: open, Copies: []string{"a", "b"}, Classes: [2]keykeep.Class{e, e}}, "not-needed", false},
		{"kept already", plan.KeyKeepInput{Gate: open, Kept: true, Classes: [2]keykeep.Class{keykeep.Same, keykeep.Same}}, "kept", false},
		{"one copy kept, the other empty (a cut between them)", plan.KeyKeepInput{Gate: open, Kept: true, Classes: [2]keykeep.Class{keykeep.Same, e}}, "keep", true},
		{"one copy kept, the other foreign", plan.KeyKeepInput{Gate: open, Kept: true, Classes: [2]keykeep.Class{f, keykeep.Same}}, "kept", false},
		{"gate closed", plan.KeyKeepInput{Gate: closed, Classes: [2]keykeep.Class{e, e}}, "gated", false},
		{"foreign data (Q4)", plan.KeyKeepInput{Gate: open, Classes: [2]keykeep.Class{f, e}}, "not-possible", false},
		{"another record (Q6)", plan.KeyKeepInput{Gate: open, Classes: [2]keykeep.Class{e, keykeep.Other}}, "not-possible", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t, fx, unit2)
			deviceMode(w)
			in := c.in
			in.RecordSHA256 = strings.Repeat("ee", 32)
			w.in.KeyKeep = &in
			p, err := plan.Compute(w.in)
			if err != nil {
				t.Fatal(err)
			}
			if p.KeyKeep == nil || p.KeyKeep.State != c.state || (p.Steps[0].Op == "keep-key") != c.keyStep {
				t.Fatalf("state %+v, first step %s", p.KeyKeep, p.Steps[0].Op)
			}
			for i, s := range p.Steps {
				if s.N != i+1 {
					t.Fatalf("step %d has N %d", i, s.N)
				}
			}
			if c.keyStep {
				k := p.Steps[0].KeepKey
				if p.Steps[1].Op != "bootctl" || p.Steps[1].Bootctl.Args[0] != "disable" || k.Partition != "backup" {
					t.Fatalf("the keep-key step is not right before the disable: %+v", k)
				}
			}
			if (c.state == "gated" || c.state == "not-possible") != strings.Contains(strings.Join(p.Notes, " "), "The key is not kept on the unit") {
				t.Fatalf("notes %q", p.Notes)
			}
		})
	}
}

func TestCheckKeepKeyRange(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	w := newWorld(t, fx, unit2)
	tb, err := gpt.Parse(w.f, w.in.DiskBytes)
	if err != nil {
		t.Fatal(err)
	}
	e, _ := tb.Find("backup")
	l, _ := keykeep.LBAs(e.Start, e.Sectors())
	for _, lba := range l {
		if err := plan.CheckKeepKeyRange(tb, lba); err != nil {
			t.Fatal(err)
		}
	}
	for _, lba := range []int64{l[0] - 1, l[0] + 1, l[1] - 1, l[1] + 1, e.Start, plan.ABLBA, 64, 0} {
		if err := plan.CheckKeepKeyRange(tb, lba); err == nil {
			t.Fatalf("LBA %d passed", lba)
		}
	}
}
