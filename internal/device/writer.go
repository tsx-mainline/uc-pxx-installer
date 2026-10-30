//go:build install

package device

// The write layer. This file has the only open of an eMMC node for writing
// in p10inst. Everything it writes is one range of one partition of the
// target slot, from a plan whose id checked out. It never opens the whole
// disk for writing, and never the misc partition (the A/B sector belongs to
// uc-pxx-bootctl-inst).

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/plan"
)

// partWriter writes one partition image of the target. It collects the data
// in an aligned buffer and writes whole buffers with O_DIRECT, so each
// primitive write is chunk bytes (the last one may be shorter, but whole
// sectors).
type partWriter struct {
	d      *Disk
	p      Part
	target string
	f      *os.File
	base   int64 // the step's offset in the partition
	pos    int64 // bytes written after base
	buf    []byte
	n      int // bytes in buf
	cut    *install.Cutter
	dead   bool // a write error or a (simulated) power cut: no more data
	cutOff bool // a simulated power cut: no more I/O at all
	closed bool
}

// openWrite opens the partition of one write step. Before the open it
// checks the range rule of the plan, that the partition is a target
// partition with the plan's index, start and size, and that the node is the
// block device that sysfs names. It then clears the kernel read-only flag of
// this node and of the disk (the disk flag applies to every partition), and
// opens the node O_RDWR|O_DIRECT|O_EXCL (O_EXCL fails while the partition is
// mounted or held).
func (d *Disk) openWrite(target string, w *plan.WriteStep, chunk int, cut *install.Cutter) (*partWriter, error) {
	if d.Table == nil {
		return nil, errors.New("internal error: the disk has no GPT")
	}
	if !d.Locked() {
		return nil, errors.New("write refused: the A/B lock is not held")
	}
	if err := plan.CheckWriteRange(d.Table, target, w.Partition, w.Offset, w.Length); err != nil {
		return nil, fmt.Errorf("write refused: %w", err)
	}
	p, ok := d.byName[w.Partition]
	if !ok || p.Index != w.GPTIndex || p.Start != w.StartLBA || p.Size != w.PartSectors {
		return nil, fmt.Errorf("write refused: %s is p%d start %d size %d, the plan says p%d start %d size %d",
			w.Partition, p.Index, p.Start, p.Size, w.GPTIndex, w.StartLBA, w.PartSectors)
	}
	if chunk <= 0 || chunk%gpt.Sector != 0 {
		return nil, fmt.Errorf("write chunk %d is not whole sectors", chunk)
	}
	node := d.partNode(p)
	if err := d.e.checkNode(node, p.Major, p.Minor, p.Bytes()); err != nil {
		return nil, fmt.Errorf("write refused: %w", err)
	}
	if err := d.e.setRO(d.diskNode(), d.Major, d.Minor, false); err != nil {
		return nil, err
	}
	if err := d.e.setRO(node, p.Major, p.Minor, false); err != nil {
		d.e.setRO(d.diskNode(), d.Major, d.Minor, true)
		return nil, err
	}
	flags := os.O_RDWR | syscall.O_CLOEXEC | syscall.O_NOFOLLOW
	if !d.e.regular {
		flags |= syscall.O_DIRECT | syscall.O_EXCL
	}
	f, err := os.OpenFile(node, flags, 0)
	if err == nil {
		err = d.e.sameOpen(f, p.Major, p.Minor)
		if err != nil {
			f.Close()
		}
	}
	if err != nil {
		d.e.setRO(node, p.Major, p.Minor, true)
		d.e.setRO(d.diskNode(), d.Major, d.Minor, true)
		return nil, fmt.Errorf("open %s for writing: %w", node, err)
	}
	return &partWriter{d: d, p: p, target: target, f: f, base: w.Offset, buf: alignedBuf(chunk), cut: cut}, nil
}

// Write collects b and writes every full buffer.
func (w *partWriter) Write(b []byte) (int, error) {
	if w.dead || w.closed {
		return 0, errors.New("write after a stop")
	}
	done := 0
	for done < len(b) {
		k := copy(w.buf[w.n:], b[done:])
		w.n += k
		done += k
		if w.n == len(w.buf) {
			if err := w.flush(); err != nil {
				return done, err
			}
		}
	}
	return done, nil
}

// flush is one primitive write: the buffer at base+pos. The range rule is
// checked again for exactly this range.
func (w *partWriter) flush() error {
	if w.n == 0 {
		return nil
	}
	if w.n%gpt.Sector != 0 {
		w.dead = true
		return fmt.Errorf("%s: the image does not end on a whole sector", w.p.Name)
	}
	off := w.base + w.pos
	data := w.buf[:w.n]
	if err := plan.CheckWriteRange(w.d.Table, w.target, w.p.Name, off, int64(len(data))); err != nil {
		w.dead = true
		return fmt.Errorf("write refused: %w", err)
	}
	if err := w.cut.Before(func() {
		half := (len(data) / 2) &^ (gpt.Sector - 1)
		if half > 0 {
			w.f.WriteAt(data[:half], off)
		}
	}); err != nil {
		w.dead, w.cutOff = true, true
		return err
	}
	n, err := w.f.WriteAt(data, off)
	if err == nil && n != len(data) {
		err = fmt.Errorf("short write: %d of %d bytes", n, len(data))
	}
	if err != nil {
		w.dead = true
		return fmt.Errorf("%s at %d: %w", w.p.Name, off, err)
	}
	w.pos += int64(n)
	w.n = 0
	return w.cut.After(install.Event{Op: "data", Partition: w.p.Name, Off: w.p.Start*gpt.Sector + off, Len: int64(n)})
}

// Close writes the last buffer, fsyncs the node (that also flushes the
// eMMC cache), closes it, drops its page cache and sets the read-only flags
// of the node and the disk again. After a write error it writes no more data
// but still fsyncs and sets the flags. After a simulated power cut it only
// closes the node, because a power cut does nothing more either
// (Medium.Finish sets the flags again).
func (w *partWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	var errs []error
	if !w.dead {
		if err := w.flush(); err != nil {
			errs = append(errs, err)
		}
	}
	if w.cutOff {
		w.f.Close()
		return errors.Join(errs...)
	}
	if err := w.f.Sync(); err != nil {
		errs = append(errs, fmt.Errorf("%s: fsync: %w", w.p.Name, err))
	}
	if err := w.f.Close(); err != nil {
		errs = append(errs, err)
	}
	node := w.d.partNode(w.p)
	if err := w.d.e.flushBuf(node, w.p.Major, w.p.Minor); err != nil {
		errs = append(errs, err)
	}
	if err := w.d.e.setRO(node, w.p.Major, w.p.Minor, true); err != nil {
		errs = append(errs, err)
	}
	if err := w.d.e.setRO(w.d.diskNode(), w.d.Major, w.d.Minor, true); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
