package plan_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/plan"
)

// TestReopenGate: Reopen (the install of a plan file) checks the recorded
// facts of pass 1 against the embedded manifest with CheckRecorded: the gate
// is read again, and the boot chain and the AVB facts must be the manifest's.
func TestReopenGate(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{User: true, TestBuild: true})
	w := newWorld(t, fx, unit2)
	deviceMode(w)
	p1, err := plan.Compute(w.in)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(p1)
	reopen := func(edit func(p *plan.Payload), text []byte) error {
		var pf plan.Plan
		json.Unmarshal(b, &pf)
		if edit != nil {
			edit(pf.Payload)
		}
		ms := fx.Manifests
		if text != nil {
			if ms, err = fx.LoadManifest(text); err != nil {
				t.Fatal(err)
			}
		}
		_, err := plan.Reopen(bytes.NewReader(fx.FW), int64(len(fx.FW)), fx.Keys, ms, pf.Payload)
		return err
	}
	if err := reopen(nil, nil); err != nil {
		t.Fatal(err)
	}
	for name, e := range map[string]func(p *plan.Payload){
		"AVB not verified":   func(p *plan.Payload) { p.AVB.Verified = false },
		"AVB key version":    func(p *plan.Payload) { p.AVB.PSKKeyVersion++ },
		"resource status":    func(p *plan.Payload) { p.BootChain.ResourceStatus = "pending" },
		"uboot of the chain": func(p *plan.Payload) { p.BootChain.UBootSHA256 = strings.Repeat("2", 64) },
		"boot resource":      func(p *plan.Payload) { p.BootResource = strings.Repeat("2", 64) },
		"nvram model":        func(p *plan.Payload) { p.NvramModel = "" },
		"test build":         func(p *plan.Payload) { p.TestBuild = false },
		"fingerprint":        func(p *plan.Payload) { p.Fingerprint = "x" },
	} {
		if err := reopen(e, nil); err == nil {
			t.Errorf("%s: reopened", name)
		}
	}
	// the manifest no longer allows the install: the plan file does not install
	for _, c := range []struct{ from, to string }{
		{`"status": "install"`, `"status": "recognise"`},
		{`"resource_status": "booted"`, `"resource_status": "pending"`},
		{`"nvram_model": "gs-nvram-72005-v1"`, `"nvram_model": null`},
	} {
		text := strings.Replace(string(fx.Manifest), c.from, c.to, 1)
		if text == string(fx.Manifest) {
			t.Fatalf("fixture: no %s", c.from)
		}
		if err := reopen(nil, []byte(text)); err == nil || !strings.Contains(err.Error(), "approved") {
			t.Errorf("%s: %v", c.to, err)
		}
	}
	// the notes of the reopened payload are the plan's (the names of the slots come from the manifests)
	var pf plan.Plan
	json.Unmarshal(b, &pf)
	pl, err := plan.Reopen(bytes.NewReader(fx.FW), int64(len(fx.FW)), fx.Keys, fx.Manifests, pf.Payload)
	if err != nil {
		t.Fatal(err)
	}
	in := w.in
	in.Payload, in.KnownBefore = pl, plan.KnownBeforeOf(&pf)
	if p2, err := plan.Compute(in); err != nil || p2.ID != p1.ID || strings.Join(p2.Notes, "|") != strings.Join(p1.Notes, "|") {
		t.Fatalf("plan of the reopened payload: %v", err)
	}
}
