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

// WithStep2Variants (mainline-rc-7): each STEP-2 payload gets its own set; an
// entry that is not a both-slot STEP-2 payload with a sha256 manifest (a
// placeholder) is left out; step 2's pins never name a STEP-2 payload.
// EndUserPayload: only a listed, reviewed, normal manifest.
func TestWithStep2Variants(t *testing.T) {
	h := func(c string) string { return strings.Repeat(c, 64) }
	normal := mainline.Reviewed{Version: "n", ManifestSHA256: h("1"), TemplateSHA256: h("2"), RootfsSHA256: h("3"), Slots: "ab"}
	eu := mainline.Reviewed{Version: "e", ManifestSHA256: h("a"), TemplateSHA256: h("2"), RootfsSHA256: h("b"), Slots: "ab"}
	g := mainline.TestGate([]mainline.Reviewed{normal, eu}, nil, nil)
	set := mainline.Step2Set{Files: []mainline.PinnedFile{{Path: "/x", SHA256: h("c")}}, Other: []string{"/y"}}
	g2 := g.WithStep2Variants([]mainline.Step2Payload{
		{Reviewed: mainline.Reviewed{Version: "s", ManifestSHA256: h("4"), TemplateSHA256: h("2"), RootfsSHA256: h("5"), Slots: "ab", Step2: true}, Set: set},
		{Reviewed: mainline.Reviewed{Version: "p", ManifestSHA256: "EU_STEP2_MANIFEST", TemplateSHA256: h("2"), RootfsSHA256: h("5"), Slots: "ab", Step2: true}, Set: set},
		{Reviewed: mainline.Reviewed{Version: "x", ManifestSHA256: h("6"), TemplateSHA256: h("2"), RootfsSHA256: h("7"), Slots: "a", Step2: true}, Set: set},
		{Reviewed: mainline.Reviewed{Version: "y", ManifestSHA256: h("8"), TemplateSHA256: h("2"), RootfsSHA256: h("9"), Slots: "ab"}, Set: set},
	})
	if n := g2.ReviewedCount(); n != 3 {
		t.Fatalf("the installer's gate has %d payloads, want 3 (two normal ones and the one both-slot STEP-2 one)", n)
	}
	if got := mainline.StepTwoManifests(g2); len(got) != 2 || got[0] != h("1") || got[1] != h("a") {
		t.Fatalf("step 2's pins %v", got)
	}
	if mainline.EndUserPayload(g2, h("a")) {
		t.Fatal("an unlisted payload is an end-user payload")
	}
	g3 := mainline.WithEndUserPayloads(g2, h("a"), h("4"), h("f"))
	if !mainline.EndUserPayload(g3, h("a")) || mainline.EndUserPayload(g3, h("1")) {
		t.Fatal("EndUserPayload: the listed normal payload only")
	}
	if mainline.EndUserPayload(g3, h("4")) || mainline.EndUserPayload(g3, h("f")) || mainline.EndUserPayload(nil, h("a")) {
		t.Fatal("EndUserPayload: a STEP-2 payload, an unreviewed one or no gate")
	}
	if !mainline.EndUserPayload(mainline.EmbeddedGate(), "62acc9ffeaeeb60b045f5f6ef221250fab7feffb30935f19cf0aff7e6355c222") ||
		mainline.EndUserPayload(mainline.EmbeddedGate(), "b5df32c78cd0c5673f3e69ca9375d5b11d15cd1d7a70ca343d3421ef6f87dbe0") {
		t.Fatal("the release: 0.2.0-rc6-eu is the end-user payload, 0.2.0-rc6 is not")
	}
}
