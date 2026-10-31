//go:build install

package device

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/plan"
)

// Part is one partition of the eMMC, as sysfs and the GPT both describe it.
type Part struct {
	Index        int    // N of mmcblk0pN, the GPT index
	Name         string // GPT name
	Dev          string // "mmcblk0pN"
	Start, Size  int64  // sectors
	Major, Minor uint32
}

// Bytes is the size in bytes.
func (p Part) Bytes() int64 { return p.Size * gpt.Sector }

// Disk is the eMMC after OpenEMMC checked it. Every method opens the device
// nodes again for each use, and checks again that a node is the block device
// that sysfs names.
type Disk struct {
	e            *Env
	Sectors      int64
	Major, Minor uint32
	Table        *gpt.Table
	parts        []Part // sorted by start
	byName       map[string]Part
	ID           plan.DeviceID
	lock         *os.File
	roWasOff     []string // devices whose read-only flag was off when OpenEMMC found them
}

// ROWasOff lists the devices whose kernel read-only flag was off when
// OpenEMMC found them (for example after a run that was killed inside a
// write). OpenEMMC set them again.
func (d *Disk) ROWasOff() []string { return d.roWasOff }

// Block device ioctls (linux/fs.h).
const (
	blkROSet     = 0x125d     // _IO(0x12, 93)
	blkROGet     = 0x125e     // _IO(0x12, 94)
	blkFlsBuf    = 0x1261     // _IO(0x12, 97)
	blkSSZGet    = 0x1268     // _IO(0x12, 104)
	blkGetSize64 = 0x80081272 // _IOR(0x12, 114, size_t), the same on arm64 and amd64
	align        = 4096       // buffer alignment for O_DIRECT (the eMMC needs 512)
	ioChunk      = 1 << 20
)

func ioctlInt(f *os.File, req uintptr, v *int32) error {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), req, uintptr(unsafe.Pointer(v)))
	if e != 0 {
		return e
	}
	return nil
}

// alignedBuf returns n bytes whose address is a multiple of align.
func alignedBuf(n int) []byte {
	b := make([]byte, n+align)
	off := int(uintptr(unsafe.Pointer(&b[0])) & (align - 1))
	if off != 0 {
		off = align - off
	}
	return b[off : off+n]
}

func readTrim(p string) (string, error) {
	b, err := os.ReadFile(p)
	return strings.TrimSpace(strings.TrimRight(string(b), "\x00")), err
}

func parseDevNum(s string) (uint32, uint32, error) {
	a, b, ok := strings.Cut(strings.TrimSpace(s), ":")
	ma, e1 := strconv.ParseUint(a, 10, 32)
	mi, e2 := strconv.ParseUint(b, 10, 32)
	if !ok || e1 != nil || e2 != nil {
		return 0, 0, fmt.Errorf("bad device number %q", s)
	}
	return uint32(ma), uint32(mi), nil
}

// checkNode checks that a device node is the block device major:minor. In a
// test environment with regular files, it must be a regular file of size
// bytes instead.
func (e *Env) checkNode(path string, major, minor uint32, size int64) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if e.regular {
		if !st.Mode().IsRegular() || (size > 0 && st.Size() != size) {
			return fmt.Errorf("%s: not a test file of %d bytes", path, size)
		}
		return nil
	}
	if st.Mode()&os.ModeDevice == 0 || st.Mode()&os.ModeCharDevice != 0 {
		return fmt.Errorf("%s is not a block device (mode %v)", path, st.Mode())
	}
	sys := st.Sys().(*syscall.Stat_t)
	if major64(sys.Rdev) != major || minor64(sys.Rdev) != minor {
		return fmt.Errorf("%s is %d:%d, sysfs says %d:%d", path, major64(sys.Rdev), minor64(sys.Rdev), major, minor)
	}
	return nil
}

func major64(d uint64) uint32 { return uint32((d>>32)&0xfffff000 | (d>>8)&0xfff) }
func minor64(d uint64) uint32 { return uint32((d>>12)&0xffffff00 | d&0xff) }

// sameOpen checks that an open file is still the node it was opened as.
func (e *Env) sameOpen(f *os.File, major, minor uint32) error {
	if e.regular {
		return nil
	}
	st, err := f.Stat()
	if err != nil {
		return err
	}
	sys := st.Sys().(*syscall.Stat_t)
	if st.Mode()&os.ModeDevice == 0 || major64(sys.Rdev) != major || minor64(sys.Rdev) != minor {
		return fmt.Errorf("%s changed while it was opened", f.Name())
	}
	return nil
}

// openRead opens a node for reading, O_DIRECT on a device.
func (e *Env) openRead(path string, major, minor uint32) (*os.File, error) {
	flags := os.O_RDONLY | syscall.O_CLOEXEC | syscall.O_NOFOLLOW
	if !e.regular {
		flags |= syscall.O_DIRECT
	}
	f, err := os.OpenFile(path, flags, 0)
	if err != nil {
		return nil, err
	}
	if err := e.sameOpen(f, major, minor); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// readDirect reads len(p) bytes at off with whole, aligned sectors through
// an aligned buffer (O_DIRECT needs both). It never reads past limit.
func readDirect(f *os.File, p []byte, off, limit int64) error {
	if off < 0 || off+int64(len(p)) > limit {
		return fmt.Errorf("%s: read %d+%d outside 0..%d", f.Name(), off, len(p), limit)
	}
	for len(p) > 0 {
		a := off &^ (gpt.Sector - 1)
		end := off + int64(len(p))
		if end-a > ioChunk {
			end = a + ioChunk
		}
		ae := (end + gpt.Sector - 1) &^ (gpt.Sector - 1)
		if ae > limit {
			return fmt.Errorf("%s: read past the end", f.Name())
		}
		buf := alignedBuf(int(ae - a))
		n, err := f.ReadAt(buf, a)
		if n != len(buf) {
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			return fmt.Errorf("%s at %d: %w", f.Name(), a, err)
		}
		k := copy(p, buf[off-a:end-a])
		p = p[k:]
		off += int64(k)
	}
	return nil
}

// Part returns a partition by name.
func (d *Disk) Part(name string) (Part, bool) {
	p, ok := d.byName[name]
	return p, ok
}

// Bytes is the disk size in bytes.
func (d *Disk) Bytes() int64 { return d.Sectors * gpt.Sector }

func (d *Disk) diskNode() string { return d.e.path("/dev/" + emmcDev) }

func (d *Disk) partNode(p Part) string { return d.e.path("/dev/" + p.Dev) }

// openPartRead opens a partition node read-only (O_DIRECT).
func (d *Disk) openPartRead(p Part) (*os.File, error) {
	if err := d.e.checkNode(d.partNode(p), p.Major, p.Minor, p.Bytes()); err != nil {
		return nil, err
	}
	return d.e.openRead(d.partNode(p), p.Major, p.Minor)
}

// ReadPart reads len(b) bytes at off of a partition, through its own node.
func (d *Disk) ReadPart(p Part, b []byte, off int64) error {
	f, err := d.openPartRead(p)
	if err != nil {
		return err
	}
	defer f.Close()
	return readDirect(f, b, off, p.Bytes())
}

// ReadAB reads the A/B sector (misc + 4) fresh from the device, through the
// misc partition node. bootctl writes it through the whole-disk node, so a
// buffered read could see a stale page; O_DIRECT cannot.
func (d *Disk) ReadAB() ([]byte, error) {
	misc, ok := d.byName["misc"]
	if !ok || misc.Start != plan.MiscStart {
		return nil, errors.New("no misc partition at LBA 49152")
	}
	if d.e.abReadErr != nil {
		if err := d.e.abReadErr(); err != nil {
			return nil, fmt.Errorf("read the A/B sector: %w", err)
		}
	}
	b := make([]byte, gpt.Sector)
	if err := d.ReadPart(misc, b, (plan.ABLBA-plan.MiscStart)*gpt.Sector); err != nil {
		return nil, fmt.Errorf("read the A/B sector: %w", err)
	}
	return b, nil
}

// View is a read-only view of the whole disk by absolute offset. A range
// inside a partition is read through that partition's node, a range outside
// every partition (the GPT, the gaps) through the whole-disk node. All reads
// are O_DIRECT on a device.
type View struct {
	d     *Disk
	files map[string]*os.File
}

// View opens a view. Close it after use.
func (d *Disk) View() *View { return &View{d: d, files: map[string]*os.File{}} }

// Close closes the nodes of the view.
func (v *View) Close() error {
	for _, f := range v.files {
		f.Close()
	}
	v.files = map[string]*os.File{}
	return nil
}

func (v *View) file(key string, open func() (*os.File, error)) (*os.File, error) {
	if f := v.files[key]; f != nil {
		return f, nil
	}
	f, err := open()
	if err != nil {
		return nil, err
	}
	v.files[key] = f
	return f, nil
}

// ReadAt implements io.ReaderAt.
func (v *View) ReadAt(p []byte, off int64) (int, error) {
	d := v.d
	total := len(p)
	if off < 0 || off >= d.Bytes() {
		return 0, io.EOF
	}
	short := false
	if off+int64(len(p)) > d.Bytes() {
		p = p[:d.Bytes()-off]
		short = true
	}
	done := 0
	for done < len(p) {
		lba := (off + int64(done)) / gpt.Sector
		var part *Part
		next := d.Sectors // the first partition start after lba, or the end
		for i := range d.parts {
			q := &d.parts[i]
			if lba >= q.Start && lba < q.Start+q.Size {
				part = q
				break
			}
			if q.Start > lba && q.Start < next {
				next = q.Start
			}
		}
		pos := off + int64(done)
		var end int64
		var err error
		if part != nil {
			end = (part.Start + part.Size) * gpt.Sector
			if end > off+int64(len(p)) {
				end = off + int64(len(p))
			}
			var f *os.File
			pp := *part
			if f, err = v.file(pp.Dev, func() (*os.File, error) { return d.openPartRead(pp) }); err == nil {
				err = readDirect(f, p[done:done+int(end-pos)], pos-pp.Start*gpt.Sector, pp.Bytes())
			}
		} else {
			end = next * gpt.Sector
			if end > off+int64(len(p)) {
				end = off + int64(len(p))
			}
			var f *os.File
			if f, err = v.file(emmcDev, func() (*os.File, error) { return d.e.openRead(d.diskNode(), d.Major, d.Minor) }); err == nil {
				err = readDirect(f, p[done:done+int(end-pos)], pos, d.Bytes())
			}
		}
		if err != nil {
			return done, err
		}
		done += int(end - pos)
	}
	if short || done < total {
		return done, io.EOF
	}
	return done, nil
}

// HashRange hashes n bytes at the absolute offset off (a fresh read).
func (d *Disk) HashRange(off, n int64) (string, error) {
	v := d.View()
	defer v.Close()
	h := sha256.New()
	buf := make([]byte, ioChunk)
	for n > 0 {
		k := int64(len(buf))
		if k > n {
			k = n
		}
		if _, err := v.ReadAt(buf[:k], off); err != nil {
			return "", err
		}
		h.Write(buf[:k])
		off += k
		n -= k
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// setRO sets or clears the kernel read-only flag of one node (BLKROSET) and
// reads it back (BLKROGET). It changes no byte on the eMMC.
func (e *Env) setRO(path string, major, minor uint32, ro bool) error {
	if e.regular {
		return nil
	}
	f, err := e.openRead(path, major, minor)
	if err != nil {
		return err
	}
	defer f.Close()
	v := int32(0)
	if ro {
		v = 1
	}
	if err := ioctlInt(f, blkROSet, &v); err != nil {
		return fmt.Errorf("%s: BLKROSET %d: %w", path, v, err)
	}
	var got int32
	if err := ioctlInt(f, blkROGet, &got); err != nil {
		return fmt.Errorf("%s: BLKROGET: %w", path, err)
	}
	if (got != 0) != ro {
		return fmt.Errorf("%s: the read-only flag is %d after BLKROSET %d", path, got, v)
	}
	return nil
}

// flushBuf drops the page cache of a node (BLKFLSBUF).
func (e *Env) flushBuf(path string, major, minor uint32) error {
	if e.regular {
		return nil
	}
	f, err := e.openRead(path, major, minor)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, _, en := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), blkFlsBuf, 0); en != 0 {
		return fmt.Errorf("%s: BLKFLSBUF: %w", path, en)
	}
	return nil
}

// fsyncNode opens a node read-only and fsyncs it. On a block device this
// writes back its page cache and sends a cache flush to the eMMC
// (blkdev_fsync -> blkdev_issue_flush -> REQ_PREFLUSH; the mmc driver
// flushes the eMMC cache for it).
func (e *Env) fsyncNode(path string, major, minor uint32) error {
	f, err := e.openRead(path, major, minor)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return fmt.Errorf("%s: fsync: %w", path, err)
	}
	return nil
}

// getRO reads the kernel read-only flag of a node (BLKROGET). A test
// environment with regular files reports every flag as set.
func (e *Env) getRO(path string, major, minor uint32) (bool, error) {
	if e.regular {
		return true, nil
	}
	f, err := e.openRead(path, major, minor)
	if err != nil {
		return false, err
	}
	defer f.Close()
	var got int32
	if err := ioctlInt(f, blkROGet, &got); err != nil {
		return false, fmt.Errorf("%s: BLKROGET: %w", path, err)
	}
	return got != 0, nil
}

// roOff lists the nodes whose read-only flag is off now.
func (d *Disk) roOff() []string {
	var off []string
	if ro, err := d.e.getRO(d.diskNode(), d.Major, d.Minor); err != nil || !ro {
		off = append(off, emmcDev)
	}
	for _, p := range d.parts {
		if ro, err := d.e.getRO(d.partNode(p), p.Major, p.Minor); err != nil || !ro {
			off = append(off, p.Dev)
		}
	}
	return off
}

// ReadOnlyAll sets the kernel read-only flag on the disk and on every
// partition, and checks it, as the RAM image does at its start.
func (d *Disk) ReadOnlyAll() error {
	var errs []string
	if err := d.e.setRO(d.diskNode(), d.Major, d.Minor, true); err != nil {
		errs = append(errs, err.Error())
	}
	for _, p := range d.parts {
		if err := d.e.setRO(d.partNode(p), p.Major, p.Minor, true); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return errors.New("read-only flag: " + strings.Join(errs, "; "))
	}
	return nil
}

// Lock takes the A/B lock (/run/uc-pxx-abdata.lock) for the whole run. It
// fails when another A/B writer holds it.
func (d *Disk) Lock() error {
	if d.lock != nil {
		return nil
	}
	p := d.e.path(ABLockPath)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return fmt.Errorf("%s is held by another A/B writer: %w", ABLockPath, err)
	}
	d.lock = f
	return nil
}

// Unlock releases the A/B lock.
func (d *Disk) Unlock() {
	if d.lock != nil {
		d.lock.Close()
		d.lock = nil
	}
}

// Locked reports that this process holds the A/B lock.
func (d *Disk) Locked() bool { return d.lock != nil }

func sortParts(p []Part) {
	sort.Slice(p, func(i, j int) bool { return p[i].Start < p[j].Start })
}

func ioctlU64(f *os.File, req uintptr, v *uint64) error {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), req, uintptr(unsafe.Pointer(v)))
	if e != 0 {
		return e
	}
	return nil
}
