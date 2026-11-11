package xfer

// The step-2 transfer gadget (STEP2-USB-TRANSPORT-2026-11-11.md §3.1, §3.2):
// one FunctionFS function and nothing else, with MS OS 1.0 descriptors so
// that Windows binds WinUSB by itself, and (when configfs has it) the WebUSB
// BOS. It reuses internal/gadget for the UDC (Bound, Bind, FirstUDC,
// UDCState, Reconnect) and keeps its own configfs code here, so that
// internal/gadget, which uc-pxx-inst links, does not change at all.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"uc-pxx-installer/internal/gadget"
)

// GadgetConfig describes a gadget with one FunctionFS function.
type GadgetConfig struct {
	Root         string // configfs gadget folder, for example /sys/kernel/config/usb_gadget/uc-pxx-step2
	Instance     string // the function instance: functions/ffs.<Instance>; also the FunctionFS mount source
	VendorID     uint16
	ProductID    uint16
	BCDDevice    uint16
	Manufacturer string
	Product      string
	Serial       string
	Config       string // the configuration string
	VendorCode   byte   // MS OS 1.0 bMS_VendorCode
	LandingPage  string // WebUSB landing page; "" = no WebUSB BOS
	WebUSBCode   byte   // WebUSB bVendorCode
}

// validInstance: 1 to 31 of a-z 0-9 _ -, not starting with _ or -.
func validInstance(s string) bool {
	if len(s) == 0 || len(s) > 31 || s[0] == '_' || s[0] == '-' {
		return false
	}
	for _, c := range []byte(s) {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// NewGadget makes the gadget folders of c: the device, its strings, one
// configuration with MS OS descriptors, and the FunctionFS function (not yet
// linked into the configuration: LinkFFS does that once the daemon has written
// its descriptors). It does not bind the gadget.
func NewGadget(c GadgetConfig) error {
	if !validInstance(c.Instance) {
		return fmt.Errorf("function instance %q", c.Instance)
	}
	if c.VendorID == 0 || c.ProductID == 0 {
		return errors.New("no USB vendor or product id")
	}
	g := c.Root
	if err := os.MkdirAll(g, 0o755); err != nil {
		return err
	}
	steps := [][2]string{
		{"idVendor", fmt.Sprintf("0x%04x", c.VendorID)},
		{"idProduct", fmt.Sprintf("0x%04x", c.ProductID)},
		{"bcdDevice", fmt.Sprintf("0x%04x", c.BCDDevice)},
		{"bcdUSB", "0x0200"}, // with webusb/use = 1 the kernel reports 2.10 itself (the BOS)
		// the class is in the interface (vendor class 0xff)
		{"bDeviceClass", "0x00"},
		{"bDeviceSubClass", "0x00"},
		{"bDeviceProtocol", "0x00"},
	}
	for _, s := range steps {
		if err := write(filepath.Join(g, s[0]), s[1]); err != nil {
			return err
		}
	}
	str := filepath.Join(g, "strings/0x409")
	cfg := filepath.Join(g, "configs/c.1")
	cstr := filepath.Join(cfg, "strings/0x409")
	for _, d := range []string{str, cstr} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	for k, v := range map[string]string{"manufacturer": c.Manufacturer, "product": c.Product, "serialnumber": c.Serial} {
		if err := write(filepath.Join(str, k), v); err != nil {
			return err
		}
	}
	if err := write(filepath.Join(cstr, "configuration"), c.Config); err != nil {
		return err
	}
	if err := write(filepath.Join(cfg, "MaxPower"), "100"); err != nil {
		return err
	}
	// MS OS 1.0: string 0xEE = "MSFT100" + bMS_VendorCode; the extended compat
	// id and the extended properties come from the FunctionFS descriptors.
	od := filepath.Join(g, "os_desc")
	for _, s := range [][2]string{{"use", "1"}, {"b_vendor_code", fmt.Sprintf("0x%02x", c.VendorCode)}, {"qw_sign", "MSFT100"}} {
		if err := write(filepath.Join(od, s[0]), s[1]); err != nil {
			return err
		}
	}
	if l := filepath.Join(od, "c.1"); !exists(l) {
		if err := os.Symlink(cfg, l); err != nil {
			return err
		}
	}
	if c.LandingPage != "" {
		wu := filepath.Join(g, "webusb")
		if _, err := os.Stat(wu); err == nil {
			for _, s := range [][2]string{{"bcdVersion", "0x0100"}, {"bVendorCode", fmt.Sprintf("0x%02x", c.WebUSBCode)},
				{"landingPage", c.LandingPage}, {"use", "1"}} {
				if err := write(filepath.Join(wu, s[0]), s[1]); err != nil {
					return err
				}
			}
		}
	}
	return os.MkdirAll(filepath.Join(g, "functions", "ffs."+c.Instance), 0o755)
}

func write(p, v string) error {
	if err := os.WriteFile(p, []byte(v), 0o644); err != nil {
		return fmt.Errorf("%s: %w", p, err)
	}
	return nil
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// LinkFFS links the FunctionFS function into the configuration.
func LinkFFS(root, instance string) error {
	fn := "ffs." + instance
	l := filepath.Join(root, "configs/c.1", fn)
	if exists(l) {
		return nil
	}
	return os.Symlink(filepath.Join(root, "functions", fn), l)
}

// Unbind takes the gadget off its UDC (the host sees a disconnect).
func Unbind(root string) error {
	if gadget.Bound(root) == "" {
		return nil
	}
	return write(filepath.Join(root, "UDC"), "\n")
}

// RemoveGadget takes the gadget of NewGadget apart again (configfs needs the links
// and folders removed in this order). The gadget must be unbound and the
// FunctionFS instance unmounted first.
func RemoveGadget(root, instance string) error {
	var errs []string
	rm := func(p string) {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err.Error())
		}
	}
	rm(filepath.Join(root, "configs/c.1", "ffs."+instance))
	rm(filepath.Join(root, "os_desc/c.1"))
	rm(filepath.Join(root, "configs/c.1/strings/0x409"))
	rm(filepath.Join(root, "configs/c.1"))
	rm(filepath.Join(root, "functions", "ffs."+instance))
	rm(filepath.Join(root, "strings/0x409"))
	rm(root)
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}
