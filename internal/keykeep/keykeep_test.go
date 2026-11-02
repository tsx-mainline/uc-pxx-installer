package keykeep

import (
	"bytes"
	"encoding/json"
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

// recorded is the result of each check as KEY-KEEP.md records it (check 1:
// section 9; checks 2 and 3: section 10, "Check 2: pass." and "Check 3: pass,
// with one condition."). It is a copy on purpose: checks.json and this table
// change together, in one reviewed commit, and the test fails on any other
// change of the gate. KEY-KEEP.md is in another repository, so the test does
// not read its text.
var recorded = []struct {
	id, source string
	passed     bool
	condition  []string // words the condition must hold; nil: no condition
}{
	{"backup-content", "KEY-KEEP.md section 9", true, nil},
	{"bootchain-no-backup", "KEY-KEEP.md section 10.2", true, nil},
	{"android-no-backup", "KEY-KEEP.md section 10.3", true, []string{"section 10.3", "vendor-signed SD update card", "read backup again before trusting the record"}},
}

func TestChecksAsRecorded(t *testing.T) {
	var c Checks
	if err := json.Unmarshal(checksJSON, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Checks) != len(recorded) || len(CheckIDs) != len(recorded) {
		t.Fatalf("%d checks, KEY-KEEP.md records %d", len(c.Checks), len(recorded))
	}
	for i, r := range recorded {
		x := c.Checks[i]
		if x.ID != r.id || CheckIDs[i] != r.id || x.Source != r.source || x.Passed != r.passed || x.Evidence == "" {
			t.Errorf("check %d: %s source %q passed %v, KEY-KEEP.md records %s %q %v", i+1, x.ID, x.Source, x.Passed, r.id, r.source, r.passed)
		}
		if (x.Condition != "") != (r.condition != nil) {
			t.Errorf("%s: condition %q, KEY-KEEP.md records a condition: %v", x.ID, x.Condition, r.condition != nil)
		}
		for _, w := range r.condition {
			if !strings.Contains(x.Condition, w) {
				t.Errorf("%s: the condition does not say %q", x.ID, w)
			}
		}
	}
}

// mutate changes check i of the embedded file (a generic copy) and gives the new file.
func mutate(t *testing.T, i int, f func(map[string]any)) []byte {
	var m map[string]any
	if err := json.Unmarshal(checksJSON, &m); err != nil {
		t.Fatal(err)
	}
	if i >= 0 {
		f(m["checks"].([]any)[i].(map[string]any))
	} else {
		f(m)
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestGate(t *testing.T) {
	g := TheGate()
	if !g.Open || len(g.SHA256) != 64 || g.SHA256 != SHA256(checksJSON) || !strings.Contains(g.Why, "passed, 1 with a condition") {
		t.Fatalf("the embedded gate: %+v (KEY-KEEP.md §9, §10: all three passed)", g)
	}
	if len(g.Conditions) != 1 || !strings.HasPrefix(g.Conditions[0], "android-no-backup: ") || !strings.Contains(g.Conditions[0], "SD update card") {
		t.Fatalf("the conditions of the gate: %q", g.Conditions)
	}
	if g := gateOf(mutate(t, -1, func(map[string]any) {})); !g.Open {
		t.Fatalf("the same checks, written again: %+v", g)
	}
	for i, id := range CheckIDs {
		for name, f := range map[string]func(map[string]any){
			"not passed":         func(x map[string]any) { x["passed"] = false },
			"no evidence":        func(x map[string]any) { x["evidence"] = "" },
			"no source":          func(x map[string]any) { x["source"] = "" },
			"a source elsewhere": func(x map[string]any) { x["source"] = "FLOW.md section 1" },
			"a bare source":      func(x map[string]any) { x["source"] = SourcePrefix },
		} {
			g := gateOf(mutate(t, i, f))
			if g.Open || !strings.Contains(g.Why, "open: "+id) || g.Conditions != nil {
				t.Errorf("%s %s: %+v", id, name, g)
			}
		}
		for name, f := range map[string]func(map[string]any){
			"another id":    func(x map[string]any) { x["id"] = "x" },
			"unknown field": func(x map[string]any) { x["passed_by"] = "x" },
		} {
			if g := gateOf(mutate(t, i, f)); g.Open || !strings.Contains(g.Why, "does not read") {
				t.Errorf("%s %s: %+v", id, name, g)
			}
		}
	}
	if g := gateOf(mutate(t, -1, func(m map[string]any) { c := m["checks"].([]any); c[1], c[2] = c[2], c[1] })); g.Open {
		t.Fatal("checks in another order")
	}
	if g := gateOf(mutate(t, -1, func(m map[string]any) { m["checks"] = m["checks"].([]any)[:2] })); g.Open {
		t.Fatal("two checks")
	}
	// a condition of check 2 is carried too
	c2 := gateOf(mutate(t, 1, func(x map[string]any) { x["condition"] = "test" }))
	if !c2.Open || len(c2.Conditions) != 2 || c2.Conditions[0] != "bootchain-no-backup: test" {
		t.Fatalf("two conditions: %+v", c2)
	}
	if g := gateOf([]byte("{}")); g.Open {
		t.Fatal("an empty file")
	}
}

// TestGateStrict: the variants that the decoder alone took (review R3 I-1,
// the reviewer's probe G1): data after the object, a key twice (the last
// one wins), a check without a field. Each closes the gate. An empty
// condition ("") is the way to say "no condition": the gate stays open,
// and the sha256 of the file in the plan's gate shows the change.
func TestGateStrict(t *testing.T) {
	for name, b := range map[string][]byte{
		"data after the object":     append(append([]byte{}, checksJSON...), []byte(` {"x":1}`)...),
		"a second object":           append(append([]byte{}, checksJSON...), checksJSON...),
		"a key twice (false, true)": []byte(strings.Replace(string(checksJSON), `"passed": true`, `"passed": false, "passed": true`, 1)),
		"a top key twice":           []byte(strings.Replace(string(checksJSON), `"schema": 1,`, `"schema": 1, "schema": 1,`, 1)),
		"no condition field":        mutate(t, 2, func(x map[string]any) { delete(x, "condition") }),
		"no source field":           mutate(t, 0, func(x map[string]any) { delete(x, "source") }),
		"no what field":             mutate(t, 1, func(x map[string]any) { delete(x, "what") }),
	} {
		if g := gateOf(b); g.Open || !strings.Contains(g.Why, "does not read") || g.Conditions != nil {
			t.Errorf("%s: %+v", name, g)
		}
	}
	if !strings.Contains(string(checksJSON), `"passed": true`) {
		t.Fatal("the probe of a key twice needs `\"passed\": true` in checks.json")
	}
	g := gateOf(mutate(t, 2, func(x map[string]any) { x["condition"] = "" }))
	if !g.Open || len(g.Conditions) != 0 || g.SHA256 == TheGate().SHA256 {
		t.Fatalf("an empty condition: %+v", g)
	}
}
