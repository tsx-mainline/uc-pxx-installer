package flowio

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"uc-pxx-installer/internal/flow"
)

// A child of the flow (the plan or the install of the device mode) runs
// under a supervisor: `p10inst flow-supervise --dir DIR -- ARGV...`, started
// in its own session. The supervisor starts the child with its output in
// DIR/output.txt, waits for it and writes DIR/end.json. So the end of an
// install is known even when the UI process died in between (busybox starts
// the UI again, and the new UI reads DIR). The supervisor ignores the
// signals of a terminal; it never signals the child.

// endRecord is DIR/end.json.
type endRecord struct {
	Exited bool   `json:"exited"`
	Code   int    `json:"code"`
	Signal string `json:"signal,omitempty"`
	Err    string `json:"error,omitempty"`
}

// SuperviseMain is the supervisor process. It returns its exit status.
func SuperviseMain(dir string, argv []string) int {
	signal.Ignore(syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGPIPE)
	if len(argv) == 0 || !filepath.IsAbs(dir) {
		fmt.Fprintln(os.Stderr, "flow-supervise: give --dir ABS -- ARGV...")
		return 2
	}
	if err := superviseMain(dir, argv); err != nil {
		fmt.Fprintln(os.Stderr, "flow-supervise:", err)
		return 1
	}
	return 0
}

func superviseMain(dir string, argv []string) error {
	if err := writeRunFile(dir, filepath.Join(dir, "supervisor.pid"), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		return err
	}
	out, err := os.OpenFile(filepath.Join(dir, "output.txt"), os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	for _, k := range []string{"UC_PXX_BOOTCTL_EXEC", "TMPDIR"} { // devtest children only read these
		if v, ok := os.LookupEnv(k); ok {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	rec := endRecord{}
	if err := cmd.Start(); err != nil {
		rec.Err = err.Error()
		b, _ := json.Marshal(rec)
		return writeRunFile(dir, filepath.Join(dir, "end.json"), append(b, '\n'), 0o644)
	}
	writeRunFile(dir, filepath.Join(dir, "child.pid"), []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644)
	werr := cmd.Wait()
	var ee *exec.ExitError
	switch {
	case werr == nil:
		rec.Exited = true
	case errors.As(werr, &ee):
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			rec.Signal = ws.Signal().String()
		} else {
			rec.Exited, rec.Code = true, ee.ExitCode()
		}
	default:
		rec.Err = werr.Error()
	}
	b, _ := json.Marshal(rec)
	return writeRunFile(dir, filepath.Join(dir, "end.json"), append(b, '\n'), 0o644)
}

// child is a supervised child as the UI sees it.
type child struct {
	kind string // "plan" or "install"
	dir  string
	off  int64 // bytes of output.txt read so far
	part string
}

// startSupervised starts the supervisor of one child in its own session.
func startSupervised(c *Config, kind, dir string, argv []string) (*child, error) {
	if kind != "plan" && kind != "install" && kind != "repair" && kind != "switch" {
		return nil, fmt.Errorf("unknown child %q", kind)
	}
	if len(argv) < 2 || argv[0] != c.Exe || argv[1] != kind {
		return nil, fmt.Errorf("the %s child must be %s %s ...", kind, c.Exe, kind)
	}
	args := append([]string{"flow-supervise", "--dir", dir, "--"}, argv...)
	cmd := exec.Command(c.Exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Dir = "/"
	cmd.Env = append([]string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}, c.ChildEnv...)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go cmd.Wait() // reap it; if this process dies first, init does
	return &child{kind: kind, dir: dir}, nil
}

// end reads DIR/end.json; ok is false while the child runs.
func (ch *child) end() (flow.End, bool) {
	b, err := os.ReadFile(filepath.Join(ch.dir, "end.json"))
	if err != nil {
		return flow.End{}, false
	}
	var rec endRecord
	if json.Unmarshal(b, &rec) != nil {
		return flow.End{Lost: true}, true
	}
	if rec.Err != "" {
		return flow.End{Exited: true, Code: 2}, true
	}
	return flow.End{Exited: rec.Exited, Code: rec.Code, Signal: rec.Signal}, true
}

// alive reports whether the supervisor of the child still runs.
func (ch *child) alive(proc string) bool {
	b, err := os.ReadFile(filepath.Join(ch.dir, "supervisor.pid"))
	if err != nil {
		return true // not written yet
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 1 {
		return false
	}
	cl, err := os.ReadFile(filepath.Join(proc, strconv.Itoa(pid), "cmdline"))
	return err == nil && strings.Contains(string(cl), "flow-supervise")
}

// lines returns the complete new lines of the child's output.
func (ch *child) lines() []string {
	f, err := os.Open(filepath.Join(ch.dir, "output.txt"))
	if err != nil {
		return nil
	}
	defer f.Close()
	buf := make([]byte, 64<<10)
	var out []string
	for {
		n, _ := f.ReadAt(buf, ch.off)
		if n == 0 {
			break
		}
		ch.off += int64(n)
		s := ch.part + string(buf[:n])
		parts := strings.Split(s, "\n")
		ch.part = parts[len(parts)-1]
		out = append(out, parts[:len(parts)-1]...)
		if n < len(buf) {
			break
		}
	}
	return out
}

// output is the whole output of the child.
func (ch *child) output() []byte {
	b, _ := os.ReadFile(filepath.Join(ch.dir, "output.txt"))
	return b
}

// cancelPlan stops a plan child (it writes nothing) with SIGTERM. It never
// signals an install child: the flow has no way to stop an install.
func cancelPlan(ch *child) error {
	if ch == nil || ch.kind != "plan" {
		return errors.New("only a plan child can be stopped")
	}
	b, err := os.ReadFile(filepath.Join(ch.dir, "child.pid"))
	if err != nil {
		return err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 1 {
		return fmt.Errorf("bad child pid %q", b)
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}
