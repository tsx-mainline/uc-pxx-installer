//go:build install && devtest

package devmode_test

// The sweeps run their points in parallel (FASTTEST.md): each cut point,
// kill point and fault run is a subtest with its own fake unit and its own
// temporary folder (t.TempDir), and calls t.Parallel. go test -parallel N
// bounds how many run at once in this package. A point never shares a unit
// with another point that runs at the same time.

import (
	"os"
	"strings"
	"sync"
	"testing"
)

// point runs f as a parallel subtest.
func point(t *testing.T, name string, f func(t *testing.T)) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		t.Parallel()
		f(t)
	})
}

var sweepListMu sync.Mutex

// sweepList records a sweep point: with P10INST_SWEEP_LIST=FILE each point
// appends one line "Test|shape|point" to FILE. The list of the old serial
// sweeps and of these parallel ones must be the same set (FASTTEST.md).
func sweepList(t *testing.T, id ...string) {
	f := os.Getenv("P10INST_SWEEP_LIST")
	if f == "" {
		return
	}
	top, _, _ := strings.Cut(t.Name(), "/")
	line := top + "|" + strings.Join(id, "|") + "\n"
	sweepListMu.Lock()
	defer sweepListMu.Unlock()
	w, err := os.OpenFile(f, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.WriteString(line); err != nil {
		t.Fatal(err)
	}
}
