package keykeep

import (
	"bytes"
	"strings"
	"testing"

	"uc-pxx-installer/internal/pcdfw"
)

// a made-up key: never a real one in a test
func fakeKey(b byte) pcdfw.Keys {
	var k pcdfw.Keys
	for i := range k.Head {
		k.Head[i], k.IV[i] = b+byte(i), b^byte(3*i+1)
	}
	return k
}

func TestRoundTrip(t *testing.T) {
	k := fakeKey(0x40)
	r := Encode(k)
	if len(r) != 512 || string(r[:16]) != Magic || !bytes.Equal(Encode(k), r) {
		t.Fatal("not a deterministic record")
	}
	got, err := Decode(r)
	if err != nil || got != k {
		t.Fatalf("decode: %v", err)
	}
	if !Present(r) || Present(make([]byte, 512)) {
		t.Fatal("Present")
	}
	// every single bit flip is found (the CRC, or the fixed fields)
	for i := 0; i < 512; i++ {
		for bit := 0; bit < 8; bit++ {
			b := append([]byte(nil), r...)
			b[i] ^= 1 << bit
			if _, err := Decode(b); err == nil {
				t.Fatalf("byte %d bit %d: decoded", i, bit)
			}
		}
	}
}

func TestClassify(t *testing.T) {
	want := Encode(fakeKey(1))
	other := Encode(fakeKey(9))
	zero, ff := make([]byte, 512), bytes.Repeat([]byte{0xff}, 512)
	cases := map[string]struct {
		sec []byte
		c   Class
	}{
		"zero":   {zero, Empty},
		"ff":     {ff, Empty},
		"same":   {want, Same},
		"other":  {other, Other},
		"text":   {append([]byte("hello"), zero[5:]...), Foreign},
		"one ff": {append(append([]byte(nil), zero[:100]...), append([]byte{0x42}, zero[101:]...)...), Foreign},
	}
	for n := 1; n < 512; n += 7 {
		cases["torn on zero "+string(rune('a'+n%26))+strings.Repeat("x", n/26)] = struct {
			sec []byte
			c   Class
		}{append(append([]byte(nil), want[:n]...), zero[n:]...), Torn}
		cases["torn on ff "+string(rune('a'+n%26))+strings.Repeat("x", n/26)] = struct {
			sec []byte
			c   Class
		}{append(append([]byte(nil), want[:n]...), ff[n:]...), Torn}
	}
	for name, c := range cases {
		if got := Classify(c.sec, want); got != c.c {
			t.Errorf("%s: %s, want %s", name, got, c.c)
		}
	}
	for _, c := range []Class{Empty, Same, Torn} {
		if !c.Writable() {
			t.Fatal(c)
		}
	}
	for _, c := range []Class{Other, Foreign, Unread} {
		if c.Writable() {
			t.Fatal(c)
		}
	}
}

func TestLBAs(t *testing.T) {
	l, err := LBAs(446464, 32768)
	if err != nil || l != UnitLBAs {
		t.Fatalf("the UC-P10 record sectors: %v %v, want %v", l, err, UnitLBAs)
	}
	if _, err := LBAs(100, MinSectors-1); err == nil {
		t.Fatal("a too small backup")
	}
}

func TestGate(t *testing.T) {
	g := TheGate()
	if g.Open || !strings.Contains(g.Why, "bootchain-no-backup") || !strings.Contains(g.Why, "android-no-backup") || len(g.SHA256) != 64 {
		t.Fatalf("the embedded gate: %+v (checks 2 and 3 are still open)", g)
	}
	open := bytes.ReplaceAll(checksJSON, []byte(`"passed": false,
      "evidence": ""`), []byte(`"passed": true,
      "evidence": "x"`))
	if g := gateOf(open); !g.Open {
		t.Fatalf("all passed: %+v", g)
	}
	noEvidence := bytes.ReplaceAll(checksJSON, []byte(`"passed": false`), []byte(`"passed": true`))
	if g := gateOf(noEvidence); g.Open {
		t.Fatal("passed without evidence")
	}
	if g := gateOf([]byte("{}")); g.Open {
		t.Fatal("an empty file")
	}
}
