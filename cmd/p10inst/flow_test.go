//go:build install && devtest

package main

// The whole on-screen flow, end to end, on a fake unit (package devicetest):
// the real p10inst devtest binary runs `p10inst flow --stdin` with keys from
// a pipe; the flow starts the real device mode as its child under the real
// supervisor; uc-pxx-bootctl-inst is the real binary (UC_PXX_BOOTCTL, under
// UC_PXX_BOOTCTL_EXEC=qemu-aarch64-static on a PC) with --file on a copy of
// the A/B sector. No real disk is opened.

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/device/devicetest"
	"uc-pxx-installer/internal/drivestate"
	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/fatfs"
	"uc-pxx-installer/internal/fatfs/fattest"
	"uc-pxx-installer/internal/flowio"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/plan"
)

var testBin string // the devtest p10inst, built once

func TestMain(m *testing.M) {
	d, err := os.MkdirTemp("", "p10inst-flow-bin-")
	if err != nil {
		panic(err)
	}
	testBin = filepath.Join(d, "p10inst")
	out, err := exec.Command("go", "build", "-tags", "install,devtest", "-o", testBin, ".").CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "go build: %v\n%s", err, out)
		os.Exit(2)
	}
	rc := m.Run()
	os.RemoveAll(d)
	os.Exit(rc)
}

const (
	testSerial = "TESTSERIAL4711"
	testMAC    = "02:11:22:33:44:55"
)

var (
	fixture = fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	small   = fakeemmc.ParseSmallLayout(fakeemmc.SmallLayout)
)

func bootctlBin(t *testing.T) []byte {
	p := os.Getenv("UC_PXX_BOOTCTL")
	if p == "" {
		t.Skip("UC_PXX_BOOTCTL not set: the flow tests run the real uc-pxx-bootctl-inst")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if devicetest.SHA256(b) != device.BootctlSHA256 {
		t.Fatalf("%s is not the reviewed binary", p)
	}
	return b
}

// vendorStore is one copy of the Rockchip vendor storage with the serial.
func vendorStore(serial string) []byte {
	b := make([]byte, 128*512)
	le := binary.LittleEndian
	le.PutUint32(b[0:], 0x524B5644)
	le.PutUint32(b[4:], 3)
	le.PutUint16(b[10:], 1)
	le.PutUint16(b[16:], 1)  // id 1: the serial
	le.PutUint16(b[18:], 0)  // offset
	le.PutUint16(b[20:], 64) // size
	copy(b[1024:], serial)
	le.PutUint32(b[len(b)-4:], 3)
	return b
}

type funit struct {
	t     *testing.T
	dir   string
	root  *devicetest.Root
	start []byte
	plan  string // where the flow writes the plan
}

// newFUnit makes a fake unit with a serial number, a MAC, the gadget lun
// (the host has the drive) and the drive with the made-up firmware.
func newFUnit(t *testing.T, u fakeemmc.Unit) *funit {
	bin := bootctlBin(t)
	dir := t.TempDir()
	src, err := fixture.Image(dir, "src.img", u)
	if err != nil {
		t.Fatal(err)
	}
	r, err := devicetest.New(filepath.Join(dir, "root"), src, small, bin, devicetest.Opts{})
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(src)
	f := &funit{t: t, dir: dir, root: r}
	// the serial number in the vendor storage (sector 7168, in the disk node) and the MAC in cfg
	disk, _ := os.OpenFile(r.Path("/dev/mmcblk0"), os.O_RDWR, 0)
	disk.WriteAt(vendorStore(testSerial), 7168*512)
	disk.Close()
	cfg, _ := os.OpenFile(r.Node("cfg"), os.O_RDWR, 0)
	cfg.WriteAt([]byte{0x02, 0x11, 0x22, 0x33, 0x44, 0x55}, 6)
	cfg.Close()
	// the drive: FAT16 with the firmware, attached to the host
	im := fattest.New(fattest.Spec{Type: 16, Sectors: 2*len(fixture.FW)/512 + 40000, SecPerClus: 4, Label: "P10-INSTALL"})
	im.Root.File("UC-P10 1.0.0.94.bin", fixture.FW)
	// the fake unit's system has no updater library: the key file of §1.2
	im.Root.File(flowio.KeyFileName, []byte(fmt.Sprintf("head_key=%x\niv=%x\n", fixture.Keys.Head[:], fixture.Keys.IV[:])))
	img := r.Path(drivestate.Image)
	os.MkdirAll(filepath.Dir(img), 0o755)
	if err := os.WriteFile(img, im.B, 0o644); err != nil {
		t.Fatal(err)
	}
	lun := r.Path(drivestate.LunFile)
	os.MkdirAll(filepath.Dir(lun), 0o755)
	os.WriteFile(lun, []byte(img+"\n"), 0o644)
	f.start, _ = r.ABSector()
	f.plan = r.Path(flowio.FlowDir + "/plan.json")
	return f
}

// ui is one `p10inst flow --stdin` process.
type uiProc struct {
	t   *testing.T
	f   *funit
	cmd *exec.Cmd
	in  io.WriteCloser
	out bytes.Buffer
}

func (f *funit) startUI(extra ...string) *uiProc {
	args := append([]string{"flow", "--stdin", "--test-root", f.root.Dir, "--test-fake-unit", "--no-reboot"}, extra...)
	cmd := exec.Command(testBin, args...)
	cmd.Env = append(os.Environ(), "TMPDIR="+f.dir)
	u := &uiProc{t: f.t, f: f, cmd: cmd}
	in, _ := cmd.StdinPipe()
	u.in = in
	cmd.Stdout, cmd.Stderr = &u.out, &u.out
	if err := cmd.Start(); err != nil {
		f.t.Fatal(err)
	}
	return u
}

func (u *uiProc) keys(s string) {
	for _, c := range []byte(s) {
		u.in.Write([]byte{c})
		time.Sleep(60 * time.Millisecond)
	}
}

func (f *funit) flowLog() string {
	b, _ := os.ReadFile(f.root.Path(flowio.FlowDir + "/flow.log"))
	return string(b)
}

// wait waits for a line of the flow log.
func (f *funit) wait(what string, d time.Duration) {
	f.t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if strings.Contains(f.flowLog(), what) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	f.t.Fatalf("no %q in the flow log after %s:\n%s", what, d, f.flowLog())
}

func (f *funit) eject() { os.WriteFile(f.root.Path(drivestate.LunFile), []byte("\n"), 0o644) }

func (f *funit) state() map[string]any {
	b, _ := os.ReadFile(f.root.Path(flowio.FlowDir + "/state.json"))
	var m map[string]any
	json.Unmarshal(b, &m)
	return m
}

func (f *funit) endScreen() string {
	fl, _ := f.state()["flow"].(map[string]any)
	s, _ := fl["screen"].(string)
	return s
}

func (u *uiProc) quit() {
	u.t.Helper()
	// every screen that this UI drew fitted the panel (drawFlow: the flow
	// log says "layout: ..." for a screen whose rows do not fit)
	if l := u.f.flowLog(); strings.Contains(l, "layout: ") {
		u.t.Fatalf("a screen did not fit:\n%s", l)
	}
	u.keys("q")
	done := make(chan error, 1)
	go func() { done <- u.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		u.cmd.Process.Kill()
		u.t.Fatalf("the UI did not quit:\n%s", u.f.flowLog())
	}
}

// toPlan drives the UI from the status to the plan screen (with the keeper
// screen: both slots of the fake unit are successful).
func (u *uiProc) toPlan(keeperKeys string) {
	f := u.f
	f.wait("status: A/B valid true", 60*time.Second)
	u.keys("j\r") // Install
	// the checklist: auto-update not clear on the small layout (confirm),
	// URL, XiO, (info), power, Continue
	u.keys("\rj\rj\rjj\rj\r")
	f.wait("screen firmware", 10*time.Second)
	f.eject() // the host ejects the drive
	f.wait("drive: firmware", 30*time.Second)
	f.wait("plan child ended", 300*time.Second)
	if strings.Contains(f.flowLog(), "choose the keeper") {
		u.keys(keeperKeys + "\r")
		f.wait("plan child started", 10*time.Second)
		f.waitCount("plan child ended", 1, 300*time.Second)
	}
}

// checkBundle checks the bundle on the drive: the allowlist, the files, no
// serial number, no MAC, no raw CID, no key; and the run copy.
func (f *funit) checkBundle(wantScreen string) string {
	t := f.t
	t.Helper()
	fs, err := fatfs.OpenImage(f.root.Path(drivestate.Image))
	if err != nil {
		t.Fatalf("the drive after the flow: %v", err)
	}
	defer fs.Close()
	if d := fs.Dirty(); len(d) > 0 {
		t.Fatalf("dirty drive: %v", d)
	}
	if p := fs.Check(); len(p) > 0 {
		t.Fatalf("drive: %v", p)
	}
	top, _ := fs.ReadDir("/")
	if len(top) != 1 || !top[0].Dir || !strings.HasPrefix(top[0].Name, "p10-install-") {
		t.Fatalf("top folder of the drive: %+v", top)
	}
	name := top[0].Name
	if strings.Contains(name, testSerial) {
		t.Fatal("the serial is in the folder name")
	}
	es, _ := fs.ReadDir("/" + name)
	id := strings.Split(strings.TrimPrefix(name, "p10-install-"), "-")[0]
	var all []byte
	have := map[string]bool{}
	for _, e := range es {
		if !flowio.Allowed(e.Name, id) {
			t.Fatalf("%s is not in the allowlist", e.Name)
		}
		b, err := fs.ReadFile("/"+name+"/"+e.Name, 64<<20)
		if err != nil {
			t.Fatal(err)
		}
		have[e.Name] = true
		all = append(all, b...)
		// the run copy is the same
		rc, err := os.ReadFile(f.root.Path(flowio.RunDir + "/" + name + "/" + e.Name))
		if err != nil || !bytes.Equal(rc, b) {
			t.Fatalf("%s: the copy in /run differs (%v)", e.Name, err)
		}
	}
	for _, need := range []string{"README.txt", "result.txt", "plan.json", "install-" + id + ".jsonl", "install-" + id + ".out",
		"child-plan.txt", "child-install.txt", "ab-sector.txt", "ab-steps.txt", "status-before.txt", "status-after.txt",
		"nvram.txt", "emmc.txt", "binaries.txt", "cmdline.txt", "dmesg.txt", "flow.log"} {
		if !have[need] {
			t.Errorf("the bundle has no %s", need)
		}
	}
	low := bytes.ToLower(all)
	cid, _ := os.ReadFile(f.root.Path("/sys/class/block/mmcblk0/device/cid"))
	for what, s := range map[string]string{"serial": testSerial, "MAC": testMAC, "MAC hex": strings.ReplaceAll(testMAC, ":", ""),
		"CID": strings.TrimSpace(string(cid)), "key": hex.EncodeToString(fixture.Keys.Head[:]), "IV": hex.EncodeToString(fixture.Keys.IV[:])} {
		if bytes.Contains(low, bytes.ToLower([]byte(s))) {
			t.Errorf("the bundle holds the %s %q", what, s)
		}
	}
	if bytes.Contains(all, fixture.Keys.Head[:]) {
		t.Error("the bundle holds the raw key")
	}
	// no firmware data: no 1 KiB block of the firmware file
	for off := 0; off+1024 <= len(fixture.FW); off += 64 << 10 {
		if bytes.Contains(all, fixture.FW[off:off+1024]) {
			t.Fatalf("the bundle holds firmware data (offset %d)", off)
		}
	}
	if !strings.Contains(string(all), "screen: "+wantScreen) {
		t.Errorf("result.txt does not say screen %s", wantScreen)
	}
	// the real key check of the repository on the run copy
	if py, err := exec.LookPath("python3"); err == nil {
		out, err := exec.Command(py, "-I", "../../ci/check-no-key.py", f.root.Path(flowio.RunDir+"/"+name)).CombinedOutput()
		if err != nil {
			t.Errorf("check-no-key.py on the bundle: %v\n%s", err, out)
		}
	}
	// the drive is with the host again
	if v, _ := os.ReadFile(f.root.Path(drivestate.LunFile)); strings.TrimSpace(string(v)) != f.root.Path(drivestate.Image) {
		t.Errorf("the lun after the end: %q", v)
	}
	return name
}

func (f *funit) image() []byte {
	b, err := f.root.Image()
	if err != nil {
		f.t.Fatal(err)
	}
	return b
}

func readPlanFile(t *testing.T, p string) *plan.Plan {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var pl plan.Plan
	if err := json.Unmarshal(b, &pl); err != nil || !pl.VerifyID() {
		t.Fatalf("plan: %v", err)
	}
	return &pl
}

func (f *funit) cutState(p *plan.Plan) install.CutState {
	cs, err := install.CheckCutState(bytes.NewReader(f.image()), p, f.start)
	if err != nil {
		f.t.Fatalf("cut state: %v (%s)", err, cs.AB)
	}
	return cs
}

var unitBoth = fakeemmc.Unit{AB: [2][3]byte{{15, 0, 1}, {14, 0, 1}}, A: "old", B: "old"}

// TestFlowInstall: the whole flow with the touch-key logic on a terminal:
// checklist, firmware eject by the host, the keeper screen (both slots are
// successful), the plan, Enter and y, the install, Done, the bundle on the
// drive, and the restart.
func TestFlowInstall(t *testing.T) {
	f := newFUnit(t, unitBoth)
	u := f.startUI()
	u.toPlan("") // keep A (it boots next and is selected)
	f.wait("action start-plan", time.Second)
	time.Sleep(500 * time.Millisecond)
	u.keys("\ry") // start the install
	f.wait("install child started", 20*time.Second)
	f.wait("bundle p10-install-", 600*time.Second)
	if s := f.endScreen(); s != "done" {
		t.Fatalf("end screen %q\n%s", s, f.flowLog())
	}
	p := readPlanFile(t, f.plan)
	if p.Keeper != "a" || p.Target != "b" {
		t.Fatalf("keeper %s target %s", p.Keeper, p.Target)
	}
	cs := f.cutState(p)
	if cs.Boots != "target" || !cs.Checked {
		t.Fatalf("%+v", cs)
	}
	// only the target's ranges and the A/B sector changed: the device-mode
	// tests check the bytes; here the keeper and the install's own lines
	if !strings.Contains(f.flowLog(), "after the install: readonly") || !strings.Contains(f.flowLog(), "drive detached and unchanged to the end") {
		t.Fatalf("flow log:\n%s", f.flowLog())
	}
	name := f.checkBundle("done")
	t.Logf("bundle %s", name)
	// restart from the Done screen: Enter, then y
	u.keys("\ry")
	done := make(chan error, 1)
	go func() { done <- u.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		u.cmd.Process.Kill()
		t.Fatalf("no restart:\n%s", f.flowLog())
	}
	if !strings.Contains(f.flowLog(), "action restart") {
		t.Fatalf("flow log:\n%s", f.flowLog())
	}
}

// TestFlowUICrash: the UI dies (SIGKILL) during the install. The install
// goes on under its supervisor; a new UI finds it, follows it, and ends on
// Done with the bundle.
func TestFlowUICrash(t *testing.T) {
	f := newFUnit(t, unitBoth)
	u := f.startUI("--test-child-args", "--test-pause-after 3")
	u.toPlan("")
	time.Sleep(500 * time.Millisecond)
	u.keys("\ry")
	f.wait("install child started", 20*time.Second)
	u.cmd.Process.Signal(syscall.SIGKILL)
	u.cmd.Wait()
	if strings.Contains(f.flowLog(), "install child ended") {
		t.Fatal("the install ended before the UI was killed: the test proves nothing")
	}
	u2 := f.startUI()
	f.wait("UI started again: an install runs", 30*time.Second)
	f.wait("bundle p10-install-", 600*time.Second)
	if s := f.endScreen(); s != "done" {
		t.Fatalf("end screen %q\n%s", s, f.flowLog())
	}
	f.checkBundle("done")
	u2.quit()
}

// TestFlowSecondUI: one flow UI at a time (owner, 2026-10-30). A second UI
// on a terminal (--stdin) is refused with exit 4 and starts nothing. A UI on
// a screen (--tty, as init starts it on tty1) waits, says who runs, and
// takes over when that UI ends. After a crash (SIGKILL) the lock is not
// stale: a new UI starts at once.
func TestFlowSecondUI(t *testing.T) {
	f := newFUnit(t, unitBoth)
	u1 := f.startUI()
	f.wait("status: A/B valid true", 60*time.Second)
	u2 := f.startUI()
	done := make(chan error, 1)
	go func() { done <- u2.cmd.Wait() }()
	select {
	case err := <-done:
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != exitUIBusy {
			t.Fatalf("second UI: %v\n%s", err, u2.out.String())
		}
	case <-time.After(20 * time.Second):
		u2.cmd.Process.Kill()
		t.Fatalf("the second UI did not refuse:\n%s", u2.out.String())
	}
	if want := fmt.Sprintf("another flow UI runs (pid %d on ", u1.cmd.Process.Pid); !strings.Contains(u2.out.String(), want) {
		t.Fatalf("second UI said %q, want %q", u2.out.String(), want)
	}
	if strings.Contains(f.flowLog(), "UI started again") {
		t.Fatalf("the refused UI started the flow:\n%s", f.flowLog())
	}
	// the screen UI: waits while u1 runs
	tty := filepath.Join(f.dir, "tty1")
	os.WriteFile(tty, nil, 0o644)
	out3, _ := os.Create(filepath.Join(f.dir, "u3.out"))
	defer out3.Close()
	said := func() string { b, _ := os.ReadFile(out3.Name()); return string(b) }
	u3 := exec.Command(testBin, "flow", "--tty", tty, "--test-root", f.root.Dir, "--test-fake-unit", "--no-reboot")
	u3.Env = append(os.Environ(), "TMPDIR="+f.dir)
	u3.Stdout, u3.Stderr = out3, out3
	if err := u3.Start(); err != nil {
		t.Fatal(err)
	}
	defer u3.Process.Kill()
	end := time.Now().Add(20 * time.Second)
	for !strings.Contains(said(), "waiting for it to end") && time.Now().Before(end) {
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(1500 * time.Millisecond)
	if b, _ := os.ReadFile(tty); !strings.Contains(said(), "waiting for it to end") ||
		!strings.Contains(string(b), "Shown from the installer on") || !strings.Contains(string(b), "[ STATUS ]") {
		t.Fatalf("the screen UI does not wait: %q, screen %q", said(), b)
	}
	if strings.Contains(f.flowLog(), "UI started again") {
		t.Fatalf("the waiting UI started the flow:\n%s", f.flowLog())
	}
	u1.quit()
	f.wait("UI started again", 30*time.Second)
	// the screen UI crashes: no stale lock, a new UI starts at once
	u3.Process.Signal(syscall.SIGKILL)
	u3.Wait()
	os.Remove(f.root.Path(flowio.FlowDir + "/flow.log"))
	u4 := f.startUI()
	f.wait("UI started again", 30*time.Second)
	if strings.Contains(u4.out.String(), "another flow UI runs") {
		t.Fatalf("u4: %s", u4.out.String())
	}
	u4.quit()
}

// screenUI is `p10inst flow --tty FILE` (the panel's UI, as init starts it
// on tty1): it draws into a file; its stderr goes to FILE.err.
type screenUI struct {
	cmd *exec.Cmd
	tty string
}

func (f *funit) startScreenUI(name string, extra ...string) *screenUI {
	tty := filepath.Join(f.dir, name)
	os.WriteFile(tty, nil, 0o644)
	o, _ := os.Create(tty + ".err")
	args := append([]string{"flow", "--tty", tty, "--test-root", f.root.Dir, "--test-fake-unit", "--no-reboot"}, extra...)
	c := exec.Command(testBin, args...)
	c.Env = append(os.Environ(), "TMPDIR="+f.dir)
	c.Stdout, c.Stderr = o, o
	if err := c.Start(); err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { c.Process.Kill(); c.Wait(); o.Close() })
	return &screenUI{cmd: c, tty: tty}
}

// screen is what the screen UI drew since the last call (the file grows by
// one frame per draw).
func (s *screenUI) screen(from *int) string {
	b, _ := os.ReadFile(s.tty)
	if *from > len(b) {
		*from = 0
	}
	out := string(b[*from:])
	*from = len(b)
	return out
}

func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for !cond() {
		if time.Now().After(end) {
			t.Fatalf("timeout: %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestFlowTakeOver: the panel's UI runs the flow (it has the lock); a person
// at the serial shell drives it with `p10inst flow --stdin --take-over`
// (review M-2): its keys go to the panel's UI, which runs the install and
// handles its end; the panel shows the state all the time and says that
// the keys come from the terminal. A second take-over terminal is refused;
// after q the panel has its keys again, and a new take-over terminal can
// follow the install to Done.
func TestFlowTakeOver(t *testing.T) {
	f := newFUnit(t, unitBoth)
	panel := f.startScreenUI("tty1", "--test-child-args", "--test-pause-after 3")
	f.wait("status: A/B valid true", 60*time.Second)
	// --stdin alone is refused and names --take-over
	u0 := f.startUI()
	if err := u0.cmd.Wait(); err == nil || !strings.Contains(u0.out.String(), "--stdin --take-over") {
		t.Fatalf("--stdin: %v %s", err, u0.out.String())
	}
	u := f.startUI("--take-over")
	f.wait("has the keys (take-over)", 20*time.Second)
	var at int
	waitFor(t, "the panel says where the keys are", 10*time.Second, func() bool {
		return strings.Contains(panel.screen(&at), "Keys: from the terminal")
	})
	u2 := f.startUI("--take-over")
	if err := u2.cmd.Wait(); err == nil || !strings.Contains(u2.out.String(), "another terminal has the keys") {
		t.Fatalf("a second take-over: %v %s", err, u2.out.String())
	}
	u.toPlan("")
	time.Sleep(500 * time.Millisecond)
	u.keys("\ry")
	f.wait("install child started", 20*time.Second)
	waitFor(t, "the panel shows the live safety line", 20*time.Second, func() bool {
		s := panel.screen(&at)
		return strings.Contains(s, "Installing") && strings.Contains(s, "disconnect the power")
	})
	waitFor(t, "the take-over terminal shows the install", 10*time.Second, func() bool {
		return strings.Contains(u.out.String(), "Installing")
	})
	// the person leaves during the install: the install goes on
	u.quit()
	f.wait("the take-over terminal left", 20*time.Second)
	u3 := f.startUI("--take-over")
	f.waitCount("has the keys (take-over)", 1, 20*time.Second)
	f.wait("bundle p10-install-", 600*time.Second)
	if s := f.endScreen(); s != "done" {
		t.Fatalf("end screen %q\n%s", s, f.flowLog())
	}
	waitFor(t, "the panel shows Done", 10*time.Second, func() bool { return strings.Contains(panel.screen(&at), "Install done") })
	waitFor(t, "the terminal shows Done", 10*time.Second, func() bool { return strings.Contains(u3.out.String(), "Install done") })
	u3.quit()
	if strings.Contains(f.flowLog(), "layout: ") {
		t.Fatalf("a layout fault:\n%s", f.flowLog())
	}
}

// TestFlowStalledTerminal: the flow UI on a terminal whose output is not
// read any more (a serial program closed, the cable still in; review M-1).
// Its screens are dropped, not waited for: the flow handles the end of the
// install (readonly, Classify, the bundle) by itself. A screen UI that
// waits shows the flow UI's state; when the flow UI stops drawing (SIGSTOP)
// it says so in red.
func TestFlowStalledTerminal(t *testing.T) {
	f := newFUnit(t, unitBoth)
	cmd := exec.Command(testBin, "flow", "--stdin", "--test-root", f.root.Dir, "--test-fake-unit", "--no-reboot", "--test-child-args", "--test-pause-after 3")
	cmd.Env = append(os.Environ(), "TMPDIR="+f.dir)
	u := &uiProc{t: t, f: f, cmd: cmd}
	u.in, _ = cmd.StdinPipe()
	pr, pw, _ := os.Pipe()
	cmd.Stdout, cmd.Stderr = pw, &u.out
	var drain atomic.Bool
	drain.Store(true)
	go func() {
		buf := make([]byte, 4096)
		for {
			if !drain.Load() {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			if _, err := pr.Read(buf); err != nil {
				return
			}
		}
	}()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pw.Close()
	defer cmd.Process.Kill()
	u.toPlan("")
	time.Sleep(500 * time.Millisecond)
	u.keys("\ry")
	f.wait("install child started", 20*time.Second)
	panel := f.startScreenUI("tty1")
	drain.Store(false) // nobody reads the terminal any more
	var at int
	waitFor(t, "the panel shows the flow UI's install", 20*time.Second, func() bool {
		s := panel.screen(&at)
		return strings.Contains(s, "Shown from the installer on") && strings.Contains(s, "Installing")
	})
	f.wait("bundle p10-install-", 600*time.Second)
	if s := f.endScreen(); s != "done" {
		t.Fatalf("end screen %q", s)
	}
	waitFor(t, "the panel shows Done", 10*time.Second, func() bool { return strings.Contains(panel.screen(&at), "Install done") })
	// the flow UI hangs: the panel says that its state is old
	cmd.Process.Signal(syscall.SIGSTOP)
	waitFor(t, "the panel warns about a flow UI that does not draw", 20*time.Second, func() bool {
		return strings.Contains(panel.screen(&at), "has not drawn for")
	})
	cmd.Process.Signal(syscall.SIGKILL)
	cmd.Wait()
	// the panel takes over and keeps Done
	f.wait("UI started again: done", 30*time.Second)
	waitFor(t, "the panel draws Done itself", 10*time.Second, func() bool {
		s := panel.screen(&at)
		return strings.Contains(s, "Install done") && !strings.Contains(s, "Shown from the installer on")
	})
}

// TestFlowKills: the install child is SIGKILLed after primitive write k (a
// devtest flag of the child): at the first and the last data write, between
// the steps, and after the arm. The flow must wait, set the read-only flags
// again, read the sector itself, and stop on a legal state (Stopped with D1,
// or D2 after the arm), never a hold, with the bundle on the drive.
func TestFlowKills(t *testing.T) {
	for _, k := range []int{1, 2, 40, 80} {
		t.Run(fmt.Sprintf("SIGKILL after write %d", k), func(t *testing.T) {
			f := newFUnit(t, unitBoth)
			u := f.startUI("--test-child-args", fmt.Sprintf("--test-kill-after %d --test-max-write 32768", k))
			u.toPlan("")
			time.Sleep(500 * time.Millisecond)
			u.keys("\ry")
			f.wait("bundle p10-install-", 600*time.Second)
			p := readPlanFile(t, f.plan)
			cs := f.cutState(p)
			s := f.endScreen()
			if s != "stopped" || !strings.Contains(f.flowLog(), "install child ended: ended by the signal killed") {
				t.Fatalf("end screen %q (cut state %+v)\n%s", s, cs, f.flowLog())
			}
			if cs.Boots != "keeper" && !(cs.Boots == "target" && cs.Checked) {
				t.Fatalf("%+v", cs)
			}
			f.checkBundle("stopped")
			u.quit()
		})
	}
}

// TestFlowDriveChanged: the host takes the drive back during the install
// (the lun holds the file again). The device mode stops before the next
// step with the target disabled; the flow shows Stopped (D1), never armed.
func TestFlowDriveChanged(t *testing.T) {
	f := newFUnit(t, unitBoth)
	u := f.startUI("--test-child-args", "--test-max-write 32768")
	u.toPlan("")
	time.Sleep(500 * time.Millisecond)
	u.keys("\ry")
	f.wait("install child started", 20*time.Second)
	// wait for the first data step in the transcript, then attach
	tr := ""
	for i := 0; i < 600 && !strings.Contains(tr, "write uboot_b"); i++ {
		time.Sleep(100 * time.Millisecond)
		matches, _ := filepath.Glob(f.root.Path(flowio.FlowDir + "/install-*.out"))
		if len(matches) == 1 {
			b, _ := os.ReadFile(matches[0])
			tr = string(b)
		}
	}
	os.WriteFile(f.root.Path(drivestate.LunFile), []byte(f.root.Path(drivestate.Image)+"\n"), 0o644)
	f.wait("bundle p10-install-", 600*time.Second)
	p := readPlanFile(t, f.plan)
	cs := f.cutState(p)
	if s := f.endScreen(); s != "stopped" || cs.Boots != "keeper" || strings.Contains(cs.Target, "armed") {
		t.Fatalf("screen %q, %+v\n%s", f.endScreen(), cs, f.flowLog())
	}
	if !strings.Contains(f.flowLog(), "the bundle") && !strings.Contains(f.flowLog(), "bundle p10-install-") {
		t.Fatal("no bundle")
	}
	u.quit()
}

// hostCopy puts a new drive with the firmware and the key file into the
// drive file while the host has the drive (a user copying the firmware
// again after a stop).
func (f *funit) hostCopy() {
	im := fattest.New(fattest.Spec{Type: 16, Sectors: 2*len(fixture.FW)/512 + 40000, SecPerClus: 4, Label: "P10-INSTALL"})
	im.Root.File("UC-P10 1.0.0.94.bin", fixture.FW)
	im.Root.File(flowio.KeyFileName, []byte(fmt.Sprintf("head_key=%x\niv=%x\n", fixture.Keys.Head[:], fixture.Keys.IV[:])))
	img := f.root.Path(drivestate.Image)
	os.Remove(img)
	os.MkdirAll(filepath.Dir(img), 0o755) // after a power cut the RAM drive is new
	if err := os.WriteFile(img, im.B, 0o644); err != nil {
		f.t.Fatal(err)
	}
	os.WriteFile(f.root.Path(drivestate.LunFile), []byte(img+"\n"), 0o644)
}

// waitCount waits until the flow log has more than n lines with what.
func (f *funit) waitCount(what string, n int, d time.Duration) {
	f.t.Helper()
	end := time.Now().Add(d)
	for strings.Count(f.flowLog(), what) <= n {
		if time.Now().After(end) {
			f.t.Fatalf("no new %q in the flow log after %s:\n%s", what, d, f.flowLog())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// keepA are the keys of the keeper screen that keep slot A: the screen
// starts on the slot that boots next (U7).
func (f *funit) keepA() string {
	sec, err := f.root.ABSector()
	if err != nil {
		f.t.Fatal(err)
	}
	a, _ := avbab.Parse(sec)
	if a.Pick() == 1 {
		return "k\r"
	}
	return "\r"
}

// stepWrites are the primitive write counts at the end of each step of an
// install, from the JSON-lines log of a full run.
func stepWrites(t *testing.T, jsonl []byte) []int {
	var out []int
	for _, l := range strings.Split(string(jsonl), "\n") {
		var ev map[string]any
		if json.Unmarshal([]byte(l), &ev) != nil || ev["step"] == nil || ev["writes"] == nil {
			continue
		}
		w := int(ev["writes"].(float64))
		if len(out) == 0 || out[len(out)-1] != w {
			out = append(out, w)
		}
	}
	return out
}

// installLog is the install's JSON-lines log of the last run.
func (f *funit) installLog() []byte {
	m, _ := filepath.Glob(f.root.Path(flowio.FlowDir + "/install-*.jsonl"))
	if len(m) != 1 {
		f.t.Fatalf("install logs: %v", m)
	}
	b, _ := os.ReadFile(m[0])
	return b
}

// resumeToDone: after a stop, OK (try again), the host copies the firmware
// again and ejects, the keeper screen if the planner asks (keep A, the
// successful slot), the plan, the install: Done.
func (u *uiProc) resumeToDone() {
	f := u.f
	n := strings.Count(f.flowLog(), "plan child ended")
	f.hostCopy()
	fw := strings.Count(f.flowLog(), "screen firmware")
	u.keys("\r") // try again
	f.waitCount("screen firmware", fw, 10*time.Second)
	time.Sleep(300 * time.Millisecond)
	f.eject()
	f.waitCount("plan child ended", n, 300*time.Second)
	if strings.Contains(f.flowLog()[strings.LastIndex(f.flowLog(), "plan child ended"):], "keeper:") {
		// the target is armed (trying) after a stop at the end: keep the successful A
		u.keys(f.keepA())
		f.waitCount("plan child ended", n+1, 300*time.Second)
	}
	time.Sleep(300 * time.Millisecond)
	b := strings.Count(f.flowLog(), "bundle p10-install-")
	u.keys("\ry")
	f.waitCount("bundle p10-install-", b, 600*time.Second)
	if s := f.endScreen(); s != "done" {
		f.t.Fatalf("resume: end screen %q\n%s", s, f.flowLog())
	}
}

// TestFlowSweep: a kill of the install child, and a power cut of the whole
// unit (the UI, the supervisor and the child killed at once, the RAM run
// folder gone), at the end of every step of the install. After each: the
// cut rule on the disk bytes; after a kill the flow stops on a legal state
// (Stopped, never a hold) with readonly, the sector read and the bundle,
// then it resumes to Done through the flow; after a cut a new UI (a new RAM
// image) shows a valid A/B state and installs to Done.
func TestFlowSweep(t *testing.T) {
	// a full run gives the write count at the end of each step
	f0 := newFUnit(t, unitBoth)
	u0 := f0.startUI("--test-child-args", "--test-max-write 32768")
	u0.toPlan("")
	time.Sleep(300 * time.Millisecond)
	u0.keys("\ry")
	f0.wait("bundle p10-install-", 600*time.Second)
	points := stepWrites(t, f0.installLog())
	u0.quit()
	if len(points) < 5 {
		t.Fatalf("step ends %v", points)
	}
	t.Logf("write counts at the step ends: %v", points)
	kills, cuts := 0, 0
	for _, k := range points {
		if k == 0 {
			continue
		}
		t.Run(fmt.Sprintf("kill after write %d", k), func(t *testing.T) {
			f := newFUnit(t, unitBoth)
			u := f.startUI("--test-child-args", fmt.Sprintf("--test-kill-after %d --test-max-write 32768", k))
			u.toPlan("")
			time.Sleep(300 * time.Millisecond)
			u.keys("\ry")
			f.wait("bundle p10-install-", 600*time.Second)
			p := readPlanFile(t, f.plan)
			cs := f.cutState(p)
			if s := f.endScreen(); s != "stopped" {
				t.Fatalf("end screen %q (%+v)\n%s", s, cs, f.flowLog())
			}
			f.checkBundle("stopped")
			u.quit()
			// the resume through the flow: a new UI (the stop is kept), try again
			// (flow.log keeps the lines of the UI before: count the new ones)
			st := strings.Count(f.flowLog(), "status: A/B valid true")
			u2 := f.startUI("--test-child-args", "--test-max-write 32768")
			f.wait("UI started again: stopped", 30*time.Second)
			f.waitCount("status: A/B valid true", st, 60*time.Second)
			u2.resumeToDone()
			cs = f.cutState(p)
			if cs.Boots != "target" || !cs.Checked {
				t.Fatalf("after the resume: %+v", cs)
			}
			u2.quit()
			kills++
		})
		t.Run(fmt.Sprintf("power cut after write %d", k), func(t *testing.T) {
			f := newFUnit(t, unitBoth)
			u := f.startUI("--test-child-args", fmt.Sprintf("--test-pause-after %d --test-max-write 32768", k))
			u.toPlan("")
			time.Sleep(300 * time.Millisecond)
			u.keys("\ry")
			f.wait("install child started", 20*time.Second)
			var dir string
			for i := 0; i < 600; i++ {
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
			// the cut: every process of the run at once, then the RAM is gone
			pid, _ := os.ReadFile(filepath.Join(dir, "supervisor.pid"))
			var sp int
			fmt.Sscan(string(pid), &sp)
			u.cmd.Process.Signal(syscall.SIGKILL)
			syscall.Kill(-sp, syscall.SIGKILL)
			u.cmd.Wait()
			time.Sleep(300 * time.Millisecond)
			p := readPlanFile(t, f.plan)
			cs := f.cutState(p)
			if cs.Boots != "keeper" && !(cs.Boots == "target" && cs.Checked) {
				t.Fatalf("after the cut: %+v", cs)
			}
			os.RemoveAll(f.root.Path(flowio.RunDir))
			os.Remove(f.root.Path(device.ABLockPath))
			f.hostCopy()
			// a new RAM image: the status, then a whole install
			u2 := f.startUI("--test-child-args", "--test-max-write 32768")
			f.wait("status: A/B valid true", 60*time.Second)
			u2.keys("j\r")
			u2.keys("\rj\rj\rjj\rj\r")
			f.wait("screen firmware", 10*time.Second)
			f.eject()
			f.wait("plan child ended", 300*time.Second)
			if strings.Contains(f.flowLog(), "keeper:") {
				// the target trying (a cut after the arm): keep A
				u2.keys(f.keepA())
				f.waitCount("plan child ended", 1, 300*time.Second)
			}
			time.Sleep(300 * time.Millisecond)
			u2.keys("\ry")
			f.wait("bundle p10-install-", 600*time.Second)
			if s := f.endScreen(); s != "done" {
				t.Fatalf("after the cut and a new install: %q\n%s", s, f.flowLog())
			}
			u2.quit()
			cuts++
		})
	}
	t.Logf("flow sweep: %d kills and %d power cuts at step ends, each resumed to Done through the flow", kills, cuts)
}
