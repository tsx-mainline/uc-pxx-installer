//go:build install && step2

package device

// The slot side of the device layer: mainline step 2, where the running,
// successful mainline slot K writes the other slot T (MAINLINE-STEP2-DESIGN.md
// §3, MAINLINE-STEP2-REST-2026-11-10.md). It is in the step-2 build only (go
// build -tags install,step2 ./cmd/uc-pxx-step2): the official install and
// status binaries (./cmd/uc-pxx-inst, without the step2 tag) never hold it, and no
// function of this file is reached from them. Nothing of the official device
// mode is changed: this file only adds.
//
// What differs from the RAM device mode:
//
//   - the environment is a slot boot, the reverse of CheckRAMImage: the
//     property /chosen/u-boot,bootargs of U-Boot is there, the command line
//     names no RAM image, the root file system is a partition of the eMMC
//     (RunningFacts reads the facts; package plan decides on them);
//   - OpenEMMCSlot makes every check of OpenEMMC (the model, the eMMC
//     controller, the card type, the size, the exact layout in sysfs and in
//     both GPT copies, every node the block device sysfs names, the model
//     gate of the factory data), but the running system has file systems of
//     the eMMC mounted: it records them (SlotMounts) instead of refusing, and
//     it does not set the read-only flags (the running root is on the eMMC);
//   - uc-pxx-bootctl-inst r2 (the same reviewed sha256 20aa6bc5…) is at
//     SlotBootctlPath, and only these forms run: status, disable --slot T,
//     arm-trying --slot T --lower-keeper, switch --to T --expect-sector HEX
//     [--dry-run] (the finish). Never repair;
//   - SlotMedium is the install medium of a slot plan (Kind "slot"): the
//     writes go through the one write open of writer.go, unchanged (the range
//     rule of the plan before every write, O_DIRECT, O_EXCL, fsync, the
//     cache drop); the A/B steps through SlotBootctl with the read-back of
//     Medium.Bootctl; at its end it sets the read-only flags back to what
//     they were (not all on: the running system writes its own root).

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/plan"
)

const (
	// SlotBootctlPath is uc-pxx-bootctl-inst r2 in a step-2 root file system
	// (uc-pxx-linux UC_PXX_STEP2_BOOTCTL_INST: root, 0700, folder 0700).
	SlotBootctlPath = "/usr/libexec/uc-pxx-step2/uc-pxx-bootctl-inst"
	// UBootArgsPath is the property U-Boot puts into the device tree of a
	// slot boot; a RAM boot by `fastboot boot` has none.
	UBootArgsPath = "/proc/device-tree/chosen/u-boot,bootargs"
	// MountInfoPath gives the device number of every mount.
	MountInfoPath = "/proc/self/mountinfo"
	maxUBootArgs  = 64 << 10
)

// SlotLive is the environment of a running slot: the real /dev, /sys, /proc,
// the pinned bootctl sha256.
func SlotLive() *Env {
	return &Env{root: "/", layout: Layout(), bootctlSHA: BootctlSHA256, runner: execRunner{}}
}

// Mount is one mounted file system of the eMMC.
type Mount struct {
	Part  string // GPT name ("" for the whole disk)
	Dev   string // mmcblk0pN
	Point string // the mount point
}

// SlotMounts lists the mounts whose device is the eMMC or one of its
// partitions (by the device number of /proc/self/mountinfo, compared with
// sysfs), and refuses a loop device whose backing file is an eMMC node.
func (e *Env) SlotMounts(d *Disk) ([]Mount, error) {
	b, err := os.ReadFile(e.path(MountInfoPath))
	if err != nil {
		return nil, refuse("cannot read %s: %v", MountInfoPath, err)
	}
	var out []Mount
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) < 5 {
			continue
		}
		ma, mi, err := parseDevNum(f[2])
		if err != nil {
			continue
		}
		if ma == d.Major && mi == d.Minor {
			out = append(out, Mount{Dev: emmcDev, Point: f[4]})
			continue
		}
		for _, p := range d.parts {
			if p.Major == ma && p.Minor == mi {
				out = append(out, Mount{Part: p.Name, Dev: p.Dev, Point: f[4]})
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
			return nil, refuse("%s is backed by %s: an eMMC device is in use", loop, v)
		}
		if n, ok := e.sameNode(d, e.path(v)); ok {
			return nil, refuse("%s is backed by %s (%s): an eMMC device is in use", loop, v, n)
		}
	}
	return out, nil
}

// slotCmdline: /proc/cmdline as text, only printable ASCII and tab, no '"'
// (the rule of cmdlineText: the kernel reads a quoted part as one word).
func slotCmdline(b []byte) (string, error) {
	s := strings.TrimSuffix(string(b), "\n")
	for i := 0; i < len(s); i++ {
		if c := s[i]; c == '"' || (c != '\t' && (c < 0x20 || c > 0x7e)) {
			return "", refuse("the kernel command line has the byte 0x%02x (byte %d): only printable ASCII and tab, no '\"'", c, i)
		}
	}
	return s, nil
}

// RunningFacts reads the facts of the running system that a slot plan needs
// (plan.RunningSlot; NetworkUp is the caller's): the property
// /chosen/u-boot,bootargs (nil when it is missing: a RAM boot), the command
// line, the GPT name of the partition mounted at / (by its device number),
// and the boot id. The installer RAM image's marker counts as a RAM image.
func (e *Env) RunningFacts(d *Disk) (plan.RunningSlot, error) {
	var ub []byte
	st, err := os.Lstat(e.path(UBootArgsPath))
	switch {
	case err == nil && st.Mode().IsRegular() && st.Size() <= maxUBootArgs:
		if ub, err = os.ReadFile(e.path(UBootArgsPath)); err != nil {
			return plan.RunningSlot{}, refuse("cannot read %s: %v", UBootArgsPath, err)
		}
		if ub == nil {
			ub = []byte{}
		}
	case err == nil:
		return plan.RunningSlot{}, refuse("%s is not a regular file of at most %d bytes", UBootArgsPath, maxUBootArgs)
	case !os.IsNotExist(err):
		return plan.RunningSlot{}, refuse("cannot read %s: %v", UBootArgsPath, err)
	}
	raw, err := os.ReadFile(e.path("/proc/cmdline"))
	if err != nil {
		return plan.RunningSlot{}, refuse("cannot read /proc/cmdline: %v", err)
	}
	cl, err := slotCmdline(raw)
	if err != nil {
		return plan.RunningSlot{}, err
	}
	root := ""
	if b, err := os.ReadFile(e.path(MountInfoPath)); err == nil {
		dev := ""
		for _, l := range strings.Split(string(b), "\n") {
			if f := strings.Fields(l); len(f) >= 5 && f[4] == "/" {
				dev = f[2] // the last one is on top
			}
		}
		if ma, mi, err := parseDevNum(dev); err == nil {
			for _, p := range d.parts {
				if p.Major == ma && p.Minor == mi {
					root = p.Name
				}
			}
		}
	}
	r := plan.ParseRunning(ub, cl, root)
	if _, err := os.Lstat(e.path(RAMImageMarker)); err == nil {
		r.RAMImage = true
	}
	if r.BootID, err = e.BootID(); err != nil {
		return r, err
	}
	return r, nil
}

// OpenEMMCSlot is OpenEMMC for a running slot: the same checks of the disk,
// its layout (sysfs and both GPT copies), its nodes and the model gate, but
// the file systems that the running system has mounted are not refused here
// (SlotMounts lists them; the step-2 session decides), and the read-only
// flags are left as they are. Nothing is written.
func (e *Env) OpenEMMCSlot(dev string) (*Disk, error) {
	if dev != EMMCPath {
		return nil, refuse("--device must be exactly %s (the eMMC of the UC-P10), not %q", EMMCPath, dev)
	}
	model, _ := readTrim(e.path("/proc/device-tree/model"))
	mi, ok := ModelOf(model)
	if !ok || mi.Base != Models[0].Base {
		return nil, refuse("the device-tree model is %q, not %q: step 2 is for the UC-P10 only", model, Models[0].DTModel)
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
		return nil, refuse("%s is a card of type %q, not the eMMC (MMC)", emmcDev, t)
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
	d.Table = t
	cs := sha256.Sum256([]byte(strings.ToLower(cid)))
	d.ID = plan.DeviceID{Node: EMMCPath, Model: model, Host: EMMCHost, CIDSHA256: hex.EncodeToString(cs[:]),
		SysfsLayoutSHA256: sysfsLayoutSHA(d.Sectors, d.parts), UnitModel: um}
	return d, nil
}

// slotBootctlForm reports whether args is a form that step 2 runs, and
// whether it writes: status, disable --slot T, arm-trying --slot T
// --lower-keeper, switch --to a --expect-sector HEX [--dry-run]. Never repair,
// never a switch to B.
func slotBootctlForm(args []string) (ok, writes bool) {
	if len(args) > 0 && args[0] == "switch" {
		// the finish switches only back to A (review STEP2-REST-1 NIT-1)
		if len(args) < 3 || args[2] != "a" {
			return false, false
		}
		return repairForm(args)
	}
	if len(args) > 0 && args[0] == "repair" {
		return false, false
	}
	return bootctlForm(args)
}

// SlotBootctlBinary reads uc-pxx-bootctl-inst at SlotBootctlPath and checks
// its sha256 (the reviewed r2), that it is a regular file, and on the unit
// that root owns it with no access for others (0700).
func (e *Env) SlotBootctlBinary() ([]byte, string, error) {
	p := e.path(SlotBootctlPath)
	st, err := os.Lstat(p)
	if err != nil {
		return nil, "", fmt.Errorf("uc-pxx-bootctl-inst: %w", err)
	}
	if !st.Mode().IsRegular() || st.Size() > maxBootctlBytes {
		return nil, "", fmt.Errorf("%s is not a regular file of at most %d bytes", SlotBootctlPath, maxBootctlBytes)
	}
	if !e.IsTest() {
		sys, _ := st.Sys().(*syscall.Stat_t)
		if sys == nil || sys.Uid != 0 || st.Mode().Perm()&0o077 != 0 {
			return nil, "", fmt.Errorf("%s must be owned by root with no access for group and others (mode %v)", SlotBootctlPath, st.Mode().Perm())
		}
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, "", err
	}
	s := sha256.Sum256(b)
	sum := hex.EncodeToString(s[:])
	if sum != e.bootctlSHA {
		return nil, sum, fmt.Errorf("%s has sha256 %s, not the reviewed binary %s: refusing to run it", SlotBootctlPath, sum, e.bootctlSHA)
	}
	return b, sum, nil
}

// SlotBootctl runs uc-pxx-bootctl-inst with a form of slotBootctlForm. A
// write form needs the A/B lock and the FUA guard, and runs with the
// read-only flag of the whole disk cleared (the tool writes LBA 49156 through
// /dev/mmcblk0); the flag is set back to what it was right after.
func (d *Disk) SlotBootctl(args []string) (*BootctlResult, error) {
	ok, writes := slotBootctlForm(args)
	if !ok {
		return nil, fmt.Errorf("uc-pxx-bootctl-inst %q is not a form that step 2 runs", args)
	}
	bin, _, err := d.e.SlotBootctlBinary()
	if err != nil {
		return nil, err
	}
	if writes {
		if !d.Locked() {
			return nil, errors.New("uc-pxx-bootctl-inst: the A/B lock is not held")
		}
		if err := d.e.checkFUA(); err != nil {
			return nil, err
		}
		was, err := d.e.getRO(d.diskNode(), d.Major, d.Minor)
		if err != nil {
			return nil, err
		}
		if err := d.e.setRO(d.diskNode(), d.Major, d.Minor, false); err != nil {
			return nil, err
		}
		defer d.e.setRO(d.diskNode(), d.Major, d.Minor, was)
	}
	return d.e.runner.Run(d.e, bin, args)
}

// SlotBootctlStatus runs `status` and reads its output.
func (d *Disk) SlotBootctlStatus() (*plan.BootctlStatus, error) {
	r, err := d.SlotBootctl([]string{"status"})
	if err != nil {
		return nil, err
	}
	if r.RC != 0 {
		return nil, fmt.Errorf("uc-pxx-bootctl-inst status: exit %d: %s", r.RC, strings.TrimSpace(r.Stderr))
	}
	return plan.ParseBootctlStatus(r.Stderr, r.Stdout)
}

// CanWriteABSlot is CanWriteAB for a slot: the A/B lock, the FUA guard, the
// reviewed binary at SlotBootctlPath. A failure means nothing was written.
func (d *Disk) CanWriteABSlot() error {
	if !d.Locked() {
		return errors.New("the A/B lock is not held")
	}
	if err := d.e.checkFUA(); err != nil {
		return err
	}
	_, _, err := d.e.SlotBootctlBinary()
	return err
}

// SlotBootctlInfo is the plan's record of the tool.
func (d *Disk) SlotBootctlInfo() (plan.Bootctl, error) {
	_, sha, err := d.e.SlotBootctlBinary()
	if err != nil {
		return plan.Bootctl{}, err
	}
	return plan.Bootctl{Path: SlotBootctlPath, SHA256: sha}, nil
}

// RKSS hashes the four ranges of the detect-and-stop compare, fresh from the
// eMMC (O_DIRECT through the whole-disk node: they are outside every
// partition).
func (d *Disk) RKSS() (map[string]string, error) {
	v := d.View()
	defer v.Close()
	return plan.ReadRKSS(v)
}

// SlotMedium is the install medium of a slot plan (install.Medium, Kind
// "slot"). It is a Medium for the writes, the verify, the reads and the sync,
// with its own Prepare, A/B steps (SlotBootctl) and Finish.
type SlotMedium struct {
	*Medium
	ro map[string]bool // the read-only flag of each node at Prepare
}

// SlotMedium makes the medium of a slot plan; chunk as Medium.
func (d *Disk) SlotMedium(chunk int) *SlotMedium {
	return &SlotMedium{Medium: d.Medium(chunk), ro: map[string]bool{}}
}

// Kind implements install.Medium.
func (m *SlotMedium) Kind() string { return "slot" }

// Prepare implements install.Medium: fresh from the device, the plan's
// eMMC, size, GPT layout, A/B sector and bootctl binary; the keeper hashes
// of a slot plan (plan.KeeperStaticParts). It records the read-only flags.
func (m *SlotMedium) Prepare(p *plan.Plan, hooks *install.Hooks) (*gpt.Table, error) {
	d := m.d
	m.cut.Hooks = hooks
	if !d.Locked() {
		return nil, errors.New("the A/B lock is not held")
	}
	if p.Mode != "slot" || p.Disk.Source != "device:"+EMMCPath || p.Device == nil || *p.Device != d.ID {
		return nil, errors.New("the plan is not a slot plan for this eMMC")
	}
	if p.Disk.Sectors != d.Sectors {
		return nil, fmt.Errorf("the eMMC has %d sectors, the plan is for %d", d.Sectors, p.Disk.Sectors)
	}
	v := d.View()
	t, err := gpt.Parse(v, d.Bytes())
	v.Close()
	if err != nil {
		return nil, fmt.Errorf("GPT: %w", err)
	}
	if t.LayoutSHA256() != p.Disk.LayoutSHA256 || t.LayoutSHA256() != d.Table.LayoutSHA256() {
		return nil, errors.New("the GPT layout differs from the plan")
	}
	sec, err := d.ReadAB()
	if err != nil {
		return nil, err
	}
	if s := sha256.Sum256(sec); hex.EncodeToString(s[:]) != p.AB.SectorSHA256 {
		return nil, errors.New("the A/B sector differs from the plan")
	}
	_, sha, err := d.e.SlotBootctlBinary()
	if err != nil {
		return nil, err
	}
	if sha != p.Bootctl.SHA256 || p.Bootctl.Path != SlotBootctlPath || p.Bootctl.Exec != "" {
		return nil, errors.New("the bootctl binary is not the one the plan names")
	}
	if len(p.KeeperSHA256) != len(plan.KeeperStaticParts()) {
		return nil, errors.New("the plan has no keeper hashes of the keeper's static parts")
	}
	m.ro[emmcDev], err = d.e.getRO(d.diskNode(), d.Major, d.Minor)
	if err != nil {
		return nil, err
	}
	for _, part := range plan.MainlineParts() {
		pp, ok := d.byName[part+"_"+p.Target]
		if !ok {
			return nil, fmt.Errorf("no %s_%s", part, p.Target)
		}
		if m.ro[pp.Dev], err = d.e.getRO(d.partNode(pp), pp.Major, pp.Minor); err != nil {
			return nil, err
		}
	}
	m.p, m.target = p, p.Target
	return t, nil
}

// BootctlStatus implements install.Medium.
func (m *SlotMedium) BootctlStatus() (*plan.BootctlStatus, error) { return m.d.SlotBootctlStatus() }

// Bootctl implements install.Medium: Medium.Bootctl's rule with the tool at
// SlotBootctlPath. Before the write bootctl's own status must agree with the
// installer's decoder on cur. Exit 0: the sector read back fresh must be the
// prediction with bytes 32..511 unchanged. Exit 1 or 2: it must be cur. Any
// other end: cur or the prediction is a known state (the run stops); anything
// else is install.ErrABUnknown (do not power off, do not restart).
func (m *SlotMedium) Bootctl(b *plan.BootctlStep, cur []byte) (*install.BootctlRun, error) {
	st, err := m.d.SlotBootctlStatus()
	if err != nil {
		return nil, err
	}
	a, _ := avbab.Parse(cur)
	if err := st.Agrees(a); err != nil {
		return nil, fmt.Errorf("before %s: bootctl status and the installer's decoder disagree: %w", strings.Join(b.Args, " "), err)
	}
	if err := m.cut.Before(nil); err != nil {
		return nil, err
	}
	want, _ := b.After.Raw()
	r, rerr := m.d.SlotBootctl(b.Args)
	run, err := slotABOutcome(m.d, b, cur, want, r, rerr, func() error {
		if b.NoOp {
			return nil
		}
		return m.cut.After(install.Event{Op: "ab", Partition: "misc", Off: plan.ABLBA * gpt.Sector, Len: gpt.Sector})
	})
	return run, err
}

// slotABOutcome is the read-back rule of an A/B write (Medium.Bootctl's): the
// sector read fresh after the tool, compared with cur and with want (bytes
// 0..31; bytes 32..511 must be cur's). wrote is called when the sector is
// the prediction.
func slotABOutcome(d *Disk, b *plan.BootctlStep, cur []byte, want [32]byte, r *BootctlResult, rerr error, wrote func() error) (*install.BootctlRun, error) {
	args := strings.Join(b.Args, " ")
	run := &install.BootctlRun{RC: -1}
	how := ""
	switch {
	case rerr != nil:
		how = "did not run to its end (" + rerr.Error() + ")"
		run.Stderr = rerr.Error()
	default:
		run.RC, run.Stderr = r.RC, r.Stderr
		how = fmt.Sprintf("ended with exit %d", r.RC)
		if r.Signal != "" {
			how = "was ended by the signal " + r.Signal
		}
	}
	back, berr := d.ReadABFresh()
	valid := berr == nil
	if valid {
		ab, _ := avbab.Parse(back)
		valid = ab.Validate() == nil
	}
	isCur := valid && bytes.Equal(back, cur)
	isNext := valid && bytes.Equal(back[:32], want[:]) && bytes.Equal(back[32:], cur[32:])
	if rerr == nil && r.Signal == "" {
		switch r.RC {
		case 0:
			if err := wrote(); err != nil {
				return run, err
			}
			if berr != nil {
				return run, fmt.Errorf("%w: the A/B sector cannot be read back: %v", ErrABUnknown, berr)
			}
			if !valid {
				return run, fmt.Errorf("%w: the A/B sector is not valid after uc-pxx-bootctl-inst", ErrABUnknown)
			}
			if !isNext {
				return run, fmt.Errorf("%w: after uc-pxx-bootctl-inst %s the A/B sector is %x, the plan predicts %x (tail same: %v)",
					ErrABUnknown, args, back[:32], want, bytes.Equal(back[32:], cur[32:]))
			}
			return run, nil
		case 1, 2:
			if !isCur {
				return run, fmt.Errorf("%w: uc-pxx-bootctl-inst exit %d says unchanged, but the sector differs", ErrABUnknown, r.RC)
			}
			what := map[int]string{1: "refused", 2: "I/O error"}[r.RC]
			return run, fmt.Errorf("uc-pxx-bootctl-inst %s: exit %d (%s, the A/B sector is verified unchanged): %s",
				args, r.RC, what, strings.TrimSpace(r.Stderr))
		}
	}
	switch {
	case isCur:
		return run, fmt.Errorf("uc-pxx-bootctl-inst %s %s; the A/B sector read back is unchanged (%s)", args, how, plan.State(first32(back)))
	case isNext && !b.NoOp:
		if err := wrote(); err != nil {
			return run, err
		}
		return run, fmt.Errorf("uc-pxx-bootctl-inst %s %s; the A/B sector read back is the plan's next state (%s); stopped there",
			args, how, plan.State(first32(back)))
	}
	return run, fmt.Errorf("%w: uc-pxx-bootctl-inst %s %s, and the A/B sector is neither the state before nor the predicted one: %s",
		ErrABUnknown, args, how, strings.TrimSpace(run.Stderr))
}

// SlotSwitch runs the finish's switch (`switch --to T --expect-sector HEX`)
// with the read-back rule of an A/B step: cur is the 512 bytes the finish
// plan was made from, want the predicted bytes 0..31. A power-cut hook of a
// test may stop it before the write (cut, nil on the unit).
func (d *Disk) SlotSwitch(b *plan.BootctlStep, cur []byte, cut *install.Cutter) (*install.BootctlRun, error) {
	if len(b.Args) != 5 || b.Args[0] != "switch" || b.Args[3] != "--expect-sector" || b.Args[4] != hex.EncodeToString(cur) {
		return nil, errors.New("the finish's switch is not switch --to T --expect-sector of the sector it was made from")
	}
	if cut != nil {
		if err := cut.Before(nil); err != nil {
			return nil, err
		}
	}
	want, err := b.After.Raw()
	if err != nil {
		return nil, err
	}
	r, rerr := d.SlotBootctl(b.Args)
	return slotABOutcome(d, b, cur, want, r, rerr, func() error {
		if cut != nil {
			return cut.After(install.Event{Op: "ab", Partition: "misc", Off: plan.ABLBA * gpt.Sector, Len: gpt.Sector})
		}
		return nil
	})
}

// OpenWrite implements install.Medium: Medium's write (writer.go,
// unchanged). writer.go sets the whole-disk read-only flag at the end of each
// write, as the RAM image keeps it; on a running system the disk flag makes
// every partition read-only (bdev_read_only: the partition's or the disk's
// flag), also the running root. So after each write the disk flag is set
// back to what Prepare found.
func (m *SlotMedium) OpenWrite(w *plan.WriteStep) (install.PartWriter, error) {
	pw, err := m.Medium.OpenWrite(w)
	if err != nil {
		m.restoreDiskRO()
		return nil, err
	}
	return &slotWriter{PartWriter: pw, m: m}, nil
}

type slotWriter struct {
	install.PartWriter
	m *SlotMedium
}

func (w *slotWriter) Close() error {
	err := w.PartWriter.Close()
	return errors.Join(err, w.m.restoreDiskRO())
}

func (m *SlotMedium) restoreDiskRO() error {
	was, seen := m.ro[emmcDev]
	if !seen {
		return nil
	}
	return m.d.e.setRO(m.d.diskNode(), m.d.Major, m.d.Minor, was)
}

// WriteKeySector implements install.Medium: a slot plan has no keep-key
// step.
func (m *SlotMedium) WriteKeySector(lba int64, rec []byte) error {
	return errors.New("a slot plan writes no key-record sector")
}

// Finish implements install.Medium: the read-only flags of the disk node and
// of the target's partition nodes set back to what Prepare found.
func (m *SlotMedium) Finish() error {
	if m.view != nil {
		m.view.Close()
		m.view = nil
	}
	var errs []error
	for _, part := range plan.MainlineParts() {
		if m.p == nil {
			break
		}
		pp, ok := m.d.byName[part+"_"+m.target]
		if was, seen := m.ro[pp.Dev]; ok && seen {
			errs = append(errs, m.d.e.setRO(m.d.partNode(pp), pp.Major, pp.Minor, was))
		}
	}
	if was, seen := m.ro[emmcDev]; seen {
		errs = append(errs, m.d.e.setRO(m.d.diskNode(), m.d.Major, m.d.Minor, was))
	}
	return errors.Join(errs...)
}

// PartReader is a read-only view of one partition through its node,
// O_DIRECT (the finish reads the step-2 record from the root file system of
// the step-2 keeper with package ext4: that partition is not mounted in the
// running slot). The caller calls the returned close.
func (d *Disk) PartReader(name string) (io.ReaderAt, int64, func(), error) {
	p, ok := d.byName[name]
	if !ok {
		return nil, 0, nil, fmt.Errorf("no partition %s", name)
	}
	f, err := d.openPartRead(p)
	if err != nil {
		return nil, 0, nil, err
	}
	return &directReader{f: f, limit: p.Bytes()}, p.Bytes(), func() { f.Close() }, nil
}

type directReader struct {
	f     *os.File
	limit int64
}

func (r *directReader) ReadAt(p []byte, off int64) (int, error) {
	if off >= r.limit {
		return 0, io.EOF
	}
	n := len(p)
	if off+int64(n) > r.limit {
		n = int(r.limit - off)
	}
	if err := readDirect(r.f, p[:n], off, r.limit); err != nil {
		return 0, err
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}
