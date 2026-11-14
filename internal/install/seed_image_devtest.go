//go:build install && devtest

package install

// The devtest seeder of a dry run (seed_image.go): a loop device over the
// partition's range of the image file (the loop ioctls, as root: the privileged test
// container of ci/dryrun-real.py --seed-root) and the unit's own
// kwmount.MountWrite. Never in a release build.

import (
	"fmt"
	"os"
	"path/filepath"

	"uc-pxx-installer/internal/keywrite"
	"uc-pxx-installer/internal/keywrite/looptest"
	kwmount "uc-pxx-installer/internal/keywrite/mount"
)

func init() { ImageSeeder = loopSeed }

func loopSeed(path string, off, length int64, files []keywrite.File, folderThere bool, stop kwmount.Hook) (bool, error) {
	if os.Geteuid() != 0 {
		return false, fmt.Errorf("the devtest seeder needs root (a loop device): run the dry run in the privileged test container")
	}
	dev, detach, err := looptest.Attach(path, off, length)
	if err != nil {
		return false, fmt.Errorf("loop: %v", err)
	}
	defer detach()
	tmp, err := os.MkdirTemp("", "seed-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(tmp)
	return kwmount.MountWrite(dev, filepath.Join(tmp, "mnt"), files, folderThere, stop)
}
