package zstd

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"
)

// genText is the large test input in place of the Go tree's Opticks text:
// deterministic, compressible text of about 4 MiB.
func genText() []byte {
	var b bytes.Buffer
	for i := 0; b.Len() < 4<<20; i++ {
		fmt.Fprintf(&b, "line %d of the uc-pxx zstd test: the quick brown fox %x jumps over %d lazy dogs\n", i, i*2654435761, i%97)
	}
	return b.Bytes()
}

// TestUCPXXWindow23 decodes what zstd -19 (windowLog 23, the payload's
// setting) makes of a 64 MiB input with long repeats, when the zstd tool is
// there: the decoder's 8 MiB window covers it.
func TestUCPXXWindow23(t *testing.T) {
	z, err := exec.LookPath("zstd")
	if err != nil {
		t.Skip("no zstd tool")
	}
	in := bytes.Repeat(genText(), 16)
	cmd := exec.Command(z, "-q", "-c", "-19", "--zstd=wlog=23")
	cmd.Stdin = bytes.NewReader(in)
	var c bytes.Buffer
	cmd.Stdout, cmd.Stderr = &c, os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(NewReader(bytes.NewReader(c.Bytes())))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, in) {
		t.Fatalf("decoded %d bytes, not the %d of the input", len(got), len(in))
	}
}

// TestUCPXXMeasure measures the unpack of a real compressed root file system
// (UC_PXX_ZST_MEASURE=FILE.zst; skipped without it): the decode and the
// sha256 of the unpacked bytes, as the step-1 receiver does them, streamed
// with no copy kept. Run under qemu-aarch64 for the RC-10 record.
func TestUCPXXMeasure(t *testing.T) {
	p := os.Getenv("UC_PXX_ZST_MEASURE")
	if p == "" {
		t.Skip("UC_PXX_ZST_MEASURE not set")
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fi, _ := f.Stat()
	h := sha256.New()
	t0 := time.Now()
	n, err := io.CopyBuffer(h, NewReader(bufio.NewReaderSize(f, 1<<20)), make([]byte, 1<<20))
	d := time.Since(t0)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("MEASURE %s: %d compressed bytes, %d unpacked, sha256 %x, %.2f s, %.1f MiB/s unpacked", p, fi.Size(), n, h.Sum(nil),
		d.Seconds(), float64(n)/d.Seconds()/(1<<20))
}
