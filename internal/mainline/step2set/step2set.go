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
// rebuilt STEP-2 payload (RC-PAYLOAD-REBUILD-2026-11-11.md §9, uc-pxx-linux
// rc-payload (after 9aebf9c): uc-pxx-step2 1cbb32ae = ci/build-step2.sh of mainline-rc
// be239e3 (the MAJOR-1 fix of MAINLINE-RC-REVIEW; uc-pxx-step2 does not hold this package)).
var Files = []mainline.PinnedFile{
	{Path: "/usr/libexec/uc-pxx-step2/uc-pxx-bootctl-inst", SHA256: "20aa6bc5d2146faace461eec0b50f741c681b3c58827788b9b806d9444b00203"},
	{Path: "/usr/libexec/uc-pxx-step2/uc-pxx-step2", SHA256: "1cbb32aeab30ffc310c5faaaf699373c69611698dfb1964fea27337b445fb378"},
	{Path: "/etc/init.d/uc-pxx-step2", SHA256: "07e207ccba058bb25681a7bbc0c8a8b763b70e8bcba431f6473804b2128d2569"},
	{Path: "/etc/conf.d/uc-pxx-step2", SHA256: "5e872840f1b5b5eeed34260f66d7c6fdc2d128e75045dcb8305ded2d7aece765"},
	{Path: "/etc/uc-pxx-step2/supervised", SHA256: "57272a2ad2c43331bb659b4d40dd938b1dcc35897f5cf45b0528030e6008cd5a"},
	{Path: "/etc/tsx/step2-bootctl-inst", SHA256: "8988dfb6e75e12d29e712fcbc93095b3206e6da69b212dca7d6f13f40d7c6208"},
}

// Other are the folders and the runlevel link of the set (allowed by name).
var Other = []string{"/usr/libexec/uc-pxx-step2", "/etc/uc-pxx-step2", "/etc/runlevels/default/uc-pxx-step2"}

// Gate is g with the step-2 set: the gate of the RAM installer.
func Gate(g *mainline.Gate) *mainline.Gate { return g.WithStep2Set(Files, Other) }
