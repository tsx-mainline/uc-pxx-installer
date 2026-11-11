//go:build install && step2

// uc-pxx-step2: mainline step 2 from a running, successful mainline slot
// (MAINLINE-STEP2-DESIGN.md; MAINLINE-STEP2-REST-2026-11-10.md). It is a
// separate binary of uc-pxx-installer: go build -tags install,step2
// -trimpath -ldflags "-s -w" ./cmd/uc-pxx-step2. It links the engine (plan,
// install, the slot side of package device) and never the RAM device mode,
// pcdfw or the RAM flow. It refuses outside a slot boot.
//
//	uc-pxx-step2 plan   --device /dev/mmcblk0 --payload DIR (--confirmed | --auto) [--data auto|yes|no] [--out FILE]
//	uc-pxx-step2 start  --device /dev/mmcblk0 --payload DIR --plan FILE (--confirmed | --auto) [--data …] [--supervised]
//	uc-pxx-step2 finish --device /dev/mmcblk0 (--dry-run [--out FILE] | [--expect-id ID]) [--supervised]
//	uc-pxx-step2 service
//	uc-pxx-step2 pins
//
// pins prints the manifest sha256s of the payloads that the release gate of
// this binary lets step 2 install, one per line (the pins of uc-pxx-xfer, the
// step-2 USB transport). It reads nothing and writes nothing; it runs anywhere.
//
// Exit: 0 done; 1 refused or stopped with a known A/B state (the output says
// which slot boots), or a finish whose target has not marked itself yet; 2
// usage; 3 the A/B state is not known: do not power off, do not restart, keep
// the unit powered and ask; 4 a finish STOP (an RKSS range, the keeper, the
// layout or the kept key changed): keep the unit powered, do not restart,
// write nothing more, ask the owner.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/step2"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  uc-pxx-step2 plan   --device /dev/mmcblk0 --payload DIR (--confirmed | --auto) [--data auto|yes|no] [--out FILE]
  uc-pxx-step2 start  --device /dev/mmcblk0 --payload DIR --plan FILE (--confirmed | --auto) [--data auto|yes|no] [--supervised]
  uc-pxx-step2 finish --device /dev/mmcblk0 (--dry-run [--out FILE] | [--expect-id ID]) [--supervised]
  uc-pxx-step2 service
  uc-pxx-step2 pins`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd := os.Args[1]
	if cmd == "pins" {
		if len(os.Args) != 2 {
			usage()
		}
		for _, p := range mainline.EmbeddedGate().StepTwoManifests() {
			fmt.Println(p)
		}
		return
	}
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	o := step2.Options{Out: os.Stdout, Confirm: os.Stdin}
	fs.StringVar(&o.Device, "device", "", "the eMMC (/dev/mmcblk0)")
	switch cmd {
	case "plan", "start":
		fs.StringVar(&o.Payload, "payload", "", "the uc-pxx-mainline folder")
		fs.StringVar(&o.Data, "data", "auto", "the data steps: auto, yes or no")
		fs.BoolVar(&o.Confirmed, "confirmed", false, "the user confirmed (the 3 s hold of OK, or typed by the person)")
		fs.BoolVar(&o.Auto, "auto", false, "the allowed AUTO-STEP2 countdown ran out")
		if cmd == "plan" {
			fs.StringVar(&o.JSONOut, "out", "", "write the plan file")
		} else {
			fs.StringVar(&o.PlanFile, "plan", "", "the plan file of `plan`")
			fs.BoolVar(&o.Supervised, "supervised", false, "wait for a GO line before each A/B write and the data-format")
		}
	case "finish":
		fs.BoolVar(&o.DryRun, "dry-run", false, "plan the finish only")
		fs.StringVar(&o.JSONOut, "out", "", "write the finish plan")
		fs.StringVar(&o.ExpectID, "expect-id", "", "run only the finish plan of this id")
		fs.BoolVar(&o.Supervised, "supervised", false, "a supervised finish (needs --expect-id)")
	case "service":
		o.Device = device.EMMCPath
	default:
		usage()
	}
	fs.Parse(os.Args[2:])
	if fs.NArg() != 0 {
		usage()
	}
	e := device.SlotLive()
	var err error
	switch cmd {
	case "plan":
		_, err = step2.Plan(e, o)
	case "start":
		signal(func() { _, err = step2.Start(e, o) })
	case "finish":
		signal(func() { _, err = step2.Finish(e, o) })
	case "service":
		var r *step2.ServiceResult
		r, err = step2.Service(e, o, step2.ServiceEnv{Panel: step2.LivePanel, Reboot: func() error {
			syscall.Sync()
			return exec.Command("/sbin/reboot").Run()
		}})
		if r != nil {
			fmt.Println("uc-pxx-step2 service:", r.What)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "uc-pxx-step2:", err)
		if errors.Is(err, step2.ErrStop) {
			fmt.Fprintln(os.Stderr, "uc-pxx-step2: STOP: keep the unit powered, do not restart, write nothing more, ask the owner")
			os.Exit(4)
		}
		if errors.Is(err, install.ErrABUnknown) {
			fmt.Fprintln(os.Stderr, "uc-pxx-step2: THE A/B STATE IS NOT KNOWN: do not power off, do not restart; keep the unit powered and ask")
			os.Exit(3)
		}
		os.Exit(1)
	}
}

// signal ignores ^C, hangup, SIGTERM and SIGPIPE while f runs (a write is
// never cut short by a signal of the terminal).
func signal(f func()) {
	sigs := []os.Signal{syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGPIPE}
	ignore(sigs)
	defer reset(sigs)
	f()
}
