// Package plan computes the install plan: the exact, ordered list of writes
// that install one payload onto one A/B slot, with every safety check of the
// design (fw-installer-design.md §5.3). It only reads. It opens no file: the
// caller gives it the disk as an io.ReaderAt.
//
// The plan has this order, so that a power loss at any point boots the
// keeper:
//
//  1. uc-pxx-bootctl disable --slot T     target unbootable, keeper unchanged
//  2. write each partition image of T     (trust is skipped when identical)
//  3. sync
//  4. verify: re-read each written range and compare its sha256
//  5. uc-pxx-bootctl arm-trying --slot T --lower-keeper
//  6. read back the A/B sector, decode it with the installer's own decoder,
//     and compare it with bootctl status and with the prediction
//
// It refuses when it is not sure: auto update firmware of stock Android on in
// the active nvram part (always), or not clear and not confirmed by the user, a
// keeper that is not successful, a priority
// tie, a pre-fastboot A/B sector that differs from the live one, a GPT that
// does not check out, a write that would touch anything outside the target's
// own partitions, a payload that is not completely whitelisted, a vbmeta key
// or rollback index that it cannot confirm.
package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/nvram"
	"uc-pxx-installer/internal/vbmeta"
)

const (
	Schema = 1
	Kind   = "p10inst-install-plan"
	// ABLBA is the sector of the A/B metadata: misc start + 4.
	ABLBA     = avbab.LBA
	MiscStart = 49152
	// ProtectedEnd: sectors 0..2047 hold the MBR, the primary GPT and the
	// shared idbloader (sector 64). No write may touch them.
	ProtectedEnd = 2048
)

// SlotParts are the partitions of one slot that an install writes, in the
// container order.
var SlotParts = []string{"uboot", "trust", "dtbo", "vbmeta", "boot", "system", "vendor", "oem"}

// NeverWritten is shown in every plan. The checks below enforce it.
var NeverWritten = []string{
	"sectors 0-2047: protective MBR, primary GPT, idbloader (sector 64)",
	"the backup GPT (the last 33 sectors)",
	"misc, except the A/B sector LBA 49156, and that only through uc-pxx-bootctl",
	"the BCB (misc LBA 49184): the installer never sets it",
	"security, cfg, userdata, frp, metadata, cache, backup, others, resource, kernel, dtb",
	"every partition of the keeper slot",
}

// Check is one preflight check.
type Check struct {
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// BootctlStep is an A/B transition through uc-pxx-bootctl.
type BootctlStep struct {
	Args   []string `json:"args"`
	Before ABState  `json:"ab_before"`
	After  ABState  `json:"ab_after"`
	NoOp   bool     `json:"no_change,omitempty"`
}

// WriteStep is one partition image.
type WriteStep struct {
	Partition    string `json:"partition"` // with suffix, for example "boot_b"
	GPTIndex     int    `json:"gpt_index"`
	StartLBA     int64  `json:"part_start_lba"`
	PartSectors  int64  `json:"part_sectors"`
	Offset       int64  `json:"offset"` // in the partition, bytes
	Length       int64  `json:"length"`
	SHA256       string `json:"sha256"`        // of the data written
	BeforeSHA256 string `json:"before_sha256"` // of the same range now
	Segment      int    `json:"segment"`
	SegID        string `json:"seg_id"`
	Action       string `json:"action"` // "write" or "skip-identical"
}

// Step is one step. Exactly one of Bootctl and Write is set for the ops
// "bootctl", "write" and "skip".
type Step struct {
	N       int          `json:"n"`
	Op      string       `json:"op"` // bootctl, write, skip, sync, verify, check-ab
	Why     string       `json:"why"`
	Bootctl *BootctlStep `json:"bootctl,omitempty"`
	Write   *WriteStep   `json:"write,omitempty"`
	Verify  []string     `json:"verify,omitempty"`
	Expect  *ABState     `json:"expect,omitempty"`
}

// Disk describes the disk the plan is for.
type Disk struct {
	Source       string `json:"source"`
	Sectors      int64  `json:"sectors"`
	LayoutSHA256 string `json:"gpt_layout_sha256"`
}

// DeviceID binds a device-mode plan to one eMMC. The CID (it holds the
// serial number of the chip) goes into the plan only as a sha256.
type DeviceID struct {
	Node              string `json:"node"`  // "/dev/mmcblk0"
	Model             string `json:"model"` // the device-tree model
	Host              string `json:"host"`  // the eMMC controller
	CIDSHA256         string `json:"cid_sha256"`
	SysfsLayoutSHA256 string `json:"sysfs_layout_sha256"`
}

// RAMImage names the installer RAM image that a device-mode plan was made in:
// the token of its kernel command line (uc-pxx.id=TOKEN; image/build.sh
// writes TOKEN to token.txt). It is part of the plan id, so a plan made in one
// RAM image is not the plan of another one (--plan refuses it). The token is
// recorded, not checked against a list: the release review pins the image
// sha, and the procedure compares the token.
type RAMImage struct {
	Token string `json:"token"`
	// BootID is the kernel's boot id (/proc/sys/kernel/random/boot_id) of the
	// run that made the plan. It is part of the plan id: a plan binds to one
	// boot of the RAM image. Nothing can boot a slot (and so change oem_K or a
	// rollback index) without a new boot id, so the keeper hashes of a plan
	// with this boot id still describe the keeper (the repair of the hold,
	// DEVMODE-R4.md §2.3).
	BootID string `json:"boot_id"`
}

// Bootctl names the boot-control tool the plan runs.
type Bootctl struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Exec   string `json:"exec,omitempty"` // a runner such as qemu-aarch64-static (dry runs only)
}

// AB is the A/B part of the plan.
type AB struct {
	LBA          int64    `json:"lba"`
	Now          ABState  `json:"now"`
	SectorSHA256 string   `json:"sector_sha256"`
	PreFastboot  *ABState `json:"pre_fastboot,omitempty"`
	Final        ABState  `json:"final"`
}

// AutoUpdate is what the plan read of Auto Update Firmware of stock Android
// (package nvram): the state of the active nvram part and why. It holds no
// value of any other nvram key.
type AutoUpdate struct {
	State   string `json:"state"`       // off, on or unknown (not clear)
	Part    int    `json:"active_part"` // -1: no active part
	Counter uint64 `json:"write_counter"`
	Valid   int    `json:"valid_parts"`
	Why     string `json:"why"`
	// UnclearConfirmed is the user's confirmation that the plan may go on
	// while the state is not clear (--autoupdate-unclear-confirmed). It
	// never lets an "on" pass.
	UnclearConfirmed bool `json:"unclear_confirmed"`
}

// Plan is the whole plan.
type Plan struct {
	Schema       int                   `json:"schema"`
	Kind         string                `json:"kind"`
	ID           string                `json:"id"`
	Tool         string                `json:"tool"`
	Mode         string                `json:"mode"`
	Disk         Disk                  `json:"disk"`
	Device       *DeviceID             `json:"device,omitempty"`
	RAMImage     *RAMImage             `json:"ram_image,omitempty"`
	Bootctl      Bootctl               `json:"bootctl"`
	AB           AB                    `json:"ab"`
	Keeper       string                `json:"keeper"`
	Target       string                `json:"target"`
	KeeperWhy    string                `json:"keeper_why"`
	Payload      *Payload              `json:"payload"`
	Vbmeta       map[string]VbmetaInfo `json:"current_vbmeta"`
	KeeperSHA256 map[string]string     `json:"keeper_sha256,omitempty"`
	// FirmwareIdent is the identity of the firmware source that pass 1 read
	// (device mode): "file size=…,mtime=…,ctime=…,dev=…,ino=…" or "drive …"
	// (the P10-INSTALL drive file). The install of a plan file checks it
	// instead of hashing the firmware again (DEVMODE-R4.md §3).
	FirmwareIdent string      `json:"firmware_ident,omitempty"`
	KeySource     string      `json:"key_source"`
	AutoUpdate    *AutoUpdate `json:"autoupdate,omitempty"`
	Checks        []Check     `json:"checks"`
	Steps         []Step      `json:"steps"`
	NeverWritten  []string    `json:"never_written"`
	Notes         []string    `json:"notes,omitempty"`
}

// Input is what Compute needs.
type Input struct {
	Disk      io.ReaderAt
	DiskBytes int64
	Source    string // for example "image:/dev/shm/x.img" or "device:/dev/mmcblk0"
	Mode      string // "dry-run" or "device"
	Tool      string
	// PreFastboot is the A/B sector as the entry page read it before
	// fastboot entry (p10inst.ab). nil when not known.
	PreFastboot []byte
	// Keeper is "" (choose by the rules), "a" or "b" (the user's choice).
	Keeper           string
	Payload          *Payload
	Bootctl          Bootctl
	BootctlStatus    *BootctlStatus // `uc-pxx-bootctl status` of the current sector
	PrereqsConfirmed bool
	// AutoUpdateUnclearConfirmed: the user confirmed to go on although the
	// auto-update state of stock Android is not clear (the on-screen
	// confirmation, --autoupdate-unclear-confirmed). It does not pass "on".
	// PrereqsConfirmed does not set it.
	AutoUpdateUnclearConfirmed bool
	HashKeeper                 bool
	// KeySlots are the slots whose system gave the key (empty: KeySource
	// names where it came from).
	KeySlots  []string
	KeySource string
	// Device is the checked eMMC (device mode only).
	Device *DeviceID
	// RAMImage is the installer RAM image that the device mode runs in (device
	// mode only: required there).
	RAMImage *RAMImage
	// RequireBootctlSHA256 is the only bootctl sha256 that the plan accepts
	// (device mode: the reviewed binary).
	RequireBootctlSHA256 string
	// ApprovedResources replaces the approved boot-image resource list
	// (tests only: the stock resource is vendor data, not in the repo). nil
	// = ApprovedResource only. The command line has no way to set it.
	ApprovedResources []string
	// FirmwareIdent is the identity of the firmware source (device mode:
	// required). It goes into the plan.
	FirmwareIdent string
	// KnownBefore are the sha256 of the target's write ranges from a plan
	// made earlier in this boot (the plan file of an install). A part whose
	// manifest action is "write" (always written) takes its before-hash from
	// here and is not read again: the hash is only recorded, the write
	// replaces those bytes. A "write-if-different" part is always hashed
	// again (its hash decides the skip). nil: every range is hashed.
	KnownBefore map[string]string
}

// ErrRefused is returned with a plan whose checks show why.
var ErrRefused = errors.New("install refused")

type checker struct{ list []Check }

func (c *checker) add(id string, ok bool, f string, a ...any) bool {
	c.list = append(c.list, Check{ID: id, OK: ok, Detail: fmt.Sprintf(f, a...)})
	return ok
}

func (c *checker) failed() []string {
	var out []string
	for _, x := range c.list {
		if !x.OK {
			out = append(out, x.ID+": "+x.Detail)
		}
	}
	return out
}

func slotIndex(s string) int {
	switch s {
	case "a":
		return 0
	case "b":
		return 1
	}
	return -1
}

func hashRange(r io.ReaderAt, off, n int64) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, io.NewSectionReader(r, off, n)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func readCurrentVbmeta(in Input, t *gpt.Table, slot string) VbmetaInfo {
	e, ok := t.Find("vbmeta_" + slot)
	if !ok {
		return VbmetaInfo{Err: "no partition vbmeta_" + slot}
	}
	n := e.Bytes()
	if n > vbmeta.MaxRead {
		n = vbmeta.MaxRead
	}
	b := make([]byte, n)
	if _, err := in.Disk.ReadAt(b, e.Start*gpt.Sector); err != nil {
		return VbmetaInfo{Err: err.Error()}
	}
	vi, err := vbmeta.Parse(b)
	if err != nil {
		return VbmetaInfo{Err: err.Error()}
	}
	return VbmetaInfo{RollbackIndex: vi.RollbackIndex, KeySHA1: vi.PublicKeySHA1, Flags: vi.Flags}
}

// CheckWriteRange is the rule for every partition write, used by the
// planner and again by the executor before each write: the partition must be
// one of the target's own partitions, and the byte range must lie inside it,
// outside sectors 0..2047, outside the backup GPT, off the A/B sector and off
// every other partition.
func CheckWriteRange(t *gpt.Table, target, name string, off, length int64) error {
	ok := false
	for _, p := range SlotParts {
		ok = ok || name == p+"_"+target
	}
	if !ok {
		return fmt.Errorf("%s is not a partition of slot %s that an install writes", name, target)
	}
	e, found := t.Find(name)
	if !found {
		return fmt.Errorf("no partition %s", name)
	}
	if off < 0 || length <= 0 || off%gpt.Sector != 0 || length%gpt.Sector != 0 {
		return fmt.Errorf("%s: range %d+%d is not whole sectors", name, off, length)
	}
	if off+length > e.Bytes() {
		return fmt.Errorf("%s: range %d+%d is larger than the partition (%d bytes)", name, off, length, e.Bytes())
	}
	first := e.Start + off/gpt.Sector
	last := first + length/gpt.Sector - 1
	if first < ProtectedEnd {
		return fmt.Errorf("%s: range starts at LBA %d, inside sectors 0..2047", name, first)
	}
	if last >= t.BackupStart() || last > t.LastUsable {
		return fmt.Errorf("%s: range ends at LBA %d, in or after the backup GPT", name, last)
	}
	if first <= ABLBA && ABLBA <= last {
		return fmt.Errorf("%s: range holds the A/B sector", name)
	}
	for _, o := range t.Entries {
		if o.Index != e.Index && first <= o.End && o.Start <= last {
			return fmt.Errorf("%s: range overlaps partition %s", name, o.Name)
		}
	}
	return nil
}

func sameBytes(a, b [4]byte, from int) bool {
	for i := from; i < 4; i++ {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// keeperStaysBootable checks one predicted state: legal, the keeper
// successful, and only the keeper's priority may differ from the start.
func keeperStaysBootable(start, s [32]byte, k int, lowered bool) error {
	a, _ := avbab.Parse(s[:])
	if err := a.Validate(); err != nil {
		return err
	}
	if !a.Slots[k].Keeper() {
		return fmt.Errorf("slot %s would not stay successful", avbab.SlotName(k))
	}
	ks0, ks := slotBytes(start, k), slotBytes(s, k)
	if !sameBytes(ks0, ks, 1) {
		return fmt.Errorf("slot %s: a byte other than the priority would change", avbab.SlotName(k))
	}
	want := ks0[0]
	if lowered && want == 15 {
		want = 14
	}
	if ks[0] != want {
		return fmt.Errorf("slot %s: priority %d, want %d", avbab.SlotName(k), ks[0], want)
	}
	for i := 0; i < 32; i++ {
		inSlots := i >= 8 && i < 16
		if !inSlots && i < 28 && s[i] != start[i] {
			return fmt.Errorf("byte %d outside the slots would change", i)
		}
	}
	return nil
}

// Compute makes the plan. On a refusal it returns the plan (with the failed
// checks) and ErrRefused.
func Compute(in Input) (*Plan, error) {
	c := &checker{}
	p := &Plan{Schema: Schema, Kind: Kind, Tool: in.Tool, Mode: in.Mode, Bootctl: in.Bootctl,
		Payload: in.Payload, NeverWritten: NeverWritten, Vbmeta: map[string]VbmetaInfo{}}
	refuse := func() (*Plan, error) {
		p.Checks = c.list
		p.Steps = nil
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
		c.add("mode", true, "device: the writes go to the eMMC partition nodes of the target, the A/B sector only through uc-pxx-bootctl-inst")
		c.add("device", true, "%s: %s, eMMC on %s, sysfs layout %s", in.Device.Node, in.Device.Model, in.Device.Host,
			in.Device.SysfsLayoutSHA256[:min(16, len(in.Device.SysfsLayoutSHA256))])
	default:
		c.add("mode", false, "mode %q with source %q: only a dry run on an image file, or the device mode on /dev/mmcblk0 with the device checked, the RAM image token and boot id and the firmware identity recorded, the keeper hashed and the bootctl sha256 pinned", in.Mode, in.Source)
		return refuse()
	}

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
	c.add("gpt-slots", len(missing) == 0, "every slot partition exists once%s", listText(" (missing: ", missing, ")"))
	if len(c.failed()) > 0 {
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

	// The keeper.
	var pre *avbab.AB
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
				c.add("pre-fastboot", false, "the A/B sector changed after the entry page read it (%s, now %s); the installer has no restore step yet",
					st, p.AB.Now)
			default:
				pre = &a
				c.add("pre-fastboot", true, "the live A/B sector is the one the entry page read before fastboot entry")
			}
		}
	} else {
		c.add("pre-fastboot", true, "not given: the keeper comes from the live sector, or from the user's choice")
	}
	if len(c.failed()) > 0 {
		return refuse()
	}
	k := -1
	switch {
	case in.Keeper != "":
		k = slotIndex(in.Keeper)
		if k < 0 {
			c.add("keeper", false, "--keeper must be a or b")
			return refuse()
		}
		if pre != nil && !pre.Tie() && pre.Pick() != k {
			c.add("keeper", false, "slot %s was booting before fastboot entry, but the keeper chosen is %s", avbab.SlotName(pre.Pick()), in.Keeper)
			return refuse()
		}
		p.KeeperWhy = "chosen by the user"
	case cur.Tie():
		c.add("keeper", false, "both slots have priority %d: the installer never relies on the tie rule; choose the keeper", cur.Slots[0].Priority)
		return refuse()
	case pre == nil && cur.Slots[0].Keeper() && cur.Slots[1].Keeper():
		c.add("keeper", false, "both slots are successful and the pre-fastboot A/B sector is not known; choose the keeper")
		return refuse()
	default:
		k = cur.Pick()
		p.KeeperWhy = "the slot that boots now (libavb choice: the bootable slot with the higher priority)"
	}
	if !c.add("keeper", cur.Slots[k].Keeper(), "slot %s must be successful {prio>=1, tries 0, succ 1}: %v", avbab.SlotName(k), cur.Slots[k]) {
		return refuse()
	}
	tg := 1 - k
	p.Keeper, p.Target = avbab.SlotName(k), avbab.SlotName(tg)
	c.add("target", tg != k && cur.Slots[k].Bootable(), "target %s is not the keeper, and the keeper %s is bootable", p.Target, p.Keeper)

	// The predicted A/B states.
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
	if in.Payload == nil || in.Payload.file == nil {
		c.add("payload", false, "no payload checked by pass 1")
		return refuse()
	}
	pl := in.Payload
	c.add("payload", true, "%s %s (incremental %s), firmware sha256 %s; pass 1: %s", pl.Version, pl.BuildType, pl.Incremental,
		pl.FirmwareSHA256, pl.Pass1)
	c.add("prerequisites", in.PrereqsConfirmed, "the user confirmed: automatic firmware update off (design §3)")
	au := readAutoUpdate(in, t)
	p.AutoUpdate = &AutoUpdate{State: au.State, Part: au.Part, Counter: au.Counter, Valid: au.Valid, Why: au.Why,
		UnclearConfirmed: in.AutoUpdateUnclearConfirmed}
	auOK, auWhy := autoUpdateRule(au.State, in.AutoUpdateUnclearConfirmed)
	c.add("autoupdate", auOK, "Auto Update Firmware of stock Android (nvram %s in %s) must be off: %s; %s; %s",
		nvram.KeyFirmware, nvram.PartName, au.State, au.Why, auWhy)
	// RULES.md: a boot partition gets only an image with the stock resource.
	// The rule names boot_a; the installer applies it to both slots, because
	// U-Boot reads the resource of the slot that it boots.
	approved := in.ApprovedResources
	if approved == nil {
		approved = []string{ApprovedResource}
	}
	okRes := false
	for _, a := range approved {
		okRes = okRes || (a != "" && pl.BootResource == a)
	}
	c.add("boot-resource", okRes, "boot image resource sha256 %s must be an approved stock resource (%s)",
		pl.BootResource, strings.Join(shortList(approved), ", "))

	// vbmeta: key and rollback index.
	for _, s := range []string{"a", "b"} {
		p.Vbmeta[s] = readCurrentVbmeta(in, t, s)
	}
	kv := p.Vbmeta[p.Keeper]
	switch {
	case kv.Err != "":
		c.add("vbmeta-key", false, "cannot read the keeper's vbmeta_%s (%s): cannot confirm the payload's AVB key", p.Keeper, kv.Err)
	default:
		c.add("vbmeta-key", kv.KeySHA1 == pl.Vbmeta.KeySHA1, "payload AVB key sha1 %s, keeper vbmeta_%s key sha1 %s",
			pl.Vbmeta.KeySHA1, p.Keeper, kv.KeySHA1)
	}
	var rb []string
	okRB := true
	for _, s := range []string{"a", "b"} {
		v := p.Vbmeta[s]
		if v.Err != "" {
			okRB = false
			rb = append(rb, fmt.Sprintf("vbmeta_%s unreadable (%s)", s, v.Err))
			continue
		}
		if pl.Vbmeta.RollbackIndex < v.RollbackIndex {
			okRB = false
		}
		rb = append(rb, fmt.Sprintf("vbmeta_%s %d", s, v.RollbackIndex))
	}
	c.add("rollback-index", okRB, "payload rollback index %d must be >= both current ones: %s", pl.Vbmeta.RollbackIndex, strings.Join(rb, ", "))

	// The writes.
	n := 1
	steps := []Step{{N: n, Op: "bootctl", Why: "make the target unbootable while it is written: a power loss boots the keeper",
		Bootctl: &BootctlStep{Args: []string{"disable", "--slot", p.Target}, Before: State(now), After: State(s1), NoOp: s1 == now}}}
	var verify []string
	for _, part := range pl.Parts {
		n++
		name := part.Name + "_" + p.Target
		e, _ := t.Find(name)
		if err := CheckWriteRange(t, p.Target, name, 0, part.Size); err != nil {
			c.add("write-"+name, false, "%v", err)
			continue
		}
		before, known := in.KnownBefore[name]
		if !known || part.Action != "write" || len(before) != 64 {
			var err error
			if before, err = hashRange(in.Disk, e.Start*gpt.Sector, part.Size); err != nil {
				c.add("write-"+name, false, "read: %v", err)
				continue
			}
		}
		w := &WriteStep{Partition: name, GPTIndex: e.Index, StartLBA: e.Start, PartSectors: e.Sectors(), Length: part.Size,
			SHA256: part.SHA256, BeforeSHA256: before, Segment: part.Segment, SegID: part.SegID, Action: "write"}
		st := Step{N: n, Op: "write", Why: fmt.Sprintf("payload %s image (manifest sha256)", part.Name), Write: w}
		if part.Action == "write-if-different" && before == part.SHA256 {
			w.Action = "skip-identical"
			st.Op, st.Why = "skip", "the partition already holds this exact image"
		}
		c.add("write-"+name, true, "p%d LBA %d..%d, %d of %d bytes, inside %s only", e.Index, e.Start,
			e.Start+part.Size/gpt.Sector-1, part.Size, e.Bytes(), name)
		steps = append(steps, st)
		verify = append(verify, name)
	}
	if len(c.failed()) > 0 {
		return refuse()
	}
	steps = append(steps,
		Step{N: n + 1, Op: "sync", Why: "flush every written byte before the verify pass and before the target is armed"},
		Step{N: n + 2, Op: "verify", Why: "re-read every target partition and compare the sha256; one mismatch stops the install, the target stays disabled",
			Verify: verify},
		Step{N: n + 3, Op: "bootctl", Why: "make the target the next boot (trying); the keeper stays successful as the fallback",
			Bootctl: &BootctlStep{Args: []string{"arm-trying", "--slot", p.Target, "--lower-keeper"}, Before: State(s1), After: State(s2)}},
	)
	fin := State(s2)
	steps = append(steps, Step{N: n + 4, Op: "check-ab", Why: "read back the A/B sector; the installer's decoder and bootctl status must agree with the prediction",
		Expect: &fin})
	p.Steps = steps
	p.AB.Final = fin

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
	switch {
	case len(in.KeySlots) > 0:
		p.KeySource = "updater libraries in system_" + strings.Join(in.KeySlots, " and system_")
		onlyTarget := true
		for _, s := range in.KeySlots {
			onlyTarget = onlyTarget && s == p.Target
		}
		if onlyTarget {
			p.Notes = append(p.Notes, "The key comes only from system_"+p.Target+", which this install overwrites. "+
				"If the install stops during the system write, the key may not be found on the unit again: then the user must give a key file.")
		}
	default:
		p.KeySource = in.KeySource
	}
	if pre == nil {
		p.Notes = append(p.Notes, "No pre-fastboot A/B sector was given. Fastboot entry through the boot-mode register wrote nothing to misc when it was measured, but only with slot A active (design §10 item 1).")
	}
	p.Checks = c.list
	if len(c.failed()) > 0 {
		return refuse()
	}
	p.Seal()
	return p, nil
}

// Seal sets the ID: the sha256 of the plan's JSON without the ID. The
// executor runs only a plan whose ID checks out.
func (p *Plan) Seal() {
	p.ID = ""
	b, _ := json.Marshal(p)
	s := sha256.Sum256(b)
	p.ID = hex.EncodeToString(s[:])
}

func shortList(l []string) []string {
	var out []string
	for _, s := range l {
		if len(s) > 16 {
			s = s[:16] + "…"
		}
		out = append(out, s)
	}
	return out
}

// UnclearFlag is the command-line flag of the confirmation for a state that is
// not clear.
const UnclearFlag = "--autoupdate-unclear-confirmed"

// autoUpdateRule is the user's rule (INSTALLER-END-USER-PLAN.md §6.1 U2): the
// active part is the one the Android driver selects (package nvram). Off
// passes. On always refuses, whatever the user confirmed. Not clear (unknown)
// refuses unless the user confirmed to go on.
func autoUpdateRule(state string, unclearConfirmed bool) (bool, string) {
	switch {
	case state == nvram.Off:
		return true, "off in the active part"
	case state == nvram.On:
		return false, "ON in the active part: always refused (turn it off in the web UI, Settings, Auto Update)"
	case unclearConfirmed:
		return true, "not clear; the user confirmed to go on (" + UnclearFlag + ")"
	}
	return false, "not clear: refused unless the user confirms to go on (" + UnclearFlag + ")"
}

// readAutoUpdate reads the nvram ring of stock Android in others (package
// nvram; only the auto-update keys come out). The Android driver reads
// absolute sectors. The device mode has already checked the whole layout of
// the unit, so there others is at nvram.PartStart. A dry-run image is a
// stand-in: its others is read where its GPT puts it.
func readAutoUpdate(in Input, t *gpt.Table) nvram.AutoUpdate {
	au := nvram.AutoUpdate{State: nvram.Unknown, Part: -1}
	e, ok := t.Find(nvram.PartName)
	if !ok {
		au.Why = "no partition " + nvram.PartName
		return au
	}
	if e.Bytes() < nvram.RingOffset+nvram.RingSize {
		au.Why = fmt.Sprintf("%s has %d bytes, too small for the nvram ring", nvram.PartName, e.Bytes())
		return au
	}
	ring := make([]byte, nvram.RingSize)
	if _, err := in.Disk.ReadAt(ring, e.Start*gpt.Sector+nvram.RingOffset); err != nil {
		au.Why = fmt.Sprintf("read %s: %v", nvram.PartName, err)
		return au
	}
	return nvram.Parse(ring)
}

func errText(err error, ok string) string {
	if err != nil {
		return err.Error()
	}
	return ok
}

func listText(pre string, l []string, post string) string {
	if len(l) == 0 {
		return ""
	}
	return pre + strings.Join(l, ", ") + post
}

// VerifyID recomputes the ID of a plan.
func (p *Plan) VerifyID() bool {
	q := *p
	q.ID = ""
	b, _ := json.Marshal(&q)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:]) == p.ID
}

// KnownBeforeOf are the before-hashes of the plan's always-written parts
// (manifest action "write"), for Input.KnownBefore of a re-check of the plan.
func KnownBeforeOf(p *Plan) map[string]string {
	kb := map[string]string{}
	if p.Payload == nil {
		return kb
	}
	for _, s := range p.Steps {
		if s.Op != "write" || s.Write == nil || len(s.Write.BeforeSHA256) != 64 {
			continue
		}
		name := strings.TrimSuffix(s.Write.Partition, "_"+p.Target)
		if p.Payload.Part(name).Action == "write" {
			kb[s.Write.Partition] = s.Write.BeforeSHA256
		}
	}
	return kb
}
