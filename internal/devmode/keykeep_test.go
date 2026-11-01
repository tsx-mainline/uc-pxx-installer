//go:build install && devtest

package devmode_test

// The keep-key step (KEY-KEEP.md, owner answers 2026-10-31) on fake units.
// The fake firmware's key is made up (package pcdfwtest): no real key in a
// test.

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/device/devicetest"
	"uc-pxx-installer/internal/devmode"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/keykeep"
	"uc-pxx-installer/internal/plan"
	"uc-pxx-installer/internal/status"
)

var openGate = &keykeep.Gate{Open: true, Why: "test: the checks passed", SHA256: strings.Repeat("ab", 32)}

// closedGate: no keep-key step. For the tests of the A/B steps whose write
// numbers and step counts start at the disable (the embedded gate is open, so a
// unit whose key is only in the target gets a keep-key step first).
var closedGate = &keykeep.Gate{Why: "the read-only checks of KEY-KEEP.md §2.1 have not all passed (open: android-no-backup)", SHA256: strings.Repeat("cd", 32)}

// closeGate gives the unit an environment with the keep-key gate closed.
func (u *unit) closeGate() *unit {
	u.gate = closedGate
	u.env = u.newEnv()
	return u
}

// newKKUnit is a fake unit whose environment has the keep-key gate open.
func newKKUnit(t *testing.T, bin []byte, exe string) *unit {
	u := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe)
	u.gate = openGate
	u.env = u.newEnv()
	return u
}

func (u *unit) recordLBAs() [2]int64 {
	e := u.root.Layout
	for _, x := range e.Entries {
		if x.Name == "backup" {
			l, _ := keykeep.LBAs(x.Start, x.Size)
			return l
		}
	}
	u.t.Fatal("no backup in the fake layout")
	return [2]int64{}
}

func (u *unit) recordSector(i int) []byte {
	img := u.image()
	l := u.recordLBAs()[i]
	return img[l*512 : (l+1)*512]
}

func (u *unit) setRecordSector(i int, b []byte) {
	l := u.recordLBAs()[i]
	for _, x := range u.root.Layout.Entries {
		if x.Name == "backup" {
			f, err := os.OpenFile(u.root.Node("backup"), os.O_WRONLY, 0)
			if err != nil {
				u.t.Fatal(err)
			}
			f.WriteAt(b, (l-x.Start)*512)
			f.Close()
		}
	}
	u.before = u.image()
}

// noKey: no output, log, transcript or plan file of the run holds a byte
// window of the key or of the record.
func noKey(t *testing.T, u *unit, texts ...string) {
	t.Helper()
	k := u.fx.Keys
	needles := [][]byte{k.Head[:8], k.Head[8:], k.IV[:8], k.IV[8:],
		[]byte(hex.EncodeToString(k.Head[:])[:16]), []byte(hex.EncodeToString(k.IV[:])[:16]),
		[]byte(strings.ToUpper(hex.EncodeToString(k.Head[:])[:16])), []byte(keykeep.Magic)}
	var all [][]byte
	for _, s := range texts {
		all = append(all, []byte(s))
	}
	filepath.Walk(filepath.Join(u.root.Dir, "run"), func(p string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() && !strings.HasSuffix(p, "keys.txt") {
			b, _ := os.ReadFile(p)
			all = append(all, b)
		}
		return nil
	})
	for i, b := range all {
		for _, n := range needles {
			if bytes.Contains(b, n) {
				t.Fatalf("output %d holds a piece of the key or the record magic (%q)", i, n)
			}
		}
	}
}

// TestKeyKeepEmbeddedGate: the embedded checks.json (KEY-KEEP.md §9, §10: all
// three checks passed, check 3 with a condition) opens the gate; the plan
// carries the condition and the sha of the checks.
func TestKeyKeepEmbeddedGate(t *testing.T) {
	bin, exe := bootctlBin(t)
	u := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe)
	p := u.plan("a")
	g := keykeep.TheGate()
	if p.KeyKeep == nil || p.KeyKeep.State != "keep" || !p.KeyKeep.Gate.Open || p.KeyKeep.Gate.SHA256 != g.SHA256 ||
		len(p.KeyKeep.Gate.Conditions) != 1 || !strings.Contains(p.KeyKeep.Gate.Conditions[0], "SD update card") || p.Steps[0].Op != "keep-key" {
		t.Fatalf("key_keep %+v, first step %s", p.KeyKeep, p.Steps[0].Op)
	}
	js, _ := jsonMarshal(p)
	if !strings.Contains(string(js), `"conditions"`) || !strings.Contains(string(js), "read backup again before trusting the record") {
		t.Fatal("the plan file does not hold the condition of the gate")
	}
}

// TestKeyKeepGateClosed: with the gate closed nothing is written to backup.
func TestKeyKeepGateClosed(t *testing.T) {
	bin, exe := bootctlBin(t)
	u := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe).closeGate()
	p := u.plan("a")
	if p.KeyKeep == nil || p.KeyKeep.State != "gated" || p.KeyKeep.Gate.Open || !strings.Contains(p.KeyKeep.Why, "android-no-backup") {
		t.Fatalf("key_keep %+v", p.KeyKeep)
	}
	if p.Steps[0].Op != "bootctl" {
		t.Fatalf("a keep-key step with the gate closed")
	}
	if _, out, err := u.install(u.opts("a")); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	u.checkDone(p) // nothing outside the target and the A/B sector changed: backup untouched
}

func TestKeyKeep(t *testing.T) {
	bin, exe := bootctlBin(t)
	u := newKKUnit(t, bin, exe)
	p := u.plan("a")
	if p.KeyKeep == nil || p.KeyKeep.State != "keep" || p.Steps[0].Op != "keep-key" || p.Steps[1].Op != "bootctl" ||
		p.Steps[0].KeepKey.Before != [2]keykeep.Class{keykeep.Empty, keykeep.Empty} {
		t.Fatalf("plan: key_keep %+v, first steps %s %s", p.KeyKeep, p.Steps[0].Op, p.Steps[1].Op)
	}
	js, _ := jsonMarshal(p)
	_, out, err := u.install(u.opts("a"))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	u.checkDone(p)
	rec := keykeep.Encode(u.fx.Keys)
	for i := 0; i < 2; i++ {
		if !bytes.Equal(u.recordSector(i), rec) {
			t.Fatalf("record copy %d not written", i+1)
		}
	}
	// every other sector of backup is unchanged (zero)
	img := u.image()
	l := u.recordLBAs()
	for lba := l[0] - 2048 + 1; lba <= l[1]; lba++ {
		if lba != l[0] && lba != l[1] && !bytes.Equal(img[lba*512:(lba+1)*512], make([]byte, 512)) {
			t.Fatalf("backup LBA %d changed", lba)
		}
	}
	noKey(t, u, out, string(js))
	// the status row (the status image reads it, read-only)
	r := status.Collect(status.Source{Root: u.root.Dir, Manifests: u.fx.Manifests})
	if r.KeyKept != "yes" || !strings.Contains(r.Text(), "Firmware key kept on the unit: yes") {
		t.Fatalf("status: %q", r.KeyKept)
	}
	noKey(t, u, r.Text())
	// the next plan: kept already, no step
	if p2 := u.plan("a"); p2.KeyKeep.State != "kept" || p2.Steps[0].Op == "keep-key" {
		t.Fatalf("second plan: %+v", p2.KeyKeep)
	}
}

// TestKeyKeepNoKeyFile: after a stop inside the system write, the next
// install finds the key on the unit, without the key file.
func TestKeyKeepNoKeyFile(t *testing.T) {
	bin, exe := bootctlBin(t)
	u := newKKUnit(t, bin, exe)
	p := u.plan("a")
	o := u.opts("a")
	o.MaxWrite = sweepWrite
	sys := false
	o.Hooks = &install.Hooks{CutAfter: -1, Observe: func(ev install.Event) error {
		if ev.Partition == "system_b" && !sys {
			sys = true
			return install.ErrPowerCut
		}
		return nil
	}}
	if _, out, err := u.install(o); !errors.Is(err, install.ErrPowerCut) {
		t.Fatalf("%v\n%s", err, out)
	}
	u.cutCheck(p, "cut in system_b")
	u.env = u.newEnv()
	o2 := u.opts("a")
	o2.Keys = ""
	p2 := u.plan2(o2)
	if p2.KeySource != devmode.KeySourceRecord || p2.KeyKeep.State != "kept" {
		t.Fatalf("the plan without a key file: key source %q, key_keep %+v", p2.KeySource, p2.KeyKeep)
	}
	_, out, err := u.install(o2)
	if err != nil {
		t.Fatalf("install without a key file: %v\n%s", err, out)
	}
	noKey(t, u, out)
}

func (u *unit) plan2(o devmode.Options) *plan.Plan {
	var out bytes.Buffer
	p, err := devmode.Plan(u.newEnv(), o, &out)
	if err != nil {
		u.t.Fatalf("plan: %v\n%s", err, out.String())
	}
	return p
}

// TestKeyKeepSectors: what is in the record sectors decides; foreign data and
// another key's record are never written over (owner Q4, Q6).
func TestKeyKeepSectors(t *testing.T) {
	bin, exe := bootctlBin(t)
	for _, c := range []struct {
		name  string
		s0    []byte
		state string
	}{
		{"foreign data", append([]byte("some data"), make([]byte, 503)...), "not-possible"},
		{"ff", bytes.Repeat([]byte{0xff}, 512), "keep"},
		{"another key's record", nil, "not-possible"},
		{"a torn record of this key", nil, "keep"},
	} {
		t.Run(c.name, func(t *testing.T) {
			u := newKKUnit(t, bin, exe)
			s0 := c.s0
			rec := keykeep.Encode(u.fx.Keys)
			switch c.name {
			case "another key's record":
				k := u.fx.Keys
				k.Head[0] ^= 1
				s0 = keykeep.Encode(k)
			case "a torn record of this key":
				s0 = append(append([]byte(nil), rec[:100]...), make([]byte, 412)...)
			}
			u.setRecordSector(0, s0)
			p := u.plan("a")
			if p.KeyKeep.State != c.state {
				t.Fatalf("state %s, want %s (%s)", p.KeyKeep.State, c.state, p.KeyKeep.Why)
			}
			_, out, err := u.install(u.opts("a"))
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			u.checkDone(p)
			if c.state == "not-possible" {
				if !bytes.Equal(u.recordSector(0), s0) || !bytes.Equal(u.recordSector(1), make([]byte, 512)) {
					t.Fatal("a record sector was written")
				}
				found := false
				for _, n := range p.Notes {
					found = found || strings.Contains(n, "The key is not kept on the unit")
				}
				if !found {
					t.Fatalf("no plan note: %q", p.Notes)
				}
			} else if !bytes.Equal(u.recordSector(0), rec) || !bytes.Equal(u.recordSector(1), rec) {
				t.Fatal("the record was not kept")
			}
		})
	}
}

// TestKeyKeepPowerCut: a cut before each of the two record writes (clean and
// torn) and after them, before the disable: the A/B sector is S0 and the
// unit boots as before; the next install completes, without the key file when
// a whole copy was kept.
func TestKeyKeepPowerCut(t *testing.T) {
	bin, exe := bootctlBin(t)
	for _, torn := range []bool{false, true} {
		for k := 0; k <= 2; k++ {
			t.Run(fmt.Sprintf("cut before write %d torn %v", k+1, torn), func(t *testing.T) {
				u := newKKUnit(t, bin, exe)
				p := u.plan("a")
				o := u.opts("a")
				o.Hooks = &install.Hooks{CutAfter: k, Torn: torn}
				if _, out, err := u.install(o); !errors.Is(err, install.ErrPowerCut) {
					t.Fatalf("%v\n%s", err, out)
				}
				cs := u.cutCheck(p, "keep-key cut")
				if cs.Boots != "keeper" || !bytes.Equal(u.ab(), u.start) {
					t.Fatalf("after the cut: %+v, A/B %x", cs, u.ab()[:32])
				}
				o2 := u.opts("a")
				if k >= 1 {
					o2.Keys = "" // copy 1 is whole
				}
				u.env = u.newEnv()
				if _, out, err := u.install(o2); err != nil {
					t.Fatalf("resume: %v\n%s", err, out)
				}
				rec := keykeep.Encode(u.fx.Keys)
				if !bytes.Equal(u.recordSector(0), rec) || !bytes.Equal(u.recordSector(1), rec) {
					t.Fatal("the record is not whole after the resume")
				}
			})
		}
	}
}

var _ = device.EMMCPath
