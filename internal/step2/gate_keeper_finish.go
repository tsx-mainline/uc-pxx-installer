//go:build install && step2 && finish

package step2

import "uc-pxx-installer/internal/clone"

// gateKeeperFiles: the finish executor plans no clone; it holds no pin of
// uc-pxx-provision (gate_keeper.go).
func gateKeeperFiles() []clone.PathPin { return nil }
