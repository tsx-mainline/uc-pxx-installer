// Package manifest loads the firmware manifests (see package manifests).
package manifest

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"uc-pxx-installer/manifests"
)

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

// Manifest describes one official firmware.
type Manifest struct {
	Schema   int `json:"schema"`
	Firmware struct {
		Version string `json:"version"`
		File    string `json:"file"`
		Size    int64  `json:"size"`
		SHA256  string `json:"sha256"`
	} `json:"firmware"`
	Build         map[string]string `json:"build"`
	BuildPropPath string            `json:"build_prop_path"`
	Vbmeta        struct {
		ImageBytes      int    `json:"image_bytes"`
		Algorithm       int    `json:"algorithm"`
		RollbackIndex   uint64 `json:"rollback_index"`
		Flags           uint32 `json:"flags"`
		ReleaseString   string `json:"release_string"`
		PublicKeySHA1   string `json:"public_key_sha1"`
		PublicKeySHA256 string `json:"public_key_sha256"`
	} `json:"vbmeta"`
	Partitions []Part `json:"partitions"`
}

// Partitions the installer may write, in the container order.
var known = []string{"uboot", "trust", "dtbo", "vbmeta", "boot", "system", "vendor", "oem"}

func isSHA256(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32 && s == strings.ToLower(s)
}

// check rejects a manifest that the installer must not trust.
func (m *Manifest) check() error {
	if m.Schema != 1 {
		return fmt.Errorf("schema %d", m.Schema)
	}
	if m.Firmware.Version == "" || !isSHA256(m.Firmware.SHA256) || m.Firmware.Size <= 0 {
		return fmt.Errorf("firmware entry incomplete")
	}
	if m.Build["ro.build.version.incremental"] == "" {
		return fmt.Errorf("no ro.build.version.incremental")
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
	return nil
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

// Name is a short label, for example "1.0.6.20 (user)".
func (m *Manifest) Name() string {
	return fmt.Sprintf("%s (%s)", m.Firmware.Version, m.Build["ro.build.type"])
}

// Load reads every *.json manifest of fsys. An invalid manifest is an error.
func Load(fsys fs.FS) ([]*Manifest, error) {
	names, err := fs.Glob(fsys, "*.json")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var out []*Manifest
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
		out = append(out, m)
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

// ByIncremental finds the manifest of the build that a slot's build.prop
// names. The Crestron version is not in build.prop.
func ByIncremental(ms []*Manifest, incremental string) *Manifest {
	for _, m := range ms {
		if m.Build["ro.build.version.incremental"] == incremental {
			return m
		}
	}
	return nil
}
