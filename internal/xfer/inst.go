package xfer

// The step-1 USB service of the RAM installer (`uc-pxx-xfer inst`; mainline-rc-10
// I-6, owner 2026-11-15): the install image's rcS starts it as root in place of
// `uc-pxx-inst usbd`. It makes the installer's gadget (inst_gadget.go), runs the
// receiver of kind step1 as its own user (`uc-pxx-xfer serve`, no root, no new
// privileges, no eMMC code), and seals what the receiver hands over into
// root-only folders of a RAM stage, for the on-screen flow:
//
//   - the firmware stage first: firmware.bin of a pinned size and sha256 (and
//     SSH-KEY.PUB), COMMIT {mode}. On the COMMIT the receiver is stopped, no
//     process of its user may be left, the folder is checked
//     (handover.CheckReadyStep1FW) and sealed into InstStage/fw (root 0500,
//     files 0400), and step1.json says so; the receiver then only gives the
//     status (state "busy") until the flow asks for more;
//   - the flow's requests (the want file): "bundle" after the stash (firmware.bin
//     is removed first, so the RAM holds one of the two at a time), "firmware"
//     to start again (both sealed folders are removed), "drive" for the drive,
//     "busy";
//   - the bundle stage: a mainline bundle (handover.CheckReady with the step-1
//     gate's pins), sealed into InstStage/bundle;
//   - the drive mode (the DRIVE frame of the page, or the flow's "drive" on
//     the unit's Vol-): the gadget is made again with the RAM drive
//     UC-PXX-INST added (InstBCDDeviceDrive) and the receiver gives the status
//     (state "drive") for the rest of the boot; the flow reads the drive as
//     before.
//
// Every change of the receiver takes the gadget off the UDC first: the host
// sees a disconnect and a new device with the same identity a moment later
// (the page opens it again with the permission it has).
//
// It writes only configfs, the UDC's soft_connect, its FunctionFS and stage
// mounts, the stage, InstRunDir/step1.json and /run/uc-pxx-inst/usb.state; it
// runs the installer's usb-net-up once for the network, as usbd did; it opens
// no block device.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"uc-pxx-installer/internal/gadget"
	"uc-pxx-installer/internal/xfer/handover"
)

// InstConfig of RunInst.
type InstConfig struct {
	User     string
	ConfigFS string // InstConfigFS
	FFS      string // handover.InstFFS
	Stage    string // handover.InstStage
	RunDir   string // handover.InstRunDir
	PinsFile string // handover.InstPinsFile
	Drive    string // the RAM drive file ("" = no drive mode)
	NetUp    string // the installer's usb-net-up ("" = none)
	PHY      string
	Idle     time.Duration
	Reserve  int64
	Self     string
	ChildLog io.Writer
	Logf     func(string, ...any)
	// Root is "/" (tests: the RAM image marker and the command line); SysRoot
	// the root of /sys/class (tests)
	Root, SysRoot string
	// MountStage: mount the stage tmpfs (false in the tests, which give a
	// folder)
	MountStage bool
	// StateFile is usb.state for the status lines ("" = none)
	StateFile string
}

// RAMImageMarker is the marker file of the installer RAM image.
const RAMImageMarker = "/etc/uc-pxx-inst/ram-image"

// InRAMImage reports the installer RAM image: its marker file and exactly one
// word uc-pxx-inst.ram=1 on the kernel command line.
func InRAMImage(root string) error {
	if _, err := os.Lstat(filepath.Join(root, RAMImageMarker)); err != nil {
		return fmt.Errorf("not the installer RAM image (no %s)", RAMImageMarker)
	}
	b, err := os.ReadFile(filepath.Join(root, "proc/cmdline"))
	if err != nil {
		return err
	}
	n := 0
	for _, w := range strings.Fields(string(b)) {
		if w == "uc-pxx-inst.ram=1" {
			n++
		}
	}
	if n != 1 {
		return fmt.Errorf("not the installer RAM image (%d words uc-pxx-inst.ram=1 on the command line)", n)
	}
	return nil
}

// ReadInstPins reads the pins file (root's own file).
func ReadInstPins(p string) (*handover.InstPins, error) {
	b, err := readSmall(p, 64<<10)
	if err != nil {
		return nil, err
	}
	var ps handover.InstPins
	if err := decodeStrict(b, &ps); err != nil {
		return nil, fmt.Errorf("%s: %v", p, err)
	}
	for _, f := range ps.FW {
		if f.Size <= 0 || !IsSHA(f.SHA256) {
			return nil, fmt.Errorf("%s: firmware pin %d %q", p, f.Size, f.SHA256)
		}
	}
	for _, s := range ps.Bundle {
		if !IsSHA(s) {
			return nil, fmt.Errorf("%s: bundle pin %q", p, s)
		}
	}
	if len(ps.FW) == 0 {
		return nil, fmt.Errorf("%s: no firmware pin", p)
	}
	return &ps, nil
}

// IsSHA reports 64 lower-case hex digits.
func IsSHA(s string) bool { return pinned([]string{s}, s) }

// inst is the state of one run of the service.
type inst struct {
	c       InstConfig
	id      Ident
	pins    *handover.InstPins
	dir     string // the receiver's folder in the stage
	res     handover.InstResult
	phy     func() (bool, error)
	logf    func(string, ...any)
	child   *exec.Cmd
	exited  chan error
	msgs    chan string
	linked  bool
	netDone bool
	// noProc and chown: handover.NoProcessOf and os.Lchown to root (the
	// tests give fakes: they run as a user)
	noProc func(uid int) error
	chown  func(p string) error
	// failures: the receiver starts that failed and the crash ends in a row
	// (backoff; the service never gives up while it runs: review I-6 MAJOR-2)
	failures int
	// backoff: the wait before a start after failures (instBackoff; the
	// tests give a short one)
	backoff func(n int) time.Duration
	// startFails: the receiver starts that failed in a row (a crash after a
	// good start does not count): at instMaxStartFails the service ends
	startFails int
}

// instMaxStartFails is how many failed starts in a row end the service.
const instMaxStartFails = 5

// instBackoff is 1 s, 2 s, 4 s ... up to 30 s.
func instBackoff(n int) time.Duration {
	if n >= 5 {
		return 30 * time.Second
	}
	return time.Duration(1<<n) * time.Second
}

// errRanAlready: step1.json is there: another run of the service in this
// boot (it is not overwritten).
var errRanAlready = errors.New("the service ran already in this boot")

// errInstStopped: a signal came while the receiver started.
var errInstStopped = errors.New("stopped")

// RunInst runs the service until stop.
func RunInst(c InstConfig, stop <-chan os.Signal) error {
	if c.Root == "" {
		c.Root = "/"
	}
	if c.SysRoot == "" {
		c.SysRoot = "/"
	}
	s := &inst{c: c, logf: c.Logf, noProc: handover.NoProcessOf, chown: func(p string) error { return os.Lchown(p, 0, 0) },
		backoff: instBackoff}
	if s.logf == nil {
		s.logf = func(string, ...any) {}
	}
	err := s.run(stop)
	if err != nil && !errors.Is(err, errRanAlready) && c.RunDir != "" {
		// the flow shows the drive; rcS starts uc-pxx-inst usbd (exit 1)
		s.res = handover.InstResult{Mode: handover.InstModeFailed, Error: err.Error()}
		if os.MkdirAll(c.RunDir, 0o755) == nil {
			s.publish()
		}
	}
	return err
}

func (s *inst) run(stop <-chan os.Signal) error {
	c := s.c
	if os.Geteuid() != 0 {
		return errors.New("inst needs root")
	}
	if err := InRAMImage(c.Root); err != nil {
		return err
	}
	var err error
	if s.id, err = LookupUser(c.User); err != nil {
		return err
	}
	if s.phy, err = phyRule(c.PHY); err != nil {
		return err
	}
	if s.pins, err = ReadInstPins(c.PinsFile); err != nil {
		return err
	}
	if err := os.MkdirAll(c.RunDir, 0o755); err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(c.RunDir, handover.InstResultFile)); err == nil {
		return fmt.Errorf("%s exists: %w", filepath.Join(c.RunDir, handover.InstResultFile), errRanAlready)
	}
	if c.MountStage {
		if err := os.MkdirAll(c.Stage, 0o711); err != nil {
			return err
		}
		opts := fmt.Sprintf("size=%d,mode=0711", handover.InstStageSize)
		if err := syscall.Mount("uc-pxx-inst-stage", c.Stage, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, opts); err != nil {
			return fmt.Errorf("mount the stage: %w", err)
		}
	}
	if s.dir, err = CheckStage(c.Stage, s.id); err != nil {
		return err
	}
	for _, d := range []string{handover.InstFWDir, handover.InstBundleDir} {
		if _, err := os.Lstat(filepath.Join(c.Stage, d)); err == nil {
			return fmt.Errorf("%s exists in the stage: not a new stage", d)
		}
	}
	cleanIncoming(s.dir)
	if _, err := os.Lstat(c.ConfigFS); err == nil {
		return fmt.Errorf("%s exists: another gadget owner runs (uc-pxx-inst usbd?)", c.ConfigFS)
	}
	if err := NewInstGadget(c.ConfigFS); err != nil {
		return fmt.Errorf("gadget: %w", err)
	}
	mounted := false
	defer func() {
		Unbind(c.ConfigFS)
		s.stopChild()
		if mounted {
			syscall.Unmount(c.FFS, 0)
		}
		RemoveInstGadget(c.ConfigFS)
		cleanIncoming(s.dir)
	}()
	if err := os.MkdirAll(c.FFS, 0o755); err != nil {
		return err
	}
	opts := fmt.Sprintf("uid=%d,gid=%d,rmode=0500,fmode=0600", s.id.UID, s.id.GID)
	if err := syscall.Mount(InstInstance, c.FFS, "functionfs", syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, opts); err != nil {
		return fmt.Errorf("mount functionfs on %s: %w", c.FFS, err)
	}
	mounted = true
	s.res = handover.InstResult{Mode: handover.InstModeUSB, Stage: handover.InstStageFirmware}
	return s.loop(stop)
}

// publish writes step1.json (root, 0644, atomic).
func (s *inst) publish() {
	s.res.Seq++
	b, _ := json.MarshalIndent(s.res, "", " ")
	tmp := filepath.Join(s.c.RunDir, "."+handover.InstResultFile+".tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err == nil {
		os.Chmod(tmp, 0o644)
		if err := os.Rename(tmp, filepath.Join(s.c.RunDir, handover.InstResultFile)); err != nil {
			s.logf("step1.json: %v", err)
		}
	} else {
		s.logf("step1.json: %v", err)
	}
}

// args is the receiver's command line for the stage.
func (s *inst) args() []string {
	a := []string{"serve", "--ffs", s.c.FFS, "--kind", KindStep1, "--idle", s.c.Idle.String(), "--reserve", strconv.FormatInt(s.c.Reserve, 10),
		"--info", filepath.Join(s.c.RunDir, handover.InstInfoFile)}
	switch s.res.Stage {
	case handover.InstStageFirmware:
		a = append(a, "--dir", s.dir, "--stage", Step1Firmware)
		for _, p := range s.pins.FW {
			a = append(a, "--fw-pin", fmt.Sprintf("%d:%s", p.Size, p.SHA256))
		}
	case handover.InstStageBundle:
		a = append(a, "--dir", s.dir, "--stage", Step1Bundle)
		for _, p := range s.pins.Bundle {
			a = append(a, "--pin", p)
		}
	case handover.InstStageDrive:
		a = append(a, "--status-only", "--status-state", StateDrive)
	default:
		a = append(a, "--status-only", "--status-state", StateBusy)
	}
	return a
}

// startChild starts the receiver for the current stage and waits for its
// "ready" (the descriptors are written); then the functions are linked once.
func (s *inst) startChild(stop <-chan os.Signal) error {
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd := exec.Command(s.c.Self, s.args()...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	cmd.Dir = "/"
	cmd.Stdout, cmd.Stderr = s.c.ChildLog, s.c.ChildLog
	cmd.ExtraFiles = []*os.File{pw}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: uint32(s.id.UID), Gid: uint32(s.id.GID), Groups: []uint32{}},
		Setpgid:    true,
		Pdeathsig:  syscall.SIGTERM,
	}
	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		return fmt.Errorf("receiver: %w", err)
	}
	pw.Close()
	s.child, s.exited, s.msgs = cmd, make(chan error, 1), make(chan string, 16)
	msgs, exited := s.msgs, s.exited
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			msgs <- sc.Text()
		}
		close(msgs)
	}()
	go func() { exited <- cmd.Wait() }()
	select {
	case m, ok := <-msgs:
		if !ok || m != "ready" {
			s.stopChild()
			return fmt.Errorf("receiver did not start (%q)", m)
		}
	case e := <-exited:
		s.child = nil
		return fmt.Errorf("receiver ended at the start: %v", e)
	case <-time.After(15 * time.Second):
		s.stopChild()
		return errors.New("receiver: no ready in 15 s")
	case sig := <-stop:
		s.stopChild()
		return fmt.Errorf("signal %v at the start: %w", sig, errInstStopped)
	}
	if !s.linked {
		if err := LinkInst(s.c.ConfigFS); err != nil {
			s.stopChild()
			return err
		}
		s.linked = true
	}
	s.logf("receiver ready (uid %d): stage %s, mode %s", s.id.UID, s.res.Stage, s.res.Mode)
	return nil
}

// stopChild takes the gadget off the UDC and ends the receiver.
func (s *inst) stopChild() {
	if e := Unbind(s.c.ConfigFS); e != nil {
		s.logf("unbind: %v", e)
	}
	if s.child == nil {
		return
	}
	s.child.Process.Signal(syscall.SIGTERM)
	select {
	case <-s.exited:
	case <-time.After(10 * time.Second):
		s.child.Process.Kill()
		<-s.exited
	}
	s.child = nil
}

// seal checks the receiver's hand-over of the current stage and moves it
// into its root-only folder.
func (s *inst) seal() error {
	if err := s.noProc(s.id.UID); err != nil {
		return err
	}
	var rd *handover.Ready
	var err error
	var to string
	switch s.res.Stage {
	case handover.InstStageFirmware:
		rd, err = handover.CheckReadyStep1FW(s.dir, s.id.UID, s.pins.FW)
		to = filepath.Join(s.c.Stage, handover.InstFWDir)
	case handover.InstStageBundle:
		rd, err = handover.CheckReady(s.dir, s.id.UID, s.pins.Bundle)
		to = filepath.Join(s.c.Stage, handover.InstBundleDir)
	default:
		return fmt.Errorf("a hand-over in the stage %s", s.res.Stage)
	}
	if err != nil {
		return err
	}
	if err := sealFolder(rd.Folder, to, s.chown); err != nil {
		return err
	}
	os.Remove(filepath.Join(s.dir, ReadyRecord))
	sealed := &handover.InstSealed{Dir: to, Record: rd.Record}
	if s.res.Stage == handover.InstStageFirmware {
		s.res.Firmware = sealed
	} else {
		s.res.Bundle = sealed
	}
	s.logf("sealed %s: %s %s", to, rd.Record.Files[0].Name, rd.Record.Files[0].SHA256)
	return nil
}

// sealFolder renames from (the receiver's ready folder) to to (in the same
// tmpfs), then makes every file root-owned 0400 and the folder 0500. No
// process of the receiver's user is left (NoProcessOf), so nothing can
// change a file after the check.
func sealFolder(from, to string, chown func(string) error) error {
	if _, err := os.Lstat(to); err == nil {
		return fmt.Errorf("%s exists", to)
	}
	if err := os.Rename(from, to); err != nil {
		return err
	}
	ents, err := os.ReadDir(to)
	if err != nil {
		return err
	}
	for _, e := range ents {
		p := filepath.Join(to, e.Name())
		fi, err := os.Lstat(p)
		if err != nil || !fi.Mode().IsRegular() {
			return fmt.Errorf("%s: not a regular file (%v)", p, err)
		}
		if err := chown(p); err != nil {
			return err
		}
		if err := os.Chmod(p, 0o400); err != nil {
			return err
		}
	}
	if err := chown(to); err != nil {
		return err
	}
	return os.Chmod(to, 0o500)
}

// readWant reads the flow's request: "SEQ WANT".
func (s *inst) readWant() (int, string) {
	b, err := readSmall(filepath.Join(s.c.RunDir, handover.InstWantFile), 256)
	if err != nil {
		return 0, ""
	}
	f := strings.Fields(string(b))
	if len(f) != 2 {
		return 0, ""
	}
	n, err := strconv.Atoi(f[0])
	if err != nil || n <= 0 {
		return 0, ""
	}
	return n, f[1]
}

// act changes the stage for a request (want, or the receiver's DRIVE). It
// returns true when the receiver must start again.
func (s *inst) act(want string) (bool, error) {
	switch {
	case s.res.Mode == handover.InstModeDrive:
		return false, fmt.Errorf("the drive mode is on for this boot: %q not done", want)
	case want == handover.WantDrive:
		if s.c.Drive == "" {
			return false, errors.New("this image has no RAM drive")
		}
		s.stopChild()
		s.dropHandover()
		if err := AddInstDrive(s.c.ConfigFS, s.c.Drive); err != nil {
			return true, err
		}
		s.res.Mode, s.res.Stage = handover.InstModeDrive, handover.InstStageDrive
		return true, nil
	case want == handover.WantFirmware:
		s.stopChild()
		for _, d := range []string{handover.InstFWDir, handover.InstBundleDir} {
			if err := os.RemoveAll(filepath.Join(s.c.Stage, d)); err != nil {
				return true, err
			}
		}
		s.dropHandover()
		s.res.Firmware, s.res.Bundle, s.res.Stage = nil, nil, handover.InstStageFirmware
		return true, nil
	case want == handover.WantBundle:
		if s.res.Firmware == nil || s.res.Firmware.Record.Mode != handover.ModeMainline || s.res.Bundle != nil ||
			s.res.Stage == handover.InstStageBundle {
			return false, errors.New("the bundle stage needs a sealed mainline firmware stage, no bundle yet, and not the bundle stage already")
		}
		s.stopChild()
		s.dropHandover()
		// the firmware is not needed after the stash: free its RAM first
		if err := os.Remove(filepath.Join(s.res.Firmware.Dir, FirmwareName)); err != nil && !os.IsNotExist(err) {
			return true, err
		}
		s.res.Stage = handover.InstStageBundle
		return true, nil
	case want == handover.WantBusy:
		s.stopChild()
		s.dropHandover()
		s.res.Stage = handover.InstStageBusy
		return true, nil
	}
	return false, fmt.Errorf("request %q", want)
}

// complete seals the COMMIT of the stopped receiver (or drops it).
func (s *inst) complete() {
	if err := s.seal(); err != nil {
		// nothing is handed to the flow: the stage starts again
		s.res.Error = "the hand-over: " + err.Error()
		s.logf("%s", s.res.Error)
		s.dropHandover()
	} else {
		s.res.Error, s.res.Stage = "", handover.InstStageBusy
		s.failures = 0
	}
	s.publish()
}

// dropHandover removes what a stopped receiver left in its folder: the
// files of a session (incoming.*), and a ready folder and record that were
// not sealed (a COMMIT whose "complete" this loop did not take yet): a new
// receiver refuses to start while a ready folder is there (review I-6
// MAJOR-2), and a cut PUT must not hold the stage's RAM.
func (s *inst) dropHandover() {
	cleanIncoming(s.dir)
	if err := os.RemoveAll(filepath.Join(s.dir, ReadyDir)); err != nil {
		s.logf("the ready folder: %v", err)
	}
	os.Remove(filepath.Join(s.dir, ReadyRecord))
}

func (s *inst) loop(stop <-chan os.Signal) error {
	s.publish()
	sys := s.c.SysRoot
	restart := true
	prevActive, last := false, ""
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		if restart {
			restart = false
			if s.failures > 0 {
				// a failed start or a crash before: wait, then start again (the
				// service never ends by itself: the drive stays the fallback)
				w := s.backoff(s.failures)
				s.logf("the receiver starts again in %v (%d failures in a row)", w, s.failures)
				select {
				case sig := <-stop:
					s.logf("signal %v: stopping", sig)
					return nil
				case <-time.After(w):
				}
			}
			if err := s.startChild(stop); err != nil {
				if errors.Is(err, errInstStopped) {
					s.logf("%v: stopping", err)
					return nil
				}
				s.child, s.exited, s.msgs = nil, nil, nil
				s.failures++
				if s.startFails++; s.startFails >= instMaxStartFails {
					// it never starts: end with an error, so that step1.json
					// says "failed" and rcS starts the drive gadget (review
					// I-6 re-look MINOR-R1: the drive stays the fallback)
					return fmt.Errorf("the receiver did not start %d times in a row: %v", s.startFails, err)
				}
				s.res.Error = "the receiver did not start: " + err.Error()
				s.logf("%s", s.res.Error)
				s.publish()
				restart = true
				continue
			}
			s.startFails = 0
			s.publish()
		}
		udc := gadget.FirstUDC(sys)
		active, perr := s.phy()
		bound := gadget.Bound(s.c.ConfigFS)
		switch {
		case udc == "":
		case bound == "" && active:
			if err := gadget.Bind(s.c.ConfigFS, udc); err == nil {
				s.logf("PHY active: gadget bound to %s", udc)
			}
		case bound != "" && active && !prevActive:
			gadget.Reconnect(sys, bound)
		}
		prevActive = active
		s.netUp(sys)
		st := "udc " + gadget.UDCState(sys, udc) + " (" + s.res.Mode + ", " + s.res.Stage + ")"
		if perr != nil {
			st += ", PHY state not readable: " + perr.Error()
		}
		if st != last {
			s.logf("%s", st)
			s.state(st)
			last = st
		}
		if n, w := s.readWant(); n > s.res.WantSeq {
			s.res.WantSeq = n
			r, err := s.act(w)
			if err != nil {
				s.res.Error = "request " + strconv.Itoa(n) + " " + w + ": " + err.Error()
				s.logf("%s", s.res.Error)
			} else {
				s.res.Error = ""
			}
			restart = restart || r
			s.publish()
			continue
		}
		select {
		case sig := <-stop:
			s.logf("signal %v: stopping", sig)
			return nil
		case e := <-s.exited:
			s.child = nil
			// its last lines first: a "complete" written before the end is
			// taken (the pipe ends with the process)
			complete := false
			for drain := time.After(2 * time.Second); s.msgs != nil; {
				select {
				case m, ok := <-s.msgs:
					if !ok {
						s.msgs = nil
					} else if m == "complete" {
						complete = true
					}
				case <-drain:
					s.msgs = nil
				}
			}
			if complete {
				s.logf("the receiver ended after its COMMIT: %v", e)
				s.complete()
			} else {
				s.failures++ // an end that was not asked for
				s.logf("the receiver ended: %v; it starts again", e)
				s.dropHandover()
			}
			restart = true
		case m, ok := <-s.msgs:
			if !ok {
				s.msgs = nil
				continue
			}
			switch m {
			case "complete":
				s.stopChild()
				s.complete()
				restart = true
			case "drive":
				r, err := s.act(handover.WantDrive)
				if err != nil {
					s.res.Error = "the page's DRIVE: " + err.Error()
				}
				restart = restart || r
				s.publish()
			case "reconnect":
				Unbind(s.c.ConfigFS) // the idle end: bound again at the next tick
			}
		case <-tick.C:
		}
	}
}

// netUp runs the installer's usb-net-up once when usb0 is there (as usbd).
func (s *inst) netUp(sys string) {
	if s.netDone || s.c.NetUp == "" {
		return
	}
	if _, err := os.Stat(filepath.Join(sys, "sys/class/net/usb0")); err != nil {
		return
	}
	s.netDone = true
	cmd := exec.Command(s.c.NetUp, "usb0")
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	if b, err := cmd.CombinedOutput(); err != nil {
		s.logf("%s: %v: %s", s.c.NetUp, err, strings.TrimSpace(string(b)))
	} else {
		s.logf("usb0 up: %s", strings.TrimSpace(string(b)))
	}
}

// state writes usb.state for the status lines (as usbd).
func (s *inst) state(v string) {
	if s.c.StateFile == "" {
		return
	}
	tmp := s.c.StateFile + ".tmp"
	if os.WriteFile(tmp, []byte(v+"\n"), 0o644) == nil {
		os.Rename(tmp, s.c.StateFile)
	}
}
