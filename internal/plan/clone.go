package plan

// The step-2 plan kind clone (DESIGN-R23 §4): the running, provisioned,
// successful mainline slot K (= b in the end-user path) makes the other slot
// T a copy of itself. It is an OtherSlotPlan with Kind CloneKind and the
// part Clone; no payload. ComputeOtherSlot and every function it uses are
// unchanged: this file only adds.
//
// What differs from ComputeOtherSlot:
//
//   - the sources: uboot_T, trust_T, dtbo_T, vbmeta_T and boot_T are the
//     first bytes of the keeper's partition of the same name, each
//     write-if-different, and the keeper's part must be the clone gate's pin
//     (the reviewed mainline chain: the 1.0.0.94 userdebug uboot, trust and
//     dtbo, the patched vbmeta, the assembled boot); system_T is the whole
//     system_K, copied while K's root file system is frozen ("copy-frozen";
//     its sha256 is known only when the copy has read it), and both
//     partitions must have the same size;
//   - pass 1 is the provisioning done record of K (clone.DoneRecordPath:
//     its profile is in the gate and its ref_b is the gate's); the plan id
//     holds the record's sha256, so the re-check in Start gives the same id.
//     mainline-rc-10b (owner 2026-11-16): K's live root is not compared with
//     ref_b any more (the done record is written only after provisioning's
//     health gate passed; KeeperContent and KeeperEntries stay empty);
//   - after the verify, two steps: "delta" (the gate's clone-delta.json on
//     system_T, then e2fsck -fn clean) and "content-check" (system_T read
//     through package ext4, read only: no file of the forbidden pins anywhere
//     in it; since mainline-rc-10b its content manifest is recorded for the
//     log, not compared with ref_a);
//   - the AUTO-STEP2 attempt key is "clone <ref_a>".
//
// The steps: disable --slot T -> uboot, trust, dtbo, vbmeta, boot of T
// (write or skip) -> copy-frozen system_T -> sync -> verify -> delta ->
// content-check -> [data-format -> data-migrate] -> arm-trying --slot T
// --lower-keeper -> check-ab. Any refusal before the arm leaves T {0,0,0}.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/clone"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/mainline"
)

const (
	// CloneKind is the kind of a clone plan.
	CloneKind = "p10inst-mainline-clone-plan"
	// The ops of a clone plan beyond an other-slot plan's.
	OpCopyFrozen   = "copy-frozen"
	OpDelta        = "delta"
	OpContentCheck = "content-check"
	// CloneSourceFrozen is the write source of system_T.
	CloneSourceFrozen = "keeper-frozen"
)

// ClonePart is the part of a clone plan that an other-slot plan does not
// have.
type ClonePart struct {
	Gate    string `json:"gate"` // the clone gate's name: "embedded" or "test"
	Profile string `json:"profile"`
	BuildID string `json:"build_id"`
	// Done is the provisioning done record that the plan read.
	Done CloneDone `json:"done_record"`
	// KeeperContent, KeeperEntries: empty since mainline-rc-10b (K's live
	// root is not compared with RefB; the fields stay for the record's shape).
	KeeperContent string `json:"keeper_content_sha256"`
	KeeperEntries int    `json:"keeper_content_entries"`
	RefA          string `json:"ref_a_sha256"`
	RefB          string `json:"ref_b_sha256"`
	// Chain: the keeper's parts as read and hashed (the gate's pins).
	Chain []ChainPart `json:"keeper_chain"`
	// Delta is the gate's clone-delta.json, DeltaSHA256 its sha256.
	Delta       *clone.Delta `json:"delta"`
	DeltaSHA256 string       `json:"delta_sha256"`
	// Exclude is the content manifest's exclusion list.
	Exclude       []string `json:"exclude"`
	ExcludeSHA256 string   `json:"exclude_sha256"`
	// Forbidden: no file of A may have one of these (the gate's pins and the
	// files of ForbiddenFrom read from K).
	Forbidden     []clone.Pin `json:"forbidden"`
	FreezeBoundMS int64       `json:"freeze_bound_ms"`
	BlockSize     int64       `json:"block_size"`
	SystemBytes   int64       `json:"system_bytes"`
	MountPoint    string      `json:"mount_point"`
	AttemptKey    string      `json:"attempt_key"`
}

// CloneDone is what the plan read of the done record.
type CloneDone struct {
	Path         string `json:"path"`
	SHA256       string `json:"sha256"`
	Profile      string `json:"profile"`
	Release      string `json:"release"`
	BundleSHA256 string `json:"bundle_sha256"`
	RefB         string `json:"ref_b_sha256"`
}

// CloneInput is what ComputeClone needs: the fields of Input for the disk,
// the mode, the A/B tool and the device (Payload, Keeper, KeySlots,
// KeyKeep, KnownBefore, PreFastboot and RAMImage must be empty), the running
// facts, the clone gate, the done record as read (nil: none), the
// forbidden files read from K, the confirmation and the data steps.
type CloneInput struct {
	Input
	Running RunningSlot
	Gate    *clone.Gate
	// Done are the bytes of clone.DoneRecordPath (nil with DoneErr: none).
	Done      []byte
	DoneErr   error
	Forbidden []clone.Pin
	Confirm   ConfirmGate
	Data      bool
	KeyPins   []KeyPin
	KeyPinSet string
}

// CloneParts are the target's partitions of a clone plan, in order.
func CloneParts() []string { return MainlineParts() }

// ComputeClone makes a clone plan. On a refusal it returns the plan (with
// the failed checks, no steps) and ErrRefused.
func ComputeClone(in CloneInput) (*OtherSlotPlan, error) {
	c := &checker{}
	p := &OtherSlotPlan{Plan: Plan{Schema: Schema, Kind: CloneKind, Tool: in.Tool, Mode: in.Mode, Bootctl: in.Bootctl,
		Vbmeta: map[string]VbmetaInfo{}, KeySource: "none: a clone reads no firmware container"},
		Running: in.Running, Confirm: in.Confirm, Clone: &ClonePart{}}
	p.Confirm.Flag = OtherSlotConfirmFlag
	cp := p.Clone
	refuse := func() (*OtherSlotPlan, error) {
		p.Checks = c.list
		p.Steps, p.Sources, p.Timeline = nil, nil, nil
		return p, fmt.Errorf("%w: %s", ErrRefused, strings.Join(c.failed(), "; "))
	}
	switch {
	case in.Mode == "dry-run" && strings.HasPrefix(in.Source, "image:"):
		c.add("mode", true, "dry run: every write goes to %s", in.Source)
	case in.Mode == "slot" && in.Source == "device:/dev/mmcblk0" && in.Device != nil && in.Device.Node == "/dev/mmcblk0" &&
		in.HashKeeper && in.RequireBootctlSHA256 != "" && in.FirmwareIdent != "":
		p.Device = in.Device
		p.FirmwareIdent = in.FirmwareIdent
		c.add("mode", true, "slot: the running mainline copies itself into the other slot's eMMC partition nodes, the A/B sector only through uc-pxx-bootctl-inst")
		um := in.Device.UnitModel
		if !c.add("unit-model", um.ChecksumOK && um.Base == mainline.Model,
			"the unit's factory data says %q (%s; checksum valid %v); a clone is only for a %s", um.FactoryModel, um.Base, um.ChecksumOK, mainline.Model) {
			return refuse()
		}
	default:
		c.add("mode", false, "mode %q with source %q: only a dry run on an image file, or the slot mode on /dev/mmcblk0 with the device checked, the done record's identity recorded, the keeper hashed and the bootctl sha256 pinned", in.Mode, in.Source)
		return refuse()
	}
	var wrong []string
	if in.Payload != nil {
		wrong = append(wrong, "an official firmware payload")
	}
	if in.Keeper != "" {
		wrong = append(wrong, "a keeper choice (the keeper is always the running slot)")
	}
	if len(in.KeySlots) > 0 || in.KeyKeep != nil {
		wrong = append(wrong, "a firmware key or a keep-key step")
	}
	if in.KnownBefore != nil {
		wrong = append(wrong, "known before-hashes")
	}
	if in.PreFastboot != nil {
		wrong = append(wrong, "a pre-fastboot A/B sector")
	}
	if in.RAMImage != nil {
		wrong = append(wrong, "a RAM image (a clone runs from a slot boot)")
	}
	if !c.add("clone-input", len(wrong) == 0, "a clone plan takes only the running facts and the clone gate%s", listText(" (given: ", wrong, ")")) {
		return refuse()
	}
	g := in.Gate
	if err := g.Check(); !c.add("clone-gate", err == nil, "%s", errText(err, "the clone gate is well formed")) {
		return refuse()
	}
	cp.Gate = g.Name

	// The running system (as ComputeOtherSlot).
	ru := in.Running
	if !c.add("running-slot", ru.UBootArgs && !ru.RAMImage && ru.SlotWords == 1 && (ru.Slot == "a" || ru.Slot == "b"),
		"the system must run from a slot boot: /chosen/u-boot,bootargs present (%v), exactly one androidboot.slot_suffix=_a|_b (%d words, slot %q), not the installer RAM image (%v)",
		ru.UBootArgs, ru.SlotWords, ru.Slot, ru.RAMImage) {
		return refuse()
	}
	// the clone copies B to A only (DESIGN-R23 §4; clone review MINOR-1):
	// a run from slot A is refused
	if !c.add("clone-keeper-b", ru.Slot == "b", "a clone runs from slot B (the provisioned mainline slot) to slot A; this system runs from slot %s", strings.ToUpper(ru.Slot)) {
		return refuse()
	}
	k, tg := 1, 0
	p.Keeper, p.Target = avbab.SlotName(k), avbab.SlotName(tg)
	p.NeverWritten = OtherSlotNeverWritten(p.Keeper, in.Data)
	c.add("unlocked", ru.VerifiedBootState == "orange" && ru.DeviceState == "unlocked",
		"this boot's U-Boot read the unit as unlocked: androidboot.verifiedbootstate=%s (want orange), androidboot.vbmeta.device_state=%s (want unlocked)",
		orNone(ru.VerifiedBootState), orNone(ru.DeviceState))
	c.add("running-root", ru.Root == "system_"+p.Keeper, "the root file system is %s, want system_%s", orNone(ru.Root), p.Keeper)
	c.add("running-token", ru.Token != "", "the kernel command line has exactly one uc-pxx.id= word (%q)", ru.Token)
	c.add("running-boot-id", ru.BootID != "" || in.Mode != "slot", "a slot plan is bound to this boot (boot id %q)", ru.BootID)
	c.add("network", true, "log only (owner QT7-3, no network precondition): eth0 IPv4 or usb0 and sshd now: %v", ru.NetworkUp)

	// The GPT.
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
	ud, udOK := t.Find(DataPartition)
	if in.Data && !udOK {
		missing = append(missing, DataPartition)
	}
	c.add("gpt-slots", len(missing) == 0, "every slot partition exists once%s", listText(" (missing: ", missing, ")"))
	if len(c.failed()) > 0 {
		return refuse()
	}
	sysK, _ := t.Find("system_" + p.Keeper)
	sysT, _ := t.Find("system_" + p.Target)
	cp.SystemBytes = sysK.Bytes()
	if !c.add("clone-size", sysK.Bytes() == sysT.Bytes() && sysK.Bytes() > 0 && sysK.Bytes()%gpt.Sector == 0,
		"system_%s (%d bytes) and system_%s (%d bytes) must have the same size (whole sectors)", p.Keeper, sysK.Bytes(),
		p.Target, sysT.Bytes()) {
		return refuse()
	}

	// The running kernel is the image in boot_K.
	bk, _ := t.Find("boot_" + p.Keeper)
	name := bootHeaderName(in.Disk, bk.Start*gpt.Sector)
	if !c.add("running-image", ru.Token != "" && name == ru.Token,
		"the running kernel's token %q must be the name field of the boot_%s header (%q)", ru.Token, p.Keeper, name) {
		return refuse()
	}

	// The A/B sector.
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
	if !c.add("keeper", cur.Slots[k].Keeper(), "the running slot %s must be successful {prio>=1, tries 0, succ 1}: %v", p.Keeper, cur.Slots[k]) {
		return refuse()
	}
	if !c.add("keeper-boots", !cur.Tie() && cur.Pick() == k,
		"the running slot %s must be the slot that boots now (the strictly higher priority of the bootable slots; never the tie rule): %s",
		p.Keeper, p.AB.Now) {
		return refuse()
	}
	p.KeeperWhy = "slot " + strings.ToUpper(p.Keeper) + ", the running, provisioned mainline: successful and the slot that boots now (a clone always keeps the running slot)"
	c.add("target", true, "target %s is not the keeper, and the keeper %s is bootable", strings.ToUpper(p.Target), strings.ToUpper(p.Keeper))
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

	// Pass 1 of the clone: the done record (mainline-rc-10b: no content check
	// of the keeper's live root).
	var done *clone.DoneRecord
	var derr error
	switch {
	case in.Done == nil && in.DoneErr == nil:
		derr = fmt.Errorf("no record")
	case in.DoneErr != nil:
		derr = in.DoneErr
	default:
		done, derr = clone.ParseDone(in.Done)
	}
	if !c.add("clone-done", derr == nil, "the provisioning done record %s of slot %s: %s", clone.DoneRecordPath, strings.ToUpper(p.Keeper),
		errText(derr, "read and parsed")) {
		return refuse()
	}
	cp.Done = CloneDone{Path: clone.DoneRecordPath, SHA256: clone.SHA256(in.Done), Profile: done.Profile, Release: done.Release,
		BundleSHA256: done.BundleSHA256, RefB: done.RefBSHA256}
	pr, ok := g.Profile(done.Profile)
	if !c.add("clone-profile", ok && pr.RefB == done.RefBSHA256 && pr.BundleSHA256 == done.BundleSHA256,
		"the done record's profile %q (ref_b %s, bundle %s) must be a profile of the clone gate %s with that ref_b and bundle", done.Profile,
		shortSHA(done.RefBSHA256), shortSHA(done.BundleSHA256), g.Name) {
		return refuse()
	}
	cp.Profile, cp.BuildID, cp.RefA, cp.RefB = pr.Name, pr.BuildID, pr.RefA, pr.RefB
	cp.AttemptKey = clone.AttemptKey(pr.RefA)
	cp.Delta, cp.DeltaSHA256 = g.Delta, g.DeltaSHA256()
	cp.Exclude, cp.ExcludeSHA256 = append([]string(nil), g.Exclude...), g.ExcludeSHA256()
	cp.Forbidden = append(append([]clone.Pin(nil), g.Forbidden...), in.Forbidden...)
	cp.FreezeBoundMS = g.FreezeBound.Milliseconds()
	cp.BlockSize = clone.BlockSize
	cp.MountPoint = clone.MountPoint
	var fbad []string
	for _, f := range in.Forbidden {
		if f.Size <= 0 || !clone.IsSHA256(f.SHA256) {
			fbad = append(fbad, fmt.Sprintf("%s (%d bytes, %q)", f.What, f.Size, f.SHA256))
		}
	}
	c.add("clone-forbidden", len(fbad) == 0, "the files that must not be in slot %s: %d of the gate, %d read from slot %s%s", strings.ToUpper(p.Target),
		len(g.Forbidden), len(in.Forbidden), strings.ToUpper(p.Keeper), listText(": not a pin: ", fbad, ""))

	// The confirmation and the kept key.
	c.add("confirm", in.Confirm.Confirmed, "the user confirmed on the panel (the 3 s hold of OK, or the automatic start after its countdown; auto %v): %v",
		in.Confirm.Auto, in.Confirm.Confirmed)
	p.Key = ReadKeyGate(in.Disk, t)
	c.add("key-kept", p.Key.Copies == 2 && p.Key.Equal,
		"the firmware key must be kept on the unit before the last system with it is written (KEY-KEEP.md): %s (LBA %d and %d of %s)",
		p.Key.Why, p.Key.LBAs[0], p.Key.LBAs[1], p.Key.Partition)
	pins, set := in.KeyPins, in.KeyPinSet
	if pins == nil {
		pins, set = ReleaseKeyPins(), KeyPinSetRelease
	}
	if set != KeyPinSetRelease && set != KeyPinSetTest {
		set = "unknown"
	}
	MatchKeyPin(&p.Key, pins, set)
	pinOK, pinWhy := p.Key.KeyPinned()
	c.add("key-pinned", pinOK && set != "unknown", "the kept record must be a firmware key this release knows (owner O3): %s", pinWhy)
	if in.Data {
		p.Data = &DataPlan{Partition: DataPartition, GPTIndex: ud.Index, StartLBA: ud.Start, Sectors: ud.Sectors(), FSType: "ext4",
			Label: DataLabel, FormatAs: DataFormatLabel, Before: DataState(in.Disk, ud.Start*gpt.Sector),
			Erases: "the Android data of slot " + strings.ToUpper(p.Target) + " (userdata)"}
		c.add("data", ud.Name == DataPartition && ud.Sectors() > 0 && p.Data.Before != "ext4 label "+DataLabel &&
			!strings.HasPrefix(p.Data.Before, "unreadable"),
			"userdata (p%d, LBA %d, %d sectors, now %s) becomes /data of both slots: ext4 %s, then %s; its Android data is erased. A userdata that holds ext4 %s already is a finished /data",
			ud.Index, ud.Start, ud.Sectors(), p.Data.Before, DataFormatLabel, DataLabel, DataLabel)
	}

	// The keeper's chain: the gate's pins.
	var chainBad []string
	for _, part := range clone.ChainParts() {
		pin, _ := g.Pin(part)
		e, _ := t.Find(part + "_" + p.Keeper)
		et, _ := t.Find(part + "_" + p.Target)
		cpart := ChainPart{Part: part, Partition: e.Name, Size: pin.Size, Want: pin.SHA256}
		switch {
		case pin.Size > e.Bytes() || pin.Size > et.Bytes():
			chainBad = append(chainBad, fmt.Sprintf("%s: the pin of %d bytes does not fit %s (%d) or %s (%d)", part, pin.Size, e.Name, e.Bytes(), et.Name, et.Bytes()))
		default:
			h, err := hashRange(in.Disk, e.Start*gpt.Sector, pin.Size)
			if err != nil {
				chainBad = append(chainBad, fmt.Sprintf("%s: read: %v", e.Name, err))
				break
			}
			cpart.SHA256 = h
			if h != pin.SHA256 {
				chainBad = append(chainBad, fmt.Sprintf("%s holds sha256 %s, not the pin %s", e.Name, shortSHA(h), shortSHA(pin.SHA256)))
			}
		}
		cp.Chain = append(cp.Chain, cpart)
	}
	if !c.add("keeper-chain", len(chainBad) == 0,
		"slot %s (the running slot) must hold the clone gate's chain: uboot, trust, dtbo, the patched vbmeta and the assembled boot, read and hashed now%s",
		strings.ToUpper(p.Keeper), listText(": ", chainBad, "")) {
		return refuse()
	}
	for _, s := range []string{"a", "b"} {
		p.Vbmeta[s] = readCurrentVbmeta(in.Input, t, s)
	}

	// The steps.
	n := 1
	steps := []Step{{N: n, Op: "bootctl", Why: "make slot " + strings.ToUpper(p.Target) + " unbootable while it is written: a power loss boots the running slot " + strings.ToUpper(p.Keeper),
		Bootctl: &BootctlStep{Args: []string{"disable", "--slot", p.Target}, Before: State(now), After: State(s1), NoOp: s1 == now}}}
	var verify []string
	for i, part := range CloneParts() {
		name := part + "_" + p.Target
		e, _ := t.Find(name)
		src := WriteSource{Partition: name, KeeperPartition: part + "_" + p.Keeper}
		n++
		if part == "system" {
			src.From, src.Length = CloneSourceFrozen, sysK.Bytes()
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
				BeforeSHA256: before, Segment: -1, SegID: src.From, Action: OpCopyFrozen}
			steps = append(steps, Step{N: n, Op: OpCopyFrozen, Write: w,
				Why: fmt.Sprintf("%s from the whole %s, read while the root file system of slot %s is frozen (at most %s), its sha256 taken from the reads",
					name, src.KeeperPartition, strings.ToUpper(p.Keeper), g.FreezeBound)})
			c.add("write-"+name, true, "p%d LBA %d..%d, %d bytes, the whole of %s", e.Index, e.Start, e.Start+e.Sectors()-1, src.Length, name)
			verify = append(verify, name)
			p.Sources = append(p.Sources, src)
			continue
		}
		pin := cp.Chain[i]
		src.From, src.Length, src.SHA256 = "keeper", pin.Size, pin.SHA256
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
		st := Step{N: n, Op: "write", Why: fmt.Sprintf("%s from %s (sha256 checked while written)", name, src.KeeperPartition), Write: w}
		if before == src.SHA256 {
			w.Action = "skip-identical"
			st.Op, st.Why = "skip", "the partition already holds this exact image (a copy of the running slot's)"
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
		Step{N: n + 1, Op: "sync", Why: "flush every written byte before the verify pass and before the target is armed"},
		Step{N: n + 2, Op: "verify", Why: "re-read every written partition and compare the sha256 (system_" + p.Target + ": the sha256 of the copy's reads); one mismatch stops, the target stays disabled",
			Verify: verify},
		Step{N: n + 3, Op: OpDelta, Why: "mount system_" + p.Target + " at " + clone.MountPoint + " (rw, nosuid, nodev, noexec), apply clone-delta.json " + shortSHA(cp.DeltaSHA256) + ", unmount, e2fsck -fn clean"},
		Step{N: n + 4, Op: OpContentCheck, Why: "system_" + p.Target + " read through package ext4 (read only): no forbidden file in it (r2, uc-pxx-step2, uc-pxx-provision); its content manifest is recorded, not compared with ref_a"})
	n += 4
	if in.Data {
		steps = append(steps,
			Step{N: n + 1, Op: OpDataFormat, Why: "userdata: ext4 " + DataFormatLabel + ", which no slot mounts (the Android data is erased here, after the target is checked)"},
			Step{N: n + 2, Op: OpDataMigrate,
				Why: "copy the running slot's /data and the tsx-data directories into it, compare, write the markers, then relabel it " + DataLabel + " (the last action); before the arm, so the target never boots without its data"})
		n += 2
	}
	steps = append(steps,
		Step{N: n + 1, Op: "bootctl", Why: "make slot " + strings.ToUpper(p.Target) + " the next boot (trying); the running slot stays successful as the fallback",
			Bootctl: &BootctlStep{Args: []string{"arm-trying", "--slot", p.Target, "--lower-keeper"}, Before: State(s1), After: State(s2)}})
	fin := State(s2)
	steps = append(steps, Step{N: n + 2, Op: "check-ab", Why: "read back the A/B sector; the installer's decoder and bootctl status must agree with the prediction",
		Expect: &fin})
	p.Steps = steps
	p.AB.Final = fin
	p.Timeline = append(p.Timeline, ABPoint{Step: 0, Op: "start", AB: State(now), Keeper: slotState(cur.Slots[k]),
		Target: slotState(cur.Slots[tg]), Boots: avbab.SlotName(cur.Pick())})
	for _, s := range steps {
		raw := s1
		if s.Op == "bootctl" && s.Bootctl.Args[0] == "arm-trying" || s.Op == "check-ab" {
			raw = s2
		}
		a, _ := avbab.Parse(raw[:])
		p.Timeline = append(p.Timeline, ABPoint{Step: s.N, Op: s.Op, AB: State(raw), Keeper: slotState(a.Slots[k]),
			Target: slotState(a.Slots[tg]), Boots: avbab.SlotName(a.Pick())})
	}
	rkssMagicCheck(c, in.Disk, steps, p.Target)
	rk, rerr := ReadRKSS(in.Disk)
	if !c.add("rkss-record", rerr == nil, "LBA 0-8, 64-392, 1024-1279 and 1088-1416 hashed for the finish's detect-and-stop compare: %s",
		errText(rerr, "recorded")) {
		return refuse()
	}
	p.RKSS = rk
	if in.HashKeeper {
		p.KeeperSHA256 = map[string]string{}
		for _, part := range KeeperStaticParts() {
			e, _ := t.Find(part + "_" + p.Keeper)
			h, err := hashRange(in.Disk, e.Start*gpt.Sector, e.Bytes())
			if err != nil {
				c.add("keeper-hash", false, "%s: %v", e.Name, err)
				return refuse()
			}
			p.KeeperSHA256[e.Name] = h
		}
	}
	T, K := strings.ToUpper(p.Target), strings.ToUpper(p.Keeper)
	p.Notes = append(p.Notes,
		"Slot "+T+" becomes a copy of slot "+K+" (the profile "+pr.Name+", A's build id "+strings.TrimSpace(pr.BuildID)+"); slot "+K+" (the running system) is not written and stays the fallback.",
		"uboot, trust, dtbo, vbmeta and boot of slot "+T+" are byte copies of slot "+K+"'s (the clone gate's pins); an equal part is skipped.",
		"system_"+p.Target+" is a block copy of system_"+p.Keeper+" made while the root file system of slot "+K+" is frozen (the panel and every writer of the root wait; at most "+g.FreezeBound.String()+"), then the pinned difference, e2fsck -fn and the search for the forbidden files.",
		"After the restart slot "+T+" marks itself successful only when the boot is healthy (uptime 60 s, the OpenRC default runlevel finished, no kernel oops; within 600 s); otherwise its tries run out and slot "+K+" starts again.")
	p.Checks = c.list
	if len(c.failed()) > 0 {
		return refuse()
	}
	p.Seal()
	return p, nil
}
