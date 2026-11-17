//go:build install && step2 && devtest

package step2

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeProgs stands for the programs and the procfs of a livePanel (RC-10c
// review MINOR-2): it records each program; the kiosk stop waits on block
// (when set), as a hung rc-service would.
type fakeProgs struct {
	mu    sync.Mutex
	calls []string
	block chan struct{}
	comp  atomic.Bool
}

func (f *fakeProgs) run(limit time.Duration, name string, args ...string) error {
	f.mu.Lock()
	f.calls = append(f.calls, filepath.Base(name)+" "+strings.Join(args, " "))
	b := f.block
	f.mu.Unlock()
	if b != nil && name == rcService && len(args) == 2 && args[1] == "stop" {
		<-b
	}
	return nil
}

func (f *fakeProgs) list() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeProgs) count(call string) int {
	n := 0
	for _, c := range f.list() {
		if c == call {
			n++
		}
	}
	return n
}

// testPanel is a livePanel on a plain file (no tty, no keys) with f's
// programs and short times.
func testPanel(t *testing.T, f *fakeProgs) (*livePanel, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tty")
	tty, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	p := newLivePanel(tty, nil)
	p.run, p.compositor = f.run, f.comp.Load
	p.gone, p.every = 300*time.Millisecond, 50*time.Millisecond
	p.checked = time.Now().Add(-time.Hour)
	return p, path
}

func waitReclaim(t *testing.T, p *livePanel) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		p.mu.Lock()
		r := p.reclaim
		p.mu.Unlock()
		if r == nil {
			return
		}
	}
	t.Fatal("the reclaim did not end")
}

const (
	stopCall  = "rc-service kiosk stop"
	startCall = "rc-service kiosk start"
	chvtCall  = "chvt 1"
)

// TestPanelShowNeverWaits: a reclaim whose kiosk stop hangs does not hold
// Show; while it runs no second one starts; a compositor that stays means no
// chvt (it would wait for the switch).
func TestPanelShowNeverWaits(t *testing.T) {
	f := &fakeProgs{block: make(chan struct{})}
	f.comp.Store(true)
	p, path := testPanel(t, f)
	t0 := time.Now()
	p.Show([]string{"first"})
	time.Sleep(100 * time.Millisecond)
	p.Show([]string{"second"})
	if d := time.Since(t0); d > time.Second {
		t.Fatalf("Show waited %v for the reclaim", d)
	}
	if n := f.count(stopCall); n != 1 {
		t.Fatalf("%d kiosk stops while one hangs, want 1 (%q)", n, f.list())
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "second") {
		t.Fatalf("the screen %q", b)
	}
	close(f.block)
	waitReclaim(t, p)
	if f.count(chvtCall) != 0 {
		t.Fatalf("chvt with a compositor still there: %q", f.list())
	}
	p.Close(false)
}

// TestPanelReclaimRate: at most one reclaim per p.every, at most p.max in
// all.
func TestPanelReclaimRate(t *testing.T) {
	f := &fakeProgs{}
	f.comp.Store(true)
	p, _ := testPanel(t, f)
	p.every = time.Hour
	p.Show([]string{"a"})
	waitReclaim(t, p)
	for i := 0; i < 20; i++ {
		p.Show([]string{"a"})
		time.Sleep(5 * time.Millisecond)
	}
	if n := f.count(stopCall); n != 1 {
		t.Fatalf("%d reclaims within p.every, want 1", n)
	}
	p.every, p.gone = 10*time.Millisecond, 20*time.Millisecond
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		p.Show([]string{"a"})
	}
	waitReclaim(t, p)
	if n := f.count(stopCall); n != p.max || p.max != maxReclaims {
		t.Fatalf("%d reclaims, want %d (max %d)", n, maxReclaims, p.max)
	}
	p.Close(false)
}

// TestPanelTakeScreen: with the compositor gone, the kiosk stop, chvt 1 and
// the text (cursor hidden, the screen cleared); with one still there, no
// chvt and nothing written.
func TestPanelTakeScreen(t *testing.T) {
	f := &fakeProgs{}
	p, path := testPanel(t, f)
	p.takeScreen()
	if got := strings.Join(f.list(), ","); got != stopCall+","+chvtCall {
		t.Fatalf("programs %q", got)
	}
	if b, _ := os.ReadFile(path); string(b) != "\033[?25l\033[H\033[2J" {
		t.Fatalf("the screen %q", b)
	}
	f2 := &fakeProgs{}
	f2.comp.Store(true)
	p2, path2 := testPanel(t, f2)
	p2.takeScreen()
	if got := strings.Join(f2.list(), ","); got != stopCall {
		t.Fatalf("programs %q", got)
	}
	if b, _ := os.ReadFile(path2); len(b) != 0 {
		t.Fatalf("the screen %q", b)
	}
}

// TestPanelCloseWaitsForReclaim: Close waits for a reclaim that runs, which
// then starts no more programs; the kiosk start is the last program.
func TestPanelCloseWaitsForReclaim(t *testing.T) {
	f := &fakeProgs{block: make(chan struct{})}
	f.comp.Store(true)
	p, _ := testPanel(t, f)
	p.Show([]string{"a"})
	time.Sleep(50 * time.Millisecond)
	go func() {
		time.Sleep(200 * time.Millisecond)
		close(f.block)
	}()
	t0 := time.Now()
	p.Close(true)
	if time.Since(t0) < 150*time.Millisecond {
		t.Fatal("Close did not wait for the reclaim")
	}
	if got := strings.Join(f.list(), ","); got != stopCall+","+startCall {
		t.Fatalf("programs %q", got)
	}
	p.Show([]string{"after"})
	p.Draw([]string{"after"})
	if got := strings.Join(f.list(), ","); got != stopCall+","+startCall {
		t.Fatalf("programs after Close %q", got)
	}
}

// TestRunFor: a program of the panel is killed at its deadline.
func TestRunFor(t *testing.T) {
	t0 := time.Now()
	if err := runFor(500*time.Millisecond, "/bin/sh", "-c", "sleep 30"); err == nil || time.Since(t0) > 10*time.Second {
		t.Fatalf("%v after %v", err, time.Since(t0))
	}
	if err := runFor(5*time.Second, "/bin/sh", "-c", "exit 0"); err != nil {
		t.Fatal(err)
	}
}
