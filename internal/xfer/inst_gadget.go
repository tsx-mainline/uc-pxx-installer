package xfer

// The USB gadget of the RAM installer (mainline-rc-10, I-6): the identity of
// the step-2 transport (1d6b:0105, the vendor interface first, serial
// UC-PXX-STEP2), so that the browser's permission of step 4 also covers the
// steps on the mainline slots, plus the installer's serial shell (ACM) and
// network (NCM) as the 1d6b:0104 gadget of `uc-pxx-inst usbd` had them, and
// in the drive mode the RAM drive UC-PXX-INST (mass storage). A composite
// device with interface associations (class EF/02/01). Its own bcdDevice
// values (Windows caches the MS OS answer per VID, PID and bcdDevice):
// InstBCDDevice without the drive, InstBCDDeviceDrive with it. It reuses
// internal/gadget for the UDC only; internal/gadget itself (linked by the
// status build of uc-pxx-inst) does not change.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	InstBCDDevice      = 0x0301
	InstBCDDeviceDrive = 0x0302
	// InstConfigFS is the gadget folder: the one of `uc-pxx-inst usbd`, so
	// that the drive's lun file (drivestate.LunFile) is where the flow and
	// the device mode read it.
	InstConfigFS = "/sys/kernel/config/usb_gadget/uc-pxx-inst"
	InstInstance = "uc-pxx-inst"
	// the functions after the vendor interface (the names of uc-pxx-inst usbd)
	instACM   = "acm.usb0"
	instNCM   = "ncm.usb0"
	instDrive = "mass_storage.usb0"
	// the NCM addresses of uc-pxx-inst usbd (the installer's 10.99.0.2 network)
	instNCMHost = "02:70:31:30:00:01"
	instNCMDev  = "02:70:31:30:00:02"
)

// NewInstGadget makes the gadget folders of the RAM installer: the device
// (VID, PID, serial and strings of the step-2 transport, class EF/02/01,
// bcdDevice InstBCDDevice), MS OS 1.0 descriptors, WebUSB when configfs has
// it, and the functions: the FunctionFS function (linked first, by
// LinkInst, once its daemon wrote the descriptors), ACM and NCM. No drive:
// AddInstDrive adds it.
func NewInstGadget(root string) error {
	c := GadgetConfig{Root: root, Instance: InstInstance, VendorID: USBVendorID, ProductID: USBProductID,
		BCDDevice: InstBCDDevice, Manufacturer: USBMaker, Product: USBProduct, Serial: USBSerial, Config: "installer",
		VendorCode: MSOSVendorCode, LandingPage: LandingPage, WebUSBCode: WebUSBCode}
	if err := NewGadget(c); err != nil {
		return err
	}
	// a composite device with interface associations (ACM, NCM)
	for _, s := range [][2]string{{"bDeviceClass", "0xef"}, {"bDeviceSubClass", "0x02"}, {"bDeviceProtocol", "0x01"}} {
		if err := write(filepath.Join(root, s[0]), s[1]); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "functions", instACM), 0o755); err != nil {
		return err
	}
	n := filepath.Join(root, "functions", instNCM)
	if err := os.MkdirAll(n, 0o755); err != nil {
		return err
	}
	for _, s := range [][2]string{{"host_addr", instNCMHost}, {"dev_addr", instNCMDev}} {
		if err := write(filepath.Join(n, s[0]), s[1]); err != nil {
			return err
		}
	}
	return nil
}

// AddInstDrive adds the RAM drive (mass storage, the file drive) and sets
// InstBCDDeviceDrive. The gadget must be unbound. The file must be a regular
// file of a tmpfs (the RAM drive of the image's rcS), never a device node.
func AddInstDrive(root, drive string) error {
	abs, err := filepath.Abs(drive)
	if err != nil {
		return err
	}
	if err := driveInRAM(abs); err != nil {
		return fmt.Errorf("drive backing file %q: %v: only a RAM file is allowed", drive, err)
	}
	lun := filepath.Join(root, "functions", instDrive, "lun.0")
	if err := os.MkdirAll(lun, 0o755); err != nil {
		return err
	}
	for _, s := range [][2]string{{"removable", "1"}, {"ro", "0"}, {"nofua", "0"}, {"file", abs}} {
		if err := write(filepath.Join(lun, s[0]), s[1]); err != nil {
			return err
		}
	}
	if err := write(filepath.Join(root, "bcdDevice"), fmt.Sprintf("0x%04x", InstBCDDeviceDrive)); err != nil {
		return err
	}
	return linkFunc(root, instDrive)
}

// driveInRAM: p is a regular file (no link) of a tmpfs. A variable for the
// tests (their folder need not be a tmpfs).
var driveInRAM = func(p string) error {
	fi, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	var sf syscall.Statfs_t
	if err := syscall.Statfs(p, &sf); err != nil {
		return err
	}
	if int64(sf.Type) != tmpfsMagic {
		return fmt.Errorf("not on a tmpfs (type %#x)", sf.Type)
	}
	return nil
}

func linkFunc(root, fn string) error {
	l := filepath.Join(root, "configs/c.1", fn)
	if exists(l) {
		return nil
	}
	return os.Symlink(filepath.Join(root, "functions", fn), l)
}

// LinkInst links the functions into the configuration in their order: the
// vendor interface (interface 0), ACM, NCM (the drive is linked by
// AddInstDrive, after them).
func LinkInst(root string) error {
	for _, fn := range []string{"ffs." + InstInstance, instACM, instNCM} {
		if err := linkFunc(root, fn); err != nil {
			return err
		}
	}
	return nil
}

// InstFunctions lists the functions linked into the configuration, in the
// order of their links' names (the test of the drive mode).
func InstFunctions(root string) ([]string, error) {
	ents, err := os.ReadDir(filepath.Join(root, "configs/c.1"))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.Type()&os.ModeSymlink != 0 {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// RemoveInstGadget takes the gadget of NewInstGadget apart (unbound, the
// FunctionFS instance unmounted).
func RemoveInstGadget(root string) error {
	var errs []string
	rm := func(p string) {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err.Error())
		}
	}
	for _, fn := range []string{"ffs." + InstInstance, instACM, instNCM, instDrive} {
		rm(filepath.Join(root, "configs/c.1", fn))
	}
	rm(filepath.Join(root, "os_desc/c.1"))
	rm(filepath.Join(root, "configs/c.1/strings/0x409"))
	rm(filepath.Join(root, "configs/c.1"))
	for _, fn := range []string{"ffs." + InstInstance, instACM, instNCM, instDrive} {
		rm(filepath.Join(root, "functions", fn))
	}
	rm(filepath.Join(root, "strings/0x409"))
	rm(root)
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}
