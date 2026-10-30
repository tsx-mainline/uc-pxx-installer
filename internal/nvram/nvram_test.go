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
		// 72005=true in a part that Android may load is always on (never "not
		// clear", so no confirmation can pass it)
		{"tie, the later part (the driver's) on", []part{{Index: 1, Counter: 4, Records: off}, {Index: 9, Counter: 4, Records: on}}, nvram.On, 9, "parts 1 and 9 have this highest"},
		{"tie, the earlier part on", []part{{Index: 1, Counter: 4, Records: on}, {Index: 9, Counter: 4, Records: off}}, nvram.On, 1, "72005=true"},
		{"tie of three, one on", []part{{Index: 1, Counter: 4, Records: off}, {Index: 5, Counter: 4, Records: on}, {Index: 9, Counter: 4, Records: off}}, nvram.On, 5, "parts 1, 5, 9"},
		{"tie, the part on does not inflate", []part{{Index: 1, Counter: 4, Records: on, BadZlib: true}, {Index: 9, Counter: 4, Records: off}}, nvram.Unknown, -1, "same highest write counter"},
		{"72005 twice, once true (the later one)", []part{{Index: 0, Counter: 1, Records: append(off, "72005=true")}}, nvram.On, 0, "72005=true"},
		{"72005 twice, true first", []part{{Index: 0, Counter: 1, Records: []string{"72005=true", "\x0172005=false"}}}, nvram.On, 0, "one of them true"},
		{"72016 twice, 72005 true", []part{{Index: 0, Counter: 1, Records: append(on, "72016=x")}}, nvram.On, 0, "active part 0 of 32"},
		{"72016 twice, 72005 false", []part{{Index: 0, Counter: 1, Records: append(off, "72016=x")}}, nvram.Unknown, 0, "key 72016 is there twice"},
		{"72012 twice", []part{{Index: 0, Counter: 1, Records: append(off, "72012=false")}}, nvram.Unknown, 0, "key 72012 is there twice"},
		{"a newer part with a zlib length past the part, the valid part on", []part{{Index: 0, Counter: 1, Records: on}, {Index: 1, Counter: 2, Records: off, ZlibLen: 0x10000}}, nvram.On, 0, "past the part"},
		{"a tie below the highest is fine", []part{{Index: 1, Counter: 4, Records: on}, {Index: 2, Counter: 4, Records: on}, {Index: 3, Counter: 5, Records: off}}, nvram.Off, 3, ""},
		{"an empty zlib stream (length 0) is accepted, then does not inflate", []part{{Index: 0, Counter: 1, Records: off}, {Index: 1, Counter: 2, Records: off, ZlibLen: -1}}, nvram.Unknown, 1, "zlib"},
		{"a newer part with a zlib length past the part", []part{{Index: 0, Counter: 1, Records: off}, {Index: 1, Counter: 2, Records: off, ZlibLen: 0x10000}}, nvram.Unknown, -1, "past the part"},
		{"an older part with a zlib length past the part", []part{{Index: 0, Counter: 3, Records: off}, {Index: 1, Counter: 2, Records: on, ZlibLen: 0x10000}}, nvram.Off, 0, ""},
		{"a newer part with a zlib length past the part, the same counter, on", []part{{Index: 0, Counter: 2, Records: off}, {Index: 1, Counter: 2, Records: on, ZlibLen: 0x10000}}, nvram.On, 1, "past the part"},
		{"a newer part with a zlib length past the part, on", []part{{Index: 0, Counter: 1, Records: off}, {Index: 1, Counter: 2, Records: on, ZlibLen: 0xffda}}, nvram.On, 1, "past the part"},
		{"the active part's text is longer than the driver's buffer", []part{{Index: 0, Counter: 1, Records: off}, {Index: 1, Counter: 2, Records: append(off, "9="+strings.Repeat("x", 0x1ffd8))}}, nvram.Unknown, 1, "longer than the driver's buffer"},
		{"a text just as long as the driver's buffer", []part{{Index: 1, Counter: 2, Records: append(off, "9="+strings.Repeat("x", 0x1ffd8-len(strings.Join(off, "\x00"))-5))}}, nvram.Off, 1, ""},
		{"the text length is not the header's", []part{{Index: 0, Counter: 1, Records: off}, {Index: 1, Counter: 2, Records: off, TextLen: 7}}, nvram.Unknown, 1, "the header says 7"},
		{"active part does not inflate", []part{{Index: 0, Counter: 1, Records: off}, {Index: 1, Counter: 2, Records: off, BadZlib: true}}, nvram.Unknown, 1, "zlib"},
		{"no key 72005", []part{{Index: 0, Counter: 1, Records: []string{"72012=true"}}}, nvram.Unknown, 0, "no key 72005"},
		{"72005 twice", []part{{Index: 0, Counter: 1, Records: append(off, "72005=false")}}, nvram.Unknown, 0, "twice"},
		{"72005=1 (getBool reads it as true)", []part{{Index: 0, Counter: 1, Records: []string{"72005=1"}}}, nvram.On, 0, "72005=1"},
		{"72005 not true or false", []part{{Index: 0, Counter: 1, Records: []string{"72005=yes"}}}, nvram.Unknown, 0, "neither"},
		{"72005 empty", []part{{Index: 0, Counter: 1, Records: []string{"72005="}}}, nvram.Unknown, 0, "neither"},
		{"prefix 1 before the key (as the driver's commit writes it)", []part{{Index: 0, Counter: 1, Records: []string{"\x0172005=false"}}}, nvram.Off, 0, ""},
		{"prefix ':' before the key", []part{{Index: 0, Counter: 1, Records: []string{":72005=false"}}}, nvram.Off, 0, ""},
		{"prefix 2 before the key (a lock)", []part{{Index: 0, Counter: 1, Records: []string{"\x0272005=false"}}}, nvram.Unknown, 0, "lock prefix"},
		{"a key name that only starts with 72005", []part{{Index: 0, Counter: 1, Records: []string{"720050=false", "172005=false"}}}, nvram.Unknown, 0, "no key"},
	}
	for _, c := range cases {
		au := parse(c.parts...)
		if au.State != c.state || au.Part != c.part || !strings.Contains(au.Why, c.why) {
			t.Errorf("%s: state %s part %d why %q; want %s part %d why ~%q", c.name, au.State, au.Part, au.Why, c.state, c.part, c.why)
		}
	}
}

// TestDriverRule: the cases of the release review (AUTOUPDATE-CHECK-2-REVIEW.md
// §4.3, D1..D14) and the reader of Android (N3), each with "true" and with
// "false". The expected state is the driver's: "on" when a part that Android
// may load gives 72005 a true value, "off" only for a certain "false".
func TestDriverRule(t *testing.T) {
	u16 := func(v uint16) *uint16 { return &v }
	fw := func(v string) []string { return []string{"1=test", "72012=true", "72005=" + v} }
	rec := func(r ...string) []string { return append([]string{"1=test"}, r...) }
	z := func(r []string) []byte { return zlibOf(r) }
	// no valid part: every part has a wrong checksum
	broken := func(p0 part) []part {
		ps := []part{p0}
		for i := 1; i < nvram.Parts; i++ {
			ps = append(ps, part{Index: i, Counter: 5, Records: fw("false"), Sum: u16(0x1234)})
		}
		return ps
	}
	cases := []struct {
		name  string
		parts []part
		state string
		part  int
		why   string
	}{
		{"D1 the text length is not the header's, true", []part{{Index: 31, Counter: 127, Records: fw("true"), TextLen: 3}}, nvram.On, 31, "72005=true"},
		{"D1 the text length is not the header's, false", []part{{Index: 31, Counter: 127, Records: fw("false"), TextLen: 3}}, nvram.Unknown, 31, "the header says 3"},
		{"D2 zlib length 0xffffffff, checksum 0, true", []part{{Index: 0, Counter: 9, Records: fw("false")}, {Index: 1, Counter: 10, Records: fw("true"), ZlibLen: 0xffffffff}}, nvram.On, 1, "72005=true"},
		{"D2 zlib length 0xffffffff, checksum 0, false", []part{{Index: 0, Counter: 9, Records: fw("false")}, {Index: 1, Counter: 10, Records: fw("false"), ZlibLen: 0xffffffff}}, nvram.Unknown, 1, "past the part"},
		{"D2 zlib length 0x80000000, checksum 0, true", []part{{Index: 1, Counter: 10, Records: fw("true"), ZlibLen: 0x80000000}}, nvram.On, 1, ""},
		{"D2 zlib length 0x80000000, checksum 0, false: valid for certain", []part{{Index: 0, Counter: 9, Records: fw("false")}, {Index: 1, Counter: 10, Records: fw("false"), ZlibLen: 0x80000000}}, nvram.Unknown, 1, "past the part"},
		{"D2 zlib length 0x80000000, checksum not 0: not valid for certain", []part{{Index: 0, Counter: 9, Records: fw("false")}, {Index: 1, Counter: 10, Records: fw("true"), ZlibLen: 0x80000000, Sum: u16(7)}}, nvram.Off, 0, ""},
		{"D2 zlib length 0x7ffffffe (the sum reads past the part), checksum 0, false", []part{{Index: 0, Counter: 9, Records: fw("false")}, {Index: 1, Counter: 10, Records: fw("false"), ZlibLen: 0x7ffffffe}}, nvram.Unknown, -1, "past the part"},
		{"D2 zlib length 0x7fffffff, checksum 0, true", []part{{Index: 1, Counter: 10, Records: fw("true"), ZlibLen: 0x7fffffff}}, nvram.On, 1, ""},
		{"D2 zlib length 0xffffffff, checksum not 0: not valid", []part{{Index: 0, Counter: 9, Records: fw("false")}, {Index: 1, Counter: 10, Records: fw("true"), ZlibLen: 0xffffffff, Sum: u16(1)}}, nvram.Off, 0, ""},
		{"D3 zlib length 0xffda (the checksum reads past the part), true", []part{{Index: 0, Counter: 9, Records: fw("false")}, {Index: 1, Counter: 10, Records: fw("true"), ZlibLen: 0xffda}}, nvram.On, 1, "past the part"},
		{"D3 zlib length 0xffda, false", []part{{Index: 0, Counter: 9, Records: fw("false")}, {Index: 1, Counter: 10, Records: fw("false"), ZlibLen: 0xffda}}, nvram.Unknown, -1, "past the part"},
		{"D4 flags 0, a zlib length shorter than the stream, true", []part{{Index: 1, Counter: 10, Records: fw("true"), FlagsZero: true, Zlib: z(fw("true")), ZlibLen: 9}}, nvram.On, 1, "72005=true"},
		{"D4 flags 0, a zlib length shorter than the stream, false", []part{{Index: 1, Counter: 10, Records: fw("false"), FlagsZero: true, Zlib: z(fw("false")), ZlibLen: 9}}, nvram.Unknown, 1, "flags 0"},
		{"D4 flags 1, a zlib length shorter than the stream, true: cleared", []part{{Index: 1, Counter: 10, Records: fw("true"), Zlib: z(fw("true")), ZlibLen: 9}}, nvram.Unknown, 1, "incomplete"},
		{"D5 no valid part, part 0 flags 0 with true", broken(part{Index: 0, Counter: 5, Records: fw("true"), FlagsZero: true, Sum: u16(0x4321)}), nvram.On, 0, "the driver loads part 0"},
		{"D5 no valid part, part 0 flags 0 with false", broken(part{Index: 0, Counter: 5, Records: fw("false"), FlagsZero: true, Sum: u16(0x4321)}), nvram.Unknown, -1, "the driver loads part 0 with flags 0"},
		{"D5 no valid part, part 0 flags 1 with true: an empty store", broken(part{Index: 0, Counter: 5, Records: fw("true"), Sum: u16(0x4321)}), nvram.Unknown, -1, "no valid nvram part"},
		{"D5 part 3 valid: the default part 0 is not loaded", append(broken(part{Index: 0, Counter: 5, Records: fw("true"), FlagsZero: true, Sum: u16(0x4321)})[:3],
			part{Index: 3, Counter: 0, Records: fw("false")}), nvram.Off, 3, ""},
		{"D6 ':72005=true' after '\x0172005=false'", []part{{Index: 0, Counter: 1, Records: rec("\x0172005=false", ":72005=true")}}, nvram.On, 0, "72005=true"},
		{"D6 ':72005=false' after '\x0172005=true'", []part{{Index: 0, Counter: 1, Records: rec("\x0172005=true", ":72005=false")}}, nvram.On, 0, "one of them true"},
		{"D6 ':72005=false' after '\x0172005=false'", []part{{Index: 0, Counter: 1, Records: rec("\x0172005=false", ":72005=false")}}, nvram.Unknown, 0, "twice"},
		{"D6b only ':72005=true'", []part{{Index: 0, Counter: 1, Records: rec(":72005=true")}}, nvram.On, 0, "72005=true"},
		{"D6b only ':72005=false'", []part{{Index: 0, Counter: 1, Records: rec(":72005=false")}}, nvram.Off, 0, ""},
		{"D7 a record without '=' before the only 72005=false", []part{{Index: 0, Counter: 1, Records: rec("noequals", "\x0172005=false")}}, nvram.Unknown, 0, "without \"=\""},
		{"D7 a record without '=' before the only 72005=true (the driver never reads it)", []part{{Index: 0, Counter: 1, Records: rec("noequals", "\x0172005=true")}}, nvram.Unknown, 0, "without \"=\""},
		{"D7 a record without '=' after 72005=true", []part{{Index: 0, Counter: 1, Records: rec("\x0172005=true", "noequals", "\x0172005=false")}}, nvram.On, 0, "72005=true"},
		{"D7 a record without '=' after 72005=false", []part{{Index: 0, Counter: 1, Records: rec("\x0172005=false", "noequals")}}, nvram.Unknown, 0, "without \"=\""},
		{"D8 '\x0372005=false' (hidden from get)", []part{{Index: 0, Counter: 1, Records: rec("\x0372005=false")}}, nvram.Unknown, 0, "lock prefix"},
		{"D8 '\x0372005=true' (hidden from get, still on)", []part{{Index: 0, Counter: 1, Records: rec("\x0372005=true")}}, nvram.On, 0, "one of them true"},
		{"D8b '\x0272005=false' then '\x0172005=true' (the lock keeps false; still on)", []part{{Index: 0, Counter: 1, Records: rec("\x0272005=false", "\x0172005=true")}}, nvram.On, 0, "one of them true"},
		{"D8b '\x0272005=true' then '\x0172005=false' (the lock keeps true)", []part{{Index: 0, Counter: 1, Records: rec("\x0272005=true", "\x0172005=false")}}, nvram.On, 0, "72005=true"},
		{"D9 no empty record at the end, false", []part{{Index: 0, Counter: 1, Records: fw("false"), NoEnd: true}}, nvram.Unknown, 0, "not cleared"},
		{"D9 no empty record at the end, true", []part{{Index: 0, Counter: 1, Records: fw("true"), NoEnd: true}}, nvram.On, 0, "72005=true"},
		{"D9 the driver's NULs end a text of exactly 0x1ffd8 bytes", []part{{Index: 0, Counter: 1, NoEnd: true,
			Records: append(fw("false"), "9="+strings.Repeat("x", 0x1ffd8-len(strings.Join(fw("false"), "\x00"))-4))}}, nvram.Off, 0, ""},
		{"D10 72005=TRUE (getBool: false)", []part{{Index: 0, Counter: 1, Records: fw("TRUE")}}, nvram.Unknown, 0, "neither"},
		{"D10 72005=' true' (getBool: false)", []part{{Index: 0, Counter: 1, Records: fw(" true")}}, nvram.Unknown, 0, "neither"},
		{"D10 72005='true ' (getBool: false)", []part{{Index: 0, Counter: 1, Records: fw("true ")}}, nvram.Unknown, 0, "neither"},
		{"N3 72005=1 (getBool: true)", []part{{Index: 0, Counter: 1, Records: fw("1")}}, nvram.On, 0, "72005=1"},
		{"N3 72005=0 (getBool: false, not what the web UI writes)", []part{{Index: 0, Counter: 1, Records: fw("0")}}, nvram.Unknown, 0, "neither"},
		{"D11 tie: the earlier part true", []part{{Index: 30, Counter: 127, Records: fw("true")}, {Index: 31, Counter: 127, Records: fw("false")}}, nvram.On, 30, "highest write counter"},
		{"D12 tie: the later part (the driver's) true", []part{{Index: 30, Counter: 127, Records: fw("false")}, {Index: 31, Counter: 127, Records: fw("true")}}, nvram.On, 31, "highest write counter"},
		{"D14 text past 0x1ffd8 with true: cleared", []part{{Index: 0, Counter: 1, Records: append(fw("true"), "9="+strings.Repeat("A", 0x30000))}}, nvram.Unknown, 0, "longer than the driver's buffer"},
	}
	for _, c := range cases {
		au := parse(c.parts...)
		if au.State != c.state || au.Part != c.part || !strings.Contains(au.Why, c.why) {
			t.Errorf("%s: state %s part %d why %q; want %s part %d why ~%q", c.name, au.State, au.Part, au.Why, c.state, c.part, c.why)
		}
	}
}

func zlibOf(r []string) []byte {
	p := nvramtest.Encode(part{Records: r})
	n := int(p[12]) | int(p[13])<<8
	return p[nvram.HeaderSize : nvram.HeaderSize+n]
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

// FuzzParse: any part content, also a tie and a part past the part, never
// panics, and no value of another key comes out (the secret record, also as a
// prefixed 72005 value that is not true or false).
func FuzzParse(f *testing.F) {
	f.Add(nvramtest.Encode(part{Records: withSecret(off), Counter: 9}), uint64(9), false)
	f.Add(nvramtest.Encode(part{Records: withSecret(on), Counter: 9, ZlibLen: 0xffffffff}), uint64(9), true)
	f.Add(nvramtest.Encode(part{Records: withSecret([]string{":72005=made-up-admin-password-77f1"}), Counter: 1, NoEnd: true}), uint64(1), true)
	f.Add(nvramtest.Encode(part{Records: withSecret(off), Counter: 3, FlagsZero: true}), uint64(3), false)
	f.Fuzz(func(t *testing.T, p []byte, other uint64, tie bool) {
		ring := nvramtest.Ring(part{Index: 4, Counter: other, Records: withSecret(off)})
		if tie {
			copy(ring[7*nvram.PartSize:], nvramtest.Encode(part{Counter: other, Records: withSecret(on)}))
		}
		copy(ring[9*nvram.PartSize:10*nvram.PartSize], p)
		au := nvram.Parse(ring)
		out := fmt.Sprintf("%#v", au)
		if strings.Contains(out, "made-up") {
			t.Fatalf("a secret came out: %s", out)
		}
		switch au.State {
		case nvram.On, nvram.Off, nvram.Unknown:
		default:
			t.Fatalf("state %q", au.State)
		}
	})
}
