//go:build install && devtest

package main

// The on-screen flow of a mainline install, end to end (phase 5): the real
// devtest p10inst runs `p10inst flow --stdin`; its children are the real
// mainline device mode (the dispatcher of mainline_install.go chooses it from
// the drive) on a fake unit whose slot B holds the fixture's boot chain, with
// the real uc-pxx-bootctl-inst r2; the payload gate is the devtest gate of
// --test-review-bundle (the release's gate lists no payload yet).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/drivestate"
	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/fatfs/fattest"
	"uc-pxx-installer/internal/flowio"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/plan"
)

// newMLFUnit is newFUnit for a first mainline install: B (the fixture's
// boot chain, successful) boots, A is the target; the drive holds the
// bundle in uc-pxx-mainline/ and the key file (a mainline install takes
// none). It returns the folder that --test-review-bundle names.
func newMLFUnit(t *testing.T) (*funit, string) {
	f := newFUnit(t, fakeemmc.Unit{AB: [2][3]byte{{14, 0, 1}, {15, 0, 1}}, A: "old", B: "new"})
	b, _ := mlBundleFor(t)
	im := fattest.New(fattest.Spec{Type: 16, Sectors: 2*(len(b.Rootfs)+len(b.Template))/512 + 40000, SecPerClus: 4, Label: "P10-INSTALL"})
	d := im.Root.Mkdir(mainline.Dir)
	d.File(mainline.ManifestFile, b.ManifestJSON())
	d.File(mainline.TemplateFile, b.Template)
	d.File(mainline.RootfsFile, b.Rootfs)
	im.Root.File(flowio.KeyFileName, []byte("head_key=00\niv=00\n"))
	if err := os.WriteFile(f.root.Path(drivestate.Image), im.B, 0o644); err != nil {
		t.Fatal(err)
	}
	host, err := b.Write(filepath.Join(f.dir, "host"))
	if err != nil {
		t.Fatal(err)
	}
	return f, host
}

// toMLPlan: Install, the host ejects, the unlock question and the panel
// question answered from the terminal, the plan child.
func (u *uiProc) toMLPlan() {
	f := u.f
	f.wait("status: A/B valid true", 60*time.Second)
	u.keys("j\r")
	f.wait("screen firmware", 10*time.Second)
	f.eject()
	f.wait("drive: mainline bundle uc-pxx-mainline", 30*time.Second)
	f.wait("screen unlock", 10*time.Second)
	u.panelYes() // Enter, the wait after the warning, y
	f.wait("screen panel", 10*time.Second)
	u.panelYes()
	f.wait("plan child ended", 300*time.Second)
	if !strings.Contains(f.flowLog(), "screen plan\n") {
		f.t.Fatalf("no plan screen:\n%s", f.flowLog())
	}
}

func (f *funit) mlPlan() *plan.MainlinePlan {
	b, err := os.ReadFile(f.plan)
	if err != nil {
		f.t.Fatal(err)
	}
	var p plan.MainlinePlan
	if err := json.Unmarshal(b, &p); err != nil || p.Kind != plan.MainlineKind || !p.VerifyID() {
		f.t.Fatalf("the mainline plan: %v", err)
	}
	return &p
}

func (f *funit) checkMLDone() {
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
	for _, want := range []string{"--unlock-confirmed", "unlock: \"Is the unit unlocked?\": yes, from a terminal, after its warning",
		"after the install: readonly", "drive detached and unchanged to the end"} {
		if !strings.Contains(log, want) {
			t.Fatalf("no %q in the flow log:\n%s", want, log)
		}
	}
	for _, l := range strings.Split(log, "\n") {
		if strings.Contains(l, "child started:") && (strings.Contains(l, "--keys") || strings.Contains(l, "--keeper")) {
			t.Fatalf("a mainline child with a key or a keeper: %s", l)
		}
		if strings.Contains(l, "repair child started") {
			t.Fatalf("a repair child: %s", l)
		}
	}
	name := f.checkBundle("done")
	res, _ := os.ReadFile(f.root.Path(flowio.RunDir + "/" + name + "/result.txt"))
	if !strings.Contains(string(res), "mainline: uc-pxx-mainline v0.0.1-test, token mltest-cli") ||
		!strings.Contains(string(res), "yes, from a terminal, after its warning") {
		t.Fatalf("result.txt:\n%s", res)
	}
}

// TestFlowMainlineInstall: the whole mainline flow on a terminal: the
// unlock and panel questions, the plan, Enter y, the install by the real
// device mode, Done (exit 0 and A armed, B the successful fallback), the
// bundle on the drive.
func TestFlowMainlineInstall(t *testing.T) {
	f, host := newMLFUnit(t)
	u := f.startUI("--test-child-args", "--test-review-bundle "+host)
	u.toMLPlan()
	time.Sleep(500 * time.Millisecond)
	u.keys("\ry")
	f.wait("install child started", 20*time.Second)
	f.wait("bundle p10-install-", 600*time.Second)
	f.checkMLDone()
	u.quit()
}

// TestFlowMainlineUICrash: the UI dies during the mainline install; a new UI
// follows it (the mainline plan file by its own id check) and ends on Done.
func TestFlowMainlineUICrash(t *testing.T) {
	f, host := newMLFUnit(t)
	u := f.startUI("--test-child-args", "--test-review-bundle "+host+" --test-pause-after 3")
	u.toMLPlan()
	time.Sleep(500 * time.Millisecond)
	u.keys("\ry")
	f.wait("install child started", 20*time.Second)
	u.cmd.Process.Signal(syscall.SIGKILL)
	u.cmd.Wait()
	if strings.Contains(f.flowLog(), "install child ended") {
		t.Fatal("the install ended before the UI was killed: the test proves nothing")
	}
	u2 := f.startUI("--test-child-args", "--test-review-bundle "+host)
	f.wait("UI started again: an install runs", 30*time.Second)
	f.wait("bundle p10-install-", 600*time.Second)
	f.checkMLDone()
	u2.quit()
}

// TestFlowMainlineRefused: the release's payload gate (no
// --test-review-bundle) refuses the bundle: the refused screen, nothing
// written, the drive back with the host.
func TestFlowMainlineRefused(t *testing.T) {
	f, _ := newMLFUnit(t)
	before := f.image()
	u := f.startUI()
	u.toMLPlanRefused()
	if s := string(f.image()); s != string(before) {
		t.Fatal("the unit changed")
	}
	if !strings.Contains(f.flowLog(), "payload-reviewed") {
		t.Fatalf("flow log:\n%s", f.flowLog())
	}
	u.quit()
}

func (u *uiProc) toMLPlanRefused() {
	f := u.f
	f.wait("status: A/B valid true", 60*time.Second)
	u.keys("j\r")
	f.wait("screen firmware", 10*time.Second)
	f.eject()
	f.wait("screen unlock", 30*time.Second)
	u.panelYes()
	f.wait("screen panel", 10*time.Second)
	u.panelYes()
	f.wait("plan child ended", 300*time.Second)
	f.wait("screen refused", 10*time.Second)
	f.wait("drive given back to the computer", 10*time.Second)
}
