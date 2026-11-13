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
	// uc-pxx-step2 f092d15e of mainline-rc-7 0d8b5a8 (ci/build-step2.sh, twice
	// byte-identical; its gate lists 0.2.0-rc7 and 0.2.0-rc7-eu): in both sets
	step2Bin = "f092d15e26af72cc111ab03e460b4fcbca912d4fcdfc4be08e2bf449edf20df9"
)

// Supervised is the SUPERVISED STEP-2 payload 0.2.0-rc7-step2 of uc-pxx-linux
// rc-payload-7 19844a6 (the maintainer variant plus the step-2 set; uc-pxx-step2
// f092d15e of mainline-rc-7 0d8b5a8), built twice byte-identical from the
// published testing packages only. Its set: r2 and uc-pxx-step2 (0700), the service and
// its conf, the supervised marker /etc/uc-pxx-step2/supervised (every step-2
// run of the unit is supervised: no countdown, no automatic finish), and the
// bootctl-inst marker (its one line names the uc-pxx-step2 sha and says
// "(supervised)").
var Supervised = mainline.Step2Payload{
	Reviewed: mainline.Reviewed{Version: "0.2.0-rc7-step2", ManifestSHA256: "79b1e4a8bd5f528832fb31476653bb622a3c62d5c9fd630d55815ae253632ffc",
		TemplateSHA256: template, RootfsSHA256: "acaae5535006f081e3ad89ca4144f616ccfe74d5a173133a9ddac307c9de9046", Slots: "ab", Step2: true},
	Set: mainline.Step2Set{
		Files: []mainline.PinnedFile{r2,
			{Path: "/usr/libexec/uc-pxx-step2/uc-pxx-step2", SHA256: step2Bin},
			service, conf,
			{Path: "/etc/uc-pxx-step2/supervised", SHA256: "57272a2ad2c43331bb659b4d40dd938b1dcc35897f5cf45b0528030e6008cd5a"},
			{Path: "/etc/tsx/step2-bootctl-inst", SHA256: "7e8997a7c3bb9601b597b102db747f2042b207550d730797930295c1adc7c5fe"},
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
	Reviewed: mainline.Reviewed{Version: "0.2.0-rc7-eu-step2", ManifestSHA256: "6701da2002491908257290ae911b35aeeb589c4fcb62c7575c90aafed9831de2",
		TemplateSHA256: template, RootfsSHA256: "6f417d08b45e731c51c1d648afa36b0698c4383530f1fb7881d5ffd8215bad54", Slots: "ab", Step2: true},
	Set: mainline.Step2Set{
		Files: []mainline.PinnedFile{r2,
			{Path: "/usr/libexec/uc-pxx-step2/uc-pxx-step2", SHA256: step2Bin},
			service, conf,
			{Path: "/etc/tsx/step2-bootctl-inst", SHA256: "ac961ea2a3849de47db5abb67dfbb2f23088d0151ec643e865f7e80058c19639"},
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
