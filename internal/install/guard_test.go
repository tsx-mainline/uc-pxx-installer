package install_test

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/plan"
)

// TestGuardStops: Options.Guard (the drive rule of the on-screen flow) stops
// the run before any step. Each stop must leave the power-cut rule true, the
// keeper as the next boot, the target never armed, nothing outside the
// target's ranges changed, the error naming the guard, and a resume that
// completes. A guard that passes changes nothing.
func TestGuardStops(t *testing.T) {
	tool := realTool(t)
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	errDrive := errors.New("the drive changed (test)")
	shapes := []struct {
		name string
		unit fakeemmc.Unit
	}{
		{"keeper a 15, target b", unitFwd},
		{"keeper b 15, target a", unitRev},
		{"keeper a 14, target b trust identical", fakeemmc.Unit{AB: [2][3]byte{{14, 0, 1}, {13, 0, 1}}, A: "old", B: "old",
			Tweak: map[string][]byte{"trust_b": fx.New["trust"]}}},
	}
	total := 0
	for _, sh := range shapes {
		t.Run(sh.name, func(t *testing.T) {
			r := newRig(t, fx, tool, sh.unit)
			im := r.open()
			p0, _, err := r.makePlan(im, "")
			im.Close()
			if err != nil {
				t.Fatal(err)
			}
			// the guard passes: the run is the normal one
			r.reset()
			im = r.open()
			p, pl, _ := r.makePlan(im, "")
			var seen []int
			if _, err := install.Execute(p, pl, im, install.Options{Bootctl: tool, MaxWrite: sweepWrite,
				Guard: func(s plan.Step) error { seen = append(seen, s.N); return nil }}); err != nil {
				t.Fatal(err)
			}
			im.Close()
			if len(seen) != len(p0.Steps) {
				t.Fatalf("the guard ran before %d of %d steps", len(seen), len(p0.Steps))
			}
			r.checkDone(p0)
			for _, st := range p0.Steps {
				r.reset()
				im := r.open()
				p, pl, err := r.makePlan(im, "")
				if err != nil || p.ID != p0.ID {
					t.Fatalf("plan: %v", err)
				}
				var log bytes.Buffer
				res, err := install.Execute(p, pl, im, install.Options{Bootctl: tool, MaxWrite: sweepWrite, Log: &log,
					Guard: func(s plan.Step) error {
						if s.N == st.N {
							return errDrive
						}
						return nil
					}})
				im.Close()
				if !errors.Is(err, errDrive) || res == nil || res.Steps != st.N-1 || !strings.Contains(err.Error(), "stopped before the step") {
					t.Fatalf("guard before step %d (%s): err %v, res %+v", st.N, st.Op, err, res)
				}
				if !strings.Contains(log.String(), `"guard":"the drive changed (test)"`) {
					t.Fatalf("step %d: the log has no guard line:\n%s", st.N, log.String())
				}
				cut, _ := os.ReadFile(r.path)
				cs, err := install.CheckCutState(bytes.NewReader(cut), p, r.start)
				if err != nil {
					t.Fatalf("guard before step %d: %v (A/B %s)", st.N, err, cs.AB)
				}
				if sha(masked(cut, p)) != sha(masked(r.before, p)) {
					t.Fatalf("guard before step %d: a byte outside the target's write ranges changed", st.N)
				}
				// before the check-ab step the arm has run: the target is
				// armed and complete. Before every other step: keeper boots.
				if st.Op == "check-ab" {
					if cs.Boots != "target" || !cs.Checked {
						t.Fatalf("guard before check-ab: %+v", cs)
					}
				} else if cs.Boots != "keeper" || strings.Contains(cs.Target, "armed") {
					t.Fatalf("guard before step %d (%s): %+v: the target must not be armed", st.N, st.Op, cs)
				}
				r.resume(p, cs, st.N)
				total++
			}
		})
	}
	t.Logf("guard: %d stops (one before every step of every shape) checked and resumed", total)
}
