package plan

// The mainline branch of the plan (MAINLINE-INSTALL-PLAN.md, phase 3). It is
// selected only by the payload kind: the caller gives a mainline payload
// (package mainline) to ComputeMainline instead of an official firmware to
// Compute. Compute and every function it uses are unchanged; this file only
// adds. The decisions are the PROVISIONAL ones of the plan (Q1-Q14):
//
//   - the target is always slot A and the keeper always slot B (Q1): today's
//     mainline boot control works only in slot A;
//   - the keeper B must be successful, the slot that boots now, and hold the
//     reviewed 1.0.0.94 userdebug boot chain: its uboot_b, trust_b, dtbo_b,
//     vbmeta_b and the resource of boot_b are read and hashed here and must be
//     the manifest's requires_bootchain and a reviewed firmware manifest that
//     the release's firmware gate opens (Q5);
//   - no R(K): a both-trying start (the BCB-route reset) is refused; the
//     mainline entry is the write-free register route from Android in B (Q2,
//     Q11);
//   - the user confirmed that the unit is unlocked (UnlockFlag, Q4);
//   - the auto-update gate, the panel and the prerequisites as in Compute;
//     the model gate UC-P10 (Q14);
//   - vbmeta_a is the patched vbmeta of contract v2 (the owner's decision of
//     2026-11-09, replacing Q3's "signed, unchanged"): the keeper's verified
//     signed vbmeta_b with only its boot image_size changed (mainline.PatchVbmeta),
//     exactly the manifest's vbmeta_patched and, with the release gate, a
//     reviewed patch; its rollback index and ATX key versions are the keeper's
//     (the header and the key metadata are not changed); the boot image must
//     fit the patched image_size, which must fit boot_a;
//   - vendor_a and oem_a are not written (Q6).
//
// The steps are the order of Compute, unchanged: disable A -> write uboot_a,
// trust_a (skipped when identical), dtbo_a, vbmeta_a, boot_a, system_a -> sync
// -> verify -> arm-trying --slot a --lower-keeper -> check-ab. The bootctl argv
// forms are Compute's.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"uc-pxx-installer/internal/avb"
	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/manifest"
	"uc-pxx-installer/internal/nvram"
)

const (
	// MainlineKind is the kind of a mainline plan. The official executor
	// never runs one: its id is the sha256 of the whole mainline plan, so
	// the embedded Plan's own VerifyID fails.
	MainlineKind = "p10inst-mainline-plan"
	// UnlockFlag is the user's confirmation that the unit is unlocked.
	UnlockFlag = "--unlock-confirmed"
	// MainlineTarget and MainlineKeeper are fixed (Q1).
	MainlineTarget = "a"
	MainlineKeeper = "b"
)

// MainlineParts are the target partitions of a mainline install, in the
// order of the writes (SlotParts without vendor and oem, Q6).
func MainlineParts() []string { return []string{"uboot", "trust", "dtbo", "vbmeta", "boot", "system"} }

// mainlineChainParts are the parts that slot A gets from the keeper.
func mainlineChainParts() []string { return []string{"uboot", "trust", "dtbo", "vbmeta"} }

// MainlineNeverWritten is the never-written list of a mainline plan.
func MainlineNeverWritten() []string {
	return []string{
		"sectors 0-2047: protective MBR, primary GPT, idbloader (sector 64)",
		"the backup GPT (the last 33 sectors)",
		"misc, except the A/B sector LBA 49156, and that only through uc-pxx-bootctl",
		"the BCB (misc LBA 49184): the installer never sets it",
		"security, cfg, userdata, frp, metadata, cache, others, resource, kernel, dtb",
		MainlineKeepKeyNeverWritten,
		"vendor_a and oem_a (a mainline install leaves them as they are)",
		"every partition of the keeper slot B",
	}
}

// ChainPart is one part of the keeper's boot chain as the plan read it.
type ChainPart struct {
	Part      string `json:"part"`      // "uboot", ..., "resource"
	Partition string `json:"partition"` // the keeper partition read, "uboot_b"
	Offset    int64  `json:"offset"`    // in the partition (the resource: in boot_b)
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`      // read and hashed when the plan was made
	Want      string `json:"want_sha256"` // the manifest's requires_bootchain = the reviewed firmware manifest
}

// MainlineChain is the boot chain that slot A gets: the reviewed firmware
// manifest it is from, the keeper's parts, the vbmeta facts and the assembled
// boot image.
type MainlineChain struct {
	Firmware       string      `json:"reviewed_firmware"`
	FirmwareSHA256 string      `json:"reviewed_firmware_sha256"`
	Version        string      `json:"version"`
	BuildType      string      `json:"build_type"`
	UBootBuild     string      `json:"uboot_build"`
	Keeper         []ChainPart `json:"keeper_parts"`
	// VbmetaVerified: the keeper's vbmeta (the bytes vbmeta_a gets) passed
	// the AVB verifier with the reviewed manifest's pins, and its facts are
	// the manifest's.
	VbmetaVerified bool `json:"vbmeta_verified"`
	// SignedBootSize is the image_size of the boot hash descriptor of the
	// signed vbmeta_b; BootCap the patched one of vbmeta_a (contract v2).
	SignedBootSize int64                `json:"vbmeta_signed_boot_size"`
	BootCap        int64                `json:"vbmeta_boot_cap"`
	VbmetaPatch    *mainline.PatchFacts `json:"vbmeta_patch"`
	RollbackIndex  uint64               `json:"rollback_index"`
	PIKKeyVersion  uint64               `json:"pik_key_version"`
	PSKKeyVersion  uint64               `json:"psk_key_version"`
	AssembledSize  int64                `json:"boot_assembled_size"`
	AssembledSHA   string               `json:"boot_assembled_sha256"`
	TemplateSHA    string               `json:"boot_template_sha256"`
}

// WriteSource says where the bytes of one write step come from.
type WriteSource struct {
	Partition string `json:"partition"` // the target partition, "uboot_a"
	// From: "keeper" (the first Length bytes of KeeperPartition),
	// "patched-vbmeta" (the first Length bytes of KeeperPartition vbmeta_b,
	// sha256 SourceSHA256, patched by mainline.PatchVbmeta to BootImageSize),
	// "assembled-boot" (the payload template with the resource of the
	// keeper's boot_b, mainline.Assemble) or "rootfs" (rootfs.ext4).
	From            string `json:"from"`
	KeeperPartition string `json:"keeper_partition,omitempty"`
	Length          int64  `json:"length"`
	SHA256          string `json:"sha256"`
	TemplateSHA256  string `json:"template_sha256,omitempty"`
	ResourceSHA256  string `json:"resource_sha256,omitempty"`
	SourceSHA256    string `json:"source_sha256,omitempty"`
	BootImageSize   int64  `json:"boot_image_size,omitempty"`
}

// ABPoint is the predicted A/B state after one step of the plan.
type ABPoint struct {
	Step   int     `json:"step"`
	Op     string  `json:"op"`
	AB     ABState `json:"ab"`
	Keeper string  `json:"keeper"` // the keeper slot's state ("successful")
	Target string  `json:"target"` // the target slot's state
	Boots  string  `json:"boots_next"`
}

// UnlockGate is the unlock confirmation (Q4).
type UnlockGate struct {
	Confirmed bool   `json:"confirmed"`
	Flag      string `json:"flag"`
	Why       string `json:"why"`
}

// MainlinePlan is a mainline plan: the fields of Plan (Kind MainlineKind, no
// official payload), the mainline payload, the boot chain, the source of
// every write, the unlock gate and the predicted A/B state at every step.
type MainlinePlan struct {
	Plan
	Mainline *mainline.Payload `json:"mainline"`
	Chain    *MainlineChain    `json:"bootchain"`
	Sources  []WriteSource     `json:"write_sources"`
	Unlock   UnlockGate        `json:"unlock"`
	Timeline []ABPoint         `json:"ab_timeline"`
	// Gate is what the payload gate found (mainline.Gate: a reviewed
	// payload, the reviewed kernel Image, the pinned boot-control files).
	Gate *mainline.GateRecord `json:"payload_gate"`
}

// Seal sets the ID: the sha256 of the whole mainline plan's JSON without
// the ID.
func (p *MainlinePlan) Seal() {
	p.ID = ""
	b, _ := json.Marshal(p)
	s := sha256.Sum256(b)
	p.ID = hex.EncodeToString(s[:])
}

// VerifyID recomputes the ID of a mainline plan.
func (p *MainlinePlan) VerifyID() bool {
	q := *p
	q.ID = ""
	b, _ := json.Marshal(&q)
	s := sha256.Sum256(b)
	return p.ID != "" && hex.EncodeToString(s[:]) == p.ID
}

// Source is the write source of a target partition (nil if none).
func (p *MainlinePlan) Source(partition string) *WriteSource {
	for i := range p.Sources {
		if p.Sources[i].Partition == partition {
			return &p.Sources[i]
		}
	}
	return nil
}

// MainlineInput is what ComputeMainline needs: the fields of Input that
// Compute uses for the disk, the mode, the A/B tool, the gates and the
// device (Payload, Keeper other than "b", KeySlots, KeyKeep and KnownBefore
// must be empty), the checked mainline payload, the reviewed firmware
// manifests (the embedded ones; tests give their own) and the unlock
// confirmation.
type MainlineInput struct {
	Input
	Mainline        *mainline.Payload
	Manifests       []*manifest.Manifest
	UnlockConfirmed bool
	// Gate is the payload gate: mainline.EmbeddedGate() (the release's
	// reviewed payloads, kernels and boot-control pins); tests give a
	// mainline.TestGate. A nil gate refuses.
	Gate *mainline.Gate
	// RecheckBefore (device mode only) are the before-hashes of the
	// always-written slot A parts from the plan that a person approved
	// (MainlineKnownBeforeOf), for the re-check of that plan: those bytes are
	// replaced and no step decides by them, so they are not read again. The
	// trust_a hash (it decides the skip) is always read.
	RecheckBefore map[string]string
	// MainlineKey is the keep-key input (task 2): the record of the key that
	// the first drive phase proved, and its two sectors now. nil refuses: a
	// mainline install keeps the key before step 2 needs it.
	MainlineKey *MainlineKeyInput
}

// MainlineKnownBeforeOf are the before-hashes of the always-written parts of
// a mainline plan (every write step but trust_a), for RecheckBefore.
func MainlineKnownBeforeOf(p *MainlinePlan) map[string]string {
	kb := map[string]string{}
	for _, s := range p.Steps {
		if s.Op != "write" || s.Write == nil || len(s.Write.BeforeSHA256) != 64 || s.Write.Partition == "trust_"+p.Target {
			continue
		}
		kb[s.Write.Partition] = s.Write.BeforeSHA256
	}
	return kb
}

// mainlineChainManifest finds the reviewed firmware manifest of the
// required boot chain: a manifest that the firmware gate opens (Gate) whose
// version, build type, uboot, trust, dtbo, vbmeta images and boot resource
// are exactly the required ones. Exactly one must match.
func mainlineChainManifest(ms []*manifest.Manifest, rb mainline.Bootchain) (*manifest.Manifest, string) {
	var found []*manifest.Manifest
	for _, m := range ms {
		if Gate(m) != "" {
			continue
		}
		u, _ := m.Part("uboot")
		tr, _ := m.Part("trust")
		d, _ := m.Part("dtbo")
		v, _ := m.Part("vbmeta")
		bc := m.BootChain
		if m.Firmware.Version == rb.Version && m.Build["ro.build.type"] == rb.BuildType &&
			bc.UBootSHA256 == rb.UBootSHA256 && u.SHA256 == rb.UBootSHA256 &&
			bc.TrustSHA256 == rb.TrustSHA256 && tr.SHA256 == rb.TrustSHA256 &&
			d.SHA256 == rb.DtboSHA256 && v.SHA256 == rb.VbmetaSHA256 && bc.BootResourceSHA256 == rb.ResourceSHA256 {
			found = append(found, m)
		}
	}
	switch len(found) {
	case 0:
		return nil, "no firmware manifest that this release opens has this boot chain"
	case 1:
		return found[0], ""
	}
	return nil, fmt.Sprintf("%d opened firmware manifests have this boot chain: not unique", len(found))
}

func slotState(s avbab.Slot) string { return short(s) }

// ComputeMainline makes a mainline plan. On a refusal it returns the plan
// (with the failed checks, no steps) and ErrRefused.
func ComputeMainline(in MainlineInput) (*MainlinePlan, error) {
	c := &checker{}
	p := &MainlinePlan{Plan: Plan{Schema: Schema, Kind: MainlineKind, Tool: in.Tool, Mode: in.Mode, Bootctl: in.Bootctl,
		NeverWritten: MainlineNeverWritten(), Vbmeta: map[string]VbmetaInfo{},
		KeySource: "none: a mainline install reads no firmware container"},
		Mainline: in.Mainline, Unlock: UnlockGate{Confirmed: in.UnlockConfirmed, Flag: UnlockFlag}}
	refuse := func() (*MainlinePlan, error) {
		p.Checks = c.list
		p.Steps, p.Sources, p.Timeline = nil, nil, nil
		return p, fmt.Errorf("%w: %s", ErrRefused, strings.Join(c.failed(), "; "))
	}
	switch {
	case in.Mode == "dry-run" && strings.HasPrefix(in.Source, "image:"):
		c.add("mode", true, "dry run: every write goes to %s", in.Source)
	case in.Mode == "device" && in.Source == "device:/dev/mmcblk0" && in.Device != nil && in.Device.Node == "/dev/mmcblk0" &&
		in.HashKeeper && in.RequireBootctlSHA256 != "" && in.RAMImage != nil && in.RAMImage.Token != "" &&
		in.RAMImage.BootID != "" && in.FirmwareIdent != "":
		p.Device = in.Device
		p.RAMImage = in.RAMImage
		p.FirmwareIdent = in.FirmwareIdent
		c.add("mode", true, "device: the writes go to the eMMC partition nodes of slot A, the A/B sector only through uc-pxx-bootctl-inst")
		c.add("device", true, "%s: %s, eMMC on %s, sysfs layout %s", in.Device.Node, in.Device.Model, in.Device.Host,
			in.Device.SysfsLayoutSHA256[:min(16, len(in.Device.SysfsLayoutSHA256))])
		um := in.Device.UnitModel
		if !c.add("unit-model", um.ChecksumOK && um.Base != "" && um.DTModel == in.Device.Model && DTBase(um.DTModel) == um.Base,
			"the unit's factory data says %q (%s, suffix %q, hardware %s; checksum valid %v); this RAM image is for the %s",
			um.FactoryModel, um.Base, um.Suffix, orNone(um.HWRev), um.ChecksumOK, DTBase(in.Device.Model)) {
			return refuse()
		}
		if !c.add("mainline-model", um.Base == mainline.Model, "a mainline install is only for a %s (Q14); this unit is a %s",
			mainline.Model, um.Base) {
			return refuse()
		}
	default:
		c.add("mode", false, "mode %q with source %q: only a dry run on an image file, or the device mode on /dev/mmcblk0 with the device checked, the RAM image token and boot id and the payload identity recorded, the keeper hashed and the bootctl sha256 pinned", in.Mode, in.Source)
		return refuse()
	}
	// The inputs that belong to the official branch only.
	var wrong []string
	if in.Payload != nil {
		wrong = append(wrong, "an official firmware payload")
	}
	if in.Keeper != "" && in.Keeper != MainlineKeeper {
		wrong = append(wrong, "keeper "+in.Keeper+" (the mainline keeper is always b)")
	}
	if len(in.KeySlots) > 0 || in.KeyKeep != nil {
		wrong = append(wrong, "a firmware key or a keep-key step")
	}
	if in.KnownBefore != nil {
		wrong = append(wrong, "known before-hashes")
	}
	if in.RecheckBefore != nil && in.Mode != "device" {
		wrong = append(wrong, "the re-check before-hashes of a device plan")
	}
	if !c.add("mainline-input", len(wrong) == 0, "a mainline plan takes only a mainline payload%s", listText(" (given: ", wrong, ")")) {
		return refuse()
	}

	// The GPT (as Compute).
	t, err := gpt.Parse(in.Disk, in.DiskBytes)
	if !c.add("gpt", err == nil, "%s", errText(err, "primary and backup GPT valid, CRCs match, no overlap, no duplicate name")) {
		return refuse()
	}
	p.Disk = Disk{Source: in.Source, Sectors: t.DiskSectors, LayoutSHA256: t.LayoutSHA256()}
	misc, ok := t.Find("misc")
	c.add("gpt-misc", ok && misc.Start == MiscStart && misc.Contains(ABLBA),
		"misc must start at LBA %d and hold the A/B sector %d (found: %v at %d)", MiscStart, ABLBA, ok, misc.Start)
	var missing []string
	for _, s := range []string{"a", "b"} {
		for _, n := range SlotParts {
			if _, ok := t.Find(n + "_" + s); !ok {
				missing = append(missing, n+"_"+s)
			}
		}
	}
	c.add("gpt-slots", len(missing) == 0, "every slot partition exists once%s", listText(" (missing: ", missing, ")"))
	if len(c.failed()) > 0 {
		return refuse()
	}

	// The A/B sector (as Compute).
	sector := make([]byte, gpt.Sector)
	if _, err := in.Disk.ReadAt(sector, ABLBA*gpt.Sector); err != nil {
		c.add("ab-read", false, "read LBA %d: %v", ABLBA, err)
		return refuse()
	}
	var now [32]byte
	copy(now[:], sector)
	cur, _ := avbab.Parse(sector)
	sum := sha256.Sum256(sector)
	p.AB = AB{LBA: ABLBA, Now: State(now), SectorSHA256: hex.EncodeToString(sum[:])}
	if err := cur.Validate(); !c.add("ab-valid", err == nil, "%s", errText(err, "magic, version, big-endian CRC valid; both slots legal; a slot is bootable")) {
		return refuse()
	}
	if in.BootctlStatus == nil {
		c.add("bootctl-status", false, "uc-pxx-bootctl status was not run")
	} else if err := in.BootctlStatus.Agrees(cur); err != nil {
		c.add("bootctl-status", false, "%v", err)
	} else {
		c.add("bootctl-status", true, "uc-pxx-bootctl status agrees with the installer's decoder")
	}
	if in.RequireBootctlSHA256 != "" {
		c.add("bootctl-binary", in.Bootctl.Path != "" && in.Bootctl.SHA256 == in.RequireBootctlSHA256,
			"%s sha256 %s must be the reviewed binary %s", in.Bootctl.Path, in.Bootctl.SHA256, in.RequireBootctlSHA256)
	} else {
		c.add("bootctl-binary", in.Bootctl.Path != "" && len(in.Bootctl.SHA256) == 64, "%s sha256 %s", in.Bootctl.Path, in.Bootctl.SHA256)
	}
	if in.PreFastboot != nil {
		if len(in.PreFastboot) < 32 {
			c.add("pre-fastboot", false, "the pre-fastboot A/B sector is shorter than 32 bytes")
		} else {
			a, _ := avbab.Parse(in.PreFastboot)
			var raw [32]byte
			copy(raw[:], in.PreFastboot)
			st := State(raw)
			p.AB.PreFastboot = &st
			switch {
			case a.Validate() != nil:
				c.add("pre-fastboot", false, "the pre-fastboot A/B sector is not valid: %v", a.Validate())
			case raw != now:
				c.add("pre-fastboot", false, "the A/B sector changed after the entry page read it (%s, now %s): the mainline entry is the write-free register route",
					st, p.AB.Now)
			default:
				c.add("pre-fastboot", true, "the live A/B sector is the one the entry page read before fastboot entry")
			}
		}
	} else {
		c.add("pre-fastboot", true, "not given: the keeper comes from the live sector")
	}
	if len(c.failed()) > 0 {
		return refuse()
	}

	// The keeper: always B, successful, and the slot that boots now. No
	// R(K): the mainline entry writes nothing to the A/B sector.
	const k, tg = 1, 0
	p.Keeper, p.Target = MainlineKeeper, MainlineTarget
	if !c.add("no-reestablish", !reestablishEligible(cur),
		"the start must not be the both-trying reset of the BCB route (now %s): a mainline install never re-establishes the keeper (no R(K)); its entry is the write-free register route from Android in slot B",
		p.AB.Now) {
		return refuse()
	}
	if !c.add("keeper", cur.Slots[k].Keeper(), "slot B must be successful {prio>=1, tries 0, succ 1}: %v", cur.Slots[k]) {
		return refuse()
	}
	if !c.add("keeper-boots", !cur.Tie() && cur.Pick() == k,
		"slot B must be the slot that boots now (the strictly higher priority of the bootable slots; never the tie rule): %s", p.AB.Now) {
		return refuse()
	}
	p.KeeperWhy = "slot B, Android userdebug: successful and the slot that boots now (a mainline install always keeps B and writes A)"
	c.add("target", true, "target A is not the keeper, and the keeper B is bootable")

	// The predicted A/B states (as Compute).
	s1, err1 := PredictDisable(now, tg)
	s2, err2 := PredictArm(s1, tg, true)
	if err1 != nil || err2 != nil {
		c.add("ab-predict", false, "bootctl would refuse: %v %v", err1, err2)
		return refuse()
	}
	var why []string
	if err := keeperStaysBootable(now, s1, k, false); err != nil {
		why = append(why, "after disable: "+err.Error())
	}
	if err := keeperStaysBootable(now, s2, k, true); err != nil {
		why = append(why, "after arm: "+err.Error())
	}
	a1, _ := avbab.Parse(s1[:])
	a2, _ := avbab.Parse(s2[:])
	if a1.Pick() != k || a1.Slots[tg].Bootable() {
		why = append(why, "after disable the target would still be bootable")
	}
	if a2.Pick() != tg || a2.Slots[tg] != (avbab.Slot{Priority: 15, Tries: 7}) || a2.Slots[tg].Priority <= a2.Slots[k].Priority {
		why = append(why, "after arm the target would not boot next with a strictly higher priority")
	}
	c.add("ab-predict", len(why) == 0, "every state stays legal, the keeper stays successful in each, only its priority may drop 15->14%s",
		listText(": ", why, ""))

	// The payload.
	ml := in.Mainline
	if !c.add("payload", ml.Verified() && ml.Kind == mainline.Kind, "the mainline payload must be checked by pass 1 (mainline.Verify)") {
		return refuse()
	}
	mm := ml.Manifest
	if err := mm.Check(); !c.add("mainline-manifest", err == nil, "%s %s, model %s, kernel %s, token %s (manifest sha256 %s; %s): %s",
		mm.Kind, mm.Version, mm.Model, shortSHA(mm.Kernel.Rev), mm.Token, shortSHA(ml.ManifestSHA256), ml.Source,
		errText(err, "pass 1: "+ml.Pass1)) {
		return refuse()
	}
	// The payload gate: only a reviewed payload, with the reviewed kernel
	// Image and the pinned boot-control files (the manifest is not signed).
	gr := in.Gate.Check(ml)
	p.Gate = gr
	for _, g := range gr.Checks {
		c.add(g.Name, g.OK, "%s", g.Why)
	}
	if !gr.OK() {
		return refuse()
	}
	if !c.add("payload-slot", gr.Reviewed.AllowsSlot(p.Target), "the payload is reviewed for slot(s) %q; this plan writes slot %s",
		gr.Reviewed.Slots, strings.ToUpper(p.Target)) {
		return refuse()
	}
	c.add("prerequisites", in.PrereqsConfirmed, "the user confirmed: automatic firmware update off (design §3)")
	c.add("panel", in.PanelConfirmed || in.Mode != "device",
		"the user sees the installer's screen on the unit's panel (%v): the board powers the panel from PMIC switch 2, as every approved firmware assumes",
		in.PanelConfirmed)
	au := readAutoUpdate(in.Input, t)
	p.AutoUpdate = &AutoUpdate{State: au.State, Part: au.Part, Counter: au.Counter, Valid: au.Valid, Why: au.Why,
		UnclearConfirmed: in.AutoUpdateUnclearConfirmed}
	auOK, auWhy := autoUpdateRule(au.State, in.AutoUpdateUnclearConfirmed)
	c.add("autoupdate", auOK, "Auto Update Firmware of stock Android (nvram %s in %s) must be off: %s; %s; %s",
		nvram.KeyFirmware, nvram.PartName, au.State, au.Why, auWhy)
	p.Unlock.Why = "a locked unit never starts mainline from a slot (the boot image is not signed); the user confirmed that the TEE unlock tool read back the unlocked state"
	c.add("unlock", in.UnlockConfirmed, "the user confirmed that the unit is unlocked (%s; the unlock tool's read-back, or ro.boot.vbmeta.device_state=unlocked in Android): %v",
		UnlockFlag, in.UnlockConfirmed)

	// The boot chain: the reviewed firmware manifest and the keeper's parts.
	rb := mm.RequiresBootchain
	cm, cmWhy := mainlineChainManifest(in.Manifests, rb)
	if !c.add("chain-manifest", cm != nil, "the required boot chain %s %s (uboot %s, trust %s, dtbo %s, vbmeta %s, resource %s) must be a reviewed firmware that this release opens%s",
		rb.Version, rb.BuildType, shortSHA(rb.UBootSHA256), shortSHA(rb.TrustSHA256), shortSHA(rb.DtboSHA256),
		shortSHA(rb.VbmetaSHA256), shortSHA(rb.ResourceSHA256), optText(": ", cmWhy)) {
		return refuse()
	}
	ch := &MainlineChain{Firmware: cm.Name(), FirmwareSHA256: cm.Firmware.SHA256, Version: cm.Firmware.Version,
		BuildType: cm.Build["ro.build.type"], UBootBuild: cm.BootChain.UBootBuild, TemplateSHA: mm.BootTemplate.SHA256}
	p.Chain = ch
	if !c.add("chain-userdebug", ch.UBootBuild == mainline.BuildType && ch.BuildType == mainline.BuildType,
		"the boot chain's U-Boot must be the %s build (its build: %s, U-Boot %s): a user U-Boot forces locked and never starts mainline",
		mainline.BuildType, ch.BuildType, ch.UBootBuild) {
		return refuse()
	}
	want := map[string]string{"uboot": rb.UBootSHA256, "trust": rb.TrustSHA256, "dtbo": rb.DtboSHA256, "vbmeta": rb.VbmetaSHA256}
	type image struct {
		size int64
		sha  string
	}
	data := map[string]image{}
	var chainBad []string
	for _, part := range mainlineChainParts() {
		mp, _ := cm.Part(part)
		name := part + "_" + p.Keeper
		e, _ := t.Find(name)
		cp := ChainPart{Part: part, Partition: name, Size: mp.Size, Want: want[part]}
		if mp.Size <= 0 || mp.Size%gpt.Sector != 0 || mp.Size > e.Bytes() {
			chainBad = append(chainBad, fmt.Sprintf("%s: the image of %d bytes does not fit %s (%d bytes)", part, mp.Size, name, e.Bytes()))
		} else if h, err := hashRange(in.Disk, e.Start*gpt.Sector, mp.Size); err != nil {
			chainBad = append(chainBad, fmt.Sprintf("%s: read: %v", name, err))
		} else {
			cp.SHA256 = h
			if h != cp.Want {
				chainBad = append(chainBad, fmt.Sprintf("%s holds sha256 %s, not %s", name, shortSHA(h), shortSHA(cp.Want)))
			}
		}
		ch.Keeper = append(ch.Keeper, cp)
		data[part] = image{mp.Size, cp.SHA256}
	}
	be, _ := t.Find("boot_" + p.Keeper)
	res, rerr := mainline.ReadResource(in.Disk, be.Start*gpt.Sector, be.Bytes())
	rp := ChainPart{Part: "resource", Partition: "boot_" + p.Keeper, Size: int64(len(res)), Want: rb.ResourceSHA256}
	if rerr != nil {
		chainBad = append(chainBad, "boot_b: "+rerr.Error())
	} else if rp.SHA256 = sha256Hex(res); rp.SHA256 != rp.Want {
		chainBad = append(chainBad, fmt.Sprintf("the resource of boot_b has sha256 %s, not %s", shortSHA(rp.SHA256), shortSHA(rp.Want)))
	}
	ch.Keeper = append(ch.Keeper, rp)
	if !c.add("keeper-chain", len(chainBad) == 0,
		"slot B must hold the boot chain of %s: uboot_b, trust_b, dtbo_b, vbmeta_b and the resource of boot_b, read and hashed now%s",
		cm.Name(), listText(": ", chainBad, "")) {
		return refuse()
	}

	// vbmeta_a is the patch of the keeper's signed vbmeta: verify vbmeta_b,
	// make the patch (only the boot image_size), require exactly the
	// manifest's (and the gate's reviewed) patch, take the boot cap from it,
	// and compare the rollback facts with the keeper's as Compute does (R-RB).
	vm, _ := cm.Part("vbmeta")
	ve, _ := t.Find("vbmeta_" + p.Keeper)
	vb := make([]byte, vm.Size)
	if _, err := in.Disk.ReadAt(vb, ve.Start*gpt.Sector); err != nil {
		c.add("vbmeta-verify", false, "read vbmeta_b: %v", err)
		return refuse()
	}
	v, verr := avb.Verify(vb, cm.Pins())
	if verr == nil {
		verr = sameAVB(cm, v)
	}
	ch.VbmetaVerified = verr == nil
	if !c.add("vbmeta-verify", verr == nil, "vbmeta_b (the source of vbmeta_a's patch) is signed by the pinned chain of %s and its facts are the manifest's: %s",
		cm.Name(), errText(verr, "verified")) {
		return refuse()
	}
	bd, ok := v.Find("boot")
	if ok && bd.Type == "hash" {
		ch.SignedBootSize = int64(bd.ImageSize)
	}
	vp := mm.VbmetaPatched
	pv, pf, perr := mainline.PatchVbmeta(vb, uint64(max(vp.BootImageSize, 0)))
	var pbad []string
	switch {
	case perr != nil:
		pbad = append(pbad, perr.Error())
	case pf.SourceSHA256 != vp.SourceSHA256 || pf.SHA256 != vp.SHA256 || int64(len(pv)) != vp.Size || pf.FieldOffset != vp.FieldOffset:
		pbad = append(pbad, fmt.Sprintf("the patch of vbmeta_b (source %s) is %d bytes sha256 %s with the field at %d; the manifest's vbmeta_patched is source %s, %d bytes %s, field %d",
			shortSHA(pf.SourceSHA256), len(pv), shortSHA(pf.SHA256), pf.FieldOffset, shortSHA(vp.SourceSHA256), vp.Size, shortSHA(vp.SHA256), vp.FieldOffset))
	case in.Gate.ChecksVbmeta() && !p.Gate.VbmetaPatch.Matches(pf, len(pv)):
		pbad = append(pbad, "it is not the payload gate's reviewed patch")
	}
	if perr == nil {
		ch.VbmetaPatch = &pf
		ch.BootCap = int64(pf.NewImageSize)
	}
	if !c.add("vbmeta-patch", len(pbad) == 0,
		"vbmeta_a is vbmeta_b with only the boot image_size %d -> %d (byte %d), sha256 %s: the manifest's vbmeta_patched%s%s",
		pf.OldImageSize, pf.NewImageSize, pf.FieldOffset, shortSHA(pf.SHA256),
		map[bool]string{true: " and a reviewed patch of the payload gate", false: ""}[in.Gate.ChecksVbmeta()], listText(": ", pbad, "")) {
		return refuse()
	}
	bootA, _ := t.Find("boot_" + p.Target)
	c.add("boot-cap", ok && bd.Type == "hash" && int64(pf.OldImageSize) == ch.SignedBootSize && ch.BootCap == mm.VbmetaBootCap &&
		ch.BootCap > ch.SignedBootSize && ch.BootCap <= bootA.Bytes(),
		"the boot hash descriptor of the signed vbmeta has image_size %d, the patched one %d; the manifest's vbmeta_boot_cap is %d; boot_a holds %d bytes (libavb reads image_size bytes of it)",
		ch.SignedBootSize, ch.BootCap, mm.VbmetaBootCap, bootA.Bytes())
	pfacts, ferr := avb.ReadFacts(pv)
	ch.RollbackIndex, ch.PIKKeyVersion, ch.PSKKeyVersion = v.RollbackIndex, v.ATX.PIKKeyVersion(), v.ATX.PSKKeyVersion()
	for _, s := range []string{"a", "b"} {
		p.Vbmeta[s] = readCurrentVbmeta(in.Input, t, s)
	}
	kv := p.Vbmeta[p.Keeper]
	switch {
	case kv.Err != "" || kv.ATXErr != "":
		c.add("rollback", false, "cannot read the keeper's rollback index and key versions in vbmeta_b (%s%s)", kv.Err, kv.ATXErr)
	case ferr != nil:
		c.add("rollback", false, "cannot read the rollback index and key versions of the patched vbmeta: %v", ferr)
	default:
		// vbmeta_a is the patch of vbmeta_b (sha256 checked while it is
		// written): the header and the key metadata are vbmeta_b's
		// (PatchVbmeta), so its rollback facts are the keeper's by
		// construction; this reads them from the patched bytes with the
		// ATX reader, re-reads vbmeta_b with the official reader
		// (readCurrentVbmeta), and requires all three to agree, and the
		// rollback location 0.
		c.add("rollback", v.RollbackIndexLocation == 0 && kv.Location == 0 && pfacts.RollbackIndexLocation == 0 &&
			v.RollbackIndex == kv.RollbackIndex && pfacts.RollbackIndex == kv.RollbackIndex &&
			v.ATX.PIKKeyVersion() == kv.PIKKeyVersion && pfacts.PIKKeyVersion == kv.PIKKeyVersion &&
			v.ATX.PSKKeyVersion() == kv.PSKKeyVersion && pfacts.PSKKeyVersion == kv.PSKKeyVersion &&
			v.KeySHA1() == kv.KeySHA1 && pfacts.KeySHA1 == kv.KeySHA1,
			"vbmeta_a is the patch of the AVB-verified vbmeta_b (only the boot image_size; sha256 checked while it is written), so it has the keeper's rollback index %d, PIK key version %d, PSK key version %d and key sha1 %s; the patched bytes read %d, %d, %d, %s; the official vbmeta reader of vbmeta_b reads %d, %d, %d, %s; rollback location 0 (%d, %d, %d)",
			v.RollbackIndex, v.ATX.PIKKeyVersion(), v.ATX.PSKKeyVersion(), v.KeySHA1(), pfacts.RollbackIndex, pfacts.PIKKeyVersion,
			pfacts.PSKKeyVersion, pfacts.KeySHA1, kv.RollbackIndex, kv.PIKKeyVersion,
			kv.PSKKeyVersion, kv.KeySHA1, v.RollbackIndexLocation, pfacts.RollbackIndexLocation, kv.Location)
	}

	// boot_a: the template with the keeper's resource.
	img, aerr := mainline.Assemble(ml.TemplateBytes(), res)
	if aerr == nil {
		ch.AssembledSize, ch.AssembledSHA = int64(len(img)), sha256Hex(img)
	}
	if !c.add("boot-assembled", aerr == nil && ch.AssembledSHA == mm.BootAssembled.SHA256 && ch.AssembledSize == mm.BootAssembled.Size,
		"the template %s with the resource of boot_b (kernel/mkimage.sh --resource) gives %d bytes sha256 %s; the manifest predicts %d bytes %s%s",
		shortSHA(mm.BootTemplate.SHA256), ch.AssembledSize, shortSHA(ch.AssembledSHA), mm.BootAssembled.Size,
		shortSHA(mm.BootAssembled.SHA256), optText(": ", errText(aerr, ""))) {
		return refuse()
	}
	c.add("boot-fits", ch.BootCap > 0 && ch.AssembledSize <= ch.BootCap,
		"boot_a of %d bytes must fit the image_size %d of the vbmeta boot descriptor (libavb loads no more)", ch.AssembledSize, ch.BootCap)
	if len(c.failed()) > 0 {
		return refuse()
	}

	// The writes: uboot, trust, dtbo, vbmeta from the keeper (the reviewed
	// manifest's actions: trust only when different), boot assembled,
	// system the root file system.
	n := 1
	steps := []Step{{N: n, Op: "bootctl", Why: "make slot A unbootable while it is written: a power loss boots the keeper B",
		Bootctl: &BootctlStep{Args: []string{"disable", "--slot", p.Target}, Before: State(now), After: State(s1), NoOp: s1 == now}}}
	var verify []string
	for _, part := range MainlineParts() {
		name := part + "_" + p.Target
		e, _ := t.Find(name)
		src := WriteSource{Partition: name}
		action := "write"
		switch part {
		case "boot":
			src.From, src.Length, src.SHA256 = "assembled-boot", ch.AssembledSize, ch.AssembledSHA
			src.TemplateSHA256, src.ResourceSHA256 = mm.BootTemplate.SHA256, rp.SHA256
		case "system":
			src.From, src.Length, src.SHA256 = "rootfs", mm.Rootfs.Size, mm.Rootfs.SHA256
		case "vbmeta":
			mp, _ := cm.Part(part)
			src.From, src.KeeperPartition = "patched-vbmeta", part+"_"+p.Keeper
			src.Length, src.SHA256 = int64(len(pv)), pf.SHA256
			src.SourceSHA256, src.BootImageSize = pf.SourceSHA256, int64(pf.NewImageSize)
			action = mp.Action
		default:
			mp, _ := cm.Part(part)
			src.From, src.KeeperPartition = "keeper", part+"_"+p.Keeper
			src.Length, src.SHA256 = data[part].size, data[part].sha
			action = mp.Action
		}
		n++
		if err := CheckWriteRange(t, p.Target, name, 0, src.Length); err != nil {
			c.add("write-"+name, false, "%v", err)
			continue
		}
		before, known := in.RecheckBefore[name]
		if !known || action != "write" || len(before) != 64 {
			var err error
			if before, err = hashRange(in.Disk, e.Start*gpt.Sector, src.Length); err != nil {
				c.add("write-"+name, false, "read: %v", err)
				continue
			}
		}
		w := &WriteStep{Partition: name, GPTIndex: e.Index, StartLBA: e.Start, PartSectors: e.Sectors(), Length: src.Length,
			SHA256: src.SHA256, BeforeSHA256: before, Segment: -1, SegID: src.From, Action: "write"}
		st := Step{N: n, Op: "write", Why: fmt.Sprintf("%s from %s (sha256 checked while written)", name, src.From), Write: w}
		if action == "write-if-different" && before == src.SHA256 {
			w.Action = "skip-identical"
			st.Op, st.Why = "skip", "the partition already holds this exact image"
		}
		c.add("write-"+name, true, "p%d LBA %d..%d, %d of %d bytes, inside %s only", e.Index, e.Start,
			e.Start+src.Length/gpt.Sector-1, src.Length, e.Bytes(), name)
		steps = append(steps, st)
		verify = append(verify, name)
		p.Sources = append(p.Sources, src)
	}
	if len(c.failed()) > 0 {
		return refuse()
	}
	steps = append(steps,
		Step{N: n + 1, Op: "sync", Why: "flush every written byte before the verify pass and before slot A is armed"},
		Step{N: n + 2, Op: "verify", Why: "re-read every written partition of slot A and compare the sha256; one mismatch stops the install, slot A stays disabled",
			Verify: verify},
		Step{N: n + 3, Op: "bootctl", Why: "make slot A the next boot (trying); the keeper B stays successful as the fallback",
			Bootctl: &BootctlStep{Args: []string{"arm-trying", "--slot", p.Target, "--lower-keeper"}, Before: State(s1), After: State(s2)}},
	)
	fin := State(s2)
	steps = append(steps, Step{N: n + 4, Op: "check-ab", Why: "read back the A/B sector; the installer's decoder and bootctl status must agree with the prediction",
		Expect: &fin})
	rkssMagicCheck(c, in.Disk, steps, p.Target) // review T4 MAJOR-1
	steps, kok := mainlineKeepKey(&p.Plan, pinnedKey(in.MainlineKey, in.Gate), t, c, steps)
	if !kok {
		return refuse()
	}
	p.Steps = steps
	p.AB.Final = fin
	a0 := cur
	p.Timeline = append(p.Timeline, ABPoint{Step: 0, Op: "start", AB: State(now), Keeper: slotState(a0.Slots[k]),
		Target: slotState(a0.Slots[tg]), Boots: avbab.SlotName(a0.Pick())})
	for _, s := range steps {
		raw := s1
		switch {
		case s.Op == "keep-key":
			raw = now // before the disable: the A/B sector is as found
		case s.Op == "bootctl" && s.Bootctl.Args[0] == "arm-trying" || s.Op == "check-ab":
			raw = s2
		}
		a, _ := avbab.Parse(raw[:])
		p.Timeline = append(p.Timeline, ABPoint{Step: s.N, Op: s.Op, AB: State(raw), Keeper: slotState(a.Slots[k]),
			Target: slotState(a.Slots[tg]), Boots: avbab.SlotName(a.Pick())})
	}

	if in.HashKeeper {
		p.KeeperSHA256 = map[string]string{}
		for _, part := range SlotParts {
			e, _ := t.Find(part + "_" + p.Keeper)
			h, err := hashRange(in.Disk, e.Start*gpt.Sector, e.Bytes())
			if err != nil {
				c.add("keeper-hash", false, "%s: %v", e.Name, err)
				return refuse()
			}
			p.KeeperSHA256[e.Name] = h
		}
	}
	p.Notes = append(p.Notes,
		"Slot A gets mainline Linux "+mm.Version+" (token "+mm.Token+"); slot B keeps Android "+ch.Version+" "+ch.BuildType+" as the fallback.",
		fmt.Sprintf("vbmeta_a is the signed %s vbmeta with only the boot image_size %d -> %d (sha256 %s): its signature and boot digest do not match mainline's, so slot A starts only on an unlocked unit (orange state); a locked unit starts slot B again.",
			ch.Version, ch.SignedBootSize, ch.BootCap, shortSHA(pf.SHA256)),
		"After the first start, mainline marks slot A successful only with a network link (eth0 IPv4 or usb0) and sshd within 300 s; otherwise the tries run out and slot B starts again.")
	if in.Mode == "device" {
		p.Notes = append(p.Notes, "The before_sha256 of every slot A write range is read when the plan is made; the install replaces those bytes, and only the trust skip decides by one.")
	}
	if in.PreFastboot == nil {
		p.Notes = append(p.Notes, "No pre-fastboot A/B sector was given: the keeper is the live sector's B.")
	}
	p.Checks = c.list
	if len(c.failed()) > 0 {
		return refuse()
	}
	p.Seal()
	return p, nil
}

// optText is pre+s, or "" for an empty s.
func optText(pre, s string) string {
	if s == "" {
		return ""
	}
	return pre + s
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Render prints a mainline plan for a human: the plan of Compute's form,
// then the boot chain, the write sources and the A/B timeline.
func (p *MainlinePlan) Render(w io.Writer) {
	pf := func(f string, a ...any) { fmt.Fprintf(w, f, a...) }
	q := p.Plan
	if !p.VerifyID() {
		q.ID = ""
	}
	q.Render(w)
	if m := p.Mainline; m != nil {
		pf("mainline  %s %s, token %s, kernel %s, manifest sha256 %s (%s)\n", m.Manifest.Kind, m.Manifest.Version, m.Manifest.Token,
			shortSHA(m.Manifest.Kernel.Rev), shortSHA(m.ManifestSHA256), m.Source)
	}
	pf("unlock    confirmed %v (%s)\n", p.Unlock.Confirmed, p.Unlock.Flag)
	if g := p.Gate; g != nil {
		rv := "none"
		if g.Reviewed != nil {
			rv = g.Reviewed.Version
		}
		pf("gate      %s: reviewed payload %s, Linux Image sha256 %s, %d boot-control files read\n", g.Gate, rv, shortSHA(g.ImageSHA256), len(g.Files))
	}
	if ch := p.Chain; ch != nil {
		pf("bootchain %s (%s %s, U-Boot %s), vbmeta_b verified %v, boot cap %d (signed %d, vbmeta_a patched), rollback %d, PIK %d, PSK %d\n", ch.Firmware,
			ch.Version, ch.BuildType, ch.UBootBuild, ch.VbmetaVerified, ch.BootCap, ch.SignedBootSize, ch.RollbackIndex, ch.PIKKeyVersion, ch.PSKKeyVersion)
		for _, k := range ch.Keeper {
			pf("  keeper  %-9s %-8s %10d bytes sha256 %s (want %s)\n", k.Partition, k.Part, k.Size, shortSHA(k.SHA256), shortSHA(k.Want))
		}
	}
	for _, s := range p.Sources {
		pf("  source  %-9s <- %s %s %d bytes sha256 %s\n", s.Partition, s.From, s.KeeperPartition, s.Length, shortSHA(s.SHA256))
	}
	for _, t := range p.Timeline {
		pf("  ab      after step %2d %-8s %s\n", t.Step, t.Op, t.AB)
	}
}
