package plan

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"strconv"

	"uc-pxx-installer/internal/avb"
	"uc-pxx-installer/internal/bootimg"
	"uc-pxx-installer/internal/manifest"
	"uc-pxx-installer/internal/pcdfw"
	"uc-pxx-installer/internal/vbmeta"
)

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
	// From the manifest (schema 2), checked in pass 1.
	Fingerprint  string        `json:"fingerprint"`
	BuildDateUTC int64         `json:"build_date_utc"`
	TestBuild    bool          `json:"test_build"`
	NvramModel   string        `json:"nvram_model"`
	BootChain    BootChainInfo `json:"boot_chain"`
	AVB          AVBInfo       `json:"avb"`

	file *pcdfw.File
	segs []pcdfw.Segment
	ms   []*manifest.Manifest // to name what the slots hold (plan notes)
}

// BootChainInfo is the boot chain of the payload's manifest: the uboot, trust
// and boot resource that must be written together, as one reviewed triple.
type BootChainInfo struct {
	UBootSHA256        string `json:"uboot_sha256"`
	TrustSHA256        string `json:"trust_sha256"`
	BootResourceSHA256 string `json:"boot_resource_sha256"`
	UBootBuild         string `json:"uboot_build"` // "user": the U-Boot forces "locked"
	ResourceStatus     string `json:"resource_status"`
}

// AVBInfo is what pass 1 proved with the AVB verifier (package avb).
type AVBInfo struct {
	Verified              bool     `json:"verified"` // signature, ATX chain to the pins, every descriptor and image
	PIKCertSHA256         string   `json:"pik_cert_sha256"`
	PSKCertSHA256         string   `json:"psk_cert_sha256"`
	PIKKeyVersion         uint64   `json:"pik_key_version"`
	PSKKeyVersion         uint64   `json:"psk_key_version"`
	RollbackIndex         uint64   `json:"rollback_index"`
	RollbackIndexLocation uint32   `json:"rollback_index_location"`
	Checked               []string `json:"checked"` // the images whose descriptor was recomputed
}

// VbmetaInfo is what the plan records of a vbmeta image.
type VbmetaInfo struct {
	RollbackIndex uint64 `json:"rollback_index"`
	KeySHA1       string `json:"public_key_sha1"`
	Flags         uint32 `json:"flags"`
	Err           string `json:"error,omitempty"`
	// The rollback facts (FIRMWARE-GENERAL §2.4), read without a signature
	// check: a keeper's vbmeta may be patched. ATXErr: no ATX metadata.
	Location      uint32 `json:"rollback_index_location,omitempty"`
	PIKKeyVersion uint64 `json:"pik_key_version,omitempty"`
	PSKKeyVersion uint64 `json:"psk_key_version,omitempty"`
	PIKCertSHA256 string `json:"pik_cert_sha256,omitempty"`
	ATXErr        string `json:"atx_error,omitempty"`
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

// Gate is the manifest's verdict on installing a firmware, before any byte
// of it is read: "" when it may be installed, or why not, in plain words for
// the screen. VerifyOfficial refuses with it at once (pass 1 takes minutes),
// and the plan checks the recorded facts again.
func Gate(m *manifest.Manifest) string {
	switch {
	case m.Status == manifest.StatusRefuse:
		return m.RefuseReason
	case !m.Installable():
		return "This firmware is known, but it is not approved for installing."
	case !m.ResourceApproved():
		return "This firmware is not approved for this installer yet: its start-up settings for the board and the screen were not checked."
	case !m.NvramChecked():
		return "This firmware is not approved for this installer yet: the installer has not checked how it reads the Auto Update setting."
	}
	return ""
}

// VerifyOfficial is pass 1 of an official firmware: the whole file must be
// one that a manifest lists with status install, every partition image must
// decrypt and inflate to the size and sha256 of the manifest, and the AVB
// verifier must prove it Crestron-signed: the vbmeta signature, the ATX
// chain to the pinned PIK, every hash and hashtree descriptor recomputed
// over the images, and the AVB footers. The signed facts must equal the
// manifest's. Nothing is stored except the vbmeta image (8 KiB) and the
// level-0 hashes of a hash tree (system: 14.4 MB). progress may be nil.
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
		return nil, fmt.Errorf("this firmware file is not one the installer knows: only official Crestron files with a reviewed manifest can be installed (sha256 %s is in no manifest)", sha)
	}
	if why := Gate(m); why != "" {
		return nil, fmt.Errorf("firmware %s: %s", m.Name(), why)
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
	date, _ := strconv.ParseInt(m.Build["ro.build.date.utc"], 10, 64)
	p := &Payload{Kind: "official", Version: m.Firmware.Version, BuildType: m.Build["ro.build.type"],
		Incremental: m.Build["ro.build.version.incremental"], FirmwareFile: m.Firmware.File,
		FirmwareSize: size, FirmwareSHA256: sha, file: f, segs: segs, ms: ms,
		Fingerprint: m.Build["ro.build.fingerprint"], BuildDateUTC: date, TestBuild: m.TestBuild, NvramModel: m.NvramModel,
		BootChain: chainOf(m)}
	descs := map[string]avb.Descriptor{}
	for _, d := range m.AVB.Descriptors {
		if d.Partition != "" {
			descs[d.Partition] = d
		}
	}
	checks := map[string]*avb.ImageCheck{}
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
		ws := []io.Writer{c}
		var rh *resourceHasher
		if s.Name() == "boot" {
			rh = &resourceHasher{}
			ws = append(ws, rh)
		}
		if _, covered := avb.Covered[s.Name()]; covered {
			d, ok := descs[s.Name()]
			if !ok {
				return nil, fmt.Errorf("%s: the manifest has no AVB descriptor for it", s.Name())
			}
			ic, err := avb.NewImageCheck(s.Name(), d, mp.Size)
			if err != nil {
				return nil, err
			}
			checks[s.Name()] = ic
			ws = append(ws, ic)
		}
		if _, err := f.WriteSegment(s, io.MultiWriter(ws...)); err != nil {
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
	// The AVB proof: the signed vbmeta, then each covered image against it.
	v, err := avb.Verify(vb, m.Pins())
	if err != nil {
		return nil, fmt.Errorf("not signed by Crestron, or changed: %w", err)
	}
	if err := sameAVB(m, v); err != nil {
		return nil, fmt.Errorf("the signed vbmeta differs from the manifest: %w", err)
	}
	var checked []string
	for _, s := range segs {
		if ic := checks[s.Name()]; ic != nil {
			if err := ic.Done(v); err != nil {
				return nil, fmt.Errorf("not signed by Crestron, or changed: %w", err)
			}
			checked = append(checked, s.Name())
		}
	}
	say("pass 1: AVB: vbmeta signature, ATX chain to the pinned PIK, %s recomputed: Crestron-signed", joinNames(checked))
	p.AVB = AVBInfo{Verified: true, PIKCertSHA256: v.ATX.PIK.SHA256(), PSKCertSHA256: v.ATX.PSK.SHA256(),
		PIKKeyVersion: v.ATX.PIKKeyVersion(), PSKKeyVersion: v.ATX.PSKKeyVersion(), RollbackIndex: v.RollbackIndex,
		RollbackIndexLocation: v.RollbackIndexLocation, Checked: checked}
	p.Pass1 = fmt.Sprintf("%d of %d partition images match the manifest", len(p.Parts), len(m.Partitions))
	if err := CheckRecorded(m, p); err != nil {
		return nil, err
	}
	return p, nil
}

func joinNames(l []string) string {
	s := ""
	for i, x := range l {
		if i > 0 {
			s += ", "
		}
		s += x
	}
	return s
}

func chainOf(m *manifest.Manifest) BootChainInfo {
	c := m.BootChain
	return BootChainInfo{UBootSHA256: c.UBootSHA256, TrustSHA256: c.TrustSHA256, BootResourceSHA256: c.BootResourceSHA256,
		UBootBuild: c.UBootBuild, ResourceStatus: c.ResourceStatus}
}

// sameAVB: the facts of the signed vbmeta are the manifest's, every one.
func sameAVB(m *manifest.Manifest, v *avb.Vbmeta) error {
	a := m.AVB
	switch {
	case v.ATX.PIK.SHA256() != a.PIKCertSHA256 || v.ATX.PSK.SHA256() != a.PSKCertSHA256 ||
		v.ATX.MetadataSHA256() != a.PublicKeyMetadataSHA256 || hex.EncodeToString(v.ATX.PSK.Subject) != a.PSKSubject:
		return fmt.Errorf("the ATX certificates")
	case v.ATX.PIKKeyVersion() != a.PIKKeyVersion || v.ATX.PSKKeyVersion() != a.PSKKeyVersion:
		return fmt.Errorf("the key versions %d/%d, the manifest %d/%d", v.ATX.PIKKeyVersion(), v.ATX.PSKKeyVersion(),
			a.PIKKeyVersion, a.PSKKeyVersion)
	case v.RollbackIndex != a.RollbackIndex || v.RollbackIndexLocation != a.RollbackIndexLocation || v.Flags != a.Flags:
		return fmt.Errorf("the rollback index, its location or the flags")
	case v.KeySHA1() != m.Vbmeta.PublicKeySHA1 || v.KeySHA256() != m.Vbmeta.PublicKeySHA256:
		return fmt.Errorf("the vbmeta key")
	case len(v.Descriptors) != len(a.Descriptors):
		return fmt.Errorf("%d descriptors, the manifest %d", len(v.Descriptors), len(a.Descriptors))
	}
	for i := range v.Descriptors {
		if v.Descriptors[i] != a.Descriptors[i] {
			return fmt.Errorf("descriptor %d (%s %s)", i, v.Descriptors[i].Type, v.Descriptors[i].Partition)
		}
	}
	return nil
}

// CheckRecorded checks the recorded facts of a payload (pass 1, or a plan
// file) against its manifest: the gate, the boot chain, the AVB facts. The
// install of a plan file (Reopen) must call it too.
func CheckRecorded(m *manifest.Manifest, p *Payload) error {
	if why := Gate(m); why != "" {
		return fmt.Errorf("firmware %s: %s", m.Name(), why)
	}
	a := m.AVB
	switch {
	case p.BootChain != chainOf(m):
		return fmt.Errorf("the payload's boot chain is not the manifest's")
	case p.Fingerprint != m.Build["ro.build.fingerprint"] || p.TestBuild != m.TestBuild || p.NvramModel != m.NvramModel:
		return fmt.Errorf("the payload's build facts are not the manifest's")
	case !p.AVB.Verified || p.AVB.PIKCertSHA256 != a.PIKCertSHA256 || p.AVB.PSKCertSHA256 != a.PSKCertSHA256 ||
		p.AVB.PIKKeyVersion != a.PIKKeyVersion || p.AVB.PSKKeyVersion != a.PSKKeyVersion ||
		p.AVB.RollbackIndex != a.RollbackIndex || p.AVB.RollbackIndexLocation != a.RollbackIndexLocation:
		return fmt.Errorf("the payload's AVB facts are not the manifest's, or not verified")
	case len(p.AVB.Checked) != len(avb.Covered):
		return fmt.Errorf("pass 1 recomputed %d of %d AVB-covered images", len(p.AVB.Checked), len(avb.Covered))
	}
	return nil
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
	// The gate, the boot chain and the AVB facts that pass 1 recorded must be
	// the manifest's (FIRMWARE-GENERAL §11.6): the gate is read again from the
	// embedded manifest, so a plan file of a firmware that is no longer
	// approved does not install.
	if err := CheckRecorded(m, want); err != nil {
		return nil, err
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
	if len(want.BootResource) != 64 || want.BootResource != m.BootChain.BootResourceSHA256 {
		return nil, fmt.Errorf("the plan's boot resource sha256 %q is not the manifest's", want.BootResource)
	}
	if want.Pass1 != fmt.Sprintf("%d of %d partition images match the manifest", len(want.Parts), len(m.Partitions)) {
		return nil, fmt.Errorf("the plan's pass 1 did not check every partition image (%q)", want.Pass1)
	}
	p := *want
	p.Parts = append([]PayloadPart(nil), want.Parts...)
	p.file, p.segs, p.ms = f, segs, ms
	return &p, nil
}
