//go:build install && devtest

package main

// The two drive phases and the direct path of the on-screen flow, end to end
// (owner O1/O8, MAINLINE-FLOW-2026-11-10.md): the real devtest uc-pxx-inst runs
// `uc-pxx-inst flow --stdin`; step 1 is the flow's own stash child (`uc-pxx-inst plan
// --mainline-stash`, the real first drive phase of the device mode: the
// official open, the key, pass 1 of the fixture's userdebug firmware, the RAM
// stash); step 2 the real mainline device mode, the direct path (--direct)
// or the userdebug-first path, on a fake unit with the real
// uc-pxx-bootctl-inst r2 under qemu. The kill and power-cut sweeps of the
// direct path cut at the end of every step and resume through the flow.

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/devmode"
	"uc-pxx-installer/internal/drivestate"
	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/fatfs"
	"uc-pxx-installer/internal/fatfs/fattest"
	"uc-pxx-installer/internal/flow"
	"uc-pxx-installer/internal/flowio"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/keykeep"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/plan"
)

// dUnitReset is a factory-stock unit after the BCB-route entry: the libavb
// reset (both slots trying, A 15, retry 6 and 7); slot A holds the fixture's
// firmware (a known manifest), slot B the old one.
var dUnitReset = fakeemmc.Unit{AB: [2][3]byte{{15, 6, 0}, {14, 7, 0}}, A: "new", B: "old"}

// mlUnitFirst is the userdebug-first start: B (the fixture's boot chain)
// successful and booting, A the target.
var mlUnitFirst = fakeemmc.Unit{AB: [2][3]byte{{14, 0, 1}, {15, 0, 1}}, A: "old", B: "new"}

// stepOneDrive is the drive of step 1: the fixture's firmware .bin, the
// empty folder uc-pxx-mainline and the key file (the fake unit's systems have
// no updater library).
func stepOneDrive() []byte {
	im := fattest.New(fattest.Spec{Type: 16, Sectors: 2*len(fixture.FW)/512 + 40000, SecPerClus: 4, Label: "UC-PXX-INST"})
	im.Root.File("UC-P10 1.0.0.94 userdebug.bin", fixture.FW)
	im.Root.Mkdir(mainline.Dir)
	im.Root.File(flowio.KeyFileName, []byte(fmt.Sprintf("head_key=%x\niv=%x\n", fixture.Keys.Head[:], fixture.Keys.IV[:])))
	return im.B
}

// bundleDrive is the drive of step 2: the folder with the made-up bundle.
func bundleDrive(t *testing.T) ([]byte, string) {
	b, _ := mlBundleFor(t)
	im := fattest.New(fattest.Spec{Type: 16, Sectors: 2*(len(b.Rootfs)+len(b.Template))/512 + 40000, SecPerClus: 4, Label: "UC-PXX-INST"})
	d := im.Root.Mkdir(mainline.Dir)
	d.File(mainline.ManifestFile, b.ManifestJSON())
	d.File(mainline.TemplateFile, b.Template)
	d.File(mainline.RootfsFile, b.Rootfs)
	host, err := b.Write(filepath.Join(t.TempDir(), "host"))
	if err != nil {
		t.Fatal(err)
	}
	return im.B, host
}

// newStepUnit is a fake unit u whose drive holds step 1 (no stash yet). It
// returns the drive of step 2 and the folder that --test-review-bundle names.
func newStepUnit(t *testing.T, u fakeemmc.Unit) (*funit, []byte, string) {
	f := newFUnit(t, u)
	startImgs.Store(f, f.image())
	if err := os.WriteFile(f.root.Path(drivestate.Image), stepOneDrive(), 0o644); err != nil {
		t.Fatal(err)
	}
	b2, host := bundleDrive(t)
	return f, b2, host
}

// startImgs are the disks of the units at their start (directCut).
var startImgs sync.Map

// hostWrite: the computer writes the drive (it has it: the lun holds it).
func (f *funit) hostWrite(b []byte) {
	f.t.Helper()
	img := f.root.Path(drivestate.Image)
	if v, _ := os.ReadFile(f.root.Path(drivestate.LunFile)); strings.TrimSpace(string(v)) != img {
		f.t.Fatalf("the host does not have the drive (lun %q)", v)
	}
	if err := os.WriteFile(img, b, 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// stepOne: Install, the host ejects the step-1 drive, the stash child, its
// end: the drive is new, empty and back with the host.
func (u *uiProc) stepOne() {
	f := u.f
	n := strings.Count(f.flowLog(), "stash child ended")
	g := strings.Count(f.flowLog(), "drive given back to the computer")
	f.wait("status: A/B valid true", 60*time.Second)
	u.keys("j\r")
	f.wait("screen firmware", 10*time.Second)
	f.eject()
	f.wait("drive: mainline step 1: firmware", 30*time.Second)
	f.waitCount("stash child ended", n, 300*time.Second)
	if !strings.Contains(f.flowLog()[strings.LastIndex(f.flowLog(), "stash child ended"):], "stash child ended: exit 0; stash true") {
		f.t.Fatalf("step 1:\n%s", f.flowLog())
	}
	f.waitCount("drive given back to the computer", g, 30*time.Second)
	fs, err := fatfs.OpenImage(f.root.Path(drivestate.Image))
	if err != nil {
		f.t.Fatal(err)
	}
	top, _ := fs.ReadDir("/")
	fs.Close()
	if len(top) != 0 {
		f.t.Fatalf("the drive after step 1 holds %v", top)
	}
	if _, err := os.Stat(f.root.Path(flowio.FlowDir + "/keys.txt")); err == nil {
		f.t.Fatal("the key file stays after step 1")
	}
}

// confirmReplace: on the plan screen of a direct plan, Enter then y
// confirms that slot B is replaced (owner F1); the next Enter then y starts.
func (u *uiProc) confirmReplace() {
	f := u.f
	f.t.Helper()
	f.wait("screen plan", 10*time.Second)
	time.Sleep(300 * time.Millisecond)
	u.keys("\ry")
	time.Sleep(500 * time.Millisecond)
}

// stepTwo: the host copies the folder and ejects; the unlock and panel
// questions (the panel only if this UI has not asked it), the plan child.
func (u *uiProc) stepTwo(drive2 []byte, panel bool) {
	f := u.f
	n := strings.Count(f.flowLog(), "plan child ended")
	un := strings.Count(f.flowLog(), "screen unlock")
	pn := strings.Count(f.flowLog(), "screen panel")
	pl := strings.Count(f.flowLog(), "screen plan\n")
	f.hostWrite(drive2)
	f.eject()
	f.waitCount("screen unlock", un, 60*time.Second)
	if panel {
		u.yesTo("screen panel", pn)
		u.yesTo("plan child started", strings.Count(f.flowLog(), "plan child started"))
	} else {
		u.yesTo("plan child started", strings.Count(f.flowLog(), "plan child started"))
	}
	f.waitCount("plan child ended", n, 300*time.Second)
	f.waitCount("screen plan\n", pl, 10*time.Second) // the screen line comes after the end line (review M-5)
}

// yesTo answers yes from the terminal (Enter, the warning, y after
// flow.PanelWarnWait) until the flow log has a new line with next: on a
// loaded host the warning may be drawn late, and a y that comes too early
// is not an answer (the rule), so the y is sent again.
func (u *uiProc) yesTo(next string, n int) {
	f := u.f
	f.t.Helper()
	u.panelYes()
	for i := 0; i < 20 && strings.Count(f.flowLog(), next) <= n; i++ {
		time.Sleep(time.Second)
		if strings.Count(f.flowLog(), next) <= n {
			u.keys("y")
		}
	}
	f.waitCount(next, n, 10*time.Second)
}

// readDirectPlan reads the flow's direct plan file by its own id.
func (f *funit) readDirectPlan() *plan.DirectPlan {
	f.t.Helper()
	b, err := os.ReadFile(f.plan)
	if err != nil {
		f.t.Fatal(err)
	}
	p, err := plan.DirectPlanOf(b)
	if err != nil {
		f.t.Fatalf("the direct plan: %v", err)
	}
	return p
}

// directCut is the cardinal rule of the direct path on the bytes a cut
// leaves: the sector valid, bytes 32..511 as before; slot A bootable at every
// point, and A boots until the arm; slot A's eight partitions unchanged; a
// bootable B holds the whole payload (it is the armed state). It returns
// "keeper" or "target" (what boots) and whether B is armed.
func (f *funit) directCut(p *plan.DirectPlan, start []byte) (string, bool) {
	f.t.Helper()
	img := f.image()
	sec := img[plan.ABLBA*512 : (plan.ABLBA+1)*512]
	a, _ := avbab.Parse(sec)
	if err := a.Validate(); err != nil || !bytes.Equal(sec[32:], start[32:]) {
		f.t.Fatalf("the A/B sector: %v (bytes 32..511 kept %v)", err, bytes.Equal(sec[32:], start[32:]))
	}
	if !a.Slots[0].Bootable() {
		f.t.Fatalf("slot A is not bootable: %v %v", a.Slots[0], a.Slots[1])
	}
	v, _ := startImgs.Load(f)
	img0 := v.([]byte)
	t, _ := gpt.Parse(bytes.NewReader(img), int64(len(img)))
	t0, _ := gpt.Parse(bytes.NewReader(img0), int64(len(img0)))
	for _, n := range []string{"uboot", "trust", "dtbo", "vbmeta", "boot", "system", "vendor", "oem"} {
		e, ok := t.Find(n + "_a")
		e0, _ := t0.Find(n + "_a")
		if !ok || !bytes.Equal(img[e.Start*512:(e.Start+e.Sectors())*512], img0[e0.Start*512:(e0.Start+e0.Sectors())*512]) {
			f.t.Fatalf("%s_a changed", n)
		}
	}
	armed := a.Slots[1] == avbab.Slot{Priority: 15, Tries: 7}
	if a.Slots[1].Bootable() {
		// a bootable B holds none of this install (the reset, before R(K))
		// or all of it (armed): never a half-written B
		allOld, allNew := true, true
		for _, st := range p.Steps {
			if st.Write == nil {
				continue
			}
			w := st.Write
			h := devicetestSHA(img[w.StartLBA*512+w.Offset : w.StartLBA*512+w.Offset+w.Length])
			allOld, allNew = allOld && h == w.BeforeSHA256, allNew && h == w.SHA256
		}
		switch {
		case armed && !allNew:
			f.t.Fatalf("slot B is armed but does not hold the whole payload")
		case !armed && !(a.Slots[1] == avbab.Slot{Priority: 14, Tries: 7} && allOld):
			f.t.Fatalf("slot B is bootable (%v) and not as before the install, or partly written (old %v, new %v)", a.Slots[1], allOld, allNew)
		}
	}
	if !armed && a.Pick() != 0 {
		f.t.Fatalf("slot A does not boot before the arm: %v %v", a.Slots[0], a.Slots[1])
	}
	if a.Pick() == 0 {
		return "keeper", armed
	}
	return "target", armed
}

func devicetestSHA(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// directDone: slot B holds the stash's chain and the payload, A/B A{14,0,1}
// B{15,7,0}, both key-record sectors hold the fixture's record.
func (f *funit) directDone() {
	f.t.Helper()
	if s := f.endScreen(); s != "done" {
		f.t.Fatalf("end screen %q\n%s", s, f.flowLog())
	}
	p := f.readDirectPlan()
	if p.Keeper != "a" || p.Target != "b" || !p.UnlockDirect.Confirmed {
		f.t.Fatalf("plan keeper %s target %s unlock %v", p.Keeper, p.Target, p.UnlockDirect.Confirmed)
	}
	sec, _ := f.root.ABSector()
	a, _ := avbab.Parse(sec)
	if a.Pick() != 1 || a.Slots[1] != (avbab.Slot{Priority: 15, Tries: 7}) || a.Slots[0] != (avbab.Slot{Priority: 14, Successful: 1}) {
		f.t.Fatalf("A/B after: %v %v", a.Slots[0], a.Slots[1])
	}
	f.recordKept()
	img := f.image()
	t, _ := gpt.Parse(bytes.NewReader(img), int64(len(img)))
	for _, n := range []string{"uboot", "trust", "dtbo"} {
		e, _ := t.Find(n + "_b")
		if b := fixture.New[n]; !bytes.Equal(img[e.Start*512:e.Start*512+int64(len(b))], b) {
			f.t.Fatalf("%s_b does not hold the stash's image", n)
		}
	}
	log := f.flowLog()
	for _, want := range []string{"mainline: the path: direct", " --unlock-confirmed --direct", " --direct " + plan.ReplaceFlag,
		"mainline: the person confirmed that slot B is replaced", "after the install: readonly",
		"drive detached and unchanged to the end"} {
		if !strings.Contains(log, want) {
			f.t.Fatalf("no %q in the flow log:\n%s", want, log)
		}
	}
	for _, l := range strings.Split(log, "\n") {
		if strings.Contains(l, "repair child started") || strings.Contains(l, "child started:") && strings.Contains(l, "--keeper") {
			f.t.Fatalf("a repair child, or a keeper: %s", l)
		}
	}
}

// checkMLStepsDone is checkMLDone of a run with step 1: the stash child
// (plan --mainline-stash) takes --keys of the drive's key file; the plan
// and the install child take none.
func (f *funit) checkMLStepsDone() {
	t := f.t
	t.Helper()
	if s := f.endScreen(); s != "done" {
		t.Fatalf("end screen %q\n%s", s, f.flowLog())
	}
	p := f.mlPlan()
	if p.Keeper != "b" || p.Target != "a" || !p.Unlock.Confirmed {
		t.Fatalf("plan keeper %s target %s unlock %v", p.Keeper, p.Target, p.Unlock.Confirmed)
	}
	sec, _ := f.root.ABSector()
	a, _ := avbab.Parse(sec)
	if a.Pick() != 0 || a.Slots[0] != (avbab.Slot{Priority: 15, Tries: 7}) || a.Slots[1] != (avbab.Slot{Priority: 14, Successful: 1}) {
		t.Fatalf("A/B after: %v %v", a.Slots[0], a.Slots[1])
	}
	log := f.flowLog()
	stash := 0
	for _, l := range strings.Split(log, "\n") {
		switch {
		case strings.Contains(l, "plan child started:") && strings.Contains(l, " plan --mainline-stash "):
			stash++
			// (--autoupdate-unclear-confirmed is the devtest flow's argument of every plan child, --test-autoupdate-off)
			if strings.Contains(l, "--prereqs-confirmed") || strings.Contains(l, "--panel-confirmed") || strings.Contains(l, "--unlock-confirmed") ||
				strings.Contains(l, "--keeper") || strings.Contains(l, "--yes") {
				t.Fatalf("the stash child with a confirmation or a keeper: %s", l)
			}
		case strings.Contains(l, "child started:") && (strings.Contains(l, "--keys") || strings.Contains(l, "--keeper")):
			t.Fatalf("a mainline child with a key or a keeper: %s", l)
		case strings.Contains(l, "repair child started"):
			t.Fatalf("a repair child: %s", l)
		}
	}
	if stash == 0 {
		t.Fatalf("no stash child:\n%s", log)
	}
	f.checkBundle("done")
}

// recordKept: both key-record sectors hold the fixture's key record.
func (f *funit) recordKept() {
	f.t.Helper()
	img := f.image()
	rec := keykeep.Encode(fixture.Keys)
	recs, _, err := keptRecordsOf(img)
	if err != nil || !bytes.Equal(recs[0], rec) || !bytes.Equal(recs[1], rec) {
		f.t.Fatalf("the key record is not kept in both sectors (%v)", err)
	}
}

func keptRecordsOf(img []byte) ([2][]byte, [2]int64, error) {
	var recs [2][]byte
	t, err := gpt.Parse(bytes.NewReader(img), int64(len(img)))
	if err != nil {
		return recs, [2]int64{}, err
	}
	e, ok := t.Find(keykeep.Partition)
	if !ok {
		return recs, [2]int64{}, fmt.Errorf("no backup")
	}
	lbas, err := keykeep.LBAs(e.Start, e.Sectors())
	if err != nil {
		return recs, lbas, err
	}
	for i, l := range lbas {
		recs[i] = img[l*512 : (l+1)*512]
	}
	return recs, lbas, nil
}

// bcbReset is what the next page entry makes of the A/B sector: U-Boot's
// BCB writer scribbles misc, libavb resets the block to both slots trying
// (A 15 retry 6, B 14 retry 7; bytes 32..511 as they were). The page's
// pre-entry rule allows the entry only when the slot that boots next is
// successful and, if B boots, A is successful too.
func (f *funit) bcbReset() bool {
	f.t.Helper()
	sec, _ := f.root.ABSector()
	a, _ := avbab.Parse(sec)
	n := a.Pick()
	if a.Validate() == nil && n == 0 && a.Slots[0].Bootable() && !a.Slots[0].Keeper() {
		// a cut before R(K): the slot table is still the reset. The unit
		// first starts slot A (stock, its partitions unchanged: directCut),
		// whose Android marks it successful; only then may the page enter
		// again (its pre-entry rule)
		sec[8], sec[9], sec[10] = 15, 0, 1
		binary.BigEndian.PutUint32(sec[28:], crc32.ChecksumIEEE(sec[:28]))
		a, _ = avbab.Parse(sec)
	}
	if a.Validate() != nil || n < 0 || !a.Slots[n].Keeper() || n == 1 && !a.Slots[0].Keeper() {
		return false // the page refuses the entry
	}
	copy(sec[8:16], []byte{15, 6, 0, 0, 14, 7, 0, 0})
	binary.BigEndian.PutUint32(sec[28:], crc32.ChecksumIEEE(sec[:28]))
	m, err := os.OpenFile(f.root.Node("misc"), os.O_WRONLY, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	m.WriteAt(sec, (plan.ABLBA-plan.MiscStart)*512)
	m.Close()
	return true
}

// TestFlowMainlineStepsUserdebugFirst: both drive phases through the flow on
// a userdebug-first unit whose record sectors are empty (unit 3's state):
// step 1 (the stash), the swap, step 2, the install (keep-key first), Done,
// the record in both sectors.
func TestFlowMainlineStepsUserdebugFirst(t *testing.T) {
	f, d2, host := newStepUnit(t, mlUnitFirst)
	u := f.startUI("--test-child-args", "--test-review-bundle "+host)
	u.stepOne()
	u.stepTwo(d2, true)
	time.Sleep(500 * time.Millisecond)
	u.keys("\ry")
	f.wait("install child started", 20*time.Second)
	f.wait("bundle p10-install-", 600*time.Second)
	f.checkMLStepsDone()
	f.recordKept()
	if strings.Contains(f.flowLog(), "--direct") {
		t.Fatal("the userdebug-first unit got --direct")
	}
	u.quit()
}

// TestFlowMainlineDirect: the direct path through the flow on a
// factory-stock unit after the BCB-route entry: step 1, step 2 (the unlock
// for slot B), keep-key → R(K) → the six writes of B → the arm, Done.
func TestFlowMainlineDirect(t *testing.T) {
	f, d2, host := newStepUnit(t, dUnitReset)
	u := f.startUI("--test-child-args", "--test-review-bundle "+host+" --test-review-slots ab")
	u.stepOne()
	u.stepTwo(d2, true)
	u.confirmReplace()
	if strings.Contains(f.flowLog(), "install child started") {
		t.Fatalf("the install started on the confirmation of slot B:\n%s", f.flowLog())
	}
	u.keys("\ry")
	f.wait("install child started", 20*time.Second)
	f.wait("bundle p10-install-", 600*time.Second)
	f.directDone()
	u.quit()
}

// TestFlowMainlineDirectLockWrite (owner MAJOR-3): the direct path through
// the flow on a locked unit (the in-RAM reader's stub reads 0x00; the
// writer's stub reads 0x01 back): the plan holds the lock-state write
// intent; after the replace confirmation the plan screen shows the warning,
// and the install child starts only after a second Enter then y (the
// agreement) and a third (the start). The child gets --lockwrite-confirmed
// and --lockwrite-from-terminal; the writer's record says confirmed, from a
// terminal; the install ends Done as without the write.
func TestFlowMainlineDirectLockWrite(t *testing.T) {
	f, d2, host := newStepUnit(t, dUnitReset)
	u := f.startUI("--test-child-args", "--test-review-bundle "+host+" --test-review-slots ab --test-lock-state 0x00 --test-lock-write 0x01")
	u.stepOne()
	u.stepTwo(d2, true)
	f.wait("screen plan", 60*time.Second)
	p := f.readDirectPlan()
	if p.UnlockDirect.Write == nil || p.UnlockDirect.LockState != plan.LockStateLockedValue {
		t.Fatalf("the plan has no lock-state write intent: %+v", p.UnlockDirect)
	}
	u.confirmReplace()
	f.wait("screen plan", 10*time.Second)
	if strings.Contains(f.flowLog(), "install child started") {
		t.Fatalf("the install started on the confirmation of slot B:\n%s", f.flowLog())
	}
	// the agreement: Enter then y, counted only PanelWarnWait after the warning was drawn (flow review MINOR-A);
	// under load the draw can come late, so try again until state.json records the agreement (flow re-look NIT-R1)
	var st []byte
	for try := 0; try < 10 && !strings.Contains(string(st), `"lockwrite": {`); try++ {
		time.Sleep(flow.PanelWarnWait + time.Second)
		u.keys("\ry")
		for w := 0; w < 30 && !strings.Contains(string(st), `"lockwrite": {`); w++ {
			time.Sleep(100 * time.Millisecond)
			st, _ = os.ReadFile(f.root.Path(flowio.FlowDir + "/state.json"))
		}
	}
	if strings.Contains(f.flowLog(), "install child started") {
		t.Fatalf("the install started on the agreement:\n%s", f.flowLog())
	}
	if !strings.Contains(string(st), `"lockwrite": {`) || !strings.Contains(string(st), `"plan_id": "`+p.ID+`"`) {
		t.Fatalf("state.json has no record of the agreement:\n%s", st)
	}
	u.keys("\ry")
	f.wait("install child started", 20*time.Second)
	f.wait("bundle p10-install-", 600*time.Second)
	f.directDone()
	log := f.flowLog()
	for _, want := range []string{" --direct " + plan.ReplaceFlag + " " + plan.LockWriteFlag + " " + flow.LockWriteTerminalFlag,
		"mainline: the person agreed to the lock-state write, lock_state 0x00 -> 0x01 (" + plan.LockWriteFlag + "), from a terminal: true"} {
		if !strings.Contains(log, want) {
			t.Fatalf("no %q in the flow log:\n%s", want, log)
		}
	}
	rec, err := os.ReadFile(f.root.Path(devmode.LockWriteRecord))
	if err != nil || !strings.Contains(string(rec), `"confirmed": true`) || !strings.Contains(string(rec), `"confirm_from_terminal": true`) {
		t.Fatalf("the writer's record (%v):\n%s", err, rec)
	}
	u.quit()
}

// TestFlowMainlineDirectNoStepOne: the bundle on a direct-path unit without
// step 1: refused at once (no plan child), nothing written, the drive made
// empty for step 1.
func TestFlowMainlineDirectNoStepOne(t *testing.T) {
	f, d2, host := newStepUnit(t, dUnitReset)
	os.WriteFile(f.root.Path(drivestate.Image), d2, 0o644)
	before := f.image()
	u := f.startUI("--test-child-args", "--test-review-bundle "+host+" --test-review-slots ab")
	f.wait("status: A/B valid true", 60*time.Second)
	u.keys("j\r")
	f.wait("screen firmware", 10*time.Second)
	f.eject()
	f.wait("screen refused", 30*time.Second)
	f.wait("drive given back to the computer", 10*time.Second)
	if strings.Contains(f.flowLog(), "child started") || !bytes.Equal(f.image(), before) {
		t.Fatalf("a child, or a change:\n%s", f.flowLog())
	}
	u.quit()
}

// TestFlowMainlineStepOneUICrash: the UI dies while the stash child runs. A
// new UI: the drive is still detached; Install reads it again, waits for the
// stash child of the dead UI, and shows step 2 at once (no second stash).
func TestFlowMainlineStepOneUICrash(t *testing.T) {
	f, d2, host := newStepUnit(t, mlUnitFirst)
	u := f.startUI("--test-child-args", "--test-review-bundle "+host)
	f.wait("status: A/B valid true", 60*time.Second)
	u.keys("j\r")
	f.wait("screen firmware", 10*time.Second)
	f.eject()
	f.wait("plan child started: ", 30*time.Second)
	u.cmd.Process.Signal(syscall.SIGKILL)
	u.cmd.Wait()
	if strings.Contains(f.flowLog(), "stash child ended") {
		t.Log("step 1 ended before the UI was killed: the new UI finds the stash (TestStashWait covers the wait)")
	}
	u2 := f.startUI("--test-child-args", "--test-review-bundle "+host)
	f.wait("UI started again", 30*time.Second)
	f.waitCount("status: A/B valid true", 1, 60*time.Second)
	u2.keys("j\r")
	f.waitCount("screen firmware", 1, 10*time.Second)
	f.waitCount("drive: mainline step 1: firmware", 1, 300*time.Second)
	if !strings.Contains(f.flowLog()[strings.LastIndex(f.flowLog(), "drive: mainline step 1"):], "the stash of this boot is there: true") {
		t.Fatalf("the read did not wait for the stash child:\n%s", f.flowLog())
	}
	if strings.Count(f.flowLog(), "mainline step 1: the stash child reads") != 1 {
		t.Fatalf("a second stash child:\n%s", f.flowLog())
	}
	f.waitCount("drive given back to the computer", 0, 30*time.Second)
	u2.stepTwo(d2, true) // the first UI never asked the panel question (step 1 does not)
	time.Sleep(500 * time.Millisecond)
	u2.keys("\ry")
	f.wait("bundle p10-install-", 600*time.Second)
	f.checkMLStepsDone()
	u2.quit()
}

// TestFlowMainlineStepOneCut: a power cut while the stash child runs: the
// unit is unchanged; a new RAM image (no stash) does both steps again.
func TestFlowMainlineStepOneCut(t *testing.T) {
	f, d2, host := newStepUnit(t, mlUnitFirst)
	before := f.image()
	u := f.startUI("--test-child-args", "--test-review-bundle "+host)
	f.wait("status: A/B valid true", 60*time.Second)
	u.keys("j\r")
	f.wait("screen firmware", 10*time.Second)
	f.eject()
	f.wait("plan child started: ", 30*time.Second)
	f.cutAll(u, "plan-")
	if !bytes.Equal(f.image(), before) {
		t.Fatal("step 1 changed the unit")
	}
	os.RemoveAll(f.root.Path(flowio.RunDir))
	os.Remove(f.root.Path(device.ABLockPath))
	f.newDrive(stepOneDrive())
	u2 := f.startUI("--test-child-args", "--test-review-bundle "+host)
	u2.stepOne()
	u2.stepTwo(d2, true)
	time.Sleep(500 * time.Millisecond)
	u2.keys("\ry")
	f.wait("bundle p10-install-", 600*time.Second)
	f.checkMLStepsDone()
	u2.quit()
}

// cutAll is a power cut: every process of the run (the UI, the supervisor of
// the newest child folder with the prefix, and its child) at once.
func (f *funit) cutAll(u *uiProc, prefix string) {
	f.t.Helper()
	var dir string
	var sp int
	// the child folder and its supervisor.pid are read again on every turn:
	// the flow log's "plan child started" can come before both are there
	// (the race that made this test fail now and then: MAINLINE-RC2-LS)
	for i := 0; i < 100 && sp == 0; i++ {
		m, _ := filepath.Glob(f.root.Path(flowio.FlowDir + "/" + prefix + "*"))
		for _, d := range m {
			if st, err := os.Stat(d); err == nil && st.IsDir() {
				dir = d
			}
		}
		pid, _ := os.ReadFile(filepath.Join(dir, "supervisor.pid"))
		fmt.Sscan(string(pid), &sp)
		time.Sleep(50 * time.Millisecond)
	}
	if sp == 0 {
		f.t.Fatalf("no supervisor in %s", dir)
	}
	u.cmd.Process.Signal(syscall.SIGKILL)
	syscall.Kill(-sp, syscall.SIGKILL)
	u.cmd.Wait()
	time.Sleep(300 * time.Millisecond)
}

// newDrive is the RAM drive of a new RAM image (after a power cut) with b,
// with the host.
func (f *funit) newDrive(b []byte) {
	f.t.Helper()
	img := f.root.Path(drivestate.Image)
	os.MkdirAll(filepath.Dir(img), 0o755)
	if err := os.WriteFile(img, b, 0o644); err != nil {
		f.t.Fatal(err)
	}
	os.WriteFile(f.root.Path(drivestate.LunFile), []byte(img+"\n"), 0o644)
}

// TestFlowMainlineDirectSweep: the direct install through the flow, cut at
// the end of every step: a kill of the install child (the RAM and the stash
// stay: Stopped on a legal state, then the flow resumes to Done with a new
// copy of the folder), and a power cut of the whole run (the RAM and the
// stash gone): the cardinal rule on the disk bytes (slot A bootable at every
// point and booting until the arm, its partitions unchanged, B bootable only
// armed and whole), then the next entry (the page's BCB reset when its
// pre-entry rule allows it) and both steps again to Done. A cut after the
// arm leaves the installed, armed B (the install's verify ran before it).
func TestFlowMainlineDirectSweep(t *testing.T) {
	t.Parallel()
	f0, d20, host0 := newStepUnit(t, dUnitReset)
	u0 := f0.startUI("--test-child-args", "--test-review-bundle "+host0+" --test-review-slots ab --test-max-write 32768")
	u0.stepOne()
	u0.stepTwo(d20, true)
	u0.confirmReplace()
	u0.keys("\ry")
	f0.wait("bundle p10-install-", 600*time.Second)
	f0.directDone()
	points := stepWrites(t, f0.installLog())
	u0.quit()
	if len(points) < 6 {
		t.Fatalf("step ends %v", points)
	}
	t.Logf("direct flow: write counts at the step ends: %v", points)
	var kills, cuts atomic.Int64
	t.Cleanup(func() {
		t.Logf("direct flow sweep: %d kills and %d power cuts at step ends, each checked and resumed through the flow", kills.Load(), cuts.Load())
	})
	for _, k := range points {
		if k == 0 {
			continue
		}
		t.Run(fmt.Sprintf("kill after write %d", k), func(t *testing.T) {
			t.Parallel()
			sweepList(t, fmt.Sprintf("direct kill after write %d", k))
			f, d2, host := newStepUnit(t, dUnitReset)
			start, _ := f.root.ABSector()
			args := "--test-review-bundle " + host + " --test-review-slots ab --test-max-write 32768"
			u := f.startUI("--test-child-args", args+fmt.Sprintf(" --test-kill-after %d", k))
			u.stepOne()
			u.stepTwo(d2, true)
			u.confirmReplace()
			u.keys("\ry")
			f.wait("bundle p10-install-", 600*time.Second)
			p := f.readDirectPlan()
			boots, armed := f.directCut(p, start)
			if s := f.endScreen(); s != "stopped" || !strings.Contains(f.flowLog(), "install child ended: ended by the signal killed") {
				t.Fatalf("end screen %q (%s, armed %v)\n%s", s, boots, armed, f.flowLog())
			}
			f.checkBundle("stopped")
			u.quit()
			if armed {
				kills.Add(1)
				return // the install is whole (verified before the arm): the next restart starts B
			}
			// the resume through the flow: a new UI (the stop is kept), try
			// again, a new copy of the folder (the stash of this boot is there)
			st := strings.Count(f.flowLog(), "status: A/B valid true")
			u2 := f.startUI("--test-child-args", args)
			f.wait("UI started again: stopped", 30*time.Second)
			f.waitCount("status: A/B valid true", st, 60*time.Second)
			fw := strings.Count(f.flowLog(), "screen firmware")
			u2.keys("\r") // try again
			f.waitCount("screen firmware", fw, 10*time.Second)
			time.Sleep(300 * time.Millisecond)
			u2.stepTwo(d2, false)
			b := strings.Count(f.flowLog(), "bundle p10-install-")
			u2.confirmReplace()
			u2.keys("\ry")
			f.waitCount("bundle p10-install-", b, 600*time.Second)
			f.directDone()
			u2.quit()
			kills.Add(1)
		})
		t.Run(fmt.Sprintf("power cut after write %d", k), func(t *testing.T) {
			t.Parallel()
			sweepList(t, fmt.Sprintf("direct power cut after write %d", k))
			f, d2, host := newStepUnit(t, dUnitReset)
			start, _ := f.root.ABSector()
			args := "--test-review-bundle " + host + " --test-review-slots ab --test-max-write 32768"
			u := f.startUI("--test-child-args", args+fmt.Sprintf(" --test-pause-after %d", k))
			u.stepOne()
			u.stepTwo(d2, true)
			u.confirmReplace()
			u.keys("\ry")
			f.wait("install child started", 20*time.Second)
			var dir string
			for i := 0; i < 1200; i++ {
				m, _ := filepath.Glob(f.root.Path(flowio.FlowDir + "/install-*-*"))
				for _, d := range m {
					if st, err := os.Stat(d); err == nil && st.IsDir() {
						dir = d
					}
				}
				b, _ := os.ReadFile(filepath.Join(dir, "output.txt"))
				if strings.Contains(string(b), fmt.Sprintf("paused after write %d", k)) {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			pid, _ := os.ReadFile(filepath.Join(dir, "supervisor.pid"))
			var sp int
			fmt.Sscan(string(pid), &sp)
			u.cmd.Process.Signal(syscall.SIGKILL)
			syscall.Kill(-sp, syscall.SIGKILL)
			u.cmd.Wait()
			time.Sleep(300 * time.Millisecond)
			p := f.readDirectPlan()
			boots, armed := f.directCut(p, start)
			os.RemoveAll(f.root.Path(flowio.RunDir)) // the RAM is gone: the flow's state and the stash
			os.Remove(f.root.Path(device.ABLockPath))
			if armed {
				if boots != "target" {
					t.Fatalf("armed, but %s boots", boots)
				}
				cuts.Add(1)
				return // the next restart starts the whole, verified B; A is the fallback
			}
			// the next entry: the page's BCB route (the libavb reset), then a
			// new RAM image: both steps again
			if !f.bcbReset() {
				t.Fatal("the page's pre-entry rule refuses the entry after the cut")
			}
			f.newDrive(stepOneDrive())
			u2 := f.startUI("--test-child-args", args)
			u2.stepOne()
			u2.stepTwo(d2, true)
			u2.confirmReplace()
			u2.keys("\ry")
			f.wait("bundle p10-install-", 600*time.Second)
			f.directDone()
			u2.quit()
			cuts.Add(1)
		})
	}
}

// TestMainlineCLIDirectImageFlags (DIRECT-DRYRUN-UNIT3 F2): the image dry
// run takes --direct (with --stash, without --pre-ab) and the install's
// --replace-target-confirmed (only with --direct); `plan --mainline-stash`
// is the stash command. (The whole direct dry run on a real layout is
// ci/dryrun-real.py --shape direct: the image mode uses the embedded
// manifests, which the made-up fixture is not in.)
func TestMainlineCLIDirectImageFlags(t *testing.T) {
	dir := t.TempDir()
	img, err := fixture.Image(dir, "emmc.img", dUnitReset)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := mlBundleFor(t)
	d, err := b.Write(dir)
	if err != nil {
		t.Fatal(err)
	}
	common := []string{"--image", img, "--mainline", d, "--bootctl", "/nonexistent", "--prereqs-confirmed", "--unlock-confirmed"}
	for _, c := range []struct {
		args []string
		code int
		want string
	}{
		{append([]string{"plan", "--direct"}, common...), 2, "--direct needs --stash DIR"},
		{append([]string{"plan", "--direct", "--stash", dir, "--pre-ab", "00"}, common...), 2, "takes no --pre-ab"},
		{append([]string{"install", "--dry-run", "--replace-target-confirmed"}, common...), 2, "for --direct only"},
		{[]string{"plan", "--mainline-stash", "--image", img}, 2, "usage"},
	} {
		out, code := runBin(t, nil, c.args...)
		if code != c.code || !strings.Contains(out, c.want) || strings.Contains(out, "flag provided but not defined") {
			t.Fatalf("%q: exit %d\n%s", c.args, code, out)
		}
	}
}
