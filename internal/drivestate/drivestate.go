// Package drivestate reads, and never changes, the state of the UC-PXX-INST
// drive: whether the USB host has it (the lun "file" of the gadget in
// configfs), and the identity of the RAM file behind it (size, times, inode).
//
// The on-screen flow and the device mode use it for one rule
// (INSTALLER-END-USER-PLAN.md §2.6, FAT-READER-REVIEW.md F1): the drive is
// detached from the host before the firmware is read, and it stays detached,
// with the file unchanged, until the install has ended. Nothing else writes
// the file while the lun is empty, so its size, its modification time, its
// change time and its inode stay the same. Any difference means that
// something changed the firmware under the installer.
package drivestate

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// LunFile is the backing-file attribute of the drive's lun (package gadget,
// uc-pxx-inst usbd). It holds the path of the drive file while the host has the
// drive, and nothing after an eject.
const LunFile = "/sys/kernel/config/usb_gadget/uc-pxx-inst/functions/mass_storage.usb0/lun.0/file"

// Image is the default drive file (image/overlay/etc/init.d/rcS).
const Image = "/run/uc-pxx-inst/drive/drive.img"

// Ident is the identity of the drive file at one moment.
type Ident struct {
	Size    int64
	MtimeNS int64
	CtimeNS int64
	Dev     uint64
	Ino     uint64
}

// String is the form of --fw-drive-expect: size=N,mtime=N,ctime=N,dev=N,ino=N.
func (i Ident) String() string {
	return fmt.Sprintf("size=%d,mtime=%d,ctime=%d,dev=%d,ino=%d", i.Size, i.MtimeNS, i.CtimeNS, i.Dev, i.Ino)
}

// Parse reads the String form. Every field must be there, once.
func Parse(s string) (Ident, error) {
	var id Ident
	seen := map[string]bool{}
	for _, kv := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || seen[k] {
			return id, fmt.Errorf("drive identity %q: bad field %q", s, kv)
		}
		seen[k] = true
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil || n > 1<<62 {
			return id, fmt.Errorf("drive identity %q: bad number in %q", s, kv)
		}
		switch k {
		case "size":
			id.Size = int64(n)
		case "mtime":
			id.MtimeNS = int64(n)
		case "ctime":
			id.CtimeNS = int64(n)
		case "dev":
			id.Dev = n
		case "ino":
			id.Ino = n
		default:
			return id, fmt.Errorf("drive identity %q: unknown field %q", s, k)
		}
	}
	if len(seen) != 5 {
		return id, fmt.Errorf("drive identity %q: needs size, mtime, ctime, dev and ino", s)
	}
	return id, nil
}

// Stat reads the identity of the drive file. It must be a regular file (not
// a symlink, not a device node).
func Stat(path string) (Ident, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return Ident{}, err
	}
	if !fi.Mode().IsRegular() {
		return Ident{}, fmt.Errorf("%s is not a regular file (%v)", path, fi.Mode())
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return Ident{}, errors.New("no stat data")
	}
	return Ident{Size: st.Size, MtimeNS: st.Mtim.Nano(), CtimeNS: st.Ctim.Nano(), Dev: uint64(st.Dev), Ino: st.Ino}, nil
}

// LunEmpty reports whether the lun file attribute is empty: the host does
// not have the drive. A lun file that cannot be read is an error, never
// "empty".
func LunEmpty(lun string) (bool, string, error) {
	b, err := os.ReadFile(lun)
	if err != nil {
		return false, "", err
	}
	v := strings.TrimSpace(string(b))
	return v == "", v, nil
}

// Detached is the rule of the drive: the lun is empty and the file has the
// identity want. It returns nil, or what is different.
func Detached(lun, image string, want Ident) error {
	empty, v, err := LunEmpty(lun)
	switch {
	case err != nil:
		return fmt.Errorf("the drive state cannot be read (%v): the drive may be attached to the computer", err)
	case !empty:
		return fmt.Errorf("the UC-PXX-INST drive is attached to the computer again (the lun holds %q)", v)
	}
	got, err := Stat(image)
	if err != nil {
		return fmt.Errorf("the drive file: %v", err)
	}
	if got != want {
		return fmt.Errorf("the drive file changed: it is %s, it was %s", got, want)
	}
	return nil
}
