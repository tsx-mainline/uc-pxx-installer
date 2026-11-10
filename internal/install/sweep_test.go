package install_test

// The power-cut sweep runs its points in parallel (FASTTEST.md): each cut
// point is a subtest with its own fresh image in its own temporary folder
// (t.TempDir), and calls t.Parallel. go test -parallel N bounds how many run
// at once in this package.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"

	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/plan"
)

// point runs f as a parallel subtest.
func point(t *testing.T, name string, f func(t *testing.T)) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		t.Parallel()
		f(t)
	})
}

// sweepPoints are the cut points k of a sweep over n primitive writes:
// every k from 0 to n. Under -short (the fast tier) a fixed sample: every
// 13th k, both ends, and the writes around each step change and each A/B
// write (from the journal of the full run).
func sweepPoints(n int, ev []install.Event) []int {
	if !testing.Short() {
		ks := make([]int, 0, n+1)
		for k := 0; k <= n; k++ {
			ks = append(ks, k)
		}
		return ks
	}
	in := map[int]bool{0: true, n: true}
	for k := 0; k <= n; k += 13 {
		in[k] = true
	}
	step := -1
	for _, e := range ev {
		var b []int
		if e.Step != step {
			b = append(b, e.Write-1)
			step = e.Step
		}
		if e.Op == "ab" {
			b = append(b, e.Write-1, e.Write)
		}
		for _, x := range b {
			for _, k := range []int{x - 1, x, x + 1} {
				if k >= 0 && k <= n {
					in[k] = true
				}
			}
		}
	}
	var ks []int
	for k := range in {
		ks = append(ks, k)
	}
	sort.Ints(ks)
	return ks
}

var sweepListMu sync.Mutex

// sweepList records a sweep point: with P10INST_SWEEP_LIST=FILE each point
// appends one line "Test|shape|point" to FILE (FASTTEST.md: the points of
// the old serial sweep and of this parallel one must be the same set).
func sweepList(t *testing.T, id ...string) {
	f := os.Getenv("P10INST_SWEEP_LIST")
	if f == "" {
		return
	}
	top, _, _ := strings.Cut(t.Name(), "/")
	sweepListMu.Lock()
	defer sweepListMu.Unlock()
	w, err := os.OpenFile(f, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.WriteString(top + "|" + strings.Join(id, "|") + "\n"); err != nil {
		t.Fatal(err)
	}
}

// sameOutside reports whether two images are equal outside the target's
// write ranges and the A/B sector: the bytes that no step may change. It
// compares in place (it replaces the sha256 of two masked copies, 2 x 44 MiB
// per check).
func sameOutside(a, b []byte, p *plan.Plan) bool {
	if len(a) != len(b) {
		return false
	}
	type span struct{ lo, hi int64 }
	var ms []span
	for _, s := range p.Steps {
		if s.Write != nil {
			o := s.Write.StartLBA*512 + s.Write.Offset
			ms = append(ms, span{o, o + s.Write.Length})
		}
		if s.KeepKey != nil {
			for _, l := range s.KeepKey.LBAs {
				ms = append(ms, span{l * 512, (l + 1) * 512})
			}
		}
	}
	ms = append(ms, span{plan.ABLBA * 512, (plan.ABLBA + 1) * 512})
	sort.Slice(ms, func(i, j int) bool { return ms[i].lo < ms[j].lo })
	at := int64(0)
	for _, m := range ms {
		if m.lo < 0 || m.hi > int64(len(a)) || m.lo > m.hi {
			panic(fmt.Sprintf("mask range %d..%d outside the image", m.lo, m.hi))
		}
		if m.lo > at && !bytes.Equal(a[at:m.lo], b[at:m.lo]) {
			return false
		}
		if m.hi > at {
			at = m.hi
		}
	}
	return bytes.Equal(a[at:], b[at:])
}

// samePlan reports whether plan b (of an image at path pb) is plan a (of an
// image at path pa) but for the path: the old sweep made every point's image
// at the same path and compared the plan ids; each parallel point has its
// own folder.
func samePlan(a, b *plan.Plan, pa, pb string) bool {
	if pa == pb {
		return false // not the point's own folder
	}
	ca, cb := *a, *b
	ca.ID, cb.ID = "", ""
	ja, err1 := json.Marshal(&ca)
	jb, err2 := json.Marshal(&cb)
	return err1 == nil && err2 == nil && bytes.Equal(ja, bytes.ReplaceAll(jb, []byte(pb), []byte(pa)))
}
