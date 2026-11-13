// Package step2set is the exact step-2 set of a STEP-2 mainline payload
// (BOTH-SLOT-PACKAGING-2026-11-10 §4; PROPOSED data, mainline-rc, none of it a
// RULES pin yet). Only the RAM installer (uc-pxx-inst: step 1 puts the STEP-2
// payload into the first mainline slot) links this package: uc-pxx-step2
// never does, so the step-2 binary's own bytes do not depend on its pin, and
// step 2 never installs a STEP-2 payload (its gate has no step-2 set).
package step2set

import "uc-pxx-installer/internal/mainline"

// Files: r2 and uc-pxx-step2 at /usr/libexec/uc-pxx-step2 (0700); the OpenRC
// service (after tsx-autoupdate: STEP2-STOP-HOLD §4 item 5) and its conf;
// the supervised marker; the bootctl-inst marker (its one line names the
// uc-pxx-step2 sha, so it changes with every step-2 binary). The data of the
// STEP-2 release payload 0.2.0-rc6-step2 (uc-pxx-linux rc-payload-6 d9c4a12:
// uc-pxx-step2 fe4502b9 = ci/build-step2.sh of mainline-rc-6, whose
// gate lists the normal release payload; uc-pxx-step2 does not hold this
// package; the conf names the sealed RAM folder of the USB receive,
// UC_PXX_STEP2_USB=yes).
var Files = []mainline.PinnedFile{
	{Path: "/usr/libexec/uc-pxx-step2/uc-pxx-bootctl-inst", SHA256: "20aa6bc5d2146faace461eec0b50f741c681b3c58827788b9b806d9444b00203"},
	{Path: "/usr/libexec/uc-pxx-step2/uc-pxx-step2", SHA256: "fe4502b9cae25dc0811b35e910635a9789f44c3a5a8cf0c99f0d6d0b3c93dd5a"},
	{Path: "/etc/init.d/uc-pxx-step2", SHA256: "07e207ccba058bb25681a7bbc0c8a8b763b70e8bcba431f6473804b2128d2569"},
	{Path: "/etc/conf.d/uc-pxx-step2", SHA256: "0cfef7b58d7ededff190b39b1b44f3ef2e7d2c3ae34b2ca9c9595ceeb0c418b2"},
	{Path: "/etc/uc-pxx-step2/supervised", SHA256: "57272a2ad2c43331bb659b4d40dd938b1dcc35897f5cf45b0528030e6008cd5a"},
	{Path: "/etc/tsx/step2-bootctl-inst", SHA256: "912662958e1ff7cc9eef9cef89212888aa172d14d86e1a799fec6f73742bf6db"},
}

// Other are the folders and the runlevel link of the set (allowed by name).
var Other = []string{"/usr/libexec/uc-pxx-step2", "/etc/uc-pxx-step2", "/etc/runlevels/default/uc-pxx-step2"}

// Reviewed are the reviewed STEP-2 payloads of this release (Step2 true,
// Slots "ab"). They are here, not in the mainline package's reviewedPayloads:
// uc-pxx-step2 links that list, and the STEP-2 payload holds uc-pxx-step2
// (mainline-rc-6). The STEP-2 release payload 0.2.0-rc6-step2 of uc-pxx-linux
// d9c4a12 (the maintainer variant plus the step-2 set; uc-pxx-step2 fe4502b9),
// built twice byte-identical from the published testing packages only. The
// release review (R-D) and the owner's RULES line name it before any unit uses it.
var Reviewed = []mainline.Reviewed{
	{Version: "0.2.0-rc6-step2", ManifestSHA256: "52fff00bfeeb50597afc1419e4e7db9fdec1aa992717a8f776942ab9de23de46",
		TemplateSHA256: "0498d4d70c57ddcf54ee559b1bd104a2f863654046c09afc9254c3c905f03fc8",
		RootfsSHA256:   "6e6b1b41f08d9cc5a4ade345c280a487f84d1dc9fecad7399741d6b3f2295eaa", Slots: "ab", Step2: true},
}

// Gate is g with the step-2 set and the reviewed STEP-2 payloads: the gate of
// the RAM installer.
func Gate(g *mainline.Gate) *mainline.Gate {
	return g.WithStep2Set(Files, Other).WithStep2Payloads(Reviewed)
}
