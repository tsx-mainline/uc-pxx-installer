//go:build install

package device

// uc-pxx-bootctl-inst is the only writer of the A/B sector. uc-pxx-inst runs it
// with exactly these command forms: status, disable --slot T, arm-trying
// --slot T --lower-keeper (the install), and repair --keeper K
// --expect-sector HEX [--dry-run] (the repair of the hold, repair.go), and
// switch --to T --expect-sector HEX [--dry-run] (the slot switch). Before every run it reads the binary,
// checks its sha256 against the reviewed one, and runs a private copy of
// exactly those bytes. It never kills the tool: a write cut short is the
// worst case for the A/B sector.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"uc-pxx-installer/internal/install"
)

// ErrABUnknown means that uc-pxx-bootctl-inst did not finish with a known
// A/B sector (exit 3, a signal, or a sector that is neither the old nor the
// predicted one). The user must not power off or restart.
var ErrABUnknown = install.ErrABUnknown

const maxBootctlBytes = 8 << 20

// bootctlForm reports whether args is one of the forms the device mode
// runs, and whether it writes.
func bootctlForm(args []string) (ok, writes bool) {
	slot := func(s string) bool { return s == "a" || s == "b" }
	switch {
	case len(args) > 0 && (args[0] == "repair" || args[0] == "switch"):
		return repairForm(args)
	case len(args) == 1 && args[0] == "status":
		return true, false
	case len(args) == 3 && args[0] == "disable" && args[1] == "--slot" && slot(args[2]):
		return true, true
	case len(args) == 4 && args[0] == "arm-trying" && args[1] == "--slot" && slot(args[2]) && args[3] == "--lower-keeper":
		return true, true
	}
	return false, false
}

// BootctlBinary reads uc-pxx-bootctl-inst and checks its sha256. It returns
// the bytes and the sha256.
func (e *Env) BootctlBinary() ([]byte, string, error) {
	p := e.path(BootctlPath)
	st, err := os.Lstat(p)
	if err != nil {
		return nil, "", fmt.Errorf("uc-pxx-bootctl-inst: %w", err)
	}
	if !st.Mode().IsRegular() || st.Size() > maxBootctlBytes {
		return nil, "", fmt.Errorf("%s is not a regular file of at most %d bytes", BootctlPath, maxBootctlBytes)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, "", err
	}
	s := sha256.Sum256(b)
	sum := hex.EncodeToString(s[:])
	if sum != e.bootctlSHA {
		return nil, sum, fmt.Errorf("%s has sha256 %s, not the reviewed binary %s: refusing to run it", BootctlPath, sum, e.bootctlSHA)
	}
	return b, sum, nil
}

// Bootctl runs uc-pxx-bootctl-inst with one of the forms of bootctlForm. A write form
// needs the A/B lock, and runs with the read-only flag of the whole disk
// cleared (the tool writes LBA 49156 through /dev/mmcblk0); the flag is set
// again right after.
func (d *Disk) Bootctl(args []string) (*BootctlResult, error) {
	ok, writes := bootctlForm(args)
	if !ok {
		return nil, fmt.Errorf("uc-pxx-bootctl-inst %q is not a form the device mode runs", args)
	}
	bin, _, err := d.e.BootctlBinary()
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
		if err := d.e.setRO(d.diskNode(), d.Major, d.Minor, false); err != nil {
			return nil, err
		}
		defer d.e.setRO(d.diskNode(), d.Major, d.Minor, true)
	}
	return d.e.runner.Run(d.e, bin, args)
}

// execRunner runs a private copy of the checked bytes: a new 0700 directory
// under /run/uc-pxx-inst, the file written, read back and checked again, run, and
// removed. In a test environment with ChrootBootctl it runs the copy with
// chroot into the test root, so the tool's fixed paths (/dev/mmcblk0,
// /sys/class/block/mmcblk0) are those of the test root.
type execRunner struct{}

func (execRunner) Run(e *Env, bin []byte, args []string) (*BootctlResult, error) {
	base := e.path(runDir)
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(base, "bootctl-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	exe := filepath.Join(dir, "uc-pxx-bootctl-inst")
	if err := os.WriteFile(exe, bin, 0o700); err != nil {
		return nil, err
	}
	back, err := os.ReadFile(exe)
	if err != nil || !bytes.Equal(back, bin) {
		return nil, errors.New("the private copy of uc-pxx-bootctl-inst differs from the checked bytes")
	}
	var cmd *exec.Cmd
	if e.chroot {
		if !testBuild {
			return nil, ErrNotTestBuild
		}
		inside := "/" + strings.TrimPrefix(strings.TrimPrefix(exe, e.root), "/")
		cmd = exec.Command("/usr/sbin/chroot", append([]string{e.root, inside}, args...)...)
	} else {
		cmd = exec.Command(exe, args...)
	}
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	cmd.Dir = "/"
	// Its own session: no controlling terminal and its own process group, so
	// a ^C, a terminal hangup or a signal to uc-pxx-inst's group never reaches it.
	// Its output goes to files, not to pipes, so it never gets SIGPIPE when
	// uc-pxx-inst or its terminal is gone. stdin is /dev/null (exec's default).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	outF, err := os.OpenFile(filepath.Join(dir, "stdout"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	defer outF.Close()
	errF, err := os.OpenFile(filepath.Join(dir, "stderr"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	defer errF.Close()
	cmd.Stdout, cmd.Stderr = outF, errF
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	start := time.Now()
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case err = <-done:
			o, _ := os.ReadFile(outF.Name())
			e, _ := os.ReadFile(errF.Name())
			r := &BootctlResult{RC: 0, Stdout: string(o), Stderr: string(e)}
			if ee, ok := err.(*exec.ExitError); ok {
				r.RC = ee.ExitCode() // -1 when a signal ended it
				if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
					r.Signal = ws.Signal().String()
				}
			} else if err != nil {
				return r, err
			}
			return r, nil
		case <-tick.C:
			fmt.Fprintf(os.Stderr, "uc-pxx-inst: uc-pxx-bootctl-inst %s still runs after %s: do not power off, do not restart\n",
				strings.Join(args, " "), time.Since(start).Round(time.Second))
		}
	}
}
