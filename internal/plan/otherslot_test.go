package plan_test

// The other-slot planner (mainline step 2, MAINLINE-STEP2-DESIGN.md §11):
// the plan of a made-up unit whose slot A runs mainline (the fixture's
// reviewed chain, boot_a = the assembled mainline image) and whose slot B
// holds Android, and every refusal.

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/keykeep"
	"uc-pxx-installer/internal/mainline/mltest"
	"uc-pxx-installer/internal/plan"
)

// osUBootArgs is a U-Boot command line as the shim copies it (docs/boot.md),
// NUL-terminated.
func osUBootArgs(slot, vbs, state string) []byte {
	return []byte("storagemedia=emmc androidboot.mode=normal androidboot.vbmeta.device_state=" + state +
		" androidboot.verifiedbootstate=" + vbs + " skip_initramfs androidboot.slot_suffix=_" + slot +
		" androidboot.serialno=X console=ttyFIQ0\x00")
}

const osToken = "mltest-1"

// osUnit: slot K runs mainline (the reviewed chain, boot_K = the assembled
// image of osToken), the other slot holds what (the old images or the
// reviewed chain), and backup holds the kept key (both copies) unless noKey.
func osUnit(t *testing.T, fx *fakeemmc.Fixture, ab [2][3]byte, k, other string, noKey bool) fakeemmc.Unit {
	t.Helper()
	_, res, _ := mlChain(t, fx)
	u := fakeemmc.Unit{AB: ab, Tweak: map[string][]byte{"boot_" + k: mltest.Expected(osToken, 30000, 11, res)}}
	if k == "a" {
		u.A, u.B = "new", other
	} else {
		u.A, u.B = other, "new"
	}
	if !noKey {
		u.Tweak["backup"] = osBackup(fx, 4096)
	}
	return u
}

// osBackup is a backup partition of n sectors with the key record of the
// fixture's key in both copies.
func osBackup(fx *fakeemmc.Fixture, n int64) []byte {
	b := make([]byte, n*512)
	rec := keykeep.Encode(fx.Keys)
	copy(b[(n-keykeep.FromEnd1)*512:], rec)
	copy(b[(n-keykeep.FromEnd2)*512:], rec)
	return b
}

type osWorld struct {
	in   plan.OtherSlotInput
	path string
}

func newOSWorld(t *testing.T, fx *fakeemmc.Fixture, u fakeemmc.Unit, k string) *osWorld {
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
	in := plan.OtherSlotInput{Input: plan.Input{Disk: f, DiskBytes: st.Size(), Source: "image:" + p, Mode: "dry-run", Tool: "test",
		Bootctl:       plan.Bootctl{Path: "/test/uc-pxx-bootctl", SHA256: strings.Repeat("ab", 32)},
		BootctlStatus: fakeStatus(sec), HashKeeper: true},
		Running:  plan.ParseRunning(osUBootArgs(k, "orange", "unlocked"), "console=ttyS2,115200n8 rdinit=/init uc-pxx.id="+osToken, "system_"+k),
		Mainline: mlPayload(t, b), Manifests: fx.Manifests, Gate: b.Gate(), Confirm: plan.ConfirmGate{Confirmed: true}, Data: true}
	in.Running.NetworkUp = true
	return &osWorld{in: in, path: p}
}

func osOps(p *plan.OtherSlotPlan) string {
	var ops []string
	for _, s := range p.Steps {
		ops = append(ops, s.Op)
	}
	return strings.Join(ops, " ")
}

func osRender(p *plan.OtherSlotPlan) string {
	var sb strings.Builder
	p.Render(&sb)
	return sb.String()
}

var osStep2 = [2][3]byte{{15, 0, 1}, {14, 0, 1}} // unit 2's S0: A mainline, B Android, both successful

func TestOtherSlotStep2OK(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	w := newOSWorld(t, fx, osUnit(t, fx, osStep2, "a", "new", false), "a")
	p, err := plan.ComputeOtherSlot(w.in)
	if err != nil {
		t.Fatalf("%v\n%s", err, osRender(p))
	}
	if p.Kind != plan.OtherSlotKind || p.Keeper != "a" || p.Target != "b" || p.Payload != nil || !p.VerifyID() || p.Plan.VerifyID() {
		t.Fatalf("kind %s keeper %s target %s", p.Kind, p.Keeper, p.Target)
	}
	// B holds the reviewed chain already: the four chain parts are skips, uboot_b is never written
	if got := osOps(p); got != "bootctl skip skip skip skip write write sync verify data-format data-migrate bootctl check-ab" {
		t.Fatalf("steps %s", got)
	}
	if a := strings.Join(p.Steps[0].Bootctl.Args, " "); a != "disable --slot b" {
		t.Fatalf("disable argv %s", a)
	}
	arm := p.Steps[len(p.Steps)-2].Bootctl
	if strings.Join(arm.Args, " ") != "arm-trying --slot b --lower-keeper" {
		t.Fatalf("arm argv %v", arm.Args)
	}
	if p.Steps[0].Bootctl.After.String() != "A{15,0,1} B{0,0,0}, boots next: A" {
		t.Fatalf("after disable %v", p.Steps[0].Bootctl.After)
	}
	fin := raw([3]byte{14, 0, 1}, [3]byte{15, 7, 0})
	if p.AB.Final.Hex32 != plan.State(fin).Hex32 {
		t.Fatalf("final %v", p.AB.Final)
	}
	wantSrc := map[string]string{"uboot_b": "keeper uboot_a", "trust_b": "keeper trust_a", "dtbo_b": "keeper dtbo_a",
		"vbmeta_b": "keeper vbmeta_a", "boot_b": "assembled-boot ", "system_b": "rootfs "}
	for _, s := range p.Steps {
		if s.Write == nil {
			continue
		}
		src := p.Source(s.Write.Partition)
		if src == nil || src.From+" "+src.KeeperPartition != wantSrc[s.Write.Partition] || src.SHA256 != s.Write.SHA256 {
			t.Fatalf("%s: source %+v", s.Write.Partition, src)
		}
	}
	_, res, _ := mlChain(t, fx)
	if p.Chain.AssembledSHA != mltest.SHA(mltest.Expected(osToken, 30000, 11, res)) || !p.Chain.VbmetaVerified {
		t.Fatalf("chain %+v", p.Chain)
	}
	if p.Key.Copies != 2 || !p.Key.Equal || p.Data == nil || p.Data.Partition != "userdata" || p.Data.Label != "tsxdata" ||
		p.Data.FormatAs != "tsxdata-new" || p.Data.Before != "no ext4" ||
		len(p.KeeperSHA256) != 5 || p.KeeperSHA256["boot_a"] == "" || p.KeeperSHA256["system_a"] != "" || p.KeeperSHA256["vendor_a"] != "" {
		t.Fatalf("key %+v data %+v keeper %d", p.Key, p.Data, len(p.KeeperSHA256))
	}
	// the A/B timeline: the keeper A is successful at every step, B unbootable until the arm
	for _, tl := range p.Timeline {
		raw, _ := tl.AB.Raw()
		a, _ := avbab.Parse(raw[:])
		if !a.Slots[0].Keeper() {
			t.Fatalf("step %d: keeper A %v", tl.Step, a.Slots[0])
		}
		if tl.Step > 0 && tl.Op != "bootctl" && tl.Op != "check-ab" && a.Slots[1].Bootable() {
			t.Fatalf("step %d (%s): B bootable %v", tl.Step, tl.Op, a.Slots[1])
		}
	}
	nw := strings.Join(p.NeverWritten, "|")
	if !strings.Contains(nw, "keeper slot A (the running system)") || !strings.Contains(nw, "backup: all of it") ||
		!strings.Contains(nw, "vendor and oem") || strings.Contains(nw, "userdata") {
		t.Fatalf("never written %v", p.NeverWritten)
	}
	// stable; the JSON round trip keeps the id; no other plan kind reads it as valid
	p2, err := plan.ComputeOtherSlot(w.in)
	if err != nil || p2.ID != p.ID {
		t.Fatal("plan id not stable")
	}
	b, _ := json.Marshal(p)
	var back plan.OtherSlotPlan
	if err := json.Unmarshal(b, &back); err != nil || !back.VerifyID() {
		t.Fatalf("JSON round trip: %v", err)
	}
	var ml plan.MainlinePlan
	var off plan.Plan
	if json.Unmarshal(b, &ml) != nil || ml.VerifyID() || json.Unmarshal(b, &off) != nil || off.VerifyID() {
		t.Fatal("an other-slot plan file reads as a valid mainline or official plan")
	}
	// never a byte of the key in the plan
	for _, part := range [][]byte{fx.Keys.Head[:], fx.Keys.IV[:]} {
		if bytes.Contains(b, part) || strings.Contains(string(b), strings.ToLower(hexOf(part))) {
			t.Fatal("the plan holds key bytes")
		}
	}
	r := osRender(p)
	for _, s := range []string{"bootctl disable --slot b", "bootctl arm-trying --slot b --lower-keeper", "running   slot \"a\"",
		"key kept  2 copies, equal true", "data      userdata", "source  boot_b    <- assembled-boot"} {
		if !strings.Contains(r, s) {
			t.Fatalf("render lacks %q:\n%s", s, r)
		}
	}
}

func hexOf(b []byte) string {
	const d = "0123456789abcdef"
	out := make([]byte, 0, 2*len(b))
	for _, c := range b {
		out = append(out, d[c>>4], d[c&15])
	}
	return string(out)
}

// A target that holds the old images gets the chain written (copies of A's).
func TestOtherSlotWritesChainWhenDifferent(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	w := newOSWorld(t, fx, osUnit(t, fx, osStep2, "a", "old", false), "a")
	p, err := plan.ComputeOtherSlot(w.in)
	if err != nil {
		t.Fatalf("%v\n%s", err, osRender(p))
	}
	if got := osOps(p); got != "bootctl write write write write write write sync verify data-format data-migrate bootctl check-ab" {
		t.Fatalf("steps %s", got)
	}
}

// Without Data: a repair of the other slot, no data steps, userdata never written.
func TestOtherSlotRepairNoData(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	w := newOSWorld(t, fx, osUnit(t, fx, osStep2, "a", "old", false), "a")
	w.in.Data = false
	p, err := plan.ComputeOtherSlot(w.in)
	if err != nil {
		t.Fatalf("%v\n%s", err, osRender(p))
	}
	if got := osOps(p); got != "bootctl write write write write write write sync verify bootctl check-ab" || p.Data != nil {
		t.Fatalf("steps %s data %v", got, p.Data)
	}
	if !strings.Contains(strings.Join(p.NeverWritten, "|"), "userdata") {
		t.Fatal("userdata not in the never-written list")
	}
}

// Running from slot B (the generalised mode): keeper B, target A.
func TestOtherSlotFromB(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	w := newOSWorld(t, fx, osUnit(t, fx, [2][3]byte{{14, 0, 1}, {15, 0, 1}}, "b", "old", false), "b")
	p, err := plan.ComputeOtherSlot(w.in)
	if err != nil {
		t.Fatalf("%v\n%s", err, osRender(p))
	}
	if p.Keeper != "b" || p.Target != "a" || strings.Join(p.Steps[0].Bootctl.Args, " ") != "disable --slot a" ||
		p.Source("boot_a") == nil || p.Source("uboot_a").KeeperPartition != "uboot_b" {
		t.Fatalf("keeper %s target %s", p.Keeper, p.Target)
	}
	fin := raw([3]byte{15, 7, 0}, [3]byte{14, 0, 1})
	if p.AB.Final.Hex32 != plan.State(fin).Hex32 {
		t.Fatalf("final %v", p.AB.Final)
	}
}

// A resumed step 2: B is disabled already ({0,0,0}).
func TestOtherSlotResumeDisabledTarget(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	w := newOSWorld(t, fx, osUnit(t, fx, [2][3]byte{{15, 0, 1}, {0, 0, 0}}, "a", "old", false), "a")
	p, err := plan.ComputeOtherSlot(w.in)
	if err != nil {
		t.Fatalf("%v\n%s", err, osRender(p))
	}
	if !p.Steps[0].Bootctl.NoOp {
		t.Fatal("the disable of a disabled target is not a no-op")
	}
}

func osFailed(p *plan.OtherSlotPlan) []string {
	var f []string
	for _, c := range p.Checks {
		if !c.OK {
			f = append(f, c.ID)
		}
	}
	return f
}

func TestOtherSlotRefusals(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	type tc struct {
		name  string
		ab    [2][3]byte
		other string
		noKey bool
		tweak map[string][]byte
		edit  func(w *osWorld)
		want  string // the failed check
	}
	run := func(c tc) {
		t.Run(c.name, func(t *testing.T) {
			ab := c.ab
			if ab == ([2][3]byte{}) {
				ab = osStep2
			}
			other := c.other
			if other == "" {
				other = "new"
			}
			u := osUnit(t, fx, ab, "a", other, c.noKey)
			for k, v := range c.tweak {
				u.Tweak[k] = v
			}
			w := newOSWorld(t, fx, u, "a")
			if c.edit != nil {
				c.edit(w)
			}
			p, err := plan.ComputeOtherSlot(w.in)
			if err == nil {
				t.Fatalf("accepted:\n%s", osRender(p))
			}
			f := osFailed(p)
			if !strings.Contains(" "+strings.Join(f, " ")+" ", " "+c.want+" ") || len(p.Steps) != 0 || p.ID != "" {
				t.Fatalf("failed %v, want %s (steps %d id %q): %v", f, c.want, len(p.Steps), p.ID, err)
			}
		})
	}
	run_ := func(name, want string, edit func(w *osWorld)) { run(tc{name: name, want: want, edit: edit}) }
	running := func(ub []byte, cmd, root string) func(w *osWorld) {
		return func(w *osWorld) { w.in.Running = plan.ParseRunning(ub, cmd, root); w.in.Running.NetworkUp = true }
	}
	cmd := "console=ttyS2 uc-pxx.id=" + osToken
	run_("a RAM boot (no u-boot,bootargs)", "running-slot", running(nil, cmd, "system_a"))
	run_("no slot word", "running-slot", running([]byte("androidboot.verifiedbootstate=orange\x00"), cmd, "system_a"))
	run_("two slot words", "running-slot", running([]byte("androidboot.slot_suffix=_a androidboot.slot_suffix=_b\x00"), cmd, "system_a"))
	run_("the installer RAM image", "running-slot", running(osUBootArgs("a", "orange", "unlocked"), cmd+" p10inst.ram=1", "system_a"))
	run_("locked (green)", "unlocked", running(osUBootArgs("a", "green", "locked"), cmd, "system_a"))
	run_("orange but locked", "unlocked", running(osUBootArgs("a", "orange", "locked"), cmd, "system_a"))
	run_("root is system_b", "running-root", running(osUBootArgs("a", "orange", "unlocked"), cmd, "system_b"))
	run_("no token", "running-token", running(osUBootArgs("a", "orange", "unlocked"), "console=ttyS2", "system_a"))
	run_("two tokens", "running-token", running(osUBootArgs("a", "orange", "unlocked"), cmd+" uc-pxx.id=other", "system_a"))
	run_("the running kernel is not boot_a's image", "running-image", running(osUBootArgs("a", "orange", "unlocked"),
		"console=ttyS2 uc-pxx.id=other-1", "system_a"))
	run_("U-Boot says B, the unit runs A's root", "running-root", running(osUBootArgs("b", "orange", "unlocked"), cmd, "system_a"))
	run(tc{name: "U-Boot says B (keeper B), but B is no mainline: no token", ab: [2][3]byte{{14, 0, 1}, {15, 0, 1}},
		edit: running(osUBootArgs("b", "orange", "unlocked"), cmd, "system_b"), want: "running-image"})
	run(tc{name: "the running slot A is trying", ab: [2][3]byte{{15, 6, 0}, {14, 0, 1}}, want: "keeper"})
	run(tc{name: "the running slot A is not the pick", ab: [2][3]byte{{14, 0, 1}, {15, 0, 1}}, want: "keeper-boots"})
	run(tc{name: "a tie", ab: [2][3]byte{{15, 0, 1}, {15, 0, 1}}, want: "keeper-boots"})
	run(tc{name: "no key record", noKey: true, want: "key-kept"})
	run(tc{name: "one key copy", tweak: map[string][]byte{"backup": func() []byte {
		b := osBackup(fx, 4096)
		copy(b[(4096-keykeep.FromEnd2)*512:], make([]byte, 512))
		return b
	}()}, want: "key-kept"})
	run(tc{name: "two different key copies", tweak: map[string][]byte{"backup": func() []byte {
		b := osBackup(fx, 4096)
		k := fx.Keys
		k.IV[0] ^= 1
		copy(b[(4096-keykeep.FromEnd2)*512:], keykeep.Encode(k))
		return b
	}()}, want: "key-kept"})
	run(tc{name: "a damaged key copy", tweak: map[string][]byte{"backup": func() []byte {
		b := osBackup(fx, 4096)
		b[(4096-keykeep.FromEnd1)*512+30] ^= 1
		return b
	}()}, want: "key-kept"})
	run_("no confirmation", "confirm", func(w *osWorld) { w.in.Confirm = plan.ConfirmGate{} })
	run_("no network now", "network", func(w *osWorld) { w.in.Running.NetworkUp = false })
	// userdata holds a finished /data (step 2 done, or a stale replay): the Data mode refuses
	run(tc{name: "userdata is a finished /data", tweak: map[string][]byte{"userdata": mltest.Rootfs(1<<20, "tsxdata", 5)}, want: "data"})
	run_("no gate", "payload-gate", func(w *osWorld) { w.in.Gate = nil })
	run_("an official payload as well", "otherslot-input", func(w *osWorld) { w.in.Payload = &plan.Payload{} })
	run_("a keeper choice", "otherslot-input", func(w *osWorld) { w.in.Keeper = "b" })
	run_("a pre-fastboot sector", "otherslot-input", func(w *osWorld) { w.in.PreFastboot = make([]byte, 32) })
	run_("a RAM image record", "otherslot-input", func(w *osWorld) { w.in.RAMImage = &plan.RAMImage{Token: "p10inst-x"} })
	run_("a keep-key input", "otherslot-input", func(w *osWorld) { w.in.KeySlots = []string{"b"} })
	run_("device mode (the RAM installer's)", "mode", func(w *osWorld) { w.in.Mode = "device" })
	run_("slot mode without the device", "mode", func(w *osWorld) { w.in.Mode, w.in.Source = "slot", "device:/dev/mmcblk0" })
	run_("bootctl status disagrees", "bootctl-status", func(w *osWorld) { w.in.BootctlStatus.Slots[1][0] = 3 })
	run_("a pinned bootctl that is not the binary", "bootctl-binary", func(w *osWorld) { w.in.RequireBootctlSHA256 = strings.Repeat("cd", 32) })
	// the keeper A's chain is not the reviewed one (A holds the old uboot)
	run(tc{name: "the keeper's uboot is not the reviewed chain", tweak: map[string][]byte{"uboot_a": fx.Old["uboot"]}, want: "keeper-chain"})
	run(tc{name: "the keeper's resource is not the reviewed one", tweak: map[string][]byte{"boot_a": func() []byte {
		return mltest.Expected(osToken, 30000, 11, bytes.Repeat([]byte{7}, 4096))
	}()}, want: "keeper-chain"})
}

// The step-1 gate is the same: a payload that the gate does not list is refused.
func TestOtherSlotUnreviewedPayload(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	w := newOSWorld(t, fx, osUnit(t, fx, osStep2, "a", "new", false), "a")
	other := mltest.NewBundle("other-9", 30001, 1<<20, mltest.Chain{}, nil, 0)
	w.in.Gate = other.Gate()
	p, err := plan.ComputeOtherSlot(w.in)
	if err == nil {
		t.Fatalf("an unreviewed payload was accepted:\n%s", osRender(p))
	}
}

func TestParseRunning(t *testing.T) {
	r := plan.ParseRunning(osUBootArgs("b", "orange", "unlocked"), "console=ttyS2 uc-pxx.id=tok-1 rdinit=/init", "system_b")
	if !r.UBootArgs || r.Slot != "b" || r.SlotWords != 1 || r.VerifiedBootState != "orange" || r.DeviceState != "unlocked" ||
		r.Token != "tok-1" || r.RAMImage || r.Root != "system_b" {
		t.Fatalf("%+v", r)
	}
	for _, c := range []struct {
		ub   []byte
		slot string
		n    int
	}{
		{nil, "", 0},
		{[]byte{}, "", 0},
		{[]byte("androidboot.slot_suffix=_c\x00"), "", 1},
		{[]byte("androidboot.slot_suffix=a\x00"), "", 1},
		{[]byte("androidboot.slot_suffix=_a\x00androidboot.slot_suffix=_a\x00"), "", 2},
		{[]byte("xandroidboot.slot_suffix=_a\x00"), "", 0},
		{[]byte("androidboot.slot_suffix=_a\tconsole=x\x00"), "a", 1},
	} {
		r := plan.ParseRunning(c.ub, "", "")
		if r.Slot != c.slot || r.SlotWords != c.n || r.UBootArgs != (c.ub != nil) {
			t.Fatalf("%q: %+v", c.ub, r)
		}
	}
	if r := plan.ParseRunning(nil, "uc-pxx.id=a uc-pxx.id=b", ""); r.Token != "" {
		t.Fatal("two tokens give a token")
	}
	if r := plan.ParseRunning(nil, "token=p10inst-83c4f7", ""); !r.RAMImage {
		t.Fatal("a p10inst- token is not the RAM image")
	}
}

// keyKeep LBAs on the real layout are the reviewed ones (KEY-KEEP.md §3.1).
func TestOtherSlotKeyLBAsOnTheUnitLayout(t *testing.T) {
	lbas, err := keykeep.LBAs(446464, 32768)
	if err != nil || lbas != keykeep.UnitLBAs || lbas[0] != 477184 || lbas[1] != 479231 {
		t.Fatalf("%v %v", lbas, err)
	}
	_ = gpt.Sector
}
