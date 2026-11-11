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
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/device/devicetest"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/plan"
	"uc-pxx-installer/internal/step2"
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
	un := &unit{t: t, dir: dir, root: root, payload: payload, saved: map[string]string{}, real: cfg}
	un.bc = &devicetest.Bootctl{Root: root, Exec: exe, Tmp: dir}
	un.data = &fakeData{u: un}
	un.live = &step2.Live{NetworkUp: func() bool { return true },
		LockAutoupdate: func() (func(), error) { return func() {}, nil },
		Quiet:          func() error { return un.quiet }}
	un.boot(slot)
	un.start, _ = root.ABSector()
	return un
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
// finish with its id; the end.
func realFinish(t testing.TB, u *unit, p *plan.OtherSlotPlan, before map[string]string, misc0 []byte) {
	u.boot(p.Target)
	u.mark(p.Target)
	var out bytes.Buffer
	o := u.opts(&out)
	o.DryRun = true
	fr, err := step2.Finish(u.env(), o)
	if err != nil {
		t.Fatalf("finish --dry-run: %v\n%s", err, out.String())
	}
	o.DryRun, o.ExpectID = false, fr.Plan.ID
	fr2, err := step2.Finish(u.env(), o)
	if err != nil {
		t.Fatalf("finish: %v\n%s", err, out.String())
	}
	if p.Keeper == "a" && !fr2.Switched || p.Keeper == "b" && (fr2.Switched || !fr2.Plan.Done) {
		t.Fatalf("finish of keeper %s: switched %v done %v", p.Keeper, fr2.Switched, fr2.Plan.Done)
	}
	realEnd(t, u, p, before, misc0)
}

func TestStep2RealLayout(t *testing.T) {
	img, shape, payload := realInputs(t)
	bin, exe := bootctlBin(t)
	cfg := realGate(t, payload)
	K := map[string]string{"direct": "b", "ml": "a"}[shape]
	// step 1's target boots (the U-Boot slot word of K, root system_K) and
	// marks itself
	u := newRealUnit(t, img, payload, cfg, K, bin, exe)
	a0 := u.ab()
	t.Logf("start (step 1's end): A %v B %v", a0.Slots[0], a0.Slots[1])
	u.mark(K)
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
	if _, _, err := u.plan(u.opts(nil)); err == nil {
		t.Fatal("a second step-2 plan after the arm was not refused")
	}
	realFinish(t, u, p, before, misc0)
	// a second finish: done, nothing written
	sums := nodeSums(t, u)
	var o3 = u.opts(nil)
	if fr3, err := step2.Finish(u.env(), o3); err != nil || !fr3.Plan.Done || fr3.Switched {
		t.Fatalf("a second finish: %v %+v", err, fr3)
	}
	if s2 := nodeSums(t, u); fmt.Sprint(s2) != fmt.Sprint(sums) {
		t.Fatal("a second finish wrote")
	}

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
