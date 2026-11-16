package flow

import (
	"strings"
	"testing"
	"time"

	"uc-pxx-installer/internal/status"
)

// TestUnlockStatusRow (I-1, owner R7): the Status screen's row says the
// unlock status in plain words; the hex value is never on the screen; a
// change seen by the read is the hold.
func TestUnlockStatusRow(t *testing.T) {
	for _, c := range []struct {
		value   string
		changed bool
		word    string
	}{{"0x01", false, "unlocked"}, {"0x00", false, "locked"}, {"", false, "could not read"}, {"0x03", false, "could not read"}, {"0x01", true, "changed"}} {
		if w := UnlockOf(c.value, c.changed); w != c.word {
			t.Errorf("%q %v: %q", c.value, c.changed, w)
		}
	}
	m := New("t", false)
	m.SetUnit(Unit{ABValid: true, Lines: []status.Line{{Text: "Unit"}, {Text: "Bootloader                     unlocked (heuristic)"}}})
	body := func() string {
		var s []string
		for _, l := range m.View(time.Now()).Body {
			s = append(s, l.Text)
		}
		return strings.Join(s, "\n")
	}
	if b := body(); !strings.Contains(b, "Unlock status                  reading …") || strings.Contains(b, "Bootloader") {
		t.Fatalf("before the read:\n%s", b)
	}
	for _, w := range []string{UnlockReading, UnlockUnlocked, UnlockLocked, UnlockUnread} {
		m.SetUnlock(w, "")
		want := map[string]string{UnlockReading: "reading …"}[w]
		if want == "" {
			want = w
		}
		if b := body(); !strings.Contains(b, "Unlock status                  "+want) || strings.Contains(b, "0x0") {
			t.Fatalf("%s:\n%s", w, b)
		}
	}
	m.SetUnlock(UnlockChanged, "STOP: the reader saw the BCB change")
	if m.Screen != ScreenHold || m.RestartBlocked() == "" {
		t.Fatalf("a changed read: screen %s", m.Screen)
	}
}

// TestPhaseWordsClosed: every screen, with and without each child, with and
// without the drive mode, gives a word of PhaseWords (the page interface).
func TestPhaseWordsClosed(t *testing.T) {
	words := map[string]bool{}
	for _, w := range PhaseWords {
		words[w] = true
	}
	seen := map[string]bool{}
	for s := ScreenStatus; s <= ScreenUnlock; s++ {
		for _, run := range []string{"", "plan", "install", "switch", "repair"} {
			for _, drive := range []bool{false, true} {
				for _, reading := range []bool{false, true} {
					for _, swap := range []int{0, 1, 2} { // no ml state, the swap view, the stash child
						m := New("test", true)
						m.Screen, m.running, m.reading = s, run, reading
						m.usb = &usbState{drive: drive}
						switch swap {
						case 1:
							m.ml = &mlState{swap: true}
						case 2:
							m.ml = &mlState{stashing: true}
						}
						for _, msg := range []string{"", "Canceled."} {
							m.Msg = msg
							if p := m.Phase(); !words[p] {
								t.Fatalf("screen %v run %q drive %v: phase %q is not in PhaseWords", s, run, drive, p)
							} else {
								seen[p] = true
							}
						}
					}
				}
			}
		}
	}
	for _, w := range PhaseWords {
		if !seen[w] {
			t.Errorf("the word %q of PhaseWords is never given", w)
		}
	}
}
