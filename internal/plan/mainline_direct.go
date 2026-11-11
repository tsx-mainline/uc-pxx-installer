package plan

// The direct path of a mainline install (MAINLINE-INT-2026-11-10.md task 3;
// PAGE-DRIVEN-INSTALL-DESIGN.md §3.1, MAINLINE-INSTALL-PLAN.md "Owner goal
// 2026-11-09 (late)"): a factory-stock unit, entered by the BCB route (the
// page or uc-pxx-enter --plain-entry), goes to mainline without a userdebug
// install first. The RAM installer keeps stock A by R(K) (today's reviewed
// form) and writes mainline into B:
//
//   - uboot_b, trust_b, dtbo_b: the images of the reviewed 1.0.0.94
//     userdebug firmware, from the stash of the first drive phase (package
//     mlstash: the user's .bin, read through the official firmware gate and
//     pass 1), never from the keeper (stock A holds a user U-Boot);
//   - vbmeta_b: the patched vbmeta of contract v2, made here from the
//     stash's signed vbmeta (verified with the reviewed manifest's pins);
//   - boot_b: the payload's template with the stash's boot resource
//     (290ef405 of the reviewed firmware);
//   - system_b: the payload's rootfs.ext4; vendor_b and oem_b are not
//     written.
//
// The order is the reviewed one: keep-key -> [R(K)] -> disable b -> the six
// writes -> sync -> verify -> arm-trying --slot b --lower-keeper -> check-ab.
// R(K) is the release-1.2 form (repair --keeper a --expect-sector <live
// sector>), only on the valid both-trying start with A the higher priority
// (the BCB-route reset); any other start must already have A successful and
// booting. The keeper is always A. The key is kept as in task 2: the stock
// system_a is the last copy that step 2 overwrites.
//
// The unlock (MAINLINE-RC2-LS-2026-11-11.md, the in-RAM lock_state reader of
// MAINLINE-RC-2026-11-11.md §10): the plan takes the unlock only from the
// reader's run of this RAM session (DirectUnlockInput.Reader, devmode): 0x01
// with no error and nothing changed passes; another byte, a failed read, a
// change of security, the A/B sector or the BCB around the read, or no read
// refuses. The person's confirmation (--unlock-confirmed) is recorded and
// opens nothing. The unlock marker of the page's adb one-liner
// (/uc-pxx/unlock-marker on the ext4 `cache` partition, read read-only by GPT
// name) is recorded as a claim only; it never opens the gate.
//
// This file only adds; ComputeMainline and Compute are unchanged. Its plan has
// its own kind: no other executor runs it.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	// MainlineDirectKind is the kind of a direct-path plan.
	MainlineDirectKind = "p10inst-mainline-direct-plan"
	// DirectKeeper and DirectTarget are the slots of the direct path.
	DirectKeeper = "a"
	DirectTarget = "b"
)

// StashInput is the stash of the first drive phase as the planner needs it
// (package plan does not read files: the device mode loads the stash).
type StashInput struct {
	IndexSHA256    string
	FirmwareName   string
	FirmwareSHA256 string
	BuildType      string
	UBootBuild     string
	// Parts are the bytes of uboot, trust, dtbo, vbmeta (the signed one) and
	// resource.
	Parts map[string][]byte
}

// StashRecord is what the plan records of the stash (never the record bytes).
type StashRecord struct {
	IndexSHA256    string      `json:"index_sha256"`
	FirmwareName   string      `json:"firmware"`
	FirmwareSHA256 string      `json:"firmware_sha256"`
	Parts          []ChainPart `json:"parts"`
}

// UnlockMarker is the claim of the page's adb unlock one-liner, read from the
// ext4 cache partition (/uc-pxx/unlock-marker; PAGE-MAINLINE-2026-11-10.md):
// key=value lines. Only a claim: Android wrote it, nothing proves it.
type UnlockMarker struct {
	Found        bool   `json:"found"`
	Why          string `json:"why,omitempty"` // not found, unreadable or malformed
	LockState    string `json:"lock_state,omitempty"`
	VerifiedBoot string `json:"verifiedbootstate,omitempty"`
	Model        string `json:"model,omitempty"`
	ClientSHA256 string `json:"client_sha256,omitempty"`
	Tool         string `json:"tool,omitempty"`
	UnlockedAt   string `json:"unlocked_at,omitempty"`
}

// UnlockMarkerPath is the marker's path inside the cache file system.
const UnlockMarkerPath = "/uc-pxx/unlock-marker"

// ParseUnlockMarker parses the marker: at most 4 KiB, LF-terminated
// key=value lines, each known key at most once, no other key.
func ParseUnlockMarker(b []byte) UnlockMarker {
	m := UnlockMarker{Found: true}
	if len(b) == 0 || len(b) > 4096 {
		m.Why = fmt.Sprintf("%d bytes (1 to 4096)", len(b))
		return m
	}
	seen := map[string]bool{}
	for _, l := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		k, v, ok := strings.Cut(l, "=")
		if !ok || seen[k] || len(v) > 256 || strings.ContainsAny(v, "\r\x00") {
			m.Why = fmt.Sprintf("a malformed line %q", l)
			return m
		}
		seen[k] = true
		switch k {
		case "lock_state":
			m.LockState = v
		case "verifiedbootstate":
			m.VerifiedBoot = v
		case "model":
			m.Model = v
		case "client_sha256":
			m.ClientSHA256 = v
		case "tool":
			m.Tool = v
		case "unlocked_at":
			m.UnlockedAt = v
		default:
			m.Why = fmt.Sprintf("an unknown key %q", k)
			return m
		}
	}
	return m
}

// DirectUnlockInput is the unlock evidence of a direct plan.
type DirectUnlockInput struct {
	// Confirmed: the user confirmed that the unit is unlocked
	// (--unlock-confirmed, after the unlock screen).
	Confirmed bool
	// Reader is the in-RAM reader's run of this RAM session (nil: no read).
	Reader *LockStateRead
	// Marker is what the cache partition holds (nil: not read).
	Marker *UnlockMarker
}

// DirectUnlock is what the plan records of the unlock.
type DirectUnlock struct {
	Confirmed bool           `json:"confirmed"`
	Flag      string         `json:"flag"`
	LockState string         `json:"lock_state"` // "0x01", or "not read …" / "none: …"
	Marker    *UnlockMarker  `json:"cache_marker,omitempty"`
	Basis     string         `json:"basis"`
	Reader    *LockStateRead `json:"reader,omitempty"` // the in-RAM reader's run (nil: none)
}

// DirectPlan is a direct-path plan: the fields of a mainline plan (Kind
// MainlineDirectKind, keeper a, target b), the stash and the unlock evidence.
type DirectPlan struct {
	MainlinePlan
	Stash        *StashRecord `json:"stash"`
	UnlockDirect DirectUnlock `json:"unlock_direct"`
	// TargetNow is what slot B holds now (owner F1, 2026-11-11): the plan
	// says it, and the install replaces it only after the person confirmed
	// it (ReplaceFlag; not a plan input: the confirm comes after the plan).
	TargetNow *DirectTargetNow `json:"target_now"`
}

// ReplaceFlag is the person's confirmation that the direct path replaces
// what slot B holds now (the plan's TargetNow): an input of the install,
// not of the plan, so it is asked after the plan was shown.
const ReplaceFlag = "--replace-target-confirmed"

// DirectTargetNow is slot B at the plan: the one known firmware whose uboot,
// trust, dtbo, vbmeta and boot images it holds byte for byte, or unknown.
type DirectTargetNow struct {
	Slot     string `json:"slot"`
	Known    bool   `json:"known"`
	Firmware string `json:"firmware,omitempty"` // the manifest's name, e.g. "1.0.0.94 (userdebug)"
	Why      string `json:"why,omitempty"`      // why it is not known
	// UBootSame: uboot_b holds the stash's U-Boot already (so its first
	// start in slot B is not the first start of that U-Boot there)
	UBootSame bool   `json:"uboot_same"`
	Text      string `json:"text"` // the plain words of the plan and the screen
}

// TargetNowCheck is the id of the plan check that names what slot B holds
// (the flow's screen shows its detail).
const TargetNowCheck = "target-now"

// Seal sets the ID: the sha256 of the whole plan's JSON without the ID.
func (p *DirectPlan) Seal() {
	p.ID = ""
	b, _ := json.Marshal(p)
	s := sha256.Sum256(b)
	p.ID = hex.EncodeToString(s[:])
}

// VerifyID recomputes the ID of a direct plan.
func (p *DirectPlan) VerifyID() bool {
	q := *p
	q.ID = ""
	b, _ := json.Marshal(&q)
	s := sha256.Sum256(b)
	return p.ID != "" && hex.EncodeToString(s[:]) == p.ID
}

// MainlineDirectInput is what ComputeMainlineDirect needs: the fields of
// Input for the disk, the mode, the A/B tool, the gates and the device
// (Payload, KeySlots, KeyKeep, KnownBefore and PreFastboot must be empty;
// Keeper "" or "a"), the payload, the reviewed manifests, the gate, the
// stash, the key input and the unlock evidence.
type MainlineDirectInput struct {
	Input
	Mainline    *mainline.Payload
	Manifests   []*manifest.Manifest
	Gate        *mainline.Gate
	Stash       *StashInput
	MainlineKey *MainlineKeyInput
	Unlock      DirectUnlockInput
}

// MainlineDirectNeverWritten is the never-written list of a direct plan.
func MainlineDirectNeverWritten() []string {
	return []string{
		"sectors 0-2047: protective MBR, primary GPT, idbloader (sector 64)",
		"the backup GPT (the last 33 sectors)",
		"misc, except the A/B sector LBA 49156, and that only through uc-pxx-bootctl-inst",
		"the BCB (misc LBA 49184): the installer never sets it",
		"security, cfg, userdata, frp, metadata, cache, others, resource, kernel, dtb",
		MainlineKeepKeyNeverWritten,
		"vendor_b and oem_b (a mainline install leaves them as they are)",
		"every partition of the keeper slot A (stock)",
	}
}

// directSources are the write sources of the six target parts.
const (
	SrcStash             = "stash"
	SrcStashPatchedVbmet = "stash-patched-vbmeta"
	SrcAssembledStash    = "assembled-boot-stash"
)

// ComputeMainlineDirect makes a direct-path plan. On a refusal it returns the
// plan (with the failed checks, no steps) and ErrRefused.
func ComputeMainlineDirect(in MainlineDirectInput) (*DirectPlan, error) {
	c := &checker{}
	p := &DirectPlan{MainlinePlan: MainlinePlan{Plan: Plan{Schema: Schema, Kind: MainlineDirectKind, Tool: in.Tool, Mode: in.Mode,
		Bootctl: in.Bootctl, NeverWritten: MainlineDirectNeverWritten(), Vbmeta: map[string]VbmetaInfo{},
		KeySource: "none"}, Mainline: in.Mainline, Unlock: UnlockGate{Confirmed: in.Unlock.Confirmed, Flag: UnlockFlag}}}
	refuse := func() (*DirectPlan, error) {
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
		p.Device, p.RAMImage, p.FirmwareIdent = in.Device, in.RAMImage, in.FirmwareIdent
		c.add("mode", true, "device: the writes go to the eMMC partition nodes of slot B, the A/B sector only through uc-pxx-bootctl-inst")
		um := in.Device.UnitModel
		if !c.add("unit-model", um.ChecksumOK && um.Base != "" && um.DTModel == in.Device.Model && DTBase(um.DTModel) == um.Base,
			"the unit's factory data says %q (%s; checksum valid %v); this RAM image is for the %s", um.FactoryModel, um.Base,
			um.ChecksumOK, DTBase(in.Device.Model)) {
			return refuse()
		}
		if !c.add("mainline-model", um.Base == mainline.Model, "a mainline install is only for a %s; this unit is a %s", mainline.Model, um.Base) {
			return refuse()
		}
	default:
		c.add("mode", false, "mode %q with source %q: only a dry run on an image file, or the device mode on /dev/mmcblk0 with the device checked, the RAM image token and boot id and the payload identity recorded, the keeper hashed and the bootctl sha256 pinned", in.Mode, in.Source)
		return refuse()
	}
	var wrong []string
	if in.Payload != nil {
		wrong = append(wrong, "an official firmware payload")
	}
	if in.Keeper != "" && in.Keeper != DirectKeeper {
		wrong = append(wrong, "keeper "+in.Keeper+" (the direct path always keeps slot A)")
	}
	if len(in.KeySlots) > 0 || in.KeyKeep != nil {
		wrong = append(wrong, "a firmware key or an official keep-key input")
	}
	if in.KnownBefore != nil {
		wrong = append(wrong, "known before-hashes")
	}
	if in.PreFastboot != nil {
		wrong = append(wrong, "a pre-fastboot A/B sector (the BCB route resets the A/B block; R(K) runs only without one)")
	}
	if !c.add("direct-input", len(wrong) == 0, "a direct plan takes only the payload, the stash and the unit%s", listText(" (given: ", wrong, ")")) {
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

	// The A/B sector and the keeper: R(K) on the BCB-route reset, else A
	// successful and booting.
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
	if len(c.failed()) > 0 {
		return refuse()
	}
	const k, tg = 0, 1
	p.Keeper, p.Target = DirectKeeper, DirectTarget
	eff, effCur := now, cur
	var rkArgs []string
	if reestablishEligible(cur) {
		rkSector, rerr := PredictRepair(sector, k)
		if !c.add("reestablish", cur.Pick() == k && rerr == nil && cur.Slots[k].Bootable() && !cur.Slots[k].Keeper(),
			"the live A/B is the both-trying reset of the BCB route (%s); the direct path re-establishes slot A, the higher-priority slot, as successful with R(K): %s",
			p.AB.Now, errText(rerr, "R(K) is keeper-only bootable")) {
			return refuse()
		}
		copy(eff[:], rkSector)
		effCur, _ = avbab.Parse(eff[:])
		rkArgs = []string{"repair", "--keeper", DirectKeeper, "--expect-sector", hex.EncodeToString(sector)}
		p.KeeperWhy = "slot A, stock: re-established as successful by R(K) before the install (the higher-priority slot of the BCB-route reset; the pre-entry rule of the page and the CLI made sure that A was successful before the entry)"
	} else {
		// review T3 MINOR-1: besides the reset, only the resume state after
		// R(K) (or a cut after it) is a direct start; anything else is not a
		// factory-stock unit after the BCB route
		if !c.add("direct-start", cur.Slots[0] == (avbab.Slot{Priority: 15, Successful: 1}) && cur.Slots[1] == (avbab.Slot{}),
			"the start must be the BCB-route reset (both slots trying, A 15) or the state after R(K), A{15,0,1} B{0,0,0}: %s", p.AB.Now) {
			return refuse()
		}
		p.KeeperWhy = "slot A, stock: successful and the slot that boots now, B disabled (an install that R(K) began)"
	}
	if !c.add("keeper", effCur.Slots[k].Keeper(), "slot A must be successful {prio>=1, tries 0, succ 1} (after R(K) if the start is the BCB-route reset): %v", effCur.Slots[k]) {
		return refuse()
	}
	if !c.add("keeper-boots", !effCur.Tie() && effCur.Pick() == k,
		"slot A must be the slot that boots (the strictly higher priority of the bootable slots; never the tie rule): %s", State(eff)) {
		return refuse()
	}
	c.add("target", true, "target B is not the keeper, and the keeper A is bootable")
	// review T3 MINOR-2: the keeper A must be an intact slot of a known
	// firmware (uboot, trust, dtbo, vbmeta and boot of one reviewed
	// manifest, byte for byte); only the RAM installer can check it
	km, kwhy := keeperFirmware(in.Disk, t, p.Keeper, in.Manifests)
	if !c.add("keeper-firmware", km != nil, "slot A must hold, byte for byte, the uboot, trust, dtbo, vbmeta and boot images of one known firmware (the stock keeper): %s",
		kwhy) {
		return refuse()
	}
	p.Notes = append(p.Notes, "Slot A holds "+km.Name()+" (uboot, trust, dtbo, vbmeta and boot as its manifest).")
	// owner F1: what slot B holds now (it is replaced); by the same rule as
	// the keeper (the five images, byte for byte; never system or oem)
	tn := &DirectTargetNow{Slot: p.Target}
	if tm, twhy := keeperFirmware(in.Disk, t, p.Target, in.Manifests); tm != nil {
		tn.Known, tn.Firmware = true, tm.Name()
		tn.Text = "slot B holds the Crestron firmware " + tm.Name() + " (uboot, trust, dtbo, vbmeta and boot as its manifest); it is replaced by mainline Linux; slot A stays the fallback"
	} else {
		tn.Why = twhy
		tn.Text = "slot B holds firmware that this installer does not know (" + twhy + "); it is replaced by mainline Linux; slot A stays the fallback"
	}
	p.TargetNow = tn
	c.add(TargetNowCheck, true, "%s (the install needs the person's confirmation of it, %s)", tn.Text, ReplaceFlag)
	s1, err1 := PredictDisable(eff, tg)
	s2, err2 := PredictArm(s1, tg, true)
	if err1 != nil || err2 != nil {
		c.add("ab-predict", false, "bootctl would refuse: %v %v", err1, err2)
		return refuse()
	}
	var why []string
	if err := keeperStaysBootable(eff, s1, k, false); err != nil {
		why = append(why, "after disable: "+err.Error())
	}
	if err := keeperStaysBootable(eff, s2, k, true); err != nil {
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

	// The payload and its gate.
	ml := in.Mainline
	if !c.add("payload", ml != nil && ml.Verified() && ml.Kind == mainline.Kind, "the mainline payload must be checked by pass 1 (mainline.Verify)") {
		return refuse()
	}
	mm := ml.Manifest
	if err := mm.Check(); !c.add("mainline-manifest", err == nil, "%s %s, model %s, kernel %s, token %s (manifest sha256 %s; %s): %s",
		mm.Kind, mm.Version, mm.Model, shortSHA(mm.Kernel.Rev), mm.Token, shortSHA(ml.ManifestSHA256), ml.Source,
		errText(err, "pass 1: "+ml.Pass1)) {
		return refuse()
	}
	if in.Gate == nil {
		c.add("payload-gate", false, "no payload gate")
		return refuse()
	}
	gr := in.Gate.Check(ml)
	p.Gate = gr
	for _, g := range gr.Checks {
		c.add(g.Name, g.OK, "%s", g.Why)
	}
	if !gr.OK() {
		return refuse()
	}
	if !c.add("payload-slot", gr.Reviewed.AllowsSlot(p.Target), "the payload is reviewed for slot(s) %q; the direct path writes slot B (a slot-A-only build takes system_a as root and its updaters write slot A, the keeper)",
		gr.Reviewed.Slots) {
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

	// The unlock: only the in-RAM reader's run of this RAM session; the
	// confirmation is recorded and opens nothing; the cache marker is a
	// claim only.
	u := in.Unlock
	p.UnlockDirect = DirectUnlock{Confirmed: u.Confirmed, Flag: UnlockFlag, Marker: u.Marker, Reader: u.Reader}
	const how = "unlock the unit with the TEE tool; the installer reads lock_state itself, a confirmation does not open the gate"
	var uok bool
	switch r := u.Reader; {
	case r == nil:
		p.UnlockDirect.LockState = "not read"
		p.UnlockDirect.Basis = "no lock_state read in this RAM session (the direct path takes the unlock only from the in-RAM reader; " + how + ")"
	case r.Changed:
		p.UnlockDirect.LockState = orNone(r.LockState)
		p.UnlockDirect.Basis = "STOP: security, the A/B sector or the BCB changed around the in-RAM read, or the eMMC read-only flag was off after it (keep the unit powered, do not restart, write nothing; ask for help)"
	case r.Error != "":
		p.UnlockDirect.LockState = "none: the read failed"
		p.UnlockDirect.Basis = "the in-RAM reader failed: " + r.Error + " (" + how + ")"
	case r.LockState == "0x01":
		p.UnlockDirect.LockState, p.UnlockDirect.Basis = "0x01", "lock_state read in this RAM session by the in-RAM reader: unlocked"
		uok = r.Unlocked()
	default:
		p.UnlockDirect.LockState = orNone(r.LockState)
		p.UnlockDirect.Basis = "lock_state read in this RAM session by the in-RAM reader: " + orNone(r.LockState) + ", not unlocked (" + how + ")"
	}
	if r := u.Reader; r != nil && r.Stub != "" {
		p.UnlockDirect.Basis += "; TEST STUB: " + r.Stub
	}
	p.Unlock.Confirmed = uok
	p.Unlock.Why = "a locked unit never starts mainline from a slot (the boot image is not signed): slot B then fails its verification and the keeper A starts"
	mk := "none"
	if u.Marker != nil && u.Marker.Found {
		mk = fmt.Sprintf("lock_state=%s verifiedbootstate=%s (a claim of Android, not a proof)", orNone(u.Marker.LockState), orNone(u.Marker.VerifiedBoot))
	}
	c.add("unlock", uok, "the unit must be unlocked: %s; lock_state %s; cache marker %s", p.UnlockDirect.Basis, p.UnlockDirect.LockState, mk)

	// The boot chain: the reviewed firmware manifest and the stash.
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
	st := in.Stash
	if !c.add("stash", st != nil && st.FirmwareSHA256 == cm.Firmware.SHA256 && st.BuildType == mainline.BuildType &&
		st.UBootBuild == mainline.BuildType && len(st.IndexSHA256) == 64,
		"the stash of the first drive phase must be of the reviewed firmware %s (sha256 %s): %s", cm.Name(), shortSHA(cm.Firmware.SHA256),
		stashText(st)) {
		return refuse()
	}
	want := map[string]string{"uboot": rb.UBootSHA256, "trust": rb.TrustSHA256, "dtbo": rb.DtboSHA256, "vbmeta": rb.VbmetaSHA256,
		"resource": rb.ResourceSHA256}
	p.Stash = &StashRecord{IndexSHA256: st.IndexSHA256, FirmwareName: st.FirmwareName, FirmwareSHA256: st.FirmwareSHA256}
	var sbad []string
	for _, part := range []string{"uboot", "trust", "dtbo", "vbmeta", "resource"} {
		b := st.Parts[part]
		cp := ChainPart{Part: part, Partition: "stash", Size: int64(len(b)), SHA256: sha256Hex(b), Want: want[part]}
		if part != "resource" {
			mp, _ := cm.Part(part)
			if mp.Size != cp.Size {
				sbad = append(sbad, fmt.Sprintf("%s: %d bytes, the manifest's image %d", part, cp.Size, mp.Size))
			}
		}
		if len(b) == 0 || cp.SHA256 != cp.Want {
			sbad = append(sbad, fmt.Sprintf("%s: sha256 %s, want %s", part, shortSHA(cp.SHA256), shortSHA(cp.Want)))
		}
		p.Stash.Parts = append(p.Stash.Parts, cp)
		ch.Keeper = append(ch.Keeper, cp)
	}
	if !c.add("stash-chain", len(sbad) == 0, "the stash holds the boot chain of %s: uboot, trust, dtbo, vbmeta and the boot resource, each of the payload's requires_bootchain%s",
		cm.Name(), listText(": ", sbad, "")) {
		return refuse()
	}
	if ub := st.Parts["uboot"]; len(ub) > 0 {
		if e, ok := t.Find("uboot_" + p.Target); ok && int64(len(ub)) <= e.Bytes() {
			h, _ := hashRange(in.Disk, e.Start*gpt.Sector, int64(len(ub)))
			p.TargetNow.UBootSame = h == sha256Hex(ub)
		}
	}

	// vbmeta_b: the patch of the stash's signed vbmeta.
	vb := st.Parts["vbmeta"]
	v, verr := avb.Verify(vb, cm.Pins())
	if verr == nil {
		verr = sameAVB(cm, v)
	}
	ch.VbmetaVerified = verr == nil
	if !c.add("vbmeta-verify", verr == nil, "the stash's vbmeta (the source of vbmeta_b's patch) is signed by the pinned chain of %s and its facts are the manifest's: %s",
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
		pbad = append(pbad, fmt.Sprintf("the patch of the stash's vbmeta (source %s) is %d bytes sha256 %s with the field at %d; the manifest's vbmeta_patched is source %s, %d bytes %s, field %d",
			shortSHA(pf.SourceSHA256), len(pv), shortSHA(pf.SHA256), pf.FieldOffset, shortSHA(vp.SourceSHA256), vp.Size, shortSHA(vp.SHA256), vp.FieldOffset))
	case in.Gate.ChecksVbmeta() && !p.Gate.VbmetaPatch.Matches(pf, len(pv)):
		pbad = append(pbad, "it is not the payload gate's reviewed patch")
	}
	if perr == nil {
		ch.VbmetaPatch = &pf
		ch.BootCap = int64(pf.NewImageSize)
	}
	if !c.add("vbmeta-patch", len(pbad) == 0,
		"vbmeta_b is the stash's signed vbmeta with only the boot image_size %d -> %d (byte %d), sha256 %s: the manifest's vbmeta_patched%s%s",
		pf.OldImageSize, pf.NewImageSize, pf.FieldOffset, shortSHA(pf.SHA256),
		map[bool]string{true: " and a reviewed patch of the payload gate", false: ""}[in.Gate.ChecksVbmeta()], listText(": ", pbad, "")) {
		return refuse()
	}
	bootB, _ := t.Find("boot_" + p.Target)
	c.add("boot-cap", ok && bd.Type == "hash" && int64(pf.OldImageSize) == ch.SignedBootSize && ch.BootCap == mm.VbmetaBootCap &&
		ch.BootCap > ch.SignedBootSize && ch.BootCap <= bootB.Bytes(),
		"the boot hash descriptor of the signed vbmeta has image_size %d, the patched one %d; the manifest's vbmeta_boot_cap is %d; boot_b holds %d bytes",
		ch.SignedBootSize, ch.BootCap, mm.VbmetaBootCap, bootB.Bytes())
	// Rollback (the firmware gate's rule): the patched vbmeta's rollback
	// index and ATX key versions must be the keeper's (stock A), location 0.
	pfacts, ferr := avb.ReadFacts(pv)
	ch.RollbackIndex, ch.PIKKeyVersion, ch.PSKKeyVersion = v.RollbackIndex, v.ATX.PIKKeyVersion(), v.ATX.PSKKeyVersion()
	for _, s := range []string{"a", "b"} {
		p.Vbmeta[s] = readCurrentVbmeta(in.Input, t, s)
	}
	kv := p.Vbmeta[p.Keeper]
	switch {
	case kv.Err != "" || kv.ATXErr != "":
		c.add("rollback", false, "cannot read the keeper's rollback index and key versions in vbmeta_a (%s%s)", kv.Err, kv.ATXErr)
	case ferr != nil:
		c.add("rollback", false, "cannot read the rollback index and key versions of the patched vbmeta: %v", ferr)
	default:
		c.add("rollback", v.RollbackIndexLocation == 0 && kv.Location == 0 && pfacts.RollbackIndexLocation == 0 &&
			pfacts.RollbackIndex == kv.RollbackIndex && v.RollbackIndex == pfacts.RollbackIndex &&
			pfacts.PIKKeyVersion == kv.PIKKeyVersion && pfacts.PSKKeyVersion == kv.PSKKeyVersion,
			"vbmeta_b (the patch) has rollback index %d, PIK key version %d, PSK key version %d; the keeper A's vbmeta_a has %d, %d, %d; rollback location 0 (%d, %d)",
			pfacts.RollbackIndex, pfacts.PIKKeyVersion, pfacts.PSKKeyVersion, kv.RollbackIndex, kv.PIKKeyVersion, kv.PSKKeyVersion,
			pfacts.RollbackIndexLocation, kv.Location)
	}

	// boot_b: the template with the stash's resource.
	res := st.Parts["resource"]
	img, aerr := mainline.Assemble(ml.TemplateBytes(), res)
	if aerr == nil {
		ch.AssembledSize, ch.AssembledSHA = int64(len(img)), sha256Hex(img)
	}
	if !c.add("boot-assembled", aerr == nil && ch.AssembledSHA == mm.BootAssembled.SHA256 && ch.AssembledSize == mm.BootAssembled.Size,
		"the template %s with the stash's resource (kernel/mkimage.sh --resource) gives %d bytes sha256 %s; the manifest predicts %d bytes %s%s",
		shortSHA(mm.BootTemplate.SHA256), ch.AssembledSize, shortSHA(ch.AssembledSHA), mm.BootAssembled.Size,
		shortSHA(mm.BootAssembled.SHA256), optText(": ", errText(aerr, ""))) {
		return refuse()
	}
	c.add("boot-fits", ch.BootCap > 0 && ch.AssembledSize <= ch.BootCap,
		"boot_b of %d bytes must fit the image_size %d of the vbmeta boot descriptor (libavb loads no more)", ch.AssembledSize, ch.BootCap)
	if len(c.failed()) > 0 {
		return refuse()
	}

	// The writes of slot B.
	n := 1
	steps := []Step{{N: n, Op: "bootctl", Why: "make slot B unbootable while it is written: a power loss boots the keeper A",
		Bootctl: &BootctlStep{Args: []string{"disable", "--slot", p.Target}, Before: State(eff), After: State(s1), NoOp: s1 == eff}}}
	var verify []string
	for _, part := range MainlineParts() {
		name := part + "_" + p.Target
		e, _ := t.Find(name)
		src := WriteSource{Partition: name}
		action := "write"
		switch part {
		case "boot":
			src.From, src.Length, src.SHA256 = SrcAssembledStash, ch.AssembledSize, ch.AssembledSHA
			src.TemplateSHA256, src.ResourceSHA256 = mm.BootTemplate.SHA256, sha256Hex(res)
		case "system":
			src.From, src.Length, src.SHA256 = "rootfs", mm.Rootfs.Size, mm.Rootfs.SHA256
		case "vbmeta":
			mp, _ := cm.Part(part)
			src.From, src.Length, src.SHA256 = SrcStashPatchedVbmet, int64(len(pv)), pf.SHA256
			src.SourceSHA256, src.BootImageSize = pf.SourceSHA256, int64(pf.NewImageSize)
			action = mp.Action
		default:
			mp, _ := cm.Part(part)
			src.From, src.Length, src.SHA256 = SrcStash, int64(len(st.Parts[part])), want[part]
			action = mp.Action
		}
		n++
		if err := CheckWriteRange(t, p.Target, name, 0, src.Length); err != nil {
			c.add("write-"+name, false, "%v", err)
			continue
		}
		before, err := hashRange(in.Disk, e.Start*gpt.Sector, src.Length)
		if err != nil {
			c.add("write-"+name, false, "read: %v", err)
			continue
		}
		w := &WriteStep{Partition: name, GPTIndex: e.Index, StartLBA: e.Start, PartSectors: e.Sectors(), Length: src.Length,
			SHA256: src.SHA256, BeforeSHA256: before, Segment: -1, SegID: src.From, Action: "write"}
		stp := Step{N: n, Op: "write", Why: fmt.Sprintf("%s from %s (sha256 checked while written)", name, src.From), Write: w}
		if action == "write-if-different" && before == src.SHA256 {
			w.Action = "skip-identical"
			stp.Op, stp.Why = "skip", "the partition already holds this exact image"
		}
		c.add("write-"+name, true, "p%d LBA %d..%d, %d of %d bytes, inside %s only", e.Index, e.Start,
			e.Start+src.Length/gpt.Sector-1, src.Length, e.Bytes(), name)
		steps = append(steps, stp)
		verify = append(verify, name)
		p.Sources = append(p.Sources, src)
	}
	if len(c.failed()) > 0 {
		return refuse()
	}
	fin := State(s2)
	steps = append(steps,
		Step{N: n + 1, Op: "sync", Why: "flush every written byte before the verify pass and before slot B is armed"},
		Step{N: n + 2, Op: "verify", Why: "re-read every written partition of slot B and compare the sha256; one mismatch stops the install, slot B stays disabled",
			Verify: verify},
		Step{N: n + 3, Op: "bootctl", Why: "make slot B the next boot (trying); the keeper A stays successful as the fallback",
			Bootctl: &BootctlStep{Args: []string{"arm-trying", "--slot", p.Target, "--lower-keeper"}, Before: State(s1), After: State(s2)}},
		Step{N: n + 4, Op: "check-ab", Why: "read back the A/B sector; the installer's decoder and bootctl status must agree with the prediction",
			Expect: &fin})
	if rkArgs != nil {
		rk := Step{N: 1, Op: "bootctl",
			Why:     "re-establish the keeper A as successful before the install (the BCB-route both-trying start): R(K) makes A successful and discards B; A stays bootable, so a power loss here boots A",
			Bootctl: &BootctlStep{Args: rkArgs, Before: State(now), After: State(eff)}}
		out := []Step{rk}
		for _, s := range steps {
			s.N++
			out = append(out, s)
		}
		steps = out
	}
	rkssMagicCheck(c, in.Disk, steps, p.Target) // review T4 MAJOR-1
	steps, kok := mainlineKeepKey(&p.Plan, pinnedKey(in.MainlineKey, in.Gate), t, c, steps)
	if !kok {
		return refuse()
	}
	p.Steps = steps
	p.AB.Final = fin
	p.Timeline = append(p.Timeline, ABPoint{Step: 0, Op: "start", AB: State(now), Keeper: slotState(cur.Slots[k]),
		Target: slotState(cur.Slots[tg]), Boots: avbab.SlotName(cur.Pick())})
	for _, s := range steps {
		raw := s1
		switch {
		case s.Op == "keep-key":
			raw = now
		case s.Op == "bootctl" && s.Bootctl.Args[0] == "repair":
			raw = eff
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
		"Slot B gets mainline Linux "+mm.Version+" (token "+mm.Token+") with the boot chain of "+ch.Version+" "+ch.BuildType+" from the stash of the first drive phase; slot A (stock) is not written and stays the fallback.",
		fmt.Sprintf("vbmeta_b is the signed %s vbmeta with only the boot image_size %d -> %d (sha256 %s): slot B starts only on an unlocked unit (orange state); a locked unit starts slot A again.",
			ch.Version, ch.SignedBootSize, ch.BootCap, shortSHA(pf.SHA256)),
		bootNote(tn),
		"After the first start, mainline marks slot B successful only with a network link (eth0 IPv4 or usb0) and sshd within 300 s; otherwise the tries run out and slot A starts again.")
	if rkArgs != nil {
		p.Notes = append(p.Notes, "The start is the BCB-route reset (both slots trying): R(K) first makes slot A successful and B {0,0,0}; the install then makes two A/B writes, R(K) and the arm, each a rule-9 point in a supervised session.")
	}
	p.Checks = c.list
	if len(c.failed()) > 0 {
		return refuse()
	}
	p.Seal()
	return p, nil
}

// bootNote is the plan's note on the first start of slot B, from what
// uboot_b holds now (review DIRECT-DRYRUN-UNIT3 F3).
func bootNote(tn *DirectTargetNow) string {
	const reread = "the UBOOT-RKSS-1024 §6.6 re-read of LBA 0-8, 64-392, 1024-1279 and 1088-1416 belongs after it (supervised sessions)."
	if tn != nil && tn.UBootSame {
		return "uboot_b holds the stash's userdebug U-Boot already (it is not written again): the first start of slot B runs the same U-Boot as before, with a new boot image; " + reread
	}
	return "The first start of slot B runs the stash's userdebug U-Boot in slot B for the first time: " + reread
}

// keeperFirmware finds the one manifest whose uboot, trust, dtbo, vbmeta and
// boot images the slot holds at the start of its partitions (any status:
// the keeper is not written).
func keeperFirmware(r io.ReaderAt, t *gpt.Table, slot string, ms []*manifest.Manifest) (*manifest.Manifest, string) {
	cache := map[string]string{}
	hash := func(part string, size int64) string {
		k := fmt.Sprintf("%s/%d", part, size)
		if h, ok := cache[k]; ok {
			return h
		}
		e, ok := t.Find(part + "_" + slot)
		h := ""
		if ok && size > 0 && size <= e.Bytes() {
			h, _ = hashRange(r, e.Start*gpt.Sector, size)
		}
		cache[k] = h
		return h
	}
	var found []*manifest.Manifest
	for _, m := range ms {
		ok := true
		for _, part := range []string{"uboot", "trust", "dtbo", "vbmeta", "boot"} {
			mp, has := m.Part(part)
			if !has || hash(part, mp.Size) != mp.SHA256 {
				ok = false
				break
			}
		}
		if ok {
			found = append(found, m)
		}
	}
	switch len(found) {
	case 1:
		return found[0], found[0].Name()
	case 0:
		return nil, "no known firmware matches"
	}
	return nil, fmt.Sprintf("%d firmwares match: not unique", len(found))
}

func stashText(s *StashInput) string {
	if s == nil {
		return "no stash of this boot (put the 1.0.0.94 userdebug .bin on the UC-PXX-INST drive first)"
	}
	return fmt.Sprintf("the stash is of %s (sha256 %s, %s build, %s U-Boot)", s.FirmwareName, shortSHA(s.FirmwareSHA256), s.BuildType, s.UBootBuild)
}

// Source is the write source of a target partition (nil if none).
func (p *DirectPlan) Source(partition string) *WriteSource { return p.MainlinePlan.Source(partition) }

// Render prints a direct plan for a human.
func (p *DirectPlan) Render(w io.Writer) {
	q := p.MainlinePlan
	if !p.VerifyID() {
		q.ID = ""
	}
	q.Render(w)
	if s := p.Stash; s != nil {
		fmt.Fprintf(w, "stash     %s (sha256 %s), index sha256 %s\n", s.FirmwareName, shortSHA(s.FirmwareSHA256), shortSHA(s.IndexSHA256))
	}
	u := p.UnlockDirect
	fmt.Fprintf(w, "unlock    %s; lock_state %s; confirmed %v (recorded; it opens nothing)\n", u.Basis, u.LockState, u.Confirmed)
	if r := u.Reader; r != nil {
		fmt.Fprintf(w, "reader    boot %s, token %s; module %s, supplicant %s, client %s; rpc log %s\n", r.BootID, r.Token,
			orNone(r.Module), orNone(r.Supplicant), orNone(r.Client), orNone(r.RPCLog))
		fmt.Fprintf(w, "reader    before: security %s, A/B first 32 %s, BCB %s, read-only %v\n", orNone(r.Before.Security),
			orNone(r.Before.AB32), orNone(r.Before.BCB), r.Before.ReadOnly)
		fmt.Fprintf(w, "reader    after:  security %s, A/B first 32 %s, BCB %s, read-only %v; changed %v\n", orNone(r.After.Security),
			orNone(r.After.AB32), orNone(r.After.BCB), r.After.ReadOnly, r.Changed)
	}
	if m := u.Marker; m != nil {
		fmt.Fprintf(w, "marker    cache %s: found %v lock_state %s verifiedbootstate %s %s\n", UnlockMarkerPath, m.Found, orNone(m.LockState),
			orNone(m.VerifiedBoot), m.Why)
	}
}

// DirectPlanOf reads a direct plan file whose content matches its id.
func DirectPlanOf(b []byte) (*DirectPlan, error) {
	var p DirectPlan
	d := json.NewDecoder(bytes.NewReader(b))
	if err := d.Decode(&p); err != nil {
		return nil, err
	}
	if p.Kind != MainlineDirectKind {
		return nil, fmt.Errorf("a plan of kind %q, not a direct mainline plan", p.Kind)
	}
	if !p.VerifyID() {
		return nil, errors.New("its content does not match its id (an edited plan)")
	}
	return &p, nil
}
