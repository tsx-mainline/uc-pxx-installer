package flowio

// The reads of the eMMC and the changes of the gadget that only the flow
// needs. They are here, not in packages blk and gadget, so that the status
// build (which has blk and gadget, but not this package) stays the same
// binary.

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"

	"uc-pxx-installer/internal/blk"
)

// readPartDirect reads n bytes at off of a partition with O_DIRECT, so the
// page cache cannot answer (the A/B sector and the BCB after an install).
// off and n must be multiples of 512. A partition node that is a regular
// file (a test tree) is read without O_DIRECT. It opens O_RDONLY only.
func readPartDirect(d *blk.Disk, name string, off int64, n int) ([]byte, error) {
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
	// O_DIRECT needs an aligned buffer (the address through reflect: no unsafe)
	raw := make([]byte, n+4096)
	skip := int((4096 - reflect.ValueOf(&raw[0]).Pointer()%4096) % 4096)
	buf := raw[skip : skip+n]
	if _, err := f.ReadAt(buf, off); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return append([]byte(nil), buf...), nil
}

func lunFile(gadgetDir string) string {
	return filepath.Join(gadgetDir, "functions/mass_storage.usb0/lun.0/file")
}

// ejectDrive takes the drive from the host now, as an eject on the host
// would: forced_eject when the kernel has it (it works while the host
// prevents the removal), else an empty lun file. It never touches the drive
// file itself.
func ejectDrive(gadgetDir string) error {
	fe := filepath.Join(gadgetDir, "functions/mass_storage.usb0/lun.0/forced_eject")
	if _, err := os.Stat(fe); err == nil {
		if err := os.WriteFile(fe, []byte("1"), 0o644); err == nil {
			return nil
		}
	}
	return os.WriteFile(lunFile(gadgetDir), []byte("\n"), 0o644)
}

// attachDrive gives the drive file to the host again (a removable lun: the
// host sees a new medium). The file must be a RAM file in /run.
func attachDrive(gadgetDir, drive string) error {
	abs, err := filepath.Abs(drive)
	if err != nil {
		return err
	}
	if underKernelFS(abs) || !strings.Contains(abs, "/run/") {
		return fmt.Errorf("drive backing file %s: only a RAM file in /run", drive)
	}
	return os.WriteFile(lunFile(gadgetDir), []byte(abs), 0o644)
}
