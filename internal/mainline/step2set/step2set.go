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
	// uc-pxx-step2 104e752f of mainline-rc-8 1fcc625 (ci/build-step2.sh, twice
	// byte-identical; its gate lists 0.2.0-rc8 and 0.2.0-rc8-eu): in both sets
	step2Bin = "104e752f5e658d9224301f8f08f67628b62f58e100a471ba7d346b458fa9e212"
)

// Supervised is the SUPERVISED STEP-2 payload 0.2.0-rc8-step2 of uc-pxx-linux
// rc-payload-8 876d36e (the maintainer variant plus the step-2 set; uc-pxx-step2
// 104e752f of mainline-rc-8 1fcc625), built twice byte-identical from the
// published testing packages only. Its set: r2 and uc-pxx-step2 (0700), the service and
// its conf, the supervised marker /etc/uc-pxx-step2/supervised (every step-2
// run of the unit is supervised: no countdown, no automatic finish), and the
// bootctl-inst marker (its one line names the uc-pxx-step2 sha and says
// "(supervised)").
var Supervised = mainline.Step2Payload{
	Reviewed: mainline.Reviewed{Version: "0.2.0-rc8-step2", ManifestSHA256: "98b90fe73dcebc1f54c2cdb1e2c07f2867326645404cf2e99097b814d93eef20",
		TemplateSHA256: template, RootfsSHA256: "38123e36df05eb9dbbce98ce2e22a3fbf2ff26355373921d39ee82cfce2434ed", Slots: "ab", Step2: true},
	Set: mainline.Step2Set{
		Files: []mainline.PinnedFile{r2,
			{Path: "/usr/libexec/uc-pxx-step2/uc-pxx-step2", SHA256: step2Bin},
			service, conf,
			{Path: "/etc/uc-pxx-step2/supervised", SHA256: "57272a2ad2c43331bb659b4d40dd938b1dcc35897f5cf45b0528030e6008cd5a"},
			{Path: "/etc/tsx/step2-bootctl-inst", SHA256: "9c733e92ae4ead1120faff2759e304a1928a693045557d31a3b56b5bbdb53dbc"},
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
	Reviewed: mainline.Reviewed{Version: "0.2.0-rc8-eu-step2", ManifestSHA256: "174565ff46aed8a9bba8deb036cd0c4910ecb2e3017ea10a48754c428a526045",
		TemplateSHA256: template, RootfsSHA256: "38788885d76878ff8d36a21ef311a949c9cda83037c779e5612e920bd119de71", Slots: "ab", Step2: true},
	Set: mainline.Step2Set{
		Files: []mainline.PinnedFile{r2,
			{Path: "/usr/libexec/uc-pxx-step2/uc-pxx-step2", SHA256: step2Bin},
			service, conf,
			{Path: "/etc/tsx/step2-bootctl-inst", SHA256: "7ebf4ddedd7469532d7e8ececa0eaa2c466cd8ebdf2b807545d42634c4a543e8"},
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
