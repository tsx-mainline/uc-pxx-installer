package install

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"uc-pxx-installer/internal/bootctl"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/plan"
)

// Medium is what an install writes to: an image file that stands for the
// eMMC (the dry run, dryMedium below) or the eMMC itself (the device mode,
// package device). The executor (Run) does the same steps in the same order
// on both. It checks every result itself: a medium only does the I/O.
type Medium interface {
	// Kind is the plan mode that this medium runs: "dry-run" or "device".
	Kind() string
	// Prepare checks that the medium is the one the plan was made for (size,
	// GPT layout, A/B sector, bootctl binary) and binds it to the plan. It
	// writes nothing. It returns the GPT of the medium.
	Prepare(p *plan.Plan, hooks *Hooks) (*gpt.Table, error)
	// SetStep tells the medium the plan step that runs now (for events).
	SetStep(n int)
	// ReadAB reads the 512 bytes of the A/B sector from the medium.
	ReadAB() ([]byte, error)
	// BootctlStatus runs `uc-pxx-bootctl status` on the current A/B sector.
	BootctlStatus() (*plan.BootctlStatus, error)
	// Bootctl runs one A/B transition of the plan. cur is the sector that the
	// executor read just before, and that equals the plan's "before" state.
	// On a nil error the medium holds the new sector. The executor reads it
	// back and compares it with the prediction.
	Bootctl(s *plan.BootctlStep, cur []byte) (*BootctlRun, error)
	// OpenWrite opens one target partition for its write step.
	OpenWrite(w *plan.WriteStep) (PartWriter, error)
	// HashRange hashes length bytes at the absolute byte offset off.
	HashRange(off, length int64) (string, error)
	// Verify reads one written range again and returns its sha256.
	Verify(w *plan.WriteStep) (string, error)
	// Sync makes every write durable. names are the target partitions.
	Sync(names []string) error
	// Writes is the number of primitive writes done.
	Writes() int
	// Reader is a read-only view of the whole medium, by absolute offset.
	Reader() io.ReaderAt
	// Finish runs once at the end, after the last step or after a stop.
	Finish() error
}

// PartWriter writes one partition image, in order, from the step's offset.
type PartWriter interface {
	io.Writer
	// Close makes the data durable and closes the partition. It is called
	// once, also after an error.
	Close() error
}

// BootctlRun is the outcome of one uc-pxx-bootctl write command.
type BootctlRun struct {
	RC     int
	Stderr string
}

// Cutter counts the primitive writes of a medium and runs the test hooks.
// Both media use it, so the power-cut sweep means the same on both.
type Cutter struct {
	Hooks *Hooks
	N     int // primitive writes done
	Step  int
}

// Before is called before a primitive write. It returns ErrPowerCut when the
// hook cuts here. torn (may be nil) writes the first half of the write.
func (c *Cutter) Before(torn func()) error {
	h := c.Hooks
	if h == nil || h.CutAfter < 0 || c.N != h.CutAfter {
		return nil
	}
	if h.Torn && torn != nil {
		torn()
	}
	return ErrPowerCut
}

// After is called after a primitive write is done.
func (c *Cutter) After(ev Event) error {
	c.N++
	ev.Write, ev.Step = c.N, c.Step
	if c.Hooks == nil || c.Hooks.Observe == nil {
		return nil
	}
	return c.Hooks.Observe(ev)
}

// dryMedium is the image file of the dry run, with the uc-pxx-bootctl that
// works on a copy of the A/B sector.
type dryMedium struct {
	im   *Image
	tool bootctl.Tool
}

func (d *dryMedium) Kind() string            { return "dry-run" }
func (d *dryMedium) SetStep(n int)           { d.im.step = n }
func (d *dryMedium) ReadAB() ([]byte, error) { return d.im.readAB() }
func (d *dryMedium) Writes() int             { return d.im.writes }
func (d *dryMedium) Reader() io.ReaderAt     { return d.im.f }
func (d *dryMedium) Finish() error           { return nil }

func (d *dryMedium) Prepare(p *plan.Plan, hooks *Hooks) (*gpt.Table, error) {
	im := d.im
	im.hooks = hooks
	if im.size != p.Disk.Sectors*gpt.Sector {
		return nil, fmt.Errorf("image has %d bytes, the plan is for %d sectors", im.size, p.Disk.Sectors)
	}
	t, err := gpt.Parse(im.f, im.size)
	if err != nil {
		return nil, fmt.Errorf("GPT: %w", err)
	}
	if t.LayoutSHA256() != p.Disk.LayoutSHA256 {
		return nil, errors.New("the GPT layout differs from the plan")
	}
	sec, err := im.readAB()
	if err != nil {
		return nil, err
	}
	if s := sha256.Sum256(sec); hex.EncodeToString(s[:]) != p.AB.SectorSHA256 {
		return nil, errors.New("the A/B sector differs from the plan")
	}
	sha, err := d.tool.SHA256()
	if err != nil || sha != p.Bootctl.SHA256 || d.tool.Path != p.Bootctl.Path {
		return nil, errors.New("the bootctl binary is not the one the plan names")
	}
	im.t, im.target = t, p.Target
	return t, nil
}

func (d *dryMedium) BootctlStatus() (*plan.BootctlStatus, error) {
	sec, err := d.im.readAB()
	if err != nil {
		return nil, err
	}
	st, _, err := d.tool.Status(sec)
	return st, err
}

// Bootctl runs the tool on a copy of the sector. The result goes into the
// image only when it is exactly the plan's prediction (writeAB).
func (d *dryMedium) Bootctl(b *plan.BootctlStep, cur []byte) (*BootctlRun, error) {
	want1, _ := b.After.Raw()
	res, err := d.tool.Apply(cur, b.Args)
	if err != nil {
		return nil, err
	}
	run := &BootctlRun{RC: res.RC, Stderr: res.Stderr}
	if res.RC != bootctl.RCOK {
		return run, rcError(b.Args, res.RC, res.Stderr, "ON A DEVICE THE A/B STATE WOULD BE UNKNOWN: do not reboot")
	}
	if !bytes.Equal(res.Sector[32:], cur[32:]) {
		return run, errors.New("uc-pxx-bootctl changed bytes 32..511 of the sector")
	}
	if !bytes.Equal(res.Sector[:32], want1[:]) {
		return run, fmt.Errorf("uc-pxx-bootctl wrote %x, the plan predicts %x: stopping, nothing put into the image", res.Sector[:32], want1)
	}
	if bytes.Equal(res.Sector, cur) {
		if !b.NoOp {
			return run, errors.New("uc-pxx-bootctl changed nothing, but the plan predicts a change")
		}
		return run, nil
	}
	return run, d.im.writeAB(cur, res.Sector)
}

func (d *dryMedium) OpenWrite(w *plan.WriteStep) (PartWriter, error) {
	return &imageWriter{im: d.im, name: w.Partition, off: w.Offset}, nil
}

func (d *dryMedium) HashRange(off, length int64) (string, error) {
	h := sha256.New()
	_, err := io.Copy(h, io.NewSectionReader(d.im.f, off, length))
	return hex.EncodeToString(h.Sum(nil)), err
}

func (d *dryMedium) Verify(w *plan.WriteStep) (string, error) {
	return d.HashRange(w.StartLBA*gpt.Sector+w.Offset, w.Length)
}

func (d *dryMedium) Sync(names []string) error { return d.im.f.Sync() }

// imageWriter writes one partition of the image. Each Write is one
// primitive write, with the range checked again (Image.writeData).
type imageWriter struct {
	im   *Image
	name string
	off  int64
}

func (w *imageWriter) Write(b []byte) (int, error) {
	if err := w.im.writeData(w.name, w.off, b); err != nil {
		return 0, err
	}
	w.off += int64(len(b))
	return len(b), nil
}

func (w *imageWriter) Close() error { return nil }

// rcError explains a non-zero exit code of uc-pxx-bootctl.
func rcError(args []string, rc int, stderr, unknown string) error {
	what := "refused, sector unchanged"
	switch rc {
	case bootctl.RCIOSame:
		what = "I/O error, sector verified unchanged"
	case bootctl.RCUnknown:
		what = unknown
	}
	if rc != bootctl.RCRefused && rc != bootctl.RCIOSame && rc != bootctl.RCUnknown {
		what = "unexpected exit: " + unknown
	}
	return fmt.Errorf("uc-pxx-bootctl %s: exit %d (%s): %s", joinArgs(args), rc, what, trim(stderr))
}
