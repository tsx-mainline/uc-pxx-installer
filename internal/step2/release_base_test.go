//go:build install && step2 && !finish

package step2

import (
	"os"
	"path/filepath"
	"testing"

	"uc-pxx-installer/internal/clone"
)

// TestReleaseBaseKeeperFiles (RC-10 release review BLOCKER-1): on the files of
// the release's R23 base (RC_RELEASE_BASE_TREE = a folder that holds the base
// rootfs's /etc/tsx/step2-bootctl-inst, /usr/libexec/uc-pxx-step2/uc-pxx-step2
// and /usr/libexec/uc-pxx-provision/uc-pxx-provision at their paths; not in
// the repo), the two checks of keeperFiles that a test build skips pass: the
// marker names the base's uc-pxx-step2 as a word (markerNames, as the clone
// plan runs it with the running program's sha256), and the base's
// uc-pxx-provision is the embedded keeper pin (size and sha256).
func TestReleaseBaseKeeperFiles(t *testing.T) {
	d := os.Getenv("RC_RELEASE_BASE_TREE")
	if d == "" {
		t.Skip("RC_RELEASE_BASE_TREE not set (the release base's files are not in the repo)")
	}
	s2, err := os.ReadFile(filepath.Join(d, clone.Step2Binary))
	if err != nil {
		t.Fatal(err)
	}
	if err := markerNames(filepath.Join(d, clone.Step2Marker), clone.SHA256(s2)); err != nil {
		t.Fatalf("the base's marker: %v", err)
	}
	pins := gateKeeperFiles()
	if len(pins) != 1 {
		t.Fatalf("keeper pins %+v", pins)
	}
	p, err := os.ReadFile(filepath.Join(d, pins[0].Path))
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(p)) != pins[0].Size || clone.SHA256(p) != pins[0].SHA256 {
		t.Fatalf("the base's %s: %d bytes %s, the pin %+v", pins[0].Path, len(p), clone.SHA256(p), pins[0])
	}
	// the RC-10 first build's marker form: the sha256 followed by ")," is not a word
	bad := filepath.Join(t.TempDir(), "m")
	os.WriteFile(bad, []byte("x (uc-pxx-step2 "+clone.SHA256(s2)+"), y.\n"), 0o644)
	if markerNames(bad, clone.SHA256(s2)) == nil {
		t.Fatal("a sha256 glued to punctuation was taken")
	}
}
