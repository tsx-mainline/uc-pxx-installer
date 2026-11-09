package flow

import (
	"strings"
	"testing"

	"uc-pxx-installer/internal/avbab"
)

// A slot that is bootable but was never marked successful (trying: the
// never-tried slot B of a stock unit, A{15,0,1} B{14,7,0}, or the target
// of an install that has not started yet) is never called "the fallback"
// (review P10-ENTER-CC2 I-2). A successful slot still is.
func TestNeverTriedNotFallback(t *testing.T) {
	stock := sector([3]byte{15, 0, 1}, [3]byte{14, 7, 0}, 0)
	s0 := sector([3]byte{15, 0, 1}, [3]byte{14, 0, 1}, 0)
	for _, c := range []struct{ got, want string }{
		{abSentence(stock, false), "slot A starts next; slot B is not tested yet (7 tries left), so it is not a proven fallback"},
		{abSentence(stock, true), "slot A starts next; slot B is not tested yet (7 tries left), so it is not a proven fallback"},
		{plainAB(stock), "Slot A starts next. Slot B is not tested yet (7 tries left), so it is not a proven fallback."},
		{abSentence(s0, false), "slot A starts next; slot B is the fallback"},
		{abSentence(s0, true), "slot A starts next; slot B is the fallback and stays bootable"},
		{plainAB(s0), "Slot A starts next. Slot B is the fallback."},
		{plainAB(sector([3]byte{15, 0, 1}, [3]byte{0, 0, 0}, 0)), "Slot A starts next. Slot B will not start until it is installed again."},
		{otherAfterSwitch("b", avbab.Slot{Priority: 14, Tries: 7}), "Start this slot next: slot B stays not tested yet (7 tries left), so it is not a proven fallback."},
		{otherAfterSwitch("a", avbab.Slot{Priority: 15, Successful: 1}), "Start this slot next: slot A becomes the fallback."},
	} {
		if c.got != c.want {
			t.Errorf("got  %q\nwant %q", c.got, c.want)
		}
	}

	// the flow's status page of a stock unit: B shows its state, no role
	r := unit2Report()
	a, _ := avbab.Parse(stock)
	r.AB, r.Slots[0].AB, r.Slots[1].AB = a, a.Slots[0], a.Slots[1]
	for _, l := range UnitFrom(r).Lines {
		if strings.Contains(l.Text, "Slot B") && strings.Contains(l.Text, "fallback") {
			t.Errorf("status row %q", l.Text)
		}
	}
	r = unit2Report()
	found := false
	for _, l := range UnitFrom(r).Lines {
		found = found || strings.Contains(l.Text, "Slot B") && strings.Contains(l.Text, "fallback")
	}
	if !found {
		t.Error("a successful slot B is not shown as the fallback")
	}

	// the switch to A while B (trying, after an install) starts next
	d := newDrive(t, false, "off")
	d.m.Unit.Slots[0].AB = avbab.Slot{Priority: 14, Successful: 1}
	d.m.Unit.Slots[1].AB = avbab.Slot{Priority: 15, Tries: 7}
	d.m.Unit.Next = 1
	d.m.Sel = 2 // Switch
	d.press(KeyOK)
	d.want(ScreenSwitch)
	f := d.m.View(d.now)
	if !hasRow(f, "      Start this slot next: slot B stays not tested yet (7 tries left), so it is not a proven fallback.") {
		t.Fatalf("switch screen: %q", rowsOf(f))
	}
	for _, row := range rowsOf(f) {
		if strings.Contains(row, "fallback") && !strings.Contains(row, "not a proven fallback") {
			t.Errorf("switch row %q", row)
		}
	}
	d.m.switchTo, d.m.Screen = "a", ScreenSwitching
	if got := rowsOf(d.m.View(d.now)); got[0] != "Slot A is made the slot that starts next; slot B stays not tested yet (7 tries left)." {
		t.Errorf("switching: %q", got[0])
	}
}
