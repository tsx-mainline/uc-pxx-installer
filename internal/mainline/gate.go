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
//     system_a). All read with internal/ext4, in place.
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
var reviewedKernels = map[string]string{}

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
	// a test gate checks only the lists it was given
	checkKernel, checkFiles bool
}

// EmbeddedGate is the gate of this release: the lists above, every check.
func EmbeddedGate() *Gate {
	return &Gate{name: "embedded", reviewed: append([]Reviewed(nil), reviewedPayloads...), kernels: copyMap(reviewedKernels),
		bootControl: append([]PinnedFile(nil), bootControlPins...), checkKernel: true, checkFiles: true}
}

// TestGate is a gate for tests and the devtest build: the payloads given,
// and the kernel and file checks only when kernels / files is not nil. A
// release build must not hold it (ci/check-no-writer.py).
func TestGate(reviewed []Reviewed, kernels map[string]string, files []PinnedFile) *Gate {
	return &Gate{name: "test", reviewed: append([]Reviewed(nil), reviewed...), kernels: copyMap(kernels),
		bootControl: append([]PinnedFile(nil), files...), checkKernel: kernels != nil, checkFiles: files != nil}
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
	if img := LinuxImage(p.tmpl, p.Template); img != nil {
		r.ImageSHA256 = hexSHA(img)
	}
	if g.checkKernel {
		want, ok := g.kernels[p.Manifest.Kernel.Rev]
		add("kernel-reviewed", ok && r.ImageSHA256 != "" && r.ImageSHA256 == want,
			"the Linux Image of the template (the kernel part from %#x on) has sha256 %s; the reviewed Image of kernel %s is %s",
			ImageOffset, orNone(short(r.ImageSHA256)), short(p.Manifest.Kernel.Rev), orNone(short(want)))
	}
	if g.checkFiles {
		var bad []string
		fs, err := ext4.Open(p.rootfs, p.rootfs.Size())
		if err != nil {
			bad = append(bad, "rootfs.ext4: "+err.Error())
		}
		for _, f := range g.bootControl {
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
			bad = append(bad, checkList(fs, g.bootControl)...)
			bad = append(bad, checkAbsent(fs)...)
		}
		sort.Strings(bad)
		add("boot-control-files", len(bad) == 0 && len(g.bootControl) > 0,
			"the %d boot-control files of rootfs.ext4 must have their pinned sha256 (RULES.md), the list %s must agree, and no %s may be in the tree%s",
			len(g.bootControl), bootControlList, noBootctlInst, optList(bad))
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

// checkAbsent: no entry of the tree is named uc-pxx-bootctl-inst (or starts
// with it); a tree that cannot be walked is refused.
func checkAbsent(fs *ext4.FS) []string {
	var bad []string
	err := fs.Walk(maxTreeEntries, func(p string) {
		if strings.HasPrefix(path.Base(p), noBootctlInst) {
			bad = append(bad, fmt.Sprintf("%s is in the tree (RULES.md: never in system_a)", p))
		}
	})
	if err != nil {
		bad = append(bad, "the tree of rootfs.ext4 cannot be walked: "+err.Error())
	}
	return bad
}
