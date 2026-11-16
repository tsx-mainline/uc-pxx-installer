package mainline_test

import (
	"strings"
	"testing"

	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/mainline/step2set"
)

// TestReleaseGateRealBase (mainline-rc-10, R23): on the release's R23 base
// (RC_RELEASE_BASE = a folder with its manifest.json, boot-template.img and the
// unpacked rootfs.ext4, the form the drive read and the step-1 receiver give
// the children; not in the repo), the RAM installer's gate (step2set.Gate)
// passes it as step2set.EndUser with its own step-2 set (the Linux Image of the
// both-slot kernel); uc-pxx-step2's gate (the release's mainline gate) refuses
// it at the first check; the base with a supervised marker in its set is
// refused; the release has no normal payload, so step 2 pins none and the
// step-1 receiver's bundle pins are exactly the base's manifest.
func TestReleaseGateRealBase(t *testing.T) {
	pb, rb := realPayload(t, "RC_RELEASE_BASE")
	release := mainline.EmbeddedGate()
	inst := step2set.Gate(mainline.EmbeddedGate())
	r := inst.Check(pb)
	if !r.OK() || r.Reviewed == nil || *r.Reviewed != step2set.EndUser.Reviewed {
		t.Fatalf("the RAM installer's gate, the base: ok %v as %+v: %v", r.OK(), r.Reviewed, failedChecks(r))
	}
	if rb.ManifestSHA256 != step2set.EndUser.Reviewed.ManifestSHA256 || rb.RootfsSHA256 != step2set.EndUser.Reviewed.RootfsSHA256 ||
		rb.TemplateSHA256 != step2set.EndUser.Reviewed.TemplateSHA256 {
		t.Fatalf("the folder is not the release's base: %+v", rb)
	}
	if r.ImageSHA256 != "85f5a417e12feabb3c8a52b246ce8b89d6f9c70d2e6994525f69ff307f13f3d7" {
		t.Fatalf("the base's Image %s", r.ImageSHA256)
	}
	if r := release.Check(pb); r.OK() || !strings.Contains(strings.Join(failedChecks(r), "\n"), "payload-reviewed") {
		t.Fatalf("the release's gate (uc-pxx-step2) and the base: ok %v %v", r.OK(), failedChecks(r))
	}
	sup := step2set.EndUser
	sup.Set.Files = append(append([]mainline.PinnedFile(nil), sup.Set.Files...),
		mainline.PinnedFile{Path: "/etc/uc-pxx-step2/supervised", SHA256: "57272a2ad2c43331bb659b4d40dd938b1dcc35897f5cf45b0528030e6008cd5a"})
	if r := mainline.EmbeddedGate().WithStep2Variants([]mainline.Step2Payload{sup}).Check(pb); r.OK() ||
		!strings.Contains(strings.Join(failedChecks(r), "\n"), "/etc/uc-pxx-step2/supervised") {
		t.Fatalf("the base with the supervised set: ok %v %v", r.OK(), failedChecks(r))
	}
	if got := mainline.StepTwoManifests(release); len(got) != 0 {
		t.Fatalf("step 2's pins %v, want none", got)
	}
	if got := mainline.GateManifests(inst); len(got) != 1 || got[0] != rb.ManifestSHA256 {
		t.Fatalf("the step-1 bundle pins %v, want the base %s", got, rb.ManifestSHA256)
	}
	if mainline.EndUserPayload(inst, rb.ManifestSHA256) {
		t.Fatal("the base (a STEP-2 payload) is an end-user normal payload")
	}
}
