package mainline_test

import (
	"strings"
	"testing"

	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/mainline/step2set"
)

// TestReleaseGateReal (mainline-rc-5): on the release payloads
// (RC_RELEASE_NORMAL / RC_RELEASE_STEP2 = their uc-pxx-mainline folders; not
// in the repo), the release's gate (uc-pxx-step2's gate) passes the normal
// payload for both slots and refuses the STEP-2 payload; the RAM installer's
// gate (step2set.Gate) passes both, each as the reviewed entry with its own
// triple; step 2 pins both normal manifests (the maintainer and end-user variant).
func TestReleaseGateReal(t *testing.T) {
	pn, rn := realPayload(t, "RC_RELEASE_NORMAL")
	ps, rs := realPayload(t, "RC_RELEASE_STEP2")
	release := mainline.EmbeddedGate()
	inst := step2set.Gate(mainline.EmbeddedGate())

	same := func(r *mainline.Reviewed, want mainline.Reviewed, step2 bool) bool {
		return r != nil && r.Version == want.Version && r.ManifestSHA256 == want.ManifestSHA256 &&
			r.TemplateSHA256 == want.TemplateSHA256 && r.RootfsSHA256 == want.RootfsSHA256 &&
			r.Slots == "ab" && r.Step2 == step2 && r.AllowsSlot("a") && r.AllowsSlot("b")
	}
	for _, c := range []struct {
		name  string
		g     *mainline.Gate
		p     *mainline.Payload
		want  mainline.Reviewed
		step2 bool
	}{
		{"the release's gate, the normal payload", release, pn, rn, false},
		{"the RAM installer's gate, the normal payload", inst, pn, rn, false},
		{"the RAM installer's gate, the STEP-2 payload", inst, ps, rs, true},
	} {
		r := c.g.Check(c.p)
		if !r.OK() {
			t.Fatalf("%s: refused: %v", c.name, failedChecks(r))
		}
		if !same(r.Reviewed, c.want, c.step2) {
			t.Fatalf("%s: passed as %+v, want %+v (step2 %v)", c.name, r.Reviewed, c.want, c.step2)
		}
		if r.ImageSHA256 != "85f5a417e12feabb3c8a52b246ce8b89d6f9c70d2e6994525f69ff307f13f3d7" {
			t.Fatalf("%s: Image %s", c.name, r.ImageSHA256)
		}
	}
	// uc-pxx-step2's gate does not list the STEP-2 payload (step2set.Reviewed
	// is only in the RAM installer's gate): refused at the first check
	if r := release.Check(ps); r.OK() || !strings.Contains(strings.Join(failedChecks(r), "\n"), "payload-reviewed") {
		t.Fatalf("the release's gate (uc-pxx-step2) and the STEP-2 payload: ok %v %v", r.OK(), failedChecks(r))
	}
	// mainline-rc-6: the gate lists two normal payloads (the maintainer and the
	// end-user variant), so step 2 pins both; the test's RC_RELEASE_NORMAL is
	// the maintainer one (rn), which must be among them.
	has := func(got []string, want string) bool {
		for _, g := range got {
			if g == want {
				return true
			}
		}
		return false
	}
	if got := mainline.StepTwoManifests(release); len(got) != 2 || !has(got, rn.ManifestSHA256) {
		t.Fatalf("step 2's pins %v, want two including the normal manifest %s", got, rn.ManifestSHA256)
	}
	if got := mainline.StepTwoManifests(inst); len(got) != 2 || !has(got, rn.ManifestSHA256) {
		t.Fatalf("the RAM installer's gate: step 2's pins %v", got)
	}
	// mainline-rc-7 (code review NIT-4): the maintainer payload (it has the
	// maintainer's ssh key) listed as an end-user payload is refused
	if r := mainline.WithEndUserPayloads(release, rn.ManifestSHA256).Check(pn); r.OK() ||
		!strings.Contains(strings.Join(failedChecks(r), "\n"), "an end-user payload has no ssh key") {
		t.Fatalf("the maintainer payload as an end-user one: ok %v %v", r.OK(), failedChecks(r))
	}
}

// TestReleaseGateRealEndUser (mainline-rc-7): on the end-user release payloads
// (RC_RELEASE_EU = the end-user normal folder, RC_RELEASE_EU_STEP2 = the
// end-user STEP-2 folder, RC_RELEASE_STEP2 = the supervised STEP-2 folder),
// the RAM installer's gate passes the end-user STEP-2 payload as
// step2set.EndUser; uc-pxx-step2's gate refuses it; each STEP-2 payload is
// refused against the other's step-2 set (the supervised marker is required
// in the one and refused in the other); the end-user normal payload is an
// end-user payload of the release (EndUserPayload) and the maintainer one is
// not.
func TestReleaseGateRealEndUser(t *testing.T) {
	pe, re := realPayload(t, "RC_RELEASE_EU")
	pes, res := realPayload(t, "RC_RELEASE_EU_STEP2")
	ps, rs := realPayload(t, "RC_RELEASE_STEP2")
	release := mainline.EmbeddedGate()
	inst := step2set.Gate(mainline.EmbeddedGate())
	r := inst.Check(pes)
	if !r.OK() || r.Reviewed == nil || *r.Reviewed != step2set.EndUser.Reviewed {
		t.Fatalf("the RAM installer's gate, the end-user STEP-2 payload: ok %v as %+v: %v", r.OK(), r.Reviewed, failedChecks(r))
	}
	if res.ManifestSHA256 != step2set.EndUser.Reviewed.ManifestSHA256 || rs.ManifestSHA256 != step2set.Supervised.Reviewed.ManifestSHA256 {
		t.Fatalf("the folders are not the release's STEP-2 payloads")
	}
	if r := release.Check(pes); r.OK() {
		t.Fatal("uc-pxx-step2's gate passed the end-user STEP-2 payload")
	}
	if r := inst.Check(pe); !r.OK() || r.Reviewed == nil || r.Reviewed.Step2 {
		t.Fatalf("the RAM installer's gate, the end-user normal payload: %v", failedChecks(r))
	}
	// each STEP-2 payload against the other's set
	swap := func(p mainline.Step2Payload, set mainline.Step2Set) []mainline.Step2Payload {
		p.Set = set
		return []mainline.Step2Payload{p}
	}
	for _, c := range []struct {
		name string
		p    *mainline.Payload
		v    []mainline.Step2Payload
		want string
	}{
		{"the end-user payload with the supervised set", pes, swap(step2set.EndUser, step2set.Supervised.Set), "/etc/uc-pxx-step2/supervised"},
		{"the supervised payload with the end-user set", ps, swap(step2set.Supervised, step2set.EndUser.Set), "/etc/uc-pxx-step2"},
	} {
		r := mainline.EmbeddedGate().WithStep2Variants(c.v).Check(c.p)
		if r.OK() || !strings.Contains(strings.Join(failedChecks(r), "\n"), c.want) {
			t.Fatalf("%s: ok %v: %v", c.name, r.OK(), failedChecks(r))
		}
	}
	if !mainline.EndUserPayload(release, re.ManifestSHA256) || !mainline.EndUserPayload(inst, re.ManifestSHA256) {
		t.Fatalf("the end-user normal payload %s is not an end-user payload of the release", re.ManifestSHA256)
	}
	for _, m := range mainline.StepTwoManifests(release) {
		if m != re.ManifestSHA256 && mainline.EndUserPayload(release, m) {
			t.Fatalf("%s (not the end-user payload) is an end-user payload", m)
		}
	}
	if mainline.EndUserPayload(inst, res.ManifestSHA256) || mainline.EndUserPayload(inst, rs.ManifestSHA256) {
		t.Fatal("a STEP-2 payload is an end-user (normal) payload")
	}
}
