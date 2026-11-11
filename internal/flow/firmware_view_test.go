package flow

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestFirmwareViewIsViews: the firmware screen of this build (firmwareView,
// mainline_render.go) is the case ScreenFirmware of View (render.go) line for
// line, with exactly one line more: txtMLFirmwareCopy after txtFirmwareCopy.
// It compares the source of the two (View's case cannot run any more: the
// mainline hook renders the screen first, so that View's code stays as it
// was), then the rows of each state.
func TestFirmwareViewIsViews(t *testing.T) {
	read := func(name string) []string {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(string(b), "\n")
	}
	between := func(lines []string, start, end string) []string {
		var out []string
		in := false
		for _, l := range lines {
			switch {
			case !in && strings.TrimSpace(l) == start:
				in = true
			case in && strings.TrimSpace(l) == end:
				return out
			case in:
				out = append(out, strings.TrimSpace(l))
			}
		}
		t.Fatalf("no %q ... %q", start, end)
		return nil
	}
	view := between(read("render.go"), "case ScreenFirmware:", "case ScreenTarget:")
	// the body of firmwareView: up to the comment of the next function,
	// without its closing "}" and the empty line
	mine := between(read("mainline_render.go"),
		"func (m *Model) firmwareView(f *Frame, add func(level int, format string, a ...any), now time.Time) {",
		"// mlDriveRow names the bundle of the drive (the version, the size).")
	if len(mine) < 2 || mine[len(mine)-2] != "}" || mine[len(mine)-1] != "" {
		t.Fatalf("the end of firmwareView: %q", mine)
	}
	mine = mine[:len(mine)-2]
	var want []string
	for _, l := range view {
		want = append(want, strings.Replace(l, "m.setKeys(&f, ", "m.setKeys(f, ", 1))
		if l == `add(levelNormal, "%s", txtFirmwareCopy)` {
			want = append(want, `add(levelNormal, "%s", txtMLFirmwareCopy)`)
		}
	}
	if !reflect.DeepEqual(mine, want) {
		t.Fatalf("firmwareView is not View's firmware screen plus one line:\n%s\n---\n%s", strings.Join(mine, "\n"), strings.Join(want, "\n"))
	}
	m := New("t", true)
	m.Screen = ScreenFirmware
	now := time.Unix(100, 0)
	for name, set := range map[string]func(){
		"waiting": func() {},
		"reading": func() { m.reading = true },
		"refused": func() { m.refused = "no file" },
		"copying": func() { m.copyStart, m.copyLast = now.Add(-10*time.Second), now.Add(-time.Second) },
	} {
		m.reading, m.refused, m.copyStart, m.copyLast = false, "", time.Time{}, time.Time{}
		set()
		r := rowsOf(m.View(now))
		if len(r) < 3 || r[0] != txtFirmwareCopy || r[1] != txtMLFirmwareCopy || r[2] != txtFirmwareEject {
			t.Fatalf("%s: rows %q", name, r)
		}
	}
	if !strings.Contains(txtMLFirmwareCopy, "uc-pxx-mainline") || txtFirmwareCopy != "Copy the firmware file (.bin) to the drive UC-PXX-INST on your computer." {
		t.Fatal("texts")
	}
}
