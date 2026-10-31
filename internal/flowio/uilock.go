package flowio

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// One flow UI at a time (owner, 2026-10-30). Two UIs on one run folder
// would both act on the same state: both could follow one install and
// write its bundle, or both could start a child. So `p10inst flow` takes
// an exclusive flock(2) on UILockFile before it reads the state of an
// earlier UI, and holds it until the process ends.
//
// A lock of flock(2) belongs to the open file. The kernel drops it when the
// last descriptor of that file is closed, and so when the process dies in
// any way (a crash, SIGKILL). So:
//   - a UI that crashed leaves no stale lock: the UI that busybox starts
//     again takes the lock at once and follows the install (FLOW.md §2.1);
//   - the lock never passes to a child: Go opens every file with
//     O_CLOEXEC, and the supervisor and the device mode get only their
//     output file (TestUILockCrash runs the real supervisor);
//   - the bytes in the file (pid, terminal, time) only say who has the
//     lock; they are not the lock. Old bytes of a UI that died block
//     nothing.
//
// The file is never removed or replaced while a UI runs: a new file at the
// path would let a second UI lock it. After the flock the UI checks that
// the path is still the file that it locked.

// UILockFile is the lock file of the flow UI (in Root).
const UILockFile = FlowDir + "/ui.lock"

// lockTestHook runs between the open and the flock (tests only: it
// replaces the file to check the same-file check). nil on the unit.
var lockTestHook func(path string)

// ErrUIBusy is the error of TakeUILock while another flow UI runs.
var ErrUIBusy = errors.New("another flow UI runs")

// errReplaced: the path was removed or replaced between the open and the
// flock (TakeUILock tries again).
var errReplaced = errors.New("the lock file was replaced while it was locked")

// Holder is what the lock file says about the UI that has the lock.
type Holder struct {
	PID   int
	TTY   string
	Since string
}

func (h Holder) String() string {
	if h.PID == 0 {
		return "the lock file does not say which"
	}
	return fmt.Sprintf("pid %d on %s since %s", h.PID, h.TTY, h.Since)
}

// UILock is the held lock. Close releases it; the end of the process
// releases it too.
type UILock struct{ f *os.File }

// Close releases the lock.
func (l *UILock) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// TakeUILock tries once to take the lock of the flow UI under root. tty is
// the terminal of this UI (written into the lock file for the message of
// the next UI). While another UI has the lock it returns ErrUIBusy and
// what the lock file says about that UI.
func TakeUILock(root, tty string) (*UILock, Holder, error) { return takeLockAt(root, UILockFile, tty) }

// TakeInputLock tries once to take the lock of the remote input (one
// `p10inst flow --stdin --take-over` at a time, remote.go).
func TakeInputLock(root, tty string) (*UILock, Holder, error) {
	return takeLockAt(root, InputLockFile, tty)
}

func takeLockAt(root, file, tty string) (*UILock, Holder, error) {
	base, path := filepath.Join(root, RunDir), filepath.Join(root, file)
	if root == "" || root == "/" {
		base, path = RunDir, file
	}
	f, err := takeUILock(base, path, tty, os.Getpid(), time.Now())
	for i := 0; i < 3 && errors.Is(err, errReplaced); i++ {
		f, err = takeUILock(base, path, tty, os.Getpid(), time.Now())
	}
	if errors.Is(err, ErrUIBusy) {
		return nil, ReadUIHolder(path), err
	}
	if err != nil {
		return nil, Holder{}, err
	}
	return &UILock{f: f}, Holder{}, nil
}

// WaitUILock waits until the lock is free and takes it. waiting is called
// with the holder each time the holder changes (for a message on the
// screen). It returns an error other than ErrUIBusy at once.
func WaitUILock(root, tty string, poll time.Duration, waiting func(Holder)) (*UILock, error) {
	last := Holder{PID: -1}
	for {
		l, h, err := TakeUILock(root, tty)
		if err == nil {
			return l, nil
		}
		if !errors.Is(err, ErrUIBusy) {
			return nil, err
		}
		if h != last && waiting != nil {
			waiting(h)
		}
		last = h
		time.Sleep(poll)
	}
}

// UIHolder is what the flow lock file under root says (read only).
func UIHolder(root string) Holder {
	_, path := rootPath(root, UILockFile)
	return ReadUIHolder(path)
}

// ReadUIHolder reads the lock file (a hint only: see above).
func ReadUIHolder(path string) Holder {
	var h Holder
	b, err := os.ReadFile(path)
	if err != nil {
		return h
	}
	for _, l := range strings.Split(string(b), "\n") {
		k, v, _ := strings.Cut(l, "=")
		switch k {
		case "pid":
			h.PID, _ = strconv.Atoi(v)
		case "tty":
			h.TTY = v
		case "since":
			h.Since = v
		}
	}
	return h
}

// takeUILock opens (or makes) the lock file inside base, takes the flock
// without waiting, checks that the path is still that file, and writes
// pid, terminal and time into it. It is the only function that writes the
// lock file (ci/goallow).
func takeUILock(base, path, tty string, pid int, now time.Time) (*os.File, error) {
	if !inside(base, path) || underKernelFS(path) || !strings.Contains(filepath.Clean(base), "/run/") && !strings.HasSuffix(filepath.Clean(base), "/run") {
		return nil, fmt.Errorf("%s is not a file of the run folder %s", path, base)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return nil, err
	}
	if lockTestHook != nil {
		lockTestHook(path)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrUIBusy
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	st, err1 := f.Stat()
	now2, err2 := os.Lstat(path)
	if err1 != nil || err2 != nil || !os.SameFile(st, now2) {
		f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, errReplaced)
	}
	info := fmt.Sprintf("pid=%d\ntty=%s\nsince=%s\n", pid, tty, now.UTC().Format(time.RFC3339))
	if err := f.Truncate(0); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.WriteAt([]byte(info), 0); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
