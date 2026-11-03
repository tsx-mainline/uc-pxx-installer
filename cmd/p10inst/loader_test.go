//go:build !install

package main

// The restart into the download mode of the status build: `p10inst loader`
// and the page "Download mode" of `p10inst ui`, run as the real status
// binary on a made-up root. Every root here is a test root, and the restart
// runs only with the root "/" (loaderRestart), so no test can restart the
// host. ci/lint.sh runs these tests in the default build.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var statusBin string // the default (status) build of p10inst

func TestMain(m *testing.M) {
	d, err := os.MkdirTemp("", "p10inst-status-bin-")
	if err != nil {
		panic(err)
	}
	statusBin = filepath.Join(d, "p10inst")
	out, err := exec.Command("go", "build", "-o", statusBin, ".").CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "go build: %v\n%s", err, out)
		os.Exit(2)
	}
	rc := m.Run()
	os.RemoveAll(d)
	os.Exit(rc)
}

func cell(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

// unitRoot: the files of the status RAM image on a UC-P10 that the checks
// read (as in internal/rebootmode's test). ok=false: no mode-loader.
func unitRoot(t *testing.T, ok bool) string {
	root := t.TempDir()
	w := func(rel string, b []byte) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w("etc/p10inst/ram-image", []byte("p10inst installer RAM image\n"))
	w("proc/cmdline", []byte("console=ttyS2,115200n8 p10inst.ram=1 uc-pxx.id=p10inst-e13d74\n"))
	w("proc/mounts", []byte("rootfs / rootfs rw 0 0\n"))
	w("proc/device-tree/compatible", []byte("crestron,uc-p10\x00rockchip,rk3399\x00"))
	w("proc/device-tree/model", []byte("Crestron UC-P10\x00"))
	g := "proc/device-tree/syscon@ff320000/"
	w(g+"compatible", []byte("rockchip,rk3399-pmugrf\x00syscon\x00simple-mfd\x00"))
	w(g+"reboot-mode/compatible", []byte("syscon-reboot-mode\x00"))
	w(g+"reboot-mode/offset", cell(0x300))
	if ok {
		w(g+"reboot-mode/mode-loader", cell(0x5242c301))
	}
	dev := filepath.Join(root, "sys/devices/platform/ff320000.syscon/ff320000.syscon:reboot-mode")
	os.MkdirAll(dev, 0o755)
	os.Symlink("../../../../firmware/devicetree/base/syscon@ff320000/reboot-mode", filepath.Join(dev, "of_node"))
	drv := filepath.Join(root, "sys/bus/platform/drivers/syscon-reboot-mode")
	os.MkdirAll(drv, 0o755)
	os.Symlink(dev, filepath.Join(drv, "ff320000.syscon:reboot-mode"))
	return root
}

func run(t *testing.T, stdin string, args ...string) (string, int) {
	t.Helper()
	c := exec.Command(statusBin, args...)
	c.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	err := c.Run()
	rc := 0
	if e, ok := err.(*exec.ExitError); ok {
		rc = e.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), rc
}

func TestLoaderCheck(t *testing.T) {
	out, rc := run(t, "", "loader", "--check", "--root", unitRoot(t, true))
	if rc != 0 || !strings.Contains(out, "loader: ok: device tree: syscon@ff320000/reboot-mode, offset 0x300, mode-loader = 0x5242c301") ||
		!strings.Contains(out, "RESULT: a restart writes 0x5242c301 (\"loader\") to the boot-mode register 0xff320300") {
		t.Fatalf("rc %d\n%s", rc, out)
	}
	out, rc = run(t, "", "loader", "--check", "--root", unitRoot(t, false))
	if rc != 1 || !strings.Contains(out, "refused: the reboot-mode node has mode-loader") || !strings.Contains(out, "RESULT: refused") {
		t.Fatalf("rc %d\n%s", rc, out)
	}
}

func TestLoaderUsage(t *testing.T) {
	for _, a := range [][]string{{"loader"}, {"loader", "--check", "--yes"}, {"loader", "--check", "x"}} {
		if out, rc := run(t, "", a...); rc != 2 || !strings.Contains(out, "p10inst loader --check") {
			t.Errorf("%v: rc %d\n%s", a, rc, out)
		}
	}
}

// --yes with a test root never restarts: a refused check is exit 1, a check
// that passes on a test root is exit 2 (loaderRestart: "/" only).
func TestLoaderYesTestRoot(t *testing.T) {
	out, rc := run(t, "", "loader", "--yes", "--root", unitRoot(t, false))
	if rc != 1 || !strings.Contains(out, "Nothing was written") {
		t.Fatalf("rc %d\n%s", rc, out)
	}
	out, rc = run(t, "", "loader", "--yes", "--root", unitRoot(t, true))
	if rc != 2 || !strings.Contains(out, "a test root (--root): the checks only, no restart") {
		t.Fatalf("rc %d\n%s", rc, out)
	}
}

// uiRun drives `p10inst ui --stdin --no-reboot` with the keys, one write each
// with a pause, then q.
func uiRun(t *testing.T, root string, keys ...string) string {
	t.Helper()
	c := exec.Command(statusBin, "ui", "--stdin", "--no-reboot", "--root", root)
	in, err := c.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, io.Discard
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	for _, k := range append(keys, "q") {
		time.Sleep(300 * time.Millisecond)
		io.WriteString(in, k)
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		c.Process.Kill()
		t.Fatal("the UI did not end")
	}
	return out.String()
}

func TestUILoaderPage(t *testing.T) {
	// Down x3 = Download mode, Enter, y
	out := uiRun(t, unitRoot(t, true), "jjj", "\r", "y")
	if !strings.Contains(out, "[ DOWNLOAD MODE ]") || !strings.Contains(out, "Restart into the download mode") ||
		!strings.Contains(out, "Press y to restart into the download mode now") ||
		!strings.Contains(out, "restart into the download mode (test: no reboot)") {
		t.Fatalf("no download-mode restart:\n%q", out)
	}
	if strings.Contains(out, "restart (test: no reboot)") {
		t.Fatal("the plain restart ran")
	}
}

func TestUILoaderRefused(t *testing.T) {
	out := uiRun(t, unitRoot(t, false), "jjj", "\r", "y")
	if !strings.Contains(out, "refused: the reboot-mode node has mode-loader") || !strings.Contains(out, "Not possible here") {
		t.Fatalf("no refusal on the page:\n%q", out)
	}
	if strings.Contains(out, "test: no reboot") {
		t.Fatal("a restart was asked although the check refused")
	}
}

// Without --no-reboot and on a test root, the hold reaches loaderFromUI,
// which refuses (no restart), and the UI shows why and goes on.
func TestUILoaderTestRootNoRestart(t *testing.T) {
	root := unitRoot(t, true)
	c := exec.Command(statusBin, "ui", "--stdin", "--root", root)
	in, _ := c.StdinPipe()
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, io.Discard
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"jjj", "\r", "y", "q"} {
		time.Sleep(300 * time.Millisecond)
		io.WriteString(in, k)
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		c.Process.Kill()
		t.Fatal("the UI did not end")
	}
	if !strings.Contains(out.String(), "No restart: a test root (--root): the checks only, no restart") {
		t.Fatalf("no refusal message:\n%q", out.String())
	}
}
