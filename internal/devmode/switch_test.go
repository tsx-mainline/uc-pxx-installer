//go:build install && devtest

package devmode_test

// The slot switch (DEVMODE-R4.md §4) on fake units with the real tool.

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/device/devicetest"
	"uc-pxx-installer/internal/devmode"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/plan"
)

func (u *unit) switchTo(to string, edit func(o *devmode.SwitchOptions)) (*devmode.RepairResult, string, int) {
	o := devmode.SwitchOptions{Device: device.EMMCPath, To: to, LogDir: filepath.Join(u.root.Dir, "run/switch"), Yes: true}
	if edit != nil {
		edit(&o)
	}
	var out bytes.Buffer
	var res *devmode.RepairResult
	var err error
	u.lockRetry(func() error {
		out.Reset()
		res, err = devmode.Switch(u.newEnv(), o, &out)
		return err
	})
	code := devmode.RepairExitCode(res, err)
	if err != nil {
		out.WriteString("\nRESULT: " + err.Error() + "\n")
	}
	return res, out.String(), code
}

func TestSwitch(t *testing.T) {
	bin, exe := bootctlBin(t)
	u := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe)
	s4 := func(p, tr, s byte) [4]byte { return [4]byte{p, tr, s, 0} }
	for _, c := range []struct {
		name   string
		a, b   [4]byte
		to     string
		code   int
		wa, wb [4]byte // the slots afterwards (code 0 with a write)
		writes int
		want   string
	}{
		{"S0 to b", s4(15, 0, 1), s4(14, 0, 1), "b", 0, s4(14, 0, 1), s4(15, 0, 1), 1, "RESULT: switched"},
		{"B next, to a", s4(14, 0, 1), s4(15, 0, 1), "a", 0, s4(15, 0, 1), s4(14, 0, 1), 1, "RESULT: switched"},
		{"tie 15/15 to a", s4(15, 0, 1), s4(15, 0, 1), "a", 0, s4(15, 0, 1), s4(14, 0, 1), 1, "RESULT: switched"},
		{"A 3, B 2 to b", s4(3, 0, 1), s4(2, 0, 1), "b", 0, s4(3, 0, 1), s4(15, 0, 1), 1, "RESULT: switched"},
		{"from a trying slot to b", s4(15, 5, 0), s4(14, 0, 1), "b", 0, s4(14, 5, 0), s4(15, 0, 1), 1, "RESULT: switched"},
		{"S0 to a: nothing to do", s4(15, 0, 1), s4(14, 0, 1), "a", 0, s4(15, 0, 1), s4(14, 0, 1), 0, "RESULT: no switch needed"},
		{"to a trying slot", s4(14, 0, 1), s4(15, 7, 0), "b", 1, s4(14, 0, 1), s4(15, 7, 0), 0, "is not successful"},
		{"from a disabled slot", s4(0, 0, 0), s4(15, 0, 1), "b", 1, s4(0, 0, 0), s4(15, 0, 1), 0, "would not be a fallback"},
		{"to b while a is disabled... a is the only one", s4(15, 0, 1), s4(0, 0, 0), "b", 1, s4(15, 0, 1), s4(0, 0, 0), 0, "is not successful"},
		{"to a successful slot of priority 0", s4(0, 0, 1), s4(15, 0, 1), "a", 1, s4(0, 0, 1), s4(15, 0, 1), 0, "is not successful"},
		{"from a slot that is not bootable", s4(14, 0, 1), s4(15, 0, 0), "a", 1, s4(14, 0, 1), s4(15, 0, 0), 0, "would not be a fallback"},
	} {
		t.Run(c.name, func(t *testing.T) {
			sec := abSector(u.start, c.a, c.b)
			u.setAB(sec)
			u.bc.Calls = nil
			// the dry run first
			_, dout, dcode := u.switchTo(c.to, func(o *devmode.SwitchOptions) { o.Yes, o.DryRun = false, true })
			if !bytes.Equal(u.ab(), sec) || len(u.writeCalls()) != 0 {
				t.Fatalf("the dry run wrote\n%s", dout)
			}
			if wantDry := map[bool]int{true: 5, false: c.code}[c.writes == 1]; dcode != wantDry {
				t.Fatalf("dry run exit %d, want %d\n%s", dcode, wantDry, dout)
			}
			res, out, code := u.switchTo(c.to, nil)
			if code != c.code || res.Writes != c.writes || !strings.Contains(out, c.want) {
				t.Fatalf("exit %d writes %d, want %d %d %q\n%s", code, res.Writes, c.code, c.writes, c.want, out)
			}
			want := abSector(u.start, c.wa, c.wb)
			if !bytes.Equal(u.ab(), want) {
				t.Fatalf("A/B %x, want %x", u.ab()[:32], want[:32])
			}
			a, _ := avbab.Parse(u.ab())
			if a.Validate() != nil || a.Pick() < 0 {
				t.Fatal("no bootable slot")
			}
			if c.writes == 1 && (avbab.SlotName(a.Pick()) != c.to || !a.Slots[1-a.Pick()].Bootable()) {
				t.Fatalf("after the switch: %x", u.ab()[:32])
			}
		})
	}
	t.Run("an invalid sector", func(t *testing.T) {
		bad := brokenStates(u.start, 0)["crc wrong"]
		u.setAB(bad)
		u.bc.Calls = nil
		_, out, code := u.switchTo("b", nil)
		if code != 1 || !bytes.Equal(u.ab(), bad) || len(u.writeCalls()) != 0 || !strings.Contains(out, "not valid") {
			t.Fatalf("exit %d\n%s", code, out)
		}
	})
	t.Run("an unreadable sector", func(t *testing.T) {
		u.setAB(u.start)
		u.abErr = errors.New("input/output error")
		defer func() { u.abErr = nil }()
		_, out, code := u.switchTo("b", nil)
		if code != 1 || len(u.writeCalls()) != 0 || !strings.Contains(out, "never written") {
			t.Fatalf("exit %d\n%s", code, out)
		}
	})
	t.Run("not confirmed", func(t *testing.T) {
		u.setAB(u.start)
		_, out, code := u.switchTo("b", func(o *devmode.SwitchOptions) { o.Yes, o.Confirm = false, strings.NewReader("switch\n") })
		if code != 1 || !bytes.Equal(u.ab(), u.start) || !strings.Contains(out, "not confirmed") {
			t.Fatalf("exit %d\n%s", code, out)
		}
	})
}

// TestSwitchMismatch: no second write after a bad write.
func TestSwitchMismatch(t *testing.T) {
	bin, exe := bootctlBin(t)
	for _, c := range []struct {
		mode string
		code int
	}{
		{"garbage", 3}, {"wrong", 3}, {"tail", 3}, {"lie", 0}, {"refuse", 1}, {"unknown", 1}, {"unknown-wrote", 0},
		{"signal", 1}, {"signal-wrote", 0}, {"torn-rc3:16", 3}, {"torn-rc3:32", 0},
	} {
		t.Run(c.mode, func(t *testing.T) {
			u := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe)
			u.bc.Mode = c.mode
			res, out, code := u.switchTo("b", nil)
			if code != c.code || res.Writes != 1 || len(u.writeCalls()) != 1 {
				t.Fatalf("exit %d writes %d (tool %q), want %d and one write\n%s", code, res.Writes, u.writeCalls(), c.code, out)
			}
			if hold := strings.Contains(out, "DO NOT POWER OFF"); hold != (code == 3) {
				t.Fatalf("exit %d with hold text %v\n%s", code, hold, out)
			}
		})
	}
}

// TestSwitchPowerCut: a cut before, inside (torn) and after the write.
// The switch goes from two bootable slots to two bootable slots; only a torn
// write cut inside bytes 1..31 can leave fewer (the sector-atomicity
// assumption of every A/B write, DEVMODE-R4.md §2.5).
func TestSwitchPowerCut(t *testing.T) {
	bin, exe := bootctlBin(t)
	t.Parallel()
	var residual atomic.Int64
	t.Cleanup(func() {
		t.Logf("switch power-cut sweep: 514 cut points; %d torn inside bytes 1..31 leave fewer bootable slots", residual.Load())
	})
	// each of the 514 cuts is a parallel point with its own unit (a cut run
	// leaves its A/B lock open, as a power loss would: see TestRepairPowerCut)
	for n := -1; n <= 512; n++ {
		point(t, fmt.Sprintf("cut %d", n), func(t *testing.T) {
			sweepList(t, strconv.Itoa(n))
			u := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe)
			want, _ := plan.PredictSwitch(u.start, 1)
			u.setAB(u.start)
			u.bc.Mode = ""
			var hooks *install.Hooks
			switch {
			case n == -1:
				hooks = &install.Hooks{CutAfter: 0}
			case n == 512:
				hooks = &install.Hooks{CutAfter: -1, Observe: func(install.Event) error { return install.ErrPowerCut }}
			default:
				u.bc.Mode = "torn:" + strconv.Itoa(n)
			}
			_, out, _ := u.switchTo("b", func(o *devmode.SwitchOptions) { o.Hooks = hooks })
			got := u.ab()
			switch {
			case n == -1 || n == 0:
				if !bytes.Equal(got, u.start) {
					t.Fatalf("cut %d: changed\n%s", n, out)
				}
			case n >= 32:
				if !bytes.Equal(got, want) {
					t.Fatalf("cut %d: %x, want the switch\n%s", n, got[:32], out)
				}
			}
			a, _ := avbab.Parse(got)
			if a.Validate() != nil || !a.Slots[0].Bootable() || !a.Slots[1].Bootable() {
				if n < 1 || n > 31 {
					t.Fatalf("cut %d: %x has fewer bootable slots", n, got[:32])
				}
				residual.Add(1)
			}
		})
	}
}
