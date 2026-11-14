package install

// The ssh key seed in a dry run on an image file (mainline-rc-8): the image
// has no block device, so the seed needs ImageSeeder, which only a devtest
// build sets (seed_image_devtest.go: a loop device over the partition's range
// and the same keywrite.MountWrite as the unit's, as root in a privileged
// test container). A release dry run refuses a plan with the seed.

import (
	"errors"
	"fmt"
	"io"

	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/keywrite"
	"uc-pxx-installer/internal/plan"
)

// ImageSeeder seeds files into the ext4 at [off, off+length) of the image
// file path. stop, when not nil, is the cut test's hook (keywrite.Hook);
// snap is called by the seeder at the hook's stop, while the file system is
// still mounted, so the caller can keep the image's bytes as a power cut
// there would leave them.
var ImageSeeder func(path string, off, length int64, files []keywrite.File, folderThere bool, stop keywrite.Hook) (madeFolder bool, err error)

// CanSeed implements SSHKeyWriter for the dry run.
func (d *dryMedium) CanSeed() error {
	if ImageSeeder == nil {
		return errors.New("a dry run on an image file cannot seed the ssh key (no block device; only a devtest build's loop seeder can)")
	}
	return nil
}

// SeedSSHKey implements SSHKeyWriter for the dry run.
func (d *dryMedium) SeedSSHKey(st *plan.SSHKeyStep, files []keywrite.File, rootfs io.ReaderAt) error {
	im := d.im
	if ImageSeeder == nil {
		return errors.New("a dry run on an image file cannot seed the ssh key (no block device; only a devtest build's loop seeder can)")
	}
	e, ok := im.t.Find(st.Partition)
	if !ok || e.Start != st.StartLBA || st.Partition != "system_"+im.target || st.Length <= 0 || st.Length > e.Bytes() {
		return fmt.Errorf("the seed: no partition %s at LBA %d of the target", st.Partition, st.StartLBA)
	}
	off := e.Start * gpt.Sector
	part := func() io.ReaderAt { return io.NewSectionReader(im.f, off, st.Length) }
	folder, err := keywrite.Before(part(), st.Length, files)
	if err != nil {
		return fmt.Errorf("before the seed: %v", err)
	}
	if folder == st.FolderMade {
		return fmt.Errorf("before the seed: /data/tsx is there %v, the plan says the seed makes it %v", folder, st.FolderMade)
	}
	c := im.cutter()
	var stop keywrite.Hook
	if h := im.hooks; h != nil && h.CutAfter >= 0 && c.N == h.CutAfter {
		// the cut is in the seed: a torn cut stops it after the first file
		// is written (before its rename), with the image as the disk is at
		// that moment; a clean cut stops it before the mount
		if !h.Torn {
			return ErrPowerCut
		}
		var snap []byte
		stop = func(s keywrite.Stage, _ string) error {
			if s != keywrite.StageRename {
				return nil
			}
			snap = make([]byte, st.Length)
			if _, err := im.f.ReadAt(snap, off); err != nil {
				return err
			}
			return ErrPowerCut
		}
		_, err := ImageSeeder(im.path, off, st.Length, files, folder, stop)
		if snap != nil {
			if _, werr := im.f.WriteAt(snap, off); werr != nil {
				return werr
			}
		}
		if errors.Is(err, ErrPowerCut) || snap != nil {
			return ErrPowerCut
		}
		return fmt.Errorf("the torn seed did not reach its cut: %v", err)
	}
	made, err := ImageSeeder(im.path, off, st.Length, files, folder, nil)
	if err != nil {
		return fmt.Errorf("the seed (mount, write, unmount): %v", err)
	}
	if err := keywrite.Check(part(), st.Length, files, made); err != nil {
		return fmt.Errorf("after the seed: %v", err)
	}
	if err := keywrite.SameTree(part(), st.Length, rootfs, st.Length, files, made); err != nil {
		return fmt.Errorf("after the seed: %v", err)
	}
	im.writes++
	return c.After(Event{Op: "ssh-key", Partition: st.Partition, Off: off, Len: st.Length})
}
