//go:build install && step2

package step2

// The checks of the running system that are not of the eMMC (design P10,
// P16): tsx-autoupdate is quiet and its lock is held for the whole run; the
// network is up for the target's mark after the restart.

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"uc-pxx-installer/internal/device"
)

const (
	// AutoupdateLock is the lock of tsx-autoupdate (tsx-linux-common: one run
	// at a time, a non-blocking flock of $RUN/autoupdate.lock).
	AutoupdateLock = "/run/tsx/autoupdate.lock"
	// RestartBlock is tsx-autoupdate's "busy" or "hold N" file.
	RestartBlock = "/run/tsx/restart-block"
	// AutoupdateTool answers `get mode` (auto or manual) and sets the STOP
	// hold (`stop-hold set step2 REASON`, tsx-linux-common step2-stop-hold).
	AutoupdateTool = "/usr/local/sbin/tsx-autoupdate"
	// HoldsDir is tsx-autoupdate's durable hold folder: one file per owner
	// (STEP2-STOP-HOLD-2026-11-11.md §4). It survives a restart; the restart
	// block does not exist until `tsx-autoupdate boot` ran.
	HoldsDir = "/var/lib/tsx/autoupdate/holds"
	// StopHoldOwner is step 2's owner name of the hold.
	StopHoldOwner = "step2"
	// StopHoldSetMarker records in this boot (tmpfs) that `stop-hold set`
	// gave exit 0: it is never run again in this boot (a repeat would undo a
	// person's clear: STEP2-STOP-HOLD review MINOR-1).
	StopHoldSetMarker = "/run/uc-pxx-step2.stop-hold-set"
)

// SlotChanged are the markers of the slot-side updaters (an update of that
// slot is between its arm and its restart).
var SlotChanged = []string{"/run/uc-pxx-slot-a-changed", "/run/uc-pxx-slot-b-changed"}

// Live are the checks of the running system. Tests give their own.
type Live struct {
	// NetworkUp: eth0 or usb0 has an IPv4 address and sshd runs (P16).
	NetworkUp func() bool
	// LockAutoupdate takes the tsx-autoupdate lock (non-blocking) and
	// returns its release.
	LockAutoupdate func() (func(), error)
	// Quiet: tsx-autoupdate is in mode manual, no restart block or hold (also
	// no durable hold in HoldsDir), no slot-changed marker.
	Quiet func() error
	// SetStopHold runs `tsx-autoupdate stop-hold set step2 REASON` and gives
	// its exit code (-1: it did not run or end in 30 s). nil: not available
	// (the STOP keeps the lock and the "hold 4" block only).
	SetStopHold func(reason string) int
}

func (l *Live) setStopHold(reason string) int {
	if l.SetStopHold == nil {
		return -1
	}
	return l.SetStopHold(reason)
}

// HoldsClear fails closed (STEP2-STOP-HOLD review MAJOR-1): it refuses when
// tsx-autoupdate's hold folder has any entry but a dot file, or cannot be
// read (also when it is missing: a tsx-autoupdate without the durable hold
// is not the payload's). After a restart the restart block is gone until
// `tsx-autoupdate boot` ran, so the block alone would let step 2 write while
// the last boot's STOP is set.
func HoldsClear(e *device.Env) error {
	ents, err := os.ReadDir(e.Path(HoldsDir))
	if err != nil {
		return refuse("tsx-autoupdate's hold folder %s cannot be read (%v): step 2 waits", HoldsDir, err)
	}
	var held []string
	for _, d := range ents {
		if !strings.HasPrefix(d.Name(), ".") {
			held = append(held, d.Name())
		}
	}
	if len(held) > 0 {
		return refuse("tsx-autoupdate has a hold set (%s: %s): step 2 waits until a person checked the unit and cleared it",
			HoldsDir, strings.Join(held, ", "))
	}
	return nil
}

// runStopHoldSet is the default SetStopHold: the fixed argv, PATH and
// LC_ALL, 30 s. The reason is one line of at most 200 characters.
func runStopHoldSet(e *device.Env, reason string) int {
	cmd := exec.Command(e.Path(AutoupdateTool), "stop-hold", "set", StopHoldOwner, firstLine(reason))
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	if err := cmd.Start(); err != nil {
		return -1
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			return 0
		}
		var x *exec.ExitError
		if errors.As(err, &x) {
			return x.ExitCode()
		}
		return -1
	case <-time.After(30 * time.Second):
		cmd.Process.Kill()
		return -1
	}
}

func (l *Live) lockAutoupdate() (func(), error) {
	if l.LockAutoupdate == nil {
		return nil, refuse("no tsx-autoupdate lock check")
	}
	return l.LockAutoupdate()
}

func (l *Live) quiet() error {
	if l.Quiet == nil {
		return refuse("no tsx-autoupdate check")
	}
	return l.Quiet()
}

// LiveChecks are the checks of the unit, inside e's root.
func LiveChecks(e *device.Env) *Live {
	return &Live{
		NetworkUp: func() bool { return (ipv4Up("eth0") || ipv4Up("usb0")) && sshdRuns(e) },
		LockAutoupdate: func() (func(), error) {
			p := e.Path(AutoupdateLock)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return nil, refuse("%s: %v", AutoupdateLock, err)
			}
			f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_APPEND|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o644)
			if err != nil {
				return nil, refuse("%s: %v", AutoupdateLock, err)
			}
			if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
				f.Close()
				return nil, refuse("tsx-autoupdate runs now (%s is held): try again later", AutoupdateLock)
			}
			return func() { f.Close() }, nil
		},
		SetStopHold: func(reason string) int { return runStopHoldSet(e, reason) },
		Quiet: func() error {
			if err := HoldsClear(e); err != nil {
				return err
			}
			if _, err := os.Lstat(e.Path(RestartBlock)); err == nil {
				b, _ := os.ReadFile(e.Path(RestartBlock))
				return refuse("tsx-autoupdate has a restart block or a hold (%s: %q): step 2 waits until a person cleared it",
					RestartBlock, strings.TrimSpace(string(b)))
			}
			for _, m := range SlotChanged {
				if _, err := os.Lstat(e.Path(m)); err == nil {
					return refuse("%s exists: a slot update waits for its restart", m)
				}
			}
			cmd := exec.Command(e.Path(AutoupdateTool), "get", "mode")
			cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
			var out bytes.Buffer
			cmd.Stdout = &out
			done := make(chan error, 1)
			if err := cmd.Start(); err != nil {
				return refuse("tsx-autoupdate get mode: %v", err)
			}
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				if err != nil || strings.TrimSpace(out.String()) != "manual" {
					return refuse("tsx-autoupdate get mode gives %q (%v): step 2 runs only in mode manual", strings.TrimSpace(out.String()), err)
				}
			case <-time.After(30 * time.Second):
				cmd.Process.Kill()
				return refuse("tsx-autoupdate get mode did not answer in 30 s")
			}
			return nil
		},
	}
}

func ipv4Up(name string) bool {
	i, err := net.InterfaceByName(name)
	if err != nil || i.Flags&net.FlagUp == 0 {
		return false
	}
	as, err := i.Addrs()
	if err != nil {
		return false
	}
	for _, a := range as {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() {
			return true
		}
	}
	return false
}

// sshdRuns: a process named sshd (from /proc/N/comm).
func sshdRuns(e *device.Env) bool {
	ents, _ := os.ReadDir(e.Path("/proc"))
	for _, d := range ents {
		if _, err := strconv.Atoi(d.Name()); err != nil {
			continue
		}
		if b, err := os.ReadFile(e.Path(fmt.Sprintf("/proc/%s/comm", d.Name()))); err == nil && strings.TrimSpace(string(b)) == "sshd" {
			return true
		}
	}
	return false
}
