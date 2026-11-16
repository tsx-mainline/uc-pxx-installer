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

// roGet and roSet are BLKROGET and BLKROSET of f: the only ioctls of this
// package, each with its constant number (ci/goallow pins them).
func roGet(f *os.File, v *int32) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), blkROGet, uintptr(unsafe.Pointer(v))); e != 0 {
		return e
	}
	return nil
}

func roSet(f *os.File, v *int32) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), blkROSet, uintptr(unsafe.Pointer(v))); e != 0 {
		return e
	}
	return nil
}

// ROGuard is what GuardReadOnly set: Restore clears the flag again on
// exactly the nodes whose flag it set (a node that was read-only before
// stays so).
type ROGuard struct {
	d   *Disk
	set []string
}

// Devices are the nodes whose flag the guard set.
func (g *ROGuard) Devices() []string { return append([]string(nil), g.set...) }

// GuardReadOnly sets the kernel read-only flag (BLKROSET 1, read back by
// BLKROGET) of the disk and of each of its partitions except the partitions
// named in except (GPT names; each must be exactly one partition). It opens
// every node read-only and changes no byte on the eMMC. The disk comes
// first: on 6.x a partition's BLKROGET then reads 1 (bdev_read_only includes
// the disk's flag), so in effect only the disk's flag is set, and it covers
// every partition, the excepted ones too, for new userspace write opens; a
// file system that is mounted already keeps its writes (block/blk-core.c
// bio_check_ro only warns, once per node). It is a net against the accidents
// of package scripts, not against a root program: one can clear the flag
// again, and it does not cover an MMC_IOC_CMD on a read-only fd, a mapping
// made before, or the boot0/boot1 hardware partitions (other disks, read
// only by the mmc driver's default). A node whose flag is set already is left
// as it is. On an error the flags
// it set are cleared again and the error is returned.
func (d *Disk) GuardReadOnly(except ...string) (*ROGuard, error) {
	skip := map[string]bool{}
	for _, n := range except {
		p, err := d.Part(n)
		if err != nil {
			return nil, err
		}
		skip[p.Dev] = true
	}
	devs := []string{d.Dev}
	for _, p := range d.Parts {
		if !skip[p.Dev] {
			devs = append(devs, p.Dev)
		}
	}
	g := &ROGuard{d: d}
	for _, dev := range devs {
		changed, err := d.setRO(dev, 1, true)
		if changed {
			g.set = append(g.set, dev)
		}
		if err != nil {
			if rerr := g.Restore(); rerr != nil {
				return nil, fmt.Errorf("%v (and the restore: %v)", err, rerr)
			}
			return nil, err
		}
	}
	return g, nil
}

// Restore clears the read-only flag of the nodes the guard set (BLKROSET 0,
// read back), each once. It goes on after an error and returns the first.
func (g *ROGuard) Restore() error {
	var first error
	left := g.set[:0:0]
	for _, dev := range g.set {
		if _, err := g.d.setRO(dev, 0, false); err != nil {
			left = append(left, dev)
			if first == nil {
				first = err
			}
		}
	}
	g.set = left
	return first
}

// setRO sets the flag of dev to v (0 or 1) and reads it back. With keepSet
// (the guard's set) a node whose flag is v already is not changed; changed
// reports a BLKROSET that was done.
func (d *Disk) setRO(dev string, v int32, keepSet bool) (changed bool, err error) {
	f, err := os.OpenFile(d.DevPath(dev), os.O_RDONLY, 0)
	if err != nil {
		return false, err
	}
	defer f.Close()
	if keepSet {
		var cur int32
		if err := roGet(f, &cur); err != nil {
			return false, fmt.Errorf("%s: BLKROGET: %v", dev, err)
		}
		if cur == v {
			return false, nil
		}
	}
	x := v
	if err := roSet(f, &x); err != nil {
		return false, fmt.Errorf("%s: BLKROSET %d: %v", dev, v, err)
	}
	var got int32
	if err := roGet(f, &got); err != nil {
		return true, fmt.Errorf("%s: BLKROGET after BLKROSET %d: %v", dev, v, err)
	}
	if got != v {
		return true, fmt.Errorf("%s: the read-only flag is %d after BLKROSET %d", dev, got, v)
	}
	return true, nil
}

// ReadPartDirect reads n bytes at off of a partition with O_DIRECT (past the
// page cache: what the eMMC holds now, as another writer left it). off and n
// must be multiples of 512 (the eMMC's logical block). It never reads past
// the end of the partition.
func (d *Disk) ReadPartDirect(name string, off int64, n int) ([]byte, error) {
	p, err := d.Part(name)
	if err != nil {
		return nil, err
	}
	if off < 0 || n <= 0 || off%512 != 0 || n%512 != 0 || off+int64(n) > p.Size*512 {
		return nil, fmt.Errorf("%s: a direct read of %d+%d (512-byte blocks within %d bytes)", name, off, n, p.Size*512)
	}
	f, err := os.OpenFile(d.DevPath(p.Dev), os.O_RDONLY|syscall.O_DIRECT, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// a buffer aligned to 4096 bytes (O_DIRECT needs the logical block size)
	raw := make([]byte, n+4096)
	a := int(uintptr(unsafe.Pointer(&raw[0])) & 4095)
	if a != 0 {
		a = 4096 - a
	}
	buf := raw[a : a+n]
	if _, err := f.ReadAt(buf, off); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return append([]byte(nil), buf...), nil
}
