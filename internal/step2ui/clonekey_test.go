package step2ui

import (
	"strings"
	"testing"
)

// TestCloneAttemptKey: the clone's attempt key ("clone <ref_a>") is one
// attempt like a manifest sha256; a malformed one counts as failed.
func TestCloneAttemptKey(t *testing.T) {
	key := "clone " + strings.Repeat("a", 64)
	at := ParseAttempts([]byte(AttemptLine(key)), nil)
	if len(at) != 1 || at[0].Failed || at[0].Drive != key {
		t.Fatalf("%+v", at)
	}
	if ok, _ := AutoAllowed(true, key, at, false); ok {
		t.Fatal("a second countdown for the same clone key")
	}
	if ok, _ := AutoAllowed(true, "clone "+strings.Repeat("b", 64), at, false); !ok {
		t.Fatal("another clone key has no countdown")
	}
	for _, bad := range []string{"drive clone x\n", "drive clone " + strings.Repeat("A", 64) + "\n", "drive clone  " + strings.Repeat("a", 64) + "\n"} {
		if at := ParseAttempts([]byte(bad), nil); len(at) != 1 || !at[0].Failed {
			t.Fatalf("%q: %+v", bad, at)
		}
	}
}
