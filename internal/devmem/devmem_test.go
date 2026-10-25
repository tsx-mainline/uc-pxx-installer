package devmem

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestRead32(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mem")
	b := make([]byte, 3*4096)
	binary.LittleEndian.PutUint32(b[0x1004:], 0x5242c309)
	os.WriteFile(p, b, 0o644)
	old, oldc := Path, Compatible
	Path, Compatible = p, ""
	defer func() { Path, Compatible = old, oldc }()
	v, err := Read32(0x1004)
	if err != nil || v != 0x5242c309 {
		t.Fatalf("%#x %v", v, err)
	}
	if _, err := Read32(0x1006); err == nil {
		t.Error("unaligned read accepted")
	}
	if BootModeName(v) != "fastboot" || BootModeName(0x5242c300) != "normal" || BootModeName(0x5242c301) != "loader (rockusb)" {
		t.Error("boot mode names")
	}
	if BootModeName(0) != "none (a normal boot)" {
		t.Error("zero")
	}
}

func TestNotRK3399(t *testing.T) {
	p := filepath.Join(t.TempDir(), "compatible")
	os.WriteFile(p, []byte("linux,dummy-virt\x00"), 0o644)
	old := Compatible
	Compatible = p
	defer func() { Compatible = old }()
	if _, err := Read32(0xff320300); err != ErrNotRK3399 {
		t.Fatal(err)
	}
}
