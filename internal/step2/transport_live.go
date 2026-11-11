//go:build install && step2

package step2

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/xfer/handover"
)

const (
	// XferBin is uc-pxx-xfer in every both-slot root file system (the USB
	// set: /usr/libexec/uc-pxx-xfer, root, 0755, so that the receiver's user
	// can run it: review STEP2-USB-REVIEW F2).
	XferBin = "/usr/libexec/uc-pxx-xfer/uc-pxx-xfer"
	// XferUser is the receiver's user (the USB set's passwd entry).
	XferUser = "uc-pxx-xfer"
)

// LiveTransport is the transport of a unit: rc-service for uc-pxx-usb, the
// stage tmpfs, and uc-pxx-xfer from XferBin, whose sha256 must be the pin of
// this binary's gate (the USB set) before it runs as root.
func LiveTransport() Transport { return liveTransport{} }

type liveTransport struct{}

func (liveTransport) Take() error { return exec.Command(rcService, "uc-pxx-usb", "stop").Run() }
func (liveTransport) Give() error { return exec.Command(rcService, "uc-pxx-usb", "start").Run() }

func (liveTransport) MountStage() error {
	if err := os.MkdirAll(filepath.Dir(StageDir), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(StageDir, 0o711); err != nil {
		return err
	}
	opts := fmt.Sprintf("size=%d,mode=0711", StageSize)
	return syscall.Mount("uc-pxx-step2-stage", StageDir, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, opts)
}

func (liveTransport) UnmountStage() error { return syscall.Unmount(StageDir, 0) }

func (liveTransport) UID() (int, error) {
	u, err := user.Lookup(XferUser)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(u.Uid)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("user %s: uid %q", XferUser, u.Uid)
	}
	return n, nil
}

func (liveTransport) NoProcessOf(uid int) error { return handover.NoProcessOf(uid) }

// xferChecked: XferBin is a regular file of root, not writable by others, with
// the sha256 that the gate's USB set pins.
func xferChecked() error {
	fi, err := os.Lstat(XferBin)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() || !rootOwned(fi) || fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s: not a regular file of root that only root can write", XferBin)
	}
	b, err := os.ReadFile(XferBin)
	if err != nil {
		return err
	}
	s := sha256.Sum256(b)
	sum := hex.EncodeToString(s[:])
	for _, p := range mainline.EmbeddedBothSlot().Pins {
		if p.Path == XferBin {
			if p.SHA256 == sum {
				return nil
			}
			return fmt.Errorf("%s has sha256 %s, the USB set pins %s: it does not run", XferBin, sum, p.SHA256)
		}
	}
	return fmt.Errorf("the gate pins no %s", XferBin)
}

func (liveTransport) Cleanup() error {
	if err := xferChecked(); err != nil {
		return err
	}
	return exec.Command(XferBin, "cleanup", "--stage", StageDir).Run()
}

func (liveTransport) Start(statusOnly bool, pins []string) (Proc, error) {
	if err := xferChecked(); err != nil {
		return nil, err
	}
	args := []string{"run", "--info", XferInfo}
	if statusOnly {
		args = append(args, "--status-only")
	} else {
		args = append(args, "--stage", StageDir)
		for _, p := range pins {
			args = append(args, "--pin", p)
		}
	}
	c := exec.Command(XferBin, args...)
	c.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	c.Dir = "/"
	c.Stdout, c.Stderr = os.Stderr, os.Stderr // the service's log
	if err := c.Start(); err != nil {
		return nil, err
	}
	p := &liveProc{c: c, done: make(chan struct{})}
	go func() { p.err = c.Wait(); close(p.done) }()
	return p, nil
}

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
