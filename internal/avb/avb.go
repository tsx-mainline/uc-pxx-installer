// Package avb checks that a UC-P10 firmware is signed by Crestron, offline,
// with the Go standard library only (plan FIRMWARE-GENERAL §1.4, §6.1).
//
// It checks the signed vbmeta image (the header, the SHA256_RSA4096
// signature with the key it carries, every descriptor), the Android Things
// (ATX) certificate chain in the public key metadata up to a pinned PIK
// certificate, and, as the partition images stream past (ImageCheck), every
// hash and hashtree descriptor and the AVB footer of each covered image. It
// refuses what no known UC-P10 firmware has: a chain or property descriptor,
// an unknown descriptor, a descriptor for a partition the installer does not
// write, vbmeta flags other than 0, a rollback index location other than 0,
// another algorithm.
//
// The root of trust of a unit is the product root key in its ATX permanent
// attributes. The installer cannot read them, so it pins the PIK certificate
// that a locked UC-P10 U-Boot accepted (Pins). The rules are the same as in
// tools/fw-manifest.py, which is a separate implementation.
//
// It only reads. It never writes, and it opens no file.
package avb

import (
	"bytes"
	"crypto"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

// Pins are the trust anchors of the ATX chain: the sha256 of the whole PIK
// certificate and the PSK subject (sha256 of the product id).
type Pins struct {
	PIKCertSHA256 string
	PSKSubject    string
}

// The Crestron UC-P10 pins (FIRMWARE-GENERAL §1.2, §1.3; the same in all 37
// public firmware files, FIRMWARE-ALL37 §0). A new PIK needs a reviewed
// change here.
const (
	CrestronPIKCertSHA256 = "dbd1313e5617c949e383943129415230d14054301d25c2fec7954ca4e4d8f2d4"
	CrestronPSKSubject    = "f08d249f209eae1961d0c2dc936148cd8fa989a29e2b93628b0c28f9a38d6538"
)

// CrestronPins returns the pins of the real firmware.
func CrestronPins() Pins {
	return Pins{PIKCertSHA256: CrestronPIKCertSHA256, PSKSubject: CrestronPSKSubject}
}

// Format constants (libavb, libavb_atx).
const (
	HeaderSize    = 256
	FooterSize    = 64
	SHA256RSA4096 = 2
	rsaBytes      = 512
	KeySize       = 8 + 2*rsaBytes // AvbRSAPublicKeyHeader + n + rr
	Block         = 4096

	tagProperty = 0
	tagHashtree = 1
	tagHash     = 2
	tagCmdline  = 3
	tagChain    = 4

	hashtreeFixed = 164
	hashFixed     = 116
	cmdlineFixed  = 8
	cmdlineFlags  = 0x3

	atxSignedSize   = 4 + KeySize + 32 + 32 + 8 // version, key, subject, usage, key version
	atxCertSize     = atxSignedSize + rsaBytes
	ATXMetadataSize = 4 + 2*atxCertSize
)

// Covered is the descriptor kind that the vbmeta must have for each
// partition, and no other partition may have one.
var Covered = map[string]string{"boot": "hash", "dtbo": "hash", "system": "hashtree", "vendor": "hashtree"}

// dmPartition is the partition whose dm-verity table is on the kernel command
// line (system as root).
const dmPartition = "system"

var hashSizes = map[string]int{"sha1": 20, "sha256": 32, "sha512": 64}

func newHash(alg string) hash.Hash {
	switch alg {
	case "sha1":
		return sha1.New()
	case "sha256":
		return sha256.New()
	case "sha512":
		return sha512.New()
	}
	return nil
}

var (
	usagePIK = sha256.Sum256([]byte("com.google.android.things.vboot.ca"))
	usagePSK = sha256.Sum256([]byte("com.google.android.things.vboot"))
)

// Descriptor is one decoded descriptor. Its JSON form is the one of the
// manifests (tools/fw-manifest.py, schema 2): a hash and a hashtree
// descriptor have no flags (they must be 0), a kernel command line has no
// partition.
type Descriptor struct {
	Type          string `json:"type"` // hash, hashtree, kernel_cmdline
	Partition     string `json:"partition,omitempty"`
	ImageSize     uint64 `json:"image_size,omitempty"`
	TreeOffset    uint64 `json:"tree_offset,omitempty"`
	TreeSize      uint64 `json:"tree_size,omitempty"`
	DataBlockSize uint32 `json:"data_block_size,omitempty"`
	HashBlockSize uint32 `json:"hash_block_size,omitempty"`
	FECNumRoots   uint32 `json:"fec_num_roots,omitempty"`
	FECOffset     uint64 `json:"fec_offset,omitempty"`
	FECSize       uint64 `json:"fec_size,omitempty"`
	HashAlgorithm string `json:"hash_algorithm,omitempty"`
	Salt          string `json:"salt,omitempty"`
	RootDigest    string `json:"root_digest,omitempty"`
	Digest        string `json:"digest,omitempty"`
	Flags         uint32 `json:"flags,omitempty"` // kernel_cmdline only
	Cmdline       string `json:"cmdline,omitempty"`
}

// Header is the decoded AvbVBMetaImageHeader.
type Header struct {
	Major, Minor          uint32
	AuthSize, AuxSize     uint64
	Algorithm             uint32
	HashOffset, HashSize  uint64
	SigOffset, SigSize    uint64
	PKOffset, PKSize      uint64
	PKMOffset, PKMSize    uint64
	DescOffset, DescSize  uint64
	RollbackIndex         uint64
	Flags                 uint32
	RollbackIndexLocation uint32
	Release               string
	Total                 uint64
}

// Vbmeta is a parsed vbmeta image.
type Vbmeta struct {
	Header
	PublicKey   []byte
	PKMetadata  []byte
	Descriptors []Descriptor
	Raw         [][]byte // each descriptor's bytes, as in the image
	ATX         *ATX     // set by Verify
}

// KeySHA1 and KeySHA256 are the hashes of the public key blob.
func (v *Vbmeta) KeySHA1() string   { s := sha1.Sum(v.PublicKey); return hex.EncodeToString(s[:]) }
func (v *Vbmeta) KeySHA256() string { s := sha256.Sum256(v.PublicKey); return hex.EncodeToString(s[:]) }

// Find returns the hash or hashtree descriptor of a partition.
func (v *Vbmeta) Find(part string) (Descriptor, bool) {
	for _, d := range v.Descriptors {
		if d.Partition == part && (d.Type == "hash" || d.Type == "hashtree") {
			return d, true
		}
	}
	return Descriptor{}, false
}

func failf(f string, a ...any) error { return errors.New(fmt.Sprintf(f, a...)) }

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// cstring is a NUL-terminated printable ASCII string in a fixed field; the
// rest of the field must be zero.
func cstring(b []byte, what string) (string, error) {
	i := bytes.IndexByte(b, 0)
	if i < 0 {
		return "", failf("%s: not NUL-terminated", what)
	}
	if !allZero(b[i:]) {
		return "", failf("%s: bytes after the NUL are not zero", what)
	}
	for _, c := range b[:i] {
		if c < 0x20 || c >= 0x7f {
			return "", failf("%s: not printable ASCII", what)
		}
	}
	return string(b[:i]), nil
}

// ParseHeader decodes and checks the header and the block layout. It does no
// cryptography.
func ParseHeader(b []byte, what string) (Header, error) {
	var h Header
	if len(b) < HeaderSize {
		return h, failf("%s: %d bytes, shorter than the vbmeta header", what, len(b))
	}
	if string(b[:4]) != "AVB0" {
		return h, failf("%s: no AVB0 magic", what)
	}
	be := binary.BigEndian
	h.Major, h.Minor = be.Uint32(b[4:]), be.Uint32(b[8:])
	h.AuthSize, h.AuxSize = be.Uint64(b[12:]), be.Uint64(b[20:])
	h.Algorithm = be.Uint32(b[28:])
	h.HashOffset, h.HashSize = be.Uint64(b[32:]), be.Uint64(b[40:])
	h.SigOffset, h.SigSize = be.Uint64(b[48:]), be.Uint64(b[56:])
	h.PKOffset, h.PKSize = be.Uint64(b[64:]), be.Uint64(b[72:])
	h.PKMOffset, h.PKMSize = be.Uint64(b[80:]), be.Uint64(b[88:])
	h.DescOffset, h.DescSize = be.Uint64(b[96:]), be.Uint64(b[104:])
	h.RollbackIndex = be.Uint64(b[112:])
	h.Flags = be.Uint32(b[120:])
	h.RollbackIndexLocation = be.Uint32(b[124:])
	var err error
	if h.Release, err = cstring(b[128:176], what+": release string"); err != nil {
		return h, err
	}
	if !allZero(b[176:HeaderSize]) {
		return h, failf("%s: reserved header bytes are not zero", what)
	}
	if h.Major != 1 || h.Minor != 0 {
		return h, failf("%s: needs libavb %d.%d, not 1.0 (what the UC-P10 U-Boots were checked with)", what, h.Major, h.Minor)
	}
	const lim = 1 << 20
	if h.AuthSize > lim || h.AuxSize > lim || h.AuthSize%64 != 0 || h.AuxSize%64 != 0 {
		return h, failf("%s: block sizes %d/%d are not valid", what, h.AuthSize, h.AuxSize)
	}
	h.Total = HeaderSize + h.AuthSize + h.AuxSize
	if h.Total > uint64(len(b)) {
		return h, failf("%s: truncated: the header says %d bytes, the image has %d", what, h.Total, len(b))
	}
	for _, r := range []struct {
		n      string
		o, s   uint64
		within uint64
	}{{"hash", h.HashOffset, h.HashSize, h.AuthSize}, {"signature", h.SigOffset, h.SigSize, h.AuthSize},
		{"public key", h.PKOffset, h.PKSize, h.AuxSize}, {"public key metadata", h.PKMOffset, h.PKMSize, h.AuxSize},
		{"descriptors", h.DescOffset, h.DescSize, h.AuxSize}} {
		if r.o > r.within || r.s > r.within-r.o {
			return h, failf("%s: the %s is outside its block", what, r.n)
		}
	}
	return h, nil
}

func (h Header) aux(b []byte) []byte  { return b[HeaderSize+h.AuthSize : h.Total] }
func (h Header) auth(b []byte) []byte { return b[HeaderSize : HeaderSize+h.AuthSize] }

// parseKey checks an AvbRSAPublicKeyHeader blob of an RSA-4096 key and returns
// the modulus.
func parseKey(blob []byte, what string) (*big.Int, error) {
	if len(blob) != KeySize {
		return nil, failf("%s: public key is %d bytes, not %d (RSA-4096)", what, len(blob), KeySize)
	}
	be := binary.BigEndian
	bits, n0inv := be.Uint32(blob), be.Uint32(blob[4:])
	if bits != 4096 {
		return nil, failf("%s: public key has %d bits, not 4096", what, bits)
	}
	n := new(big.Int).SetBytes(blob[8 : 8+rsaBytes])
	rr := new(big.Int).SetBytes(blob[8+rsaBytes:])
	if n.BitLen() != 4096 || n.Bit(0) == 0 {
		return nil, failf("%s: public key modulus is not a 4096-bit odd number", what)
	}
	if uint32(n.Uint64())*n0inv+1 != 0 {
		return nil, failf("%s: public key n0inv does not match the modulus", what)
	}
	want := new(big.Int).Mod(new(big.Int).Lsh(big.NewInt(1), 2*4096), n) // R^2 mod n, R = 2^4096
	if rr.Cmp(want) != 0 {
		return nil, failf("%s: public key rr does not match the modulus", what)
	}
	return n, nil
}

// verifyRSA is PKCS#1 v1.5 with the public exponent 65537, as in libavb.
func verifyRSA(n *big.Int, sig, digest []byte, h crypto.Hash, what string) error {
	if len(sig) != rsaBytes {
		return failf("%s: signature is %d bytes, not %d", what, len(sig), rsaBytes)
	}
	if err := rsa.VerifyPKCS1v15(&rsa.PublicKey{N: n, E: 65537}, h, digest, sig); err != nil {
		return failf("%s: signature does not verify", what)
	}
	return nil
}

// splitDescriptors cuts the descriptor block into (tag, body, raw).
func splitDescriptors(d []byte, what string) (tags []uint64, bodies, raws [][]byte, err error) {
	be := binary.BigEndian
	for pos := 0; pos < len(d); {
		if len(d)-pos < 16 {
			return nil, nil, nil, failf("%s: descriptor header truncated at %d", what, pos)
		}
		tag, nbf := be.Uint64(d[pos:]), be.Uint64(d[pos+8:])
		if nbf%8 != 0 {
			return nil, nil, nil, failf("%s: descriptor at %d: size %d is not a multiple of 8", what, pos, nbf)
		}
		if nbf > uint64(len(d)-pos-16) {
			return nil, nil, nil, failf("%s: descriptor at %d runs past the descriptor block", what, pos)
		}
		end := pos + 16 + int(nbf)
		tags, bodies, raws = append(tags, tag), append(bodies, d[pos+16:end]), append(raws, d[pos:end])
		pos = end
	}
	return tags, bodies, raws, nil
}

// take returns the variable fields after a fixed part; the descriptor must be
// exactly their size padded with zeros to 8 bytes.
func take(body []byte, fixed int, lengths []uint32, what string) ([][]byte, error) {
	need := uint64(fixed)
	for _, n := range lengths {
		need += uint64(n)
	}
	if (need+7)/8*8 != uint64(len(body)) {
		return nil, failf("%s: descriptor size %d does not match its fields (%d)", what, len(body), need)
	}
	if !allZero(body[need:]) {
		return nil, failf("%s: descriptor padding is not zero", what)
	}
	var out [][]byte
	pos := uint64(fixed)
	for _, n := range lengths {
		out = append(out, body[pos:pos+uint64(n)])
		pos += uint64(n)
	}
	return out, nil
}

var partRE = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

func partName(b []byte, what string) (string, error) {
	if !partRE.Match(b) {
		return "", failf("%s: bad partition name %q", what, b)
	}
	return string(b), nil
}

func hashAlg(field []byte, what string) (string, error) {
	s, err := cstring(field, what+": hash algorithm")
	if err != nil {
		return "", err
	}
	if _, ok := hashSizes[s]; !ok {
		return "", failf("%s: hash algorithm %q is not supported", what, s)
	}
	return s, nil
}

func decodeDescriptor(tag uint64, body []byte, what string) (Descriptor, error) {
	be := binary.BigEndian
	switch tag {
	case tagHashtree:
		if len(body) < hashtreeFixed {
			return Descriptor{}, failf("%s: hashtree descriptor truncated", what)
		}
		d := Descriptor{Type: "hashtree", ImageSize: be.Uint64(body[4:]), TreeOffset: be.Uint64(body[12:]),
			TreeSize: be.Uint64(body[20:]), DataBlockSize: be.Uint32(body[28:]), HashBlockSize: be.Uint32(body[32:]),
			FECNumRoots: be.Uint32(body[36:]), FECOffset: be.Uint64(body[40:]), FECSize: be.Uint64(body[48:])}
		ver := be.Uint32(body)
		nl, sl, rl, flags := be.Uint32(body[88:]), be.Uint32(body[92:]), be.Uint32(body[96:]), be.Uint32(body[100:])
		f, err := take(body, hashtreeFixed, []uint32{nl, sl, rl}, what+": hashtree")
		if err != nil {
			return d, err
		}
		if d.Partition, err = partName(f[0], what); err != nil {
			return d, err
		}
		w := what + ": hashtree " + d.Partition
		if d.HashAlgorithm, err = hashAlg(body[56:88], w); err != nil {
			return d, err
		}
		if !allZero(body[104:164]) {
			return d, failf("%s: reserved bytes are not zero", w)
		}
		if len(f[2]) != hashSizes[d.HashAlgorithm] {
			return d, failf("%s: root digest is %d bytes, not %d for %s", w, len(f[2]), hashSizes[d.HashAlgorithm], d.HashAlgorithm)
		}
		if ver != 1 || flags != 0 {
			return d, failf("%s: dm-verity version %d, flags 0x%x", w, ver, flags)
		}
		d.Salt, d.RootDigest = hex.EncodeToString(f[1]), hex.EncodeToString(f[2])
		return d, nil
	case tagHash:
		if len(body) < hashFixed {
			return Descriptor{}, failf("%s: hash descriptor truncated", what)
		}
		d := Descriptor{Type: "hash", ImageSize: be.Uint64(body)}
		nl, sl, dl, flags := be.Uint32(body[40:]), be.Uint32(body[44:]), be.Uint32(body[48:]), be.Uint32(body[52:])
		f, err := take(body, hashFixed, []uint32{nl, sl, dl}, what+": hash")
		if err != nil {
			return d, err
		}
		if d.Partition, err = partName(f[0], what); err != nil {
			return d, err
		}
		w := what + ": hash " + d.Partition
		if d.HashAlgorithm, err = hashAlg(body[8:40], w); err != nil {
			return d, err
		}
		if !allZero(body[56:116]) {
			return d, failf("%s: reserved bytes are not zero", w)
		}
		if len(f[2]) != hashSizes[d.HashAlgorithm] {
			return d, failf("%s: digest is %d bytes, not %d for %s", w, len(f[2]), hashSizes[d.HashAlgorithm], d.HashAlgorithm)
		}
		if flags != 0 {
			return d, failf("%s: flags 0x%x", w, flags)
		}
		d.Salt, d.Digest = hex.EncodeToString(f[1]), hex.EncodeToString(f[2])
		return d, nil
	case tagCmdline:
		if len(body) < cmdlineFixed {
			return Descriptor{}, failf("%s: kernel cmdline descriptor truncated", what)
		}
		flags, cl := be.Uint32(body), be.Uint32(body[4:])
		f, err := take(body, cmdlineFixed, []uint32{cl}, what+": kernel cmdline")
		if err != nil {
			return Descriptor{}, err
		}
		if flags&^cmdlineFlags != 0 || flags == cmdlineFlags {
			return Descriptor{}, failf("%s: kernel cmdline flags 0x%x", what, flags)
		}
		for _, c := range f[0] {
			if c < 0x20 || c >= 0x7f {
				return Descriptor{}, failf("%s: kernel cmdline is not printable ASCII", what)
			}
		}
		return Descriptor{Type: "kernel_cmdline", Flags: flags, Cmdline: string(f[0])}, nil
	case tagChain:
		return Descriptor{}, failf("%s: chain partition descriptor: not allowed (no known UC-P10 firmware has one)", what)
	case tagProperty:
		return Descriptor{}, failf("%s: property descriptor: not allowed (no known UC-P10 firmware has one)", what)
	}
	return Descriptor{}, failf("%s: unknown descriptor tag %d", what, tag)
}

// Parse decodes a vbmeta image structurally: the header, the key and
// metadata bytes, every descriptor. It does no cryptography. A chain,
// property or unknown descriptor is an error.
func Parse(b []byte, what string) (*Vbmeta, error) {
	h, err := ParseHeader(b, what)
	if err != nil {
		return nil, err
	}
	aux := h.aux(b)
	v := &Vbmeta{Header: h, PublicKey: aux[h.PKOffset : h.PKOffset+h.PKSize],
		PKMetadata: aux[h.PKMOffset : h.PKMOffset+h.PKMSize]}
	tags, bodies, raws, err := splitDescriptors(aux[h.DescOffset:h.DescOffset+h.DescSize], what)
	if err != nil {
		return nil, err
	}
	for i := range tags {
		d, err := decodeDescriptor(tags[i], bodies[i], fmt.Sprintf("%s descriptor %d", what, i))
		if err != nil {
			return nil, err
		}
		v.Descriptors = append(v.Descriptors, d)
	}
	v.Raw = raws
	return v, nil
}

// Verify checks the signed vbmeta partition image of a firmware completely:
// the layout, the signature, flags 0, rollback index location 0, the ATX
// chain to pins, the descriptor set and the dm-verity table. The images that
// the descriptors cover are checked by ImageCheck.
func Verify(b []byte, pins Pins) (*Vbmeta, error) {
	const what = "vbmeta"
	v, err := Parse(b, what)
	if err != nil {
		return nil, err
	}
	h := v.Header
	if !allZero(b[h.Total:]) {
		return nil, failf("%s: bytes after the vbmeta blocks are not zero", what)
	}
	if h.Algorithm != SHA256RSA4096 {
		return nil, failf("%s: algorithm %d, not SHA256_RSA4096 (2)", what, h.Algorithm)
	}
	if h.HashSize != 32 || h.SigSize != rsaBytes {
		return nil, failf("%s: hash or signature size does not fit SHA256_RSA4096", what)
	}
	auth := h.auth(b)
	used := append([]byte(nil), auth...)
	copy(used[h.HashOffset:h.HashOffset+32], make([]byte, 32))
	copy(used[h.SigOffset:h.SigOffset+rsaBytes], make([]byte, rsaBytes))
	if !allZero(used) {
		return nil, failf("%s: bytes in the authentication block outside the hash and the signature are not zero", what)
	}
	hs := sha256.New()
	hs.Write(b[:HeaderSize])
	hs.Write(h.aux(b))
	digest := hs.Sum(nil)
	if !bytes.Equal(auth[h.HashOffset:h.HashOffset+32], digest) {
		return nil, failf("%s: the hash over the header and the auxiliary block does not match", what)
	}
	n, err := parseKey(v.PublicKey, what)
	if err != nil {
		return nil, err
	}
	if err := verifyRSA(n, auth[h.SigOffset:h.SigOffset+h.SigSize], digest, crypto.SHA256, what); err != nil {
		return nil, err
	}
	if h.Flags != 0 {
		return nil, failf("%s: flags 0x%x (verification or hashtree disabled)", what, h.Flags)
	}
	if h.RollbackIndexLocation != 0 {
		return nil, failf("%s: rollback index location %d, not 0", what, h.RollbackIndexLocation)
	}
	a, err := ParseATX(v.PKMetadata)
	if err != nil {
		return nil, err
	}
	if err := a.Check(v.PublicKey, pins); err != nil {
		return nil, err
	}
	v.ATX = a
	if err := checkDescriptorSet(v); err != nil {
		return nil, err
	}
	return v, nil
}

func checkDescriptorSet(v *Vbmeta) error {
	seen := map[string]string{}
	var cmdlines []Descriptor
	for _, d := range v.Descriptors {
		switch d.Type {
		case "kernel_cmdline":
			cmdlines = append(cmdlines, d)
			continue
		}
		if _, dup := seen[d.Partition]; dup {
			return failf("vbmeta: two descriptors for partition %s", d.Partition)
		}
		kind, ok := Covered[d.Partition]
		if !ok {
			return failf("vbmeta: descriptor for partition %s, which is not one of boot, dtbo, system, vendor", d.Partition)
		}
		if d.Type != kind {
			return failf("vbmeta: partition %s has a %s descriptor, not %s", d.Partition, d.Type, kind)
		}
		seen[d.Partition] = d.Type
	}
	for _, p := range []string{"boot", "dtbo", "system", "vendor"} {
		if _, ok := seen[p]; !ok {
			return failf("vbmeta: no descriptor for partition %s", p)
		}
	}
	sys, _ := v.Find(dmPartition)
	return checkDMTable(cmdlines, sys)
}

var dmRE = regexp.MustCompile(`^dm="([^"]*)" root=/dev/dm-0$`)

// checkDMTable: the dm-verity table on the verity-on command line is exactly
// the system hashtree descriptor. The kernel verifies system with this table,
// not with the descriptor.
func checkDMTable(cmdlines []Descriptor, d Descriptor) error {
	const w = "kernel cmdline dm= table"
	var found []string
	for _, c := range cmdlines {
		has := strings.Contains(c.Cmdline, "dm=")
		if has && c.Flags != 1 {
			return failf("%s: a dm= table outside the verity-on command line", w)
		}
		if has {
			found = append(found, c.Cmdline)
		}
	}
	if len(found) != 1 {
		return failf("%s: %d command lines with a dm= table, not 1", w, len(found))
	}
	m := dmRE.FindStringSubmatch(found[0])
	if m == nil {
		return failf("%s: not of the form dm=\"...\" root=/dev/dm-0", w)
	}
	t := strings.Split(m[1], " ")
	dev := "PARTUUID=$(ANDROID_" + strings.ToUpper(dmPartition) + "_PARTUUID)"
	u := func(x uint64) string { return strconv.FormatUint(x, 10) }
	bs := u(Block)
	head := []string{"1", "vroot", "none", "ro", "1,0", u(d.ImageSize / 512), "verity", "1", dev, dev,
		bs, bs, u(d.ImageSize / Block), u(d.TreeOffset / Block), d.HashAlgorithm, d.RootDigest, d.Salt}
	if len(t) < len(head) || strings.Join(t[:len(head)], " ") != strings.Join(head, " ") {
		return failf("%s: does not match the %s hashtree descriptor", w, dmPartition)
	}
	opt := t[len(head):]
	if len(opt) == 0 {
		return failf("%s: optional argument count does not match", w)
	}
	cnt, err := strconv.Atoi(opt[0])
	if err != nil || opt[0] != strconv.Itoa(cnt) || cnt != len(opt)-1 {
		return failf("%s: optional argument count does not match", w)
	}
	opt = opt[1:]
	want := []string{"$(ANDROID_VERITY_MODE)"}
	for _, o := range opt {
		if o == "ignore_zero_blocks" {
			want = append(want, o)
			break
		}
	}
	if d.FECNumRoots != 0 {
		fb := u(d.FECOffset / Block)
		want = append(want, "use_fec_from_device", dev, "fec_roots", u(uint64(d.FECNumRoots)), "fec_blocks", fb, "fec_start", fb)
	}
	if strings.Join(opt, " ") != strings.Join(want, " ") {
		return failf("%s: optional arguments %v do not match the %s hashtree descriptor", w, opt, dmPartition)
	}
	return nil
}
