package mainline_test

import (
	"sort"
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
	// the release gate pins both RC-6 normal payloads (the maintainer and the
	// end-user variant), sorted, each once
	wantPins := []string{
		"7271e9f10046660950b9c26bf493b17dbfd63ccd3680c82d6df02630d690a339",
		"4a850c95e9b553e0cf32f63453c673b58ba388990f0b54b621be9f49ba35626c",
	}
	sort.Strings(wantPins)
	if got := mainline.StepTwoManifests(mainline.EmbeddedGate()); strings.Join(got, ",") != strings.Join(wantPins, ",") {
		t.Fatalf("the release gate pins %v (reviewed %d)", got, mainline.EmbeddedReviewedCount())
	}
	var nilGate *mainline.Gate
	if mainline.StepTwoManifests(nilGate) != nil {
		t.Fatal("a nil gate pins something")
	}
}
