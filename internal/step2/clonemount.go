//go:build install && step2

package step2

// The one eMMC mount of a clone run besides the running root
// (DESIGN-R23 §4.3 item 6): system_T, read-write, nosuid, nodev, noexec,
// noatime, at clone.MountPoint, for the difference only. check-no-writer
// allows an ext4 mount here and in the seed's file only.

import "syscall"

// cloneMountOptions are the data options of the mount.
const cloneMountOptions = "errors=remount-ro"

func cloneMount(node, dir string) error {
	return syscall.Mount(node, dir, "ext4", syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC|syscall.MS_NOATIME, cloneMountOptions)
}
