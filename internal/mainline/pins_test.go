package mainline_test

import (
	"strings"
	"testing"

	"uc-pxx-installer/internal/mainline"
)

// TestStepTwoManifests (STEP2-USB-TRANSPORT C3): the pins of the step-2 USB
// transport are the gate's reviewed normal payloads: never a STEP-2 payload,
// sorted, each once; the release gate pins the normal release payload.
func TestStepTwoManifests(t *testing.T) {
	a, b, c := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	g := mainline.TestGate([]mainline.Reviewed{
		{Version: "n2", ManifestSHA256: b, Slots: "ab"},
		{Version: "s", ManifestSHA256: c, Slots: "ab", Step2: true},
		{Version: "n1", ManifestSHA256: a, Slots: "a"},
		{Version: "n2 again", ManifestSHA256: b, Slots: "ab"},
		{Version: "bad", ManifestSHA256: "not a sha", Slots: "ab"},
	}, nil, nil)
	got := mainline.StepTwoManifests(g)
	if strings.Join(got, ",") != a+","+b {
		t.Fatalf("pins %v", got)
	}
	if got := mainline.StepTwoManifests(mainline.EmbeddedGate()); len(got) != 1 || got[0] != "31f19e795080e2848687e64a612f69510dca32e4146fed93f71a958e961b114c" {
		t.Fatalf("the release gate pins %v (reviewed %d)", got, mainline.EmbeddedReviewedCount())
	}
	var nilGate *mainline.Gate
	if mainline.StepTwoManifests(nilGate) != nil {
		t.Fatal("a nil gate pins something")
	}
}
