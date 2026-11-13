// SPDX-License-Identifier: GPL-2.0-or-later

// Command uc-pxx-send sends a step-2 payload or a finish set to a running
// mainline slot over its uc-pxx-xfer USB interface, from a host with no
// browser (the CLI form of the installer page's step-2 and finish sections).
// It never writes the unit's storage; step 2 and the finish start only on the
// unit. It is drivable from a build host over the USB-C cable.
//
//	uc-pxx-send payload --dir DIR/uc-pxx-mainline [--auto] [--dev BUS:ADDR]
//	uc-pxx-send finish  --dir DIR/uc-pxx-finish    [--auto] [--dev BUS:ADDR]
//	uc-pxx-send hello   [--dev BUS:ADDR]            (read only: print the unit's state and pins)
//
// With no --dev the one uc-pxx-xfer device on the host is used (vendor id
// 1d6b:0105). The tool opens no block device. For a payload the unit must be
// in mode "payload" (a STEP-2 keeper waiting for its payload); for a finish
// set it must be in mode "finish" (an unfinished step-2 target on its first
// boot). The unit checks every file against the pins it names in HELLO.
//
// NOT FOR USE ON A UNIT until the Opus review that the step-2 USB transport
// asks for and an owner RULES line; the installer page keeps STEP2_USB_ENABLED
// and FINISH_USB_ENABLED false. This command is the host counterpart of that
// reviewed, owner-gated interface.
package main

import (
	"flag"
	"fmt"
	"os"

	"uc-pxx-installer/internal/sender"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	dir := fs.String("dir", "", "the folder of the set (uc-pxx-mainline for a payload, uc-pxx-finish for a finish set)")
	dev := fs.String("dev", "", "the USB device BUS:ADDR (default: the one uc-pxx-xfer device on the host)")
	auto := fs.Bool("auto", false, "ask the unit to continue by itself where the slot allows it (the default is the panel hold)")
	fs.Parse(os.Args[2:])

	log := func(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) }

	t, err := openUSB(*dev)
	if err != nil {
		fail("opening the uc-pxx-xfer device: %v", err)
	}
	defer t.Close()
	d := sender.New(t, log)

	switch cmd {
	case "hello":
		h, err := d.Hello()
		if err != nil {
			fail("%v", err)
		}
		fmt.Printf("proto %d\nusb %s\nmode %s\nstate %s\nroom %d\npins:\n", h.Proto, h.USB, h.Mode, h.State, h.Room)
		for _, p := range h.Pins {
			fmt.Printf("  %s\n", p)
		}
	case "payload", "finish":
		if *dir == "" {
			usage()
		}
		kind := sender.Payload
		if cmd == "finish" {
			kind = sender.Finish
		}
		first, err := d.Send(kind, *dir, *auto)
		if err != nil {
			fail("%v", err)
		}
		fmt.Printf("done: the unit took the %s (first-file sha256 %s)\n", cmd, first)
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: uc-pxx-send payload|finish --dir DIR [--auto] [--dev BUS:ADDR]")
	fmt.Fprintln(os.Stderr, "       uc-pxx-send hello [--dev BUS:ADDR]")
	os.Exit(2)
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "uc-pxx-send: "+format+"\n", a...)
	os.Exit(1)
}
