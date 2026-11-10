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
	"uc-pxx-installer/internal/plan"
)

// dUnitReset is the BCB-route reset of a factory-stock unit: both slots
// trying, A 15; both slots hold the stock (old) firmware.
var dUnitReset = fakeemmc.Unit{AB: [2][3]byte{{15, 6, 0}, {14, 7, 0}}, A: "old", B: "old"}

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
		Unlock: plan.DirectUnlockInput{Confirmed: true}}
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
		!strings.Contains(p.UnlockDirect.LockState, "not read") {
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
	for _, s := range []string{"bootctl repair --keeper a", "stash     ", "unlock    the user's confirmation"} {
		if !strings.Contains(r, s) {
			t.Fatalf("render lacks %q:\n%s", s, r)
		}
	}
}

// A resumed install: R(K) was written, B is {0,0,0}: no R(K), the disable is a no-op.
func TestDirectResumedAfterRK(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	w := newDWorld(t, fx, fakeemmc.Unit{AB: [2][3]byte{{15, 0, 1}, {0, 0, 0}}, A: "old", B: "old"})
	p, err := plan.ComputeMainlineDirect(w.in)
	if err != nil {
		t.Fatalf("%v\n%s", err, dRender(p))
	}
	if got := dOps(p); got != "keep-key bootctl write write write write write write sync verify bootctl check-ab" || !p.Steps[1].Bootctl.NoOp {
		t.Fatalf("steps %s", got)
	}
	dCardinal(t, p)
}

// The in-RAM lock_state reader (the hook): 0x01 passes without the
// confirmation; any other value refuses whatever was confirmed.
func TestDirectUnlockHook(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	one, zero := byte(1), byte(0)
	for _, c := range []struct {
		name string
		u    plan.DirectUnlockInput
		ok   bool
	}{
		{"read 0x01, no confirmation", plan.DirectUnlockInput{LockState: &one}, true},
		{"read 0x00, confirmed", plan.DirectUnlockInput{LockState: &zero, Confirmed: true}, false},
		{"not read, confirmed", plan.DirectUnlockInput{Confirmed: true}, true},
		{"not read, not confirmed", plan.DirectUnlockInput{}, false},
		{"a marker only (a claim)", plan.DirectUnlockInput{Marker: &plan.UnlockMarker{Found: true, LockState: "0x01", VerifiedBoot: "orange"}}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newDWorld(t, fx, dUnitReset)
			w.in.Unlock = c.u
			p, err := plan.ComputeMainlineDirect(w.in)
			if (err == nil) != c.ok {
				t.Fatalf("err %v\n%s", err, dRender(p))
			}
		})
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
		{"the reset with B the higher priority", fakeemmc.Unit{AB: [2][3]byte{{14, 7, 0}, {15, 6, 0}}, A: "old", B: "old"}, nil, "reestablish"},
		{"A trying, B disabled (not the reset)", fakeemmc.Unit{AB: [2][3]byte{{15, 3, 0}, {0, 0, 0}}, A: "old", B: "old"}, nil, "keeper"},
		{"B successful and booting", fakeemmc.Unit{AB: [2][3]byte{{14, 0, 1}, {15, 0, 1}}, A: "old", B: "new"}, nil, "keeper-boots"},
		{"a tie", fakeemmc.Unit{AB: [2][3]byte{{15, 0, 1}, {15, 0, 1}}, A: "old", B: "old"}, nil, "keeper-boots"},
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
