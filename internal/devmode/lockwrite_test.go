//go:build install && devtest

package devmode_test

// The in-RAM lock_state WRITER of the direct path (devmode lockwrite.go,
// MAINLINE-UNLOCK-INTEGRATION-2026-11-12.md) with a stub of its TEE part on a
// fake unit. The owner's rule (2026-11-11 ~23:05): the reader reads lock_state;
// 0x01 -> proceed; 0x00 -> write 0x01, read it back 0x01, then proceed;
// anything else / a failed read / a failed read-back / a change -> refuse,
// write nothing to the target. The writer only ever writes 0x01, never 0x00;
// it refuses on a unit with mainline in both slots; it backs up the security
// partition first and restores it on any failure.

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/devmode"
	"uc-pxx-installer/internal/fakeemmc"
)

// stubWrite is a TEE part of the writer. It records the `from` value it was
// asked for (the installer must only ever write 0x01, so the CA's current is
// 0x00), optionally changes the fake unit's security node (to test the restore
// path) or its BCB (a STOP), and returns the read-back byte, or an error.
type writeStub struct {
	readBack  byte
	touchSec  bool // write readBack into the security node's first byte
	writeErr  bool // the writer fails (no read-back)
	changeBCB bool // the BCB moves around the write (a STOP)
	fromSeen  *int32
	calls     *int32
}

func stubWrite(m *mlu, s writeStub) devmode.LockWriter {
	return func(e *device.Env, d *device.Disk, from byte, say func(string)) devmode.LockWriteTEE {
		if s.calls != nil {
			atomic.AddInt32(s.calls, 1)
		}
		if s.fromSeen != nil {
			atomic.StoreInt32(s.fromSeen, int32(from))
		}
		t := devmode.LockWriteTEE{Stub: "test writer", Supplicant: devmode.OpteeRWSupplicantSHA256, Client: devmode.OpteeSetlockSHA256}
		if s.changeBCB {
			misc, _ := d.Part("misc")
			for _, w := range []struct {
				node string
				off  int64
			}{{device.EMMCPath, 49184*512 + 72}, {"/dev/" + misc.Dev, (49184-misc.Start)*512 + 72}} {
				f, err := os.OpenFile(e.Path(w.node), os.O_WRONLY, 0)
				if err != nil {
					t.Err = err
					return t
				}
				f.WriteAt([]byte("stub-write-changed-bcb"), w.off)
				f.Close()
			}
			one := byte(1)
			t.ReadBack = &one
			return t
		}
		if s.touchSec {
			sec, _ := d.Part("security")
			if f, err := os.OpenFile(e.Path("/dev/"+sec.Dev), os.O_WRONLY, 0); err == nil {
				f.WriteAt([]byte{s.readBack}, 0)
				f.Close()
			}
		}
		if s.writeErr {
			t.Err = errors.New("the setlock CA did not confirm 0x01 (exit 2)")
			return t
		}
		rb := s.readBack
		t.ReadBack = &rb
		return t
	}
}

func readLockWrite(t *testing.T, m *mlu) map[string]any {
	t.Helper()
	b, err := os.ReadFile(m.root.Path(devmode.LockWriteRecord))
	if err != nil {
		t.Fatalf("lockwrite record: %v", err)
	}
	var r map[string]any
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func secByte0(t *testing.T, m *mlu) byte {
	t.Helper()
	b, err := os.ReadFile(m.root.Node("security"))
	if err != nil {
		t.Fatal(err)
	}
	return b[0]
}

func TestDirectLockWrite(t *testing.T) {
	bin, exe := bootctlBin(t)

	t.Run("0x01: no write, the install proceeds", func(t *testing.T) {
		m := newMLU(t, dUnitReset, true, bin, exe)
		var calls int32
		o := m.mlOpts()
		o.LockReader = countedLock(m, "0x01", nil)
		o.LockWriter = stubWrite(m, writeStub{readBack: 1, calls: &calls})
		p, pout, perr := m.dPlan(o)
		if perr != nil {
			t.Fatalf("plan: %v\n%s", perr, pout)
		}
		if _, out, err := m.dInstall(o); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if calls != 0 {
			t.Fatalf("the writer ran on a 0x01 unit (%d calls)", calls)
		}
		if _, err := os.Stat(m.root.Path(devmode.LockWriteRecord)); err == nil {
			t.Fatal("a 0x01 unit left a lock-state write record")
		}
		m.dDone(p)
	})

	t.Run("0x00: writes 0x01, reads back 0x01, the install proceeds", func(t *testing.T) {
		m := newMLU(t, dUnitReset, true, bin, exe)
		var calls, from int32
		o := m.mlOpts()
		o.LockReader = countedLock(m, "0x00", nil)
		// the stub does NOT touch the security node, so dDone's "nothing
		// outside the write ranges changed" still holds (the real write
		// re-encrypts security; that is not modelled on the fake unit)
		o.LockWriter = stubWrite(m, writeStub{readBack: 1, calls: &calls, fromSeen: &from})
		p, pout, perr := m.dPlan(o)
		if perr != nil {
			t.Fatalf("plan: %v\n%s", perr, pout)
		}
		_, out, err := m.dInstall(o)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if calls != 1 || from != 0 {
			t.Fatalf("writer calls %d, from 0x%02x (the installer must write only from 0x00)", calls, from)
		}
		if p.UnlockDirect.Write == nil || p.UnlockDirect.Write.To != "0x01" || p.UnlockDirect.Write.From != "0x00" {
			t.Fatalf("plan write intent %+v", p.UnlockDirect.Write)
		}
		if !strings.Contains(out, "lock_state 0x01 read back: UNLOCKED") {
			t.Fatalf("output:\n%s", out)
		}
		rec := readLockWrite(t, m)
		if rec["verified"] != true || rec["read_back"] != "0x01" || rec["to"] != "0x01" || rec["from"] != "0x00" {
			t.Fatalf("lockwrite record %+v", rec)
		}
		m.dDone(p)
	})

	for _, c := range []struct {
		name string
		stub writeStub
		want string
	}{
		{"read-back 0x02: refuse and restore", writeStub{readBack: 2, touchSec: true}, "did not reach lock_state 0x01"},
		{"the write failed: refuse and restore", writeStub{readBack: 0xee, touchSec: true, writeErr: true}, "did not reach lock_state 0x01"},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := newMLU(t, dUnitReset, true, bin, exe)
			sec0 := secByte0(t, m)
			o := m.mlOpts()
			o.LockReader = countedLock(m, "0x00", nil)
			o.LockWriter = stubWrite(m, c.stub)
			res, out, err := m.dInstall(o)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want %q, got %v\n%s", c.want, err, out)
			}
			if res != nil && res.Writes != 0 {
				t.Fatalf("the target was written on a failed unlock (%d writes)", res.Writes)
			}
			// the security node is restored from the backup; the target (B) is
			// unchanged
			if got := secByte0(t, m); got != sec0 {
				t.Fatalf("security not restored: byte0 0x%02x, backup 0x%02x", got, sec0)
			}
			rec := readLockWrite(t, m)
			if rec["verified"] == true || rec["restored"] != true {
				t.Fatalf("lockwrite record %+v (want verified=false, restored=true)", rec)
			}
			// the security partition is restored byte for byte and nothing was
			// written to the target, so the whole eMMC image is unchanged
			if !bytes.Equal(m.image(), m.before) {
				t.Fatal("the eMMC image changed after a refused unlock (target written or security not restored)")
			}
		})
	}

	t.Run("a change around the write: STOP (exit 3), every later plan holds", func(t *testing.T) {
		m := newMLU(t, dUnitReset, true, bin, exe)
		o := m.mlOpts()
		o.LockReader = countedLock(m, "0x00", nil)
		o.LockWriter = stubWrite(m, writeStub{readBack: 1, changeBCB: true})
		_, out, err := m.dInstall(o)
		if err == nil || !errors.Is(err, device.ErrABUnknown) || devmode.ExitCode(err) != 3 || !strings.Contains(err.Error(), "keep the unit powered") {
			t.Fatalf("want exit 3 STOP, got %v (exit %d)\n%s", err, devmode.ExitCode(err), out)
		}
		rec := readLockWrite(t, m)
		if rec["changed"] != true {
			t.Fatalf("lockwrite record %+v (want changed=true)", rec)
		}
		// every later mainline plan of this boot is held (LockWriteHeld)
		o2 := m.mlOpts()
		o2.LockReader = countedLock(m, "0x01", nil)
		if _, _, err := m.dPlan(o2); devmode.ExitCode(err) != 3 {
			t.Fatalf("the next direct plan is not held: %v", err)
		}
		if _, _, err := m.mlPlan(m.mlOpts()); devmode.ExitCode(err) != 3 {
			t.Fatalf("the userdebug-first plan is not held: %v", err)
		}
	})

	t.Run("both slots mainline: the plan refuses, no lock-state write", func(t *testing.T) {
		bad := fakeemmc.Unit{AB: [2][3]byte{{15, 6, 0}, {14, 7, 0}}, A: "old", B: "old"}
		m := newMLU(t, bad, true, bin, exe)
		var calls int32
		o := m.mlOpts()
		o.LockReader = countedLock(m, "0x00", nil)
		o.LockWriter = stubWrite(m, writeStub{readBack: 1, calls: &calls})
		_, out, err := m.dInstall(o)
		if err == nil || !strings.Contains(out, "keeper-firmware") {
			t.Fatalf("a both-mainline unit was not refused at keeper-firmware: %v\n%s", err, out)
		}
		if calls != 0 {
			t.Fatalf("the lock-state writer ran on a both-mainline unit (%d calls)", calls)
		}
		if _, err := os.Stat(m.root.Path(devmode.LockWriteRecord)); err == nil {
			t.Fatal("a both-mainline unit left a lock-state write record")
		}
	})
}
