package plan

// The "other slot" plan of mainline step 2 (MAINLINE-STEP2-DESIGN.md §3, §11):
// the running, successful mainline slot K writes mainline into the other slot
// T, with itself as the keeper. Step 2 is K = a, T = b; the same plan later
// re-installs a broken slot from the running one. It is selected only by the
// caller (plan.ComputeOtherSlot); Compute, ComputeMainline and every function
// they use are unchanged: this file only adds.
//
// What differs from ComputeMainline (the RAM installer's mainline branch):
//
//   - it runs from a slot boot, not from the RAM image: the running facts
//     (RunningSlot, read from /proc/device-tree/chosen/u-boot,bootargs and
//     /proc/cmdline) must name the slot K, the unlocked orange state and the
//     token of boot_K, and the root file system must be system_K;
//   - the keeper is K (the running slot): successful and the strict pick;
//   - the boot chain (uboot, trust, dtbo, vbmeta) of T is a byte copy of K's
//     after the sha256 match with the reviewed manifest, each part
//     write-if-different (equal parts are skipped, so an equal U-Boot is never
//     written); the resource of boot_T is the resource of boot_K;
//   - vbmeta_K is the patched vbmeta of payload contract v2 (step 1 wrote
//     it; review F4): its sha256 must be the manifest's vbmeta_patched (and,
//     with the release gate, a reviewed patch). The plan un-patches it (the
//     boot image_size set back to the reviewed firmware's signed value),
//     requires the signed reviewed vbmeta's sha256, AVB-verifies those bytes
//     with the manifest's pins, patches them again and requires exactly the
//     bytes of vbmeta_K; the boot cap is the patched image_size;
//   - the firmware key must be kept on the unit (both key-record sectors of
//     backup decode and hold the same key; KEY-KEEP.md): after step 2 no
//     system holds it;
//   - with Data, two data steps (userdata becomes /data, §6 of the design):
//     data-format and data-migrate, both after the verify and before the arm
//     (a failed write stage keeps the Android data; T never boots before
//     /data is ready). data-format makes ext4 with the temporary label
//     DataFormatLabel, which no slot mounts; data-migrate relabels it to
//     DataLabel as its last action (review F1). A userdata that already holds
//     DataLabel (a finished /data) refuses the Data mode. The executor runs
//     the two steps through its caller's hook;
//   - the on-panel confirmation (or the automatic start) instead of the RAM
//     flow's prerequisites, panel and auto-update gates (stock Android does
//     not run during step 2: T is disabled first).
//
//   - the keeper hashes are of the parts that nothing writes while K runs
//     (uboot, trust, dtbo, vbmeta, boot of K): system_K is the running root
//     and vendor_K/oem_K may hold the phase-1 /data (review F3).
//
// The steps: disable --slot T -> the six writes of T -> sync -> verify ->
// [data-format -> data-migrate] -> arm-trying --slot T --lower-keeper ->
// check-ab. The bootctl argv forms are Compute's.

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
	"uc-pxx-installer/internal/keykeep"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/manifest"
)

const (
	// OtherSlotKind is the kind of an other-slot plan. No other executor
	// runs one: its id is the sha256 of the whole plan, so the embedded
	// Plan's own VerifyID fails, and the mainline executor needs its kind.
	OtherSlotKind = "p10inst-mainline-otherslot-plan"
	// OtherSlotConfirmFlag is the user's confirmation on the panel (the 3 s
	// hold of OK, or the automatic start after its countdown).
	OtherSlotConfirmFlag = "--confirmed"
	// DataLabel is the label of the ext4 file system on userdata (the
	// finished /data, mounted by both slots).
	DataLabel = "tsxdata"
	// DataFormatLabel is the label that data-format gives; no slot mounts
	// it. data-migrate relabels it to DataLabel as its last action.
	DataFormatLabel = "tsxdata-new"
	// DataPartition is the partition that becomes /data of both slots.
	DataPartition = "userdata"
	// The two data step ops.
	OpDataFormat  = "data-format"
	OpDataMigrate = "data-migrate"
)

// RunningSlot are the facts of the running system that an other-slot plan
// needs. The device side reads them from /proc/device-tree/chosen/
// u-boot,bootargs, /proc/cmdline and the mount of /; ParseRunning makes them.
type RunningSlot struct {
	// UBootArgs: the property /chosen/u-boot,bootargs was there (a slot boot;
	// a RAM boot by `fastboot boot` has none).
	UBootArgs bool `json:"uboot_bootargs"`
	// Slot is "a" or "b" (the one word androidboot.slot_suffix=_a|_b), else "".
	Slot              string `json:"slot"`
	SlotWords         int    `json:"slot_words"`
	VerifiedBootState string `json:"verifiedbootstate"`
	DeviceState       string `json:"vbmeta_device_state"`
	// Token is the word uc-pxx.id= of /proc/cmdline ("" when none or more than one).
	Token string `json:"token"`
	// RAMImage: the command line names the installer RAM image (p10inst.ram=1
	// or a p10inst- token).
	RAMImage bool `json:"ram_image"`
	// Root is the GPT name of the partition mounted at / ("" when unknown).
	Root string `json:"root_partition"`
	// BootID is /proc/sys/kernel/random/boot_id of this boot: a slot plan is
	// bound to the boot it was made in (review F2).
	BootID string `json:"boot_id,omitempty"`
	// NetworkUp: eth0 has an IPv4 address (or usb0 is configured) and sshd
	// runs now. The target's mark hook needs it after the restart (P16).
	NetworkUp bool `json:"network_up"`
}

// KeeperStaticParts are the keeper's parts that the plan hashes and the
// executor checks again at the end: the parts that nothing writes while the
// keeper runs.
func KeeperStaticParts() []string { return []string{"uboot", "trust", "dtbo", "vbmeta", "boot"} }

// ParseRunning makes the running facts from the bytes of the property
// /chosen/u-boot,bootargs (nil: the property is missing), the text of
// /proc/cmdline and the GPT name of the partition mounted at /.
func ParseRunning(ubootargs []byte, cmdline, root string) RunningSlot {
	r := RunningSlot{UBootArgs: ubootargs != nil, Root: root}
	words := func(s string) []string {
		return strings.FieldsFunc(s, func(c rune) bool { return c == 0 || c == ' ' || c == '\t' || c == '\n' })
	}
	var slots []string
	for _, w := range words(string(ubootargs)) {
		k, v, ok := strings.Cut(w, "=")
		if !ok {
			continue
		}
		switch k {
		case "androidboot.slot_suffix":
			slots = append(slots, v)
		case "androidboot.verifiedbootstate":
			r.VerifiedBootState = v
		case "androidboot.vbmeta.device_state":
			r.DeviceState = v
		}
	}
	r.SlotWords = len(slots)
	if len(slots) == 1 && (slots[0] == "_a" || slots[0] == "_b") {
		r.Slot = slots[0][1:]
	}
	var tokens []string
	for _, w := range words(cmdline) {
		if v, ok := strings.CutPrefix(w, "uc-pxx.id="); ok {
			tokens = append(tokens, v)
		}
		if w == "p10inst.ram=1" || strings.Contains(w, "p10inst-") {
			r.RAMImage = true
		}
	}
	if len(tokens) == 1 {
		r.Token = tokens[0]
	}
	return r
}

// ConfirmGate is the user's confirmation of the other-slot install.
type ConfirmGate struct {
	Confirmed bool   `json:"confirmed"`
	Auto      bool   `json:"auto"` // the automatic start (AUTO-STEP2 or the setting), after its countdown
	Flag      string `json:"flag"`
}

// KeyGate is what the plan found of the kept firmware key (never a byte of
// it): both record sectors of backup decode and hold the same key.
type KeyGate struct {
	Partition string   `json:"partition"`
	LBAs      [2]int64 `json:"lbas"`
	Copies    int      `json:"valid_copies"`
	Equal     bool     `json:"copies_equal"`
	Why       string   `json:"why"`
}

// DataPlan is the userdata part of a step-2 plan.
type DataPlan struct {
	Partition string `json:"partition"`
	GPTIndex  int    `json:"gpt_index"`
	StartLBA  int64  `json:"start_lba"`
	Sectors   int64  `json:"sectors"`
	FSType    string `json:"fs_type"`
	Label     string `json:"label"`        // the label of the finished /data
	FormatAs  string `json:"format_label"` // the label of data-format (no slot mounts it)
	// Before is what userdata held when the plan was made (DataState); the
	// executor reads it again before the run.
	Before string `json:"before"`
	// Erases: the Android data of the target slot is erased (the confirm
	// screen says so).
	Erases string `json:"erases"`
}

// OtherSlotPlan is an other-slot plan: the fields of Plan (Kind
// OtherSlotKind), the mainline payload, the keeper's boot chain, the source of
// every write, the running facts, the gates, the data part and the predicted
// A/B state at every step.
type OtherSlotPlan struct {
	Plan
	Mainline *mainline.Payload    `json:"mainline"`
	Chain    *MainlineChain       `json:"bootchain"`
	Sources  []WriteSource        `json:"write_sources"`
	Running  RunningSlot          `json:"running"`
	Confirm  ConfirmGate          `json:"confirm"`
	Key      KeyGate              `json:"key_kept"`
	Data     *DataPlan            `json:"data,omitempty"`
	Timeline []ABPoint            `json:"ab_timeline"`
	Gate     *mainline.GateRecord `json:"payload_gate"`
	// RKSS are the hashes of LBA 0-8, 64-392, 1024-1279 and 1088-1416 before
	// step 2 (UBOOT-RKSS-1024 §6.6): the finish compares them (task 4).
	RKSS map[string]string `json:"rkss_record,omitempty"`
}

// Seal sets the ID: the sha256 of the whole plan's JSON without the ID.
func (p *OtherSlotPlan) Seal() {
	p.ID = ""
	b, _ := json.Marshal(p)
	s := sha256.Sum256(b)
	p.ID = hex.EncodeToString(s[:])
}

// VerifyID recomputes the ID of an other-slot plan.
func (p *OtherSlotPlan) VerifyID() bool {
	q := *p
	q.ID = ""
	b, _ := json.Marshal(&q)
	s := sha256.Sum256(b)
	return p.ID != "" && hex.EncodeToString(s[:]) == p.ID
}

// Source is the write source of a target partition (nil if none).
func (p *OtherSlotPlan) Source(partition string) *WriteSource {
	for i := range p.Sources {
		if p.Sources[i].Partition == partition {
			return &p.Sources[i]
		}
	}
	return nil
}

// OtherSlotInput is what ComputeOtherSlot needs: the fields of Input for the
// disk, the mode, the A/B tool and the device (Payload, Keeper, KeySlots,
// KeyKeep, KnownBefore, PreFastboot and RAMImage must be empty), the running
// facts, the checked payload, the reviewed firmware manifests, the gate, the
// confirmation and whether the plan has the data steps.
type OtherSlotInput struct {
	Input
	Running   RunningSlot
	Mainline  *mainline.Payload
	Manifests []*manifest.Manifest
	// Gate is the payload gate (mainline.EmbeddedGate() in a release; tests
	// give mainline.TestGate). A nil gate refuses.
	Gate    *mainline.Gate
	Confirm ConfirmGate
	// Data: the plan formats userdata and migrates /data (step 2). Without
	// it the plan only re-installs the other slot (a repair).
	Data bool
}

// OtherSlotNeverWritten is the never-written list of an other-slot plan of
// keeper k.
func OtherSlotNeverWritten(k string, data bool) []string {
	l := []string{
		"sectors 0-2047: protective MBR, primary GPT, idbloader (sector 64)",
		"the backup GPT (the last 33 sectors)",
		"misc, except the A/B sector LBA 49156, and that only through uc-pxx-bootctl-inst",
		"the BCB (misc LBA 49184)",
		"security, cfg, frp, metadata, cache, others, resource, kernel, dtb",
		"backup: all of it (the key record is only read)",
		"vendor and oem of both slots",
		"every partition of the keeper slot " + strings.ToUpper(k) + " (the running system)",
	}
	if !data {
		l = append(l, "userdata")
	}
	return l
}

// ReadKeyGate reads the two key-record sectors of backup (read only).
func ReadKeyGate(r io.ReaderAt, t *gpt.Table) KeyGate {
	g := KeyGate{Partition: keykeep.Partition}
	e, ok := t.Find(keykeep.Partition)
	if !ok {
		g.Why = "no backup partition"
		return g
	}
	lbas, err := keykeep.LBAs(e.Start, e.Sectors())
	if err != nil {
		g.Why = err.Error()
		return g
	}
	g.LBAs = lbas
	var recs [][]byte
	var why []string
	for i, lba := range lbas {
		sec := make([]byte, gpt.Sector)
		if _, err := r.ReadAt(sec, lba*gpt.Sector); err != nil {
			why = append(why, fmt.Sprintf("copy %d (LBA %d): read: %v", i+1, lba, err))
			continue
		}
		k, err := keykeep.Decode(sec)
		if err != nil {
			why = append(why, fmt.Sprintf("copy %d (LBA %d): %v", i+1, lba, err))
			continue
		}
		g.Copies++
		recs = append(recs, append(append([]byte(nil), k.Head[:]...), k.IV[:]...))
	}
	g.Equal = len(recs) == 2 && bytes.Equal(recs[0], recs[1])
	switch {
	case g.Copies == 2 && g.Equal:
		g.Why = "both copies decode and hold the same key"
	case g.Copies == 2:
		why = append(why, "the two copies hold different keys")
	}
	if len(why) > 0 {
		g.Why = strings.Join(why, "; ")
	}
	return g
}

// bootHeaderName reads the name field of the boot image header at the start
// of a partition ("" when it is no Android boot image).
func bootHeaderName(r io.ReaderAt, off int64) string {
	h := make([]byte, 64)
	if _, err := r.ReadAt(h, off); err != nil || string(h[:8]) != "ANDROID!" {
		return ""
	}
	return strings.TrimRight(string(h[48:64]), "\x00")
}

// DataState says what a partition holds, from its ext4 superblock (at byte
// 1024): "ext4 label <L>" (L may be empty), or "no ext4".
func DataState(r io.ReaderAt, off int64) string {
	sb := make([]byte, 1024)
	if _, err := r.ReadAt(sb, off+1024); err != nil {
		return "unreadable: " + err.Error()
	}
	if sb[0x38] != 0x53 || sb[0x39] != 0xEF {
		return "no ext4"
	}
	return "ext4 label " + strings.TrimRight(string(sb[0x78:0x88]), "\x00")
}

// ComputeOtherSlot makes an other-slot plan. On a refusal it returns the plan
// (with the failed checks, no steps) and ErrRefused.
func ComputeOtherSlot(in OtherSlotInput) (*OtherSlotPlan, error) {
	c := &checker{}
	p := &OtherSlotPlan{Plan: Plan{Schema: Schema, Kind: OtherSlotKind, Tool: in.Tool, Mode: in.Mode, Bootctl: in.Bootctl,
		Vbmeta: map[string]VbmetaInfo{}, KeySource: "none: an other-slot install reads no firmware container"},
		Mainline: in.Mainline, Running: in.Running, Confirm: in.Confirm}
	p.Confirm.Flag = OtherSlotConfirmFlag
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
		c.add("mode", true, "slot: the running mainline writes the other slot's eMMC partition nodes, the A/B sector only through uc-pxx-bootctl-inst")
		um := in.Device.UnitModel
		if !c.add("unit-model", um.ChecksumOK && um.Base == mainline.Model,
			"the unit's factory data says %q (%s; checksum valid %v); an other-slot install is only for a %s", um.FactoryModel, um.Base,
			um.ChecksumOK, mainline.Model) {
			return refuse()
		}
	default:
		c.add("mode", false, "mode %q with source %q: only a dry run on an image file, or the slot mode on /dev/mmcblk0 with the device checked, the payload identity recorded, the keeper hashed and the bootctl sha256 pinned", in.Mode, in.Source)
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
		wrong = append(wrong, "a pre-fastboot A/B sector (a slot run has no fastboot entry)")
	}
	if in.RAMImage != nil {
		wrong = append(wrong, "a RAM image (an other-slot install runs from a slot boot)")
	}
	if !c.add("otherslot-input", len(wrong) == 0, "an other-slot plan takes only a mainline payload and the running facts%s",
		listText(" (given: ", wrong, ")")) {
		return refuse()
	}

	// The running system (P1-P3, P5 of the design).
	ru := in.Running
	if !c.add("running-slot", ru.UBootArgs && !ru.RAMImage && ru.SlotWords == 1 && (ru.Slot == "a" || ru.Slot == "b"),
		"the system must run from a slot boot: /chosen/u-boot,bootargs present (%v), exactly one androidboot.slot_suffix=_a|_b (%d words, slot %q), not the installer RAM image (%v)",
		ru.UBootArgs, ru.SlotWords, ru.Slot, ru.RAMImage) {
		return refuse()
	}
	k, tg := 0, 1
	if ru.Slot == "b" {
		k, tg = 1, 0
	}
	p.Keeper, p.Target = avbab.SlotName(k), avbab.SlotName(tg)
	p.NeverWritten = OtherSlotNeverWritten(p.Keeper, in.Data)
	c.add("unlocked", ru.VerifiedBootState == "orange" && ru.DeviceState == "unlocked",
		"this boot's U-Boot read the unit as unlocked: androidboot.verifiedbootstate=%s (want orange), androidboot.vbmeta.device_state=%s (want unlocked)",
		orNone(ru.VerifiedBootState), orNone(ru.DeviceState))
	c.add("running-root", ru.Root == "system_"+p.Keeper, "the root file system is %s, want system_%s", orNone(ru.Root), p.Keeper)
	c.add("running-token", ru.Token != "", "the kernel command line has exactly one uc-pxx.id= word (%q)", ru.Token)
	c.add("running-boot-id", ru.BootID != "" || in.Mode != "slot", "a slot plan is bound to this boot (boot id %q)", ru.BootID)
	c.add("network", ru.NetworkUp, "eth0 has an IPv4 address (or usb0 is configured) and sshd runs now: the target slot marks itself only with them (%v)",
		ru.NetworkUp)

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
	ud, udOK := t.Find(DataPartition)
	if in.Data && !udOK {
		missing = append(missing, DataPartition)
	}
	c.add("gpt-slots", len(missing) == 0, "every slot partition exists once%s", listText(" (missing: ", missing, ")"))
	if len(c.failed()) > 0 {
		return refuse()
	}

	// The running kernel is the image in boot_K (the mark hook's rule).
	bk, _ := t.Find("boot_" + p.Keeper)
	name := bootHeaderName(in.Disk, bk.Start*gpt.Sector)
	if !c.add("running-image", ru.Token != "" && name == ru.Token,
		"the running kernel's token %q must be the name field of the boot_%s header (%q)", ru.Token, p.Keeper, name) {
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
	p.KeeperWhy = "slot " + strings.ToUpper(p.Keeper) + ", the running mainline: successful and the slot that boots now (an other-slot install always keeps the running slot)"
	c.add("target", true, "target %s is not the keeper, and the keeper %s is bootable", strings.ToUpper(p.Target), strings.ToUpper(p.Keeper))

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
	if !c.add("payload-slot", gr.Reviewed.AllowsSlot(p.Target) && gr.Reviewed.AllowsSlot(p.Keeper),
		"the payload is reviewed for slot(s) %q; step 2 runs it in both slots (it writes slot %s from slot %s)", gr.Reviewed.Slots,
		strings.ToUpper(p.Target), strings.ToUpper(p.Keeper)) {
		return refuse()
	}

	// The confirmation and the kept key.
	c.add("confirm", in.Confirm.Confirmed, "the user confirmed on the panel (the 3 s hold of OK, or the automatic start after its countdown; auto %v): %v",
		in.Confirm.Auto, in.Confirm.Confirmed)
	p.Key = ReadKeyGate(in.Disk, t)
	c.add("key-kept", p.Key.Copies == 2 && p.Key.Equal,
		"the firmware key must be kept on the unit before the last system with it is written (KEY-KEEP.md): %s (LBA %d and %d of %s)",
		p.Key.Why, p.Key.LBAs[0], p.Key.LBAs[1], p.Key.Partition)
	if in.Data {
		p.Data = &DataPlan{Partition: DataPartition, GPTIndex: ud.Index, StartLBA: ud.Start, Sectors: ud.Sectors(), FSType: "ext4",
			Label: DataLabel, FormatAs: DataFormatLabel, Before: DataState(in.Disk, ud.Start*gpt.Sector),
			Erases: "the Android data of slot " + strings.ToUpper(p.Target) + " (userdata)"}
		c.add("data", ud.Name == DataPartition && ud.Sectors() > 0 && p.Data.Before != "ext4 label "+DataLabel &&
			!strings.HasPrefix(p.Data.Before, "unreadable"),
			"userdata (p%d, LBA %d, %d sectors, now %s) becomes /data of both slots: ext4 %s, then %s; its Android data is erased. A userdata that holds ext4 %s already is a finished /data: re-install the other slot without the data steps",
			ud.Index, ud.Start, ud.Sectors(), p.Data.Before, DataFormatLabel, DataLabel, DataLabel)
	}

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
	// vbmeta_K is the patch of the signed vbmeta (contract v2, review F4)
	vp := mm.VbmetaPatched
	want := map[string]string{"uboot": rb.UBootSHA256, "trust": rb.TrustSHA256, "dtbo": rb.DtboSHA256, "vbmeta": vp.SHA256}
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
		if part == "vbmeta" && (vp.Size != mp.Size || vp.SourceSHA256 != rb.VbmetaSHA256) {
			chainBad = append(chainBad, fmt.Sprintf("vbmeta: the manifest's vbmeta_patched (%d bytes, source %s) is not of the reviewed firmware's vbmeta (%d bytes, %s)",
				vp.Size, shortSHA(vp.SourceSHA256), mp.Size, shortSHA(rb.VbmetaSHA256)))
		} else if mp.Size <= 0 || mp.Size%gpt.Sector != 0 || mp.Size > e.Bytes() {
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
	res, rerr := mainline.ReadResource(in.Disk, bk.Start*gpt.Sector, bk.Bytes())
	rp := ChainPart{Part: "resource", Partition: "boot_" + p.Keeper, Size: int64(len(res)), Want: rb.ResourceSHA256}
	if rerr != nil {
		chainBad = append(chainBad, "boot_"+p.Keeper+": "+rerr.Error())
	} else if rp.SHA256 = sha256Hex(res); rp.SHA256 != rp.Want {
		chainBad = append(chainBad, fmt.Sprintf("the resource of boot_%s has sha256 %s, not %s", p.Keeper, shortSHA(rp.SHA256), shortSHA(rp.Want)))
	}
	ch.Keeper = append(ch.Keeper, rp)
	if !c.add("keeper-chain", len(chainBad) == 0,
		"slot %s (the running slot) must hold the boot chain of %s: uboot, trust, dtbo, vbmeta and the resource of boot_%s, read and hashed now%s",
		strings.ToUpper(p.Keeper), cm.Name(), p.Keeper, listText(": ", chainBad, "")) {
		return refuse()
	}

	// vbmeta_T is a byte copy of vbmeta_K, the patched vbmeta of contract v2
	// that step 1 wrote (review F4). Un-patch it (the boot image_size back to
	// the reviewed firmware's signed value), require the signed reviewed
	// vbmeta, verify it, patch it again and require exactly vbmeta_K; take
	// the boot cap from the patch; compare the rollback facts as
	// ComputeMainline does.
	vm, _ := cm.Part("vbmeta")
	ve, _ := t.Find("vbmeta_" + p.Keeper)
	vb := make([]byte, vm.Size)
	if _, err := in.Disk.ReadAt(vb, ve.Start*gpt.Sector); err != nil {
		c.add("vbmeta-verify", false, "read vbmeta_%s: %v", p.Keeper, err)
		return refuse()
	}
	var signedSize uint64
	nboot := 0
	for _, d := range cm.AVB.Descriptors {
		if d.Type == "hash" && d.Partition == "boot" {
			signedSize, nboot = d.ImageSize, nboot+1
		}
	}
	sv, uf, uerr := mainline.PatchVbmeta(vb, signedSize)
	var ubad []string
	switch {
	case nboot != 1:
		ubad = append(ubad, fmt.Sprintf("the reviewed firmware's vbmeta has %d hash descriptors of boot, not one", nboot))
	case uerr != nil:
		ubad = append(ubad, "un-patch: "+uerr.Error())
	case uf.SHA256 != vp.SourceSHA256 || uf.SHA256 != rb.VbmetaSHA256 || uf.OldImageSize != uint64(max(vp.BootImageSize, 0)) ||
		uf.FieldOffset != vp.FieldOffset:
		ubad = append(ubad, fmt.Sprintf("vbmeta_%s with the boot image_size %d -> %d (byte %d) is sha256 %s; want the signed %s with the field at %d holding %d",
			p.Keeper, uf.OldImageSize, signedSize, uf.FieldOffset, shortSHA(uf.SHA256), shortSHA(vp.SourceSHA256), vp.FieldOffset, vp.BootImageSize))
	}
	var v *avb.Vbmeta
	verr := errors.New(strings.Join(ubad, "; "))
	if len(ubad) == 0 {
		if v, verr = avb.Verify(sv, cm.Pins()); verr == nil {
			verr = sameAVB(cm, v)
		}
	}
	ch.VbmetaVerified = verr == nil
	if !c.add("vbmeta-verify", verr == nil, "vbmeta_%s un-patched (the boot image_size set back to the signed %d) is the signed vbmeta %s of %s, signed by its pinned chain, and its facts are the manifest's: %s",
		p.Keeper, signedSize, shortSHA(rb.VbmetaSHA256), cm.Name(), errText(verr, "verified")) {
		return refuse()
	}
	pv, pf, perr := mainline.PatchVbmeta(sv, uint64(max(vp.BootImageSize, 0)))
	var pbad []string
	switch {
	case perr != nil:
		pbad = append(pbad, perr.Error())
	case !bytes.Equal(pv, vb) || pf.SourceSHA256 != vp.SourceSHA256 || pf.SHA256 != vp.SHA256 || int64(len(pv)) != vp.Size ||
		pf.FieldOffset != vp.FieldOffset:
		pbad = append(pbad, fmt.Sprintf("the patch of the verified vbmeta (source %s) is %d bytes sha256 %s with the field at %d; vbmeta_%s is %s; the manifest's vbmeta_patched is source %s, %d bytes %s, field %d",
			shortSHA(pf.SourceSHA256), len(pv), shortSHA(pf.SHA256), pf.FieldOffset, p.Keeper, shortSHA(sha256Hex(vb)),
			shortSHA(vp.SourceSHA256), vp.Size, shortSHA(vp.SHA256), vp.FieldOffset))
	case in.Gate.ChecksVbmeta() && !p.Gate.VbmetaPatch.Matches(pf, len(pv)):
		pbad = append(pbad, "it is not the payload gate's reviewed patch")
	}
	if perr == nil {
		ch.VbmetaPatch = &pf
		ch.SignedBootSize, ch.BootCap = int64(pf.OldImageSize), int64(pf.NewImageSize)
	}
	if !c.add("vbmeta-patch", len(pbad) == 0,
		"vbmeta_%s (the bytes vbmeta_%s gets) is the verified vbmeta with only the boot image_size %d -> %d (byte %d), sha256 %s: the manifest's vbmeta_patched%s%s",
		p.Keeper, p.Target, pf.OldImageSize, pf.NewImageSize, pf.FieldOffset, shortSHA(pf.SHA256),
		map[bool]string{true: " and a reviewed patch of the payload gate", false: ""}[in.Gate.ChecksVbmeta()], listText(": ", pbad, "")) {
		return refuse()
	}
	bd, ok := v.Find("boot")
	bootT, _ := t.Find("boot_" + p.Target)
	c.add("boot-cap", ok && bd.Type == "hash" && int64(bd.ImageSize) == ch.SignedBootSize && ch.BootCap == mm.VbmetaBootCap &&
		ch.BootCap > ch.SignedBootSize && ch.BootCap <= bootT.Bytes(),
		"the boot hash descriptor of the signed vbmeta has image_size %d, the patched one (vbmeta_%s) %d; the manifest's vbmeta_boot_cap is %d; boot_%s holds %d bytes (libavb reads image_size bytes of it)",
		ch.SignedBootSize, p.Keeper, ch.BootCap, mm.VbmetaBootCap, p.Target, bootT.Bytes())
	pfacts, ferr := avb.ReadFacts(vb)
	ch.RollbackIndex, ch.PIKKeyVersion, ch.PSKKeyVersion = v.RollbackIndex, v.ATX.PIKKeyVersion(), v.ATX.PSKKeyVersion()
	for _, s := range []string{"a", "b"} {
		p.Vbmeta[s] = readCurrentVbmeta(in.Input, t, s)
	}
	kv := p.Vbmeta[p.Keeper]
	switch {
	case kv.Err != "" || kv.ATXErr != "":
		c.add("rollback", false, "cannot read the keeper's rollback index and key versions in vbmeta_%s (%s%s)", p.Keeper, kv.Err, kv.ATXErr)
	case ferr != nil:
		c.add("rollback", false, "cannot read the rollback index and key versions of vbmeta_%s: %v", p.Keeper, ferr)
	default:
		c.add("rollback", v.RollbackIndexLocation == 0 && kv.Location == 0 && pfacts.RollbackIndexLocation == 0 &&
			v.RollbackIndex == kv.RollbackIndex && pfacts.RollbackIndex == kv.RollbackIndex &&
			v.ATX.PIKKeyVersion() == kv.PIKKeyVersion && pfacts.PIKKeyVersion == kv.PIKKeyVersion &&
			v.ATX.PSKKeyVersion() == kv.PSKKeyVersion && pfacts.PSKKeyVersion == kv.PSKKeyVersion &&
			v.KeySHA1() == kv.KeySHA1 && pfacts.KeySHA1 == kv.KeySHA1,
			"vbmeta_%s is a byte copy of vbmeta_%s (sha256 checked while it is written), the patch of the AVB-verified signed vbmeta, so it has its rollback index %d, PIK key version %d, PSK key version %d and key sha1 %s; the ATX reader of vbmeta_%s reads %d, %d, %d, %s; the official vbmeta reader reads %d, %d, %d, %s; rollback location 0 (%d, %d, %d)",
			p.Target, p.Keeper, v.RollbackIndex, v.ATX.PIKKeyVersion(), v.ATX.PSKKeyVersion(), v.KeySHA1(), p.Keeper, pfacts.RollbackIndex,
			pfacts.PIKKeyVersion, pfacts.PSKKeyVersion, pfacts.KeySHA1, kv.RollbackIndex, kv.PIKKeyVersion, kv.PSKKeyVersion, kv.KeySHA1,
			v.RollbackIndexLocation, pfacts.RollbackIndexLocation, kv.Location)
	}

	// boot_T: the template with the keeper's resource.
	img, aerr := mainline.Assemble(ml.TemplateBytes(), res)
	if aerr == nil {
		ch.AssembledSize, ch.AssembledSHA = int64(len(img)), sha256Hex(img)
	}
	if !c.add("boot-assembled", aerr == nil && ch.AssembledSHA == mm.BootAssembled.SHA256 && ch.AssembledSize == mm.BootAssembled.Size,
		"the template %s with the resource of boot_%s (kernel/mkimage.sh --resource) gives %d bytes sha256 %s; the manifest predicts %d bytes %s%s",
		shortSHA(mm.BootTemplate.SHA256), p.Keeper, ch.AssembledSize, shortSHA(ch.AssembledSHA), mm.BootAssembled.Size,
		shortSHA(mm.BootAssembled.SHA256), optText(": ", errText(aerr, ""))) {
		return refuse()
	}
	c.add("boot-fits", ch.BootCap > 0 && ch.AssembledSize <= ch.BootCap,
		"boot_%s of %d bytes must fit the image_size %d of the vbmeta boot descriptor (libavb loads no more)", p.Target, ch.AssembledSize, ch.BootCap)
	if len(c.failed()) > 0 {
		return refuse()
	}

	// The steps.
	n := 1
	steps := []Step{{N: n, Op: "bootctl", Why: "make slot " + strings.ToUpper(p.Target) + " unbootable while it is written: a power loss boots the running slot " + strings.ToUpper(p.Keeper),
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
		default:
			src.From, src.KeeperPartition = "keeper", part+"_"+p.Keeper
			src.Length, src.SHA256 = data[part].size, data[part].sha
			action = "write-if-different"
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
		st := Step{N: n, Op: "write", Why: fmt.Sprintf("%s from %s (sha256 checked while written)", name, src.From), Write: w}
		if action == "write-if-different" && before == src.SHA256 {
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
		Step{N: n + 2, Op: "verify", Why: "re-read every written partition and compare the sha256; one mismatch stops, the target stays disabled",
			Verify: verify})
	n += 2
	if in.Data {
		steps = append(steps,
			Step{N: n + 1, Op: OpDataFormat, Why: "userdata: ext4 " + DataFormatLabel + ", which no slot mounts (the Android data is erased here, after the target's writes are verified)"},
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

	rkssMagicCheck(c, in.Disk, steps, p.Target) // review T4 MAJOR-1
	// the RKSS record of the finish (read only; task 4)
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
		"Slot "+T+" gets mainline Linux "+mm.Version+" (token "+mm.Token+"); slot "+K+" (the running system) is not written and stays the fallback.",
		"uboot, trust, dtbo and vbmeta of slot "+T+" are byte copies of slot "+K+"'s ("+ch.Version+" "+ch.BuildType+"; vbmeta the patched one of contract v2, boot image_size "+fmt.Sprint(ch.BootCap)+"); an equal part is skipped.",
		"After the restart slot "+T+" marks itself successful only with a network link (eth0 IPv4 or usb0) and sshd within 300 s; otherwise its tries run out and slot "+K+" starts again.")
	if in.Data {
		p.Notes = append(p.Notes, "userdata is formatted ext4 "+DataFormatLabel+" after the writes of slot "+T+" are verified, filled with the data of slot "+K+", then relabelled "+DataLabel+" and becomes /data of both slots: the Android data of slot "+T+" is erased.",
			"The keeper hashes cover uboot, trust, dtbo, vbmeta and boot of slot "+K+": system_"+p.Keeper+" is the running root, vendor and oem may hold /data.")
	}
	p.Checks = c.list
	if len(c.failed()) > 0 {
		return refuse()
	}
	p.Seal()
	return p, nil
}

// Render prints an other-slot plan for a human: the plan of Compute's form,
// then the running facts, the gates, the boot chain, the write sources and
// the A/B timeline.
func (p *OtherSlotPlan) Render(w io.Writer) {
	pf := func(f string, a ...any) { fmt.Fprintf(w, f, a...) }
	q := p.Plan
	if !p.VerifyID() {
		q.ID = ""
	}
	q.Render(w)
	r := p.Running
	pf("running   slot %q (u-boot,bootargs %v, %d slot words), %s/%s, token %q, root %s, RAM image %v, boot id %s, network %v\n",
		r.Slot, r.UBootArgs, r.SlotWords, orNone(r.VerifiedBootState), orNone(r.DeviceState), r.Token, orNone(r.Root), r.RAMImage,
		orNone(r.BootID), r.NetworkUp)
	if m := p.Mainline; m != nil {
		pf("mainline  %s %s, token %s, kernel %s, manifest sha256 %s (%s)\n", m.Manifest.Kind, m.Manifest.Version, m.Manifest.Token,
			shortSHA(m.Manifest.Kernel.Rev), shortSHA(m.ManifestSHA256), m.Source)
	}
	pf("confirm   %v (auto %v)\n", p.Confirm.Confirmed, p.Confirm.Auto)
	pf("key kept  %d copies, equal %v (%s LBA %d, %d): %s\n", p.Key.Copies, p.Key.Equal, p.Key.Partition, p.Key.LBAs[0], p.Key.LBAs[1], p.Key.Why)
	if d := p.Data; d != nil {
		pf("data      %s p%d LBA %d, %d sectors (now %s) -> %s %s, then %s; erases %s\n", d.Partition, d.GPTIndex, d.StartLBA, d.Sectors,
			d.Before, d.FSType, d.FormatAs, d.Label, d.Erases)
	}
	if g := p.Gate; g != nil {
		rv := "none"
		if g.Reviewed != nil {
			rv = g.Reviewed.Version
		}
		pf("gate      %s: reviewed payload %s, Linux Image sha256 %s, %d boot-control files read\n", g.Gate, rv, shortSHA(g.ImageSHA256), len(g.Files))
	}
	if ch := p.Chain; ch != nil {
		pf("bootchain %s (%s %s, U-Boot %s), signed vbmeta verified %v, boot cap %d (signed %d, vbmeta_%s patched), rollback %d, PIK %d, PSK %d\n", ch.Firmware,
			ch.Version, ch.BuildType, ch.UBootBuild, ch.VbmetaVerified, ch.BootCap, ch.SignedBootSize, p.Keeper, ch.RollbackIndex, ch.PIKKeyVersion, ch.PSKKeyVersion)
		for _, k := range ch.Keeper {
			pf("  keeper  %-9s %-8s %10d bytes sha256 %s (want %s)\n", k.Partition, k.Part, k.Size, shortSHA(k.SHA256), shortSHA(k.Want))
		}
	}
	for _, s := range p.Sources {
		pf("  source  %-9s <- %s %s %d bytes sha256 %s\n", s.Partition, s.From, s.KeeperPartition, s.Length, shortSHA(s.SHA256))
	}
	for _, t := range p.Timeline {
		pf("  ab      after step %2d %-12s %s\n", t.Step, t.Op, t.AB)
	}
}
