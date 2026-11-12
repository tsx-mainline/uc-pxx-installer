//go:build install && !devtest

package main

import (
	"flag"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/devmode"
	"uc-pxx-installer/internal/flowio"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/plan"
)

// A release install build has no test flag: the device mode always works on
// the real /dev, /sys and /proc of the unit.
const devtestUsage = ""

func devtestFlags(*flag.FlagSet) func(*devmode.Options) (*device.Env, error) { return nil }

// A release flow has no test flag: it always works on the real unit.
func flowTestFlags(*flag.FlagSet) func(*flowio.Config) bool { return nil }

// A release install build has no devtest flag (boolFlags).
func devtestBoolFlag(string) bool { return false }

// A release build has only the release's payload gate (mainline.EmbeddedGate).
func mainlineTestGate(*flag.FlagSet) func() *mainline.Gate { return nil }

// A release build gives the stash child no test argument (stashChildTestFlags).
func stashChildTestFlags(*flag.FlagSet) {}

// A release build has no stub of the lock_state reader: the device mode runs
// the image's reader, the image mode has none (lockTestFlag).
func lockTestFlag(*flag.FlagSet) (func(bool) devmode.LockReader, func() *plan.LockStateRead) {
	return nil, nil
}

// A release build has no stub of the lock_state writer: the device mode runs
// the image's writer (lockwrite.go teeWrite).
func writeTestFlag(*flag.FlagSet) func(bool) devmode.LockWriter { return nil }
