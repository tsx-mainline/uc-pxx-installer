package ui

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/status"
)

// screenRows draws m and returns the rows of the screen (no escapes).
func screenRows(t *testing.T, m *Model, w, h int, now time.Time) []string {
	t.Helper()
	var b bytes.Buffer
	if err := m.Draw(&b, w, h, now); err != nil {
		t.Fatal(err)
	}
	s := b.String()
	s = strings.TrimPrefix(s, "\x1b[?25l\x1b[H")
	s = strings.TrimSuffix(s, "\x1b[0m")
	for _, c := range colors {
		s = strings.ReplaceAll(s, c, "")
	}
	rows := strings.Split(s, "\r\n")
	if len(rows) != h {
		t.Fatalf("%d rows, want %d", len(rows), h)
	}
	for i, r := range rows {
		if n := len([]rune(r)); n != w {
			t.Fatalf("row %d: %d cells, want %d: %q", i, n, w, r)
		}
		rows[i] = strings.TrimRight(r, " ")
	}
	return rows
}

// a long status report: the status page does not fit on one screen
func longReport() *status.Report {
	r := &status.Report{Model: "Crestron UC-P10", ABValid: true, Next: 0, Fallback: 1, Unlock: "unlocked"}
	r.AB.Slots = [2]avbab.Slot{{Priority: 15, Successful: 1}, {Priority: 14, Successful: 1}}
	r.Slots[0] = status.Slot{Name: "a", AB: r.AB.Slots[0], Summary: "mainline"}
	r.Slots[1] = status.Slot{Name: "b", AB: r.AB.Slots[1], Summary: "1.0.0.94 userdebug"}
	return r
}

func extraLines() []status.Line {
	return []status.Line{{Level: status.Heading, Text: "Link and keys"}, {Text: "  USB        x"}, {Text: "  Keys       touch keys ready"},
		{Text: "  Boot mode  register"}}
}

// The status UI has the flow's layout (owner feedback FLOW-FEEDBACK-R3
// item 5): the menu with its selection is the first row of the status
// page, the keys are labelled above the touch keys in the last row, and the
// message has two rows above it.
func TestFlowLayout(t *testing.T) {
	t0 := time.Unix(1000, 0)
	for _, p := range []struct {
		panel Panel
		w, h  int
	}{{PanelUCP10, 120, 37}, {PanelUCP8, 91, 28}} {
		m := &Model{Title: "title", Report: longReport(), Extra: extraLines(), Loader: &LoaderPage{OK: true, Lines: []string{"ok: x"}}, Panel: p.panel}
		for i := 0; i < 40; i++ {
			m.Extra = append(m.Extra, status.Line{Text: "  more"})
		}
		rows := screenRows(t, m, p.w, p.h, t0)
		if rows[2] != "[ STATUS ]    Log      Restart      Download mode" {
			t.Fatalf("%s: menu row %q", p.panel.Model, rows[2])
		}
		var k [KeyPlaces]string
		k[PlaceSpeaker], k[PlaceVolDown], k[PlaceVolUp], k[PlaceMic] = "More", "Previous", "Next", "Open"
		if rows[p.h-1] != p.panel.KeyLegend(k, p.w) {
			t.Fatalf("%s: footer %q", p.panel.Model, rows[p.h-1])
		}
		m.Handle(Event{KeyPrev, true}, t0) // Vol- = previous: wraps to Download mode
		rows = screenRows(t, m, p.w, p.h, t0)
		if rows[2] != "  Status      Log      Restart    [ DOWNLOAD MODE ]" {
			t.Fatalf("%s: menu row %q", p.panel.Model, rows[2])
		}
		for _, r := range rows[p.h-4:] {
			if strings.Contains(r, "[") {
				t.Fatalf("%s: the selection is in the footer: %q", p.panel.Model, rows[p.h-4:])
			}
		}
		m.Msg = strings.Repeat("a message ", 30)
		rows = screenRows(t, m, p.w, p.h, t0)
		if !strings.HasPrefix(rows[p.h-3], "a message") || !strings.HasSuffix(rows[p.h-2], " ...") {
			t.Fatalf("%s: message rows %q", p.panel.Model, rows[p.h-4:])
		}
	}
}

// Speaker shows the next rows of a status that does not fit ("More"); the
// rule above the message says which rows are on the screen. A status that
// fits has no More, and Speaker does nothing there.
func TestStatusMore(t *testing.T) {
	t0 := time.Unix(1000, 0)
	m := &Model{Title: "title", Report: longReport(), Extra: extraLines()}
	for i := 0; i < 60; i++ {
		m.Extra = append(m.Extra, status.Line{Text: fmt.Sprintf("  extra row %d", i)})
	}
	rows := screenRows(t, m, 120, 37, t0)
	if !strings.Contains(rows[37-4], " rows 1-29 of ") || !strings.Contains(rows[36], "More") {
		t.Fatalf("first page: %q %q", rows[37-4], rows[36])
	}
	m.Handle(Event{KeyBack, true}, t0)
	rows = screenRows(t, m, 120, 37, t0)
	if !strings.Contains(rows[37-4], " rows 30-58 of ") || rows[2] != "[ STATUS ]    Log      Restart" {
		t.Fatalf("second page: %q %q", rows[37-4], rows[2])
	}
	if m.Page != PageStatus || m.Sel != 0 {
		t.Fatal("More left the status page")
	}
	for i := 0; i < 5; i++ {
		m.Handle(Event{KeyBack, true}, t0)
		screenRows(t, m, 120, 37, t0)
	}
	rows = screenRows(t, m, 120, 37, t0)
	if !strings.Contains(rows[37-4], " rows ") {
		t.Fatal("no rule")
	}
	short := &Model{Title: "title"}
	rows = screenRows(t, short, 120, 37, t0)
	if strings.Contains(rows[36], "More") || strings.Contains(rows[37-4], "rows") {
		t.Fatalf("a short status: %q", rows[36])
	}
	short.Handle(Event{KeyBack, true}, t0)
	if short.Scroll != 0 || short.Page != PageStatus {
		t.Fatal("Speaker on a status that fits")
	}
}

// The log opens at its end; Vol- scrolls up (older rows), Vol+ down; Back
// and OK go back to the status page.
func TestLogScroll(t *testing.T) {
	t0 := time.Unix(1000, 0)
	m := &Model{Title: "title"}
	for i := 0; i < 100; i++ {
		m.Log = append(m.Log, fmt.Sprintf("line %03d", i))
	}
	m.Handle(Event{KeyNext, true}, t0)
	m.Handle(Event{KeyOK, true}, t0)
	m.Handle(Event{KeyOK, false}, t0)
	if m.Page != PageLog {
		t.Fatal("not the log")
	}
	rows := screenRows(t, m, 120, 37, t0)
	if rows[32] != "  line 099" || rows[2] != "Log (newest last)" {
		t.Fatalf("the end: %q %q", rows[2], rows[32])
	}
	m.Handle(Event{KeyPrev, true}, t0)
	rows = screenRows(t, m, 120, 37, t0)
	if rows[32] != "  line 098" {
		t.Fatalf("up: %q", rows[32])
	}
	for i := 0; i < 500; i++ {
		m.Handle(Event{KeyPrev, true}, t0)
	}
	rows = screenRows(t, m, 120, 37, t0)
	if rows[3] != "  line 000" {
		t.Fatalf("the top: %q", rows[3])
	}
	m.Handle(Event{KeyNext, true}, t0)
	rows = screenRows(t, m, 120, 37, t0)
	if rows[3] != "  line 001" {
		t.Fatalf("down from the top: %q", rows[3])
	}
	m.Handle(Event{KeyOK, true}, t0)
	if m.Page != PageStatus || m.Sel != 0 {
		t.Fatal("OK on the log")
	}
}

// On a page with an action the Vol keys do not move anything, and a hold
// that runs goes on (release 1.1); Back cancels it.
func TestActionPageKeys(t *testing.T) {
	t0 := time.Unix(1000, 0)
	m := &Model{Title: "title", Loader: &LoaderPage{OK: true}}
	m.Handle(Event{KeyPrev, true}, t0) // Download mode
	m.Handle(Event{KeyOK, true}, t0)   // opens the page and starts the hold
	if m.Page != PageLoader {
		t.Fatal("not the page")
	}
	m.Handle(Event{KeyNext, true}, t0.Add(time.Second))
	m.Handle(Event{KeyPrev, true}, t0.Add(time.Second))
	if m.Page != PageLoader || m.Sel != int(PageLoader) {
		t.Fatal("a Vol key moved")
	}
	rows := screenRows(t, m, 120, 37, t0.Add(1500*time.Millisecond))
	if !strings.Contains(strings.Join(rows, "\n"), "Keep holding OK: restart into the download mode in 2 s") {
		t.Fatal("the hold stopped")
	}
	var k [KeyPlaces]string
	k[PlaceSpeaker], k[PlaceMic] = "Back", "Hold: download"
	if rows[36] != PanelUCP10.KeyLegend(k, 120) || !strings.Contains(rows[36], "Hold:download") {
		t.Fatalf("footer %q", rows[36])
	}
	m.Tick(t0.Add(3 * time.Second))
	if !m.RestartLoader || m.Restart {
		t.Fatal("the 3 s hold")
	}
	m = &Model{Title: "title", Loader: &LoaderPage{OK: true}}
	m.Handle(Event{KeyPrev, true}, t0)
	m.Handle(Event{KeyOK, true}, t0)
	m.Handle(Event{KeyBack, true}, t0.Add(time.Second))
	m.Tick(t0.Add(5 * time.Second))
	if m.RestartLoader || m.Page != PageStatus || m.Sel != 0 {
		t.Fatal("Back during the hold")
	}
	// the Restart page: Back and the hold only
	m = &Model{Title: "title", Sel: int(PageRestart)}
	m.Handle(Event{KeyOK, true}, t0)
	m.Handle(Event{KeyOK, false}, t0)
	rows = screenRows(t, m, 91, 28, t0)
	k = [KeyPlaces]string{}
	k[PlaceSpeaker], k[PlaceMic] = "Back", "Hold: restart"
	if rows[27] != PanelUCP10.KeyLegend(k, 91) {
		t.Fatalf("restart footer %q", rows[27])
	}
	// a refused download page: no OK label
	m = &Model{Title: "title", Loader: &LoaderPage{OK: false, Lines: []string{"refused: x"}}, Sel: int(PageLoader)}
	m.Handle(Event{KeyOK, true}, t0)
	rows = screenRows(t, m, 120, 37, t0)
	k = [KeyPlaces]string{}
	k[PlaceSpeaker] = "Back"
	if rows[36] != PanelUCP10.KeyLegend(k, 120) {
		t.Fatalf("refused footer %q", rows[36])
	}
}

// A terminal shows its keys as text, with q.
func TestTerminalFooter(t *testing.T) {
	t0 := time.Unix(1000, 0)
	m := &Model{Title: "title", Terminal: true}
	rows := screenRows(t, m, 120, 37, t0)
	if rows[36] != "Up = Previous   Down = Next   Enter = Open   q = quit" {
		t.Fatalf("%q", rows[36])
	}
	m.Sel = int(PageRestart)
	m.Handle(Event{KeyOK, true}, t0)
	rows = screenRows(t, m, 120, 37, t0)
	if rows[36] != "Enter, then y = restart   b = Back   q = quit" || !m.Confirm {
		t.Fatalf("%q", rows[36])
	}
}

// Every page fits both panels: no cut row, no "do not fit" sign (the
// status scrolls with More, the log scrolls).
func TestPagesFit(t *testing.T) {
	t0 := time.Unix(1000, 0)
	lines := []string{
		"ok: the installer RAM image (token p10inst-e13d74), no eMMC file system mounted",
		"ok: the machine is a Crestron UC-P10 (rockchip,rk3399)",
		"ok: device tree: syscon@ff320000/reboot-mode, offset 0x300, mode-loader = 0x5242c301 (BOOT_BL_DOWNLOAD)",
		"ok: the driver syscon-reboot-mode is bound (ff320000.syscon:reboot-mode)",
	}
	for _, p := range []struct {
		panel Panel
		w, h  int
	}{{PanelUCP10, 120, 37}, {PanelUCP8, 91, 28}} {
		for _, terminal := range []bool{false, true} {
			for _, page := range []Page{PageStatus, PageLog, PageRestart, PageLoader} {
				for _, ok := range []bool{true, false} {
					m := &Model{Title: "title", Report: longReport(), Extra: extraLines(), Terminal: terminal, Panel: p.panel,
						Loader: &LoaderPage{OK: ok, Lines: lines}}
					if !ok {
						m.Loader.Lines = append(lines[:1:1], "refused: an eMMC file system is mounted (/dev/mmcblk0p13 on /mnt): the restart does not stop a system")
					}
					m.Log = []string{strings.Repeat("a long log line ", 20)}
					m.Sel = int(page)
					m.Handle(Event{KeyOK, true}, t0)
					rows := screenRows(t, m, p.w, p.h, t0.Add(time.Second))
					if s := strings.Join(rows, "\n"); strings.Contains(s, "do not fit") {
						t.Errorf("%s %v page %d ok %v:\n%s", p.panel.Model, terminal, page, ok, s)
					}
				}
			}
		}
	}
}

// RELEASE-1.1-REVIEW S-2: the two texts no longer state as fact what is
// not measured, or not true with a BCB that holds a command; the withdrawn
// BCB restore is not offered.
func TestS2Texts(t *testing.T) {
	t0 := time.Unix(1000, 0)
	m := &Model{Title: "title", Loader: &LoaderPage{OK: true}, Sel: int(PageLoader)}
	m.Handle(Event{KeyOK, true}, t0)
	s := strings.Join(screenRows(t, m, 120, 37, t0), " ")
	s = strings.Join(strings.Fields(s), " ")
	for _, bad := range []string{"before the BCB", "restore the BCB", "reviewed script"} {
		if strings.Contains(s, bad) {
			t.Errorf("download page says %q", bad)
		}
	}
	for _, want := range []string{"With no command in the BCB, U-Boot then enters the download mode (measured on a UC-P10, 2026-11-03).",
		"With a command in the BCB, this was not measured.", "The restart writes nothing to the eMMC."} {
		if !strings.Contains(s, want) {
			t.Errorf("download page lacks %q:\n%s", want, s)
		}
	}
	m = &Model{Title: "title", Sel: int(PageRestart)}
	m.Handle(Event{KeyOK, true}, t0)
	s = strings.Join(strings.Fields(strings.Join(screenRows(t, m, 120, 37, t0), " ")), " ")
	if !strings.Contains(s, "so the unit does not come back into fastboot, unless the BCB in misc holds a boot command.") {
		t.Errorf("restart page:\n%s", s)
	}
}

func TestWrapRows(t *testing.T) {
	for _, c := range []struct {
		in   string
		w    int
		want []string
	}{
		{"short", 10, []string{"short"}},
		{"", 10, []string{""}},
		{"  one two three four", 10, []string{"  one two", "    three", "    four"}},
		{"  one two three four", 14, []string{"  one two", "      three", "      four"}},
		{"abcdefghijklmnop", 6, []string{"abcdef", "    gh", "    ij", "    kl", "    mn", "    op"}},
	} {
		got := wrap(c.in, c.w)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("wrap(%q, %d) = %q, want %q", c.in, c.w, got, c.want)
		}
		for _, r := range got {
			if cells(r) > c.w {
				t.Errorf("row %q longer than %d", r, c.w)
			}
		}
	}
}

func TestPanelFor(t *testing.T) {
	if PanelFor("crestron,uc-p8\x00rockchip,rk3399\x00") != PanelUCP8 || PanelFor("crestron,uc-p10\x00rockchip,rk3399") != PanelUCP10 ||
		PanelFor("") != PanelUCP10 {
		t.Fatal("PanelFor")
	}
	if (Panel{}).KeyLegend([KeyPlaces]string{PlaceMic: "Open"}, 120) != PanelUCP10.KeyLegend([KeyPlaces]string{PlaceMic: "Open"}, 120) {
		t.Fatal("the zero panel is not the UC-P10")
	}
}
