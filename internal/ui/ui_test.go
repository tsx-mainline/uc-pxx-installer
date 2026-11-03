package ui

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestMapCode(t *testing.T) {
	for code, k := range map[uint16]Key{114: KeyPrev, 115: KeyNext, 113: KeyOK, 202: KeyBack, 148: KeyNone, 149: KeyNone} {
		if MapCode(code) != k {
			t.Errorf("code %d", code)
		}
	}
}

func TestHoldToRestart(t *testing.T) {
	m := &Model{}
	t0 := time.Unix(1000, 0)
	m.Handle(Event{KeyNext, true}, t0)
	m.Handle(Event{KeyNext, true}, t0)
	if m.Sel != int(PageRestart) {
		t.Fatalf("sel %d", m.Sel)
	}
	m.Handle(Event{KeyOK, true}, t0)
	m.Tick(t0.Add(2 * time.Second))
	if m.Restart {
		t.Fatal("restart before 3 s")
	}
	// a release before 3 s cancels
	m.Handle(Event{KeyOK, false}, t0.Add(2*time.Second))
	m.Tick(t0.Add(4 * time.Second))
	if m.Restart {
		t.Fatal("restart after a short press")
	}
	m.Handle(Event{KeyOK, true}, t0.Add(5*time.Second))
	m.Tick(t0.Add(8 * time.Second))
	if !m.Restart {
		t.Fatal("no restart after a 3 s hold")
	}
}

func TestOKOnStatusDoesNothing(t *testing.T) {
	m := &Model{}
	t0 := time.Unix(1000, 0)
	m.Handle(Event{KeyOK, true}, t0)
	m.Tick(t0.Add(10 * time.Second))
	if m.Restart || m.Page != PageStatus {
		t.Fatal("OK on the status item restarted")
	}
	m.Handle(Event{KeyPrev, true}, t0) // wraps to Restart
	m.Handle(Event{KeyBack, true}, t0)
	if m.Sel != 0 || m.Page != PageStatus {
		t.Fatal("back does not return to the status")
	}
}

func TestTerminalConfirm(t *testing.T) {
	m := &Model{Terminal: true, Sel: int(PageRestart)}
	t0 := time.Unix(1000, 0)
	m.Handle(Event{KeyYes, true}, t0)
	if m.Restart {
		t.Fatal("y without Enter restarted")
	}
	m.Handle(Event{KeyOK, true}, t0)
	m.Handle(Event{KeyYes, true}, t0)
	if !m.Restart {
		t.Fatal("Enter, y did not restart")
	}
}

func TestDraw(t *testing.T) {
	m := &Model{Title: "title", Page: PageLog, Log: []string{strings.Repeat("x", 300)}}
	var b bytes.Buffer
	if err := m.Draw(&b, 120, 37, time.Now()); err != nil {
		t.Fatal(err)
	}
	rows := strings.Split(b.String(), "\r\n")
	if len(rows) != 37 {
		t.Fatalf("%d rows", len(rows))
	}
	if !strings.Contains(b.String(), "[ STATUS ]") || !strings.Contains(b.String(), Legend) {
		t.Error("no menu or legend")
	}
}

func TestParseTerm(t *testing.T) {
	ev := ParseTerm([]byte("\x1b[A\x1b[Bj\ry"))
	var keys []Key
	for _, e := range ev {
		if e.Down {
			keys = append(keys, e.Key)
		}
	}
	want := []Key{KeyPrev, KeyNext, KeyNext, KeyOK, KeyYes}
	if len(keys) != len(want) {
		t.Fatalf("%v", keys)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("%v", keys)
		}
	}
}

func TestFindTouch(t *testing.T) {
	root := t.TempDir()
	for ev, name := range map[string]string{"event0": "gpio-keys", "event1": "Atmel maXTouch Touchscreen"} {
		d := filepath.Join(root, "sys/class/input", ev, "device")
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "name"), []byte(name+"\n"), 0o644)
	}
	if got := FindTouch(root); got != filepath.Join(root, "dev/input/event1") {
		t.Error(got)
	}
}

func TestMenuWithoutLoader(t *testing.T) {
	m := &Model{}
	var b bytes.Buffer
	m.Draw(&b, 120, 37, time.Now())
	if strings.Contains(b.String(), "Download mode") {
		t.Fatal("the download mode is in the menu without Model.Loader (the install build)")
	}
	t0 := time.Unix(1000, 0)
	for i := 0; i < 3; i++ {
		m.Handle(Event{KeyNext, true}, t0)
	}
	if m.Sel != 0 {
		t.Fatalf("3 Next from Status: sel %d, want 0 (three items)", m.Sel)
	}
}

func loaderModel(ok bool) *Model {
	return &Model{Loader: &LoaderPage{OK: ok, Lines: []string{"ok: the installer RAM image"}}}
}

func TestHoldToLoader(t *testing.T) {
	m := loaderModel(true)
	t0 := time.Unix(1000, 0)
	m.Handle(Event{KeyPrev, true}, t0) // wraps to the last item
	if m.Sel != int(PageLoader) {
		t.Fatalf("sel %d", m.Sel)
	}
	m.Handle(Event{KeyOK, true}, t0)
	m.Tick(t0.Add(2900 * time.Millisecond))
	if m.RestartLoader || m.Restart {
		t.Fatal("restart before 3 s")
	}
	m.Handle(Event{KeyOK, false}, t0.Add(2900*time.Millisecond))
	m.Tick(t0.Add(5 * time.Second))
	if m.RestartLoader || m.Restart {
		t.Fatal("restart after a short press")
	}
	m.Handle(Event{KeyOK, true}, t0.Add(6*time.Second))
	var b bytes.Buffer
	m.Draw(&b, 120, 37, t0.Add(7*time.Second))
	if !strings.Contains(b.String(), "Keep holding OK: restart into the download mode in 2 s") {
		t.Fatalf("no countdown: %q", b.String())
	}
	m.Tick(t0.Add(9 * time.Second))
	if !m.RestartLoader || m.Restart {
		t.Fatalf("after the 3 s hold: loader %v, plain restart %v", m.RestartLoader, m.Restart)
	}
}

func TestLoaderRefusedNeverRestarts(t *testing.T) {
	m := loaderModel(false)
	m.Loader.Lines = append(m.Loader.Lines, "refused: no mode-loader")
	t0 := time.Unix(1000, 0)
	m.Sel = int(PageLoader)
	m.Handle(Event{KeyOK, true}, t0)
	m.Tick(t0.Add(10 * time.Second))
	if m.RestartLoader || m.Restart || m.Page != PageLoader {
		t.Fatal("a refused check restarted")
	}
	var b bytes.Buffer
	m.Draw(&b, 120, 37, t0)
	if !strings.Contains(b.String(), "refused: no mode-loader") || !strings.Contains(b.String(), "Not possible here") {
		t.Fatalf("%q", b.String())
	}
	mt := loaderModel(false)
	mt.Terminal, mt.Sel = true, int(PageLoader)
	mt.Handle(Event{KeyOK, true}, t0)
	mt.Handle(Event{KeyYes, true}, t0)
	if mt.RestartLoader || mt.Confirm {
		t.Fatal("a refused check restarted on the terminal")
	}
}

func TestTerminalConfirmLoader(t *testing.T) {
	m := loaderModel(true)
	m.Terminal, m.Sel = true, int(PageLoader)
	t0 := time.Unix(1000, 0)
	m.Handle(Event{KeyYes, true}, t0)
	if m.RestartLoader {
		t.Fatal("y without Enter")
	}
	m.Handle(Event{KeyOK, true}, t0)
	m.Handle(Event{KeyNext, true}, t0) // another key cancels
	m.Handle(Event{KeyYes, true}, t0)
	if m.RestartLoader {
		t.Fatal("y after a cancel")
	}
	m.Sel = int(PageLoader)
	m.Handle(Event{KeyOK, true}, t0)
	m.Handle(Event{KeyYes, true}, t0)
	if !m.RestartLoader || m.Restart {
		t.Fatal("Enter, y did not ask for the download mode")
	}
}

// The hold of the Restart page still gives the plain restart, also with the
// download-mode page in the menu, and its texts are those of before.
func TestRestartUnchangedWithLoader(t *testing.T) {
	m := loaderModel(true)
	m.Sel = int(PageRestart)
	t0 := time.Unix(1000, 0)
	m.Handle(Event{KeyOK, true}, t0)
	var b bytes.Buffer
	m.Draw(&b, 120, 37, t0.Add(time.Second))
	if !strings.Contains(b.String(), "Keep holding OK: restart in 2 s") {
		t.Fatalf("%q", b.String())
	}
	m.Tick(t0.Add(3 * time.Second))
	if !m.Restart || m.RestartLoader {
		t.Fatal("the Restart page")
	}
	m2 := &Model{Sel: int(PageRestart)}
	b.Reset()
	m2.Handle(Event{KeyOK, true}, t0)
	m2.Handle(Event{KeyOK, false}, t0)
	m2.Draw(&b, 120, 37, t0)
	if !strings.Contains(b.String(), "Hold the OK key (Mic) for 3 seconds to restart.") {
		t.Fatalf("%q", b.String())
	}
}

func TestLoaderPageFits(t *testing.T) {
	m := loaderModel(true)
	m.Loader.Lines = []string{
		"ok: the installer RAM image (token p10inst-e13d74), no eMMC file system mounted",
		"ok: the machine is a Crestron UC-P10 (rockchip,rk3399)",
		"ok: device tree: syscon@ff320000/reboot-mode, offset 0x300, mode-loader = 0x5242c301 (BOOT_BL_DOWNLOAD)",
		"ok: the driver syscon-reboot-mode is bound (ff320000.syscon:reboot-mode)",
	}
	m.Page, m.Sel = PageLoader, int(PageLoader)
	body := m.body(time.Unix(1000, 0))
	if len(body) > 37-5 {
		t.Fatalf("%d rows", len(body))
	}
	for _, l := range body {
		if len(l.Text) > 120 {
			t.Errorf("cut row: %q", l.Text)
		}
	}
	if body[len(body)-1].Text != "  Hold the OK key (Mic) for 3 seconds to restart into the download mode." {
		t.Errorf("last row %q", body[len(body)-1].Text)
	}
}

// openPTY is a pseudo terminal: the master (the "keyboard" side) and the
// slave (the terminal the UI draws on).
func openPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pty: %v", err)
	}
	t.Cleanup(func() { m.Close() })
	// the ioctls through SyscallConn: Fd() would make m blocking, and its
	// read deadline would then not work
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
	s, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("pty slave: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return m, s
}

// echoOf is what the terminal echoes for the input in, within a moment.
func echoOf(t *testing.T, m *os.File, in string) string {
	t.Helper()
	if _, err := m.Write([]byte(in)); err != nil {
		t.Fatal(err)
	}
	m.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	var got []byte
	buf := make([]byte, 64)
	for {
		n, err := m.Read(buf)
		got = append(got, buf[:n]...)
		if err != nil {
			return string(got)
		}
	}
}

// TestRawStopsTouchKeyEcho: the cause of the page jump of R3 (owner feedback
// item 2). The console keyboard turns Vol- into F14, ESC [ 26 ~; a terminal
// in its normal mode echoes it as "^[[26~" (that echo, after the last cell
// of the screen, scrolled the page). Raw, which p10inst flow sets on tty1,
// echoes nothing.
func TestRawStopsTouchKeyEcho(t *testing.T) {
	m, s := openPTY(t)
	if got := echoOf(t, m, "\x1b[26~"); !strings.Contains(got, "^[[26~") {
		t.Fatalf("a terminal in its normal mode echoes %q (the test expects the echo of the cause)", got)
	}
	restore, err := Raw(s.Fd())
	if err != nil {
		t.Fatal(err)
	}
	if got := echoOf(t, m, "\x1b[26~\x1b[25~"); got != "" {
		t.Errorf("raw mode echoes %q", got)
	}
	restore()
}
