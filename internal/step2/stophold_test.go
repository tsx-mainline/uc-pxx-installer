//go:build install && step2 && devtest

package step2_test

// The durable STOP hold (STEP2-STOP-HOLD-2026-11-11.md §4, items 1-5): the
// service records a finish STOP with `tsx-autoupdate stop-hold set step2
// REASON` (fixed argv, PATH, LC_ALL), never writes the restart block itself
// after its exit 0 and never sets again in that boot; on any other end it
// keeps the "hold 4" fallback and tries again until the first exit 0; it
// never clears. Quiet refuses while the hold folder has an entry or cannot
// be read (review MAJOR-1).

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/step2"
	"uc-pxx-installer/internal/step2ui"
)

func holdEnv(t *testing.T) (*device.Env, string) {
	t.Helper()
	root := t.TempDir()
	e, err := device.NewForTest(device.TestOptions{Root: root, RegularFiles: true, Layout: small, Manifests: fx.Manifests})
	if err != nil {
		t.Fatal(err)
	}
	return e, root
}

// fakeTool puts a tsx-autoupdate into the root that logs its argv and
// environment and exits with the codes given, one per call (the last again).
func fakeTool(t *testing.T, root string, codes ...string) string {
	t.Helper()
	log := filepath.Join(root, "calls.log")
	n := filepath.Join(root, "n")
	p := filepath.Join(root, strings.TrimPrefix(step2.AutoupdateTool, "/"))
	os.MkdirAll(filepath.Dir(p), 0o755)
	q := func(s string) string { return "'" + s + "'" } // the test folders have spaces and parentheses
	script := "#!/bin/sh\necho \"argv[$#]: $*|PATH=$PATH|LC_ALL=$LC_ALL\" >> " + q(log) + "\n" +
		"i=$(cat " + q(n) + " 2>/dev/null || echo 0); echo $((i + 1)) > " + q(n) + "\n" +
		"set -- " + strings.Join(codes, " ") + "\nshift $i 2>/dev/null || true\n" +
		"c=$1; [ -n \"$c\" ] || c=" + codes[len(codes)-1] + "\nexit $c\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return log
}

func calls(log string) []string {
	b, _ := os.ReadFile(log)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestHoldsClear(t *testing.T) {
	e, root := holdEnv(t)
	d := filepath.Join(root, strings.TrimPrefix(step2.HoldsDir, "/"))
	if err := step2.HoldsClear(e); err == nil {
		t.Fatal("a missing hold folder passed (it must fail closed)")
	}
	os.MkdirAll(d, 0o755)
	if err := step2.HoldsClear(e); err != nil {
		t.Fatalf("an empty hold folder: %v", err)
	}
	os.WriteFile(filepath.Join(d, ".tmp"), nil, 0o644)
	if err := step2.HoldsClear(e); err != nil {
		t.Fatalf("a dot file only: %v", err)
	}
	os.WriteFile(filepath.Join(d, "step2"), []byte("STOP\n"), 0o644)
	if err := step2.HoldsClear(e); err == nil || !strings.Contains(err.Error(), "step2") {
		t.Fatalf("a step2 hold passed: %v", err)
	}
	os.Remove(filepath.Join(d, "step2"))
	os.WriteFile(filepath.Join(d, "other"), nil, 0o644)
	if err := step2.HoldsClear(e); err == nil {
		t.Fatal("another owner's hold passed")
	}
	os.Remove(filepath.Join(d, "other"))
	if os.Getuid() != 0 {
		os.Chmod(d, 0)
		if err := step2.HoldsClear(e); err == nil {
			t.Fatal("an unreadable hold folder passed")
		}
		os.Chmod(d, 0o755)
	}
	os.RemoveAll(d)
	os.MkdirAll(filepath.Dir(d), 0o755)
	os.WriteFile(d, nil, 0o644) // a file in place of the folder
	if err := step2.HoldsClear(e); err == nil {
		t.Fatal("a file in place of the hold folder passed")
	}
	// LiveChecks' Quiet refuses with the hold before anything else
	os.Remove(d)
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, "step2"), nil, 0o644)
	if err := step2.LiveChecks(e).Quiet(); err == nil || !strings.Contains(err.Error(), "hold") {
		t.Fatalf("Quiet with a hold: %v", err)
	}
}

func block(root string) string {
	b, err := os.ReadFile(filepath.Join(root, strings.TrimPrefix(step2.RestartBlock, "/")))
	if err != nil {
		return "none"
	}
	return strings.TrimSpace(string(b))
}

type holdPanel struct{ lines [][]string }

func (p *holdPanel) Show(l []string)          { p.lines = append(p.lines, l) }
func (p *holdPanel) Keys() <-chan step2ui.Key { return nil }
func (p *holdPanel) Close(bool)               {}

func TestStopHoldDurable(t *testing.T) {
	stop := errors.New("the check after step 2: LBA 1024 changed\nsecond line")
	run := func(t *testing.T, codes []string, turns int) (string, *holdPanel, string, []string) {
		e, root := holdEnv(t)
		log := fakeTool(t, root, codes...)
		live := step2.LiveChecks(e)
		live.LockAutoupdate = func() (func(), error) { return func() {}, nil }
		pn := &holdPanel{}
		n := 0
		se := step2.ServiceEnv{Panel: func() (step2.Panel, error) { return pn, nil },
			Turn: func() bool { n++; return n <= turns }}
		if _, err := step2.StopHoldForTest(e, step2.Options{Live: live}, se, stop); err != stop {
			t.Fatalf("the STOP's error %v", err)
		}
		return root, pn, block(root), calls(log)
	}
	t.Run("exit 0: recorded, no block of its own, set once", func(t *testing.T) {
		root, pn, b, c := run(t, []string{"0"}, 5)
		if len(c) != 1 || c[0] != "argv[4]: stop-hold set step2 STOP after step 2: the check after step 2: LBA 1024 changed|PATH=/usr/sbin:/usr/bin:/sbin:/bin|LC_ALL=C" {
			t.Fatalf("calls %q", c)
		}
		if b != "none" {
			t.Fatalf("step 2 wrote the restart block %q after exit 0", b)
		}
		if len(pn.lines) != 1 || strings.Contains(strings.Join(pn.lines[0], " "), "could not be recorded") {
			t.Fatalf("panel %q", pn.lines)
		}
		if _, err := os.Stat(filepath.Join(root, strings.TrimPrefix(step2.StopHoldSetMarker, "/"))); err != nil {
			t.Fatal("no marker of the set in this boot")
		}
	})
	t.Run("exit 2 (an older tsx-autoupdate): the fallback, set again until exit 0", func(t *testing.T) {
		_, pn, b, c := run(t, []string{"2", "1", "0"}, 6)
		if len(c) != 3 {
			t.Fatalf("calls %q (want 3: 2, 1, then 0 and no more)", c)
		}
		if b != "hold 4" {
			t.Fatalf("restart block %q", b)
		}
		if len(pn.lines) != 2 || !strings.Contains(strings.Join(pn.lines[0], " "), "could not be recorded") ||
			strings.Contains(strings.Join(pn.lines[1], " "), "could not be recorded") {
			t.Fatalf("panel %q", pn.lines)
		}
	})
	t.Run("never recorded: the block every turn, set every turn", func(t *testing.T) {
		_, _, b, c := run(t, []string{"2"}, 4)
		if len(c) != 5 || b != "hold 4" {
			t.Fatalf("calls %d, block %q", len(c), b)
		}
	})
	t.Run("set once in this boot already: never again", func(t *testing.T) {
		e, root := holdEnv(t)
		log := fakeTool(t, root, "0")
		m := filepath.Join(root, strings.TrimPrefix(step2.StopHoldSetMarker, "/"))
		os.MkdirAll(filepath.Dir(m), 0o755)
		os.WriteFile(m, []byte("set\n"), 0o644)
		live := step2.LiveChecks(e)
		live.LockAutoupdate = func() (func(), error) { return func() {}, nil }
		step2.StopHoldForTest(e, step2.Options{Live: live}, step2.ServiceEnv{Turn: func() bool { return false }}, stop)
		if _, err := os.Stat(log); err == nil {
			t.Fatalf("set ran again: %q", calls(log))
		}
	})
	t.Run("never clear", func(t *testing.T) {
		_, _, _, c := run(t, []string{"2", "0"}, 5)
		for _, l := range c {
			if strings.Contains(l, "clear") {
				t.Fatalf("a clear: %q", l)
			}
		}
	})
}
