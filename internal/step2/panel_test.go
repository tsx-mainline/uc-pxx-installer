//go:build install && step2 && devtest

package step2_test

import (
	"encoding/binary"
	"testing"
	"time"

	"uc-pxx-installer/internal/step2"
	"uc-pxx-installer/internal/step2ui"
)

func ev(typ, code uint16, val int32) []byte {
	b := make([]byte, 24)
	binary.LittleEndian.PutUint16(b[16:], typ)
	binary.LittleEndian.PutUint16(b[18:], code)
	binary.LittleEndian.PutUint32(b[20:], uint32(val))
	return b
}

// Review T4-2 NIT-1: OK pressed, its release lost (SYN_DROPPED), one short
// tap later: no start 3 s after the tap without a held key.
func TestKeysOfSynDropped(t *testing.T) {
	var b []byte
	b = append(b, ev(1, 113, 1)...) // Mic (OK) down
	b = append(b, ev(0, 3, 0)...)   // SYN_DROPPED: the release was lost
	b = append(b, ev(1, 113, 2)...) // autorepeat: dropped
	b = append(b, ev(1, 202, 1)...) // Speaker (Back) down
	ks := step2.KeysOf(b)
	want := []step2ui.Key{step2ui.KeyOKDown, step2ui.KeyOKUp, step2ui.KeyBack}
	if len(ks) != len(want) {
		t.Fatalf("keys %v, want %v", ks, want)
	}
	for i := range ks {
		if ks[i] != want[i] {
			t.Fatalf("keys %v, want %v", ks, want)
		}
	}
	t0 := time.Unix(1700000000, 0)
	m := step2ui.New(t0, false, "")
	for _, k := range step2.KeysOf(append(ev(1, 113, 1), ev(0, 3, 0)...)) {
		m.Handle(k, t0)
	}
	m.Handle(step2ui.KeyNone, t0.Add(5*time.Second))
	if m.Screen != step2ui.ScreenAsk {
		t.Fatalf("a press whose release was lost started the run: %s", m.Screen)
	}
}
