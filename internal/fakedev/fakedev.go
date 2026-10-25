// Package fakedev builds a fake device tree for tests: sys/class/block with
// the uevent, start and size files of each partition, and dev/ with one
// regular file per device. The whole disk file and the partition files are
// separate files, so a test writes the data it needs where the code reads it.
package fakedev

import (
	"fmt"
	"os"
	"path/filepath"
)

// Part is one fake partition.
type Part struct {
	Name  string
	Start int64 // sectors
	Size  int64 // sectors
	Data  []byte
}

// Layout is the UC-P10 partition table (the part that the tests use).
var Layout = []Part{
	{Name: "uboot_a", Start: 16384, Size: 8192},
	{Name: "uboot_b", Start: 24576, Size: 8192},
	{Name: "misc", Start: 49152, Size: 8192},
	{Name: "boot_a", Start: 184320, Size: 131072},
	{Name: "boot_b", Start: 315392, Size: 131072},
	{Name: "cfg", Start: 3666944, Size: 4096},
	{Name: "system_a", Start: 3703808, Size: 3670016},
	{Name: "system_b", Start: 7373824, Size: 3670016},
}

// Dev is a fake device.
type Dev struct {
	Root string
	Disk string // path of the whole-disk file
}

// New makes the tree. The files are sparse.
func New(root string, parts []Part) (*Dev, error) {
	d := &Dev{Root: root, Disk: filepath.Join(root, "dev/mmcblk0")}
	for _, p := range []string{"sys/class/block/mmcblk0", "dev"} {
		if err := os.MkdirAll(filepath.Join(root, p), 0o755); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(filepath.Join(root, "sys/class/block/mmcblk0/ro"), []byte("0\n"), 0o644); err != nil {
		return nil, err
	}
	disk, err := os.Create(d.Disk)
	if err != nil {
		return nil, err
	}
	disk.Truncate(16 << 30)
	disk.Close()
	for i, p := range parts {
		dev := fmt.Sprintf("mmcblk0p%d", i+1)
		sd := filepath.Join(root, "sys/class/block", dev)
		if err := os.MkdirAll(sd, 0o755); err != nil {
			return nil, err
		}
		files := map[string]string{
			"uevent": fmt.Sprintf("MAJOR=179\nMINOR=%d\nDEVNAME=%s\nDEVTYPE=partition\nPARTN=%d\nPARTNAME=%s\n", i+1, dev, i+1, p.Name),
			"start":  fmt.Sprintf("%d\n", p.Start),
			"size":   fmt.Sprintf("%d\n", p.Size),
			"ro":     "0\n",
		}
		for k, v := range files {
			if err := os.WriteFile(filepath.Join(sd, k), []byte(v), 0o644); err != nil {
				return nil, err
			}
		}
		f, err := os.Create(filepath.Join(root, "dev", dev))
		if err != nil {
			return nil, err
		}
		if len(p.Data) > 0 {
			f.Write(p.Data)
		}
		f.Truncate(p.Size * 512)
		f.Close()
	}
	return d, nil
}

// WriteDisk writes data into the whole-disk file at sector lba.
func (d *Dev) WriteDisk(lba int64, data []byte) error {
	f, err := os.OpenFile(d.Disk, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteAt(data, lba*512)
	return err
}

// WritePart writes data into partition n (1-based) at byte off.
func (d *Dev) WritePart(n int, off int64, data []byte) error {
	f, err := os.OpenFile(filepath.Join(d.Root, "dev", fmt.Sprintf("mmcblk0p%d", n)), os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteAt(data, off)
	return err
}

// PartNum returns the 1-based number of a partition name in parts.
func PartNum(parts []Part, name string) int {
	for i, p := range parts {
		if p.Name == name {
			return i + 1
		}
	}
	return 0
}
