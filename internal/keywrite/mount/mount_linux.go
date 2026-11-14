//go:build install

// Package mount is the eMMC write of the ssh key seed (package keywrite):
// the read-write mount of a slot's root file system, the two files, the
// unmount. Only the install side imports it (the device medium; the devtest
// image seeder).
package mount

// The read-write mount of the seed (the device mode of the RAM installer;
// devtest dry runs run the same code on a loop device in a privileged test
// container). Everything is relative to the mount's folder fd: no path of the
// root file system is followed as a link.

import (
	"fmt"
	"os"
	"path"
	"syscall"
	"unsafe"

	"uc-pxx-installer/internal/keywrite"
)

// Stage and Hook are keywrite's (the types only; the cut tests).
type (
	Stage = keywrite.Stage
	Hook  = keywrite.Hook
)

const (
	StageMount   = keywrite.StageMount
	StageMkdir   = keywrite.StageMkdir
	StageWrite   = keywrite.StageWrite
	StageRename  = keywrite.StageRename
	StageUnmount = keywrite.StageUnmount
)

const mountFlags = syscall.MS_NOATIME | syscall.MS_NODEV | syscall.MS_NOSUID | syscall.MS_NOEXEC

// MountWrite mounts the ext4 on node at mnt (an empty folder that it makes,
// 0700), writes the files and unmounts. madeFolder tells whether it made
// /data/tsx. On any error after the mount it still unmounts (the error is
// returned; the caller holds the install: slot B stays {0,0,0}).
func MountWrite(node, mnt string, files []keywrite.File, folderThere bool, hook Hook) (madeFolder bool, err error) {
	if hook == nil {
		hook = func(Stage, string) error { return nil }
	}
	for _, f := range files {
		if !keywrite.ValidPath(f.Path) {
			return false, fmt.Errorf("the seed path %s is not one of the two", f.Path)
		}
	}
	if err := os.Mkdir(mnt, 0o700); err != nil {
		return false, fmt.Errorf("the mount folder %s: %v", mnt, err)
	}
	defer os.Remove(mnt)
	if err := hook(StageMount, ""); err != nil {
		return false, err
	}
	if err := syscall.Mount(node, mnt, "ext4", mountFlags, keywrite.MountOptions); err != nil {
		return false, fmt.Errorf("mount %s (ext4, %s): %v", node, keywrite.MountOptions, err)
	}
	mounted := true
	defer func() {
		if mounted {
			if uerr := syscall.Unmount(mnt, 0); uerr != nil && err == nil {
				err = fmt.Errorf("unmount %s: %v", mnt, uerr)
			}
		}
	}()
	root, err := openDir(-1, mnt)
	if err != nil {
		return false, err
	}
	defer root.Close()
	data, err := openDir(int(root.Fd()), "data")
	if err != nil {
		return false, fmt.Errorf("/data: %v", err)
	}
	defer data.Close()
	if !folderThere {
		if err := hook(StageMkdir, ""); err != nil {
			return false, err
		}
		if err := syscall.Mkdirat(int(data.Fd()), "tsx", keywrite.FolderMode); err != nil {
			return false, fmt.Errorf("mkdir %s: %v", keywrite.Folder, err)
		}
		madeFolder = true
	}
	tsx, err := openDir(int(data.Fd()), "tsx")
	if err != nil {
		return madeFolder, fmt.Errorf("%s: %v", keywrite.Folder, err)
	}
	defer tsx.Close()
	if madeFolder {
		if err := tsx.Chown(0, 0); err != nil {
			return madeFolder, err
		}
		if err := tsx.Chmod(keywrite.FolderMode); err != nil {
			return madeFolder, err
		}
	}
	for _, f := range files {
		name := path.Base(f.Path)
		if err := hook(StageWrite, f.Path); err != nil {
			return madeFolder, err
		}
		fd, err := syscall.Openat(int(tsx.Fd()), name+".new", syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, uint32(f.Mode))
		if err != nil {
			return madeFolder, fmt.Errorf("create %s.new: %v", f.Path, err)
		}
		w := os.NewFile(uintptr(fd), f.Path+".new")
		_, werr := w.Write(f.Data)
		if werr == nil {
			werr = w.Chown(0, 0)
		}
		if werr == nil {
			werr = w.Chmod(os.FileMode(f.Mode))
		}
		if werr == nil {
			werr = w.Sync()
		}
		if cerr := w.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return madeFolder, fmt.Errorf("write %s.new: %v", f.Path, werr)
		}
		if err := hook(StageRename, f.Path); err != nil {
			return madeFolder, err
		}
		if err := renameNoReplace(int(tsx.Fd()), name+".new", name); err != nil {
			return madeFolder, fmt.Errorf("rename %s: %v", f.Path, err)
		}
		if err := tsx.Sync(); err != nil {
			return madeFolder, fmt.Errorf("fsync %s: %v", keywrite.Folder, err)
		}
	}
	if madeFolder {
		if err := data.Sync(); err != nil {
			return madeFolder, fmt.Errorf("fsync /data: %v", err)
		}
	}
	if err := hook(StageUnmount, ""); err != nil {
		return madeFolder, err
	}
	if _, _, e := syscall.Syscall(sysSyncfs, root.Fd(), 0, 0); e != 0 {
		return madeFolder, fmt.Errorf("syncfs: %v", e)
	}
	root.Close()
	data.Close()
	tsx.Close()
	mounted = false
	if err := syscall.Unmount(mnt, 0); err != nil {
		mounted = true
		return madeFolder, fmt.Errorf("unmount %s: %v", mnt, err)
	}
	return madeFolder, nil
}

func openDir(dirfd int, name string) (*os.File, error) {
	var fd int
	var err error
	if dirfd < 0 {
		fd, err = syscall.Open(name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	} else {
		fd, err = syscall.Openat(dirfd, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	}
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

// renameNoReplace is renameat2(RENAME_NOREPLACE) in one folder: it never
// replaces a file (key code review NIT).
func renameNoReplace(dirfd int, from, to string) error {
	f, err := syscall.BytePtrFromString(from)
	if err != nil {
		return err
	}
	t, err := syscall.BytePtrFromString(to)
	if err != nil {
		return err
	}
	const renameNoreplace = 1
	if _, _, e := syscall.Syscall6(sysRenameat2, uintptr(dirfd), uintptr(unsafe.Pointer(f)), uintptr(dirfd), uintptr(unsafe.Pointer(t)), renameNoreplace, 0); e != 0 {
		return e
	}
	return nil
}
