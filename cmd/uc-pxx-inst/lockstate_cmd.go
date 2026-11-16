//go:build install

package main

// uc-pxx-inst lockstate --device /dev/mmcblk0 (mainline-rc-10, I-1; owner
// R7): the in-RAM lock_state reader's one read-only read of this boot, for
// the Status screen of the on-screen flow (devmode.ReadLockStateAtStart). It
// prints LOCKSTATE: VALUE (0x00, 0x01, ..., or none) and exits 0 with a
// value or a recorded failed read, 1 on a refusal before the read, 3 when the
// read saw security, the A/B sector or the BCB change (keep the unit
// powered). It writes nothing to the eMMC and causes no TEE write.

import (
	"flag"
	"fmt"
	"os"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/devmode"
)

func init() {
	extraCmds["lockstate"] = cmdLockState
	extraUsage += "  uc-pxx-inst lockstate --device /dev/mmcblk0   the in-RAM lock_state read of this boot (reads only)\n"
}

func cmdLockState(args []string) {
	fs := flag.NewFlagSet("lockstate", flag.ExitOnError)
	var o devmode.MainlineOptions
	fs.StringVar(&o.Device, "device", "", "the eMMC: must be /dev/mmcblk0")
	envf := devtestFlags(fs)
	lockf, _ := lockTestFlag(fs)
	fs.Parse(args)
	if fs.NArg() != 0 || o.Device == "" {
		usage()
	}
	e := device.Live()
	if envf != nil {
		te, err := envf(&o.Options)
		if err != nil {
			fail(2, "%v", err)
		}
		if te != nil {
			e = te
		}
	}
	if lockf != nil {
		o.LockReader = lockf(e.IsTest())
	}
	r, err := devmode.ReadLockStateAtStart(e, o, os.Stdout)
	if r != nil {
		v := r.LockState
		if v == "" {
			v = "none"
		}
		fmt.Printf("LOCKSTATE: %s\n", v)
		if r.Error != "" {
			fmt.Printf("LOCKSTATE-ERROR: %s\n", r.Error)
		}
		if r.Changed {
			fmt.Println("LOCKSTATE-CHANGED: yes")
		}
	}
	if err != nil {
		fmt.Printf("\nRESULT: %v\n", err)
		os.Exit(devmode.ExitCode(err))
	}
	fmt.Println("\nRESULT: lock state read (nothing written)")
}
