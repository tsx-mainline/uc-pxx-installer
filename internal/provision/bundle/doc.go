// Package bundle reads the R23 profile bundle (R23-FORMATS Part 2 §6; the
// uc-pxx-linux tools/mainline-payload/bundle.py "make" and "check"): one
// uncompressed ustar tar, every member a regular file (mode 0644, uid and gid
// 0, no user or group name, mtime 0), in this order: bundle.json; the four
// signed indexes (main, community, common, uc-pxx); the .apk files sorted by
// member path; lva.tar (the kiosk-ha profile only); then the end of the
// archive (zero blocks) up to the pinned size.
//
// It holds the three things the unit needs, for the root provision service
// (internal/provision) and the uid-990 USB receiver (internal/xfer, kind
// provision) alike:
//
//   - ParseDoc: bundle.json, strictly, against what profiles.json expects;
//   - Unpacker: the stream reader. bundle.json first, its sha256 must be the
//     profile's index_sha256 before any other byte is used; then each member
//     must be the next line of bundle.json (name, size, the header fields)
//     and is written through a Sink while its sha256 is computed; the
//     sha256 of the whole stream and its size must be the pins at the end;
//     a checkpoint at the start of each member allows a resume (an HTTP
//     Range) of a cut stream;
//   - InstalledSize: the installed size of the bundle's packages, from the
//     I: fields of its own indexes (the free-space check).
//
// The package does no I/O of its own (no os, no syscall): it reads the
// bytes it is given and writes only through the Sink it is given.
package bundle

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Kind is bundle.json's "kind".
const Kind = "uc-pxx-profile-bundle"

// DocName is the first member.
const DocName = "bundle.json"

// LVAName is the member of /opt/lva (kiosk-ha).
const LVAName = "lva.tar"

// MaxDoc is the largest bundle.json the unit takes (276 packages of the
// kiosk-ha bundle are about 110 kB).
const MaxDoc = 4 << 20

// MaxMembers caps the members of a bundle.
const MaxMembers = 8192

// Repos are the four repositories of a bundle, in the order of its indexes
// and of the unit's repositories file (main, community: Alpine-signed;
// common, uc-pxx: tsx-signed).
var Repos = []string{"main", "community", "common", "uc-pxx"}

// Arch is the only architecture.
const Arch = "aarch64"

// Index is an index line of bundle.json.
type Index struct {
	Repo   string `json:"repo"`
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Package is a package line of bundle.json.
type Package struct {
	Name     string   `json:"name"`
	Version  string   `json:"version"`
	Repo     string   `json:"repo"`
	Path     string   `json:"path"`
	Size     int64    `json:"size"`
	SHA256   string   `json:"sha256"`
	Checksum string   `json:"checksum"`
	Scripts  []string `json:"scripts"`
	Triggers []string `json:"triggers"`
}

// File is lva.tar's line.
type File struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Doc is bundle.json (schema 1).
type Doc struct {
	Schema   int       `json:"schema"`
	Kind     string    `json:"kind"`
	Release  string    `json:"release"`
	Profile  string    `json:"profile"`
	Alpine   string    `json:"alpine"`
	Arch     string    `json:"arch"`
	Indexes  []Index   `json:"indexes"`
	Packages []Package `json:"packages"`
	LVA      *File     `json:"lva"`
	Add      []string  `json:"add"`
	World    []string  `json:"world"`
	Base     []string  `json:"base"`
}

// Member is one member after bundle.json.
type Member struct {
	Path   string
	Size   int64
	SHA256 string
}

// Expect is what profiles.json says of the bundle.
type Expect struct {
	Release  string
	Profile  string
	Alpine   string
	Packages int
	// LVA: the profile's steps need lva.tar (the profile-apply.sh step lva:
	// the ha apply of kiosk-ha)
	LVA bool
}

var (
	alpineForm = regexp.MustCompile(`^v[0-9]+\.[0-9]+$`)
	nameForm   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)
	verForm    = regexp.MustCompile(`^[0-9][A-Za-z0-9._+~-]*$`)
	pathForm   = regexp.MustCompile(`^v[0-9]+\.[0-9]+/(main|community|common|uc-pxx)/aarch64/[A-Za-z0-9][A-Za-z0-9._+~-]*$`)
	shaForm    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	scriptForm = regexp.MustCompile(`^\.[A-Za-z0-9._-]{1,64}$`)
	trigForm   = regexp.MustCompile(`^/[\x21-\x7e]{0,255}$`)
)

// IsSHA256: 64 lowercase hex digits.
func IsSHA256(s string) bool { return shaForm.MatchString(s) }

// ParseDoc parses bundle.json strictly (no unknown key, nothing after the
// object) and checks it against e and its own rules (bundle.py check): the
// four indexes in their order and at their paths; the packages sorted by
// path, each path the one of its name, version and repo, each once; lva.tar
// exactly when e.LVA; the names of add and world, the base lines NAME=VERSION
// sorted; no package of the bundle in the base.
// With e nil only the document's own rules are checked.
func ParseDoc(b []byte, e *Expect) (*Doc, error) {
	if len(b) == 0 || len(b) > MaxDoc {
		return nil, fmt.Errorf("bundle.json of %d bytes (1 to %d)", len(b), MaxDoc)
	}
	var d Doc
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("bundle.json: %v", err)
	}
	if dec.More() {
		return nil, errors.New("bundle.json: data after the object")
	}
	if err := d.check(e); err != nil {
		return nil, fmt.Errorf("bundle.json: %v", err)
	}
	return &d, nil
}

func (d *Doc) check(e *Expect) error {
	if e != nil {
		switch {
		case d.Release != e.Release || d.Profile != e.Profile || d.Alpine != e.Alpine:
			return fmt.Errorf("release %q profile %q alpine %q, profiles.json says %q %q %q", d.Release, d.Profile, d.Alpine,
				e.Release, e.Profile, e.Alpine)
		case len(d.Packages) != e.Packages:
			return fmt.Errorf("%d packages, profiles.json says %d", len(d.Packages), e.Packages)
		case (d.LVA != nil) != e.LVA:
			return fmt.Errorf("lva.tar: %v, the profile's steps need it: %v", d.LVA != nil, e.LVA)
		}
	}
	switch {
	case d.Schema != 1 || d.Kind != Kind || d.Arch != Arch:
		return fmt.Errorf("schema %d kind %q arch %q, want 1 %q %q", d.Schema, d.Kind, d.Arch, Kind, Arch)
	case !alpineForm.MatchString(d.Alpine) || d.Release == "" || d.Profile == "":
		return fmt.Errorf("alpine %q release %q profile %q", d.Alpine, d.Release, d.Profile)
	case len(d.Indexes) != len(Repos):
		return fmt.Errorf("%d indexes, want %d", len(d.Indexes), len(Repos))
	case len(d.Packages) == 0 || len(d.Packages)+len(d.Indexes)+2 > MaxMembers:
		return fmt.Errorf("%d packages", len(d.Packages))
	}
	for i, x := range d.Indexes {
		want := d.Alpine + "/" + Repos[i] + "/" + Arch + "/APKINDEX.tar.gz"
		if x.Repo != Repos[i] || x.Path != want || x.Size <= 0 || !IsSHA256(x.SHA256) {
			return fmt.Errorf("index %d: %+v (want %s at %s)", i, x, Repos[i], want)
		}
	}
	seen := map[string]bool{}
	names := map[string]bool{}
	for i, p := range d.Packages {
		want := d.Alpine + "/" + p.Repo + "/" + Arch + "/" + p.Name + "-" + p.Version + ".apk"
		switch {
		case !nameForm.MatchString(p.Name) || !verForm.MatchString(p.Version) || !pathForm.MatchString(p.Path) || p.Path != want:
			return fmt.Errorf("package %d: %s %s at %q (want %q)", i, p.Name, p.Version, p.Path, want)
		case seen[p.Path] || names[p.Name]:
			return fmt.Errorf("package %s twice", p.Name)
		case i > 0 && d.Packages[i-1].Path >= p.Path:
			return errors.New("the packages are not sorted by path")
		case p.Size <= 0 || !IsSHA256(p.SHA256) || !strings.HasPrefix(p.Checksum, "Q1") && !strings.HasPrefix(p.Checksum, "Q2"):
			return fmt.Errorf("package %s: size %d sha256 %q checksum %q", p.Name, p.Size, p.SHA256, p.Checksum)
		case p.Scripts == nil || p.Triggers == nil:
			return fmt.Errorf("package %s: no scripts or triggers list", p.Name)
		}
		for _, s := range p.Scripts {
			if !scriptForm.MatchString(s) {
				return fmt.Errorf("package %s: script %q", p.Name, s)
			}
		}
		for _, s := range p.Triggers {
			if !trigForm.MatchString(s) {
				return fmt.Errorf("package %s: trigger %q", p.Name, s)
			}
		}
		seen[p.Path], names[p.Name] = true, true
	}
	if d.LVA != nil && (d.LVA.Path != LVAName || d.LVA.Size <= 0 || !IsSHA256(d.LVA.SHA256)) {
		return fmt.Errorf("lva %+v", *d.LVA)
	}
	if len(d.Add) == 0 {
		return errors.New("no names to add")
	}
	for _, l := range [][]string{d.Add, d.World} {
		for _, n := range l {
			if !nameForm.MatchString(n) {
				return fmt.Errorf("the name %q", n)
			}
		}
	}
	if !sort.StringsAreSorted(d.World) || !sort.StringsAreSorted(d.Base) || len(d.Base) == 0 {
		return errors.New("world or base is not sorted, or no base")
	}
	for i, l := range d.Base {
		n, v, ok := strings.Cut(l, "=")
		if !ok || !nameForm.MatchString(n) || !verForm.MatchString(v) || i > 0 && d.Base[i-1] == l {
			return fmt.Errorf("base line %q", l)
		}
		if names[n] {
			return fmt.Errorf("the bundle holds %s, a package of the base", n)
		}
	}
	return nil
}

// Members are the members after bundle.json, in the order of the tar.
func (d *Doc) Members() []Member {
	var out []Member
	for _, x := range d.Indexes {
		out = append(out, Member{Path: x.Path, Size: x.Size, SHA256: x.SHA256})
	}
	for _, p := range d.Packages {
		out = append(out, Member{Path: p.Path, Size: p.Size, SHA256: p.SHA256})
	}
	if d.LVA != nil {
		out = append(out, Member{Path: d.LVA.Path, Size: d.LVA.Size, SHA256: d.LVA.SHA256})
	}
	return out
}

// BaseSet is the base as a map name -> version.
func (d *Doc) BaseSet() map[string]string {
	m := map[string]string{}
	for _, l := range d.Base {
		n, v, _ := strings.Cut(l, "=")
		m[n] = v
	}
	return m
}

// PackageSet is the bundle's packages as a map name -> version.
func (d *Doc) PackageSet() map[string]string {
	m := map[string]string{}
	for _, p := range d.Packages {
		m[p.Name] = p.Version
	}
	return m
}

// RepoDirs are the four repository folders relative to the bundle's root
// (the lines of the unit's repositories file, after the root folder).
func (d *Doc) RepoDirs() []string {
	var out []string
	for _, r := range Repos {
		out = append(out, d.Alpine+"/"+r)
	}
	return out
}
