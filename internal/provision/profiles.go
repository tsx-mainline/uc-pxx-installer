// Package provision is the first-boot provisioning of an R23 base slot
// (DESIGN-R23 §3; MAINLINE-RC10-2026-11-15 §6.10): uc-pxx-provision installs
// the chosen profile (kiosk-ha, kiosk or console) from its one pinned
// snapshot bundle, runs the profile steps, checks the installed package set
// and starts the profile's services (the health gate of mainline-rc-10b; the
// tree is no longer compared with the profile's ref_b) and writes the done
// record that step 2's clone needs.
//
// What it writes, and nothing else: files of the running root file system
// through the mount (by apk, by the profile script, and its own records in
// /var/lib/uc-pxx-provision), files in /run (its RAM repo in its own tmpfs,
// its info, progress and repositories files), the tty1 text, and the kernel
// read-only flag of the eMMC nodes (BLKROSET, set during apk add and the
// profile steps and cleared again; internal/blk). It never opens an eMMC node
// for writing, never writes the A/B sector, the BCB, misc, the GPT or a
// partition, and never changes TEE state (ci/check-no-writer.py
// --provision). It execs only the base's /sbin/apk (add --no-network, fix
// --reinstall: only the bundle's repositories, never /etc/apk/repositories),
// /bin/sh with the profile script, rc-service uc-pxx-usb (the gadget),
// rc-service start and status of the profile's services (services.go) and the
// pinned uc-pxx-xfer.
package provision

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"uc-pxx-installer/internal/clone"
	"uc-pxx-installer/internal/provision/bundle"
	"uc-pxx-installer/internal/xfer/handover"
)

// ProfilesKind is the "kind" of profiles.json.
const ProfilesKind = "uc-pxx-provision-profiles"

// BundleRef is a profile's bundle in profiles.json.
type BundleRef struct {
	File   string `json:"file"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Entry is a profile of profiles.json (R23-FORMATS Part 2 §7).
type Entry struct {
	Apply       string     `json:"apply"`
	BuildID     string     `json:"build_id"`
	Bundle      *BundleRef `json:"bundle"`
	IndexSHA256 *string    `json:"index_sha256"`
	Installed   string     `json:"installed_sha256"`
	Packages    int        `json:"packages"`
	RefA        string     `json:"ref_a_sha256"`
	RefB        string     `json:"ref_b_sha256"`
}

// Profiles is profiles.json of the base (/usr/share/uc-pxx-provision): the
// release's pinned list, pinned itself by the base rootfs sha256 (step 1).
type Profiles struct {
	Alpine               string           `json:"alpine"`
	Arch                 string           `json:"arch"`
	BaseVersion          string           `json:"base_version"`
	CloneDeltaSHA256     string           `json:"clone_delta_sha256"`
	ContentExcludeSHA256 string           `json:"content_exclude_sha256"`
	Kind                 string           `json:"kind"`
	Profiles             map[string]Entry `json:"profiles"`
	Release              string           `json:"release"`
	Schema               int              `json:"schema"`
	URLs                 []string         `json:"urls"`
}

// MaxProfiles is the largest profiles.json read.
const MaxProfiles = 64 << 10

var (
	applyNames  = map[string]bool{"console": true, "kiosk": true, "ha": true}
	releaseForm = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,64}$`)
	alpineForm  = regexp.MustCompile(`^v[0-9]+\.[0-9]+$`)
)

// ParseProfiles parses profiles.json strictly and checks its own rules
// (bundle.py profiles): the known profile names, the apply names (kiosk-ha
// applies "ha"), build_id "RELEASE-NAME\n", the sha256s, a bundle for every
// profile but console, the base URLs.
func ParseProfiles(b []byte) (*Profiles, error) {
	if len(b) == 0 || len(b) > MaxProfiles {
		return nil, fmt.Errorf("profiles.json of %d bytes (1 to %d)", len(b), MaxProfiles)
	}
	var p Profiles
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("profiles.json: %v", err)
	}
	if dec.More() {
		return nil, errors.New("profiles.json: data after the object")
	}
	switch {
	case p.Schema != 1 || p.Kind != ProfilesKind || p.Arch != bundle.Arch:
		return nil, fmt.Errorf("profiles.json: schema %d kind %q arch %q", p.Schema, p.Kind, p.Arch)
	case !releaseForm.MatchString(p.Release) || !alpineForm.MatchString(p.Alpine):
		return nil, fmt.Errorf("profiles.json: release %q alpine %q", p.Release, p.Alpine)
	case !clone.IsSHA256(p.CloneDeltaSHA256) || !clone.IsSHA256(p.ContentExcludeSHA256):
		return nil, errors.New("profiles.json: the exclusion list or the difference is not a sha256")
	case len(p.URLs) == 0 || len(p.Profiles) == 0:
		return nil, errors.New("profiles.json: no url or no profile")
	}
	for _, u := range p.URLs {
		if err := handover.CheckBaseURL(u); err != nil {
			return nil, fmt.Errorf("profiles.json: %v", err)
		}
	}
	for name, e := range p.Profiles {
		want := map[string]string{"console": "console", "kiosk": "kiosk", "kiosk-ha": "ha"}[name]
		switch {
		case want == "" || e.Apply != want || !applyNames[e.Apply]:
			return nil, fmt.Errorf("profiles.json: profile %q applies %q", name, e.Apply)
		case e.BuildID != p.Release+"-"+name+"\n":
			return nil, fmt.Errorf("profiles.json: profile %s: build_id %q", name, e.BuildID)
		case !clone.IsSHA256(e.RefA) || !clone.IsSHA256(e.RefB) || !clone.IsSHA256(e.Installed):
			return nil, fmt.Errorf("profiles.json: profile %s: ref_a, ref_b or installed_sha256 is not a sha256", name)
		case name == handover.ProfileConsole && (e.Bundle != nil || e.IndexSHA256 != nil || e.Packages != 0):
			return nil, errors.New("profiles.json: the console profile has a bundle")
		case name != handover.ProfileConsole && (e.Bundle == nil || e.IndexSHA256 == nil || e.Packages <= 0):
			return nil, fmt.Errorf("profiles.json: profile %s has no bundle", name)
		}
		if e.Bundle != nil {
			hp := handover.ProvisionProfile{Name: name, File: e.Bundle.File, Size: e.Bundle.Size, SHA256: e.Bundle.SHA256, IndexSHA256: *e.IndexSHA256}
			if err := hp.Check(); err != nil {
				return nil, fmt.Errorf("profiles.json: %v", err)
			}
		}
	}
	return &p, nil
}

// Profile is a profile the unit offers: its profiles.json entry, checked
// against this program's clone gate.
type Profile struct {
	Name string
	Entry
}

// HO is the profile as the receiver takes it (uc-pxx-xfer --profile).
func (p Profile) HO() handover.ProvisionProfile {
	if p.Bundle == nil {
		return handover.ProvisionProfile{Name: p.Name}
	}
	return handover.ProvisionProfile{Name: p.Name, File: p.Bundle.File, Size: p.Bundle.Size, SHA256: p.Bundle.SHA256, IndexSHA256: *p.IndexSHA256}
}

// BundleSHA256 is the bundle's sha256 ("" for console).
func (p Profile) BundleSHA256() string {
	if p.Bundle == nil {
		return ""
	}
	return p.Bundle.SHA256
}

// Expect is what the profile's bundle.json must say.
func (p Profile) Expect(ps *Profiles) *bundle.Expect {
	return &bundle.Expect{Release: ps.Release, Profile: p.Name, Alpine: ps.Alpine, Packages: p.Packages, LVA: p.Apply == "ha"}
}

// Offer checks profiles.json against this program's clone gate and release
// data (the exclusion list and the difference must be the gate's; the
// release must be the gate's) and gives the profiles the unit offers: those
// that the gate pins with the same ref_a, ref_b, build id and bundle. A
// profile the gate does not pin is left out (the note says why); a gate with
// no profile (today, until the release build fills it) offers none.
func Offer(ps *Profiles, g *clone.Gate, rel *clone.ReleaseData) ([]Profile, []string, error) {
	switch {
	case ps.ContentExcludeSHA256 != g.ExcludeSHA256():
		return nil, nil, fmt.Errorf("profiles.json names the exclusion list %s, this program's is %s", ps.ContentExcludeSHA256, g.ExcludeSHA256())
	case ps.CloneDeltaSHA256 != g.DeltaSHA256():
		return nil, nil, fmt.Errorf("profiles.json names the difference %s, this program's is %s", ps.CloneDeltaSHA256, g.DeltaSHA256())
	case len(g.Profiles) == 0:
		return nil, []string{"this program's clone gate pins no profile (the release build has not filled it): nothing is offered"}, nil
	case rel == nil || ps.Release != rel.Release:
		return nil, nil, fmt.Errorf("profiles.json is of release %q, this program's of %q", ps.Release, relName(rel))
	}
	var out []Profile
	var notes []string
	names := make([]string, 0, len(ps.Profiles))
	for n := range ps.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		e := ps.Profiles[n]
		gp, ok := g.Profile(n)
		bsha := ""
		if e.Bundle != nil {
			bsha = e.Bundle.SHA256
		}
		if !ok || gp.RefA != e.RefA || gp.RefB != e.RefB || gp.BuildID != e.BuildID || gp.BundleSHA256 != bsha {
			notes = append(notes, fmt.Sprintf("profile %s is not offered: this program's clone gate does not pin it with these ref_a, ref_b, build id and bundle", n))
			continue
		}
		out = append(out, Profile{Name: n, Entry: e})
	}
	return out, notes, nil
}

func relName(r *clone.ReleaseData) string {
	if r == nil {
		return ""
	}
	return r.Release
}

// find is the offered profile of that name.
func find(l []Profile, name string) (Profile, bool) {
	for _, p := range l {
		if p.Name == name {
			return p, true
		}
	}
	return Profile{}, false
}

// Conf is /etc/conf.d/uc-pxx-provision (uc-pxx-linux rootfs/provision).
type Conf struct {
	Profiles string // UC_PXX_PROVISION_PROFILES
	Apply    string // UC_PXX_PROVISION_APPLY
	USB      bool   // UC_PXX_PROVISION_USB=yes
}

// ConfFile is the conf of the service.
const ConfFile = "/etc/conf.d/uc-pxx-provision"

// The pinned places of the conf (a conf naming other files refuses: the
// base pins these).
const (
	ProfilesFile = "/usr/share/uc-pxx-provision/profiles.json"
	ApplyScript  = "/usr/share/uc-pxx-provision/profile-apply.sh"
)

// ParseConf reads the conf: KEY=VALUE lines (quotes taken off), comments.
// UC_PXX_PROVISION_PROFILES and UC_PXX_PROVISION_APPLY must name the base's
// own files.
func ParseConf(b []byte) (Conf, error) {
	var c Conf
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			return c, fmt.Errorf("the conf line %q", l)
		}
		v = strings.Trim(v, "\"'")
		switch k {
		case "UC_PXX_PROVISION_PROFILES":
			c.Profiles = v
		case "UC_PXX_PROVISION_APPLY":
			c.Apply = v
		case "UC_PXX_PROVISION_USB":
			c.USB = v == "yes"
		}
	}
	if c.Profiles != ProfilesFile || c.Apply != ApplyScript {
		return c, fmt.Errorf("the conf names %q and %q, not the base's %s and %s", c.Profiles, c.Apply, ProfilesFile, ApplyScript)
	}
	return c, nil
}
