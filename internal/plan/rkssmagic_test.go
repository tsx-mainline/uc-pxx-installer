package plan_test

// Review T4-2 NIT-2: the RKSS magic precondition in the two RAM-installer
// mainline planners (ComputeMainline writes uboot_a, ComputeMainlineDirect
// writes uboot_b): with the magic at LBA 1024, in either byte order, both
// refuse by the check rkss-magic; without it they plan.

import (
	"io"
	"testing"

	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/plan"
)

// overlay is a disk with some bytes replaced at off.
type overlay struct {
	r   io.ReaderAt
	off int64
	b   []byte
}

func (o overlay) ReadAt(p []byte, off int64) (int, error) {
	n, err := o.r.ReadAt(p, off)
	for i := 0; i < n; i++ {
		if j := off + int64(i) - o.off; j >= 0 && j < int64(len(o.b)) {
			p[i] = o.b[j]
		}
	}
	return n, err
}

func refusedBy(cs []plan.Check, id string) bool {
	for _, c := range cs {
		if c.ID == id && !c.OK {
			return true
		}
	}
	return false
}

func TestMainlinePlannersRKSSMagic(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	for _, m := range [][]byte{nil, {0x88, 0x77, 0x66, 0x55}, {0x55, 0x66, 0x77, 0x88}} {
		w := newMLWorld(t, fx, mlUnit, nil)
		if m != nil {
			w.in.Disk = overlay{w.in.Disk, 1024*512 + 64, m}
		}
		p, err := plan.ComputeMainline(w.in)
		if (err == nil) != (m == nil) || m != nil && !refusedBy(p.Checks, "rkss-magic") {
			t.Fatalf("ComputeMainline with %x at LBA 1024: %v\n%s", m, err, mlRender(p))
		}
		d := newDWorld(t, fx, dUnitReset)
		if m != nil {
			d.in.Disk = overlay{d.in.Disk, 1024*512 + 64, m}
		}
		dp, err := plan.ComputeMainlineDirect(d.in)
		if (err == nil) != (m == nil) || m != nil && !refusedBy(dp.Checks, "rkss-magic") {
			t.Fatalf("ComputeMainlineDirect with %x at LBA 1024: %v\n%s", m, err, dRender(dp))
		}
	}
}
