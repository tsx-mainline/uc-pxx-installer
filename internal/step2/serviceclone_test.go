//go:build install && step2 && devtest

package step2_test

// The service of an end-user base with the clone (UC_PXX_STEP2_KIND=clone):
// no payload receive; the wait for the provisioning done record ("failed:
// check" without it); the consent over the USB cable (the kind consent:
// COMMIT {auto, plan_id}); auto true gives the countdown once per attempt
// key "clone <ref_a>"; auto false starts nothing (the panel's hold decides).

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/clone"
	"uc-pxx-installer/internal/step2"
	"uc-pxx-installer/internal/step2ui"
	"uc-pxx-installer/internal/xfer/handover"
)

// consentTransport is fakeTransport with the consent receiver: the page's
// COMMIT comes at once (auto as given; none when commit is false).
type consentTransport struct {
	*fakeTransport
	commit   bool
	auto     bool
	planID   string // the id the page names ("": the unit's)
	consents []string
}

func (c *consentTransport) StartConsent(id string) (step2.Proc, error) {
	c.consents = append(c.consents, id)
	xd := filepath.Join(c.stage(), "xfer")
	os.MkdirAll(xd, 0o700)
	if c.commit {
		pid := c.planID
		if pid == "" {
			pid = id
		}
		if pid == id { // the receiver takes only the unit's plan id
			b, _ := json.Marshal(handover.Consent{Version: handover.RecordVersion, Kind: handover.RecordKindConsent, PlanID: pid, Auto: c.auto, Session: 1, Time: 1})
			os.WriteFile(filepath.Join(xd, handover.ConsentRecord), b, 0o600)
		}
	}
	return &fakeProc{done: make(chan struct{})}, nil
}

// confClone: the base's conf is in B's tree (and so in ref_b).
func (u *cloneUnit) confClone() {
	if b, err := os.ReadFile(u.root.Path(step2.ConfFile)); err != nil || string(b) != "UC_PXX_STEP2_KIND=clone\nUC_PXX_STEP2_USB=yes\n" {
		u.t.Fatalf("the base's conf: %q %v", b, err)
	}
}

func (u *cloneUnit) serviceClone(tr step2.Transport, keys []step2ui.Key) (*step2.ServiceResult, *fakePanel, bool, error) {
	pn := &fakePanel{keys: make(chan step2ui.Key, 64), script: keys}
	c := &clock{t: time.Unix(1700000000, 0)}
	rebooted := false
	var out bytes.Buffer
	o := u.opts(&out)
	o.Payload, o.Confirmed, o.Clone = "", false, false
	o.InfoFile = step2.XferInfo
	r, err := step2.Service(u.env(), o, step2.ServiceEnv{Panel: func() (step2.Panel, error) { return pn, nil }, Now: c.now,
		Sleep: func(time.Duration) {}, Reboot: func() error { rebooted = true; return nil }, Hold: func() {}, Transport: tr})
	if err != nil {
		u.t.Logf("service output:\n%s", out.String())
	}
	return r, pn, rebooted, err
}

func TestStep2ServiceClone(t *testing.T) {
	bin, exe := bootctlBin(t)
	hold := []step2ui.Key{step2ui.KeyNone, step2ui.KeyOKDown, step2ui.KeyNone, step2ui.KeyNone, step2ui.KeyNone, step2ui.KeyNone}

	t.Run("auto consent: the countdown, the clone, the restart", func(t *testing.T) {
		u := newCloneUnit(t, bin, exe, nil)
		u.confClone()
		ct := &consentTransport{fakeTransport: &fakeTransport{u: u.unit}, commit: true, auto: true}
		r, _, rebooted, err := u.serviceClone(ct, nil)
		if err != nil || !rebooted || r.Start == nil || r.Start.Plan == nil {
			t.Fatalf("service: %v %+v rebooted %v", err, r, rebooted)
		}
		p := r.Start.Plan
		// the consent names the shown plan (confirmed by the page's choice);
		// the started plan differs from it in the confirmation only
		if len(ct.consents) != 1 || !handover.PlanIDForm.MatchString(ct.consents[0]) || ct.consents[0] == p.ID[:12] || ct.payloadStarts != 0 {
			t.Fatalf("consents %v, payload receives %d", ct.consents, ct.payloadStarts)
		}
		at, _ := os.ReadFile(u.root.Path(step2.StateDir + "/" + step2.AttemptsFile))
		if string(at) != "drive clone "+u.refA.SHA256+"\n" {
			t.Fatalf("attempts %q", at)
		}
		st, _ := os.ReadFile(u.root.Path(step2.StateDir + "/" + step2.StateFile))
		if !strings.Contains(string(st), "started "+p.ID+" supervised=false auto=true") {
			t.Fatalf("state %q", st)
		}
		if in := u.info(); in.PlanID != p.ID[:12] || !strings.HasPrefix(in.Step2, "restarting") {
			t.Fatalf("info %+v", in)
		}
		a := u.ab()
		if a.Slots[0] != (avbab.Slot{Priority: 15, Tries: 7}) || !a.Slots[1].Keeper() {
			t.Fatalf("A %v B %v", a.Slots[0], a.Slots[1])
		}
		u.cloneEnd(p)
	})

	t.Run("consent without auto: the hold decides", func(t *testing.T) {
		u := newCloneUnit(t, bin, exe, nil)
		u.confClone()
		ct := &consentTransport{fakeTransport: &fakeTransport{u: u.unit}, commit: true, auto: false}
		r, _, rebooted, err := u.serviceClone(ct, hold)
		if err != nil || !rebooted || r.Start == nil {
			t.Fatalf("service: %v %+v", err, r)
		}
		if at, _ := os.ReadFile(u.root.Path(step2.StateDir + "/" + step2.AttemptsFile)); len(at) != 0 {
			t.Fatalf("an attempt line without an automatic start: %q", at)
		}
		st, _ := os.ReadFile(u.root.Path(step2.StateDir + "/" + step2.StateFile))
		if !strings.Contains(string(st), "auto=false") {
			t.Fatalf("state %q", st)
		}
	})

	t.Run("consent of another plan id: no countdown, Back = not now", func(t *testing.T) {
		u := newCloneUnit(t, bin, exe, nil)
		u.confClone()
		ct := &consentTransport{fakeTransport: &fakeTransport{u: u.unit}, commit: true, auto: true, planID: "ffffffffffff"}
		img := u.image()
		r, _, rebooted, err := u.serviceClone(ct, []step2ui.Key{step2ui.KeyNone, step2ui.KeyBack})
		if err != nil || rebooted || r.What != "not now" || !bytes.Equal(img, u.image()) {
			t.Fatalf("service: %v %+v", err, r)
		}
	})

	t.Run("no done record: failed: check, nothing written", func(t *testing.T) {
		u := newCloneUnit(t, bin, exe, nil)
		u.confClone()
		os.Remove(u.root.Path(clone.DoneRecordPath))
		ct := &consentTransport{fakeTransport: &fakeTransport{u: u.unit}, commit: true, auto: true}
		img := u.image()
		_, _, rebooted, err := u.serviceClone(ct, nil)
		if err == nil || rebooted || !strings.Contains(err.Error(), "failed: check") || !bytes.Equal(img, u.image()) {
			t.Fatalf("service: %v", err)
		}
		if in := u.info(); in.Step2 != "failed: check" {
			t.Fatalf("info %+v", in)
		}
		if len(ct.consents) != 0 || ct.takes != 0 {
			t.Fatalf("the gadget was taken: %+v", ct.fakeTransport)
		}
	})

	t.Run("no done record while uc-pxx-provision still runs: the wait goes on to its cap", func(t *testing.T) {
		u := newCloneUnit(t, bin, exe, nil)
		u.confClone()
		os.Remove(u.root.Path(clone.DoneRecordPath))
		os.MkdirAll(u.root.Path("/run"), 0o755)
		os.WriteFile(u.root.Path(step2.ProvisionPidFile), []byte("4242\n"), 0o644)
		os.MkdirAll(u.root.Path("/proc/4242"), 0o755)
		os.WriteFile(u.root.Path("/proc/4242/comm"), []byte("uc-pxx-provisio\n"), 0o644)
		ct := &consentTransport{fakeTransport: &fakeTransport{u: u.unit}, commit: true, auto: true}
		_, _, rebooted, err := u.serviceClone(ct, nil)
		if err == nil || rebooted || !strings.Contains(err.Error(), "uc-pxx-provision still runs") || ct.takes != 0 {
			t.Fatalf("service: %v", err)
		}
	})
	t.Run("a done record of another ref_b: failed: check", func(t *testing.T) {
		u := newCloneUnit(t, bin, exe, nil)
		u.confClone()
		b, _ := os.ReadFile(u.root.Path(clone.DoneRecordPath))
		os.WriteFile(u.root.Path(clone.DoneRecordPath), bytes.Replace(b, []byte(u.gate.Profiles[0].RefB), []byte(strings.Repeat("e", 64)), 1), 0o644)
		img := u.image()
		_, _, _, err := u.serviceClone(&consentTransport{fakeTransport: &fakeTransport{u: u.unit}}, nil)
		if err == nil || !bytes.Equal(img, u.image()) || u.info().Step2 != "failed: check" {
			t.Fatalf("service: %v info %+v", err, u.info())
		}
	})
}
