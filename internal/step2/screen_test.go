//go:build install && step2 && devtest

package step2_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"uc-pxx-installer/internal/plan"
	"uc-pxx-installer/internal/step2"
	"uc-pxx-installer/internal/step2ui"
	"uc-pxx-installer/internal/xfer/handover"
)

// TestFrame (R31): a frame draws over the old one: cursor home, each line with
// the rest of its row cleared, the rest of the screen cleared; never a clear
// of the whole screen first (that flashed on the panel 5 times a second).
func TestFrame(t *testing.T) {
	f := step2.Frame([]string{"abcdef", "", "x"}, 4)
	want := "\033[H" + "abcd\033[K\r\n" + "ef\033[K\r\n" + "\033[K\r\n" + "x\033[K\r\n" + "\033[J"
	if f != want {
		t.Fatalf("%q, want %q", f, want)
	}
	if strings.Contains(f, "\033[2J") {
		t.Fatal("a clear of the whole screen")
	}
	if step2.Frame([]string{"a"}, 0) != "\033[Ha\033[K\r\n\033[J" {
		t.Fatalf("%q", step2.Frame([]string{"a"}, 0))
	}
}

// TestCompositorRunning: the kiosk's compositor by /proc/N/comm.
func TestCompositorRunning(t *testing.T) {
	proc := t.TempDir()
	put := func(pid, comm string) {
		os.MkdirAll(filepath.Join(proc, pid), 0o755)
		os.WriteFile(filepath.Join(proc, pid, "comm"), []byte(comm+"\n"), 0o644)
	}
	put("1", "init")
	put("self", "sway")
	put("40", "swaybg")
	put("41", "chromium")
	if step2.CompositorRunning(proc) {
		t.Fatal("no compositor runs (swaybg, chromium, a non-number folder)")
	}
	for _, c := range []string{"sway", "cage", "kiosk-session"} {
		put("77", c)
		if !step2.CompositorRunning(proc) {
			t.Fatalf("%s not seen", c)
		}
	}
	if step2.CompositorRunning(filepath.Join(proc, "none")) {
		t.Fatal("no procfs: none runs")
	}
}

type drawPanel struct {
	shows, draws [][]string
}

func (p *drawPanel) Show(l []string)          { p.shows = append(p.shows, l) }
func (p *drawPanel) Draw(l []string)          { p.draws = append(p.draws, l) }
func (p *drawPanel) Keys() <-chan step2ui.Key { return nil }
func (p *drawPanel) Close(bool)               {}

// TestCopyProgress (R32): about once a second the panel draws the bar (Draw:
// no program runs while the root is frozen) and the info file says "started
// <id12> copy <D> of <T>"; at the end of the copy the code is "started <id12>".
func TestCopyProgress(t *testing.T) {
	e, root := holdEnv(t)
	o := step2.Options{InfoFile: "/run/uc-pxx-step2/xfer-info.json"}
	q := &plan.OtherSlotPlan{}
	q.ID, q.Target, q.Keeper = strings.Repeat("ab", 32), "a", "b"
	pn := &drawPanel{}
	now := time.Unix(1800000000, 0)
	cb := step2.CopyProgressForTest(e, o, pn, q, func() time.Time { return now })
	code := func() string {
		in, err := handover.ReadInfoFile(filepath.Join(root, strings.TrimPrefix(o.InfoFile, "/")))
		if err != nil {
			t.Fatal(err)
		}
		return in.Step2
	}
	total := int64(1792 << 20)
	cb(0, total)
	if c := code(); c != "started abababababab copy 0 of 1792" || len(pn.draws) != 1 {
		t.Fatalf("%q %d", c, len(pn.draws))
	}
	now = now.Add(500 * time.Millisecond)
	cb(100<<20, total) // within the second: nothing new
	if c := code(); c != "started abababababab copy 0 of 1792" || len(pn.draws) != 1 {
		t.Fatalf("throttle: %q %d", c, len(pn.draws))
	}
	now = now.Add(600 * time.Millisecond)
	cb(448<<20, total)
	if c := code(); c != "started abababababab copy 448 of 1792" || len(pn.draws) != 2 ||
		pn.draws[1][0] != "Copying this system into slot A: 448 of 1792 MiB" || !strings.HasPrefix(pn.draws[1][1], "[") {
		t.Fatalf("%q %q", c, pn.draws)
	}
	now = now.Add(100 * time.Millisecond)
	cb(total, total) // the end of the copy is never throttled
	if c := code(); c != "started abababababab" || len(pn.draws) != 3 || !strings.Contains(pn.draws[2][0], "is copied") {
		t.Fatalf("end: %q %q", c, pn.draws)
	}
	if len(pn.shows) != 0 {
		t.Fatalf("Show while the root is frozen: %q", pn.shows)
	}
}
