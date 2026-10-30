package blk

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// ReadPartDirect reads n bytes at off of a partition with O_DIRECT, so the
// page cache cannot answer (the on-screen flow reads the A/B sector and the
// BCB this way after an install). off and n must be multiples of 512. A
// partition node that is a regular file (a test tree) is read without
// O_DIRECT.
func (d *Disk) ReadPartDirect(name string, off int64, n int) ([]byte, error) {
	p, err := d.Part(name)
	if err != nil {
		return nil, err
	}
	if off < 0 || off%512 != 0 || n <= 0 || n%512 != 0 || off+int64(n) > p.Size*512 {
		return nil, fmt.Errorf("%s: direct read %d+%d: not whole sectors inside the partition", name, off, n)
	}
	node := d.DevPath(p.Dev)
	st, err := os.Lstat(node)
	if err != nil {
		return nil, err
	}
	var f *os.File
	if st.Mode().IsRegular() {
		f, err = os.OpenFile(node, os.O_RDONLY, 0)
	} else {
		f, err = os.OpenFile(node, os.O_RDONLY|syscall.O_DIRECT, 0)
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// O_DIRECT needs an aligned buffer
	raw := make([]byte, n+4096)
	skip := int((4096 - uintptr(unsafe.Pointer(&raw[0]))%4096) % 4096)
	buf := raw[skip : skip+n]
	if _, err := f.ReadAt(buf, off); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return append([]byte(nil), buf...), nil
}
