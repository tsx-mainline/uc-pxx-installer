package plan

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"

	"uc-pxx-installer/internal/bootimg"
	"uc-pxx-installer/internal/manifest"
	"uc-pxx-installer/internal/pcdfw"
	"uc-pxx-installer/internal/vbmeta"
)

// ApprovedResource is the only boot-image resource ("second") that RULES.md
// allows in a boot partition: the stock resource. U-Boot reads rk-kernel.dtb
// from it right after it reads the A/B state; it is not verified that the
// tries are counted before that, so a bad resource might hang every boot.
const ApprovedResource = "290ef40577d434d702c3544076dbebd6ddd17e16087eff0e84c92549d67bb363"

// resourceHasher hashes the second stage ("resource") of an Android boot
// image while the image streams past: it reads the header from the first
// bytes, then hashes the bytes of the second region.
type resourceHasher struct {
	head      []byte
	pos       int64
	off, size int64
	ok        bool
	err       error
	h         hash.Hash
}

func (r *resourceHasher) Write(p []byte) (int, error) {
	n := len(p)
	if !r.ok && r.err == nil {
		need := bootimg.HeaderSize - len(r.head)
		if need > len(p) {
			need = len(p)
		}
		r.head = append(r.head, p[:need]...)
		if len(r.head) == bootimg.HeaderSize {
			hd, err := bootimg.Parse(r.head)
			if err != nil {
				r.err = err
			} else {
				pg := int64(hd.PageSize)
				pages := func(x uint32) int64 { return (int64(x) + pg - 1) / pg }
				r.off = pg * (1 + pages(hd.KernelSize) + pages(hd.RamdiskSize))
				r.size = int64(hd.SecondSize)
				r.ok, r.h = true, sha256.New()
			}
		}
	}
	if r.ok {
		lo, hi := r.pos, r.pos+int64(n)
		a, b := max(lo, r.off), min(hi, r.off+r.size)
		if a < b {
			r.h.Write(p[a-lo : b-lo])
		}
	}
	r.pos += int64(n)
	return n, nil
}

// ResourceHasher hashes the resource ("second") of a boot image as the image
// streams past (Write), for the executor's own check of the boot write.
type ResourceHasher struct{ r resourceHasher }

// NewResourceHasher starts one.
func NewResourceHasher() *ResourceHasher { return &ResourceHasher{} }

// Write takes the next bytes of the boot image. It never fails.
func (h *ResourceHasher) Write(p []byte) (int, error) { return h.r.Write(p) }

// Sum is the sha256 of the resource, or why there is none.
func (h *ResourceHasher) Sum() (string, error) { return h.r.sum() }

// sum is the resource sha256, or an error text.
func (r *resourceHasher) sum() (string, error) {
	switch {
	case r.err != nil:
		return "", r.err
	case !r.ok:
		return "", fmt.Errorf("boot image shorter than its header")
	case r.size == 0:
		return "", fmt.Errorf("boot image has no resource (second)")
	case r.pos < r.off+r.size:
		return "", fmt.Errorf("boot image ends inside its resource")
	}
	return hex.EncodeToString(r.h.Sum(nil)), nil
}

// PayloadPart is one partition image of a payload.
type PayloadPart struct {
	Name    string `json:"name"` // without the slot suffix, for example "boot"
	Segment int    `json:"segment"`
	SegID   string `json:"seg_id"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
	Action  string `json:"action"` // manifest action: "write" or "write-if-different"
}

// Payload is a firmware that pass 1 has checked completely.
type Payload struct {
	Kind           string        `json:"kind"` // "official"
	Version        string        `json:"version"`
	BuildType      string        `json:"build_type"`
	Incremental    string        `json:"incremental"`
	FirmwareFile   string        `json:"firmware_file"`
	FirmwareSize   int64         `json:"firmware_size"`
	FirmwareSHA256 string        `json:"firmware_sha256"`
	Parts          []PayloadPart `json:"parts"`
	Vbmeta         VbmetaInfo    `json:"vbmeta"`
	BootResource   string        `json:"boot_resource_sha256"` // sha256 of the boot image's second ("resource")
	Pass1          string        `json:"pass1"`

	file *pcdfw.File
	segs []pcdfw.Segment
}

// VbmetaInfo is what the plan records of a vbmeta image.
type VbmetaInfo struct {
	RollbackIndex uint64 `json:"rollback_index"`
	KeySHA1       string `json:"public_key_sha1"`
	Flags         uint32 `json:"flags"`
	Err           string `json:"error,omitempty"`
}

// File and Segments give the executor the opened container (pass 2).
func (p *Payload) File() *pcdfw.File            { return p.file }
func (p *Payload) Segments() []pcdfw.Segment    { return p.segs }
func (p *Payload) Part(name string) PayloadPart { return p.part(name) }

func (p *Payload) part(name string) PayloadPart {
	for _, x := range p.Parts {
		if x.Name == name {
			return x
		}
	}
	return PayloadPart{}
}

type countHash struct {
	h   hash.Hash
	n   int64
	buf *bytes.Buffer // keeps the bytes (vbmeta only)
	max int64
}

func (c *countHash) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	if c.buf != nil {
		if int64(c.buf.Len())+int64(len(p)) > c.max {
			return 0, fmt.Errorf("vbmeta image larger than %d bytes", c.max)
		}
		c.buf.Write(p)
	}
	return c.h.Write(p)
}

// VerifyOfficial is pass 1 of an official firmware: the whole file must be
// one that a manifest lists, and every partition image must decrypt and
// inflate to the size and sha256 of the manifest. Nothing is stored except
// the vbmeta image (8 KiB). progress may be nil.
func VerifyOfficial(r io.ReaderAt, size int64, k pcdfw.Keys, ms []*manifest.Manifest, progress func(string)) (*Payload, error) {
	say := func(f string, a ...any) {
		if progress != nil {
			progress(fmt.Sprintf(f, a...))
		}
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.NewSectionReader(r, 0, size)); err != nil {
		return nil, err
	}
	sha := hex.EncodeToString(h.Sum(nil))
	m := manifest.ByFirmwareSHA(ms, sha)
	if m == nil {
		return nil, fmt.Errorf("not a supported firmware (sha256 %s is in no manifest)", sha)
	}
	// Only the userdebug build is installed (owner decision 2026-10-25). The
	// other manifests are there to recognise what a slot holds.
	if m.Build["ro.build.type"] != "userdebug" {
		return nil, fmt.Errorf("firmware %s is not the userdebug build; only the userdebug build is installed", m.Name())
	}
	if m.Firmware.Size != size {
		return nil, fmt.Errorf("firmware size %d, the manifest says %d", size, m.Firmware.Size)
	}
	say("firmware %s: listed in the manifests", m.Name())
	f, err := pcdfw.Open(r, size, k)
	if err != nil {
		return nil, err
	}
	segs, err := f.Check()
	if err != nil {
		return nil, err
	}
	if len(segs) != len(m.Partitions) {
		return nil, fmt.Errorf("%d segments, the manifest lists %d", len(segs), len(m.Partitions))
	}
	p := &Payload{Kind: "official", Version: m.Firmware.Version, BuildType: m.Build["ro.build.type"],
		Incremental: m.Build["ro.build.version.incremental"], FirmwareFile: m.Firmware.File,
		FirmwareSize: size, FirmwareSHA256: sha, file: f, segs: segs}
	var vb []byte
	for _, s := range segs {
		mp, ok := m.Part(s.Name())
		if !ok {
			return nil, fmt.Errorf("segment %d (%s) is not in the manifest", s.Index, s.Name())
		}
		if mp.Segment != s.Index || mp.SegID != fmt.Sprintf("0x%04x", s.SegID) {
			return nil, fmt.Errorf("segment %d (%s, 0x%04x): the manifest says segment %d, %s",
				s.Index, s.Name(), s.SegID, mp.Segment, mp.SegID)
		}
		c := &countHash{h: sha256.New()}
		if s.Name() == "vbmeta" {
			c.buf, c.max = new(bytes.Buffer), vbmeta.MaxRead
		}
		var w io.Writer = c
		var rh *resourceHasher
		if s.Name() == "boot" {
			rh = &resourceHasher{}
			w = io.MultiWriter(c, rh)
		}
		if _, err := f.WriteSegment(s, w); err != nil {
			return nil, fmt.Errorf("%s: %w", s.Name(), err)
		}
		if rh != nil {
			if p.BootResource, err = rh.sum(); err != nil {
				return nil, fmt.Errorf("boot: %w", err)
			}
		}
		got := hex.EncodeToString(c.h.Sum(nil))
		if got != mp.SHA256 || c.n != mp.Size {
			return nil, fmt.Errorf("%s: %d bytes sha256 %s, the manifest says %d bytes %s", s.Name(), c.n, got, mp.Size, mp.SHA256)
		}
		say("pass 1: %-7s %11d bytes  sha256 %s  matches", s.Name(), c.n, got[:16])
		if c.buf != nil {
			vb = c.buf.Bytes()
		}
		p.Parts = append(p.Parts, PayloadPart{Name: s.Name(), Segment: s.Index, SegID: mp.SegID,
			Size: mp.Size, SHA256: mp.SHA256, Action: mp.Action})
	}
	if vb == nil {
		return nil, fmt.Errorf("the firmware has no vbmeta")
	}
	vi, err := vbmeta.Parse(vb)
	if err != nil {
		return nil, fmt.Errorf("payload vbmeta: %w", err)
	}
	if vi.PublicKeySHA1 != m.Vbmeta.PublicKeySHA1 || vi.PublicKeySHA256 != m.Vbmeta.PublicKeySHA256 ||
		vi.RollbackIndex != m.Vbmeta.RollbackIndex {
		return nil, fmt.Errorf("payload vbmeta key or rollback index differs from the manifest")
	}
	p.Vbmeta = VbmetaInfo{RollbackIndex: vi.RollbackIndex, KeySHA1: vi.PublicKeySHA1, Flags: vi.Flags}
	p.Pass1 = fmt.Sprintf("%d of %d partition images match the manifest", len(p.Parts), len(m.Partitions))
	return p, nil
}

// Reopen is the install's use of a payload that pass 1 checked in an earlier
// run of this boot (the plan file): the firmware is NOT hashed again
// (DEVMODE-R4.md §3, the owner's decision of 2026-10-31). It checks every
// recorded fact of the payload against the embedded manifests, opens the
// container with the key and walks its segment table (pcdfw Check: the
// headers and the first block of every body), and requires the segments the
// manifest lists. It returns a payload with the recorded facts and the opened
// container, for the executor. The bytes are checked as they are written: the
// executor hashes each partition image while it writes it and compares it
// with the manifest sha256 (and reads it back) before the target is armed.
func Reopen(r io.ReaderAt, size int64, k pcdfw.Keys, ms []*manifest.Manifest, want *Payload) (*Payload, error) {
	if want == nil || want.Kind != "official" {
		return nil, fmt.Errorf("the plan has no official payload")
	}
	m := manifest.ByFirmwareSHA(ms, want.FirmwareSHA256)
	if m == nil {
		return nil, fmt.Errorf("the plan's firmware sha256 %s is in no manifest", want.FirmwareSHA256)
	}
	if m.Build["ro.build.type"] != "userdebug" {
		return nil, fmt.Errorf("firmware %s is not the userdebug build; only the userdebug build is installed", m.Name())
	}
	if size != m.Firmware.Size || want.FirmwareSize != size {
		return nil, fmt.Errorf("firmware size %d, the plan says %d, the manifest %d", size, want.FirmwareSize, m.Firmware.Size)
	}
	if want.Version != m.Firmware.Version || want.BuildType != m.Build["ro.build.type"] ||
		want.Incremental != m.Build["ro.build.version.incremental"] || want.FirmwareFile != m.Firmware.File {
		return nil, fmt.Errorf("the plan's payload names differ from manifest %s", m.Name())
	}
	f, err := pcdfw.Open(r, size, k)
	if err != nil {
		return nil, err
	}
	segs, err := f.Check()
	if err != nil {
		return nil, err
	}
	if len(segs) != len(m.Partitions) || len(want.Parts) != len(segs) {
		return nil, fmt.Errorf("%d segments, the manifest lists %d, the plan %d", len(segs), len(m.Partitions), len(want.Parts))
	}
	for i, s := range segs {
		mp, ok := m.Part(s.Name())
		if !ok || mp.Segment != s.Index || mp.SegID != fmt.Sprintf("0x%04x", s.SegID) {
			return nil, fmt.Errorf("segment %d (%s, 0x%04x) is not the manifest's", s.Index, s.Name(), s.SegID)
		}
		got := PayloadPart{Name: s.Name(), Segment: s.Index, SegID: mp.SegID, Size: mp.Size, SHA256: mp.SHA256, Action: mp.Action}
		if want.Parts[i] != got {
			return nil, fmt.Errorf("part %d of the plan (%+v) is not the manifest's %+v", i, want.Parts[i], got)
		}
	}
	if want.Vbmeta.Err != "" || want.Vbmeta.KeySHA1 != m.Vbmeta.PublicKeySHA1 || want.Vbmeta.RollbackIndex != m.Vbmeta.RollbackIndex {
		return nil, fmt.Errorf("the plan's payload vbmeta key or rollback index differs from the manifest")
	}
	if len(want.BootResource) != 64 {
		return nil, fmt.Errorf("the plan has no boot resource sha256")
	}
	if want.Pass1 != fmt.Sprintf("%d of %d partition images match the manifest", len(want.Parts), len(m.Partitions)) {
		return nil, fmt.Errorf("the plan's pass 1 did not check every partition image (%q)", want.Pass1)
	}
	p := *want
	p.Parts = append([]PayloadPart(nil), want.Parts...)
	p.file, p.segs = f, segs
	return &p, nil
}
