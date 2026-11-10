package plan_test

import (
	"bytes"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"uc-pxx-installer/internal/avb"
	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/keykeep"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/mainline/mltest"
	"uc-pxx-installer/internal/manifest"
	"uc-pxx-installer/internal/nvram/nvramtest"
	"uc-pxx-installer/internal/plan"
)

// The made-up mainline unit: slot B holds the fixture's payload images (the
// stand-in for the reviewed 1.0.0.94 userdebug chain: the fixture's own
// manifest, test pins), successful and booting; slot A holds old images.
var mlUnit = fakeemmc.Unit{AB: [2][3]byte{{14, 0, 1}, {15, 0, 1}}, A: "old", B: "new"}

// mlChain is the boot chain of the fixture's payload, its resource bytes
// and the boot cap of its signed vbmeta.
func mlChain(t *testing.T, fx *fakeemmc.Fixture) (mltest.Chain, []byte, int64) {
	t.Helper()
	res, err := mainline.ReadResource(bytes.NewReader(fx.New["boot"]), 0, int64(len(fx.New["boot"])))
	if err != nil {
		t.Fatal(err)
	}
	m := fx.Manifests[0]
	var cap int64
	for _, d := range m.AVB.Descriptors {
		if d.Partition == "boot" {
			cap = int64(d.ImageSize)
		}
	}
	return mltest.Chain{Version: m.Firmware.Version, BuildType: m.Build["ro.build.type"],
		UBoot: fx.ManifestPartSHA("uboot"), Trust: fx.ManifestPartSHA("trust"), Dtbo: fx.ManifestPartSHA("dtbo"),
		Vbmeta: fx.ManifestPartSHA("vbmeta"), Resource: mltest.SHA(res)}, res, cap
}

// mlBundle is a mainline bundle for the fixture: a template that fits the
// fixture's boot cap and boot partition, a 1 MiB root file system.
func mlBundle(t *testing.T, fx *fakeemmc.Fixture) *mltest.Bundle {
	ch, res, cap := mlChain(t, fx)
	// contract v2: vbmeta_a is the patch of the keeper's vbmeta. cap is the
	// signed boot image_size; the patched one is 64 KiB more (it must be
	// larger than the signed one and fit the fixture's boot_a)
	ps := cap + 64<<10
	b := mltest.NewBundle("mltest-1", 30000, 1<<20, ch, res, ps)
	if _, err := b.PatchVbmeta(fx.New["vbmeta"], ps); err != nil {
		panic(err) // the fixture's own signed vbmeta
	}
	return b
}

func mlPayload(t *testing.T, b *mltest.Bundle) *mainline.Payload {
	t.Helper()
	d, err := b.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	src, err := mainline.DirSource(d)
	if err != nil {
		t.Fatal(err)
	}
	p, err := mainline.Verify(src)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

type mlWorld struct {
	in  plan.MainlineInput
	img []byte
}

func newMLWorld(t *testing.T, fx *fakeemmc.Fixture, u fakeemmc.Unit, b *mltest.Bundle) *mlWorld {
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
	if b == nil {
		b = mlBundle(t, fx)
	}
	in := plan.MainlineInput{Input: plan.Input{Disk: f, DiskBytes: st.Size(), Source: "image:" + p, Mode: "dry-run", Tool: "test",
		PreFastboot:   append([]byte(nil), sec[:32]...),
		Bootctl:       plan.Bootctl{Path: "/test/uc-pxx-bootctl", SHA256: strings.Repeat("ab", 32)},
		BootctlStatus: fakeStatus(sec), PrereqsConfirmed: true, HashKeeper: true},
		Mainline: mlPayload(t, b), Manifests: fx.Manifests, UnlockConfirmed: true, Gate: b.Gate()}
	in.MainlineKey = plan.ReadMainlineKey(f, st.Size(), keykeep.Encode(fx.Keys), mlKeyGate, "test firmware")
	img, _ := os.ReadFile(p)
	return &mlWorld{in: in, img: img}
}

// mlKeyGate is an open keep-key gate for the tests (task 2).
var mlKeyGate = keykeep.Gate{Open: true, Why: "test", SHA256: strings.Repeat("ab", 32)}

func mlRender(p *plan.MainlinePlan) string {
	var sb strings.Builder
	p.Render(&sb)
	return sb.String()
}

func mlOps(p *plan.MainlinePlan) string {
	var ops []string
	for _, s := range p.Steps {
		ops = append(ops, s.Op)
	}
	return strings.Join(ops, " ")
}

func TestMainlineComputeOK(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	w := newMLWorld(t, fx, mlUnit, nil)
	p, err := plan.ComputeMainline(w.in)
	if err != nil {
		t.Fatalf("%v\n%s", err, mlRender(p))
	}
	if p.Kind != plan.MainlineKind || p.Keeper != "b" || p.Target != "a" || p.Payload != nil || !p.VerifyID() {
		t.Fatalf("kind %s keeper %s target %s", p.Kind, p.Keeper, p.Target)
	}
	if p.Plan.VerifyID() {
		t.Fatal("the embedded official plan's VerifyID passes: the official executor could run it")
	}
	// task 2: the keep-key step first (the fixture's backup has no record)
	if got := mlOps(p); got != "keep-key bootctl write write write write write write sync verify bootctl check-ab" {
		t.Fatalf("steps %s", got)
	}
	if kk := p.Steps[0].KeepKey; kk == nil || p.KeyKeep == nil || p.KeyKeep.State != "keep" || kk.RecordSHA256 != p.KeyKeep.RecordSHA256 {
		t.Fatalf("keep-key %+v %+v", kk, p.KeyKeep)
	}
	if a := p.Steps[1].Bootctl.Args; strings.Join(a, " ") != "disable --slot a" {
		t.Fatalf("disable argv %v", a)
	}
	arm := p.Steps[len(p.Steps)-2].Bootctl
	if strings.Join(arm.Args, " ") != "arm-trying --slot a --lower-keeper" {
		t.Fatalf("arm argv %v", arm.Args)
	}
	fin := raw([3]byte{15, 7, 0}, [3]byte{14, 0, 1})
	if p.AB.Final.String() != plan.State(fin).String() || p.AB.Final.Hex32 != plan.State(fin).Hex32 {
		t.Fatalf("final %v", p.AB.Final)
	}
	if p.Steps[1].Bootctl.After.String() != "A{0,0,0} B{15,0,1}, boots next: B" {
		t.Fatalf("after disable %v", p.Steps[1].Bootctl.After)
	}
	wantSrc := map[string]string{"uboot_a": "keeper uboot_b", "trust_a": "keeper trust_b", "dtbo_a": "keeper dtbo_b",
		"vbmeta_a": "patched-vbmeta vbmeta_b", "boot_a": "assembled-boot ", "system_a": "rootfs "}
	var parts []string
	for _, s := range p.Steps {
		if s.Write == nil {
			continue
		}
		parts = append(parts, s.Write.Partition)
		src := p.Source(s.Write.Partition)
		if src == nil || src.From+" "+src.KeeperPartition != wantSrc[s.Write.Partition] || src.SHA256 != s.Write.SHA256 ||
			src.Length != s.Write.Length || s.Write.SegID != src.From || s.Write.Segment != -1 {
			t.Fatalf("%s: source %+v", s.Write.Partition, src)
		}
	}
	if strings.Join(parts, " ") != "uboot_a trust_a dtbo_a vbmeta_a boot_a system_a" {
		t.Fatalf("writes %v", parts)
	}
	// the keeper's bytes are the reviewed chain, and they are what A gets;
	// vbmeta_a is their patch (contract v2): only the boot image_size
	for _, n := range []string{"uboot", "trust", "dtbo"} {
		if p.Source(n+"_a").SHA256 != fx.ManifestPartSHA(n) {
			t.Fatalf("%s source sha", n)
		}
	}
	want, wpf, err := mainline.PatchVbmeta(fx.New["vbmeta"], uint64(p.Chain.BootCap))
	vs := p.Source("vbmeta_a")
	if err != nil || vs.SourceSHA256 != fx.ManifestPartSHA("vbmeta") || vs.SHA256 != mltest.SHA(want) || vs.Length != int64(len(want)) ||
		vs.BootImageSize != p.Chain.BootCap || p.Chain.VbmetaPatch == nil || p.Chain.VbmetaPatch.SHA256 != wpf.SHA256 ||
		len(wpf.Changed) == 0 || int64(wpf.Changed[0]) < wpf.FieldOffset || int64(wpf.Changed[len(wpf.Changed)-1]) >= wpf.FieldOffset+8 {
		t.Fatalf("vbmeta_a source %+v, patch %+v %v", vs, wpf, err)
	}
	ch, res, cap := mlChain(t, fx)
	asm := mltest.Expected("mltest-1", 30000, 11, res)
	if p.Chain.AssembledSHA != mltest.SHA(asm) || p.Chain.SignedBootSize != cap || p.Chain.BootCap != cap+64<<10 || !p.Chain.VbmetaVerified ||
		p.Source("boot_a").ResourceSHA256 != ch.Resource || p.Chain.Firmware != fx.Manifests[0].Name() {
		t.Fatalf("chain %+v", p.Chain)
	}
	if len(p.KeeperSHA256) != 8 || !p.Unlock.Confirmed || p.AutoUpdate.State != "off" {
		t.Fatalf("keeper hashes %d unlock %v", len(p.KeeperSHA256), p.Unlock)
	}
	// the never-written list names vendor_a, oem_a and backup except the two key-record sectors (task 2)
	nw := strings.Join(p.NeverWritten, "|")
	if !strings.Contains(nw, "vendor_a and oem_a") || !strings.Contains(nw, plan.MainlineKeepKeyNeverWritten) || !strings.Contains(nw, "slot B") {
		t.Fatalf("never written %v", p.NeverWritten)
	}
	// stable id; the JSON keeps the id
	p2, err := plan.ComputeMainline(w.in)
	if err != nil || p2.ID != p.ID {
		t.Fatal("plan id not stable")
	}
	b, _ := json.Marshal(p)
	var back plan.MainlinePlan
	if err := json.Unmarshal(b, &back); err != nil || !back.VerifyID() || back.ID != p.ID {
		t.Fatalf("JSON round trip: %v", err)
	}
	// the JSON of a mainline plan read as an official plan does not check out
	var off plan.Plan
	if err := json.Unmarshal(b, &off); err != nil || off.VerifyID() {
		t.Fatal("a mainline plan file reads as a valid official plan")
	}
	r := mlRender(p)
	for _, want := range []string{"bootctl disable --slot a", "bootctl arm-trying --slot a --lower-keeper", "unlock    confirmed true",
		"source  boot_a    <- assembled-boot", "after step  0 start"} {
		if !strings.Contains(r, want) {
			t.Fatalf("render lacks %q:\n%s", want, r)
		}
	}
}

// mlCardinal checks the predicted A/B state after every step of a mainline
// plan: valid, the keeper B successful and bootable at every step, the target
// A unbootable from the disable until the arm, and A next only after the arm.
func mlCardinal(t *testing.T, p *plan.MainlinePlan) {
	t.Helper()
	armed := false
	if len(p.Timeline) != len(p.Steps)+1 {
		t.Fatalf("%d timeline points for %d steps", len(p.Timeline), len(p.Steps))
	}
	for i, pt := range p.Timeline {
		r, err := pt.AB.Raw()
		if err != nil {
			t.Fatal(err)
		}
		a, _ := avbab.Parse(r[:])
		if err := a.Validate(); err != nil {
			t.Fatalf("step %d: %v", pt.Step, err)
		}
		if !a.Slots[1].Keeper() || !a.Slots[1].Bootable() {
			t.Fatalf("step %d: the keeper B is not successful: %v", pt.Step, a.Slots[1])
		}
		if i == 0 {
			continue
		}
		s := p.Steps[i-1]
		if s.N != pt.Step {
			t.Fatalf("timeline step %d is plan step %d", pt.Step, s.N)
		}
		if s.Op == "bootctl" && s.Bootctl.Args[0] == "arm-trying" {
			armed = true
			if s.Bootctl.After.Hex32 != pt.AB.Hex32 {
				t.Fatal("timeline after the arm is not the arm's prediction")
			}
		}
		switch {
		case s.Op == "keep-key":
			// task 2: the keep-key step is the first, before the disable: the A/B sector is as found
			if s.N != 1 || pt.AB.Hex32 != p.Timeline[0].AB.Hex32 {
				t.Fatalf("step %d: a keep-key step that is not the first, or the A/B state is not the start's", s.N)
			}
		case !armed && (a.Slots[0].Bootable() || a.Pick() != 1):
			t.Fatalf("step %d (%s): slot A is bootable before the arm: %s", pt.Step, s.Op, pt.AB)
		case armed && (a.Pick() != 0 || a.Slots[0] != (avbab.Slot{Priority: 15, Tries: 7})):
			t.Fatalf("step %d: after the arm A must be {15,7,0} and next: %s", pt.Step, pt.AB)
		}
		if s.Op == "write" || s.Op == "skip" {
			if !strings.HasSuffix(s.Write.Partition, "_a") || strings.HasPrefix(s.Write.Partition, "vendor") ||
				strings.HasPrefix(s.Write.Partition, "oem") {
				t.Fatalf("step %d writes %s", s.N, s.Write.Partition)
			}
		}
	}
	if !armed || p.Steps[len(p.Steps)-1].Op != "check-ab" {
		t.Fatal("no arm, or the plan does not end with the check")
	}
}

func TestMainlineShapes(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	for _, c := range []struct {
		name   string
		unit   fakeemmc.Unit
		ops    string
		final  string
		noopDi bool
	}{
		{"A 14 successful, B 15", mlUnit, "keep-key bootctl write write write write write write sync verify bootctl check-ab",
			"A{15,7,0} B{14,0,1}, boots next: A", false},
		{"trust_a identical: skipped", fakeemmc.Unit{AB: mlUnit.AB, A: "old", B: "new", Tweak: map[string][]byte{"trust_a": fx.New["trust"]}},
			"keep-key bootctl write skip write write write write sync verify bootctl check-ab", "A{15,7,0} B{14,0,1}, boots next: A", false},
		{"keeper B 14: not lowered", fakeemmc.Unit{AB: [2][3]byte{{13, 0, 1}, {14, 0, 1}}, A: "old", B: "new"},
			"keep-key bootctl write write write write write write sync verify bootctl check-ab", "A{15,7,0} B{14,0,1}, boots next: A", false},
		{"A disabled already", fakeemmc.Unit{AB: [2][3]byte{{0, 0, 0}, {15, 0, 1}}, A: "old", B: "new"},
			"keep-key bootctl write write write write write write sync verify bootctl check-ab", "A{15,7,0} B{14,0,1}, boots next: A", true},
		{"A trying, lower", fakeemmc.Unit{AB: [2][3]byte{{3, 5, 0}, {15, 0, 1}}, A: "old", B: "new"},
			"keep-key bootctl write write write write write write sync verify bootctl check-ab", "A{15,7,0} B{14,0,1}, boots next: A", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, err := plan.ComputeMainline(newMLWorld(t, fx, c.unit, nil).in)
			if err != nil {
				t.Fatalf("%v\n%s", err, mlRender(p))
			}
			if mlOps(p) != c.ops || p.AB.Final.String() != c.final || p.Steps[1].Bootctl.NoOp != c.noopDi {
				t.Fatalf("ops %s final %s noop %v", mlOps(p), p.AB.Final, p.Steps[1].Bootctl.NoOp)
			}
			mlCardinal(t, p)
		})
	}
}

// smallPart is SmallLayout with one partition's size changed.
func smallPart(name string, sectors int) string {
	var out []string
	for _, l := range strings.Split(fakeemmc.SmallLayout, "\n") {
		f := strings.Fields(l)
		if len(f) == 4 && f[1] == name {
			f[3] = strconv.Itoa(sectors)
			l = strings.Join(f, " ")
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

func TestMainlineRefusals(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	withheld := fakeemmc.NewFixture(fakeemmc.FixtureOpt{Withheld: true})
	user := fakeemmc.NewFixture(fakeemmc.FixtureOpt{User: true})
	unit := func(ab [2][3]byte) fakeemmc.Unit { return fakeemmc.Unit{AB: ab, A: "old", B: "new"} }
	tweak := func(m map[string][]byte) fakeemmc.Unit {
		return fakeemmc.Unit{AB: mlUnit.AB, A: "old", B: "new", Tweak: m}
	}
	// a fresh copy of the fixture's manifests with other pins (WithTestPins
	// changes the manifests it is given)
	otherPins, err := fx.LoadManifest(fx.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.WithTestPins(otherPins, avb.Pins{PIKCertSHA256: strings.Repeat("0", 64), PSKSubject: fx.Pins.PSKSubject})
	cases := []struct {
		name   string
		fx     *fakeemmc.Fixture
		unit   fakeemmc.Unit
		bundle func(b *mltest.Bundle)
		in     func(in *plan.MainlineInput)
		want   string
	}{
		{"target A boots now (unit 2's S0)", fx, unit([2][3]byte{{15, 0, 1}, {14, 0, 1}}), nil, nil, "keeper-boots"},
		{"tie", fx, unit([2][3]byte{{15, 0, 1}, {15, 0, 1}}), nil, nil, "keeper-boots"},
		{"keeper B trying", fx, unit([2][3]byte{{14, 0, 1}, {15, 7, 0}}), nil, nil, "keeper"},
		{"keeper B unbootable", fx, unit([2][3]byte{{15, 0, 1}, {0, 0, 0}}), nil, nil, "keeper"},
		{"keeper holds the old firmware", fx, unit(mlUnit.AB), nil, nil, ""},
		{"both trying (BCB-route reset), A higher", fx, unit([2][3]byte{{15, 7, 0}, {14, 7, 0}}), nil, nil, "no-reestablish"},
		{"both trying (BCB-route reset), B higher", fx, unit([2][3]byte{{14, 7, 0}, {15, 7, 0}}), nil, nil, "no-reestablish"},
		{"armed target with B lower (after the arm)", fx, unit([2][3]byte{{15, 7, 0}, {14, 0, 1}}), nil, nil, "keeper-boots"},
		{"keeper a requested", fx, mlUnit, nil, func(in *plan.MainlineInput) { in.Keeper = "a" }, "mainline-input"},
		{"official payload given too", fx, mlUnit, nil, func(in *plan.MainlineInput) { in.Payload = &plan.Payload{} }, "mainline-input"},
		{"keep-key input", fx, mlUnit, nil, func(in *plan.MainlineInput) { in.KeyKeep = &plan.KeyKeepInput{} }, "mainline-input"},
		{"key slots", fx, mlUnit, nil, func(in *plan.MainlineInput) { in.KeySlots = []string{"b"} }, "mainline-input"},
		{"known before", fx, mlUnit, nil, func(in *plan.MainlineInput) { in.KnownBefore = map[string]string{} }, "mainline-input"},
		{"no unlock confirmation", fx, mlUnit, nil, func(in *plan.MainlineInput) { in.UnlockConfirmed = false }, "unlock"},
		{"prerequisites not confirmed", fx, mlUnit, nil, func(in *plan.MainlineInput) { in.PrereqsConfirmed = false }, "prerequisites"},
		{"auto update on", fx, tweak(map[string][]byte{"others": nvramtest.On()}), nil, nil, "autoupdate"},
		{"pre-fastboot sector differs", fx, mlUnit, nil, func(in *plan.MainlineInput) {
			r := raw([3]byte{15, 0, 1}, [3]byte{14, 0, 1})
			in.PreFastboot = r[:]
		}, "pre-fastboot"},
		{"bootctl status disagrees", fx, mlUnit, nil, func(in *plan.MainlineInput) { in.BootctlStatus.Pick = 0 }, "bootctl-status"},
		{"no payload", fx, mlUnit, nil, func(in *plan.MainlineInput) { in.Mainline = nil }, "payload"},
		{"payload not from pass 1", fx, mlUnit, nil, func(in *plan.MainlineInput) {
			in.Mainline = &mainline.Payload{Kind: mainline.Kind, Manifest: in.Mainline.Manifest}
		}, "payload"},
		{"keeper uboot_b differs", fx, tweak(map[string][]byte{"uboot_b": fx.Old["uboot"]}), nil, nil, "keeper-chain"},
		{"keeper trust_b differs", fx, tweak(map[string][]byte{"trust_b": fx.Old["trust"]}), nil, nil, "keeper-chain"},
		{"keeper dtbo_b differs", fx, tweak(map[string][]byte{"dtbo_b": fx.Old["dtbo"]}), nil, nil, "keeper-chain"},
		{"keeper vbmeta_b differs", fx, tweak(map[string][]byte{"vbmeta_b": fx.Old["vbmeta"]}), nil, nil, "keeper-chain"},
		{"keeper boot_b resource differs", fx, tweak(map[string][]byte{"boot_b": fx.Old["boot"]}), nil, nil, "keeper-chain"},
		{"keeper boot_b is not a boot image", fx, tweak(map[string][]byte{"boot_b": fx.Old["system"][:200<<10]}), nil, nil, "keeper-chain"},
		{"required chain is in no manifest", fx, mlUnit, func(b *mltest.Bundle) {
			b.Sub("requires_bootchain")["uboot_sha256"] = strings.Repeat("1", 64)
		}, nil, "chain-manifest"},
		{"required chain version differs", fx, mlUnit, func(b *mltest.Bundle) {
			b.Sub("requires_bootchain")["version"] = "1.0.0.94"
		}, nil, "chain-manifest"},
		{"chain manifest withheld (gate closed)", withheld, mlUnit, nil, nil, "chain-manifest"},
		{"user build chain", user, mlUnit, func(b *mltest.Bundle) {
			b.Sub("requires_bootchain")["build_type"] = "userdebug"
		}, nil, "chain-manifest"},
		{"no manifests", fx, mlUnit, nil, func(in *plan.MainlineInput) { in.Manifests = nil }, "chain-manifest"},
		{"vbmeta not signed by the chain's pins", fx, mlUnit, nil, func(in *plan.MainlineInput) { in.Manifests = otherPins }, "vbmeta-verify"},
		{"patched image_size is not the manifest's patch", fx, mlUnit, func(b *mltest.Bundle) {
			b.Manifest["vbmeta_boot_cap"] = b.Manifest["vbmeta_boot_cap"].(int64) + 2048
			b.Sub("vbmeta_patched")["boot_image_size"] = b.Manifest["vbmeta_boot_cap"]
		}, nil, "vbmeta-patch"},
		{"patched sha is not the manifest's", fx, mlUnit, func(b *mltest.Bundle) {
			b.Sub("vbmeta_patched")["sha256"] = strings.Repeat("e", 64)
		}, nil, "vbmeta-patch"},
		{"patched field offset is not the manifest's", fx, mlUnit, func(b *mltest.Bundle) {
			b.Sub("vbmeta_patched")["field_offset"] = b.Sub("vbmeta_patched")["field_offset"].(int64) + 8
		}, nil, "vbmeta-patch"},
		{"patched size is not the manifest's", fx, mlUnit, func(b *mltest.Bundle) {
			b.Sub("vbmeta_patched")["size"] = b.Sub("vbmeta_patched")["size"].(int) + 512
		}, nil, "vbmeta-patch"},
		{"patch gate: not a reviewed patch", fx, mlUnit, nil, func(in *plan.MainlineInput) {
			in.Gate = in.Gate.WithVbmetaPatches(nil)
		}, "vbmeta-patch-reviewed"},
		{"assembled sha is not the manifest's", fx, mlUnit, func(b *mltest.Bundle) {
			b.Sub("boot_assembled")["sha256"] = strings.Repeat("e", 64)
		}, nil, "boot-assembled"},
		{"assembled size is not the manifest's", fx, mlUnit, func(b *mltest.Bundle) {
			b.Sub("boot_assembled")["size"] = b.Sub("boot_assembled")["size"].(int) + 2048
		}, nil, "boot-assembled"},
		{"patched image_size larger than boot_a", fx, fakeemmc.Unit{Layout: smallPart("boot_a", 200), AB: mlUnit.AB, A: "old", B: "new"}, nil, nil, "boot-cap"},
		{"rootfs larger than system_a", fx, fakeemmc.Unit{Layout: smallPart("system_a", 1024), AB: mlUnit.AB, A: "old", B: "new"}, nil, nil, "write-system_a"},
		{"device mode without the device", fx, mlUnit, nil, func(in *plan.MainlineInput) { in.Mode = "device" }, "mode"},
		{"dry run on a device node", fx, mlUnit, nil, func(in *plan.MainlineInput) { in.Source = "device:/dev/mmcblk0" }, "mode"},
		{"wrong model UC-P8", fx, mlUnit, nil, func(in *plan.MainlineInput) {
			mlDeviceMode(in, "Crestron UC-P8", "UC-P8")
		}, "mainline-model"},
		{"factory data of another model", fx, mlUnit, nil, func(in *plan.MainlineInput) {
			mlDeviceMode(in, "Crestron UC-P10", "UC-P8")
		}, "unit-model"},
		{"device mode, panel not confirmed", fx, mlUnit, nil, func(in *plan.MainlineInput) {
			mlDeviceMode(in, "Crestron UC-P10", "UC-P10")
			in.PanelConfirmed = false
		}, "panel"},
		{"device mode, other bootctl", fx, mlUnit, nil, func(in *plan.MainlineInput) {
			mlDeviceMode(in, "Crestron UC-P10", "UC-P10")
			in.RequireBootctlSHA256 = strings.Repeat("cd", 32)
		}, "bootctl-binary"},
	}
	for _, c := range cases {
		if c.want == "" { // a unit whose B holds the old images: the chain check
			c.unit.B, c.want = "old", "keeper-chain"
		}
		t.Run(c.name, func(t *testing.T) {
			var b *mltest.Bundle
			if c.bundle != nil {
				b = mlBundle(t, c.fx)
				c.bundle(b)
			}
			w := newMLWorld(t, c.fx, c.unit, b)
			if c.in != nil {
				c.in(&w.in)
			}
			p, err := plan.ComputeMainline(w.in)
			if err == nil {
				t.Fatalf("accepted:\n%s", mlRender(p))
			}
			var failed []string
			for _, ch := range p.Checks {
				if !ch.OK {
					failed = append(failed, ch.ID)
				}
			}
			if !strings.Contains(" "+strings.Join(failed, " ")+" ", " "+c.want+" ") || !strings.Contains(err.Error(), c.want+": ") {
				t.Fatalf("failed checks %v (err %v), want %q", failed, err, c.want)
			}
			if len(p.Steps) != 0 || len(p.Sources) != 0 || p.ID != "" || p.VerifyID() {
				t.Fatalf("a refused plan has steps or an id")
			}
		})
	}
	// with the device fields, the plan passes (the device mode's own gate)
	t.Run("device mode OK", func(t *testing.T) {
		w := newMLWorld(t, fx, mlUnit, nil)
		mlDeviceMode(&w.in, "Crestron UC-P10", "UC-P10")
		p, err := plan.ComputeMainline(w.in)
		if err != nil {
			t.Fatalf("%v\n%s", err, mlRender(p))
		}
		if p.RAMImage == nil || p.Device == nil || p.FirmwareIdent == "" {
			t.Fatal("device fields not recorded")
		}
		mlCardinal(t, p)
	})
}

func mlDeviceMode(in *plan.MainlineInput, dt, base string) {
	in.Mode, in.Source = "device", "device:/dev/mmcblk0"
	in.Device = &plan.DeviceID{Node: "/dev/mmcblk0", Model: "Crestron UC-P10", Host: "fe330000.mmc", CIDSHA256: strings.Repeat("1", 64),
		SysfsLayoutSHA256: strings.Repeat("2", 64), UnitModel: plan.UnitModel{DTModel: dt, FactoryModel: base + "-T-HS", Base: base, ChecksumOK: true}}
	if dt != in.Device.Model {
		in.Device.Model = dt
	}
	in.RAMImage = &plan.RAMImage{Token: "p10inst-test01", BootID: "boot-id-test"}
	in.RequireBootctlSHA256 = in.Bootctl.SHA256
	in.FirmwareIdent = "drive size=1,mtime=1,ctime=1,dev=1,ino=1"
	in.PanelConfirmed = true
}

// TestMainlineRecheckBefore: the re-check of a device plan takes the
// before-hashes of the always-written parts from the plan (they are
// recorded only); the trust_a hash, which decides the skip, is read again.
// The same plan id comes out; in a dry run RecheckBefore is refused.
func TestMainlineRecheckBefore(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	w := newMLWorld(t, fx, mlUnit, nil)
	mlDeviceMode(&w.in, "Crestron UC-P10", "UC-P10")
	p0, err := plan.ComputeMainline(w.in)
	if err != nil {
		t.Fatalf("%v\n%s", err, mlRender(p0))
	}
	kb := plan.MainlineKnownBeforeOf(p0)
	if _, ok := kb["trust_a"]; ok || len(kb) != 5 {
		t.Fatalf("known before %v", kb)
	}
	for _, n := range []string{"uboot_a", "dtbo_a", "vbmeta_a", "boot_a", "system_a"} {
		if len(kb[n]) != 64 {
			t.Fatalf("no before-hash of %s", n)
		}
	}
	in := w.in
	in.RecheckBefore = kb
	p1, err := plan.ComputeMainline(in)
	if err != nil || p1.ID != p0.ID {
		t.Fatalf("re-check: %v, id %s vs %s", err, p1.ID, p0.ID)
	}
	// the known hashes are used as given (a changed value changes the id),
	// the trust hash is not: it is read again
	in.RecheckBefore = map[string]string{}
	for k, v := range kb {
		in.RecheckBefore[k] = v
	}
	in.RecheckBefore["system_a"] = strings.Repeat("0", 64)
	in.RecheckBefore["trust_a"] = strings.Repeat("0", 64)
	p2, err := plan.ComputeMainline(in)
	if err != nil || p2.ID == p0.ID {
		t.Fatalf("a changed known hash: %v (same id %v)", err, p2.ID == p0.ID)
	}
	for _, s := range p2.Steps {
		if s.Write != nil && s.Write.Partition == "trust_a" && s.Write.BeforeSHA256 == strings.Repeat("0", 64) {
			t.Fatal("the trust_a before-hash was taken from the input")
		}
	}
	// a dry run takes none
	d := newMLWorld(t, fx, mlUnit, nil)
	d.in.RecheckBefore = kb
	if _, err := plan.ComputeMainline(d.in); err == nil || !strings.Contains(err.Error(), "re-check before-hashes") {
		t.Fatalf("dry run with RecheckBefore: %v", err)
	}
}

// TestMainlinePayloadGate (the review of c0fb5c5, finding 1): a plan needs
// a payload that the gate lists as reviewed. The release's gate refuses
// every bundle today; a nil gate refuses; a test gate that lists another
// root file system refuses. The gate record is in the plan JSON.
func TestMainlinePayloadGate(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	b := mlBundle(t, fx)
	for _, c := range []struct {
		name string
		gate *mainline.Gate
		want []string
	}{
		// one refusal: an unreviewed bundle is not parsed further (review of 01b3841, finding 1)
		{"the release gate", mainline.EmbeddedGate(), []string{"payload-reviewed"}},
		{"no gate", nil, []string{"payload-reviewed"}},
		{"another rootfs reviewed", func() *mainline.Gate {
			r := b.Reviewed()
			r.RootfsSHA256 = strings.Repeat("0", 64)
			return mainline.TestGate([]mainline.Reviewed{r}, nil, nil)
		}(), []string{"payload-reviewed"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newMLWorld(t, fx, mlUnit, b)
			w.in.Gate = c.gate
			p, err := plan.ComputeMainline(w.in)
			if err == nil || p.ID != "" || len(p.Steps) != 0 {
				t.Fatalf("not refused: %v", err)
			}
			var failed []string
			for _, ck := range p.Checks {
				if !ck.OK {
					failed = append(failed, ck.ID)
				}
			}
			if strings.Join(failed, " ") != strings.Join(c.want, " ") {
				t.Fatalf("failed %v, want %v", failed, c.want)
			}
		})
	}
	w := newMLWorld(t, fx, mlUnit, b)
	p, err := plan.ComputeMainline(w.in)
	if err != nil || p.Gate == nil || p.Gate.Gate != "test" || p.Gate.Reviewed == nil || !strings.Contains(mlRender(p), "gate      test: reviewed payload test") {
		t.Fatalf("%v %+v", err, p.Gate)
	}
}

// TestMainlineVbmetaPatchGate (contract v2): with a gate that checks the
// patched vbmeta, the plan passes only when the manifest's vbmeta_patched is
// the gate's reviewed patch and the plan's own patch of vbmeta_b is exactly
// it; the patch facts and the gate entry are in the plan.
func TestMainlineVbmetaPatchGate(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	b := mlBundle(t, fx)
	_, _, signed := mlChain(t, fx)
	good := b.VbmetaPatch(uint64(signed))
	w := newMLWorld(t, fx, mlUnit, b)
	w.in.Gate = w.in.Gate.WithVbmetaPatches([]mainline.VbmetaPatch{good})
	p, err := plan.ComputeMainline(w.in)
	if err != nil || p.Gate.VbmetaPatch == nil || *p.Gate.VbmetaPatch != good || p.Chain.VbmetaPatch.SHA256 != good.SHA256 {
		t.Fatalf("%v %+v", err, p.Gate)
	}
	for name, edit := range map[string]func(*mainline.VbmetaPatch){
		"other result":      func(v *mainline.VbmetaPatch) { v.SHA256 = strings.Repeat("1", 64) },
		"other source":      func(v *mainline.VbmetaPatch) { v.SourceSHA256 = strings.Repeat("2", 64) },
		"other field":       func(v *mainline.VbmetaPatch) { v.FieldOffset += 8 },
		"other image_size":  func(v *mainline.VbmetaPatch) { v.BootImageSize += 2048 },
		"other size":        func(v *mainline.VbmetaPatch) { v.Size += 512 },
		"other old size":    func(v *mainline.VbmetaPatch) { v.OldImageSize++ },
		"the release patch": func(v *mainline.VbmetaPatch) { *v = mainline.ReviewedVbmetaPatches()[0] },
	} {
		t.Run(name, func(t *testing.T) {
			bad := good
			edit(&bad)
			w := newMLWorld(t, fx, mlUnit, b)
			w.in.Gate = w.in.Gate.WithVbmetaPatches([]mainline.VbmetaPatch{bad})
			p, err := plan.ComputeMainline(w.in)
			if err == nil || p.ID != "" || len(p.Steps) != 0 {
				t.Fatalf("not refused")
			}
			if !strings.Contains(err.Error(), "vbmeta-patch-reviewed: ") && !strings.Contains(err.Error(), "vbmeta-patch: ") {
				t.Fatalf("refused by %v", err)
			}
		})
	}
}

// TestMainlineKeepKeyStates (task 2): a mainline plan keeps the firmware key.
// Kept already: no step. Empty, torn or one copy: the keep-key step first.
// No key input, a closed gate, foreign data or another key's record in a
// sector that must be written: refused.
func TestMainlineKeepKeyStates(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	e, s, f, o, tr := keykeep.Empty, keykeep.Same, keykeep.Foreign, keykeep.Other, keykeep.Torn
	for _, c := range []struct {
		name    string
		classes [2]keykeep.Class
		closed  bool
		none    bool
		state   string
		ok      bool
	}{
		{"both empty", [2]keykeep.Class{e, e}, false, false, "keep", true},
		{"both kept", [2]keykeep.Class{s, s}, false, false, "kept", true},
		{"one kept, one empty (a cut between the copies)", [2]keykeep.Class{s, e}, false, false, "keep", true},
		{"one torn", [2]keykeep.Class{tr, e}, false, false, "keep", true},
		{"one kept, the other foreign (step 2 needs both)", [2]keykeep.Class{s, f}, false, false, "not-possible", false},
		{"one kept, the other unreadable", [2]keykeep.Class{s, keykeep.Unread}, false, false, "not-possible", false},
		{"both kept, gate closed", [2]keykeep.Class{s, s}, true, false, "kept", true},
		{"empty, gate closed", [2]keykeep.Class{e, e}, true, false, "gated", false},
		{"foreign data", [2]keykeep.Class{f, e}, false, false, "not-possible", false},
		{"another key's record", [2]keykeep.Class{o, o}, false, false, "not-possible", false},
		{"no key input (no stash)", [2]keykeep.Class{e, e}, false, true, "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newMLWorld(t, fx, mlUnit, nil)
			if c.none {
				w.in.MainlineKey = nil
			} else {
				w.in.MainlineKey.Classes = c.classes
				if c.closed {
					w.in.MainlineKey.Gate = keykeep.Gate{Why: "closed", SHA256: strings.Repeat("cd", 32)}
				}
			}
			p, err := plan.ComputeMainline(w.in)
			if (err == nil) != c.ok {
				t.Fatalf("err %v\n%s", err, mlRender(p))
			}
			if !c.ok {
				if len(p.Steps) != 0 || p.ID != "" {
					t.Fatal("a refused plan with steps or an id")
				}
				found := false
				for _, ch := range p.Checks {
					found = found || ch.ID == "key-keep" && !ch.OK
				}
				if !found || (c.state != "" && p.KeyKeep.State != c.state) {
					t.Fatalf("no failed key-keep check, or state %+v", p.KeyKeep)
				}
				return
			}
			if p.KeyKeep == nil || p.KeyKeep.State != c.state || (p.Steps[0].Op == "keep-key") != (c.state == "keep") {
				t.Fatalf("state %+v, first step %s", p.KeyKeep, p.Steps[0].Op)
			}
			if c.state == "keep" {
				kk := p.Steps[0].KeepKey
				if kk.Before != c.classes || kk.LBAs[0] == kk.LBAs[1] || kk.RecordSHA256 != keykeep.SHA256(keykeep.Encode(fx.Keys)) {
					t.Fatalf("keep-key step %+v", kk)
				}
			}
			mlCardinal(t, p)
		})
	}
}
