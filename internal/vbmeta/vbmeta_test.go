package vbmeta

import (
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"os"
	"testing"

	"uc-pxx-installer/internal/pcdfw/pcdfwtest"
)

func TestParse(t *testing.T) {
	key := []byte("a made-up public key of some length")
	b := pcdfwtest.Vbmeta(8192, key, 7)
	in, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	s := sha1.Sum(key)
	if in.RollbackIndex != 7 || in.PublicKeySHA1 != hex.EncodeToString(s[:]) || in.Algorithm != 2 || in.Release != "avbtool test" {
		t.Fatalf("%+v", in)
	}
	if _, err := Parse(make([]byte, 8192)); err != ErrNoMagic {
		t.Fatalf("zero bytes: %v", err)
	}
	bad := append([]byte(nil), b...)
	binary.BigEndian.PutUint64(bad[64:], 1<<20) // key offset outside the aux block
	if _, err := Parse(bad); err == nil {
		t.Fatal("key outside the aux block accepted")
	}
	if _, err := Parse(b[:300]); err == nil {
		t.Fatal("short image accepted")
	}
}

// UC_PXX_VBMETA: a vbmeta image of a real firmware (not in the repo), and
// UC_PXX_VBMETA_SHA1, the key sha1 from its manifest.
func TestRealVbmeta(t *testing.T) {
	p, want := os.Getenv("UC_PXX_VBMETA"), os.Getenv("UC_PXX_VBMETA_SHA1")
	if p == "" {
		t.Skip("UC_PXX_VBMETA not set")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	in, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if in.PublicKeySHA1 != want || in.ImageBytes != 6528 {
		t.Fatalf("%+v", in)
	}
}
