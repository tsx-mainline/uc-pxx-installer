package nvram_test

import (
	"fmt"
	"strings"
	"testing"

	"uc-pxx-installer/internal/nvram"
	"uc-pxx-installer/internal/nvram/nvramtest"
)

type part = nvramtest.Part

var (
	off = nvramtest.Records("false")
	on  = nvramtest.Records("true")
)

// secret is a made-up record that stands for the admin password and the
// hashes of the store. It must never reach the output.
const secret = "2=made-up-admin-password-77f1"

func withSecret(r []string) []string { return append([]string{secret, "8181=made-up-hash-9c2e"}, r...) }

func parse(parts ...part) nvram.AutoUpdate { return nvram.Parse(nvramtest.Ring(parts...)) }

func TestRule(t *testing.T) {
	// the ring of a unit after 128 saves: counters 96..127 in order, the
	// last four saves turned auto update off
	var ring []part
	for i := 0; i < nvram.Parts; i++ {
		r := on
		if i >= 28 {
			r = off
		}
		ring = append(ring, part{Index: i, Counter: uint64(96 + i), Records: withSecret(r)})
	}
	wrapped := append([]part{}, ring...)
	wrapped[0] = part{Index: 0, Counter: 128, Records: on} // the next save, after part 31
	cases := []struct {
		name  string
		parts []part
		state string
		part  int
		why   string
	}{
		{"one part off", []part{{Index: 5, Counter: 1, Records: off}}, nvram.Off, 5, "highest write counter"},
		{"one part on", []part{{Index: 5, Counter: 1, Records: on}}, nvram.On, 5, ""},
		{"counter 0 is valid", []part{{Index: 0, Counter: 0, Records: off}}, nvram.Off, 0, ""},
		{"newest part off", []part{{Index: 0, Counter: 10, Records: on}, {Index: 1, Counter: 11, Records: off}}, nvram.Off, 1, ""},
		{"an older part on, the newer off", []part{{Index: 7, Counter: 3, Records: on}, {Index: 2, Counter: 9, Records: off}}, nvram.Off, 2, ""},
		{"an older part off, the newer on", []part{{Index: 0, Counter: 3, Records: off}, {Index: 1, Counter: 4, Records: on}}, nvram.On, 1, ""},
		{"most parts on, the newest off", ring, nvram.Off, 31, "of 32"},
		{"wrapped to part 0", wrapped, nvram.On, 0, ""},
		{"the address does not count", []part{{Index: 31, Counter: 5, Records: off}, {Index: 0, Counter: 6, Records: on}}, nvram.On, 0, ""},
		// The driver skips a part with a bad checksum, so the older part is
		// active: that is what Android loads.
		{"corrupt checksum of the newest", []part{{Index: 0, Counter: 3, Records: on}, {Index: 1, Counter: 4, Records: off, BadSum: true}}, nvram.On, 0, ""},
		{"corrupt checksum, the only part", []part{{Index: 1, Counter: 4, Records: off, BadSum: true}}, nvram.Unknown, -1, "no valid nvram part"},
		{"no magic", []part{{Index: 1, Counter: 4, Records: off, BadMagic: true}}, nvram.Unknown, -1, "no valid nvram part"},
		{"no parts", nil, nvram.Unknown, -1, "no valid nvram part"},
		{"tie on the highest counter", []part{{Index: 1, Counter: 4, Records: off}, {Index: 9, Counter: 4, Records: off}}, nvram.Unknown, -1, "same highest write counter"},
		{"a tie below the highest is fine", []part{{Index: 1, Counter: 4, Records: on}, {Index: 2, Counter: 4, Records: on}, {Index: 3, Counter: 5, Records: off}}, nvram.Off, 3, ""},
		{"an empty zlib stream (length 0) is accepted, then does not inflate", []part{{Index: 0, Counter: 1, Records: off}, {Index: 1, Counter: 2, Records: off, ZlibLen: -1}}, nvram.Unknown, 1, "zlib"},
		{"a newer part with a zlib length past the part", []part{{Index: 0, Counter: 1, Records: off}, {Index: 1, Counter: 2, Records: off, ZlibLen: 0x10000}}, nvram.Unknown, -1, "past the part"},
		{"an older part with a zlib length past the part", []part{{Index: 0, Counter: 3, Records: off}, {Index: 1, Counter: 2, Records: on, ZlibLen: 0x10000}}, nvram.Off, 0, ""},
		{"active part does not inflate", []part{{Index: 0, Counter: 1, Records: off}, {Index: 1, Counter: 2, Records: off, BadZlib: true}}, nvram.Unknown, 1, "zlib"},
		{"no key 72005", []part{{Index: 0, Counter: 1, Records: []string{"72012=true"}}}, nvram.Unknown, 0, "no key 72005"},
		{"72005 twice", []part{{Index: 0, Counter: 1, Records: append(off, "72005=false")}}, nvram.Unknown, 0, "twice"},
		{"72005 not true or false", []part{{Index: 0, Counter: 1, Records: []string{"72005=1"}}}, nvram.Unknown, 0, "neither"},
		{"72005 empty", []part{{Index: 0, Counter: 1, Records: []string{"72005="}}}, nvram.Unknown, 0, "neither"},
		{"attribute byte before the key", []part{{Index: 0, Counter: 1, Records: []string{"\x0272005=false"}}}, nvram.Off, 0, ""},
		{"a key name that only starts with 72005", []part{{Index: 0, Counter: 1, Records: []string{"720050=false", "172005=false"}}}, nvram.Unknown, 0, "no key"},
	}
	for _, c := range cases {
		au := parse(c.parts...)
		if au.State != c.state || au.Part != c.part || !strings.Contains(au.Why, c.why) {
			t.Errorf("%s: state %s part %d why %q; want %s part %d why ~%q", c.name, au.State, au.Part, au.Why, c.state, c.part, c.why)
		}
	}
}

func TestValues(t *testing.T) {
	au := parse(part{Index: 4, Counter: 70, Records: withSecret(off)})
	if au.Firmware != "false" || au.APK != "true" || au.URL != "https://example.invalid/fw.txt" || au.Counter != 70 || au.Valid != 1 {
		t.Errorf("%+v", au)
	}
	// the list ends at the first empty record, as in the driver
	au = parse(part{Index: 0, Counter: 1, Records: []string{"72005=false", "", "72005=true"}})
	if au.State != nvram.Off {
		t.Errorf("records after the end: %+v", au)
	}
	// a value with control bytes is shown as printable text only
	au = parse(part{Index: 0, Counter: 1, Records: []string{"72005=false", "72016=http://x\x1b[2J\n"}})
	if au.URL != "http://x?[2J?" {
		t.Errorf("url %q", au.URL)
	}
}

func TestSecretsNeverInOutput(t *testing.T) {
	stores := [][]part{
		{{Index: 0, Counter: 1, Records: withSecret(off)}},
		{{Index: 0, Counter: 1, Records: withSecret(on)}},
		{{Index: 0, Counter: 1, Records: withSecret(nil)}},
		{{Index: 0, Counter: 1, Records: withSecret(append(off, "72005=true"))}},
		{{Index: 0, Counter: 1, Records: withSecret(off), BadZlib: true}},
		{{Index: 0, Counter: 1, Records: withSecret(off), BadSum: true}},
		{{Index: 0, Counter: 2, Records: withSecret(off)}, {Index: 1, Counter: 2, Records: withSecret(on)}},
		// a secret in the place of a value that is shown
		{{Index: 0, Counter: 1, Records: []string{"72005=made-up-admin-password-77f1"}}},
	}
	for i, s := range stores {
		out := fmt.Sprintf("%+v %#v", parse(s...), parse(s...))
		for _, bad := range []string{"made-up-admin-password", "made-up-hash", "8181", "100=", "made-up"} {
			if i < len(stores)-1 && strings.Contains(out, bad) {
				t.Errorf("store %d: output holds %q: %s", i, bad, out)
			}
		}
		if i == len(stores)-1 && strings.Contains(out, "made-up-admin-password") {
			t.Errorf("an unexpected 72005 value is printed: %s", out)
		}
	}
}

func TestShortRing(t *testing.T) {
	au := nvram.Parse(make([]byte, 100))
	if au.State != nvram.Unknown || !strings.Contains(au.Why, "not 2097152") {
		t.Errorf("%+v", au)
	}
}

func TestCheckPartition(t *testing.T) {
	if err := nvram.CheckPartition(3671040, 32768); err != nil {
		t.Error(err)
	}
	if err := nvram.CheckPartition(3671041, 32768); err == nil {
		t.Error("a moved others accepted")
	}
	if err := nvram.CheckPartition(3671040, 8191); err == nil {
		t.Error("a small others accepted")
	}
}

func TestChecksum(t *testing.T) {
	// the sum of the big-endian words, negated; an odd length is padded with 0
	if got := nvram.Checksum([]byte{0x12, 0x34, 0x56}, 3); got != 0x10000-(0x1234+0x5600) {
		t.Errorf("%04x", got)
	}
	if got := nvram.Checksum([]byte{0xff, 0xff, 0xff, 0xff}, 4); got != 2 {
		t.Errorf("no end-around carry: %04x", got)
	}
}
