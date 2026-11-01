package factory_test

import (
	"encoding/binary"
	"errors"
	"os"
	"strings"
	"testing"

	"uc-pxx-installer/internal/factory"
	"uc-pxx-installer/internal/factory/factorytest"
)

// TestUnit2Like: the made-up block with unit 2's structure reads as unit 2
// did (UC-P8.md §11): L 2034, 34 records ending at byte 396, the checksum
// valid, V1.5A, UC-P10 with the suffix -T-HS.
func TestUnit2Like(t *testing.T) {
	blk := factorytest.Unit2Like("UC-P10-T-HS")
	b, m, err := factory.Read(blk)
	if err != nil {
		t.Fatal(err)
	}
	if b.Length != 2034 || b.Records != 34 || !b.ChecksumOK() || b.HWRev != "V1.5A" {
		t.Fatalf("block %+v", b)
	}
	if m.Base != factory.UCP10 || m.Value != "UC-P10-T-HS" || m.Suffix != "-T-HS" || m.HWRev != "V1.5A" {
		t.Fatalf("model %+v", m)
	}
	// the records end at byte 396, as on unit 2
	p := 92
	for i := 0; i < 34; i++ {
		p += 4 + 2*int(binary.LittleEndian.Uint16(blk[p+2:]))
	}
	if p != 396 {
		t.Fatalf("records end at %d, want 396", p)
	}
	// the MAC of the fixture is the made-up one
	if string(blk[6:12]) != string(factorytest.FakeMAC) {
		t.Fatal("the fixture MAC is not the made-up one")
	}
}

func TestModels(t *testing.T) {
	for _, c := range []struct {
		value, base, suffix string
	}{
		{"UC-P10", factory.UCP10, ""},
		{"UC-P10-T-HS", factory.UCP10, "-T-HS"},
		{"UC-P10-T-C-HS-I", factory.UCP10, "-T-C-HS-I"},
		{"UC-P8", factory.UCP8, ""},
		{"UC-P8-T-HS", factory.UCP8, "-T-HS"},
		{"UC-P8-C-I", factory.UCP8, "-C-I"},
		{"UC-P80", factory.UCP8, "0"},   // U-Boot compares 5 bytes: the 800x1280 panel
		{"UC-P100", factory.UCP10, "0"}, // U-Boot compares 6 bytes
	} {
		_, m, err := factory.Read(factorytest.Build(factorytest.Opt{Model: c.value}))
		if err != nil || m.Base != c.base || m.Suffix != c.suffix || m.Value != c.value {
			t.Errorf("%q: %+v %v", c.value, m, err)
		}
	}
}

// TestRefused: every block that U-Boot would not read as a UC-P10 or a
// UC-P8, or would read only with a wrong checksum, is refused.
func TestRefused(t *testing.T) {
	for _, c := range []struct {
		name string
		o    factorytest.Opt
		want string
	}{
		{"UC-P12", factorytest.Opt{Model: "UC-P12"}, `says "UC-P12"`},
		{"UC-P1", factorytest.Opt{Model: "UC-P1"}, `says "UC-P1"`},
		{"lower case", factorytest.Opt{Model: "uc-p10"}, `says "uc-p10"`},
		{"leading space", factorytest.Opt{Model: " UC-P10"}, `says " UC-P10"`},
		{"other text", factorytest.Opt{Model: "TSW-1070"}, `says "TSW-1070"`},
		{"control byte", factorytest.Opt{Model: "UC-P10\x01"}, "not printable"},
		{"inner NUL", factorytest.Opt{Model: "UC-P10\x00X"}, "not printable"},
		{"leading NUL", factorytest.Opt{Model: "\x00UC-P10"}, "not printable"},
		{"no model", factorytest.Opt{NoModel: true}, "no model record"},
		{"bad checksum", factorytest.Opt{BadSum: true}, "checksum is wrong"},
		{"zero checksum", factorytest.Opt{ZeroSum: true}, "checksum is wrong"},
	} {
		_, _, err := factory.Read(factorytest.Build(c.o))
		if err == nil || !errors.Is(err, factory.ErrNoModel) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}

// TestParseErrors: blocks that cannot be read as a whole.
func TestParseErrors(t *testing.T) {
	for _, c := range []struct {
		name string
		blk  []byte
		want string
	}{
		{"short", make([]byte, 512), "512 bytes"},
		{"all zero", make([]byte, factory.Size), "length 0 out of range"},
		{"all FF", fill(0xff), "out of range"},
		{"length 86", factorytest.Build(factorytest.Opt{Length: 0x56}), "length 86"},
		{"length 4097", factorytest.Build(factorytest.Opt{Length: 4097}), "length 4097"},
		{"records past L", factorytest.Build(factorytest.Opt{Length: 300}), "past the length 300"},
		{"count past L", factorytest.Build(factorytest.Opt{Count: 40}), "past the length"},
		{"two models", factorytest.Build(factorytest.Opt{TwoModels: true}), "2 model records"},
		{"model too long", factorytest.Build(factorytest.Opt{Model: "UC-P10" + strings.Repeat("x", 70)}), "more than 64"},
	} {
		_, err := factory.Parse(c.blk)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	// a count of fewer records is read (U-Boot reads only that many): the
	// model record then is not found
	b, err := factory.Parse(factorytest.Build(factorytest.Opt{Count: 5}))
	if err != nil || b.Model != "" {
		t.Fatalf("count 5: %+v %v", b, err)
	}
}

// TestChecksumRule: the checksum is the negated sum of the big-endian words
// without the word at byte 4, stored big-endian (checked on unit 2).
func TestChecksumRule(t *testing.T) {
	blk := make([]byte, factory.Size)
	blk[10], blk[11] = 0x12, 0x34
	blk[98], blk[99] = 0x00, 0x01
	blk[100] = 0x07
	blk[4], blk[5] = 0xff, 0xff // the checksum word is not counted
	for _, c := range []struct {
		l    int
		want uint16
	}{
		{100, 0x89cb}, // -(0x6400 + 0x1234 + 0x0001)
		{99, 0x8acb},  // -(0x6300 + 0x1234 + 0x0001): the last word is bytes 98, 99
		{101, 0x81cb}, // -(0x6500 + 0x1234 + 0x0001 + 0x0700): an odd L reads the byte after it
	} {
		binary.LittleEndian.PutUint32(blk, uint32(c.l))
		if got := factory.Checksum(blk, c.l); got != c.want {
			t.Errorf("L %d: checksum %04x, want %04x", c.l, got, c.want)
		}
	}
}

// TestSuffixNeverDecides: the same base with any printable suffix gives the
// same base.
func TestSuffixNeverDecides(t *testing.T) {
	for _, s := range []string{"", "-T", "-T-C", "-HS", "-X-Y-Z", " anything"} {
		_, m, err := factory.Read(factorytest.Build(factorytest.Opt{Model: "UC-P8" + s}))
		if err != nil || m.Base != factory.UCP8 {
			t.Errorf("UC-P8%s: %+v %v", s, m, err)
		}
	}
}

func fill(c byte) []byte {
	b := make([]byte, factory.Size)
	for i := range b {
		b[i] = c
	}
	return b
}

// FuzzRead: no input makes the reader panic, read past the block, or name a
// model that the prefix rule and the checksum do not give.
func FuzzRead(f *testing.F) {
	f.Add(factorytest.Unit2Like("UC-P10-T-HS"))
	f.Add(factorytest.Unit2Like("UC-P8-T-HS"))
	f.Add(factorytest.Build(factorytest.Opt{BadSum: true}))
	f.Add(make([]byte, factory.Size))
	f.Fuzz(func(t *testing.T, in []byte) {
		blk := make([]byte, factory.Size)
		copy(blk, in)
		b, m, err := factory.Read(blk)
		if err != nil {
			return
		}
		if !b.ChecksumOK() || b.Length < factory.MinLength || b.Length > factory.Size {
			t.Fatalf("accepted %+v", b)
		}
		switch m.Base {
		case factory.UCP8:
			if !strings.HasPrefix(m.Value, "UC-P8") {
				t.Fatalf("UC-P8 from %q", m.Value)
			}
		case factory.UCP10:
			if strings.HasPrefix(m.Value, "UC-P8") || !strings.HasPrefix(m.Value, "UC-P10") {
				t.Fatalf("UC-P10 from %q", m.Value)
			}
		default:
			t.Fatalf("base %q", m.Base)
		}
		if m.Value != m.Base+m.Suffix {
			t.Fatalf("value %q base %q suffix %q", m.Value, m.Base, m.Suffix)
		}
	})
}

// TestRealBlock reads a real factory block, if UC_PXX_FACTORY_BLOCK names
// one (never in the repository). It logs only the model, the revision and
// the checksum result: the block holds the MAC address and other data of
// the unit.
func TestRealBlock(t *testing.T) {
	p := os.Getenv("UC_PXX_FACTORY_BLOCK")
	if p == "" {
		t.Skip("UC_PXX_FACTORY_BLOCK is not set")
	}
	blk, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	b, m, err := factory.Read(blk)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("real block: L %d, %d records, checksum ok %v, model %q = base %s + suffix %q, hwrev %s",
		b.Length, b.Records, b.ChecksumOK(), m.Value, m.Base, m.Suffix, m.HWRev)
	if want := os.Getenv("UC_PXX_FACTORY_WANT"); want != "" && m.Base != want {
		t.Fatalf("base %s, want %s", m.Base, want)
	}
}
