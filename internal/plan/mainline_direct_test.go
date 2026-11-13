package plan_test

// The direct path (task 3): a factory-stock unit after the BCB-route entry
// (both slots trying, A 15) gets mainline in B, keeper stock A by R(K), the
// boot chain from the stash of the first drive phase.

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/keykeep"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/mainline/mltest"
	"uc-pxx-installer/internal/plan"
)

// dUnitReset is the BCB-route reset of a factory-stock unit: both slots
// trying, A 15. Slot A holds the images of a firmware with a manifest (the
// keeper-firmware check: the fixture has a manifest only of its "new"
// images); slot B the old images.
var dUnitReset = fakeemmc.Unit{AB: [2][3]byte{{15, 6, 0}, {14, 7, 0}}, A: "new", B: "old"}

// dStash is the stash of the fixture's reviewed (userdebug) firmware.
func dStash(t *testing.T, fx *fakeemmc.Fixture) *plan.StashInput {
	t.Helper()
	res, err := mainline.ReadResource(bytes.NewReader(fx.New["boot"]), 0, int64(len(fx.New["boot"])))
	if err != nil {
		t.Fatal(err)
	}
	m := fx.Manifests[0]
	return &plan.StashInput{IndexSHA256: strings.Repeat("5a", 32), FirmwareName: m.Name(), FirmwareSHA256: m.Firmware.SHA256,
		BuildType: "userdebug", UBootBuild: "userdebug",
		Parts: map[string][]byte{"uboot": fx.New["uboot"], "trust": fx.New["trust"], "dtbo": fx.New["dtbo"], "vbmeta": fx.New["vbmeta"], "resource": res}}
}

type dWorld struct {
	in  plan.MainlineDirectInput
	img []byte
}

func newDWorld(t *testing.T, fx *fakeemmc.Fixture, u fakeemmc.Unit) *dWorld {
	t.Helper()
	p, err := fx.Image(t.TempDir(), "emmc.img", u)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	st, _ := f.Stat()
	sec := make([]byte, 512)
	f.ReadAt(sec, plan.ABLBA*512)
	b := mlBundle(t, fx)
	in := plan.MainlineDirectInput{Input: plan.Input{Disk: f, DiskBytes: st.Size(), Source: "image:" + p, Mode: "dry-run", Tool: "test",
		Bootctl:       plan.Bootctl{Path: "/test/uc-pxx-bootctl", SHA256: strings.Repeat("ab", 32)},
		BootctlStatus: fakeStatus(sec), PrereqsConfirmed: true, HashKeeper: true},
		Mainline: mlPayload(t, b), Manifests: fx.Manifests, Gate: b.Gate(), Stash: dStash(t, fx),
		Unlock: plan.DirectUnlockInput{Confirmed: true, Reader: readOne()}}
	in.MainlineKey = plan.ReadMainlineKey(f, st.Size(), keykeep.Encode(fx.Keys), mlKeyGate, "test firmware")
	img, _ := os.ReadFile(p)
	return &dWorld{in: in, img: img}
}

func dOps(p *plan.DirectPlan) string { return mlOps(&p.MainlinePlan) }

func dRender(p *plan.DirectPlan) string {
	var sb strings.Builder
	p.Render(&sb)
	return sb.String()
}

// dCardinal: at every point of the timeline the keeper A is bootable, a slot
// boots, and B is unbootable from R(K) (or the disable) until the arm.
func dCardinal(t *testing.T, p *plan.DirectPlan) {
	t.Helper()
	if len(p.Timeline) != len(p.Steps)+1 {
		t.Fatalf("%d timeline points for %d steps", len(p.Timeline), len(p.Steps))
	}
	armed := false
	for i, pt := range p.Timeline {
		r, _ := pt.AB.Raw()
		a, _ := avbab.Parse(r[:])
		if err := a.Validate(); err != nil || !a.Slots[0].Bootable() || a.Pick() < 0 {
			t.Fatalf("point %d (%s): %s: %v", pt.Step, pt.Op, pt.AB, err)
		}
		if i == 0 {
			continue
		}
		s := p.Steps[i-1]
		switch {
		case s.Op == "keep-key":
			if pt.AB.Hex32 != p.Timeline[0].AB.Hex32 {
				t.Fatal("the keep-key point is not the start")
			}
		case s.Op == "bootctl" && s.Bootctl.Args[0] == "arm-trying":
			armed = true
		}
		if s.Op == "write" || s.Op == "skip" {
			if !strings.HasSuffix(s.Write.Partition, "_b") || strings.HasPrefix(s.Write.Partition, "vendor") || strings.HasPrefix(s.Write.Partition, "oem") {
				t.Fatalf("step %d writes %s", s.N, s.Write.Partition)
			}
			if a.Slots[1].Bootable() || a.Pick() != 0 {
				t.Fatalf("step %d writes %s while B is bootable: %s", s.N, s.Write.Partition, pt.AB)
			}
		}
		if armed && (a.Pick() != 1 || a.Slots[1] != (avbab.Slot{Priority: 15, Tries: 7}) || !a.Slots[0].Keeper()) {
			t.Fatalf("after the arm: %s", pt.AB)
		}
	}
	if !armed {
		t.Fatal("no arm")
	}
}

func TestDirectFromTheReset(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	w := newDWorld(t, fx, dUnitReset)
	p, err := plan.ComputeMainlineDirect(w.in)
	if err != nil {
		t.Fatalf("%v\n%s", err, dRender(p))
	}
	if p.Kind != plan.MainlineDirectKind || p.Keeper != "a" || p.Target != "b" || !p.VerifyID() || p.MainlinePlan.VerifyID() || p.Plan.VerifyID() {
		t.Fatalf("kind %s keeper %s target %s", p.Kind, p.Keeper, p.Target)
	}
	if got := dOps(p); got != "keep-key bootctl bootctl write write write write write write sync verify bootctl check-ab" {
		t.Fatalf("steps %s", got)
	}
	rk := p.Steps[1].Bootctl
	if len(rk.Args) != 5 || strings.Join(rk.Args[:4], " ") != "repair --keeper a --expect-sector" || rk.After.String() != "A{15,0,1} B{0,0,0}, boots next: A" {
		t.Fatalf("R(K) %v %s", rk.Args[:4], rk.After)
	}
	if d := p.Steps[2].Bootctl; strings.Join(d.Args, " ") != "disable --slot b" || !d.NoOp {
		t.Fatalf("disable %v noop %v", d.Args, d.NoOp)
	}
	if a := p.Steps[len(p.Steps)-2].Bootctl; strings.Join(a.Args, " ") != "arm-trying --slot b --lower-keeper" {
		t.Fatalf("arm %v", a.Args)
	}
	if p.AB.Final.String() != "A{14,0,1} B{15,7,0}, boots next: B" {
		t.Fatalf("final %s", p.AB.Final)
	}
	wantFrom := map[string]string{"uboot_b": "stash", "trust_b": "stash", "dtbo_b": "stash", "vbmeta_b": "stash-patched-vbmeta",
		"boot_b": "assembled-boot-stash", "system_b": "rootfs"}
	for _, s := range p.Sources {
		if wantFrom[s.Partition] != s.From || s.KeeperPartition != "" {
			t.Fatalf("source %+v", s)
		}
	}
	rb := w.in.Mainline.Manifest.RequiresBootchain
	if p.Source("uboot_b").SHA256 != rb.UBootSHA256 || p.Source("vbmeta_b").SHA256 != w.in.Mainline.Manifest.VbmetaPatched.SHA256 ||
		p.Source("boot_b").SHA256 != w.in.Mainline.Manifest.BootAssembled.SHA256 {
		t.Fatal("sources are not the payload's")
	}
	if len(p.KeeperSHA256) != 8 || p.KeyKeep.State != "keep" || p.Stash == nil || len(p.Stash.Parts) != 5 || !p.Unlock.Confirmed ||
		p.UnlockDirect.LockState != "0x01" || p.UnlockDirect.Reader == nil || p.UnlockDirect.Reader.Supplicant != "64c139f6" {
		t.Fatalf("keeper %d key %+v stash %+v unlock %+v", len(p.KeeperSHA256), p.KeyKeep, p.Stash, p.UnlockDirect)
	}
	nw := strings.Join(p.NeverWritten, "|")
	if !strings.Contains(nw, "keeper slot A") || !strings.Contains(nw, "vendor_b and oem_b") || !strings.Contains(nw, plan.MainlineKeepKeyNeverWritten) {
		t.Fatalf("never written %v", p.NeverWritten)
	}
	dCardinal(t, p)
	p2, err := plan.ComputeMainlineDirect(w.in)
	if err != nil || p2.ID != p.ID {
		t.Fatal("plan id not stable")
	}
	r := dRender(p)
	for _, s := range []string{"bootctl repair --keeper a", "stash     ", "unlock    lock_state read in this RAM session by the in-RAM reader: unlocked",
		"reader    boot b00t, token p10inst-test; module 5870e0fd, supplicant 64c139f6, client 1424ac1e", "changed false"} {
		if !strings.Contains(r, s) {
			t.Fatalf("render lacks %q:\n%s", s, r)
		}
	}
}

// A resumed install: R(K) was written, B is {0,0,0}: no R(K), the disable is a no-op.
func TestDirectResumedAfterRK(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	w := newDWorld(t, fx, fakeemmc.Unit{AB: [2][3]byte{{15, 0, 1}, {0, 0, 0}}, A: "new", B: "old"})
	p, err := plan.ComputeMainlineDirect(w.in)
	if err != nil {
		t.Fatalf("%v\n%s", err, dRender(p))
	}
	if got := dOps(p); got != "keep-key bootctl write write write write write write sync verify bootctl check-ab" || !p.Steps[1].Bootctl.NoOp {
		t.Fatalf("steps %s", got)
	}
	dCardinal(t, p)
}

// readOne is a reader run that read 0x01 with nothing changed.
func readOne() *plan.LockStateRead {
	s := plan.ReadSums{Security: "5ec", AB32: "ab", BCB: "bcb", ReadOnly: true}
	return &plan.LockStateRead{BootID: "b00t", Token: "p10inst-test", Module: "5870e0fd", Supplicant: "64c139f6", Client: "1424ac1e",
		LockState: "0x01", Before: s, After: s}
}

// The in-RAM lock_state reader is the only unlock source (MAINLINE-RC2-LS):
// 0x01 with no error and nothing changed passes, confirmed or not; another
// byte, a failed read, a change, no read or only a marker refuses, whatever
// was confirmed.
func TestDirectUnlockReader(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	with := func(f func(r *plan.LockStateRead)) *plan.LockStateRead { r := readOne(); f(r); return r }
	for _, c := range []struct {
		name  string
		u     plan.DirectUnlockInput
		ok    bool
		write bool // the plan records a lock-state write (0x00 -> 0x01)
		want  string
	}{
		{"read 0x01, no confirmation", plan.DirectUnlockInput{Reader: readOne()}, true, false, "unlocked"},
		{"read 0x01, confirmed", plan.DirectUnlockInput{Reader: readOne(), Confirmed: true}, true, false, "unlocked"},
		// 0x00: the installer writes 0x01 before any target write (owner 2026-11-11 ~23:05)
		{"read 0x00: the installer writes 0x01", plan.DirectUnlockInput{Reader: with(func(r *plan.LockStateRead) { r.LockState = "0x00" })}, true, true, "writes 0x01 (security/p16 only) and reads it back 0x01 before any target-slot write"},
		{"read 0x00, confirmed: still writes 0x01", plan.DirectUnlockInput{Reader: with(func(r *plan.LockStateRead) { r.LockState = "0x00" }), Confirmed: true}, true, true, "0x00 (locked)"},
		{"read 0x03", plan.DirectUnlockInput{Reader: with(func(r *plan.LockStateRead) { r.LockState = "0x03" })}, false, false, "0x03, not unlocked"},
		{"read 0x81", plan.DirectUnlockInput{Reader: with(func(r *plan.LockStateRead) { r.LockState = "0x81" })}, false, false, "not unlocked"},
		{"read 0xff", plan.DirectUnlockInput{Reader: with(func(r *plan.LockStateRead) { r.LockState = "0xff" })}, false, false, "not unlocked"},
		{"the read failed, confirmed", plan.DirectUnlockInput{Reader: with(func(r *plan.LockStateRead) { r.LockState, r.Error = "", "the client: exit 3" }), Confirmed: true}, false, false, "the in-RAM reader failed: the client: exit 3"},
		{"0x01 and an error", plan.DirectUnlockInput{Reader: with(func(r *plan.LockStateRead) { r.Error = "the reads after" })}, false, false, "failed"},
		{"0x01, changed", plan.DirectUnlockInput{Reader: with(func(r *plan.LockStateRead) { r.Changed = true; r.After.BCB = "other" }), Confirmed: true}, false, false, "STOP"},
		{"0x00, changed: STOP, no write", plan.DirectUnlockInput{Reader: with(func(r *plan.LockStateRead) { r.LockState = "0x00"; r.Changed = true; r.After.BCB = "other" })}, false, false, "STOP"},
		{"not read, confirmed", plan.DirectUnlockInput{Confirmed: true}, false, false, "no lock_state read"},
		{"not read, not confirmed", plan.DirectUnlockInput{}, false, false, "no lock_state read"},
		{"a marker only (a claim)", plan.DirectUnlockInput{Marker: &plan.UnlockMarker{Found: true, LockState: "0x01", VerifiedBoot: "orange"}, Confirmed: true}, false, false, "no lock_state read"},
		{"a stub read 0x01 says so", plan.DirectUnlockInput{Reader: with(func(r *plan.LockStateRead) { r.Stub = "devtest x" })}, true, false, "TEST STUB: devtest x"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newDWorld(t, fx, dUnitReset)
			w.in.Unlock = c.u
			p, err := plan.ComputeMainlineDirect(w.in)
			if (err == nil) != c.ok {
				t.Fatalf("err %v\n%s", err, dRender(p))
			}
			if !strings.Contains(p.UnlockDirect.Basis, c.want) || p.Unlock.Confirmed != c.ok {
				t.Fatalf("basis %q (want %q), unlock gate %v", p.UnlockDirect.Basis, c.want, p.Unlock.Confirmed)
			}
			if (p.UnlockDirect.Write != nil) != c.write {
				t.Fatalf("write intent %+v, want write=%v", p.UnlockDirect.Write, c.write)
			}
			if c.write {
				wr := p.UnlockDirect.Write
				if wr.From != "0x00" || wr.To != "0x01" || wr.Security != plan.SecurityPartNode {
					t.Fatalf("write intent %+v is not 0x00 -> 0x01 on %s", wr, plan.SecurityPartNode)
				}
			}
		})
	}
}

// TestDirectLockWriteNeverBothMainline: a unit with mainline in both slots
// (the keeper A is of no known Crestron firmware) never gets a lock-state
// write; the plan refuses at keeper-firmware with no write intent.
func TestDirectLockWriteNeverBothMainline(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	// A "new" (known) keeper gives a write intent on 0x00
	w := newDWorld(t, fx, dUnitReset)
	w.in.Unlock = plan.DirectUnlockInput{Reader: func() *plan.LockStateRead { r := readOne(); r.LockState = "0x00"; return r }()}
	if p, err := plan.ComputeMainlineDirect(w.in); err != nil || p.UnlockDirect.Write == nil {
		t.Fatalf("a stock keeper + 0x00 must plan a write: err %v, write %+v", err, p.UnlockDirect.Write)
	}
	// the keeper A of no known firmware (A = mainline on both slots): refuse,
	// no write intent, no lock-write-safe passing
	bad := fakeemmc.Unit{AB: [2][3]byte{{15, 6, 0}, {14, 7, 0}}, A: "old", B: "old"}
	w2 := newDWorld(t, fx, bad)
	w2.in.Unlock = plan.DirectUnlockInput{Reader: func() *plan.LockStateRead { r := readOne(); r.LockState = "0x00"; return r }()}
	p2, err := plan.ComputeMainlineDirect(w2.in)
	if err == nil {
		t.Fatalf("a both-mainline unit was accepted:\n%s", dRender(p2))
	}
	if p2.UnlockDirect.Write != nil {
		t.Fatalf("a both-mainline unit got a lock-state write intent: %+v", p2.UnlockDirect.Write)
	}
	kf := false
	for _, ch := range p2.Checks {
		if ch.ID == "keeper-firmware" && !ch.OK {
			kf = true
		}
		if ch.ID == "lock-write-safe" {
			t.Fatalf("a both-mainline unit reached the lock-write-safe check")
		}
	}
	if !kf {
		t.Fatalf("expected keeper-firmware to refuse:\n%s", dRender(p2))
	}
}

func TestDirectRefusals(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	for _, c := range []struct {
		name string
		unit fakeemmc.Unit
		edit func(w *dWorld)
		want string
	}{
		{"the reset with B the higher priority", fakeemmc.Unit{AB: [2][3]byte{{14, 7, 0}, {15, 6, 0}}, A: "new", B: "old"}, nil, "reestablish"},
		{"A trying, B disabled (not the reset)", fakeemmc.Unit{AB: [2][3]byte{{15, 3, 0}, {0, 0, 0}}, A: "new", B: "old"}, nil, "direct-start"},
		{"B successful and booting", fakeemmc.Unit{AB: [2][3]byte{{14, 0, 1}, {15, 0, 1}}, A: "new", B: "new"}, nil, "direct-start"},
		{"a tie", fakeemmc.Unit{AB: [2][3]byte{{15, 0, 1}, {15, 0, 1}}, A: "new", B: "old"}, nil, "direct-start"},
		// review T3 MINOR-1: besides the reset only A{15,0,1} B{0,0,0}
		{"both successful, A next (unit 2's shape)", fakeemmc.Unit{AB: [2][3]byte{{15, 0, 1}, {14, 0, 1}}, A: "new", B: "old"}, nil, "direct-start"},
		{"A successful, B trying lower", fakeemmc.Unit{AB: [2][3]byte{{15, 0, 1}, {14, 7, 0}}, A: "new", B: "old"}, nil, "direct-start"},
		// review T3 MINOR-2: the keeper A must hold a known firmware's images
		{"the keeper A of no known firmware", fakeemmc.Unit{AB: [2][3]byte{{15, 6, 0}, {14, 7, 0}}, A: "old", B: "old"}, nil, "keeper-firmware"},
		{"the keeper A's boot changed", fakeemmc.Unit{AB: [2][3]byte{{15, 6, 0}, {14, 7, 0}}, A: "new", B: "old",
			Tweak: map[string][]byte{"boot_a": mltestExpected()}}, nil, "keeper-firmware"},
		// review T3 MAJOR-1: a payload reviewed for slot A only
		{"a payload reviewed for slot A only", dUnitReset, func(w *dWorld) {
			r := mlBundle(t, fx).Reviewed()
			r.Slots = ""
			w.in.Gate = mainline.TestGate([]mainline.Reviewed{r}, nil, nil)
		}, "payload-slot"},
		{"no stash", dUnitReset, func(w *dWorld) { w.in.Stash = nil }, "stash"},
		{"a stash of another firmware", dUnitReset, func(w *dWorld) { w.in.Stash.FirmwareSHA256 = strings.Repeat("0", 64) }, "stash"},
		{"a stash of a user U-Boot", dUnitReset, func(w *dWorld) { w.in.Stash.UBootBuild = "user" }, "stash"},
		{"a stashed uboot changed", dUnitReset, func(w *dWorld) { w.in.Stash.Parts["uboot"] = fx.Old["uboot"] }, "stash-chain"},
		{"a stashed resource changed", dUnitReset, func(w *dWorld) { w.in.Stash.Parts["resource"] = bytes.Repeat([]byte{7}, 4096) }, "stash-chain"},
		{"the stashed vbmeta is the old one", dUnitReset, func(w *dWorld) { w.in.Stash.Parts["vbmeta"] = fx.Old["vbmeta"] }, "stash-chain"},
		{"no key input", dUnitReset, func(w *dWorld) { w.in.MainlineKey = nil }, "key-keep"},
		{"not unlocked", dUnitReset, func(w *dWorld) { w.in.Unlock = plan.DirectUnlockInput{} }, "unlock"},
		{"a pre-fastboot sector", dUnitReset, func(w *dWorld) { w.in.PreFastboot = make([]byte, 32) }, "direct-input"},
		{"keeper b", dUnitReset, func(w *dWorld) { w.in.Keeper = "b" }, "direct-input"},
		{"an official keep-key input", dUnitReset, func(w *dWorld) { w.in.KeyKeep = &plan.KeyKeepInput{} }, "direct-input"},
		{"no prerequisites", dUnitReset, func(w *dWorld) { w.in.PrereqsConfirmed = false }, "prerequisites"},
		{"a gate that lists no reviewed patch", dUnitReset, func(w *dWorld) { w.in.Gate = mlBundle(t, fx).Gate().WithVbmetaPatches(nil) }, "vbmeta-patch-reviewed"},
		{"no gate", dUnitReset, func(w *dWorld) { w.in.Gate = nil }, "payload-gate"},
		{"device mode without the device", dUnitReset, func(w *dWorld) { w.in.Mode, w.in.Source = "device", "device:/dev/mmcblk0" }, "mode"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newDWorld(t, fx, c.unit)
			if c.edit != nil {
				c.edit(w)
			}
			p, err := plan.ComputeMainlineDirect(w.in)
			if err == nil {
				t.Fatalf("accepted:\n%s", dRender(p))
			}
			found := false
			for _, ch := range p.Checks {
				found = found || ch.ID == c.want && !ch.OK
			}
			if !found || len(p.Steps) != 0 || p.ID != "" {
				t.Fatalf("failed checks %v, want %s: %v", p.Checks, c.want, err)
			}
		})
	}
}

func TestParseUnlockMarker(t *testing.T) {
	ok := plan.ParseUnlockMarker([]byte("lock_state=0x01\nverifiedbootstate=orange\nmodel=UC-P10\nclient_sha256=ab\ntool=unlock-linux-macos.sh\nunlocked_at=2026-11-10T01:00:00Z\n"))
	if !ok.Found || ok.Why != "" || ok.LockState != "0x01" || ok.VerifiedBoot != "orange" || ok.Model != "UC-P10" {
		t.Fatalf("%+v", ok)
	}
	for _, b := range []string{"", "lock_state=0x01\nlock_state=0x00\n", "x=1\n", "lock_state\n", "lock_state=0x01\r\n", strings.Repeat("a", 5000)} {
		if m := plan.ParseUnlockMarker([]byte(b)); m.Why == "" {
			t.Fatalf("%q accepted: %+v", b, m)
		}
	}
}

func mltestExpected() []byte {
	return mltest.Expected("other-1", 30000, 11, bytes.Repeat([]byte{3}, 4096))
}
