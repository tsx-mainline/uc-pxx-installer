//go:build install && step2

package step2

import (
	"testing"

	"uc-pxx-installer/internal/mainline"
)

// TestReleaseTargetFiles (RC-10b review MINOR-2): the release clone gate pins
// every both-slot file of the installer's gate and the apk keys in the copy.
func TestReleaseTargetFiles(t *testing.T) {
	b := mainline.EmbeddedBothSlot()
	s := &session{o: Options{}}
	got := s.cloneGate().TargetFiles
	if len(got) != len(b.Pins)+len(b.APKKeys) || len(got) < 26 {
		t.Fatalf("%d pins, want %d + %d", len(got), len(b.Pins), len(b.APKKeys))
	}
	seen := map[string]string{}
	for _, p := range got {
		seen[p.Path] = p.SHA256
	}
	for _, p := range append(append([]mainline.PinnedFile(nil), b.Pins...), b.APKKeys...) {
		if seen[p.Path] != p.SHA256 {
			t.Errorf("%s: %q, want %s", p.Path, seen[p.Path], p.SHA256)
		}
	}
	for _, p := range []string{"/usr/sbin/uc-pxx-bootctl", "/etc/uc-pxx-mark-boot.sh", "/usr/local/sbin/tsx-autoupdate",
		"/usr/libexec/uc-pxx-xfer/uc-pxx-xfer", "/usr/share/uc-pxx-usb/finish.json"} {
		if seen[p] == "" {
			t.Errorf("%s is not pinned", p)
		}
	}
}
