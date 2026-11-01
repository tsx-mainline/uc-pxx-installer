//go:build install

package main

// The device mode: p10inst plan|install --device /dev/mmcblk0. It runs only
// inside the installer RAM image and only on the eMMC of a UC-P10 (package
// device checks both). install writes only after a confirmation: --yes, or
// the word INSTALL typed on stdin. See internal/devmode for the order.

import (
	"flag"
	"fmt"
	"os"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/devmode"
)

type deviceArgs struct {
	fs   *flag.FlagSet
	o    devmode.Options
	libs libList
	env  func(o *devmode.Options) (*device.Env, error) // devtest builds only
}

func newDeviceArgs(name string, inst bool) *deviceArgs {
	a := &deviceArgs{fs: flag.NewFlagSet(name, flag.ExitOnError)}
	f := a.fs
	f.StringVar(&a.o.Device, "device", "", "the eMMC: must be /dev/mmcblk0")
	f.StringVar(&a.o.FW, "fw", "", "firmware file (pcdfw)")
	f.StringVar(&a.o.FWDrive, "fw-drive", "", "the P10-INSTALL drive file: the firmware is its one .bin, read in place; the drive must stay detached and unchanged to the end (instead of --fw)")
	f.StringVar(&a.o.FWDriveExpect, "fw-drive-expect", "", "with --fw-drive: the identity of the drive file when the firmware was read (size=…,mtime=…,ctime=…,dev=…,ino=…)")
	f.StringVar(&a.o.Keys, "keys", "", "key file (default: search the libraries in system_a/system_b of the eMMC)")
	f.Var(&a.libs, "lib", "library to find the key in (repeat)")
	f.StringVar(&a.o.Keeper, "keeper", "", "the keeper slot (a or b), when the rules cannot choose")
	f.BoolVar(&a.o.PrereqsConfirmed, "prereqs-confirmed", false, "the user turned off automatic firmware update")
	f.BoolVar(&a.o.PanelConfirmed, "panel-confirmed", false, "the user sees the installer's screen on the unit's panel (only boards with the panel supply on PMIC switch 2 are supported)")
	f.BoolVar(&a.o.AutoUpdateUnclearConfirmed, "autoupdate-unclear-confirmed", false, "the Android auto-update setting is not clear (no clear active nvram part or 72005), and the user confirmed to go on; never passes Auto Update Firmware on")
	f.StringVar(&a.o.JSONOut, "json", "", "write the plan as JSON to this file")
	if inst {
		f.StringVar(&a.o.PlanFile, "plan", "", "run only if the computed plan has the id of this plan file")
		f.BoolVar(&a.o.Yes, "yes", false, "the confirmation: do not ask for INSTALL")
		f.StringVar(&a.o.LogPath, "log", "", "JSON-lines log (default /run/p10inst/install-ID.jsonl); the transcript goes next to it, with .out for .jsonl")
	}
	a.env = devtestFlags(f)
	return a
}

func (a *deviceArgs) parse(args []string) *device.Env {
	a.fs.Parse(args)
	if a.fs.NArg() != 0 || a.o.Device == "" || (a.o.FW == "") == (a.o.FWDrive == "") || (a.o.FWDriveExpect != "" && a.o.FWDrive == "") {
		usage()
	}
	a.o.Libs = a.libs
	if a.env != nil {
		e, err := a.env(&a.o)
		if err != nil {
			fail(2, "%v", err)
		}
		if e != nil {
			return e
		}
	}
	return device.Live()
}

func cmdDevicePlan(args []string) {
	a := newDeviceArgs("plan", false)
	e := a.parse(args)
	_, err := devmode.Plan(e, a.o, os.Stdout)
	if err != nil {
		fmt.Printf("\nRESULT: %v\n", err)
		os.Exit(devmode.ExitCode(err))
	}
	fmt.Println("\nRESULT: plan OK (nothing written)")
}

func cmdDeviceInstall(args []string) {
	a := newDeviceArgs("install", true)
	e := a.parse(args)
	a.o.Confirm = os.Stdin
	_, err := devmode.Install(e, a.o, os.Stdout)
	if err != nil {
		fmt.Printf("\nRESULT: %v\n", err)
		os.Exit(devmode.ExitCode(err))
	}
	fmt.Println("\nRESULT: install done")
}

// cmdDeviceRepair is p10inst repair: the A/B repair of the hold
// (devmode.Repair, DEVMODE-R4.md §2). Exit 0 safe (no repair needed, or
// repaired and read back), 1 refused (nothing written), 3 the A/B state is
// not known after a write (do not power off), 5 a dry run that would repair.
func cmdDeviceRepair(args []string) {
	fs := flag.NewFlagSet("repair", flag.ExitOnError)
	var o devmode.RepairOptions
	fs.StringVar(&o.Device, "device", "", "the eMMC: must be /dev/mmcblk0")
	fs.StringVar(&o.PlanFile, "plan", "", "the plan JSON of the install that ended on the hold (made in this boot)")
	fs.StringVar(&o.LogDir, "log", "", "the log folder: repair-TIME.jsonl, .out, -ab-before.bin, -ab-after.bin")
	fs.BoolVar(&o.Yes, "yes", false, "the confirmation: do not ask for REPAIR")
	fs.BoolVar(&o.DryRun, "dry-run", false, "every check and the tool's dry run; write nothing (exit 5: a repair is needed and possible)")
	env := devtestFlags(fs)
	fs.Parse(args)
	if fs.NArg() != 0 || o.Device == "" || o.PlanFile == "" || o.LogDir == "" || (o.Yes && o.DryRun) {
		usage()
	}
	e := device.Live()
	if env != nil {
		var do devmode.Options
		te, err := env(&do)
		if err != nil {
			fail(2, "%v", err)
		}
		if te != nil {
			e = te
		}
		o.Hooks = do.Hooks
	}
	o.Confirm = os.Stdin
	res, err := devmode.Repair(e, o, os.Stdout)
	code := devmode.RepairExitCode(res, err)
	if err != nil {
		fmt.Printf("\nRESULT: %v\n", err)
	}
	os.Exit(code)
}

// cmdDeviceSwitch is p10inst switch: the slot switch (devmode.Switch,
// DEVMODE-R4.md §4). The exit codes are those of the repair.
func cmdDeviceSwitch(args []string) {
	fs := flag.NewFlagSet("switch", flag.ExitOnError)
	var o devmode.SwitchOptions
	fs.StringVar(&o.Device, "device", "", "the eMMC: must be /dev/mmcblk0")
	fs.StringVar(&o.To, "to", "", "the slot that boots next afterwards (a or b): it must be successful")
	fs.StringVar(&o.LogDir, "log", "", "the log folder: switch-TIME.jsonl, .out, -ab-before.bin, -ab-after.bin")
	fs.BoolVar(&o.Yes, "yes", false, "the confirmation: do not ask for SWITCH")
	fs.BoolVar(&o.DryRun, "dry-run", false, "every check and the tool's dry run; write nothing (exit 5: a switch is needed and possible)")
	env := devtestFlags(fs)
	fs.Parse(args)
	if fs.NArg() != 0 || o.Device == "" || o.To == "" || o.LogDir == "" || (o.Yes && o.DryRun) {
		usage()
	}
	e := device.Live()
	if env != nil {
		var do devmode.Options
		te, err := env(&do)
		if err != nil {
			fail(2, "%v", err)
		}
		if te != nil {
			e = te
		}
		o.Hooks = do.Hooks
	}
	o.Confirm = os.Stdin
	res, err := devmode.Switch(e, o, os.Stdout)
	code := devmode.RepairExitCode(res, err)
	if err != nil {
		fmt.Printf("\nRESULT: %v\n", err)
	}
	os.Exit(code)
}
