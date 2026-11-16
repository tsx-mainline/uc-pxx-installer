//go:build install && step2 && !finish

package step2

import "testing"

// TestGateKeeperEmbedded (mainline-rc-10 release data): the embedded keeper
// pin is the release's uc-pxx-provision (65cd9cf1…, 6946940 bytes, built at
// 370343f after gate-profiles.json), and the release clone gate of this
// program (the profiles of internal/clone with that pin) passes Gate.Check:
// a clone plan does not refuse for an empty gate.
func TestGateKeeperEmbedded(t *testing.T) {
	l := gateKeeperFiles()
	if len(l) != 1 || l[0].Path != "/usr/libexec/uc-pxx-provision/uc-pxx-provision" || l[0].Size != 6946940 ||
		l[0].SHA256 != "65cd9cf18db954338f7df84dd346713ec9415206d0f277d7089f0f31c3c9a390" {
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
