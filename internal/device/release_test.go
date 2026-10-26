//go:build install && !devtest

package device

import (
	"errors"
	"testing"
)

// In a release install build there is no test environment at all.
func TestReleaseBuildHasNoTestEnv(t *testing.T) {
	_, err := NewForTest(TestOptions{Root: t.TempDir(), RegularFiles: true})
	if !errors.Is(err, ErrNotTestBuild) {
		t.Fatalf("NewForTest in a release build: %v", err)
	}
	if testBuildMarker != "" {
		t.Fatal("the devtest marker is in a release build")
	}
	e := Live()
	if e.IsTest() || e.regular || e.chroot || e.bootctlSHA != BootctlSHA256 || e.manifests != nil || e.resources != nil {
		t.Fatalf("Live is not the device: %+v", e)
	}
}
