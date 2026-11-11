//go:build install && step2

package device

import "testing"

// The forms step 2 runs (review STEP2-REST-1 NIT-1, NIT-5): never repair,
// never a switch to B, never another form.
func TestSlotBootctlForm(t *testing.T) {
	sec := make([]byte, 512)
	hexSec := SwitchArgs("a", sec, false)[4]
	for _, c := range []struct {
		args       []string
		ok, writes bool
	}{
		{[]string{"status"}, true, false},
		{[]string{"disable", "--slot", "b"}, true, true},
		{[]string{"arm-trying", "--slot", "a", "--lower-keeper"}, true, true},
		{[]string{"switch", "--to", "a", "--expect-sector", hexSec}, true, true},
		{[]string{"switch", "--to", "a", "--expect-sector", hexSec, "--dry-run"}, true, false},
		{[]string{"switch", "--to", "b", "--expect-sector", hexSec}, false, false},
		{RepairArgs("a", sec, false), false, false},
		{RepairArgs("a", sec, true), false, false},
		{[]string{"arm-trying", "--slot", "b"}, false, false},
		{[]string{"mark-successful"}, false, false},
	} {
		ok, w := slotBootctlForm(c.args)
		if ok != c.ok || w != c.writes {
			t.Fatalf("%v: ok %v writes %v", c.args, ok, w)
		}
	}
}
