//go:build xfer

// uc-pxx-xfer: the step-2 USB transport T-A (STEP2-USB-TRANSPORT-2026-11-11.md;
// owner answers 2026-11-11). A separate binary of uc-pxx-installer:
//
//	go build -tags xfer -trimpath -ldflags "-s -w" ./cmd/uc-pxx-xfer
//
// It links no eMMC code (ci/check-no-writer.py --xfer): no device layer, no
// install engine, no A/B tool, no step-2 engine. It receives a normal mainline
// payload from the installer page over WebUSB into a RAM folder and hands the
// complete, checked folder to the root step-2 service (internal/xfer
// CheckReady; the seal is the service's).
//
//	uc-pxx-xfer run [--stage DIR] [--user NAME] [--pin SHA256 ...] [--idle 30s] [--info FILE] [--phy auto|rk3399|none]
//	uc-pxx-xfer run --status-only [--info FILE] ...
//	uc-pxx-xfer check-ready [--stage DIR] [--user NAME] --pin SHA256 ...
//	uc-pxx-xfer cleanup [--stage DIR]
//	uc-pxx-xfer usb-id
//
// check-ready (read only, for the root seal of the step-2 service and the
// tests): internal/xfer CheckReady of the receiver's hand-over in the stage;
// it prints the record as JSON. Exit 0 ready, 1 not.
//
// cleanup (root): takes apart what a run that was killed left (the gadget,
// the FunctionFS mount, the incoming folders; never the ready folder), so that
// run can start again.
//
// run (root): the gadget in configfs, FunctionFS mounted for the receiver's
// user, the receiver started as that user (`serve`, never by hand), the PHY
// rule, until SIGTERM. --pin: the manifest sha256s of the step-2 gate
// (`uc-pxx-step2 pins`); with none, every manifest is refused.
//
// Exit: 0 stopped by a signal; 1 an error (the gadget is taken apart); 2 usage.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"uc-pxx-installer/internal/xfer"
)

type pinList []string

func (p *pinList) String() string     { return strings.Join(*p, ",") }
func (p *pinList) Set(v string) error { *p = append(*p, v); return nil }

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  uc-pxx-xfer run [--stage DIR] [--user NAME] [--pin SHA256 ...] [--idle 30s] [--info FILE] [--phy auto|rk3399|none]
  uc-pxx-xfer run --status-only [--info FILE] [--user NAME] [--phy ...]
  uc-pxx-xfer check-ready [--stage DIR] [--user NAME] --pin SHA256 ...
  uc-pxx-xfer cleanup [--stage DIR]
  uc-pxx-xfer usb-id`)
	os.Exit(2)
}

func logf(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "%s uc-pxx-xfer: %s\n", time.Now().UTC().Format("15:04:05.000"), fmt.Sprintf(f, a...))
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "run":
		os.Exit(run(os.Args[2:]))
	case "serve":
		os.Exit(serve(os.Args[2:]))
	case "check-ready":
		os.Exit(checkReady(os.Args[2:]))
	case "cleanup":
		os.Exit(cleanup(os.Args[2:]))
	case "usb-id":
		fmt.Printf("%04x:%04x\n", xfer.USBVendorID, xfer.USBProductID)
	default:
		usage()
	}
}

func run(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	var pins pinList
	c := xfer.SuperviseConfig{ChildLog: os.Stderr, Logf: logf}
	fs.StringVar(&c.Stage, "stage", xfer.DefaultStage, "the tmpfs stage (mounted by the step-2 service)")
	fs.StringVar(&c.User, "user", xfer.DefaultUser, "the receiver's user")
	fs.StringVar(&c.ConfigFS, "configfs", xfer.DefaultConfigFS, "the gadget folder")
	fs.StringVar(&c.FFS, "ffs", xfer.DefaultFFS, "the FunctionFS mount point")
	fs.Var(&pins, "pin", "a manifest sha256 of the step-2 gate (repeat)")
	fs.DurationVar(&c.Idle, "idle", xfer.DefaultIdle, "end a session with a partial folder after this idle time")
	fs.Int64Var(&c.Reserve, "reserve", xfer.DefaultReserve, "RAM bytes kept free")
	fs.BoolVar(&c.StatusOnly, "status-only", false, "after the seal: HELLO and STATUS only")
	fs.StringVar(&c.InfoFile, "info", "", "the step-2 service's info file for HELLO/STATUS")
	fs.StringVar(&c.PHY, "phy", "auto", "the USB PHY rule: auto, rk3399 or none")
	fs.Parse(args)
	if fs.NArg() != 0 {
		usage()
	}
	c.Pins = pins
	self, err := os.Executable()
	if err != nil {
		logf("%v", err)
		return 1
	}
	c.Self = self
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	signal.Ignore(syscall.SIGPIPE)
	if err := xfer.Supervise(c, stop); err != nil {
		logf("%v", err)
		return 1
	}
	return 0
}

func cleanup(args []string) int {
	fs := flag.NewFlagSet("cleanup", flag.ExitOnError)
	c := xfer.SuperviseConfig{}
	fs.StringVar(&c.Stage, "stage", xfer.DefaultStage, "the tmpfs stage")
	fs.StringVar(&c.ConfigFS, "configfs", xfer.DefaultConfigFS, "the gadget folder")
	fs.StringVar(&c.FFS, "ffs", xfer.DefaultFFS, "the FunctionFS mount point")
	fs.Parse(args)
	if fs.NArg() != 0 {
		usage()
	}
	if err := xfer.Cleanup(c); err != nil {
		logf("cleanup: %v", err)
		return 1
	}
	return 0
}

func checkReady(args []string) int {
	fs := flag.NewFlagSet("check-ready", flag.ExitOnError)
	var pins pinList
	stage := fs.String("stage", xfer.DefaultStage, "the tmpfs stage")
	name := fs.String("user", xfer.DefaultUser, "the receiver's user")
	fs.Var(&pins, "pin", "a manifest sha256 of the step-2 gate (repeat)")
	fs.Parse(args)
	if fs.NArg() != 0 {
		usage()
	}
	id, err := xfer.LookupUser(*name)
	if err != nil {
		logf("%v", err)
		return 1
	}
	r, err := xfer.CheckReady(filepath.Join(*stage, xfer.XferDir), id.UID, pins)
	if err != nil {
		logf("not ready: %v", err)
		return 1
	}
	b, _ := json.MarshalIndent(r, "", " ")
	fmt.Println(string(b))
	return 0
}

const prSetNoNewPrivs = 38

// serve is the receiver, started by run as the receiver's user. It refuses to
// run as root.
func serve(args []string) int {
	if _, _, e := syscall.RawSyscall6(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0, 0, 0, 0); e != 0 {
		logf("no_new_privs: %v", e)
		return 1
	}
	if os.Getuid() == 0 || os.Geteuid() == 0 || os.Getgid() == 0 || os.Getegid() == 0 {
		logf("serve refuses to run as root (run starts it as its own user)")
		return 1
	}
	syscall.Umask(0o077)
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	var pins pinList
	ffs := fs.String("ffs", "", "the FunctionFS mount")
	cfg := xfer.Config{Logf: logf}
	fs.StringVar(&cfg.Dir, "dir", "", "the receiver's folder")
	fs.Var(&pins, "pin", "a manifest sha256")
	fs.DurationVar(&cfg.Idle, "idle", xfer.DefaultIdle, "idle time")
	fs.Int64Var(&cfg.Reserve, "reserve", xfer.DefaultReserve, "RAM bytes kept free")
	fs.BoolVar(&cfg.StatusOnly, "status-only", false, "status only")
	fs.StringVar(&cfg.InfoFile, "info", "", "info file")
	fs.Parse(args)
	if fs.NArg() != 0 || *ffs == "" || (cfg.Dir == "") != cfg.StatusOnly {
		usage()
	}
	cfg.Pins = pins
	ctl := os.NewFile(3, "control")
	if ctl == nil {
		logf("no control pipe")
		return 1
	}
	cfg.Reconnect = func() { fmt.Fprintln(ctl, "reconnect") }
	r, err := xfer.NewReceiver(cfg)
	if err != nil {
		logf("%v", err)
		return 1
	}
	stop := make(chan struct{})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	signal.Ignore(syscall.SIGPIPE)
	go func() {
		s := <-sig
		logf("signal %v", s)
		close(stop)
	}()
	d := xfer.Daemon{Dir: *ffs, R: r, Logf: logf, Ready: func() { fmt.Fprintln(ctl, "ready") }}
	err = d.Run(stop)
	if err != nil {
		logf("%v", err)
		return 1
	}
	return 0
}
