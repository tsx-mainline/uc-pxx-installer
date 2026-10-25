package gpt

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const small = `disk 4096
1 uboot_a 2048 256
2 misc 2304 512
3 boot_a 2816 1024
`

func image(t *testing.T, l *Layout) []byte {
	pri, bak, err := Build(l)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, l.DiskSectors*Sector)
	copy(b, pri)
	copy(b[(l.DiskSectors-1-EntrySectors)*Sector:], bak)
	return b
}

func layout(t *testing.T, s string) *Layout {
	l, err := ReadLayout(strings.NewReader(s))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestRoundTrip(t *testing.T) {
	l := layout(t, small)
	b := image(t, l)
	tb, err := Parse(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	if len(tb.Entries) != 3 || tb.DiskSectors != 4096 {
		t.Fatalf("%d entries, %d sectors", len(tb.Entries), tb.DiskSectors)
	}
	e, ok := tb.Find("misc")
	if !ok || e.Start != 2304 || e.Sectors() != 512 || e.Index != 2 || !e.Contains(2308) || e.Contains(2816) {
		t.Fatalf("misc: %+v", e)
	}
	if at, ok := tb.At(2900); !ok || at.Name != "boot_a" {
		t.Fatalf("At: %+v", at)
	}
	if tb.LayoutSHA256() == "" || tb.BackupStart() != 4096-1-32 {
		t.Fatal("layout sha or backup start")
	}
}

// The real layout of the UC-P10 (names, starts, sizes from a unit) builds a
// table that parses back to the same layout, on a full-size sparse file.
func TestUCP10Layout(t *testing.T) {
	f, err := os.Open("testdata/ucp10-layout.txt")
	if err != nil {
		t.Fatal(err)
	}
	l, err := ReadLayout(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	pri, bak, err := Build(l)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "emmc.img")
	img, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer img.Close()
	img.Truncate(l.DiskSectors * Sector)
	img.WriteAt(pri, 0)
	img.WriteAt(bak, (l.DiskSectors-1-EntrySectors)*Sector)
	tb, err := Parse(img, l.DiskSectors*Sector)
	if err != nil {
		t.Fatal(err)
	}
	if len(tb.Entries) != len(l.Entries) {
		t.Fatalf("%d entries, want %d", len(tb.Entries), len(l.Entries))
	}
	for _, le := range l.Entries {
		e, ok := tb.Find(le.Name)
		if !ok || e.Start != le.Start || e.Sectors() != le.Size || e.Index != le.Index {
			t.Fatalf("%s: %+v", le.Name, e)
		}
	}
	misc, _ := tb.Find("misc")
	if misc.Start != 49152 || !misc.Contains(49156) {
		t.Fatalf("misc %+v", misc)
	}
	ud, _ := tb.Find("userdata")
	if ud.End != tb.LastUsable {
		t.Fatalf("userdata ends at %d, last usable %d", ud.End, tb.LastUsable)
	}
}

func fixCRC(b []byte, hdrLBA int64) {
	h := b[hdrLBA*Sector:]
	binary.LittleEndian.PutUint32(h[16:], 0)
	binary.LittleEndian.PutUint32(h[16:], crc32.ChecksumIEEE(h[:HeaderSize]))
}

func TestRefusals(t *testing.T) {
	l := layout(t, small)
	last := l.DiskSectors - 1
	cases := []struct {
		name string
		mod  func(b []byte)
		want string
	}{
		{"primary header crc", func(b []byte) { b[Sector+40]++ }, "header CRC"},
		{"backup header crc", func(b []byte) { b[last*Sector+40]++ }, "backup GPT header: header CRC"},
		{"primary entries", func(b []byte) { b[2*Sector+60]++ }, "entries: CRC"},
		{"backup entries", func(b []byte) { b[(last-EntrySectors)*Sector+60]++ }, "differ"},
		{"no signature", func(b []byte) { b[Sector] = 'X' }, "no signature"},
		{"entry count", func(b []byte) {
			binary.LittleEndian.PutUint32(b[Sector+80:], 64)
			fixCRC(b, 1)
		}, "entries of"},
	}
	for _, c := range cases {
		b := image(t, l)
		c.mod(b)
		_, err := Parse(bytes.NewReader(b), int64(len(b)))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
	for _, c := range []struct{ name, layout, want string }{
		{"overlap", "disk 4096\n1 a 2048 256\n2 b 2200 100\n", "overlap"},
		{"duplicate", "disk 4096\n1 a 2048 256\n2 a 2400 100\n", "both have the name"},
	} {
		b := image(t, layout(t, c.layout))
		if _, err := Parse(bytes.NewReader(b), int64(len(b))); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v", c.name, err)
		}
	}
	if _, _, err := Build(layout(t, "disk 4096\n1 a 4000 200\n")); err == nil {
		t.Error("Build accepted an entry past the usable range")
	}
}
