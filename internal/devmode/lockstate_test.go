//go:build install && devtest

package devmode_test

// The in-RAM lock_state reader of the direct path (devmode lockstate.go,
// MAINLINE-RC2-LS-2026-11-11.md) with a stub of its TEE part on a fake unit:
// only 0x01 plans; 0x00, another byte, a failed read, no read refuse; a
// change around the read is a STOP (exit 3) that holds every later mainline
// plan of the boot; the reader runs once per boot (a later plan and the
// install's re-check read its record); a start without a record or a record
// of another boot refuses; the userdebug-first path never runs it.

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/devmode"
	"uc-pxx-installer/internal/plan"
)

// stubLock is a TEE part that gives the byte of v ("0xNN"), fails ("fail"),
// or gives 0x01 and changes the fake unit's BCB ("changed").
func stubLock(m *mlu, v string) devmode.LockReader {
	return countedLock(m, v, nil)
}

func countedLock(m *mlu, v string, n *int32) devmode.LockReader {
	return func(e *device.Env, d *device.Disk, say func(string)) devmode.LockTEE {
		if n != nil {
			atomic.AddInt32(n, 1)
		}
		t := devmode.LockTEE{Stub: "test " + v, Module: devmode.OpteeModuleSHA256, Supplicant: devmode.OpteeSupplicantSHA256, Client: devmode.OpteeClientSHA256}
		switch v {
		case "fail":
			t.Err = errors.New("the client: exit 3 (OPEN_SESSION failed)")
			return t
		case "changed":
			// the fake unit's disk node and its misc node are separate files: both change
			misc, _ := d.Part("misc")
			for _, w := range []struct {
				node string
				off  int64
			}{{device.EMMCPath, 49184*512 + 100}, {"/dev/" + misc.Dev, (49184-misc.Start)*512 + 100}} {
				f, err := os.OpenFile(e.Path(w.node), os.O_WRONLY, 0)
				if err != nil {
					t.Err = err
					return t
				}
				f.WriteAt([]byte("changed"), w.off)
				f.Close()
			}
			v = "0x01"
		}
		var b byte
		if v == "0x00" {
			b = 0
		} else if v == "0x03" {
			b = 3
		} else {
			b = 1
		}
		t.Byte = &b
		return t
	}
}

func lockRecord(t *testing.T, m *mlu) *plan.LockStateRead {
	t.Helper()
	b, err := os.ReadFile(m.root.Path(devmode.LockStateRecord))
	if err != nil {
		t.Fatal(err)
	}
	var r plan.LockStateRead
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return &r
}

func TestDirectLockReader(t *testing.T) {
	bin, exe := bootctlBin(t)
	t.Run("0x01: plans; once per boot; the install's re-check reads the record", func(t *testing.T) {
		m := newMLU(t, dUnitReset, true, bin, exe)
		var n int32
		o := m.mlOpts()
		o.UnlockConfirmed = false // the confirmation opens nothing and is not needed
		o.LockReader = countedLock(m, "0x01", &n)
		o.JSONOut = filepath.Join(m.root.Dir, "run/d-plan.json")
		p, out, err := m.dPlan(o)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		r := p.UnlockDirect.Reader
		if n != 1 || !p.Unlock.Confirmed || p.UnlockDirect.LockState != "0x01" || r == nil || r.Changed || r.Error != "" ||
			r.Before != r.After || !r.Before.ReadOnly || r.Before.Security == "" || r.Before.AB32 == "" || r.Before.BCB == "" ||
			r.Before.Boot == "" || r.Before.GPTBackup == "" || r.Before.Misc == "" ||
			r.Supplicant != devmode.OpteeSupplicantSHA256 || !strings.Contains(p.UnlockDirect.Basis, "in-RAM reader: unlocked") {
			t.Fatalf("n %d unlock %+v reader %+v\n%s", n, p.UnlockDirect, r, out)
		}
		if !strings.Contains(out, "SAME") || !strings.Contains(out, "lock_state 0x01") {
			t.Fatalf("output:\n%s", out)
		}
		rec := lockRecord(t, m)
		if rec.LockState != "0x01" || rec.BootID == "" || rec.Token == "" {
			t.Fatalf("record %+v", rec)
		}
		// a second plan of this boot: the record, not a second read; the same plan
		p2, out, err := m.dPlan(o)
		if err != nil || n != 1 || p2.ID != p.ID || !strings.Contains(out, "ran in this boot already") {
			t.Fatalf("second plan: n %d, id %s vs %s, %v\n%s", n, p2.ID, p.ID, err, out)
		}
		// the install of the plan file: its re-check reads the record (no read)
		ins := m.mlOpts()
		ins.UnlockConfirmed = false // the plan records it: the same answer gives the same plan id
		ins.LockReader = countedLock(m, "0x01", &n)
		ins.PlanFile, ins.Yes = o.JSONOut, true
		if _, out, err := m.dInstall(ins); err != nil || n != 1 {
			t.Fatalf("install: n %d %v\n%s", n, err, out)
		}
		m.dDone(p)
	})
	// 0x00 no longer refuses: the plan passes with the lock-state write intent
	// (the device mode writes 0x01 before the target; the image mode refuses
	// it at install, TestDirectLockWrite). The refusal cases are the other
	// non-0x01 reads.
	for _, c := range []struct{ v, want string }{
		{"0x03", "0x03, not unlocked"},
		{"fail", "the in-RAM reader failed: the client: exit 3"},
	} {
		t.Run(c.v+": refused, confirmed or not; the record holds the boot", func(t *testing.T) {
			m := newMLU(t, dUnitReset, true, bin, exe)
			var n int32
			o := m.mlOpts()
			o.LockReader = countedLock(m, c.v, &n)
			p, out, err := m.dPlan(o)
			if err == nil || devmode.ExitCode(err) != 1 || !strings.Contains(out, "FAIL unlock") || !strings.Contains(p.UnlockDirect.Basis, c.want) || p.Unlock.Confirmed {
				t.Fatalf("%v (exit %d)\n%s", err, devmode.ExitCode(err), out)
			}
			if !bytes.Equal(m.image(), m.before) {
				t.Fatal("the plan changed the unit")
			}
			// a 0x01 reader later in this boot does not run: the record answers
			o.LockReader = countedLock(m, "0x01", &n)
			if _, out, err := m.dPlan(o); err == nil || n != 1 || !strings.Contains(out, "FAIL unlock") {
				t.Fatalf("a second read in this boot: n %d %v\n%s", n, err, out)
			}
		})
	}
	t.Run("changed: STOP (exit 3), every later mainline plan of the boot holds", func(t *testing.T) {
		m := newMLU(t, dUnitReset, true, bin, exe)
		o := m.mlOpts()
		o.LockReader = stubLock(m, "changed")
		_, out, err := m.dPlan(o)
		if err == nil || !errors.Is(err, device.ErrABUnknown) || devmode.ExitCode(err) != 3 || !strings.Contains(err.Error(), "keep the unit powered") ||
			!strings.Contains(out, "CHANGED") {
			t.Fatalf("%v\n%s", err, out)
		}
		if r := lockRecord(t, m); !r.Changed || r.LockState != "0x01" || r.Before.BCB == r.After.BCB || r.Before.AB32 != r.After.AB32 {
			t.Fatalf("record %+v", r)
		}
		var n int32
		o.LockReader = countedLock(m, "0x01", &n)
		if _, _, err := m.dPlan(o); devmode.ExitCode(err) != 3 || n != 0 {
			t.Fatalf("the next direct plan: n %d %v", n, err)
		}
		if _, out, err := m.mlPlan(m.mlOpts()); devmode.ExitCode(err) != 3 || !strings.Contains(err.Error(), "lock_state reader of this boot") {
			t.Fatalf("the userdebug-first plan in this boot: %v\n%s", err, out)
		}
	})
	t.Run("a change only in LBA 1024 (the RKSS backup copy area) is caught (review B-3)", func(t *testing.T) {
		m := newMLU(t, dUnitReset, true, bin, exe)
		o := m.mlOpts()
		o.LockReader = func(e *device.Env, d *device.Disk, say func(string)) devmode.LockTEE {
			f, _ := os.OpenFile(e.Path(device.EMMCPath), os.O_WRONLY, 0)
			f.WriteAt([]byte("lba1024"), 1024*512)
			f.Close()
			b := byte(1)
			return devmode.LockTEE{Byte: &b, Stub: "test lba1024"}
		}
		_, out, err := m.dPlan(o)
		if devmode.ExitCode(err) != 3 {
			t.Fatalf("%v\n%s", err, out)
		}
		if r := lockRecord(t, m); !r.Changed || r.Before.Boot == r.After.Boot || r.Before.BCB != r.After.BCB {
			t.Fatalf("record %+v", r)
		}
	})
	t.Run("a read that started and left no record: refused, never read again", func(t *testing.T) {
		m := newMLU(t, dUnitReset, true, bin, exe)
		os.MkdirAll(m.root.Path(devmode.LockRunDir), 0o700)
		os.WriteFile(m.root.Path(devmode.LockStarted), []byte("killed\n"), 0o600)
		var n int32
		o := m.mlOpts()
		o.LockReader = countedLock(m, "0x01", &n)
		_, out, err := m.dPlan(o)
		if err == nil || n != 0 || !strings.Contains(out, "did not end") || devmode.ExitCode(err) != 1 {
			t.Fatalf("n %d %v\n%s", n, err, out)
		}
	})
	t.Run("a record of another boot: refused", func(t *testing.T) {
		m := newMLU(t, dUnitReset, true, bin, exe)
		os.MkdirAll(m.root.Path(devmode.LockRunDir), 0o700)
		b, _ := json.Marshal(plan.LockStateRead{BootID: "another-boot", Token: "p10inst-000000", LockState: "0x01"})
		os.WriteFile(m.root.Path(devmode.LockStateRecord), b, 0o600)
		_, out, err := m.dPlan(m.mlOpts())
		if err == nil || !strings.Contains(err.Error(), "not this boot") {
			t.Fatalf("%v\n%s", err, out)
		}
	})
	t.Run("an unreadable record: STOP (exit 3)", func(t *testing.T) {
		m := newMLU(t, dUnitReset, true, bin, exe)
		os.MkdirAll(m.root.Path(devmode.LockRunDir), 0o700)
		os.WriteFile(m.root.Path(devmode.LockStateRecord), []byte("{not json"), 0o600)
		if _, out, err := m.dPlan(m.mlOpts()); devmode.ExitCode(err) != 3 {
			t.Fatalf("%v\n%s", err, out)
		}
		if _, _, err := m.mlPlan(m.mlOpts()); devmode.ExitCode(err) != 3 {
			t.Fatalf("the userdebug-first plan: %v", err)
		}
	})
	t.Run("the userdebug-first path never runs the reader and takes no reader", func(t *testing.T) {
		u := mlUnit3
		m := newMLU(t, u, false, bin, exe)
		var n int32
		if _, out, err := m.mlPlan(m.mlOpts()); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if _, err := os.Stat(m.root.Path(devmode.LockRunDir)); err == nil || n != 0 {
			t.Fatal("the userdebug-first plan ran the reader")
		}
		o := m.mlOpts()
		o.LockReader = countedLock(m, "0x01", &n)
		if _, _, err := m.mlPlan(o); err == nil || !strings.Contains(err.Error(), "lock_state reader") || n != 0 {
			t.Fatalf("a userdebug-first plan with a reader: %v", err)
		}
	})
}
