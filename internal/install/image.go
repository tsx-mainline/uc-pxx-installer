// Package install executes an install plan (Run). It has one executor and
// two media: an image file that stands for the eMMC (the dry run, this
// file), and the eMMC itself (package device, the device mode). This package
// has no code path to a block device: the dry-run medium refuses device nodes.
// The status-only RAM image does not contain this package
// (ci/check-no-writer.py checks the import graph and the built binary).
package install

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/plan"
)

// ErrPowerCut is returned when the test hook cuts the run.
var ErrPowerCut = errors.New("simulated power cut")

// Event is reported after every primitive write.
type Event struct {
	Write     int    // primitive writes done so far, this one included
	Step      int    // plan step number
	Op        string // "data" (partition bytes) or "ab" (the A/B sector)
	Partition string
	Off, Len  int64 // byte range in the image
}

// Image is the image file that stands for the eMMC. Every write goes
// through two methods: writeData for the target's partitions, and writeAB for
// the A/B sector that uc-pxx-bootctl computed. Both check their range again.
type Image struct {
	f      *os.File
	path   string
	size   int64
	t      *gpt.Table
	target string
	writes int
	step   int
	hooks  *Hooks
	cut    Cutter
}

// Hooks are test hooks.
type Hooks struct {
	// CutAfter stops the run before primitive write number CutAfter+1 and
	// returns ErrPowerCut, with no clean-up: what a power cut leaves. A
	// negative value never cuts.
	CutAfter int
	// Torn writes the first half of the cut write first (partition data
	// only; the A/B sector is one 512-byte write, atomic on the eMMC).
	Torn bool
	// Observe runs after every primitive write.
	Observe func(Event) error
}

func refusePath(p string) error {
	abs, err := filepath.Abs(p)
	if err != nil {
		return err
	}
	for _, bad := range []string{"/dev/", "/proc/", "/sys/"} {
		if strings.HasPrefix(abs+"/", bad) || strings.HasPrefix(abs, bad) {
			return fmt.Errorf("%s: the dry run writes only to an image file, never under %s", p, bad)
		}
	}
	return nil
}

// OpenImage opens an existing regular file read-write. It refuses a block or
// character device, a symlink, and any path under /dev, /proc or /sys.
func OpenImage(path string) (*Image, error) {
	if err := refusePath(path); err != nil {
		return nil, err
	}
	lst, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !lst.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file (mode %v): the dry run writes only to an image file", path, lst.Mode())
	}
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || !os.SameFile(st, lst) {
		f.Close()
		return nil, fmt.Errorf("%s changed while it was opened", path)
	}
	return &Image{f: f, path: path, size: st.Size()}, nil
}

// Reader is a read-only view for the planner.
func (im *Image) Reader() io.ReaderAt { return im.f }

// Size is the image size in bytes.
func (im *Image) Size() int64 { return im.size }

// Path is the image path.
func (im *Image) Path() string { return im.path }

// Close closes the file.
func (im *Image) Close() error { return im.f.Close() }

// Writes is the number of primitive writes done.
func (im *Image) Writes() int { return im.writes }

func (im *Image) cutter() *Cutter {
	im.cut.Hooks, im.cut.N, im.cut.Step = im.hooks, im.writes, im.step
	return &im.cut
}

// writeData writes b at off of the target partition name. The range is
// checked against the GPT on every call (plan.CheckWriteRange).
func (im *Image) writeData(name string, off int64, b []byte) error {
	if im.t == nil || im.target == "" {
		return errors.New("internal error: image not bound to a plan")
	}
	if err := plan.CheckWriteRange(im.t, im.target, name, off, int64(len(b))); err != nil {
		return fmt.Errorf("write refused: %w", err)
	}
	e, _ := im.t.Find(name)
	abs := e.Start*gpt.Sector + off
	c := im.cutter()
	if err := c.Before(func() {
		half := (len(b) / 2) &^ (gpt.Sector - 1)
		im.f.WriteAt(b[:half], abs)
	}); err != nil {
		return err
	}
	if _, err := im.f.WriteAt(b, abs); err != nil {
		return err
	}
	im.writes++
	return c.After(Event{Op: "data", Partition: name, Off: abs, Len: int64(len(b))})
}

func (im *Image) readAB() ([]byte, error) {
	b := make([]byte, gpt.Sector)
	_, err := im.f.ReadAt(b, plan.ABLBA*gpt.Sector)
	return b, err
}

// writeAB puts the sector that uc-pxx-bootctl computed into the image. It is
// the dry-run stand-in for bootctl's own write of LBA 49156: guarded on the
// image still holding cur, refused unless next is valid and differs from cur
// only in the 32 bytes of AvbABData, then synced and read back.
func (im *Image) writeAB(cur, next []byte) error {
	if len(cur) != gpt.Sector || len(next) != gpt.Sector {
		return errors.New("A/B sector must be 512 bytes")
	}
	now, err := im.readAB()
	if err != nil {
		return err
	}
	if !bytes.Equal(now, cur) {
		return errors.New("A/B write refused: the image sector is not the one bootctl started from")
	}
	if !bytes.Equal(next[32:], cur[32:]) {
		return errors.New("A/B write refused: bytes 32..511 would change")
	}
	a, _ := avbab.Parse(next)
	if err := a.Validate(); err != nil {
		return fmt.Errorf("A/B write refused: the new sector is not valid: %w", err)
	}
	c := im.cutter()
	if err := c.Before(nil); err != nil {
		return err
	}
	if _, err := im.f.WriteAt(next, plan.ABLBA*gpt.Sector); err != nil {
		return err
	}
	if err := im.f.Sync(); err != nil {
		return err
	}
	im.writes++
	back, err := im.readAB()
	if err != nil {
		return err
	}
	if !bytes.Equal(back, next) {
		return errors.New("A/B read-back differs from what was written")
	}
	return c.After(Event{Op: "ab", Partition: "misc", Off: plan.ABLBA * gpt.Sector, Len: gpt.Sector})
}
