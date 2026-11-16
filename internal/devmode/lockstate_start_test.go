//go:build install && devtest

package devmode_test

// mainline-rc-10, I-1: the read at the start of the flow (`uc-pxx-inst
// lockstate`) is the reader of the direct path unchanged: once per boot, and
// the direct plan of the same boot reuses its record (no second TEE read, the
// same plan id); it writes nothing to the unit.

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"uc-pxx-installer/internal/devmode"
)

func TestLockStateAtStart(t *testing.T) {
	bin, exe := bootctlBin(t)
	for _, v := range []string{"0x01", "0x00"} {
		t.Run(v, func(t *testing.T) {
			m := newMLU(t, dUnitReset, true, bin, exe)
			var n int32
			lo := devmode.MainlineOptions{Options: devmode.Options{Device: "/dev/mmcblk0"}, LockReader: countedLock(m, v, &n)}
			var out bytes.Buffer
			r, err := devmode.ReadLockStateAtStart(m.newEnv(), lo, &out)
			if err != nil || r == nil || r.LockState != v || r.Changed || n != 1 {
				t.Fatalf("%+v %v n %d\n%s", r, err, n, out.String())
			}
			if !bytes.Equal(m.image(), m.before) {
				t.Fatal("the read changed the unit")
			}
			// a second start of the UI: the record, not a second read
			if r2, err := devmode.ReadLockStateAtStart(m.newEnv(), lo, io.Discard); err != nil || r2.LockState != v || n != 1 {
				t.Fatalf("second read: %+v %v n %d", r2, err, n)
			}
			// the direct plan of this boot reuses the record
			o := m.mlOpts()
			o.LockReader = countedLock(m, v, &n)
			o.JSONOut = filepath.Join(m.root.Dir, "run/d-plan.json")
			p, pout, err := m.dPlan(o)
			if err != nil || n != 1 || !strings.Contains(pout, "ran in this boot already") || p.UnlockDirect.LockState != v {
				t.Fatalf("the plan after the start read: n %d %v\n%s", n, err, pout)
			}
		})
	}
	t.Run("changed: exit 3, the plan of the boot holds", func(t *testing.T) {
		m := newMLU(t, dUnitReset, true, bin, exe)
		_, err := devmode.ReadLockStateAtStart(m.newEnv(), devmode.MainlineOptions{Options: devmode.Options{Device: "/dev/mmcblk0"},
			LockReader: stubLock(m, "changed")}, io.Discard)
		if devmode.ExitCode(err) != 3 {
			t.Fatalf("%v", err)
		}
		o := m.mlOpts()
		if _, _, err := m.dPlan(o); devmode.ExitCode(err) != 3 {
			t.Fatalf("a plan after a changed read: %v", err)
		}
	})
	t.Run("only the device", func(t *testing.T) {
		m := newMLU(t, dUnitReset, true, bin, exe)
		for _, o := range []devmode.Options{{Device: "/dev/mmcblk0", FW: "x"}, {Device: "/dev/mmcblk0", PlanFile: "p"}, {Device: "/dev/mmcblk0", Yes: true}} {
			if _, err := devmode.ReadLockStateAtStart(m.newEnv(), devmode.MainlineOptions{Options: o}, io.Discard); err == nil {
				t.Fatalf("taken: %+v", o)
			}
		}
	})
}
