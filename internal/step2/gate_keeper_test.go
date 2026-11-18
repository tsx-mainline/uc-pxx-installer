//go:build install && step2 && !finish

package step2

import "testing"

// TestGateKeeperEmbedded (mainline-rc-10 release data): the embedded keeper
// pin is the release's uc-pxx-provision (66495eb3…, 6946940 bytes, mainline-rc-10d; built at
// 2404651 after gate-profiles.json), and the release clone gate of this
// program (the profiles of internal/clone with that pin) passes Gate.Check:
// a clone plan does not refuse for an empty gate.
func TestGateKeeperEmbedded(t *testing.T) {
	l := gateKeeperFiles()
	if len(l) != 1 || l[0].Path != "/usr/libexec/uc-pxx-provision/uc-pxx-provision" || l[0].Size != 6946940 ||
		l[0].SHA256 != "66495eb32b7179350dc2d46dbffd179cc5a3c764961433f02ca1f4cb1b3648b1" {
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
