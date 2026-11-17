package provision

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
	"time"
)

// TestExecLimit: every rc-service call has a bound (RC-10b review MINOR-1);
// apk, the profile script and uc-pxx-xfer have none.
func TestExecLimit(t *testing.T) {
	for _, c := range []struct {
		argv []string
		want time.Duration
	}{
		{[]string{RCService, "kiosk", "start"}, RCCallLimit},
		{[]string{RCService, "tsx-mqtt", "status"}, RCStatusLimit},
		{[]string{RCService, "uc-pxx-usb", "stop"}, RCCallLimit},
		{[]string{RCService, "uc-pxx-usb", "start"}, RCCallLimit},
		{[]string{APKBin, "add", "--no-network"}, 0},
		{[]string{ShellBin, ApplyScript, "APPLY", "/"}, 0},
		{[]string{XferBin, "cleanup", "--stage", "x"}, 0},
	} {
		if got := execLimit(c.argv); got != c.want {
			t.Errorf("%q: %v, want %v", c.argv, got, c.want)
		}
	}
	if RCCallLimit <= 0 || RCStatusLimit <= 0 || RCStatusLimit > RCCallLimit || RCCallLimit > ServiceWait {
		t.Fatalf("limits %v %v (ServiceWait %v)", RCCallLimit, RCStatusLimit, ServiceWait)
	}
}

// TestRunBounded: a call that does not end is killed at its limit; a call
// that ends but leaves a process holding its output ends after the delay
// (an error with a limit; with no limit the exit status decides); a normal
// call gives its lines and its exit status.
func TestRunBounded(t *testing.T) {
	run := func(script string, limit, delay time.Duration) ([]string, error, time.Duration) {
		var lines []string
		t0 := time.Now()
		err := runBounded([]string{"/bin/sh", "-c", script}, limit, delay, func(l string) { lines = append(lines, l) })
		return lines, err, time.Since(t0)
	}
	t.Run("hangs: killed at the limit", func(t *testing.T) {
		l, err, d := run("echo begin; sleep 60", time.Second, time.Second)
		if err == nil || !strings.Contains(err.Error(), "did not end within 1s") || d > 15*time.Second {
			t.Fatalf("%v after %v", err, d)
		}
		if len(l) != 1 || l[0] != "begin" {
			t.Fatalf("lines %q", l)
		}
	})
	t.Run("a daemon keeps the output: the pipe is closed after the delay", func(t *testing.T) {
		l, err, d := run("sleep 60 & echo up; exit 0", 30*time.Second, time.Second)
		if err == nil || !strings.Contains(err.Error(), "holds its output open") || d > 15*time.Second {
			t.Fatalf("%v after %v", err, d)
		}
		if len(l) != 1 || l[0] != "up" {
			t.Fatalf("lines %q", l)
		}
	})
	t.Run("a normal call", func(t *testing.T) {
		l, err, _ := run("echo a; echo b >&2; exit 0", 30*time.Second, time.Second)
		if err != nil || strings.Join(l, ",") != "a,b" {
			t.Fatalf("%v %q", err, l)
		}
	})
	t.Run("an exit status is not a timeout", func(t *testing.T) {
		_, err, _ := run("exit 3", 30*time.Second, time.Second)
		if err == nil || !strings.Contains(err.Error(), "exit status 3") || strings.Contains(err.Error(), "within") {
			t.Fatalf("%v", err)
		}
	})
	t.Run("no limit: the call ends by itself", func(t *testing.T) {
		_, err, _ := run("sleep 1; exit 0", 0, 0)
		if err != nil {
			t.Fatalf("%v", err)
		}
	})
	// RC-10c review MINOR-1 (the reviewer's probe): the exact pair that
	// liveExec passes for apk (and the profile script), a child that keeps the
	// pipe 7 s after the exit. The exit status decides; the pipe is closed
	// after execWaitDelay, and the left process is one line.
	apk := []string{APKBin, "add", "--no-network", "--no-cache", "--repositories-file", RepositoriesFile, "x"}
	if execLimit(apk) != 0 || execLimit([]string{ShellBin, ApplyScript, "APPLY", "/"}) != 0 {
		t.Fatal("apk or the profile script has a limit")
	}
	t.Run("apk's pair: a left process does not fail a good run", func(t *testing.T) {
		l, err, d := run("sleep 7 & echo ok; exit 0", execLimit(apk), execWaitDelay)
		if err != nil || d >= 7*time.Second || d < execWaitDelay-time.Second {
			t.Fatalf("%v after %v", err, d)
		}
		if len(l) != 2 || l[0] != "ok" || !strings.Contains(l[1], "ended with exit status 0") || !strings.Contains(l[1], "held its output open") {
			t.Fatalf("lines %q", l)
		}
	})
	t.Run("apk's pair: the exit status still decides", func(t *testing.T) {
		_, err, d := run("sleep 7 & exit 3", execLimit(apk), execWaitDelay)
		if err == nil || !strings.Contains(err.Error(), "exit status 3") || d >= 7*time.Second {
			t.Fatalf("%v after %v", err, d)
		}
	})
	t.Run("rc-service's pair: a left process is an error", func(t *testing.T) {
		_, err, d := run("sleep 7 & exit 0", execLimit([]string{RCService, "kiosk", "start"}), execWaitDelay)
		if err == nil || !strings.Contains(err.Error(), "holds its output open") || d >= 7*time.Second {
			t.Fatalf("%v after %v", err, d)
		}
	})
}

// TestRunBoundedOnlyFromLiveExec: runBounded starts a process for any argv,
// so its one caller is liveExec, after liveExec's allow-list (the goallow
// rule lets both start a process; this keeps the allow-list in front).
func TestRunBoundedOnlyFromLiveExec(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	var callers []string
	for _, p := range pkgs {
		for _, f := range p.Files {
			for _, d := range f.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok {
					continue
				}
				ast.Inspect(fd, func(n ast.Node) bool {
					if id, ok := n.(*ast.Ident); ok && id.Name == "runBounded" && id.Pos() != fd.Name.Pos() {
						callers = append(callers, fd.Name.Name)
					}
					return true
				})
			}
		}
	}
	if strings.Join(callers, ",") != "liveExec" {
		t.Fatalf("runBounded is used in %q, want only liveExec", callers)
	}
}
