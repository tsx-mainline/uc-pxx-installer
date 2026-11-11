//go:build install && step2 && !devtest

package step2_test

// The release build of step 2 (go test -tags install,step2): every test
// option of Options is refused before anything is read, so a release binary
// cannot run with a test gate, test key pins, a fake data tool or power-cut
// hooks.

import (
	"strings"
	"testing"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/plan"
	"uc-pxx-installer/internal/step2"
)

func TestReleaseRefusesTestOptions(t *testing.T) {
	for name, o := range map[string]step2.Options{
		"Hooks":     {Hooks: &install.Hooks{}},
		"Gate":      {Gate: mainline.EmbeddedGate()},
		"KeyPins":   {KeyPins: []plan.KeyPin{}},
		"Live":      {Live: &step2.Live{}},
		"FinishCut": {FinishCut: &install.Cutter{}},
	} {
		o.Device = "/dev/mmcblk0"
		for _, f := range []func() error{
			func() error { _, err := step2.Plan(device.SlotLive(), o); return err },
			func() error { _, err := step2.Start(device.SlotLive(), o); return err },
			func() error { _, err := step2.Finish(device.SlotLive(), o); return err },
		} {
			if err := f(); err == nil || !strings.Contains(err.Error(), "devtest build") {
				t.Fatalf("%s: %v", name, err)
			}
		}
	}
}
