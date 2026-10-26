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

// CheckRAMImage refuses unless the program runs inside the installer RAM
// image: the marker file, p10inst.ram=1 on the command line, no word of a
// booted system on it, the root file system in RAM, no eMMC file system
// mounted, no Android files, and root rights.
func (e *Env) CheckRAMImage() error {
	m := e.path(RAMImageMarker)
	st, err := os.Lstat(m)
	if err != nil || !st.Mode().IsRegular() {
		return refuse("not the installer RAM image: no marker file %s", RAMImageMarker)
	}
	first, _ := readTrim(m)
	first, _, _ = strings.Cut(first, "\n")
	if strings.TrimSpace(first) != RAMImageMagic {
		return refuse("not the installer RAM image: %s does not start with %q", RAMImageMarker, RAMImageMagic)
	}
	cl, err := readTrim(e.path("/proc/cmdline"))
	if err != nil {
		return refuse("cannot read /proc/cmdline: %v", err)
	}
	ram := false
	for _, w := range strings.Fields(cl) {
		ram = ram || w == RAMCmdline
		for _, b := range bootedWords {
			if strings.HasPrefix(w, b) {
				return refuse("the kernel command line has %q: this is a booted system, not the installer RAM image", w)
			}
		}
	}
	if !ram {
		return refuse("the kernel command line has no %s: not the installer RAM image", RAMCmdline)
	}
	f, err := os.Open(e.path("/proc/mounts"))
	if err != nil {
		return refuse("cannot read /proc/mounts: %v", err)
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
			return refuse("%s is mounted on %s: the device mode needs every eMMC file system unmounted", fl[0], fl[1])
		}
	}
	switch rootFS {
	case "rootfs", "tmpfs", "ramfs":
	default:
		return refuse("the root file system is %q, not in RAM: not the installer RAM image", rootFS)
	}
	for _, p := range []string{"/init.rc", "/system/build.prop", "/system/bin/sh"} {
		if _, err := os.Lstat(e.path(p)); err == nil {
			return refuse("%s exists: this is Android, not the installer RAM image", p)
		}
	}
	if !e.regular && os.Geteuid() != 0 {
		return refuse("the device mode needs root")
	}
	return nil
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
	if model != Model {
		return nil, refuse("the device-tree model is %q, not %q: this is not a UC-P10", model, Model)
	}
	compat, _ := os.ReadFile(e.path("/proc/device-tree/compatible"))
	okc := false
	for _, c := range strings.Split(string(compat), "\x00") {
		okc = okc || c == Compatible
	}
	if !okc {
		return nil, refuse("the device-tree compatible does not list %s: this is not a UC-P10", Compatible)
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
	if err := e.checkSizes(d); err != nil {
		return nil, refuse("%v", err)
	}
	d.Table = t
	cs := sha256.Sum256([]byte(strings.ToLower(cid)))
	d.ID = plan.DeviceID{Node: EMMCPath, Model: model, Host: EMMCHost, CIDSHA256: hex.EncodeToString(cs[:]),
		SysfsLayoutSHA256: sysfsLayoutSHA(d.Sectors, d.parts)}
	if err := d.ReadOnlyAll(); err != nil {
		return nil, refuse("%v", err)
	}
	return d, nil
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
