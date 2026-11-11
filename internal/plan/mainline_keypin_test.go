package plan_test

import (
	"strings"
	"testing"

	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/keykeep"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/plan"
)

// TestMainlineKeyPin (owner O3): with the release's pin of the key record,
// a mainline plan keeps or finds kept only the pinned record: the record's
// sha256 equal to the pin passes ("kept" with no step, or "keep"), any other
// record refuses with key-pin before any step, whatever the sectors hold; no
// pin (a test gate) is the check of before. The release's gate pins the
// 1.0.0.94 userdebug key's record, a test gate none.
func TestMainlineKeyPin(t *testing.T) {
	if got := mainline.EmbeddedGate().KeyRecordPin(); got != mainline.ReviewedKeyRecordSHA256 ||
		mainline.ReviewedKeyRecordSHA256 != "7143b925e62e867ae6a428fafac17252d4e7b001c96767ee00c40f77f787b08e" {
		t.Fatalf("the release's pin %q", got)
	}
	var nilGate *mainline.Gate
	if mainline.TestGate(nil, nil, nil).KeyRecordPin() != "" || nilGate.KeyRecordPin() != "" {
		t.Fatal("a test gate (or nil) has a pin")
	}
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	rec := keykeep.SHA256(keykeep.Encode(fx.Keys))
	e, s := keykeep.Empty, keykeep.Same
	for _, c := range []struct {
		name    string
		pin     string
		classes [2]keykeep.Class
		state   string
		ok      bool
	}{
		{"kept, the pin of this record", rec, [2]keykeep.Class{s, s}, "kept", true},
		{"empty, the pin of this record", rec, [2]keykeep.Class{e, e}, "keep", true},
		{"kept, another pin", mainline.ReviewedKeyRecordSHA256, [2]keykeep.Class{s, s}, "not-possible", false},
		{"empty, another pin", mainline.ReviewedKeyRecordSHA256, [2]keykeep.Class{e, e}, "not-possible", false},
		{"a pin of a wrong length", rec[:63], [2]keykeep.Class{s, s}, "not-possible", false},
		{"no pin (a test gate)", "", [2]keykeep.Class{s, s}, "kept", true},
	} {
		for _, direct := range []bool{false, true} {
			t.Run(c.name+map[bool]string{false: "", true: " (direct)"}[direct], func(t *testing.T) {
				var checks []plan.Check
				var kk *plan.KeyKeepInfo
				var steps []plan.Step
				var err error
				var src string
				if direct {
					w := newDWorld(t, fx, dUnitReset)
					w.in.MainlineKey.Pin, w.in.MainlineKey.Classes = c.pin, c.classes
					p, e := plan.ComputeMainlineDirect(w.in)
					checks, kk, steps, err, src = p.Checks, p.KeyKeep, p.Steps, e, p.KeySource
				} else {
					w := newMLWorld(t, fx, mlUnit, nil)
					w.in.MainlineKey.Pin, w.in.MainlineKey.Classes = c.pin, c.classes
					p, e := plan.ComputeMainline(w.in)
					checks, kk, steps, err, src = p.Checks, p.KeyKeep, p.Steps, e, p.KeySource
				}
				if (err == nil) != c.ok {
					t.Fatalf("err %v", err)
				}
				pinCheck := ""
				for _, ch := range checks {
					if ch.ID == "key-pin" {
						pinCheck = map[bool]string{true: "ok", false: "fail"}[ch.OK]
					}
				}
				switch {
				case c.pin == "" && pinCheck != "":
					t.Fatalf("a key-pin check without a pin: %s", pinCheck)
				case c.pin != "" && c.ok && (pinCheck != "ok" || !strings.Contains(src, "the release's pin")):
					t.Fatalf("pin check %q, key source %q", pinCheck, src)
				case !c.ok && (pinCheck != "fail" || len(steps) != 0):
					t.Fatalf("pin check %q, %d steps", pinCheck, len(steps))
				}
				if kk == nil || kk.State != c.state {
					t.Fatalf("key keep %+v, want %s", kk, c.state)
				}
				if c.ok && (steps[0].Op == "keep-key") != (c.state == "keep") {
					t.Fatalf("first step %s", steps[0].Op)
				}
			})
		}
	}
}

// TestMainlineKeyPinByGate (review M-3): the release's gate pins the record
// whatever the caller set (an empty or another Pin); a test gate keeps the
// caller's Pin; no key input stays none.
func TestMainlineKeyPinByGate(t *testing.T) {
	emb, tg := mainline.EmbeddedGate(), mainline.TestGate(nil, nil, nil)
	for _, pin := range []string{"", strings.Repeat("0", 64)} {
		in := &plan.MainlineKeyInput{Pin: pin}
		if got := plan.PinnedKey(in, emb); got.Pin != mainline.ReviewedKeyRecordSHA256 || in.Pin != pin {
			t.Fatalf("the release gate with the caller's pin %q: %q (the caller's input changed: %v)", pin, got.Pin, in.Pin != pin)
		}
		if got := plan.PinnedKey(in, tg); got.Pin != pin {
			t.Fatalf("a test gate: %q, want the caller's %q", got.Pin, pin)
		}
	}
	if plan.PinnedKey(nil, emb) != nil {
		t.Fatal("no key input became one")
	}
}
