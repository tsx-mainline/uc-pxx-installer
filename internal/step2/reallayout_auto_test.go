//go:build install && step2 && devtest

package step2_test

// The END-USER step 2 end to end on the REAL UC-P10 layout (mainline-rc-7;
// owner 2026-11-13: the model of the unit-2 end-user e2e): the start is the
// image that a real-layout step 1 of the direct path left (ci/dryrun-real.py
// --shape direct --a-fw userdebug --b-fw userdebug --lock-state 0x00
// --lock-write 0x01 --keep-image: unit 2's E0, the BCB-route reset, the
// lock-state write, the END-USER STEP-2 payload armed in B). The unit has no
// supervised marker (the end-user STEP-2 payload). Then: B boots and marks
// itself; the step-2 SERVICE takes the end-user normal payload (RL_PAYLOAD)
// from its folder, with AUTO-STEP2 next to it (as the page's COMMIT {auto}
// leaves it): the 30 s countdown runs out with no key, step 2 runs by itself
// (plan auto true, state line supervised=false auto=true, the attempt line
// first), the unit restarts into A, A marks itself, the FINISH SERVICE (the
// finish executor's finish-service, as uc-pxx-usb runs it) finishes by itself
// (K = b: the done record), the end A{15,0,1} B{14,0,1}. With RL_CUTS, power
// cuts (clean and torn) at the start's A/B writes, step changes and data
// steps: each checked by install.CheckCutState, then the power comes back:
// the keeper boots, the service asks again WITHOUT the countdown (one
// automatic attempt per payload: the attempt record holds its manifest
// sha256) and the person's 3 s hold resumes it to the end. mainline-rc-8
// (RULES-PATCH-4-REVIEW MAJOR-2): before that resume, a RE-SEND: a new copy
// of the same payload (new files: other inodes and times, as a new USB send
// into the new RAM stage after the restart) with AUTO-STEP2 next to it gets
// no second countdown either (Back: not now, nothing written). RL_AUTO=1
// runs it (with RL_IMG, RL_SHAPE=direct, RL_PAYLOAD).
//
// mainline-rc-8: with RL_SSH_KEY (the public key that step 1 seeded into B: the
// image of the dry run e2e-E0-eu-key), B's root file system must hold exactly
// the seed (/data/tsx/panel.conf 0600 with the key, /data/tsx/setup-v2 0644
// "2") at the start, when the data migrate runs (uc-pxx-data-migrate copies
// the keeper's /data into A's userdata) and at every end: step 2 never
// changes the keeper's seed, so A gets the key by the migrate.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/drivestate"
	"uc-pxx-installer/internal/ext4"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/plan"
	"uc-pxx-installer/internal/sshkey"
	"uc-pxx-installer/internal/step2"
	"uc-pxx-installer/internal/step2ui"
	"uc-pxx-installer/internal/xfer/handover"
)

type autoFolder struct {
	once sync.Once
	dir  string
	err  error
}

// autoDrive: the folder of the first send; autoResend: a second copy (the
// re-send after a restart: the same bytes, new files).
var autoDrive, autoResend autoFolder

// autoPayload copies the payload folder once into a "drive" folder X/uc-pxx-mainline
// with X/AUTO-STEP2 next to it (the folder of the page's sealed stage).
func autoPayload(t *testing.T, src string) string { return autoCopy(t, &autoDrive, src) }

func autoCopy(t *testing.T, autoDrive *autoFolder, src string) string {
	autoDrive.once.Do(func() {
		d, err := os.MkdirTemp("", "auto-drive-")
		if err != nil {
			autoDrive.err = err
			return
		}
		pd := filepath.Join(d, "uc-pxx-mainline")
		if err := os.MkdirAll(pd, 0o755); err != nil {
			autoDrive.err = err
			return
		}
		for _, n := range []string{mainline.ManifestFile, mainline.TemplateFile, mainline.RootfsFile} {
			in, err := os.Open(filepath.Join(src, n))
			if err != nil {
				autoDrive.err = err
				return
			}
			out, err := os.Create(filepath.Join(pd, n))
			if err == nil {
				_, err = io.Copy(out, in)
				out.Close()
			}
			in.Close()
			if err != nil {
				autoDrive.err = err
				return
			}
		}
		autoDrive.err = os.WriteFile(filepath.Join(d, step2ui.AutoFile), []byte("the person chose on the page: continue by itself\n"), 0o444)
		autoDrive.dir = pd
	})
	if autoDrive.err != nil {
		t.Fatal(autoDrive.err)
	}
	return autoDrive.dir
}

// autoService runs the step-2 service once on the unit with these panel keys
// (none: the countdown runs out by itself).
func autoService(t testing.TB, u *unit, o step2.Options, keys []step2ui.Key) (*step2.ServiceResult, *fakePanel, error) {
	pn := &fakePanel{keys: make(chan step2ui.Key, 64), script: keys}
	c := &clock{t: timeOf(1700000000)}
	o.Payload, o.Confirmed = "", false
	o.Out = &bytes.Buffer{} // never a typed nil (RC-7 R-D MINOR-1)
	r, err := step2.Service(u.env(), o, step2.ServiceEnv{Panel: func() (step2.Panel, error) { return pn, nil }, Now: c.now,
		Sleep: func(time.Duration) {}, Reboot: func() error { return nil }, Hold: func() {}})
	return r, pn, err
}

func TestStep2RealLayoutAuto(t *testing.T) {
	if os.Getenv("RL_AUTO") == "" {
		t.Skip("RL_AUTO not set (the end-user automatic step 2 on the real layout)")
	}
	img, shape, payload := realInputs(t)
	if shape != "direct" {
		t.Fatal("RL_AUTO needs RL_SHAPE=direct (the end-user path: keeper B, the end-user STEP-2 payload)")
	}
	if os.Getenv("UC_PXX_TSX_COMMON") == "" {
		t.Fatal("UC_PXX_TSX_COMMON not set")
	}
	bin, exe := bootctlBin(t)
	cfg := realGate(t, payload)
	drive := autoPayload(t, payload)
	K := "b"
	u := newRealUnit(t, img, drive, cfg, K, bin, exe)
	autoSeed = seedFiles(t)
	checkSeed(t, u, "start (step 1's end)")
	if _, err := os.Lstat(u.root.Path(step2ui.SupervisedMarker)); err == nil {
		t.Fatal("the test unit has the supervised marker")
	}
	u.conf()
	a0 := u.ab()
	t.Logf("start (step 1's end): A %v B %v", a0.Slots[0], a0.Slots[1])
	u.mark(K)
	u.boot(K)
	before := nodeSums(t, u)
	misc0, _ := os.ReadFile(u.root.Node("misc"))
	img1 := filepath.Join(u.dir, "marked.img")
	if os.Getenv("RL_CUTS") != "" {
		saveImage(t, u, img1)
	}

	// the automatic run: no key, the countdown runs out
	var evs []install.Event
	o := u.opts(nil)
	o.Hooks = &install.Hooks{CutAfter: -1, Observe: func(e install.Event) error { evs = append(evs, e); return nil }}
	r, pn, err := autoService(t, u, o, nil)
	if err != nil || r.Start == nil || r.Start.Plan == nil {
		t.Fatalf("the automatic step 2: %v %+v\n%v", err, r, pn.shown)
	}
	p := r.Start.Plan
	if p.Keeper != K || p.Target != "a" || !p.Confirm.Auto {
		t.Fatalf("plan keeper %s target %s auto %v", p.Keeper, p.Target, p.Confirm.Auto)
	}
	counted := false
	for _, s := range pn.shown {
		if len(s) > 0 && strings.Contains(strings.Join(s, " "), "Starts by itself in") {
			counted = true
		}
	}
	st, _ := os.ReadFile(u.root.Path(step2.StateDir + "/" + step2.StateFile))
	at, _ := os.ReadFile(u.root.Path(step2.StateDir + "/" + step2.AttemptsFile))
	if !counted || !strings.Contains(string(st), "started "+p.ID+" supervised=false auto=true") || !strings.Contains(string(st), "armed "+p.ID) ||
		string(at) != step2ui.AttemptLine(p.Mainline.ManifestSHA256) {
		t.Fatalf("countdown shown %v, state %q, attempts %q (want the manifest sha256 %s)", counted, st, at, p.Mainline.ManifestSHA256)
	}
	if autoSeed != nil && !hasOp(u.data.ops, "migrate") {
		t.Fatalf("the automatic run did not run the data migrate: %v", u.data.ops)
	}
	ar := u.ab()
	t.Logf("automatic start: plan %s, %d steps, %d primitive writes, data %v; armed A %v B %v", p.ID[:12], len(p.Steps), len(evs), u.data.ops, ar.Slots[0], ar.Slots[1])
	autoFinish(t, u, p, before, misc0)

	nc, _ := strconv.Atoi(os.Getenv("RL_CUTS"))
	if nc == 0 {
		return
	}
	idx := map[int]bool{0: true, 1: true, len(evs) - 1: true}
	for i := 1; i < len(evs); i++ {
		if evs[i].Step != evs[i-1].Step || evs[i].Op == "ab" {
			idx[i] = true
		}
	}
	var pts []string
	for i := range idx {
		pts = append(pts, fmt.Sprintf("cut:%d", i))
	}
	sort.Slice(pts, func(i, j int) bool { a, _ := strconv.Atoi(pts[i][4:]); b, _ := strconv.Atoi(pts[j][4:]); return a < b })
	for len(pts) > nc {
		pts = append(pts[:len(pts)/2], pts[len(pts)/2+1:]...)
	}
	pts = append(pts, fmt.Sprintf("torn:%d", len(evs)/2), "data:format-mid", "data:migrate-end")
	for _, pt := range pts {
		t.Run(pt, func(t *testing.T) {
			u := newRealUnit(t, img1, drive, cfg, K, bin, exe)
			u.conf()
			o := u.opts(nil)
			kind, arg, _ := strings.Cut(pt, ":")
			if kind == "data" {
				u.data.cut = arg
			} else {
				k, _ := strconv.Atoi(arg)
				o.Hooks = &install.Hooks{CutAfter: k, Torn: kind == "torn"}
			}
			r, pn, err := autoService(t, u, o, nil)
			if err == nil || !(errors.Is(err, install.ErrPowerCut) || strings.Contains(err.Error(), errCut.Error())) {
				t.Fatalf("%s: the automatic start did not stop at the cut: %v %+v\n%v", pt, err, r, pn.shown)
			}
			if r == nil || r.Start == nil || r.Start.Plan == nil {
				t.Fatalf("%s: no plan of the automatic start", pt)
			}
			p0 := r.Start.Plan
			cs, err := install.CheckCutState(diskReader{u}, &p0.Plan, u.start)
			if err != nil {
				t.Fatalf("%s: the cardinal rule: %v (%+v)", pt, err, cs)
			}
			if cs.Boots != "keeper" {
				t.Fatalf("%s: a cut before the end boots the %s", pt, cs.Boots)
			}
			c := u.ab()
			t.Logf("%s: A %v B %v, boots the keeper %s", pt, c.Slots[0], c.Slots[1], p0.Keeper)
			checkSeed(t, u, pt+": the cut")
			// the power comes back: the keeper boots; the RAM stage is gone.
			// mainline-rc-8: the page sends the same payload again (new
			// files) with "continue by itself": no second countdown (the
			// attempt record holds the manifest sha256); Back: not now,
			// nothing written
			u.boot(p0.Keeper)
			u.data.cut = ""
			at0, _ := os.ReadFile(u.root.Path(step2.StateDir + "/" + step2.AttemptsFile))
			ab0, _ := u.root.ABSector()
			misc1 := fileSum(t, u.root.Node("misc"), 0, -1)
			resend := autoCopy(t, &autoResend, drive)
			if a, b := identOf(t, drive), identOf(t, resend); a == b {
				t.Fatalf("%s: the re-send has the same files: %s", pt, a)
			}
			u.payload = resend
			u.conf()
			r2, pn2, err := autoService(t, u, u.opts(nil), []step2ui.Key{step2ui.KeyBack})
			if err != nil || r2.What != "not now" || r2.Start != nil {
				t.Fatalf("%s: the re-send: %v %+v\n%v", pt, err, r2, pn2.shown)
			}
			resent := false
			for _, s := range pn2.shown {
				l := strings.Join(s, " ")
				if strings.Contains(l, "Starts by itself in") {
					t.Fatalf("%s: a second countdown for a re-send of the same payload", pt)
				}
				resent = resent || strings.Contains(l, "started step 2 once already")
			}
			at1, _ := os.ReadFile(u.root.Path(step2.StateDir + "/" + step2.AttemptsFile))
			ab1, _ := u.root.ABSector()
			if !resent || !bytes.Equal(at0, at1) || !bytes.Equal(ab0, ab1) || fileSum(t, u.root.Node("misc"), 0, -1) != misc1 {
				t.Fatalf("%s: the re-send: reason shown %v, attempts %q -> %q, A/B or misc changed %v", pt, resent, at0, at1, !bytes.Equal(ab0, ab1))
			}
			t.Logf("%s: the re-send of the same manifest (new files): no countdown, not now, nothing written", pt)
			// the person's 3 s hold resumes it, from the re-sent folder (the
			// first one is gone after a real restart: code review NIT-2); a
			// second press after the first (NIT-3: if a countdown ever came,
			// the first press only stops it and the check below fails)
			r1, pn1, err := autoService(t, u, u.opts(nil), []step2ui.Key{step2ui.KeyOKDown, step2ui.KeyNone, step2ui.KeyNone,
				step2ui.KeyNone, step2ui.KeyNone, step2ui.KeyOKDown, step2ui.KeyNone, step2ui.KeyNone, step2ui.KeyNone, step2ui.KeyNone})
			if err != nil || r1.Start == nil || r1.Start.Plan == nil {
				t.Fatalf("%s: the resume by the hold: %v %+v\n%v", pt, err, r1, pn1.shown)
			}
			for _, s := range pn1.shown {
				if strings.Contains(strings.Join(s, " "), "Starts by itself in") {
					t.Fatalf("%s: a second countdown for the same drive", pt)
				}
			}
			p1 := r1.Start.Plan
			if p1.ID == p0.ID || p1.Confirm.Auto {
				t.Fatalf("%s: the resume: plan %s (the cut's %s), auto %v", pt, p1.ID[:12], p0.ID[:12], p1.Confirm.Auto)
			}
			autoFinish(t, u, p1, before, misc0)
		})
	}
}

// autoFinish: A boots and marks itself; the finish executor's finish-service
// (staged into /run/uc-pxx-finish as uc-pxx-usb seals it) finishes by itself:
// K = b, the done record, no write; the end.
func autoFinish(t testing.TB, u *unit, p *plan.OtherSlotPlan, before map[string]string, misc0 []byte) {
	u.boot(p.Target)
	u.mark(p.Target)
	exe := stageFinish(t, u)
	log, err := runFinish(t, u, p.Keeper, exe, "finish-service")
	if err != nil || !strings.Contains(log, "finish: done") {
		t.Fatalf("finish-service (the executor in %s): %v\n%s", handover.FinishDir, err, log)
	}
	if _, err := os.Stat(filepath.Join(u.root.Path(step2.StateDir), step2.DoneFile)); err != nil {
		t.Fatalf("no done record after the finish service: %v\n%s", err, log)
	}
	t.Logf("the finish service ran by itself as its own process from %s (keeper %s)", handover.FinishDir, p.Keeper)
	os.RemoveAll(u.root.Path(handover.FinishDir))
	realEnd(t, u, p, before, misc0)
	checkSeed(t, u, "the end")
}

// identOf is the identity of the manifest file of a payload folder (lstat).
func identOf(t testing.TB, dir string) string {
	id, err := drivestate.Stat(filepath.Join(dir, mainline.ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	return id.String()
}

// saveImage writes the unit's disk to an image file (sparse).
func saveImage(t testing.TB, u *unit, path string) {
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	buf, zero := make([]byte, 1<<20), make([]byte, 1<<20)
	dr := diskReader{u}
	tot := device.Layout().DiskSectors * gpt.Sector
	f.Truncate(tot)
	for off := int64(0); off < tot; off += int64(len(buf)) {
		k := int64(len(buf))
		if tot-off < k {
			k = tot - off
		}
		if _, err := dr.ReadAt(buf[:k], off); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(buf[:k], zero[:k]) {
			f.WriteAt(buf[:k], off)
		}
	}
}

// autoSeed: the files of the key that step 1 seeded into B (RL_SSH_KEY), or
// nil (no key).
var autoSeed map[string]seedFile

type seedFile struct {
	perm uint16
	data []byte
}

func seedFiles(t testing.TB) map[string]seedFile {
	p := os.Getenv("RL_SSH_KEY")
	if p == "" {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	k, err := sshkey.Parse(b)
	if err != nil {
		t.Fatalf("RL_SSH_KEY: %v", err)
	}
	t.Logf("the seeded key: %s", k.Fingerprint)
	return map[string]seedFile{"/data/tsx/panel.conf": {0o600, k.PanelConf()}, "/data/tsx/setup-v2": {0o644, sshkey.SetupStamp()}}
}

// checkSeed reads B's root file system (system_b) of the unit and checks the
// seed's files: root's, the mode, the bytes; no link is followed.
func checkSeed(t testing.TB, u *unit, when string) {
	t.Helper()
	if autoSeed == nil {
		return
	}
	f, err := os.Open(u.root.Node("system_b"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	fs, err := ext4.Open(f, fi.Size())
	if err != nil {
		t.Fatalf("%s: system_b: %v", when, err)
	}
	for _, n := range []string{"/data/tsx/panel.conf", "/data/tsx/setup-v2"} {
		w := autoSeed[n]
		b, st, err := ext4.ReadFileNoFollow(fs, n, 1<<16)
		if err != nil || st.Type != "file" || st.Perm != w.perm || st.UID != 0 || st.GID != 0 || !bytes.Equal(b, w.data) {
			t.Fatalf("%s: B's %s: %v %+v (want %o, %d bytes, the key's)", when, n, err, st, w.perm, len(w.data))
		}
	}
	t.Logf("%s: B's root holds the seed (panel.conf 0600, setup-v2 0644)", when)
}

func hasOp(ops []string, op string) bool {
	for _, o := range ops {
		if o == op {
			return true
		}
	}
	return false
}
