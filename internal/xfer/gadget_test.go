package xfer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidInstance(t *testing.T) {
	for _, s := range []string{"uc-pxx-step2", "a", "x_1", strings.Repeat("a", 31)} {
		if !validInstance(s) {
			t.Errorf("%q refused", s)
		}
	}
	for _, s := range []string{"", "-a", "_a", "A", "a/b", "a.b", "../x", strings.Repeat("a", 32)} {
		if validInstance(s) {
			t.Errorf("%q taken", s)
		}
	}
}

// TestNewGadget makes the configfs tree in a plain folder (configfs itself
// needs the kernel; the guest test uses the real one).
func TestNewGadget(t *testing.T) {
	root := filepath.Join(t.TempDir(), "g")
	os.MkdirAll(filepath.Join(root, "webusb"), 0o755) // configfs makes os_desc and webusb itself
	os.MkdirAll(filepath.Join(root, "os_desc"), 0o755)
	c := GadgetConfig{Root: root, Instance: Instance, VendorID: USBVendorID, ProductID: USBProductID, BCDDevice: USBBCDDevice,
		Manufacturer: USBMaker, Product: USBProduct, Serial: USBSerial, Config: "step2", VendorCode: MSOSVendorCode,
		LandingPage: LandingPage, WebUSBCode: WebUSBCode}
	if err := NewGadget(c); err != nil {
		t.Fatal(err)
	}
	for f, want := range map[string]string{"idVendor": "0x1d6b", "idProduct": "0x0105", "bcdDevice": "0x0201",
		"strings/0x409/serialnumber": "UC-PXX-STEP2", "os_desc/use": "1", "os_desc/b_vendor_code": "0xcd",
		"os_desc/qw_sign": "MSFT100", "webusb/use": "1", "webusb/landingPage": LandingPage} {
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil || string(b) != want {
			t.Errorf("%s = %q (%v), want %q", f, b, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "functions/ffs.uc-pxx-step2")); err != nil {
		t.Fatal(err)
	}
	if l, err := os.Readlink(filepath.Join(root, "os_desc/c.1")); err != nil || l != filepath.Join(root, "configs/c.1") {
		t.Fatalf("os_desc link %q %v", l, err)
	}
	if err := LinkFFS(root, Instance); err != nil {
		t.Fatal(err)
	}
	if err := LinkFFS(root, Instance); err != nil {
		t.Fatal("a second link")
	}
	c.Instance = "../x"
	if err := NewGadget(c); err == nil {
		t.Fatal("a bad instance name was taken")
	}
	c.Instance, c.VendorID = Instance, 0
	if err := NewGadget(c); err == nil {
		t.Fatal("no vendor id was taken")
	}
}
