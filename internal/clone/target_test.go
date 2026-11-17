package clone

import (
	"strings"
	"testing"
)

// TestCheckTargetFiles (RC-10b review MINOR-2): every pinned file is one
// regular-file line of the manifest with the pinned sha256.
func TestCheckTargetFiles(t *testing.T) {
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	m := Entry{Path: "/usr/sbin/uc-pxx-bootctl", Type: 'f', Mode: 0o755, Size: 10, Data: a}.Line() +
		Entry{Path: "/etc/apk/keys/x y.pub", Type: 'f', Mode: 0o644, Size: 3, Data: b}.Line() +
		Entry{Path: "/usr/sbin/uc-pxx-slot", Type: 'l', Mode: 0o777, Size: 64, Data: a}.Line() + // a link whose target reads as the sha
		Entry{Path: "/etc/fstab", Type: 'f', Mode: 0o644, Size: 3, Data: b}.Line()
	ok := []TargetPin{{"/usr/sbin/uc-pxx-bootctl", a}, {"/etc/apk/keys/x y.pub", b}}
	if err := CheckTargetFiles([]byte(m), ok); err != nil {
		t.Fatal(err)
	}
	if err := CheckTargetFiles([]byte(m), nil); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		pins []TargetPin
		want string
	}{
		{[]TargetPin{{"/usr/sbin/uc-pxx-bootctl", b}}, "/usr/sbin/uc-pxx-bootctl is f aaaaaaaaaaaaaaaa, the pin bbbbbbbbbbbbbbbb"},
		{[]TargetPin{{"/usr/sbin/uc-pxx-arm-slot", a}}, "/usr/sbin/uc-pxx-arm-slot is missing"},
		{[]TargetPin{{"/usr/sbin/uc-pxx-slot", a}}, "/usr/sbin/uc-pxx-slot is l aaaaaaaaaaaaaaaa, the pin aaaaaaaaaaaaaaaa"},
		{[]TargetPin{{"/usr/sbin/uc-pxx-bootctl", a}, {"/etc/fstab", a}}, "/etc/fstab is f bbbb"},
		{[]TargetPin{{"/usr/sbin/uc-pxx-bootct", a}}, "/usr/sbin/uc-pxx-bootct is missing"},
	} {
		err := CheckTargetFiles([]byte(m), c.pins)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c.pins, err, c.want)
		}
	}
}
