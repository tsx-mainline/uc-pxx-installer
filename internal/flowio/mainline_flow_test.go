package flowio

// The two drive phases and the direct path in the runner (mainline.go) with
// the fake child of flowio_test.go: the read of a step-1 drive (the key file
// copied, the .bin's facts), the stash child as the plan child (its
// arguments, its end, the drive made new and empty), step 2 with the stash
// there, the direct path's arguments and plan file, and the wait for a stash
// child of an earlier UI. Nothing here touches a disk.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"uc-pxx-installer/internal/drivestate"
	"uc-pxx-installer/internal/fatfs"
	"uc-pxx-installer/internal/fatfs/fattest"
	"uc-pxx-installer/internal/flow"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/mainline/mldrive"
	"uc-pxx-installer/internal/mlstash"
	"uc-pxx-installer/internal/plan"
)

// The A/B states of a direct install.
var (
	dS0 = sector([3]byte{15, 6, 0}, [3]byte{14, 7, 0})
	dRK = sector([3]byte{15, 0, 1}, [3]byte{0, 0, 0})
	dD2 = sector([3]byte{14, 0, 1}, [3]byte{15, 7, 0})
)

func TestMainlineStashDir(t *testing.T) {
	if mlStashDir != mlstash.Dir {
		t.Fatalf("mlStashDir %q, mlstash.Dir %q", mlStashDir, mlstash.Dir)
	}
}

// stepOneImage is a drive of step 1: the .bin, the empty folder, the key file.
func stepOneImage(extra func(im *fattest.Image)) []byte {
	im := fattest.New(fattest.Spec{Type: 16, Sectors: 70000, SecPerClus: 4, Label: "UC-PXX-INST"})
	im.Root.File("UC-P10 1.0.0.94 userdebug.bin", []byte(strings.Repeat("firmware", 1000)))
	im.Root.Mkdir(mainline.Dir)
	im.Root.File(KeyFileName, []byte("head_key=00\niv=00\n"))
	if extra != nil {
		extra(im)
	}
	return im.B
}

func TestStepOneShape(t *testing.T) {
	b := mlBundle()
	for _, c := range []struct {
		name string
		mk   func() []byte
		want bool
	}{
		{"one .bin and the empty folder", func() []byte { return stepOneImage(nil) }, true},
		{"and a stray file in the folder", func() []byte {
			im := fattest.New(fattest.Spec{Type: 16, Sectors: 70000, SecPerClus: 4, Label: "UC-PXX-INST"})
			im.Root.File("a.bin", []byte("x"))
			im.Root.Mkdir(mainline.Dir).File("._DS", []byte("y"))
			return im.B
		}, true},
		{"the folder holds a manifest", func() []byte {
			im := fattest.New(fattest.Spec{Type: 16, Sectors: 70000, SecPerClus: 4, Label: "UC-PXX-INST"})
			im.Root.File("a.bin", []byte("x"))
			im.Root.Mkdir(mainline.Dir).File("MANIFEST.JSON", b.ManifestJSON())
			return im.B
		}, false},
		{"the folder holds the rootfs", func() []byte {
			im := fattest.New(fattest.Spec{Type: 16, Sectors: 70000, SecPerClus: 4, Label: "UC-PXX-INST"})
			im.Root.File("a.bin", []byte("x"))
			im.Root.Mkdir(mainline.Dir).File(mainline.RootfsFile, []byte("r"))
			return im.B
		}, false},
		{"two .bin", func() []byte {
			return stepOneImage(func(im *fattest.Image) { im.Root.File("b.bin", []byte("x")) })
		}, false},
		{"the .bin in the folder", func() []byte {
			im := fattest.New(fattest.Spec{Type: 16, Sectors: 70000, SecPerClus: 4, Label: "UC-PXX-INST"})
			im.Root.Mkdir(mainline.Dir).File("a.bin", []byte("x"))
			return im.B
		}, false},
		{"no folder", func() []byte {
			im := fattest.New(fattest.Spec{Type: 16, Sectors: 70000, SecPerClus: 4, Label: "UC-PXX-INST"})
			im.Root.File("a.bin", []byte("x"))
			return im.B
		}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "d.img")
			os.WriteFile(p, c.mk(), 0o644)
			top, err := mldrive.TopOfImage(p)
			if err != nil {
				t.Fatal(err)
			}
			if got := stepOne(p, top); got != c.want {
				t.Fatalf("stepOne %v, want %v (%+v)", got, c.want, top)
			}
		})
	}
}

// newStashRig is newMLRig with a drive of step 1, and the fake uc-pxx-inst's
// stash child: `plan --mainline-stash ...` makes the stash folder (exit 0),
// or, with STASH=refuse in the rig's folder, prints a refusal (exit 1).
func newStashRig(t *testing.T, start, after []byte, ending string) *rig {
	g, _ := newMLRig(t, after, ending, nil)
	os.WriteFile(filepath.Join(g.root, drivestate.Image), stepOneImage(nil), 0o644)
	for p, off := range map[string]int64{g.misc: 4 * 512, filepath.Join(g.root, "dev", "mmcblk0"): 49156 * 512} {
		f, _ := os.OpenFile(p, os.O_RDWR, 0)
		f.WriteAt(start, off)
		f.Close()
	}
	s, _ := os.ReadFile(g.r.c.Exe)
	stash := `plan)
	if [ "$2" = --mainline-stash ]; then
		echo "pass 1: check the whole firmware against the manifests (nothing is written)"
		if [ -e "$D/STASH-REFUSE" ]; then echo "RESULT: pass 1: segment boot: sha256 differs"; exit 1; fi
		mkdir -p -m 0700 "` + g.root + mlStashDir + `"
		echo "RESULT: stash done (nothing written to the eMMC)"
		exit 0
	fi
`
	s2 := strings.Replace(string(s), "plan)\n", stash, 1)
	os.WriteFile(g.r.c.Exe, []byte(s2), 0o755)
	return g
}

// toStepTwo: Install, the eject, step 1 (the stash child), its end: the swap.
func (g *rig) toStepTwo() {
	g.t.Helper()
	g.r.Start()
	g.pump(func() bool { return g.m.Unit != nil }, 20*time.Second)
	g.key(flow.KeyNext)
	g.key(flow.KeyOK)
	g.key(flow.KeyOK) // eject
	g.pump(func() bool { return strings.Contains(strings.Join(g.r.log, "\n"), "stash child ended") }, 30*time.Second)
	g.pump(func() bool {
		v, _ := os.ReadFile(g.lunFile())
		return strings.TrimSpace(string(v)) != ""
	}, 10*time.Second)
}

// TestMainlineStepOneRun: the whole run of both phases on a userdebug-first
// unit: the step-1 drive read (the .bin, the key file copied), the stash
// child as the plan child (its argv: no confirmation, --keys of the copied
// key file), its end (the drive made new: empty; the key file removed), the
// swap view, then the bundle and the install to Done.
func TestMainlineStepOneRun(t *testing.T) {
	g := newStashRig(t, mlS0, mlD2, "exit 0")
	g.toStepTwo()
	if g.m.Screen != flow.ScreenFirmware || !strings.Contains(bodyText(g.m), "Step 1 is done") {
		t.Fatalf("after step 1: %s\n%s", g.m.Screen, bodyText(g.m))
	}
	args := g.mlArgs()
	want := "plan --mainline-stash --device /dev/mmcblk0 --fw-drive " + g.root + drivestate.Image + " --fw-drive-expect size="
	if len(args) != 1 || !strings.HasPrefix(args[0], want) || !strings.HasSuffix(args[0], " --keys "+g.root+FlowDir+"/keys.txt") ||
		strings.Contains(args[0], "confirmed") {
		t.Fatalf("the stash child %q", args)
	}
	if _, err := os.Lstat(g.r.keyFile()); err == nil {
		t.Fatal("the key file stays after step 1")
	}
	// the drive was made new and empty
	fs, err := fatfs.OpenImage(filepath.Join(g.root, drivestate.Image))
	if err != nil {
		t.Fatal(err)
	}
	top, _ := fs.ReadDir("/")
	fs.Close()
	if len(top) != 0 {
		t.Fatalf("the drive after step 1 holds %v", top)
	}
	log := strings.Join(g.r.log, "\n")
	for _, w := range []string{"drive: mainline step 1: firmware \"UC-P10 1.0.0.94 userdebug.bin\"", "the stash of this boot is there: false",
		"mainline step 1: the stash child reads the .bin", "stash child ended: exit 0; stash true", "action give-back-drive (make the drive new)"} {
		if !strings.Contains(log, w) {
			t.Fatalf("no %q in the flow log:\n%s", w, log)
		}
	}
	// step 2: the host copies the folder (the bundle of newMLRig) and ejects
	b := mlBundle()
	im := fattest.New(fattest.Spec{Type: 16, Sectors: 70000, SecPerClus: 4, Label: "UC-PXX-INST"})
	d := im.Root.Mkdir(mainline.Dir)
	d.File(mainline.ManifestFile, b.ManifestJSON())
	d.File(mainline.TemplateFile, b.Template)
	d.File(mainline.RootfsFile, b.Rootfs)
	os.WriteFile(filepath.Join(g.root, drivestate.Image), im.B, 0o644)
	g.key(flow.KeyOK) // eject
	g.pump(func() bool { return g.m.Screen == flow.ScreenUnlock }, 30*time.Second)
	if g.m.MainlineDirect() {
		t.Fatal("the userdebug-first unit took the direct path")
	}
	g.panelYes()
	g.panelYes()
	g.pump(func() bool { return g.m.Screen == flow.ScreenPlan }, 30*time.Second)
	g.key(flow.KeyOK)
	g.key(flow.KeyYes)
	g.end()
	if g.m.Screen != flow.ScreenDone {
		t.Fatalf("screen %s", g.m.Screen)
	}
	args = g.mlArgs()
	if len(args) != 3 || !strings.HasPrefix(args[1], "plan --device") || strings.Contains(args[1], "--direct") ||
		!strings.HasPrefix(args[2], "install --device") || strings.Contains(args[2], "--direct") || strings.Contains(args[2], "--keys") {
		t.Fatalf("children %q", args)
	}
	if !strings.Contains(strings.Join(g.r.log, "\n"), "mainline: the stash of step 1 is there: true") {
		t.Fatalf("flow log:\n%s", strings.Join(g.r.log, "\n"))
	}
}

// TestMainlineStepOneRefused: the stash child refuses: the refused screen
// with its words, nothing remade (the .bin is still on the drive), and a
// drive of step 1 with the stash there already: no second stash child.
func TestMainlineStepOneRefused(t *testing.T) {
	g := newStashRig(t, mlS0, mlD2, "exit 0")
	os.WriteFile(filepath.Join(filepath.Dir(g.root), "STASH-REFUSE"), nil, 0o644)
	g.toStepTwo()
	if g.m.Screen != flow.ScreenRefused || !strings.Contains(bodyText(g.m), "not the reviewed 1.0.0.94 userdebug firmware") {
		t.Fatalf("%s\n%s", g.m.Screen, bodyText(g.m))
	}
	fs, err := fatfs.OpenImage(filepath.Join(g.root, drivestate.Image))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.ReadFile("/UC-P10 1.0.0.94 userdebug.bin", 1<<20); err != nil {
		t.Fatalf("the .bin is gone after a refusal: %v", err)
	}
	fs.Close()
	// the stash of this boot is there (made by hand here): no child, the swap at once
	os.MkdirAll(filepath.Join(g.root, mlStashDir), 0o700)
	n := len(g.mlArgs())
	g.key(flow.KeyOK) // try again
	g.key(flow.KeyOK) // eject
	g.pump(func() bool {
		return g.m.Screen == flow.ScreenFirmware && strings.Contains(bodyText(g.m), "Step 1 was done already")
	}, 30*time.Second)
	if len(g.mlArgs()) != n || g.m.Running() != "" {
		t.Fatalf("a child for a step-1 drive with the stash there: %q", g.mlArgs())
	}
}

// dPlanFile is a sealed direct plan of the shape of plan.ComputeMainlineDirect.
func dPlanFile(t *testing.T) *plan.DirectPlan {
	mp := mlPlanFile(t, mlBundle())
	sum := sha256.Sum256(dS0)
	s0, rk, d2 := plan.State(raw(dS0)), plan.State(raw(dRK)), plan.State(raw(dD2))
	p := &plan.DirectPlan{MainlinePlan: *mp}
	p.Kind, p.Keeper, p.Target = plan.MainlineDirectKind, "a", "b"
	p.AB = plan.AB{LBA: 49156, Now: s0, Final: d2, SectorSHA256: hex.EncodeToString(sum[:])}
	p.Steps = []plan.Step{
		{N: 1, Op: "bootctl", Bootctl: &plan.BootctlStep{Args: []string{"repair", "--keeper", "a", "--expect-sector", "00"}, Before: s0, After: rk}},
		{N: 2, Op: "bootctl", Bootctl: &plan.BootctlStep{Args: []string{"disable", "--slot", "b"}, Before: rk, After: rk}},
		{N: 3, Op: "write", Write: &plan.WriteStep{Partition: "boot_b", Length: 1 << 20}},
		{N: 4, Op: "write", Write: &plan.WriteStep{Partition: "system_b", Length: 2 << 20}},
		{N: 5, Op: "sync"},
		{N: 6, Op: "verify"},
		{N: 7, Op: "bootctl", Bootctl: &plan.BootctlStep{Args: []string{"arm-trying", "--slot", "b", "--lower-keeper"}, Before: rk, After: d2}},
		{N: 8, Op: "check-ab"},
	}
	p.Stash = &plan.StashRecord{IndexSHA256: strings.Repeat("5", 64), FirmwareName: "1.0.0.94 (userdebug)", FirmwareSHA256: strings.Repeat("c", 64)}
	p.UnlockDirect = plan.DirectUnlock{Confirmed: true, Flag: plan.UnlockFlag, LockState: "not read: no in-RAM reader", Basis: "the person's confirmation"}
	p.ID = ""
	p.Seal()
	if !p.VerifyID() || p.MainlinePlan.VerifyID() || p.Plan.VerifyID() {
		t.Fatal("the direct test plan's ids")
	}
	return p
}

// TestMainlineDirectRun: both phases on a factory-stock unit after the
// BCB-route entry: step 1, then the bundle: the direct path (--direct for
// the plan and the install child), the direct plan file read by its own id,
// Done, and result.txt with the direct lines; a UI started again shows Done.
func TestMainlineDirectRun(t *testing.T) { directRun(t, false) }

// TestMainlineDirectRunLockWrite (owner MAJOR-3): a direct plan of a locked
// unit (the lock-state write intent): after the replace confirmation the
// plan screen shows the warning, and only the next Enter then y agrees; the
// install child then gets --lockwrite-confirmed and --lockwrite-from-terminal,
// and the agreement is in the flow log, state.json, mainline.json and
// result.txt.
func TestMainlineDirectRunLockWrite(t *testing.T) { directRun(t, true) }

// dLockPlanFile is dPlanFile of a locked unit: the in-RAM reader read 0x00,
// the plan holds the lock-state write intent and its check.
func dLockPlanFile(t *testing.T) *plan.DirectPlan {
	p := dPlanFile(t)
	p.UnlockDirect.LockState = plan.LockStateLockedValue
	p.UnlockDirect.Write = &plan.LockStateWriteIntent{From: plan.LockStateLockedValue, To: plan.LockStateUnlockedValue, Security: plan.SecurityPartNode}
	p.Checks = append(p.Checks, plan.Check{ID: plan.LockWriteCheck, OK: true, Detail: "keeper A is a proven Crestron stock slot"})
	p.ID = ""
	p.Seal()
	if !p.VerifyID() {
		t.Fatal("the lock test plan's id")
	}
	return p
}

func directRun(t *testing.T, lock bool) {
	g := newStashRig(t, dS0, dD2, "exit 0")
	dp := dPlanFile(t)
	if lock {
		dp = dLockPlanFile(t)
	}
	pj, _ := json.MarshalIndent(dp, "", "  ")
	os.WriteFile(filepath.Join(filepath.Dir(g.root), "plan.json"), pj, 0o644)
	// the start sector the fake install child writes into the jsonl
	s, _ := os.ReadFile(g.r.c.Exe)
	os.WriteFile(g.r.c.Exe, []byte(strings.Replace(string(s), hex.EncodeToString(mlS0[:32]), hex.EncodeToString(dS0[:32]), 1)), 0o755)
	g.toStepTwo()
	b := mlBundle()
	im := fattest.New(fattest.Spec{Type: 16, Sectors: 70000, SecPerClus: 4, Label: "UC-PXX-INST"})
	d := im.Root.Mkdir(mainline.Dir)
	d.File(mainline.ManifestFile, b.ManifestJSON())
	d.File(mainline.TemplateFile, b.Template)
	d.File(mainline.RootfsFile, b.Rootfs)
	os.WriteFile(filepath.Join(g.root, drivestate.Image), im.B, 0o644)
	g.key(flow.KeyOK)
	g.pump(func() bool { return g.m.Screen == flow.ScreenUnlock }, 30*time.Second)
	if !g.m.MainlineDirect() || !strings.Contains(bodyText(g.m), "It is for slot B.") {
		t.Fatalf("not the direct path:\n%s", bodyText(g.m))
	}
	g.panelYes()
	g.panelYes()
	g.pump(func() bool { return g.m.Screen == flow.ScreenPlan }, 30*time.Second)
	if g.m.Plan.Kind != plan.MainlineDirectKind || g.m.Plan.ID != dp.ID {
		t.Fatalf("plan %s %s", g.m.Plan.Kind, g.m.Plan.ID)
	}
	g.key(flow.KeyOK)
	g.key(flow.KeyYes) // confirms the replacement of slot B
	if g.m.Screen != flow.ScreenPlan || !g.m.ReplaceConfirmed() || len(g.mlArgs()) != 2 {
		t.Fatalf("after the confirmation: %s %q", g.m.Screen, g.mlArgs())
	}
	if lock {
		v := bodyText(g.m)
		if !strings.Contains(v, "WARNING: the unit is locked (lock_state 0x00).") || !strings.Contains(v, "agree to the lock-state write") ||
			g.m.LockWriteConfirmed() {
			t.Fatalf("no warning before the agreement:\n%s", v)
		}
		g.key(flow.KeyOK)
		g.key(flow.KeyYes) // agrees to the lock-state write
		if g.m.Screen != flow.ScreenPlan || !g.m.LockWriteConfirmed() || !g.m.LockWriteFromTerminal() || len(g.mlArgs()) != 2 {
			t.Fatalf("after the agreement: %s %v %q", g.m.Screen, g.m.LockWriteConfirmed(), g.mlArgs())
		}
		st, _ := os.ReadFile(filepath.Join(g.root, FlowDir, "state.json"))
		if !strings.Contains(string(st), `"lockwrite": {`) || !strings.Contains(string(st), `"from_terminal": true`) ||
			!strings.Contains(string(st), `"plan_id": "`+dp.ID+`"`) {
			t.Fatalf("state.json has no record of the agreement:\n%s", st)
		}
	} else if g.m.LockWriteConfirmed() || strings.Contains(bodyText(g.m), "WARNING: the unit is locked") {
		t.Fatalf("a lock-state warning for a plan with no write intent:\n%s", bodyText(g.m))
	}
	g.key(flow.KeyOK)
	g.key(flow.KeyYes)
	g.end()
	if g.m.Screen != flow.ScreenDone || g.m.Outcome.State != flow.StateD2 {
		t.Fatalf("screen %s state %s (%s)", g.m.Screen, g.m.Outcome.State, g.m.Outcome.Why)
	}
	args := g.mlArgs()
	tail := " --unlock-confirmed --direct " + plan.ReplaceFlag
	if lock {
		tail += " " + plan.LockWriteFlag + " " + plan.LockWriteTerminalFlag
	}
	if len(args) != 3 || !strings.HasSuffix(args[1], " --unlock-confirmed --direct") || !strings.HasSuffix(args[2], tail) ||
		!strings.Contains(args[2], " --plan ") || strings.Contains(args[1], "lockwrite") {
		t.Fatalf("children %q", args)
	}
	if lock != strings.Contains(strings.Join(g.r.log, "\n"), "mainline: the person agreed to the lock-state write, lock_state 0x00 -> 0x01 (--lockwrite-confirmed), from a terminal: true") {
		t.Fatalf("lock %v: the agreement line in the flow log:\n%s", lock, strings.Join(g.r.log, "\n"))
	}
	mj, _ := os.ReadFile(filepath.Join(g.root, FlowDir, "mainline.json"))
	if lock != strings.Contains(string(mj), `"lockwrite": {`) || !strings.Contains(string(mj), `"direct": true`) {
		t.Fatalf("lock %v: mainline.json:\n%s", lock, mj)
	}
	if !strings.Contains(strings.Join(g.r.log, "\n"), "mainline: the path: direct") {
		t.Fatal("no path line in the flow log")
	}
	matches, _ := filepath.Glob(filepath.Join(g.root, RunDir, "p10-install-"+dp.ID[:12]) + "-*")
	if len(matches) != 1 {
		t.Fatalf("bundle %v", matches)
	}
	res, _ := os.ReadFile(filepath.Join(matches[0], "result.txt"))
	for _, w := range []string{"mainline (the direct path): uc-pxx-mainline v0.0.1-test", "slot a keeps its Crestron firmware; slot b gets mainline",
		"step 1 (the stash): 1.0.0.94 (userdebug), index sha256 5555",
		"the lock-state screen: go on (the direct path: the installer reads lock_state itself), from a terminal, after its warning"} {
		if !strings.Contains(string(res), w) {
			t.Fatalf("no %q in result.txt:\n%s", w, res)
		}
	}
	lockLines := []string{"the lock-state write in the plan: lock_state 0x00 -> 0x01 on mmcblk0p16, before any target write",
		"the lock-state write (lock_state 0x00 -> 0x01): agreed on the plan screen (--lockwrite-confirmed), from a terminal true, plan " + dp.ID}
	if !lock {
		lockLines = []string{"unlock in the plan: confirmed true (--unlock-confirmed), lock_state not read", "the lock-state write: not agreed in this run"}
	}
	for _, w := range lockLines {
		if !strings.Contains(string(res), w) {
			t.Fatalf("no %q in result.txt:\n%s", w, res)
		}
	}
	g2 := g.newUI()
	g2.pump(func() bool { return g2.m.Unit != nil }, 20*time.Second)
	if g2.m.Screen != flow.ScreenDone || g2.m.Plan == nil || g2.m.Plan.ID != dp.ID || !strings.Contains(bodyText(g2.m), "slot A were not written") {
		t.Fatalf("a new UI: %s\n%s", g2.m.Screen, bodyText(g2.m))
	}
}

// TestMainlineDirectHold: exit 3 of a direct install: the hold, no repair
// child; Check again reads the sector against the direct plan.
func TestMainlineDirectHold(t *testing.T) {
	g := newStashRig(t, dS0, dRK, "exit 3")
	dp := dPlanFile(t)
	pj, _ := json.MarshalIndent(dp, "", "  ")
	os.WriteFile(filepath.Join(filepath.Dir(g.root), "plan.json"), pj, 0o644)
	s, _ := os.ReadFile(g.r.c.Exe)
	os.WriteFile(g.r.c.Exe, []byte(strings.Replace(string(s), hex.EncodeToString(mlS0[:32]), hex.EncodeToString(dS0[:32]), 1)), 0o755)
	g.toStepTwo()
	b := mlBundle()
	im := fattest.New(fattest.Spec{Type: 16, Sectors: 70000, SecPerClus: 4, Label: "UC-PXX-INST"})
	d := im.Root.Mkdir(mainline.Dir)
	d.File(mainline.ManifestFile, b.ManifestJSON())
	d.File(mainline.TemplateFile, b.Template)
	d.File(mainline.RootfsFile, b.Rootfs)
	os.WriteFile(filepath.Join(g.root, drivestate.Image), im.B, 0o644)
	g.key(flow.KeyOK)
	g.pump(func() bool { return g.m.Screen == flow.ScreenUnlock }, 30*time.Second)
	g.panelYes()
	g.panelYes()
	g.pump(func() bool { return g.m.Screen == flow.ScreenPlan }, 30*time.Second)
	g.key(flow.KeyOK)
	g.key(flow.KeyYes) // the replacement of slot B
	g.key(flow.KeyOK)
	g.key(flow.KeyYes)
	g.end()
	if g.m.Screen != flow.ScreenHold || g.m.Outcome.State != flow.StateD1 {
		t.Fatalf("screen %s state %s", g.m.Screen, g.m.Outcome.State)
	}
	time.Sleep(300 * time.Millisecond)
	g.pump(func() bool { return true }, time.Second)
	if g.m.Running() != "" || len(g.mlArgs()) != 3 {
		t.Fatalf("a child after the hold: %q", g.mlArgs())
	}
	g.key(flow.KeyOK) // Check again
	g.pump(func() bool { return strings.Contains(bodyText(g.m), "Checked again:") }, 10*time.Second)
	if g.m.RestartBlocked() == "" || len(g.mlArgs()) != 3 {
		t.Fatalf("hold:\n%s\n%q", bodyText(g.m), g.mlArgs())
	}
}

// TestReadPlanDirect: readPlan takes a direct plan by its own id; an edited
// one, and its embedded plans alone, are refused.
func TestReadPlanDirect(t *testing.T) {
	dir := t.TempDir()
	dp := dPlanFile(t)
	write := func(v any) string {
		b, _ := json.MarshalIndent(v, "", "  ")
		p := filepath.Join(dir, "p.json")
		os.WriteFile(p, b, 0o644)
		return p
	}
	if p, err := readPlan(write(dp)); err != nil || p.ID != dp.ID || p.Kind != plan.MainlineDirectKind || p.Target != "b" {
		t.Fatalf("direct plan: %v", err)
	}
	ed := *dp
	ed.UnlockDirect.Confirmed = false
	if _, err := readPlan(write(&ed)); err == nil {
		t.Fatal("an edited direct plan was read")
	}
	for _, inner := range []any{&dp.MainlinePlan, &dp.MainlinePlan.Plan} {
		if _, err := readPlan(write(inner)); err == nil {
			t.Fatal("an embedded plan alone was read as a direct plan")
		}
	}
}

// TestStashWait: a step-1 drive read waits for a stash child of an earlier
// UI (its command line in the process table) and goes on when it is gone;
// another drive's stash child is not waited for.
func TestStashWait(t *testing.T) {
	g, _ := newMLRig(t, mlD2, "exit 0", nil)
	proc := t.TempDir()
	g.r.c.Proc = proc
	os.WriteFile(filepath.Join(g.root, drivestate.Image), stepOneImage(nil), 0o644)
	os.WriteFile(g.lunFile(), []byte("\n"), 0o644)
	cmd := func(drive string) []byte {
		return []byte(strings.Join([]string{g.r.c.Exe, "plan", "--mainline-stash", "--device", "/dev/mmcblk0", "--fw-drive", drive}, "\x00") + "\x00")
	}
	os.MkdirAll(filepath.Join(proc, "4242"), 0o755)
	os.WriteFile(filepath.Join(proc, "4242", "cmdline"), cmd("/other/drive.img"), 0o644)
	if g.r.stashRuns() {
		t.Fatal("waits for another drive's stash child")
	}
	os.WriteFile(filepath.Join(proc, "4242", "cmdline"), cmd(g.r.drive()), 0o644)
	if !g.r.stashRuns() {
		t.Fatal("the stash child of this drive is not seen")
	}
	done := make(chan flow.MainlineDrive, 1)
	go func() { done <- g.r.readStepOne() }()
	select {
	case r := <-done:
		t.Fatalf("the read did not wait: %+v", r)
	case <-time.After(1500 * time.Millisecond):
	}
	os.RemoveAll(filepath.Join(proc, "4242"))
	select {
	case r := <-done:
		if !r.OK || !r.Stash || !r.KeyFile || r.FWName != `"UC-P10 1.0.0.94 userdebug.bin"` {
			t.Fatalf("read %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the read still waits")
	}
}
