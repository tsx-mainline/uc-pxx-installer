//go:build install && step2 && finish

// uc-pxx-step2-finish: the finish executor of mainline step 2 (mainline-rc-4;
// owner decision 2026-11-11, Q-T8 = option 5). The finish build of
// uc-pxx-step2: the same package internal/step2, with only the finish and the
// target's part of the service (step2.Finish, step2.FinishService); it links
// no plan or start of step 2, no payload check and no install engine
// (ci/check-no-writer.py --step2-finish). It reads r2 at
// /run/uc-pxx-finish/uc-pxx-bootctl-inst (the reviewed sha256 20aa6bc5…, root,
// 0700).
//
//	go build -tags install,step2,finish -trimpath -ldflags "-s -w" ./cmd/uc-pxx-step2-finish
//
// A normal mainline build has no uc-pxx-step2. When it is an unfinished
// step-2 target, its uc-pxx-usb service receives this binary and r2 over the
// USB cable into RAM, checks both against the slot's own pinned list
// (/usr/share/uc-pxx-usb/finish.json), seals them in /run/uc-pxx-finish and
// runs `finish-service` from there. A supervised session may stage the same
// two files there by hand (sha256 checked) and run `finish --dry-run`, the
// rule-9 GO, then `finish --expect-id ID`. Nothing is installed.
//
//	uc-pxx-step2-finish finish --device /dev/mmcblk0 (--dry-run [--out FILE] | [--expect-id ID]) [--supervised] [--info FILE]
//	uc-pxx-step2-finish finish-service [--info FILE]
//
// Exit: 0 done; 1 refused, not yet, or not a step-2 target (nothing written);
// 2 usage; 3 the A/B state is not known: do not power off, do not restart,
// keep the unit powered and ask; 4 a finish STOP: keep the unit powered, do
// not restart, write nothing more, ask the owner.
//
// Its bytes do not depend on the gate's data (no gate is in it: the finish
// list of the USB set names its sha256; ci/check-fixedpoint.sh).
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"syscall"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/step2"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  uc-pxx-step2-finish finish --device /dev/mmcblk0 (--dry-run [--out FILE] | [--expect-id ID]) [--supervised] [--info FILE]
  uc-pxx-step2-finish finish-service [--info FILE]`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	o := step2.Options{Out: os.Stdout, Confirm: os.Stdin}
	fs.StringVar(&o.InfoFile, "info", "", "the info file of the USB interface")
	switch cmd {
	case "finish":
		fs.StringVar(&o.Device, "device", "", "the eMMC (/dev/mmcblk0)")
		fs.BoolVar(&o.DryRun, "dry-run", false, "plan the finish only")
		fs.StringVar(&o.JSONOut, "out", "", "write the finish plan")
		fs.StringVar(&o.ExpectID, "expect-id", "", "run only the finish plan of this id")
		fs.BoolVar(&o.Supervised, "supervised", false, "a supervised finish (needs --expect-id)")
	case "finish-service":
		o.Device = device.EMMCPath
	default:
		usage()
	}
	fs.Parse(os.Args[2:])
	if fs.NArg() != 0 {
		usage()
	}
	e, err := env(&o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "uc-pxx-step2-finish:", err)
		os.Exit(1)
	}
	// a write is never cut short by a signal of the terminal
	sigs := []os.Signal{syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGPIPE}
	ignore(sigs)
	switch cmd {
	case "finish":
		_, err = step2.Finish(e, o)
	case "finish-service":
		var r *step2.ServiceResult
		r, err = step2.FinishService(e, o, step2.ServiceEnv{Panel: step2.LivePanel})
		if r != nil {
			fmt.Println("uc-pxx-step2-finish finish-service:", r.What)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "uc-pxx-step2-finish:", err)
		switch {
		case errors.Is(err, step2.ErrStop):
			fmt.Fprintln(os.Stderr, "uc-pxx-step2-finish: STOP: keep the unit powered, do not restart, write nothing more, ask the owner")
			os.Exit(4)
		case errors.Is(err, install.ErrABUnknown):
			fmt.Fprintln(os.Stderr, "uc-pxx-step2-finish: THE A/B STATE IS NOT KNOWN: do not power off, do not restart; keep the unit powered and ask")
			os.Exit(3)
		}
		os.Exit(1)
	}
}
