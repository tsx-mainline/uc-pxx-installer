//go:build install

package device

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"uc-pxx-installer/internal/factory"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/plan"
)

// Refusal is a check of the device mode that failed. Nothing was written.
type Refusal struct{ Why string }

func (r *Refusal) Error() string { return "refused, nothing written: " + r.Why }

func refuse(f string, a ...any) error { return &Refusal{Why: fmt.Sprintf(f, a...)} }

// IsRefusal reports a refusal.
func IsRefusal(err error) bool {
	var r *Refusal
	return errors.As(err, &r)
}

// Command-line words that only a booted system has: the token of a
// mainline slot boot, Android, a root file system on the eMMC.
var bootedWords = []string{"uc-pxx.id=", "androidboot.", "root=", "skip_initramfs", "init="}

// isRAMToken reports the token word of the installer RAM image:
// RAMTokenPrefix and exactly 6 lowercase hex digits.
func isRAMToken(w string) bool {
	h, ok := strings.CutPrefix(w, RAMTokenPrefix)
	if !ok || len(h) != 6 {
		return false
	}
	for _, c := range h {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// cmdlineText checks the bytes of /proc/cmdline before the word checks and
// returns the line without the newline that the kernel adds at its end
// (fs/proc/cmdline.c). The word checks split the line with strings.Fields.
// The kernel (next_arg, lib/cmdline.c) splits at every byte that isspace
// accepts (lib/ctype.c: tab, LF, VT, FF, CR, space and 0xA0), and it reads
// "..." as one word and drops the quotes. So a word such as root= could hide
// from the word checks behind a 0xA0 byte or in quotes. Only printable ASCII
// (0x20..0x7e) and tab pass, and no '"'. A tab is allowed because both
// strings.Fields and the kernel split at it. No image of ours has any other
// byte: the bootargs are plain ASCII and the token is [a-z0-9-].
func cmdlineText(b []byte) (string, error) {
	s := strings.TrimSuffix(string(b), "\n")
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"':
			return "", refuse("the kernel command line has a '\"' (byte %d): the kernel reads a quoted part as one word and drops the quotes, so the word checks cannot trust the line: not the installer RAM image", i)
		case c == '\t':
		case c < 0x20 || c > 0x7e:
			return "", refuse("the kernel command line has the byte 0x%02x (byte %d), only printable ASCII and tab may be there: not the installer RAM image", c, i)
		}
	}
	return s, nil
}

// CheckRAMImage refuses unless the program runs inside the installer RAM
// image: the marker file, a kernel command line of printable ASCII
// (cmdlineText) with p10inst.ram=1 and exactly one token word
// uc-pxx.id=p10inst-XXXXXX (the image's own) and no word of a booted system,
// the root file system in RAM, no eMMC file system mounted, no Android files,
// and root rights. It returns the token (p10inst-XXXXXX, as image/build.sh
// writes it to token.txt). It does not compare the token with a list: the
// release review pins the image, and the procedure compares the token that
// the run prints and that the plan holds.
func (e *Env) CheckRAMImage() (string, error) {
	m := e.path(RAMImageMarker)
	st, err := os.Lstat(m)
	if err != nil || !st.Mode().IsRegular() {
		return "", refuse("not the installer RAM image: no marker file %s", RAMImageMarker)
	}
	first, _ := readTrim(m)
	first, _, _ = strings.Cut(first, "\n")
	if strings.TrimSpace(first) != RAMImageMagic {
		return "", refuse("not the installer RAM image: %s does not start with %q", RAMImageMarker, RAMImageMagic)
	}
	raw, err := os.ReadFile(e.path("/proc/cmdline"))
	if err != nil {
		return "", refuse("cannot read /proc/cmdline: %v", err)
	}
	cl, err := cmdlineText(raw)
	if err != nil {
		return "", err
	}
	ram, ids, token := false, 0, ""
	for _, w := range strings.Fields(cl) {
		ram = ram || w == RAMCmdline
		if strings.HasPrefix(w, "uc-pxx.id=") {
			if ids++; ids > 1 {
				return "", refuse("the kernel command line has a second uc-pxx.id= word %q: not the installer RAM image", w)
			}
			if isRAMToken(w) {
				token = strings.TrimPrefix(w, "uc-pxx.id=")
				continue
			}
		}
		for _, b := range bootedWords {
			if strings.HasPrefix(w, b) {
				return "", refuse("the kernel command line has %q: this is a booted system, not the installer RAM image", w)
			}
		}
	}
	if !ram {
		return "", refuse("the kernel command line has no %s: not the installer RAM image", RAMCmdline)
	}
	if token == "" {
		return "", refuse("the kernel command line has no token word %sXXXXXX: every installer RAM image has exactly one (image/build.sh): not the installer RAM image", RAMTokenPrefix)
	}
	f, err := os.Open(e.path("/proc/mounts"))
	if err != nil {
		return "", refuse("cannot read /proc/mounts: %v", err)
	}
	defer f.Close()
	rootFS := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fl := strings.Fields(sc.Text())
		if len(fl) < 3 {
			continue
		}
		if fl[1] == "/" {
			rootFS = fl[2] // the last one is on top
		}
		if strings.HasPrefix(fl[0], "/dev/"+emmcDev) || strings.Contains(fl[0], "/"+emmcDev+"p") {
			return "", refuse("%s is mounted on %s: the device mode needs every eMMC file system unmounted", fl[0], fl[1])
		}
	}
	switch rootFS {
	case "rootfs", "tmpfs", "ramfs":
	default:
		return "", refuse("the root file system is %q, not in RAM: not the installer RAM image", rootFS)
	}
	for _, p := range []string{"/init.rc", "/system/build.prop", "/system/bin/sh"} {
		if _, err := os.Lstat(e.path(p)); err == nil {
			return "", refuse("%s exists: this is Android, not the installer RAM image", p)
		}
	}
	if !e.regular && os.Geteuid() != 0 {
		return "", refuse("the device mode needs root")
	}
	return token, nil
}

func readSysfsPart(dir string) (Part, error) {
	var p Part
	b, err := os.ReadFile(filepath.Join(dir, "uevent"))
	if err != nil {
		return p, err
	}
	partn := -1
	for _, l := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			continue
		}
		switch k {
		case "PARTNAME":
			p.Name = v
		case "DEVNAME":
			p.Dev = v
		case "PARTN":
			partn, _ = strconv.Atoi(v)
		}
	}
	idx, err := readTrim(filepath.Join(dir, "partition"))
	if err != nil {
		return p, err
	}
	if p.Index, err = strconv.Atoi(idx); err != nil || p.Index != partn {
		return p, fmt.Errorf("%s: partition %q and PARTN %d differ", dir, idx, partn)
	}
	for k, dst := range map[string]*int64{"start": &p.Start, "size": &p.Size} {
		s, err := readTrim(filepath.Join(dir, k))
		if err != nil {
			return p, err
		}
		if *dst, err = strconv.ParseInt(s, 10, 64); err != nil {
			return p, err
		}
	}
	dn, err := readTrim(filepath.Join(dir, "dev"))
	if err != nil {
		return p, err
	}
	p.Major, p.Minor, err = parseDevNum(dn)
	return p, err
}

func sysfsLayoutSHA(sectors int64, parts []Part) string {
	h := sha256.New()
	fmt.Fprintf(h, "disk %d\n", sectors)
	for _, p := range parts {
		fmt.Fprintf(h, "%d %s %d %d\n", p.Index, p.Name, p.Start, p.Size)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// OpenEMMC checks that dev is the eMMC of a UC-P10 and returns it. It
// refuses anything else: another device name, an SD card, another board, a
// disk with another layout, a GPT that differs from sysfs, a device node that
// is not the block device sysfs names, a partition in use. At the end it sets
// the kernel read-only flag on the disk and on every partition (it changes no
// byte on the eMMC). Nothing is written.
func (e *Env) OpenEMMC(dev string) (*Disk, error) {
	if dev != EMMCPath {
		return nil, refuse("--device must be exactly %s (the eMMC of the UC-P10), not %q", EMMCPath, dev)
	}
	model, _ := readTrim(e.path("/proc/device-tree/model"))
	mi, ok := ModelOf(model)
	if !ok {
		return nil, refuse("the device-tree model is %q, not %q or %q: this is not a UC-P10 or a UC-P8", model, Models[0].DTModel, Models[1].DTModel)
	}
	compat, _ := os.ReadFile(e.path("/proc/device-tree/compatible"))
	okc := false
	for _, c := range strings.Split(string(compat), "\x00") {
		okc = okc || c == mi.Compatible
	}
	if !okc {
		return nil, refuse("the device-tree compatible does not list %s: this is not a %s", mi.Compatible, mi.Base)
	}
	sys := e.path("/sys/class/block/" + emmcDev)
	real, err := filepath.EvalSymlinks(sys)
	if err != nil {
		return nil, refuse("no %s in sysfs: %v", emmcDev, err)
	}
	if !strings.Contains(real+"/", "/"+EMMCHost+"/") {
		return nil, refuse("%s is not on the eMMC controller %s (sysfs: %s)", emmcDev, EMMCHost, strings.TrimPrefix(real, e.root))
	}
	if t, _ := readTrim(filepath.Join(sys, "device/type")); t != "MMC" {
		return nil, refuse("%s is a card of type %q, not the eMMC (MMC): an SD card?", emmcDev, t)
	}
	if _, err := os.Stat(filepath.Join(sys, "partition")); err == nil {
		return nil, refuse("%s is a partition, not a disk", emmcDev)
	}
	if s, _ := readTrim(filepath.Join(sys, "queue/logical_block_size")); s != "512" {
		return nil, refuse("%s has %q-byte sectors, not 512", emmcDev, s)
	}
	l := e.layout
	sz, _ := readTrim(filepath.Join(sys, "size"))
	if sz != strconv.FormatInt(l.DiskSectors, 10) {
		return nil, refuse("%s has %s sectors, the UC-P10 eMMC has %d", emmcDev, sz, l.DiskSectors)
	}
	cid, err := readTrim(filepath.Join(sys, "device/cid"))
	if err != nil || len(cid) != 32 {
		return nil, refuse("%s: no eMMC CID in sysfs", emmcDev)
	}
	dn, err := readTrim(filepath.Join(sys, "dev"))
	if err != nil {
		return nil, refuse("%s: %v", emmcDev, err)
	}
	d := &Disk{e: e, Sectors: l.DiskSectors, byName: map[string]Part{}}
	if d.Major, d.Minor, err = parseDevNum(dn); err != nil {
		return nil, refuse("%s: %v", emmcDev, err)
	}
	if err := e.checkNode(d.diskNode(), d.Major, d.Minor, d.Bytes()); err != nil {
		return nil, refuse("%v", err)
	}
	if h, _ := os.ReadDir(filepath.Join(sys, "holders")); len(h) > 0 {
		return nil, refuse("%s is held by %s", emmcDev, h[0].Name())
	}

	// The partitions in sysfs must be exactly the expected layout.
	dirs, _ := filepath.Glob(filepath.Join(sys, emmcDev+"p*"))
	for _, dir := range dirs {
		p, err := readSysfsPart(dir)
		if err != nil {
			return nil, refuse("sysfs %s: %v", filepath.Base(dir), err)
		}
		if p.Dev != filepath.Base(dir) || p.Dev != fmt.Sprintf("%sp%d", emmcDev, p.Index) {
			return nil, refuse("sysfs %s: DEVNAME %q, partition %d", filepath.Base(dir), p.Dev, p.Index)
		}
		if _, dup := d.byName[p.Name]; dup || p.Name == "" {
			return nil, refuse("sysfs: partition name %q is empty or used twice", p.Name)
		}
		if h, _ := os.ReadDir(filepath.Join(dir, "holders")); len(h) > 0 {
			return nil, refuse("%s (%s) is held by %s", p.Dev, p.Name, h[0].Name())
		}
		d.parts = append(d.parts, p)
		d.byName[p.Name] = p
	}
	sortParts(d.parts)
	if len(d.parts) != len(l.Entries) {
		return nil, refuse("sysfs lists %d partitions, the UC-P10 layout has %d", len(d.parts), len(l.Entries))
	}
	for _, want := range l.Entries {
		p, ok := d.byName[want.Name]
		if !ok || p.Index != want.Index || p.Start != want.Start || p.Size != want.Size {
			return nil, refuse("partition %s: sysfs has p%d start %d size %d, the UC-P10 layout p%d start %d size %d",
				want.Name, p.Index, p.Start, p.Size, want.Index, want.Start, want.Size)
		}
		if err := e.checkNode(d.partNode(p), p.Major, p.Minor, p.Bytes()); err != nil {
			return nil, refuse("%v", err)
		}
	}

	// The GPT on the disk (both copies) must be the same layout.
	v := d.View()
	t, err := gpt.Parse(v, d.Bytes())
	v.Close()
	if err != nil {
		return nil, refuse("GPT of %s: %v", emmcDev, err)
	}
	if t.DiskSectors != l.DiskSectors || len(t.Entries) != len(l.Entries) {
		return nil, refuse("the GPT has %d entries on %d sectors, the UC-P10 layout %d on %d", len(t.Entries), t.DiskSectors,
			len(l.Entries), l.DiskSectors)
	}
	for _, want := range l.Entries {
		g, ok := t.Find(want.Name)
		if !ok || g.Index != want.Index || g.Start != want.Start || g.Sectors() != want.Size {
			return nil, refuse("partition %s: the GPT has p%d start %d size %d, sysfs and the UC-P10 layout p%d start %d size %d",
				want.Name, g.Index, g.Start, g.Sectors(), want.Index, want.Start, want.Size)
		}
	}
	misc, _ := t.Find("misc")
	if misc.Start != plan.MiscStart || !misc.Contains(plan.ABLBA) {
		return nil, refuse("misc is not at LBA %d", plan.MiscStart)
	}
	um, err := d.unitModel(mi)
	if err != nil {
		return nil, err
	}
	if err := e.checkSizes(d); err != nil {
		return nil, refuse("%v", err)
	}
	if err := e.checkNotInUse(d); err != nil {
		return nil, err
	}
	d.Table = t
	cs := sha256.Sum256([]byte(strings.ToLower(cid)))
	d.ID = plan.DeviceID{Node: EMMCPath, Model: model, Host: EMMCHost, CIDSHA256: hex.EncodeToString(cs[:]),
		SysfsLayoutSHA256: sysfsLayoutSHA(d.Sectors, d.parts), UnitModel: um}
	d.roWasOff = d.roOff()
	if err := d.ReadOnlyAll(); err != nil {
		return nil, refuse("%v", err)
	}
	return d, nil
}

// sameNode reports whether a path (symlinks followed) is the disk node or a
// partition node of d, and which one.
func (e *Env) sameNode(d *Disk, path string) (string, bool) {
	st, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	type node struct {
		name, path   string
		major, minor uint32
	}
	nodes := []node{{emmcDev, d.diskNode(), d.Major, d.Minor}}
	for _, p := range d.parts {
		nodes = append(nodes, node{p.Dev + " (" + p.Name + ")", d.partNode(p), p.Major, p.Minor})
	}
	for _, n := range nodes {
		if e.regular {
			if ns, err := os.Stat(n.path); err == nil && os.SameFile(st, ns) {
				return n.name, true
			}
			continue
		}
		if st.Mode()&os.ModeDevice != 0 && st.Mode()&os.ModeCharDevice == 0 {
			r := st.Sys().(*syscall.Stat_t).Rdev
			if major64(r) == n.major && minor64(r) == n.minor {
				return n.name, true
			}
		}
	}
	return "", false
}

// checkNotInUse refuses an eMMC device that is mounted under another name
// (a /dev/disk/by-* link, a copy of the node), and a loop device whose
// backing file is an eMMC device. CheckRAMImage sees only the names.
func (e *Env) checkNotInUse(d *Disk) error {
	if b, err := os.ReadFile(e.path("/proc/mounts")); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			f := strings.Fields(l)
			if len(f) < 2 || !strings.HasPrefix(f[0], "/") {
				continue
			}
			if n, ok := e.sameNode(d, e.path(f[0])); ok {
				return refuse("%s is mounted on %s as %s: the device mode needs every eMMC file system unmounted", n, f[1], f[0])
			}
		}
	}
	files, _ := filepath.Glob(e.path("/sys/block/loop*/loop/backing_file"))
	for _, bf := range files {
		v, err := readTrim(bf)
		if err != nil || v == "" {
			continue
		}
		loop := filepath.Base(filepath.Dir(filepath.Dir(bf)))
		if strings.HasPrefix(v, "/dev/"+emmcDev) {
			return refuse("%s is backed by %s: an eMMC device is in use", loop, v)
		}
		if n, ok := e.sameNode(d, e.path(v)); ok {
			return refuse("%s is backed by %s (%s): an eMMC device is in use", loop, v, n)
		}
	}
	return nil
}

// checkSizes asks the kernel for the sector size and the size of the disk
// node (BLKSSZGET, BLKGETSIZE64).
func (e *Env) checkSizes(d *Disk) error {
	if e.regular {
		return nil
	}
	f, err := e.openRead(d.diskNode(), d.Major, d.Minor)
	if err != nil {
		return err
	}
	defer f.Close()
	var ss int32
	if err := ioctlInt(f, blkSSZGet, &ss); err != nil || ss != gpt.Sector {
		return fmt.Errorf("%s: logical sector size %d (%v)", d.diskNode(), ss, err)
	}
	var n uint64
	if err := ioctlU64(f, blkGetSize64, &n); err != nil || int64(n) != d.Bytes() {
		return fmt.Errorf("%s: size %d bytes (%v), want %d", d.diskNode(), n, err, d.Bytes())
	}
	return nil
}

// unitModel is the model gate (UC-P8.md §2.3, §11): the factory data block
// in cfg (read through the partition node, after the layout check) must
// name a UC-P10 or a UC-P8 by the rule of the stock U-Boot, with a valid
// checksum, and its base must be the model of this RAM image. The suffix
// and the revision are recorded; nothing is decided on them.
func (d *Disk) unitModel(mi ModelInfo) (plan.UnitModel, error) {
	um := plan.UnitModel{DTModel: mi.DTModel}
	cfg, ok := d.byName[factory.Partition]
	if !ok {
		return um, refuse("the factory data: no %s partition. Nothing was written.", factory.Partition)
	}
	blk := make([]byte, factory.Size)
	if err := d.ReadPart(cfg, blk, 0); err != nil {
		return um, refuse("the factory data (%s): %v. Nothing was written.", factory.Partition, err)
	}
	b, m, err := factory.Read(blk)
	if err != nil {
		return um, refuse("the factory data of this unit cannot be used: %v. This installer image is for the %s. Nothing was written.", err, mi.Base)
	}
	um.FactoryModel, um.Base, um.Suffix, um.HWRev, um.ChecksumOK = m.Value, m.Base, m.Suffix, m.HWRev, b.ChecksumOK()
	if m.Base != mi.Base {
		return um, refuse("this unit's factory data says %q: it is a %s. This installer image is for the %s. Nothing was written.", m.Value, m.Base, mi.Base)
	}
	return um, nil
}
