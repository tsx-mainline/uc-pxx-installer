package clone

// The release build's data of the clone gate (R23; MAINLINE-RC10 record
// §12, item 3): the hook through which the release build supplies the
// profiles. Until the release build fills gate-profiles.json it holds no
// profile, and every clone plan (check clone-profile) and every provisioning
// refuses.
//
// gate-profiles.json is written by ci/r23-gate-data.py from the refs.txt of
// the R23 reference build (uc-pxx-linux rootfs/r23-refs.sh) and the sha256 of
// the uc-pxx-xfer of the base, in the canonical form of the uc-pxx-linux
// tools (json.dumps(indent=1, sort_keys=True) + "\n"):
//
//	{"kind": "uc-pxx-clone-gate-profiles", "profiles": [Profile, ...],
//	 "release": "0.2.0-rc10", "schema": 1, "xfer_sha256": "<64 hex>"}
//
// A Profile is the gate's entry of R23-FORMATS §4 ({name, build_id,
// ref_a_sha256, ref_b_sha256, bundle_sha256}). xfer_sha256 is the
// uc-pxx-xfer of the base (/usr/libexec/uc-pxx-xfer/uc-pxx-xfer):
// uc-pxx-provision runs it as root only with this sha256.
//
// The pin of uc-pxx-provision (Gate.KeeperFiles) is NOT here: uc-pxx-provision
// links this package, so a pin of its own bytes here is a fixed point. It is
// in internal/step2 (gate-keeper.json, the build of uc-pxx-step2 only), which
// is built after uc-pxx-provision (R23-FORMATS §9.1: the build order).
//
// Build order of a release (DESIGN-R23 §7.5): uc-pxx-xfer; the base tree; the
// reference trees and bundles (ref_a, ref_b; they do not depend on
// uc-pxx-provision, uc-pxx-step2, the step-2 marker or profiles.json: the
// exclusion list leaves them out); gate-profiles.json; uc-pxx-provision;
// gate-keeper.json; uc-pxx-step2; the base image.

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

//go:embed gate-profiles.json
var gateProfilesJSON []byte

// ReleaseKind is the "kind" of gate-profiles.json.
const ReleaseKind = "uc-pxx-clone-gate-profiles"

// ReleaseData is gate-profiles.json.
type ReleaseData struct {
	Kind       string    `json:"kind"`
	Profiles   []Profile `json:"profiles"`
	Release    string    `json:"release"`
	Schema     int       `json:"schema"`
	XferSHA256 string    `json:"xfer_sha256"`
}

var releaseName = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,64}$`)

// ProfileNames are the profiles a release may have (R23-FORMATS §9 item 4).
var ProfileNames = []string{"console", "kiosk", "kiosk-ha"}

func knownProfile(n string) bool {
	for _, p := range ProfileNames {
		if p == n {
			return true
		}
	}
	return false
}

// ParseRelease parses gate-profiles.json strictly. An empty release (no
// profile) has release "" and xfer_sha256 ""; a release with a profile needs
// both, and each profile is a known name with its own pins.
func ParseRelease(b []byte) (*ReleaseData, error) {
	var d ReleaseData
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("data after the object")
	}
	switch {
	case d.Schema != 1 || d.Kind != ReleaseKind:
		return nil, fmt.Errorf("schema %d kind %q, want 1 %q", d.Schema, d.Kind, ReleaseKind)
	case d.Profiles == nil:
		return nil, errors.New("no profiles list")
	case len(d.Profiles) == 0 && (d.Release != "" || d.XferSHA256 != ""):
		return nil, errors.New("a release without a profile names no release and no uc-pxx-xfer")
	case len(d.Profiles) > 0 && (!releaseName.MatchString(d.Release) || !IsSHA256(d.XferSHA256)):
		return nil, fmt.Errorf("release %q, xfer_sha256 %q", d.Release, d.XferSHA256)
	}
	seen := map[string]bool{}
	for _, p := range d.Profiles {
		switch {
		case !knownProfile(p.Name) || seen[p.Name]:
			return nil, fmt.Errorf("profile %q (unknown or twice)", p.Name)
		case !BuildIDOK(p.BuildID) || p.BuildID != d.Release+"-"+p.Name+"\n":
			return nil, fmt.Errorf("profile %s: build_id %q (want %q)", p.Name, p.BuildID, d.Release+"-"+p.Name+"\n")
		case !IsSHA256(p.RefA) || !IsSHA256(p.RefB):
			return nil, fmt.Errorf("profile %s: ref_a or ref_b is not a sha256", p.Name)
		case (p.Name == ProfileConsole) != (p.BundleSHA256 == ""):
			return nil, fmt.Errorf("profile %s: bundle_sha256 %q (console has none, every other profile one)", p.Name, p.BundleSHA256)
		case p.BundleSHA256 != "" && !IsSHA256(p.BundleSHA256):
			return nil, fmt.Errorf("profile %s: bundle_sha256 is not a sha256", p.Name)
		}
		seen[p.Name] = true
	}
	return &d, nil
}

// Release is the release data of this build (gate-profiles.json).
func Release() *ReleaseData {
	d, err := ParseRelease(gateProfilesJSON)
	if err != nil {
		panic("the embedded gate-profiles.json: " + err.Error())
	}
	return d
}

// KeeperKind is the "kind" of internal/step2's gate-keeper.json.
const KeeperKind = "uc-pxx-clone-gate-keeper"

// ParseKeeper parses gate-keeper.json strictly: the pins of the files of
// B's root that the content check leaves out and the plan checks by their
// own pins (only uc-pxx-provision).
func ParseKeeper(b []byte) ([]PathPin, error) {
	var d struct {
		KeeperFiles []PathPin `json:"keeper_files"`
		Kind        string    `json:"kind"`
		Schema      int       `json:"schema"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("data after the object")
	}
	if d.Schema != 1 || d.Kind != KeeperKind || d.KeeperFiles == nil {
		return nil, fmt.Errorf("schema %d kind %q, want 1 %q with a keeper_files list", d.Schema, d.Kind, KeeperKind)
	}
	for _, f := range d.KeeperFiles {
		if f.Path != ProvisionBinary || f.Size <= 0 || !IsSHA256(f.SHA256) {
			return nil, fmt.Errorf("keeper file %+v (only %s, by size and sha256)", f, ProvisionBinary)
		}
	}
	if len(d.KeeperFiles) > 1 {
		return nil, errors.New("more than one pin")
	}
	return d.KeeperFiles, nil
}
