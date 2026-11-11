package mainline_test

import (
	"strings"
	"testing"

	"uc-pxx-installer/internal/mainline"
)

// TestStepTwoManifests (STEP2-USB-TRANSPORT C3): the pins of the step-2 USB
// transport are the gate's reviewed normal payloads: never a STEP-2 payload,
// sorted, each once; the release gate lists none yet.
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
	if n := len(mainline.StepTwoManifests(mainline.EmbeddedGate())); n != mainline.EmbeddedReviewedCount() || n != 0 {
		t.Fatalf("the release gate pins %d (reviewed %d)", n, mainline.EmbeddedReviewedCount())
	}
	var nilGate *mainline.Gate
	if mainline.StepTwoManifests(nilGate) != nil {
		t.Fatal("a nil gate pins something")
	}
}
