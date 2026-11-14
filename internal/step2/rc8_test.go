//go:build install && step2 && devtest

package step2_test

// mainline-rc-8 (RULES-PATCH-4-REVIEW MAJOR-2): the AUTO-STEP2 one-attempt
// record is keyed on the payload's manifest sha256, not on the identity of
// its files (size, times, inode). After a restart the RAM stage is gone and
// a new USB send of the same payload makes new files; that send gets no
// second countdown. A send of another manifest is refused by the receiver's
// pins (the seal) as before: nothing is shown, nothing written.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"uc-pxx-installer/internal/drivestate"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/step2"
	"uc-pxx-installer/internal/step2ui"
	"uc-pxx-installer/internal/xfer/handover"
)

// serviceUSBWith is serviceUSB with the options o (o.Payload, o.Confirmed and
// o.InfoFile are set as serviceUSB sets them).
func (u *unit) serviceUSBWith(ft *fakeTransport, keys []step2ui.Key, o step2.Options) (*step2.ServiceResult, *fakePanel, bool, error) {
	pn := &fakePanel{keys: make(chan step2ui.Key, 64), script: keys}
	c := &clock{t: timeOf(1700000000)}
	rebooted := false
	o.Payload, o.Confirmed = "", false
	o.InfoFile = step2.XferInfo
	if o.Out == nil {
		o.Out = &bytes.Buffer{}
	}
	r, err := step2.Service(u.env(), o, step2.ServiceEnv{Panel: func() (step2.Panel, error) { return pn, nil }, Now: c.now,
		Sleep: func(time.Duration) {}, Reboot: func() error { rebooted = true; return nil }, Hold: func() {}, Transport: ft})
	return r, pn, rebooted, err
}

// restartRAM is what a restart does to the RAM stage of the sealed payload:
// it is gone (the folder, its AUTO-STEP2, the receiver's folder).
func (u *unit) restartRAM(t *testing.T) {
	sd := u.root.Path(step2.StageDir)
	filepath.Walk(sd, func(p string, fi os.FileInfo, err error) error {
		if err == nil && fi.IsDir() {
			os.Chmod(p, 0o755)
		}
		return nil
	})
	if err := os.RemoveAll(sd); err != nil {
		t.Fatal(err)
	}
}

func countdownShown(pn *fakePanel) bool {
	for _, s := range pn.shown {
		if strings.Contains(strings.Join(s, " "), "Starts by itself in") {
			return true
		}
	}
	return false
}

func shownHas(pn *fakePanel, sub string) bool {
	for _, s := range pn.shown {
		if strings.Contains(strings.Join(s, " "), sub) {
			return true
		}
	}
	return false
}

func TestStep2AutoResendRC8(t *testing.T) {
	bin, exe := bootctlBin(t)
	_, res := bundle()
	newU := func(t *testing.T) (*unit, *fakeTransport) {
		u := newUnit(t, unitOf(res, shapes[0].ab, "a", "new"), "a", bin, exe)
		u.confUSB()
		t.Cleanup(func() { u.restartRAM(t) })
		return u, &fakeTransport{u: u, src: u.payload, auto: true}
	}
	manifestSHA := func(t *testing.T, dir string) string {
		b, err := os.ReadFile(filepath.Join(dir, mainline.ManifestFile))
		if err != nil {
			t.Fatal(err)
		}
		s := sha256.Sum256(b)
		return hex.EncodeToString(s[:])
	}
	attempts := func(u *unit) string {
		b, _ := os.ReadFile(u.root.Path(step2.StateDir + "/" + step2.AttemptsFile))
		return string(b)
	}
	sealedIdent := func(t *testing.T, u *unit) string {
		id, err := drivestate.Stat(filepath.Join(u.root.Path(step2.StagePayload), mainline.ManifestFile))
		if err != nil {
			t.Fatal(err)
		}
		return id.String()
	}
	// the first key answers the receive screen
	back := []step2ui.Key{step2ui.KeyNone, step2ui.KeyBack}
	// (a second hold after it: if a countdown came, the first press only
	// stopped it; the test then fails on the countdown instead of waiting)
	hold := []step2ui.Key{step2ui.KeyNone, step2ui.KeyOKDown, step2ui.KeyNone, step2ui.KeyNone, step2ui.KeyNone, step2ui.KeyNone,
		step2ui.KeyOKDown, step2ui.KeyNone, step2ui.KeyNone, step2ui.KeyNone, step2ui.KeyNone}

	t.Run("Back in the countdown, a restart, a new send of the same payload: no second countdown", func(t *testing.T) {
		u, ft := newU(t)
		m := manifestSHA(t, u.payload)
		r, pn, _, err := u.serviceUSB(ft, back)
		if err != nil || r.What != "not now" || !countdownShown(pn) || !bytes.Equal(u.image(), u.before) {
			t.Fatalf("the first send: %v %+v countdown %v", err, r, countdownShown(pn))
		}
		at1 := attempts(u)
		id1 := sealedIdent(t, u)
		// the restart: the RAM stage is gone; the keeper boots again; the page
		// sends the same payload again with "continue by itself"
		u.restartRAM(t)
		u.boot("a")
		ft2 := &fakeTransport{u: u, src: u.payload, auto: true}
		r, pn, _, err = u.serviceUSB(ft2, back)
		if err != nil || r.What != "not now" || !bytes.Equal(u.image(), u.before) {
			t.Fatalf("the second send: %v %+v", err, r)
		}
		if id2 := sealedIdent(t, u); id2 == id1 {
			t.Fatalf("the test did not make new files: %s", id2)
		}
		if _, err := os.Lstat(filepath.Join(filepath.Dir(u.root.Path(step2.StagePayload)), step2ui.AutoFile)); err != nil {
			t.Fatalf("the second send asked for AUTO-STEP2, the seal wrote it: %v", err)
		}
		if countdownShown(pn) || !shownHas(pn, "started step 2 once already") {
			t.Fatalf("a second countdown for the same manifest (or no reason shown): %v", pn.shown)
		}
		// the record checks after the behaviour (code review NIT-1)
		if at1 != step2ui.AttemptLine(m) {
			t.Fatalf("the attempt record %q, want the manifest sha256 %s", at1, m)
		}
		if at := attempts(u); at != at1 {
			t.Fatalf("the attempt record changed: %q", at)
		}
	})
	t.Run("a power cut in the automatic run, a restart, a new send of the same payload: no countdown; the hold resumes", func(t *testing.T) {
		u, ft := newU(t)
		m := manifestSHA(t, u.payload)
		o := u.opts(&bytes.Buffer{})
		o.Hooks = &install.Hooks{CutAfter: 3}
		r, pn, _, err := u.serviceUSBWith(ft, []step2ui.Key{step2ui.KeyNone}, o)
		if err == nil || !errors.Is(err, install.ErrPowerCut) || r == nil || r.Start == nil || r.Start.Plan == nil || !r.Start.Plan.Confirm.Auto || !countdownShown(pn) {
			t.Fatalf("the automatic start did not stop at the cut: %v %+v", err, r)
		}
		p0 := r.Start.Plan
		cs, err := install.CheckCutState(bytes.NewReader(u.image()), &p0.Plan, u.start)
		if err != nil || cs.Boots != "keeper" {
			t.Fatalf("the cut state: %v %+v", err, cs)
		}
		u.restartRAM(t)
		u.boot(p0.Keeper)
		ft2 := &fakeTransport{u: u, src: u.payload, auto: true}
		r, pn, rebooted, err := u.serviceUSB(ft2, hold)
		if err != nil || r.Start == nil || r.Start.Plan == nil || !rebooted {
			t.Fatalf("the resume by the hold: %v %+v\n%v", err, r, pn.shown)
		}
		if countdownShown(pn) || !shownHas(pn, "started step 2 once already") {
			t.Fatalf("a second countdown after the cut: %v", pn.shown)
		}
		p1 := r.Start.Plan
		if p1.Confirm.Auto || !p1.Confirm.Confirmed || p1.ID == p0.ID {
			t.Fatalf("the resume: auto %v confirmed %v, plan %s (the cut's %s)", p1.Confirm.Auto, p1.Confirm.Confirmed, p1.ID[:12], p0.ID[:12])
		}
		if at := attempts(u); at != step2ui.AttemptLine(m) {
			t.Fatalf("the attempt record %q", at)
		}
		u.finishRun(p1)
	})
	t.Run("a new send of another manifest: refused by the pins (the seal), nothing shown, nothing written", func(t *testing.T) {
		u, ft := newU(t)
		m := manifestSHA(t, u.payload)
		if r, _, _, err := u.serviceUSB(ft, back); err != nil || r.What != "not now" {
			t.Fatalf("the first send: %v %+v", err, r)
		}
		u.restartRAM(t)
		u.boot("a")
		// another manifest: the same files, the manifest with one more byte
		// at its end (a JSON reader still reads it; its sha256 is not pinned)
		other := filepath.Join(u.dir, "other", "uc-pxx-mainline")
		os.MkdirAll(other, 0o755)
		for _, n := range []string{mainline.ManifestFile, mainline.TemplateFile, mainline.RootfsFile} {
			b, _ := os.ReadFile(filepath.Join(u.payload, n))
			if n == mainline.ManifestFile {
				b = append(b, '\n')
			}
			os.WriteFile(filepath.Join(other, n), b, 0o644)
		}
		if mo := manifestSHA(t, other); mo == m || handover.Pinned(mainline.StepTwoManifests(u.b.Gate()), mo) {
			t.Fatalf("the other manifest %s is pinned", mo)
		}
		img := u.image()
		ft2 := &fakeTransport{u: u, src: other, auto: true}
		r, pn, _, err := u.serviceUSB(ft2, []step2ui.Key{step2ui.KeyNone})
		if err == nil || !strings.Contains(err.Error(), "not pinned") || r.Start != nil || !bytes.Equal(img, u.image()) {
			t.Fatalf("another manifest: %v %+v", err, r)
		}
		if countdownShown(pn) {
			t.Fatal("a countdown for another manifest")
		}
		if _, err := os.Lstat(u.root.Path(step2.StagePayload)); !os.IsNotExist(err) {
			t.Fatal("a folder was sealed")
		}
		if at := attempts(u); at != step2ui.AttemptLine(m) {
			t.Fatalf("the attempt record changed: %q", at)
		}
	})
}
