//go:build install && step2 && devtest

package step2_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uc-pxx-installer/internal/step2"
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
