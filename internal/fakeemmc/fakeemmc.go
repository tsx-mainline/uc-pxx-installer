// Package fakeemmc builds an image file that stands for the UC-P10 eMMC, for
// dry runs and tests: a sparse file with a GPT made from a layout (names,
// starts and sizes only), partition contents copied from files, marker bytes
// in the regions an install must never touch, and an A/B sector.
//
// The A/B sector of a test image is assembled here from slot values. That is
// for test images only. It is never a source for a device write.
package fakeemmc

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"os"

	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/plan"
)

// Spec describes a test image.
type Spec struct {
	Layout *gpt.Layout
	// AB is {priority, tries, successful} of slot a and b.
	AB [2][3]byte
	// Fill maps a partition name to a file whose bytes go at its start.
	Fill map[string]string
	// FillBytes maps a partition name to bytes that go at its start.
	FillBytes map[string][]byte
	// Markers writes a pattern into sectors 64..2047, the first and the last
	// 64 KiB of every partition that is not filled, the misc sectors around
	// the A/B sector, and bytes 32..511 of the A/B sector.
	Markers bool
}

// Pattern is the marker for a region: a sha256 chain of its name.
func Pattern(name string, n int) []byte {
	out := make([]byte, 0, n+32)
	s := sha256.Sum256([]byte("p10inst-marker " + name))
	for len(out) < n {
		out = append(out, s[:]...)
		s = sha256.Sum256(s[:])
	}
	return out[:n]
}

// ABSector assembles a 512-byte A/B sector (test images only).
func ABSector(slots [2][3]byte, tail []byte) []byte {
	s := make([]byte, gpt.Sector)
	copy(s, "\x00AB0")
	s[4] = 1
	for i := 0; i < 2; i++ {
		copy(s[8+4*i:], slots[i][:])
	}
	binary.BigEndian.PutUint32(s[28:], crc32.ChecksumIEEE(s[:28]))
	copy(s[32:], tail)
	return s
}

// Create makes a new image file. It refuses an existing path.
func Create(path string, sp Spec) (err error) {
	l := sp.Layout
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	if err := f.Truncate(l.DiskSectors * gpt.Sector); err != nil {
		return err
	}
	pri, bak, err := gpt.Build(l)
	if err != nil {
		return err
	}
	if _, err := f.WriteAt(pri, 0); err != nil {
		return err
	}
	if _, err := f.WriteAt(bak, (l.DiskSectors-1-gpt.EntrySectors)*gpt.Sector); err != nil {
		return err
	}
	filled := map[string]bool{}
	find := func(name string) (gpt.LayoutEntry, error) {
		for _, e := range l.Entries {
			if e.Name == name {
				return e, nil
			}
		}
		return gpt.LayoutEntry{}, fmt.Errorf("no partition %s in the layout", name)
	}
	for name, src := range sp.Fill {
		e, err := find(name)
		if err != nil {
			return err
		}
		in, err := os.Open(src)
		if err != nil {
			return err
		}
		st, _ := in.Stat()
		if st.Size() > e.Size*gpt.Sector {
			in.Close()
			return fmt.Errorf("%s (%d bytes) does not fit into %s", src, st.Size(), name)
		}
		_, err = io.Copy(io.NewOffsetWriter(f, e.Start*gpt.Sector), in)
		in.Close()
		if err != nil {
			return err
		}
		filled[name] = true
	}
	for name, b := range sp.FillBytes {
		e, err := find(name)
		if err != nil {
			return err
		}
		if int64(len(b)) > e.Size*gpt.Sector {
			return fmt.Errorf("%d bytes do not fit into %s", len(b), name)
		}
		if _, err := f.WriteAt(b, e.Start*gpt.Sector); err != nil {
			return err
		}
		filled[name] = true
	}
	var tail []byte
	if sp.Markers {
		if _, err := f.WriteAt(Pattern("idbloader", (plan.ProtectedEnd-64)*gpt.Sector), 64*gpt.Sector); err != nil {
			return err
		}
		for _, e := range l.Entries {
			if filled[e.Name] {
				continue
			}
			n := int64(64 << 10)
			if n > e.Size*gpt.Sector/2 {
				n = e.Size * gpt.Sector / 2
			}
			if e.Name == "misc" {
				// misc: everything in its first 64 KiB except the A/B sector
				m := Pattern("misc", 64<<10)
				f.WriteAt(m[:4*gpt.Sector], e.Start*gpt.Sector)
				f.WriteAt(m[5*gpt.Sector:], (e.Start+5)*gpt.Sector)
				continue
			}
			if _, err := f.WriteAt(Pattern(e.Name+" head", int(n)), e.Start*gpt.Sector); err != nil {
				return err
			}
			if _, err := f.WriteAt(Pattern(e.Name+" tail", int(n)), (e.Start+e.Size)*gpt.Sector-n); err != nil {
				return err
			}
		}
		tail = Pattern("ab tail", gpt.Sector-32)
	}
	_, err = f.WriteAt(ABSector(sp.AB, tail), plan.ABLBA*gpt.Sector)
	return err
}
