//go:build install && devtest

package devmode_test

// One full firmware check (owner decision 2026-10-31, DEVMODE-R4.md §3): the
// plan run hashes the firmware against the manifests; the install of that
// plan does not hash it again. It binds the plan to the firmware source by its
// identity, re-checks the plan's payload against the manifests, re-reads the
// unit, and hashes each image as it writes it.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/device/devicetest"
	"uc-pxx-installer/internal/devmode"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/pcdfw"
	"uc-pxx-installer/internal/plan"
)

// countingFW is a firmware source that counts the bytes read from it.
type countingFW struct {
	b []byte
	n *atomic.Int64
}

func (c countingFW) ReadAt(p []byte, off int64) (int, error) {
	n, err := bytes.NewReader(c.b).ReadAt(p, off)
	c.n.Add(int64(n))
	return n, err
}
func (c countingFW) Size() int64 { return int64(len(c.b)) }

func source(b []byte, n *atomic.Int64) func() (devmode.FWFile, error) {
	return func() (devmode.FWFile, error) { return countingFW{b, n}, nil }
}

// TestSingleFirmwareCheck: the install of a plan file reads the firmware once
// (to write it), the plan run once (pass 1). Before this change the install
// read it five times (pass 1 twice, each two reads, then the writes).
func TestSingleFirmwareCheck(t *testing.T) {
	bin, exe := bootctlBin(t)
	u := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe)
	size := int64(len(u.fx.FW))
	var planRead, instRead atomic.Int64
	o := u.opts("a")
	o.FW, o.FWSource = "test firmware", source(u.fx.FW, &planRead)
	o.JSONOut = filepath.Join(u.root.Dir, "run/plan.json")
	var out bytes.Buffer
	p, err := devmode.Plan(u.newEnv(), o, &out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	// pass 1 reads the file twice: the sha256 of the whole container, then
	// every segment decrypted and inflated
	if r := planRead.Load(); r < size+size/2 {
		t.Fatalf("the plan run read %d of %d bytes: no full pass 1", r, size)
	}
	o.JSONOut, o.PlanFile, o.FWSource = "", filepath.Join(u.root.Dir, "run/plan.json"), source(u.fx.FW, &instRead)
	_, iout, err := u.install(o)
	if err != nil {
		t.Fatalf("%v\n%s", err, iout)
	}
	u.checkDone(p)
	if r := instRead.Load(); r > size+size/10 {
		t.Fatalf("the install of the plan file read %d bytes of a %d-byte firmware: more than one pass", r, size)
	}
	if strings.Contains(iout, "pass 1: check the whole firmware") || !strings.Contains(iout, "the firmware is not hashed again") ||
		!strings.Contains(iout, "re-check: the plan has the same id "+p.ID) {
		t.Fatalf("output:\n%s", iout)
	}
	t.Logf("firmware %d bytes: the plan run read %d, the install of its plan %d", size, planRead.Load(), instRead.Load())
	// without --plan: one pass 1 here, then the writes
	u2 := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe)
	var both atomic.Int64
	o2 := u2.opts("a")
	o2.FW, o2.FWSource = "test firmware", source(u2.fx.FW, &both)
	if _, out, err := u2.install(o2); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if r := both.Load(); r < size+size/2 || r > 3*size+size/10 {
		t.Fatalf("an install without a plan file read %d bytes: want one pass 1 and the writes", r)
	}
}

// TestFirmwareIdentity: the plan binds the firmware file by its identity.
func TestFirmwareIdentity(t *testing.T) {
	bin, exe := bootctlBin(t)
	t.Run("the file changed after the plan", func(t *testing.T) {
		u := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe)
		p, pf := u.planFile("a")
		if !strings.HasPrefix(p.FirmwareIdent, "file size=") {
			t.Fatalf("firmware_ident %q", p.FirmwareIdent)
		}
		later := time.Now().Add(time.Minute)
		os.Chtimes(u.fw, later, later)
		o := u.opts("a")
		o.PlanFile = pf
		_, out, err := u.install(o)
		if err == nil || !strings.Contains(err.Error(), "the firmware is not the one that pass 1 checked") {
			t.Fatalf("err = %v\n%s", err, out)
		}
		u.untouched()
	})
	t.Run("the file changed during the install", func(t *testing.T) {
		u := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe)
		p, pf := u.planFile("a")
		o := u.opts("a")
		o.PlanFile = pf
		o.MaxWrite = sweepWrite
		o.Hooks = &install.Hooks{CutAfter: -1, Observe: func(ev install.Event) error {
			if ev.Write == 3 {
				later := time.Now().Add(time.Hour)
				os.Chtimes(u.fw, later, later)
			}
			return nil
		}}
		_, out, err := u.install(o)
		if err == nil || devmode.ExitCode(err) != 1 || !strings.Contains(err.Error(), "firmware rule: the firmware file changed") {
			t.Fatalf("err = %v\n%s", err, out)
		}
		cs := u.cutCheck(p, "the firmware changed")
		if cs.Boots != "keeper" || cs.Target != "disabled" {
			t.Fatalf("after the stop: %+v", cs)
		}
	})
	t.Run("a plan of another boot", func(t *testing.T) {
		u := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe)
		_, pf := u.planFile("a")
		os.WriteFile(u.root.Path(device.BootIDPath), []byte("11111111-2222-4333-8444-555555555555\n"), 0o644)
		o := u.opts("a")
		o.PlanFile = pf
		_, out, err := u.install(o)
		if err == nil || !strings.Contains(err.Error(), "the plan now has id") {
			t.Fatalf("err = %v\n%s", err, out)
		}
		u.untouched()
	})
	t.Run("a plan whose payload was edited and sealed again", func(t *testing.T) {
		u := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe)
		p, pf := u.planFile("a")
		p.Payload.Parts[0].SHA256 = strings.Repeat("0", 64)
		p.Seal()
		writePlan(t, pf, p)
		o := u.opts("a")
		o.PlanFile = pf
		_, out, err := u.install(o)
		if err == nil || !strings.Contains(err.Error(), "the plan's payload") {
			t.Fatalf("err = %v\n%s", err, out)
		}
		u.untouched()
	})
	t.Run("a plan without a firmware identity", func(t *testing.T) {
		u := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe)
		p, pf := u.planFile("a")
		p.FirmwareIdent = ""
		p.Seal()
		writePlan(t, pf, p)
		o := u.opts("a")
		o.PlanFile = pf
		if _, out, err := u.install(o); err == nil || !strings.Contains(err.Error(), "no firmware identity") {
			t.Fatalf("err = %v\n%s", err, out)
		}
		u.untouched()
	})
}

// TestInlineMismatch: a firmware whose bytes differ from what the plan's pass
// 1 saw (here: a source that keeps its identity, a damaged body in the
// middle) is found while it is written. The target is disabled before the
// first write, so the stop leaves the keeper booting, and the target is never
// armed.
func TestInlineMismatch(t *testing.T) {
	bin, exe := bootctlBin(t)
	u := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe)
	var n atomic.Int64
	o := u.opts("a")
	o.FW, o.FWSource = "test firmware", source(u.fx.FW, &n)
	o.JSONOut = filepath.Join(u.root.Dir, "run/plan.json")
	var out bytes.Buffer
	p, err := devmode.Plan(u.newEnv(), o, &out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	f, _ := pcdfw.Open(bytes.NewReader(u.fx.FW), int64(len(u.fx.FW)), u.fx.Keys)
	segs, _ := f.Segments()
	big := segs[0]
	for _, s := range segs {
		if s.Size > big.Size {
			big = s
		}
	}
	bad := append([]byte(nil), u.fx.FW...)
	at := big.Offset + pcdfw.HdrRegion + int64(big.Size-pcdfw.HdrRegion)/2
	for i := int64(0); i < 64; i++ {
		bad[at+i] ^= 0x5a
	}
	o.JSONOut, o.PlanFile, o.FWSource = "", filepath.Join(u.root.Dir, "run/plan.json"), source(bad, &n)
	_, iout, err := u.install(o)
	if err == nil || devmode.ExitCode(err) != 1 {
		t.Fatalf("err = %v (exit %d)\n%s", err, devmode.ExitCode(err), iout)
	}
	cs := u.cutCheck(p, "inline mismatch")
	if cs.Boots != "keeper" || cs.Target != "disabled" {
		t.Fatalf("after the stop: %+v", cs)
	}
	if strings.Contains(iout, "arm-trying") && !strings.Contains(iout, "STOP") {
		t.Fatalf("armed?\n%s", iout)
	}
	t.Logf("stopped at: %v", err)
	_ = json.Marshal
	_ = plan.ABLBA
}
