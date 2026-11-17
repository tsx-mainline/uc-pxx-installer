// Package step2set is the exact step-2 set of each STEP-2 mainline payload
// (BOTH-SLOT-PACKAGING-2026-11-10 §4; mainline-rc-7: one set per payload).
// Only the RAM installer (uc-pxx-inst: step 1 puts the STEP-2 payload into the
// first mainline slot) links this package: uc-pxx-step2 never does, so the
// step-2 binary's own bytes do not depend on its pin, and step 2 never
// installs a STEP-2 payload (its gate has no step-2 set).
package step2set

import "uc-pxx-installer/internal/mainline"

// The files of the set (mainline-rc-10, R23): r2 at /usr/libexec/uc-pxx-step2
// (0700), the OpenRC service (after tsx-autoupdate and uc-pxx-provision:
// 35972b43, uc-pxx-linux r23-base) and the clone conf (UC_PXX_STEP2_KIND=clone,
// UC_PXX_STEP2_USB=yes, no payload folder: rootfs/step2/conf.d/uc-pxx-step2-clone).
var (
	r2        = mainline.PinnedFile{Path: "/usr/libexec/uc-pxx-step2/uc-pxx-bootctl-inst", SHA256: "20aa6bc5d2146faace461eec0b50f741c681b3c58827788b9b806d9444b00203"}
	service   = mainline.PinnedFile{Path: "/etc/init.d/uc-pxx-step2", SHA256: "35972b430666d7d83569dc30020e156e62f51921b4942edcd0312fbe37885ddc"}
	cloneConf = mainline.PinnedFile{Path: "/etc/conf.d/uc-pxx-step2", SHA256: "5b3402b646c8ed0db53d15ffa38364fb43db21378a2ddf491567e2257fabf424"}
	template  = "0498d4d70c57ddcf54ee559b1bd104a2f863654046c09afc9254c3c905f03fc8"
	// uc-pxx-step2 86d305ec of mainline-rc-10b 3f06cc2 (ci/build-step2.sh; its
	// clone gate holds the three 0.2.0-rc10 profiles with the RC-10b refs and the
	// pin of uc-pxx-provision 6dfc8268; its mainline gate no normal payload).
	// RC-10: 65eeed56 of 5de60c4.
	step2Bin = "86d305ecfdcb9c6c027d1420e49b6f4ba0b77079e209807a2eeb121e4f4fa2cc"
	// uc-pxx-provision 6dfc8268 of mainline-rc-10b baed193 (ci/build-provision.sh;
	// the health gate; it holds the clone gate's profiles) and the base's
	// profiles.json (rootfs/r23-refs.sh): the provisioning set's two release
	// files. RC-10: 65cd9cf1 and 449fe52a.
	provision    = mainline.PinnedFile{Path: "/usr/libexec/uc-pxx-provision/uc-pxx-provision", SHA256: "6dfc8268754e805066e92c77392540fda1721eb60521e90c03cc7fff14f54abb"}
	profilesJSON = mainline.PinnedFile{Path: "/usr/share/uc-pxx-provision/profiles.json", SHA256: "a385a7031c4eee7d6e4301a4c55e7e3e09f41b2ce64347f6984f47afd3e30cbd"}
	// the provisioning service, its conf (the base's profiles.json and
	// profile-apply.sh) and the profile steps (RC-10 release review MINOR-2)
	provInit  = mainline.PinnedFile{Path: "/etc/init.d/uc-pxx-provision", SHA256: "96be38267044abedc90c1b76be20472dc3ba1664b0eea73dbea3c6f46a4f8555"}
	provConf  = mainline.PinnedFile{Path: "/etc/conf.d/uc-pxx-provision", SHA256: "610dab9db74eaf19cc051fcccf4c40010dd2b82a5833b1ca7a2495f5dba9315b"}
	provApply = mainline.PinnedFile{Path: "/usr/share/uc-pxx-provision/profile-apply.sh", SHA256: "ae12db0ec82178976b2066972415ec5a16eee23a46e05d761162abe76e6fb22c"}
)

// EndUser is the END-USER STEP-2 payload of the release: in RC-10b the R23
// base 0.2.0-rc10b-base of uc-pxx-linux r23-base-rc10b a93819a (RC-10:
// 0.2.0-rc10-base of r23-base 2d34eed, manifest 3b2755c1) (the console system
// with the step-2 set, the USB set and the provisioning set; DESIGN-R23),
// built from the published testing packages only (snap11) in the two-stage
// order (stage 2 with --expect-refs of stage 1; one build by the author, the
// reviewer's rebuild the second). Its set: r2
// and uc-pxx-step2 (0700), the service, the clone conf, the bootctl-inst
// marker (its one line names the uc-pxx-step2 sha and says "(end user)"), no
// /etc/uc-pxx-step2 (no supervised marker: AUTO-STEP2 may give the countdown),
// uc-pxx-provision, profiles.json, the provisioning service, its conf and
// profile-apply.sh. The release review (R-D) and a RULES
// line name it before any unit uses it.
var EndUser = mainline.Step2Payload{
	Reviewed: mainline.Reviewed{Version: "0.2.0-rc10b-base", ManifestSHA256: "04f4f064608a0336bfdada9a08163d2b8187659e055ebc0496e5a9123f792839",
		TemplateSHA256: template, RootfsSHA256: "b4fa39fe09fb00475526201acbe0203a7c1c0b177886e6bea07ec8ce49e3d046", Slots: "ab", Step2: true},
	Set: mainline.Step2Set{
		Files: []mainline.PinnedFile{r2,
			{Path: "/usr/libexec/uc-pxx-step2/uc-pxx-step2", SHA256: step2Bin},
			service, cloneConf,
			{Path: "/etc/tsx/step2-bootctl-inst", SHA256: "b25e925499b43c8ceb4f3c410648378524d9b94785ae0dc6637bae9f7cd38f2b"},
			provision, profilesJSON, provInit, provConf, provApply,
		},
		Other: []string{"/usr/libexec/uc-pxx-step2", "/etc/runlevels/default/uc-pxx-step2"},
	},
}

// Payloads are the reviewed STEP-2 payloads of this release, each with its own
// set. They are here, not in the mainline package's reviewedPayloads:
// uc-pxx-step2 links that list, and a STEP-2 payload holds uc-pxx-step2 (no
// fixed point, MAINLINE-RC4-REVIEW INFO-1). mainline-rc-10: only the R23 base
// (no supervised STEP-2 payload: the RC-9 supervised and end-user STEP-2
// payloads hold the RC-9 USB set and step-2 binary).
var Payloads = []mainline.Step2Payload{EndUser}

// Gate is g with the reviewed STEP-2 payloads, each checked against its own
// step-2 set: the gate of the RAM installer.
func Gate(g *mainline.Gate) *mainline.Gate {
	return g.WithStep2Variants(Payloads)
}
