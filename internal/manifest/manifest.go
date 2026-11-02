// Package manifest loads the firmware manifests (see package manifests).
//
// Schema 2 (plan FIRMWARE-GENERAL §6.1): besides the image sha256 of each
// partition, a manifest records the boot chain (uboot, trust, the boot
// resource), the AVB facts of the signed vbmeta (the ATX chain, the key
// versions, every descriptor), and what a person decided in the reviewed
// commit: the status (install, recognise or refuse), the reason of a refusal,
// the resource status and the evidence. tools/fw-manifest.py writes a
// manifest from the firmware, with the AVB checks, and never writes "install".
package manifest

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"

	"uc-pxx-installer/internal/avb"
	"uc-pxx-installer/manifests"
)

// Schema is the only manifest schema the installer reads.
const Schema = 2

// The statuses.
const (
	StatusInstall   = "install"   // the installer may write it into the target slot
	StatusRecognise = "recognise" // only to name what a slot holds
	StatusRefuse    = "refuse"    // never installed; RefuseReason says why, in plain words
)

// The resource statuses (boot_chain.resource_status). Only "booted" and
// "reviewed" may be installed: U-Boot reads the DTB of this resource before
// the A/B tries are counted (RULES.md, the resource rule).
const (
	ResourceBooted   = "booted"   // the resource booted on a UC-P10 (evidence)
	ResourceReviewed = "reviewed" // an independent review of its DTB said GO, and the owner allowed it
	ResourcePending  = "pending"  // not decided yet
	ResourceNoGo     = "no-go"    // the review said it must never be installed
)

// NvramModels are the models of the nvram reader that the installer has
// (FIRMWARE-GENERAL §5.1): an installable firmware must name one.
var NvramModels = []string{"gs-nvram-72005-v1"}

// Part is one partition image of a firmware.
type Part struct {
	Segment   int    `json:"segment"`
	SegID     string `json:"seg_id"`
	Partition string `json:"partition"`
	Image     string `json:"image"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	Action    string `json:"action"` // "write" or "write-if-different"
}

// BootChain is what runs or is parsed before the A/B tries are counted.
type BootChain struct {
	UBootSHA256           string `json:"uboot_sha256"`
	TrustSHA256           string `json:"trust_sha256"`
	BootResourceSHA256    string `json:"boot_resource_sha256"`
	UBootBuild            string `json:"uboot_build"` // "user" (forced locked) or "userdebug"
	UBootLoaderVersion    uint32 `json:"uboot_loader_version"`
	UBootNormalisedSHA256 string `json:"uboot_normalised_sha256"` // review evidence: the code without build-time data
	ResourceStatus        string `json:"resource_status"`
	Evidence              string `json:"evidence"`
	// BootchainStatus is whether this pair of U-Boot code (the normalised
	// sha) and boot resource booted on a UC-P10 that we have (the owner's
	// decision on FWGEN-REVIEW F-1, 2026-11-01: "proven only, then widen").
	// Only "booted" passes the gate. BootchainBooted is then the record of
	// that boot: the firmware file that booted, which must be a manifest of
	// the same set with the same pair and its own record.
	BootchainStatus string      `json:"bootchain_status"` // "booted" or "not-booted"
	BootchainBooted *BootRecord `json:"bootchain_booted"`
}

// BootRecord is the first-hand record of a boot chain that booted.
type BootRecord struct {
	FirmwareSHA256 string `json:"firmware_sha256"` // the file whose boot chain booted
	Unit           string `json:"unit"`
	Slot           string `json:"slot"`
	Date           string `json:"date"`
	Record         string `json:"record"` // what was seen, where the log is
}

// The install statuses.
const (
	InstallOpen     = "open"
	InstallWithheld = "withheld"
)

// The boot-chain statuses.
const (
	BootchainBooted    = "booted"
	BootchainNotBooted = "not-booted"
)

// Footer is the AVB footer of a covered image.
type Footer struct {
	OriginalImageSize uint64 `json:"original_image_size"`
	VbmetaOffset      uint64 `json:"vbmeta_offset"`
	VbmetaSize        uint64 `json:"vbmeta_size"`
}

// AVB are the facts of the signed vbmeta.
type AVB struct {
	Algorithm               string            `json:"algorithm"`
	RequiredLibavb          string            `json:"required_libavb"`
	Flags                   uint32            `json:"flags"`
	RollbackIndex           uint64            `json:"rollback_index"`
	RollbackIndexLocation   uint32            `json:"rollback_index_location"`
	PublicKeyMetadataSHA256 string            `json:"public_key_metadata_sha256"`
	PIKCertSHA256           string            `json:"pik_cert_sha256"`
	PIKKeyVersion           uint64            `json:"pik_key_version"`
	PSKCertSHA256           string            `json:"psk_cert_sha256"`
	PSKSubject              string            `json:"psk_subject"`
	PSKKeyVersion           uint64            `json:"psk_key_version"`
	Descriptors             []avb.Descriptor  `json:"descriptors"`
	ChainPartitions         []string          `json:"chain_partitions"`
	Footers                 map[string]Footer `json:"footers"`
	NotCovered              []string          `json:"not_covered"`
}

// Vbmeta is the header summary of the vbmeta image.
type Vbmeta struct {
	ImageBytes      int    `json:"image_bytes"`
	Algorithm       int    `json:"algorithm"`
	RollbackIndex   uint64 `json:"rollback_index"`
	Flags           uint32 `json:"flags"`
	ReleaseString   string `json:"release_string"`
	PublicKeySHA1   string `json:"public_key_sha1"`
	PublicKeySHA256 string `json:"public_key_sha256"`
}

// Manifest describes one official firmware file.
type Manifest struct {
	Schema       int    `json:"schema"`
	Status       string `json:"status"`
	RefuseReason string `json:"refuse_reason"`
	TestBuild    bool   `json:"test_build"` // a Crestron test build (its file name says so: "_based_")
	// InstallStatus is whether this release offers the file for install:
	// "open" or "withheld" (with WithheldReason). It is apart from the
	// boot record: a file whose boot chain booted can still be withheld (the
	// owner, 2026-11-01: release 1 offers only 1.0.0.94 userdebug). Opening
	// a file is a reviewed data change.
	InstallStatus  string `json:"install_status"`
	WithheldReason string `json:"withheld_reason"`
	Firmware       struct {
		Version string `json:"version"`
		File    string `json:"file"`
		Size    int64  `json:"size"`
		SHA256  string `json:"sha256"`
	} `json:"firmware"`
	Build         map[string]string `json:"build"`
	BuildPropPath string            `json:"build_prop_path"`
	NvramModel    string            `json:"nvram_model"`
	BootChain     BootChain         `json:"boot_chain"`
	Vbmeta        Vbmeta            `json:"vbmeta"`
	AVB           AVB               `json:"avb"`
	Partitions    []Part            `json:"partitions"`

	pins *avb.Pins // tests only (WithTestPins); nil: the Crestron pins
}

// Partitions the installer may write, in the container order.
var known = []string{"uboot", "trust", "dtbo", "vbmeta", "boot", "system", "vendor", "oem"}

func isSHA256(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32 && s == strings.ToLower(s)
}

var versionRE = regexp.MustCompile(`^[0-9]+(\.[0-9]+){3}$`)

// check rejects a manifest that the installer must not trust.
func (m *Manifest) check() error {
	if m.Schema != Schema {
		return fmt.Errorf("schema %d, the installer reads only schema %d", m.Schema, Schema)
	}
	if !versionRE.MatchString(m.Firmware.Version) || !isSHA256(m.Firmware.SHA256) || m.Firmware.Size <= 0 || m.Firmware.File == "" {
		return fmt.Errorf("firmware entry incomplete")
	}
	for _, k := range []string{"ro.build.version.incremental", "ro.build.fingerprint", "ro.build.type"} {
		if m.Build[k] == "" {
			return fmt.Errorf("no %s", k)
		}
	}
	seen := map[string]bool{}
	for _, p := range m.Partitions {
		ok := false
		for _, k := range known {
			ok = ok || p.Partition == k
		}
		if !ok {
			return fmt.Errorf("unknown partition %q", p.Partition)
		}
		if seen[p.Partition] {
			return fmt.Errorf("partition %s listed twice", p.Partition)
		}
		seen[p.Partition] = true
		if !isSHA256(p.SHA256) || p.Size <= 0 {
			return fmt.Errorf("partition %s: bad sha256 or size", p.Partition)
		}
		if p.Action != "write" && p.Action != "write-if-different" {
			return fmt.Errorf("partition %s: unknown action %q", p.Partition, p.Action)
		}
	}
	if len(seen) != len(known) {
		return fmt.Errorf("%d partitions, want %d", len(seen), len(known))
	}
	if err := m.checkChain(); err != nil {
		return err
	}
	if err := m.checkAVB(); err != nil {
		return err
	}
	return m.checkHuman()
}

func (m *Manifest) checkChain() error {
	c := m.BootChain
	ub, _ := m.Part("uboot")
	tr, _ := m.Part("trust")
	if c.UBootSHA256 != ub.SHA256 || c.TrustSHA256 != tr.SHA256 {
		return fmt.Errorf("boot_chain: the uboot and trust sha256 must be the ones of the partition list")
	}
	if !isSHA256(c.BootResourceSHA256) || !isSHA256(c.UBootNormalisedSHA256) {
		return fmt.Errorf("boot_chain: bad resource or normalised U-Boot sha256")
	}
	if c.UBootBuild != "user" && c.UBootBuild != "userdebug" {
		return fmt.Errorf("boot_chain: uboot_build %q", c.UBootBuild)
	}
	if c.UBootBuild != m.Build["ro.build.type"] {
		return fmt.Errorf("boot_chain: a %s U-Boot with a %s Android", c.UBootBuild, m.Build["ro.build.type"])
	}
	return nil
}

func (m *Manifest) checkAVB() error {
	a := m.AVB
	if a.Algorithm != "SHA256_RSA4096" || a.RequiredLibavb != "1.0" || a.Flags != 0 || a.RollbackIndexLocation != 0 {
		return fmt.Errorf("avb: algorithm %s, libavb %s, flags %d, location %d: not SHA256_RSA4096, 1.0, 0, 0",
			a.Algorithm, a.RequiredLibavb, a.Flags, a.RollbackIndexLocation)
	}
	if m.Vbmeta.Algorithm != avb.SHA256RSA4096 || m.Vbmeta.Flags != 0 || m.Vbmeta.RollbackIndex != a.RollbackIndex ||
		len(m.Vbmeta.PublicKeySHA1) != 40 || !isSHA256(m.Vbmeta.PublicKeySHA256) {
		return fmt.Errorf("vbmeta: the summary does not match the avb facts")
	}
	for _, s := range []string{a.PublicKeyMetadataSHA256, a.PIKCertSHA256, a.PSKCertSHA256, a.PSKSubject} {
		if !isSHA256(s) {
			return fmt.Errorf("avb: a certificate hash or the subject is not a sha256")
		}
	}
	if len(a.ChainPartitions) != 0 {
		return fmt.Errorf("avb: chain partitions are not allowed")
	}
	got := map[string]bool{}
	for _, d := range a.Descriptors {
		if d.Type == "kernel_cmdline" {
			continue
		}
		kind, ok := avb.Covered[d.Partition]
		if !ok || kind != d.Type || got[d.Partition] {
			return fmt.Errorf("avb: descriptor %s for %s is not allowed", d.Type, d.Partition)
		}
		got[d.Partition] = true
		p, _ := m.Part(d.Partition)
		if d.ImageSize > uint64(p.Size) {
			return fmt.Errorf("avb: %s image size %d is larger than its image (%d)", d.Partition, d.ImageSize, p.Size)
		}
		if _, ok := a.Footers[d.Partition]; !ok {
			return fmt.Errorf("avb: no footer of %s", d.Partition)
		}
	}
	if len(got) != len(avb.Covered) || len(a.Footers) != len(avb.Covered) {
		return fmt.Errorf("avb: %d covered partitions and %d footers, want %d", len(got), len(a.Footers), len(avb.Covered))
	}
	return nil
}

func (m *Manifest) checkHuman() error {
	c := m.BootChain
	switch m.InstallStatus {
	case InstallOpen:
		if m.WithheldReason != "" || m.Status != StatusInstall {
			return fmt.Errorf("install_status open needs status install and no withheld reason")
		}
	case InstallWithheld:
		if strings.TrimSpace(m.WithheldReason) == "" {
			return fmt.Errorf("install_status withheld needs a withheld reason")
		}
	default:
		return fmt.Errorf("install_status %q, not open or withheld", m.InstallStatus)
	}
	switch c.BootchainStatus {
	case BootchainBooted:
		r := c.BootchainBooted
		if r == nil || !isSHA256(r.FirmwareSHA256) || strings.TrimSpace(r.Unit) == "" || strings.TrimSpace(r.Slot) == "" ||
			strings.TrimSpace(r.Date) == "" || strings.TrimSpace(r.Record) == "" {
			return fmt.Errorf("boot_chain: bootchain_status booted needs the boot record (firmware_sha256, unit, slot, date, record)")
		}
	case BootchainNotBooted:
		if c.BootchainBooted != nil {
			return fmt.Errorf("boot_chain: a boot record with bootchain_status not-booted")
		}
	default:
		return fmt.Errorf("boot_chain: bootchain_status %q, not booted or not-booted", c.BootchainStatus)
	}
	switch c.ResourceStatus {
	case ResourceBooted, ResourceReviewed, ResourcePending, ResourceNoGo:
	default:
		return fmt.Errorf("boot_chain: resource_status %q", c.ResourceStatus)
	}
	if m.NvramModel != "" && !m.NvramChecked() {
		return fmt.Errorf("unknown nvram model %q", m.NvramModel)
	}
	switch m.Status {
	case StatusInstall:
		if strings.TrimSpace(c.Evidence) == "" {
			return fmt.Errorf("status install needs the review evidence (boot_chain.evidence)")
		}
		if c.ResourceStatus == ResourceNoGo {
			return fmt.Errorf("status install with a no-go resource")
		}
		if m.RefuseReason != "" {
			return fmt.Errorf("status install with a refuse reason")
		}
	case StatusRefuse:
		if strings.TrimSpace(m.RefuseReason) == "" {
			return fmt.Errorf("status refuse needs a refuse reason")
		}
	case StatusRecognise:
		if m.RefuseReason != "" {
			return fmt.Errorf("status recognise with a refuse reason")
		}
	default:
		return fmt.Errorf("status %q, not install, recognise or refuse", m.Status)
	}
	return nil
}

// checkBootRecords: a manifest that says its boot chain booted points at a
// manifest of the same set that has the same pair (normalised U-Boot sha,
// boot resource sha) and its own first-hand record (it points at itself).
// So a claim of "booted" without matching evidence does not load.
func checkBootRecords(ms []*Manifest) error {
	for _, m := range ms {
		c := m.BootChain
		if c.BootchainStatus != BootchainBooted {
			continue
		}
		ref := ByFirmwareSHA(ms, c.BootchainBooted.FirmwareSHA256)
		switch {
		case ref == nil:
			return fmt.Errorf("%s: the boot record names firmware %s, which is in no manifest", m.Name(), c.BootchainBooted.FirmwareSHA256)
		case ref.BootChain.UBootNormalisedSHA256 != c.UBootNormalisedSHA256 || ref.BootChain.BootResourceSHA256 != c.BootResourceSHA256:
			return fmt.Errorf("%s: the boot record names %s, whose U-Boot and resource pair is another", m.Name(), ref.Name())
		case ref.BootChain.BootchainStatus != BootchainBooted || ref.BootChain.BootchainBooted.FirmwareSHA256 != ref.Firmware.SHA256:
			return fmt.Errorf("%s: the boot record names %s, which has no first-hand boot record", m.Name(), ref.Name())
		case *ref.BootChain.BootchainBooted != *c.BootchainBooted:
			return fmt.Errorf("%s: the boot record differs from the first-hand record of %s", m.Name(), ref.Name())
		}
	}
	return nil
}

// Offered: this release offers the file for install (install_status open).
func (m *Manifest) Offered() bool { return m.InstallStatus == InstallOpen }

// BootchainProven: the pair of U-Boot code and resource booted on a unit.
func (m *Manifest) BootchainProven() bool { return m.BootChain.BootchainStatus == BootchainBooted }

// Installable: the manifest says the firmware may be written into a slot.
// The planner still refuses a resource that is not approved.
func (m *Manifest) Installable() bool { return m.Status == StatusInstall }

// NvramChecked: the installer's reader of Auto Update Firmware was checked
// against this firmware's nvram driver (FIRMWARE-GENERAL §5.1). The planner
// refuses a firmware without it: its Android might read "off" as "on".
func (m *Manifest) NvramChecked() bool {
	for _, n := range NvramModels {
		if m.NvramModel == n {
			return true
		}
	}
	return false
}

// ResourceApproved: the boot resource may be written (booted or reviewed).
func (m *Manifest) ResourceApproved() bool {
	s := m.BootChain.ResourceStatus
	return s == ResourceBooted || s == ResourceReviewed
}

// Pins are the ATX pins the payload must chain to.
func (m *Manifest) Pins() avb.Pins {
	if m.pins != nil {
		return *m.pins
	}
	return avb.CrestronPins()
}

// WithTestPins sets the pins of a test chain on manifests that a test loaded
// (the real pins are vendor data that a made-up firmware cannot chain to).
// The embedded manifests never have them; the command line has no way to
// set them.
func WithTestPins(ms []*Manifest, p avb.Pins) []*Manifest {
	for _, m := range ms {
		q := p
		m.pins = &q
	}
	return ms
}

// Part returns the entry for a partition name.
func (m *Manifest) Part(name string) (Part, bool) {
	for _, p := range m.Partitions {
		if p.Partition == name {
			return p, true
		}
	}
	return Part{}, false
}

// Name is a short label, for example "1.0.6.20 (user)". Two files have the
// version 1.0.0.94: the build type tells them apart.
func (m *Manifest) Name() string {
	s := fmt.Sprintf("%s (%s)", m.Firmware.Version, m.Build["ro.build.type"])
	if m.TestBuild {
		s += ", Crestron test build"
	}
	return s
}

// Load reads every *.json manifest of fsys. An invalid manifest is an error,
// and so are two manifests for one firmware file or one build fingerprint.
func Load(fsys fs.FS) ([]*Manifest, error) {
	names, err := fs.Glob(fsys, "*.json")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var out []*Manifest
	shas, fps := map[string]string{}, map[string]string{}
	for _, n := range names {
		b, err := fs.ReadFile(fsys, n)
		if err != nil {
			return nil, err
		}
		m := new(Manifest)
		dec := json.NewDecoder(strings.NewReader(string(b)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(m); err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		if err := m.check(); err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		if o, dup := shas[m.Firmware.SHA256]; dup {
			return nil, fmt.Errorf("%s: the same firmware file as %s", n, o)
		}
		if o, dup := fps[m.Build["ro.build.fingerprint"]]; dup {
			return nil, fmt.Errorf("%s: the same build fingerprint as %s", n, o)
		}
		shas[m.Firmware.SHA256], fps[m.Build["ro.build.fingerprint"]] = n, n
		out = append(out, m)
	}
	if err := checkBootRecords(out); err != nil {
		return nil, err
	}
	return out, nil
}

// Embedded loads the manifests built into the binary.
func Embedded() ([]*Manifest, error) {
	return Load(manifests.FS)
}

// ByFirmwareSHA finds the manifest of a firmware file by its sha256.
func ByFirmwareSHA(ms []*Manifest, sha string) *Manifest {
	for _, m := range ms {
		if m.Firmware.SHA256 == sha {
			return m
		}
	}
	return nil
}

// ByFingerprint finds the manifest of the build that a slot's build.prop
// names (ro.build.fingerprint: it carries a build tag). The Crestron
// version is not in build.prop, and ro.build.version.incremental is a build
// time, not unique by design (FIRMWARE-ALL37 §0.1).
func ByFingerprint(ms []*Manifest, fingerprint string) *Manifest {
	if fingerprint == "" {
		return nil
	}
	for _, m := range ms {
		if m.Build["ro.build.fingerprint"] == fingerprint {
			return m
		}
	}
	return nil
}
