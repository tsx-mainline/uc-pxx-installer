// Package clone holds the rules and the pinned data of the step-2 plan kind
// clone (DESIGN-R23 §4): slot A is made a copy of the running, provisioned
// slot B. It defines the formats that the release build must supply:
//
//   - the clone gate (Gate): the keeper's boot chain pins, one entry per
//     profile (A's build id, ref_a, ref_b, the bundle), the difference
//     (clone-delta.json), the exclusion list of the content manifest, the
//     files that must not be in A, and the bound of the freeze;
//   - clone-delta.json (Delta, ParseDelta): what step 2 changes in A after
//     the block copy;
//   - the provisioning done record (DoneRecord, ParseDone): what
//     uc-pxx-provision writes on B when the profile is installed and checked;
//   - the content manifest (manifest.go): one sorted line per path, its
//     sha256 is ref_a / ref_b.
//
// This package only reads: it never writes a file (goallow: a reader
// package). The difference is applied by step 2 (internal/step2, its
// mount of system_a), with ApplyDelta of package clonedelta.
package clone

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"
)

const (
	// DoneRecordPath is the done record of uc-pxx-provision on B's root.
	DoneRecordPath = "/var/lib/uc-pxx-provision/done"
	// DoneRecordKind is its "kind" field.
	DoneRecordKind = "uc-pxx-provision-done"
	// MaxDoneRecord is the largest done record read.
	MaxDoneRecord = 64 << 10
	// MountPoint is the only eMMC mount that a clone run makes besides the
	// root: system_a for the difference (rw, nosuid, nodev, noexec).
	MountPoint = "/run/uc-pxx-step2/a"
	// FreezeBound is the longest time the running root is frozen: the thaw
	// guard thaws then, and the copy stops (DESIGN-R23 §4.3 item 4).
	// 110 s: under the kernel's default hung_task_timeout of 120 s, so a
	// hung-task panic (if the kernel has it) never fires in a freeze (clone
	// review NOTE-6); the estimate on the eMMC is 45-80 s, to measure
	FreezeBound = 110 * time.Second
	// BlockSize is the read size of the copy of system_b.
	BlockSize = 4 << 20
	// ProfileConsole is the profile without a bundle.
	ProfileConsole = "console"
)

// ChainPin is one part of the keeper's boot chain: the first Size bytes of
// <part>_b must have SHA256; slot A gets the same bytes.
type ChainPin struct {
	Part   string `json:"part"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Profile is one installable system of this release.
type Profile struct {
	Name string `json:"name"`
	// BuildID is the exact content of /etc/tsx/build-id in A (the
	// difference writes it byte for byte): "VERSION\n", the form mkrootfs
	// writes (one line, one newline at its end; Check refuses any other
	// form; R23-FORMATS §4).
	BuildID string `json:"build_id"`
	// RefA, RefB: the sha256 of the content manifest of the reference A and
	// the reference B of this profile (built twice by the release).
	RefA string `json:"ref_a_sha256"`
	RefB string `json:"ref_b_sha256"`
	// BundleSHA256: the profile bundle that uc-pxx-provision installed (""
	// for the console profile, which has none).
	BundleSHA256 string `json:"bundle_sha256"`
}

// Pin is a file by size and sha256.
type Pin struct {
	What   string `json:"what"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Gate is the clone gate of a release (EmbeddedGate) or of a test.
type Gate struct {
	// Name is "embedded" or "test".
	Name     string     `json:"name"`
	Chain    []ChainPin `json:"chain"`
	Profiles []Profile  `json:"profiles"`
	// Delta is clone-delta.json, DeltaJSON its exact bytes (DeltaSHA256 is
	// their sha256).
	Delta     *Delta `json:"-"`
	DeltaJSON []byte `json:"-"`
	// Exclude is the exclusion list of the content manifest (manifest.go).
	Exclude []string `json:"exclude"`
	// Forbidden: no regular file of A may have one of these sizes and
	// sha256s (r2 20aa6bc5…).
	Forbidden []Pin `json:"forbidden"`
	// ForbiddenFrom: paths of B's root whose bytes must not be in A either
	// (uc-pxx-step2 and uc-pxx-provision: read from B at plan time).
	ForbiddenFrom []string `json:"forbidden_from"`
	// FreezeBound: the thaw guard (FreezeBound in a release).
	FreezeBound time.Duration `json:"-"`
	// KeeperFiles: files of B's root that the content manifest leaves out
	// (the exclusion list names their folders: the gate is in uc-pxx-step2,
	// so ref_b cannot cover uc-pxx-step2 or the binary built with it) and
	// that the plan checks by their own pins instead: uc-pxx-provision by
	// its sha256 (R23-FORMATS §9.1). A gate with a profile needs it.
	KeeperFiles []PathPin `json:"keeper_files"`
}

// PathPin is a file of a path by size and sha256.
type PathPin struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Step2Marker is the step-2 marker of a STEP-2 base: one line that names
// the uc-pxx-step2 sha256. Left out of the content check (it depends on the
// gate's program) and checked by the plan against the running program.
const Step2Marker = "/etc/tsx/step2-bootctl-inst"

// ProvisionBinary is uc-pxx-provision on B (pinned by KeeperFiles).
const ProvisionBinary = "/usr/libexec/uc-pxx-provision/uc-pxx-provision"

// Step2Binary is uc-pxx-step2 on B: it holds the gate, so it is pinned
// only by step 1 (the base rootfs sha256 of the installer's step-2 set,
// written and read back by the RAM installer); the plan checks that the
// running program is that file (step2: selfCheck).
const Step2Binary = "/usr/libexec/uc-pxx-step2/uc-pxx-step2"

// BuildIDOK: the form of a build id file, "VERSION\n" (printable, no
// space, one newline at the end).
func BuildIDOK(s string) bool {
	v, ok := strings.CutSuffix(s, "\n")
	if !ok || v == "" || len(v) > 64 {
		return false
	}
	for _, c := range v {
		if c <= ' ' || c > '~' {
			return false
		}
	}
	return true
}

// ChainParts are the parts of the keeper's chain, in the order of the plan.
func ChainParts() []string { return []string{"uboot", "trust", "dtbo", "vbmeta", "boot"} }

// DeltaSHA256 is the sha256 of the gate's clone-delta.json.
func (g *Gate) DeltaSHA256() string { return SHA256(g.DeltaJSON) }

// ExcludeSHA256 is the sha256 of the exclusion list (one line per entry).
func (g *Gate) ExcludeSHA256() string { return SHA256([]byte(strings.Join(g.Exclude, "\n") + "\n")) }

// Profile is the gate's profile of that name.
func (g *Gate) Profile(name string) (Profile, bool) {
	for _, p := range g.Profiles {
		if p.Name == name {
			return p, true
		}
	}
	return Profile{}, false
}

// Pin is the gate's pin of a chain part.
func (g *Gate) Pin(part string) (ChainPin, bool) {
	for _, c := range g.Chain {
		if c.Part == part {
			return c, true
		}
	}
	return ChainPin{}, false
}

// Check reports what is wrong with the gate itself (a malformed gate
// refuses every plan).
func (g *Gate) Check() error {
	if g == nil {
		return errors.New("no clone gate")
	}
	var bad []string
	if g.Name != "embedded" && g.Name != "test" {
		bad = append(bad, fmt.Sprintf("gate name %q", g.Name))
	}
	if len(g.Chain) != len(ChainParts()) {
		bad = append(bad, fmt.Sprintf("%d chain pins, want %d", len(g.Chain), len(ChainParts())))
	}
	for i, part := range ChainParts() {
		if i < len(g.Chain) && (g.Chain[i].Part != part || g.Chain[i].Size <= 0 || g.Chain[i].Size%512 != 0 || !IsSHA256(g.Chain[i].SHA256)) {
			bad = append(bad, fmt.Sprintf("chain pin %d (%+v)", i, g.Chain[i]))
		}
	}
	seen := map[string]bool{}
	for _, p := range g.Profiles {
		if p.Name == "" || seen[p.Name] || !IsSHA256(p.RefA) || !IsSHA256(p.RefB) || !BuildIDOK(p.BuildID) ||
			(p.Name == ProfileConsole) != (p.BundleSHA256 == "") || (p.BundleSHA256 != "" && !IsSHA256(p.BundleSHA256)) {
			bad = append(bad, fmt.Sprintf("profile %q", p.Name))
		}
		seen[p.Name] = true
	}
	if g.Delta == nil {
		bad = append(bad, "no difference")
	} else if d, err := ParseDelta(g.DeltaJSON); err != nil {
		bad = append(bad, "clone-delta.json: "+err.Error())
	} else if !d.Equal(g.Delta) {
		bad = append(bad, "clone-delta.json is not the gate's difference")
	}
	if _, err := NewExcluder(g.Exclude); err != nil {
		bad = append(bad, "the exclusion list: "+err.Error())
	}
	for _, f := range g.Forbidden {
		if f.Size <= 0 || !IsSHA256(f.SHA256) {
			bad = append(bad, fmt.Sprintf("forbidden pin %+v", f))
		}
	}
	for _, p := range g.ForbiddenFrom {
		if !cleanAbs(p) {
			bad = append(bad, "forbidden path "+p)
		}
	}
	prov := false
	for _, f := range g.KeeperFiles {
		if !cleanAbs(f.Path) || f.Size <= 0 || !IsSHA256(f.SHA256) {
			bad = append(bad, fmt.Sprintf("keeper file pin %+v", f))
		}
		prov = prov || f.Path == ProvisionBinary
	}
	if len(g.Profiles) > 0 && !prov {
		bad = append(bad, "no pin of "+ProvisionBinary+" (it is not in ref_b: KeeperFiles must pin it)")
	}
	if g.FreezeBound <= 0 || g.FreezeBound > FreezeBound {
		bad = append(bad, fmt.Sprintf("freeze bound %s (0 < bound <= %s)", g.FreezeBound, FreezeBound))
	}
	if len(bad) > 0 {
		return errors.New(strings.Join(bad, "; "))
	}
	return nil
}

// Dir is an entry of Delta.Empty.
type Dir struct {
	Path string `json:"path"`
	Mode string `json:"mode"` // 4 octal digits, "0755"
	UID  uint32 `json:"uid"`
	GID  uint32 `json:"gid"`
}

// File is an entry of Delta.Write. From names where its bytes come from:
// "build-id" (the profile's BuildID), the only source.
type File struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	UID  uint32 `json:"uid"`
	GID  uint32 `json:"gid"`
	From string `json:"from"`
}

// Delta is clone-delta.json (schema 1), applied to A in this order:
//
//  1. Remove: each path (a file, a link or a whole folder tree) is removed;
//     a missing path is no error;
//  2. RemoveChildren: every entry under each folder is removed, the folder
//     stays;
//  3. Empty: every entry under each folder is removed, the folder gets the
//     mode and owner (made when missing; its parent must be a folder);
//  4. Write: each file is written new (a temporary file, fsync, rename)
//     with the mode and owner; a folder there is an error.
//
// No path component is followed as a symbolic link: every component before
// the last must be a folder. A path is absolute and clean, never "/".
type Delta struct {
	Schema         int      `json:"schema"`
	Remove         []string `json:"remove"`
	RemoveChildren []string `json:"remove_children"`
	Empty          []Dir    `json:"empty"`
	Write          []File   `json:"write"`
}

// ParseDelta parses clone-delta.json strictly (no unknown key, no trailing
// data) and checks it.
func ParseDelta(b []byte) (*Delta, error) {
	var d Delta
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("data after the object")
	}
	if err := d.check(); err != nil {
		return nil, err
	}
	return &d, nil
}

func (d *Delta) check() error {
	if d.Schema != 1 {
		return fmt.Errorf("schema %d, want 1", d.Schema)
	}
	var all []string
	for _, p := range d.Remove {
		all = append(all, p)
	}
	for _, p := range d.RemoveChildren {
		all = append(all, p)
	}
	for _, e := range d.Empty {
		if _, err := ParseMode(e.Mode); err != nil {
			return fmt.Errorf("empty %s: %v", e.Path, err)
		}
		all = append(all, e.Path)
	}
	for _, f := range d.Write {
		if _, err := ParseMode(f.Mode); err != nil {
			return fmt.Errorf("write %s: %v", f.Path, err)
		}
		if f.From != "build-id" {
			return fmt.Errorf("write %s: from %q (only build-id)", f.Path, f.From)
		}
		all = append(all, f.Path)
	}
	for i, p := range all {
		if !cleanAbs(p) {
			return fmt.Errorf("path %q is not absolute and clean, or is /", p)
		}
		for j, q := range all {
			if i != j && (p == q || strings.HasPrefix(q, p+"/")) {
				return fmt.Errorf("the paths %s and %s overlap", p, q)
			}
		}
	}
	return nil
}

// Equal: the same difference.
func (d *Delta) Equal(o *Delta) bool {
	a, _ := json.Marshal(d)
	b, _ := json.Marshal(o)
	return bytes.Equal(a, b)
}

// ParseMode parses 4 octal digits (at most 07777).
func ParseMode(s string) (uint32, error) {
	if len(s) != 4 {
		return 0, fmt.Errorf("mode %q is not 4 octal digits", s)
	}
	v, err := strconv.ParseUint(s, 8, 32)
	if err != nil || v > 0o7777 {
		return 0, fmt.Errorf("mode %q is not 4 octal digits", s)
	}
	return uint32(v), nil
}

func cleanAbs(p string) bool {
	return strings.HasPrefix(p, "/") && p != "/" && path.Clean(p) == p && !strings.ContainsAny(p, "\x00\n")
}

// DoneRecord is /var/lib/uc-pxx-provision/done (schema 1), written by
// uc-pxx-provision after the content check of B passed (DESIGN-R23 §3.4
// item 7).
type DoneRecord struct {
	Schema  int    `json:"schema"`
	Kind    string `json:"kind"`
	Release string `json:"release"`
	Profile string `json:"profile"`
	// BundleSHA256 is "" for the console profile.
	BundleSHA256    string `json:"bundle_sha256"`
	RefBSHA256      string `json:"ref_b_sha256"`
	InstalledSHA256 string `json:"installed_sha256"`
}

// ParseDone parses a done record strictly.
func ParseDone(b []byte) (*DoneRecord, error) {
	if len(b) == 0 || len(b) > MaxDoneRecord {
		return nil, fmt.Errorf("%d bytes (1 to %d)", len(b), MaxDoneRecord)
	}
	var r DoneRecord
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("data after the object")
	}
	switch {
	case r.Schema != 1 || r.Kind != DoneRecordKind:
		return nil, fmt.Errorf("schema %d kind %q, want 1 %q", r.Schema, r.Kind, DoneRecordKind)
	case r.Profile == "" || r.Release == "":
		return nil, errors.New("no profile or release")
	case !IsSHA256(r.RefBSHA256) || !IsSHA256(r.InstalledSHA256):
		return nil, errors.New("ref_b_sha256 or installed_sha256 is not a sha256")
	case r.BundleSHA256 != "" && !IsSHA256(r.BundleSHA256):
		return nil, errors.New("bundle_sha256 is not a sha256")
	}
	return &r, nil
}

// SHA256 is the hex sha256 of b.
func SHA256(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// IsSHA256: 64 lowercase hex digits.
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

// AttemptKey is the AUTO-STEP2 attempt key of a clone plan (DESIGN-R23
// §4.2): one automatic attempt per release and profile.
func AttemptKey(refA string) string { return "clone " + refA }
