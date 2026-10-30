package flowio

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"uc-pxx-installer/internal/drivestate"
	"uc-pxx-installer/internal/fatfs"
	"uc-pxx-installer/internal/fatmk"
)

// The only file writes of the flow (ci/goallow checks that no other
// function of this package writes): its own files in the run folder, and
// the drive file while the host does not have the drive. Nothing here can
// reach the eMMC: every path is checked to lie inside base (a folder in
// /run), and is opened with O_NOFOLLOW.

// inside reports whether p is base or a path under it, after Clean.
func inside(base, p string) bool {
	b, q := filepath.Clean(base), filepath.Clean(p)
	return q == b || strings.HasPrefix(q, b+"/")
}

func underKernelFS(p string) bool {
	q := filepath.Clean(p)
	for _, k := range []string{"/dev", "/proc", "/sys"} {
		if q == k || strings.HasPrefix(q, k+"/") {
			return true
		}
	}
	return false
}

// writeRunFile writes data to path (a new file renamed into place) inside
// base, with the mode perm. It makes the folders it needs.
func writeRunFile(base, path string, data []byte, perm os.FileMode) error {
	if !inside(base, path) || underKernelFS(path) || !strings.Contains(filepath.Clean(base), "/run/") && !strings.HasSuffix(filepath.Clean(base), "/run") {
		return fmt.Errorf("%s is not a file of the run folder %s", path, base)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp%d", path, os.Getpid())
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, perm)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp)
		return errors.Join(werr, cerr)
	}
	return os.Rename(tmp, path)
}

// DriveSize is the size of a new drive file when the old one is gone.
const DriveSize = 1200 << 20

// makeDriveFile makes the drive file again with a new FAT32 volume that
// holds files (the log bundle, or nothing), and checks it with package
// fatfs. The lun must be empty: the file is never written while the host
// has the drive. The old file is removed first (the RAM drive has room for
// one full drive only).
func makeDriveFile(lun, drive string, files []fatmk.File, now time.Time) error {
	if underKernelFS(drive) || !strings.Contains(filepath.Clean(drive), "/run/") {
		return fmt.Errorf("drive file %s: only a file in /run", drive)
	}
	empty, v, err := drivestate.LunEmpty(lun)
	if err != nil || !empty {
		return fmt.Errorf("the host has the drive (lun %q, %v): the drive file is not written", v, err)
	}
	size := int64(DriveSize)
	if st, err := os.Lstat(drive); err == nil {
		if !st.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", drive)
		}
		if st.Size() >= 33<<20 {
			size = st.Size()
		}
		if err := os.Remove(drive); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(drive, os.O_RDWR|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	err = f.Truncate(size)
	if err == nil {
		err = fatmk.Make(f, size, files, fatmk.Options{Label: "P10-INSTALL", Time: now, VolumeID: uint32(now.Unix())})
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return checkDrive(drive, files)
}

// checkDrive reads the new drive back with package fatfs.
func checkDrive(drive string, files []fatmk.File) error {
	fs, err := fatfs.OpenImage(drive)
	if err != nil {
		return fmt.Errorf("the new drive does not read back: %w", err)
	}
	defer fs.Close()
	if d := fs.Dirty(); len(d) > 0 {
		return fmt.Errorf("the new drive reads as dirty: %v", d)
	}
	if p := fs.Check(); len(p) > 0 {
		return fmt.Errorf("the new drive has errors: %v", p)
	}
	for _, x := range files {
		b, err := fs.ReadFile("/"+x.Path, int64(len(x.Data))+1)
		if err != nil || !bytes.Equal(b, x.Data) {
			return fmt.Errorf("the new drive: %s does not read back (%v)", x.Path, err)
		}
	}
	return nil
}

// removeRunFile removes one file of the run folder (an old end record).
func removeRunFile(base, path string) error {
	if !inside(base, path) || underKernelFS(path) {
		return fmt.Errorf("%s is not a file of the run folder %s", path, base)
	}
	return os.Remove(path)
}
