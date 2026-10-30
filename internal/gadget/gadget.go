// Package gadget makes the composite USB gadget of the installer in configfs:
//
//   - serial (CDC-ACM, /dev/ttyGS0): a root shell on the device
//   - network (CDC-NCM, usb0): ssh and copies for power users
//   - drive (mass storage): the RAM-backed "P10-INSTALL" drive
//
// All three use USB classes that Windows 10/11, macOS and Linux drive with
// their built-in drivers. The drive is backed by a file in RAM, never by an
// eMMC device: New refuses a backing file under /dev.
//
// The USB-C port of the UC-P10 needs one more step. The mainline USB 2.0 PHY
// driver suspends the OTG port while it sees no VBUS, and then the dwc3
// controller has no clock. A gadget that is bound in that state does not
// enumerate when a cable comes later. So Supervise binds the gadget only while
// the PHY is active, and it reconnects the controller each time the PHY
// becomes active again (a cable plugged in).
package gadget

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config describes the gadget.
type Config struct {
	Root      string // configfs gadget folder, for example /sys/kernel/config/usb_gadget/p10inst
	Serial    string // USB serial number string
	Product   string
	Drive     string // backing file of the drive, "" = no drive
	DriveRO   bool
	NCM       bool
	ACM       bool
	HostMAC   string // NCM MAC on the host side
	DeviceMAC string // NCM MAC on the device side
}

func write(p, v string) error {
	if err := os.WriteFile(p, []byte(v), 0o644); err != nil {
		return fmt.Errorf("%s: %w", p, err)
	}
	return nil
}

// New makes the gadget folders. It does not bind the gadget.
func New(c Config) error {
	if c.Drive != "" {
		abs, err := filepath.Abs(c.Drive)
		if err != nil {
			return err
		}
		if strings.HasPrefix(abs, "/dev/") || strings.HasPrefix(abs, "/sys/") {
			return fmt.Errorf("drive backing file %s: only a RAM file is allowed", c.Drive)
		}
	}
	if !c.ACM && !c.NCM && c.Drive == "" {
		return errors.New("no function")
	}
	g := c.Root
	if err := os.MkdirAll(g, 0o755); err != nil {
		return err
	}
	steps := [][2]string{
		// Linux Foundation, multifunction composite gadget
		{"idVendor", "0x1d6b"},
		{"idProduct", "0x0104"},
		{"bcdDevice", "0x0100"},
		{"bcdUSB", "0x0200"},
		// Miscellaneous device, interface association: hosts bind each
		// function to its own class driver.
		{"bDeviceClass", "0xef"},
		{"bDeviceSubClass", "0x02"},
		{"bDeviceProtocol", "0x01"},
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
	for k, v := range map[string]string{"manufacturer": "uc-pxx", "product": c.Product, "serialnumber": c.Serial} {
		if err := write(filepath.Join(str, k), v); err != nil {
			return err
		}
	}
	if err := write(filepath.Join(cstr, "configuration"), "installer"); err != nil {
		return err
	}
	if err := write(filepath.Join(cfg, "MaxPower"), "100"); err != nil {
		return err
	}
	link := func(fn string) error {
		l := filepath.Join(cfg, fn)
		if _, err := os.Lstat(l); err == nil {
			return nil
		}
		return os.Symlink(filepath.Join(g, "functions", fn), l)
	}
	if c.ACM {
		if err := os.MkdirAll(filepath.Join(g, "functions/acm.usb0"), 0o755); err != nil {
			return err
		}
		if err := link("acm.usb0"); err != nil {
			return err
		}
	}
	if c.NCM {
		fn := filepath.Join(g, "functions/ncm.usb0")
		if err := os.MkdirAll(fn, 0o755); err != nil {
			return err
		}
		if c.HostMAC != "" {
			if err := write(filepath.Join(fn, "host_addr"), c.HostMAC); err != nil {
				return err
			}
		}
		if c.DeviceMAC != "" {
			if err := write(filepath.Join(fn, "dev_addr"), c.DeviceMAC); err != nil {
				return err
			}
		}
		if err := link("ncm.usb0"); err != nil {
			return err
		}
	}
	if c.Drive != "" {
		fn := filepath.Join(g, "functions/mass_storage.usb0")
		lun := filepath.Join(fn, "lun.0")
		if err := os.MkdirAll(lun, 0o755); err != nil {
			return err
		}
		ro := "0"
		if c.DriveRO {
			ro = "1"
		}
		for _, s := range [][2]string{{"removable", "1"}, {"ro", ro}, {"nofua", "0"}, {"file", c.Drive}} {
			if err := write(filepath.Join(lun, s[0]), s[1]); err != nil {
				return err
			}
		}
		if err := link("mass_storage.usb0"); err != nil {
			return err
		}
	}
	return nil
}

// Bound returns the UDC that the gadget is bound to, or "".
func Bound(root string) string {
	b, _ := os.ReadFile(filepath.Join(root, "UDC"))
	return strings.TrimSpace(string(b))
}

// Bind binds the gadget to a UDC.
func Bind(root, udc string) error {
	return write(filepath.Join(root, "UDC"), udc)
}

// FirstUDC returns the first controller in /sys/class/udc, or "".
func FirstUDC(sysRoot string) string {
	m, _ := filepath.Glob(filepath.Join(sysRoot, "sys/class/udc/*"))
	if len(m) == 0 {
		return ""
	}
	return filepath.Base(m[0])
}

// UDCState is the USB state of the controller ("configured", "not attached", ...).
func UDCState(sysRoot, udc string) string {
	b, err := os.ReadFile(filepath.Join(sysRoot, "sys/class/udc", udc, "state"))
	if err != nil {
		return "absent"
	}
	return strings.TrimSpace(string(b))
}

// Reconnect makes the controller drop off the bus and come back. dwc3 then
// does a core reset and restarts the gadget.
func Reconnect(sysRoot, udc string) error {
	p := filepath.Join(sysRoot, "sys/class/udc", udc, "soft_connect")
	if err := write(p, "disconnect"); err != nil {
		return err
	}
	return write(p, "connect")
}

// LunFile is the backing-file attribute of the drive's lun.
func LunFile(root string) string {
	return filepath.Join(root, "functions/mass_storage.usb0/lun.0/file")
}

// Eject takes the drive away from the host now, as an eject on the host
// would: forced_eject when the kernel has it (it works while the host
// prevents the removal), else an empty lun file. It never touches the
// backing file itself.
func Eject(root string) error {
	fe := filepath.Join(root, "functions/mass_storage.usb0/lun.0/forced_eject")
	if _, err := os.Stat(fe); err == nil {
		if err := write(fe, "1"); err == nil {
			return nil
		}
	}
	return write(LunFile(root), "\n")
}

// Attach gives the drive file to the host again (a removable lun: the host
// sees a new medium). The file must be a RAM file, never a device.
func Attach(root, drive string) error {
	abs, err := filepath.Abs(drive)
	if err != nil {
		return err
	}
	if strings.HasPrefix(abs, "/dev/") || strings.HasPrefix(abs, "/sys/") || strings.HasPrefix(abs, "/proc/") {
		return fmt.Errorf("drive backing file %s: only a RAM file is allowed", drive)
	}
	return write(LunFile(root), abs)
}

// Ejected reports whether the host has ejected the drive (START STOP UNIT):
// the kernel then clears the lun file.
func Ejected(root string) bool {
	b, err := os.ReadFile(filepath.Join(root, "functions/mass_storage.usb0/lun.0/file"))
	return err == nil && strings.TrimSpace(string(b)) == ""
}
