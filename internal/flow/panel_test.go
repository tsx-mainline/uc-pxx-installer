package flow

// W-P6: the flow on the UC-P8 (1280 x 800; UC-P8.md §5). The key row is the
// owner's UC-P10 row scaled by 2/3 (an estimate until the third party's
// photo); the screens must fit the consoles of both candidate fonts:
// ter-124b (106 x 33, the image's rcS) and ter-128b (91 x 28).

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

// p8Sizes are the consoles of the two fonts of UC-P8.md §5.2.
var p8Sizes = []struct {
	font       string
	cols, rows int
}{{"ter-124b", 106, 33}, {"ter-128b", 91, 28}}

// TestPanelGeometry: the UC-P8 row is the UC-P10 row × 2/3 (margin 33 1/3,
// logo 480..800, keys 148 8/9 px, centres 108, 257, 406, 874, 1023, 1172);
// the UC-P10 row is unchanged; the device tree chooses the panel.
func TestPanelGeometry(t *testing.T) {
	want := []float64{108, 257, 406, 874, 1023, 1172}
	for i := 0; i < KeyPlaces; i++ {
		l10, r10 := PanelUCP10.KeyEdgesPx(i)
		if gl, gr := KeyEdgesPx(i); gl != l10 || gr != r10 {
			t.Errorf("key %d: the UC-P10 edges changed: %v..%v, %v..%v", i+1, gl, gr, l10, r10)
		}
		l8, r8 := PanelUCP8.KeyEdgesPx(i)
		if math.Abs(l8-l10*2/3) > 1e-9 || math.Abs(r8-r10*2/3) > 1e-9 {
			t.Errorf("key %d: UC-P8 %v..%v is not 2/3 of %v..%v", i+1, l8, r8, l10, r10)
		}
		if c := (l8 + r8) / 2; math.Abs(c-want[i]) > 1 {
			t.Errorf("key %d: centre %v, want %v", i+1, c, want[i])
		}
	}
	if PanelUCP8.LogoFromPx != 480 || PanelUCP8.LogoToPx != 800 || math.Abs(PanelUCP8.MarginPx-100.0/3) > 1e-9 ||
		math.Abs(PanelUCP8.KeyWidthPx()-1340.0/9) > 1e-9 || PanelUCP8.Cols != 106 || PanelUCP8.Rows != 33 {
		t.Errorf("UC-P8 panel %+v", PanelUCP8)
	}
	for in, want := range map[string]string{
		"crestron,uc-p8\x00rockchip,rk3399\x00":  "UC-P8",
		"crestron,uc-p10\x00rockchip,rk3399\x00": "UC-P10",
		"":                                       "UC-P10",
		"crestron,uc-p80\x00":                    "UC-P10",
	} {
		if got := PanelFor(in).Model; got != want {
			t.Errorf("PanelFor(%q) = %s, want %s", in, got, want)
		}
	}
	// a UC-P8 model draws the UC-P8 row; a model without a panel the UC-P10's
	m := New("t", false)
	if f := m.View(time.Now()); f.panel().Model != "UC-P10" {
		t.Error("the default panel")
	}
	m.Panel = PanelUCP8
	if f := m.View(time.Now()); f.panel().Model != "UC-P8" {
		t.Error("the UC-P8 panel is not in the frame")
	}
}

// TestKeyLabelsP8: every label of the flow on every key place fits the key
// at both UC-P8 sizes (the label, or a short form), centred on it, never
// over the logo.
func TestKeyLabelsP8(t *testing.T) {
	labels := []string{"Back", "Cancel", "Log", "Status", "Next", "Previous", "Check again", "Scroll down", "Scroll up",
		"Open", "Eject", "Try again", "Install here", "Install again", "Hold: start", "Hold: restart", "Hold: repair",
		"Hold: switch", "Check", "No", "Yes"}
	for _, sz := range p8Sizes {
		for place := 0; place < KeyPlaces; place++ {
			from, to := PanelUCP8.KeyCells(place, sz.cols)
			for _, l := range labels {
				var k [KeyPlaces]string
				k[place] = l
				row := []rune(PanelUCP8.keyColumns(k, sz.cols) + strings.Repeat(" ", sz.cols))
				got := LabelFor(l, to-from)
				ok := got == l
				for _, s := range shortLabels[l] {
					ok = ok || got == s
				}
				if !ok || strings.TrimSpace(string(row[from:to])) != got {
					t.Errorf("%s, key %d (%d..%d): %q gives %q", sz.font, place+1, from, to, l, string(row))
				}
				logo := string(row[int(PanelUCP8.LogoFromPx*float64(sz.cols)/PanelUCP8.Px):int(PanelUCP8.LogoToPx*float64(sz.cols)/PanelUCP8.Px)])
				if strings.TrimSpace(logo) != "" {
					t.Errorf("%s: %q over the logo", sz.font, l)
				}
			}
		}
	}
	// on the UC-P10 no label needs a short form
	for place := 0; place < KeyPlaces; place++ {
		from, to := PanelUCP10.KeyCells(place, PanelCols)
		for _, l := range labels {
			if LabelFor(l, to-from) != l {
				t.Errorf("UC-P10: %q is shortened", l)
			}
		}
	}
}

// TestScreensFitP8: every screen of TestScreensFit, and the screens of the
// hold's repair, the switch and the panel question, fit both UC-P8 sizes.
func TestScreensFitP8(t *testing.T) {
	for _, sz := range p8Sizes {
		t.Run(fmt.Sprintf("%s %dx%d", sz.font, sz.cols, sz.rows), func(t *testing.T) {
			screensFit(t, PanelUCP8, sz.cols, sz.rows)
			newerScreensFit(t, sz.cols, sz.rows)
		})
	}
}

// newerScreensFit: the screens since flow-r3 (the repair, the safe end, the
// switch and its confirm, the switch's hold, the panel question and its
// terminal warning) on the UC-P8 panel at cols x rows.
func newerScreensFit(t *testing.T, cols, rows int) {
	chk := func(d *drive, what string) {
		t.Helper()
		checkFitAt(t, d.m, d.now, what, cols, rows)
	}
	nd := func(terminal bool) *drive {
		d := newDrive(t, terminal, "off")
		d.m.Panel = PanelUCP8
		return d
	}
	for _, terminal := range []bool{false, true} {
		w := func(s string) string { return fmt.Sprintf("%s (terminal %v)", s, terminal) }
		// the panel question, and a terminal's warning
		d := nd(terminal)
		d.toChecklist()
		d.m.DriveEjected(true)
		d.m.Take()
		d.m.DriveRead(DriveResult{OK: true, Name: `"UC-P10_1.0.0.94_userdebug_firmware.bin"`, Size: 1020215296})
		chk(d, w("panel question"))
		d.press(KeyOK)
		chk(d, w("panel question, after OK"))
		if terminal {
			// a terminal: the switch's confirm (Enter, then y)
			d = nd(true)
			d.m.Sel = 2
			d.press(KeyOK)
			d.press(KeyOK)
			d.m.Take()
			d.m.ToolEnded(switchDry5)
			chk(d, w("switch confirm"))
			d.press(KeyOK)
			chk(d, w("switch confirm, after Enter"))
			continue
		}
		// the hold: checking, the offer, a refusal, twice, dead; the repair; the safe end
		d = nd(terminal)
		d.toHold()
		d.m.BundleDone("the drive P10-INSTALL, folder p10-install-0123456789ab-20261102T120000Z")
		chk(d, w("hold, checking"))
		d.m.ToolEnded(ToolResult{Tool: "repair", DryRun: true, End: End{Exited: true, Code: 5}})
		chk(d, w("hold, repair offered"))
		d.hold(HoldOK)
		d.m.Take()
		d.m.ChildLine("REPAIR-WRITE: 1: the A/B sector is written now", d.now)
		chk(d, w("repair"))
		d.m.ToolEnded(ToolResult{Tool: "repair", End: End{Exited: true, Code: 1}, Result: "repair refused, nothing written: keeper: system_a sha256 differs from the plan"})
		d.m.Take()
		d.m.ToolEnded(ToolResult{Tool: "repair", DryRun: true, End: End{Exited: true, Code: 5}})
		chk(d, w("hold, offered again"))
		d.hold(HoldOK)
		d.m.Take()
		d.m.ToolEnded(ToolResult{Tool: "repair", End: End{Exited: true, Code: 1}, Result: "repair refused, nothing written: keeper: system_a sha256 differs from the plan"})
		chk(d, w("hold, tried twice"))
		d = nd(terminal)
		d.toHold()
		d.m.ToolEnded(ToolResult{Tool: "repair", DryRun: true, End: End{Exited: true, Code: 5}})
		d.hold(HoldOK)
		d.m.Take()
		d.m.ToolEnded(ToolResult{Tool: "repair", End: End{Exited: true, Code: 3}, Stop: "STOP: x"})
		d.m.BundleDone("the drive P10-INSTALL, folder p10-install-0123456789ab-20261102T120000Z")
		chk(d, w("hold, repair dead"))
		d.press(KeyOK)
		d.m.Rechecked(StateD1, "")
		chk(d, w("hold, dead, read again"))
		d = nd(terminal)
		d.toHold()
		d.m.ToolEnded(ToolResult{Tool: "repair", DryRun: true, End: End{Exited: true}, Safe: true, After: d1Sec})
		d.m.BundleDone("the drive P10-INSTALL, folder p10-install-0123456789ab-20261102T120000Z")
		chk(d, w("safe"))
		// the switch, its confirm, its run, its hold
		d = nd(terminal)
		d.m.Sel = 2
		d.press(KeyOK)
		chk(d, w("switch"))
		d.press(KeyOK)
		d.m.Take()
		chk(d, w("switch, checking"))
		d.m.ToolEnded(switchDry5)
		chk(d, w("switch confirm"))
		d.hold(HoldOK)
		d.m.Take()
		chk(d, w("switching"))
		d.m.ToolEnded(switch3)
		d.m.BundleDone("the drive P10-INSTALL, folder p10-switch-20261102T120000Z")
		chk(d, w("switch hold"))
	}
}
