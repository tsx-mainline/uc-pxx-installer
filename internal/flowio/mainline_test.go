package flowio

// The mainline install in the runner (mainline.go) with the fake child of
// flowio_test.go: the read of a mainline drive, the children's arguments, the
// mainline plan file, the end screens, the hold without a repair, the log
// bundle and a UI started again. Nothing here touches a disk.

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
	"uc-pxx-installer/internal/fatfs/fattest"
	"uc-pxx-installer/internal/flow"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/mainline/mltest"
	"uc-pxx-installer/internal/plan"
)

// The A/B states of a mainline install (keeper B, target A).
var (
	mlS0 = sector([3]byte{14, 0, 1}, [3]byte{15, 0, 1})
	mlD1 = sector([3]byte{0, 0, 0}, [3]byte{15, 0, 1})
	mlD2 = sector([3]byte{15, 7, 0}, [3]byte{14, 0, 1})
)

func mlBundle() *mltest.Bundle {
	res := mltest.Fill(77, 5000)
	ch := mltest.Chain{Version: "1.0.0.94", BuildType: "userdebug", UBoot: strings.Repeat("1", 64), Trust: strings.Repeat("2", 64),
		Dtbo: strings.Repeat("3", 64), Vbmeta: strings.Repeat("4", 64), Resource: mltest.SHA(res)}
	return mltest.NewBundle("tok-flowio", 9000, 64<<10, ch, res, 38029312)
}

// mlPlanFile is a sealed mainline plan (the shape of plan.ComputeMainline):
// kind MainlineKind, keeper b, target a, the bundle's manifest.
func mlPlanFile(t *testing.T, b *mltest.Bundle) *plan.MainlinePlan {
	var m mainline.Manifest
	if err := json.Unmarshal(b.ManifestJSON(), &m); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(mlS0)
	s0, d1, d2 := plan.State(raw(mlS0)), plan.State(raw(mlD1)), plan.State(raw(mlD2))
	p := &plan.MainlinePlan{Plan: plan.Plan{Schema: 1, Kind: plan.MainlineKind, Mode: "device", Keeper: "b", Target: "a",
		AB: plan.AB{LBA: 49156, Now: s0, Final: d2, SectorSHA256: hex.EncodeToString(sum[:])},
		Steps: []plan.Step{
			{N: 1, Op: "bootctl", Bootctl: &plan.BootctlStep{Args: []string{"disable", "--slot", "a"}, Before: s0, After: d1}},
			{N: 2, Op: "write", Write: &plan.WriteStep{Partition: "boot_a", Length: 1 << 20}},
			{N: 3, Op: "write", Write: &plan.WriteStep{Partition: "system_a", Length: 2 << 20}},
			{N: 4, Op: "sync"},
			{N: 5, Op: "verify"},
			{N: 6, Op: "bootctl", Bootctl: &plan.BootctlStep{Args: []string{"arm-trying", "--slot", "a", "--lower-keeper"}, Before: d1, After: d2}},
			{N: 7, Op: "check-ab"},
		},
		Device: testPlan().Device},
		Mainline: &mainline.Payload{Kind: mainline.Kind, Source: "the folder uc-pxx-mainline of the P10-INSTALL drive",
			ManifestSHA256: mltest.SHA(b.ManifestJSON()), Manifest: m},
		Chain:  &plan.MainlineChain{Firmware: "UC-P10 1.0.0.94 userdebug", Version: "1.0.0.94", BuildType: "userdebug"},
		Unlock: plan.UnlockGate{Confirmed: true, Flag: plan.UnlockFlag}}
	p.Seal()
	if !p.VerifyID() || p.Plan.VerifyID() {
		t.Fatal("the test plan's ids")
	}
	return p
}

// newMLRig is newRig with a mainline bundle on the drive (and what build
// adds), the sector of a mainline start, a mainline plan for the fake plan
// child, and the fake child's command lines in args.log.
func newMLRig(t *testing.T, after []byte, ending string, build func(im *fattest.Image)) (*rig, *plan.MainlinePlan) {
	g := newRig(t, after, ending)
	dir := filepath.Dir(g.root)
	// the sector of a first mainline install, in the misc node and the disk
	for p, off := range map[string]int64{g.misc: 4 * 512, filepath.Join(g.root, "dev", "mmcblk0"): 49156 * 512} {
		f, err := os.OpenFile(p, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteAt(mlS0, off)
		f.Close()
	}
	b := mlBundle()
	im := fattest.New(fattest.Spec{Type: 16, Sectors: 70000, SecPerClus: 4, Label: "P10-INSTALL"})
	d := im.Root.Mkdir(mainline.Dir)
	d.File(mainline.ManifestFile, b.ManifestJSON())
	d.File(mainline.TemplateFile, b.Template)
	d.File(mainline.RootfsFile, b.Rootfs)
	im.Root.File(KeyFileName, []byte("head_key=00\niv=00\n")) // a key file on the drive: a mainline install takes none
	if build != nil {
		build(im)
	}
	os.WriteFile(filepath.Join(g.root, drivestate.Image), im.B, 0o644)
	mp := mlPlanFile(t, b)
	pj, _ := json.MarshalIndent(mp, "", "  ")
	os.WriteFile(filepath.Join(dir, "plan.json"), pj, 0o644)
	s, _ := os.ReadFile(g.r.c.Exe)
	s2 := strings.Replace(string(s), "case \"$1\" in", "[ \"$1\" = flow-supervise ] || echo \"$*\" >> \"$D/args.log\"\ncase \"$1\" in", 1)
	// the start sector the fake install child writes into the jsonl
	s2 = strings.Replace(s2, hex.EncodeToString(s0[:32]), hex.EncodeToString(mlS0[:32]), 1)
	os.WriteFile(g.r.c.Exe, []byte(s2), 0o755)
	return g, mp
}

func (g *rig) mlArgs() []string {
	b, _ := os.ReadFile(filepath.Join(filepath.Dir(g.root), "args.log"))
	if s := strings.TrimSpace(string(b)); s != "" {
		return strings.Split(s, "\n")
	}
	return nil
}

// toMLInstall: Install, the eject, the unlock question, the panel question
// (both answered from the terminal), the plan, Enter y.
func (g *rig) toMLInstall() {
	g.t.Helper()
	g.r.Start()
	g.pump(func() bool { return g.m.Unit != nil }, 20*time.Second)
	g.key(flow.KeyNext)
	g.key(flow.KeyOK)
	g.key(flow.KeyOK) // eject from the unit
	g.pump(func() bool { return g.m.Screen == flow.ScreenUnlock }, 30*time.Second)
	g.panelYes() // the same protocol: Enter, the warning drawn PanelWarnWait ago, y
	if g.m.Screen != flow.ScreenPanel || !g.m.UnlockFromTerminal() {
		g.t.Fatalf("after the unlock yes: %s", g.m.Screen)
	}
	g.panelYes()
	g.pump(func() bool { return g.m.Screen == flow.ScreenPlan }, 30*time.Second)
	g.key(flow.KeyOK)
	g.key(flow.KeyYes)
	if g.m.Screen != flow.ScreenProgress {
		g.t.Fatalf("screen %s", g.m.Screen)
	}
}

func TestMainlineFolderName(t *testing.T) {
	if flow.MainlineFolder != mainline.Dir {
		t.Fatalf("flow.MainlineFolder %q, mainline.Dir %q", flow.MainlineFolder, mainline.Dir)
	}
}

// TestMainlineRun: the whole mainline run on the fake child: the drive read
// (the manifest's facts, no key file copied), the children's arguments
// (--unlock-confirmed, --fw-drive-expect; no --keys, no --keeper), the plan
// screen, Done (exit 0, the final sector), the bundle with the mainline lines
// of result.txt, and a UI started again on Done.
func TestMainlineRun(t *testing.T) {
	g, mp := newMLRig(t, mlD2, "exit 0", nil)
	g.toMLInstall()
	g.end()
	if g.m.Screen != flow.ScreenDone || g.m.Outcome.State != flow.StateD2 {
		t.Fatalf("screen %s state %s (%s)", g.m.Screen, g.m.Outcome.State, g.m.Outcome.Why)
	}
	args := g.mlArgs()
	if len(args) != 2 || !strings.HasPrefix(args[0], "plan ") || !strings.HasPrefix(args[1], "install ") {
		t.Fatalf("children %q", args)
	}
	for _, a := range args {
		if !strings.Contains(a, " --fw-drive-expect size=") || !strings.Contains(a, " --prereqs-confirmed --panel-confirmed --unlock-confirmed") ||
			strings.Contains(a, "--keys") || strings.Contains(a, "--keeper") {
			t.Fatalf("child %q", a)
		}
	}
	if !strings.Contains(args[1], " --plan "+g.root+FlowDir+"/plan.json --yes --log ") {
		t.Fatalf("install %q", args[1])
	}
	if _, err := os.Lstat(g.r.keyFile()); err == nil {
		t.Fatal("the key file was copied for a mainline install")
	}
	log := strings.Join(g.r.log, "\n")
	for _, want := range []string{"drive: mainline bundle uc-pxx-mainline: version v0.0.1-test, token tok-flowio",
		"slot B must hold 1.0.0.94 userdebug", "unlock: \"Is the unit unlocked?\": yes, from a terminal, after its warning"} {
		if !strings.Contains(log, want) {
			t.Fatalf("no %q in the flow log:\n%s", want, log)
		}
	}
	if b := bodyText(g.m); !strings.Contains(b, "sshd runs within 300 s") {
		t.Fatalf("done:\n%s", b)
	}
	// the bundle: the plan file and the mainline lines of result.txt
	rd := filepath.Join(g.root, RunDir, "p10-install-"+mp.ID[:12])
	matches, _ := filepath.Glob(rd + "-*")
	if len(matches) != 1 {
		t.Fatalf("bundle %v", matches)
	}
	res, _ := os.ReadFile(filepath.Join(matches[0], "result.txt"))
	for _, want := range []string{"plan " + mp.ID + ": keeper b, target a",
		"mainline: uc-pxx-mainline v0.0.1-test, token tok-flowio, kernel " + mainline.ReviewedKernelRev,
		"slot b keeps Android userdebug 1.0.0.94 (UC-P10 1.0.0.94 userdebug)", "unlock in the plan: confirmed true (--unlock-confirmed)",
		"\"Is the unit unlocked?\" (the TEE unlock tool read back UNLOCKED): yes, from a terminal, after its warning"} {
		if !strings.Contains(string(res), want) {
			t.Fatalf("no %q in result.txt:\n%s", want, res)
		}
	}
	pj, _ := os.ReadFile(filepath.Join(matches[0], "plan.json"))
	if !strings.Contains(string(pj), plan.MainlineKind) {
		t.Fatal("the bundle's plan.json is not the mainline plan")
	}
	// a UI started again: Done with the mainline text (the plan's kind), and
	// its bundle line from mainline.json
	g2 := g.newUI()
	g2.pump(func() bool { return g2.m.Unit != nil }, 20*time.Second)
	if g2.m.Screen != flow.ScreenDone || !strings.Contains(bodyText(g2.m), "sshd runs within 300 s") || g2.m.Plan == nil || g2.m.Plan.ID != mp.ID {
		t.Fatalf("a new UI: %s\n%s", g2.m.Screen, bodyText(g2.m))
	}
	if got := g2.r.mainlineResult(g2.m.Plan); !strings.Contains(got, "yes, from a terminal, after its warning") {
		t.Fatalf("result after a new UI: %q", got)
	}
}

// TestMainlineEnds: the end of a mainline install child by the flow's rule:
// exit 3 is a hold, and no repair child starts (p10inst repair refuses
// mainline plans); exit 1 with the plan's D1 is Stopped.
func TestMainlineEnds(t *testing.T) {
	t.Run("exit 3: the hold, no repair", func(t *testing.T) {
		g, _ := newMLRig(t, mlD2, "exit 3", nil)
		g.toMLInstall()
		g.end()
		if g.m.Screen != flow.ScreenHold || g.m.Outcome.State != flow.StateD2 {
			t.Fatalf("screen %s state %s", g.m.Screen, g.m.Outcome.State)
		}
		time.Sleep(300 * time.Millisecond)
		g.pump(func() bool { return true }, time.Second)
		if g.m.Running() != "" || len(g.mlArgs()) != 2 {
			t.Fatalf("a child after the hold: %q (running %q)", g.mlArgs(), g.m.Running())
		}
		g.key(flow.KeyOK) // Check again: a read of the sector against the plan
		g.pump(func() bool {
			return strings.Contains(bodyText(g.m), "Checked again: slot A is installed and starts next. Keep the unit powered.")
		}, 10*time.Second)
		if g.m.RestartBlocked() == "" || strings.Contains(bodyText(g.m), "epair") || len(g.mlArgs()) != 2 {
			t.Fatalf("hold:\n%s\n%q", bodyText(g.m), g.mlArgs())
		}
		// a UI started again: still the hold, still no repair
		g2 := g.newUI()
		g2.pump(func() bool { return g2.m.Unit != nil }, 20*time.Second)
		if g2.m.Screen != flow.ScreenHold || g2.m.Running() != "" || len(g.mlArgs()) != 2 {
			t.Fatalf("a new UI: %s %q", g2.m.Screen, g.mlArgs())
		}
	})
	t.Run("exit 1, D1: stopped", func(t *testing.T) {
		g, _ := newMLRig(t, mlD1, "exit 1", nil)
		g.toMLInstall()
		g.end()
		if g.m.Screen != flow.ScreenStopped || g.m.Outcome.State != flow.StateD1 {
			t.Fatalf("screen %s state %s", g.m.Screen, g.m.Outcome.State)
		}
	})
}

// TestMainlineDriveRefusals: a drive with the folder and a .bin, a folder
// without manifest.json, a manifest that is not contract v1: refused on the
// firmware screen, the drive goes back, no child starts.
func TestMainlineDriveRefusals(t *testing.T) {
	for _, c := range []struct {
		name  string
		build func(im *fattest.Image)
		want  string
	}{
		{"a .bin too", func(im *fattest.Image) { im.Root.File("UC-P10 1.0.0.94.bin", []byte("x")) },
			"it holds the folder uc-pxx-mainline and a firmware file (UC-P10 1.0.0.94.bin): leave only one of them there"},
	} {
		t.Run(c.name, func(t *testing.T) {
			g, _ := newMLRig(t, mlD2, "exit 0", c.build)
			g.r.Start()
			g.pump(func() bool { return g.m.Unit != nil }, 20*time.Second)
			g.key(flow.KeyNext)
			g.key(flow.KeyOK)
			g.key(flow.KeyOK)
			g.pump(func() bool { return strings.Contains(bodyText(g.m), "The drive was refused: ") }, 30*time.Second)
			if !strings.Contains(bodyText(g.m), c.want) || g.m.Screen != flow.ScreenFirmware || len(g.mlArgs()) != 0 {
				t.Fatalf("%s\n%q", bodyText(g.m), g.mlArgs())
			}
			g.pump(func() bool {
				v, _ := os.ReadFile(g.lunFile())
				return strings.TrimSpace(string(v)) != ""
			}, 10*time.Second)
		})
	}
	// a manifest that is not contract v1, and a folder without one
	for name, edit := range map[string]func(b *mltest.Bundle) []byte{
		"a manifest with an unknown key": func(b *mltest.Bundle) []byte {
			b.Manifest["extra"] = 1
			return b.ManifestJSON()
		},
		"no manifest": func(b *mltest.Bundle) []byte { return nil },
	} {
		t.Run(name, func(t *testing.T) {
			g, _ := newMLRig(t, mlD2, "exit 0", nil)
			b := mlBundle()
			im := fattest.New(fattest.Spec{Type: 16, Sectors: 70000, SecPerClus: 4, Label: "P10-INSTALL"})
			d := im.Root.Mkdir(mainline.Dir)
			if m := edit(b); m != nil {
				d.File(mainline.ManifestFile, m)
			}
			d.File(mainline.TemplateFile, b.Template)
			os.WriteFile(filepath.Join(g.root, drivestate.Image), im.B, 0o644)
			g.r.Start()
			g.pump(func() bool { return g.m.Unit != nil }, 20*time.Second)
			g.key(flow.KeyNext)
			g.key(flow.KeyOK)
			g.key(flow.KeyOK)
			g.pump(func() bool { return strings.Contains(bodyText(g.m), "The drive was refused: ") }, 30*time.Second)
			if b := bodyText(g.m); !strings.Contains(b, "uc-pxx-mainline") || len(g.mlArgs()) != 0 {
				t.Fatalf("%s\n%q", b, g.mlArgs())
			}
		})
	}
}

// TestMainlineThenOfficial: after a mainline drive (refused at the unlock
// question) an official drive is the official flow: official arguments
// (with --keys of the key file), no unlock flag.
func TestMainlineThenOfficial(t *testing.T) {
	g, _ := newMLRig(t, d2, "exit 0", nil)
	g.r.Start()
	g.pump(func() bool { return g.m.Unit != nil }, 20*time.Second)
	g.key(flow.KeyNext)
	g.key(flow.KeyOK)
	g.key(flow.KeyOK)
	g.pump(func() bool { return g.m.Screen == flow.ScreenUnlock }, 30*time.Second)
	g.key(flow.KeyBack) // No
	if g.m.Screen != flow.ScreenRefused || !strings.Contains(bodyText(g.m), "Mainline Linux needs an unlocked unit") {
		t.Fatalf("%s\n%s", g.m.Screen, bodyText(g.m))
	}
	g.pump(func() bool {
		v, _ := os.ReadFile(g.lunFile())
		return strings.TrimSpace(string(v)) != ""
	}, 10*time.Second)
	// the user copies a firmware file instead (the official drive of newRig)
	im := fattest.New(fattest.Spec{Type: 16, Sectors: 70000, SecPerClus: 4, Label: "P10-INSTALL"})
	im.Root.File("fw.bin", []byte(strings.Repeat("firmware", 1000)))
	im.Root.File(KeyFileName, []byte("head_key=00\niv=00\n"))
	os.WriteFile(filepath.Join(g.root, drivestate.Image), im.B, 0o644)
	// the official plan for the fake child, and the official start sector
	pj, _ := json.MarshalIndent(testPlan(), "", "  ")
	os.WriteFile(filepath.Join(filepath.Dir(g.root), "plan.json"), pj, 0o644)
	f, _ := os.OpenFile(g.misc, os.O_RDWR, 0)
	f.WriteAt(s0, 4*512)
	f.Close()
	g.key(flow.KeyOK)                                                             // try again
	g.key(flow.KeyOK)                                                             // eject
	g.pump(func() bool { return g.m.Screen == flow.ScreenPanel }, 30*time.Second) // the official order: the panel question
	g.panelYes()
	g.pump(func() bool { return g.m.Screen == flow.ScreenPlan }, 30*time.Second)
	args := g.mlArgs()
	if len(args) != 1 || !strings.Contains(args[0], "--keys ") || strings.Contains(args[0], "--unlock-confirmed") || g.m.Mainline() {
		t.Fatalf("official plan child %q", args)
	}
	if _, err := os.Lstat(filepath.Join(g.root, mlRunFile)); err == nil {
		t.Fatal("mainline.json stays for an official plan")
	}
}

// TestReadPlanKinds: readPlan takes an official plan by its own id and a
// mainline plan by the mainline id; an edited mainline plan is refused.
func TestReadPlanKinds(t *testing.T) {
	dir := t.TempDir()
	mp := mlPlanFile(t, mlBundle())
	write := func(v any) string {
		b, _ := json.MarshalIndent(v, "", "  ")
		p := filepath.Join(dir, "p.json")
		os.WriteFile(p, b, 0o644)
		return p
	}
	if p, err := readPlan(write(mp)); err != nil || p.ID != mp.ID || p.Kind != plan.MainlineKind || p.Target != "a" {
		t.Fatalf("mainline plan: %v", err)
	}
	ed := *mp
	ed.Unlock.Confirmed = false
	if _, err := readPlan(write(&ed)); err == nil {
		t.Fatal("an edited mainline plan was read")
	}
	inner := mp.Plan // the embedded plan alone, with the mainline id
	if _, err := readPlan(write(&inner)); err == nil {
		t.Fatal("the embedded plan alone was read as a mainline plan")
	}
	off := testPlan()
	if p, err := readPlan(write(off)); err != nil || p.ID != off.ID {
		t.Fatalf("official plan: %v", err)
	}
}
