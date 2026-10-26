//go:build install && devtest

package device_test

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/device/devicetest"
	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/plan"
)

var small = fakeemmc.ParseSmallLayout(fakeemmc.SmallLayout)

func TestLayoutIsTheUnitLayout(t *testing.T) {
	a, err := os.ReadFile("ucp10-layout.txt")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("../gpt/testdata/ucp10-layout.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("internal/device/ucp10-layout.txt differs from internal/gpt/testdata/ucp10-layout.txt")
	}
	l := device.Layout()
	if l.DiskSectors != 30535680 || len(l.Entries) != 28 {
		t.Fatalf("layout: %d sectors, %d entries", l.DiskSectors, len(l.Entries))
	}
}

func TestBootctlForms(t *testing.T) {
	for _, c := range []struct {
		args   string
		ok, wr bool
	}{
		{"status", true, false},
		{"disable --slot a", true, true},
		{"disable --slot b", true, true},
		{"arm-trying --slot a --lower-keeper", true, true},
		{"arm-trying --slot b --lower-keeper", true, true},
		{"arm-trying --slot b", false, false},
		{"mark-successful --expect a --token x", false, false},
		{"status --file /tmp/x", false, false},
		{"disable --slot b --dry-run", false, false},
		{"disable --slot c", false, false},
		{"arm-trying --lower-keeper --slot b", false, false},
		{"", false, false},
	} {
		ok, wr := device.BootctlFormForTest(strings.Fields(c.args))
		if ok != c.ok || wr != c.wr {
			t.Errorf("%q: ok %v writes %v", c.args, ok, wr)
		}
	}
}

type fakeUnit struct {
	root *devicetest.Root
	env  *device.Env
	d    *device.Disk
	img  []byte
}

func newFake(t *testing.T, bin []byte, sha string, r device.BootctlRunner) *fakeUnit {
	dir := t.TempDir()
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	src, err := fx.Image(dir, "src.img", fakeemmc.Unit{AB: [2][3]byte{{15, 0, 1}, {14, 0, 1}}, A: "old", B: "old"})
	if err != nil {
		t.Fatal(err)
	}
	img, _ := os.ReadFile(src)
	root, err := devicetest.New(filepath.Join(dir, "root"), src, small, bin, devicetest.Opts{})
	if err != nil {
		t.Fatal(err)
	}
	e, err := device.NewForTest(device.TestOptions{Root: root.Dir, RegularFiles: true, Layout: small, BootctlSHA256: sha, Runner: r})
	if err != nil {
		t.Fatal(err)
	}
	d, err := e.OpenEMMC(device.EMMCPath)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeUnit{root: root, env: e, d: d, img: img}
}

// The view reads every range as the disk holds it, across partition
// boundaries, through the partition files and the disk file.
func TestViewReads(t *testing.T) {
	u := newFake(t, []byte("x"), "", nil)
	v := u.d.View()
	defer v.Close()
	rnd := rand.New(rand.NewSource(1))
	for i := 0; i < 400; i++ {
		off := rnd.Int63n(int64(len(u.img)))
		n := rnd.Intn(300000) + 1
		if off+int64(n) > int64(len(u.img)) {
			n = int(int64(len(u.img)) - off)
		}
		b := make([]byte, n)
		if _, err := v.ReadAt(b, off); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(b, u.img[off:off+int64(n)]) {
			t.Fatalf("read %d+%d differs", off, n)
		}
	}
	ab, err := u.d.ReadAB()
	if err != nil || !bytes.Equal(ab, u.img[plan.ABLBA*512:(plan.ABLBA+1)*512]) {
		t.Fatalf("ReadAB: %v", err)
	}
}

// The write layer checks the range rule itself, whatever the caller does.
func TestWriteLayerRefuses(t *testing.T) {
	u := newFake(t, []byte("x"), "", nil)
	step := func(name string, off, n int64) *plan.WriteStep {
		e, _ := u.d.Table.Find(name)
		return &plan.WriteStep{Partition: name, GPTIndex: e.Index, StartLBA: e.Start, PartSectors: e.Sectors(), Offset: off, Length: n}
	}
	if _, err := device.OpenWriteForTest(u.d, "b", step("boot_b", 0, 4096), 4096); err == nil || !strings.Contains(err.Error(), "A/B lock") {
		t.Fatalf("without the lock: %v", err)
	}
	if err := u.d.Lock(); err != nil {
		t.Fatal(err)
	}
	defer u.d.Unlock()
	for _, c := range []struct {
		target string
		w      *plan.WriteStep
		want   string
	}{
		{"b", step("boot_a", 0, 4096), "not a partition of slot b"},
		{"b", step("misc", 0, 512), "not a partition of slot b"},
		{"b", step("userdata", 0, 512), "not a partition of slot b"},
		{"b", step("cfg", 0, 512), "not a partition of slot b"},
		{"b", step("boot_b", 0, 512*513), "larger than the partition"},
		{"b", step("boot_b", 100, 512), "not whole sectors"},
		{"b", &plan.WriteStep{Partition: "boot_b", GPTIndex: 13, StartLBA: 58496, PartSectors: 512, Length: 512}, "the plan says p13"},
	} {
		if _, err := device.OpenWriteForTest(u.d, c.target, c.w, 4096); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.w.Partition, err, c.want)
		}
	}
	// a write that runs past the planned range is refused at the write
	w, err := device.OpenWriteForTest(u.d, "b", step("dtbo_b", 0, 4096), 4096)
	if err != nil {
		t.Fatal(err)
	}
	big := bytes.Repeat([]byte{0xa5}, 256*512+4096)
	_, werr := w.Write(big)
	w.Close()
	if werr == nil || !strings.Contains(werr.Error(), "larger than the partition") {
		t.Fatalf("past the end: %v", werr)
	}
	after, _ := u.root.Image()
	e, _ := u.d.Table.Find("dtbo_b")
	if !bytes.Equal(after[:e.Start*512], u.img[:e.Start*512]) || !bytes.Equal(after[(e.Start+e.Sectors())*512:], u.img[(e.Start+e.Sectors())*512:]) {
		t.Fatal("a byte outside dtbo_b changed")
	}
}

type countRunner struct{ calls []string }

func (c *countRunner) Run(e *device.Env, bin []byte, args []string) (*device.BootctlResult, error) {
	c.calls = append(c.calls, strings.Join(args, " "))
	return &device.BootctlResult{}, nil
}

// uc-pxx-bootctl-inst runs only with its sha256, only in the three forms, and
// a write form only with the A/B lock.
func TestBootctlGuards(t *testing.T) {
	bin := []byte("#!/bin/sh\necho \"uc-pxx-bootctl: args $*\" >&2\nexit 0\n")
	sha := devicetest.SHA256(bin)
	cr := &countRunner{}
	u := newFake(t, bin, sha, cr)
	if _, err := u.d.Bootctl([]string{"mark-successful", "--expect", "a"}); err == nil {
		t.Fatal("a fourth form ran")
	}
	if _, err := u.d.Bootctl([]string{"disable", "--slot", "b"}); err == nil || !strings.Contains(err.Error(), "A/B lock") {
		t.Fatalf("a write form without the lock: %v", err)
	}
	if _, err := u.d.Bootctl([]string{"status"}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(u.root.Path(device.BootctlPath), append(bin, '#'), 0o755)
	if _, err := u.d.Bootctl([]string{"status"}); err == nil || !strings.Contains(err.Error(), "not the reviewed binary") {
		t.Fatalf("a changed binary: %v", err)
	}
	os.Remove(u.root.Path(device.BootctlPath))
	os.Symlink("/bin/true", u.root.Path(device.BootctlPath))
	if _, err := u.d.Bootctl([]string{"status"}); err == nil {
		t.Fatal("a symlink ran")
	}
	if strings.Join(cr.calls, "|") != "status" {
		t.Fatalf("runner calls %q", cr.calls)
	}
}

// The default runner runs a private copy of the checked bytes, and removes it.
func TestExecRunner(t *testing.T) {
	bin := []byte("#!/bin/sh\necho \"uc-pxx-bootctl: current: A{prio 15 tries 0 succ 1} B{prio 14 tries 0 succ 1} crc 00000000\" >&2\necho \"self $0\"\necho libavb-would-boot: a\nexit 0\n")
	u := newFake(t, bin, devicetest.SHA256(bin), nil)
	r, err := u.d.Bootctl([]string{"status"})
	if err != nil || r.RC != 0 || !strings.Contains(r.Stdout, "libavb-would-boot: a") {
		t.Fatalf("%+v %v", r, err)
	}
	self := strings.TrimPrefix(strings.Fields(r.Stdout)[1], "")
	if !strings.HasPrefix(self, u.root.Path("/run/p10inst/bootctl-")) || !strings.HasSuffix(self, "/uc-pxx-bootctl-inst") {
		t.Fatalf("ran %s", self)
	}
	if _, err := os.Stat(filepath.Dir(self)); !os.IsNotExist(err) {
		t.Fatal("the private copy was not removed")
	}
	// a non-zero exit is reported, not hidden
	bin3 := []byte("#!/bin/sh\nexit 3\n")
	u3 := newFake(t, bin3, devicetest.SHA256(bin3), nil)
	if r, err := u3.d.Bootctl([]string{"status"}); err != nil || r.RC != 3 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestOpenEMMCSetsNothingOnRegularFiles(t *testing.T) {
	u := newFake(t, []byte("x"), "", nil)
	after, err := u.root.Image()
	if err != nil || !bytes.Equal(after, u.img) {
		t.Fatalf("OpenEMMC changed the disk: %v", err)
	}
	if u.d.ID.Model != device.Model || u.d.ID.Host != device.EMMCHost || len(u.d.ID.CIDSHA256) != 64 {
		t.Fatalf("identity %+v", u.d.ID)
	}
	_ = gpt.Sector
}

// The default runner starts the tool in its own session and process group,
// with stdout and stderr in files, not pipes (review F2): a ^C, a hangup or a
// closed pipe of p10inst cannot reach it.
func TestExecRunnerSession(t *testing.T) {
	bin := []byte("#!/bin/sh\nset -- $(cut -d' ' -f1,5,6 /proc/$$/stat)\necho \"pid $1 pgrp $2 sid $3\"\necho \"out $(readlink /proc/$$/fd/1)\"\necho \"err $(readlink /proc/$$/fd/2)\" >&2\nexit 0\n")
	u := newFake(t, bin, devicetest.SHA256(bin), nil)
	r, err := u.d.Bootctl([]string{"status"})
	if err != nil || r.RC != 0 {
		t.Fatalf("%+v %v", r, err)
	}
	var pid, pgrp, sid int
	if _, err := fmt.Sscanf(r.Stdout, "pid %d pgrp %d sid %d", &pid, &pgrp, &sid); err != nil {
		t.Fatalf("%q: %v", r.Stdout, err)
	}
	if pgrp != pid || sid != pid || pgrp == syscall.Getpgrp() {
		t.Fatalf("pid %d pgrp %d sid %d (ours: pgrp %d): not its own session", pid, pgrp, sid, syscall.Getpgrp())
	}
	if !strings.Contains(r.Stdout, "out "+u.root.Path("/run/p10inst/bootctl-")) || !strings.HasSuffix(strings.TrimSpace(r.Stdout), "/stdout") ||
		!strings.Contains(r.Stderr, "/stderr") || strings.Contains(r.Stdout+r.Stderr, "pipe:") {
		t.Fatalf("output is not in files: %q %q", r.Stdout, r.Stderr)
	}
	// a tool that a signal ends is reported as such, not as a plain exit
	bin2 := []byte("#!/bin/sh\nkill -HUP $$\nsleep 5\n")
	u2 := newFake(t, bin2, devicetest.SHA256(bin2), nil)
	r2, err := u2.d.Bootctl([]string{"status"})
	if err != nil || r2.RC != -1 || r2.Signal != "hangup" {
		t.Fatalf("%+v %v", r2, err)
	}
}
