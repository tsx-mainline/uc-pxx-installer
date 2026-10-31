//go:build install && devtest

package devmode_test

// The A/B repair of the hold (DEVMODE-R4.md §2) on fake units, with the real
// uc-pxx-bootctl-inst (the build with `repair`) under UC_PXX_BOOTCTL_EXEC.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/device/devicetest"
	"uc-pxx-installer/internal/devmode"
	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/plan"
)

// abSector builds the first 32 bytes of a sector with a big-endian CRC, over
// the tail of base (bytes 32..511).
func abSector(base []byte, a, b [4]byte) []byte {
	s := append([]byte(nil), base...)
	copy(s[:32], make([]byte, 32))
	copy(s, "\x00AB0")
	s[4] = 1
	copy(s[8:], a[:])
	copy(s[12:], b[:])
	binary.BigEndian.PutUint32(s[28:], crc32.ChecksumIEEE(s[:28]))
	return s
}

// setAB puts a sector into the misc node file of the fake unit (the test's
// own fake eMMC, as a broken earlier write would have left it).
func (u *unit) setAB(sec []byte) {
	u.t.Helper()
	f, err := os.OpenFile(u.root.Node("misc"), os.O_WRONLY, 0)
	if err != nil {
		u.t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteAt(sec, (plan.ABLBA-plan.MiscStart)*512); err != nil {
		u.t.Fatal(err)
	}
}

func (u *unit) ab() []byte {
	u.t.Helper()
	b, err := u.root.ABSector()
	if err != nil {
		u.t.Fatal(err)
	}
	return b
}

// planFile makes the plan of the unit (keeper k) and writes it to a file.
func (u *unit) planFile(k string) (*plan.Plan, string) {
	p := u.plan(k)
	f := filepath.Join(u.root.Dir, "run/plan.json")
	writePlan(u.t, f, p)
	return p, f
}

func (u *unit) repairOpts(planPath string) devmode.RepairOptions {
	return devmode.RepairOptions{Device: device.EMMCPath, PlanFile: planPath, LogDir: filepath.Join(u.root.Dir, "run/repair"), Yes: true}
}

func (u *unit) repair(o devmode.RepairOptions) (*devmode.RepairResult, string, int) {
	var out bytes.Buffer
	res, err := devmode.Repair(u.newEnv(), o, &out)
	code := devmode.RepairExitCode(res, err)
	if err != nil {
		fmt.Fprintf(&out, "\nRESULT: %v\n", err)
	}
	return res, out.String(), code
}

// writeCalls are the write calls of the tool (not status, not dry runs).
func (u *unit) writeCalls() []string {
	var w []string
	for _, c := range u.bc.Calls {
		if c != "status" && !strings.HasSuffix(c, "--dry-run") {
			w = append(w, strings.SplitN(c, " --expect-sector", 2)[0])
		}
	}
	return w
}

// keeperBoots: the libavb choice of the sector is the keeper, which is
// successful, and the target is not bootable.
func keeperOnly(t testing.TB, sec []byte, k int) {
	t.Helper()
	a, _ := avbab.Parse(sec)
	if err := a.Validate(); err != nil || a.Pick() != k || !a.Slots[k].Keeper() || a.Slots[1-k].Bootable() {
		t.Fatalf("A/B %x: not keeper-only bootable (%v)", sec[:32], err)
	}
}

// brokenStates are sectors that need a repair (and one that is fine), over
// the tail of the unit's sector. k is the keeper.
func brokenStates(base []byte, k int) map[string][]byte {
	sl := func(p, t, s byte) [4]byte { return [4]byte{p, t, s, 0} }
	two := func(keeper, target [4]byte) []byte {
		if k == 0 {
			return abSector(base, keeper, target)
		}
		return abSector(base, target, keeper)
	}
	good := two(sl(15, 0, 1), sl(14, 0, 1))
	le := append([]byte(nil), good...)
	binary.LittleEndian.PutUint32(le[28:], crc32.ChecksumIEEE(le[:28]))
	crc := append([]byte(nil), good...)
	crc[30] ^= 0x10
	magic := append([]byte(nil), good...)
	magic[2] = 'X'
	zero := append(make([]byte, 32), base[32:]...)
	ff := append(bytes.Repeat([]byte{0xff}, 32), base[32:]...)
	d1 := two(sl(15, 0, 1), sl(0, 0, 0))
	torn := append(append([]byte(nil), d1[:13]...), good[13:]...) // slot B's priority new, its CRC old
	return map[string][]byte{
		"bad magic":                             magic,
		"crc little-endian":                     le,
		"crc wrong":                             crc,
		"first 32 bytes zero":                   zero,
		"first 32 bytes ff":                     ff,
		"keeper succ 1 tries 1 (unit-1 brick)":  two([4]byte{15, 1, 1, 0}, sl(14, 0, 1)),
		"no bootable slot":                      two(sl(15, 0, 0), sl(14, 0, 0)),
		"both disabled":                         two(sl(0, 0, 0), sl(0, 0, 0)),
		"torn S0 to D1 (13 new bytes)":          torn,
		"target trying, keeper disabled":        two(sl(0, 0, 0), sl(15, 7, 0)),
		"target successful next, keeper trying": two(sl(14, 3, 0), sl(15, 0, 1)),
	}
}

func TestRepairStates(t *testing.T) {
	bin, exe := bootctlBin(t)
	for _, sh := range []struct {
		name, keeper string
		k            int
		unit         fakeemmc.Unit
	}{{"keeper a", "a", 0, unitFwd}, {"keeper b", "b", 1, unitRev}} {
		u := newUnit(t, sh.unit, devicetest.Opts{}, bin, exe)
		p, pf := u.planFile(sh.keeper)
		for name, sec := range brokenStates(u.start, sh.k) {
			t.Run(sh.name+"/"+name, func(t *testing.T) {
				u.setAB(sec)
				u.bc.Calls = nil
				before := u.image()
				// the dry run: exit 5, nothing written, the prediction
				o := u.repairOpts(pf)
				o.Yes, o.DryRun = false, true
				res, out, code := u.repair(o)
				want, _ := plan.PredictRepair(sec, sh.k)
				if code != devmode.RepairExitWouldRepair || !bytes.Equal(res.Would, want) || !bytes.Equal(u.image(), before) || len(u.writeCalls()) != 0 {
					t.Fatalf("dry run: exit %d, would %x\n%s", code, res.Would, out)
				}
				res, out, code = u.repair(u.repairOpts(pf))
				if code != devmode.RepairExitSafe || res.Writes != 1 || !res.Class.NeedsWrite() || !strings.Contains(out, "RESULT: repaired") {
					t.Fatalf("exit %d, writes %d, class %s\n%s", code, res.Writes, res.Class, out)
				}
				after := u.ab()
				if !bytes.Equal(after, want) {
					t.Fatalf("A/B %x, want %x", after, want)
				}
				keeperOnly(t, after, sh.k)
				// nothing else on the unit changed
				img := u.image()
				copy(img[plan.ABLBA*512:], before[plan.ABLBA*512:(plan.ABLBA+1)*512])
				if !bytes.Equal(img, before) {
					t.Fatal("a byte outside the A/B sector changed")
				}
				if w := u.writeCalls(); len(w) != 1 || w[0] != "repair --keeper "+sh.keeper {
					t.Fatalf("tool write calls %q", w)
				}
				// a second repair: nothing to do
				if res, out, code := u.repair(u.repairOpts(pf)); code != 0 || res.Writes != 0 || res.Class != plan.RepairKeeperOK {
					t.Fatalf("second repair: exit %d writes %d %s\n%s", code, res.Writes, res.Class, out)
				}
				// and an install from the repaired state completes (keeper as before)
				u.setAB(after)
				o2 := u.opts(sh.keeper)
				if _, out, err := u.install(o2); err != nil {
					t.Fatalf("install after the repair: %v\n%s", err, out)
				}
				u.setAB(u.start)
				_ = p
			})
		}
	}
}

// TestRepairNotNeeded: valid states in which the keeper boots are never
// written (the install's S0, D1, D2, and a keeper that is trying but boots).
func TestRepairNotNeeded(t *testing.T) {
	bin, exe := bootctlBin(t)
	u := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe)
	_, pf := u.planFile("a")
	for name, c := range map[string]struct {
		a, b  [4]byte
		class plan.RepairClass
	}{
		"S0":                  {[4]byte{15, 0, 1}, [4]byte{14, 0, 1}, plan.RepairKeeperOK},
		"D1":                  {[4]byte{15, 0, 1}, [4]byte{0, 0, 0}, plan.RepairKeeperOK},
		"D2":                  {[4]byte{14, 0, 1}, [4]byte{15, 7, 0}, plan.RepairKeeperOK},
		"R(a)":                {[4]byte{15, 0, 1}, [4]byte{0, 0, 0}, plan.RepairKeeperOK},
		"keeper trying, next": {[4]byte{15, 3, 0}, [4]byte{0, 0, 0}, plan.RepairKeeperNext},
		"keeper trying, B 14": {[4]byte{15, 3, 0}, [4]byte{14, 0, 1}, plan.RepairKeeperNext},
	} {
		sec := abSector(u.start, c.a, c.b)
		u.setAB(sec)
		u.bc.Calls = nil
		before := u.image()
		res, out, code := u.repair(u.repairOpts(pf))
		if code != devmode.RepairExitSafe || res.Writes != 0 || res.Class != c.class || !strings.Contains(out, "RESULT: no repair needed") {
			t.Fatalf("%s: exit %d writes %d class %s\n%s", name, code, res.Writes, res.Class, out)
		}
		if !bytes.Equal(u.image(), before) || len(u.writeCalls()) != 0 {
			t.Fatalf("%s: written (%q)", name, u.bc.Calls)
		}
		for _, call := range u.bc.Calls {
			if strings.HasPrefix(call, "repair") {
				t.Fatalf("%s: the tool ran %q", name, call)
			}
		}
	}
}

// TestRepairRefusals: nothing is written unless every check passes.
func TestRepairRefusals(t *testing.T) {
	bin, exe := bootctlBin(t)
	for _, c := range []struct {
		name string
		edit func(u *unit, pf string) devmode.RepairOptions
		want string
	}{
		{"an unreadable sector", func(u *unit, pf string) devmode.RepairOptions {
			u.abErr = errors.New("input/output error")
			return u.repairOpts(pf)
		}, "an unreadable sector is never written"},
		{"the keeper's system changed", func(u *unit, pf string) devmode.RepairOptions {
			u.poke("system_a", 8192)
			return u.repairOpts(pf)
		}, "system_a has sha256"},
		{"the keeper's oem changed", func(u *unit, pf string) devmode.RepairOptions {
			u.poke("oem_a", 4096)
			return u.repairOpts(pf)
		}, "oem_a has sha256"},
		{"the keeper's uboot changed", func(u *unit, pf string) devmode.RepairOptions {
			u.poke("uboot_a", 512)
			return u.repairOpts(pf)
		}, "uboot_a has sha256"},
		{"a plan of another boot", func(u *unit, pf string) devmode.RepairOptions {
			os.WriteFile(u.root.Path(device.BootIDPath), []byte("11111111-2222-4333-8444-555555555555\n"), 0o644)
			return u.repairOpts(pf)
		}, "the plan was made in another boot"},
		{"a plan of another RAM image", func(u *unit, pf string) devmode.RepairOptions {
			other := strings.Replace(devicetest.RAMCmdline, "uc-pxx.id="+fakeToken, "uc-pxx.id=p10inst-00a9ff", 1)
			os.WriteFile(u.root.Path("/proc/cmdline"), []byte(other+"\n"), 0o644)
			return u.repairOpts(pf)
		}, "the plan was made in another RAM image"},
		{"an edited plan", func(u *unit, pf string) devmode.RepairOptions {
			b, _ := os.ReadFile(pf)
			os.WriteFile(pf, bytes.Replace(b, []byte(`"keeper": "a"`), []byte(`"keeper": "b"`), 1), 0o644)
			return u.repairOpts(pf)
		}, "does not match its id"},
		{"a bootctl still runs", func(u *unit, pf string) devmode.RepairOptions {
			os.MkdirAll(u.root.Path("/proc/4242"), 0o755)
			os.WriteFile(u.root.Path("/proc/4242/cmdline"), []byte("/run/p10inst/bootctl-1/uc-pxx-bootctl-inst\x00arm-trying\x00"), 0o644)
			return u.repairOpts(pf)
		}, "uc-pxx-bootctl-inst still runs (pid 4242"},
		{"not confirmed", func(u *unit, pf string) devmode.RepairOptions {
			o := u.repairOpts(pf)
			o.Yes, o.Confirm = false, strings.NewReader("repair\n")
			return o
		}, "not confirmed"},
		{"no plan", func(u *unit, pf string) devmode.RepairOptions {
			o := u.repairOpts(pf)
			o.PlanFile = ""
			return o
		}, "needs --plan"},
		{"a log under /proc", func(u *unit, pf string) devmode.RepairOptions {
			o := u.repairOpts(pf)
			o.LogDir = "/proc/self"
			return o
		}, "is under /proc"},
	} {
		t.Run(c.name, func(t *testing.T) {
			u := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe)
			_, pf := u.planFile("a")
			bad := brokenStates(u.start, 0)["crc wrong"]
			u.setAB(bad)
			before := u.image()
			u.bc.Calls = nil
			o := c.edit(u, pf)
			res, out, code := u.repair(o)
			if code != devmode.RepairExitRefused || !strings.Contains(out, c.want) || res.Writes != 0 {
				t.Fatalf("exit %d writes %d, want %q\n%s", code, res.Writes, c.want, out)
			}
			if w := u.writeCalls(); len(w) != 0 {
				t.Fatalf("the tool wrote: %q", w)
			}
			if !bytes.Equal(u.ab(), bad) {
				t.Fatal("the A/B sector changed")
			}
			_ = before
		})
	}
}

// poke changes one byte of a partition (a keeper that is not intact).
func (u *unit) poke(part string, off int64) {
	f, err := os.OpenFile(u.root.Node(part), os.O_RDWR, 0)
	if err != nil {
		u.t.Fatal(err)
	}
	defer f.Close()
	b := make([]byte, 1)
	f.ReadAt(b, off)
	b[0] ^= 0x5a
	f.WriteAt(b, off)
}

// TestRepairMismatch: the tool misbehaves on its write. The read-back is
// compared with the prediction byte for byte; at most one more write, only
// when the read-back still needs a repair; else a stop (exit 3) with the
// hold text, or exit 1 when nothing changed.
func TestRepairMismatch(t *testing.T) {
	bin, exe := bootctlBin(t)
	for _, c := range []struct {
		mode   string
		once   bool
		code   int
		writes int
		final  string // "R", "before", "other"
	}{
		{"garbage", true, 0, 2, "R"},          // bad CRC read back: one more write, which works
		{"garbage", false, 3, 2, "other"},     // twice: stop
		{"wrong", true, 0, 2, "R"},            // keeper not successful, no bootable slot: one more write
		{"wrong", false, 3, 2, "other"},       //
		{"tail", false, 3, 1, "other"},        // valid, keeper successful, byte 300 changed: no second write
		{"lie", false, 0, 1, "R"},             // exit 1 but wrote exactly R: the bytes count
		{"refuse", true, 0, 2, "R"},           // did not take once
		{"refuse", false, 1, 2, "before"},     // did not take twice: nothing changed
		{"unknown", false, 3, 2, "before"},    // exit 3 with no write, twice: the tool says unknown
		{"unknown-wrote", false, 0, 1, "R"},   // exit 3 after the right write
		{"signal", true, 0, 2, "R"},           // ended by a signal before its write
		{"signal-wrote", false, 0, 1, "R"},    // ended by a signal after its write
		{"torn-rc3:1", true, 0, 2, "R"},       // a torn write, the power stayed on
		{"torn-rc3:16", true, 0, 2, "R"},      //
		{"torn-rc3:31", true, 0, 2, "R"},      //
		{"torn-rc3:16", false, 3, 2, "other"}, // torn twice
		{"torn-rc3:32", false, 0, 1, "R"},     // 32 bytes new: the whole AvbABData, the tail was the same
		{"torn-rc3:0", true, 0, 2, "R"},       // nothing written, exit 3
	} {
		name := fmt.Sprintf("%s once=%v", c.mode, c.once)
		t.Run(name, func(t *testing.T) {
			u := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe)
			_, pf := u.planFile("a")
			bad := brokenStates(u.start, 0)["no bootable slot"]
			u.setAB(bad)
			u.bc.Mode, u.bc.Once = c.mode, c.once
			res, out, code := u.repair(u.repairOpts(pf))
			if code != c.code || res.Writes != c.writes {
				t.Fatalf("exit %d writes %d, want %d %d\n%s", code, res.Writes, c.code, c.writes, out)
			}
			hold := strings.Contains(out, "DO NOT POWER OFF")
			safe := strings.Contains(out, "safe to power off")
			if code == 3 && (!hold || safe) || code != 3 && hold || code == 0 && !safe {
				t.Fatalf("exit %d: hold text %v, safe text %v\n%s", code, hold, safe, out)
			}
			want, _ := plan.PredictRepair(bad, 0)
			got := u.ab()
			switch c.final {
			case "R":
				if !bytes.Equal(got, want) {
					t.Fatalf("A/B %x, want R %x", got[:32], want[:32])
				}
				keeperOnly(t, got, 0)
			case "before":
				if !bytes.Equal(got, bad) {
					t.Fatalf("A/B %x, want it unchanged", got[:32])
				}
			default:
				if bytes.Equal(got, want) || bytes.Equal(got, bad) {
					t.Fatalf("A/B %x: a mismatch case ended on R or on the old sector", got[:32])
				}
			}
			if len(u.writeCalls()) != c.writes || c.writes > 2 {
				t.Fatalf("tool write calls %q", u.writeCalls())
			}
		})
	}
}

// TestRepairPowerCut cuts the power at every point of the repair write: before
// it, inside it (torn: the first N bytes of the new sector, N = 1..511), and
// after it before the read-back. After a cut the unit boots what is on the
// sector, so the check is the sector alone: the number of bootable slots
// (libavb rule, decoded here with package avbab) is never lower than before,
// except in a torn write that is cut inside bytes 1..31 of AvbABData. That
// case exists for every single-sector A/B write (also the install's disable
// and arm); it needs the sector write to be atomic, which the eMMC's reliable
// write is meant to give (DEVMODE-R4.md §2.5).
func TestRepairPowerCut(t *testing.T) {
	bin, exe := bootctlBin(t)
	u := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe)
	_, pf := u.planFile("a")
	bootable := func(sec []byte) int {
		a, _ := avbab.Parse(sec)
		if a.Validate() != nil {
			return 0
		}
		n := 0
		for _, s := range a.Slots {
			if s.Bootable() {
				n++
			}
		}
		return n
	}
	points, residual := 0, 0
	for name, bad := range brokenStates(u.start, 0) {
		want, _ := plan.PredictRepair(bad, 0)
		cuts := []string{"before", "after"}
		for n := 1; n < 512; n++ {
			if n <= 40 || n%37 == 0 || n == 511 {
				cuts = append(cuts, "torn:"+strconv.Itoa(n))
			}
		}
		for _, cut := range cuts {
			u.setAB(bad)
			u.bc.Mode, u.bc.Once = "", false
			o := u.repairOpts(pf)
			switch {
			case cut == "before":
				o.Hooks = &install.Hooks{CutAfter: 0}
			case cut == "after":
				o.Hooks = &install.Hooks{CutAfter: -1, Observe: func(install.Event) error { return install.ErrPowerCut }}
			default:
				u.bc.Mode = cut
			}
			var outb bytes.Buffer
			_, err := devmode.Repair(u.newEnv(), o, &outb)
			if !errors.Is(err, install.ErrPowerCut) {
				t.Fatalf("%s, cut %s: %v\n%s", name, cut, err, outb.String())
			}
			got := u.ab()
			points++
			switch {
			case cut == "before":
				if !bytes.Equal(got, bad) {
					t.Fatalf("%s: a cut before the write changed the sector", name)
				}
			case cut == "after", cut == "torn:"+strconv.Itoa(32) || strings.HasPrefix(cut, "torn:") && atoiTorn(cut) >= 32:
				if !bytes.Equal(got, want) {
					t.Fatalf("%s, cut %s: %x, want R", name, cut, got[:32])
				}
			default:
				if !bytes.Equal(got[32:], bad[32:]) {
					t.Fatalf("%s, cut %s: the tail changed", name, cut)
				}
			}
			// the cardinal rule: never zero bootable slots where there was
			// one (the repair itself may take a bootable target away: that
			// is R)
			if b0, b1 := bootable(bad), bootable(got); b0 > 0 && b1 == 0 {
				if n := atoiTorn(cut); n < 1 || n > 31 {
					t.Fatalf("%s, cut %s: no bootable slot, %d before", name, cut, b0)
				}
				residual++
			}
			if b1 := bootable(got); bootable(bad) == 0 && b1 == 0 && !(atoiTorn(cut) >= 0 && atoiTorn(cut) < 32 || cut == "before") {
				t.Fatalf("%s, cut %s: still no bootable slot", name, cut)
			}
		}
	}
	t.Logf("repair power-cut sweep: %d cut points; %d of them (torn inside bytes 1..31, from a state with a bootable target) leave no bootable slot", points, residual)
}

func atoiTorn(cut string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(cut, "torn:"))
	if err != nil || !strings.HasPrefix(cut, "torn:") {
		return -1
	}
	return n
}

// The kill sweep of the repair: p10inst repair is SIGKILLed right before the
// tool's write, and right after it (before the read-back). The tool is not
// killed (it runs in its own session on a unit; here it runs in the killed
// process, so "after" means its write is done). Then the repair runs again
// in the same boot: it writes once (killed before) or finds R (killed after).
const repairChildEnv = "P10INST_REPAIR_CHILD"

func repairChild() int {
	f := strings.Split(os.Getenv(repairChildEnv), "|")
	root, pf, when := f[0], f[1], f[2]
	r := devicetest.Reopen(root, small)
	bc := &devicetest.Bootctl{Root: r, Exec: os.Getenv("UC_PXX_BOOTCTL_EXEC"), Tmp: filepath.Dir(root)}
	e, err := device.NewForTest(device.TestOptions{Root: root, RegularFiles: true, Layout: small, Runner: bc,
		Manifests: fixture.Manifests, ApprovedResources: []string{fixture.Resource}})
	if err != nil {
		fmt.Println(err)
		return 2
	}
	kill := func() {
		syscall.Kill(os.Getpid(), syscall.SIGKILL)
		select {}
	}
	o := devmode.RepairOptions{Device: device.EMMCPath, PlanFile: pf, LogDir: filepath.Join(root, "run/repair"), Yes: true}
	switch when {
	case "before":
		o.BeforeWrite = kill
	case "after":
		o.Hooks = &install.Hooks{CutAfter: -1, Observe: func(install.Event) error { kill(); return nil }}
	}
	var out bytes.Buffer
	res, err := devmode.Repair(e, o, &out)
	fmt.Print(out.String())
	return devmode.RepairExitCode(res, err)
}

func TestRepairKillSweep(t *testing.T) {
	bin, exe := bootctlBin(t)
	n := 0
	for name, bad := range brokenStates(make([]byte, 512), 0) {
		for _, when := range []string{"before", "after"} {
			u := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe)
			_, pf := u.planFile("a")
			bad := append(append([]byte(nil), bad[:32]...), u.start[32:]...)
			u.setAB(bad)
			cmd := exec.Command(os.Args[0], "-test.run=^$")
			cmd.Env = append(os.Environ(), repairChildEnv+"="+strings.Join([]string{u.root.Dir, pf, when}, "|"))
			out, err := cmd.CombinedOutput()
			var ee *exec.ExitError
			if !errors.As(err, &ee) || !ee.Sys().(syscall.WaitStatus).Signaled() {
				t.Fatalf("%s, kill %s: the child was not killed: %v\n%s", name, when, err, out)
			}
			want, _ := plan.PredictRepair(bad, 0)
			got := u.ab()
			if when == "before" && !bytes.Equal(got, bad) || when == "after" && !bytes.Equal(got, want) {
				t.Fatalf("%s, kill %s: A/B %x", name, when, got[:32])
			}
			// the repair again, in the same boot (the A/B lock died with the child)
			res, out2, code := u.repair(u.repairOpts(pf))
			wantWrites := map[string]int{"before": 1, "after": 0}[when]
			if code != 0 || res.Writes != wantWrites || !bytes.Equal(u.ab(), want) {
				t.Fatalf("%s, kill %s: the repair again: exit %d writes %d\n%s", name, when, code, res.Writes, out2)
			}
			n++
		}
	}
	t.Logf("repair kill sweep: %d kills, each repaired by the next run", n)
}

// TestDeviceHoldRepair: an install whose A/B write goes wrong ends with
// exit 3 (the hold). The repair then makes the keeper the only bootable slot,
// and an install from there completes.
func TestDeviceHoldRepair(t *testing.T) {
	bin, exe := bootctlBin(t)
	for _, c := range []struct {
		mode  string
		armed bool // the bad write is the arm (else the disable)
	}{
		{"garbage", false},
		{"wrong", false},
		{"tail", false},
		{"torn-rc3:13", false},
		{"torn-rc3:29", false},
		{"garbage", true},
		{"torn-rc3:12", true},
	} {
		t.Run(fmt.Sprintf("%s at the %s", c.mode, map[bool]string{false: "disable", true: "arm"}[c.armed]), func(t *testing.T) {
			u := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe)
			p, pf := u.planFile("a")
			o := u.opts("a")
			o.PlanFile = pf
			u.bc.Mode, u.bc.Once = c.mode, !c.armed
			if c.armed {
				// the mode only on the second write call (the arm): the
				// first one is normal
				u.bc.Mode = ""
				o.Hooks = &install.Hooks{CutAfter: -1, Observe: func(ev install.Event) error {
					if ev.Op == "ab" {
						u.bc.Mode, u.bc.Once, u.bc.Writes = c.mode, true, 0
					}
					return nil
				}}
			}
			_, out, err := u.install(o)
			if devmode.ExitCode(err) != 3 || !strings.Contains(out, "DO NOT POWER OFF") {
				t.Fatalf("install: exit %d, want 3 (the hold): %v\n%s", devmode.ExitCode(err), err, out)
			}
			u.bc.Mode, u.bc.Once = "", false
			res, rout, code := u.repair(u.repairOpts(pf))
			switch {
			case c.mode == "tail":
				// the first 32 bytes are D1 (valid, keeper successful): no
				// repair is needed; only bytes 32..511 differ
				if code != 0 || res.Writes != 0 || res.Class != plan.RepairKeeperOK {
					t.Fatalf("repair: exit %d writes %d %s\n%s", code, res.Writes, res.Class, rout)
				}
			default:
				if code != 0 || res.Writes != 1 {
					t.Fatalf("repair: exit %d writes %d %s\n%s", code, res.Writes, res.Class, rout)
				}
				keeperOnly(t, u.ab(), 0)
			}
			// install again from the repaired unit: done, the keeper untouched
			o2 := u.opts("a")
			if _, out, err := u.install(o2); err != nil {
				t.Fatalf("install after the repair: %v\n%s", err, out)
			}
			_ = p
		})
	}
}
