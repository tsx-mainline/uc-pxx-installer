package install_test

// End-to-end power-cut sweep of the "re-establish the keeper" install
// (KEEPER-REESTABLISH.md). The start is the valid, both-slots-trying A/B block
// the BCB `bootonce-bootloader` route leaves on a stock unit (A{15,6,0}
// B{14,7,0}). The plan prepends R(K) (the reviewed repair write), so the
// install does TWO A/B writes: R(K) then the arm. These run the real
// uc-pxx-bootctl-inst (UC_PXX_BOOTCTL), cut the install before every primitive
// write, and prove the cardinal rule at each cut: the keeper is bootable and
// libavb boots it until the final arm; the target is never selectable while it
// is partly written. CheckCutState is NOT used here: its "keeper unchanged"
// invariant is deliberately violated by R(K) (it marks the keeper successful).

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/plan"
)

var unitBothTrying = fakeemmc.Unit{AB: [2][3]byte{{15, 6, 0}, {14, 7, 0}}, A: "old", B: "old"}

// payloadState reports whether every target write range holds its before-image
// (allOld) or its payload image (allNew).
func payloadState(img []byte, p *plan.Plan) (allOld, allNew bool) {
	allOld, allNew = true, true
	for _, s := range p.Steps {
		if s.Write == nil {
			continue
		}
		w := s.Write
		h := sha256.Sum256(img[w.StartLBA*512+w.Offset : w.StartLBA*512+w.Offset+w.Length])
		got := hex.EncodeToString(h[:])
		allOld = allOld && got == w.BeforeSHA256
		allNew = allNew && got == w.SHA256
	}
	return
}

// reestablishCutOK checks one cut image and returns which slot boots. It fails
// the test on any state that breaks the cardinal rule.
func reestablishCutOK(t *testing.T, img []byte, p *plan.Plan) string {
	t.Helper()
	sec := img[plan.ABLBA*512 : (plan.ABLBA+1)*512]
	a, _ := avbab.Parse(sec)
	if err := a.Validate(); err != nil {
		t.Fatalf("A/B not valid at the cut: %v", err)
	}
	k := 0
	if p.Keeper == "b" {
		k = 1
	}
	tg := 1 - k
	// the cardinal rule: the keeper is bootable, and some slot boots.
	if !a.Slots[k].Bootable() {
		t.Fatalf("the keeper (slot %s) is not bootable at the cut: %v", p.Keeper, a.Slots[k])
	}
	if a.Pick() < 0 {
		t.Fatal("no slot boots at the cut")
	}
	// the keeper is the reset-trying keeper (before R(K)), R(K) {15,0,1}, or
	// R(K) after the lower-arm {14,0,1}. Nothing else may appear.
	ks := a.Slots[k]
	okKeeper := (ks.Successful == 0 && ks.Tries > 0 && ks.Priority == 15) ||
		ks == (avbab.Slot{Priority: 15, Successful: 1}) ||
		ks == (avbab.Slot{Priority: 14, Successful: 1})
	if !okKeeper {
		t.Fatalf("the keeper reached an unexpected state at the cut: %v", ks)
	}
	allOld, allNew := payloadState(img, p)
	// the target is never selectable while partly written.
	if a.Slots[tg].Bootable() && !allOld && !allNew {
		t.Fatalf("the target %v is bootable while it is partly written", a.Slots[tg])
	}
	if a.Pick() == tg {
		if a.Slots[tg] != (avbab.Slot{Priority: 15, Tries: 7}) || !allNew {
			t.Fatalf("the target boots next but is not armed with the whole payload: %v allNew=%v", a.Slots[tg], allNew)
		}
		return "target"
	}
	return "keeper"
}

func TestReestablishCutSweep(t *testing.T) {
	tool := realTool(t)
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	r := newRig(t, fx, tool, unitBothTrying)
	im := r.open()
	p0, pl, err := r.makePlan(im, "")
	if err != nil {
		t.Fatal(err)
	}
	if p0.Keeper != "a" || p0.Target != "b" || len(p0.Steps) == 0 || p0.Steps[0].Bootctl == nil ||
		p0.Steps[0].Bootctl.Args[0] != "repair" {
		t.Fatalf("not a re-establish plan: keeper %s, step0 %+v", p0.Keeper, p0.Steps[0])
	}
	j := &journal{}
	if _, err := install.Execute(p0, pl, im, install.Options{Bootctl: tool, MaxWrite: sweepWrite,
		Hooks: &install.Hooks{CutAfter: -1, Observe: j.observe}}); err != nil {
		t.Fatal(err)
	}
	im.Close()
	n := len(j.ev)
	// R(K) and the arm are both A/B writes in the journal.
	abWrites := 0
	for _, e := range j.ev {
		if e.Op == "ab" {
			abWrites++
		}
	}
	if abWrites != 2 {
		t.Fatalf("a re-establish install makes two A/B writes (R(K) and the arm), got %d", abWrites)
	}
	t.Parallel()
	for _, torn := range []bool{false, true} {
		for _, k := range sweepPoints(n, j.ev) {
			point(t, fmt.Sprintf("cut %d torn %v", k, torn), func(t *testing.T) {
				r := newRig(t, fx, tool, unitBothTrying)
				im := r.open()
				p, pl, err := r.makePlan(im, "")
				if err != nil {
					t.Fatal(err)
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
				boots := reestablishCutOK(t, cut, p)
				if !sameOutside(cut, r.before, p) {
					t.Fatalf("cut %d: a byte outside the target's write ranges and the A/B sector changed", k)
				}
				if k < n && boots != "keeper" {
					t.Fatalf("cut %d: boots %s; only the final arm may make the target boot next", k, boots)
				}
				if k == n && boots != "target" {
					t.Fatalf("complete run: boots %s, want the target", boots)
				}
				reestablishResume(t, r, boots == "target")
			})
		}
	}
}

// reestablishResume runs the installer again on the cut image. When the target
// already boots next (the full run) a new plan is refused; otherwise it
// completes and lands on the normal end state with the target holding the
// payload.
func reestablishResume(t *testing.T, r *rig, targetBoots bool) {
	t.Helper()
	im := r.open()
	defer im.Close()
	p2, pl, err := r.makePlan(im, "")
	if targetBoots {
		if err == nil {
			t.Fatal("the target boots next, but a new plan was accepted")
		}
		return
	}
	if err != nil {
		var sb strings.Builder
		p2.Render(&sb)
		t.Fatalf("resume refused: %v\n%s", err, sb.String())
	}
	if _, err := install.Execute(p2, pl, im, install.Options{Bootctl: r.tool}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	im.Close()
	after, err := os.ReadFile(r.path)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := avbab.Parse(after[plan.ABLBA*512:])
	if err := a.Validate(); err != nil {
		t.Fatalf("resume final A/B not valid: %v", err)
	}
	// the normal end state: keeper a {14,0,1} successful, target b {15,7,0} armed.
	fin := fakeemmc.ABSector([2][3]byte{{14, 0, 1}, {15, 7, 0}}, nil)[:32]
	if !bytes.Equal(after[plan.ABLBA*512:plan.ABLBA*512+32], fin) {
		t.Fatalf("resume final A/B %x, want %x", after[plan.ABLBA*512:plan.ABLBA*512+32], fin)
	}
	tb, err := gpt.Parse(bytes.NewReader(after), int64(len(after)))
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range plan.SlotParts {
		e, _ := tb.Find(part + "_b")
		img := r.fx.New[part]
		if !bytes.Equal(after[e.Start*512:e.Start*512+int64(len(img))], img) {
			t.Fatalf("after the resume, %s does not hold the payload", e.Name)
		}
	}
}
