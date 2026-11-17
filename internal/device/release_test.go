//go:build install && !devtest

package device

import (
	"errors"
	"reflect"
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
	// Live is the environment of the installer RAM image: the real root, no
	// test option, the embedded layout and the A/B tool of that image. From
	// RC-10 on (I-2) the tool of the RAM image is r3 RAMBootctlSHA256. A
	// running slot (SlotLive) keeps r2 BootctlSHA256.
	e := Live()
	if e.IsTest() || e.root != "/" || e.regular || e.chroot || e.bootctlSHA != RAMBootctlSHA256 ||
		e.manifests != nil || e.abReadErr != nil || e.gate != nil || e.seedRegular != nil {
		t.Fatalf("Live is not the device: %+v", e)
	}
	if _, ok := e.runner.(execRunner); !ok {
		t.Fatalf("Live does not run the real binary: runner %T", e.runner)
	}
	if !reflect.DeepEqual(e.layout, Layout()) {
		t.Fatal("Live does not have the embedded UC-P10 layout")
	}
}
