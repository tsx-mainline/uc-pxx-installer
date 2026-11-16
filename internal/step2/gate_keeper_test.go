//go:build install && step2 && !finish

package step2

import "testing"

// TestGateKeeperEmbedded: the embedded keeper pin is empty until the
// release build fills it (with the profiles of internal/clone, the clone
// gate then fails Gate.Check without it: every clone plan refuses).
func TestGateKeeperEmbedded(t *testing.T) {
	if l := gateKeeperFiles(); len(l) != 0 {
		t.Fatalf("the embedded keeper pin is %+v", l)
	}
	s := &session{}
	if g := s.cloneGate(); len(g.Profiles) != 0 || len(g.KeeperFiles) != 0 {
		t.Fatalf("the release clone gate %+v", g)
	}
}
