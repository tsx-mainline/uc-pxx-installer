package fakeemmc

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"path/filepath"
	"strings"
	"testing/fstest"

	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/manifest"
	"uc-pxx-installer/internal/nvram/nvramtest"
	"uc-pxx-installer/internal/pcdfw"
	"uc-pxx-installer/internal/pcdfw/pcdfwtest"
)

// SmallLayout is a small disk with the UC-P10 partition names. The start of
// the disk is as on the unit (uboot_a at 16384, misc at 49152, so the A/B
// sector is LBA 49156); the slot partitions are small. others (the nvram
// store of stock Android) has just room for the nvram ring.
const SmallLayout = `disk 90496
1 uboot_a 16384 512
2 uboot_b 24576 512
3 trust_a 32768 512
4 trust_b 40960 512
5 misc 49152 8192
6 resource 57344 512
9 dtbo_a 57856 256
10 dtbo_b 58112 256
11 vbmeta_a 58368 64
12 vbmeta_b 58432 64
13 boot_a 58496 512
14 boot_b 59008 512
16 security 59520 64
20 cfg 59584 64
22 system_a 59648 6144
23 system_b 65792 6144
24 vendor_a 71936 1024
25 vendor_b 72960 1024
26 oem_a 73984 1024
27 oem_b 75008 1024
28 userdata 76032 2048
21 others 78080 8192
15 backup 86272 4096
`

// ParseSmallLayout parses SmallLayout (or a changed copy of it).
func ParseSmallLayout(s string) *gpt.Layout {
	l, err := gpt.ReadLayout(strings.NewReader(s))
	if err != nil {
		panic(err)
	}
	return l
}

// Fixture is a made-up firmware with its manifest, and made-up old slot
// contents. It holds no vendor byte and no vendor key.
type Fixture struct {
	Keys      pcdfw.Keys
	FW        []byte
	Manifest  []byte
	Manifests []*manifest.Manifest
	New       map[string][]byte // the payload images, by partition name
	Old       map[string][]byte // other images (what a slot held before)
	Key       []byte            // the made-up AVB public key
	Resource  string            // sha256 of the payload boot image's resource (second)
}

// bootImage makes an Android boot image (header v0, page 2048) of n bytes
// with a made-up kernel, ramdisk and resource. It returns the image and the
// sha256 of the resource.
func bootImage(seed int64, n int) ([]byte, string) {
	const pg = 2048
	ks, rs, ss := 100<<10, 50<<10, 20<<10
	b := fill(seed, "", n)
	copy(b, "ANDROID!")
	le := binary.LittleEndian
	le.PutUint32(b[8:], uint32(ks))
	le.PutUint32(b[16:], uint32(rs))
	le.PutUint32(b[24:], uint32(ss))
	le.PutUint32(b[36:], pg)
	le.PutUint32(b[40:], 0)
	copy(b[48:64], make([]byte, 16))
	copy(b[64:576], make([]byte, 512))
	copy(b[608:1632], make([]byte, 1024))
	off := pg * (1 + (ks+pg-1)/pg + (rs+pg-1)/pg)
	return b, shaHex(b[off : off+ss])
}

// FixtureOpt changes the fixture.
type FixtureOpt struct {
	Rollback uint64 // rollback index of the payload vbmeta
	OldKey   bool   // give the old vbmetas another AVB key
	OldRB    uint64 // rollback index of the old vbmetas
}

var order = []struct {
	name    string
	id      uint16
	magic   string
	size    int
	chunked bool
	action  string
}{
	{"uboot", 0xfd02, "LOADER  ", 256 << 10, false, "write"},
	{"trust", 0xfd03, "BL3X", 128 << 10, false, "write-if-different"},
	{"dtbo", 0xfe01, "\xd7\xb7\xab\x1e", 64 << 10, false, "write"},
	{"vbmeta", 0xfe02, "", 8192, false, "write"},
	{"boot", 0xfe04, "ANDROID!", 200 << 10, false, "write"},
	{"system", 0xfe10, "", 2560 << 10, true, "write"},
	{"vendor", 0xfe06, "", 256 << 10, true, "write"},
	{"oem", 0xfe08, "", 384 << 10, true, "write"},
}

func fill(seed int64, magic string, n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	copy(b, magic)
	return b
}

// NewFixture builds the fixture.
func NewFixture(o FixtureOpt) *Fixture {
	fx := &Fixture{Keys: pcdfwtest.Keys(), New: map[string][]byte{}, Old: map[string][]byte{}}
	fx.Key = fill(99, "test-avb-key", 1032)
	oldKey := fx.Key
	if o.OldKey {
		oldKey = fill(98, "other-avb-key", 1032)
	}
	var specs []pcdfwtest.Spec
	for i, p := range order {
		img := fill(int64(100+i), p.magic, p.size)
		old := fill(int64(200+i), p.magic, p.size)
		if p.name == "vbmeta" {
			img = pcdfwtest.Vbmeta(p.size, fx.Key, o.Rollback)
			old = pcdfwtest.Vbmeta(p.size, oldKey, o.OldRB)
		}
		if p.name == "boot" {
			img, fx.Resource = bootImage(int64(100+i), p.size)
			old, _ = bootImage(int64(200+i), p.size)
		}
		fx.New[p.name], fx.Old[p.name] = img, old
		specs = append(specs, pcdfwtest.Spec{ID: p.id, Image: img, Chunked: p.chunked})
	}
	fx.FW = pcdfwtest.Build(fx.Keys, specs)
	fx.Manifest = fx.manifest(o.Rollback)
	ms, err := manifest.Load(fstest.MapFS{"test.json": {Data: fx.Manifest}})
	if err != nil {
		panic(err)
	}
	fx.Manifests = ms
	return fx
}

func shaHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func (fx *Fixture) manifest(rb uint64) []byte {
	type part struct {
		Segment   int    `json:"segment"`
		SegID     string `json:"seg_id"`
		Partition string `json:"partition"`
		Image     string `json:"image"`
		Size      int64  `json:"size"`
		SHA256    string `json:"sha256"`
		Action    string `json:"action"`
	}
	var parts []part
	for i, p := range order {
		parts = append(parts, part{i, fmt.Sprintf("0x%04x", p.id), p.name, fmt.Sprintf("%02d_%s.img", i, p.name),
			int64(len(fx.New[p.name])), shaHex(fx.New[p.name]), p.action})
	}
	s1 := sha1.Sum(fx.Key)
	m := map[string]any{
		"schema":          1,
		"firmware":        map[string]any{"version": "9.9.9.1", "file": "test-fw.bin", "size": len(fx.FW), "sha256": shaHex(fx.FW)},
		"build":           map[string]string{"ro.build.type": "userdebug", "ro.build.version.incremental": "999"},
		"build_prop_path": "/system/build.prop",
		"vbmeta": map[string]any{"image_bytes": 8192, "algorithm": 2, "rollback_index": rb, "flags": 0,
			"release_string": "avbtool test", "public_key_sha1": hex.EncodeToString(s1[:]), "public_key_sha256": shaHex(fx.Key)},
		"partitions": parts,
	}
	b, _ := json.MarshalIndent(m, "", " ")
	return b
}

// Unit describes the made-up unit before the install.
type Unit struct {
	Layout string     // "" = SmallLayout
	AB     [2][3]byte // slot a, slot b
	// Contents of each slot: "old", "new" (the payload images) or "" (empty).
	A, B string
	// Tweak changes partition contents: name -> bytes (applied last). By
	// default others holds a made-up nvram store with auto update firmware
	// off (nvramtest.Off); Tweak["others"] replaces it.
	Tweak map[string][]byte
}

// Image creates the unit image in dir and returns its path.
func (fx *Fixture) Image(dir, name string, u Unit) (string, error) {
	ls := u.Layout
	if ls == "" {
		ls = SmallLayout
	}
	l := ParseSmallLayout(ls)
	fits := func(name string, n int) bool {
		for _, e := range l.Entries {
			if e.Name == name {
				return int64(n) <= e.Size*gpt.Sector
			}
		}
		return false
	}
	fb := map[string][]byte{}
	for slot, what := range map[string]string{"a": u.A, "b": u.B} {
		src := map[string]map[string][]byte{"old": fx.Old, "new": fx.New}[what]
		for n, b := range src {
			if fits(n+"_"+slot, len(b)) { // a broken test layout may lack room
				fb[n+"_"+slot] = b
			}
		}
	}
	if fits("others", len(nvramtest.Off())) {
		fb["others"] = nvramtest.Off()
	}
	if fits("backup", 512) {
		fb["backup"] = make([]byte, 512) // as on unit 2 (KEY-KEEP §9): all zero, no marker
	}
	for n, b := range u.Tweak {
		fb[n] = b
	}
	path := filepath.Join(dir, name)
	return path, Create(path, Spec{Layout: l, AB: u.AB, FillBytes: fb, Markers: true})
}

// Equal reports equal byte slices (helper for tests).
func Equal(a, b []byte) bool { return bytes.Equal(a, b) }

// ManifestPartSHA is the sha256 of a payload image, as the manifest lists it.
func (fx *Fixture) ManifestPartSHA(name string) string { return shaHex(fx.New[name]) }
