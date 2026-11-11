package xfer

// The uc-pxx-usb service (mainline-rc-4; owner decisions 2026-11-11, HANDOFF
// "USB interfaces in every mainline build"): every mainline build brings up
// the vendor interface of the step-2 transport on its USB-C port, as a root
// service (`uc-pxx-xfer usb`, OpenRC uc-pxx-usb). What it offers:
//
//   - (a) the status for the page, always (HELLO/STATUS; the info file);
//   - (b) the receive of the step-2 payload is NOT here: in a STEP-2 build the
//     step-2 service stops this service while step 2 waits for its payload and
//     runs its own receive and seal (internal/step2);
//   - (c) in a normal build that is an unfinished step-2 target (no done record
//     of the finish on this slot's root, no finish started in this boot, no
//     finish set staged by hand), the receive of the PINNED finish executor:
//     the slot's own finish list (handover.FinishListPath, a pinned file of the
//     USB set) is the receiver's one pin; the list names
//     uc-pxx-step2-finish and uc-pxx-bootctl-inst r2 by size and sha256.
//     After a COMMIT the receiver is stopped, no process of its user may be
//     left, the hand-over is checked (CheckReadyFinish), and the two files are
//     copied as root into a new root-only folder in RAM (SealFinish:
//     handover.FinishDir) and hashed again against the slot's own list. Only
//     then the finish executor runs from there (`finish-service`, its own
//     session, once per boot); it reads the step-2 record on the other slot's
//     root and decides by the reviewed finish rules (it refuses when this slot
//     is not a step-2 target). Nothing is installed; the receiver goes back to
//     status only.
//
// The receiver (`serve`) stays an unprivileged process with no eMMC code; this
// root side opens no block device: it writes configfs, the FunctionFS and
// stage mounts, its own /run folder, the sealed copy in /run, and it starts
// the sealed executor. The USB network function (NCM) only when the person
// set UC_PXX_USB_NET=on (the init script passes --net); no serial console.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"uc-pxx-installer/internal/xfer/handover"
)

// USBConfig of RunUSB.
type USBConfig struct {
	User     string
	ConfigFS string
	FFS      string
	PHY      string
	Idle     time.Duration
	Reserve  int64
	NCM      bool
	Self     string
	ChildLog io.Writer
	Logf     func(string, ...any)
	// Root is "/" (tests: another root for the mode decision and the info)
	Root string
}

// Modes of RunUSB.
const (
	ModeStatus = "status"
	ModeFinish = "finish"
)

// DecideMode says what the service offers on this boot (see the file comment)
// and why. In ModeFinish it also returns the slot's own list and its sha256.
func DecideMode(root string) (mode, why string, l *handover.FinishList, pin string) {
	p := func(s string) string { return filepath.Join(root, s) }
	ex := func(s string) bool { _, err := os.Lstat(p(s)); return err == nil }
	switch {
	case ex(handover.StepTwoBinary):
		return ModeStatus, "a STEP-2 build: the step-2 service receives the payload itself", nil, ""
	case ex(handover.StepTwoDone):
		return ModeStatus, "step 2 is finished on this slot (its done record is there)", nil, ""
	case ex(handover.FinishStarted):
		return ModeStatus, "the finish executor was started in this boot", nil, ""
	case ex(handover.FinishDir):
		return ModeStatus, handover.FinishDir + " exists (a finish set staged by hand): nothing is received", nil, ""
	}
	l, pin, err := handover.ReadFinishList(p(handover.FinishListPath))
	if err != nil {
		return ModeStatus, "no finish list: " + err.Error(), nil, ""
	}
	return ModeFinish, "no done record of step 2 on this slot: the pinned finish executor may be sent", l, pin
}

// RunUSB runs the uc-pxx-usb service until stop (see the file comment).
func RunUSB(c USBConfig, stop <-chan os.Signal) error {
	logf := c.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if c.Root == "" {
		c.Root = "/"
	}
	if os.Geteuid() != 0 {
		return errors.New("usb needs root")
	}
	// a supervisor that was killed leaves its gadget and mounts: take them
	// apart first (this service is the only owner of the gadget when it runs)
	sc := SuperviseConfig{User: c.User, ConfigFS: c.ConfigFS, FFS: c.FFS, PHY: c.PHY, Idle: c.Idle, Reserve: c.Reserve,
		Self: c.Self, ChildLog: c.ChildLog, Logf: logf, NCM: c.NCM, InfoFile: handover.USBInfo, StatusOnly: true}
	if err := Cleanup(sc); err != nil {
		logf("cleanup: %v", err)
	}
	if err := os.MkdirAll(handover.USBRunDir, 0o755); err != nil {
		return err
	}
	unmountStage := func() {
		if err := syscall.Unmount(handover.FinishStage, 0); err != nil && err != syscall.EINVAL && err != syscall.ENOENT {
			logf("umount %s: %v", handover.FinishStage, err)
		}
	}
	unmountStage()
	mode, why, list, pin := DecideMode(c.Root)
	logf("mode %s: %s; the USB network function %v", mode, why, c.NCM)
	if mode == ModeFinish {
		info := func(code string, lines ...string) {
			if err := handover.WriteInfo(handover.USBInfo, handover.BaseInfo(c.Root, code, lines)); err != nil {
				logf("info: %v", err)
			}
		}
		info("finish: waiting", "This slot is a step-2 target: the page may send the pinned finish executor.")
		err := c.finish(sc, list, pin, stop, info, logf)
		unmountStage()
		switch {
		case errors.Is(err, errStopped):
			return nil
		case err != nil:
			info("finish: refused", firstLine(err.Error()))
			logf("finish: %v", err)
		}
	} else if err := handover.WriteInfo(handover.USBInfo, handover.BaseInfo(c.Root, "idle", []string{why})); err != nil {
		logf("info: %v", err)
	}
	// status only, until stop (the finish executor, if it was started, writes
	// the info file from now on)
	return Supervise(sc, stop)
}

var errStopped = errors.New("stopped by a signal")

// finish: the receive of the finish set, the check, the seal, the start.
func (c USBConfig) finish(sc SuperviseConfig, list *handover.FinishList, pin string, stop <-chan os.Signal,
	info func(string, ...string), logf func(string, ...any)) error {
	if err := os.MkdirAll(handover.FinishStage, 0o711); err != nil {
		return err
	}
	opts := fmt.Sprintf("size=%d,mode=0711", handover.FinishStageSize)
	if err := syscall.Mount("uc-pxx-finish-stage", handover.FinishStage, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, opts); err != nil {
		return fmt.Errorf("mount the finish stage: %w", err)
	}
	fc := sc
	fc.StatusOnly, fc.Kind, fc.Stage, fc.Pins, fc.StopOnComplete = false, KindFinish, handover.FinishStage, []string{pin}, true
	err := Supervise(fc, stop)
	switch {
	case err == nil:
		return errStopped
	case !errors.Is(err, ErrComplete):
		return err
	}
	info("finish: checking", "The finish set was received: the unit checks it.")
	id, err := LookupUser(c.User)
	if err != nil {
		return err
	}
	if err := handover.NoProcessOf(id.UID); err != nil {
		return err
	}
	dir := filepath.Join(handover.FinishStage, XferDir)
	ready, l, err := handover.CheckReadyFinish(dir, id.UID, pin)
	if err != nil {
		return fmt.Errorf("the finish set: %w", err)
	}
	// the list the receiver took is the slot's own (its sha256 is the pin);
	// compare its entries too, then seal by the slot's own list
	for i := range list.Files {
		if l.Files[i] != list.Files[i] {
			return errors.New("the received list is not the slot's own")
		}
	}
	if err := os.WriteFile(handover.FinishStarted, []byte("sealing\n"), 0o644); err != nil {
		return err
	}
	if err := handover.SealFinish(ready.Folder, list, handover.FinishDir); err != nil {
		return fmt.Errorf("the seal: %w", err)
	}
	os.RemoveAll(dir)
	logf("finish set sealed in %s: %s %s, %s %s", handover.FinishDir, list.Files[0].Name, list.Files[0].SHA256, list.Files[1].Name, list.Files[1].SHA256)
	info("finish: started", "The finish runs on the unit (the check after step 2).")
	if err := startFinish(logf); err != nil {
		return err
	}
	return nil
}

// startFinish runs the sealed executor, as root, in its own session (a STOP
// keeps it, its locks and its screen for the rest of the boot; a restart of
// this service never ends it), its output appended to handover.FinishLog.
func startFinish(logf func(string, ...any)) error {
	bin := filepath.Join(handover.FinishDir, handover.FinishBin)
	lf, err := os.OpenFile(handover.FinishLog, os.O_WRONLY|os.O_CREATE|os.O_APPEND|syscall.O_NOFOLLOW, 0o640)
	if err != nil {
		return err
	}
	defer lf.Close()
	fmt.Fprintf(lf, "== %s uc-pxx-usb starts the sealed finish executor %s\n", time.Now().UTC().Format(time.RFC3339), bin)
	cmd := exec.Command(bin, "finish-service", "--info", handover.USBInfo)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	cmd.Dir = "/"
	cmd.Stdout, cmd.Stderr = lf, lf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", bin, err)
	}
	pid := cmd.Process.Pid
	os.WriteFile(handover.FinishStarted, []byte(fmt.Sprintf("started pid %d\n", pid)), 0o644)
	go func() {
		err := cmd.Wait()
		logf("the finish executor (pid %d) ended: %v", pid, err)
		// an executor that ended before it wrote its own result leaves the
		// page with "started" for ever: say that it ended (RC-4 page review,
		// finding 4). Its own result codes are kept.
		if in, rerr := handover.ReadInfoFile(handover.USBInfo); rerr == nil && (in.Step2 == "finish: started" || in.Step2 == "finish: checking") {
			code := "finish: refused"
			why := "The finish executor ended before it gave a result"
			if err != nil {
				why += " (" + firstLine(err.Error()) + ")"
			}
			in.Step2, in.Lines = code, []string{why + ": see " + handover.FinishLog + " on the unit."}
			if werr := handover.WriteInfo(handover.USBInfo, in); werr != nil {
				logf("info: %v", werr)
			}
		}
	}()
	logf("the finish executor runs (pid %d), log %s", pid, handover.FinishLog)
	return nil
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	if len(l) > 200 {
		l = l[:200]
	}
	return l
}
