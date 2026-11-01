package plan_test

import (
	"bytes"
	"strings"
	"testing"

	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/manifest"
	"uc-pxx-installer/internal/plan"
)

// TestGateEmbedded: the owner's decision on FWGEN-REVIEW F-1 (2026-11-01,
// "proven only, then widen"): of the embedded set exactly the two files
// whose pair of U-Boot and resource booted on unit 2 pass the gate. Each of
// the other 23 install manifests is refused for its untested bootloader, the
// 12 refused ones for the older board.
func TestGateEmbedded(t *testing.T) {
	ms, err := manifest.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	var open []string
	untested, board := 0, 0
	for _, m := range ms {
		g := plan.Gate(m)
		switch {
		case g == "":
			open = append(open, m.Firmware.File)
		case g == plan.NoteBootchainNotTested:
			untested++
			if m.Status != manifest.StatusInstall || m.BootchainProven() {
				t.Errorf("%s: refused for the bootloader, but status %s, proven %v", m.Name(), m.Status, m.BootchainProven())
			}
		case strings.Contains(g, "older UC-P10 board"):
			board++
		default:
			t.Errorf("%s: gate %q", m.Name(), g)
		}
	}
	if strings.Join(open, " ") != "pcdfw-1.0.0.94-userdebug.bin pcdfw-1.0.6.20.bin" || untested != 23 || board != 12 {
		t.Fatalf("open %v, %d untested bootloaders (want 23), %d older board (want 12)", open, untested, board)
	}
}

// TestGateBootchain: pass 1 refuses a firmware whose pair never booted, with
// the plain text; the plan and Reopen refuse a recorded fact edited to say
// it did; the other recorded facts of F-5 are checked too.
func TestGateBootchain(t *testing.T) {
	nb := fakeemmc.NewFixture(fakeemmc.FixtureOpt{NotBooted: true})
	if _, err := plan.VerifyOfficial(bytes.NewReader(nb.FW), int64(len(nb.FW)), nb.Keys, nb.Manifests, nil); err == nil ||
		!strings.Contains(err.Error(), plan.NoteBootchainNotTested) {
		t.Fatalf("not-booted pair: %v", err)
	}
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	w := newWorld(t, fx, unit2)
	if w.in.Payload.BootChain.BootchainStatus != "booted" {
		t.Fatalf("recorded %q", w.in.Payload.BootChain.BootchainStatus)
	}
	w.in.Payload.BootChain.BootchainStatus = "not-booted"
	p, err := plan.Compute(w.in)
	if err == nil {
		t.Fatal("a not-booted pair planned")
	}
	failed := false
	for _, c := range p.Checks {
		failed = failed || (c.ID == "boot-chain" && !c.OK)
	}
	if !failed {
		t.Fatalf("no boot-chain refusal: %v", err)
	}
	// CheckRecorded (Reopen): the boot-chain status and the F-5 facts
	m := fx.Manifests[0]
	for name, e := range map[string]func(p *plan.Payload){
		"bootchain status": func(p *plan.Payload) { p.BootChain.BootchainStatus = "not-booted" },
		"build date":       func(p *plan.Payload) { p.BuildDateUTC++ },
		"vbmeta flags":     func(p *plan.Payload) { p.Vbmeta.Flags = 1 },
		"checked names":    func(p *plan.Payload) { p.AVB.Checked = []string{"boot", "boot", "boot", "boot"} },
		"checked order":    func(p *plan.Payload) { p.AVB.Checked = []string{"boot", "dtbo", "system", "vendor"} },
	} {
		q := *newWorld(t, fx, unit2).in.Payload
		q.AVB.Checked = append([]string(nil), q.AVB.Checked...)
		e(&q)
		if err := plan.CheckRecorded(m, &q); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	q := *newWorld(t, fx, unit2).in.Payload
	if err := plan.CheckRecorded(m, &q); err != nil {
		t.Fatalf("unchanged: %v", err)
	}
}
