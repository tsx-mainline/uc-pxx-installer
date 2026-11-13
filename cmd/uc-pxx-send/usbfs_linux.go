// SPDX-License-Identifier: GPL-2.0-or-later
//go:build linux && (amd64 || arm64)

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"uc-pxx-installer/internal/sender"
	"uc-pxx-installer/internal/xfer"
)

// usbfs is a Transport over Linux usbfs (/dev/bus/usb): it claims the vendor
// interface of the uc-pxx-xfer device and does bulk OUT and bulk IN transfers.
// It writes nothing to the unit's storage; it only moves frames and file data
// over the vendor-class endpoints.
type usbfs struct {
	f          *os.File
	iface      uint32
	epOut      byte
	epIn       byte
	claimed    bool
	readBuf    []byte
	timeoutOut uint32
	timeoutIn  uint32
}

// ioctl numbers (linux usbdevfs; the _IOC encoding is the same on amd64 and
// arm64). The direction/size fields match the kernel structs below.
const (
	usbdevfsBulk           = 0xc0185502 // USBDEVFS_BULK: _IOWR('U', 2, struct usbdevfs_bulktransfer) (24 bytes)
	usbdevfsClaimInterface = 0x8004550f // USBDEVFS_CLAIMINTERFACE: _IOR('U', 15, unsigned int)
	usbdevfsReleaseIface   = 0x80045510 // USBDEVFS_RELEASEINTERFACE: _IOR('U', 16, unsigned int)
	usbdevfsSetInterface   = 0x80085504 // USBDEVFS_SETINTERFACE: _IOR('U', 4, struct usbdevfs_setinterface) (8 bytes)
)

// bulkTransfer is struct usbdevfs_bulktransfer (24 bytes on 64-bit: a 4-byte
// pad after timeout aligns the pointer).
type bulkTransfer struct {
	ep      uint32
	len     uint32
	timeout uint32
	_       uint32
	data    uintptr
}

type setInterface struct {
	iface uint32
	alt   uint32
}

func openUSB(devArg string) (sender.Transport, error) {
	node, err := findDevice(devArg)
	if err != nil {
		return nil, err
	}
	iface, epOut, epIn, alt, err := vendorEndpoints(node)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(node, os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	// a USB character device (major 189, USB_DEVICE_MAJOR), never a block
	// device or another node (review MINOR-4)
	var st syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &st); err != nil || st.Mode&syscall.S_IFMT != syscall.S_IFCHR || devMajor(uint64(st.Rdev)) != 189 {
		f.Close()
		return nil, fmt.Errorf("%s is not a USB character device (major 189): %v", node, err)
	}
	u := &usbfs{f: f, iface: iface, epOut: epOut, epIn: epIn,
		readBuf: make([]byte, xfer.FrameRead), timeoutOut: 20000, timeoutIn: 300000}
	if err := ioctl(f.Fd(), usbdevfsClaimInterface, unsafe.Pointer(&iface)); err != nil {
		f.Close()
		return nil, fmt.Errorf("claiming interface %d: %w", iface, err)
	}
	u.claimed = true
	si := setInterface{iface: iface, alt: alt}
	if err := ioctl(f.Fd(), usbdevfsSetInterface, unsafe.Pointer(&si)); err != nil {
		u.Close()
		return nil, fmt.Errorf("set interface %d alt %d: %w", iface, alt, err)
	}
	return u, nil
}

func (u *usbfs) Out(b []byte) error {
	return u.bulk(u.epOut, b, u.timeoutOut, false)
}

func (u *usbfs) In() ([]byte, error) {
	n, err := u.bulkIn(u.readBuf, u.timeoutIn)
	if err != nil {
		return nil, err
	}
	out := make([]byte, n)
	copy(out, u.readBuf[:n])
	return out, nil
}

func (u *usbfs) Close() error {
	if u.claimed {
		i := u.iface
		ioctl(u.f.Fd(), usbdevfsReleaseIface, unsafe.Pointer(&i))
		u.claimed = false
	}
	return u.f.Close()
}

// bulk does one bulk OUT (in==false). A short write is an error.
func (u *usbfs) bulk(ep byte, b []byte, timeout uint32, in bool) error {
	var p unsafe.Pointer
	if len(b) > 0 {
		p = unsafe.Pointer(&b[0])
	}
	bt := bulkTransfer{ep: uint32(ep), len: uint32(len(b)), timeout: timeout, data: uintptr(p)}
	n, err := ioctlN(u.f.Fd(), usbdevfsBulk, unsafe.Pointer(&bt))
	runtime.KeepAlive(b) // the buffer is a uintptr in bt: keep it alive across the ioctl (review MINOR-5)
	if err != nil {
		return err
	}
	if n != len(b) {
		return fmt.Errorf("short bulk transfer: %d of %d bytes", n, len(b))
	}
	return nil
}

// bulkIn does one bulk IN into b and returns the byte count.
func (u *usbfs) bulkIn(b []byte, timeout uint32) (int, error) {
	bt := bulkTransfer{ep: uint32(u.epIn), len: uint32(len(b)), timeout: timeout, data: uintptr(unsafe.Pointer(&b[0]))}
	n, err := ioctlN(u.f.Fd(), usbdevfsBulk, unsafe.Pointer(&bt))
	runtime.KeepAlive(b)
	return n, err
}

// devMajor is the major number of a Linux dev_t (glibc's gnu_dev_major).
func devMajor(d uint64) uint64 { return (d>>8)&0xfff | (d>>32)&^uint64(0xfff) }

func ioctl(fd uintptr, req uint, arg unsafe.Pointer) error {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(req), uintptr(arg))
	if e != 0 {
		return e
	}
	return nil
}

func ioctlN(fd uintptr, req uint, arg unsafe.Pointer) (int, error) {
	r, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(req), uintptr(arg))
	if e != 0 {
		return 0, e
	}
	return int(r), nil
}

// findDevice returns the /dev/bus/usb/BBB/AAA node of the uc-pxx-xfer device.
// devArg "BUS:ADDR" names one; empty finds the single 1d6b:0105 device.
func findDevice(devArg string) (string, error) {
	if devArg != "" {
		parts := strings.SplitN(devArg, ":", 2)
		if len(parts) != 2 {
			return "", fmt.Errorf("--dev %q is not BUS:ADDR", devArg)
		}
		bus, e1 := strconv.Atoi(parts[0])
		addr, e2 := strconv.Atoi(parts[1])
		if e1 != nil || e2 != nil || bus < 1 || bus > 999 || addr < 1 || addr > 127 {
			return "", fmt.Errorf("--dev %q is not BUS:ADDR (bus 1..999, address 1..127)", devArg)
		}
		node := fmt.Sprintf("/dev/bus/usb/%03d/%03d", bus, addr)
		if _, err := os.Stat(node); err != nil {
			return "", fmt.Errorf("no device at %s", node)
		}
		return node, nil
	}
	var found []string
	dirs, _ := filepath.Glob("/sys/bus/usb/devices/*")
	for _, d := range dirs {
		vid := strings.TrimSpace(readFile(filepath.Join(d, "idVendor")))
		pid := strings.TrimSpace(readFile(filepath.Join(d, "idProduct")))
		if vid != fmt.Sprintf("%04x", xfer.USBVendorID) || pid != fmt.Sprintf("%04x", xfer.USBProductID) {
			continue
		}
		bus := strings.TrimSpace(readFile(filepath.Join(d, "busnum")))
		addr := strings.TrimSpace(readFile(filepath.Join(d, "devnum")))
		b, _ := strconv.Atoi(bus)
		a, _ := strconv.Atoi(addr)
		found = append(found, fmt.Sprintf("/dev/bus/usb/%03d/%03d", b, a))
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("no uc-pxx-xfer device (%04x:%04x) on the host", xfer.USBVendorID, xfer.USBProductID)
	case 1:
		return found[0], nil
	default:
		return "", fmt.Errorf("more than one uc-pxx-xfer device: name one with --dev BUS:ADDR (%v)", found)
	}
}

// vendorEndpoints parses the device's descriptors and returns the vendor
// interface number, its bulk OUT and bulk IN endpoint addresses and its
// alternate setting. It reads the whole descriptor set from the usbfs node.
func vendorEndpoints(node string) (iface uint32, epOut, epIn byte, alt uint32, err error) {
	f, err := os.Open(node)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	defer f.Close()
	buf := make([]byte, 4096)
	n, err := f.Read(buf)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	d := buf[:n]
	// the device descriptor first: the uc-pxx-xfer id 1d6b:0105, also with
	// --dev (review MINOR-4)
	if len(d) < 18 || d[0] != 18 || d[1] != 0x01 || binary.LittleEndian.Uint16(d[8:]) != xfer.USBVendorID ||
		binary.LittleEndian.Uint16(d[10:]) != xfer.USBProductID {
		return 0, 0, 0, 0, fmt.Errorf("%s is not the uc-pxx-xfer device %04x:%04x", node, xfer.USBVendorID, xfer.USBProductID)
	}
	// walk the descriptor list: [bLength, bDescriptorType, ...]
	var curIface = -1
	var curAlt uint32
	var isVendor bool
	var gotOut, gotIn bool
	for i := 0; i+2 <= len(d); {
		l := int(d[i])
		if l < 2 || i+l > len(d) {
			break
		}
		switch d[i+1] {
		case 0x04: // INTERFACE
			cls, sub, proto := d[i+5], d[i+6], d[i+7]
			curIface = int(d[i+2])
			curAlt = uint32(d[i+3])
			isVendor = cls == xfer.IfaceClass && sub == xfer.IfaceSubclass && proto == xfer.IfaceProtocol
			if isVendor {
				gotOut, gotIn = false, false
			}
		case 0x05: // ENDPOINT
			if isVendor && l >= 4 {
				addr := d[i+2]
				attr := d[i+3] & 0x03
				if attr == 0x02 { // bulk
					if addr&0x80 != 0 {
						epIn, gotIn = addr, true
					} else {
						epOut, gotOut = addr, true
					}
				}
			}
			if isVendor && gotOut && gotIn {
				return uint32(curIface), epOut, epIn, curAlt, nil
			}
		}
		i += l
	}
	return 0, 0, 0, 0, errors.New("no vendor interface with a bulk IN and a bulk OUT endpoint on the device")
}

func readFile(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}
