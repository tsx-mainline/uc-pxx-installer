package gadget

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

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
