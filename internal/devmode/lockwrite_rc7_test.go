//go:build install && devtest

package devmode_test

// mainline-rc-7: the tests the RC-6 flow review asked for before any end-user
// use of the lock-state write. MINOR-E (a restore that fails, a restore that
// reads back other bytes: an exit-3 lock-state STOP with its own words, the
// record, every later plan of the boot held), NIT-F (a re-protect failure
// after the write: never verified, restored or held; a refusal before the run:
// no writer call, no start marker), NIT-R2 (a record of this boot that is not
// a verified 0x01: the install does not start, before keep-key and R(K)),
// MINOR-B (the child's STOP text names p16 and security.bak and reads the A/B
// sector back, not "the A/B state is not known").

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/devmode"
)

// lockStopText: the child's words of a lock-state STOP (flow review MINOR-B)
func lockStopText(t *testing.T, out string) {
	t.Helper()
	for _, want := range []string{
		"The lock-state write stopped: the security partition (p16) may not be as before.",
		"copy /run/uc-pxx-inst/optee/security.bak off the unit",
		"A/B sector read back now: 00414230",
		"slot B not written",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("the transcript has no %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "The A/B state is not known") {
		t.Fatalf("a lock-state STOP says the A/B state is not known\n%s", out)
	}
}

func TestDirectLockWriteTornRestore(t *testing.T) {
	bin, exe := bootctlBin(t)
	for _, c := range []struct {
		name string
		mk   func(m *mlu) (devmode.LockWriter, func())
	}{
		{"the restore cannot open security", func(m *mlu) (devmode.LockWriter, func()) {
			inner := stubWrite(m, writeStub{readBack: 2, touchSec: true})
			var node string
			return func(e *device.Env, d *device.Disk, from byte, say func(string)) devmode.LockWriteTEE {
				t := inner(e, d, from, say)
				sec, _ := d.Part("security")
				node = e.Path("/dev/" + sec.Dev)
				os.Chmod(node, 0o400) // the restore's open for writing fails
				return t
			}, func() { os.Chmod(node, 0o600) }
		}},
		{"the restore does not read back as the backup", func(m *mlu) (devmode.LockWriter, func()) {
			inner := stubWrite(m, writeStub{readBack: 2, touchSec: true})
			var stop int32
			done := make(chan struct{})
			return func(e *device.Env, d *device.Disk, from byte, say func(string)) devmode.LockWriteTEE {
					t := inner(e, d, from, say)
					sec, _ := d.Part("security")
					path := e.Path("/dev/" + sec.Dev)
					go func() { // a late writer: byte 0 of security keeps changing
						defer close(done)
						for atomic.LoadInt32(&stop) == 0 {
							if f, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
								f.WriteAt([]byte{0x5a}, 0)
								f.Close()
							}
							time.Sleep(200 * time.Microsecond)
						}
					}()
					return t
				}, func() {
					atomic.StoreInt32(&stop, 1)
					<-done
				}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := newMLU(t, dUnitReset, true, bin, exe)
			o := m.mlOpts()
			o.LockReader = countedLock(m, "0x00", nil)
			w, undo := c.mk(m)
			o.LockWriter = w
			_, out, err := m.dInstall(o)
			undo()
			if err == nil || !errors.Is(err, device.ErrABUnknown) || devmode.ExitCode(err) != 3 || !devmode.IsLockStop(err) ||
				!strings.Contains(err.Error(), "did not restore") || !strings.Contains(err.Error(), "LOCK-STATE WRITE STOPPED") {
				t.Fatalf("want an exit-3 lock-state STOP (the backup did not restore), got %v (exit %d)\n%s", err, devmode.ExitCode(err), out)
			}
			stoppedAfterRK(t, m)
			lockStopText(t, out)
			rec := readLockWrite(t, m)
			if rec["verified"] == true || rec["restored"] == true || rec["backup_sha256"] == "" {
				t.Fatalf("record %+v", rec)
			}
			if why := devmode.LockWriteHeld(m.env); why == "" || !strings.Contains(why, "did not restore") {
				t.Fatalf("LockWriteHeld %q", why)
			}
			o2 := m.mlOpts()
			o2.LockReader = countedLock(m, "0x01", nil)
			if _, _, err := m.dPlan(o2); devmode.ExitCode(err) != 3 {
				t.Fatalf("the next direct plan is not held: %v", err)
			}
		})
	}
}

// NIT-F (MINOR-2): the CA reads back 0x01, but the security node cannot be
// synced after the write (the re-protect fails): the write is never counted
// as verified; the restore is tried and, failing the same way, it is the
// exit-3 lock-state STOP.
func TestDirectLockWriteReprotectFails(t *testing.T) {
	bin, exe := bootctlBin(t)
	m := newMLU(t, dUnitReset, true, bin, exe)
	o := m.mlOpts()
	o.LockReader = countedLock(m, "0x00", nil)
	inner := stubWrite(m, writeStub{readBack: 1, touchSec: true})
	var node string
	o.LockWriter = func(e *device.Env, d *device.Disk, from byte, say func(string)) devmode.LockWriteTEE {
		t := inner(e, d, from, say)
		sec, _ := d.Part("security")
		node = e.Path("/dev/" + sec.Dev)
		os.Chmod(node, 0o000) // the re-protect's fsync open fails
		return t
	}
	_, out, err := m.dInstall(o)
	os.Chmod(node, 0o600)
	if devmode.ExitCode(err) != 3 || !devmode.IsLockStop(err) {
		t.Fatalf("a re-protect failure: %v (exit %d)\n%s", err, devmode.ExitCode(err), out)
	}
	if rec := readLockWrite(t, m); rec["verified"] == true || rec["restored"] == true {
		t.Fatalf("record %+v", rec)
	}
	stoppedAfterRK(t, m)
	lockStopText(t, out)
}

// NIT-F (MINOR-3): a refusal before the run (here: no confirm of what slot
// B holds, owner F1) calls no writer and leaves no start marker.
func TestDirectLockWriteRefusedBeforeRun(t *testing.T) {
	bin, exe := bootctlBin(t)
	m := newMLU(t, dUnitReset, true, bin, exe)
	var calls int32
	o := m.mlOpts()
	o.LockReader = countedLock(m, "0x00", nil)
	o.LockWriter = stubWrite(m, writeStub{readBack: 1, calls: &calls})
	o.LockWriteConfirmed = true // the lock-state agreement, but not the replace confirm
	_, out, err := m.dInstallAs(o)
	if err == nil || devmode.ExitCode(err) == 0 || calls != 0 {
		t.Fatalf("no replace confirm: %v, writer calls %d\n%s", err, calls, out)
	}
	if _, err := os.Stat(m.root.Path(devmode.LockWriteStarted)); err == nil {
		t.Fatal("a start marker without a write")
	}
	if !bytes.Equal(m.image(), m.before) {
		t.Fatal("a refused install wrote something")
	}
}

// NIT-R2: after a restored failure of this boot, a new install refuses before
// keep-key and R(K) ("the install does not start"); the writer is not called
// and nothing more is written.
func TestDirectLockWriteRetryRefusedEarly(t *testing.T) {
	bin, exe := bootctlBin(t)
	m := newMLU(t, dUnitReset, true, bin, exe)
	o := m.mlOpts()
	o.LockReader = countedLock(m, "0x00", nil)
	o.LockWriter = stubWrite(m, writeStub{readBack: 2, touchSec: true})
	if _, out, err := m.dInstall(o); devmode.ExitCode(err) != 1 {
		t.Fatalf("first: %v (exit %d)\n%s", err, devmode.ExitCode(err), out)
	}
	stoppedAfterRK(t, m)
	img := m.image()
	var calls int32
	o2 := m.mlOpts()
	o2.LockReader = countedLock(m, "0x00", nil)
	o2.LockWriter = stubWrite(m, writeStub{readBack: 1, calls: &calls})
	_, out, err := m.dInstall(o2)
	if err == nil || devmode.ExitCode(err) != 1 || calls != 0 || !bytes.Equal(m.image(), img) ||
		!strings.Contains(out, "the install does not start") || !strings.Contains(out, "did not verify") {
		t.Fatalf("the retry: %v (exit %d), writer calls %d, image changed %v\n%s", err, devmode.ExitCode(err), calls, !bytes.Equal(m.image(), img), out)
	}
}
