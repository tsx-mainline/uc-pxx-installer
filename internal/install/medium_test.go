package install

// Review F6: when the medium verified an A/B step, but the executor's own
// second read-back fails, the state is "not known" (exit 3 on a device), not
// a plain stop.

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"uc-pxx-installer/internal/bootctl"
	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/plan"
)

type failingReadBack struct {
	*dryMedium
	calls  int
	failAt int
}

func (f *failingReadBack) ReadAB() ([]byte, error) {
	f.calls++
	if f.calls == f.failAt {
		return nil, errors.New("test: read error")
	}
	return f.dryMedium.ReadAB()
}

func TestReadBackFailureIsUnknown(t *testing.T) {
	p := os.Getenv("UC_PXX_BOOTCTL")
	if p == "" {
		t.Skip("UC_PXX_BOOTCTL not set")
	}
	tool := bootctl.Tool{Path: p, Exec: os.Getenv("UC_PXX_BOOTCTL_EXEC"), TmpDir: t.TempDir()}
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	path, err := fx.Image(t.TempDir(), "emmc.img", fakeemmc.Unit{AB: [2][3]byte{{15, 0, 1}, {14, 0, 1}}, A: "old", B: "old"})
	if err != nil {
		t.Fatal(err)
	}
	im, err := OpenImage(path)
	if err != nil {
		t.Fatal(err)
	}
	defer im.Close()
	sec, _ := im.readAB()
	st, _, err := tool.Status(sec)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := tool.Info()
	pl, err := plan.VerifyOfficial(bytes.NewReader(fx.FW), int64(len(fx.FW)), fx.Keys, fx.Manifests, nil)
	if err != nil {
		t.Fatal(err)
	}
	pp, err := plan.Compute(plan.Input{Disk: im.f, DiskBytes: im.size, Source: "image:" + filepath.Base(path), Mode: "dry-run",
		Tool: "test", PreFastboot: sec[:32], Payload: pl, Bootctl: info, BootctlStatus: st, PrereqsConfirmed: true,
		ApprovedResources: []string{fx.Resource}})
	if err != nil {
		t.Fatal(err)
	}
	// ReadAB calls: 1 the "start" log line, 2 ab_before of step 1, 3 the
	// sector before bootctl, 4 the read-back after it
	m := &failingReadBack{dryMedium: &dryMedium{im: im, tool: tool}, failAt: 4}
	_, err = Run(pp, pl, m, Options{})
	if !errors.Is(err, ErrABUnknown) {
		t.Fatalf("err = %v, want ErrABUnknown", err)
	}
}
