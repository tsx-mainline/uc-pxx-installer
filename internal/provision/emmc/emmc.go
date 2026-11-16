// Package emmc is uc-pxx-provision's only access to the eMMC (mainline-rc-10
// R23 review MAJOR-1): two functions on top of the read-only internal/blk,
// and nothing else of it. internal/provision itself may not import
// internal/blk (ci/check-no-writer.py), so a write through a blk node is out
// of its reach; this package is a goallow reader (no write open, no write
// call) and blk's ioctls are pinned to BLKROSET and BLKROGET.
//
//   - ABSector: the 512 bytes of the A/B sector (misc, 4 sectors in), read
//     with O_DIRECT (the mark check);
//   - Guard: the kernel read-only flag of the eMMC and of its partitions but
//     the named ones, and its restore.
package emmc

import (
	"fmt"

	"uc-pxx-installer/internal/blk"
)

// MiscStart is the first sector of misc; the A/B sector is 4 sectors in (LBA
// 49156).
const MiscStart = 49152

// ABSector reads the A/B sector of the eMMC under root ("/" on a unit).
func ABSector(root string) ([]byte, error) {
	d, err := blk.Find(root)
	if err != nil {
		return nil, err
	}
	m, err := d.Part("misc")
	if err != nil {
		return nil, err
	}
	if m.Start != MiscStart {
		return nil, fmt.Errorf("misc starts at %d, not %d", m.Start, MiscStart)
	}
	return d.ReadPartDirect("misc", 4*512, 512)
}

// Guard sets the kernel read-only flag of the eMMC under root and of every
// partition but the named ones (blk GuardReadOnly) and returns the restore.
func Guard(root string, except ...string) (func() error, error) {
	d, err := blk.Find(root)
	if err != nil {
		return nil, err
	}
	g, err := d.GuardReadOnly(except...)
	if err != nil {
		return nil, err
	}
	return g.Restore, nil
}
