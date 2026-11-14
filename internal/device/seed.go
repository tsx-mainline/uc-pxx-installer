//go:build install

package device

// The ssh key seed of a direct install (mainline-rc-8; package keywrite):
// install.SSHKeyWriter of the eMMC medium.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/keywrite"
	"uc-pxx-installer/internal/plan"
)

// SeedMountDir is the empty folder of the seed's read-write mount (the RAM
// image's /run; made and removed by the seed).
const SeedMountDir = "/run/uc-pxx-inst/keywrite-mnt"

// CanSeed implements install.SSHKeyWriter.
func (m *Medium) CanSeed() error {
	if m.d.e.regular {
		if m.d.e.seedRegular == nil {
			return errors.New("the seed needs a block device (a test of regular files has no seed)")
		}
		return nil
	}
	// before any write (key code review MINOR-5): the kernel has ext4, and the
	// mount folder is not there (a stale one: a seed of this boot crashed)
	fsb, err := os.ReadFile("/proc/filesystems")
	if err != nil || !strings.Contains(string(fsb), "\text4\n") {
		return fmt.Errorf("the kernel has no ext4 (/proc/filesystems: %v)", err)
	}
	if _, err := os.Lstat(m.d.e.path(SeedMountDir)); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s is there already (%v): an earlier seed of this boot did not end", SeedMountDir, err)
	}
	return nil
}

// SeedSSHKey implements install.SSHKeyWriter: the read-write mount of the
// system partition of the plan's step (kwmount.MountWrite), then the
// read-back through the O_DIRECT view (no mount). Before the mount the
// partition's file system must be what the seed needs (keywrite.Before).
func (m *Medium) SeedSSHKey(st *plan.SSHKeyStep, files []keywrite.File, rootfs io.ReaderAt) error {
	if m.p == nil {
		return errors.New("internal error: the medium is not bound to a plan")
	}
	p, ok := m.d.byName[st.Partition]
	if !ok || p.Start != st.StartLBA || st.Partition != "system_"+m.target || st.Length <= 0 || st.Length > p.Bytes() {
		return fmt.Errorf("the seed: no partition %s at LBA %d of the target", st.Partition, st.StartLBA)
	}
	part := func() io.ReaderAt { return io.NewSectionReader(m.Reader(), p.Start*gpt.Sector, st.Length) }
	folder, err := keywrite.Before(part(), st.Length, files)
	if err != nil {
		return fmt.Errorf("before the seed: %v", err)
	}
	if folder == st.FolderMade {
		return fmt.Errorf("before the seed: /data/tsx is there %v, the plan says the seed makes it %v", folder, st.FolderMade)
	}
	node := m.d.partNode(p)
	if err := m.d.e.checkNode(node, p.Major, p.Minor, p.Bytes()); err != nil {
		return err
	}
	if err := m.cut.Before(nil); err != nil {
		return err // a test's power cut before the mount
	}
	var made bool
	switch {
	case m.d.e.regular && m.d.e.seedRegular != nil:
		made, err = m.d.e.seedRegular(node, files, folder)
	case m.d.e.regular:
		return errors.New("the seed needs a block device (a test of regular files has no seed)")
	default:
		made, err = m.d.e.seedNode(seedNodes{disk: m.d.diskNode(), dmaj: m.d.Major, dmin: m.d.Minor, part: node, pmaj: p.Major, pmin: p.Minor},
			m.d.e.path(SeedMountDir), files, folder)
	}
	if err != nil {
		return fmt.Errorf("the seed (mount, write, unmount): %v", err)
	}
	if err := m.d.e.flushBuf(node, p.Major, p.Minor); err != nil {
		return err
	}
	if m.view != nil {
		m.view.Close()
	}
	if err := keywrite.Check(part(), st.Length, files, made); err != nil {
		return fmt.Errorf("after the seed: %v", err)
	}
	// every other entry of system_b equals the payload's root file system
	// (key code review MINOR-4)
	if err := keywrite.SameTree(part(), st.Length, rootfs, st.Length, files, made); err != nil {
		return fmt.Errorf("after the seed: %v", err)
	}
	return m.cut.After(install.Event{Op: "ssh-key", Partition: st.Partition, Off: p.Start * gpt.Sector, Len: st.Length})
}

// seedNodes are the disk and the partition node of the seed.
type seedNodes struct {
	disk, part             string
	dmaj, dmin, pmaj, pmin uint32
}

// seedNode is the read-write mount of the seed on the partition node: the
// kernel read-only flags of the disk and the partition (set by the RAM image
// at its start and by the writer after each write) are cleared just before
// the mount and set again after it on every path (an ext4 rw mount of a
// read-only block device fails with EACCES: RC-8 key code review MAJOR-1).
func (e *Env) seedNode(n seedNodes, mnt string, files []keywrite.File, folder bool) (made bool, err error) {
	if err := e.checkNode(n.part, n.pmaj, n.pmin, 0); err != nil {
		return false, err
	}
	if err := e.setRO(n.disk, n.dmaj, n.dmin, false); err != nil {
		return false, err
	}
	defer func() {
		if rerr := e.setRO(n.disk, n.dmaj, n.dmin, true); rerr != nil && err == nil {
			err = rerr
		}
	}()
	if err := e.setRO(n.part, n.pmaj, n.pmin, false); err != nil {
		return false, err
	}
	defer func() {
		if rerr := e.setRO(n.part, n.pmaj, n.pmin, true); rerr != nil && err == nil {
			err = rerr
		}
	}()
	if SeedMount == nil {
		return false, errors.New("this build has no seed mount (only the RAM installer's device mode has it)")
	}
	return SeedMount(n.part, mnt, files, folder)
}

// SeedMount is the read-write mount of the seed (keywrite/mount.MountWrite).
// The device mode sets it (devmode/seedmount.go), so the builds of uc-pxx-step2
// and of the finish executor, which link this package, never hold the eMMC
// mount (check-no-writer).
var SeedMount func(node, mnt string, files []keywrite.File, folderThere bool) (bool, error)
