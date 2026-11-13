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
// automatic attempt per drive) and the person's 3 s hold resumes it to the
// end. RL_AUTO=1 runs it (with RL_IMG, RL_SHAPE=direct, RL_PAYLOAD).

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
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/plan"
	"uc-pxx-installer/internal/step2"
	"uc-pxx-installer/internal/step2ui"
	"uc-pxx-installer/internal/xfer/handover"
)

var autoDrive struct {
	once sync.Once
	dir  string
	err  error
}

// autoPayload copies the payload folder once into a "drive" folder X/uc-pxx-mainline
// with X/AUTO-STEP2 next to it (the folder of the page's sealed stage).
func autoPayload(t *testing.T, src string) string {
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
		!strings.HasPrefix(string(at), "drive ") {
		t.Fatalf("countdown shown %v, state %q, attempts %q", counted, st, at)
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
			// the power comes back: the keeper boots; the service asks again
			// with no countdown (this drive's automatic attempt was made);
			// the person's 3 s hold resumes it
			u.boot(p0.Keeper)
			u.data.cut = ""
			r1, pn1, err := autoService(t, u, u.opts(nil), []step2ui.Key{step2ui.KeyOKDown})
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
