package xfer

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uc-pxx-installer/internal/xfer/handover"
)

// TestConsentKind: the step-2 consent without a payload (record §6.10):
// HELLO mode "step2", state waiting-consent, info.plan_id from the root's
// info file; a COMMIT is taken only with the unit's plan id, once; auto false
// is taken and starts nothing (the record says so); no PUT is taken; the root
// reads the record with ReadConsent for its own plan id only.
func TestConsentKind(t *testing.T) {
	info := filepath.Join(t.TempDir(), "info.json")
	if err := handover.WriteInfo(info, handover.Info{Step2: "waiting-consent", PlanID: "0123456789ab", Slot: "b"}); err != nil {
		t.Fatal(err)
	}
	for _, auto := range []bool{true, false} {
		g := newRig(t, Config{Kind: KindConsent, PlanID: "0123456789ab", InfoFile: info})
		h, wait := g.session()
		hl := h.req(TypeHello, struct{}{})
		in, _ := hl["info"].(map[string]any)
		if hl["mode"] != "step2" || hl["state"] != StateWaitingConsent || in["plan_id"] != "0123456789ab" || len(hl["pins"].([]any)) != 0 {
			t.Fatalf("hello %v", hl)
		}
		if b, _ := h.put(ManifestName, []byte("{}"), 2, DataChunk); b["accept"] != false || !strings.Contains(b["reason"].(string), "takes no file") {
			t.Fatalf("put %v", b)
		}
		if c := h.req(TypeCommit, map[string]any{"auto": auto, "plan_id": "ffffffffffff"}); c["ok"] != false || !strings.Contains(c["reason"].(string), "not the plan") {
			t.Fatalf("commit of another plan %v", c)
		}
		if _, err := handover.ReadConsent(g.dir, os.Getuid(), "0123456789ab"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("a record after a refused COMMIT: %v", err)
		}
		if c := h.req(TypeCommit, map[string]any{"auto": auto, "plan_id": "0123456789ab"}); c["ok"] != true || c["auto"] != auto {
			t.Fatalf("commit %v", c)
		}
		if c := h.req(TypeCommit, map[string]any{"auto": true, "plan_id": "0123456789ab"}); c["ok"] != false {
			t.Fatalf("a second commit %v", c)
		}
		if st := g.state(); st.State != StateConsentComplete {
			t.Fatalf("state %+v", st)
		}
		c, err := handover.ReadConsent(g.dir, os.Getuid(), "0123456789ab")
		if err != nil || c.Auto != auto || c.PlanID != "0123456789ab" {
			t.Fatalf("record %+v %v", c, err)
		}
		if _, err := handover.ReadConsent(g.dir, os.Getuid(), "0123456789ac"); err == nil {
			t.Fatal("the record was taken for another plan id")
		}
		if _, err := handover.ReadConsent(g.dir, os.Getuid()+1, "0123456789ab"); err == nil {
			t.Fatal("the record was taken of another user")
		}
		h.l.disable()
		wait()
	}
	// a mode is an unknown key here: a protocol violation of this kind
	g := newRig(t, Config{Kind: KindConsent, PlanID: "0123456789ab"})
	h, wait := g.session()
	h.l.hostOut(h.frame(TypeCommit, map[string]any{"auto": true, "plan_id": "0123456789ab", "mode": "official"}))
	h.noReply()
	wait()
	for _, bad := range []Config{{Kind: KindConsent}, {Kind: KindConsent, PlanID: "0123456789AB"}, {Kind: KindConsent, PlanID: "0123456789ab", StatusOnly: true},
		{Kind: KindConsent, PlanID: "0123456789ab", Pins: []string{strings.Repeat("a", 64)}}, {Kind: KindPayload, PlanID: "0123456789ab"}} {
		bad.Dir = t.TempDir()
		if _, err := NewReceiver(bad); err == nil {
			t.Fatalf("receiver %+v taken", bad)
		}
	}
}
