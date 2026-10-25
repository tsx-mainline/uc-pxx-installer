// Package devmem reads 32-bit SoC registers through /dev/mem. It only reads:
// the file is opened O_RDONLY and the page is mapped PROT_READ, so a write
// through this package is not possible.
//
// The installer reads two kinds of register: the boot-mode register
// (PMUGRF_OS_REG0), to show what the next plain restart does, and the USB 2.0
// PHY state in the GRF, to bind the USB gadget only while the PHY runs.
package devmem

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"syscall"
)

// Path is the memory device. Tests change it.
var Path = "/dev/mem"

// Compatible is the device tree file that must name the RK3399 before a
// register is read. Tests set it to "".
var Compatible = "/proc/device-tree/compatible"

// ErrNotRK3399 means that the machine is not an RK3399, so the register
// addresses of this package do not exist.
var ErrNotRK3399 = errors.New("not an RK3399")

// Read32 reads one 32-bit register. A bus fault becomes an error, not a
// crash.
func Read32(addr uint64) (v uint32, err error) {
	if addr%4 != 0 {
		return 0, fmt.Errorf("address %#x is not 4-byte aligned", addr)
	}
	if Compatible != "" {
		b, _ := os.ReadFile(Compatible)
		if !bytes.Contains(b, []byte("rockchip,rk3399")) {
			return 0, ErrNotRK3399
		}
	}
	defer debug.SetPanicOnFault(debug.SetPanicOnFault(true))
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("read %#x: %v", addr, r)
		}
	}()
	f, err := os.OpenFile(Path, os.O_RDONLY|syscall.O_SYNC, 0)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	page := uint64(os.Getpagesize())
	base := addr &^ (page - 1)
	m, err := syscall.Mmap(int(f.Fd()), int64(base), int(page), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		// A regular file (tests) cannot always be mapped: read it instead.
		b := make([]byte, 4)
		if _, rerr := f.ReadAt(b, int64(addr)); rerr != nil {
			return 0, fmt.Errorf("mmap %#x: %v", base, err)
		}
		return binary.LittleEndian.Uint32(b), nil
	}
	defer syscall.Munmap(m)
	off := addr - base
	return binary.LittleEndian.Uint32(m[off : off+4]), nil
}

// Boot-mode register of the RK3399 and the values of the vendor U-Boot
// (include/dt-bindings/soc/rockchip,boot-mode.h).
const (
	BootModeReg = 0xff320300
	bootMagic   = 0x5242c300
)

// BootModeName names a boot-mode value.
func BootModeName(v uint32) string {
	if v&0xffffff00 != bootMagic {
		if v == 0 {
			return "none (a normal boot)"
		}
		return "no boot-mode magic (a normal boot)"
	}
	switch v & 0xff {
	case 0x00:
		return "normal"
	case 0x01:
		return "loader (rockusb)"
	case 0x02:
		return "maskrom"
	case 0x03:
		return "recovery"
	case 0x09:
		return "fastboot"
	}
	return fmt.Sprintf("mode %#x", v&0xff)
}

// USB 2.0 PHY 0 of the RK3399 (inno-usb2phy, OTG port), in the GRF.
const (
	grfUSB2PHYStatus = 0xff77e2ac // bit 12: utmi_bvalid (the PHY sees VBUS)
	grfUSB2PHYSusp   = 0xff77e454 // bits 1:0: 2 = not suspended
)

// USBPHYActive reports whether the OTG PHY sees VBUS and is not suspended.
// The mainline PHY driver suspends the port while it sees no VBUS. That stops
// the UTMI clock of dwc3, and a gadget bound in that state never enumerates.
func USBPHYActive() (bool, error) {
	st, err := Read32(grfUSB2PHYStatus)
	if err != nil {
		return false, err
	}
	sus, err := Read32(grfUSB2PHYSusp)
	if err != nil {
		return false, err
	}
	return (st>>12)&1 == 1 && sus&3 == 2, nil
}
