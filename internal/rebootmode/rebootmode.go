// Package rebootmode is the restart of the status RAM image into the
// download mode of the vendor U-Boot (rockusb, USB 2207:330c "Loader"). It is
// the way back for a unit whose BCB holds a stale command (every start goes
// to fastboot): fastboot, then `fastboot boot` of the status image, then this
// restart, then the BCB restore from the download mode (DLMODE.md of the
// plan, RULES.md "Stale BCB").
//
// It is the route of /usr/sbin/uc-pxx-reboot loader of uc-pxx-linux
// (tools/uc-pxx-reboot/uc-pxx-reboot-syscall.c): sync(2), then reboot(2) with
// LINUX_REBOOT_CMD_RESTART2 and the argument "loader". The kernel's
// syscon-reboot-mode driver turns the argument into the magic of the device
// tree node reboot-mode (mode-loader = BOOT_BL_DOWNLOAD, 0x5242c301) and
// writes it to offset 0x300 of the PMU GRF (PMUGRF_OS_REG0, 0xff320300). The
// vendor U-Boot reads that register at the next start, before it reads the
// BCB, and enters rockusb. A cold power cycle clears the register.
//
// Nothing is written to the eMMC: the package opens no file for writing
// (ci/check-no-writer.py, ci/goallow). Its only action is the one reboot(2)
// call in restart2. A register write from user space would not work: a
// restart writes the magic of its own argument (a plain restart writes
// mode-normal) over it (FASTBOOT-MODE.md, session 1).
//
// Probe checks first, from the files of the running kernel, that the
// restart will do this. It refuses outside the installer RAM image (no
// uc-pxx-inst.ram=1, an eMMC file system mounted: a direct restart does not stop
// a booted system cleanly), on a machine that is not a UC-P10 or UC-P8, and
// when the live device tree or the driver would not write 0x5242c301 to the
// PMU GRF at 0x300 (then the kernel ignores the argument).
package rebootmode

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// The values of the check (rk3399-crestron-uc-pxx.dtsi of the uc-pxx-lts
// kernel, include/dt-bindings/soc/rockchip,boot-mode.h).
const (
	// Arg is the reboot command argument: the name of the mode-loader
	// property without its "mode-" prefix. It is the only argument this
	// package ever passes.
	Arg = "loader"
	// Magic is BOOT_BL_DOWNLOAD, the value U-Boot reads as "loader".
	Magic = 0x5242c301
	// Offset is the offset of PMUGRF_OS_REG0 in the PMU GRF.
	Offset = 0x300
	// RegAddr is the register (the PMU GRF at 0xff320000 plus Offset).
	RegAddr = 0xff320300

	pmugrfNode   = "syscon@ff320000"
	pmugrfCompat = "rockchip,rk3399-pmugrf"
	nodeName     = "reboot-mode"
	nodeCompat   = "syscon-reboot-mode"
	driverDir    = "sys/bus/platform/drivers/syscon-reboot-mode"
	// RAMMarker and RAMMagic: the marker file of the installer RAM images
	// (image/overlay/etc/uc-pxx-inst/ram-image).
	RAMMarker = "etc/uc-pxx-inst/ram-image"
	RAMMagic  = "uc-pxx-inst installer RAM image"
	// RAMCmdline is the word image/build.sh adds to the command line.
	RAMCmdline = "uc-pxx-inst.ram=1"
	// tokenWord and tokenWordOld: the token word of the RAM images (owner
	// Q-R1 2026-11-11: pxx-inst-XXXXXX) and of the images before it
	// (p10inst-XXXXXX), still accepted
	tokenWord    = "uc-pxx.id=pxx-inst-"
	tokenWordOld = "uc-pxx.id=p10inst-"
)

// Models are the device-tree models of the installer RAM images.
var Models = []string{"Crestron UC-P10", "Crestron UC-P8"}

// Result is the outcome of Probe: OK, or the first refusal in Why. Lines
// tell what was checked, one line each, for the screen and the log.
type Result struct {
	OK    bool
	Why   string
	Lines []string
	Model string
	Token string // the RAM image's token, pxx-inst-XXXXXX (or the old p10inst-XXXXXX)
}

func (r *Result) ok(f string, a ...any) { r.Lines = append(r.Lines, "ok: "+fmt.Sprintf(f, a...)) }

func (r *Result) refuse(f string, a ...any) Result {
	r.Why = fmt.Sprintf(f, a...)
	r.Lines = append(r.Lines, "refused: "+r.Why)
	r.OK = false
	return *r
}

func cstrings(b []byte) []string {
	var out []string
	for _, s := range bytes.Split(bytes.TrimRight(b, "\x00"), []byte{0}) {
		out = append(out, string(s))
	}
	return out
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// u32 reads a one-cell device-tree property.
func u32(path string) (uint32, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	if len(b) != 4 {
		return 0, fmt.Errorf("%d bytes, not one cell", len(b))
	}
	return binary.BigEndian.Uint32(b), nil
}

// Probe checks, read only, that a restart with Arg enters the download mode
// and that this is the installer RAM image. root is "/" on the unit (tests:
// a folder with proc, sys and etc).
func Probe(root string) Result {
	var r Result
	p := func(rel string) string { return filepath.Join(root, rel) }

	// 1. the installer RAM image
	mk, err := os.ReadFile(p(RAMMarker))
	first, _, _ := strings.Cut(string(mk), "\n")
	if err != nil || strings.TrimSpace(first) != RAMMagic {
		return r.refuse("not the installer RAM image: no marker file /%s", RAMMarker)
	}
	cl, err := os.ReadFile(p("proc/cmdline"))
	if err != nil {
		return r.refuse("cannot read /proc/cmdline: %v", err)
	}
	ram, tokens := false, 0
	for _, w := range strings.Fields(string(cl)) {
		ram = ram || w == RAMCmdline
		if strings.HasPrefix(w, "uc-pxx.id=") {
			tokens++
			r.Token = strings.TrimPrefix(w, "uc-pxx.id=")
			if !strings.HasPrefix(w, tokenWord) && !strings.HasPrefix(w, tokenWordOld) {
				return r.refuse("the kernel command line has %q: a booted system, not the installer RAM image", w)
			}
		}
	}
	if !ram || tokens != 1 {
		return r.refuse("the kernel command line has no %s or not exactly one token word: not the installer RAM image", RAMCmdline)
	}
	mounts, err := os.ReadFile(p("proc/mounts"))
	if err != nil {
		return r.refuse("cannot read /proc/mounts: %v", err)
	}
	for _, l := range strings.Split(string(mounts), "\n") {
		if f := strings.Fields(l); len(f) > 0 && strings.HasPrefix(f[0], "/dev/mmcblk") {
			return r.refuse("an eMMC file system is mounted (%s on %s): the restart does not stop a system", f[0], f[1])
		}
	}
	r.ok("the installer RAM image (token %s), no eMMC file system mounted", r.Token)

	// 2. the machine
	dt := p("proc/device-tree")
	compat, _ := os.ReadFile(filepath.Join(dt, "compatible"))
	model, _ := os.ReadFile(filepath.Join(dt, "model"))
	r.Model = strings.TrimRight(string(model), "\x00\n ")
	if !has(cstrings(compat), "rockchip,rk3399") || !has(Models, r.Model) {
		return r.refuse("the device tree is %q (%s), not a UC-P10 or UC-P8", r.Model, strings.Join(cstrings(compat), ", "))
	}
	r.ok("the machine is a %s (rockchip,rk3399)", r.Model)

	// 3. the reboot-mode node of the live device tree
	grf := filepath.Join(dt, pmugrfNode)
	gc, err := os.ReadFile(filepath.Join(grf, "compatible"))
	if err != nil || !has(cstrings(gc), pmugrfCompat) || !has(cstrings(gc), "syscon") {
		return r.refuse("no PMU GRF syscon node /%s with %q", pmugrfNode, pmugrfCompat)
	}
	node := filepath.Join(grf, nodeName)
	nc, err := os.ReadFile(filepath.Join(node, "compatible"))
	if err != nil || !has(cstrings(nc), nodeCompat) {
		return r.refuse("the device tree has no %s node with %q under the PMU GRF: the kernel would ignore the argument", nodeName, nodeCompat)
	}
	if st, err := os.ReadFile(filepath.Join(node, "status")); err == nil {
		if s := strings.TrimRight(string(st), "\x00"); s != "okay" && s != "ok" {
			return r.refuse("the %s node has status %q", nodeName, s)
		}
	}
	off, err := u32(filepath.Join(node, "offset"))
	if err != nil || off != Offset {
		return r.refuse("the %s node has offset %#x (%v), not %#x", nodeName, off, err, Offset)
	}
	if b, err := os.ReadFile(filepath.Join(node, "mask")); err == nil && !bytes.Equal(b, []byte{0xff, 0xff, 0xff, 0xff}) {
		return r.refuse("the %s node has a mask (%x): the register would not get the whole magic", nodeName, b)
	}
	mag, err := u32(filepath.Join(node, "mode-"+Arg))
	if err != nil || mag != Magic {
		return r.refuse("the %s node has mode-%s %#08x (%v), not %#08x (BOOT_BL_DOWNLOAD)", nodeName, Arg, mag, err, Magic)
	}
	r.ok("device tree: %s/%s, offset %#x, mode-%s = %#08x (BOOT_BL_DOWNLOAD)", pmugrfNode, nodeName, off, Arg, mag)

	// 4. the driver is bound to that node (else nothing writes the register)
	ents, _ := os.ReadDir(p(driverDir))
	bound := ""
	for _, e := range ents {
		l, err := os.Readlink(filepath.Join(p(driverDir), e.Name(), "of_node"))
		if err == nil && strings.HasSuffix(filepath.ToSlash(l), "/"+pmugrfNode+"/"+nodeName) {
			bound = e.Name()
		}
	}
	if bound == "" {
		return r.refuse("the driver %s is not bound to the %s node: the kernel would ignore the argument", nodeCompat, nodeName)
	}
	r.ok("the driver %s is bound (%s)", nodeCompat, bound)
	r.OK = true
	return r
}

// restart2 is the one action of the package: sync(2), then reboot(2) with
// LINUX_REBOOT_CMD_RESTART2 and the argument. On success it does not return.
// Tests replace it.
var restart2 = func(arg string) error {
	p, err := syscall.BytePtrFromString(arg)
	if err != nil {
		return err
	}
	syscall.Sync()
	_, _, e := syscall.Syscall6(syscall.SYS_REBOOT, syscall.LINUX_REBOOT_MAGIC1, syscall.LINUX_REBOOT_MAGIC2,
		syscall.LINUX_REBOOT_CMD_RESTART2, uintptr(unsafe.Pointer(p)), 0, 0)
	return fmt.Errorf("reboot(2) RESTART2 %q: %v", arg, e)
}

// Restart checks again (Probe) and then restarts into the download mode. It
// returns only when it refused or the system call failed.
func Restart(root string) error {
	r := Probe(root)
	if !r.OK {
		return fmt.Errorf("refused: %s", r.Why)
	}
	return restart2(Arg)
}
