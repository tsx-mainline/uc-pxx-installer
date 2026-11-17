//go:build install && step2

package step2

// The live panel of the service: the kiosk stopped, the text on tty1 (the
// LCD), the touch keys grabbed (EVIOCGRAB) so the kiosk under it never sees
// them. The keys come from the evdev of the maXTouch controller as
// ui.ReadEvdev reads them (autorepeat values dropped), mapped as the RAM
// flow maps them: Mic = OK, Speaker = Back, Vol-/Vol+.
//
// The panel owns the screen (R31: the screen flashed during the countdown):
// LivePanel stops the kiosk and waits until its compositor is gone before it
// switches to tty1 and puts tty1 in text mode; Show draws a frame only when
// its text changed, over the old one (cursor home, each line, the rest of the
// line and of the screen cleared), never a clear of the whole screen first;
// and while the panel is open, a compositor that came back (a kiosk restart
// by something else) is stopped again and the screen taken back. Every
// program of the panel has a deadline, and Show never waits for one: a
// reclaim runs in the background, at most once per reclaimEvery and at most
// maxReclaims times (RC-10c review MINOR-2).

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"uc-pxx-installer/internal/step2ui"
	"uc-pxx-installer/internal/ui"
)

const (
	evSyn      = 0
	synDropped = 3
	evKey      = 1
	eventSize  = 24         // struct input_event on 64-bit Linux
	eviocgrab  = 0x40044590 // _IOW('E', 0x90, int)
	panelTTY   = "/dev/tty1"
	rcService  = "/sbin/rc-service"
	kioskName  = "kiosk"
	chvtBinary = "/usr/bin/chvt"
	kdsetmode  = 0x4B3A // KDSETMODE
	kdText     = 0      // KD_TEXT
	// kioskGone is the longest wait for the kiosk's compositor to end after
	// its stop; reclaimEvery how often Show looks for one that came back,
	// maxReclaims how often the panel takes the screen back (then it stops
	// looking).
	kioskGone    = 20 * time.Second
	reclaimEvery = 30 * time.Second
	maxReclaims  = 3
	// rcLimit bounds an rc-service call of the kiosk (stop, start), chvtLimit
	// the VT switch (busybox chvt waits until the switch is done); a program
	// that has not ended then is killed.
	rcLimit   = 30 * time.Second
	chvtLimit = 5 * time.Second
)

// compositors are the processes (their comm) of the kiosk on the screen.
var compositors = []string{"sway", "cage", "kiosk-session"}

type livePanel struct {
	tty  *os.File
	ev   *os.File
	keys chan step2ui.Key
	// run starts a program with a deadline, compositor looks for the kiosk's
	// compositor; gone, every and max are kioskGone, reclaimEvery and
	// maxReclaims (a test sets its own)
	run         func(limit time.Duration, name string, args ...string) error
	compositor  func() bool
	gone, every time.Duration
	max         int
	mu          sync.Mutex // tty, last, closed, checked, reclaims, reclaim
	last        string     // the text of the frame on the screen
	closed      bool       // Close began: no more programs, no more writes
	checked     time.Time  // when Show last looked for a compositor
	reclaims    int
	reclaim     chan struct{} // while a reclaim runs; closed at its end
}

func newLivePanel(tty, ev *os.File) *livePanel {
	return &livePanel{tty: tty, ev: ev, keys: make(chan step2ui.Key, 16), run: runFor,
		compositor: func() bool { return CompositorRunning("/proc") }, gone: kioskGone, every: reclaimEvery, max: maxReclaims}
}

// LivePanel stops the kiosk and takes tty1 and the touch keys.
func LivePanel() (Panel, error) {
	dev := ui.FindTouch("/")
	if dev == "" {
		return nil, fmt.Errorf("no maXTouch event device")
	}
	ev, err := os.Open(dev)
	if err != nil {
		return nil, err
	}
	one := 1
	if _, _, en := syscall.Syscall(syscall.SYS_IOCTL, ev.Fd(), eviocgrab, uintptr(one)); en != 0 {
		ev.Close()
		return nil, fmt.Errorf("EVIOCGRAB %s: %v", dev, en)
	}
	tty, err := os.OpenFile(panelTTY, os.O_WRONLY|syscall.O_NOCTTY, 0)
	if err != nil {
		ev.Close()
		return nil, err
	}
	p := newLivePanel(tty, ev)
	p.takeScreen() // bounded: rcLimit + kioskGone + chvtLimit
	p.checked = time.Now()
	go p.read()
	return p, nil
}

// takeScreen stops the kiosk, waits until its compositor is gone (at most
// p.gone), switches to tty1, puts it in text mode and hides the cursor; the
// next Show draws a whole frame. A compositor that is still there keeps its
// VT: then no chvt (it would wait for the switch) and no text mode. After
// Close began it starts no program and writes nothing.
func (p *livePanel) takeScreen() {
	if p.isClosed() {
		return
	}
	p.run(rcLimit, rcService, kioskName, "stop")
	for end := time.Now().Add(p.gone); p.compositor() && time.Now().Before(end) && !p.isClosed(); {
		time.Sleep(200 * time.Millisecond)
	}
	if p.compositor() || p.isClosed() {
		return
	}
	p.run(chvtLimit, chvtBinary, "1")
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	if !p.compositor() {
		// a compositor left in graphics mode would hide the text
		syscall.Syscall(syscall.SYS_IOCTL, p.tty.Fd(), kdsetmode, kdText)
	}
	p.tty.WriteString("\033[?25l\033[H\033[2J")
	p.last = ""
}

func (p *livePanel) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

// CompositorRunning: a process of the kiosk's screen (compositors, by
// /proc/N/comm) runs. proc is the procfs ("/proc"; tests another folder).
func CompositorRunning(proc string) bool {
	ents, _ := os.ReadDir(proc)
	for _, d := range ents {
		if _, err := strconv.Atoi(d.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join(proc, d.Name(), "comm"))
		if err != nil {
			continue
		}
		c := strings.TrimSpace(string(b))
		for _, n := range compositors {
			if c == n {
				return true
			}
		}
	}
	return false
}

// Frame is the text that draws lines over the frame on the screen: the
// cursor home, each line (cut to w cells and wrapped) with the rest of its
// row cleared, then the rest of the screen cleared. No clear of the whole
// screen before the text: a redraw does not flash.
func Frame(lines []string, w int) string {
	if w <= 0 {
		w = 80
	}
	var b strings.Builder
	b.WriteString("\033[H")
	for _, l := range lines {
		for len(l) > w {
			b.WriteString(l[:w] + "\033[K\r\n")
			l = l[w:]
		}
		b.WriteString(l + "\033[K\r\n")
	}
	b.WriteString("\033[J")
	return b.String()
}

// runFor runs a program of the panel; it is killed (SIGKILL) when it has
// not ended within limit (RC-10c review MINOR-2: a hung kiosk stop or a VT
// switch that never ends must not hold the panel).
func runFor(limit time.Duration, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	c := exec.CommandContext(ctx, name, args...)
	c.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	c.WaitDelay = time.Second
	return c.Run()
}

func (p *livePanel) read() {
	buf := make([]byte, eventSize*16)
	for {
		n, err := p.ev.Read(buf)
		if err != nil {
			return
		}
		for _, k := range KeysOf(buf[:n]) {
			select {
			case p.keys <- k:
			default:
			}
		}
	}
}

// KeysOf turns evdev events into keys of the confirm model: key presses and
// releases of the touch keys (autorepeat values dropped). After SYN_DROPPED
// (the kernel lost events, perhaps the release of OK) it gives KeyOKUp: any
// hold ends, and only a new press of OK starts one (review T4-2 NIT-1: a
// lost key-up never lets one short tap start the run later).
func KeysOf(b []byte) []step2ui.Key {
	var out []step2ui.Key
	for off := 0; off+eventSize <= len(b); off += eventSize {
		e := b[off : off+eventSize]
		typ := binary.LittleEndian.Uint16(e[16:])
		code := binary.LittleEndian.Uint16(e[18:])
		val := int32(binary.LittleEndian.Uint32(e[20:]))
		switch {
		case typ == evSyn && code == synDropped:
			out = append(out, step2ui.KeyOKUp)
		case typ != evKey || val == 2:
			// autorepeat: never a key of the model
		default:
			if k := keyOf(ui.MapCode(code), val == 1); k != step2ui.KeyNone {
				out = append(out, k)
			}
		}
	}
	return out
}

// keyOf maps a UI key press or release to a key of the confirm model.
func keyOf(k ui.Key, down bool) step2ui.Key {
	switch {
	case k == ui.KeyOK && down:
		return step2ui.KeyOKDown
	case k == ui.KeyOK:
		return step2ui.KeyOKUp
	case k == ui.KeyBack && down:
		return step2ui.KeyBack
	case k == ui.KeyPrev && down:
		return step2ui.KeyVolDown
	case k == ui.KeyNext && down:
		return step2ui.KeyVolUp
	}
	return step2ui.KeyNone
}

// Show draws the lines. At most every p.every it looks for a compositor
// that came back (the kiosk restarted by something else); then a reclaim
// (takeScreen) runs in the background, one at a time and at most p.max times,
// and Show draws at once: it never waits for a program.
func (p *livePanel) Show(lines []string) {
	p.startReclaim()
	p.Draw(lines)
}

func (p *livePanel) startReclaim() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.reclaim != nil || p.reclaims >= p.max || time.Since(p.checked) < p.every {
		return
	}
	p.checked = time.Now()
	if !p.compositor() {
		return
	}
	p.reclaims++
	done := make(chan struct{})
	p.reclaim = done
	go func() {
		defer close(done)
		p.takeScreen() // the kiosk came back: stop it, take the screen again
		p.mu.Lock()
		p.reclaim, p.checked = nil, time.Now()
		p.mu.Unlock()
	}()
}

// Draw is Show without the look for a compositor: it runs no program and
// waits for nothing (the copy's progress, while the root is frozen: a
// program started then could wait for the thaw).
func (p *livePanel) Draw(lines []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	w, _ := ui.Size(p.tty.Fd())
	f := Frame(lines, w)
	if f == p.last {
		return
	}
	p.last = f
	p.tty.WriteString(f)
}

func (p *livePanel) Keys() <-chan step2ui.Key { return p.keys }

// Close gives the screen and the keys back. A reclaim that runs starts no
// more programs; Close waits for the one it runs (it has a deadline), so its
// kiosk stop never comes after the kiosk start here.
func (p *livePanel) Close(restart bool) {
	p.mu.Lock()
	p.closed = true
	r := p.reclaim
	p.mu.Unlock()
	if r != nil {
		<-r
	}
	p.ev.Close() // the grab ends with the file
	p.mu.Lock()
	p.tty.WriteString("\033[?25h")
	p.tty.Close()
	p.mu.Unlock()
	if restart {
		p.run(rcLimit, rcService, kioskName, "start")
	}
}
