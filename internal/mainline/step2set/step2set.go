// Package step2set is the exact step-2 set of each STEP-2 mainline payload
// (BOTH-SLOT-PACKAGING-2026-11-10 §4; mainline-rc-7: one set per payload).
// Only the RAM installer (uc-pxx-inst: step 1 puts the STEP-2 payload into the
// first mainline slot) links this package: uc-pxx-step2 never does, so the
// step-2 binary's own bytes do not depend on its pin, and step 2 never
// installs a STEP-2 payload (its gate has no step-2 set).
package step2set

import "uc-pxx-installer/internal/mainline"

// The files common to both sets: r2 at /usr/libexec/uc-pxx-step2 (0700), the
// OpenRC service (after tsx-autoupdate: STEP2-STOP-HOLD §4 item 5) and its
// conf (the sealed RAM folder of the USB receive, UC_PXX_STEP2_USB=yes).
var (
	r2       = mainline.PinnedFile{Path: "/usr/libexec/uc-pxx-step2/uc-pxx-bootctl-inst", SHA256: "20aa6bc5d2146faace461eec0b50f741c681b3c58827788b9b806d9444b00203"}
	service  = mainline.PinnedFile{Path: "/etc/init.d/uc-pxx-step2", SHA256: "07e207ccba058bb25681a7bbc0c8a8b763b70e8bcba431f6473804b2128d2569"}
	conf     = mainline.PinnedFile{Path: "/etc/conf.d/uc-pxx-step2", SHA256: "0cfef7b58d7ededff190b39b1b44f3ef2e7d2c3ae34b2ca9c9595ceeb0c418b2"}
	template = "0498d4d70c57ddcf54ee559b1bd104a2f863654046c09afc9254c3c905f03fc8"
	// uc-pxx-step2 e6cf7147 of mainline-rc-9 811f1c2 (ci/build-step2.sh, twice
	// byte-identical; its gate lists 0.2.0-rc9 and 0.2.0-rc9-eu): in both sets
	step2Bin = "e6cf7147c9ef08e375246660ad5b29f54542a902312357e0c622d919162b2a68"
)

// Supervised is the SUPERVISED STEP-2 payload 0.2.0-rc9-step2 of uc-pxx-linux
// rc-payload-9 9c721e3 (the maintainer variant plus the step-2 set; uc-pxx-step2
// e6cf7147 of mainline-rc-9 811f1c2), built twice byte-identical from the
// published testing packages only. Its set: r2 and uc-pxx-step2 (0700), the service and
// its conf, the supervised marker /etc/uc-pxx-step2/supervised (every step-2
// run of the unit is supervised: no countdown, no automatic finish), and the
// bootctl-inst marker (its one line names the uc-pxx-step2 sha and says
// "(supervised)").
var Supervised = mainline.Step2Payload{
	Reviewed: mainline.Reviewed{Version: "0.2.0-rc9-step2", ManifestSHA256: "08c955e0a4356083743cbd77fa2ede4b4a0426aaef1645c84be864f15fd0228e",
		TemplateSHA256: template, RootfsSHA256: "ec268aff83c9446a8b4a318f9b287f101a2806abeb7bd43c90f4a26c554f6d3f", Slots: "ab", Step2: true},
	Set: mainline.Step2Set{
		Files: []mainline.PinnedFile{r2,
			{Path: "/usr/libexec/uc-pxx-step2/uc-pxx-step2", SHA256: step2Bin},
			service, conf,
			{Path: "/etc/uc-pxx-step2/supervised", SHA256: "57272a2ad2c43331bb659b4d40dd938b1dcc35897f5cf45b0528030e6008cd5a"},
			{Path: "/etc/tsx/step2-bootctl-inst", SHA256: "0c69bf2bc0fb9d54dc06a196ccd825c115edf9934c36bdb444741b6969bb7a41"},
		},
		Other: []string{"/usr/libexec/uc-pxx-step2", "/etc/uc-pxx-step2", "/etc/runlevels/default/uc-pxx-step2"},
	},
}

// EndUser is the END-USER STEP-2 payload (mainline-rc-7; owner Q-T5
// 2026-11-12: end-user step 2 starts by itself, AUTO-STEP2 with the 30 s
// countdown): the end-user variant (no ssh key, sshd off until the setup page
// gives root a key) plus the step-2 set WITHOUT the supervised marker (no
// /etc/uc-pxx-step2 at all), so step2ui.AutoAllowed may give the countdown.
// Its uc-pxx-step2 installs only an end-user normal payload
// (mainline.EndUserPayload) and logs every A/B read-back to the kernel log.
// The bootctl-inst marker's line says "(end user)". Built twice
// byte-identical from the published testing packages only; the release
// review (R-D) and a RULES line name it before any unit uses it.
var EndUser = mainline.Step2Payload{
	Reviewed: mainline.Reviewed{Version: "0.2.0-rc9-eu-step2", ManifestSHA256: "b4ebfe27867f6737739e51582b606bcc204fcccf4ae42ac5c521df667d7311eb",
		TemplateSHA256: template, RootfsSHA256: "bf2119f047a92126ea1cf02472d089031cd5b9fa7b4a08e2f56ca9785f65fec2", Slots: "ab", Step2: true},
	Set: mainline.Step2Set{
		Files: []mainline.PinnedFile{r2,
			{Path: "/usr/libexec/uc-pxx-step2/uc-pxx-step2", SHA256: step2Bin},
			service, conf,
			{Path: "/etc/tsx/step2-bootctl-inst", SHA256: "b78aa59f010c5164ebd70a86f3ac0c9e0d96d36a0ce042e85146514e3e9dcdb6"},
		},
		Other: []string{"/usr/libexec/uc-pxx-step2", "/etc/runlevels/default/uc-pxx-step2"},
	},
}

// Payloads are the reviewed STEP-2 payloads of this release, each with its own
// set. They are here, not in the mainline package's reviewedPayloads:
// uc-pxx-step2 links that list, and a STEP-2 payload holds uc-pxx-step2 (no
// fixed point, MAINLINE-RC4-REVIEW INFO-1).
var Payloads = []mainline.Step2Payload{Supervised, EndUser}

// Gate is g with the reviewed STEP-2 payloads, each checked against its own
// step-2 set: the gate of the RAM installer.
func Gate(g *mainline.Gate) *mainline.Gate {
	return g.WithStep2Variants(Payloads)
}
