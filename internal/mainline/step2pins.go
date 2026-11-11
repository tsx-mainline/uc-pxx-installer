//go:build install && step2

package mainline

import "sort"

// StepTwoManifests are the manifest sha256s of the gate's reviewed payloads
// that step 2 may install: every reviewed payload that is not a STEP-2 payload
// (step 2 installs only a normal payload). Sorted, each once. They are the
// pins of the step-2 USB transport (uc-pxx-step2 pins; STEP2-USB-TRANSPORT
// §3.4 item 2): one list, never a second hand-kept one. (A function, not a
// method: an exported method of Gate would put its name into the type data
// of every binary that holds Gate, uc-pxx-inst included. Only uc-pxx-step2
// builds this file (install && step2), so uc-pxx-inst does not change at all.)
func StepTwoManifests(g *Gate) []string {
	if g == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, r := range g.reviewed {
		if !r.Step2 && IsSHA256(r.ManifestSHA256) && !seen[r.ManifestSHA256] {
			seen[r.ManifestSHA256] = true
			out = append(out, r.ManifestSHA256)
		}
	}
	sort.Strings(out)
	return out
}
