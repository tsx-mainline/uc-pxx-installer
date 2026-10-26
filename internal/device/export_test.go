//go:build install && devtest

package device

import (
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/plan"
)

// OpenWriteForTest opens the write layer directly, without a plan, so the
// tests can check its own refusals.
func OpenWriteForTest(d *Disk, target string, w *plan.WriteStep, chunk int) (install.PartWriter, error) {
	return d.openWrite(target, w, chunk, &install.Cutter{})
}

var BootctlFormForTest = bootctlForm
