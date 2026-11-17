package clone

// The clone gate of this release (DESIGN-R23 §4.2, §4.4, §4.6). The keeper's
// boot chain pins are the reviewed mainline chain that step 1 writes into B
// (RULES-TOOLS RC-5 R-G item 5 and the U2-6 baseline): the 1.0.0.94
// userdebug uboot, trust and dtbo, the patched vbmeta cebd4d8a… and the
// assembled boot 18b8a3d3… (the template 0498d4d7… with the resource
// 290ef405…).
//
// INPUTS THE RELEASE BUILD MUST SUPPLY (until then no profile is pinned and
// every clone plan refuses: check clone-profile):
//
//   - one Profile per installable system: A's build id, ref_a and ref_b
//     (the content manifests of the reference A and B, built twice), the
//     bundle sha256: gate-profiles.json (gate_release.go; written by
//     ci/r23-gate-data.py), and the pin of uc-pxx-provision: gate-keeper.json
//     of internal/step2 (the build of uc-pxx-step2 only);
//   - clone-delta.json measured against the reference trees (the file here
//     is the draft of DESIGN-R23 §4.4, pinned by its sha256 below);
//   - the exclusion list measured by the qemu boot of the reference B (the
//     list here is the draft of DESIGN-R23 §4.6).

import (
	_ "embed"
	"fmt"
)

//go:embed clone-delta.json
var deltaJSON []byte

// DeltaPin is the sha256 of the embedded clone-delta.json.
const DeltaPin = "d3044ca39279c866d81543edf70d9d80e8a3220ac41df84ce7d71fbca44a556d"

// exclude is the draft exclusion list (DESIGN-R23 §4.6), plus the mount
// points of the running system (a live walk does not see the folder under a
// mount) and /lost+found (made by mke2fs, not by the tree).
var exclude = []string{
	"/dev", "/proc", "/sys", "/run", "/tmp", "/lost+found",
	"/etc/machine-id", "/etc/ssh/ssh_host_*", "/var/log", "/var/cache", "/var/lib/*", "!/var/lib/tsx", "/root", "/data",
	"**/__pycache__",
	"/etc/hostname", "/etc/hosts", "/etc/shadow", "/etc/shadow-", "/etc/apk/repositories", "/etc/runlevels/default/sshd",
	"/usr/share/uc-pxx-provision/profiles.json",
	// the gate's own program and the binary built with it: checked by their
	// own pins (KeeperFiles; uc-pxx-step2 by step 1 and selfCheck), not by
	// ref_b (R23-FORMATS §9.1)
	"/usr/libexec/uc-pxx-step2", "/usr/libexec/uc-pxx-provision",
	// the step-2 marker names the uc-pxx-step2 sha256 (R23-BASE-REVIEW
	// MAJOR-2: the same cycle): checked by step 1's pin (the installer's
	// step-2 set) and by the plan (step2 keeperFiles: one line that names
	// the running program's sha256)
	Step2Marker,
	// the per-unit files that the boot writes, found by a search for the
	// writes in tsx-linux-common 75c3169 (tsx-config: boot-verbose,
	// orientation, shadow, apk/repositories; tsx-hostname: hostname, hosts;
	// tsx-setup: localtime, timezone; tsx-banner: issue, motd;
	// tsx-autoupdate and kiosk: crontabs/root; udhcpc: resolv.conf; apk:
	// db/lock). tsx-config's own overrides go to /run/tsx/*.conf: the
	// /etc/tsx/*.conf defaults (autoupdate.conf among them: MODE=manual up
	// to RC-10b, no MODE line from RC-10c on, so tsx-autoupdate's default
	// mode auto) stay in the content check. Exact paths; TO CONFIRM ON UNIT 2
	"/etc/tsx/boot-verbose", "/etc/tsx/orientation", "/etc/issue", "/etc/motd", "/etc/timezone", "/etc/localtime",
	"/etc/crontabs/root", "/etc/resolv.conf", "/lib/apk/db/lock",
	// a home written by a profile's service if B ever runs it (clone review
	// NOTE-8; data-migrate carries /home)
	"/home",
}

// EmbeddedGate is the release's clone gate.
func EmbeddedGate() *Gate {
	d, err := ParseDelta(deltaJSON)
	if err != nil || SHA256(deltaJSON) != DeltaPin {
		panic(fmt.Sprintf("the embedded clone-delta.json (sha256 %s, pin %s): %v", SHA256(deltaJSON), DeltaPin, err))
	}
	return &Gate{Name: "embedded",
		Chain: []ChainPin{
			{Part: "uboot", Size: 4194304, SHA256: "c6e80c1590a44c9abd5756b8553787b84e096563f3726118949c44d1fe8d798c"},
			{Part: "trust", Size: 4194304, SHA256: "a5ab4d9c5756b5b7f3e0860269207119733b4bc84e8d64600be9671ebb84a562"},
			{Part: "dtbo", Size: 4194304, SHA256: "83b3e6321c43960bc4215635550a405b297ed965d7fc6ec548bb2b02d9d93629"},
			{Part: "vbmeta", Size: 8192, SHA256: "cebd4d8a68c8861b9801c4d83a14ab3d06f1d85b015a01686fae2d305a69f35e"},
			{Part: "boot", Size: 40777728, SHA256: "18b8a3d3b54360e90fa817ce6811c273b914f0494c5b0e79e621d796ed3d3a20"},
		},
		Profiles:      append([]Profile(nil), Release().Profiles...), // gate-profiles.json (the release build)
		Delta:         d,
		DeltaJSON:     append([]byte(nil), deltaJSON...),
		Exclude:       append([]string(nil), exclude...),
		Forbidden:     []Pin{{What: "uc-pxx-bootctl-inst r2", Size: 777976, SHA256: "20aa6bc5d2146faace461eec0b50f741c681b3c58827788b9b806d9444b00203"}},
		ForbiddenFrom: []string{"/usr/libexec/uc-pxx-step2/uc-pxx-step2", "/usr/libexec/uc-pxx-provision/uc-pxx-provision"},
		FreezeBound:   FreezeBound,
		KeeperFiles:   nil, // the uc-pxx-provision pin: internal/step2 gate-keeper.json (uc-pxx-step2 only)
	}
}
