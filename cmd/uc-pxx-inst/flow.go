//go:build install

package main

// The on-screen install flow of the install RAM image (package flow, run by
// package flowio). It is in the install build only: the status image has
// `uc-pxx-inst ui`, which never installs.
//
//	uc-pxx-inst flow [--tty DEV | --stdin] [--leds]
//
// The install image's inittab runs it on tty1 in place of `uc-pxx-inst ui`. The
// flow writes nothing to the eMMC itself: it starts the device mode as a
// child (`uc-pxx-inst install --device /dev/mmcblk0 ...`) after the hold of OK
// on the plan screen, under `uc-pxx-inst flow-supervise` (an internal command).

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"uc-pxx-installer/internal/flow"
	"uc-pxx-installer/internal/flowio"
	"uc-pxx-installer/internal/manifest"
	"uc-pxx-installer/internal/status"
	"uc-pxx-installer/internal/ui"
)

const flowUsage = `install build, the on-screen install flow (the install RAM image, tty1):
  uc-pxx-inst flow      [--tty DEV | --stdin [--take-over]] [--leds]
                    one flow runs, in the UI that has the flow lock (on the
                    install image the screen, tty1). While it runs:
                    --stdin refuses (exit 4); --stdin --take-over drives it
                    from this terminal (the screen shows the state, its touch
                    keys are off until q leaves); another --tty UI shows its
                    state and takes over when it ends
`

// exitUIBusy is the exit status of ` + "`uc-pxx-inst flow --stdin`" + ` while another flow
// UI runs (and of --take-over while another terminal has the keys).
const exitUIBusy = 4

func init() {
	extraCmds["flow"] = cmdFlow
	extraCmds["flow-supervise"] = cmdFlowSupervise
}

func cmdFlowSupervise(args []string) {
	fs := flag.NewFlagSet("flow-supervise", flag.ExitOnError)
	dir := fs.String("dir", "", "the folder of the child")
	fs.Parse(args)
	os.Exit(flowio.SuperviseMain(*dir, fs.Args()))
}

func flowKey(k ui.Key) flow.Key {
	switch k {
	case ui.KeyPrev:
		return flow.KeyPrev
	case ui.KeyNext:
		return flow.KeyNext
	case ui.KeyOK:
		return flow.KeyOK
	case ui.KeyBack:
		return flow.KeyBack
	case ui.KeyYes:
		return flow.KeyYes
	}
	return flow.KeyNone
}

var flowColors = map[int]string{
	status.Normal:  "\x1b[0;37m",
	status.Heading: "\x1b[1;36m",
	status.Good:    "\x1b[1;32m",
	status.Warn:    "\x1b[1;33m",
	status.Bad:     "\x1b[1;31m",
}

// frameBytes is one whole screen: every row of the layout, padded to the
// width in console cells.
func frameBytes(f flow.Frame, w, h int) ([]byte, flow.Rows) {
	if w < 40 {
		w = 40
	}
	if h < 12 {
		h = 12
	}
	lay := f.Layout(w, h) // every row fits the width; the body fits the room
	rows := []status.Line{{Level: status.Heading, Text: lay.Title}, {Text: repeat("-", w)}}
	rows = append(rows, lay.Body...)
	for len(rows) < 2+lay.Room {
		rows = append(rows, status.Line{})
	}
	rule := repeat("-", w)
	if lay.Rule != "" && flow.Cells(lay.Rule)+8 < w {
		rule = repeat("-", w-8-flow.Cells(lay.Rule)) + lay.Rule + repeat("-", 8)
	}
	rows = append(rows, status.Line{Text: rule})
	for _, m := range lay.Msg {
		rows = append(rows, status.Line{Level: status.Warn, Text: m})
	}
	// the key legend last: right above the touch keys
	rows = append(rows, status.Line{Level: status.Heading, Text: lay.Footer})
	var b []byte
	b = append(b, "\x1b[?25l\x1b[H"...)
	for i, r := range rows {
		b = append(b, flowColors[r.Level]...)
		t := r.Text
		if flow.Cells(t) > w {
			t = string([]rune(t)[:w])
		}
		b = append(b, t...)
		b = append(b, repeat(" ", w-flow.Cells(t))...)
		if i < len(rows)-1 {
			b = append(b, "\r\n"...)
		}
	}
	b = append(b, "\x1b[0m"...)
	return b, lay
}

// drawFlow writes one screen (a blocking write: the screen renders of the
// tests; the flow UI writes through a termOut).
func drawFlow(out *os.File, f flow.Frame, w, h int) flow.Rows {
	b, lay := frameBytes(f, w, h)
	out.Write(b)
	return lay
}

// termOut writes the screens of a UI without ever blocking the UI: one
// goroutine writes, and a screen that the terminal has not taken yet is
// replaced by the next one. A terminal that is not read (a serial program
// closed with the cable still in) stalls only that goroutine (review M-1).
type termOut struct {
	f  *os.File
	ch chan []byte
}

func newTermOut(f *os.File) *termOut {
	t := &termOut{f: f, ch: make(chan []byte, 1)}
	go func() {
		for b := range t.ch {
			t.f.Write(b)
		}
	}()
	return t
}

func (t *termOut) put(b []byte) {
	for {
		select {
		case t.ch <- b:
			return
		default:
			select {
			case <-t.ch: // drop the screen that was not written yet
			default:
			}
		}
	}
}

func (t *termOut) size() (int, int) {
	w, h := ui.Size(t.f.Fd())
	if w == 0 {
		w, h = flow.PanelCols, flow.PanelRows
	}
	return w, h
}

// mirror is the frame of a UI that only shows the flow UI's screen: the
// published frame with a note in front of its message, and a warning when
// it is old.
func mirror(p flowio.Published, err error, h flowio.Holder, note string, now time.Time) flow.Frame {
	if err != nil || p.PID == 0 {
		return flow.Frame{Title: "UC-P10 installer", Msg: note, Body: []status.Line{
			{Level: status.Warn, Text: "The installer runs on another terminal (" + h.TTY + ")."},
			{Text: "Its screen is shown here when it draws."}}}
	}
	f := p.Frame
	f.Msg = strings.TrimSpace(note + "  " + f.Msg)
	f.Keys, f.Footer = [flow.KeyPlaces]string{}, "" // the keys here do nothing
	if age := now.Sub(p.At); age > 5*time.Second {
		f.Body = append([]status.Line{{Level: status.Bad, Text: fmt.Sprintf(
			"!!! The installer on %s has not updated this screen for %s. Keep the unit powered until a person has checked it.",
			p.TTY, flow.Dur(age))}}, f.Body...)
	}
	return f
}

func repeat(s string, n int) string {
	if n <= 0 {
		return ""
	}
	out := make([]byte, 0, n*len(s))
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}

func cmdFlow(args []string) {
	fs := flag.NewFlagSet("flow", flag.ExitOnError)
	tty := fs.String("tty", "/dev/tty1", "terminal to draw on")
	stdin := fs.Bool("stdin", false, "draw on this terminal and read keys from it (the USB serial shell)")
	takeOver := fs.Bool("take-over", false, "with --stdin, while another flow UI runs: send this terminal's keys to it and show its screen")
	leds := fs.Bool("leds", false, "show the state on the status LED")
	test := flowTestFlags(fs) // devtest builds only
	fs.Parse(args)

	exe, err := os.Executable()
	if err != nil {
		fail(2, "%v", err)
	}
	ms, err := manifest.Embedded()
	if err != nil {
		fail(2, "manifests: %v", err)
	}
	c := flowio.Config{Root: "/", Exe: exe, Manifests: ms, Log: func(s string) { logf("flow: %s", s) }, Restart: restart}
	if *leds {
		c.LED = func(color string) {
			for _, l := range []string{"blue", "green", "red"} {
				setLED(l, l == color)
			}
		}
	}
	noReboot := false
	if test != nil {
		noReboot = test(&c)
	}
	// One flow at a time (flowio.UILockFile), taken before the state of an
	// earlier UI is read. A UI that died leaves no lock (flock), so the UI
	// that busybox starts again after a crash takes over at once.
	me := *tty
	if *stdin {
		me = "stdin"
		if t, err := os.Readlink("/proc/self/fd/0"); err == nil {
			me = t
		}
	}
	lock, holder, err := flowio.TakeUILock(c.Root, me)
	if err != nil && !errors.Is(err, flowio.ErrUIBusy) {
		fail(2, "flow UI lock: %v", err)
	}
	if lock == nil && *stdin && *takeOver {
		os.Exit(runRemote(c.Root, me, holder))
	}
	if lock == nil && *stdin {
		fmt.Fprintf(os.Stderr, "uc-pxx-inst flow: another flow UI runs (%s). This one does not start: use that one,\n"+
			"or drive it from here with: uc-pxx-inst flow --stdin --take-over\n", holder)
		logf("flow: not started on %s: another flow UI runs (%s)", me, holder)
		os.Exit(exitUIBusy)
	}
	var out *termOut
	if *stdin {
		out = newTermOut(os.Stdout)
	} else {
		f, err := os.OpenFile(*tty, os.O_RDWR, 0)
		if err != nil {
			fail(2, "%v", err)
		}
		// No echo on the screen (owner feedback R3 item 2: the page jumped
		// on Next). The console keyboard also gets the touch keys: Vol- is
		// F14 (ESC [ 26 ~) and Mic F13 (ESC [ 25 ~) in the kernel's keymap.
		// With echo on, tty1 echoed "^[[26~" where the last frame left the
		// cursor, after the last cell of the last row, and that scrolled
		// the whole screen up by one row until the next frame. The flow
		// reads its keys from the touch device, never from tty1.
		if restore, err := ui.Raw(f.Fd()); err == nil {
			defer restore()
		} else {
			logf("flow: %s: echo not turned off: %v", *tty, err)
		}
		f.WriteString("\x1b[9;0]\x1b[14;0]\x1b[2J")
		out = newTermOut(f)
	}
	if lock == nil {
		// The screen (tty1, started by init) while another UI runs the
		// flow: it shows that UI's screen (read only, the keys here do
		// nothing), and takes over when that UI ends. Exiting here would
		// make init start this UI again and again.
		logf("flow: another flow UI runs (%s); this screen shows its state", holder)
		fmt.Fprintf(os.Stderr, "uc-pxx-inst flow: another flow UI runs (%s); showing its state, waiting for it to end\n", holder)
		for lock == nil {
			var h flowio.Holder
			lock, h, err = flowio.TakeUILock(c.Root, me)
			if err != nil && !errors.Is(err, flowio.ErrUIBusy) {
				fail(2, "flow UI lock: %v", err)
			}
			if lock != nil {
				break
			}
			p, perr := flowio.ReadPublished(c.Root)
			fr := mirror(p, perr, h, "The installer runs on the terminal "+h.TTY+". This screen only shows it: its keys do nothing.", time.Now())
			w, ht := out.size()
			b, _ := frameBytes(fr, w, ht)
			out.put(b)
			if c.LED != nil && perr == nil {
				c.LED(p.Frame.LED)
			}
			time.Sleep(250 * time.Millisecond)
		}
		logf("flow: the flow UI lock is free; this UI on %s takes over", me)
	}
	defer lock.Close()
	events := make(chan ui.Event, 32)
	quit := make(chan bool, 1)
	if *stdin {
		if restore, err := ui.Raw(os.Stdin.Fd()); err == nil {
			defer restore()
		}
		go ui.ReadTerm(os.Stdin, events)
	} else {
		root := c.Root
		go ui.WatchTouch(root, events, func(dev string) {
			if dev != "" {
				logf("flow: touch keys on %s", dev)
			}
		})
	}
	if noReboot {
		c.Restart = func() { quit <- true }
	}
	host, _ := os.Hostname()
	// the model's screen and key row from the RAM image's device tree (W-P6;
	// the same place as rcS's font): a UC-P8 image gets the UC-P8 panel
	dtRoot := c.Root
	if dtRoot == "" {
		dtRoot = "/"
	}
	compat, _ := os.ReadFile(filepath.Join(dtRoot, "proc/device-tree/compatible"))
	panel := flow.PanelFor(string(compat))
	m := flow.New(panel.Model+" installer  -  install image  -  "+host, *stdin)
	m.Panel = panel
	r := flowio.New(c, m)
	r.Start()
	rin := flowio.NewRemoteIn(c.Root)
	remote := false
	lastProblem := ""
	draw := func() {
		w, h := out.size()
		fr := m.View(time.Now())
		// the frame for the other terminals (a waiting screen, a take-over
		// terminal); they lay it out at their own size
		flowio.Publish(c.Root, flowio.Published{PID: os.Getpid(), TTY: me, At: time.Now(), Name: m.Screen.String(), Frame: fr})
		if remote {
			// R4 finding 6: a person at the unit must see why the touch keys
			// do nothing. Put an explicit banner in the key-legend row, in
			// place of the (now off) touch-key labels, and keep any message.
			fr.Keys = [flow.KeyPlaces]string{}
			fr.Footer = "Operated remotely over the console. The touch keys here are off until it leaves."
			fr.Msg = strings.TrimSpace(fr.Msg)
		}
		b, lay := frameBytes(fr, w, h)
		out.put(b)
		m.Drawn(time.Now()) // the terminal's warning counts from its first draw (review R3 L-10)
		// a screen whose rows do not fit is a fault: the flow log says so
		// (the tests look for it)
		if p := lay.Problem(m.Screen); p != lastProblem {
			lastProblem = p
			if p != "" {
				r.Note(fmt.Sprintf("layout: screen %s on %dx%d: %s", m.Screen, w, h, p))
			}
		}
	}
	draw()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case f := <-r.Events():
			f()
			r.Do(m.Take())
			draw()
		case e := <-events:
			if remote {
				continue // a take-over terminal has the keys
			}
			if e.Key == ui.KeyQuit && *stdin {
				if m.Running() == "install" {
					m.Msg = "The install runs on; the UI cannot be left now. Use another shell."
					draw()
					continue
				}
				out.f.WriteString("\x1b[0m\x1b[?25h\r\n")
				return
			}
			m.Handle(flow.Event{Key: flowKey(e.Key), Down: e.Down}, time.Now())
			r.Do(m.Take())
			draw()
		case <-tick.C:
			now := time.Now()
			active, who, keys := rin.Poll(now)
			if active != remote {
				remote = active
				m.SetTerminal(*stdin || remote)
				if remote {
					r.Note(fmt.Sprintf("keys: the terminal %s (pid %d) has the keys (take-over)", who.TTY, who.PID))
				} else {
					r.Note("keys: the take-over terminal left; the keys here are on again")
				}
			}
			for _, k := range keys {
				m.Handle(flow.Event{Key: k.Key, Down: k.Down}, now)
				r.Do(m.Take())
			}
			r.Poll()
			m.Tick(now)
			if a := m.Take(); len(a) > 0 {
				r.Do(a)
			}
			draw()
		case <-quit:
			out.f.WriteString("\x1b[0m\x1b[?25h\r\nrestart (test: no reboot)\r\n")
			return
		}
	}
}

// runRemote is `uc-pxx-inst flow --stdin --take-over`: this terminal sends its
// keys to the flow UI and shows the flow UI's screen. It never runs a
// child and never decides anything: the flow UI does (it handles the end
// of an install even if this terminal stalls). q leaves; the flow UI then
// takes the keys of its own screen again.
func runRemote(root, me string, ui1 flowio.Holder) int {
	var il *flowio.UILock
	var h flowio.Holder
	var err error
	for i := 0; i < 40; i++ { // the flow UI tries the lock for a moment on each poll
		if il, h, err = flowio.TakeInputLock(root, me); !errors.Is(err, flowio.ErrUIBusy) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if errors.Is(err, flowio.ErrUIBusy) {
		fmt.Fprintf(os.Stderr, "uc-pxx-inst flow: another terminal has the keys of the installer (%s)\n", h)
		return exitUIBusy
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "uc-pxx-inst flow: input lock: %v\n", err)
		return 2
	}
	defer il.Close()
	logf("flow: take-over from %s (the flow UI: %s)", me, ui1)
	beat := func() {
		if err := flowio.AppendRemote(root, "beat"); err != nil {
			logf("flow: take-over: %v", err)
		}
	}
	beat()
	out := newTermOut(os.Stdout)
	events := make(chan ui.Event, 32)
	if restore, err := ui.Raw(os.Stdin.Fd()); err == nil {
		defer restore()
	}
	go ui.ReadTerm(os.Stdin, events)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	lastBeat := time.Now()
	for {
		select {
		case e := <-events:
			if e.Key == ui.KeyQuit {
				out.f.WriteString("\x1b[0m\x1b[?25h\r\nleft: the installer's screen has its keys again\r\n")
				logf("flow: take-over from %s ended (q)", me)
				return 0
			}
			k := flowKey(e.Key)
			if k == flow.KeyNone {
				continue
			}
			d := 0
			if e.Down {
				d = 1
			}
			flowio.AppendRemote(root, fmt.Sprintf("key %d %d", int(k), d))
		case now := <-tick.C:
			if now.Sub(lastBeat) >= time.Second {
				beat()
				lastBeat = now
			}
			h := flowio.UIHolder(root) // read only: this terminal never takes the flow lock
			p, perr := flowio.ReadPublished(root)
			fr := mirror(p, perr, h, "", now)
			fr.Footer = strings.TrimSpace(p.Frame.Footer + "   q = leave (the install goes on)")
			w, ht := out.size()
			b, _ := frameBytes(fr, w, ht)
			out.put(b)
		}
	}
}
