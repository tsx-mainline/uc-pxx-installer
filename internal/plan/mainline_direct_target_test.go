package plan_test

import (
	"strings"
	"testing"

	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/plan"
)

// TestDirectTargetNow (owner F1; DIRECT-DRYRUN-UNIT3 F3): the direct plan
// records what slot B holds now, by the five images of one known manifest
// (the keeper's rule), or "unknown"; the check target-now names it; the note
// on the first start of slot B follows what uboot_b holds now.
func TestDirectTargetNow(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	for _, c := range []struct {
		name          string
		b             string
		known, ubSame bool
	}{
		{"B holds the firmware of the stash (a userdebug B today)", "new", true, true},
		{"B holds images of no manifest", "old", false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			u := dUnitReset
			u.B = c.b
			w := newDWorld(t, fx, u)
			p, err := plan.ComputeMainlineDirect(w.in)
			if err != nil {
				t.Fatalf("%v\n%s", err, dRender(p))
			}
			tn := p.TargetNow
			if tn == nil || tn.Slot != "b" || tn.UBootSame != c.ubSame {
				t.Fatalf("target_now %+v", tn)
			}
			// the fixture's "old" images may or may not be a manifest's: the text follows Known
			if c.known && (!tn.Known || !strings.Contains(tn.Text, "slot B holds the Crestron firmware "+tn.Firmware)) ||
				!tn.Known && (tn.Why == "" || !strings.Contains(tn.Text, "does not know")) {
				t.Fatalf("target_now %+v", tn)
			}
			found := false
			for _, ch := range p.Checks {
				found = found || ch.ID == plan.TargetNowCheck && ch.OK && strings.Contains(ch.Detail, tn.Text) && strings.Contains(ch.Detail, plan.ReplaceFlag)
			}
			if !found {
				t.Fatalf("no target-now check:\n%s", dRender(p))
			}
			note := strings.Join(p.Notes, "\n")
			if c.ubSame != strings.Contains(note, "holds the stash's userdebug U-Boot already") || c.ubSame == strings.Contains(note, "for the first time") {
				t.Fatalf("notes:\n%s", note)
			}
		})
	}
}
