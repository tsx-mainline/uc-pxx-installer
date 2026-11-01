package avb

import (
	"bytes"
	"crypto"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"encoding/hex"
)

// Cert is one ATX certificate (libavb_atx AvbAtxCertificate; little-endian
// fields).
type Cert struct {
	Raw        []byte
	Signed     []byte // the part the signature covers
	Key        []byte // AvbRSAPublicKeyHeader blob
	Subject    []byte
	Usage      []byte
	KeyVersion uint64
	Signature  []byte
}

// SHA256 is the hash of the whole certificate.
func (c *Cert) SHA256() string { s := sha256.Sum256(c.Raw); return hex.EncodeToString(s[:]) }

// ATX is the public key metadata of a UC-P10 vbmeta: the PIK and the PSK
// certificate.
type ATX struct {
	PIK, PSK Cert
	Raw      []byte
}

// PIKKeyVersion and PSKKeyVersion are what a U-Boot writes to the TEE
// rollback locations 0x1000 and 0x1001 when this chain validates
// (FIRMWARE-GENERAL §2.2).
func (a *ATX) PIKKeyVersion() uint64 { return a.PIK.KeyVersion }
func (a *ATX) PSKKeyVersion() uint64 { return a.PSK.KeyVersion }

// MetadataSHA256 is the hash of the whole metadata.
func (a *ATX) MetadataSHA256() string { s := sha256.Sum256(a.Raw); return hex.EncodeToString(s[:]) }

func parseCert(b []byte, what string) (Cert, error) {
	le := binary.LittleEndian
	if v := le.Uint32(b); v != 1 {
		return Cert{}, failf("%s: certificate version %d, not 1", what, v)
	}
	p := 4 + KeySize
	return Cert{Raw: b, Signed: b[:atxSignedSize], Key: b[4:p], Subject: b[p : p+32], Usage: b[p+32 : p+64],
		KeyVersion: le.Uint64(b[p+64:]), Signature: b[atxSignedSize:]}, nil
}

// ParseATX decodes the ATX public key metadata. It checks only the form: it
// reads the key versions of a vbmeta whose signature it does not check (the
// keeper's vbmeta, which may be patched).
func ParseATX(pkm []byte) (*ATX, error) {
	if len(pkm) != ATXMetadataSize {
		return nil, failf("ATX: public key metadata is %d bytes, not %d", len(pkm), ATXMetadataSize)
	}
	if v := binary.LittleEndian.Uint32(pkm); v != 1 {
		return nil, failf("ATX: metadata version %d, not 1", v)
	}
	pik, err := parseCert(pkm[4:4+atxCertSize], "ATX PIK")
	if err != nil {
		return nil, err
	}
	psk, err := parseCert(pkm[4+atxCertSize:], "ATX PSK")
	if err != nil {
		return nil, err
	}
	return &ATX{PIK: pik, PSK: psk, Raw: pkm}, nil
}

// Check is the chain: the PIK certificate is the pinned one with the CA
// usage, the PSK certificate is signed by the PIK key (SHA512_RSA4096) with
// the vboot usage and the pinned subject, and the vbmeta key is the PSK key.
func (a *ATX) Check(vbmetaKey []byte, pins Pins) error {
	if s := a.PIK.SHA256(); s != pins.PIKCertSHA256 {
		return failf("ATX: PIK certificate %s is not the pinned one %s", s, pins.PIKCertSHA256)
	}
	if !bytes.Equal(a.PIK.Usage, usagePIK[:]) {
		return failf("ATX: PIK certificate usage is not vboot.ca")
	}
	n, err := parseKey(a.PIK.Key, "ATX PIK")
	if err != nil {
		return err
	}
	d := sha512.Sum512(a.PSK.Signed)
	if err := verifyRSA(n, a.PSK.Signature, d[:], crypto.SHA512, "ATX: PSK certificate (by the PIK key)"); err != nil {
		return err
	}
	if !bytes.Equal(a.PSK.Usage, usagePSK[:]) {
		return failf("ATX: PSK certificate usage is not vboot")
	}
	if s := hex.EncodeToString(a.PSK.Subject); s != pins.PSKSubject {
		return failf("ATX: PSK subject %s is not the pinned product %s", s, pins.PSKSubject)
	}
	if _, err := parseKey(a.PSK.Key, "ATX PSK"); err != nil {
		return err
	}
	if !bytes.Equal(a.PSK.Key, vbmetaKey) {
		return failf("ATX: the vbmeta public key is not the PSK key")
	}
	return nil
}

// KeeperFacts are the rollback-relevant values of a vbmeta that is on the
// eMMC (FIRMWARE-GENERAL §2.4): read without a signature check.
type KeeperFacts struct {
	RollbackIndex         uint64
	RollbackIndexLocation uint32
	PIKKeyVersion         uint64
	PSKKeyVersion         uint64
	KeySHA1               string
	PIKCertSHA256         string
}

// ReadFacts reads KeeperFacts from the first bytes of a vbmeta partition.
func ReadFacts(b []byte) (KeeperFacts, error) {
	var f KeeperFacts
	h, err := ParseHeader(b, "vbmeta")
	if err != nil {
		return f, err
	}
	aux := h.aux(b)
	pk := aux[h.PKOffset : h.PKOffset+h.PKSize]
	if len(pk) == 0 {
		return f, failf("vbmeta: no public key")
	}
	a, err := ParseATX(aux[h.PKMOffset : h.PKMOffset+h.PKMSize])
	if err != nil {
		return f, err
	}
	v := &Vbmeta{PublicKey: pk}
	return KeeperFacts{RollbackIndex: h.RollbackIndex, RollbackIndexLocation: h.RollbackIndexLocation,
		PIKKeyVersion: a.PIKKeyVersion(), PSKKeyVersion: a.PSKKeyVersion(), KeySHA1: v.KeySHA1(),
		PIKCertSHA256: a.PIK.SHA256()}, nil
}
