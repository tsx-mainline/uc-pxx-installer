package avbab

import (
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mk(a, b [4]byte) []byte {
	s := make([]byte, 512)
	copy(s, "\x00AB0")
	s[4] = 1
	copy(s[8:], a[:])
	copy(s[12:], b[:])
	binary.BigEndian.PutUint32(s[28:], crc32.ChecksumIEEE(s[:28]))
	return s
}

func decode(t *testing.T, s []byte) AB {
	a, err := Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestCases(t *testing.T) {
	cases := []struct {
		name    string
		sector  []byte
		err     string // "" = valid
		pick    int
		keepers [2]bool
	}{
		{"A succ 15, B succ 14", mk([4]byte{15, 0, 1}, [4]byte{14, 0, 1}), "", 0, [2]bool{true, true}},
		{"A trying, B succ", mk([4]byte{15, 7, 0}, [4]byte{14, 0, 1}), "", 0, [2]bool{false, true}},
		{"B higher", mk([4]byte{14, 0, 1}, [4]byte{15, 0, 1}), "", 1, [2]bool{true, true}},
		{"tie picks A", mk([4]byte{15, 0, 1}, [4]byte{15, 0, 1}), "", 0, [2]bool{true, true}},
		{"A disabled", mk([4]byte{0, 0, 0}, [4]byte{14, 0, 1}), "", 1, [2]bool{false, true}},
		{"A succ prio 0 is no keeper", mk([4]byte{0, 0, 1}, [4]byte{14, 0, 1}), "", 1, [2]bool{false, true}},
		// the sector that bricked unit 1: A dead, B successful with tries 1
		{"unit-1 brick", mk([4]byte{13, 0, 0}, [4]byte{15, 1, 1}), "illegal", -1, [2]bool{}},
		{"no bootable slot", mk([4]byte{15, 0, 0}, [4]byte{14, 0, 0}), "no bootable", -1, [2]bool{}},
		{"tries 8", mk([4]byte{15, 8, 0}, [4]byte{14, 0, 1}), "illegal", -1, [2]bool{}},
		{"slot reserved", mk([4]byte{15, 0, 1, 1}, [4]byte{14, 0, 1}), "illegal", -1, [2]bool{}},
	}
	for _, c := range cases {
		a := decode(t, c.sector)
		err := a.Validate()
		if c.err == "" {
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
				continue
			}
			if a.Pick() != c.pick {
				t.Errorf("%s: pick %d, want %d", c.name, a.Pick(), c.pick)
			}
			if a.Slots[0].Keeper() != c.keepers[0] || a.Slots[1].Keeper() != c.keepers[1] {
				t.Errorf("%s: keepers %v %v", c.name, a.Slots[0].Keeper(), a.Slots[1].Keeper())
			}
		} else if err == nil || !strings.Contains(err.Error(), c.err) {
			t.Errorf("%s: err %v, want %q", c.name, err, c.err)
		}
	}
}

func TestHeaderAndCRC(t *testing.T) {
	good := mk([4]byte{15, 0, 1}, [4]byte{14, 0, 1})
	le := append([]byte(nil), good...)
	binary.LittleEndian.PutUint32(le[28:], crc32.ChecksumIEEE(le[:28]))
	if err := decode(t, le).Validate(); err == nil || !strings.Contains(err.Error(), "little-endian") {
		t.Errorf("little-endian crc: %v", err)
	}
	for i, want := range map[int]string{1: "magic", 4: "version", 6: "reserved", 20: "reserved", 29: "crc"} {
		s := append([]byte(nil), good...)
		s[i] ^= 0x40
		if i < 28 {
			binary.BigEndian.PutUint32(s[28:], crc32.ChecksumIEEE(s[:28]))
		}
		if err := decode(t, s).Validate(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("byte %d: err %v, want %q", i, err, want)
		}
	}
	if _, err := Parse(make([]byte, 31)); err == nil {
		t.Error("short sector accepted")
	}
}

// Captured sectors from real units, if the folder is given (not in the repo):
// UC_PXX_SECTORS=/path go test ./internal/avbab/
func TestCaptured(t *testing.T) {
	dir := os.Getenv("UC_PXX_SECTORS")
	if dir == "" {
		t.Skip("UC_PXX_SECTORS not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.bin"))
	n := 0
	for _, f := range files {
		d, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, off := range []int{0, 2048} {
			if len(d) < off+512 || string(d[off:off+4]) != "\x00AB0" {
				continue
			}
			a := decode(t, d[off:off+512])
			err := a.Validate()
			s := a.Slots
			illegal := !s[0].Legal() || !s[1].Legal()
			if (err != nil) != (illegal || a.CRCStored != a.CRCComputed || a.Pick() < 0) {
				t.Errorf("%s@%d: err %v for A%v B%v", filepath.Base(f), off, err, s[0], s[1])
			}
			n++
		}
	}
	t.Logf("%d captured sectors", n)
}
