//go:build install && devtest

// Package looptest attaches a loop device to a range of a file (as root;
// tests and devtest dry runs only: ci/seed-root-test.sh, the devtest image
// seeder). It uses the loop ioctls directly, so no losetup is needed.
package looptest

import (
	"encoding/binary"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

const (
	ctlGetFree   = 0x4C82
	setFD        = 0x4C00
	clrFD        = 0x4C01
	setStatus64  = 0x4C04
	flagAutoclr  = 4
	loopMajor    = 7
	infoSize     = 232
	offOffset    = 24
	offSizelimit = 32
	offFlags     = 52
	blkROSet     = 0x125d
)

func ioctl(fd, req, arg uintptr) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, arg); e != 0 {
		return e
	}
	return nil
}

// Attach makes a loop device over [off, off+size) of path (read-write). The
// returned func detaches it.
func Attach(path string, off, size int64) (string, func(), error) {
	ctl, err := os.OpenFile("/dev/loop-control", os.O_RDWR, 0)
	if err != nil {
		return "", nil, err
	}
	defer ctl.Close()
	n, _, e := syscall.Syscall(syscall.SYS_IOCTL, ctl.Fd(), ctlGetFree, 0)
	if e != 0 {
		return "", nil, fmt.Errorf("LOOP_CTL_GET_FREE: %v", e)
	}
	dev := fmt.Sprintf("/dev/loop%d", n)
	if _, err := os.Stat(dev); os.IsNotExist(err) {
		if err := syscall.Mknod(dev, syscall.S_IFBLK|0o600, int(loopMajor<<8|(n&0xff)|((n&^0xff)<<12))); err != nil {
			return "", nil, fmt.Errorf("mknod %s: %v", dev, err)
		}
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return "", nil, err
	}
	defer f.Close()
	ld, err := os.OpenFile(dev, os.O_RDWR, 0)
	if err != nil {
		return "", nil, err
	}
	if err := ioctl(ld.Fd(), setFD, f.Fd()); err != nil {
		ld.Close()
		return "", nil, fmt.Errorf("LOOP_SET_FD %s: %v", dev, err)
	}
	var info [infoSize]byte
	binary.LittleEndian.PutUint64(info[offOffset:], uint64(off))
	binary.LittleEndian.PutUint64(info[offSizelimit:], uint64(size))
	binary.LittleEndian.PutUint32(info[offFlags:], flagAutoclr)
	if err := ioctl(ld.Fd(), setStatus64, uintptr(unsafe.Pointer(&info[0]))); err != nil {
		ioctl(ld.Fd(), clrFD, 0)
		ld.Close()
		return "", nil, fmt.Errorf("LOOP_SET_STATUS64 %s: %v", dev, err)
	}
	// a loop device keeps its kernel read-only flag across users: start and
	// end with it cleared (a test that sets it must not leave it to the next)
	zero := int32(0)
	if err := ioctl(ld.Fd(), blkROSet, uintptr(unsafe.Pointer(&zero))); err != nil {
		ioctl(ld.Fd(), clrFD, 0)
		ld.Close()
		return "", nil, fmt.Errorf("BLKROSET 0 %s: %v", dev, err)
	}
	return dev, func() {
		z := int32(0)
		ioctl(ld.Fd(), blkROSet, uintptr(unsafe.Pointer(&z)))
		ioctl(ld.Fd(), clrFD, 0)
		ld.Close()
	}, nil
}
