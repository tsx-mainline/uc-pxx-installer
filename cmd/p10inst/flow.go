//go:build install

package main

// The on-screen install flow of the install RAM image (package flow, run by
// package flowio). It is in the install build only: the status image has
// `p10inst ui`, which never installs.
//
//	p10inst flow [--tty DEV | --stdin] [--leds]
//
// The install image's inittab runs it on tty1 in place of `p10inst ui`. The
// flow writes nothing to the eMMC itself: it starts the device mode as a
// child (`p10inst install --device /dev/mmcblk0 ...`) after the hold of OK
// on the plan screen, under `p10inst flow-supervise` (an internal command).

import (
	"flag"
	"fmt"
	"os"
	"time"

	"uc-pxx-installer/internal/flow"
	"uc-pxx-installer/internal/flowio"
	"uc-pxx-installer/internal/manifest"
	"uc-pxx-installer/internal/status"
	"uc-pxx-installer/internal/ui"
)

const flowUsage = `install build, the on-screen install flow (the install RAM image, tty1):
  p10inst flow      [--tty DEV | --stdin] [--leds]
`

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

func drawFlow(out *os.File, f flow.Frame, w, h int) {
	if w < 40 {
		w = 40
	}
	if h < 12 {
		h = 12
	}
	rows := []status.Line{{Level: status.Heading, Text: f.Title}, {Text: repeat("-", w)}}
	body := f.Body
	if room := h - 5; len(body) > room {
		body = body[:room]
	}
	rows = append(rows, body...)
	for len(rows) < h-3 {
		rows = append(rows, status.Line{})
	}
	rows = append(rows, status.Line{Text: repeat("-", w)}, status.Line{Level: status.Heading, Text: f.Footer},
		status.Line{Level: status.Warn, Text: f.Msg})
	var b []byte
	b = append(b, "\x1b[?25l\x1b[H"...)
	for i, r := range rows {
		b = append(b, flowColors[r.Level]...)
		t := []rune(r.Text)
		if len(t) > w {
			t = t[:w]
		}
		b = append(b, string(t)...)
		b = append(b, repeat(" ", w-len(t))...)
		if i < len(rows)-1 {
			b = append(b, "\r\n"...)
		}
	}
	b = append(b, "\x1b[0m"...)
	out.Write(b)
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
	out := os.Stdout
	if !*stdin {
		f, err := os.OpenFile(*tty, os.O_RDWR, 0)
		if err != nil {
			fail(2, "%v", err)
		}
		out = f
		fmt.Fprint(out, "\x1b[9;0]\x1b[14;0]\x1b[2J")
	}
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
	m := flow.New("UC-P10 installer  -  install image  -  "+host, *stdin)
	r := flowio.New(c, m)
	r.Start()
	draw := func() {
		w, h := ui.Size(out.Fd())
		if w == 0 {
			w, h = 120, 37
		}
		drawFlow(out, m.View(time.Now()), w, h)
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
			if e.Key == ui.KeyQuit && *stdin {
				if m.Running() == "install" {
					m.Msg = "The install runs on; the UI cannot be left now. Use another shell."
					draw()
					continue
				}
				fmt.Fprint(out, "\x1b[0m\x1b[?25h\r\n")
				return
			}
			m.Handle(flow.Event{Key: flowKey(e.Key), Down: e.Down}, time.Now())
			r.Do(m.Take())
			draw()
		case <-tick.C:
			r.Poll()
			m.Tick(time.Now())
			if a := m.Take(); len(a) > 0 {
				r.Do(a)
			}
			draw()
		case <-quit:
			fmt.Fprint(out, "\x1b[0m\x1b[?25h\r\nrestart (test: no reboot)\r\n")
			return
		}
	}
}
