// Package mainline reads the mainline payload of the installer (payload
// contract v1, MAINLINE-INSTALL-PLAN.md): the folder uc-pxx-mainline/ on the
// P10-INSTALL drive with manifest.json, boot-template.img and rootfs.ext4. It
// parses the manifest strictly, checks the two images against it (size and
// sha256 while reading, as pass 1 of an official firmware does), and builds
// the boot image of slot A from the template and the keeper's stock resource
// exactly as uc-pxx-linux kernel/mkimage.sh --resource does (Assemble).
//
// It only reads. It opens no device. It has no package-level state that
// needs initialization, so a build that does not call it does not change.
package mainline

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// The names and values of payload contract v1.
const (
	Dir          = "uc-pxx-mainline" // the folder in the top folder of the drive
	ManifestFile = "manifest.json"
	TemplateFile = "boot-template.img"
	RootfsFile   = "rootfs.ext4"

	Schema      = 1
	Kind        = "uc-pxx-mainline"
	Model       = "UC-P10" // Q14: UC-P10 only
	RootfsLabel = "tsxroot"
	// BuildType is the only boot chain a mainline slot can start with: the
	// userdebug U-Boot shows the orange state on an unlocked unit; a user
	// U-Boot forces "locked" and never starts an unsigned boot image.
	BuildType = "userdebug"

	// ReviewedKernelRev is the only kernel a mainline payload may carry
	// (uc-pxx-linux kernel/KERNEL_REV.lts, the reviewed uc-pxx-lts pin d02e9263;
	// the cpufreq/regulator caveat of MAINLINE-INSTALL-PLAN.md). It is the
	// same pin as the RAM image kernel (image/build.sh REVIEWED_KERNEL_REV).
	// A new kernel is a reviewed change of this constant.
	ReviewedKernelRev = "d02e92631f2810a4a43fb6f09824afb9b0044ee2"

	// MaxManifest caps manifest.json.
	MaxManifest = 64 << 10
	// MaxRootfs caps rootfs.ext4 (a sanity limit; the plan checks the
	// size of system_a).
	MaxRootfs = 16 << 30
)

// FileRef is one image of the payload in the manifest.
type FileRef struct {
	File   string `json:"file"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Assembled is the predicted boot_a: the template with the keeper's resource.
type Assembled struct {
	ResourceSHA256 string `json:"resource_sha256"`
	Size           int64  `json:"size"`
	SHA256         string `json:"sha256"`
}

// Rootfs is the root file system image.
type Rootfs struct {
	File   string `json:"file"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Label  string `json:"label"`
}

// Bootchain is the boot chain that slot A gets from the keeper (Q5): the
// 1.0.0.94 userdebug uboot, trust, dtbo, vbmeta and boot resource.
type Bootchain struct {
	Version        string `json:"version"`
	BuildType      string `json:"build_type"`
	UBootSHA256    string `json:"uboot_sha256"`
	TrustSHA256    string `json:"trust_sha256"`
	DtboSHA256     string `json:"dtbo_sha256"`
	VbmetaSHA256   string `json:"vbmeta_sha256"`
	ResourceSHA256 string `json:"resource_sha256"`
}

// Kernel names the kernel of the template.
type Kernel struct {
	Rev string `json:"rev"`
}

// Manifest is manifest.json of payload contract v1.
type Manifest struct {
	Schema            int       `json:"schema"`
	Kind              string    `json:"kind"`
	Version           string    `json:"version"`
	Model             string    `json:"model"`
	Kernel            Kernel    `json:"kernel"`
	Token             string    `json:"token"`
	BootTemplate      FileRef   `json:"boot_template"`
	BootAssembled     Assembled `json:"boot_assembled"`
	Rootfs            Rootfs    `json:"rootfs"`
	RequiresBootchain Bootchain `json:"requires_bootchain"`
	VbmetaBootCap     int64     `json:"vbmeta_boot_cap"`
}

// keys is the exact key set of every object of the manifest: every key is
// required, no other key is allowed.
func keys(path string) []string {
	switch path {
	case "":
		return []string{"schema", "kind", "version", "model", "kernel", "token", "boot_template", "boot_assembled",
			"rootfs", "requires_bootchain", "vbmeta_boot_cap"}
	case "kernel":
		return []string{"rev"}
	case "boot_template":
		return []string{"file", "size", "sha256"}
	case "boot_assembled":
		return []string{"resource_sha256", "size", "sha256"}
	case "rootfs":
		return []string{"file", "size", "sha256", "label"}
	case "requires_bootchain":
		return []string{"version", "build_type", "uboot_sha256", "trust_sha256", "dtbo_sha256", "vbmeta_sha256",
			"resource_sha256"}
	}
	return nil
}

// ManifestError is every refusal of a manifest. (A type, not a package
// variable: the package has no state to initialize.)
type ManifestError struct{ Why string }

func (e *ManifestError) Error() string { return "mainline manifest: " + e.Why }

func bad(f string, a ...any) error { return &ManifestError{Why: fmt.Sprintf(f, a...)} }

// IsManifestError reports a refusal of ParseManifest.
func IsManifestError(err error) bool {
	var me *ManifestError
	return errors.As(err, &me)
}

// ParseManifest parses manifest.json strictly: one JSON object, exactly the
// keys of contract v1 at every level (an unknown, a missing or a repeated
// key refuses), no null, integer sizes, and every value well formed (Check).
func ParseManifest(b []byte) (*Manifest, error) {
	if len(b) == 0 || len(b) > MaxManifest {
		return nil, bad("%d bytes (1 to %d allowed)", len(b), MaxManifest)
	}
	if bytes.HasPrefix(b, []byte("\xef\xbb\xbf")) {
		return nil, bad("starts with a byte order mark")
	}
	if err := checkShape(b); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, bad("%v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, bad("data after the object")
	}
	if err := m.Check(); err != nil {
		return nil, err
	}
	return &m, nil
}

// checkShape walks the JSON tokens: the top value is an object, each object
// has exactly the keys of its path once, nested values are objects only
// where the contract has one, and there is no null and no array.
func checkShape(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return bad("%v", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return bad("not a JSON object")
	}
	if err := walkObject(dec, ""); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return bad("data after the object")
	}
	return nil
}

func walkObject(dec *json.Decoder, path string) error {
	want := keys(path)
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return bad("%v", err)
		}
		k, ok := tok.(string)
		if !ok {
			return bad("a key that is not a string")
		}
		full := k
		if path != "" {
			full = path + "." + k
		}
		known := false
		for _, w := range want {
			known = known || w == k
		}
		switch {
		case !known:
			return bad("unknown key %q", full)
		case seen[k]:
			return bad("key %q given twice", full)
		}
		seen[k] = true
		v, err := dec.Token()
		if err != nil {
			return bad("%s: %v", full, err)
		}
		sub := keys(full)
		switch x := v.(type) {
		case json.Delim:
			if x != '{' || sub == nil {
				return bad("%s: an object or array where the contract has a value", full)
			}
			if err := walkObject(dec, full); err != nil {
				return err
			}
		case nil:
			return bad("%s is null", full)
		default:
			if sub != nil {
				return bad("%s must be an object", full)
			}
		}
	}
	if _, err := dec.Token(); err != nil { // the closing '}'
		return bad("%v", err)
	}
	for _, w := range want {
		if !seen[w] {
			if path != "" {
				w = path + "." + w
			}
			return bad("missing key %q", w)
		}
	}
	return nil
}

// IsSHA256 reports 64 lower-case hex digits.
func IsSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func isHex40(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// ValidToken reports a build token as kernel/mkimage.sh allows it: 1 to 15
// characters of letters, digits, '.', '_' and '-'.
func ValidToken(s string) bool {
	if len(s) < 1 || len(s) > 15 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// validVersion: 1 to 64 characters of letters, digits, '.', '_', '+', '-',
// starting with a letter or a digit (a release tag or a dev id).
func validVersion(s string) bool {
	if len(s) < 1 || len(s) > 64 {
		return false
	}
	for i, c := range s {
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !alnum && (i == 0 || c != '.' && c != '_' && c != '+' && c != '-') {
			return false
		}
	}
	return true
}

// validFirmwareVersion: four dot-separated numbers ("1.0.0.94").
func validFirmwareVersion(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if len(p) < 1 || len(p) > 6 {
			return false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

// Check checks every value of a manifest: the contract constants, the
// pinned kernel, the formats, and the sizes against each other.
func (m *Manifest) Check() error {
	rb := m.RequiresBootchain
	switch {
	case m.Schema != Schema:
		return bad("schema %d, this installer reads only schema %d", m.Schema, Schema)
	case m.Kind != Kind:
		return bad("kind %q, not %q", m.Kind, Kind)
	case m.Model != Model:
		return bad("model %q: this installer installs mainline only on a %s", m.Model, Model)
	case !validVersion(m.Version):
		return bad("version %q is not a release tag or dev id", m.Version)
	case !isHex40(m.Kernel.Rev):
		return bad("kernel.rev %q is not a full commit id", m.Kernel.Rev)
	case m.Kernel.Rev != ReviewedKernelRev:
		return bad("kernel.rev %s is not the reviewed kernel %s", m.Kernel.Rev, ReviewedKernelRev)
	case !ValidToken(m.Token):
		return bad("token %q (1 to 15 of A-Z a-z 0-9 . _ -)", m.Token)
	case m.BootTemplate.File != TemplateFile:
		return bad("boot_template.file %q, not %q", m.BootTemplate.File, TemplateFile)
	case !IsSHA256(m.BootTemplate.SHA256):
		return bad("boot_template.sha256 is not 64 lower-case hex digits")
	case m.BootTemplate.Size < PageSize*2 || m.BootTemplate.Size > MaxBootImage || m.BootTemplate.Size%PageSize != 0:
		return bad("boot_template.size %d (a whole number of %d-byte pages, at most %d)", m.BootTemplate.Size, PageSize, MaxBootImage)
	case !IsSHA256(m.BootAssembled.SHA256) || !IsSHA256(m.BootAssembled.ResourceSHA256):
		return bad("boot_assembled: a sha256 is not 64 lower-case hex digits")
	case m.BootAssembled.Size <= m.BootTemplate.Size || m.BootAssembled.Size%PageSize != 0 ||
		m.BootAssembled.Size-m.BootTemplate.Size > MaxResource+PageSize:
		return bad("boot_assembled.size %d does not fit a template of %d bytes and a resource", m.BootAssembled.Size, m.BootTemplate.Size)
	case m.VbmetaBootCap <= 0 || m.VbmetaBootCap > MaxBootImage:
		return bad("vbmeta_boot_cap %d (1 to %d)", m.VbmetaBootCap, MaxBootImage)
	case m.BootAssembled.Size > m.VbmetaBootCap:
		return bad("boot_assembled.size %d is larger than vbmeta_boot_cap %d: libavb would truncate the image", m.BootAssembled.Size, m.VbmetaBootCap)
	case m.Rootfs.File != RootfsFile:
		return bad("rootfs.file %q, not %q", m.Rootfs.File, RootfsFile)
	case !IsSHA256(m.Rootfs.SHA256):
		return bad("rootfs.sha256 is not 64 lower-case hex digits")
	case m.Rootfs.Size <= 0 || m.Rootfs.Size%512 != 0 || m.Rootfs.Size > MaxRootfs:
		return bad("rootfs.size %d (a whole number of 512-byte sectors, at most %d)", m.Rootfs.Size, int64(MaxRootfs))
	case m.Rootfs.Label != RootfsLabel:
		return bad("rootfs.label %q, not %q", m.Rootfs.Label, RootfsLabel)
	case !validFirmwareVersion(rb.Version):
		return bad("requires_bootchain.version %q", rb.Version)
	case rb.BuildType != BuildType:
		return bad("requires_bootchain.build_type %q: mainline starts only with the %s U-Boot", rb.BuildType, BuildType)
	case !IsSHA256(rb.UBootSHA256) || !IsSHA256(rb.TrustSHA256) || !IsSHA256(rb.DtboSHA256) ||
		!IsSHA256(rb.VbmetaSHA256) || !IsSHA256(rb.ResourceSHA256):
		return bad("requires_bootchain: a sha256 is not 64 lower-case hex digits")
	case rb.ResourceSHA256 != m.BootAssembled.ResourceSHA256:
		return bad("boot_assembled.resource_sha256 is not requires_bootchain.resource_sha256")
	}
	return nil
}
