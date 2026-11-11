package flowio

// R-6 (MAINLINE-FLOW-REVIEW): Poll read end.json (not there yet), then saw
// the supervisor gone, and took the end as Lost although the supervisor had
// written end.json between the two reads. The test makes that order exact:
// supervisor.pid is a FIFO, so alive() blocks in its read until the test
// has written end.json; then it reads a pid with no process (not alive).

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"uc-pxx-installer/internal/flow"
)

func TestPollEndWrittenBetweenReads(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	proc := filepath.Join(t.TempDir(), "proc") // no process in it
	if err := os.MkdirAll(proc, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, FlowDir, "plan-1-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "supervisor.pid")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	wrote := make(chan error, 1)
	go func() {
		f, err := os.OpenFile(fifo, os.O_WRONLY, 0) // blocks until alive() opens it
		if err != nil {
			wrote <- err
			return
		}
		// the first end() of Poll is done: the supervisor writes end.json,
		// then alive() reads its pid (a pid with no process) and EOF
		err = os.WriteFile(filepath.Join(dir, "end.json"), []byte(`{"exited":true,"code":1}`+"\n"), 0o644)
		f.Write([]byte("999999999\n"))
		f.Close()
		wrote <- err
	}()
	r := New(Config{Root: root, Proc: proc, Now: time.Now}, flow.New("test", true))
	r.child = &child{kind: "plan", dir: dir}
	done := make(chan struct{})
	go func() { r.Poll(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Poll did not return")
	}
	if err := <-wrote; err != nil {
		t.Fatal(err)
	}
	log := strings.Join(r.log, "\n")
	if !strings.Contains(log, "plan child ended: exit 1;") || strings.Contains(log, "not recorded") {
		t.Fatalf("the end written between the two reads was not seen:\n%s", log)
	}
	if r.child != nil {
		t.Fatalf("the child is still running")
	}
}

// TestPollLostWithoutEnd: a supervisor that is gone and left no end.json
// is still Lost (the fix reads end.json once more, nothing else).
func TestPollLostWithoutEnd(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	proc := filepath.Join(t.TempDir(), "proc")
	os.MkdirAll(proc, 0o755)
	dir := filepath.Join(root, FlowDir, "plan-1-1")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "supervisor.pid"), []byte("999999999\n"), 0o644)
	r := New(Config{Root: root, Proc: proc, Now: time.Now}, flow.New("test", true))
	r.child = &child{kind: "plan", dir: dir}
	r.Poll()
	if log := strings.Join(r.log, "\n"); !strings.Contains(log, "plan child ended: the end of the install was not recorded;") {
		t.Fatalf("no Lost end:\n%s", log)
	}
}
