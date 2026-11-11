//go:build install && step2 && finish && devtest

package main

// The devtest twin of the finish executor (tests only; never a release
// build: ci/check-no-writer.py --step2-finish refuses the devtest marker).
// TestStep2RealLayout runs it as a separate process from the target's
// /run/uc-pxx-finish, next to the sealed r2, on the devicetest root of the
// run: UC_PXX_FINISH_TEST_ROOT (the root), UC_PXX_FINISH_TEST_EXEC (the runner
// of the real r2, qemu-aarch64-static), UC_PXX_FINISH_TEST_TMP, and the
// keeper's saved state folder UC_PXX_FINISH_TEST_SAVED of slot
// UC_PXX_FINISH_TEST_K (the harness moves the state folder per boot). Without
// UC_PXX_FINISH_TEST_ROOT it is the release's environment.

import (
	"os"
	"path/filepath"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/device/devicetest"
	"uc-pxx-installer/internal/step2"
)

func env(o *step2.Options) (*device.Env, error) {
	root := os.Getenv("UC_PXX_FINISH_TEST_ROOT")
	if root == "" {
		return device.SlotLive(), nil
	}
	l := device.Layout()
	r := devicetest.Reopen(root, l)
	bc := &devicetest.Bootctl{Root: r, Exec: os.Getenv("UC_PXX_FINISH_TEST_EXEC"), Tmp: os.Getenv("UC_PXX_FINISH_TEST_TMP")}
	saved, k := os.Getenv("UC_PXX_FINISH_TEST_SAVED"), os.Getenv("UC_PXX_FINISH_TEST_K")
	o.RecordReader = func(_ *device.Disk, slot, file string) ([]byte, error) {
		if slot != k || saved == "" {
			return nil, os.ErrNotExist
		}
		return os.ReadFile(filepath.Join(saved, file))
	}
	return device.NewForTest(device.TestOptions{Root: root, RegularFiles: true, Layout: l, Runner: bc})
}
