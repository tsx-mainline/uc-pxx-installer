//go:build install && step2 && finish && !devtest

package main

import (
	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/step2"
)

// env is the environment of a running slot (a release build has no other).
func env(*step2.Options) (*device.Env, error) { return device.SlotLive(), nil }
