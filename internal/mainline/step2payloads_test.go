package mainline_test

import (
	"strings"
	"testing"

	"uc-pxx-installer/internal/mainline"
)

// WithStep2Payloads (mainline-rc-5): the RAM installer's gate takes the
// reviewed STEP-2 payloads; any entry that is not a both-slot STEP-2 payload
// is left out; step 2's pins (StepTwoManifests) never name a STEP-2 payload.
func TestWithStep2Payloads(t *testing.T) {
	h := func(c string) string { return strings.Repeat(c, 64) }
	normal := mainline.Reviewed{Version: "n", ManifestSHA256: h("1"), TemplateSHA256: h("2"), RootfsSHA256: h("3"), Slots: "ab"}
	g := mainline.TestGate([]mainline.Reviewed{normal}, nil, nil)
	g2 := g.WithStep2Payloads([]mainline.Reviewed{
		{Version: "s", ManifestSHA256: h("4"), TemplateSHA256: h("2"), RootfsSHA256: h("5"), Slots: "ab", Step2: true},
		{Version: "x", ManifestSHA256: h("6"), TemplateSHA256: h("2"), RootfsSHA256: h("7"), Slots: "a", Step2: true},
		{Version: "y", ManifestSHA256: h("8"), TemplateSHA256: h("2"), RootfsSHA256: h("9"), Slots: "ab"},
	})
	if got := mainline.StepTwoManifests(g2); len(got) != 1 || got[0] != h("1") {
		t.Fatalf("step 2's pins %v", got)
	}
	if got := mainline.StepTwoManifests(g); len(got) != 1 || got[0] != h("1") {
		t.Fatalf("the first gate changed: %v", got)
	}
	if n := g2.ReviewedCount(); n != 2 {
		t.Fatalf("the installer's gate has %d payloads, want 2 (the normal one and the both-slot STEP-2 one)", n)
	}
}
