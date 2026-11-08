// Package ui is the text UI of the installer. It draws on a terminal: the
// framebuffer console tty1 of the device, or the USB serial shell. On the
// device the touch keys drive it (keys.go). It has the status screen, the
// log, the restart and, in the status image, the restart into the download
// mode (Model.Loader). It has no action that writes to the eMMC.
package ui

import (
	"fmt"
	"io"
	"math"
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
	PageLoader // the restart into the download mode: only with Model.Loader
)

var menu = []string{"Status", "Log", "Restart", "Download mode"}

// HoldOK is how long the OK key must be held for a restart.
const HoldOK = 3 * time.Second

// LoaderPage is what the page of the restart into the download mode shows:
// the checks of package rebootmode, and whether they passed. Only then does
// the hold (or Enter, y) ask for the restart.
type LoaderPage struct {
	OK    bool
	Lines []string // the checks, "ok: ..." or "refused: ..."
}

// Model is the state of the UI. Draw renders it. It has no I/O of its own,
// so the tests can drive it.
//
// The keys (the layout of the install flow, owner feedback FLOW-FEEDBACK-R3
// item 5): on the status page Vol- (previous) and Vol+ (next) move the
// selection of the menu, which is the first row of the page, and OK opens
// the selected page; Speaker shows the next rows when the status does not
// fit. On every other page Speaker goes back to the status page (with the
// selection on Status). On the log, Vol- and Vol+ scroll. On a page with an
// action (Restart, Download mode), the press of OK that opens the page also
// starts the 3 s hold, as in release 1.1; a release before 3 s cancels it.
// A terminal confirms with Enter, then y.
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
	// Loader puts the page "Download mode" into the menu (the status image
	// only; nil in the install build). RestartLoader is set when the user
	// asked for the restart into the download mode, with Loader.OK.
	Loader        *LoaderPage
	RestartLoader bool
	// Panel is the key row of the unit's screen (the zero value: the
	// UC-P10).
	Panel Panel
	// Scroll: the page of the status (Speaker), or the rows scrolled up
	// from the end of the log.
	Scroll int
	more   bool // the last Draw of the status page had more rows than fit
}

// items is the menu: the download mode only with Loader.
func (m *Model) items() []string {
	if m.Loader == nil {
		return menu[:PageLoader]
	}
	return menu
}

// holdPage: a page whose action needs the 3 s hold of OK (or Enter, y).
func (m *Model) holdPage() bool {
	return m.Page == PageRestart || (m.Page == PageLoader && m.Loader != nil && m.Loader.OK)
}

// act sets the request of the page.
func (m *Model) act() {
	switch {
	case m.Page == PageRestart:
		m.Restart = true
	case m.Page == PageLoader && m.Loader != nil && m.Loader.OK:
		m.RestartLoader = true
	}
}

// open shows page p. On a page with an action the same press of OK starts
// the hold (on a terminal: waits for y).
func (m *Model) open(p Page, now time.Time) {
	if p != m.Page {
		m.Scroll = 0
	}
	m.Page = p
	if m.holdPage() {
		if m.Terminal {
			m.Confirm = true
		} else {
			m.okDown = now
		}
	}
}

// home is the status page with the selection on Status.
func (m *Model) home() {
	m.Page, m.Sel, m.Scroll, m.okDown = PageStatus, 0, 0, time.Time{}
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
	case KeyYes:
		if m.Confirm && m.holdPage() {
			m.act()
		}
		return
	case KeyPrev, KeyNext, KeyBack, KeyOK:
		m.Confirm = false // any other key cancels a terminal's Enter
	default:
		return
	}
	switch m.Page {
	case PageStatus:
		switch e.Key {
		case KeyPrev:
			m.Sel = (m.Sel + len(m.items()) - 1) % len(m.items())
		case KeyNext:
			m.Sel = (m.Sel + 1) % len(m.items())
		case KeyBack:
			if m.more {
				m.Scroll++
			}
		case KeyOK:
			m.open(Page(m.Sel), now)
		}
	case PageLog:
		switch e.Key {
		case KeyPrev:
			m.Scroll++ // up: older rows
		case KeyNext:
			if m.Scroll > 0 {
				m.Scroll--
			}
		case KeyBack, KeyOK:
			m.home()
		}
	default:
		switch e.Key {
		case KeyBack:
			m.home()
		case KeyOK:
			m.open(m.Page, now)
		}
	}
}

// Tick checks the OK hold. It returns true when the screen must be drawn
// again.
func (m *Model) Tick(now time.Time) bool {
	if m.okDown.IsZero() || !m.holdPage() {
		return false
	}
	if now.Sub(m.okDown) >= HoldOK {
		m.okDown = time.Time{}
		m.act()
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

// menuRow is the menu, the first row of the status page: the selected item
// in capitals and brackets (the flow's status menu).
func (m *Model) menuRow() status.Line {
	var mb strings.Builder
	for i, it := range m.items() {
		if i == m.Sel {
			mb.WriteString("[ " + strings.ToUpper(it) + " ]  ")
		} else {
			mb.WriteString("  " + it + "    ")
		}
	}
	return status.Line{Level: status.Heading, Text: strings.TrimRight(mb.String(), " ")}
}

// view is the current page.
func (m *Model) view(now time.Time) screen {
	var out []status.Line
	add := func(level int, f string, a ...any) {
		out = append(out, status.Line{Level: level, Text: fmt.Sprintf(f, a...)})
	}
	var s screen
	switch m.Page {
	case PageStatus:
		s.head = []status.Line{m.menuRow(), {}}
		if m.Report == nil {
			add(status.Normal, "Reading the eMMC (read-only) ...")
		} else {
			out = append(out, m.Report.Lines()...)
		}
		out = append(out, status.Line{})
		out = append(out, m.Extra...)
		s.pages = true
		s.keys = keys{prev: "Previous", next: "Next", ok: "Open"}
	case PageLog:
		s.head = []status.Line{{Level: status.Heading, Text: "Log (newest last)"}}
		for _, l := range m.Log {
			add(status.Normal, "  %s", l)
		}
		s.fromEnd = true
		s.keys = keys{back: "Back", prev: "Scroll up", next: "Scroll down", ok: "Back"}
	case PageRestart:
		add(status.Heading, "Leave the installer")
		next := "the slot that the A/B metadata selects"
		if r := m.Report; r != nil && r.Next >= 0 {
			next = "slot " + strings.ToUpper(r.Slots[r.Next].Name) + " (" + r.Slots[r.Next].Summary + ")"
		}
		add(status.Normal, "  A restart boots %s.", next)
		// RELEASE-1.1-REVIEW S-2: a BCB that holds a command (for example a
		// stale "bootonce-bootloader") still sends U-Boot into fastboot
		add(status.Normal, "  The installer changed nothing on the eMMC. The restart writes the normal boot mode, so the unit does not come back into fastboot, unless the BCB in misc holds a boot command.")
		add(status.Normal, "  You can also disconnect the power instead.")
		add(status.Normal, "")
		add(status.Normal, "  If the installer crashes, the unit restarts into fastboot after 30 s. Then start the installer again from the computer, or disconnect the power.")
		add(status.Normal, "")
		if m.Restart {
			add(status.Good, "  Restarting ...")
		} else {
			out = append(out, m.holdLines(now, "restart")...)
			s.keys = keys{back: "Back", ok: "Hold: restart", hold: true}
		}
	case PageLoader:
		add(status.Heading, "Restart into the download mode")
		add(status.Normal, "  The unit restarts into the download mode of U-Boot (rockusb: USB 2207:330c, Loader). There a computer can read the eMMC. Use it only when the procedure asks for it.")
		// RELEASE-1.1-REVIEW S-2: the order of the register and the BCB is
		// measured only with an all-zero BCB command field (E2)
		add(status.Normal, "  The restart writes nothing to the eMMC. The kernel sets only the boot-mode register (0x5242c301, \"loader\"). With an empty command field in the BCB, U-Boot then enters the download mode (measured on a UC-P10, 2026-11-03). With a boot command in the BCB (for example bootonce-bootloader), this was not measured.")
		add(status.Normal, "  In the download mode this screen is gone. To leave it, disconnect the power: a cold start clears the register, and the unit starts as it would now.")
		add(status.Normal, "")
		add(status.Normal, "  Checks (read only):")
		if m.Loader != nil {
			for _, l := range m.Loader.Lines {
				lv := status.Good
				if strings.HasPrefix(l, "refused:") {
					lv = status.Bad
				}
				add(lv, "    %s", l)
			}
		}
		add(status.Normal, "")
		switch {
		case m.RestartLoader:
			add(status.Good, "  Restarting into the download mode ...")
		case m.Loader == nil || !m.Loader.OK:
			add(status.Bad, "  Not possible here (see the check above). OK does nothing on this page.")
			s.keys = keys{back: "Back"}
		default:
			out = append(out, m.holdLines(now, "restart into the download mode")...)
			s.keys = keys{back: "Back", ok: "Hold: download", hold: true}
		}
	}
	s.body = out
	return s
}

// holdLines is the hold (or Enter, y) part of a page with an action.
func (m *Model) holdLines(now time.Time, what string) []status.Line {
	var out []status.Line
	add := func(level int, f string, a ...any) {
		out = append(out, status.Line{Level: level, Text: fmt.Sprintf(f, a...)})
	}
	switch {
	case m.Terminal && m.Confirm:
		add(status.Warn, "  Press y to %s now. Any other key cancels.", what)
	case m.Terminal:
		add(status.Normal, "  Press Enter, then y, to %s.", what)
	case !m.okDown.IsZero():
		left := m.holdLeft(now)
		n := int(left.Seconds()*4) + 1
		s := int(math.Ceil(left.Seconds())) // whole seconds: 3, 2, 1 (the rule of flow.HoldSeconds)
		if s < 1 {
			s = 1
		}
		add(status.Warn, "  Keep holding OK: %s in %d s  %s", what, s, strings.Repeat("#", n))
	default:
		add(status.Normal, "  Hold the OK key (Mic) for 3 seconds to %s.", what)
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

// Draw renders the whole screen of w columns and h rows (in one write: the
// frame replaces the last one, row by row).
func (m *Model) Draw(out io.Writer, w, h int, now time.Time) error {
	if w < 40 {
		w = 40
	}
	if h < 12 {
		h = 12
	}
	rows := m.rows(m.view(now), w, h)
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
