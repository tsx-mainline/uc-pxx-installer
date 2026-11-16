//go:build install && step2

package step2

// The thaw guard of the clone's freeze as a separate process (clone review
// MINOR-2): the executor's guard (install.RunClone: a timer and the thaw on
// every error) lives in the step-2 process, so a kill of that process (OOM,
// SIGKILL) or three failed thaws would leave slot B's root frozen until a
// power cycle. The helper `uc-pxx-step2 thaw-guard` is started before the
// freeze, opens its own fd of "/", and thaws "/" itself (FITHAW) when:
//
//   - its parent goes (the control pipe ends without "thawed": the kernel
//     closes the parent's end when the parent dies, whatever kills it);
//   - the freeze lasts longer than ThawGuardBound (clone.FreezeBound + 5 s:
//     the executor's own guard thaws at the plan's bound, at most
//     clone.FreezeBound).
//
// It never writes to "/" (no output, no log: a write would wait on the
// frozen file system before the thaw), it is in its own process group (a
// signal to the service's group does not reach it), and it ends after its
// one job. An FITHAW of a root that is not frozen (EINVAL) is no error: the
// parent thawed it, or never froze it.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	ossignal "os/signal"
	"strings"
	"syscall"
	"time"

	"uc-pxx-installer/internal/clone"
)

// ThawGuardCmd is the subcommand of uc-pxx-step2 that runs the helper.
const ThawGuardCmd = "thaw-guard"

// ThawGuardBound is how long the helper lets "/" stay frozen.
const ThawGuardBound = clone.FreezeBound + 5*time.Second

// The helper's exit codes.
const (
	guardIdle    = 0 // the parent said "thawed" (or never froze): nothing done
	guardOrphan  = 3 // the parent went without "thawed": the helper thawed
	guardTimeout = 4 // the freeze lasted ThawGuardBound: the helper thawed
	guardFailed  = 5 // the helper's own thaws failed
)

// thawGuard is the helper's loop: ctl gives the parent's lines ("frozen",
// "thawed") and ends when the parent goes; ready gets "ready" once the helper
// holds its fd; thaw is FITHAW of that fd (nil error: thawed; errNotFrozen:
// the root was not frozen).
func thawGuard(ctl io.Reader, ready io.Writer, bound time.Duration, thaw func() error) int {
	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(ctl)
		for sc.Scan() {
			lines <- strings.TrimSpace(sc.Text())
		}
		close(lines)
	}()
	if _, err := io.WriteString(ready, "ready\n"); err != nil {
		return doThaw(thaw, guardOrphan)
	}
	var timeout <-chan time.Time
	for {
		select {
		case l, ok := <-lines:
			switch {
			case !ok:
				return doThaw(thaw, guardOrphan)
			case l == "frozen" && timeout == nil:
				t := time.NewTimer(bound)
				defer t.Stop()
				timeout = t.C
			case l == "thawed":
				return guardIdle
			}
		case <-timeout:
			return doThaw(thaw, guardTimeout)
		}
	}
}

var errNotFrozen = errors.New("the root is not frozen")

// doThaw thaws, up to 10 times 200 ms apart; not frozen counts as done.
func doThaw(thaw func() error, code int) int {
	for i := 0; i < 10; i++ {
		err := thaw()
		switch {
		case err == nil:
			return code
		case errors.Is(err, errNotFrozen):
			return guardIdle
		}
		time.Sleep(200 * time.Millisecond)
	}
	return guardFailed
}

// RunThawGuard is `uc-pxx-step2 thaw-guard`: fd 3 is the control pipe (from
// the parent), fd 4 the ready pipe (to the parent). Started only by the
// parent's freezeRoot (startThawGuard).
func RunThawGuard() int {
	ossignal.Ignore(syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGPIPE)
	ctl, ready := os.NewFile(3, "control"), os.NewFile(4, "ready")
	if ctl == nil || ready == nil {
		return 2
	}
	root, err := os.Open("/")
	if err != nil {
		return 2
	}
	code := thawGuard(ctl, ready, ThawGuardBound, func() error { return thawFS(root) })
	ready.Close()
	return code
}

// thawFS is FITHAW of the file system of f.
func thawFS(f *os.File) error {
	_, _, en := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), fithaw, 0)
	switch en {
	case 0:
		return nil
	case syscall.EINVAL:
		return errNotFrozen
	}
	return fmt.Errorf("FITHAW /: %v", en)
}

// thawGuardCommand is the helper's command (a variable for the tests).
var thawGuardCommand = func() (*exec.Cmd, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return exec.Command(self, ThawGuardCmd), nil
}

// guardProc is the parent's side of a running helper.
type guardProc struct {
	c    *exec.Cmd
	ctl  *os.File
	done chan error
}

// startThawGuard starts the helper and waits (at most 5 s) for its ready
// line: it holds its fd of "/" and reads the control pipe. No freeze
// without it.
func startThawGuard() (*guardProc, error) {
	c, err := thawGuardCommand()
	if err != nil {
		return nil, err
	}
	ctlR, ctlW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	rdyR, rdyW, err := os.Pipe()
	if err != nil {
		ctlR.Close()
		ctlW.Close()
		return nil, err
	}
	c.ExtraFiles = []*os.File{ctlR, rdyW}
	c.Stdin, c.Stdout, c.Stderr = nil, nil, nil // no output: a write to "/" would wait on the freeze
	c.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	c.Dir = "/"
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // not in the service's group; no death signal: it must outlive its parent
	if err := c.Start(); err != nil {
		ctlR.Close()
		ctlW.Close()
		rdyR.Close()
		rdyW.Close()
		return nil, err
	}
	ctlR.Close()
	rdyW.Close()
	g := &guardProc{c: c, ctl: ctlW, done: make(chan error, 1)}
	go func() { g.done <- c.Wait() }()
	got := make(chan string, 1)
	go func() {
		l, _ := bufio.NewReader(rdyR).ReadString('\n')
		got <- l
		rdyR.Close()
	}()
	select {
	case l := <-got:
		if l == "ready\n" {
			return g, nil
		}
		g.end(false)
		return nil, fmt.Errorf("the thaw guard did not start (%q)", l)
	case <-time.After(5 * time.Second):
		g.end(false)
		return nil, errors.New("the thaw guard did not start in 5 s")
	}
}

// frozen tells the helper that "/" is frozen now (its bound starts).
func (g *guardProc) frozen() error {
	_, err := io.WriteString(g.ctl, "frozen\n")
	return err
}

// end ends the helper: with thawed, "thawed" first (the parent thawed "/":
// the helper does nothing); without, the pipe only ends (the helper thaws
// itself). It waits up to 5 s for the helper and gives its exit code (-1:
// unknown).
func (g *guardProc) end(thawed bool) int {
	if thawed {
		io.WriteString(g.ctl, "thawed\n")
	}
	g.ctl.Close()
	select {
	case err := <-g.done:
		var x *exec.ExitError
		if errors.As(err, &x) {
			return x.ExitCode()
		}
		if err == nil {
			return 0
		}
	case <-time.After(5 * time.Second):
	}
	return -1
}
