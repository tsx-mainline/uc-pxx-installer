// Package status collects what the status screen shows: the A/B state of
// both slots, what each slot holds, the unlock state that can be proven, the
// auto-update setting of stock Android, and the identity of the unit. It only reads. Every block device is opened
// O_RDONLY through package blk, and no file system is mounted.
package status

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/blk"
	"uc-pxx-installer/internal/bootimg"
	"uc-pxx-installer/internal/ext4"
	"uc-pxx-installer/internal/factory"
	"uc-pxx-installer/internal/keykeep"
	"uc-pxx-installer/internal/manifest"
	"uc-pxx-installer/internal/nvram"
	"uc-pxx-installer/internal/vendorstore"
)

// MiscStart is the first sector of misc. The A/B sector is misc + 4.
const MiscStart = 49152

// Slot is what one slot holds.
type Slot struct {
	Name    string // "a" or "b"
	AB      avbab.Slot
	UBoot   string // "U-Boot of 1.0.0.94 (userdebug)", or "unknown"
	UBootUD bool   // the U-Boot is a userdebug build of a manifest
	Boot    string // what boot_X holds
	BootOEM bool   // boot_X is an image of a manifest (OEM signed)
	Token   string // uc-pxx build token in the boot_X header
	System  string // what system_X holds
	Summary string // one line: the firmware in the slot
	Errs    []string
}

// Report is the whole status.
type Report struct {
	Model, Serial, MAC, Kernel, ImageToken string
	ABValid                                bool
	ABErr                                  string
	AB                                     avbab.AB
	Next, Fallback                         int // slot index, -1 = none
	PreFastboot                            *avbab.AB
	PreFastbootErr                         string
	Slots                                  [2]Slot
	Unlock, UnlockWhy                      string
	// AutoUpdate is the auto-update setting of stock Android (nvram in
	// others). Only its three keys are read.
	AutoUpdate nvram.AutoUpdate
	ReadOnly   string // the kernel read-only flag of the eMMC
	// KeyKept says whether a firmware key record is in backup (package
	// keykeep): "yes", "no" or "unknown (why)". Never any byte of it.
	KeyKept string
	// Unit is the model in the factory data of the unit (package factory).
	Unit Unit
	Errs []string
}

// Unit is what the factory data block in cfg says. Only the model record
// and the revision are read out of it.
type Unit struct {
	Value  string // record 650 ("UC-P10-T-HS"), "" if not read
	Base   string // "UC-P10", "UC-P8", or "" (not a UC-P10 or UC-P8)
	Suffix string // shown only
	HWRev  string
	Err    string // why the model is not known
	// ImageBase is the model of this RAM image (its device tree): "UC-P10",
	// "UC-P8" or "".
	ImageBase string
}

// Match reports that the factory data names the model of this RAM image:
// the device mode's model gate passes (package device).
func (u Unit) Match() bool { return u.Err == "" && u.Base != "" && u.Base == u.ImageBase }

// Source is where the status comes from. Root is "/" on the device.
type Source struct {
	Root      string
	Manifests []*manifest.Manifest
	// Cmdline overrides /proc/cmdline (tests).
	Cmdline string
}

func (s Source) read(p string) string {
	b, err := os.ReadFile(filepath.Join(s.Root, p))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.TrimRight(string(b), "\x00"))
}

func (s Source) cmdline() string {
	if s.Cmdline != "" {
		return s.Cmdline
	}
	return s.read("proc/cmdline")
}

// CmdlineValue returns the value of word key=value on the kernel command line.
func CmdlineValue(cmdline, key string) (string, bool) {
	for _, w := range strings.Fields(cmdline) {
		if v, ok := strings.CutPrefix(w, key+"="); ok {
			return v, true
		}
	}
	return "", false
}

// Collect gathers the report. It never stops at the first error: each part
// that fails shows its error.
func Collect(src Source) *Report {
	r := &Report{Next: -1, Fallback: -1, AutoUpdate: nvram.AutoUpdate{State: nvram.Unknown, Part: -1, Why: "not read"}}
	for i := range r.Slots {
		r.Slots[i].Name = avbab.SlotName(i)
	}
	r.Model = src.read("proc/device-tree/model")
	r.Kernel = src.read("proc/sys/kernel/osrelease")
	cl := src.cmdline()
	r.ImageToken, _ = CmdlineValue(cl, "uc-pxx.id")
	if hx, ok := CmdlineValue(cl, "p10inst.ab"); ok {
		b, err := hex.DecodeString(hx)
		if err == nil && len(b) >= 32 {
			a, _ := avbab.Parse(b)
			r.PreFastboot = &a
		} else {
			r.PreFastbootErr = "p10inst.ab on the command line is not 32 bytes of hex"
		}
	}
	d, err := blk.Find(src.Root)
	if err != nil {
		r.Errs = append(r.Errs, err.Error())
		r.ABErr = err.Error()
		return r
	}
	r.readAB(d)
	r.readIdentity(d)
	r.readAutoUpdate(d)
	r.readKeyKept(d)
	for i := range r.Slots {
		r.Slots[i] = readSlot(d, src.Manifests, i, r.AB.Slots[i])
	}
	r.unlock()
	ro := 0
	for _, p := range append([]blk.Part{{Dev: d.Dev}}, d.Parts...) {
		if d.ReadOnly(p.Dev) {
			ro++
		}
	}
	switch ro {
	case len(d.Parts) + 1:
		r.ReadOnly = "yes (the kernel refuses writes to the eMMC)"
	case 0:
		r.ReadOnly = "no"
	default:
		r.ReadOnly = fmt.Sprintf("partly (%d of %d devices)", ro, len(d.Parts)+1)
	}
	return r
}

func (r *Report) readAB(d *blk.Disk) {
	misc, err := d.Part("misc")
	if err != nil {
		r.ABErr = err.Error()
		return
	}
	if misc.Start != MiscStart {
		r.ABErr = fmt.Sprintf("misc starts at sector %d, not %d: the A/B sector is not read", misc.Start, MiscStart)
		return
	}
	sec, err := d.ReadSectors(avbab.LBA, 1)
	if err != nil {
		r.ABErr = err.Error()
		return
	}
	r.AB, _ = avbab.Parse(sec)
	if err := r.AB.Validate(); err != nil {
		r.ABErr = err.Error()
		return
	}
	r.ABValid = true
	r.Next = r.AB.Pick()
	if o := 1 - r.Next; r.Next >= 0 && r.AB.Slots[o].Bootable() {
		r.Fallback = o
	}
}

func (r *Report) readIdentity(d *blk.Disk) {
	if b, err := d.ReadPart("cfg", 6, 6); err == nil {
		if mac, ok := macString(b); ok {
			r.MAC = mac
		} else {
			r.MAC = "not set"
		}
	} else {
		r.MAC = "unknown (" + err.Error() + ")"
	}
	r.Unit = Unit{ImageBase: imageBase(r.Model)}
	if blk, err := d.ReadPart(factory.Partition, 0, factory.Size); err != nil {
		r.Unit.Err = err.Error()
	} else if b, m, err := factory.Read(blk); err != nil {
		r.Unit.Err = err.Error()
		if b != nil {
			r.Unit.Value, r.Unit.HWRev = b.Model, b.HWRev
		}
	} else {
		r.Unit.Value, r.Unit.Base, r.Unit.Suffix, r.Unit.HWRev = m.Value, m.Base, m.Suffix, m.HWRev
	}
	area, err := d.ReadSectors(vendorstore.Start, vendorstore.Copies*vendorstore.CopySectors)
	if err == nil {
		r.Serial, err = vendorstore.Serial(area)
	}
	if err != nil {
		r.Serial = "unknown (" + err.Error() + ")"
	}
}

// readKeyKept looks for a valid key record in the two record sectors of
// backup (magic, version, CRC). It reads only them and keeps nothing.
func (r *Report) readKeyKept(d *blk.Disk) {
	p, err := d.Part(keykeep.Partition)
	if err != nil {
		r.KeyKept = "no (no backup partition)"
		return
	}
	lbas, err := keykeep.LBAs(p.Start, p.Size)
	if err != nil {
		r.KeyKept = "unknown (" + err.Error() + ")"
		return
	}
	r.KeyKept = "no"
	for _, l := range lbas {
		sec, err := d.ReadPart(keykeep.Partition, (l-p.Start)*512, 512)
		if err != nil {
			r.KeyKept = "unknown (" + err.Error() + ")"
			return
		}
		if keykeep.Present(sec) {
			r.KeyKept = "yes"
		}
	}
}

// readAutoUpdate reads the nvram ring of stock Android in others. The driver
// reads absolute sectors, so others must be at its place on the unit.
func (r *Report) readAutoUpdate(d *blk.Disk) {
	p, err := d.Part(nvram.PartName)
	if err == nil {
		err = nvram.CheckPartition(p.Start, p.Size)
	}
	var ring []byte
	if err == nil {
		ring, err = d.ReadPart(nvram.PartName, nvram.RingOffset, nvram.RingSize)
	}
	if err != nil {
		r.AutoUpdate.Why = err.Error()
		return
	}
	r.AutoUpdate = nvram.Parse(ring)
}

func macString(b []byte) (string, bool) {
	zero, ff := true, true
	for _, c := range b {
		zero = zero && c == 0
		ff = ff && c == 0xff
	}
	if zero || ff || b[0]&1 == 1 {
		return "", false
	}
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", b[0], b[1], b[2], b[3], b[4], b[5]), true
}

// hashPart hashes the first n bytes of a partition.
func hashPart(d *blk.Disk, name string, n int64) (string, error) {
	f, p, err := d.OpenPart(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if n > p.Size*512 {
		return "", fmt.Errorf("%s is smaller than %d bytes", name, n)
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.NewSectionReader(f, 0, n)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// matchPart finds the manifests whose image of partition kind has the sha of
// the first Size bytes of partition name.
func matchPart(d *blk.Disk, ms []*manifest.Manifest, name, kind string) (*manifest.Manifest, error) {
	cache := map[int64]string{}
	for _, m := range ms {
		p, ok := m.Part(kind)
		if !ok {
			continue
		}
		sha, done := cache[p.Size]
		if !done {
			var err error
			if sha, err = hashPart(d, name, p.Size); err != nil {
				return nil, err
			}
			cache[p.Size] = sha
		}
		if sha == p.SHA256 {
			return m, nil
		}
	}
	return nil, nil
}

func readSlot(d *blk.Disk, ms []*manifest.Manifest, i int, ab avbab.Slot) Slot {
	x := avbab.SlotName(i)
	s := Slot{Name: x, AB: ab}
	fail := func(what string, err error) { s.Errs = append(s.Errs, fmt.Sprintf("%s_%s: %v", what, x, err)) }

	// U-Boot of the slot
	if m, err := matchPart(d, ms, "uboot_"+x, "uboot"); err != nil {
		fail("uboot", err)
		s.UBoot = "unknown"
	} else if m != nil {
		s.UBoot = "U-Boot of " + m.Name()
		s.UBootUD = m.BootChain.UBootBuild == "userdebug"
	} else {
		s.UBoot = "U-Boot not in a manifest"
	}

	// boot image
	hdr, err := d.ReadPart("boot_"+x, 0, bootimg.HeaderSize)
	var h bootimg.Header
	if err == nil {
		h, err = bootimg.Parse(hdr)
	}
	switch {
	case err != nil:
		fail("boot", err)
		s.Boot = "no boot image (" + err.Error() + ")"
	case h.UcPxxToken() != "":
		s.Token = h.UcPxxToken()
		s.Boot = "uc-pxx mainline, token " + s.Token
	default:
		m, merr := matchPart(d, ms, "boot_"+x, "boot")
		switch {
		case merr != nil:
			fail("boot", merr)
		case m != nil:
			s.BootOEM = true
			s.Boot = "Android boot of " + m.Name()
		}
		if !s.BootOEM {
			s.Boot = "Android boot image, not in a manifest"
			if v := h.AndroidVersion(); v != "" {
				s.Boot += " (Android " + v + ")"
			}
		}
	}

	// system
	s.System, s.Summary = readSystem(d, ms, x, &s)
	if s.Summary == "" {
		s.Summary = s.Boot
	}
	return s
}

func readSystem(d *blk.Disk, ms []*manifest.Manifest, x string, s *Slot) (system, summary string) {
	f, p, err := d.OpenPart("system_" + x)
	if err != nil {
		s.Errs = append(s.Errs, err.Error())
		return "unknown", ""
	}
	defer f.Close()
	fs, err := ext4.Open(f, p.Size*512)
	if err != nil {
		return "no ext4 file system (" + err.Error() + ")", ""
	}
	if fs.Label == "tsxroot" {
		id, err := fs.ReadFile("/etc/tsx/build-id", 4096)
		if err != nil {
			return "uc-pxx root file system (no build id: " + err.Error() + ")", "uc-pxx mainline"
		}
		b := strings.TrimSpace(string(id))
		sum := "uc-pxx mainline, build " + b
		if s.Token != "" {
			sum += ", token " + s.Token
		}
		return "uc-pxx root file system, build " + b, sum
	}
	paths := []string{"/system/build.prop", "/build.prop"}
	for _, m := range ms {
		if m.BuildPropPath != "" && m.BuildPropPath != paths[0] {
			paths = append([]string{m.BuildPropPath}, paths...)
		}
	}
	var props map[string]string
	for _, bp := range paths {
		if b, err := fs.ReadFile(bp, 1<<20); err == nil {
			props = ext4.Props(b)
			break
		}
	}
	if props == nil {
		return fmt.Sprintf("ext4 (label %q), no build.prop", fs.Label), ""
	}
	inc := props["ro.build.version.incremental"]
	typ := props["ro.build.type"]
	if m := manifest.ByFingerprint(ms, props["ro.build.fingerprint"]); m != nil {
		return fmt.Sprintf("Android %s %s, build %s", props["ro.build.version.release"], typ, inc),
			"Crestron " + m.Name()
	}
	return fmt.Sprintf("Android %s %s, build %s (not in a manifest)", props["ro.build.version.release"], typ, inc),
		"Android build " + inc + " (unknown firmware)"
}

// unlock applies the indirect proof of the design: a slot with a userdebug
// U-Boot, a boot image that is not OEM signed, marked successful, has booted
// unsigned. So the unit was unlocked at that boot. The installer cannot ask
// OP-TEE (lock_state) from mainline.
func (r *Report) unlock() {
	for _, s := range r.Slots {
		if r.ABValid && s.AB.Keeper() && s.UBootUD && !s.BootOEM && strings.HasPrefix(s.Boot, "uc-pxx") {
			r.Unlock = "unlocked"
			r.UnlockWhy = fmt.Sprintf("slot %s booted an unsigned image with the userdebug U-Boot and is marked successful", s.Name)
			return
		}
	}
	r.Unlock = "unknown"
	r.UnlockWhy = "no slot proves it, and mainline cannot read lock_state from OP-TEE"
}

// imageBase is the model of the RAM image from its device-tree model.
func imageBase(dtModel string) string {
	switch dtModel {
	case "Crestron UC-P10":
		return factory.UCP10
	case "Crestron UC-P8":
		return factory.UCP8
	}
	return ""
}
