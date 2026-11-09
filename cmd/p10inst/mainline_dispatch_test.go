//go:build install

package main

// The mainline dispatch of mainline_install.go in the install build (no
// devtest needed): the flag scanner of the mode choice.

import (
	"flag"
	"reflect"
	"testing"
)

// TestScanFlags (review of 01b3841, finding 2): a flag's value and the
// arguments after the flags are never flags; the mode choice reads flags
// as package flag does.
func TestScanFlags(t *testing.T) {
	for _, c := range []struct {
		args []string
		ml   bool
	}{
		{[]string{"--dry-run", "--image", "/nx", "--log", "mainline", "--fw", "x"}, false}, // the review's case
		{[]string{"--log", "--mainline", "--fw", "x"}, false},                              // a value that looks like the flag
		{[]string{"--image", "x", "y", "--mainline", "d"}, false},                          // after the first argument that is no flag
		{[]string{"--", "--mainline", "d"}, false},
		{[]string{"-", "--mainline", "d"}, false},
		{[]string{"---mainline", "d"}, false},
		{[]string{"--mainline", "d"}, true},
		{[]string{"-mainline=d"}, true},
		{[]string{"--yes", "--mainline", "d"}, true}, // a bool flag takes no value
		{[]string{"--unlock-confirmed", "--prereqs-confirmed", "--dry-run", "--mainline", "d"}, true},
		{[]string{"--yes=true", "--mainline", "d"}, true},
		{[]string{"--image", "x", "--mainline"}, true}, // no value: package flag refuses it later
	} {
		if got := mlHasFlag(c.args, "mainline"); got != c.ml {
			t.Errorf("%q: mainline %v, want %v", c.args, got, c.ml)
		}
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--fw-drive", "/run/d.img"}, "/run/d.img"},
		{[]string{"-fw-drive=/run/d.img"}, "/run/d.img"},
		{[]string{"--fw-drive", "a", "--fw-drive", "b"}, "b"},
		{[]string{"--fw-drive"}, ""},
		{[]string{"--", "--fw-drive", "x"}, ""},
		{[]string{"--fw-drive-expect", "x"}, ""},
		{[]string{"--log", "--fw-drive", "--fw-drive", "d"}, "d"},
		{[]string{"--device", "/dev/mmcblk0", "--fw-drive", "d"}, "d"},
	} {
		if got := flagValue(c.args, "fw-drive"); got != c.want {
			t.Errorf("%q: %q, want %q", c.args, got, c.want)
		}
	}
}

// TestScanFlagsBools: every bool flag of the mainline flag sets (and of the
// devtest flags they take) is in boolFlags, and nothing else.
func TestScanFlagsBools(t *testing.T) {
	got := map[string]bool{}
	visit := func(fs *flag.FlagSet) {
		fs.VisitAll(func(f *flag.Flag) {
			if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
				got[f.Name] = true
			}
		})
	}
	visit(newMLPlanArgs("install", true).fs)
	fs := flag.NewFlagSet("device", flag.ContinueOnError)
	devtestFlags(fs)
	mainlineTestGate(fs)
	visit(fs)
	// the device form's own (cmdMainlineDevice) and the official ones
	for _, n := range []string{"panel-confirmed", "yes"} {
		got[n] = true
	}
	for n := range got {
		if !boolFlags[n] {
			t.Errorf("the bool flag %s is not in boolFlags", n)
		}
	}
	for n := range boolFlags {
		if !got[n] && n != "test-fake-unit" && n != "test-fixture-manifests" && n != "test-torn" {
			t.Errorf("boolFlags has %s, no flag set has it", n)
		}
	}
	if !reflect.DeepEqual(scanFlags(nil), map[string]string{}) {
		t.Fatal("no args")
	}
}
