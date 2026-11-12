//go:build install && step2 && devtest

package step2_test

// C2 of the step-2 USB transport (mainline-rc-4; STEP2-USB-BUILD §6): the
// service waits for the payload over the USB cable, restarts the transport
// after an unexpected end, seals the complete folder itself (root modes, the
// record's AUTO-STEP2, checked again), then asks the question on the panel
// and starts step 2 exactly as with a folder on a drive. A fake transport
// stands for uc-pxx-xfer: it writes the receiver's hand-over (the ready
// folder, its record, the state file) as the real receiver does.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/plan"
	"uc-pxx-installer/internal/step2"
	"uc-pxx-installer/internal/step2ui"
	"uc-pxx-installer/internal/xfer/handover"
)

type fakeProc struct {
	done chan struct{}
	once sync.Once
	stop int
}

func (p *fakeProc) Done() <-chan struct{} { return p.done }
func (p *fakeProc) Stop() error {
	p.stop++
	p.once.Do(func() { close(p.done) })
	return nil
}

type fakeTransport struct {
	u          *unit
	src        string // the payload to send (a uc-pxx-mainline folder)
	auto       bool
	failStarts int // payload starts that end at once
	after      int // polls of the state before the folder is complete
	edit       func(dir string)
	pins       []string

	takes, gives, mounts, unmounts, cleanups int
	statusStarts, payloadStarts              int
	status                                   *fakeProc
}

func (f *fakeTransport) stage() string { return f.u.root.Path(step2.StageDir) }
func (f *fakeTransport) Take() error   { f.takes++; return nil }
func (f *fakeTransport) Give() error   { f.gives++; return nil }
func (f *fakeTransport) MountStage() error {
	f.mounts++
	return os.MkdirAll(f.stage(), 0o711)
}
func (f *fakeTransport) UnmountStage() error { f.unmounts++; return nil }
func (f *fakeTransport) Cleanup() error      { f.cleanups++; return nil }
func (f *fakeTransport) UID() (int, error)   { return os.Getuid(), nil }
func (f *fakeTransport) NoProcessOf(int) error {
	return nil
}

func (f *fakeTransport) Start(statusOnly bool, pins []string) (step2.Proc, error) {
	p := &fakeProc{done: make(chan struct{})}
	if statusOnly {
		f.statusStarts++
		f.status = p
		return p, nil
	}
	f.payloadStarts++
	f.pins = pins
	xd := filepath.Join(f.stage(), "xfer")
	os.MkdirAll(xd, 0o700)
	if f.failStarts > 0 {
		f.failStarts--
		close(p.done) // the receiver ended by itself (an exit 1, an OOM kill)
		return p, nil
	}
	st, _ := json.Marshal(handover.State{State: handover.StateWaiting})
	os.WriteFile(filepath.Join(xd, handover.StateFile), st, 0o600)
	go func() {
		// the page sends; the folder is complete after f.after polls (about 1 s each)
		time.Sleep(time.Duration(f.after) * 1100 * time.Millisecond)
		cd := filepath.Join(xd, handover.ReadyDir)
		os.Mkdir(cd, 0o700)
		rec := handover.Record{Version: handover.RecordVersion, Auto: f.auto, Session: 1, Time: 1}
		for _, n := range []string{handover.ManifestName, handover.TemplateName, handover.RootfsName} {
			b, _ := os.ReadFile(filepath.Join(f.src, n))
			os.WriteFile(filepath.Join(cd, n), b, 0o600)
			// no t.Fatal here: this goroutine may outlive its test (review
			// MINOR-5); a short read only gives a record the seal refuses
			sum := sha256.Sum256(b)
			rec.Files = append(rec.Files, handover.ReadyFile{Name: n, Size: int64(len(b)), SHA256: hex.EncodeToString(sum[:])})
		}
		rec.ManifestSHA256 = rec.Files[0].SHA256
		if f.edit != nil {
			f.edit(cd)
		}
		rb, _ := json.Marshal(rec)
		os.WriteFile(filepath.Join(xd, handover.ReadyRecord), rb, 0o600)
		st, _ := json.Marshal(handover.State{State: handover.StateComplete, ManifestSHA: rec.ManifestSHA256})
		os.WriteFile(filepath.Join(xd, ".s"), st, 0o600)
		os.Rename(filepath.Join(xd, ".s"), filepath.Join(xd, handover.StateFile))
	}()
	return p, nil
}

func (u *unit) confUSB() {
	os.MkdirAll(u.root.Path("/etc/conf.d"), 0o755)
	c := "UC_PXX_STEP2_PAYLOAD=\"" + u.root.Path(step2.StagePayload) + "\"\nUC_PXX_STEP2_USB=yes\n"
	if err := os.WriteFile(u.root.Path(step2.ConfFile), []byte(c), 0o644); err != nil {
		u.t.Fatal(err)
	}
}

func (u *unit) serviceUSB(ft *fakeTransport, keys []step2ui.Key) (*step2.ServiceResult, *fakePanel, bool, error) {
	pn := &fakePanel{keys: make(chan step2ui.Key, 64), script: keys}
	c := &clock{t: time.Unix(1700000000, 0)}
	rebooted := false
	o := u.opts(&bytes.Buffer{})
	o.Payload, o.Confirmed = "", false
	o.InfoFile = step2.XferInfo
	r, err := step2.Service(u.env(), o, step2.ServiceEnv{Panel: func() (step2.Panel, error) { return pn, nil }, Now: c.now,
		Sleep: func(time.Duration) {}, Reboot: func() error { rebooted = true; return nil }, Hold: func() {}, Transport: ft})
	return r, pn, rebooted, err
}

func (u *unit) info() handover.Info {
	var in handover.Info
	b, _ := os.ReadFile(u.root.Path(step2.XferInfo))
	json.Unmarshal(b, &in)
	return in
}

func TestStep2ServiceUSB(t *testing.T) {
	bin, exe := bootctlBin(t)
	_, res := bundle()
	// the first key answers the receive screen; then the 3 s hold of OK
	hold := []step2ui.Key{step2ui.KeyNone, step2ui.KeyOKDown, step2ui.KeyNone, step2ui.KeyNone, step2ui.KeyNone, step2ui.KeyNone}
	newU := func(t *testing.T) (*unit, *fakeTransport) {
		u := newUnit(t, unitOf(res, shapes[0].ab, "a", "new"), "a", bin, exe)
		u.confUSB()
		// the sealed folder is 0555: give it back to the test's clean-up
		t.Cleanup(func() { os.Chmod(u.root.Path(step2.StagePayload), 0o755) })
		return u, &fakeTransport{u: u, src: u.payload}
	}
	sealed := func(t *testing.T, u *unit) {
		d := u.root.Path(step2.StagePayload)
		fi, err := os.Lstat(d)
		if err != nil || fi.Mode().Perm() != 0o555 {
			t.Fatalf("the sealed folder: %v %v", err, fi)
		}
		for _, n := range []string{handover.ManifestName, handover.TemplateName, handover.RootfsName} {
			fi, err := os.Lstat(filepath.Join(d, n))
			if err != nil || fi.Mode().Perm() != 0o444 || !fi.Mode().IsRegular() {
				t.Fatalf("%s: %v %v", n, err, fi)
			}
		}
		if _, err := os.Lstat(filepath.Join(u.root.Path(step2.StageDir), "xfer", handover.ReadyRecord)); !os.IsNotExist(err) {
			t.Fatalf("the record stays after the seal: %v", err)
		}
	}
	t.Run("the payload over the cable, sealed, the question, the start", func(t *testing.T) {
		u, ft := newU(t)
		ft.after = 2 // the folder appears after about 2 s (design §7 item 3)
		r, pn, rebooted, err := u.serviceUSB(ft, hold)
		if err != nil || r.Start == nil || !rebooted {
			t.Fatalf("%v %+v", err, r)
		}
		if ft.takes != 1 || ft.mounts != 1 || ft.payloadStarts != 1 || ft.statusStarts != 1 || ft.status.stop != 1 || ft.cleanups < 2 {
			t.Fatalf("transport %+v", ft)
		}
		if len(ft.pins) != 1 || ft.pins[0] != mainline.StepTwoManifests(u.b.Gate())[0] {
			t.Fatalf("pins %v", ft.pins)
		}
		sealed(t, u)
		if _, err := os.Lstat(filepath.Join(filepath.Dir(u.root.Path(step2.StagePayload)), step2ui.AutoFile)); !os.IsNotExist(err) {
			t.Fatal("AUTO-STEP2 without the person's choice")
		}
		if r.Start.Plan.Confirm.Auto || !r.Start.Plan.Confirm.Confirmed {
			t.Fatalf("confirm %+v", r.Start.Plan.Confirm)
		}
		if in := u.info(); in.Step2 != "restarting" {
			t.Fatalf("info %+v", in)
		}
		if !strings.Contains(strings.Join(pn.shown[0], " "), "connect the computer") {
			t.Fatalf("the first screen %v", pn.shown[0])
		}
	})
	// review MINOR-4: the keeper of a finished step 2 (K = a after the
	// switch: A{15,0,1} B{14,0,1}, A boots first) never takes the panel or
	// the gadget again; its own record names the armed plan and B is successful
	t.Run("the keeper after a finished step 2: no receive, no panel, no gadget", func(t *testing.T) {
		u, ft := newU(t)
		p, out, err := u.startRun(u.opts(nil))
		if err != nil {
			t.Fatalf("%v\n%s", err, out.String())
		}
		u.finishRun(p)
		u.boot("a")
		u.confUSB()
		r, pn, _, err := u.serviceUSB(ft, nil)
		if err != nil || !strings.Contains(r.What, "step 2 was done before") {
			t.Fatalf("%v %+v", err, r)
		}
		if ft.takes != 0 || ft.mounts != 0 || ft.payloadStarts != 0 || len(pn.shown) != 0 {
			t.Fatalf("the done keeper used the transport or the panel: %+v, %d screens", ft, len(pn.shown))
		}
	})
	t.Run("the person chose continue by itself: AUTO-STEP2 from the record, the countdown", func(t *testing.T) {
		u, ft := newU(t)
		ft.auto = true
		r, _, rebooted, err := u.serviceUSB(ft, []step2ui.Key{step2ui.KeyNone})
		if err != nil || r.Start == nil || !rebooted || !r.Start.Plan.Confirm.Auto {
			t.Fatalf("%v %+v", err, r)
		}
		if _, err := os.Lstat(filepath.Join(filepath.Dir(u.root.Path(step2.StagePayload)), step2ui.AutoFile)); err != nil {
			t.Fatalf("AUTO-STEP2: %v", err)
		}
	})
	t.Run("Back during the receive: nothing sealed, nothing written, the gadget given back", func(t *testing.T) {
		u, ft := newU(t)
		ft.after = 30
		r, pn, _, err := u.serviceUSB(ft, []step2ui.Key{step2ui.KeyBack})
		if err != nil || r.Start != nil || !strings.Contains(r.What, "no payload") || !pn.closed {
			t.Fatalf("%v %+v", err, r)
		}
		if ft.gives != 1 || ft.unmounts != 1 || ft.statusStarts != 0 || !bytes.Equal(u.image(), u.before) {
			t.Fatalf("transport %+v", ft)
		}
		if _, err := os.Lstat(u.root.Path(step2.StagePayload)); !os.IsNotExist(err) {
			t.Fatal("a folder was sealed")
		}
		if in := u.info(); in.Step2 != "not now" {
			t.Fatalf("info %+v", in)
		}
	})
	t.Run("the transport ends by itself twice, then the payload comes", func(t *testing.T) {
		u, ft := newU(t)
		ft.failStarts = 2
		r, _, _, err := u.serviceUSB(ft, hold)
		if err != nil || r.Start == nil || ft.payloadStarts != 3 || ft.cleanups < 4 {
			t.Fatalf("%v %+v %+v", err, r, ft)
		}
	})
	t.Run("the transport ends by itself too often: no step 2, nothing written", func(t *testing.T) {
		u, ft := newU(t)
		ft.failStarts = 10
		r, _, _, err := u.serviceUSB(ft, []step2ui.Key{step2ui.KeyNone})
		if err == nil || r.Start != nil || ft.payloadStarts != 4 || ft.gives != 1 || !bytes.Equal(u.image(), u.before) {
			t.Fatalf("%v %+v %+v", err, r, ft)
		}
		if in := u.info(); in.Step2 != "transport failed" {
			t.Fatalf("info %+v", in)
		}
	})
	t.Run("a ready folder that is not the record's: the seal refuses, nothing written", func(t *testing.T) {
		u, ft := newU(t)
		ft.edit = func(d string) {
			// a byte more after the record was made from the bytes: the size differs
			f, _ := os.OpenFile(filepath.Join(d, handover.RootfsName), os.O_APPEND|os.O_WRONLY, 0)
			f.Write([]byte{0})
			f.Close()
		}
		r, _, _, err := u.serviceUSB(ft, []step2ui.Key{step2ui.KeyNone})
		if err == nil || r.Start != nil || ft.gives != 1 || !bytes.Equal(u.image(), u.before) {
			t.Fatalf("%v %+v", err, r)
		}
		if _, err := os.Lstat(u.root.Path(step2.StagePayload)); !os.IsNotExist(err) {
			t.Fatal("a folder was sealed")
		}
	})
	t.Run("a supervised unit: no receive without the session's marker; with it, the seal and the CLI", func(t *testing.T) {
		u, ft := newU(t)
		os.MkdirAll(filepath.Dir(u.root.Path(step2ui.SupervisedMarker)), 0o755)
		os.WriteFile(u.root.Path(step2ui.SupervisedMarker), []byte("supervised\n"), 0o644)
		r, _, _, err := u.serviceUSB(ft, nil)
		if err != nil || ft.takes != 0 || !strings.Contains(r.What, "supervised") {
			t.Fatalf("%v %+v %+v", err, r, ft)
		}
		os.MkdirAll(filepath.Dir(u.root.Path(step2.USBReceiveMarker)), 0o755)
		os.WriteFile(u.root.Path(step2.USBReceiveMarker), []byte("session\n"), 0o644)
		r, pn, _, err := u.serviceUSB(ft, nil)
		if err != nil || ft.takes != 1 || r.Start != nil || !strings.Contains(r.What, "sealed in RAM") || len(pn.shown) != 0 {
			t.Fatalf("%v %+v %+v", err, r, ft)
		}
		sealed(t, u)
		if ft.statusStarts != 1 || ft.status.stop != 0 || ft.gives != 0 || !bytes.Equal(u.image(), u.before) {
			t.Fatalf("the status transport stays for the session: %+v", ft)
		}
	})
	t.Run("a supervised unit, GAP-1: the run started at boot waits for the session's marker, then receives", func(t *testing.T) {
		u, ft := newU(t)
		os.MkdirAll(filepath.Dir(u.root.Path(step2ui.SupervisedMarker)), 0o755)
		os.WriteFile(u.root.Path(step2ui.SupervisedMarker), []byte("supervised\n"), 0o644)
		os.RemoveAll(filepath.Dir(u.root.Path(step2.USBReceiveMarker)))
		pn := &fakePanel{keys: make(chan step2ui.Key, 64)}
		c := &clock{t: time.Unix(1700000000, 0)}
		o := u.opts(&bytes.Buffer{})
		o.Payload, o.Confirmed = "", false
		o.InfoFile = step2.XferInfo
		sleeps := 0
		sleep := func(time.Duration) {
			sleeps++
			if sleeps == 1 {
				// the service made the marker's folder (root's, 0755) before it waits
				fi, err := os.Stat(filepath.Dir(u.root.Path(step2.USBReceiveMarker)))
				if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o755 {
					t.Errorf("the marker folder: %v %v", fi, err)
				}
				if ft.takes != 0 {
					t.Error("the gadget was taken before the marker")
				}
			}
			if sleeps == 30 { // the session makes its marker a minute later
				os.WriteFile(u.root.Path(step2.USBReceiveMarker), []byte("session\n"), 0o644)
			}
		}
		r, err := step2.Service(u.env(), o, step2.ServiceEnv{Panel: func() (step2.Panel, error) { return pn, nil }, Now: c.now,
			Sleep: sleep, Reboot: func() error { t.Error("a reboot"); return nil }, Hold: func() {}, Transport: ft})
		if err != nil || ft.takes != 1 || r.Start != nil || !strings.Contains(r.What, "sealed in RAM") || len(pn.shown) != 0 || sleeps < 30 {
			t.Fatalf("%v %+v %+v sleeps %d", err, r, ft, sleeps)
		}
		sealed(t, u)
		if !bytes.Equal(u.image(), u.before) {
			t.Fatal("the eMMC changed")
		}
	})
	t.Run("a supervised unit, GAP-1: no marker in MaxMarkerWait: no receive, nothing written", func(t *testing.T) {
		u, ft := newU(t)
		os.MkdirAll(filepath.Dir(u.root.Path(step2ui.SupervisedMarker)), 0o755)
		os.WriteFile(u.root.Path(step2ui.SupervisedMarker), []byte("supervised\n"), 0o644)
		r, _, _, err := u.serviceUSB(ft, nil)
		if err != nil || ft.takes != 0 || !strings.Contains(r.What, "no session marker") || !bytes.Equal(u.image(), u.before) {
			t.Fatalf("%v %+v %+v", err, r, ft)
		}
	})
	t.Run("a supervised unit, GAP-1: the whole sequence: marker, receive, seal, plan, start --supervised with the GO lines, finish", func(t *testing.T) {
		u, ft := newU(t)
		os.MkdirAll(filepath.Dir(u.root.Path(step2ui.SupervisedMarker)), 0o755)
		os.WriteFile(u.root.Path(step2ui.SupervisedMarker), []byte("supervised\n"), 0o644)
		os.RemoveAll(filepath.Dir(u.root.Path(step2.USBReceiveMarker)))
		c := &clock{t: time.Unix(1700000000, 0)}
		o := u.opts(&bytes.Buffer{})
		o.Payload, o.Confirmed = "", false
		o.InfoFile = step2.XferInfo
		n := 0
		r, err := step2.Service(u.env(), o, step2.ServiceEnv{Now: c.now, Transport: ft, Hold: func() {},
			Reboot: func() error { t.Error("a reboot"); return nil },
			Sleep: func(time.Duration) {
				if n++; n == 5 {
					os.WriteFile(u.root.Path(step2.USBReceiveMarker), []byte("session\n"), 0o644)
				}
			}})
		if err != nil || ft.takes != 1 || r.Start != nil || !strings.Contains(r.What, "sealed in RAM") {
			t.Fatalf("the service: %v %+v %+v", err, r, ft)
		}
		sealed(t, u)
		if !bytes.Equal(u.image(), u.before) {
			t.Fatal("the receive wrote to the eMMC")
		}
		// the session's CLI on the sealed folder: plan, then start --supervised with the GO lines
		var out bytes.Buffer
		po := u.opts(&out)
		po.Payload = u.root.Path(step2.StagePayload)
		p, f, err := u.plan(po)
		if err != nil {
			t.Fatalf("plan on the sealed folder: %v\n%s", err, out.String())
		}
		po.PlanFile, po.Supervised = f, true
		po.Confirm = strings.NewReader(goLines(p, 99))
		if _, err := step2.Start(u.env(), po); err != nil || !strings.Contains(out.String(), "SUPERVISED: before step 1") {
			t.Fatalf("start --supervised: %v\n%s", err, out.String())
		}
		// the target boots and marks; the hand finish: --dry-run, then --expect-id
		u.finishRun(p)
	})
	t.Run("a sealed folder there already: no transport", func(t *testing.T) {
		u, ft := newU(t)
		d := u.root.Path(step2.StagePayload)
		os.MkdirAll(d, 0o755)
		for _, n := range []string{handover.ManifestName, handover.TemplateName, handover.RootfsName} {
			b, _ := os.ReadFile(filepath.Join(u.payload, n))
			os.WriteFile(filepath.Join(d, n), b, 0o444)
		}
		r, _, _, err := u.serviceUSB(ft, hold[1:])
		if err != nil || r.Start == nil || ft.takes != 0 {
			t.Fatalf("%v %+v %+v", err, r, ft)
		}
	})
}

// TestFinishServiceNotATarget: the finish executor of a normal build stops
// (ErrNotTarget) on a slot that no step-2 record names as its target, and
// writes nothing; the service goes on with the keeper's side.
func TestFinishServiceNotATarget(t *testing.T) {
	bin, exe := bootctlBin(t)
	_, res := bundle()
	u := newUnit(t, unitOf(res, shapes[0].ab, "a", "new"), "a", bin, exe)
	o := u.opts(&bytes.Buffer{})
	o.InfoFile = handover.USBInfo
	r, err := step2.FinishService(u.env(), o, step2.ServiceEnv{Sleep: func(time.Duration) {}})
	if !errors.Is(err, step2.ErrNotTarget) || !bytes.Equal(u.image(), u.before) {
		t.Fatalf("%v %+v", err, r)
	}
	var in handover.Info
	b, _ := os.ReadFile(u.root.Path(handover.USBInfo))
	if json.Unmarshal(b, &in) != nil || in.Step2 != "finish: not a target" {
		t.Fatalf("info %s", b)
	}
	if handover.StepTwoDone != step2.StateDir+"/"+step2.DoneFile {
		t.Fatalf("the done record %s, the service's %s/%s", handover.StepTwoDone, step2.StateDir, step2.DoneFile)
	}
	if handover.FinishDir+"/"+handover.FinishBootctl != "/run/uc-pxx-finish/uc-pxx-bootctl-inst" {
		t.Fatal("the finish build's r2 path is not the seal's")
	}
}

// TestStep2TargetWithoutFinish (mainline-rc-4; MAINLINE-RC3-2026-11-11 §7(b)):
// a step-2 target whose finish set never comes (a normal build has no
// uc-pxx-step2; the page never sends it) stays in safe states: after its own
// mark both slots are successful and bootable, and nothing writes the A/B
// sector again in later boots (K = a: A{14,0,1} B{15,0,1}, B first; K = b:
// A{15,0,1} B{14,0,1}, the designed end). A target that never marks runs out
// of tries: the keeper (successful) boots.
func TestStep2TargetWithoutFinish(t *testing.T) {
	bin, exe := bootctlBin(t)
	_, res := bundle()
	for _, sh := range []int{0, 2} {
		sh := shapes[sh]
		t.Run(sh.name, func(t *testing.T) {
			u := newUnit(t, unitOf(res, sh.ab, sh.k, sh.what), sh.k, bin, exe)
			p, out, err := u.startRun(u.opts(nil))
			if err != nil {
				t.Fatalf("%v\n%s", err, out.String())
			}
			K, T := p.Keeper, p.Target
			ki, ti := map[string]int{"a": 0, "b": 1}[K], map[string]int{"a": 0, "b": 1}[T]
			safe := func(what string) avbab.AB {
				a := u.ab()
				if a.Validate() != nil || !a.Slots[ki].Keeper() {
					t.Fatalf("%s: A %v B %v: the keeper %s is not successful and bootable", what, a.Slots[0], a.Slots[1], K)
				}
				return a
			}
			a := safe("armed")
			if a.Pick() != ti || a.Slots[ti].Successful != 0 {
				t.Fatalf("armed: A %v B %v", a.Slots[0], a.Slots[1])
			}
			// the target boots and marks itself; no finish executor ever comes
			u.boot(T)
			u.mark(T)
			sec1, _ := u.root.ABSector()
			a = safe("marked")
			if !a.Slots[ti].Keeper() || a.Pick() != ti {
				t.Fatalf("marked: A %v B %v: want both successful, %s first", a.Slots[0], a.Slots[1], T)
			}
			want := map[string][2]avbab.Slot{"a": {{Priority: 14, Successful: 1}, {Priority: 15, Successful: 1}},
				"b": {{Priority: 15, Successful: 1}, {Priority: 14, Successful: 1}}}[K]
			if a.Slots[0] != want[0] || a.Slots[1] != want[1] {
				t.Fatalf("marked: A %v B %v, want %v", a.Slots[0], a.Slots[1], want)
			}
			// more boots of the target with no finish: nothing writes the A/B sector
			for i := 0; i < 3; i++ {
				u.boot(T)
				if sec, _ := u.root.ABSector(); !bytes.Equal(sec, sec1) {
					t.Fatalf("boot %d of %s without the finish changed the A/B sector", i+2, T)
				}
			}
			t.Logf("K=%s: without the finish A %v B %v, %s first, both successful: safe; no A/B write", K, a.Slots[0], a.Slots[1], T)
		})
	}
	t.Run("the target never marks: its tries run out, the keeper boots", func(t *testing.T) {
		u := newUnit(t, unitOf(res, shapes[0].ab, "a", "new"), "a", bin, exe)
		p, out, err := u.startRun(u.opts(nil))
		if err != nil {
			t.Fatalf("%v\n%s", err, out.String())
		}
		sec, _ := u.root.ABSector()
		a, _ := avbab.Parse(sec)
		ti := map[string]int{"a": 0, "b": 1}[p.Target]
		// libavb: a trying slot with no tries left and not successful is unbootable
		var s [2][3]byte
		for k := 0; k < 2; k++ {
			s[k] = [3]byte{a.Slots[k].Priority, a.Slots[k].Tries, a.Slots[k].Successful}
		}
		s[ti] = [3]byte{0, 0, 0}
		fh, _ := os.OpenFile(u.root.Node("misc"), os.O_WRONLY, 0)
		fh.WriteAt(fakeemmc.ABSector(s, sec[32:]), (plan.ABLBA-plan.MiscStart)*512)
		fh.Close()
		b := u.ab()
		if b.Validate() != nil || !b.Slots[1-ti].Keeper() || b.Pick() != 1-ti {
			t.Fatalf("tries out: A %v B %v: the keeper must boot", b.Slots[0], b.Slots[1])
		}
	})
}
