package nvram_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"testing"

	"uc-pxx-installer/internal/nvram"
)

// TestModelHarness is the Go side of ci/nvram-model-diff.py, which compares
// Parse with a model of the Android driver (ci/nvram_drvmodel.py). It runs
// only when NVRAM_MODEL_HARNESS names a base ring file (RingSize bytes);
// otherwise it is skipped.
//
// stdin: per case a line "N", then N times one byte part index and PartSize
// bytes; the parts replace those of a copy of the base ring. stdout: one JSON
// line per case, the AutoUpdate of Parse.
func TestModelHarness(t *testing.T) {
	path := os.Getenv("NVRAM_MODEL_HARNESS")
	if path == "" {
		t.Skip("NVRAM_MODEL_HARNESS not set: run by ci/nvram-model-diff.py")
	}
	base, err := os.ReadFile(path)
	if err != nil || len(base) != nvram.RingSize {
		t.Fatalf("base ring: %v, %d bytes", err, len(base))
	}
	in := bufio.NewReaderSize(os.Stdin, 1<<20)
	out := bufio.NewWriter(os.Stdout)
	enc := json.NewEncoder(out)
	ring := make([]byte, len(base))
	for {
		if _, err := in.Peek(1); err == io.EOF {
			return
		}
		var n int
		if _, err := fmt.Fscanf(in, "%d\n", &n); err != nil {
			t.Fatalf("case header: %v", err)
		}
		copy(ring, base)
		for k := 0; k < n; k++ {
			idx, err := in.ReadByte()
			if err != nil || int(idx) >= nvram.Parts {
				t.Fatalf("part index %d: %v", idx, err)
			}
			if _, err := io.ReadFull(in, ring[int(idx)*nvram.PartSize:(int(idx)+1)*nvram.PartSize]); err != nil {
				t.Fatalf("part %d: %v", idx, err)
			}
		}
		if err := enc.Encode(nvram.Parse(ring)); err != nil {
			t.Fatal(err)
		}
		if err := out.Flush(); err != nil {
			t.Fatal(err)
		}
	}
}
