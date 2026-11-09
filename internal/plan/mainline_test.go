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
	return mltest.NewBundle("mltest-1", 30000, 1<<20, ch, res, cap)
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
		Mainline: mlPayload(t, b), Manifests: fx.Manifests, UnlockConfirmed: true}
	img, _ := os.ReadFile(p)
	return &mlWorld{in: in, img: img}
}

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
	if got := mlOps(p); got != "bootctl write write write write write write sync verify bootctl check-ab" {
		t.Fatalf("steps %s", got)
	}
	if a := p.Steps[0].Bootctl.Args; strings.Join(a, " ") != "disable --slot a" {
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
	if p.Steps[0].Bootctl.After.String() != "A{0,0,0} B{15,0,1}, boots next: B" {
		t.Fatalf("after disable %v", p.Steps[0].Bootctl.After)
	}
	wantSrc := map[string]string{"uboot_a": "keeper uboot_b", "trust_a": "keeper trust_b", "dtbo_a": "keeper dtbo_b",
		"vbmeta_a": "keeper vbmeta_b", "boot_a": "assembled-boot ", "system_a": "rootfs "}
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
	// the keeper's bytes are the reviewed chain, and they are what A gets
	for _, n := range []string{"uboot", "trust", "dtbo", "vbmeta"} {
		if p.Source(n+"_a").SHA256 != fx.ManifestPartSHA(n) {
			t.Fatalf("%s source sha", n)
		}
	}
	ch, res, cap := mlChain(t, fx)
	asm := mltest.Expected("mltest-1", 30000, 11, res)
	if p.Chain.AssembledSHA != mltest.SHA(asm) || p.Chain.BootCap != cap || !p.Chain.VbmetaVerified ||
		p.Source("boot_a").ResourceSHA256 != ch.Resource || p.Chain.Firmware != fx.Manifests[0].Name() {
		t.Fatalf("chain %+v", p.Chain)
	}
	if len(p.KeeperSHA256) != 8 || !p.Unlock.Confirmed || p.AutoUpdate.State != "off" {
		t.Fatalf("keeper hashes %d unlock %v", len(p.KeeperSHA256), p.Unlock)
	}
	// the never-written list names vendor_a, oem_a and all of backup
	nw := strings.Join(p.NeverWritten, "|")
	if !strings.Contains(nw, "vendor_a and oem_a") || !strings.Contains(nw, "backup: all of it") || !strings.Contains(nw, "slot B") {
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
		{"A 14 successful, B 15", mlUnit, "bootctl write write write write write write sync verify bootctl check-ab",
			"A{15,7,0} B{14,0,1}, boots next: A", false},
		{"trust_a identical: skipped", fakeemmc.Unit{AB: mlUnit.AB, A: "old", B: "new", Tweak: map[string][]byte{"trust_a": fx.New["trust"]}},
			"bootctl write skip write write write write sync verify bootctl check-ab", "A{15,7,0} B{14,0,1}, boots next: A", false},
		{"keeper B 14: not lowered", fakeemmc.Unit{AB: [2][3]byte{{13, 0, 1}, {14, 0, 1}}, A: "old", B: "new"},
			"bootctl write write write write write write sync verify bootctl check-ab", "A{15,7,0} B{14,0,1}, boots next: A", false},
		{"A disabled already", fakeemmc.Unit{AB: [2][3]byte{{0, 0, 0}, {15, 0, 1}}, A: "old", B: "new"},
			"bootctl write write write write write write sync verify bootctl check-ab", "A{15,7,0} B{14,0,1}, boots next: A", true},
		{"A trying, lower", fakeemmc.Unit{AB: [2][3]byte{{3, 5, 0}, {15, 0, 1}}, A: "old", B: "new"},
			"bootctl write write write write write write sync verify bootctl check-ab", "A{15,7,0} B{14,0,1}, boots next: A", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, err := plan.ComputeMainline(newMLWorld(t, fx, c.unit, nil).in)
			if err != nil {
				t.Fatalf("%v\n%s", err, mlRender(p))
			}
			if mlOps(p) != c.ops || p.AB.Final.String() != c.final || p.Steps[0].Bootctl.NoOp != c.noopDi {
				t.Fatalf("ops %s final %s noop %v", mlOps(p), p.AB.Final, p.Steps[0].Bootctl.NoOp)
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
		{"boot cap differs from the vbmeta", fx, mlUnit, func(b *mltest.Bundle) {
			b.Manifest["vbmeta_boot_cap"] = b.Manifest["vbmeta_boot_cap"].(int64) + 2048
		}, nil, "boot-cap"},
		{"assembled sha is not the manifest's", fx, mlUnit, func(b *mltest.Bundle) {
			b.Sub("boot_assembled")["sha256"] = strings.Repeat("e", 64)
		}, nil, "boot-assembled"},
		{"assembled size is not the manifest's", fx, mlUnit, func(b *mltest.Bundle) {
			b.Sub("boot_assembled")["size"] = b.Sub("boot_assembled")["size"].(int) + 2048
		}, nil, "boot-assembled"},
		{"boot larger than boot_a", fx, fakeemmc.Unit{Layout: smallPart("boot_a", 200), AB: mlUnit.AB, A: "old", B: "new"}, nil, nil, "write-boot_a"},
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
