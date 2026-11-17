//go:build install && step2 && !finish

package step2

import "testing"

// TestGateKeeperEmbedded (mainline-rc-10 release data): the embedded keeper
// pin is the release's uc-pxx-provision (8b22b4e0…, 6946940 bytes, mainline-rc-10c; built at
// 1b3ddf6 after gate-profiles.json), and the release clone gate of this
// program (the profiles of internal/clone with that pin) passes Gate.Check:
// a clone plan does not refuse for an empty gate.
func TestGateKeeperEmbedded(t *testing.T) {
	l := gateKeeperFiles()
	if len(l) != 1 || l[0].Path != "/usr/libexec/uc-pxx-provision/uc-pxx-provision" || l[0].Size != 6946940 ||
		l[0].SHA256 != "8b22b4e08d43b9aec3245dc943dd2f54f5062026330b10190abfff8236891d3f" {
		t.Fatalf("the embedded keeper pin is %+v", l)
	}
	s := &session{}
	g := s.cloneGate()
	if len(g.Profiles) != 3 || len(g.KeeperFiles) != 1 {
		t.Fatalf("the release clone gate %+v", g)
	}
	if err := g.Check(); err != nil {
		t.Fatalf("the release clone gate: %v", err)
	}
}
