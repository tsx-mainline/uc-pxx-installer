//go:build install && step2 && devtest

package step2_test

// Step 2 end to end on the REAL UC-P10 layout with a REAL payload
// (mainline-rc, MAINLINE-RC-2026-11-11.md §4; MAINLINE-STEP2-REST §6 item 4):
// the start is the image a real-layout step 1 left (ci/dryrun-real.py
// --keep-image: the direct path, keeper A stock, B mainline armed; or the
// userdebug-first path, keeper B userdebug, A mainline armed), the real r2
// under qemu, the real key record that step 1 kept (the release pin), the
// real payload with a gate that has the release's both-slot rules but the
// DEV-marker rule (the payload is a DEV build). Then: step 1's target boots
// and marks itself, step 2 runs from it (plan, start with the data steps,
// the arm), the other slot boots and marks itself, the finish, the end
// A{15,0,1} B{14,0,1}; every byte outside misc's A/B bytes 0..31, userdata
// and the target's six partitions is as at the start; the target holds the
// payload. With RL_CUTS, a sample of power cuts (clean and torn) and data
// cuts of the start, each checked by install.CheckCutState on the whole
// disk and resumed in a new boot to the end.
//
// Inputs (the test skips without them; nothing of them is in the repo):
//   RL_IMG     the step-1 image (sparse, 15.6 GB), read only (copied)
//   RL_SHAPE   direct (K = b) or ml (K = a): which slot step 1 armed
//   RL_PAYLOAD the uc-pxx-mainline folder of the payload step 1 installed
//   RL_CUTS    "" (the full run only) or a number of cut points

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/device/devicetest"
	"uc-pxx-installer/internal/factory/factorytest"
	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/plan"
	"uc-pxx-installer/internal/step2"
	"uc-pxx-installer/internal/xfer/handover"
)

type realCfg struct {
	gate  *mainline.Gate
	token string
	man   mainline.Manifest
}

func realInputs(t *testing.T) (img, shape, payload string) {
	img, shape, payload = os.Getenv("RL_IMG"), os.Getenv("RL_SHAPE"), os.Getenv("RL_PAYLOAD")
	if img == "" || payload == "" || (shape != "direct" && shape != "ml") {
		t.Skip("RL_IMG, RL_SHAPE (direct|ml) and RL_PAYLOAD not set (the real-layout step-2 dry run)")
	}
	return
}

// realGate: the payload listed for both slots, the release's slot-A pins, the
// release's both-slot rules without the DEV-marker rule, the reviewed vbmeta
// patches, the both-slot kernel list.
func realGate(t *testing.T, dir string) *realCfg {
	src, err := mainline.DirSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := mainline.Verify(src)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	tb := p.TemplateBytes()
	s := sha256.Sum256(tb)
	rev := mainline.Reviewed{Version: p.Manifest.Version, ManifestSHA256: p.ManifestSHA256, TemplateSHA256: hex.EncodeToString(s[:]),
		RootfsSHA256: p.Manifest.Rootfs.SHA256, Slots: "ab"}
	b := mainline.EmbeddedBothSlot()
	b.DevMarkers = nil
	g := mainline.TestGate([]mainline.Reviewed{rev}, map[string]string{}, mainline.EmbeddedBootControlPins()).WithBothSlot(b).
		WithVbmetaPatches(mainline.ReviewedVbmetaPatches())
	if r := g.Check(p); !r.OK() {
		t.Fatalf("the payload does not pass the gate: %+v", r.Checks)
	}
	return &realCfg{gate: g, token: p.Manifest.Token, man: p.Manifest}
}

func newRealUnit(t testing.TB, img, payload string, cfg *realCfg, slot string, bin []byte, exe string) *unit {
	dir := t.TempDir()
	root, err := devicetest.New(filepath.Join(dir, "root"), img, device.Layout(), bin, devicetest.Opts{NoMarker: true,
		Files: map[string]string{device.SlotBootctlPath: string(bin)}})
	if err != nil {
		t.Fatal(err)
	}
	// the factory block: the fake-emmc image of step 1 has marker bytes in cfg
	// (the real one is secret): a made-up UC-P10 block with a test MAC
	if f, err := os.OpenFile(root.Node("cfg"), os.O_WRONLY, 0); err == nil {
		f.WriteAt(factorytest.WithMAC(factorytest.Unit2Like(fakeemmc.DefaultFactoryModel), []byte{0x02, 0x11, 0x22, 0x33, 0x44, 0x55}), 0)
		f.Close()
	}
	un := &unit{t: t, dir: dir, root: root, payload: payload, saved: map[string]string{}, real: cfg}
	un.bc = &devicetest.Bootctl{Root: root, Exec: exe, Tmp: dir}
	un.data = &fakeData{u: un}
	// Quiet unstubbed (MAINLINE-RC2-LS-REVIEW A-2): LiveChecks' own Quiet
	// on this boot's eMMC view, the real tsx-autoupdate (get mode), /data as
	// uc-pxx-lvm mounts it in each boot (dataLine), the running root's own
	// hold folder read raw from the real root file system; the real lock.
	// Only the network is a stub (no eth0 in the test).
	realAutoupdate(t, root.Dir)
	un.dataModel = true
	lc := step2.LiveChecks(un.env(), nil)
	un.live = &step2.Live{NetworkUp: func() bool { return true },
		LockAutoupdate: lc.LockAutoupdate,
		Quiet: func() error {
			e := un.env()
			d, err := e.OpenEMMCSlot(device.EMMCPath)
			if err != nil {
				return err
			}
			err = step2.LiveChecks(e, d).Quiet()
			un.quiets.Add(1)
			if err != nil {
				un.quietErrs.Add(1)
			}
			return err
		}}
	un.boot(slot)
	un.start, _ = root.ABSector()
	return un
}

// lvmPhase1 writes an LVM2 physical volume label into vendor_a and oem_a
// (sector 1, as pvcreate does): uc-pxx-lvm's phase-1 tsx data volume of a
// mainline slot A (K = a).
func lvmPhase1(t testing.TB, u *unit) {
	for _, n := range []string{"vendor_a", "oem_a"} {
		f, err := os.OpenFile(u.root.Node(n), os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		b := make([]byte, 512)
		copy(b, "LABELONE")
		copy(b[24:], "LVM2 001")
		if _, err := f.WriteAt(b, 512); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}

// nodeSums: the sha256 of every partition node and of the disk node outside
// the partitions (the GPT, the idbloader area, the gaps).
func nodeSums(t testing.TB, u *unit) map[string]string {
	out := map[string]string{}
	for _, e := range device.Layout().Entries {
		out[e.Name] = fileSum(t, u.root.Node(e.Name), 0, -1)
	}
	return out
}

func fileSum(t testing.TB, p string, off, n int64) string {
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if n < 0 {
		st, _ := f.Stat()
		n = st.Size() - off
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.NewSectionReader(f, off, n)); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// diskReader reads the whole disk of a devicetest root: the partitions from
// their nodes, the rest from the disk node (as Root.Image composes it).
type diskReader struct{ u *unit }

func (d diskReader) ReadAt(b []byte, off int64) (int, error) {
	n := 0
	for n < len(b) {
		o := off + int64(n)
		src, so, lim := d.u.root.Path("/dev/mmcblk0"), o, int64(len(b)-n)
		for _, e := range device.Layout().Entries {
			s, l := e.Start*gpt.Sector, e.Size*gpt.Sector
			if o >= s && o < s+l {
				src, so = d.u.root.Node(e.Name), o-s
				if s+l-o < lim {
					lim = s + l - o
				}
				break
			} else if s > o && s-o < lim {
				lim = s - o
			}
		}
		f, err := os.Open(src)
		if err != nil {
			return n, err
		}
		k, err := f.ReadAt(b[n:n+int(lim)], so)
		f.Close()
		n += k
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// realEnd: A{15,0,1} B{14,0,1} A next; the target holds the payload's bytes
// (uboot/trust/dtbo the chain's, the patched vbmeta, the assembled boot, the
// root file system: each by the manifest's own sha256); every other node as
// at the start; misc changed only in bytes 0..31 of the A/B sector.
func realEnd(t testing.TB, u *unit, p *plan.OtherSlotPlan, before map[string]string, misc0 []byte) {
	a := u.ab()
	if a.Validate() != nil || a.Slots[0] != (avbab.Slot{Priority: 15, Successful: 1}) || a.Slots[1] != (avbab.Slot{Priority: 14, Successful: 1}) || a.Pick() != 0 {
		t.Fatalf("end state %v %v", a.Slots[0], a.Slots[1])
	}
	m := u.real.man
	T := p.Target
	want := map[string][2]string{ // node -> (sha, length "" = whole)
		"uboot_" + T:  {m.RequiresBootchain.UBootSHA256, ""},
		"trust_" + T:  {m.RequiresBootchain.TrustSHA256, ""},
		"dtbo_" + T:   {m.RequiresBootchain.DtboSHA256, ""},
		"vbmeta_" + T: {m.VbmetaPatched.SHA256, strconv.FormatInt(m.VbmetaPatched.Size, 10)},
		"boot_" + T:   {m.BootAssembled.SHA256, strconv.FormatInt(m.BootAssembled.Size, 10)},
		"system_" + T: {m.Rootfs.SHA256, strconv.FormatInt(m.Rootfs.Size, 10)},
	}
	for n, w := range want {
		l := int64(-1)
		if w[1] != "" {
			l, _ = strconv.ParseInt(w[1], 10, 64)
		}
		if got := fileSum(t, u.root.Node(n), 0, l); got != w[0] {
			t.Fatalf("%s holds %s, the payload's is %s", n, got[:16], w[0][:16])
		}
	}
	after := nodeSums(t, u)
	var changed []string
	for n, s := range before {
		if after[n] != s {
			changed = append(changed, n)
		}
	}
	sort.Strings(changed)
	for _, n := range changed {
		if _, ok := want[n]; !ok && n != "misc" && n != "userdata" {
			t.Fatalf("%s changed (changed: %v)", n, changed)
		}
	}
	misc1, _ := os.ReadFile(u.root.Node("misc"))
	ab := (plan.ABLBA - plan.MiscStart) * 512
	if !bytes.Equal(misc1[:ab], misc0[:ab]) || !bytes.Equal(misc1[ab+32:], misc0[ab+32:]) {
		t.Fatal("misc changed outside bytes 0..31 of the A/B sector")
	}
	if got := u.userdata(); got != "ext4 label "+plan.DataLabel {
		t.Fatalf("userdata holds %s", got)
	}
	t.Logf("end: A{15,0,1} B{14,0,1} A next; %s holds the payload (6 parts by the manifest's sha256); changed only %v", T, changed)
}

// realFinish: the target boots and marks itself; finish --dry-run, then the
// finish with its id; the end. The finish runs as on a unit (mainline-rc-4,
// owner Q-T8 = option 5): the target is a normal build with no uc-pxx-step2,
// so the finish executor (the devtest twin of uc-pxx-step2-finish, a separate
// process) and r2 are staged into the target's /run/uc-pxx-finish (root
// 0700, as the uc-pxx-usb seal leaves them) and run from there; r2 is read by
// the executor from that folder (the finish build's SlotBootctlPath).
func realFinish(t testing.TB, u *unit, p *plan.OtherSlotPlan, before map[string]string, misc0 []byte) {
	u.boot(p.Target)
	u.mark(p.Target)
	exe := stageFinish(t, u)
	out := filepath.Join(u.dir, "finish-plan.json")
	os.Remove(out)
	log, err := runFinish(t, u, p.Keeper, exe, "finish", "--device", "/dev/mmcblk0", "--dry-run", "--out", out)
	if err != nil {
		t.Fatalf("finish --dry-run (the executor in %s): %v\n%s", handover.FinishDir, err, log)
	}
	var fp plan.FinishPlan
	if b, err := os.ReadFile(out); err != nil || json.Unmarshal(b, &fp) != nil || !fp.VerifyID() {
		t.Fatalf("the finish plan of the dry run: %v", err)
	}
	log, err = runFinish(t, u, p.Keeper, exe, "finish", "--device", "/dev/mmcblk0", "--expect-id", fp.ID)
	if err != nil {
		t.Fatalf("finish --expect-id (the executor in %s): %v\n%s", handover.FinishDir, err, log)
	}
	// the done record of the finish on the target's root (both shapes)
	if _, err := os.Stat(filepath.Join(u.root.Path(step2.StateDir), step2.DoneFile)); err != nil {
		t.Fatalf("no done record after the finish: %v\n%s", err, log)
	}
	// K = b: the plan is "done" (no write); K = a: the switch (realEnd
	// checks the A/B end state A{15,0,1} B{14,0,1} that only the switch makes)
	if p.Keeper == "b" && !fp.Done || p.Keeper == "a" && fp.Done {
		t.Fatalf("finish of keeper %s: plan done %v\n%s", p.Keeper, fp.Done, log)
	}
	t.Logf("the finish ran as its own process from %s (keeper %s, plan %s)", handover.FinishDir, p.Keeper, fp.ID[:12])
	os.RemoveAll(u.root.Path(handover.FinishDir)) // RAM: gone at the next boot
	realEnd(t, u, p, before, misc0)
}

var finishTwin struct {
	once sync.Once
	path string
	err  error
}

// stageFinish builds the devtest twin of uc-pxx-step2-finish once and puts it
// and the real r2 into the target's /run/uc-pxx-finish (0700, files 0700).
func stageFinish(t testing.TB, u *unit) string {
	finishTwin.once.Do(func() {
		d, err := os.MkdirTemp("", "finish-twin-")
		if err != nil {
			finishTwin.err = err
			return
		}
		finishTwin.path = filepath.Join(d, handover.FinishBin)
		c := exec.Command("go", "build", "-tags", "install,step2,finish,devtest", "-trimpath", "-o", finishTwin.path, "uc-pxx-installer/cmd/uc-pxx-step2-finish")
		c.Env = append(os.Environ(), "CGO_ENABLED=0")
		if b, err := c.CombinedOutput(); err != nil {
			finishTwin.err = fmt.Errorf("go build the finish twin: %v\n%s", err, b)
		}
	})
	if finishTwin.err != nil {
		t.Fatal(finishTwin.err)
	}
	dir := u.root.Path(handover.FinishDir)
	os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	bin, _ := bootctlBin(t)
	tw, err := os.ReadFile(finishTwin.path)
	if err != nil {
		t.Fatal(err)
	}
	for n, b := range map[string][]byte{handover.FinishBin: tw, handover.FinishBootctl: bin} {
		if err := os.WriteFile(filepath.Join(dir, n), b, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// the target is a normal build: no r2 at the STEP-2 path (the executor
	// must take it from /run/uc-pxx-finish)
	os.Remove(u.root.Path("/usr/libexec/uc-pxx-step2/uc-pxx-bootctl-inst"))
	return filepath.Join(dir, handover.FinishBin)
}

// runFinish runs the staged executor as its own process on the unit's root.
func runFinish(t testing.TB, u *unit, keeper, exe string, args ...string) (string, error) {
	_, qemu := bootctlBin(t)
	c := exec.Command(exe, args...)
	c.Env = []string{"PATH=/usr/local/go/bin:/usr/sbin:/usr/bin:/sbin:/bin", "UC_PXX_FINISH_TEST_ROOT=" + u.root.Dir,
		"UC_PXX_FINISH_TEST_EXEC=" + qemu, "UC_PXX_FINISH_TEST_TMP=" + u.dir, "UC_PXX_FINISH_TEST_SAVED=" + u.saved[keeper],
		"UC_PXX_FINISH_TEST_K=" + keeper}
	c.Stdin = strings.NewReader("")
	b, err := c.CombinedOutput()
	return string(b), err
}

func TestStep2RealLayout(t *testing.T) {
	img, shape, payload := realInputs(t)
	if os.Getenv("UC_PXX_TSX_COMMON") == "" {
		t.Fatal("UC_PXX_TSX_COMMON not set: the real-layout run needs the real tsx-autoupdate for the unstubbed Quiet (A-2)")
	}
	bin, exe := bootctlBin(t)
	cfg := realGate(t, payload)
	K := map[string]string{"direct": "b", "ml": "a"}[shape]
	// step 1's target boots (the U-Boot slot word of K, root system_K) and
	// marks itself
	u := newRealUnit(t, img, payload, cfg, K, bin, exe)
	a0 := u.ab()
	t.Logf("start (step 1's end): A %v B %v", a0.Slots[0], a0.Slots[1])
	u.mark(K)
	if K == "a" {
		// phase 1: the mainline slot A has its /data on the LVM volume over
		// vendor_a and oem_a; K = b (A stock) has no tsx data file system
		lvmPhase1(t, u)
	}
	u.boot(K) // the boot of K after the mark: /data as uc-pxx-lvm mounts it
	pre := u.ab()
	t.Logf("after the mark of %s: A %v B %v", K, pre.Slots[0], pre.Slots[1])
	before := nodeSums(t, u)
	misc0, _ := os.ReadFile(u.root.Node("misc"))
	img1 := filepath.Join(u.dir, "marked.img")
	// the marked state as an image file for the cut points
	if os.Getenv("RL_CUTS") != "" {
		f, _ := os.Create(img1)
		var buf = make([]byte, 1<<20)
		zero := make([]byte, 1<<20)
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
		f.Close()
	}

	// the full run, counting its primitive writes
	var evs []install.Event
	o := u.opts(nil)
	o.Hooks = &install.Hooks{CutAfter: -1, Observe: func(e install.Event) error { evs = append(evs, e); return nil }}
	p, out, err := u.startRun(o)
	if err != nil {
		t.Fatalf("plan/start: %v\n%s", err, out.String())
	}
	if p.Keeper != K || p.Target != map[string]string{"a": "b", "b": "a"}[K] {
		t.Fatalf("keeper %s target %s", p.Keeper, p.Target)
	}
	ar := u.ab()
	t.Logf("start: plan %s, %d steps, %d primitive writes, data %v; armed A %v B %v", p.ID[:12], len(p.Steps), len(evs), u.data.ops, ar.Slots[0], ar.Slots[1])
	// a second plan in this state refuses (the target boots next)
	var o2b bytes.Buffer
	if _, _, err := u.plan(u.opts(&o2b)); err == nil {
		t.Fatal("a second step-2 plan after the arm was not refused")
	}
	realFinish(t, u, p, before, misc0)
	// a second finish: done, nothing written
	sums := nodeSums(t, u)
	var o3b bytes.Buffer
	var o3 = u.opts(&o3b)
	if fr3, err := step2.Finish(u.env(), o3); err != nil || !fr3.Plan.Done || fr3.Switched {
		t.Fatalf("a second finish: %v %+v", err, fr3)
	}
	if s2 := nodeSums(t, u); fmt.Sprint(s2) != fmt.Sprint(sums) {
		t.Fatal("a second finish wrote")
	}
	if u.quiets.Load() < 3 || u.quietErrs.Load() != 0 {
		t.Fatalf("the real Quiet ran %d times, %d refused", u.quiets.Load(), u.quietErrs.Load())
	}
	t.Logf("the real Quiet (LiveChecks: hold folders, the root's own raw, /data, get mode) ran %d times, every one ok", u.quiets.Load())

	nc, _ := strconv.Atoi(os.Getenv("RL_CUTS"))
	if nc == 0 {
		return
	}
	// the cut points: the first writes, every step change, every A/B write,
	// spread over the data writes; two torn; the data steps' cuts
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
			u := newRealUnit(t, img1, payload, cfg, K, bin, exe)
			o := u.opts(nil)
			kind, arg, _ := strings.Cut(pt, ":")
			if kind == "data" {
				u.data.cut = arg
			} else {
				k, _ := strconv.Atoi(arg)
				o.Hooks = &install.Hooks{CutAfter: k, Torn: kind == "torn"}
			}
			p0, out, err := u.startRun(o)
			if err == nil || !(errors.Is(err, install.ErrPowerCut) || strings.Contains(err.Error(), errCut.Error())) {
				t.Fatalf("%s: the start did not stop at the cut: %v\n%s", pt, err, out.String())
			}
			cs, err := install.CheckCutState(diskReader{u}, &p0.Plan, u.start)
			if err != nil {
				t.Fatalf("%s: the cardinal rule: %v (%+v)", pt, err, cs)
			}
			if cs.Boots != "keeper" {
				t.Fatalf("%s: a cut before the end boots the %s", pt, cs.Boots)
			}
			c := u.ab()
			t.Logf("%s: A %v B %v, boots the keeper %s", pt, c.Slots[0], c.Slots[1], p0.Keeper)
			// the power comes back: the keeper boots (a new boot); a new
			// plan resumes to the arm; the finish
			u.boot(p0.Keeper)
			u.data.cut = ""
			p1, out, err := u.startRun(u.opts(nil))
			if err != nil {
				t.Fatalf("%s: the resume: %v\n%s", pt, err, out.String())
			}
			if p1.ID == p0.ID {
				t.Fatalf("%s: the resume ran the plan of the boot before", pt)
			}
			realFinish(t, u, p1, before, misc0)
		})
	}
}
