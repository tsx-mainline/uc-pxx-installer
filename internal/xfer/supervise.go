package xfer

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"uc-pxx-installer/internal/devmem"
	"uc-pxx-installer/internal/gadget"
)

// The root side of uc-pxx-xfer (`uc-pxx-xfer run`): it makes the gadget in
// configfs, mounts FunctionFS for the receiver's user, starts the receiver as
// that user (`uc-pxx-xfer serve`, no root, no new privileges), binds the
// gadget to the UDC while the USB PHY is active (the PHY rule of
// internal/gadget, as the RAM installer's usbd), and takes everything apart
// again at the end. It writes only configfs, the UDC's soft_connect, the
// FunctionFS mount and the receiver's folder in the tmpfs stage; it opens no
// block device.

// Defaults of `uc-pxx-xfer run`.
const (
	DefaultStage    = "/run/uc-pxx-step2/stage"
	DefaultConfigFS = "/sys/kernel/config/usb_gadget/uc-pxx-step2"
	DefaultFFS      = "/run/uc-pxx-xfer/ffs"
	DefaultUser     = "uc-pxx-xfer"
	Instance        = "uc-pxx-step2"
	XferDir         = "xfer" // the receiver's folder in the stage
	tmpfsMagic      = 0x01021994
	// MaxStage is the largest stage tmpfs (the design's 1200 MiB, with room).
	MaxStage = 1536 << 20
)

// SuperviseConfig of Supervise.
type SuperviseConfig struct {
	Stage      string // the tmpfs stage (mounted by the step-2 service)
	User       string
	ConfigFS   string
	FFS        string
	Pins       []string
	Idle       time.Duration
	Reserve    int64
	StatusOnly bool
	InfoFile   string
	Kind       string // the receiver's set: KindPayload ("") or KindFinish
	// StopOnComplete: when the receiver reports a COMMIT, take the gadget
	// off, stop the receiver and return ErrComplete (the finish mode: the root
	// side checks and seals the set; the payload mode's seal is the step-2
	// service's, which stops the transport itself).
	StopOnComplete bool
	NCM            bool   // the USB network function (the person's setting, default off)
	PHY            string // "auto": the RK3399 OTG PHY rule when the machine is an RK3399, else bind at once; "rk3399"; "none"
	SysRoot        string // "/" (tests: another root for /sys/class/udc)
	Self           string // this binary (the receiver is `Self serve ...`)
	ChildLog       io.Writer
	Logf           func(string, ...any)
}

// Ident is a user's ids.
type Ident struct{ UID, GID int }

// LookupUser returns the receiver's user; root is refused.
func LookupUser(name string) (Ident, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return Ident{}, err
	}
	uid, err1 := strconv.Atoi(u.Uid)
	gid, err2 := strconv.Atoi(u.Gid)
	if err1 != nil || err2 != nil || uid <= 0 || gid <= 0 {
		return Ident{}, fmt.Errorf("user %s: uid %s gid %s (root is not allowed)", name, u.Uid, u.Gid)
	}
	// its primary group is its own group (of the same name): never a group with
	// rights, such as disk (review F10); supplementary groups are dropped at the start
	g, err := user.LookupGroupId(u.Gid)
	if err != nil || g.Name != name {
		return Ident{}, fmt.Errorf("user %s: the primary group must be the group %s (it is gid %s %v)", name, name, u.Gid, err)
	}
	return Ident{uid, gid}, nil
}

// CheckStage checks that stage is a root-owned tmpfs folder that nobody else
// can write (the RAM staging of owner Q-T3), and returns the receiver's folder
// in it, made (or checked) for id: a folder, not a link, owned by id, 0700.
func CheckStage(stage string, id Ident) (string, error) {
	var sf syscall.Statfs_t
	if err := syscall.Statfs(stage, &sf); err != nil {
		return "", err
	}
	if int64(sf.Type) != tmpfsMagic {
		return "", fmt.Errorf("%s is not a tmpfs (type %#x): the payload is staged in RAM only", stage, sf.Type)
	}
	fi, err := os.Lstat(stage)
	if err != nil {
		return "", err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.IsDir() || !ok || st.Uid != 0 || fi.Mode().Perm()&0o022 != 0 {
		return "", fmt.Errorf("%s: must be a folder of root that only root can write", stage)
	}
	// RAM only (review F10): a bounded tmpfs, and no swap (a staged page must
	// never be written to a disk)
	if size := int64(sf.Blocks) * int64(sf.Bsize); size <= 0 || size > MaxStage {
		return "", fmt.Errorf("%s: a tmpfs of %d bytes (1 to %d allowed: mount it with size=)", stage, size, int64(MaxStage))
	}
	if err := swapOff(); err != nil {
		return "", err
	}
	dir := filepath.Join(stage, XferDir)
	fi, err = os.Lstat(dir)
	switch {
	case os.IsNotExist(err):
		if err := os.Mkdir(dir, 0o700); err != nil {
			return "", err
		}
		if err := os.Chown(dir, id.UID, id.GID); err != nil {
			return "", err
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return "", err
		}
	case err != nil:
		return "", err
	default:
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !fi.IsDir() || !ok || int(st.Uid) != id.UID || fi.Mode().Perm() != 0o700 {
			return "", fmt.Errorf("%s: not the receiver's folder (a folder of uid %d, 0700)", dir, id.UID)
		}
	}
	return dir, nil
}

// cleanIncoming removes what a receiver left in dir: incoming.* folders and
// temporary files; never the ready folder or its record.
func cleanIncoming(dir string) {
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), incomingStem) || strings.HasPrefix(e.Name(), ".") {
			os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}

func phyRule(mode string) (func() (bool, error), error) {
	switch mode {
	case "none":
		return func() (bool, error) { return true, nil }, nil
	case "rk3399":
		return devmem.USBPHYActive, nil
	case "auto", "":
		if _, err := devmem.USBPHYActive(); errors.Is(err, devmem.ErrNotRK3399) {
			return func() (bool, error) { return true, nil }, nil
		}
		return devmem.USBPHYActive, nil
	}
	return nil, fmt.Errorf("--phy %q (auto, rk3399 or none)", mode)
}

// SwapsFile lists the active swap areas, and SysBlock is the block-device
// folder of sysfs (variables for the tests, as lstatNode).
var (
	SwapsFile = "/proc/swaps"
	SysBlock  = "/sys/block"
	lstatNode = syscall.Lstat
)

// swapOff: no swap area can write a staged page to a disk. SwapsFile has only
// its header line, or only zram swap areas without a backing device: zram
// keeps its pages in RAM (review NIT-1: tsx-setup turns zram swap on when the
// kernel has CONFIG_ZRAM). A zram with a backing device (writeback), any other
// swap area, or a line that is not clear refuses.
func swapOff() error {
	sw, err := os.ReadFile(SwapsFile)
	if err != nil {
		return fmt.Errorf("%s is not readable (%v): the payload is staged in RAM only, with no swap", SwapsFile, err)
	}
	lines := strings.Split(strings.TrimSpace(string(sw)), "\n")
	for _, l := range lines[1:] {
		f := strings.Fields(l)
		if len(f) < 2 {
			return fmt.Errorf("%s: a swap line that is not clear (%q): the payload is staged in RAM only, with no swap", SwapsFile, l)
		}
		if err := zramInRAM(f[0], f[1]); err != nil {
			return fmt.Errorf("swap is on (%s: %v): the payload is staged in RAM only, with no swap to a disk", f[0], err)
		}
	}
	return nil
}

// zramInRAM: the swap area p (of the type typ in SwapsFile) is a zram device
// that keeps its pages in RAM. p is a block-device node named zramN (N a
// decimal number), its device number is the one of SysBlock/zramN/dev (a
// stat, never an open), and SysBlock/zramN/backing_dev is "none" (no
// writeback to a disk; a kernel without CONFIG_ZRAM_WRITEBACK has no such file,
// and then the pages stay in RAM).
func zramInRAM(p, typ string) error {
	n, ok := strings.CutPrefix(filepath.Base(p), "zram")
	if typ != "partition" || !ok || n == "" || len(n) > 4 || strings.Trim(n, "0123456789") != "" {
		return errors.New("not a zram device")
	}
	sysd := filepath.Join(SysBlock, "zram"+n)
	want, err := os.ReadFile(filepath.Join(sysd, "dev"))
	if err != nil {
		return fmt.Errorf("no %s: %v", filepath.Join(sysd, "dev"), err)
	}
	var st syscall.Stat_t
	if err := lstatNode(p, &st); err != nil {
		return fmt.Errorf("the node: %v", err)
	}
	got := fmt.Sprintf("%d:%d", (st.Rdev>>8)&0xfff|(st.Rdev>>32)&^0xfff, st.Rdev&0xff|(st.Rdev>>12)&^0xff)
	if st.Mode&syscall.S_IFMT != syscall.S_IFBLK || got != strings.TrimSpace(string(want)) {
		return fmt.Errorf("the node is not the block device %s of %s", strings.TrimSpace(string(want)), sysd)
	}
	b, err := os.ReadFile(filepath.Join(sysd, "backing_dev"))
	switch {
	case os.IsNotExist(err):
		return nil
	case err != nil:
		return fmt.Errorf("backing_dev not readable: %v", err)
	}
	if v := strings.TrimSpace(string(b)); v != "none" {
		return fmt.Errorf("a zram with the backing device %q (writeback to a disk)", v)
	}
	return nil
}

// ErrComplete: the receiver made its ready folder (StopOnComplete).
var ErrComplete = errors.New("the receiver reported a complete set")

// Supervise runs the transport until a signal arrives on stop or the receiver
// ends. It returns nil after a clean stop.
func Supervise(c SuperviseConfig, stop <-chan os.Signal) (err error) {
	logf := c.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if os.Geteuid() != 0 {
		return errors.New("run needs root (it makes the gadget; the receiver itself runs as its own user)")
	}
	id, err := LookupUser(c.User)
	if err != nil {
		return err
	}
	phy, err := phyRule(c.PHY)
	if err != nil {
		return err
	}
	dir := ""
	if !c.StatusOnly {
		if dir, err = CheckStage(c.Stage, id); err != nil {
			return err
		}
		if _, err := os.Lstat(filepath.Join(dir, ReadyDir)); err == nil {
			return fmt.Errorf("%s: a ready payload waits for the seal; start with --status-only after the seal", dir)
		}
		cleanIncoming(dir)
	}
	if _, err := os.Lstat(c.ConfigFS); err == nil {
		return fmt.Errorf("%s exists: another uc-pxx-xfer runs, or one did not end cleanly", c.ConfigFS)
	}
	bcd := uint16(USBBCDDevice)
	if c.NCM {
		bcd = USBBCDDeviceNet // another descriptor set: Windows caches the MS OS answer per bcdDevice
	}
	gc := GadgetConfig{Root: c.ConfigFS, Instance: Instance, VendorID: USBVendorID, ProductID: USBProductID,
		BCDDevice: bcd, Manufacturer: USBMaker, Product: USBProduct, Serial: USBSerial, Config: "step2",
		VendorCode: MSOSVendorCode, LandingPage: LandingPage, WebUSBCode: WebUSBCode, NCM: c.NCM}
	mounted := false
	var child *exec.Cmd
	defer func() {
		if e := Unbind(c.ConfigFS); e != nil {
			logf("unbind: %v", e)
		}
		if child != nil && child.Process != nil {
			child.Process.Signal(syscall.SIGTERM)
			w := make(chan struct{})
			go func() { child.Wait(); close(w) }()
			select {
			case <-w:
			case <-time.After(10 * time.Second):
				child.Process.Kill()
				<-w
			}
		}
		if mounted {
			if e := syscall.Unmount(c.FFS, 0); e != nil {
				logf("umount %s: %v", c.FFS, e)
			}
		}
		if e := RemoveGadget(c.ConfigFS, Instance); e != nil {
			logf("gadget: %v", e)
		}
		if dir != "" {
			cleanIncoming(dir)
		}
	}()
	if err := NewGadget(gc); err != nil {
		return fmt.Errorf("gadget: %w", err)
	}
	if err := os.MkdirAll(c.FFS, 0o755); err != nil {
		return err
	}
	opts := fmt.Sprintf("uid=%d,gid=%d,rmode=0500,fmode=0600", id.UID, id.GID)
	if err := syscall.Mount(Instance, c.FFS, "functionfs", syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, opts); err != nil {
		return fmt.Errorf("mount functionfs on %s: %w", c.FFS, err)
	}
	mounted = true

	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	args := []string{"serve", "--ffs", c.FFS, "--idle", c.Idle.String(), "--reserve", strconv.FormatInt(c.Reserve, 10)}
	if c.StatusOnly {
		args = append(args, "--status-only")
	} else {
		args = append(args, "--dir", dir)
	}
	if c.InfoFile != "" {
		args = append(args, "--info", c.InfoFile)
	}
	if c.Kind == KindFinish {
		args = append(args, "--kind", KindFinish)
	}
	for _, p := range c.Pins {
		args = append(args, "--pin", p)
	}
	child = exec.Command(c.Self, args...)
	child.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	child.Dir = "/"
	child.Stdin = nil
	child.Stdout, child.Stderr = c.ChildLog, c.ChildLog
	child.ExtraFiles = []*os.File{pw}
	child.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: uint32(id.UID), Gid: uint32(id.GID), Groups: []uint32{}},
		Setpgid:    true,
		Pdeathsig:  syscall.SIGTERM,
	}
	if err := child.Start(); err != nil {
		child = nil
		pw.Close()
		pr.Close()
		return fmt.Errorf("receiver: %w", err)
	}
	pw.Close()
	msgs := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			msgs <- sc.Text()
		}
		close(msgs)
	}()
	exited := make(chan error, 1)
	go func() { exited <- child.Wait() }()
	defer func() { child = nil }() // Wait runs in the goroutine above
	waitDone := func() {
		child.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			child.Process.Kill()
			<-exited
		}
	}

	// the receiver says "ready" once its descriptors are written
	select {
	case m, ok := <-msgs:
		if !ok || m != "ready" {
			waitDone()
			return fmt.Errorf("receiver did not start (%q)", m)
		}
	case e := <-exited:
		return fmt.Errorf("receiver ended at the start: %v", e)
	case <-time.After(15 * time.Second):
		waitDone()
		return errors.New("receiver: no ready in 15 s")
	case s := <-stop:
		waitDone()
		logf("signal %v at the start", s)
		return nil
	}
	if err := LinkFFS(c.ConfigFS, Instance); err != nil {
		waitDone()
		return err
	}
	logf("receiver ready (uid %d), status-only %v, kind %q, %d pin(s), network function %v", id.UID, c.StatusOnly, c.Kind, len(c.Pins), c.NCM)
	sys := c.SysRoot
	if sys == "" {
		sys = "/"
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	prevActive, last, lastBindErr := false, "", ""
	var ncm ncmState // the NCM interface that has the unit's address, and the last error logged
	for {
		udc := gadget.FirstUDC(sys)
		active, perr := phy()
		bound := gadget.Bound(c.ConfigFS)
		switch {
		case udc == "":
		case bound == "" && active:
			if err := gadget.Bind(c.ConfigFS, udc); err != nil {
				if e := err.Error(); e != lastBindErr { // once per new error, not every second
					logf("bind %s: %v", udc, err)
					lastBindErr = e
				}
			} else {
				lastBindErr = ""
				logf("PHY active: gadget bound to %s", udc)
				bound = udc
			}
		case bound != "" && active && !prevActive:
			if err := gadget.Reconnect(sys, bound); err != nil {
				logf("reconnect %s: %v", bound, err)
			} else {
				logf("PHY active again: %s reconnected", bound)
			}
		}
		prevActive = active
		if c.NCM && bound != "" {
			ncm.address(c.ConfigFS, sys, logf)
		}
		s := "udc " + gadget.UDCState(sys, udc)
		if perr != nil {
			s += ", PHY state not readable: " + perr.Error()
		}
		if s != last {
			logf("%s", s)
			last = s
		}
		select {
		case sig := <-stop:
			logf("signal %v: stopping", sig)
			if e := Unbind(c.ConfigFS); e != nil { // the host sees the disconnect first
				logf("unbind: %v", e)
			}
			waitDone()
			return nil
		case e := <-exited:
			return fmt.Errorf("the receiver ended: %v", e)
		case m, ok := <-msgs:
			if !ok {
				msgs = nil
				continue
			}
			// The idle end: take the gadget off the UDC (the host sees a
			// disconnect, every endpoint read ends); the loop binds it again at
			// its next tick, as after a cable pull. (A soft_connect disconnect
			// and connect back to back is not seen by the host: no new
			// configuration; found by the guest test.)
			if m == "complete" && c.StopOnComplete {
				logf("the receiver reports a complete set: stopping it for the root check")
				if e := Unbind(c.ConfigFS); e != nil {
					logf("unbind: %v", e)
				}
				waitDone()
				return ErrComplete
			}
			if m == "reconnect" && bound != "" {
				if err := Unbind(c.ConfigFS); err != nil {
					logf("idle: unbind %s: %v", bound, err)
				} else {
					logf("idle: the gadget was taken off %s; it is bound again at the next tick", bound)
				}
			}
		case <-tick.C:
		}
	}
}

// The unit's address on the USB network (only with the person's setting
// UC_PXX_USB_NET=on): the computer is NCMHostIP.
const (
	NCMUnitIP = "10.99.0.2/24"
	NCMHostIP = "10.99.0.1"
)

// IPCmd is ip(8) by its full path (review NIT-4: not by the PATH of the
// process that started uc-pxx-xfer). A variable for the tests.
var IPCmd = "/sbin/ip"

// ncmState: done is the NCM interface instance (its name and ifindex) that has
// the unit's address; lastErr is the last error that was logged.
type ncmState struct {
	done, lastErr string
}

// address gives the NCM interface of the gadget the unit's address and brings
// it up, once per interface instance: a gadget that is made again (the finish
// receive, a restart, a respawn) gets a new interface and the address again
// (review MINOR-2). It writes no file except the interface's disable_ipv6; it
// runs IPCmd. The loop calls it every second: an error is logged once, and
// again only when it changes or after a success (review NIT-4).
func (st *ncmState) address(cfs, sys string, logf func(string, ...any)) {
	fail := func(format string, a ...any) {
		if m := fmt.Sprintf(format, a...); m != st.lastErr {
			logf("%s", m)
			st.lastErr = m
		}
	}
	b, err := os.ReadFile(filepath.Join(cfs, "functions", "ncm."+NCMInstance, "ifname"))
	if err != nil {
		return
	}
	n := strings.TrimSpace(string(b))
	if !strings.HasPrefix(n, "usb") || len(n) > 15 || strings.ContainsAny(n, "/ \t") {
		return
	}
	ix, err := os.ReadFile(filepath.Join(sys, "sys/class/net", n, "ifindex"))
	if err != nil {
		return
	}
	inst := n + "#" + strings.TrimSpace(string(ix))
	if inst == st.done {
		return
	}
	// no IPv6 on the cable: no link-local address that sshd's key-only
	// rule (by the IPv4 address) would not see
	if err := os.WriteFile(filepath.Join(sys, "proc/sys/net/ipv6/conf", n, "disable_ipv6"), []byte("1\n"), 0o644); err != nil && !os.IsNotExist(err) {
		fail("the USB network: IPv6 off on %s: %v", n, err)
		return
	}
	env := []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	add := exec.Command(IPCmd, "addr", "add", NCMUnitIP, "dev", n)
	add.Env = env
	if out, err := add.CombinedOutput(); err != nil && !strings.Contains(string(out), "exists") {
		fail("the USB network: %s addr add %s dev %s: %v %s", IPCmd, NCMUnitIP, n, err, strings.TrimSpace(string(out)))
		return
	}
	up := exec.Command(IPCmd, "link", "set", n, "up")
	up.Env = env
	if out, err := up.CombinedOutput(); err != nil {
		fail("the USB network: %s link set %s up: %v %s", IPCmd, n, err, strings.TrimSpace(string(out)))
		return
	}
	logf("the USB network: %s has %s (the computer is %s; ssh by key only there)", n, NCMUnitIP, NCMHostIP)
	st.done, st.lastErr = inst, ""
}

// Cleanup takes apart what a run that did not end cleanly left (a SIGKILL or
// an OOM kill of the supervisor; review F9): the gadget off its UDC, the
// FunctionFS mount, the configfs folders, the incoming folders of the stage.
// It never touches the ready folder or its record. The step-2 service runs it
// (root) before it starts the transport again.
func Cleanup(c SuperviseConfig) error {
	if os.Geteuid() != 0 {
		return errors.New("cleanup needs root")
	}
	var errs []string
	if err := Unbind(c.ConfigFS); err != nil && !os.IsNotExist(err) {
		errs = append(errs, "unbind: "+err.Error())
	}
	if err := syscall.Unmount(c.FFS, 0); err != nil && err != syscall.EINVAL && err != syscall.ENOENT {
		errs = append(errs, "umount "+c.FFS+": "+err.Error())
	}
	if _, err := os.Lstat(c.ConfigFS); err == nil {
		if err := RemoveGadget(c.ConfigFS, Instance); err != nil {
			errs = append(errs, "gadget: "+err.Error())
		}
	}
	if c.Stage != "" {
		dir := filepath.Join(c.Stage, XferDir)
		if fi, err := os.Lstat(dir); err == nil && fi.IsDir() {
			cleanIncoming(dir)
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}
