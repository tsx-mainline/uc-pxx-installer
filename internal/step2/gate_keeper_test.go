//go:build install && step2 && !finish

package step2

import "testing"

// TestGateKeeperEmbedded (mainline-rc-10 release data): the embedded keeper
// pin is the release's uc-pxx-provision (6dfc8268…, 6946940 bytes, mainline-rc-10b; built at
// baed193 after gate-profiles.json), and the release clone gate of this
// program (the profiles of internal/clone with that pin) passes Gate.Check:
// a clone plan does not refuse for an empty gate.
func TestGateKeeperEmbedded(t *testing.T) {
	l := gateKeeperFiles()
	if len(l) != 1 || l[0].Path != "/usr/libexec/uc-pxx-provision/uc-pxx-provision" || l[0].Size != 6946940 ||
		l[0].SHA256 != "6dfc8268754e805066e92c77392540fda1721eb60521e90c03cc7fff14f54abb" {
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
