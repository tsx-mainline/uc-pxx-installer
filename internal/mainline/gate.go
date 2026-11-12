package mainline

// The payload gate (the review of c0fb5c5, finding 1): a mainline bundle is
// bound only by its own manifest, which nobody signs. A self-consistent
// bundle could carry any kernel (one with cpufreq on: the known memory
// corruption) or a root file system whose boot-control tools are not the
// pinned ones and write the A/B sector on the first boot. So a mainline
// payload is installed only when the release lists it as reviewed, as an
// official firmware is installed only when a reviewed manifest lists it:
//
//   - the reviewed payloads (reviewed): the sha256 of manifest.json,
//     boot-template.img and rootfs.ext4, all three;
//   - the reviewed kernels (kernels): the kernel pin of the manifest maps to
//     the sha256 of the Linux Image in the template (the kernel part from
//     ImageOffset on: kernel/mkimage.sh puts the Image there), so kernel.rev
//     is not only a string;
//   - the boot-control files of the root file system (bootControl): the
//     slot-side files that RULES.md pins by sha256 for the mainline slot
//     (bootControlPins: the boot control of the uc-pxx port, its updaters,
//     the tsx-autoupdate board step and its callers) must each be in
//     rootfs.ext4 with exactly that sha256; the list that uc-pxx-post-update
//     checks (/usr/share/uc-pxx/boot-control.sha256; RULES names it, with no
//     sha256 of its own) must give the pinned sha256 for each pinned file it
//     names and name uc-pxx-post-update; and no file named
//     uc-pxx-bootctl-inst may be anywhere in the tree (RULES: never in
//     system_a). All read with internal/ext4, in place;
//   - the patched vbmeta of slot A (vbmetaPatches, contract v2): the
//     manifest's vbmeta_patched must be a reviewed patch (vbmeta.go:
//     ReviewedVbmetaPatches, the source sha256, the size, the field, the old
//     and the new boot image_size and the result sha256). The plan makes the
//     patch again from vbmeta_b and must get exactly that patch.
//
// The release's gate (EmbeddedGate) has no reviewed payload and no reviewed
// kernel yet: it refuses every bundle until a reviewed payload is added here
// by a reviewed change. Tests and the devtest build use TestGate; a release
// build does not hold it (ci/check-no-writer.py checks the binary and the
// sources).

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"sort"
	"strings"

	"uc-pxx-installer/internal/ext4"
)

// ImageOffset is where kernel/mkimage.sh puts the Linux Image in the kernel
// part of the boot image (after the shim stub and its device tree).
const ImageOffset = 0x200000

// Reviewed is one reviewed mainline payload.
type Reviewed struct {
	Version        string `json:"version"`
	ManifestSHA256 string `json:"manifest_sha256"`
	TemplateSHA256 string `json:"template_sha256"`
	RootfsSHA256   string `json:"rootfs_sha256"`
	// Slots are the slots this payload may be installed into (task-3 review
	// MAJOR-1): "" or "a" = slot A only (the slot-A-only boot control of
	// the RULES pins: its initramfs takes system_a as root and its updaters
	// write slot A); "ab" = both slots (a both-slot W22 build). A payload
	// for slot B passes a gate that checks the boot-control files only with
	// the both-slot pins (bootControlBothPins).
	Slots string `json:"slots,omitempty"`
	// Step2 marks a STEP-2 payload of a both-slot release (MAINLINE-STEP2-REST,
	// BOTH-SLOT-PACKAGING §4): only its tree may hold the step-2 set
	// (BothSlot.Step2: uc-pxx-step2, uc-pxx-bootctl-inst r2 at
	// /usr/libexec/uc-pxx-step2/, the service and its markers), exactly.
	Step2 bool `json:"step2,omitempty"`
}

// AllowsSlot reports whether the payload may be installed into slot s.
func (r *Reviewed) AllowsSlot(s string) bool {
	switch {
	case r == nil:
		return false
	case r.Slots == "" || r.Slots == "a":
		return s == "a"
	case r.Slots == "ab":
		return s == "a" || s == "b"
	}
	return false
}

// PinnedFile is a file of the root file system with its reviewed sha256.
type PinnedFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// reviewedPayloads are the reviewed mainline payloads of this release. Empty:
// no mainline payload is reviewed yet (MAINLINE-INSTALL-PLAN.md phases 6-7).
// A payload is added here only with an Opus review and an owner line.
var reviewedPayloads = []Reviewed{}

// reviewedKernels maps a reviewed kernel pin (Manifest.Kernel.Rev) to the
// sha256 of its Linux Image (the template's kernel part from ImageOffset on).
// Empty: the Image of d02e9263 is pinned with the first reviewed payload.
// (A function, not a map variable: a package-level map is made by the
// package's init code, which every binary that imports this package links;
// mainline-rc-4: the bytes of uc-pxx-xfer and uc-pxx-step2-finish must not
// depend on the gate's data, see gateDataNote.)
func reviewedKernels() map[string]string { return map[string]string{} }

// bootControlPins are the slot-side files of the mainline root file system
// that RULES.md pins by sha256: the uc-pxx port of the automatic boot control
// (BOOTCTL-REVIEW.md, BOOT-OK-STATUS.md), uc-pxx-update-boot
// (UPDATE-BOOT-REVIEW.md), and the tsx-autoupdate board step with its
// callers (POST-UPDATE-REVIEW.md: uc-pxx-post-update, board.sh,
// tsx-autoupdate, tsx-panelctl). A new sha256 of any of them needs its own
// review and this change. (The status-only and installer images, the
// kernel/mkimage.sh of the build host and uc-pxx-bootctl-inst are not files
// of system_a; bootctl-inst must not be one: noBootctlInst.)
var bootControlPins = []PinnedFile{
	{"/usr/sbin/uc-pxx-bootctl", "38fea6d32412e7977ebc5845b138dc1c8c2df8555dae0b64b47f8a851b45e2d7"},
	{"/etc/uc-pxx-mark-boot.sh", "b60c9ef3aa29a7d873e890875217e78ce9bb9826818265d7bf3e22152bb69a5d"},
	{"/etc/init.d/uc-pxx-boot-ok", "15d7588c6f6dd9bea3bb8fbccf4e4d4c8986c7e20973f806c71ba530e78f59f4"},
	{"/usr/sbin/uc-pxx-flash-boot", "400d218b6fb83943974d6dadbb62df20cb8f75c470d7cca0f1910e8d5b46878a"},
	{"/usr/sbin/uc-pxx-arm-slot", "f5f60db87d32149ac10c83e63852b2f78019fd5140cd8991579df1e8ed4d8dcd"},
	{"/usr/sbin/uc-pxx-post-update", "a512a311af2ddfa8a08be74cb19c7aebee8d4de773f59e5c98885dc3924836f3"},
	{"/usr/sbin/uc-pxx-update-boot", "4de04b78b8a48a4cf4d79f22f18f2f69a6932a36398e4ba6ec0ac175483b3703"},
	{"/usr/local/lib/tsx/board.sh", "15a610e7079ddad643bdacc0f744f348bef0ee965d2fb4e6d912e848f6e1ced2"},
	{"/usr/local/sbin/tsx-autoupdate", "11b50dd3a8fc2d40cf9a56339ee0dfd9af9ab3d4a112128ed454861c52eb92b0"},
	{"/usr/local/sbin/tsx-panelctl", "fff41e69850f103b67d818cd321c2f880882f9908105ab25bbb8cc987eba7306"},
}

// The both-slot ("ab") rules (BOTH-SLOT-PACKAGING-2026-11-10.md §6 with the
// review's m2; PROPOSED data, mainline-rc 2026-11-11: none of these shas is a
// RULES pin yet; they are the files of the DEV both-slot payloads of
// uc-pxx-linux both-slot-packaging ff69bf6 / tsx-aports uc-pxx-both-slots
// ee01a73, review BOTH-SLOT-PACKAGING-REVIEW.md GO-with-fixes). A payload
// reviewed for slot B must carry every one of bootControlBothPins with its
// sha256, its boot-control list must name exactly the eight boot tools, its
// Linux Image must be a both-slot Image (reviewedBothSlotKernels), its
// /etc/apk/keys must be exactly bothSlotAPKKeys, no DEV marker may be in it,
// and the step-2 set only in a Step2 payload, exactly.

// bothSlotBootTools are the eight files of the both-slot uc-pxx-boot-tools
// 0.2.0-r1 (`_pins`; /usr/share/uc-pxx/boot-control.sha256 lists exactly
// these paths: bothSlotListPaths). The mark hook is 726d5675 (owner Q-T7,
// MAINLINE-RC3-2026-11-11.md: the boot is marked on generic health only:
// uptime, the OpenRC default runlevel finished, no oops; uc-pxx-linux
// rc-payload-3 bb130e9, tsx-aports rc-mark-health 997ce86); it replaces
// fe581cf2 (0.2.0-r0).
var bothSlotBootTools = []PinnedFile{
	{"/usr/sbin/uc-pxx-bootctl", "11c2afcd0272f9c989f9825074186a2341d0a33a34e0fe9ebf92cf0081f9efa7"},
	{"/etc/uc-pxx-mark-boot.sh", "726d567556b1386bee4d60a5052e28fec02676f4c044f717ea096a8ce1e7e916"},
	{"/etc/init.d/uc-pxx-boot-ok", "15d7588c6f6dd9bea3bb8fbccf4e4d4c8986c7e20973f806c71ba530e78f59f4"},
	{"/usr/sbin/uc-pxx-flash-boot", "2fad02b3d825706affabac71c45a2a04d2143970dcf014c1586593c11dfd6fee"},
	{"/usr/sbin/uc-pxx-arm-slot", "26c3f1c0a375674f8a80b2536ed6765f85ae6b13724770767039a271650f786e"},
	{"/usr/sbin/uc-pxx-update-boot", "4de04b78b8a48a4cf4d79f22f18f2f69a6932a36398e4ba6ec0ac175483b3703"},
	{"/usr/sbin/uc-pxx-post-update", "a512a311af2ddfa8a08be74cb19c7aebee8d4de773f59e5c98885dc3924836f3"},
	{"/usr/sbin/uc-pxx-slot", "cfec605c6d4042b36aa9882efa3bf8ee1f5a186ad3a06b07a7cc351b796abe2e"},
}

// bothSlotAutoupdate is the tsx-autoupdate set of the both-slot payload
// (the F1 fix: tsx-autoupdate, conf.sh, the defaults; tsx-panelctl). It is
// one list so that its swap (the STOP hold of tsx-linux-common,
// STEP2-STOP-HOLD-2026-11-11.md) is one data change of these four lines.
//
// The STOP-hold tsx-autoupdate 40c91350 and tsx-panelctl c4ebec62 of
// tsx-linux-common step2-stop-hold 7aafeb2 (STEP2-STOP-HOLD-REVIEW.md re-look
// GO 2026-11-11; conf.sh and the defaults unchanged): step 2's durable STOP
// (internal/step2 stopHold) needs this tsx-autoupdate. The rebuilt DEV
// payloads (RC-PAYLOAD-REBUILD-2026-11-11.md) hold them. PROPOSED data.
var bothSlotAutoupdate = []PinnedFile{
	{"/usr/local/sbin/tsx-autoupdate", "40c913500d29e26601fbb6d14ac9feb2b8b200b9b3b2a2c2d5d69c42180391ab"},
	{"/usr/local/lib/tsx/conf.sh", "22f631e199a6232b9e9f54f6ac35c42f1ded260e9121b0c708e4deef66785692"},
	{"/usr/local/share/tsx/defaults/autoupdate.conf", "24b59e0f82833de669cd1edea5aebf8e04611473a41838e4a3638cdeafc84757"},
	{"/usr/local/sbin/tsx-panelctl", "c4ebec627795ac9a374cd992b52147bd0543cd608224b2395a504b649aece3b5"},
}

// bothSlotBoard are the other both-slot files: the board step, the list
// itself, the userdata writers (R-F.2) and the /data mount (F7, a
// correctness condition of a slot-B boot: required, not optional).
var bothSlotBoard = []PinnedFile{
	{"/usr/local/lib/tsx/board.sh", "15a610e7079ddad643bdacc0f744f348bef0ee965d2fb4e6d912e848f6e1ced2"},
	{"/usr/share/uc-pxx/boot-control.sha256", "bca98ff6ebcf2ca55bc183a64216e61b73ee5a7030366e11848faf3a8a25ad3e"},
	{"/usr/sbin/uc-pxx-data-migrate", "9162c65df08ac5577e38a2fe124d8cb1434b9b2bcfe787d6fba76055b8006791"},
	{"/usr/sbin/uc-pxx-data-release", "0e94edad81775245c704153abf7bb0469ed88e9638324b9d798a2312539d853b"},
	{"/etc/init.d/uc-pxx-lvm", "8f8632da42376defd3eabc5b11b5dd1d9121c97451e05feaa40b593c761eb4aa"},
	{"/etc/fstab", "d4d8367497849ba3c7773fe229b422a1eb7b42d5799389fcbd75796b83a1ad2e"},
}

// bothSlotUSB is the USB set of every both-slot payload, normal and STEP-2
// (owner decisions 2026-11-11, HANDOFF "USB interfaces in every mainline
// build"; MAINLINE-RC4-2026-11-11.md): the WebUSB vendor interface of the
// step-2 transport made permanent. uc-pxx-xfer (no eMMC code:
// ci/check-no-writer.py --xfer), the OpenRC service uc-pxx-usb that runs it
// (status always; in a normal build the receive of the pinned finish
// executor while this slot is an unfinished step-2 target), its conf file
// (the USB network + ssh setting, OFF), the slot's own pinned list of the
// finish executor (finish.json: uc-pxx-step2-finish 9c88a340… 3866748 bytes and
// uc-pxx-bootctl-inst r2 20aa6bc5… 777976 bytes), and the sshd rule "a key
// only on the USB network address". uc-pxx-xfer and uc-pxx-step2-finish are
// the release builds of mainline-rc-5 5d5082f (ci/build-xfer.sh,
// ci/build-step2.sh; each twice byte-identical; ci/check-fixedpoint.sh).
// PROPOSED data of the DEV payloads of uc-pxx-linux rc-payload-4; none of
// these shas is a RULES pin.
var bothSlotUSB = []PinnedFile{
	{"/usr/libexec/uc-pxx-xfer/uc-pxx-xfer", "42d764e0da7b5871de9980e1205c66769e0092f8a63d53a8b9353a93831c952f"},
	{"/etc/init.d/uc-pxx-usb", "3bd80e76654359ef605f01ad7cbb24e687cc95da2df1285581e32427667cc9da"},
	{"/etc/conf.d/uc-pxx-usb", "66eddca6f6c58b3839ec57c45f8895dc217810a853b73288de03186618cce2ca"},
	{"/usr/share/uc-pxx-usb/finish.json", "05414bbf5a7e3e71b7acd3403f13bbc8071e6b64217e211f363bf04272b48ce1"},
	{"/etc/ssh/sshd_config.d/uc-pxx-usb.conf", "ad2fcc1ba531c2cbc72bedd2bffd72160c292484f817c5b81d3900616eaa83c9"},
}

// bothSlotUSBOther are the folders and the runlevel link of the USB set.
var bothSlotUSBOther = []string{"/usr/libexec/uc-pxx-xfer", "/usr/share/uc-pxx-usb", "/etc/runlevels/default/uc-pxx-usb"}

// gateDataNote: no data of this file may be made by init code (a map
// literal, a call such as concatPins in a variable's initializer). A
// statically initialized variable that only the gate's functions reach is
// left out of a binary that never calls them; init code would put it into
// every binary that imports this package. uc-pxx-xfer (manifest parser) and
// uc-pxx-step2-finish (the finish planner) import it, and the gate pins
// them (bothSlotUSB, finish.json): their bytes must not depend on the pins,
// or no pin could exist (a fixed point). ci/check-fixedpoint.sh builds both
// with this file's data changed and requires the same bytes.

// forbiddenPairs never ship together (BOOTCTL-11c2afcd-REVIEW (2), measured):
// the both-slot mark hook with the slot-A uc-pxx-bootctl (its --expect b
// checks boot_a). 11c2afcd goes only into "ab" payloads (bootControlBothPins),
// 38fea6d3 only into slot-A payloads and the initramfs (bootControlPins).
// Both both-slot hooks (fe581cf2 of 0.2.0-r0 and 726d5675 of 0.2.0-r1) are
// forbidden with 38fea6d3.
var forbiddenPairs = [][2]PinnedFile{
	{{"/etc/uc-pxx-mark-boot.sh", "fe581cf2212d8bc01ff54d1b88dc7d03601af075f56e74fb0d3edf2553a08056"},
		{"/usr/sbin/uc-pxx-bootctl", "38fea6d32412e7977ebc5845b138dc1c8c2df8555dae0b64b47f8a851b45e2d7"}},
	{{"/etc/uc-pxx-mark-boot.sh", "726d567556b1386bee4d60a5052e28fec02676f4c044f717ea096a8ce1e7e916"},
		{"/usr/sbin/uc-pxx-bootctl", "38fea6d32412e7977ebc5845b138dc1c8c2df8555dae0b64b47f8a851b45e2d7"}},
}

// bootControlBothPins are all the pins of the both-slot boot control (W22:
// MAINLINE-STEP2-DESIGN.md §10 L1-L5, uc-pxx-slot included). The initramfs
// files (/init adfb366d…, uc-pxx-findfs, uc-pxx-install-rootfs, …) are in
// the Linux Image, which reviewedBothSlotKernels pins whole.
//
// A function (gateDataNote): a variable made by concatPins would be init code.
// testBothPins (nil in a release) replaces the list in a test (SetBothPins).
func bootControlBothPins() []PinnedFile {
	if testBothPins != nil {
		return append([]PinnedFile(nil), (*testBothPins)...)
	}
	return concatPins(bothSlotBootTools, bothSlotAutoupdate, bothSlotBoard, bothSlotUSB)
}

var testBothPins *[]PinnedFile

// bothSlotListPaths: the path set of /usr/share/uc-pxx/boot-control.sha256
// in a both-slot payload (exactly the eight boot tools).
func bothSlotListPaths() []string { return pathsOf(bothSlotBootTools) }

// reviewedBothSlotKernels maps a kernel rev to the sha256 of its both-slot
// Linux Image (the both-slot initramfs built in). d02e9263 has two Images:
// 593ccb89… (r2, slot A only: its /init takes system_a also in a slot-B
// boot) and 85f5a417… (both-slot, initramfs 4d351397…, reproduced in two
// folders); only this one may go into a both-slot payload.
func reviewedBothSlotKernels() map[string]string {
	return map[string]string{
		"d02e92631f2810a4a43fb6f09824afb9b0044ee2": "85f5a417e12feabb3c8a52b246ce8b89d6f9c70d2e6994525f69ff307f13f3d7",
	}
}

// bothSlotAPKKeys is the exact set of /etc/apk/keys of a both-slot payload:
// the two Alpine keys and the tsx-mainline key.
var bothSlotAPKKeys = []PinnedFile{
	{"/etc/apk/keys/alpine-devel@lists.alpinelinux.org-58199dcc.rsa.pub", "73867d92083f2f8ab899a26ccda7ef63dfaa0032a938620eda605558958a8041"},
	{"/etc/apk/keys/alpine-devel@lists.alpinelinux.org-616ae350.rsa.pub", "d11f6b21c61b4274e182eb888883a8ba8acdbf820dcc7a6d82a7d9fc2fd2836d"},
	{"/etc/apk/keys/tsx-mainline-6ab9ca44.rsa.pub", "3fb7323556a7b69556ad98c602b6af89dbb2032db6791756f140e9c4e89f0c8a"},
}

// bothSlotDevMarkers must not be in a both-slot payload: a DEV build (packages
// signed by a build-host key, or the DEV overlay) is for no unit.
var bothSlotDevMarkers = []string{"/etc/tsx/dev-apk-key", "/etc/tsx/dev-overlay"}

// The exact step-2 set of a Step2 payload (BOTH-SLOT-PACKAGING §4) is NOT in
// this package: it is in internal/mainline/step2set, which only the RAM
// installer (uc-pxx-inst, step 1: it puts the STEP-2 payload into the first
// mainline slot) links. uc-pxx-step2 never holds it, so its own bytes do not
// depend on the pin of itself (a fixed point that cannot exist), and step 2
// installs only a normal payload: the release gate of uc-pxx-step2 refuses a
// Step2 payload (no step-2 set).

// BothSlot are the both-slot ("ab") rules of a gate. The release's gate has
// all of them (the lists above); a test gate has the pins of SetBothPins
// only, unless WithBothSlot gives it more. A nil list is not checked.
type BothSlot struct {
	Pins       []PinnedFile      // every file in the tree with this sha256
	ListPaths  []string          // the exact path set of the boot-control list
	Kernels    map[string]string // rev -> the both-slot Image (nil: the gate's kernels)
	APKKeys    []PinnedFile      // the exact /etc/apk/keys
	DevMarkers []string          // must not be in the tree
	Step2      []PinnedFile      // the exact step-2 set of a Step2 payload
	Step2Other []string          // its folders and links (allowed by name)
	// USBOther are the folders and links of the USB set (bothSlotUSB, whose
	// files are in Pins): the permanent USB interface of every both-slot
	// payload (mainline-rc-4, owner 2026-11-11). An entry named
	// uc-pxx-xfer* or uc-pxx-usb* must be a pinned file or one of these.
	USBOther []string
}

func embeddedBothSlot() BothSlot {
	return BothSlot{Pins: bootControlBothPins(), ListPaths: bothSlotListPaths(),
		Kernels: reviewedBothSlotKernels(), APKKeys: append([]PinnedFile(nil), bothSlotAPKKeys...),
		DevMarkers: append([]string(nil), bothSlotDevMarkers...), USBOther: append([]string(nil), bothSlotUSBOther...)}
}

// EmbeddedBothSlot is a copy of the release's both-slot rules (tests, the record).
func EmbeddedBothSlot() BothSlot { return embeddedBothSlot() }

func concatPins(l ...[]PinnedFile) []PinnedFile {
	var out []PinnedFile
	for _, x := range l {
		out = append(out, x...)
	}
	return out
}

func pathsOf(l []PinnedFile) []string {
	var out []string
	for _, p := range l {
		out = append(out, p.Path)
	}
	return out
}

const (
	// postUpdate runs `sha256sum -c` on bootControlList (RULES.md); a gate
	// that pins it also checks the list.
	postUpdate      = "/usr/sbin/uc-pxx-post-update"
	bootControlList = "/usr/share/uc-pxx/boot-control.sha256"
	// noBootctlInst: no file of this name (or with it as a prefix) in the
	// tree: the two-slot installer tool is never in system_a (RULES.md).
	noBootctlInst = "uc-pxx-bootctl-inst"
	// maxTreeEntries caps the walk of the tree.
	maxTreeEntries = 1 << 20
)

// maxPinnedFile caps the read of one boot-control file.
const maxPinnedFile = 16 << 20

// Gate is the payload gate of a plan. The zero Gate refuses everything.
type Gate struct {
	name        string
	reviewed    []Reviewed
	kernels     map[string]string
	bootControl []PinnedFile
	vbmeta      []VbmetaPatch
	both        *BothSlot // nil: the package's bootControlBothPins only (a test gate)
	// a test gate checks only the lists it was given
	checkKernel, checkFiles, checkVbmeta bool
}

// EmbeddedGate is the gate of this release: the lists above, every check.
func EmbeddedGate() *Gate {
	return &Gate{name: "embedded", reviewed: append([]Reviewed(nil), reviewedPayloads...), kernels: reviewedKernels(),
		bootControl: append([]PinnedFile(nil), bootControlPins...), vbmeta: ReviewedVbmetaPatches(),
		both: ptrBoth(embeddedBothSlot()), checkKernel: true, checkFiles: true, checkVbmeta: true}
}

func ptrBoth(b BothSlot) *BothSlot { return &b }

// WithStep2Set is a copy of the gate whose both-slot rules accept a Step2
// payload with exactly this step-2 set (files with their sha256, and the
// folders and links named in other). Only the RAM installer calls it
// (internal/mainline/step2set); a gate without it refuses every Step2 payload.
func (g *Gate) WithStep2Set(files []PinnedFile, other []string) *Gate {
	c := *g
	b := c.bothRules()
	b.Step2, b.Step2Other = append([]PinnedFile(nil), files...), append([]string(nil), other...)
	c.both = &b
	return &c
}

// WithBothSlot is a copy of a test gate with these both-slot rules (in
// place of the package's both-slot pins alone).
func (g *Gate) WithBothSlot(b BothSlot) *Gate {
	c := *g
	c.both = &b
	return &c
}

// bothRules: the gate's both-slot rules (a test gate without WithBothSlot:
// the pins of SetBothPins, nothing else).
func (g *Gate) bothRules() BothSlot {
	if g.both != nil {
		return *g.both
	}
	return BothSlot{Pins: bootControlBothPins()}
}

// TestGate is a gate for tests and the devtest build: the payloads given,
// and the kernel and file checks only when kernels / files is not nil. A
// release build must not hold it (ci/check-no-writer.py).
func TestGate(reviewed []Reviewed, kernels map[string]string, files []PinnedFile) *Gate {
	return &Gate{name: "test", reviewed: append([]Reviewed(nil), reviewed...), kernels: copyMap(kernels),
		bootControl: append([]PinnedFile(nil), files...), checkKernel: kernels != nil, checkFiles: files != nil}
}

// WithVbmetaPatches is a copy of a test gate that also checks the patched
// vbmeta against list (the release gate always does).
func (g *Gate) WithVbmetaPatches(list []VbmetaPatch) *Gate {
	c := *g
	c.vbmeta, c.checkVbmeta = append([]VbmetaPatch(nil), list...), true
	return &c
}

// ChecksVbmeta reports a gate that checks the patched vbmeta (the plan then
// requires its own patch to be the gate's reviewed one).
func (g *Gate) ChecksVbmeta() bool { return g != nil && g.checkVbmeta }

// ReviewedVbmeta is a copy of the gate's reviewed vbmeta patches (empty when
// the gate does not check them).
func (g *Gate) ReviewedVbmeta() []VbmetaPatch {
	if !g.ChecksVbmeta() {
		return nil
	}
	return append([]VbmetaPatch(nil), g.vbmeta...)
}

// EmbeddedBootControlPins are the pins of the release gate (for tests).
func EmbeddedBootControlPins() []PinnedFile { return append([]PinnedFile(nil), bootControlPins...) }

// EmbeddedReviewedCount is the number of reviewed payloads of the release.
func EmbeddedReviewedCount() int { return len(reviewedPayloads) }

func copyMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	c := map[string]string{}
	for k, v := range m {
		c[k] = v
	}
	return c
}

// Name is "embedded" or "test" ("" for the zero Gate).
func (g *Gate) Name() string {
	if g == nil {
		return ""
	}
	return g.name
}

// GateCheck is one check of the gate.
type GateCheck struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	Why  string `json:"why"`
}

// GateRecord is what the gate found, for the plan.
type GateRecord struct {
	Gate        string       `json:"gate"` // "embedded" or "test"
	Reviewed    *Reviewed    `json:"reviewed_payload"`
	ImageSHA256 string       `json:"linux_image_sha256"`
	Files       []PinnedFile `json:"boot_control_files"` // as read from rootfs.ext4
	// VbmetaPatch is the reviewed patch that the manifest names (nil when
	// the gate does not check it, or none matched)
	VbmetaPatch *VbmetaPatch `json:"vbmeta_patch_reviewed"`
	Checks      []GateCheck  `json:"checks"`
}

// LinuxImage is the Linux Image of a checked template: its kernel part from
// ImageOffset on (nil if the kernel part is not longer than that).
func LinuxImage(tmpl []byte, ti TemplateInfo) []byte {
	start, end := int64(PageSize)+ImageOffset, int64(PageSize)+ti.KernelSize
	if ti.KernelSize <= ImageOffset || end > int64(len(tmpl)) {
		return nil
	}
	return tmpl[start:end]
}

func hexSHA(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Check runs the gate on a payload that pass 1 checked (Verify or Reopen).
func (g *Gate) Check(p *Payload) *GateRecord {
	r := &GateRecord{Gate: g.Name()}
	add := func(name string, ok bool, f string, a ...any) {
		r.Checks = append(r.Checks, GateCheck{Name: name, OK: ok, Why: fmt.Sprintf(f, a...)})
	}
	if g == nil || g.name == "" {
		add("payload-reviewed", false, "no payload gate")
		return r
	}
	if !p.Verified() {
		add("payload-reviewed", false, "the payload was not checked by pass 1")
		return r
	}
	ts, rs := hexSHA(p.tmpl), p.Manifest.Rootfs.SHA256
	for i := range g.reviewed {
		e := g.reviewed[i]
		if e.ManifestSHA256 == p.ManifestSHA256 && e.TemplateSHA256 == ts && e.RootfsSHA256 == rs {
			r.Reviewed = &e
		}
	}
	add("payload-reviewed", r.Reviewed != nil,
		"the bundle (manifest %s, template %s, rootfs %s) must be a mainline payload that this release lists as reviewed (%s gate, %d reviewed)",
		short(p.ManifestSHA256), short(ts), short(rs), g.name, len(g.reviewed))
	if r.Reviewed == nil {
		// one refusal: an unreviewed bundle's template and root file system
		// are not parsed further (a hostile rootfs.ext4 never reaches the
		// ext4 reader; the release's gate lists no payload yet)
		return r
	}
	if g.checkVbmeta {
		vp := p.Manifest.VbmetaPatched
		e := FindVbmetaPatch(g.vbmeta, vp.SourceSHA256, vp.SHA256)
		ok := e != nil && e.Size == vp.Size && e.FieldOffset == vp.FieldOffset && int64(e.BootImageSize) == vp.BootImageSize &&
			int64(e.BootImageSize) == p.Manifest.VbmetaBootCap
		if ok {
			r.VbmetaPatch = e
		}
		add("vbmeta-patch-reviewed", ok,
			"the manifest's vbmeta_patched (source %s, %d bytes, image_size %d at byte %d, result %s) must be a patch that this release lists as reviewed (%d reviewed)",
			short(vp.SourceSHA256), vp.Size, vp.BootImageSize, vp.FieldOffset, short(vp.SHA256), len(g.vbmeta))
	}
	if img := LinuxImage(p.tmpl, p.Template); img != nil {
		r.ImageSHA256 = hexSHA(img)
	}
	both := g.bothRules()
	ab := r.Reviewed.AllowsSlot("b")
	if g.checkKernel {
		kernels, which := g.kernels, "reviewed"
		if ab && both.Kernels != nil {
			// a both-slot payload needs a both-slot Image (its initramfs takes
			// the root of the slot it boots); the slot-A Image never passes
			kernels, which = both.Kernels, "reviewed both-slot"
		}
		want, ok := kernels[p.Manifest.Kernel.Rev]
		add("kernel-reviewed", ok && r.ImageSHA256 != "" && r.ImageSHA256 == want,
			"the Linux Image of the template (the kernel part from %#x on) has sha256 %s; the %s Image of kernel %s is %s",
			ImageOffset, orNone(short(r.ImageSHA256)), which, short(p.Manifest.Kernel.Rev), orNone(short(want)))
	}
	if r.Reviewed.Step2 && !ab {
		add("payload-step2", false, "a STEP-2 payload is reviewed for both slots only (it is %q)", r.Reviewed.Slots)
	}
	if r.Reviewed.Step2 && len(both.Step2) == 0 {
		add("payload-step2", false, "this gate takes no STEP-2 payload: only the RAM installer installs one (step 2 installs a normal payload)")
	}
	if g.checkFiles && ab {
		// a payload for slot B needs the both-slot boot control (MAJOR-1)
		add("boot-control-both-slots", len(both.Pins) > 0,
			"the payload is reviewed for slot B (%q): it must carry the both-slot boot control, pinned by this release (%d both-slot pins)",
			r.Reviewed.Slots, len(both.Pins))
	}
	if g.checkFiles {
		pins := g.bootControl
		if ab {
			pins = both.Pins // review T4 MINOR-1: a both-slot payload is checked against the both-slot pins
		}
		var bad []string
		fs, err := ext4.Open(p.rootfs, p.rootfs.Size())
		if err != nil {
			bad = append(bad, "rootfs.ext4: "+err.Error())
		}
		for _, f := range pins {
			if fs == nil {
				break
			}
			b, err := fs.ReadFile(f.Path, maxPinnedFile)
			got := ""
			if err == nil {
				got = hexSHA(b)
			}
			r.Files = append(r.Files, PinnedFile{Path: f.Path, SHA256: got})
			switch {
			case err != nil:
				bad = append(bad, fmt.Sprintf("%s: %v", f.Path, err))
			case got != f.SHA256:
				bad = append(bad, fmt.Sprintf("%s has sha256 %s, the pin is %s", f.Path, short(got), short(f.SHA256)))
			}
		}
		if fs != nil {
			bad = append(bad, checkList(fs, pins)...)
			if ab {
				bad = append(bad, checkBoth(fs, both, r.Reviewed.Step2)...)
			} else {
				bad = append(bad, checkAbsent(fs)...)
			}
		}
		// BOOTCTL-11c2afcd-REVIEW (2): the both-slot mark hook never ships
		// with the slot-A bootctl (38fea6d3's --expect b checks boot_a)
		// (in the tree as read, and in the gate's own pins: a data error)
		got, pinned := map[string]string{}, map[string]string{}
		for _, f := range r.Files {
			got[f.Path] = f.SHA256
		}
		for _, f := range pins {
			pinned[f.Path] = f.SHA256
		}
		for _, fp := range forbiddenPairs {
			for _, m := range []map[string]string{got, pinned} {
				if m[fp[0].Path] == fp[0].SHA256 && m[fp[1].Path] == fp[1].SHA256 {
					bad = append(bad, fmt.Sprintf("%s %s never ships with %s %s", fp[0].Path, short(fp[0].SHA256), fp[1].Path, short(fp[1].SHA256)))
				}
			}
		}
		sort.Strings(bad)
		add("boot-control-files", len(bad) == 0 && len(pins) > 0,
			"the %d boot-control files of rootfs.ext4 must have their pinned sha256 (RULES.md), the list %s must agree, and no %s may be in the tree%s",
			len(pins), bootControlList, noBootctlInst, optList(bad))
	}
	return r
}

// OK reports a gate record whose every check passed.
func (r *GateRecord) OK() bool {
	if r == nil || len(r.Checks) == 0 {
		return false
	}
	for _, c := range r.Checks {
		if !c.OK {
			return false
		}
	}
	return true
}

func short(s string) string {
	if len(s) > 16 {
		return s[:16] + "…"
	}
	return s
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func optList(l []string) string {
	if len(l) == 0 {
		return ""
	}
	return ": " + strings.Join(l, "; ")
}

// checkList: when the pins name uc-pxx-post-update, the list it checks
// (bootControlList, sha256sum lines) must be in the tree, name
// uc-pxx-post-update, and give the pinned sha256 for every pinned file it
// names. It reports what is wrong.
func checkList(fs *ext4.FS, pins []PinnedFile) []string {
	want := map[string]string{}
	for _, p := range pins {
		want[p.Path] = p.SHA256
	}
	if want[postUpdate] == "" {
		return nil
	}
	b, err := fs.ReadFile(bootControlList, 64<<10)
	if err != nil {
		return []string{fmt.Sprintf("%s: %v", bootControlList, err)}
	}
	var bad []string
	named := false
	for i, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		sha, path, ok := strings.Cut(l, " ")
		path = strings.TrimPrefix(strings.TrimLeft(path, " "), "*")
		if !ok || !IsSHA256(sha) || path == "" {
			bad = append(bad, fmt.Sprintf("%s line %d is not a sha256sum line", bootControlList, i+1))
			continue
		}
		if path == postUpdate {
			named = true
		}
		if w, pinned := want[path]; pinned && sha != w {
			bad = append(bad, fmt.Sprintf("%s gives %s sha256 %s, the pin is %s", bootControlList, path, short(sha), short(w)))
		}
	}
	if !named {
		bad = append(bad, fmt.Sprintf("%s does not name %s", bootControlList, postUpdate))
	}
	return bad
}

// checkBoth: the both-slot rules beyond the pins (BOTH-SLOT-PACKAGING §6
// items 3, 5, 6): the boot-control list names exactly ListPaths; /etc/apk/keys
// is exactly APKKeys; no DEV marker; an entry named uc-pxx-bootctl-inst* or
// uc-pxx-step2* only in a Step2 payload, and there exactly the step-2 set
// (each file with its sha256) and its folders and links; an entry named
// uc-pxx-xfer* or uc-pxx-usb* (the USB set, in every both-slot payload since
// mainline-rc-4; its files are pins) only as a file of the pins or a folder
// or link of USBOther; a tree that cannot be walked is refused.
func checkBoth(fs *ext4.FS, b BothSlot, step2 bool) []string {
	var bad []string
	if b.ListPaths != nil {
		got := map[string]bool{}
		if l, err := fs.ReadFile(bootControlList, 64<<10); err == nil {
			for _, ln := range strings.Split(string(l), "\n") {
				ln = strings.TrimSpace(ln)
				if ln == "" || strings.HasPrefix(ln, "#") {
					continue
				}
				if _, p, ok := strings.Cut(ln, " "); ok {
					got[strings.TrimPrefix(strings.TrimLeft(p, " "), "*")] = true
				}
			}
		}
		want := map[string]bool{}
		for _, p := range b.ListPaths {
			want[p] = true
			if !got[p] {
				bad = append(bad, fmt.Sprintf("%s does not name %s", bootControlList, p))
			}
		}
		for p := range got {
			if !want[p] {
				bad = append(bad, fmt.Sprintf("%s names %s, which is not a both-slot boot tool", bootControlList, p))
			}
		}
	}
	for _, m := range b.DevMarkers {
		if _, err := fs.ReadFile(m, 1<<20); err == nil || !strings.Contains(err.Error(), "not found") {
			bad = append(bad, fmt.Sprintf("%s is in the tree (a DEV build is for no unit)", m))
		}
	}
	step2Files := map[string]string{}
	allowed := map[string]bool{}
	usbAllowed := map[string]bool{}
	for _, f := range b.Pins {
		usbAllowed[f.Path] = true
	}
	for _, o := range b.USBOther {
		usbAllowed[o] = true
	}
	if step2 {
		for _, f := range b.Step2 {
			step2Files[f.Path] = f.SHA256
			allowed[f.Path] = true
		}
		for _, o := range b.Step2Other {
			allowed[o] = true
		}
	}
	keys := map[string]string{}
	for _, k := range b.APKKeys {
		keys[k.Path] = k.SHA256
	}
	seenKeys := map[string]bool{}
	err := fs.Walk(maxTreeEntries, func(q string) {
		base := path.Base(q)
		if strings.HasPrefix(base, noBootctlInst) || strings.HasPrefix(base, step2Name) || q == step2Marker {
			if !allowed[q] {
				bad = append(bad, fmt.Sprintf("%s is in the tree (the step-2 set is allowed only in a STEP-2 payload, exactly)", q))
			}
		}
		// the USB set (mainline-rc-4): in every both-slot payload, exactly
		// its pinned files (b.Pins) and its folders and link (b.USBOther)
		if strings.HasPrefix(base, xferName) || strings.HasPrefix(base, usbName) {
			if !usbAllowed[q] {
				bad = append(bad, fmt.Sprintf("%s is in the tree (an entry named %s* or %s* must be a file of the USB set or its folder)", q, xferName, usbName))
			}
		}
		if b.APKKeys != nil && path.Dir(q) == apkKeysDir {
			seenKeys[q] = true
			want, ok := keys[q]
			got := ""
			if d, err := fs.ReadFile(q, 1<<20); err == nil {
				got = hexSHA(d)
			}
			if !ok || got != want {
				bad = append(bad, fmt.Sprintf("%s (sha256 %s) is not a key of the both-slot key set", q, orNone(short(got))))
			}
		}
	})
	if err != nil {
		bad = append(bad, "the tree of rootfs.ext4 cannot be walked: "+err.Error())
	}
	for k := range keys {
		if !seenKeys[k] {
			bad = append(bad, fmt.Sprintf("%s is not in the tree", k))
		}
	}
	for p, w := range step2Files {
		d, err := fs.ReadFile(p, maxPinnedFile)
		switch {
		case err != nil:
			bad = append(bad, fmt.Sprintf("%s: %v", p, err))
		case hexSHA(d) != w:
			bad = append(bad, fmt.Sprintf("%s has sha256 %s, the step-2 pin is %s", p, short(hexSHA(d)), short(w)))
		}
	}
	return bad
}

const (
	step2Name   = "uc-pxx-step2"
	xferName    = "uc-pxx-xfer" // the USB transport: the USB set of every both-slot payload (mainline-rc-4)
	usbName     = "uc-pxx-usb"  // its service, conf and finish list
	step2Marker = "/etc/tsx/step2-bootctl-inst"
	apkKeysDir  = "/etc/apk/keys"
)

// checkAbsent: no entry of the tree is named uc-pxx-bootctl-inst (or starts
// with it), and no entry of the step-2 set (uc-pxx-step2*, uc-pxx-xfer*, the
// marker); a tree that cannot be walked is refused.
func checkAbsent(fs *ext4.FS) []string {
	var bad []string
	err := fs.Walk(maxTreeEntries, func(p string) {
		if strings.HasPrefix(path.Base(p), noBootctlInst) {
			bad = append(bad, fmt.Sprintf("%s is in the tree (RULES.md: never in system_a)", p))
		} else if strings.HasPrefix(path.Base(p), step2Name) || strings.HasPrefix(path.Base(p), xferName) ||
			strings.HasPrefix(path.Base(p), usbName) || p == step2Marker {
			bad = append(bad, fmt.Sprintf("%s is in the tree (the step-2 set and the USB set are never in a slot-A payload)", p))
		}
	})
	if err != nil {
		bad = append(bad, "the tree of rootfs.ext4 cannot be walked: "+err.Error())
	}
	return bad
}

// StepTwoManifests are the manifest sha256s of the gate's reviewed payloads
// that step 2 may install: every reviewed payload that is not a STEP-2 payload
// (step 2 installs only a normal payload). Sorted, each once. They are the
// pins of the step-2 USB transport (uc-pxx-step2 pins; STEP2-USB-TRANSPORT
// §3.4 item 2): one list, never a second hand-kept one. (A function, not a
// method: an exported method of Gate would put its name into the type data
// of every binary that holds Gate, uc-pxx-inst included.)
func StepTwoManifests(g *Gate) []string {
	if g == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, r := range g.reviewed {
		if !r.Step2 && IsSHA256(r.ManifestSHA256) && !seen[r.ManifestSHA256] {
			seen[r.ManifestSHA256] = true
			out = append(out, r.ManifestSHA256)
		}
	}
	sort.Strings(out)
	return out
}
