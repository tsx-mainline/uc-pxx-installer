//go:build install && step2 && devtest

package step2_test

// Step 2 on a fake unit (package devicetest: every device node a regular
// file; sysfs, the device tree and /proc files of a temporary root) with the
// real uc-pxx-bootctl-inst r2 (UC_PXX_BOOTCTL, under UC_PXX_BOOTCTL_EXEC on
// a PC; skipped without it): plan, start, the restart into the target, its
// mark, the finish with r2's switch; the power-cut sweep (a cut before every
// primitive write of the start, clean and torn, and at four points inside
// the data steps), each point checked with install.CheckCutState and resumed
// by a new boot of the keeper to the end state A{15,0,1} B{14,0,1}; the
// supervised GO lines; the refusals.
//
// Run: go test -tags install,step2,devtest ./internal/step2/

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/device/devicetest"
	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/keykeep"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/mainline/mltest"
	"uc-pxx-installer/internal/plan"
	"uc-pxx-installer/internal/step2"
	"uc-pxx-installer/internal/step2ui"
)

const tok, linux = "mltest-s2", 30000

var (
	fx    = fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	small = fakeemmc.ParseSmallLayout(fakeemmc.SmallLayout)
	pins  = []plan.KeyPin{{Firmware: "the test fixture", RecordSHA256: keykeep.SHA256(keykeep.Encode(fx.Keys))}}
)

func bootctlBin(t testing.TB) ([]byte, string) {
	p := os.Getenv("UC_PXX_BOOTCTL")
	if p == "" {
		t.Skip("UC_PXX_BOOTCTL not set: this test runs the real uc-pxx-bootctl-inst")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if devicetest.SHA256(b) != device.BootctlSHA256 {
		t.Fatalf("%s is not the reviewed binary %s", p, device.BootctlSHA256)
	}
	return b, os.Getenv("UC_PXX_BOOTCTL_EXEC")
}

// bundle is the made-up payload whose required chain is the fixture's.
func bundle() (*mltest.Bundle, []byte) {
	res, err := mainline.ReadResource(bytes.NewReader(fx.New["boot"]), 0, int64(len(fx.New["boot"])))
	if err != nil {
		panic(err)
	}
	m := fx.Manifests[0]
	var cap int64
	for _, d := range m.AVB.Descriptors {
		if d.Partition == "boot" {
			cap = int64(d.ImageSize)
		}
	}
	ch := mltest.Chain{Version: m.Firmware.Version, BuildType: m.Build["ro.build.type"], UBoot: fx.ManifestPartSHA("uboot"),
		Trust: fx.ManifestPartSHA("trust"), Dtbo: fx.ManifestPartSHA("dtbo"), Vbmeta: fx.ManifestPartSHA("vbmeta"),
		Resource: mltest.SHA(res)}
	ps := cap + 64<<10
	b := mltest.NewBundle(tok, linux, 1<<20, ch, res, ps)
	if _, err := b.PatchVbmeta(fx.New["vbmeta"], ps); err != nil {
		panic(err)
	}
	return b, res
}

func patched() []byte {
	var cap int64
	for _, d := range fx.Manifests[0].AVB.Descriptors {
		if d.Partition == "boot" {
			cap = int64(d.ImageSize)
		}
	}
	out, _, err := mainline.PatchVbmeta(fx.New["vbmeta"], uint64(cap+64<<10))
	if err != nil {
		panic(err)
	}
	return out
}

// unitOf: slot k runs mainline (the reviewed chain, boot_k the assembled
// image, the patched vbmeta), the other slot holds what; backup holds the
// kept key in both copies.
func unitOf(res []byte, ab [2][3]byte, k, what string) fakeemmc.Unit {
	b := make([]byte, 4096*512)
	rec := keykeep.Encode(fx.Keys)
	copy(b[(4096-keykeep.FromEnd1)*512:], rec)
	copy(b[(4096-keykeep.FromEnd2)*512:], rec)
	u := fakeemmc.Unit{AB: ab, Tweak: map[string][]byte{"boot_" + k: mltest.Expected(tok, linux, 11, res), "backup": b, "vbmeta_" + k: patched()}}
	if k == "a" {
		u.A, u.B = "new", what
	} else {
		u.A, u.B = what, "new"
	}
	return u
}

// unit is one fake unit with the payload folder and the saved state folders
// of the two root file systems.
type unit struct {
	t       testing.TB
	dir     string
	root    *devicetest.Root
	bc      *devicetest.Bootctl
	b       *mltest.Bundle
	payload string
	slot    string            // the slot that runs now
	saved   map[string]string // slot -> its state folder while the other runs
	data    *fakeData
	mounts  string // more mountinfo lines
	live    *step2.Live
	locked  bool // the tsx-autoupdate lock is held by another run
	quiet   error
	plans   int
	ckpts   int    // journal checkpoints of the root
	ckptErr error  // the checkpoint fails
	start   []byte // the A/B sector at the start
	before  []byte // the disk at the start
	// real: a unit on the real UC-P10 layout with a real payload
	// (reallayout_test.go): its layout, gate, key pins and token
	real *realCfg
	// dataModel: each boot mounts /data as uc-pxx-lvm does, from the disk
	// (dataLine); the tsx-autoupdate state folder is there (its init)
	dataModel bool
	quiets    atomic.Int64 // the real Quiet's calls and refusals (reallayout_test.go)
	quietErrs atomic.Int64
}

// dataLine is the /data line of mountinfo that uc-pxx-lvm gives a boot of
// this disk: userdata with the ext4 label tsxdata, else the LVM volume
// tsx/data when vendor_a, oem_a, vendor_b or oem_b holds an LVM2 label, else
// no mount (/data the root's folder).
func (u *unit) dataLine() string {
	if u.userdata() == "ext4 label "+plan.DataLabel {
		e := u.env()
		d, err := e.OpenEMMCSlot(device.EMMCPath)
		if err != nil {
			u.t.Fatal(err)
		}
		ud, _ := d.Part(plan.DataPartition)
		return fmt.Sprintf("30 20 %d:%d / /data rw,noatime - ext4 /dev/%s rw,commit=60\n", ud.Major, ud.Minor, ud.Dev)
	}
	for _, n := range step2.DataLVMParts {
		f, err := os.Open(u.root.Node(n))
		if err != nil {
			continue
		}
		b := make([]byte, 4*512)
		f.ReadAt(b, 0)
		f.Close()
		for i := 0; i < 4; i++ {
			if string(b[i*512:i*512+8]) == "LABELONE" {
				return lvmMount
			}
		}
	}
	return ""
}

func newUnit(t testing.TB, u fakeemmc.Unit, slot string, bin []byte, exe string) *unit {
	dir := t.TempDir()
	src, err := fx.Image(dir, "src.img", u)
	if err != nil {
		t.Fatal(err)
	}
	root, err := devicetest.New(filepath.Join(dir, "root"), src, small, bin, devicetest.Opts{NoMarker: true,
		Files: map[string]string{device.SlotBootctlPath: string(bin)}})
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(src)
	b, _ := bundle()
	pd, err := b.Write(filepath.Join(dir, "payload"))
	if err != nil {
		t.Fatal(err)
	}
	un := &unit{t: t, dir: dir, root: root, b: b, payload: pd, saved: map[string]string{}}
	un.bc = &devicetest.Bootctl{Root: root, Exec: exe, Tmp: dir}
	un.data = &fakeData{u: un}
	un.live = &step2.Live{NetworkUp: func() bool { return true },
		LockAutoupdate: func() (func(), error) {
			if un.locked {
				return nil, errors.New("tsx-autoupdate runs now (test)")
			}
			return func() {}, nil
		},
		Quiet: func() error { return un.quiet }}
	un.boot(slot)
	un.before = un.image()
	un.start, _ = root.ABSector()
	return un
}

// boot makes the root a boot of slot s: U-Boot's bootargs, the command line
// with the payload's token, the root system_s, a new boot id, and the state
// folder of that slot's root file system.
func (u *unit) boot(s string) {
	t := u.t
	idx := map[string]int{"a": 22, "b": 23}[s]
	sd := u.root.Path(step2.StateDir)
	if u.slot != "" && u.slot != s {
		if _, err := os.Stat(sd); err == nil {
			to := filepath.Join(u.dir, "state-"+u.slot)
			os.RemoveAll(to)
			if err := os.Rename(sd, to); err != nil {
				t.Fatal(err)
			}
			u.saved[u.slot] = to
		}
		if from, ok := u.saved[s]; ok {
			os.MkdirAll(filepath.Dir(sd), 0o755)
			if err := os.Rename(from, sd); err != nil {
				t.Fatal(err)
			}
			delete(u.saved, s)
		}
	}
	u.slot = s
	if u.dataModel {
		u.mounts = u.dataLine()
		os.MkdirAll(u.root.Path(filepath.Dir(step2.HoldsDir)), 0o755) // tsx-autoupdate's init
	}
	id := make([]byte, 16)
	rand.Read(id)
	h := hex.EncodeToString(id)
	files := map[string]string{
		device.UBootArgsPath: "storagemedia=emmc androidboot.vbmeta.device_state=unlocked androidboot.verifiedbootstate=orange " +
			"androidboot.slot_suffix=_" + s + " console=ttyFIQ0\x00",
		"/proc/cmdline":      "console=ttyS2,115200n8 rdinit=/init uc-pxx.id=" + u.token() + "\n",
		device.MountInfoPath: fmt.Sprintf("20 1 179:%d / / rw,noatime - ext4 /dev/mmcblk0p%d rw\n21 20 0:5 / /proc rw - proc proc rw\n%s", idx, idx, u.mounts),
		device.BootIDPath:    fmt.Sprintf("%s-%s-%s-%s-%s\n", h[:8], h[8:12], h[12:16], h[16:20], h[20:]),
	}
	for k, v := range files {
		p := u.root.Path(k)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	os.Remove(u.root.Path(device.ABLockPath))
}

func (u *unit) token() string {
	if u.real != nil {
		return u.real.token
	}
	return tok
}

func (u *unit) env() *device.Env {
	l, m := small, fx.Manifests
	if u.real != nil {
		l, m = device.Layout(), nil
	}
	e, err := device.NewForTest(device.TestOptions{Root: u.root.Dir, RegularFiles: true, Layout: l, Runner: u.bc, Manifests: m})
	if err != nil {
		u.t.Fatal(err)
	}
	return e
}

func (u *unit) image() []byte {
	b, err := u.root.Image()
	if err != nil {
		u.t.Fatal(err)
	}
	return b
}

func (u *unit) opts(out *bytes.Buffer) step2.Options {
	if u.real != nil {
		// the release key pins by default (nil), as on a unit: the finish
		// executor (its own process, mainline-rc-4) has no test pins, and the
		// finish compares the pin set the step-2 plan recorded
		o := u.optsWith(out, u.real.gate, nil)
		o.Manifests, o.MaxWrite = nil, 1<<20
		return o
	}
	return u.optsWith(out, u.b.Gate(), pins)
}

func (u *unit) optsWith(out *bytes.Buffer, g *mainline.Gate, kp []plan.KeyPin) step2.Options {
	return step2.Options{Device: "/dev/mmcblk0", Payload: u.payload, Confirmed: true, Out: out, Gate: g, KeyPins: kp,
		Manifests: fx.Manifests, DataRunner: u.data, Live: u.live, MaxWrite: 64 << 10,
		Checkpoint: func() error { u.ckpts++; return u.ckptErr },
		RecordReader: func(_ *device.Disk, slot, file string) ([]byte, error) {
			d, ok := u.saved[slot]
			if !ok {
				return nil, os.ErrNotExist
			}
			return os.ReadFile(filepath.Join(d, file))
		}}
}

// plan makes the plan file (plan --confirmed --out).
func (u *unit) plan(o step2.Options) (*plan.OtherSlotPlan, string, error) {
	u.plans++
	f := filepath.Join(u.dir, fmt.Sprintf("plan-%d.json", u.plans))
	o.JSONOut = f
	p, err := step2.Plan(u.env(), o)
	return p, f, err
}

// fakeData stands for uc-pxx-data-migrate on the userdata node file: format
// writes an ext4 superblock with the label tsxdata-new, migrate the label
// tsxdata. cut makes it stop like a power cut: "format-mid" (the old
// superblock zeroed, no new one), "format-end" (formatted, before the tool
// returns), "migrate-mid" (before the label), "migrate-end" (the label
// written, before the tool returns).
type fakeData struct {
	u   *unit
	cut string
	ops []string
}

var errCut = errors.New("power cut (test)")

func (f *fakeData) sb(label string) error {
	node := f.u.root.Node("userdata")
	sb := make([]byte, 1024)
	if label != "" {
		sb[0x38], sb[0x39] = 0x53, 0xEF
		copy(sb[0x78:], label)
	}
	fh, err := os.OpenFile(node, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, werr := fh.WriteAt(sb, 1024)
	return errors.Join(werr, fh.Close())
}

func (f *fakeData) Run(op string) (int, string, error) {
	f.ops = append(f.ops, op)
	switch op {
	case "format":
		if f.cut == "touch-payload" {
			os.Chtimes(filepath.Join(f.u.payload, mainline.RootfsFile), time.Unix(5, 0), time.Unix(5, 0))
		}
		if err := f.sb(""); err != nil {
			return -1, "", err
		}
		if f.cut == "format-mid" {
			return -1, "", errCut
		}
		if err := f.sb(plan.DataFormatLabel); err != nil {
			return -1, "", err
		}
		if f.cut == "format-end" {
			return -1, "", errCut
		}
	case "migrate":
		if f.cut == "migrate-mid" {
			return -1, "", errCut
		}
		if f.cut == "nolabel" {
			return 0, "done (but no label)", nil
		}
		if err := f.sb(plan.DataLabel); err != nil {
			return -1, "", err
		}
		if f.cut == "migrate-end" {
			return -1, "", errCut
		}
	default:
		return 2, "usage", nil
	}
	return 0, "done", nil
}

func (u *unit) userdata() string {
	b, _ := os.ReadFile(u.root.Node("userdata"))
	return plan.DataState(bytes.NewReader(b), 0)
}

// mark is the target's own mark after its boot (uc-pxx-bootctl
// mark-successful of the slot-side tools): {prio, 0, 1}, the tail kept. A
// test image write, as the slot-side tool would do it.
func (u *unit) mark(slot string) {
	sec, err := u.root.ABSector()
	if err != nil {
		u.t.Fatal(err)
	}
	a, _ := avbab.Parse(sec)
	i := map[string]int{"a": 0, "b": 1}[slot]
	if !a.Slots[i].Bootable() || a.Slots[i].Successful == 1 {
		u.t.Fatalf("slot %s is not trying: %v", slot, a.Slots[i])
	}
	var s [2][3]byte
	for k := 0; k < 2; k++ {
		s[k] = [3]byte{a.Slots[k].Priority, a.Slots[k].Tries, a.Slots[k].Successful}
	}
	s[i] = [3]byte{a.Slots[i].Priority, 0, 1}
	next := fakeemmc.ABSector(s, sec[32:])
	fh, err := os.OpenFile(u.root.Node("misc"), os.O_WRONLY, 0)
	if err != nil {
		u.t.Fatal(err)
	}
	fh.WriteAt(next, (plan.ABLBA-plan.MiscStart)*512)
	fh.Close()
}

func (u *unit) ab() avbab.AB {
	sec, _ := u.root.ABSector()
	a, _ := avbab.Parse(sec)
	return a
}

// startRun is plan + start with o; it returns the plan and the start's error.
func (u *unit) startRun(o step2.Options) (*plan.OtherSlotPlan, *bytes.Buffer, error) {
	var out bytes.Buffer
	o.Out = &out
	p, f, err := u.plan(o)
	if err != nil {
		return p, &out, fmt.Errorf("plan: %w", err)
	}
	o.PlanFile = f
	_, err = step2.Start(u.env(), o)
	return p, &out, err
}

// finishRun boots the target, marks it, and runs the finish (dry run, then
// with the dry run's id). It checks the end state.
func (u *unit) finishRun(p *plan.OtherSlotPlan) {
	t := u.t
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
	if p.Keeper == "a" && (!fr2.Switched || fr.Plan.Switch == nil) || p.Keeper == "b" && (fr2.Switched || !fr2.Plan.Done) {
		t.Fatalf("finish of keeper %s: switched %v done %v", p.Keeper, fr2.Switched, fr2.Plan.Done)
	}
	u.endState(p)
	// a second finish: done, nothing written
	img := u.image()
	o.ExpectID = ""
	if fr3, err := step2.Finish(u.env(), o); err != nil || !fr3.Plan.Done || fr3.Switched || !bytes.Equal(img, u.image()) {
		t.Fatalf("a second finish: %v %+v", err, fr3)
	}
}

// endState: A{15,0,1} B{14,0,1} A next; the target holds the payload; the
// keeper's partitions are as at the start; nothing else changed but the A/B
// sector, the target's six partitions and userdata.
func (u *unit) endState(p *plan.OtherSlotPlan) {
	t := u.t
	a := u.ab()
	if a.Validate() != nil || a.Slots[0] != (avbab.Slot{Priority: 15, Successful: 1}) || a.Slots[1] != (avbab.Slot{Priority: 14, Successful: 1}) || a.Pick() != 0 {
		t.Fatalf("end state %v %v", a.Slots[0], a.Slots[1])
	}
	img := u.image()
	tb, _ := gpt.Parse(bytes.NewReader(img), int64(len(img)))
	changed := map[string]bool{"misc": true, "userdata": true}
	for _, part := range plan.MainlineParts() {
		changed[part+"_"+p.Target] = true
	}
	for _, e := range tb.Entries {
		s, n := e.Start*512, e.Bytes()
		if !changed[e.Name] && !bytes.Equal(img[s:s+n], u.before[s:s+n]) {
			t.Fatalf("%s changed", e.Name)
		}
	}
	misc, _ := tb.Find("misc")
	ab := (plan.ABLBA - misc.Start) * 512
	ms, mn := misc.Start*512, misc.Bytes()
	if !bytes.Equal(img[ms:ms+ab], u.before[ms:ms+ab]) || !bytes.Equal(img[ms+ab+32:ms+mn], u.before[ms+ab+32:ms+mn]) {
		t.Fatal("misc changed outside bytes 0..31 of the A/B sector")
	}
	holds := func(name string, want []byte) {
		e, _ := tb.Find(name)
		if !bytes.Equal(img[e.Start*512:e.Start*512+int64(len(want))], want) {
			t.Fatalf("%s does not hold its image", name)
		}
	}
	for _, n := range []string{"uboot", "trust", "dtbo"} {
		holds(n+"_"+p.Target, fx.New[n])
	}
	holds("vbmeta_"+p.Target, patched())
	_, res := bundle()
	holds("boot_"+p.Target, mltest.Expected(tok, linux, 11, res))
	holds("system_"+p.Target, u.b.Rootfs)
	if got := u.userdata(); got != "ext4 label "+plan.DataLabel {
		t.Fatalf("userdata holds %s", got)
	}
}

var shapes = []struct {
	name string
	ab   [2][3]byte
	k    string
	what string
}{
	{"K=a: A mainline 15, B Android 14 (equal chain)", [2][3]byte{{15, 0, 1}, {14, 0, 1}}, "a", "new"},
	{"K=a: B old images (chain written)", [2][3]byte{{15, 0, 1}, {14, 0, 1}}, "a", "old"},
	{"K=b: B mainline 15, A old 14 (the direct path's step 2)", [2][3]byte{{14, 0, 1}, {15, 0, 1}}, "b", "old"},
}

func TestStep2Full(t *testing.T) {
	bin, exe := bootctlBin(t)
	for _, sh := range shapes {
		t.Run(sh.name, func(t *testing.T) {
			t.Parallel()
			_, res := bundle()
			u := newUnit(t, unitOf(res, sh.ab, sh.k, sh.what), sh.k, bin, exe)
			p, out, err := u.startRun(u.opts(nil))
			if err != nil {
				t.Fatalf("%v\n%s", err, out.String())
			}
			if strings.Join(u.data.ops, " ") != "format migrate" {
				t.Fatalf("data ops %v", u.data.ops)
			}
			a := u.ab()
			ti := map[string]int{"a": 0, "b": 1}[p.Target]
			if a.Slots[ti] != (avbab.Slot{Priority: 15, Tries: 7}) || !a.Slots[1-ti].Keeper() || a.Pick() != ti {
				t.Fatalf("after start %v %v", a.Slots[0], a.Slots[1])
			}
			st, _ := os.ReadFile(u.root.Path(step2.StateDir + "/" + step2.StateFile))
			if !strings.Contains(string(st), "started "+p.ID+" supervised=false") || !strings.Contains(string(st), "armed "+p.ID) {
				t.Fatalf("state %q", st)
			}
			if u.ckpts != 2 {
				t.Fatalf("%d journal checkpoints of the root, want 2 (after the record, after the arm)", u.ckpts)
			}
			u.finishRun(p)
		})
	}
}

// sweep is one shape of the power-cut sweep: n primitive writes; each cut k
// (clean and torn) is checked and resumed.
func TestStep2PowerCutSweep(t *testing.T) {
	bin, exe := bootctlBin(t)
	var total atomic.Int64
	t.Cleanup(func() {
		t.Logf("step-2 power-cut sweep: %d cut points, each checked and resumed to A{15,0,1} B{14,0,1}", total.Load())
	})
	for _, sh := range shapes {
		// count the primitive writes of a full start
		_, res := bundle()
		u0 := newUnit(t, unitOf(res, sh.ab, sh.k, sh.what), sh.k, bin, exe)
		n := 0
		o := u0.opts(nil)
		o.Hooks = &install.Hooks{CutAfter: -1, Observe: func(install.Event) error { n++; return nil }}
		if _, out, err := u0.startRun(o); err != nil {
			t.Fatalf("%s: %v\n%s", sh.name, err, out.String())
		}
		points := []string{}
		for k := 0; k < n; k++ {
			points = append(points, fmt.Sprintf("cut:%d", k), fmt.Sprintf("torn:%d", k))
		}
		points = append(points, "data:format-mid", "data:format-end", "data:migrate-mid", "data:migrate-end")
		if testing.Short() {
			var s []string
			for i, p := range points {
				if i%7 == 0 || i < 4 || i >= len(points)-6 {
					s = append(s, p)
				}
			}
			points = s
		}
		for _, pt := range points {
			sh, pt := sh, pt
			t.Run(sh.name+"/"+pt, func(t *testing.T) {
				t.Parallel()
				total.Add(1)
				_, res := bundle()
				u := newUnit(t, unitOf(res, sh.ab, sh.k, sh.what), sh.k, bin, exe)
				o := u.opts(nil)
				kind, arg, _ := strings.Cut(pt, ":")
				if kind == "data" {
					u.data.cut = arg
				} else {
					var k int
					fmt.Sscan(arg, &k)
					o.Hooks = &install.Hooks{CutAfter: k, Torn: kind == "torn"}
				}
				p0, out, err := u.startRun(o)
				if err == nil || !(errors.Is(err, install.ErrPowerCut) || strings.Contains(err.Error(), errCut.Error())) {
					t.Fatalf("%s: the start did not stop at the cut: %v\n%s", pt, err, out.String())
				}
				cs, err := install.CheckCutState(bytes.NewReader(u.image()), &p0.Plan, u.start)
				if err != nil {
					t.Fatalf("%s: the cardinal rule: %v (%+v)\n%s", pt, err, cs, out.String())
				}
				if cs.Boots != "keeper" {
					t.Fatalf("%s: a cut before the end boots the %s", pt, cs.Boots)
				}
				// the power comes back: the keeper boots again; a new plan of
				// this boot resumes to the end
				u.boot(p0.Keeper)
				u.data.cut = ""
				p1, out, err := u.startRun(u.opts(nil))
				if err != nil {
					t.Fatalf("%s: the resume: %v\n%s", pt, err, out.String())
				}
				if p1.ID == p0.ID {
					t.Fatalf("%s: the resume ran the plan of the boot before", pt)
				}
				u.finishRun(p1)
			})
		}
	}
}

// The finish's switch cut before its write: the A/B sector is unchanged (B
// successful and next, A successful); a new finish in a new boot of B ends
// it.
func TestStep2FinishCut(t *testing.T) {
	bin, exe := bootctlBin(t)
	_, res := bundle()
	u := newUnit(t, unitOf(res, shapes[0].ab, "a", "new"), "a", bin, exe)
	p, out, err := u.startRun(u.opts(nil))
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	u.boot("b")
	u.mark("b")
	o := u.opts(&bytes.Buffer{})
	o.Auto = true // the service's automatic finish (an unsupervised start)
	o.FinishCut = &install.Cutter{Hooks: &install.Hooks{CutAfter: 0}}
	before, _ := u.root.ABSector()
	if _, err := step2.Finish(u.env(), o); !errors.Is(err, install.ErrPowerCut) {
		t.Fatalf("finish with a cut: %v", err)
	}
	if after, _ := u.root.ABSector(); !bytes.Equal(before, after) {
		t.Fatal("the cut finish changed the A/B sector")
	}
	a := u.ab()
	if !a.Slots[0].Keeper() || !a.Slots[1].Keeper() || a.Pick() != 1 {
		t.Fatalf("after the cut %v %v", a.Slots[0], a.Slots[1])
	}
	// a new boot of B: the automatic finish (unsupervised start, no expect id) ends it
	u.boot("b")
	o = u.opts(&bytes.Buffer{})
	o.Auto = true
	if fr, err := step2.Finish(u.env(), o); err != nil || !fr.Switched {
		t.Fatalf("finish after the cut: %v", err)
	}
	u.endState(p)
}

// The finish refuses, and writes nothing, when the A/B state is not the
// armed state with B marked, when the RKSS ranges changed, or when the
// expected id is another one.
func TestStep2FinishRefusals(t *testing.T) {
	bin, exe := bootctlBin(t)
	_, res := bundle()
	for _, c := range []struct {
		name string
		edit func(u *unit, o *step2.Options)
		want string
	}{
		{"B not marked yet", func(u *unit, o *step2.Options) {}, "b-successful"},
		{"LBA 1024 written by U-Boot", func(u *unit, o *step2.Options) {
			u.mark("b")
			fh, _ := os.OpenFile(u.root.Path("/dev/mmcblk0"), os.O_WRONLY, 0)
			fh.WriteAt([]byte{0x88, 0x77, 0x66, 0x55}, 1024*512)
			fh.Close()
		}, "rkss"},
		{"another expected id", func(u *unit, o *step2.Options) { u.mark("b"); o.ExpectID = strings.Repeat("0", 64) }, "reviewed dry run"},
		{"supervised without an expected id", func(u *unit, o *step2.Options) {
			u.mark("b")
			os.MkdirAll(u.root.Path("/etc/uc-pxx-step2"), 0o755)
			os.WriteFile(u.root.Path(step2ui.SupervisedMarker), nil, 0o644)
		}, "--expect-id"},
		{"no step-2 record", func(u *unit, o *step2.Options) { u.mark("b"); u.saved = map[string]string{} }, "step-2 record"},
		// mainline-rc-4 review MAJOR-1: a finish by hand with no marker on
		// the target (a normal build never has one) and no --expect-id
		{"by hand without an expected id (no marker)", func(u *unit, o *step2.Options) { u.mark("b") }, "--expect-id"},
		{"automatic after a start whose line is not supervised=false", func(u *unit, o *step2.Options) {
			u.mark("b")
			o.Auto = true
			st := filepath.Join(u.saved["a"], step2.StateFile)
			b, _ := os.ReadFile(st)
			os.WriteFile(st, bytes.ReplaceAll(b, []byte("supervised=false"), []byte("supervised=true")), 0o644)
		}, "supervised=false"},
		{"the key record removed", func(u *unit, o *step2.Options) {
			u.mark("b")
			fh, _ := os.OpenFile(u.root.Node("backup"), os.O_WRONLY, 0)
			fh.WriteAt(make([]byte, 512), (4096-keykeep.FromEnd2)*512)
			fh.Close()
		}, "key-kept"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			u := newUnit(t, unitOf(res, shapes[0].ab, "a", "new"), "a", bin, exe)
			if _, out, err := u.startRun(u.opts(nil)); err != nil {
				t.Fatalf("%v\n%s", err, out.String())
			}
			u.boot("b")
			var out bytes.Buffer
			o := u.opts(&out)
			c.edit(u, &o)
			img := u.image()
			_, err := step2.Finish(u.env(), o)
			if err == nil || !strings.Contains(err.Error()+out.String(), c.want) {
				t.Fatalf("err %v, want %q\n%s", err, c.want, out.String())
			}
			if !bytes.Equal(img, u.image()) {
				t.Fatal("a refused finish wrote something")
			}
		})
	}
}

// A supervised start waits for the GO line before each A/B write and the
// data-format: the right lines run it to the end; a wrong line before the
// disable writes nothing; a wrong line before the arm leaves the target
// disabled and the keeper booting.
func TestStep2Supervised(t *testing.T) {
	bin, exe := bootctlBin(t)
	_, res := bundle()
	for _, c := range []struct {
		name  string
		lines func(p *plan.OtherSlotPlan) string
		ok    bool
		boots string // after a stop: "untouched" or "keeper, target disabled"
	}{
		{"all GO lines", func(p *plan.OtherSlotPlan) string { return goLines(p, 99) }, true, ""},
		{"no line", func(p *plan.OtherSlotPlan) string { return "" }, false, "untouched"},
		{"a wrong line before the disable", func(p *plan.OtherSlotPlan) string { return "go 1 000000000000\n" }, false, "untouched"},
		{"a wrong line before the arm", func(p *plan.OtherSlotPlan) string { return goLines(p, 2) + "yes\n" }, false, "disabled"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			u := newUnit(t, unitOf(res, shapes[0].ab, "a", "new"), "a", bin, exe)
			var out bytes.Buffer
			o := u.opts(&out)
			p, f, err := u.plan(o)
			if err != nil {
				t.Fatalf("%v\n%s", err, out.String())
			}
			o.PlanFile, o.Supervised = f, true
			o.Confirm = strings.NewReader(c.lines(p))
			_, err = step2.Start(u.env(), o)
			if (err == nil) != c.ok {
				t.Fatalf("supervised start: %v\n%s", err, out.String())
			}
			a := u.ab()
			switch c.boots {
			case "untouched":
				if !bytes.Equal(u.image(), u.before) {
					t.Fatal("a stop before the disable wrote something")
				}
			case "disabled":
				if a.Slots[1] != (avbab.Slot{}) || !a.Slots[0].Keeper() || a.Pick() != 0 {
					t.Fatalf("after a stop before the arm: %v %v", a.Slots[0], a.Slots[1])
				}
			}
			if c.ok {
				if !strings.Contains(out.String(), "SUPERVISED: before step 1") {
					t.Fatal("no GO prompt")
				}
				// the service never finishes a supervised run by itself
				u.boot("b")
				u.mark("b")
				img := u.image()
				r, err := step2.Service(u.env(), u.opts(&bytes.Buffer{}), step2.ServiceEnv{})
				if err != nil || !strings.Contains(r.What, "supervised") || !bytes.Equal(img, u.image()) {
					t.Fatalf("service in the target of a supervised run: %v %+v", err, r)
				}
			}
		})
	}
}

func timeOf(s int64) time.Time { return time.Unix(s, 0) }

// goLines are the GO lines of the pause steps up to step max.
func goLines(p *plan.OtherSlotPlan, max int) string {
	var b strings.Builder
	for _, s := range p.Steps {
		if s.N <= max && (s.Op == "bootctl" || s.Op == plan.OpDataFormat) {
			fmt.Fprintf(&b, "go %d %s\n", s.N, p.ID[:12])
		}
	}
	return b.String()
}

// The refusals of plan and start: nothing written.
func TestStep2Refusals(t *testing.T) {
	bin, exe := bootctlBin(t)
	_, res := bundle()
	for _, c := range []struct {
		name string
		edit func(u *unit, o *step2.Options)
		want string
	}{
		{"a RAM image marker", func(u *unit, o *step2.Options) {
			os.MkdirAll(u.root.Path("/etc/uc-pxx-inst"), 0o755)
			os.WriteFile(u.root.Path(device.RAMImageMarker), []byte(device.RAMImageMagic+"\n"), 0o644)
		}, "not a slot boot"},
		{"a RAM boot (no u-boot,bootargs)", func(u *unit, o *step2.Options) { os.Remove(u.root.Path(device.UBootArgsPath)) }, "not a slot boot"},
		{"the root is system_b in a boot of A", func(u *unit, o *step2.Options) {
			os.WriteFile(u.root.Path(device.MountInfoPath), []byte("20 1 179:23 / / rw - ext4 /dev/mmcblk0p23 rw\n"), 0o644)
		}, "mounted on /"},
		{"userdata mounted with the data steps", func(u *unit, o *step2.Options) {
			b, _ := os.ReadFile(u.root.Path(device.MountInfoPath))
			os.WriteFile(u.root.Path(device.MountInfoPath), append(b, "30 20 179:28 / /data rw - ext4 /dev/mmcblk0p28 rw\n"...), 0o644)
		}, "mounted on /data"},
		{"tsx-autoupdate runs", func(u *unit, o *step2.Options) { u.locked = true }, "tsx-autoupdate runs"},
		{"tsx-autoupdate holds", func(u *unit, o *step2.Options) { u.quiet = errors.New("a hold (test)") }, "a hold"},
		{"the payload changed after the plan", func(u *unit, o *step2.Options) {
			os.Chtimes(filepath.Join(u.payload, mainline.RootfsFile), timeOf(1), timeOf(1))
		}, "changed"},
		{"the key record changed after the plan", func(u *unit, o *step2.Options) {
			fh, _ := os.OpenFile(u.root.Node("backup"), os.O_WRONLY, 0)
			fh.WriteAt(make([]byte, 512), (4096-keykeep.FromEnd1)*512)
			fh.Close()
		}, "key"},
		{"the release pins (the fixture key is not pinned)", func(u *unit, o *step2.Options) { o.KeyPins = nil }, "key-pinned"},
		{"not confirmed", func(u *unit, o *step2.Options) { o.Confirmed = false }, "confirmation"},
		{"auto on a supervised unit", func(u *unit, o *step2.Options) {
			o.Confirmed, o.Auto = false, true
			os.MkdirAll(u.root.Path("/etc/uc-pxx-step2"), 0o755)
			os.WriteFile(u.root.Path(step2ui.SupervisedMarker), nil, 0o644)
		}, "supervised"},
		{"the bootctl binary is another", func(u *unit, o *step2.Options) {
			os.WriteFile(u.root.Path(device.SlotBootctlPath), []byte("not the tool"), 0o700)
		}, "reviewed binary"},
		{"the queue takes no FUA", func(u *unit, o *step2.Options) {
			fs, _ := filepath.Glob(u.root.Path("/sys/devices/platform/*/mmc_host/mmc0/mmc0:0001/block/mmcblk0/queue/fua"))
			for _, f := range fs {
				os.WriteFile(f, []byte("0\n"), 0o644)
			}
		}, "FUA"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			u := newUnit(t, unitOf(res, shapes[0].ab, "a", "new"), "a", bin, exe)
			var out bytes.Buffer
			o := u.opts(&out)
			// the plan file is made first, the change comes after it
			_, f, perr := u.plan(o)
			o.PlanFile = f
			c.edit(u, &o)
			edited := u.image()
			var err error
			if perr != nil {
				err = perr
			} else {
				_, err = step2.Start(u.env(), o)
			}
			if err == nil || !strings.Contains(err.Error()+out.String(), c.want) {
				t.Fatalf("err %v, want %q\n%s", err, c.want, out.String())
			}
			if !bytes.Equal(u.image(), edited) {
				t.Fatal("a refused start wrote something")
			}
		})
	}
}

// fakePanel records the screens and gives prepared keys, one per Show.
type fakePanel struct {
	shown  [][]string
	keys   chan step2ui.Key
	script []step2ui.Key
	closed bool
}

func (p *fakePanel) Show(l []string) {
	p.shown = append(p.shown, l)
	if len(p.script) > 0 {
		p.keys <- p.script[0]
		p.script = p.script[1:]
	}
}
func (p *fakePanel) Keys() <-chan step2ui.Key { return p.keys }
func (p *fakePanel) Close(bool)               { p.closed = true }

// clock is a fake monotonic clock: every call is one second later.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { c.t = c.t.Add(time.Second); return c.t }

func (u *unit) service(keys []step2ui.Key) (*step2.ServiceResult, *fakePanel, bool, error) {
	pn := &fakePanel{keys: make(chan step2ui.Key, 64), script: keys}
	c := &clock{t: time.Unix(1700000000, 0)}
	rebooted := false
	o := u.opts(&bytes.Buffer{})
	o.Payload, o.Confirmed = "", false
	r, err := step2.Service(u.env(), o, step2.ServiceEnv{Panel: func() (step2.Panel, error) { return pn, nil }, Now: c.now,
		Sleep: func(time.Duration) {}, Reboot: func() error { rebooted = true; return nil }, Hold: func() {}})
	return r, pn, rebooted, err
}

func (u *unit) conf() {
	os.MkdirAll(u.root.Path("/etc/conf.d"), 0o755)
	if err := os.WriteFile(u.root.Path(step2.ConfFile), []byte("UC_PXX_STEP2_PAYLOAD=\""+u.payload+"\"\n"), 0o644); err != nil {
		u.t.Fatal(err)
	}
}

// The service: the question on the panel (hold OK 3 s starts; Back = not
// now; Vol then the hold = never), the AUTO-STEP2 countdown with its attempt
// line written first, at most one attempt per drive, a failed target, and the
// finish in the target (by itself only for an unsupervised run).
func TestStep2Service(t *testing.T) {
	bin, exe := bootctlBin(t)
	_, res := bundle()
	hold := []step2ui.Key{step2ui.KeyOKDown, step2ui.KeyNone, step2ui.KeyNone, step2ui.KeyNone, step2ui.KeyNone}
	t.Run("no payload configured: nothing", func(t *testing.T) {
		u := newUnit(t, unitOf(res, shapes[0].ab, "a", "new"), "a", bin, exe)
		r, pn, _, err := u.service(nil)
		if err != nil || !strings.Contains(r.What, "no payload") || len(pn.shown) != 0 || !bytes.Equal(u.image(), u.before) {
			t.Fatalf("%v %+v", err, r)
		}
	})
	t.Run("not now", func(t *testing.T) {
		u := newUnit(t, unitOf(res, shapes[0].ab, "a", "new"), "a", bin, exe)
		u.conf()
		r, pn, _, err := u.service([]step2ui.Key{step2ui.KeyBack})
		if err != nil || r.What != "not now" || !pn.closed || !bytes.Equal(u.image(), u.before) {
			t.Fatalf("%v %+v", err, r)
		}
	})
	t.Run("never", func(t *testing.T) {
		u := newUnit(t, unitOf(res, shapes[0].ab, "a", "new"), "a", bin, exe)
		u.conf()
		r, _, _, err := u.service(append([]step2ui.Key{step2ui.KeyVolDown}, hold...))
		if err != nil || r.What != "never" || !bytes.Equal(u.image(), u.before) {
			t.Fatalf("%v %+v", err, r)
		}
		if r2, _, _, err := u.service(hold); err != nil || r2.What != "never or done" {
			t.Fatalf("after never: %v %+v", err, r2)
		}
	})
	t.Run("hold OK, then the finish in B by itself", func(t *testing.T) {
		u := newUnit(t, unitOf(res, shapes[0].ab, "a", "new"), "a", bin, exe)
		u.conf()
		r, _, rebooted, err := u.service(hold)
		if err != nil || r.Start == nil || !rebooted {
			t.Fatalf("%v %+v", err, r)
		}
		p := r.Start.Plan
		if p.Confirm.Auto || !p.Confirm.Confirmed {
			t.Fatalf("confirm %+v", p.Confirm)
		}
		// B boots; before its mark the service checks F1 and waits
		u.boot("b")
		r, _, _, err = u.service(nil)
		if err != nil || !strings.Contains(r.What, "has not marked") {
			t.Fatalf("unmarked target: %v %+v", err, r)
		}
		u.mark("b")
		r, _, _, err = u.service(nil)
		if err != nil || r.Finish == nil || !r.Finish.Switched {
			t.Fatalf("finish by the service: %v %+v", err, r)
		}
		u.endState(p)
		// A boots first again; the payload is still configured: no question
		// (review STEP2-REST-1 MAJOR-1: the done record is on B's root only)
		u.boot("a")
		img := u.image()
		r, pn, _, err := u.service(hold)
		if err != nil || !strings.Contains(r.What, "done with this payload") || len(pn.shown) != 0 || !bytes.Equal(img, u.image()) {
			t.Fatalf("A after a finished step 2: %v %+v", err, r)
		}
	})
	t.Run("STOP in an unmarked target: shown, nothing written (T4-2 MINOR-1)", func(t *testing.T) {
		u := newUnit(t, unitOf(res, shapes[2].ab, "b", "old"), "b", bin, exe)
		if _, out, err := u.startRun(u.opts(nil)); err != nil {
			t.Fatalf("%v\n%s", err, out.String())
		}
		u.boot("a") // A's first boot of the userdebug U-Boot; U-Boot wrote LBA 1024
		fh, _ := os.OpenFile(u.root.Path("/dev/mmcblk0"), os.O_WRONLY, 0)
		fh.WriteAt([]byte{0x88, 0x77, 0x66, 0x55}, 1024*512)
		fh.Close()
		img := u.image()
		r, pn, _, err := u.service(nil)
		if !errors.Is(err, step2.ErrStop) || r.What != "finish STOP" || len(pn.shown) == 0 || !strings.HasPrefix(pn.shown[0][0], "STOP") {
			t.Fatalf("%v %+v", err, r)
		}
		// review STEP2-REST-1 MINOR-2: the restart block holds the Restart button
		if b, _ := os.ReadFile(u.root.Path(step2.RestartBlock)); string(b) != "hold 4\n" {
			t.Fatalf("restart block %q", b)
		}
		if !bytes.Equal(img, u.image()) {
			t.Fatal("a STOP wrote to the eMMC")
		}
	})
	t.Run("AUTO-STEP2: one countdown per drive, never after a failed target", func(t *testing.T) {
		u := newUnit(t, unitOf(res, shapes[0].ab, "a", "new"), "a", bin, exe)
		u.conf()
		os.WriteFile(filepath.Join(filepath.Dir(u.payload), step2ui.AutoFile), nil, 0o644)
		r, _, rebooted, err := u.service(nil) // no key: the countdown runs out
		if err != nil || r.Start == nil || !r.Start.Plan.Confirm.Auto || !rebooted {
			t.Fatalf("%v %+v", err, r)
		}
		at, _ := os.ReadFile(u.root.Path(step2.StateDir + "/" + step2.AttemptsFile))
		if !strings.HasPrefix(string(at), "drive ") {
			t.Fatalf("attempts %q", at)
		}
		// B never starts: its tries run out, libavb makes it {0,0,0}, A boots
		fh, _ := os.OpenFile(u.root.Node("misc"), os.O_WRONLY, 0)
		sec, _ := u.root.ABSector()
		fh.WriteAt(fakeemmc.ABSector([2][3]byte{{14, 0, 1}, {0, 0, 0}}, sec[32:]), (plan.ABLBA-plan.MiscStart)*512)
		fh.Close()
		u.boot("a")
		r, pn, _, err := u.service([]step2ui.Key{step2ui.KeyBack})
		at, _ = os.ReadFile(u.root.Path(step2.StateDir + "/" + step2.AttemptsFile))
		if err != nil || !strings.Contains(string(at), "failed plan-") || r.What != "not now" {
			t.Fatalf("after a failed target: %v %+v %q", err, r, at)
		}
		for _, l := range pn.shown[0] {
			if strings.Contains(l, "Starts by itself") {
				t.Fatal("a countdown after a failed target")
			}
		}
	})
}

// The data hook's own guards: it never runs the data tool while the target
// can boot, nor on a userdata that is not what the plan saw.
func TestStep2DataHook(t *testing.T) {
	bin, exe := bootctlBin(t)
	_, res := bundle()
	d := &plan.DataPlan{Partition: plan.DataPartition, Label: plan.DataLabel, FormatAs: plan.DataFormatLabel, Before: "no ext4"}
	for _, c := range []struct {
		name string
		ab   [2][3]byte
		op   string
		want string
	}{
		{"the target bootable (B Android 14)", [2][3]byte{{15, 0, 1}, {14, 0, 1}}, plan.OpDataFormat, "not disabled"},
		{"the target trying", [2][3]byte{{14, 0, 1}, {15, 7, 0}}, plan.OpDataFormat, "not disabled"},
		{"migrate on a userdata that format did not make", [2][3]byte{{15, 0, 1}, {0, 0, 0}}, plan.OpDataMigrate, "want ext4 label tsxdata-new"},
	} {
		t.Run(c.name, func(t *testing.T) {
			u := newUnit(t, unitOf(res, c.ab, "a", "new"), "a", bin, exe)
			o := u.opts(&bytes.Buffer{})
			hook, done, err := step2.DataHookForTest(u.env(), o)
			if err != nil {
				t.Fatal(err)
			}
			defer done()
			if err := hook(c.op, d); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v, want %q", err, c.want)
			}
			if len(u.data.ops) != 0 || !bytes.Equal(u.image(), u.before) {
				t.Fatal("the data tool ran")
			}
		})
	}
}

// uc-pxx-bootctl-inst misbehaves at an A/B write of the start: every end is
// a known state (the run stops; the cardinal rule holds) or ErrABUnknown
// (exit 3); never a silent success.
func TestStep2Misbehave(t *testing.T) {
	bin, exe := bootctlBin(t)
	_, res := bundle()
	for _, mode := range []string{"refuse", "unknown", "unknown-wrote", "signal", "signal-wrote", "garbage", "lie", "wrong", "tail", "arm-refuse"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			u := newUnit(t, unitOf(res, shapes[1].ab, "a", "old"), "a", bin, exe)
			u.bc.Mode = mode
			p, out, err := u.startRun(u.opts(nil))
			if err == nil {
				t.Fatalf("a misbehaving bootctl (%s) gave success\n%s", mode, out.String())
			}
			unknown := errors.Is(err, install.ErrABUnknown)
			a := u.ab()
			switch {
			case unknown:
				// exit 3: the session says do not power off; the state may be anything the tool wrote
			case a.Validate() != nil || !a.Slots[0].Keeper() || a.Pick() != 0 && !(a.Slots[1] == avbab.Slot{Priority: 15, Tries: 7}):
				t.Fatalf("%s: a stop with a known state that is not safe: %v %v (%v)", mode, a.Slots[0], a.Slots[1], err)
			}
			if mode == "garbage" || mode == "wrong" || mode == "tail" || mode == "lie" {
				if !unknown {
					t.Fatalf("%s: not ErrABUnknown: %v", mode, err)
				}
			}
			_ = p
		})
	}
}

// Review STEP2-REST-1 MINOR-1: the arm line of the keeper's state is lost (a
// cut before the journal reached the home blocks): F1 still runs in the
// target and a STOP is still shown; K = b then ends done (no write); with K
// = a the switch never runs by itself without the arm line.
func TestStep2ArmedLineLost(t *testing.T) {
	bin, exe := bootctlBin(t)
	_, res := bundle()
	for _, c := range []struct {
		name string
		sh   int
		stop bool
		want string
	}{
		{"K=b, LBA 1024 written: STOP", 2, true, ""},
		{"K=b, A marked: done, nothing written", 2, false, "finish: done"},
		{"K=a, B marked: no switch by itself", 0, false, "supervised procedure"},
	} {
		t.Run(c.name, func(t *testing.T) {
			sh := shapes[c.sh]
			u := newUnit(t, unitOf(res, sh.ab, sh.k, sh.what), sh.k, bin, exe)
			p, out, err := u.startRun(u.opts(nil))
			if err != nil {
				t.Fatalf("%v\n%s", err, out.String())
			}
			u.boot(p.Target)
			st := filepath.Join(u.saved[p.Keeper], step2.StateFile)
			b, _ := os.ReadFile(st)
			os.WriteFile(st, []byte(strings.Split(string(b), "armed ")[0]), 0o600)
			if c.stop {
				fh, _ := os.OpenFile(u.root.Path("/dev/mmcblk0"), os.O_WRONLY, 0)
				fh.WriteAt([]byte{0x88, 0x77, 0x66, 0x55}, 1024*512)
				fh.Close()
			} else {
				u.mark(p.Target)
			}
			img := u.image()
			r, _, _, err := u.service(nil)
			switch {
			case c.stop && !errors.Is(err, step2.ErrStop):
				t.Fatalf("no STOP without the arm line: %v %+v", err, r)
			case !c.stop && (err != nil || !strings.Contains(r.What, c.want)):
				t.Fatalf("%v %+v, want %q", err, r, c.want)
			}
			if !bytes.Equal(img, u.image()) {
				t.Fatal("wrote to the eMMC")
			}
		})
	}
}

// Review STEP2-REST-1 NIT-5: the data tool ends with exit 0 but writes no
// label; the payload changes during the run; the checkpoint fails: each
// stops with the keeper booting, and the first one before the arm.
func TestStep2RunGuards(t *testing.T) {
	bin, exe := bootctlBin(t)
	_, res := bundle()
	for _, c := range []struct{ mode, want string }{
		{"nolabel", "not ext4 label tsxdata"}, {"touch-payload", "payload folder changed"}, {"ckpt", "checkpoint"}} {
		t.Run(c.mode, func(t *testing.T) {
			u := newUnit(t, unitOf(res, shapes[0].ab, "a", "new"), "a", bin, exe)
			if c.mode == "ckpt" {
				u.ckptErr = errors.New("FIFREEZE (test)")
			} else {
				u.data.cut = c.mode
			}
			_, out, err := u.startRun(u.opts(nil))
			if err == nil || !strings.Contains(err.Error(), c.want) && !strings.Contains(out.String(), c.want) {
				t.Fatalf("err %v, want %q\n%s", err, c.want, out.String())
			}
			a := u.ab()
			if !a.Slots[0].Keeper() || a.Pick() != 0 || a.Slots[1].Bootable() && c.mode != "ckpt" {
				t.Fatalf("after the stop %v %v", a.Slots[0], a.Slots[1])
			}
			if c.mode == "ckpt" && !bytes.Equal(u.image(), u.before) {
				t.Fatal("a checkpoint failure wrote to the eMMC")
			}
		})
	}
}
