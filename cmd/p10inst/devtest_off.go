//go:build install && !devtest

package main

import (
	"flag"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/devmode"
	"uc-pxx-installer/internal/flowio"
)

// A release install build has no test flag: the device mode always works on
// the real /dev, /sys and /proc of the unit.
const devtestUsage = ""

func devtestFlags(*flag.FlagSet) func(*devmode.Options) (*device.Env, error) { return nil }

// A release flow has no test flag: it always works on the real unit.
func flowTestFlags(*flag.FlagSet) func(*flowio.Config) bool { return nil }
