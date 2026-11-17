//go:build install && step2

package step2_test

import (
	"errors"
	"strings"
	"testing"

	"uc-pxx-installer/internal/step2"
)

// TestModeQuiet: step 2 takes tsx-autoupdate mode auto (the RC-10c base: no
// MODE line, the tool's default) and mode manual (RC-10 to RC-10b), one word
// on one line; every other answer refuses.
func TestModeQuiet(t *testing.T) {
	for _, c := range []struct {
		out string
		err error
		ok  bool
	}{
		{"auto\n", nil, true},
		{"manual\n", nil, true},
		{"auto", nil, true},
		{" manual \n", nil, true},
		{"", nil, false},
		{"\n", nil, false},
		{"Auto\n", nil, false},
		{"off\n", nil, false},
		{"auto\nmanual\n", nil, false},
		{"auto manual\n", nil, false},
		{"autox\n", nil, false},
		{"auto\n", errors.New("exit status 1"), false},
		{"manual\n", errors.New("exit status 2"), false},
	} {
		err := step2.ModeQuiet(c.out, c.err)
		if (err == nil) != c.ok {
			t.Errorf("ModeQuiet(%q, %v) = %v, want ok %v", c.out, c.err, err, c.ok)
		}
		if err != nil && !strings.Contains(err.Error(), "mode auto or manual") {
			t.Errorf("ModeQuiet(%q): the refusal %q does not name the modes", c.out, err)
		}
	}
}
