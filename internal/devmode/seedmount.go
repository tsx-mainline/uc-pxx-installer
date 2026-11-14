//go:build install

package devmode

// The ssh key seed's read-write mount for the device medium (mainline-rc-8):
// only the RAM installer's device mode links keywrite/mount (check-no-writer:
// the builds of uc-pxx-step2 and of the finish executor must not).

import (
	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/keywrite"
	kwmount "uc-pxx-installer/internal/keywrite/mount"
)

func init() {
	device.SeedMount = func(node, mnt string, files []keywrite.File, folderThere bool) (bool, error) {
		return kwmount.MountWrite(node, mnt, files, folderThere, nil)
	}
}
