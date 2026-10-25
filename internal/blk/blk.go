// Package blk finds the eMMC partitions of the UC-P10 by their GPT names and
// opens them for reading. It has no write path: every open uses O_RDONLY.
//
// The partitions come from the kernel (sysfs uevent PARTNAME), never from raw
// LBAs, as uc-pxx-flash-boot does. Root is "/" on the device. Tests use a
// folder with a fake sys/ and dev/ tree.
package blk

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

// Part is one partition of the eMMC.
type Part struct {
	Name  string // GPT name, for example "boot_a"
	Dev   string // device name, for example "mmcblk0p13"
	Start int64  // first sector (512 bytes)
	Size  int64  // size in sectors
}

// Disk is the eMMC with its partitions.
type Disk struct {
	Root  string // "/" on the device
	Dev   string // "mmcblk0"
	Parts []Part
}

func readTrim(p string) (string, error) {
	b, err := os.ReadFile(p)
	return strings.TrimSpace(string(b)), err
}

func readInt(p string) (int64, error) {
	s, err := readTrim(p)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(s, 10, 64)
}

// Find looks for the eMMC: the mmcblk disk whose partitions include "misc"
// and "boot_a". It refuses when there is no such disk or more than one.
func Find(root string) (*Disk, error) {
	disks, err := filepath.Glob(filepath.Join(root, "sys/class/block/mmcblk*"))
	if err != nil {
		return nil, err
	}
	var found []*Disk
	for _, d := range disks {
		name := filepath.Base(d)
		if strings.Contains(name, "p") || strings.Contains(name, "boot") || strings.Contains(name, "rpmb") {
			continue
		}
		disk := &Disk{Root: root, Dev: name}
		subs, _ := filepath.Glob(filepath.Join(root, "sys/class/block", name+"p*"))
		for _, s := range subs {
			p, err := readPart(s)
			if err != nil || p.Name == "" {
				continue
			}
			disk.Parts = append(disk.Parts, p)
		}
		sort.Slice(disk.Parts, func(i, j int) bool { return disk.Parts[i].Start < disk.Parts[j].Start })
		if disk.has("misc") && disk.has("boot_a") {
			found = append(found, disk)
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("no eMMC with the partitions misc and boot_a")
	case 1:
		return found[0], nil
	}
	return nil, fmt.Errorf("%d disks with the UC-P10 partitions", len(found))
}

func readPart(dir string) (Part, error) {
	var p Part
	f, err := os.Open(filepath.Join(dir, "uevent"))
	if err != nil {
		return p, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		switch k {
		case "PARTNAME":
			p.Name = v
		case "DEVNAME":
			p.Dev = v
		}
	}
	if p.Dev == "" {
		p.Dev = filepath.Base(dir)
	}
	if p.Start, err = readInt(filepath.Join(dir, "start")); err != nil {
		return p, err
	}
	if p.Size, err = readInt(filepath.Join(dir, "size")); err != nil {
		return p, err
	}
	return p, nil
}

// has reports a partition with that name. Part refuses a name that two
// partitions use.
func (d *Disk) has(name string) bool {
	for _, p := range d.Parts {
		if p.Name == name {
			return true
		}
	}
	return false
}

// Part returns the partition with the GPT name. Two partitions with one name
// is an error.
func (d *Disk) Part(name string) (Part, error) {
	var out []Part
	for _, p := range d.Parts {
		if p.Name == name {
			out = append(out, p)
		}
	}
	switch len(out) {
	case 0:
		return Part{}, fmt.Errorf("no partition %s", name)
	case 1:
		return out[0], nil
	}
	return Part{}, fmt.Errorf("%d partitions named %s", len(out), name)
}

// DevPath is the device node of a device name.
func (d *Disk) DevPath(dev string) string {
	return filepath.Join(d.Root, "dev", dev)
}

// OpenPart opens a partition read-only.
func (d *Disk) OpenPart(name string) (*os.File, Part, error) {
	p, err := d.Part(name)
	if err != nil {
		return nil, p, err
	}
	f, err := os.OpenFile(d.DevPath(p.Dev), os.O_RDONLY, 0)
	return f, p, err
}

// OpenDisk opens the whole eMMC read-only.
func (d *Disk) OpenDisk() (*os.File, error) {
	return os.OpenFile(d.DevPath(d.Dev), os.O_RDONLY, 0)
}

// ReadPart reads n bytes at off of a partition. It never reads past the end
// of the partition.
func (d *Disk) ReadPart(name string, off int64, n int) ([]byte, error) {
	f, p, err := d.OpenPart(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if off < 0 || off+int64(n) > p.Size*512 {
		return nil, fmt.Errorf("%s: read %d+%d past the end (%d bytes)", name, off, n, p.Size*512)
	}
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, off); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return buf, nil
}

// ReadSectors reads count sectors of the whole eMMC at lba.
func (d *Disk) ReadSectors(lba int64, count int) ([]byte, error) {
	f, err := d.OpenDisk()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, count*512)
	if _, err := f.ReadAt(buf, lba*512); err != nil {
		return nil, fmt.Errorf("%s sector %d: %w", d.Dev, lba, err)
	}
	return buf, nil
}

// Block device ioctls (linux/fs.h): BLKROSET = _IO(0x12, 93), BLKROGET = _IO(0x12, 94).
const (
	blkROSet = 0x125d
	blkROGet = 0x125e
)

// SetReadOnly sets the kernel read-only flag of the disk and of each of its
// partitions (BLKROSET). After this the kernel refuses every write to the
// eMMC block devices, and every read-write mount. It changes no byte on the
// eMMC. It returns the devices that are read-only afterwards.
func (d *Disk) SetReadOnly() ([]string, error) {
	devs := []string{d.Dev}
	for _, p := range d.Parts {
		devs = append(devs, p.Dev)
	}
	var done []string
	for _, dev := range devs {
		f, err := os.OpenFile(d.DevPath(dev), os.O_RDONLY, 0)
		if err != nil {
			return done, err
		}
		one := int32(1)
		_, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), blkROSet, uintptr(unsafe.Pointer(&one)))
		var ro int32
		if e == 0 {
			_, _, e = syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), blkROGet, uintptr(unsafe.Pointer(&ro)))
		}
		f.Close()
		if e != 0 {
			return done, fmt.Errorf("%s: %v", dev, e)
		}
		if ro != 1 {
			return done, fmt.Errorf("%s: still writable after BLKROSET", dev)
		}
		done = append(done, dev)
	}
	return done, nil
}

// ReadOnly reports the kernel read-only flag of a device from sysfs.
func (d *Disk) ReadOnly(dev string) bool {
	s, _ := readTrim(filepath.Join(d.Root, "sys/class/block", dev, "ro"))
	return s == "1"
}
