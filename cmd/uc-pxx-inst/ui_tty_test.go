//go:build !install

package main

// The status UI on its screen terminal (uc-pxx-inst ui --tty, as tty1 of the
// status image) turns the echo off, as uc-pxx-inst flow does: a touch key that
// the console keyboard turns into F14 (ESC [ 26 ~) is not echoed, so it no
// longer scrolls the page (owner feedback R3 item 2). The binary is the
// status build of loader_test.go's TestMain; the terminal is a pty.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func openPTY(t *testing.T) (*os.File, string) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pty: %v", err)
	}
	t.Cleanup(func() { m.Close() })
	var n uint32
	unlock := int32(0)
	var e syscall.Errno
	rc, err := m.SyscallConn()
	if err == nil {
		err = rc.Control(func(fd uintptr) {
			if _, _, e = syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); e == 0 {
				_, _, e = syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n)))
			}
		})
	}
	if err != nil || e != 0 {
		t.Skipf("pty: %v %v", err, e)
	}
	return m, fmt.Sprintf("/dev/pts/%d", n)
}

// readFor collects what the master side reads for d.
func readFor(m *os.File, d time.Duration) string {
	var got bytes.Buffer
	buf := make([]byte, 4096)
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		m.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, _ := m.Read(buf)
		got.Write(buf[:n])
	}
	return got.String()
}

func TestUITTYNoEcho(t *testing.T) {
	m, slave := openPTY(t)
	// the slave starts in the normal mode: it echoes (the cause)
	s, err := os.OpenFile(slave, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("pty slave: %v", err)
	}
	m.Write([]byte("\x1b[26~"))
	if got := readFor(m, 300*time.Millisecond); !strings.Contains(got, "^[[26~") {
		t.Fatalf("a terminal in its normal mode echoes %q (the test expects the echo of the cause)", got)
	}
	c := exec.Command(statusBin, "ui", "--tty", slave, "--no-reboot", "--root", unitRoot(t, true))
	var errb bytes.Buffer
	c.Stderr = &errb
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { c.Process.Kill(); c.Wait(); s.Close() }()
	if got := readFor(m, 1500*time.Millisecond); !strings.Contains(got, "Link and keys") {
		t.Fatalf("the UI drew no frame on the tty: %q %s", got, errb.String())
	}
	m.Write([]byte("\x1b[26~\x1b[25~"))
	if got := readFor(m, 2500*time.Millisecond); strings.Contains(got, "^[[2") || strings.Contains(got, "\x1b[26~") {
		t.Fatalf("the status UI's tty echoes the touch keys: %q", got)
	}
}
