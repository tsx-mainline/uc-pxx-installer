package ui

import (
	"strings"
	"testing"
	"time"
)

func TestKeyTest(t *testing.T) {
	run := func(codes []uint16, timeout time.Duration) (bool, string) {
		ch := make(chan uint16, 16)
		for _, c := range codes {
			ch <- c
		}
		var out []string
		ok := KeyTest(ch, timeout, func(s string) { out = append(out, s) })
		return ok, strings.Join(out, "\n")
	}
	if ok, out := run([]uint16{148, 149, 202, 114, 115, 113}, time.Second); !ok || !strings.Contains(out, "press 6: code 113 Mic (place 6)") {
		t.Fatalf("in order: %v\n%s", ok, out)
	}
	// the UC-P10 map read in the wrong order (the reverse of the row)
	if ok, out := run([]uint16{113, 115, 114, 202, 149, 148}, time.Second); ok || !strings.Contains(out, "NOT the expected order: 113 Mic (place 6)") {
		t.Fatalf("reversed: %v\n%s", ok, out)
	}
	if ok, out := run([]uint16{148, 1}, 50*time.Millisecond); ok || !strings.Contains(out, "not complete") || !strings.Contains(out, "1 (not a key of the row)") {
		t.Fatalf("timeout: %v\n%s", ok, out)
	}
	// the row is the map of the flow: OK is the Mic key, back the Speaker key
	if MapCode(KeyRow[5].Code) != KeyOK || MapCode(KeyRow[2].Code) != KeyBack {
		t.Fatal("the key row and the flow's key map differ")
	}
}
