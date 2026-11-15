//go:build install && step2 && devtest

package step2_test

// mainline-rc-9 (U2-5 on unit 2, 2026-11-14): the end-user STEP-2 payload's
// root file system had no /var/lib/tsx; without /data nothing made it, so the
// start of tsx-autoupdate failed (`checkpath: /var/lib/tsx/autoupdate: could
// not open tsx`), and step 2's hold check (Live.Quiet → HoldsClear) refused.
// The service wrote the one-attempt line BEFORE the countdown and checked
// Quiet only in Start, after it: a refusal that was certain from the start
// used up the one automatic attempt. Now the service makes Start's live
// checks before the attempt line and the question (Start makes them again),
// and the payload ships /var/lib/tsx. The test helper unit.boot no longer
// makes the state folder in every boot (it hid the defect): it makes it only
// as tsx-autoupdate's start does (autoupdateInit).

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/ext4"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/step2"
	"uc-pxx-installer/internal/step2ui"
)

// noAttempt: the attempt record and every step-2 record are absent.
func noAttempt(t *testing.T, u *unit) {
	t.Helper()
	for _, f := range []string{step2.AttemptsFile, step2.StateFile, step2.RecordFile} {
		if _, err := os.Lstat(u.root.Path(step2.StateDir + "/" + f)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s/%s exists (%v): the refusal wrote it", step2.StateDir, f, err)
		}
	}
}

// The small fake unit: the hold folders of the test root read by the real
// code (HoldFoldersForTest); the root starts without /var/lib/tsx, as the
// root file system of the RC-8 STEP-2 payloads in a boot without /data.
func TestStep2ServiceNotReadyRC9(t *testing.T) {
	bin, exe := bootctlBin(t)
	_, res := bundle()
	newU := func(t *testing.T) *unit {
		u := newUnit(t, unitOf(res, shapes[0].ab, "a", "new"), "a", bin, exe)
		u.live.Quiet = func() error { return step2.HoldFoldersForTest(u.env()) }
		if _, err := os.Lstat(u.root.Path("/var/lib/tsx")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the test root has /var/lib/tsx: %v", err)
		}
		return u
	}
	tsxInit := func(t *testing.T, u *unit) {
		// the fixed payload's /var/lib/tsx, then tsx-autoupdate's start
		if err := os.MkdirAll(u.root.Path(filepath.Dir(step2.HoldsDir)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	auto := func(u *unit) {
		os.WriteFile(filepath.Join(filepath.Dir(u.payload), step2ui.AutoFile), nil, 0o644)
	}
	attempts := func(u *unit) string {
		b, _ := os.ReadFile(u.root.Path(step2.StateDir + "/" + step2.AttemptsFile))
		return string(b)
	}

	t.Run("AUTO-STEP2, no /var/lib/tsx: refused before the attempt line, nothing shown or written; then the fixed root runs", func(t *testing.T) {
		u := newU(t)
		u.conf()
		auto(u)
		r, pn, rebooted, err := u.service(nil)
		if err == nil || !strings.Contains(err.Error(), "/var/lib/tsx/autoupdate") || r == nil || !strings.Contains(r.What, "not ready") {
			t.Fatalf("want the refusal of the missing state folder: %v %+v", err, r)
		}
		if len(pn.shown) != 0 || rebooted || r.Start != nil {
			t.Fatalf("shown %v, rebooted %v, start %+v", pn.shown, rebooted, r.Start)
		}
		noAttempt(t, u)
		if !bytes.Equal(u.image(), u.before) {
			t.Fatal("the refusal wrote to the eMMC")
		}
		// the same unit with the folder (the fixed payload, tsx-autoupdate
		// started): the attempt is still there; the countdown runs out and
		// step 2 runs (the checks pass before the attempt line and in Start)
		tsxInit(t, u)
		u.boot("a")
		r, pn, rebooted, err = u.service(nil)
		if err != nil || r.Start == nil || !r.Start.Plan.Confirm.Auto || !rebooted || !countdownShown(pn) {
			t.Fatalf("with the folder: %v %+v", err, r)
		}
		if at := attempts(u); at != step2ui.AttemptLine(r.Start.Plan.Mainline.ManifestSHA256) {
			t.Fatalf("attempts %q", at)
		}
	})
	t.Run("a hold set: refused before the attempt line", func(t *testing.T) {
		u := newU(t)
		tsxInit(t, u)
		os.MkdirAll(u.root.Path(step2.HoldsDir), 0o755)
		os.WriteFile(u.root.Path(step2.HoldsDir+"/"+step2.StopHoldOwner), []byte("a STOP (test)\n"), 0o644)
		u.conf()
		auto(u)
		r, pn, _, err := u.service(nil)
		if err == nil || !strings.Contains(err.Error(), "hold") || len(pn.shown) != 0 || !strings.Contains(r.What, "not ready") {
			t.Fatalf("%v %+v %v", err, r, pn.shown)
		}
		noAttempt(t, u)
		if !bytes.Equal(u.image(), u.before) {
			t.Fatal("the refusal wrote to the eMMC")
		}
	})
	t.Run("no AUTO-STEP2 (the 3 s hold): the question is not shown either", func(t *testing.T) {
		u := newU(t)
		u.conf()
		hold := []step2ui.Key{step2ui.KeyOKDown, step2ui.KeyNone, step2ui.KeyNone, step2ui.KeyNone, step2ui.KeyNone}
		r, pn, _, err := u.service(hold)
		if err == nil || !strings.Contains(err.Error(), "/var/lib/tsx/autoupdate") || len(pn.shown) != 0 || r.Start != nil {
			t.Fatalf("%v %+v %v", err, r, pn.shown)
		}
		noAttempt(t, u)
		if !bytes.Equal(u.image(), u.before) {
			t.Fatal("the refusal wrote to the eMMC")
		}
	})
	t.Run("the queue takes no FUA: refused before the attempt line", func(t *testing.T) {
		u := newU(t)
		tsxInit(t, u)
		fs, _ := filepath.Glob(u.root.Path("/sys/devices/platform/*/mmc_host/mmc0/mmc0:0001/block/mmcblk0/queue/fua"))
		if len(fs) == 0 {
			t.Fatal("no fua file in the test root")
		}
		for _, f := range fs {
			os.WriteFile(f, []byte("0\n"), 0o644)
		}
		u.conf()
		auto(u)
		r, pn, _, err := u.service(nil)
		if err == nil || !strings.Contains(err.Error(), "FUA") || len(pn.shown) != 0 || !strings.Contains(r.What, "not ready") {
			t.Fatalf("%v %+v %v", err, r, pn.shown)
		}
		noAttempt(t, u)
	})
	t.Run("tsx-autoupdate busy for a while: the service waits, then asks", func(t *testing.T) {
		u := newU(t)
		tsxInit(t, u)
		calls := 0
		u.live.LockAutoupdate = func() (func(), error) {
			calls++
			if calls <= 3 {
				return nil, errors.New("tsx-autoupdate runs now (test)")
			}
			return func() {}, nil
		}
		u.conf()
		r, _, _, err := u.service([]step2ui.Key{step2ui.KeyBack})
		if err != nil || r.What != "not now" || calls < 4 {
			t.Fatalf("%v %+v, lock calls %d", err, r, calls)
		}
	})
	t.Run("tsx-autoupdate busy longer than LockWait: refused, no attempt line", func(t *testing.T) {
		u := newU(t)
		tsxInit(t, u)
		u.locked = true
		u.conf()
		auto(u)
		r, pn, _, err := u.service(nil)
		if err == nil || !strings.Contains(err.Error(), "tsx-autoupdate runs") || len(pn.shown) != 0 || !strings.Contains(r.What, "not ready") {
			t.Fatalf("%v %+v %v", err, r, pn.shown)
		}
		noAttempt(t, u)
	})
	t.Run("a cron run of tsx-autoupdate after the attempt line: Start waits for the lock, step 2 runs", func(t *testing.T) {
		u := newU(t)
		tsxInit(t, u)
		calls := 0
		// call 1: the check before the attempt line (free); calls 2..6: Start,
		// after the countdown, finds the lock held; then it is free
		u.live.LockAutoupdate = func() (func(), error) {
			calls++
			if calls >= 2 && calls <= 6 {
				return nil, errors.New("tsx-autoupdate runs now (test)")
			}
			return func() {}, nil
		}
		u.conf()
		auto(u)
		r, pn, rebooted, err := u.service(nil)
		if err != nil || r.Start == nil || !r.Start.Plan.Confirm.Auto || !rebooted || !countdownShown(pn) || calls < 7 {
			t.Fatalf("%v %+v, lock calls %d", err, r, calls)
		}
		if at := attempts(u); at != step2ui.AttemptLine(r.Start.Plan.Mainline.ManifestSHA256) {
			t.Fatalf("attempts %q", at)
		}
	})
	t.Run("the finish in B: a busy lock is waited for, then the finish runs (not a refusal)", func(t *testing.T) {
		u := newU(t)
		tsxInit(t, u)
		u.conf()
		hold := []step2ui.Key{step2ui.KeyOKDown, step2ui.KeyNone, step2ui.KeyNone, step2ui.KeyNone, step2ui.KeyNone}
		r, _, rebooted, err := u.service(hold)
		if err != nil || r.Start == nil || !rebooted {
			t.Fatalf("%v %+v", err, r)
		}
		p := r.Start.Plan
		u.boot("b")
		u.mark("b")
		calls := 0
		u.live.LockAutoupdate = func() (func(), error) {
			calls++
			if calls <= 4 {
				return nil, errors.New("tsx-autoupdate runs now (test)")
			}
			return func() {}, nil
		}
		r, _, _, err = u.service(nil)
		if err != nil || r.Finish == nil || !r.Finish.Switched || calls < 5 {
			t.Fatalf("finish with a busy lock: %v %+v, lock calls %d", err, r, calls)
		}
		u.endState(p)
	})
	t.Run("over the USB cable: refused before the receive (no gadget taken, no send possible; the service's own info file has plan-refused, which no transport serves here: re-look MINOR-R1)", func(t *testing.T) {
		u := newU(t)
		u.confUSB()
		t.Cleanup(func() { u.restartRAM(t) })
		ft := &fakeTransport{u: u, src: u.payload, auto: true}
		r, pn, rebooted, err := u.serviceUSB(ft, []step2ui.Key{step2ui.KeyNone})
		if err == nil || !strings.Contains(err.Error(), "/var/lib/tsx/autoupdate") || rebooted || !strings.Contains(r.What, "no receive") {
			t.Fatalf("%v %+v", err, r)
		}
		if ft.takes != 0 || ft.payloadStarts != 0 || ft.mounts != 0 || len(pn.shown) != 0 {
			t.Fatalf("the receive started: takes %d, payload starts %d, mounts %d, shown %v", ft.takes, ft.payloadStarts, ft.mounts, pn.shown)
		}
		if in := u.info(); in.Step2 != "plan-refused: the unit is not ready" {
			t.Fatalf("status %q %v", in.Step2, in.Lines)
		}
		noAttempt(t, u)
		if !bytes.Equal(u.image(), u.before) {
			t.Fatal("the refusal wrote to the eMMC")
		}
	})
	t.Run("over the USB cable: a hold set during the receive: refused after the seal, before the attempt line", func(t *testing.T) {
		u := newU(t)
		tsxInit(t, u)
		n := 0
		u.live.Quiet = func() error {
			n++
			if n == 1 {
				return step2.HoldFoldersForTest(u.env())
			}
			return errors.New("tsx-autoupdate has a hold set (test, after the receive)")
		}
		u.confUSB()
		t.Cleanup(func() { u.restartRAM(t) })
		ft := &fakeTransport{u: u, src: u.payload, auto: true}
		r, pn, rebooted, err := u.serviceUSB(ft, []step2ui.Key{step2ui.KeyNone})
		if err == nil || !strings.Contains(err.Error(), "hold set (test") || rebooted || !strings.Contains(r.What, "not ready") || ft.payloadStarts != 1 {
			t.Fatalf("%v %+v, payload starts %d", err, r, ft.payloadStarts)
		}
		if in := u.info(); in.Step2 != "plan-refused: the unit is not ready" {
			t.Fatalf("status %q %v", in.Step2, in.Lines)
		}
		if countdownShown(pn) || shownHas(pn, "Hold OK") {
			t.Fatalf("the question was shown: %v", pn.shown)
		}
		noAttempt(t, u)
		if !bytes.Equal(u.image(), u.before) {
			t.Fatal("the refusal wrote to the eMMC")
		}
	})
	t.Run("over the USB cable: Back pressed while the check waits: not now, before the attempt line", func(t *testing.T) {
		u := newU(t)
		tsxInit(t, u)
		n := 0
		u.live.LockAutoupdate = func() (func(), error) {
			n++
			if n >= 2 && n <= 4 { // the check after the seal waits
				return nil, errors.New("tsx-autoupdate runs now (test)")
			}
			return func() {}, nil
		}
		u.confUSB()
		t.Cleanup(func() { u.restartRAM(t) })
		ft := &fakeTransport{u: u, src: u.payload, auto: true}
		pn := &fakePanel{keys: make(chan step2ui.Key, 64)}
		var shown []string
		pnw := &backOnWait{fakePanel: pn, onShow: func(l []string) {
			shown = append(shown, strings.Join(l, " "))
			if strings.Contains(strings.Join(l, " "), "checks the unit") {
				pn.keys <- step2ui.KeyBack
			}
		}}
		c := &clock{t: timeOf(1700000000)}
		o := u.opts(&bytes.Buffer{})
		o.Payload, o.Confirmed, o.InfoFile = "", false, step2.XferInfo
		r, err := step2.Service(u.env(), o, step2.ServiceEnv{Panel: func() (step2.Panel, error) { return pnw, nil }, Now: c.now,
			Sleep: func(time.Duration) {}, Reboot: func() error { t.Error("a reboot"); return nil }, Hold: func() {}, Transport: ft})
		if err != nil || r.What != "not now" {
			t.Fatalf("%v %+v %q", err, r, shown)
		}
		for _, l := range shown {
			if strings.Contains(l, "Starts by itself") || strings.Contains(l, "Hold OK") {
				t.Fatalf("the question was shown: %q", shown)
			}
		}
		noAttempt(t, u)
		if in := u.info(); in.Step2 != "not now" {
			t.Fatalf("status %q", in.Step2)
		}
	})
	t.Run("the A/B lock held by another writer: the service waits, then asks", func(t *testing.T) {
		u := newU(t)
		tsxInit(t, u)
		u.conf()
		lp := u.root.Path(device.ABLockPath)
		os.MkdirAll(filepath.Dir(lp), 0o755)
		f, err := os.OpenFile(lp, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			t.Fatal(err)
		}
		sleeps := 0
		pn := &fakePanel{keys: make(chan step2ui.Key, 64), script: []step2ui.Key{step2ui.KeyBack}}
		c := &clock{t: timeOf(1700000000)}
		o := u.opts(&bytes.Buffer{})
		o.Payload, o.Confirmed = "", false
		r, err := step2.Service(u.env(), o, step2.ServiceEnv{Panel: func() (step2.Panel, error) { return pn, nil }, Now: c.now,
			Sleep: func(time.Duration) {
				if sleeps++; sleeps == 3 {
					syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				}
			}, Reboot: func() error { return nil }, Hold: func() {}})
		if err != nil || r.What != "not now" || sleeps < 3 {
			t.Fatalf("%v %+v, sleeps %d", err, r, sleeps)
		}
	})
	t.Run("the finish executor (finish-service, its own process): a busy lock is waited for, then the switch", func(t *testing.T) {
		u := newU(t)
		tsxInit(t, u)
		u.conf()
		hold := []step2ui.Key{step2ui.KeyOKDown, step2ui.KeyNone, step2ui.KeyNone, step2ui.KeyNone, step2ui.KeyNone}
		r, _, rebooted, err := u.service(hold)
		if err != nil || r.Start == nil || !rebooted {
			t.Fatalf("%v %+v", err, r)
		}
		p := r.Start.Plan
		u.boot("b")
		u.mark("b")
		calls := 0
		u.live.LockAutoupdate = func() (func(), error) {
			calls++
			if calls <= 4 {
				return nil, errors.New("tsx-autoupdate runs now (test)")
			}
			return func() {}, nil
		}
		c := &clock{t: timeOf(1700000000)}
		o := u.opts(&bytes.Buffer{})
		o.Payload, o.Confirmed = "", false
		fr, err := step2.FinishService(u.env(), o, step2.ServiceEnv{Now: c.now, Sleep: func(time.Duration) {}, Hold: func() {}})
		if err != nil || fr.Finish == nil || !fr.Finish.Switched || calls < 5 {
			t.Fatalf("finish-service with a busy lock: %v %+v, lock calls %d", err, fr, calls)
		}
		u.endState(p)
	})
}

// backOnWait is a fake panel that calls onShow at every Show.
type backOnWait struct {
	*fakePanel
	onShow func([]string)
}

func (p *backOnWait) Show(l []string) { p.fakePanel.Show(l); p.onShow(l) }

// TestStep2RealLayoutTsxDirRC9: the real layout, the real payload, the real
// Quiet (LiveChecks: the hold folders, the running root's own hold folder
// read raw, /data, the real tsx-autoupdate get mode). RL_TSXDIR says what the
// inputs are:
//   - missing: the image of an RC-8 step 1 (B = 0.2.0-rc8-eu-step2, no
//     /var/lib/tsx) and the RC-8 eu payload: B boots with no /data, so the
//     state folder of tsx-autoupdate is missing (autoupdateInit): the service
//     refuses before the attempt line: no question, no countdown, no record,
//     no byte of any partition changed. This is U2-5's state on unit 2.
//   - present: an image of a step 1 with the fixed STEP-2 payload and the
//     fixed eu payload: both root file systems hold /var/lib/tsx (root, 0755,
//     empty); the checks pass, the countdown is shown, Back stops it (not now,
//     nothing written), and the attempt line is the manifest sha256.
//
// Run with RL_TSXDIR, RL_IMG, RL_SHAPE=direct, RL_PAYLOAD, UC_PXX_TSX_COMMON.
func TestStep2RealLayoutTsxDirRC9(t *testing.T) {
	want := os.Getenv("RL_TSXDIR")
	if want == "" {
		t.Skip("RL_TSXDIR not set (missing|present)")
	}
	if want != "missing" && want != "present" {
		t.Fatalf("RL_TSXDIR %q: missing or present", want)
	}
	img, shape, payload := realInputs(t)
	if shape != "direct" {
		t.Fatal("RL_TSXDIR needs RL_SHAPE=direct (keeper B)")
	}
	if os.Getenv("UC_PXX_TSX_COMMON") == "" {
		t.Fatal("UC_PXX_TSX_COMMON not set")
	}
	bin, exe := bootctlBin(t)
	cfg := realGate(t, payload)
	drive := autoPayload(t, payload)
	K := "b"
	u := newRealUnit(t, img, drive, cfg, K, bin, exe)
	check := func(what, file string) {
		st, err := tsxDirOf(file)
		switch want {
		case "missing":
			if !errors.Is(err, ext4.ErrNotFound) {
				t.Fatalf("%s: /var/lib/tsx %+v %v, want none", what, st, err)
			}
		case "present":
			if err != nil || st.Type != "dir" || st.Perm != 0o755 || st.UID != 0 || st.GID != 0 {
				t.Fatalf("%s: /var/lib/tsx %+v %v, want a folder root:root 0755", what, st, err)
			}
		}
		t.Logf("%s: /var/lib/tsx %s (%+v)", what, want, st)
	}
	check("the keeper's root (system_b)", u.root.Node("system_b"))
	check("the payload's rootfs (slot A of step 2)", filepath.Join(payload, mainline.RootfsFile))
	if _, err := os.Lstat(u.root.Path(step2ui.SupervisedMarker)); err == nil {
		t.Fatal("the test unit has the supervised marker")
	}
	u.conf()
	u.mark(K)
	u.boot(K)
	_, serr := os.Lstat(u.root.Path(filepath.Dir(step2.HoldsDir)))
	if (want == "missing") != errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("the state folder of tsx-autoupdate after the boot: %v (want %s)", serr, want)
	}
	before := nodeSums(t, u)
	ab0, _ := u.root.ABSector()
	o := u.opts(nil)
	switch want {
	case "missing":
		r, pn, err := autoService(t, u, o, nil)
		if err == nil || !strings.Contains(err.Error(), "/var/lib/tsx/autoupdate") || r == nil || !strings.Contains(r.What, "not ready") || r.Start != nil {
			t.Fatalf("want the refusal of the missing state folder: %v %+v", err, r)
		}
		if len(pn.shown) != 0 {
			t.Fatalf("the question was shown: %v", pn.shown)
		}
		if u.quietErrs.Load() < 1 {
			t.Fatalf("the real Quiet did not refuse (%d calls)", u.quiets.Load())
		}
		t.Logf("refused: %v", err)
	case "present":
		r, pn, err := autoService(t, u, o, []step2ui.Key{step2ui.KeyBack})
		if err != nil || r.What != "not now" || !countdownShown(pn) {
			t.Fatalf("the countdown and Back: %v %+v %v", err, r, pn.shown)
		}
		at, _ := os.ReadFile(u.root.Path(step2.StateDir + "/" + step2.AttemptsFile))
		mb, err := os.ReadFile(filepath.Join(payload, mainline.ManifestFile))
		if err != nil {
			t.Fatal(err)
		}
		ms := sha256.Sum256(mb)
		if string(at) != step2ui.AttemptLine(hex.EncodeToString(ms[:])) {
			t.Fatalf("attempts %q, want the manifest sha256 %x", at, ms)
		}
		if u.quiets.Load() < 1 || u.quietErrs.Load() != 0 {
			t.Fatalf("the real Quiet ran %d times, %d refused", u.quiets.Load(), u.quietErrs.Load())
		}
		t.Logf("the checks passed; attempt line %q", strings.TrimSpace(string(at)))
	}
	if want == "missing" {
		noAttempt(t, u)
	} else {
		for _, f := range []string{step2.StateFile, step2.RecordFile} {
			if _, err := os.Lstat(u.root.Path(step2.StateDir + "/" + f)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s exists after Back: %v", f, err)
			}
		}
	}
	ab1, _ := u.root.ABSector()
	if !bytes.Equal(ab0, ab1) {
		t.Fatal("the A/B sector changed")
	}
	after := nodeSums(t, u)
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("partition %s changed", k)
		}
	}
}
