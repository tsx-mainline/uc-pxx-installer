//go:build install && step2 && devtest

package step2

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMarkerNames (R23-BASE-REVIEW MAJOR-2): the step-2 marker, left out of
// the content check, must be one line that names the running program's
// sha256 as a word.
func TestMarkerNames(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	p := filepath.Join(t.TempDir(), "m")
	for body, ok := range map[string]bool{
		"uc-pxx-bootctl-inst for uc-pxx-step2 " + sha + "\n": true,
		sha + " (supervised)\n":                              true,
		"uc-pxx-step2 " + sha:                                false, // no newline
		"uc-pxx-step2 " + sha + "\nmore\n":                   false,
		"uc-pxx-step2 " + strings.Repeat("cd", 32) + "\n":    false,
		"uc-pxx-step2 x" + sha + "\n":                        false,
	} {
		os.WriteFile(p, []byte(body), 0o644)
		if err := markerNames(p, sha); (err == nil) != ok {
			t.Errorf("%q: %v", body, err)
		}
	}
	os.Remove(p)
	if markerNames(p, sha) == nil {
		t.Error("a missing marker taken")
	}
}
