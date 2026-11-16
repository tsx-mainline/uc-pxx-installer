//go:build install && step2

package step2

import (
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestThawGuardLoop: the helper's rule (clone review MINOR-2): "thawed"
// after "frozen" does nothing; the pipe ending without "thawed" (the parent
// went) thaws; the bound thaws; a root that is not frozen is no error; a
// thaw that keeps failing is guardFailed.
func TestThawGuardLoop(t *testing.T) {
	run := func(lines string, end bool, thaw func() error) (int, int32) {
		var n atomic.Int32
		r, w := io.Pipe()
		go func() {
			io.WriteString(w, lines)
			if end {
				w.Close()
			}
		}()
		code := thawGuard(r, io.Discard, 100*time.Millisecond, func() error { n.Add(1); return thaw() })
		w.Close()
		return code, n.Load()
	}
	ok := func() error { return nil }
	if c, n := run("frozen\nthawed\n", false, ok); c != guardIdle || n != 0 {
		t.Fatalf("thawed: %d %d", c, n)
	}
	if c, n := run("frozen\n", true, ok); c != guardOrphan || n != 1 {
		t.Fatalf("the parent went: %d %d", c, n)
	}
	if c, n := run("frozen\n", false, ok); c != guardTimeout || n != 1 {
		t.Fatalf("the bound: %d %d", c, n)
	}
	if c, n := run("", true, func() error { return errNotFrozen }); c != guardIdle || n != 1 {
		t.Fatalf("the parent went before a freeze: %d %d", c, n)
	}
	if c, n := run("frozen\n", true, func() error { return errors.New("EBUSY") }); c != guardFailed || n != 10 {
		t.Fatalf("failing thaws: %d %d", c, n)
	}
}

// TestThawGuardHelper is the helper process of TestThawGuardProcess (this
// test binary started again): its thaw writes a marker file.
func TestThawGuardHelper(t *testing.T) {
	a := flag.Args()
	if len(a) != 2 || a[0] != "thaw-helper" {
		t.Skip("only as the helper process")
	}
	ctl, ready := os.NewFile(3, "control"), os.NewFile(4, "ready")
	os.Exit(thawGuard(ctl, ready, 300*time.Millisecond, func() error { return os.WriteFile(a[1], []byte("thawed"), 0o644) }))
}

// TestThawGuardProcess: the parent's side with a real process: the ready
// line; "thawed": the helper does nothing; the pipe ends (as at the parent's
// death): the helper thaws by itself (exit 3); "frozen" and silence: the
// bound (exit 4).
func TestThawGuardProcess(t *testing.T) {
	old := thawGuardCommand
	defer func() { thawGuardCommand = old }()
	for _, c := range []struct {
		name   string
		frozen bool
		thawed bool
		wait   time.Duration
		code   int
		marker bool
	}{
		{"the parent thawed", true, true, 0, guardIdle, false},
		{"the parent went", true, false, 0, guardOrphan, true},
		{"the bound", true, false, time.Second, guardTimeout, true},
	} {
		marker := filepath.Join(t.TempDir(), "thawed")
		thawGuardCommand = func() (*exec.Cmd, error) {
			return exec.Command(os.Args[0], "-test.run=^TestThawGuardHelper$", "--", "thaw-helper", marker), nil
		}
		g, err := startThawGuard()
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if c.frozen {
			g.frozen()
		}
		time.Sleep(c.wait)
		code := g.end(c.thawed)
		_, merr := os.Stat(marker)
		if code != c.code || (merr == nil) != c.marker {
			t.Fatalf("%s: exit %d, marker %v", c.name, code, merr)
		}
	}
	thawGuardCommand = func() (*exec.Cmd, error) { return exec.Command("/bin/true"), nil }
	if _, err := startThawGuard(); err == nil || !strings.Contains(err.Error(), "did not start") {
		t.Fatalf("a helper without the ready line: %v", err)
	}
}
