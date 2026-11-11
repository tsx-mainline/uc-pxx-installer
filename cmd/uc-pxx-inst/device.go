package main

// The commands that run on the device, in the RAM image: status, readonly,
// ui and usbd (and loader in the status build, loader.go). None of them
// writes to the eMMC. They open the eMMC O_RDONLY
// only (package blk), and readonly sets the kernel read-only flag.

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"uc-pxx-installer/internal/blk"
	"uc-pxx-installer/internal/devmem"
	"uc-pxx-installer/internal/gadget"
	"uc-pxx-installer/internal/manifest"
	"uc-pxx-installer/internal/status"
	"uc-pxx-installer/internal/ui"
)

const runDir = "/run/uc-pxx-inst"

var logPath = filepath.Join(runDir, "uc-pxx-inst.log")

// logf writes one line to the installer log and to the kernel log.
func logf(format string, a ...any) {
	line := fmt.Sprintf(format, a...)
	ts := time.Now().Format("15:04:05")
	if f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644); err == nil {
		fmt.Fprintf(f, "%s %s\n", ts, line)
		f.Close()
	}
	if k, err := os.OpenFile("/dev/kmsg", os.O_WRONLY, 0); err == nil {
		fmt.Fprintf(k, "uc-pxx-inst: %s\n", line)
		k.Close()
	}
}

func source(root string) status.Source {
	ms, err := manifest.Embedded()
	if err != nil {
		fail(2, "manifests: %v", err)
	}
	return status.Source{Root: root, Manifests: ms}
}

func cmdStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	root := fs.String("root", "/", "root of /sys, /dev and /proc (tests)")
	fs.Parse(args)
	r := status.Collect(source(*root))
	fmt.Print(r.Text())
	fmt.Println()
	for _, l := range liveLines(ui.FindTouch(*root)) {
		fmt.Println(l.Text)
	}
	if !r.ABValid {
		os.Exit(1)
	}
}

func cmdReadonly(args []string) {
	fs := flag.NewFlagSet("readonly", flag.ExitOnError)
	root := fs.String("root", "/", "root of /sys and /dev (tests)")
	fs.Parse(args)
	d, err := blk.Find(*root)
	if err != nil {
		fail(1, "%v", err)
	}
	done, err := d.SetReadOnly()
	fmt.Printf("read-only: %s\n", strings.Join(done, " "))
	if err != nil {
		fail(1, "%v", err)
	}
}

// liveLines is the part of the status that changes: USB link, keys and the
// boot-mode register.
func liveLines(touch string) []status.Line {
	var out []status.Line
	add := func(level int, f string, a ...any) {
		out = append(out, status.Line{Level: level, Text: fmt.Sprintf(f, a...)})
	}
	add(status.Heading, "Link and keys")
	usb, err := os.ReadFile(filepath.Join(runDir, "usb.state"))
	if err != nil {
		add(status.Warn, "  USB        no USB gadget (usbd is not running)")
	} else {
		add(status.Normal, "  USB        %s", strings.TrimSpace(string(usb)))
	}
	if touch != "" {
		add(status.Normal, "  Keys       touch keys ready (%s)", touch)
	} else {
		add(status.Bad, "  Keys       no touch keys: use the USB serial shell and run: uc-pxx-inst ui --stdin")
	}
	if v, err := devmem.Read32(devmem.BootModeReg); err == nil {
		add(status.Normal, "  Boot mode  register %#08x = %s. A plain restart writes \"normal\".", v, devmem.BootModeName(v))
	} else {
		add(status.Warn, "  Boot mode  cannot read the register: %v", err)
	}
	return out
}

// logRows is how many rows of the logs the log page holds (it scrolls).
const logRows = 120

func tailLog(n int) []string {
	var out []string
	if b, err := os.ReadFile(logPath); err == nil {
		out = append(out, lastLines(string(b), n/2)...)
	}
	if b, err := exec.Command("dmesg").Output(); err == nil {
		out = append(out, "--- kernel log ---")
		out = append(out, lastLines(string(b), n-len(out))...)
	}
	return out
}

func lastLines(s string, n int) []string {
	l := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if n <= 0 {
		return nil
	}
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return l
}

func setLED(name string, on bool) {
	d := filepath.Join("/sys/class/leds", name)
	os.WriteFile(filepath.Join(d, "trigger"), []byte("none"), 0)
	v := "0"
	if on {
		v = "1"
	}
	os.WriteFile(filepath.Join(d, "brightness"), []byte(v), 0)
}

func cmdUI(args []string) {
	fs := flag.NewFlagSet("ui", flag.ExitOnError)
	tty := fs.String("tty", "/dev/tty1", "terminal to draw on")
	stdin := fs.Bool("stdin", false, "draw on this terminal and read keys from it (the USB serial shell)")
	root := fs.String("root", "/", "root of /sys, /dev and /proc (tests)")
	noReboot := fs.Bool("no-reboot", false, "exit instead of the restart (tests)")
	leds := fs.Bool("leds", false, "show the result on the status LED")
	fs.Parse(args)

	out := os.Stdout
	if !*stdin {
		f, err := os.OpenFile(*tty, os.O_RDWR, 0)
		if err != nil {
			fail(2, "%v", err)
		}
		out = f
		// No echo on the screen, as uc-pxx-inst flow (owner feedback R3 item 2):
		// the console keyboard also gets the touch keys (Vol- is F14,
		// ESC [ 26 ~, Mic F13), and tty1 echoed them after the last cell of
		// the screen, which scrolled the page up by one row. The status UI
		// reads its keys from the touch device, never from this terminal.
		if restore, err := ui.Raw(f.Fd()); err == nil {
			defer restore()
		} else {
			logf("ui: %s: echo not turned off: %v", *tty, err)
		}
		// no screen blanking, no power-down, clear the screen
		fmt.Fprint(out, "\x1b[9;0]\x1b[14;0]\x1b[2J")
	}
	events := make(chan ui.Event, 32)
	touch := make(chan string, 4)
	if *stdin {
		restore, err := ui.Raw(os.Stdin.Fd())
		if err == nil {
			defer restore()
		}
		go ui.ReadTerm(os.Stdin, events)
	} else {
		go ui.WatchTouch(*root, events, func(dev string) {
			touch <- dev
			if dev != "" {
				logf("touch keys on %s", dev)
			}
		})
	}
	host, _ := os.Hostname()
	m := &ui.Model{Title: imageName(*root) + " installer  -  status (read-only)  -  " + host, Terminal: *stdin}
	// the key row of the model's screen, from the RAM image's device tree
	// (the rule of uc-pxx-inst flow): a UC-P8 image gets the UC-P8 row
	compat, _ := os.ReadFile(filepath.Join(*root, "proc/device-tree/compatible"))
	m.Panel = ui.PanelFor(string(compat))
	// the page "Download mode": the status build only (nil in the install build)
	m.Loader = loaderPage(*root)
	reports := make(chan *status.Report, 1)
	go func() {
		start := time.Now()
		r := status.Collect(source(*root))
		logf("status read in %s, A/B valid %v, next slot %d", time.Since(start).Round(time.Millisecond), r.ABValid, r.Next)
		reports <- r
	}()
	touchDev := ""
	if *stdin {
		touchDev = "(terminal)"
	}
	m.Extra = liveLines(touchDev)
	draw := func() {
		w, h := ui.Size(out.Fd())
		if w == 0 {
			w, h = 120, 37
		}
		m.Draw(out, w, h, time.Now())
	}
	draw()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	live := time.NewTicker(2 * time.Second)
	defer live.Stop()
	for {
		select {
		case r := <-reports:
			m.Report = r
			if *leds {
				setLED("blue", false)
				setLED("green", r.ABValid)
				setLED("red", !r.ABValid)
			}
			draw()
		case d := <-touch:
			touchDev = d
			m.Extra = liveLines(touchDev)
			draw()
		case e := <-events:
			if e.Key == ui.KeyQuit && *stdin {
				fmt.Fprint(out, "\x1b[0m\x1b[?25h\r\n")
				return
			}
			m.Handle(e, time.Now())
			if m.Page == ui.PageLog {
				m.Log = tailLog(logRows)
			}
			draw()
		case <-tick.C:
			if m.Tick(time.Now()) {
				draw()
			}
		case <-live.C:
			m.Extra = liveLines(touchDev)
			if m.Page == ui.PageLog {
				m.Log = tailLog(logRows)
			}
			draw()
		}
		if m.Restart {
			draw()
			logf("restart asked on the %s", map[bool]string{true: "terminal", false: "device keys"}[*stdin])
			if *noReboot {
				fmt.Fprint(out, "\x1b[0m\x1b[?25h\r\n")
				return
			}
			restart()
			return
		}
		if m.RestartLoader {
			draw()
			logf("restart into the download mode asked on the %s", map[bool]string{true: "terminal", false: "device keys"}[*stdin])
			if *noReboot {
				fmt.Fprint(out, "\x1b[0m\x1b[?25h\r\nrestart into the download mode (test: no reboot)\r\n")
				return
			}
			err := loaderFromUI(*root) // returns only on a refusal or an error
			logf("restart into the download mode: %v", err)
			m.RestartLoader = false
			m.Loader = loaderPage(*root)
			m.Msg = fmt.Sprintf("No restart: %v", err)
			draw()
		}
	}
}

// restart asks busybox init for a plain restart. The kernel then writes the
// "normal" boot mode (the reboot-mode node of the device tree), so U-Boot
// boots the slot that the A/B metadata selects.
func restart() {
	syscall.Sync()
	if err := exec.Command("/sbin/reboot").Run(); err != nil {
		logf("reboot: %v, using reboot(2)", err)
		syscall.Reboot(syscall.LINUX_REBOOT_CMD_RESTART)
	}
	time.Sleep(time.Minute)
}

func cmdUsbd(args []string) {
	fs := flag.NewFlagSet("usbd", flag.ExitOnError)
	drive := fs.String("drive", "", "RAM file behind the UC-PXX-INST drive (empty = no drive)")
	netUp := fs.String("net-up", "/etc/uc-pxx-inst/usb-net-up", "script to run once when usb0 appears")
	cfs := fs.String("configfs", "/sys/kernel/config/usb_gadget/uc-pxx-inst", "gadget folder")
	once := fs.Bool("once", false, "make the gadget, bind it if possible, and exit")
	fs.Parse(args)
	os.MkdirAll(runDir, 0o755)
	c := gadget.Config{
		Root: *cfs, Serial: "P10INST", Product: "UC-P10 installer",
		Drive: *drive, ACM: true, NCM: true,
		HostMAC: "02:70:31:30:00:01", DeviceMAC: "02:70:31:30:00:02",
	}
	if err := gadget.New(c); err != nil {
		logf("usb gadget: %v", err)
		writeState("USB gadget failed: " + err.Error())
		fail(1, "%v", err)
	}
	fn := "serial, network"
	if *drive != "" {
		fn += ", drive UC-PXX-INST"
	}
	logf("usb gadget made (%s)", fn)
	prevActive, netDone, last := false, false, ""
	for {
		udc := gadget.FirstUDC("/")
		active, perr := devmem.USBPHYActive()
		bound := gadget.Bound(*cfs)
		switch {
		case udc == "":
		case bound == "" && active:
			if err := gadget.Bind(*cfs, udc); err != nil {
				logf("bind %s: %v", udc, err)
			} else {
				logf("PHY active: gadget bound to %s", udc)
				bound = udc
			}
		case bound != "" && active && !prevActive:
			if err := gadget.Reconnect("/", bound); err != nil {
				logf("reconnect %s: %v", bound, err)
			} else {
				logf("PHY active again: %s reconnected", bound)
			}
		}
		prevActive = active
		if !netDone {
			if _, err := os.Stat("/sys/class/net/usb0"); err == nil {
				if b, err := exec.Command(*netUp, "usb0").CombinedOutput(); err != nil {
					logf("%s: %v: %s", *netUp, err, strings.TrimSpace(string(b)))
				} else {
					logf("usb0 up: %s", strings.TrimSpace(string(b)))
				}
				netDone = true
			}
		}
		st := gadget.UDCState("/", udc)
		var s string
		switch {
		case udc == "":
			s = "no USB controller yet"
		case bound == "":
			s = "waiting for a cable (" + fn + ")"
			if perr != nil {
				s = "cannot read the USB PHY state: " + perr.Error()
			}
		default:
			s = st + " (" + fn + ")"
			if st == "configured" {
				s = "connected: " + fn + ". Shell on the serial port, ssh root@10.99.0.2"
			}
		}
		if *drive != "" && gadget.Ejected(*cfs) {
			s += ". The drive was ejected"
		}
		if s != last {
			writeState(s)
			logf("usb: %s", s)
			last = s
		}
		if *once {
			return
		}
		time.Sleep(time.Second)
	}
}

func writeState(s string) {
	tmp := filepath.Join(runDir, "usb.state.tmp")
	if os.WriteFile(tmp, []byte(s+"\n"), 0o644) == nil {
		os.Rename(tmp, filepath.Join(runDir, "usb.state"))
	}
}

// imageName is the model of this RAM image for the title: "UC-P8" in the
// UC-P8 image (its device tree), else "UC-P10" as before.
func imageName(root string) string {
	b, _ := os.ReadFile(filepath.Join(root, "proc/device-tree/model"))
	if strings.TrimRight(string(b), "\x00\n ") == "Crestron UC-P8" {
		return "UC-P8"
	}
	return "UC-P10"
}
