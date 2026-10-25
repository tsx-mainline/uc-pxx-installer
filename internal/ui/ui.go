// Package ui is the text UI of the installer. It draws on a terminal: the
// framebuffer console tty1 of the device, or the USB serial shell. On the
// device the touch keys drive it (keys.go). This first version has the
// status screen, the log and the restart. It has no action that writes to the
// eMMC.
package ui

import (
	"fmt"
	"io"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
	"unsafe"

	"uc-pxx-installer/internal/status"
)

// Page is one screen.
type Page int

const (
	PageStatus Page = iota
	PageLog
	PageRestart
)

var menu = []string{"Status", "Log", "Restart"}

// HoldOK is how long the OK key must be held for a restart.
const HoldOK = 3 * time.Second

// Model is the state of the UI. Draw renders it. It has no I/O of its own,
// so the tests can drive it.
type Model struct {
	Title    string
	Report   *status.Report
	Extra    []status.Line // the live part: USB link, keys, boot-mode register
	Log      []string
	Page     Page
	Sel      int
	Terminal bool // keys come from a terminal (y confirms, no hold)
	Msg      string
	okDown   time.Time
	Confirm  bool // terminal: waiting for y
	Restart  bool // set when the user asked for the restart
	Legend   string
}

// Handle applies one key event at time now.
func (m *Model) Handle(e Event, now time.Time) {
	if e.Key == KeyOK && !e.Down {
		m.okDown = time.Time{}
		return
	}
	if !e.Down {
		return
	}
	m.Msg = ""
	switch e.Key {
	case KeyPrev:
		m.Sel = (m.Sel + len(menu) - 1) % len(menu)
		m.Confirm = false
	case KeyNext:
		m.Sel = (m.Sel + 1) % len(menu)
		m.Confirm = false
	case KeyBack:
		m.Page, m.Sel, m.Confirm = PageStatus, 0, false
	case KeyOK:
		m.Page = Page(m.Sel)
		if m.Page == PageRestart {
			if m.Terminal {
				m.Confirm = true
			} else {
				m.okDown = now
			}
		}
	case KeyYes:
		if m.Confirm && m.Page == PageRestart {
			m.Restart = true
		}
	}
}

// Tick checks the OK hold. It returns true when the screen must be drawn
// again.
func (m *Model) Tick(now time.Time) bool {
	if m.okDown.IsZero() || m.Page != PageRestart {
		return false
	}
	if now.Sub(m.okDown) >= HoldOK {
		m.okDown = time.Time{}
		m.Restart = true
	}
	return true
}

// holdLeft is the time of the OK hold that is still needed.
func (m *Model) holdLeft(now time.Time) time.Duration {
	if m.okDown.IsZero() {
		return HoldOK
	}
	return HoldOK - now.Sub(m.okDown)
}

func (m *Model) body(now time.Time) []status.Line {
	var out []status.Line
	add := func(level int, f string, a ...any) {
		out = append(out, status.Line{Level: level, Text: fmt.Sprintf(f, a...)})
	}
	switch m.Page {
	case PageStatus:
		if m.Report == nil {
			add(status.Normal, "Reading the eMMC (read-only) ...")
		} else {
			out = append(out, m.Report.Lines()...)
		}
		out = append(out, status.Line{})
		out = append(out, m.Extra...)
	case PageLog:
		add(status.Heading, "Log (newest last)")
		for _, l := range m.Log {
			add(status.Normal, "  %s", l)
		}
	case PageRestart:
		add(status.Heading, "Leave the installer")
		next := "the slot that the A/B metadata selects"
		if r := m.Report; r != nil && r.Next >= 0 {
			next = "slot " + strings.ToUpper(r.Slots[r.Next].Name) + " (" + r.Slots[r.Next].Summary + ")"
		}
		add(status.Normal, "  A restart boots %s.", next)
		add(status.Normal, "  The installer changed nothing on the eMMC. The restart writes the normal boot")
		add(status.Normal, "  mode, so the unit does not come back into fastboot.")
		add(status.Normal, "  You can also disconnect the power instead.")
		add(status.Normal, "")
		add(status.Normal, "  If the installer crashes, the unit restarts into fastboot after 30 s.")
		add(status.Normal, "  Then start the installer again from the computer, or disconnect the power.")
		add(status.Normal, "")
		if m.Restart {
			add(status.Good, "  Restarting ...")
		} else if m.Terminal {
			if m.Confirm {
				add(status.Warn, "  Press y to restart now. Any other key cancels.")
			} else {
				add(status.Normal, "  Press Enter, then y, to restart.")
			}
		} else if !m.okDown.IsZero() {
			left := m.holdLeft(now)
			n := int(left.Seconds()*4) + 1
			add(status.Warn, "  Keep holding OK: restart in %.1f s  %s", left.Seconds(), strings.Repeat("#", n))
		} else {
			add(status.Normal, "  Hold the OK key (Mic) for 3 seconds to restart.")
		}
	}
	return out
}

// Colors of the Linux console for the line levels.
var colors = map[int]string{
	status.Normal:  "\x1b[0;37m",
	status.Heading: "\x1b[1;36m",
	status.Good:    "\x1b[1;32m",
	status.Warn:    "\x1b[1;33m",
	status.Bad:     "\x1b[1;31m",
}

func fit(s string, w int) string {
	if utf8.RuneCountInString(s) > w {
		r := []rune(s)
		return string(r[:w])
	}
	return s + strings.Repeat(" ", w-utf8.RuneCountInString(s))
}

// Draw renders the whole screen of w columns and h rows.
func (m *Model) Draw(out io.Writer, w, h int, now time.Time) error {
	if w < 40 {
		w = 40
	}
	if h < 12 {
		h = 12
	}
	var rows []status.Line
	rows = append(rows, status.Line{Level: status.Heading, Text: m.Title})
	rows = append(rows, status.Line{Text: strings.Repeat("-", w)})
	body := m.body(now)
	room := h - 2 - 3
	if len(body) > room {
		body = body[:room]
	}
	rows = append(rows, body...)
	for len(rows) < h-3 {
		rows = append(rows, status.Line{})
	}
	var mb strings.Builder
	for i, item := range menu {
		if i == m.Sel {
			mb.WriteString("[ " + strings.ToUpper(item) + " ]")
		} else {
			mb.WriteString("  " + item + "  ")
		}
		mb.WriteString("  ")
	}
	legend := m.Legend
	if legend == "" {
		legend = Legend
		if m.Terminal {
			legend = "Up / Down choose   Enter = OK   b = back   q = quit"
		}
	}
	if pad := w - utf8.RuneCountInString(mb.String()) - utf8.RuneCountInString(legend); pad > 0 {
		mb.WriteString(strings.Repeat(" ", pad))
	} else {
		mb.WriteString("  ")
	}
	mb.WriteString(legend)
	rows = append(rows, status.Line{Text: strings.Repeat("-", w)})
	rows = append(rows, status.Line{Level: status.Heading, Text: mb.String()})
	rows = append(rows, status.Line{Level: status.Warn, Text: m.Msg})
	var b strings.Builder
	b.WriteString("\x1b[?25l\x1b[H")
	for i, r := range rows {
		b.WriteString(colors[r.Level])
		b.WriteString(fit(r.Text, w))
		if i < len(rows)-1 {
			b.WriteString("\r\n")
		}
	}
	b.WriteString("\x1b[0m")
	_, err := io.WriteString(out, b.String())
	return err
}

// Size reads the terminal size (TIOCGWINSZ). It returns 0, 0 when it fails.
func Size(fd uintptr) (w, h int) {
	var ws struct{ Row, Col, X, Y uint16 }
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&ws))); e != 0 {
		return 0, 0
	}
	return int(ws.Col), int(ws.Row)
}
