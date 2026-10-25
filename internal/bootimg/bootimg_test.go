package bootimg

import (
	"encoding/binary"
	"testing"
)

// Header makes a test header.
func header(name string, version, osv uint32) []byte {
	b := make([]byte, HeaderSize)
	copy(b, "ANDROID!")
	le := binary.LittleEndian
	le.PutUint32(b[8:], 3000)
	le.PutUint32(b[16:], 20)
	le.PutUint32(b[24:], 5000)
	le.PutUint32(b[36:], 2048)
	le.PutUint32(b[40:], version)
	le.PutUint32(b[44:], osv)
	copy(b[48:], name)
	copy(b[64:], "console=ttyS2")
	copy(b[608:], " extra")
	return b
}

func TestParse(t *testing.T) {
	h, err := Parse(header("ucpxx-003ba796", 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if h.UcPxxToken() != "ucpxx-003ba796" || h.AndroidVersion() != "" || h.Cmdline != "console=ttyS2 extra" {
		t.Errorf("%+v", h)
	}
	if h.Size() != 2048*(1+2+1+3) {
		t.Errorf("size %d", h.Size())
	}
	// the os_version of the stock boot_b: 0x12000136 = 9.0.0, 2019-06
	h, err = Parse(header("", 1, 0x12000136))
	if err != nil {
		t.Fatal(err)
	}
	if h.UcPxxToken() != "" || h.AndroidVersion() != "9.0.0, patch 2019-06" {
		t.Errorf("%q %q", h.UcPxxToken(), h.AndroidVersion())
	}
	if _, err := Parse(make([]byte, HeaderSize)); err != ErrNoMagic {
		t.Errorf("no magic: %v", err)
	}
	if _, err := Parse(header("", 3, 0)); err == nil {
		t.Error("header version 3 accepted")
	}
	if _, err := Parse(make([]byte, 10)); err == nil {
		t.Error("short header accepted")
	}
}
