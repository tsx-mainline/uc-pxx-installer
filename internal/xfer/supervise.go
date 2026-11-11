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
	PHY        string // "auto": the RK3399 OTG PHY rule when the machine is an RK3399, else bind at once; "rk3399"; "none"
	SysRoot    string // "/" (tests: another root for /sys/class/udc)
	Self       string // this binary (the receiver is `Self serve ...`)
	ChildLog   io.Writer
	Logf       func(string, ...any)
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
	gc := gadget.FFSConfig{Root: c.ConfigFS, Instance: Instance, VendorID: USBVendorID, ProductID: USBProductID,
		BCDDevice: USBBCDDevice, Manufacturer: USBMaker, Product: USBProduct, Serial: USBSerial, Config: "step2",
		VendorCode: MSOSVendorCode, LandingPage: LandingPage, WebUSBCode: WebUSBCode}
	mounted := false
	var child *exec.Cmd
	defer func() {
		if e := gadget.Unbind(c.ConfigFS); e != nil {
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
		if e := gadget.RemoveFFS(c.ConfigFS, Instance); e != nil {
			logf("gadget: %v", e)
		}
		if dir != "" {
			cleanIncoming(dir)
		}
	}()
	if err := gadget.NewFFS(gc); err != nil {
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
	if err := gadget.LinkFFS(c.ConfigFS, Instance); err != nil {
		waitDone()
		return err
	}
	logf("receiver ready (uid %d), status-only %v, %d pin(s)", id.UID, c.StatusOnly, len(c.Pins))
	sys := c.SysRoot
	if sys == "" {
		sys = "/"
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	prevActive, last := false, ""
	for {
		udc := gadget.FirstUDC(sys)
		active, perr := phy()
		bound := gadget.Bound(c.ConfigFS)
		switch {
		case udc == "":
		case bound == "" && active:
			if err := gadget.Bind(c.ConfigFS, udc); err != nil {
				logf("bind %s: %v", udc, err)
			} else {
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
			if e := gadget.Unbind(c.ConfigFS); e != nil { // the host sees the disconnect first
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
			if m == "reconnect" && bound != "" {
				if err := gadget.Unbind(c.ConfigFS); err != nil {
					logf("idle: unbind %s: %v", bound, err)
				} else {
					logf("idle: the gadget was taken off %s; it is bound again at the next tick", bound)
				}
			}
		case <-tick.C:
		}
	}
}
