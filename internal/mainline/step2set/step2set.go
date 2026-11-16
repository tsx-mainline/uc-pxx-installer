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
	// uc-pxx-step2 65eeed56 of mainline-rc-10 5de60c4 (ci/build-step2.sh, twice
	// byte-identical; its clone gate holds the three 0.2.0-rc10 profiles and the
	// pin of uc-pxx-provision 65cd9cf1; its mainline gate no normal payload)
	step2Bin = "65eeed56e36382ed0f7a98a62881b48b082ee4f9a4a2df2329104a44b7c572c2"
	// uc-pxx-provision 65cd9cf1 of mainline-rc-10 370343f (ci/build-provision.sh,
	// twice byte-identical; it holds the clone gate's profiles) and the base's
	// profiles.json (rootfs/r23-refs.sh): the provisioning set's two release files
	provision    = mainline.PinnedFile{Path: "/usr/libexec/uc-pxx-provision/uc-pxx-provision", SHA256: "65cd9cf18db954338f7df84dd346713ec9415206d0f277d7089f0f31c3c9a390"}
	profilesJSON = mainline.PinnedFile{Path: "/usr/share/uc-pxx-provision/profiles.json", SHA256: "449fe52afdd4c84541fdddca187f28aa9f44d6b0a4142c50990be509106735cb"}
	// the provisioning service, its conf (the base's profiles.json and
	// profile-apply.sh) and the profile steps (RC-10 release review MINOR-2)
	provInit  = mainline.PinnedFile{Path: "/etc/init.d/uc-pxx-provision", SHA256: "96be38267044abedc90c1b76be20472dc3ba1664b0eea73dbea3c6f46a4f8555"}
	provConf  = mainline.PinnedFile{Path: "/etc/conf.d/uc-pxx-provision", SHA256: "610dab9db74eaf19cc051fcccf4c40010dd2b82a5833b1ca7a2495f5dba9315b"}
	provApply = mainline.PinnedFile{Path: "/usr/share/uc-pxx-provision/profile-apply.sh", SHA256: "ae12db0ec82178976b2066972415ec5a16eee23a46e05d761162abe76e6fb22c"}
)

// EndUser is the END-USER STEP-2 payload of the release: in RC-10 the R23
// base 0.2.0-rc10-base of uc-pxx-linux r23-base 2d34eed (the console system
// with the step-2 set, the USB set and the provisioning set; DESIGN-R23),
// built twice byte-identical from the published testing packages only (snap11)
// in the two-stage order (stage 2 with --expect-refs of stage 1). Its set: r2
// and uc-pxx-step2 (0700), the service, the clone conf, the bootctl-inst
// marker (its one line names the uc-pxx-step2 sha and says "(end user)"), no
// /etc/uc-pxx-step2 (no supervised marker: AUTO-STEP2 may give the countdown),
// uc-pxx-provision, profiles.json, the provisioning service, its conf and
// profile-apply.sh. The release review (R-D) and a RULES
// line name it before any unit uses it.
var EndUser = mainline.Step2Payload{
	Reviewed: mainline.Reviewed{Version: "0.2.0-rc10-base", ManifestSHA256: "3b2755c180cbd5e9dba5ba7e324229db1cf4c1d21d996a0d407f96675b1e542c",
		TemplateSHA256: template, RootfsSHA256: "e23ab3f24af13e31c468d5ee7c0a3b9d0018c68eb9126a6fbe44d23cc01f1664", Slots: "ab", Step2: true},
	Set: mainline.Step2Set{
		Files: []mainline.PinnedFile{r2,
			{Path: "/usr/libexec/uc-pxx-step2/uc-pxx-step2", SHA256: step2Bin},
			service, cloneConf,
			{Path: "/etc/tsx/step2-bootctl-inst", SHA256: "21088af0cf65ef517ad746d9850de72147787b3eba74b43f1ee43ee806bb1fe9"},
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
