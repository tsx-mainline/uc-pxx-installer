package fakeemmc_test

import (
	"bytes"
	"encoding/binary"
	"testing"

	"uc-pxx-installer/internal/avb"
	"uc-pxx-installer/internal/fakeemmc"
)

// TestBootSize: a fixture with larger boot images (for a kernel/mkimage.sh
// boot image of a mainline bundle): the signed boot descriptor covers all
// of the image but its last 64 KiB, the vbmeta verifies with the pins, and
// the resource is where the default fixture has it. The default fixture is
// unchanged (its boot descriptor covers 176128 bytes).
func TestBootSize(t *testing.T) {
	def := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	big := fakeemmc.NewFixture(fakeemmc.FixtureOpt{BootSize: 4 << 20})
	capOf := func(fx *fakeemmc.Fixture) uint64 {
		v, err := avb.Verify(fx.New["vbmeta"], fx.Pins)
		if err != nil {
			t.Fatal(err)
		}
		d, ok := v.Find("boot")
		if !ok || d.Type != "hash" {
			t.Fatal("no boot hash descriptor")
		}
		return d.ImageSize
	}
	if c := capOf(def); c != 176128 || len(def.New["boot"]) != 200<<10 {
		t.Fatalf("default: cap %d, boot %d bytes", c, len(def.New["boot"]))
	}
	if c := capOf(big); c != 4<<20-64<<10 || len(big.New["boot"]) != 4<<20 || len(big.Old["boot"]) != 4<<20 {
		t.Fatalf("big: cap %d, boot %d bytes", c, len(big.New["boot"]))
	}
	if big.Resource != def.Resource {
		t.Fatal("the resource differs")
	}
	if !bytes.Equal(big.New["boot"][:8], []byte("ANDROID!")) || binary.LittleEndian.Uint32(big.New["boot"][24:]) != 20<<10 {
		t.Fatal("not the fixture's boot image")
	}
	if len(big.Manifests) != 1 || big.Manifests[0].Firmware.SHA256 == def.Manifests[0].Firmware.SHA256 {
		t.Fatal("manifests")
	}
}
