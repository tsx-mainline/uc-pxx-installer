package provision

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/clone"
	"uc-pxx-installer/internal/provision/emmc"
	"uc-pxx-installer/internal/xfer/handover"
)

// The programs the service runs (pinned files of the base).
const (
	APKBin    = "/sbin/apk"
	ShellBin  = "/bin/sh"
	RCService = "/sbin/rc-service"
	XferBin   = "/usr/libexec/uc-pxx-xfer/uc-pxx-xfer"
	XferUser  = "uc-pxx-xfer"
	// AutoupdateLock and AutoupdateAPKLock are tsx-autoupdate's run lock and
	// its apk lock (tsx-linux-common c706ca5f…: run_locked and fetch_first,
	// non-blocking flocks).
	AutoupdateLock    = "/run/tsx/autoupdate.lock"
	AutoupdateAPKLock = "/run/tsx/autoupdate-apk.lock"
	// USBConf is the conf of uc-pxx-usb (the person's USB network setting).
	USBConf = "/etc/conf.d/uc-pxx-usb"
	// RepositoriesFile is the unit's repositories file of the bundle (only its
	// four folders in the RAM repo; never /etc/apk/repositories).
	RepositoriesFile = handover.ProvisionRunDir + "/repositories"
	panelTTY         = "/dev/tty1"
)

// execEnv is the environment of every child: exactly these three variables
// (Alpine's default PATH). The reference build must run apk and
// profile-apply.sh with the same (R23-FORMATS Part 4, review MINOR-4:
// uc-pxx-linux rootfs/r23-refs.sh `chroot … env -i` with this list).
var execEnv = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/root", "LC_ALL=C"}

// LiveEnv is the Env of a unit (root "/"). rel is this program's release
// data: uc-pxx-xfer runs as root only with its pinned sha256.
func LiveEnv(rel *clone.ReleaseData, logf func(string, ...any)) *Env {
	return &Env{
		Root: "/", Now: time.Now, Sleep: time.Sleep, Logf: logf,
		Marked: liveMarked, Locks: liveLocks, Guard: liveGuard, Exec: liveExec, Fetch: HTTPFetcher(),
		USB: &liveUSB{rel: rel}, Panel: livePanel, Free: freeBytes, Mem: memAvailable, ClockSynced: clockSynced,
		MountStage: mountStage, UnmountStage: unmountStage,
	}
}

// liveMarked reads the A/B sector (misc + 4 sectors, O_DIRECT, read only;
// internal/provision/emmc) and the slot U-Boot started
// (androidboot.slot_suffix of the device tree).
func liveMarked() (bool, string, error) {
	slot := handover.BaseInfo("/", "", nil).Slot
	if slot != "a" && slot != "b" {
		return false, "", errors.New("U-Boot's command line names no slot (androidboot.slot_suffix)")
	}
	b, err := emmc.ABSector("/")
	if err != nil {
		return false, slot, err
	}
	a, _ := avbab.Parse(b)
	i := 0
	if slot == "b" {
		i = 1
	}
	if err := a.Validate(); err != nil {
		return false, slot, err
	}
	return a.Slots[i].Keeper() && !a.Tie() && a.Pick() == i, slot, nil
}

func flockNB(p string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_APPEND|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("%s is held", p)
	}
	return f, nil
}

// liveLocks takes the run lock, then the apk lock (as step 2 takes the run
// lock, then the A/B lock); a busy apk lock gives the run lock back first.
func liveLocks() (func(), error) {
	run, err := flockNB(AutoupdateLock)
	if err != nil {
		return nil, fmt.Errorf("tsx-autoupdate runs now (%v)", err)
	}
	apk, err := flockNB(AutoupdateAPKLock)
	if err != nil {
		run.Close()
		return nil, fmt.Errorf("tsx-autoupdate's apk update runs now (%v)", err)
	}
	return func() { apk.Close(); run.Close() }, nil
}

// liveGuard sets the kernel read-only flag of the eMMC and of every
// partition but system_<slot> (the running root, mounted already).
func liveGuard(slot string) (func() error, error) {
	return emmc.Guard("/", "system_"+slot)
}

// liveExec runs one of the pinned programs (apk, the shell with the profile
// script, rc-service: uc-pxx-usb stop/start and, mainline-rc-10b, start and
// status of the profile's services; uc-pxx-xfer) with the fixed environment; its output
// (stdout and stderr) goes to line.
func liveExec(argv []string, line func(string)) error {
	switch {
	case len(argv) == 0:
		return errors.New("no program")
	case argv[0] == APKBin && len(argv) > 6 && (argv[1] == "add" || argv[1] == "fix") && argv[2] == "--no-network" &&
		argv[3] == "--no-cache" && argv[4] == "--repositories-file" && argv[5] == RepositoriesFile:
	case argv[0] == ShellBin && len(argv) >= 4 && argv[1] == ApplyScript:
	case argv[0] == RCService && len(argv) == 3 && argv[1] == "uc-pxx-usb" && (argv[2] == "stop" || argv[2] == "start"):
	case argv[0] == RCService && len(argv) == 3 && serviceName(argv[1]) && (argv[2] == "start" || argv[2] == "status"):
	case argv[0] == XferBin && len(argv) == 4 && argv[1] == "cleanup" && argv[2] == "--stage" && argv[3] == handover.ProvisionStage:
	default:
		return fmt.Errorf("the service does not run %q", argv)
	}
	return runBounded(argv, execLimit(argv), execWaitDelay, line)
}

// The bounds of one rc-service call (RC-10b review MINOR-1: the health gate's
// ServiceWait bounds only its status loop; a start that never returns, or a
// daemon that keeps the output pipe, must not hold provisioning). A call that
// does not end in time is killed and is an error: for a status, the service
// is not started; for a start, the gate's status loop decides. apk, the
// profile script and uc-pxx-xfer cleanup have no time bound (their own work
// decides their time), and their exit status decides: their output pipe is
// closed execWaitDelay after their end too, but a process they left that
// held it is only logged, not an error (RC-10c review MINOR-1: a package
// script's leftover child must not turn a good install into a failed one).
const (
	RCCallLimit   = 60 * time.Second // rc-service NAME start (and uc-pxx-usb stop/start)
	RCStatusLimit = 20 * time.Second // rc-service NAME status
	execWaitDelay = 5 * time.Second  // after the end of the call: the output pipe is closed
)

// execLimit is the longest time of one call of argv (0: no bound).
func execLimit(argv []string) time.Duration {
	if argv[0] != RCService {
		return 0
	}
	if argv[len(argv)-1] == "status" {
		return RCStatusLimit
	}
	return RCCallLimit
}

// runBounded runs argv with the fixed environment; its output (stdout and
// stderr) goes to line. With limit > 0 the call is killed (SIGKILL) when it
// has not ended within limit; delay > 0 closes the output pipe that long after
// the end of the call (or the kill), so a child that keeps the pipe open (a
// daemon) cannot hold the caller. Such a call is an error when it has a limit
// (rc-service); with no limit (limit 0: apk, the profile script, uc-pxx-xfer
// cleanup) its exit status decides and the closed pipe is one line of the log.
func runBounded(argv []string, limit, delay time.Duration, line func(string)) error {
	ctx, cancel := context.Background(), context.CancelFunc(func() {})
	if limit > 0 {
		ctx, cancel = context.WithTimeout(ctx, limit)
	}
	defer cancel()
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	c.Env, c.Dir, c.Stdin = execEnv, "/", nil
	c.WaitDelay = delay
	pr, pw := io.Pipe()
	c.Stdout, c.Stderr = pw, pw
	if err := c.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			if line != nil {
				line(sc.Text())
			}
		}
		io.Copy(io.Discard, pr)
		close(done)
	}()
	err := c.Wait()
	pw.Close()
	<-done
	switch {
	case limit > 0 && errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fmt.Errorf("%s did not end within %s: killed (%v)", strings.Join(argv, " "), limit, err)
	case errors.Is(err, exec.ErrWaitDelay) && limit == 0 && c.ProcessState != nil && c.ProcessState.Success():
		// Wait gives ErrWaitDelay only after an exit status 0
		if line != nil {
			line(fmt.Sprintf("%s ended with exit status 0; a process it left held its output open (closed after %s)", argv[0], delay))
		}
		return nil
	case errors.Is(err, exec.ErrWaitDelay):
		return fmt.Errorf("%s ended, but a process it left holds its output open (%v)", strings.Join(argv, " "), err)
	}
	return err
}

// livePanel writes the lines on tty1 (the console base has no kiosk).
func livePanel(lines []string) {
	f, err := os.OpenFile(panelTTY, os.O_WRONLY|syscall.O_NOCTTY, 0)
	if err != nil {
		return
	}
	defer f.Close()
	var b strings.Builder
	b.WriteString("\033[H\033[2J")
	for _, l := range lines {
		b.WriteString(l + "\r\n")
	}
	f.WriteString(b.String())
}

func freeBytes(p string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(p, &st); err != nil {
		return 0
	}
	return int64(st.Bavail) * int64(st.Bsize)
}

func memAvailable() int64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Fields(l); len(f) == 3 && f[0] == "MemAvailable:" && f[2] == "kB" {
			n, err := strconv.ParseInt(f[1], 10, 64)
			if err == nil {
				return n * 1024
			}
		}
	}
	return 0
}

// clockSynced reads the kernel clock state (adjtimex with no mode: a read):
// not TIME_ERROR and STA_UNSYNC clear (chrony's rtcsync).
func clockSynced() bool {
	var tx syscall.Timex
	st, err := syscall.Adjtimex(&tx)
	return err == nil && st != 5 && tx.Status&0x40 == 0
}

// mountStage mounts the stage tmpfs (root, 0711, nosuid, nodev, noexec).
func mountStage(dir string, size int64) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o711); err != nil {
		return err
	}
	return syscall.Mount("uc-pxx-provision-stage", dir, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC,
		fmt.Sprintf("size=%d,mode=0711", size))
}

// unmountStage takes the stage tmpfs off.
func unmountStage(dir string) error { return syscall.Unmount(dir, 0) }

// liveUSB is the slot's USB link: uc-pxx-usb by rc-service, the receiver
// uc-pxx-xfer (its sha256 pinned by this program's release data).
type liveUSB struct{ rel *clone.ReleaseData }

func (u *liveUSB) Take() error { return liveExec([]string{RCService, "uc-pxx-usb", "stop"}, nil) }
func (u *liveUSB) Give() error { return liveExec([]string{RCService, "uc-pxx-usb", "start"}, nil) }

// checked: uc-pxx-xfer is a regular file of root that only root can write,
// with the pinned sha256.
func (u *liveUSB) checked() error {
	if u.rel == nil || !clone.IsSHA256(u.rel.XferSHA256) {
		return errors.New("this program pins no uc-pxx-xfer: it does not run")
	}
	fi, err := os.Lstat(XferBin)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.Mode().IsRegular() || !ok || st.Uid != 0 || fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s: not a regular file of root that only root can write", XferBin)
	}
	b, err := readRegular(XferBin, 64<<20)
	if err != nil {
		return err
	}
	s := sha256.Sum256(b)
	if got := hex.EncodeToString(s[:]); got != u.rel.XferSHA256 {
		return fmt.Errorf("%s has sha256 %s, this program pins %s: it does not run", XferBin, got, u.rel.XferSHA256)
	}
	return nil
}

func (u *liveUSB) Cleanup() error {
	if err := u.checked(); err != nil {
		return err
	}
	return liveExec([]string{XferBin, "cleanup", "--stage", handover.ProvisionStage}, nil)
}

func usbNetOn() bool {
	b, err := readRegular(USBConf, 64<<10)
	if err != nil {
		return false
	}
	on := false
	for _, l := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok && k == "UC_PXX_USB_NET" {
			on = strings.Trim(v, "\"'") == "on"
		}
	}
	return on
}

// xferArgv is the receiver's command line.
func xferArgv(a XferArgs, net bool) []string {
	args := []string{XferBin, "run", "--kind", "provision", "--stage", handover.ProvisionStage, "--info", handover.ProvisionInfo,
		"--progress", handover.ProvisionProgressFile, "--seq", strconv.Itoa(a.Seq)}
	for _, p := range a.Profiles {
		args = append(args, "--profile", p.Arg())
	}
	if a.Fixed != "" {
		args = append(args, "--fixed", a.Fixed)
	}
	if a.ConsoleKey {
		args = append(args, "--console-key")
	}
	if a.StatusOnly {
		args = append(args, "--status-only")
	}
	if net {
		args = append(args, "--net")
	}
	return args
}

func (u *liveUSB) Start(a XferArgs) (Proc, error) {
	if err := u.checked(); err != nil {
		return nil, err
	}
	argv := xferArgv(a, usbNetOn())
	c := exec.Command(argv[0], argv[1:]...)
	c.Env, c.Dir, c.Stdin = execEnv, "/", nil
	c.Stdout, c.Stderr = os.Stderr, os.Stderr // the service's log
	if err := c.Start(); err != nil {
		return nil, err
	}
	p := &liveProc{c: c, done: make(chan struct{})}
	go func() { p.err = c.Wait(); close(p.done) }()
	return p, nil
}

func (u *liveUSB) UID() (int, error) {
	x, err := user.Lookup(XferUser)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(x.Uid)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("user %s: uid %q", XferUser, x.Uid)
	}
	return n, nil
}

func (u *liveUSB) NoProcessOf(uid int) error { return handover.NoProcessOf(uid) }

type liveProc struct {
	c    *exec.Cmd
	done chan struct{}
	err  error
	once sync.Once
}

func (p *liveProc) Done() <-chan struct{} { return p.done }

func (p *liveProc) Stop() error {
	p.once.Do(func() { p.c.Process.Signal(syscall.SIGTERM) })
	select {
	case <-p.done:
	case <-time.After(15 * time.Second):
		p.c.Process.Kill()
		<-p.done
		return errors.New("uc-pxx-xfer did not end on SIGTERM in 15 s (killed)")
	}
	return nil
}
