package flowio

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"uc-pxx-installer/internal/flow"
	"uc-pxx-installer/internal/status"
)

// lockHolderMain is the helper process of TestUILockCrash: it takes the UI
// lock, starts a child the way the UI starts the device mode (the real
// startSupervised and the real supervisor), says so, and sleeps until it
// is killed.
func lockHolderMain() {
	root, exe := os.Getenv("P10INST_FLOWIO_LOCKROOT"), os.Getenv("P10INST_FLOWIO_LOCKEXE")
	l, _, err := TakeUILock(root, "holder-tty")
	if err != nil {
		fmt.Println("lock:", err)
		os.Exit(2)
	}
	defer l.Close()
	c := &Config{Root: root, Exe: exe}
	dir := filepath.Join(root, FlowDir, "plan-1-1")
	if _, err := startSupervised(c, "plan", dir, []string{exe, "plan"}); err != nil {
		fmt.Println("child:", err)
		os.Exit(2)
	}
	for i := 0; i < 200; i++ {
		if b, err := os.ReadFile(filepath.Join(dir, "child.pid")); err == nil {
			fmt.Printf("locked %s\n", strings.TrimSpace(string(b)))
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(time.Hour)
}

func init() {
	if os.Getenv("P10INST_FLOWIO_LOCKHOLDER") == "1" {
		lockHolderMain()
		os.Exit(0)
	}
}

func lockRoot(t *testing.T) string {
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(filepath.Join(root, "run"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestUILock: one UI at a time; the second learns who has the lock; the
// lock is free again after Close; old bytes in the file block nothing.
func TestUILock(t *testing.T) {
	root := lockRoot(t)
	path := filepath.Join(root, UILockFile)
	os.MkdirAll(filepath.Dir(path), 0o755)
	// the bytes of a UI that died long ago: no lock
	os.WriteFile(path, []byte("pid=1\ntty=/dev/tty1\nsince=2026-01-01T00:00:00Z\n"), 0o644)
	l1, _, err := TakeUILock(root, "/dev/tty1")
	if err != nil {
		t.Fatalf("first UI: %v", err)
	}
	l2, h, err := TakeUILock(root, "/dev/ttyGS0")
	if !errors.Is(err, ErrUIBusy) || l2 != nil {
		t.Fatalf("second UI: %v %v", l2, err)
	}
	if h.PID != os.Getpid() || h.TTY != "/dev/tty1" || h.Since == "" || !strings.Contains(h.String(), "/dev/tty1") {
		t.Fatalf("holder %+v", h)
	}
	if err := l1.Close(); err != nil {
		t.Fatal(err)
	}
	l3, _, err := TakeUILock(root, "/dev/ttyGS0")
	if err != nil {
		t.Fatalf("after Close: %v", err)
	}
	defer l3.Close()
	if h := ReadUIHolder(path); h.TTY != "/dev/ttyGS0" {
		t.Fatalf("holder %+v", h)
	}
	// a lock file outside the run folder, or a symlink, is refused
	if _, err := takeUILock(filepath.Join(root, "etc"), filepath.Join(root, "etc", "x.lock"), "t", 1, time.Now()); err == nil {
		t.Fatal("a lock outside /run")
	}
	ln := filepath.Join(root, RunDir, "flow", "link.lock")
	os.Symlink(filepath.Join(root, "target"), ln)
	if _, err := takeUILock(filepath.Join(root, RunDir), ln, "t", 1, time.Now()); err == nil {
		t.Fatal("a symlink as the lock file")
	}
	if _, err := os.Lstat(filepath.Join(root, "target")); err == nil {
		t.Fatal("the symlink was followed")
	}
}

// TestUILockCrash: the UI is SIGKILLed while its supervised child runs on
// (the install goes on after a UI crash). The lock must not stay behind:
// not stale in the file, and not held by the supervisor or the child (they
// must not inherit the descriptor). A new UI takes it at once.
func TestUILockCrash(t *testing.T) {
	root := lockRoot(t)
	dir := t.TempDir()
	self, _ := os.Executable()
	exe := filepath.Join(dir, "uc-pxx-inst")
	script := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\nflow-supervise) exec env P10INST_FLOWIO_LOCKHOLDER= P10INST_FLOWIO_SUPERVISOR=1 %q \"$@\" ;;\nplan) exec sleep 60 ;;\nesac\nexit 9\n", self)
	os.WriteFile(exe, []byte(script), 0o755)
	cmd := exec.Command(self)
	cmd.Env = append(os.Environ(), "P10INST_FLOWIO_LOCKHOLDER=1", "P10INST_FLOWIO_LOCKROOT="+root, "P10INST_FLOWIO_LOCKEXE="+exe)
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, _ := bufio.NewReader(out).ReadString('\n')
	if !strings.HasPrefix(line, "locked ") {
		cmd.Process.Kill()
		t.Fatalf("holder: %q", line)
	}
	childPID, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "locked ")))
	// at the end: stop the child and wait for its supervisor to write
	// end.json and exit, before the test folder is removed
	defer func() {
		syscall.Kill(childPID, syscall.SIGKILL)
		end := filepath.Join(root, FlowDir, "plan-1-1", "end.json")
		for i := 0; i < 200; i++ {
			if _, err := os.Stat(end); err == nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if b, err := os.ReadFile(filepath.Join(root, FlowDir, "plan-1-1", "supervisor.pid")); err == nil {
			if pid, _ := strconv.Atoi(strings.TrimSpace(string(b))); pid > 1 {
				for i := 0; i < 200 && syscall.Kill(pid, 0) == nil; i++ {
					time.Sleep(50 * time.Millisecond)
				}
			}
		}
	}()
	if _, h, err := TakeUILock(root, "/dev/tty1"); !errors.Is(err, ErrUIBusy) || h.PID != cmd.Process.Pid || h.TTY != "holder-tty" {
		t.Fatalf("while the holder runs: %v %+v", err, h)
	}
	cmd.Process.Signal(syscall.SIGKILL)
	cmd.Wait()
	if err := syscall.Kill(childPID, 0); err != nil {
		t.Fatalf("the child does not run on after the UI died: %v", err)
	}
	l, _, err := TakeUILock(root, "/dev/tty1")
	if err != nil {
		t.Fatalf("a new UI after the crash: %v (the supervisor or the child holds the lock, or it is stale)", err)
	}
	l.Close()
}

// TestWaitUILock: the screen UI waits for the other one, is told who it
// is, and takes over when it ends.
func TestWaitUILock(t *testing.T) {
	root := lockRoot(t)
	l1, _, err := TakeUILock(root, "/dev/ttyGS0")
	if err != nil {
		t.Fatal(err)
	}
	seen := make(chan Holder, 4)
	got := make(chan *UILock, 1)
	go func() {
		l, err := WaitUILock(root, "/dev/tty1", 20*time.Millisecond, func(h Holder) { seen <- h })
		if err != nil {
			t.Error(err)
		}
		got <- l
	}()
	select {
	case h := <-seen:
		if h.TTY != "/dev/ttyGS0" {
			t.Fatalf("holder %+v", h)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no waiting report")
	}
	select {
	case <-got:
		t.Fatal("took the lock while the other UI has it")
	case <-time.After(200 * time.Millisecond):
	}
	l1.Close()
	select {
	case l := <-got:
		if h := ReadUIHolder(filepath.Join(root, UILockFile)); h.TTY != "/dev/tty1" {
			t.Fatalf("holder %+v", h)
		}
		l.Close()
	case <-time.After(5 * time.Second):
		t.Fatal("did not take over")
	}
	if len(seen) != 0 {
		t.Fatalf("reported %d more times for the same holder", len(seen))
	}
}

// TestUILockReplaced: the path is replaced between the open and the flock
// (the hook of the tests): the first try sees that the locked file is not
// the file at the path and tries again; the lock held is the one of the
// file at the path, so a second UI is refused.
func TestUILockReplaced(t *testing.T) {
	root := lockRoot(t)
	path := filepath.Join(root, UILockFile)
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, nil, 0o644)
	n := 0
	lockTestHook = func(p string) {
		n++
		if n == 1 {
			tmp := p + ".new"
			os.WriteFile(tmp, nil, 0o644)
			os.Rename(tmp, p)
		}
	}
	defer func() { lockTestHook = nil }()
	l, _, err := TakeUILock(root, "/dev/tty1")
	if err != nil || n != 2 {
		t.Fatalf("take: %v after %d tries", err, n)
	}
	st1, _ := l.f.Stat()
	st2, _ := os.Stat(path)
	if !os.SameFile(st1, st2) {
		t.Fatal("the lock is on a file that is not at the path")
	}
	lockTestHook = nil
	if _, _, err := TakeUILock(root, "/dev/ttyGS0"); !errors.Is(err, ErrUIBusy) {
		t.Fatalf("a second UI: %v", err)
	}
	l.Close()
	// without the check, the first try would hold the lock of the old file
	lockTestHook = func(p string) {
		tmp := p + ".new"
		os.WriteFile(tmp, nil, 0o644)
		os.Rename(tmp, p)
	}
	if _, err := takeUILock(filepath.Join(root, RunDir), path, "t", 1, time.Now()); !errors.Is(err, errReplaced) {
		t.Fatalf("a replaced file: %v", err)
	}
}

// TestRemoteIn: the remote input of a take-over terminal: keys only while
// it holds the input lock and its heartbeat is fresh; old lines are not
// played again; one take-over terminal at a time.
func TestRemoteIn(t *testing.T) {
	root := lockRoot(t)
	AppendRemote(root, "key 3 1") // before this flow UI started: never played
	rin := NewRemoteIn(root)
	now := time.Now()
	if a, _, k := rin.Poll(now); a || k != nil {
		t.Fatalf("no take-over terminal: %v %v", a, k)
	}
	il, _, err := TakeInputLock(root, "/dev/ttyGS0")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := TakeInputLock(root, "/dev/ttyGS1"); !errors.Is(err, ErrUIBusy) {
		t.Fatalf("a second take-over terminal: %v", err)
	}
	for _, l := range []string{"beat", "key 1 1", "key 3 1", "key 3 0", "key 99 1", "junk", "key 5 1"} {
		if err := AppendRemote(root, l); err != nil {
			t.Fatal(err)
		}
	}
	a, h, keys := rin.Poll(time.Now())
	want := []RemoteKey{{flow.KeyPrev, true}, {flow.KeyOK, true}, {flow.KeyOK, false}, {flow.KeyYes, true}}
	if !a || h.TTY != "/dev/ttyGS0" || len(keys) != len(want) {
		t.Fatalf("active %v %+v keys %+v", a, h, keys)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("key %d: %+v, want %+v", i, keys[i], want[i])
		}
	}
	if _, _, k := rin.Poll(time.Now()); len(k) != 0 {
		t.Fatalf("keys played twice: %+v", k)
	}
	// the heartbeat is old: not active, its keys are dropped
	AppendRemote(root, "key 2 1")
	if a, _, k := rin.Poll(time.Now().Add(RemoteFresh + time.Second)); a || k != nil {
		t.Fatalf("stale: %v %v", a, k)
	}
	// the take-over terminal left (the lock is free): not active
	il.Close()
	AppendRemote(root, "key 2 1")
	if a, _, k := rin.Poll(time.Now()); a || k != nil {
		t.Fatalf("left: %v %v", a, k)
	}
	if err := AppendRemote(root, "two\nlines"); err == nil {
		t.Fatal("a line with a newline")
	}
	if err := appendRemote(filepath.Join(root, "etc"), filepath.Join(root, "etc", "x"), "beat"); err == nil {
		t.Fatal("a file outside /run")
	}
}

// TestPublish: the frame of the flow UI goes to the other terminals as it
// is (not laid out).
func TestPublish(t *testing.T) {
	root := lockRoot(t)
	if _, err := ReadPublished(root); err == nil {
		t.Fatal("nothing published yet")
	}
	f := flow.Frame{Title: "t", Body: []status.Line{{Level: status.Bad, Text: "!!! row"}}, Footer: "keys", Msg: "m", LED: "red", Squeeze: true}
	at := time.Now().UTC().Truncate(time.Second)
	if err := Publish(root, Published{PID: 7, TTY: "/dev/tty1", At: at, Name: "hold", Frame: f}); err != nil {
		t.Fatal(err)
	}
	p, err := ReadPublished(root)
	if err != nil || p.PID != 7 || p.TTY != "/dev/tty1" || !p.At.Equal(at) || p.Name != "hold" || p.Frame.Title != "t" ||
		p.Frame.Body[0] != f.Body[0] || p.Frame.LED != "red" || !p.Frame.Squeeze {
		t.Fatalf("%+v %v", p, err)
	}
}
