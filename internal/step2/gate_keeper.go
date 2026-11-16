//go:build install && step2 && !finish

package step2

// The pin of uc-pxx-provision in the clone gate (Gate.KeeperFiles,
// R23-FORMATS §9.1): the release build's data, written by
// ci/r23-gate-data.py after the build of uc-pxx-provision (it cannot be in
// internal/clone: uc-pxx-provision links that package, and a pin of its own
// bytes would be a fixed point). Only the build of uc-pxx-step2 holds it; the
// finish executor gets none (gate_keeper_finish.go), so its bytes do not
// depend on it (ci/check-fixedpoint.sh).
//
//	{"keeper_files": [{"path": "/usr/libexec/uc-pxx-provision/uc-pxx-provision",
//	  "sha256": "<64 hex>", "size": N}], "kind": "uc-pxx-clone-gate-keeper", "schema": 1}
//
// Until the release build fills it, the list is empty: a gate with a profile
// then fails Gate.Check (no pin of uc-pxx-provision) and every clone plan
// refuses.

import (
	_ "embed"

	"uc-pxx-installer/internal/clone"
)

//go:embed gate-keeper.json
var gateKeeperJSON []byte

func gateKeeperFiles() []clone.PathPin {
	l, err := clone.ParseKeeper(gateKeeperJSON)
	if err != nil {
		panic("the embedded gate-keeper.json: " + err.Error())
	}
	return l
}
