package flow

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/plan"
	"uc-pxx-installer/internal/status"
)

// The mainline install in the flow (mainline.go, mainline_render.go): the
// unlock screen and its confirm protocol, the keeper fixed to B, the mainline
// texts of the planning, plan, done, stopped and refused screens, the hold
// without a repair, the resume of a mainline run, the children's arguments,
// and every new screen on the panel. The official flow is the tests of the
// other files, unchanged.

// The A/B states of a mainline install: keeper B successful and booting,
// target A (MAINLINE-INSTALL-PLAN.md Q1).
var (
	mlS0 = sector([3]byte{14, 0, 1}, [3]byte{15, 0, 1}, 0)
	mlD1 = sector([3]byte{0, 0, 0}, [3]byte{15, 0, 1}, 0)
	mlD2 = sector([3]byte{15, 7, 0}, [3]byte{14, 0, 1}, 0)
)

// mlTestPlan is the embedded Plan of a mainline plan (plan.ComputeMainline):
// kind MainlineKind, keeper b, target a, the eleven steps (disable, uboot,
// trust skipped, dtbo, vbmeta, boot, system, sync, verify, arm, check-ab).
func mlTestPlan() *plan.Plan {
	s0, d1, d2 := raw32(mlS0), raw32(mlD1), raw32(mlD2)
	sum := sha256.Sum256(mlS0)
	w := func(n int, part string, l int64) plan.Step {
		return plan.Step{N: n, Op: "write", Write: &plan.WriteStep{Partition: part, Length: l, Action: "write"}}
	}
	sk := w(3, "trust_a", 4<<20)
	sk.Op, sk.Write.Action = "skip", "skip-identical"
	return &plan.Plan{Schema: 1, Kind: plan.MainlineKind, ID: strings.Repeat("9e1c", 16), Mode: "device", Keeper: "b", Target: "a",
		AB: plan.AB{Now: plan.State(s0), Final: plan.State(d2), SectorSHA256: hex.EncodeToString(sum[:])},
		Steps: []plan.Step{
			{N: 1, Op: "bootctl", Bootctl: &plan.BootctlStep{Args: []string{"disable", "--slot", "a"}, Before: plan.State(s0), After: plan.State(d1)}},
			w(2, "uboot_a", 4<<20), sk, w(4, "dtbo_a", 1<<20), w(5, "vbmeta_a", 4096), w(6, "boot_a", 30<<20), w(7, "system_a", 900<<20),
			{N: 8, Op: "sync"},
			{N: 9, Op: "verify"},
			{N: 10, Op: "bootctl", Bootctl: &plan.BootctlStep{Args: []string{"arm-trying", "--slot", "a", "--lower-keeper"}, Before: plan.State(d1), After: plan.State(d2)}},
			{N: 11, Op: "check-ab"},
		}}
}

var mlDriveOK = MainlineDrive{OK: true, Ident: "size=1,mtime=1,ctime=1,dev=1,ino=1", Size: 931 << 20,
	Version: "v1.0.0-rc1", Token: "ucpxx-5e1a0c2d", Kernel: "d02e92631f2810a4a43fb6f09824afb9b0044ee2",
	ChainVersion: "1.0.0.94", ChainBuild: "userdebug", ManifestSHA256: strings.Repeat("ab", 32)}

// newMLDrive is newDrive on the unit of a first mainline install: slot B
// (Android userdebug) successful and booting, slot A the stock firmware.
func newMLDrive(t *testing.T, terminal bool) *drive {
	d := newDrive(t, terminal, "off")
	u := *d.m.Unit
	u.Next = 1
	u.Slots[0] = SlotView{Name: "a", AB: avbab.Slot{Priority: 14, Successful: 1}, Summary: "Crestron 1.0.4.22 (user)"}
	u.Slots[1] = SlotView{Name: "b", AB: avbab.Slot{Priority: 15, Successful: 1}, Summary: "Crestron 1.0.0.94 (userdebug)"}
	u.Lines = []status.Line{{Text: "Bootloader                     unknown"}}
	d.m.SetUnit(u)
	return d
}

// toUnlock: Status → Install → the firmware screen → the eject → the
// runner's read of a mainline drive → the unlock screen.
func (d *drive) toUnlock() {
	d.t.Helper()
	d.toChecklist()
	d.press(KeyOK) // eject
	d.wantActs(ActEject)
	d.m.DriveEjected(true)
	d.wantActs(ActReadDrive)
	d.m.MainlineDriveRead(mlDriveOK)
	d.want(ScreenUnlock)
	d.wantActs()
}

// unlockYes answers yes on the unlock screen as the keys of the drive do.
func (d *drive) unlockYes() {
	d.t.Helper()
	d.press(KeyOK)
	if d.m.Terminal {
		d.m.Drawn(d.now)
		d.now = d.now.Add(PanelWarnWait)
		d.press(KeyYes)
	}
}

// panelYes answers yes to the panel question.
func (d *drive) panelYes() {
	d.t.Helper()
	d.press(KeyOK)
	if d.m.Terminal {
		d.m.Drawn(d.now)
		d.now = d.now.Add(PanelWarnWait)
		d.press(KeyYes)
	}
}

// toMLPlanning: the unlock yes, the panel yes, the plan child.
func (d *drive) toMLPlanning() {
	d.t.Helper()
	d.toUnlock()
	d.unlockYes()
	d.want(ScreenPanel)
	d.panelYes()
	d.want(ScreenPlanning)
	a := d.wantActs(ActStartPlan)
	if a[0].Keeper != "" || a[0].Unclear {
		d.t.Fatalf("plan action %+v", a[0])
	}
}

func (d *drive) toMLProgress() {
	d.t.Helper()
	d.toMLPlanning()
	d.m.PlanEnded(PlanResult{Plan: mlTestPlan()})
	d.want(ScreenPlan)
	if d.m.Terminal {
		d.press(KeyOK)
		d.press(KeyYes)
	} else {
		d.hold(HoldOK)
	}
	d.want(ScreenProgress)
	d.wantActs(ActStartInstall)
}

func TestMainlineUnlockScreen(t *testing.T) {
	t.Run("panel keys: OK is yes, then the panel question, then the plan", func(t *testing.T) {
		d := newMLDrive(t, false)
		d.toUnlock()
		if d.m.UnlockConfirmed() || !d.m.Mainline() || !d.m.keeperAsked {
			t.Fatal("before the answer")
		}
		d.press(KeyYes) // y is no panel key
		d.want(ScreenUnlock)
		d.press(KeyNext)
		d.press(KeyPrev)
		d.want(ScreenUnlock)
		d.press(KeyOK)
		d.want(ScreenPanel)
		d.wantActs()
		if !d.m.UnlockConfirmed() || d.m.UnlockFromTerminal() {
			t.Fatal("the panel's yes")
		}
		d.press(KeyOK)
		d.want(ScreenPlanning)
		d.wantActs(ActStartPlan)
	})
	t.Run("the panel question answered earlier in the run: the plan at once", func(t *testing.T) {
		d := newMLDrive(t, false)
		d.m.panelOK = true
		d.toUnlock()
		d.press(KeyOK)
		d.want(ScreenPlanning)
		d.wantActs(ActStartPlan)
	})
	t.Run("No: refused, nothing written, the drive goes back", func(t *testing.T) {
		for _, terminal := range []bool{false, true} {
			d := newMLDrive(t, terminal)
			d.toUnlock()
			if terminal {
				d.press(KeyOK) // the warning; b is still No
			}
			d.press(KeyBack)
			d.want(ScreenRefused)
			d.wantActs(ActGiveBackDrive)
			if d.m.UnlockConfirmed() {
				t.Fatal("No confirmed")
			}
			f := d.m.View(d.now)
			if got := rowsOf(f); !reflect.DeepEqual(got, []string{"Why: " + txtUnlockNo, "",
				"Press OK to try again. The drive is back on the computer."}) || f.LED != "red" {
				t.Fatalf("refused: %q", got)
			}
		}
	})
	t.Run("a terminal: Enter, then y after the warning was drawn PanelWarnWait ago", func(t *testing.T) {
		d := newMLDrive(t, true)
		d.toUnlock()
		f := d.m.View(d.now)
		if hasRow(f, txtUnlockTerminalWarn) || !hasRow(f, "  Enter (or y) = Yes, b = No. Then confirm the Yes with y.") {
			t.Fatalf("before: %q", rowsOf(f))
		}
		// a burst: Enter and y at once never confirms (the warning was not drawn)
		d.press(KeyOK)
		d.press(KeyYes)
		d.want(ScreenUnlock)
		if d.m.UnlockConfirmed() {
			t.Fatal("a key burst confirmed")
		}
		f = d.m.View(d.now)
		if !hasRow(f, txtUnlockTerminalWarn) || f.Footer != "y = Yes   b = No" {
			t.Fatalf("the warning: %q %q", rowsOf(f), f.Footer)
		}
		// drawn now: a y before PanelWarnWait does not answer, Enter never does
		d.m.Drawn(d.now)
		d.now = d.now.Add(PanelWarnWait - time.Millisecond)
		d.press(KeyYes)
		d.press(KeyOK)
		d.want(ScreenUnlock)
		if d.m.UnlockConfirmed() {
			t.Fatal("a y too early")
		}
		d.now = d.now.Add(time.Millisecond)
		d.press(KeyYes)
		d.want(ScreenPanel)
		if !d.m.UnlockConfirmed() || !d.m.UnlockFromTerminal() {
			t.Fatal("the terminal's yes is not recorded")
		}
		// the panel question has its own warning and wait
		d.press(KeyYes)
		d.want(ScreenPanel)
		d.m.Drawn(d.now)
		d.now = d.now.Add(PanelWarnWait)
		d.press(KeyYes)
		d.want(ScreenPlanning)
		d.wantActs(ActStartPlan)
	})
	t.Run("a take-over terminal: its rule; after it leaves, the Mic key", func(t *testing.T) {
		d := newMLDrive(t, false)
		d.toUnlock()
		d.m.SetTerminal(true)
		d.press(KeyOK)
		if !hasRow(d.m.View(d.now), txtUnlockTerminalWarn) || d.m.UnlockConfirmed() {
			t.Fatal("no warning on the take-over")
		}
		d.m.SetTerminal(false)
		if hasRow(d.m.View(d.now), txtUnlockTerminalWarn) {
			t.Fatal("the warning stays after the take-over")
		}
		d.press(KeyOK)
		d.want(ScreenPanel)
		if d.m.UnlockFromTerminal() {
			t.Fatal("from a terminal")
		}
	})
	t.Run("the screen, exactly", func(t *testing.T) {
		d := newMLDrive(t, false)
		d.toUnlock()
		f := d.m.View(d.now)
		want := []string{
			"Mainline Linux v1.0.0-rc1: the folder uc-pxx-mainline (976 MB). It is for slot A.",
			"",
			"Confirm that the TEE unlock tool read the lock state back as UNLOCKED (lock_state = 0x01).",
			"",
			"Mainline Linux starts only on an unlocked unit. On a locked unit slot A does not start, and the unit starts slot B (Android) again.",
			"The installer cannot read the lock state itself (its status shows: Bootloader unknown).",
			"Answer No if you did not unlock this unit, or if the tool did not show UNLOCKED.",
			"",
			"  OK (Mic) = Yes, Speaker = No.",
		}
		if got := rowsOf(f); !reflect.DeepEqual(got, want) {
			t.Fatalf("unlock screen:\n%q\nwant\n%q", got, want)
		}
		if !strings.HasSuffix(f.Title, "  -  Is the unit unlocked?") || f.Keys[PlaceMic] != "Yes" || f.Keys[PlaceSpeaker] != "No" ||
			f.Keys[PlaceVolUp] != "" || f.Keys[PlaceVolDown] != "" || f.Body[2].Level != levelHeading {
			t.Fatalf("title %q keys %q", f.Title, f.Keys)
		}
		if d.m.Screen.String() != "unlock" {
			t.Fatalf("screen name %q", d.m.Screen.String())
		}
	})
}

// TestMainlineDriveSwitch: the mode follows the drive that was read last:
// a mainline read, then an official one (LeaveMainline first) is the official
// flow; a refused mainline drive stays on the firmware screen.
func TestMainlineDriveSwitch(t *testing.T) {
	d := newMLDrive(t, false)
	d.toUnlock()
	d.press(KeyBack) // No
	d.want(ScreenRefused)
	d.wantActs(ActGiveBackDrive)
	d.press(KeyOK) // try again: the firmware screen, the drive is with the host
	d.want(ScreenFirmware)
	d.m.DriveEjected(true)
	d.wantActs(ActReadDrive)
	d.m.LeaveMainline()
	d.m.DriveRead(DriveResult{OK: true, Name: `"fw.bin"`, Size: 973, Ident: "size=1,mtime=1,ctime=1,dev=1,ino=1"})
	d.want(ScreenPanel)
	if d.m.Mainline() || d.m.UnlockConfirmed() {
		t.Fatal("still the mainline mode")
	}
	// a refused mainline drive
	d = newMLDrive(t, false)
	d.toChecklist()
	d.m.DriveEjected(true)
	d.wantActs(ActReadDrive)
	why := "it holds the folder uc-pxx-mainline and a firmware file (fw.bin): leave only one of them there"
	d.m.MainlineDriveRead(MainlineDrive{Refusal: why, Remake: false})
	d.want(ScreenFirmware)
	a := d.wantActs(ActGiveBackDrive)
	if a[0].Remake || d.m.Mainline() || !hasRow(d.m.View(d.now), "The drive was refused: "+why) {
		t.Fatalf("refused drive: %+v %q", a, rowsOf(d.m.View(d.now)))
	}
	d.m.MainlineDriveRead(mlDriveOK) // not on the firmware screen any more? it is: a new read
	d.want(ScreenUnlock)
	// a read that comes when the user left the firmware screen is dropped
	d = newMLDrive(t, false)
	d.toChecklist()
	d.m.DriveEjected(true)
	d.wantActs(ActReadDrive)
	d.press(KeyBack)
	d.want(ScreenStatus)
	d.m.MainlineDriveRead(mlDriveOK)
	d.want(ScreenStatus)
	if d.m.Mainline() {
		t.Fatal("a late read set the mainline mode")
	}
}

func TestMainlinePlanningAndPlan(t *testing.T) {
	d := newMLDrive(t, false)
	d.toMLPlanning()
	d.m.ChildStarted(NewProgress(false, d.now))
	d.now = d.now.Add(65 * time.Second)
	f := d.m.View(d.now)
	got := rowsOf(f)
	want := []string{"Mainline Linux v1.0.0-rc1: the folder uc-pxx-mainline (976 MB)",
		"Unlocked: yes (your answer on the unlock screen)", "Install to slot A. Slot B is not touched.", "",
		Pulse(65*time.Second, 40) + "   1 min 05 s", "",
		">> " + txtMLHashing, "   " + txtMLUnit}
	if !reflect.DeepEqual(got, want) || !strings.HasSuffix(f.Title, "  -  Checking the mainline files") || f.Keys[PlaceSpeaker] != "Cancel" {
		t.Fatalf("planning:\n%q\nwant\n%q", got, want)
	}
	d.m.ChildLine("pass 1: check the mainline bundle (the folder uc-pxx-mainline of the P10-INSTALL drive): the manifest, the boot template and the root file system, hashed in place (nothing is written)", d.now)
	d.m.ChildLine("  manifest manifest.json; boot-template.img 31457280 bytes and rootfs.ext4 943718400 bytes match the manifest (sha256); template token ucpxx-5e1a0c2d", d.now)
	d.m.View(d.now)            // drawn: the screen keeps what it saw
	for i := 0; i < 500; i++ { // the line is out of the log's last 400 now
		d.m.ChildLine("read the unit", d.now)
	}
	if got := rowsOf(d.m.View(d.now)); got[6] != "ok "+txtMLHashing || got[7] != ">> "+txtMLUnit {
		t.Fatalf("after pass 1: %q", got)
	}
	// Back cancels the plan child (it writes nothing)
	d.press(KeyBack)
	d.wantActs(ActCancelPlan)
	d.m.PlanEnded(PlanResult{Plan: mlTestPlan()})
	d.want(ScreenPlan)
	f = d.m.View(d.now)
	want = []string{
		"Mainline Linux v1.0.0-rc1: the folder uc-pxx-mainline (976 MB)",
		"  kernel d02e92631f28, boot token ucpxx-5e1a0c2d",
		"",
		"Slot A gets mainline Linux v1.0.0-rc1; slot B keeps Android userdebug 1.0.0.94 (fallback).",
		"Slot A now: Crestron 1.0.4.22 (user). It is replaced. Slot B is not touched.",
		"",
		txtMLGate,
		txtMLMarkPlan,
		"",
		"  Hold OK (Mic) for 3 seconds to start the install.",
	}
	if got := rowsOf(f); !reflect.DeepEqual(got, want) {
		t.Fatalf("plan:\n%q\nwant\n%q", got, want)
	}
	if f.Body[3].Level != levelWarn || f.Body[6].Level != levelGood || !strings.HasSuffix(f.Title, "  -  Ready to install") ||
		f.Keys[PlaceMic] != "Hold: start" || f.ScrollKeys != "scroll" {
		t.Fatalf("plan: %q %q %+v", f.Title, f.Keys, f.Body[3])
	}
	// the 3 s hold starts the install with the plan on the screen
	d.hold(HoldOK - time.Millisecond)
	d.want(ScreenPlan)
	d.wantActs()
	d.hold(HoldOK)
	d.want(ScreenProgress)
	d.wantActs(ActStartInstall)
	if f := d.m.View(d.now); !hasRow(f, "Installing to slot A. Slot B is not touched.") {
		t.Fatalf("progress: %q", rowsOf(f))
	}
}

// TestMainlineRefusals: a refusal of the mainline planner is a refusal
// screen in mainline words (never the keeper screen: the keeper is B), with
// the device mode's text as the details.
func TestMainlineRefusals(t *testing.T) {
	keeper := "install refused: keeper: slot B must be successful {prio>=1, tries 0, succ 1}: {prio 14, tries 7, succ 0}"
	d := newMLDrive(t, false)
	d.toMLPlanning()
	d.m.PlanEnded(PlanResult{Refusal: keeper, End: End{Exited: true, Code: 1}})
	d.want(ScreenRefused)
	d.wantActs(ActGiveBackDrive)
	f := d.m.View(d.now)
	want := []string{"Nothing was written to the unit.", "Why: Slot B must be a working Android userdebug slot (the fallback). It is not.", "",
		"Press OK to try again. The drive is back on the computer.", "", "Details: " + keeper}
	if got := rowsOf(f); !reflect.DeepEqual(got, want) || f.Clip != 6 {
		t.Fatalf("refused:\n%q\nwant\n%q", got, want)
	}
	for raw, want := range map[string]string{
		"install refused: unlock: the user confirmed that the unit is unlocked (--unlock-confirmed; ...): false": "You did not confirm that the unit is unlocked.",
		"install refused: autoupdate: Auto Update Firmware ...; unlock: x":                                       "Crestron auto-update is on, or it could not be read.",
		"install refused: no-reestablish: the start must not be the both-trying reset":                           "The slot table was reset when the installer was started (both slots are not tested yet). For mainline Linux, start the installer from Android in slot B (see the install guide).",
		"install refused: keeper-boots: slot B must be the slot that boots now":                                  "Slot B must be the slot that starts now. Start the unit in Android (slot B), then start the installer from there.",
		"install refused: payload-reviewed: the bundle is not a reviewed payload of the gate embedded":           "This mainline build is not on this installer's list of reviewed builds.",
		"install refused: kernel-reviewed: x":                                                                    "The kernel of this mainline build is not a reviewed kernel.",
		"install refused: boot-control-files: x":                                                                 "The boot control files of this mainline build are not the reviewed ones.",
		"install refused: keeper-chain: slot B must hold the boot chain":                                         "Slot B does not hold the Android userdebug boot files that this mainline build needs.",
		"install refused: chain-manifest: x":                                                                     "This mainline build needs boot files that this installer does not support.",
		"install refused: vbmeta-verify: x":                                                                      "The boot check data of slot B is not the one that this mainline build needs.",
		"install refused: boot-fits: boot_a of 40000000 bytes must fit":                                          "The boot image of this mainline build does not fit this unit (it is not as its manifest says, or it is too large).",
		"install refused: mainline-model: a mainline install is only for a UC-P10":                               "Mainline Linux is only for a UC-P10. This unit is another model.",
		"install refused: mainline-manifest: x":                                                                  txtMLDamaged,
		"pass 1: rootfs.ext4: sha256 1234, the manifest's is 5678":                                               txtMLDamaged,
		"the P10-INSTALL drive holds the folder uc-pxx-mainline and a firmware file (fw.bin): leave only one":    txtMLBoth,
		"the P10-INSTALL drive has no folder uc-pxx-mainline":                                                    txtMLNoFolder,
		"the mainline bundle changed while pass 1 read it: a, then b (<nil>)":                                    txtMLChanged,
		"refused, nothing written: the plan now has id 1234, /run/p10inst/flow/plan.json has 5678":               "The unit or the mainline files changed after the check.",
		"the install ended (ended by the signal killed) and the A/B state is not known":                          "The installer stopped unexpectedly.",
		"drive rule: the P10-INSTALL drive is attached to the computer again":                                    "The drive P10-INSTALL went back to the computer, or changed, during the install.",
		"key search: nothing found":                                                                              txtMLGeneric,
		plan.NoteNotOffered:                                                                                      txtMLGeneric,
		"something new":                                                                                          txtMLGeneric,
		// the first failed check in the refusal's order wins
		"install refused: payload-reviewed: x; unlock: y": "This mainline build is not on this installer's list of reviewed builds.",
	} {
		if got := mainlinePlain(raw); got != want {
			t.Errorf("%q:\n  %q\nwant %q", raw, got, want)
		}
	}
	// the planner asks for the keeper (an official refusal text): no keeper screen in the mainline mode
	d = newMLDrive(t, false)
	d.toMLPlanning()
	d.m.PlanEnded(PlanResult{Refusal: "install refused: keeper: both slots are successful and the pre-fastboot A/B sector is not known; choose the keeper"})
	d.want(ScreenRefused)
}

func TestMainlineEnds(t *testing.T) {
	p := mlTestPlan()
	if got := StepsOf(p); len(got) != 11 || got[0].Label != "Mark slot A as not bootable" || got[2].Label != "Write the trust firmware (already up to date)" ||
		got[5].Label != "Write the kernel" || got[6].Label != "Write the system" || got[9].Label != "Make slot A start next" ||
		got[10].Label != "Check the slot table" || got[6].Bytes != 900<<20 {
		t.Fatalf("steps %+v", got)
	}
	ok0 := End{Exited: true}
	for _, c := range []struct {
		name   string
		after  []byte
		end    End
		screen Screen
		state  string
	}{
		{"exit 0, D2", mlD2, ok0, ScreenDone, StateD2},
		{"exit 0, D1", mlD1, ok0, ScreenHold, StateD1},
		{"exit 0, S0", mlS0, ok0, ScreenHold, StateS0},
		{"exit 3, D2", mlD2, End{Exited: true, Code: 3}, ScreenHold, StateD2},
		{"exit 3, S0", mlS0, End{Exited: true, Code: 3}, ScreenHold, StateS0},
		{"exit 1, D1", mlD1, End{Exited: true, Code: 1}, ScreenStopped, StateD1},
		{"exit 1, S0", mlS0, End{Exited: true, Code: 1}, ScreenStopped, StateS0},
		{"SIGKILL, D2", mlD2, End{Signal: "killed"}, ScreenStopped, StateD2},
		{"SIGKILL, the official D2", d2Sec, End{Signal: "killed"}, ScreenHold, StateUnknown},
		{"lost, the official D1", d1Sec, End{Lost: true}, ScreenHold, StateUnknown},
	} {
		o := Classify(p, mlS0, c.after, nil, c.end)
		if o.Screen != c.screen || o.State != c.state {
			t.Errorf("%s: %s %s (%s), want %s %s", c.name, o.Screen, o.State, o.Why, c.screen, c.state)
		}
		if st, _ := Recheck(p, mlS0, c.after, nil); st != c.state {
			t.Errorf("%s: recheck %s", c.name, st)
		}
	}
	t.Run("done", func(t *testing.T) {
		d := newMLDrive(t, false)
		d.toMLProgress()
		d.m.InstallEnded(Classify(p, mlS0, mlD2, nil, ok0))
		d.want(ScreenDone)
		f := d.m.View(d.now)
		want := []string{
			"Slot A is installed and starts next. If it does not start, slot B starts again after a few restarts.",
			"Slot B was not touched.",
			"",
			txtMLCable,
			txtMLMark,
			"",
			"The log folder is being written to the drive P10-INSTALL ...",
			"",
			"  Hold OK (Mic) for 3 seconds to restart.",
		}
		if got := rowsOf(f); !reflect.DeepEqual(got, want) || f.LED != "green" || !strings.HasSuffix(f.Title, "  -  Install done") ||
			f.Body[3].Level != levelWarn || f.Keys[PlaceMic] != "Hold: restart" {
			t.Fatalf("done:\n%q\nwant\n%q", got, want)
		}
		if d.m.RestartBlocked() != "" {
			t.Fatal("restart blocked after Done")
		}
		d.hold(HoldOK)
		d.wantActs(ActRestart)
	})
	t.Run("stopped", func(t *testing.T) {
		d := newMLDrive(t, false)
		d.toMLProgress()
		stop := "STOP: step 7 (write): stopped before the step: drive rule: the P10-INSTALL drive is attached to the computer again"
		d.m.ChildStarted(NewProgress(true, d.now))
		d.m.ChildLine(stop, d.now)
		d.m.InstallEnded(Classify(p, mlS0, mlD1, nil, End{Exited: true, Code: 1}))
		d.want(ScreenStopped)
		f := d.m.View(d.now)
		want := []string{
			"Slot A is not finished and will not start; slot B starts. The power can be disconnected.", "",
			"Why: The drive P10-INSTALL went back to the computer, or changed, during the install.",
			"The log folder is being written to the drive P10-INSTALL ...",
			"Press OK to try again, or Back for the status.", "", "Details: " + stop}
		if got := rowsOf(f); !reflect.DeepEqual(got, want) || f.Body[0].Level != levelWarn || f.Clip != 7 {
			t.Fatalf("stopped:\n%q\nwant\n%q", got, want)
		}
		// try again: the firmware screen; the drive read decides the mode again
		d.press(KeyOK)
		d.want(ScreenFirmware)
	})
	t.Run("hold: no repair, the keep-powered path; Check again reads the sector", func(t *testing.T) {
		d := newMLDrive(t, false)
		d.toMLProgress()
		d.m.InstallEnded(Classify(p, mlS0, mlD1, nil, End{Exited: true, Code: 3}))
		d.want(ScreenHold)
		d.wantActs() // no repair dry run
		if d.m.Running() != "" || d.m.repairHold() || d.m.RestartBlocked() == "" {
			t.Fatalf("hold: running %q", d.m.Running())
		}
		f := d.m.View(d.now)
		want := []string{
			"!!! KEEP THE UNIT POWERED. DO NOT RESTART IT.",
			"The install did not end normally. It is not known which slot would start after a restart.",
			"",
			"",
			"What to do:",
			"  1. Leave the unit on.",
			"  2. Press OK (Mic) to check the slots again.",
			"  3. The log folder is being written to the drive P10-INSTALL ...",
			"  4. " + Contact,
		}
		if got := rowsOf(f); !reflect.DeepEqual(got, want) || f.Keys[PlaceMic] != "Check again" || f.Keys[PlaceVolDown] != "" ||
			strings.Contains(bodyOf(f), "epair") {
			t.Fatalf("hold:\n%q\nwant\n%q (keys %q)", got, want, f.Keys)
		}
		d.press(KeyOK)
		d.wantActs(ActReadAB)
		d.m.Rechecked(StateD1, "the target is disabled; the keeper boots")
		if f := d.m.View(d.now); !hasRow(f, "Checked again: slot A is not finished and will not start; slot B starts. Keep the unit powered.") {
			t.Fatalf("recheck: %q", rowsOf(f))
		}
		d.press(KeyPrev) // Vol- is nothing here (no repair offer)
		d.wantActs()
		d.hold(HoldOK) // a hold of OK is "Check again", never a repair
		for _, a := range d.m.Take() {
			if a.Kind == ActRepair || a.Kind == ActRepairCheck {
				t.Fatalf("a repair action on a mainline hold: %v", a.Kind)
			}
		}
		// a repair result that comes anyway changes nothing
		d.m.ToolEnded(ToolResult{Tool: "repair", DryRun: true, End: End{Exited: true, Code: 5}})
		d.want(ScreenHold)
		if d.m.repair == repairOffer {
			t.Fatal("a repair offered on a mainline hold")
		}
	})
}

// TestMainlineResume: a UI started again knows a mainline run from its plan:
// an install that runs is followed, Done keeps the mainline text, a hold stays
// a hold with no repair check.
func TestMainlineResume(t *testing.T) {
	p := mlTestPlan()
	d := newMLDrive(t, true)
	d.toMLProgress()
	ps := d.m.Persist()
	if ps.Running != "install" || !ps.Panel || !ps.PanelTerminal {
		t.Fatalf("persisted %+v", ps)
	}
	m := New("t", false)
	m.Resume(ps, p)
	if m.Screen != ScreenProgress || m.Running() != "install" || m.Mainline() {
		t.Fatalf("resume of a running install: %s %q", m.Screen, m.Running())
	}
	m.InstallEnded(Classify(p, mlS0, mlD2, nil, End{Exited: true}))
	f := m.View(time.Now())
	if !hasRow(f, txtMLCable) || !hasRow(f, txtMLMark) {
		t.Fatalf("done after a resume: %q", rowsOf(f))
	}
	m2 := New("t", false)
	m2.Resume(m.Persist(), p)
	if m2.Screen != ScreenDone || !hasRow(m2.View(time.Now()), txtMLMark) {
		t.Fatalf("done again: %s", m2.Screen)
	}
	// a hold
	d = newMLDrive(t, false)
	d.toMLProgress()
	d.m.InstallEnded(Classify(p, mlS0, mlD1, nil, End{Exited: true, Code: 3}))
	m3 := New("t", false)
	m3.Resume(d.m.Persist(), p)
	if m3.Screen != ScreenHold || len(m3.Take()) != 0 || m3.Running() != "" || m3.RestartBlocked() == "" {
		t.Fatalf("hold after a resume: %s %q", m3.Screen, m3.Running())
	}
	// the plan screen of the planning UI with no drive read (a UI started
	// again shows no plan screen; the text stays right without the drive)
	m4 := New("t", false)
	m4.Plan, m4.Screen = p, ScreenPlan
	if !hasRow(m4.View(time.Now()), "Slot A gets mainline Linux; slot B keeps Android (fallback).") {
		t.Fatalf("plan without the drive: %q", rowsOf(m4.View(time.Now())))
	}
}

func TestMainlineArgs(t *testing.T) {
	ps := Paths{Exe: "/usr/bin/p10inst", Drive: "/run/p10inst/drive/drive.img", DriveIdent: "size=1,mtime=1,ctime=1,dev=1,ino=1",
		PlanJSON: "/run/p10inst/flow/plan.json", Log: "/run/p10inst/flow/install-9e1c9e1c9e1c.jsonl", Keys: "/run/p10inst/flow/keys.txt"}
	if got := MainlinePlanArgs(ps, true, true, true); !reflect.DeepEqual(got, []string{"/usr/bin/p10inst", "plan", "--device", "/dev/mmcblk0",
		"--fw-drive", "/run/p10inst/drive/drive.img", "--fw-drive-expect", "size=1,mtime=1,ctime=1,dev=1,ino=1", "--json", "/run/p10inst/flow/plan.json",
		"--prereqs-confirmed", "--panel-confirmed", "--unlock-confirmed"}) {
		t.Fatalf("plan args %q", got)
	}
	if got := MainlineInstallArgs(ps, true, true, true); !reflect.DeepEqual(got, []string{"/usr/bin/p10inst", "install", "--device", "/dev/mmcblk0",
		"--fw-drive", "/run/p10inst/drive/drive.img", "--fw-drive-expect", "size=1,mtime=1,ctime=1,dev=1,ino=1", "--plan", "/run/p10inst/flow/plan.json",
		"--yes", "--log", "/run/p10inst/flow/install-9e1c9e1c9e1c.jsonl", "--prereqs-confirmed", "--panel-confirmed", "--unlock-confirmed"}) {
		t.Fatalf("install args %q", got)
	}
	// no key file and no keeper ever; no unlock flag without the yes
	if got := strings.Join(MainlinePlanArgs(ps, true, true, false), " ") + " " + strings.Join(MainlineInstallArgs(ps, true, true, false), " "); strings.Contains(got, "--keys") ||
		strings.Contains(got, "--keeper") || strings.Contains(got, "--unlock") || strings.Contains(got, "unclear") {
		t.Fatalf("args %q", got)
	}
}

// TestMainlineScreensFit: every new screen with long texts fits the UC-P10
// panel and the UC-P8 console, on the panel keys and on a terminal.
func TestMainlineScreensFit(t *testing.T) {
	long := mlDriveOK
	long.Version, long.Token = "v10.20.30-rc12+build.20261109T235959", "ucpxx-0123456789abcdef"
	for _, sz := range []struct {
		panel      Panel
		cols, rows int
	}{{PanelUCP10, PanelCols, PanelRows}, {PanelUCP8, 91, 28}} {
		for _, terminal := range []bool{false, true} {
			check := func(m *Model, what string) {
				t.Helper()
				m.Panel = sz.panel
				checkFitAt(t, m, time.Unix(100000, 0), what, sz.cols, sz.rows)
			}
			mk := func(s Screen) *Model {
				m := New("UC-P10 installer  -  install image  -  p10inst", terminal)
				m.SetUnit(longUnit(AutoUpdateView{State: "off"}, unit2Lines()))
				m.ml = &mlState{drive: long, unlockOK: true}
				m.Screen = s
				return m
			}
			m := mk(ScreenUnlock)
			check(m, "unlock")
			m.panelWarn = true
			check(m, "unlock, the terminal's warning")
			m = mk(ScreenPlanning)
			m.Prog = NewProgress(false, time.Unix(100000-200, 0))
			for i := 0; i < 30; i++ {
				m.ChildLine(longChild, time.Unix(100000, 0))
			}
			check(m, "planning")
			m = mk(ScreenPlan)
			m.Plan = mlTestPlan()
			check(m, "plan")
			for _, c := range []struct {
				after []byte
				end   End
			}{{mlD2, End{Exited: true}}, {mlD1, End{Exited: true, Code: 1}}, {mlD1, End{Exited: true, Code: 3}}} {
				m = mk(ScreenProgress)
				m.Plan, m.running = mlTestPlan(), "install"
				m.Prog = NewProgress(true, time.Unix(100000-600, 0))
				m.Prog.SetPlan(StepsOf(m.Plan))
				check(m, "progress")
				m.Prog.Stop = "STOP: " + longWhy
				m.InstallEnded(Classify(m.Plan, mlS0, c.after, nil, c.end))
				m.BundleDone(longBundle)
				check(m, "end "+m.Screen.String())
			}
			m = mk(ScreenRefused)
			m.refused, m.refusedDetail = "x", "install refused: no-reestablish: "+longWhy
			check(m, "refused")
		}
	}
}

// screenText is a frame as the UI draws it (frameBytes of cmd/p10inst,
// without the colours): the title, a rule, the body in its room, a rule, the
// message rows, the key legend.
func screenText(f Frame, w, h int) string {
	lay := f.Layout(w, h)
	rows := []string{lay.Title, strings.Repeat("-", w)}
	for _, b := range lay.Body {
		rows = append(rows, b.Text)
	}
	for len(rows) < 2+lay.Room {
		rows = append(rows, "")
	}
	rule := strings.Repeat("-", w)
	if lay.Rule != "" && Cells(lay.Rule)+8 < w {
		rule = strings.Repeat("-", w-8-Cells(lay.Rule)) + lay.Rule + strings.Repeat("-", 8)
	}
	rows = append(rows, rule)
	rows = append(rows, lay.Msg...)
	rows = append(rows, lay.Footer)
	for i := range rows {
		rows[i] = strings.TrimRight(rows[i], " ")
	}
	return strings.Join(rows, "\n") + "\n"
}

// TestMainlineRenders draws every new screen at 120 x 37 (the UC-P10 panel),
// on the panel keys; with P10INST_RENDER_DIR set it writes each into that
// folder (the renders of the review).
func TestMainlineRenders(t *testing.T) {
	now := time.Unix(100000, 0)
	mk := func(terminal bool) *drive {
		d := newMLDrive(t, terminal)
		d.m.Title = "UC-P10 installer  -  install image  -  p10inst-xxxxxx"
		d.now = now
		return d
	}
	frames := map[string]Frame{}
	d := mk(false)
	d.toUnlock()
	frames["unlock"] = d.m.View(d.now)
	dt := mk(true)
	dt.toUnlock()
	dt.press(KeyOK)
	frames["unlock-terminal-warning"] = dt.m.View(dt.now)
	d.unlockYes()
	d.panelYes()
	d.m.ChildStarted(NewProgress(false, d.now.Add(-95*time.Second)))
	frames["planning"] = d.m.View(d.now)
	d.m.ChildLine("  manifest manifest.json; boot-template.img 31457280 bytes and rootfs.ext4 943718400 bytes match the manifest (sha256); template token ucpxx-5e1a0c2d", d.now)
	frames["planning-unit"] = d.m.View(d.now)
	d.m.PlanEnded(PlanResult{Plan: mlTestPlan()})
	frames["plan"] = d.m.View(d.now)
	d.hold(HoldOK)
	d.m.InstallEnded(Classify(mlTestPlan(), mlS0, mlD2, nil, End{Exited: true}))
	d.m.BundleDone("the drive P10-INSTALL, folder p10-install-9e1c9e1c9e1c-20261109T120000Z")
	frames["done"] = d.m.View(d.now)
	h := mk(false)
	h.toMLProgress()
	h.m.InstallEnded(Classify(mlTestPlan(), mlS0, mlD1, nil, End{Exited: true, Code: 3}))
	h.m.BundleDone("the drive P10-INSTALL, folder p10-install-9e1c9e1c9e1c-20261109T120000Z")
	frames["hold"] = h.m.View(h.now)
	s := mk(false)
	s.toMLProgress()
	s.m.ChildStarted(NewProgress(true, s.now))
	s.m.ChildLine("STOP: step 7 (write): stopped before the step: drive rule: the P10-INSTALL drive is attached to the computer again", s.now)
	s.m.InstallEnded(Classify(mlTestPlan(), mlS0, mlD1, nil, End{Exited: true, Code: 1}))
	s.m.BundleDone("the drive P10-INSTALL, folder p10-install-9e1c9e1c9e1c-20261109T120000Z")
	frames["stopped"] = s.m.View(s.now)
	r := mk(false)
	r.toMLPlanning()
	r.m.PlanEnded(PlanResult{Refusal: "install refused: unlock: the user confirmed that the unit is unlocked (--unlock-confirmed; the unlock tool's read-back, or ro.boot.vbmeta.device_state=unlocked in Android): false"})
	frames["refused"] = r.m.View(r.now)
	n := mk(false)
	n.toUnlock()
	n.press(KeyBack)
	frames["refused-unlock-no"] = n.m.View(n.now)
	out := os.Getenv("P10INST_RENDER_DIR")
	for name, f := range frames {
		txt := screenText(f, PanelCols, PanelRows)
		if lay := f.Layout(PanelCols, PanelRows); lay.Problem(ScreenStatus) != "" {
			t.Fatalf("%s: %s", name, lay.Problem(ScreenStatus))
		}
		if out != "" {
			if err := os.WriteFile(filepath.Join(out, "mainline-"+name+".txt"), []byte(txt), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}
