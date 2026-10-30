package plan_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/manifest"
	"uc-pxx-installer/internal/nvram/nvramtest"
	"uc-pxx-installer/internal/plan"
)

func raw(a, b [3]byte) [32]byte {
	var r [32]byte
	copy(r[:], fakeemmc.ABSector([2][3]byte{a, b}, nil))
	return r
}

func TestPredict(t *testing.T) {
	s := func(a, b [3]byte) [32]byte { return raw(a, b) }
	cases := []struct {
		name     string
		cur      [32]byte
		op       string // "disable" or "arm"
		slot     int
		lower    bool
		want     [32]byte
		errMatch string
	}{
		{"disable b", s([3]byte{15, 0, 1}, [3]byte{14, 0, 1}), "disable", 1, false, s([3]byte{15, 0, 1}, [3]byte{}), ""},
		{"disable a", s([3]byte{14, 0, 1}, [3]byte{15, 0, 1}), "disable", 0, false, s([3]byte{}, [3]byte{15, 0, 1}), ""},
		{"disable again: no change", s([3]byte{15, 0, 1}, [3]byte{}), "disable", 1, false, s([3]byte{15, 0, 1}, [3]byte{}), ""},
		{"disable, keeper trying", s([3]byte{15, 7, 0}, [3]byte{14, 0, 1}), "disable", 1, false, [32]byte{}, "not successful"},
		{"arm b, keeper 15 lowered", s([3]byte{15, 0, 1}, [3]byte{}), "arm", 1, true, s([3]byte{14, 0, 1}, [3]byte{15, 7, 0}), ""},
		{"arm b, keeper 14", s([3]byte{14, 0, 1}, [3]byte{}), "arm", 1, true, s([3]byte{14, 0, 1}, [3]byte{15, 7, 0}), ""},
		{"arm a", s([3]byte{}, [3]byte{15, 0, 1}), "arm", 0, true, s([3]byte{15, 7, 0}, [3]byte{14, 0, 1}), ""},
		{"arm, 15 without lower", s([3]byte{15, 0, 1}, [3]byte{}), "arm", 1, false, [32]byte{}, "priority 15"},
		{"arm, already armed", s([3]byte{14, 0, 1}, [3]byte{15, 7, 0}), "arm", 1, true, s([3]byte{14, 0, 1}, [3]byte{15, 7, 0}), ""},
	}
	for _, c := range cases {
		var got [32]byte
		var err error
		if c.op == "disable" {
			got, err = plan.PredictDisable(c.cur, c.slot)
		} else {
			got, err = plan.PredictArm(c.cur, c.slot, c.lower)
		}
		if c.errMatch != "" {
			if err == nil || !strings.Contains(err.Error(), c.errMatch) {
				t.Errorf("%s: err %v", c.name, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: got %x err %v, want %x", c.name, got, err, c.want)
		}
		a, _ := avbab.Parse(got[:])
		if a.Validate() != nil {
			t.Errorf("%s: invalid result", c.name)
		}
	}
}

func TestCheckWriteRange(t *testing.T) {
	tb := &gpt.Table{DiskSectors: 80000, FirstUsable: 34, LastUsable: 79966, Entries: []gpt.Entry{
		{Index: 1, Name: "uboot_b", Start: 24576, End: 24576 + 511},
		{Index: 2, Name: "misc", Start: 49152, End: 49152 + 8191},
		{Index: 3, Name: "boot_b", Start: 59008, End: 59008 + 511},
		{Index: 4, Name: "boot_a", Start: 58496, End: 58496 + 511},
		{Index: 5, Name: "cfg", Start: 59400, End: 59600},  // overlaps boot_b
		{Index: 6, Name: "dtbo_b", Start: 1000, End: 3000}, // starts inside sectors 0..2047
		{Index: 7, Name: "vbmeta_b", Start: 49150, End: 49160},
		{Index: 8, Name: "oem_b", Start: 79900, End: 79990}, // ends in the backup GPT
		{Index: 9, Name: "userdata", Start: 70000, End: 79000},
	}}
	ok := func(name string, off, n int64) {
		if err := plan.CheckWriteRange(tb, "b", name, off, n); err != nil {
			t.Errorf("%s %d+%d: %v", name, off, n, err)
		}
	}
	bad := func(target, name string, off, n int64, want string) {
		err := plan.CheckWriteRange(tb, target, name, off, n)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s %s %d+%d: err %v, want %q", target, name, off, n, err, want)
		}
	}
	ok("uboot_b", 0, 512*512)
	ok("uboot_b", 512, 512)
	bad("b", "uboot_b", 0, 513*512, "larger than the partition")
	bad("b", "uboot_b", 1, 512, "whole sectors")
	bad("b", "uboot_b", 0, 100, "whole sectors")
	bad("b", "boot_a", 0, 512, "not a partition of slot b")
	bad("b", "misc", 0, 512, "not a partition of slot b")
	bad("b", "cfg", 0, 512, "not a partition of slot b")
	bad("b", "userdata", 0, 512, "not a partition of slot b")
	bad("b", "boot_b", 0, 512*512, "overlaps partition cfg")
	bad("b", "dtbo_b", 0, 512, "inside sectors 0..2047")
	bad("b", "vbmeta_b", 0, 11*512, "A/B sector")
	bad("b", "oem_b", 0, 91*512, "backup GPT")
	bad("b", "system_b", 0, 512, "no partition")
	bad("a", "uboot_b", 0, 512, "not a partition of slot a")
}

type world struct {
	t    *testing.T
	fx   *fakeemmc.Fixture
	path string
	in   plan.Input
	f    *os.File
}

func newWorld(t *testing.T, fx *fakeemmc.Fixture, u fakeemmc.Unit) *world {
	w := &world{t: t, fx: fx}
	p, err := fx.Image(t.TempDir(), "emmc.img", u)
	if err != nil {
		t.Fatal(err)
	}
	w.path = p
	w.f, err = os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.f.Close() })
	st, _ := w.f.Stat()
	pl, err := plan.VerifyOfficial(bytes.NewReader(fx.FW), int64(len(fx.FW)), fx.Keys, fx.Manifests, nil)
	if err != nil {
		t.Fatal(err)
	}
	sec := make([]byte, 512)
	w.f.ReadAt(sec, plan.ABLBA*512)
	w.in = plan.Input{Disk: w.f, DiskBytes: st.Size(), Source: "image:" + p, Mode: "dry-run", Tool: "test",
		PreFastboot: append([]byte(nil), sec[:32]...), Payload: pl,
		Bootctl:       plan.Bootctl{Path: "/test/uc-pxx-bootctl", SHA256: strings.Repeat("ab", 32)},
		BootctlStatus: fakeStatus(sec), PrereqsConfirmed: true, ApprovedResources: []string{fx.Resource}}
	return w
}

// fakeStatus is what bootctl status would say (the plan tests run no binary).
func fakeStatus(sec []byte) *plan.BootctlStatus {
	a, _ := avbab.Parse(sec)
	st := &plan.BootctlStatus{CRC: a.CRCStored, Pick: a.Pick()}
	for i, s := range a.Slots {
		st.Slots[i] = [3]byte{s.Priority, s.Tries, s.Successful}
	}
	return st
}

var unit2 = fakeemmc.Unit{AB: [2][3]byte{{15, 0, 1}, {14, 0, 1}}, A: "old", B: "old"}

func TestComputeOK(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	w := newWorld(t, fx, unit2)
	p, err := plan.Compute(w.in)
	if err != nil {
		var sb strings.Builder
		p.Render(&sb)
		t.Fatalf("%v\n%s", err, sb.String())
	}
	if p.Keeper != "a" || p.Target != "b" || len(p.Steps) != 13 || !p.VerifyID() {
		t.Fatalf("keeper %s target %s, %d steps", p.Keeper, p.Target, len(p.Steps))
	}
	ops := []string{}
	for _, s := range p.Steps {
		ops = append(ops, s.Op)
	}
	want := "bootctl write write write write write write write write sync verify bootctl check-ab"
	if strings.Join(ops, " ") != want {
		t.Fatalf("steps %v", ops)
	}
	if p.Steps[0].Bootctl.After.Hex32 != hex.EncodeToString(func() []byte { r := raw([3]byte{15, 0, 1}, [3]byte{}); return r[:] }()) {
		t.Fatalf("disable predicts %v", p.Steps[0].Bootctl.After)
	}
	fin := raw([3]byte{14, 0, 1}, [3]byte{15, 7, 0})
	if p.AB.Final.Hex32 != hex.EncodeToString(fin[:]) {
		t.Fatalf("final %v", p.AB.Final)
	}
	for _, s := range p.Steps {
		if s.Write != nil && !strings.HasSuffix(s.Write.Partition, "_b") {
			t.Fatalf("write to %s", s.Write.Partition)
		}
	}
	w.in.KeySlots = []string{"b"}
	pk, err := plan.Compute(w.in)
	if err != nil || pk.KeySource != "updater libraries in system_b" || len(pk.Notes) != 1 || !strings.Contains(pk.Notes[0], "overwrites") {
		t.Fatalf("key from the target only: %v %q %v", err, pk.KeySource, pk.Notes)
	}
	w.in.KeySlots = nil
	var sb strings.Builder
	p.Render(&sb)
	if !strings.Contains(sb.String(), "bootctl arm-trying --slot b --lower-keeper") {
		t.Fatalf("render:\n%s", sb.String())
	}
	// the same inputs give the same plan id
	p2, err := plan.Compute(w.in)
	if err != nil || p2.ID != p.ID {
		t.Fatal("plan id not stable")
	}
}

func TestComputeTrustSkip(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	u := unit2
	u.Tweak = map[string][]byte{"trust_b": fx.New["trust"]}
	p, err := plan.Compute(newWorld(t, fx, u).in)
	if err != nil {
		t.Fatal(err)
	}
	if p.Steps[2].Op != "skip" || p.Steps[2].Write.Partition != "trust_b" {
		t.Fatalf("step 3 is %s %v", p.Steps[2].Op, p.Steps[2].Write)
	}
}

func TestComputeReverse(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	u := fakeemmc.Unit{AB: [2][3]byte{{14, 0, 1}, {15, 0, 1}}, A: "old", B: "old"}
	p, err := plan.Compute(newWorld(t, fx, u).in)
	if err != nil {
		t.Fatal(err)
	}
	fin := raw([3]byte{15, 7, 0}, [3]byte{14, 0, 1})
	if p.Keeper != "b" || p.Target != "a" || p.AB.Final.Hex32 != hex.EncodeToString(fin[:]) {
		t.Fatalf("keeper %s target %s final %v", p.Keeper, p.Target, p.AB.Final)
	}
}

// withNvram is unit2 with this nvram store in others.
func withNvram(store []byte) fakeemmc.Unit {
	u := unit2
	u.Tweak = map[string][]byte{"others": store}
	return u
}

func TestComputeAutoUpdate(t *testing.T) {
	type P = nvramtest.Part
	secret := "2=made-up-admin-password-77f1"
	rec := func(fw string) []string { return append([]string{secret}, nvramtest.Records(fw)...) }
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	// an older part says on, the newer part (the one Android loads) says off
	w := newWorld(t, fx, withNvram(nvramtest.Partition(P{Index: 30, Counter: 9, Records: rec("true")},
		P{Index: 31, Counter: 10, Records: rec("false")}, P{Index: 0, Counter: 8, Records: rec("true")})))
	p, err := plan.Compute(w.in)
	if err != nil {
		t.Fatal(err)
	}
	var ch plan.Check
	for _, c := range p.Checks {
		if c.ID == "autoupdate" {
			ch = c
		}
	}
	if !ch.OK || !strings.Contains(ch.Detail, "must be off: off; active part 31 of 32") {
		t.Fatalf("check %+v", ch)
	}
	js, _ := json.Marshal(p)
	var sb strings.Builder
	p.Render(&sb)
	for _, out := range []string{string(js), sb.String()} {
		if strings.Contains(out, "made-up") || strings.Contains(out, "example.invalid") {
			t.Fatalf("the plan holds nvram data other than the state:\n%s", out)
		}
	}
}

// TestAutoUpdateRule is the user's rule (U2): off passes; on in a part that
// Android may load always refuses, also with the confirmation; not clear
// refuses unless the user confirmed (--autoupdate-unclear-confirmed), and
// --prereqs-confirmed is not that confirmation.
func TestAutoUpdateRule(t *testing.T) {
	type P = nvramtest.Part
	secret := "2=made-up-admin-password-77f1"
	rec := func(fw string) []string { return append([]string{secret}, nvramtest.Records(fw)...) }
	off, on := rec("false"), rec("true")
	store := func(ps ...P) fakeemmc.Unit { return withNvram(nvramtest.Partition(ps...)) }
	layout := func(from, to string) fakeemmc.Unit {
		return fakeemmc.Unit{AB: unit2.AB, A: "old", B: "old", Layout: strings.Replace(fakeemmc.SmallLayout, from, to, 1)}
	}
	long := append(append([]string{}, off...), "9="+strings.Repeat("x", 0x20000))
	cases := []struct {
		name  string
		unit  fakeemmc.Unit
		state string // off, on, unknown
		why   string
	}{
		{"off", store(P{Index: 31, Counter: 127, Records: off}, P{Index: 30, Counter: 126, Records: on}), "off", "active part 31 of 32"},
		{"off, the default test store", unit2, "off", "active part 3 of 32"},

		{"on", withNvram(nvramtest.On()), "on", "active part 3 of 32"},
		{"on: the newer part", store(P{Index: 0, Counter: 5, Records: off}, P{Index: 1, Counter: 6, Records: on}), "on", "active part 1"},
		{"on: the newest part has a bad checksum, the driver loads the older", store(P{Index: 0, Counter: 5, Records: on},
			P{Index: 1, Counter: 6, Records: off, BadSum: true}), "on", "active part 0"},
		{"on: a tie, the driver's part on", store(P{Index: 0, Counter: 5, Records: off}, P{Index: 1, Counter: 5, Records: on}), "on", "highest write counter"},
		{"on: a tie, the other part on", store(P{Index: 0, Counter: 5, Records: on}, P{Index: 1, Counter: 5, Records: off}), "on", "highest write counter"},
		{"on: 72005 twice, once true", store(P{Index: 0, Counter: 5, Records: append(append([]string{}, off...), "72005=true")}), "on", "72005=true"},
		{"on: 72005=1 (Android's getBool reads it as true)", store(P{Index: 0, Counter: 5, Records: []string{secret, "72005=1"}}), "on", "72005=1"},
		{"on: ':72005=true' after 72005=false (the driver removes the ':')", store(P{Index: 0, Counter: 5, Records: append(append([]string{}, off...), ":72005=true")}), "on", "72005=true"},
		{"on: the text length is not the header's", store(P{Index: 1, Counter: 6, Records: on, TextLen: 9}), "on", "72005=true"},
		{"on: zlib length 0xffffffff, checksum 0 (the driver checks nothing)", store(P{Index: 0, Counter: 5, Records: off},
			P{Index: 1, Counter: 6, Records: on, ZlibLen: 0xffffffff}), "on", "72005=true"},
		{"on: flags 0, a short zlib length (the driver inflates 0xffd8 bytes)", store(P{Index: 1, Counter: 6, Records: on, FlagsZero: true,
			Zlib: nvramtest.Encode(P{Records: on})[0x28 : 0x28+zlen(nvramtest.Encode(P{Records: on}))], ZlibLen: 7}), "on", "72005=true"},
		{"on: no end record, 72005=true before memory that is not cleared", store(P{Index: 0, Counter: 5, Records: on, NoEnd: true}), "on", "72005=true"},
		{"on: a newer part with a zlib length past the part", store(P{Index: 0, Counter: 5, Records: off},
			P{Index: 1, Counter: 6, Records: on, ZlibLen: 0x10000}), "on", "past the part"},
		{"on: the same counter and a zlib length past the part", store(P{Index: 0, Counter: 5, Records: off},
			P{Index: 1, Counter: 5, Records: on, ZlibLen: 0x10000}), "on", "past the part"},
		{"off: ':72005=false' (the driver removes the ':')", store(P{Index: 0, Counter: 5, Records: []string{secret, ":72005=false"}}), "off", "active part 0 of 32"},
		{"on: a doubtful newer part, the valid part on", store(P{Index: 0, Counter: 5, Records: on},
			P{Index: 1, Counter: 6, Records: off, ZlibLen: 0x10000}), "on", "past the part"},

		{"unclear: no store", withNvram([]byte{0}), "unknown", "no valid nvram part"},
		{"unclear: the only part has a bad checksum", store(P{Index: 0, Counter: 5, Records: off, BadSum: true}), "unknown", "no valid nvram part"},
		{"unclear: the only part has no magic", store(P{Index: 0, Counter: 5, Records: off, BadMagic: true}), "unknown", "no valid nvram part"},
		{"unclear: a tie on the highest counter", store(P{Index: 0, Counter: 5, Records: off}, P{Index: 1, Counter: 5, Records: off}), "unknown", "same highest write counter"},
		{"unclear: the active part does not inflate", store(P{Index: 0, Counter: 5, Records: off}, P{Index: 1, Counter: 6, Records: off, BadZlib: true}), "unknown", "zlib"},
		{"unclear: the text is too long", store(P{Index: 0, Counter: 5, Records: off}, P{Index: 1, Counter: 6, Records: long}), "unknown", "longer than the driver's buffer"},
		{"unclear: the text length is not the header's", store(P{Index: 1, Counter: 6, Records: off, TextLen: 9}), "unknown", "the header says 9"},
		{"unclear: no 72005", store(P{Index: 0, Counter: 5, Records: []string{secret, "72012=true"}}), "unknown", "no key 72005"},
		{"unclear: 72005 twice", store(P{Index: 0, Counter: 5, Records: append(append([]string{}, off...), "72005=false")}), "unknown", "key 72005 is there twice"},
		{"unclear: 72005 not true or false", store(P{Index: 0, Counter: 5, Records: []string{secret, "72005=TRUE"}}), "unknown", "neither true nor false"},
		{"unclear: no end record (the driver reads memory that is not cleared)", store(P{Index: 0, Counter: 5, Records: off, NoEnd: true}), "unknown", "not cleared"},
		{"unclear: a record without '=' first", store(P{Index: 0, Counter: 5, Records: append([]string{"noequals"}, off...)}), "unknown", "without"},
		{"unclear: 72005 hidden (prefix 3)", store(P{Index: 0, Counter: 5, Records: []string{secret, "\x0372005=false"}}), "unknown", "lock prefix"},
		{"unclear: flags 0", store(P{Index: 0, Counter: 5, Records: off, FlagsZero: true}), "unknown", "flags 0"},
		{"unclear: a newer part with a zlib length past the part", store(P{Index: 0, Counter: 5, Records: off},
			P{Index: 1, Counter: 6, Records: off, ZlibLen: 0x10000}), "unknown", "past the part"},
		{"unclear: the same counter and a zlib length past the part", store(P{Index: 0, Counter: 5, Records: off},
			P{Index: 1, Counter: 5, Records: off, ZlibLen: 0x10000}), "unknown", "past the part"},
		{"unclear: no partition others", layout("21 others 78080 8192\n", ""), "unknown", "no partition others"},
		{"unclear: others too small", layout("21 others 78080 8192", "21 others 78080 4096"), "unknown", "too small for the nvram ring"},
	}
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	for _, c := range cases {
		for _, confirmed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/confirmed=%v", c.name, confirmed), func(t *testing.T) {
				w := newWorld(t, fx, c.unit)
				w.in.AutoUpdateUnclearConfirmed = confirmed
				if !w.in.PrereqsConfirmed {
					t.Fatal("the test world must have --prereqs-confirmed")
				}
				p, err := plan.Compute(w.in)
				var ch plan.Check
				for _, x := range p.Checks {
					if x.ID == "autoupdate" {
						ch = x
					}
				}
				var sb strings.Builder
				p.Render(&sb)
				out := sb.String()
				js, _ := json.Marshal(p)
				if p.AutoUpdate == nil || p.AutoUpdate.State != c.state || p.AutoUpdate.UnclearConfirmed != confirmed ||
					!strings.Contains(p.AutoUpdate.Why, c.why) || !strings.Contains(ch.Detail, c.why) {
					t.Fatalf("autoupdate %+v, check %+v; want state %s, why ~%q\n%s", p.AutoUpdate, ch, c.state, c.why, out)
				}
				if !strings.Contains(string(js), fmt.Sprintf(`"unclear_confirmed":%v`, confirmed)) {
					t.Fatalf("the plan JSON does not record the confirmation: %s", js)
				}
				pass := c.state == "off" || (c.state == "unknown" && confirmed)
				switch {
				case pass && (err != nil || !ch.OK || p.ID == "" || !p.VerifyID()):
					t.Fatalf("refused: %v\n%s", err, out)
				case !pass && (err == nil || ch.OK || p.ID != "" || len(p.Steps) != 0):
					t.Fatalf("not refused by the check autoupdate: %v %+v\n%s", err, ch, out)
				}
				var want []string
				switch {
				case c.state == "off":
					want = []string{"autoupdate Auto Update Firmware of stock Android: off (nvram part", "off in the active part"}
				case c.state == "on":
					want = []string{"autoupdate Auto Update Firmware of stock Android: ON: always refused", "always refused (turn it off"}
				case confirmed:
					want = []string{"NOT CLEAR, the user confirmed to go on (--autoupdate-unclear-confirmed)",
						"not clear; the user confirmed to go on (--autoupdate-unclear-confirmed)"}
				default:
					want = []string{"NOT CLEAR, not confirmed (--autoupdate-unclear-confirmed): refused",
						"refused unless the user confirms to go on (--autoupdate-unclear-confirmed)"}
				}
				for _, s := range want {
					if !strings.Contains(out, s) {
						t.Fatalf("the plan output has no %q:\n%s", s, out)
					}
				}
				for _, o := range []string{string(js), out} {
					for _, bad := range []string{"made-up", "example.invalid", "xxxx", "100="} {
						if strings.Contains(o, bad) {
							t.Fatalf("the plan holds nvram data other than the state (%q):\n%s", bad, o)
						}
					}
				}
			})
		}
	}
	// The confirmation is not the prerequisites confirmation, and the other
	// way round.
	w := newWorld(t, fx, store(P{Index: 0, Counter: 5, Records: off, BadSum: true}))
	w.in.PrereqsConfirmed, w.in.AutoUpdateUnclearConfirmed = false, true
	p, err := plan.Compute(w.in)
	failed := map[string]bool{}
	for _, x := range p.Checks {
		failed[x.ID] = !x.OK
	}
	if err == nil || !failed["prerequisites"] || failed["autoupdate"] {
		t.Fatalf("--autoupdate-unclear-confirmed without --prereqs-confirmed: %v %v", err, failed)
	}
	// the confirmation changes the plan id (an approved plan file binds it)
	w = newWorld(t, fx, unit2)
	p1, err1 := plan.Compute(w.in)
	w.in.AutoUpdateUnclearConfirmed = true
	p2, err2 := plan.Compute(w.in)
	if err1 != nil || err2 != nil || p1.ID == p2.ID {
		t.Fatalf("plan ids %s %s: %v %v", p1.ID, p2.ID, err1, err2)
	}
}

func zlen(p []byte) int { return int(p[12]) | int(p[13])<<8 }

// deviceMode makes the input a device-mode input, as package devmode gives it.
func deviceMode(w *world) {
	w.in.Mode, w.in.Source, w.in.HashKeeper = "device", "device:/dev/mmcblk0", true
	w.in.RequireBootctlSHA256 = w.in.Bootctl.SHA256
	w.in.Device = &plan.DeviceID{Node: "/dev/mmcblk0", Model: "Crestron UC-P10", Host: "fe330000.mmc",
		CIDSHA256: strings.Repeat("cd", 32), SysfsLayoutSHA256: strings.Repeat("ef", 32)}
	w.in.RAMImage = &plan.RAMImage{Token: "p10inst-4f3f5e"}
}

// A device-mode plan holds the token of its RAM image, and the token is in
// its id. A dry-run plan has no RAM image (its JSON is as before).
func TestComputeRAMImage(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	w := newWorld(t, fx, unit2)
	dry, err := plan.Compute(w.in)
	if err != nil {
		t.Fatal(err)
	}
	if js, _ := json.Marshal(dry); dry.RAMImage != nil || strings.Contains(string(js), "ram_image") {
		t.Fatalf("a dry-run plan with a RAM image: %s", js)
	}
	deviceMode(w)
	p1, err := plan.Compute(w.in)
	if err != nil {
		t.Fatal(err)
	}
	if p1.RAMImage == nil || p1.RAMImage.Token != "p10inst-4f3f5e" || !p1.VerifyID() {
		t.Fatalf("RAM image %+v", p1.RAMImage)
	}
	if js, _ := json.Marshal(p1); !strings.Contains(string(js), `"ram_image":{"token":"p10inst-4f3f5e"}`) {
		t.Fatalf("plan JSON: %s", js)
	}
	var out bytes.Buffer
	p1.Render(&out)
	if !strings.Contains(out.String(), "RAM image token p10inst-4f3f5e") {
		t.Fatalf("render:\n%s", out.String())
	}
	w.in.RAMImage = &plan.RAMImage{Token: "p10inst-00a9ff"}
	p2, err := plan.Compute(w.in)
	if err != nil || p2.ID == p1.ID || !p2.VerifyID() {
		t.Fatalf("another token: id %s (first %s): %v", p2.ID, p1.ID, err)
	}
	// the id covers the token: a plan file with the token changed fails VerifyID
	p3 := *p1
	p3.RAMImage = &plan.RAMImage{Token: "p10inst-00a9ff"}
	if p3.VerifyID() {
		t.Fatal("the token is not in the id")
	}
}

func TestComputeRefusals(t *testing.T) {
	type mod func(w *world)
	type P = nvramtest.Part
	setAB := func(a, b [3]byte) fakeemmc.Unit {
		u := unit2
		u.AB = [2][3]byte{a, b}
		return u
	}
	cases := []struct {
		name  string
		opt   fakeemmc.FixtureOpt
		unit  fakeemmc.Unit
		mod   mod
		check string
	}{
		{"keeper trying", fakeemmc.FixtureOpt{}, setAB([3]byte{15, 7, 0}, [3]byte{14, 0, 1}), nil, "keeper"},
		{"tie", fakeemmc.FixtureOpt{}, setAB([3]byte{15, 0, 1}, [3]byte{15, 0, 1}), nil, "keeper"},
		{"both successful, no pre-fastboot sector", fakeemmc.FixtureOpt{}, unit2, func(w *world) { w.in.PreFastboot = nil }, "keeper"},
		{"pre-fastboot sector differs", fakeemmc.FixtureOpt{}, unit2, func(w *world) {
			r := raw([3]byte{14, 0, 1}, [3]byte{15, 0, 1})
			w.in.PreFastboot = r[:]
		}, "pre-fastboot"},
		{"user keeper against the pre-fastboot sector", fakeemmc.FixtureOpt{}, unit2, func(w *world) { w.in.Keeper = "b" }, "keeper"},
		{"bad keeper name", fakeemmc.FixtureOpt{}, unit2, func(w *world) { w.in.Keeper = "c" }, "keeper"},
		{"no bootctl status", fakeemmc.FixtureOpt{}, unit2, func(w *world) { w.in.BootctlStatus = nil }, "bootctl-status"},
		{"bootctl disagrees", fakeemmc.FixtureOpt{}, unit2, func(w *world) { w.in.BootctlStatus.Pick = 1 }, "bootctl-status"},
		{"prerequisites", fakeemmc.FixtureOpt{}, unit2, func(w *world) { w.in.PrereqsConfirmed = false }, "prerequisites"},
		{"android auto update on", fakeemmc.FixtureOpt{}, withNvram(nvramtest.On()), nil, "autoupdate"},
		{"android auto update: the newer part on", fakeemmc.FixtureOpt{}, withNvram(nvramtest.Partition(
			P{Index: 0, Counter: 5, Records: nvramtest.Records("false")}, P{Index: 1, Counter: 6, Records: nvramtest.Records("true")})), nil, "autoupdate"},
		{"android auto update: no store", fakeemmc.FixtureOpt{}, withNvram([]byte{0}), nil, "autoupdate"},
		{"android auto update: bad checksum", fakeemmc.FixtureOpt{}, withNvram(nvramtest.Partition(
			P{Index: 0, Counter: 5, Records: nvramtest.Records("false"), BadSum: true})), nil, "autoupdate"},
		{"android auto update: tie", fakeemmc.FixtureOpt{}, withNvram(nvramtest.Partition(
			P{Index: 0, Counter: 5, Records: nvramtest.Records("false")}, P{Index: 1, Counter: 5, Records: nvramtest.Records("false")})), nil, "autoupdate"},
		{"no others", fakeemmc.FixtureOpt{}, fakeemmc.Unit{AB: unit2.AB, A: "old", B: "old",
			Layout: strings.Replace(fakeemmc.SmallLayout, "21 others 78080 8192\n", "", 1)}, nil, "autoupdate"},
		{"others too small", fakeemmc.FixtureOpt{}, fakeemmc.Unit{AB: unit2.AB, A: "old", B: "old",
			Layout: strings.Replace(fakeemmc.SmallLayout, "21 others 78080 8192", "21 others 78080 4096", 1)}, nil, "autoupdate"},
		{"other AVB key", fakeemmc.FixtureOpt{OldKey: true}, unit2, nil, "vbmeta-key"},
		{"rollback index lower", fakeemmc.FixtureOpt{OldRB: 1}, unit2, nil, "rollback-index"},
		{"keeper vbmeta unreadable", fakeemmc.FixtureOpt{}, fakeemmc.Unit{AB: unit2.AB, B: "old"}, nil, "vbmeta-key"},
		{"target vbmeta unreadable", fakeemmc.FixtureOpt{}, fakeemmc.Unit{AB: unit2.AB, A: "old"}, nil, "rollback-index"},
		{"no payload", fakeemmc.FixtureOpt{}, unit2, func(w *world) { w.in.Payload = nil }, "payload"},
		{"mode", fakeemmc.FixtureOpt{}, unit2, func(w *world) { w.in.Mode = "device" }, "mode"},
		{"device mode without the RAM image", fakeemmc.FixtureOpt{}, unit2, func(w *world) { deviceMode(w); w.in.RAMImage = nil }, "mode"},
		{"device mode with an empty RAM image token", fakeemmc.FixtureOpt{}, unit2, func(w *world) {
			deviceMode(w)
			w.in.RAMImage = &plan.RAMImage{}
		}, "mode"},
		{"no bootctl sha", fakeemmc.FixtureOpt{}, unit2, func(w *world) { w.in.Bootctl.SHA256 = "" }, "bootctl-binary"},
		{"boot resource not approved", fakeemmc.FixtureOpt{}, unit2, func(w *world) { w.in.ApprovedResources = nil }, "boot-resource"},
		{"boot resource list empty", fakeemmc.FixtureOpt{}, unit2, func(w *world) { w.in.ApprovedResources = []string{""} }, "boot-resource"},
		{"missing partition", fakeemmc.FixtureOpt{}, fakeemmc.Unit{AB: unit2.AB, A: "old", B: "old",
			Layout: strings.Replace(fakeemmc.SmallLayout, "23 system_b", "23 system_x", 1)}, nil, "gpt-slots"},
		{"misc moved", fakeemmc.FixtureOpt{}, fakeemmc.Unit{AB: unit2.AB, A: "old", B: "old",
			Layout: strings.Replace(strings.Replace(fakeemmc.SmallLayout, "5 misc 49152 8192", "5 misc 49152 2", 1),
				"6 resource 57344 512", "6 resource 49154 8702", 1)}, nil, "gpt-misc"},
		{"partition too small", fakeemmc.FixtureOpt{}, fakeemmc.Unit{AB: unit2.AB, A: "old", B: "old",
			Layout: strings.Replace(fakeemmc.SmallLayout, "23 system_b 65792 6144", "23 system_b 65792 4096", 1)}, nil, "write-system_b"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fx := fakeemmc.NewFixture(c.opt)
			w := newWorld(t, fx, c.unit)
			if c.mod != nil {
				c.mod(w)
			}
			p, err := plan.Compute(w.in)
			if err == nil {
				t.Fatal("not refused")
			}
			if len(p.Steps) != 0 || p.ID != "" {
				t.Fatal("a refused plan has steps or an id")
			}
			found := false
			for _, ch := range p.Checks {
				found = found || (!ch.OK && ch.ID == c.check)
			}
			if !found {
				var sb strings.Builder
				p.Render(&sb)
				t.Fatalf("no failed check %s: %v\n%s", c.check, err, sb.String())
			}
		})
	}
}

func TestKeeperChosenByUser(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	w := newWorld(t, fx, unit2)
	w.in.PreFastboot = nil
	w.in.Keeper = "b"
	p, err := plan.Compute(w.in)
	if err != nil || p.Keeper != "b" || p.Target != "a" {
		t.Fatalf("%v keeper %s", err, p.Keeper)
	}
	// a trying slot can never be the keeper, even when the user says so
	u := unit2
	u.AB = [2][3]byte{{15, 0, 1}, {14, 7, 0}}
	w = newWorld(t, fx, u)
	w.in.PreFastboot, w.in.Keeper = nil, "b"
	if _, err := plan.Compute(w.in); err == nil {
		t.Fatal("trying keeper accepted")
	}
}

func TestVerifyOfficial(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	fw := append([]byte(nil), fx.FW...)
	fw[len(fw)/2] ^= 1
	if _, err := plan.VerifyOfficial(bytes.NewReader(fw), int64(len(fw)), fx.Keys, fx.Manifests, nil); err == nil ||
		!strings.Contains(err.Error(), "no manifest") {
		t.Fatalf("changed firmware: %v", err)
	}
	// a manifest that lists the right file but another system image
	m := strings.Replace(string(fx.Manifest), fakeemmc.NewFixture(fakeemmc.FixtureOpt{}).ManifestPartSHA("system"),
		strings.Repeat("0", 64), 1)
	ms, err := manifest.Load(fstest.MapFS{"m.json": {Data: []byte(m)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.VerifyOfficial(bytes.NewReader(fx.FW), int64(len(fx.FW)), fx.Keys, ms, nil); err == nil ||
		!strings.Contains(err.Error(), "system") {
		t.Fatalf("wrong system sha: %v", err)
	}
	bad := fx.Keys
	bad.Head[0] ^= 1
	if _, err := plan.VerifyOfficial(bytes.NewReader(fx.FW), int64(len(fx.FW)), bad, fx.Manifests, nil); err == nil {
		t.Fatal("wrong key accepted")
	}
	// a listed firmware that is not the userdebug build is refused
	m = strings.Replace(string(fx.Manifest), `"ro.build.type": "userdebug"`, `"ro.build.type": "user"`, 1)
	if m == string(fx.Manifest) {
		t.Fatal("test fixture: no ro.build.type to change")
	}
	if ms, err = manifest.Load(fstest.MapFS{"m.json": {Data: []byte(m)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := plan.VerifyOfficial(bytes.NewReader(fx.FW), int64(len(fx.FW)), fx.Keys, ms, nil); err == nil ||
		!strings.Contains(err.Error(), "userdebug") {
		t.Fatalf("user build: %v", err)
	}
}

func TestBootResource(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	pl, err := plan.VerifyOfficial(bytes.NewReader(fx.FW), int64(len(fx.FW)), fx.Keys, fx.Manifests, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pl.BootResource != fx.Resource || len(plan.ApprovedResource) != 64 {
		t.Fatalf("resource %s, want %s", pl.BootResource, fx.Resource)
	}
}

func TestParseBootctlStatus(t *testing.T) {
	st, err := plan.ParseBootctlStatus("uc-pxx-bootctl: current: A{prio 15 tries 0 succ 1} B{prio 14 tries 0 succ 1} crc da933679\n",
		"libavb-would-boot: a\n")
	if err != nil || st.Pick != 0 || st.Slots[1] != [3]byte{14, 0, 1} || st.CRC != 0xda933679 {
		t.Fatalf("%+v %v", st, err)
	}
	if _, err := plan.ParseBootctlStatus("garbage", "libavb-would-boot: a\n"); err == nil {
		t.Fatal("garbage accepted")
	}
}
