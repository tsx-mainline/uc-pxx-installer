package gadget

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNew(t *testing.T) {
	g := filepath.Join(t.TempDir(), "uc-pxx-inst")
	c := Config{Root: g, Serial: "S1", Product: "P", Drive: "/run/uc-pxx-inst/drive.img", ACM: true, NCM: true,
		HostMAC: "02:00:00:00:00:01", DeviceMAC: "02:00:00:00:00:02"}
	if err := New(c); err != nil {
		t.Fatal(err)
	}
	// a second call (usbd restarted) must not fail
	if err := New(c); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"idVendor":                                    "0x1d6b",
		"bDeviceClass":                                "0xef",
		"strings/0x409/serialnumber":                  "S1",
		"functions/ncm.usb0/host_addr":                "02:00:00:00:00:01",
		"functions/mass_storage.usb0/lun.0/file":      "/run/uc-pxx-inst/drive.img",
		"functions/mass_storage.usb0/lun.0/ro":        "0",
		"functions/mass_storage.usb0/lun.0/removable": "1",
	}
	for p, v := range want {
		b, err := os.ReadFile(filepath.Join(g, p))
		if err != nil || strings.TrimSpace(string(b)) != v {
			t.Errorf("%s = %q, %v", p, b, err)
		}
	}
	for _, fn := range []string{"acm.usb0", "ncm.usb0", "mass_storage.usb0"} {
		if l, err := os.Readlink(filepath.Join(g, "configs/c.1", fn)); err != nil || l != filepath.Join(g, "functions", fn) {
			t.Errorf("link %s: %q %v", fn, l, err)
		}
	}
	if Ejected(g) {
		t.Error("ejected with a file")
	}
	os.WriteFile(filepath.Join(g, "functions/mass_storage.usb0/lun.0/file"), []byte("\n"), 0o644)
	if !Ejected(g) {
		t.Error("not ejected with an empty file")
	}
}

func TestNoEMMCBacking(t *testing.T) {
	for _, d := range []string{"/dev/mmcblk0", "/dev/mmcblk0p22", "/sys/block/x"} {
		if err := New(Config{Root: filepath.Join(t.TempDir(), "g"), Drive: d}); err == nil {
			t.Errorf("drive %s accepted", d)
		}
	}
	if err := New(Config{Root: filepath.Join(t.TempDir(), "g")}); err == nil {
		t.Error("a gadget with no function accepted")
	}
}

func TestBindAndState(t *testing.T) {
	root := t.TempDir()
	g := filepath.Join(root, "g")
	os.MkdirAll(g, 0o755)
	os.MkdirAll(filepath.Join(root, "sys/class/udc/fe800000.usb"), 0o755)
	os.WriteFile(filepath.Join(root, "sys/class/udc/fe800000.usb/state"), []byte("configured\n"), 0o644)
	if u := FirstUDC(root); u != "fe800000.usb" {
		t.Fatal(u)
	}
	if Bound(g) != "" {
		t.Error("bound before Bind")
	}
	Bind(g, "fe800000.usb")
	if Bound(g) != "fe800000.usb" || UDCState(root, "fe800000.usb") != "configured" || UDCState(root, "x") != "absent" {
		t.Error("bind or state")
	}
	if err := Reconnect(root, "fe800000.usb"); err != nil {
		t.Error(err)
	}
}

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
