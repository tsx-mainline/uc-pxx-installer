package flowio

// The runner with a fake child: a shell script stands for `p10inst plan`
// and `p10inst install` and ends in each way the flow must handle (exit 0,
// 1, 2 with a Go panic, 3, a SIGKILL, a sector that is no state of the
// plan). The supervisor is the real one (this test binary runs
// SuperviseMain). The unit is a fake tree of package fakedev: the A/B
// sector is in the misc node file. Nothing here touches a disk.

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"uc-pxx-installer/internal/blk"
	"uc-pxx-installer/internal/drivestate"
	"uc-pxx-installer/internal/fakedev"
	"uc-pxx-installer/internal/fatfs"
	"uc-pxx-installer/internal/fatfs/fattest"
	"uc-pxx-installer/internal/flow"
	"uc-pxx-installer/internal/nvram/nvramtest"
	"uc-pxx-installer/internal/plan"
)

func TestMain(m *testing.M) {
	if os.Getenv("P10INST_FLOWIO_SUPERVISOR") == "1" {
		// flow-supervise --dir D -- ARGV
		a := os.Args[1:]
		if len(a) < 4 || a[0] != "flow-supervise" || a[1] != "--dir" || a[3] != "--" {
			os.Exit(2)
		}
		os.Exit(SuperviseMain(a[2], a[4:]))
	}
	os.Exit(m.Run())
}

func sector(a, b [3]byte) []byte {
	s := make([]byte, 512)
	copy(s, []byte{0, 'A', 'B', '0', 1, 0})
	copy(s[8:], []byte{a[0], a[1], a[2], 0, b[0], b[1], b[2], 0})
	binary.BigEndian.PutUint32(s[28:], crc32.ChecksumIEEE(s[:28]))
	for i := 32; i < 512; i++ {
		s[i] = byte(i)
	}
	return s
}

var (
	s0 = sector([3]byte{15, 0, 1}, [3]byte{14, 0, 1})
	d1 = sector([3]byte{15, 0, 1}, [3]byte{0, 0, 0})
	d2 = sector([3]byte{14, 0, 1}, [3]byte{15, 7, 0})
	xx = sector([3]byte{14, 0, 1}, [3]byte{15, 6, 0}) // valid, no state of the plan
)

func raw(b []byte) [32]byte {
	var r [32]byte
	copy(r[:], b)
	return r
}

func testPlan() *plan.Plan {
	sum := sha256.Sum256(s0)
	p := &plan.Plan{Schema: 1, Kind: plan.Kind, Mode: "device", Keeper: "a", Target: "b",
		AB: plan.AB{LBA: 49156, Now: plan.State(raw(s0)), Final: plan.State(raw(d2)), SectorSHA256: hex.EncodeToString(sum[:])},
		Steps: []plan.Step{
			{N: 1, Op: "bootctl", Bootctl: &plan.BootctlStep{Args: []string{"disable", "--slot", "b"}, Before: plan.State(raw(s0)), After: plan.State(raw(d1))}},
			{N: 2, Op: "write", Write: &plan.WriteStep{Partition: "boot_b", Length: 1 << 20}},
			{N: 3, Op: "sync"},
			{N: 4, Op: "verify"},
			{N: 5, Op: "bootctl", Bootctl: &plan.BootctlStep{Args: []string{"arm-trying", "--slot", "b", "--lower-keeper"}, Before: plan.State(raw(d1)), After: plan.State(raw(d2))}},
			{N: 6, Op: "check-ab"},
		},
		Device: &plan.DeviceID{Node: "/dev/mmcblk0", Model: "Crestron UC-P10", Host: "fe330000.mmc", CIDSHA256: strings.Repeat("ab", 32)},
	}
	p.Seal()
	return p
}

type rig struct {
	t        *testing.T
	root     string
	misc     string
	r        *Runner
	m        *flow.Model
	readonly int
	restarts int
}

// newRig makes the fake unit and the fake p10inst. ending is the shell code
// of the install child after it wrote the sector file after.bin.
func newRig(t *testing.T, after []byte, ending string) *rig {
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	dev, err := fakedev.New(root, fakedev.Layout)
	if err != nil {
		t.Fatal(err)
	}
	dev.WriteDisk(49156, s0) // the status reads the whole disk, the flow the misc node
	// Crestron auto-update off in others (the flow refuses any other state)
	dev.WriteDisk(3671040, nvramtest.Off())
	dev.WritePart(fakedev.PartNum(fakedev.Layout, "others"), 0, nvramtest.Off())
	n := fakedev.PartNum(fakedev.Layout, "misc")
	misc := filepath.Join(root, "dev", fmt.Sprintf("mmcblk0p%d", n))
	f, _ := os.OpenFile(misc, os.O_RDWR, 0)
	f.WriteAt(s0, 4*512)
	f.Close()
	// the drive with a firmware file, attached to the host
	im := fattest.New(fattest.Spec{Type: 16, Sectors: 70000, SecPerClus: 4, Label: "P10-INSTALL"})
	im.Root.File("fw.bin", []byte(strings.Repeat("firmware", 1000)))
	img := filepath.Join(root, drivestate.Image)
	os.MkdirAll(filepath.Dir(img), 0o755)
	os.WriteFile(img, im.B, 0o644)
	lun := filepath.Join(root, drivestate.LunFile)
	os.MkdirAll(filepath.Dir(lun), 0o755)
	os.WriteFile(lun, []byte(img+"\n"), 0o644)
	// the plan the fake plan child gives, and the sector the fake install leaves
	pj, _ := json.MarshalIndent(testPlan(), "", "  ")
	os.WriteFile(filepath.Join(dir, "plan.json"), pj, 0o644)
	os.WriteFile(filepath.Join(dir, "after.bin"), after, 0o644)
	self, _ := os.Executable()
	script := fmt.Sprintf(`#!/bin/sh
D=%q
MISC=%q
case "$1" in
flow-supervise) exec env P10INST_FLOWIO_SUPERVISOR=1 %q "$@" ;;
plan)
	while [ $# -gt 0 ]; do [ "$1" = --json ] && cp "$D/plan.json" "$2"; shift; done
	echo "pass 1: check the whole firmware"
	echo "RESULT: plan OK (nothing written)"
	exit 0 ;;
install)
	LOG=
	while [ $# -gt 0 ]; do [ "$1" = --log ] && LOG=$2; shift; done
	echo "pass 1: check the whole firmware"
	echo " 1/6 bootctl disable --slot b"
	echo '{"event":"start","ab":"%s"}' > "$LOG"
	echo "transcript" > "${LOG%%.jsonl}.out"
	dd if="$D/after.bin" of="$MISC" bs=512 seek=4 conv=notrunc 2>/dev/null
	%s ;;
esac
exit 9
`, dir, misc, self, hex.EncodeToString(s0[:32]), ending)
	exe := filepath.Join(dir, "p10inst")
	os.WriteFile(exe, []byte(script), 0o755)
	g := &rig{t: t, root: root, misc: misc}
	g.m = flow.New("test", true)
	g.r = New(Config{Root: root, Exe: exe, Proc: "/proc", Now: time.Now,
		Readonly: func() (string, error) { g.readonly++; return "read-only: test", nil },
		Restart:  func() { g.restarts++ }}, g.m)
	// a hold starts the repair check (a child): let it end before the test
	// folder is removed
	t.Cleanup(func() {
		end := time.Now().Add(10 * time.Second)
		for (g.m.Running() != "" || g.r.child != nil) && time.Now().Before(end) {
			select {
			case f := <-g.r.Events():
				f()
			case <-time.After(50 * time.Millisecond):
			}
			g.r.Poll()
		}
	})
	return g
}

// pump runs the main loop until cond or the timeout.
func (g *rig) pump(cond func() bool, d time.Duration) {
	g.t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		select {
		case f := <-g.r.Events():
			f()
		case <-time.After(50 * time.Millisecond):
		}
		g.r.Poll()
		g.m.Tick(time.Now())
		g.r.Do(g.m.Take())
		g.fit()
		if cond() {
			return
		}
	}
	g.t.Fatalf("timeout; screen %s; log:\n%s", g.m.Screen, strings.Join(g.r.log, "\n"))
}

func (g *rig) key(k flow.Key) {
	g.m.Handle(flow.Event{Key: k, Down: true}, time.Now())
	g.m.Handle(flow.Event{Key: k, Down: false}, time.Now())
	g.r.Do(g.m.Take())
	g.fit()
}

// fit: every screen the runner tests reach fits the panel (120 x 37).
func (g *rig) fit() {
	g.t.Helper()
	if p := g.m.View(time.Now()).Layout(flow.PanelCols, flow.PanelRows).Problem(g.m.Screen); p != "" {
		g.t.Fatalf("screen %s does not fit the panel: %s", g.m.Screen, p)
	}
}

// TestStatusFits: the status screen with the real status of a fake unit,
// and of a root without an eMMC (status.Collect), fits the panel, with the
// menu in the footer and Install refused with its message.
func TestStatusFits(t *testing.T) {
	g := newRig(t, d2, "exit 0")
	g.r.Start()
	g.pump(func() bool { return g.m.Unit != nil }, 20*time.Second)
	lay := g.m.View(time.Now()).Layout(flow.PanelCols, flow.PanelRows)
	if !strings.Contains(lay.Body[0].Text, "[ STATUS ]") {
		t.Fatalf("the menu row %q", lay.Body[0].Text)
	}
	// the flow's own status: auto-update off, the read-only flag, no
	// "This image only reads" (review L-6, owner r3)
	all := ""
	for _, l := range g.m.Unit.Lines {
		all += l.Text + "\n"
	}
	if strings.Contains(all, "This image only reads") || !strings.Contains(all, "Crestron auto-update           off") ||
		!strings.Contains(all, "eMMC read-only flag            ") || !strings.Contains(all, "Firmware key kept on the unit  no") {
		t.Fatalf("the status rows:\n%s", all)
	}
	empty := filepath.Join(t.TempDir(), "root")
	os.MkdirAll(filepath.Join(empty, "run"), 0o755)
	m := flow.New("UC-P10 installer  -  install image  -  p10inst", false)
	r := New(Config{Root: empty, Proc: "/proc", Now: time.Now}, m)
	r.Start()
	end := time.Now().Add(20 * time.Second)
	for m.Unit == nil && time.Now().Before(end) {
		select {
		case f := <-r.Events():
			f()
		case <-time.After(50 * time.Millisecond):
		}
	}
	if m.Unit == nil || m.Unit.ABValid {
		t.Fatalf("no eMMC: %+v", m.Unit)
	}
	m.Handle(flow.Event{Key: flow.KeyNext, Down: true}, time.Now())
	m.Handle(flow.Event{Key: flow.KeyOK, Down: true}, time.Now())
	lay = m.View(time.Now()).Layout(flow.PanelCols, flow.PanelRows)
	if p := lay.Problem(m.Screen); p != "" || m.Msg == "" || !strings.Contains(lay.Body[0].Text, "[ INSTALL ]") {
		t.Fatalf("no eMMC: %q, msg %q, menu %q", p, m.Msg, lay.Body[0].Text)
	}
}

func (g *rig) lunFile() string { return filepath.Join(g.root, drivestate.LunFile) }

// toInstall: status, checklist, the eject, the plan, Enter y.
func (g *rig) toInstall() {
	g.r.Start()
	g.pump(func() bool { return g.m.Unit != nil }, 20*time.Second)
	g.key(flow.KeyNext)
	g.key(flow.KeyOK)
	if g.m.Screen != flow.ScreenFirmware { // auto-update off: Install goes to the firmware screen
		g.t.Fatalf("screen %s (%s)", g.m.Screen, g.m.Msg)
	}
	g.key(flow.KeyOK) // eject from the unit
	if v, _ := os.ReadFile(g.lunFile()); strings.TrimSpace(string(v)) != "" {
		g.t.Fatalf("the lun after the eject: %q", v)
	}
	g.pump(func() bool { return g.m.Screen == flow.ScreenPlan }, 30*time.Second)
	g.key(flow.KeyOK)
	g.key(flow.KeyYes)
	if g.m.Screen != flow.ScreenProgress {
		g.t.Fatalf("screen %s", g.m.Screen)
	}
}

func (g *rig) end() {
	g.pump(func() bool { return g.m.Bundle != "" }, 60*time.Second)
}

func TestEnds(t *testing.T) {
	panicOut := `echo "panic: runtime error: index out of range"; echo "goroutine 1 [running]:"; exit 2`
	for _, c := range []struct {
		name   string
		after  []byte
		ending string
		want   flow.Screen
		state  string
	}{
		{"exit 0, D2", d2, "exit 0", flow.ScreenDone, flow.StateD2},
		{"exit 0, but D1", d1, "exit 0", flow.ScreenHold, flow.StateD1},
		{"exit 1, D1", d1, "exit 1", flow.ScreenStopped, flow.StateD1},
		{"exit 1, S0", s0, "exit 1", flow.ScreenStopped, flow.StateS0},
		{"a Go panic (exit 2), D1", d1, panicOut, flow.ScreenStopped, flow.StateD1},
		{"exit 3, D1", d1, "exit 3", flow.ScreenHold, flow.StateD1},
		{"SIGKILL, D2", d2, "kill -9 $$", flow.ScreenStopped, flow.StateD2},
		{"SIGKILL, another sector", xx, "kill -9 $$", flow.ScreenHold, flow.StateUnknown},
		{"exit 1, another sector", xx, "exit 1", flow.ScreenHold, flow.StateUnknown},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := newRig(t, c.after, c.ending)
			g.toInstall()
			g.end()
			if g.m.Screen != c.want || g.m.Outcome.State != c.state {
				t.Fatalf("screen %s state %s (%s), want %s %s", g.m.Screen, g.m.Outcome.State, g.m.Outcome.Why, c.want, c.state)
			}
			if g.readonly != 1 {
				t.Fatalf("p10inst readonly ran %d times after the end, want 1", g.readonly)
			}
			// the bundle is on the drive, and the drive is with the host again
			if v, _ := os.ReadFile(g.lunFile()); strings.TrimSpace(string(v)) != filepath.Join(g.root, drivestate.Image) {
				t.Fatalf("lun %q", v)
			}
			fs, err := fatfs.OpenImage(filepath.Join(g.root, drivestate.Image))
			if err != nil {
				t.Fatal(err)
			}
			top, _ := fs.ReadDir("/")
			var readme []byte
			if len(top) == 1 {
				readme, _ = fs.ReadFile("/"+top[0].Name+"/README.txt", 4096)
			}
			fs.Close()
			if len(top) != 1 || !strings.HasPrefix(top[0].Name, "p10-install-"+testPlan().ID[:12]+"-") {
				t.Fatalf("drive: %+v", top)
			}
			// the contact of U1 (the GitHub issues of the project)
			if !strings.Contains(string(readme), flow.BundleContact) || !strings.Contains(flow.BundleContact, "https://github.com/tsx-mainline/uc-pxx-installer/issues") {
				t.Fatalf("README.txt %q", readme)
			}
			// the restart: blocked in a hold, allowed otherwise
			g.m.Handle(flow.Event{Key: flow.KeyBack, Down: true}, time.Now())
			g.r.Do([]flow.Action{{Kind: flow.ActRestart}})
			if c.want == flow.ScreenHold {
				if g.restarts != 0 || g.m.RestartBlocked() == "" {
					t.Fatal("a restart after a hold")
				}
				// the hold survives a new UI process
				m2 := flow.New("t", true)
				r2 := New(g.r.c, m2)
				r2.Start()
				if m2.Screen != flow.ScreenHold || m2.RestartBlocked() == "" {
					t.Fatalf("new UI: screen %s", m2.Screen)
				}
			} else if g.restarts != 1 {
				t.Fatalf("restarts %d", g.restarts)
			}
		})
	}
}

// TestHoldReadAgain: the hold screen's "read the A/B sector again" (U1).
func TestHoldReadAgain(t *testing.T) {
	g := newRig(t, xx, "exit 3")
	g.toInstall()
	g.end()
	if g.m.Screen != flow.ScreenHold {
		t.Fatalf("screen %s", g.m.Screen)
	}
	view := func() string {
		var b strings.Builder
		for _, l := range g.m.View(time.Now()).Body {
			b.WriteString(l.Text + "\n")
		}
		return b.String()
	}
	// the repair check of the hold start ends first (the fake child: exit 9, refused)
	g.pump(func() bool { return g.m.Running() == "" }, 10*time.Second)
	g.key(flow.KeyOK)
	g.pump(func() bool { return strings.Contains(view(), "the slot state is still not known") }, 10*time.Second)
	// the sector is D1 now (say, bootctl finished late)
	f, _ := os.OpenFile(g.misc, os.O_RDWR, 0)
	f.WriteAt(d1, 4*512)
	f.Close()
	g.key(flow.KeyOK)
	g.pump(func() bool { return strings.Contains(view(), "Checked again: Slot B is not finished") }, 10*time.Second)
	if g.m.Screen != flow.ScreenHold || g.m.RestartBlocked() == "" {
		t.Fatal("the read again left the hold")
	}
}

// TestDriveRefusals: no .bin, two .bin, a dirty drive: refused, the drive
// goes back (made new when dirty), nothing starts.
func TestDriveRefusals(t *testing.T) {
	for _, c := range []struct {
		name   string
		build  func(im *fattest.Image)
		want   string
		remade bool
	}{
		{"no .bin", func(im *fattest.Image) { im.Root.File("notes.txt", []byte("x")) }, "no firmware file", false},
		{"two .bin", func(im *fattest.Image) { im.Root.File("a.bin", []byte("x")); im.Root.File("b.bin", []byte("y")) }, "2 firmware files", false},
		{"dirty", func(im *fattest.Image) { im.Root.File("a.bin", []byte("x")); im.SetDirty() }, "not ejected cleanly", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := newRig(t, d2, "exit 0")
			im := fattest.New(fattest.Spec{Type: 16, Sectors: 70000, SecPerClus: 4, Label: "P10-INSTALL"})
			c.build(im)
			img := filepath.Join(g.root, drivestate.Image)
			os.WriteFile(img, im.B, 0o644)
			g.r.Start()
			g.pump(func() bool { return g.m.Unit != nil }, 20*time.Second)
			g.key(flow.KeyNext)
			g.key(flow.KeyOK)
			for i := 0; i < 5; i++ {
				g.key(flow.KeyOK)
				g.key(flow.KeyNext)
			}
			g.key(flow.KeyOK)
			os.WriteFile(g.lunFile(), []byte("\n"), 0o644) // the host ejects
			g.pump(func() bool { return strings.Contains(strings.Join(g.r.log, "\n"), "drive given back") }, 20*time.Second)
			if g.m.Screen != flow.ScreenFirmware || !strings.Contains(strings.Join(g.r.log, "\n"), c.want) {
				t.Fatalf("screen %s log:\n%s", g.m.Screen, strings.Join(g.r.log, "\n"))
			}
			if g.m.Running() != "" {
				t.Fatal("a child runs")
			}
			fs, err := fatfs.OpenImage(img)
			if err != nil {
				t.Fatal(err)
			}
			es, _ := fs.ReadDir("/")
			fs.Close()
			if c.remade != (len(es) == 0) {
				t.Fatalf("remade %v, the drive has %d entries", c.remade, len(es))
			}
		})
	}
}

// TestCancel: Back on the plan screen: nothing starts, the drive goes back.
func TestCancel(t *testing.T) {
	g := newRig(t, d2, "exit 0")
	g.r.Start()
	g.pump(func() bool { return g.m.Unit != nil }, 20*time.Second)
	g.key(flow.KeyNext)
	g.key(flow.KeyOK)
	for i := 0; i < 5; i++ {
		g.key(flow.KeyOK)
		g.key(flow.KeyNext)
	}
	g.key(flow.KeyOK)
	g.key(flow.KeyOK)
	g.pump(func() bool { return g.m.Screen == flow.ScreenPlan }, 30*time.Second)
	g.key(flow.KeyBack)
	if g.m.Screen != flow.ScreenStatus {
		t.Fatalf("screen %s", g.m.Screen)
	}
	if v, _ := os.ReadFile(g.lunFile()); strings.TrimSpace(string(v)) == "" {
		t.Fatal("the drive was not given back")
	}
	b, _ := os.ReadFile(g.misc)
	if string(b[2048:2560]) != string(s0) {
		t.Fatal("the sector changed")
	}
}

// TestOwnABRead: the flow's decisions rest on its own O_DIRECT read of the
// A/B sector in misc, not on the status (which reads the whole disk
// through the page cache): here the two differ.
func TestOwnABRead(t *testing.T) {
	g := newRig(t, d2, "exit 0")
	f, _ := os.OpenFile(g.misc, os.O_RDWR, 0)
	f.WriteAt(d2, 4*512)
	f.Close()
	g.r.Start()
	g.pump(func() bool { return g.m.Unit != nil }, 20*time.Second)
	if g.m.Unit.Next != 1 || g.m.Unit.Slots[1].AB.State() != "trying" || g.r.report.Next != 0 {
		t.Fatalf("unit next %d, B %v; the status says next %d", g.m.Unit.Next, g.m.Unit.Slots[1].AB, g.r.report.Next)
	}
}

// TestReadPartDirect: whole sectors inside the partition only. On a test
// tree (regular files) it reads without O_DIRECT; the O_DIRECT path needs a
// block device and is not run here.
func TestReadPartDirect(t *testing.T) {
	root := t.TempDir()
	dev, err := fakedev.New(root, fakedev.Layout)
	if err != nil {
		t.Fatal(err)
	}
	if err := dev.WritePart(fakedev.PartNum(fakedev.Layout, "misc"), 4*512, d2); err != nil {
		t.Fatal(err)
	}
	d, err := blk.Find(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := readPartDirect(d, "misc", 4*512, 512)
	if err != nil || string(b) != string(d2) {
		t.Fatalf("%x %v", b[:8], err)
	}
	for _, c := range []struct {
		off int64
		n   int
	}{{1, 512}, {0, 100}, {0, 0}, {-512, 512}, {8192*512 - 512, 1024}} {
		if _, err := readPartDirect(d, "misc", c.off, c.n); err == nil {
			t.Errorf("read %d+%d accepted", c.off, c.n)
		}
	}
	if _, err := readPartDirect(d, "nosuch", 0, 512); err == nil {
		t.Error("no such partition accepted")
	}
}

// TestEjectAttach: forced_eject when there, else an empty lun; attach only
// a file in /run.
func TestEjectAttach(t *testing.T) {
	g := filepath.Join(t.TempDir(), "run/gadget")
	lun := lunFile(g)
	os.MkdirAll(filepath.Dir(lun), 0o755)
	os.WriteFile(lun, []byte("/run/p10inst/drive/drive.img\n"), 0o644)
	if err := ejectDrive(g); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(lun); strings.TrimSpace(string(b)) != "" {
		t.Fatalf("lun %q", b)
	}
	fe := filepath.Join(filepath.Dir(lun), "forced_eject")
	os.WriteFile(fe, nil, 0o644)
	os.WriteFile(lun, []byte("x"), 0o644)
	ejectDrive(g)
	if b, _ := os.ReadFile(fe); string(b) != "1" {
		t.Fatalf("forced_eject %q", b)
	}
	for _, bad := range []string{"/dev/mmcblk0", "/proc/self/exe", "/tmp/drive.img"} {
		if err := attachDrive(g, bad); err == nil {
			t.Errorf("attach %s accepted", bad)
		}
	}
	if err := attachDrive(g, "/run/p10inst/drive/drive.img"); err != nil {
		t.Fatal(err)
	}
}

// TestFlowLogKept: a UI that takes over (after a crash, or after it waited)
// keeps the flow log of the UIs before it (review L-1).
func TestFlowLogKept(t *testing.T) {
	g := newRig(t, d2, "exit 0")
	g.r.Start()
	g.pump(func() bool { return g.m.Unit != nil }, 20*time.Second)
	g.r.Note("layout: a fault of the first UI")
	m2 := flow.New("t", true)
	r2 := New(g.r.c, m2)
	r2.Start()
	r2.Note("the second UI")
	b, _ := os.ReadFile(filepath.Join(g.root, FlowDir, "flow.log"))
	s := string(b)
	i1, i2, i3 := strings.Index(s, "flow started"), strings.Index(s, "layout: a fault of the first UI"), strings.Index(s, "UI started again")
	if i1 < 0 || i2 < i1 || i3 < i2 || !strings.Contains(s, "the second UI") {
		t.Fatalf("flow.log:\n%s", s)
	}
}
