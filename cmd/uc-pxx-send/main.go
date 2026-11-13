// SPDX-License-Identifier: GPL-2.0-or-later

// Command uc-pxx-send sends a step-2 payload or a finish set to a running
// mainline slot over its uc-pxx-xfer USB interface, from a host with no
// browser (the CLI form of the installer page's step-2 and finish sections).
// It never writes the unit's storage; step 2 and the finish start only on the
// unit, by its own rules (the panel's hold, or on a supervised unit the
// supervised CLI with its rule-9 GO). It is drivable from a build host over
// the USB-C cable.
//
//	uc-pxx-send payload --dir DIR/uc-pxx-mainline --expect-sha256 SHA [--dev BUS:ADDR]
//	uc-pxx-send finish  --dir DIR/uc-pxx-finish   --expect-sha256 SHA [--dev BUS:ADDR]
//	uc-pxx-send hello   [--dev BUS:ADDR]           (read only: print the unit's state and pins)
//
// --expect-sha256 is the sha256 of the set's first file (manifest.json or
// finish.json): the set that the session's RULES line names. The set is
// checked on the host first (every file a regular file, opened once, its size
// and sha256), and only then is the device opened. The unit's HELLO must name
// the reviewed proto and USB id, the mode of the set and that sha among its
// pins. There is no option that asks the unit to go on by itself (RC-6 sender
// review MAJOR-1). With no --dev the one uc-pxx-xfer device on the host is
// used (1d6b:0105). The tool opens no block device.
//
// Exit codes: 0 the unit took the set into its RAM (not: step 2 or the finish
// ran); 2 usage; 3 refused before any byte of the set was sent; 4 the USB
// link failed; 5 the unit refused a file or the set.
//
// NOT FOR USE ON A UNIT until an owner RULES line names this build; the
// installer page keeps STEP2_USB_ENABLED and FINISH_USB_ENABLED false.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"uc-pxx-installer/internal/sender"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) < 1 {
		return usage()
	}
	cmd := args[0]
	if cmd != "hello" && cmd != "payload" && cmd != "finish" {
		return usage()
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	dir := fs.String("dir", "", "the folder of the set (uc-pxx-mainline for a payload, uc-pxx-finish for a finish set)")
	expect := fs.String("expect-sha256", "", "the sha256 of the set's first file (manifest.json or finish.json): the set the RULES line names")
	dev := fs.String("dev", "", "the USB device BUS:ADDR (default: the one uc-pxx-xfer device on the host)")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		return usage()
	}
	log := func(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) }

	// everything is checked before the device is opened (review MINOR-3)
	var set *sender.Set
	if cmd != "hello" {
		if *dir == "" || *expect == "" {
			return usage()
		}
		kind := sender.Payload
		if cmd == "finish" {
			kind = sender.Finish
		}
		var err error
		if set, err = sender.PrepareSet(kind, *dir, *expect); err != nil {
			return fail(err)
		}
		defer set.Close()
		log("the set: %s, %d bytes, first-file sha256 %s (checked on the host)", cmd, set.Size(), set.First)
	} else if *dir != "" || *expect != "" {
		return usage()
	}

	t, err := openUSB(*dev)
	if err != nil {
		return fail(fmt.Errorf("%w: opening the uc-pxx-xfer device: %v", sender.ErrTransport, err))
	}
	defer t.Close()
	d := sender.New(t, log)
	if cmd == "hello" {
		h, err := d.Hello()
		if err != nil {
			return fail(err)
		}
		fmt.Printf("proto %d\nusb %s\nmode %s\nstate %s\nroom %d\npins:\n", h.Proto, h.USB, h.Mode, h.State, h.Room)
		for _, p := range h.Pins {
			fmt.Printf("  %s\n", p)
		}
		return 0
	}
	if err := d.Send(set); err != nil {
		return fail(err)
	}
	fmt.Printf("done: the unit took the %s into its RAM (first-file sha256 %s)\n", cmd, set.First)
	return 0
}

func usage() int {
	fmt.Fprintln(os.Stderr, "usage: uc-pxx-send payload|finish --dir DIR --expect-sha256 SHA [--dev BUS:ADDR]")
	fmt.Fprintln(os.Stderr, "       uc-pxx-send hello [--dev BUS:ADDR]")
	return 2
}

// fail prints the error and gives its exit code: 3 refused before any byte,
// 4 the USB link, 5 the unit refused.
func fail(err error) int {
	fmt.Fprintf(os.Stderr, "uc-pxx-send: %v\n", err)
	switch {
	case errors.Is(err, sender.ErrLocal):
		return 3
	case errors.Is(err, sender.ErrUnit):
		return 5
	default:
		return 4
	}
}
