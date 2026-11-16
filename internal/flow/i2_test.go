package flow

import (
	"encoding/hex"
	"strings"
	"testing"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/plan"
)

// TestI2SwitchUntested (RC-10 I-2): the Switch screen offers a trying slot;
// its dry run has --untested; the confirm needs the device mode's proof of
// the slot's firmware (the "known" line), shows the plain words of DESIGN
// §9.2 and the bytes; the 3 s hold asks for the write with --untested; the
// end is Done only when the new slot starts next, is still trying, and the
// other slot is bootable. A dry run without the proof is no confirm.
func TestI2SwitchUntested(t *testing.T) {
	cur := sector([3]byte{15, 6, 0}, [3]byte{14, 7, 0}, 0x5a)
	want, err := plan.PredictSwitchKnown(cur, 1)
	if err != nil {
		t.Fatal(err)
	}
	toConfirm := func(known string) *drive {
		d := newDrive(t, false, "off")
		d.m.Unit.Slots[0].AB = avbab.Slot{Priority: 15, Tries: 6}
		d.m.Unit.Slots[1].AB = avbab.Slot{Priority: 14, Tries: 7}
		d.m.Sel = 2
		d.press(KeyOK) // Switch
		d.want(ScreenSwitch)
		if why := d.m.switchGate("b"); why != "" {
			t.Fatalf("a trying slot is not offered: %s", why)
		}
		d.press(KeyOK) // the dry run
		a := d.wantActs(ActSwitchCheck)
		if !a[0].Untested || !d.m.switchUntested {
			t.Fatalf("the dry run %+v", a[0])
		}
		d.m.ToolEnded(ToolResult{Tool: "switch", DryRun: true, End: End{Exited: true, Code: 5}, After: cur,
			Would: hex.EncodeToString(want[:32]), Known: known})
		return d
	}
	d := toConfirm("1.0.0.94 (userdebug)")
	d.want(ScreenSwitchConfirm)
	v := strings.Join(rowsOf(d.m.View(d.now)), "\n")
	if !strings.Contains(v, "Start slot B (1.0.0.94 (userdebug)) next. It is not tested on this unit yet. If it does not start, slot A starts again.") ||
		!strings.Contains(v, hex.EncodeToString(want[:32])) {
		t.Fatalf("the confirm:\n%s", v)
	}
	d.hold(HoldOK)
	a := d.wantActs(ActSwitch)
	if !a[0].Untested || a[0].Slot != "b" {
		t.Fatalf("the write %+v", a[0])
	}
	if p := d.m.Persist(); !p.SwitchUntested || p.SwitchKnown != "1.0.0.94 (userdebug)" {
		t.Fatalf("persisted %+v", p)
	}
	d.m.ToolEnded(ToolResult{Tool: "switch", End: End{Exited: true}, Safe: true, After: want})
	d.want(ScreenStatus)
	if !strings.HasPrefix(d.m.Msg, "Done. Slot B starts next.") {
		t.Fatal(d.m.Msg)
	}
	// no proof of the firmware: no confirm
	d = toConfirm("")
	d.want(ScreenSwitch)
	if !strings.Contains(d.m.Msg, "nothing was written") {
		t.Fatal(d.m.Msg)
	}
	// a write that read back with the slot marked, or not next: the hold
	d = toConfirm("1.0.0.94 (userdebug)")
	d.hold(HoldOK)
	d.wantActs(ActSwitch)
	bad := sector([3]byte{14, 6, 0}, [3]byte{15, 0, 1}, 0x5a)
	d.m.ToolEnded(ToolResult{Tool: "switch", End: End{Exited: true}, Safe: true, After: bad})
	d.want(ScreenHold)
	// the args
	if got := strings.Join(SwitchArgsUntested("x", "b", "/l", true), " "); got != "x switch --device /dev/mmcblk0 --to b --log /l --dry-run --untested" {
		t.Fatal(got)
	}
}
