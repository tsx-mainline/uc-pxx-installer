// Package avbtest makes small AVB-signed test firmware images for the tests:
// RSA-4096 test keys made from fixed seeds at run time (never a real key),
// an ATX chain (test PIK, test PSK), signed vbmeta images, hash and hashtree
// images with their AVB footers. It is test data only. It holds no vendor
// byte. The encoders are written from the libavb formats, apart from the
// verifier in package avb.
package avbtest

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"math/big"
	"math/rand"
	"strconv"
	"strings"
	"sync"
)

// Key is a test RSA-4096 key.
type Key struct{ *rsa.PrivateKey }

var smallPrimes = func() []uint64 {
	var ps []uint64
	for n := uint64(3); n < 20000; n += 2 {
		ok := true
		for _, p := range ps {
			if p*p > n {
				break
			}
			if n%p == 0 {
				ok = false
				break
			}
		}
		if ok {
			ps = append(ps, n)
		}
	}
	return ps
}()

// prime searches up from a random odd start, sieving out the multiples of
// the small primes before the probable-prime test.
func prime(rng *rand.Rand, bits int) *big.Int {
	b := make([]byte, bits/8)
	rng.Read(b)
	b[0] |= 0xc0
	b[len(b)-1] |= 1
	start := new(big.Int).SetBytes(b)
	res := make([]uint64, len(smallPrimes))
	m := new(big.Int)
	for i, p := range smallPrimes {
		res[i] = m.Mod(start, new(big.Int).SetUint64(p)).Uint64()
	}
	for delta := uint64(0); ; delta += 2 {
		ok := true
		for i, p := range smallPrimes {
			if (res[i]+delta)%p == 0 {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		c := new(big.Int).Add(start, new(big.Int).SetUint64(delta))
		if c.ProbablyPrime(20) {
			return c
		}
	}
}

func newKey(seed int64) *Key {
	rng := rand.New(rand.NewSource(seed))
	e := big.NewInt(65537)
	one := big.NewInt(1)
	for {
		p, q := prime(rng, 2048), prime(rng, 2048)
		if p.Cmp(q) == 0 {
			continue
		}
		n := new(big.Int).Mul(p, q)
		if n.BitLen() != 4096 {
			continue
		}
		pm, qm := new(big.Int).Sub(p, one), new(big.Int).Sub(q, one)
		phi := new(big.Int).Mul(pm, qm)
		d := new(big.Int).ModInverse(e, phi)
		if d == nil {
			continue
		}
		k := &rsa.PrivateKey{PublicKey: rsa.PublicKey{N: n, E: 65537}, D: d, Primes: []*big.Int{p, q}}
		k.Precompute()
		if k.Validate() != nil {
			continue
		}
		return &Key{k}
	}
}

var (
	once      sync.Once
	pik, psk  *Key
	otherOnce sync.Once
	other     *Key
)

// PIK and PSK are the test chain's keys. Other is a third key (a wrong
// signer). They are made once per test binary (about a second).
func PIK() *Key { once.Do(func() { pik, psk = newKey(202), newKey(303) }); return pik }
func PSK() *Key { PIK(); return psk }
func Other() *Key {
	otherOnce.Do(func() { other = newKey(404) })
	return other
}

// Blob is the AvbRSAPublicKeyHeader form of the public key.
func (k *Key) Blob() []byte {
	n := k.N
	b := make([]byte, 8, 8+1024)
	binary.BigEndian.PutUint32(b, 4096)
	// n0inv = -1/n mod 2^32
	m := new(big.Int).Lsh(big.NewInt(1), 32)
	inv := new(big.Int).ModInverse(new(big.Int).Mod(n, m), m)
	n0inv := new(big.Int).Sub(m, inv)
	binary.BigEndian.PutUint32(b[4:], uint32(n0inv.Uint64()))
	b = append(b, n.FillBytes(make([]byte, 512))...)
	rr := new(big.Int).Exp(big.NewInt(2), big.NewInt(8192), n) // computed apart from the verifier's shift
	return append(b, rr.FillBytes(make([]byte, 512))...)
}

// Sign is PKCS#1 v1.5 over a digest.
func (k *Key) Sign(digest []byte, h crypto.Hash) []byte {
	s, err := rsa.SignPKCS1v15(nil, k.PrivateKey, h, digest)
	if err != nil {
		panic(err)
	}
	return s
}

// Usages and the test product.
var (
	UsagePIK = sha256.Sum256([]byte("com.google.android.things.vboot.ca"))
	UsagePSK = sha256.Sum256([]byte("com.google.android.things.vboot"))
	Subject  = sha256.Sum256([]byte("uc-pxx-installer test product"))
)

// Cert is an ATX certificate signed by signer (SHA512_RSA4096).
func Cert(keyBlob []byte, subject, usage [32]byte, version uint64, signer *Key, certVersion uint32) []byte {
	b := binary.LittleEndian.AppendUint32(nil, certVersion)
	b = append(b, keyBlob...)
	b = append(b, subject[:]...)
	b = append(b, usage[:]...)
	b = binary.LittleEndian.AppendUint64(b, version)
	d := sha512.Sum512(b)
	return append(b, signer.Sign(d[:], crypto.SHA512)...)
}

// Metadata is the ATX public key metadata (version 1, PIK cert, PSK cert).
func Metadata(pikCert, pskCert []byte) []byte {
	b := binary.LittleEndian.AppendUint32(nil, 1)
	return append(append(b, pikCert...), pskCert...)
}

// Chain options.
type Chain struct {
	PIKVersion, PSKVersion uint64
}

// PIKCert is the test PIK certificate (self-signed: the installer pins the
// whole certificate and does not check its signature).
func PIKCert(version uint64) []byte {
	return Cert(PIK().Blob(), Subject, UsagePIK, version, PIK(), 1)
}

// PSKCert is the test PSK certificate, signed by the PIK.
func PSKCert(version uint64) []byte {
	return Cert(PSK().Blob(), Subject, UsagePSK, version, PIK(), 1)
}

// Pins are the pins of the test chain.
func Pins() (pikCertSHA256, pskSubject string) {
	s := sha256.Sum256(PIKCert(42))
	return hex.EncodeToString(s[:]), hex.EncodeToString(Subject[:])
}

func pad8(b []byte) []byte {
	for len(b)%8 != 0 {
		b = append(b, 0)
	}
	return b
}

func descriptor(tag uint64, body []byte) []byte {
	body = pad8(body)
	b := binary.BigEndian.AppendUint64(nil, tag)
	b = binary.BigEndian.AppendUint64(b, uint64(len(body)))
	return append(b, body...)
}

func algField(alg string) []byte {
	b := make([]byte, 32)
	copy(b, alg)
	return b
}

// HashDesc is a hash descriptor.
func HashDesc(name string, size uint64, alg string, salt, digest []byte, flags uint32) []byte {
	be := binary.BigEndian
	b := be.AppendUint64(nil, size)
	b = append(b, algField(alg)...)
	for _, v := range []uint32{uint32(len(name)), uint32(len(salt)), uint32(len(digest)), flags} {
		b = be.AppendUint32(b, v)
	}
	b = append(b, make([]byte, 60)...)
	b = append(append(append(b, name...), salt...), digest...)
	return descriptor(2, b)
}

// Tree is the geometry of a hash tree.
type Tree struct {
	ImageSize, TreeOffset, TreeSize uint64
	FECRoots                        uint32
	FECOffset, FECSize              uint64
	Alg                             string
	Root                            []byte
}

// HashtreeDesc is a hashtree descriptor.
func HashtreeDesc(name string, t Tree, salt []byte, version, flags uint32) []byte {
	be := binary.BigEndian
	b := be.AppendUint32(nil, version)
	b = be.AppendUint64(b, t.ImageSize)
	b = be.AppendUint64(b, t.TreeOffset)
	b = be.AppendUint64(b, t.TreeSize)
	b = be.AppendUint32(b, 4096)
	b = be.AppendUint32(b, 4096)
	b = be.AppendUint32(b, t.FECRoots)
	b = be.AppendUint64(b, t.FECOffset)
	b = be.AppendUint64(b, t.FECSize)
	b = append(b, algField(t.Alg)...)
	for _, v := range []uint32{uint32(len(name)), uint32(len(salt)), uint32(len(t.Root)), flags} {
		b = be.AppendUint32(b, v)
	}
	b = append(b, make([]byte, 60)...)
	b = append(append(append(b, name...), salt...), t.Root...)
	return descriptor(1, b)
}

// CmdlineDesc is a kernel command line descriptor.
func CmdlineDesc(flags uint32, text string) []byte {
	b := binary.BigEndian.AppendUint32(nil, flags)
	b = binary.BigEndian.AppendUint32(b, uint32(len(text)))
	return descriptor(3, append(b, text...))
}

// ChainDesc is a chain partition descriptor (refused by the verifier).
func ChainDesc(name string, location uint32, key []byte) []byte {
	be := binary.BigEndian
	b := be.AppendUint32(nil, location)
	b = be.AppendUint32(b, uint32(len(name)))
	b = be.AppendUint32(b, uint32(len(key)))
	b = append(b, make([]byte, 64)...)
	return descriptor(4, append(append(b, name...), key...))
}

// PropertyDesc is a property descriptor (refused).
func PropertyDesc(k, v string) []byte {
	b := binary.BigEndian.AppendUint64(nil, uint64(len(k)))
	b = binary.BigEndian.AppendUint64(b, uint64(len(v)))
	b = append(append(append(b, k...), 0), v...)
	return descriptor(0, append(b, 0))
}

// Raw is any descriptor with a tag.
func Raw(tag uint64, body []byte) []byte { return descriptor(tag, body) }

// VbmetaOpt is the header of a vbmeta image.
type VbmetaOpt struct {
	Size          int // the partition image size; 0 = 8192
	Algorithm     uint32
	Rollback      uint64
	Flags         uint32
	Location      uint32
	Minor         uint32
	Release       string
	Key           []byte // public key blob; nil: Signer's
	Metadata      []byte
	Descriptors   [][]byte
	Signer        *Key // nil: unsigned (algorithm 0 needs none)
	CorruptHash   bool
	CorruptSigned bool
}

// Vbmeta makes a vbmeta image.
func Vbmeta(o VbmetaOpt) []byte {
	be := binary.BigEndian
	size := o.Size
	if size == 0 {
		size = 8192
	}
	key := o.Key
	if key == nil && o.Signer != nil {
		key = o.Signer.Blob()
	}
	var aux []byte
	aux = append(aux, key...)
	pkmOff := len(aux)
	aux = append(aux, o.Metadata...)
	descOff := len(aux)
	for _, d := range o.Descriptors {
		aux = append(aux, d...)
	}
	descSize := len(aux) - descOff
	for len(aux)%64 != 0 {
		aux = append(aux, 0)
	}
	authSize := 0
	if o.Algorithm != 0 {
		authSize = 576
	}
	h := make([]byte, 256)
	copy(h, "AVB0")
	be.PutUint32(h[4:], 1)
	be.PutUint32(h[8:], o.Minor)
	be.PutUint64(h[12:], uint64(authSize))
	be.PutUint64(h[20:], uint64(len(aux)))
	be.PutUint32(h[28:], o.Algorithm)
	if authSize > 0 {
		be.PutUint64(h[32:], 0)
		be.PutUint64(h[40:], 32)
		be.PutUint64(h[48:], 32)
		be.PutUint64(h[56:], 512)
	}
	be.PutUint64(h[64:], 0)
	be.PutUint64(h[72:], uint64(len(key)))
	be.PutUint64(h[80:], uint64(pkmOff))
	be.PutUint64(h[88:], uint64(len(o.Metadata)))
	be.PutUint64(h[96:], uint64(descOff))
	be.PutUint64(h[104:], uint64(descSize))
	be.PutUint64(h[112:], o.Rollback)
	be.PutUint32(h[120:], o.Flags)
	be.PutUint32(h[124:], o.Location)
	rel := o.Release
	if rel == "" {
		rel = "avbtool 1.1.0"
	}
	copy(h[128:175], rel)
	auth := make([]byte, authSize)
	if authSize > 0 {
		d := sha256.Sum256(append(append([]byte(nil), h...), aux...))
		copy(auth, d[:])
		if o.CorruptHash {
			auth[0] ^= 1
		}
		signed := d[:]
		if o.CorruptSigned {
			x := d
			x[1] ^= 1
			signed = x[:]
		}
		copy(auth[32:], o.Signer.Sign(signed, crypto.SHA256))
	}
	out := append(append(h, auth...), aux...)
	if len(out) > size {
		panic(fmt.Sprintf("avbtest: vbmeta of %d bytes does not fit %d", len(out), size))
	}
	return append(out, make([]byte, size-len(out))...)
}

// Footer appends an AVB footer and its unsigned vbmeta (with desc) to img,
// which must have room: the vbmeta goes at voff, the footer at the end.
func Footer(img []byte, orig, voff uint64, desc []byte) {
	fv := Vbmeta(VbmetaOpt{Size: 512, Descriptors: [][]byte{desc}})
	copy(img[voff:], fv)
	f := img[len(img)-64:]
	for i := range f {
		f[i] = 0
	}
	copy(f, "AVBf")
	be := binary.BigEndian
	be.PutUint32(f[4:], 1)
	be.PutUint64(f[12:], orig)
	be.PutUint64(f[20:], voff)
	be.PutUint64(f[28:], 512)
}

func hasher(alg string) hash.Hash {
	switch alg {
	case "sha1":
		return sha1.New()
	case "sha512":
		return sha512.New()
	}
	return sha256.New()
}

// HashImage makes an image of total bytes: data, then the footer vbmeta at
// the next 4 KiB boundary, then the footer. It returns the image and the
// descriptor.
func HashImage(name string, data []byte, total int, salt []byte) ([]byte, []byte) {
	img := make([]byte, total)
	copy(img, data)
	h := sha256.New()
	h.Write(salt)
	h.Write(data)
	d := HashDesc(name, uint64(len(data)), "sha256", salt, h.Sum(nil), 0)
	voff := (len(data) + 4095) / 4096 * 4096
	Footer(img, uint64(len(data)), uint64(voff), d)
	return img, d
}

// BuildTree returns the stored tree (top level first) and the root.
func BuildTree(data []byte, salt []byte, alg string) ([]byte, []byte) {
	h := hasher(alg)
	ds := h.Size()
	pad := 1
	for pad < ds {
		pad <<= 1
	}
	zp := make([]byte, pad-ds)
	level := func(in []byte) []byte {
		var out []byte
		for i := 0; i < len(in); i += 4096 {
			h.Reset()
			h.Write(salt)
			h.Write(in[i : i+4096])
			out = append(h.Sum(out), zp...)
		}
		for len(out)%4096 != 0 {
			out = append(out, 0)
		}
		return out
	}
	var levels [][]byte
	prev := data
	for len(prev) > 4096 {
		prev = level(prev)
		levels = append(levels, prev)
	}
	h.Reset()
	h.Write(salt)
	h.Write(prev)
	var tree []byte
	for i := len(levels) - 1; i >= 0; i-- {
		tree = append(tree, levels[i]...)
	}
	return tree, h.Sum(nil)
}

// HashtreeImage makes an image of total bytes: data (whole 4 KiB blocks),
// the tree, the footer vbmeta right after the tree, the footer. No FEC.
func HashtreeImage(name string, data []byte, total int, salt []byte) ([]byte, []byte, Tree) {
	tree, root := BuildTree(data, salt, "sha1")
	img := make([]byte, total)
	copy(img, data)
	copy(img[len(data):], tree)
	t := Tree{ImageSize: uint64(len(data)), TreeOffset: uint64(len(data)), TreeSize: uint64(len(tree)), Alg: "sha1", Root: root}
	d := HashtreeDesc(name, t, salt, 1, 0)
	Footer(img, uint64(len(data)), uint64(len(data)+len(tree)), d)
	return img, d, t
}

// DMCmdline is the verity-on command line for the system tree t.
func DMCmdline(t Tree, salt []byte) string {
	dev := "PARTUUID=$(ANDROID_SYSTEM_PARTUUID)"
	u := func(x uint64) string { return strconv.FormatUint(x, 10) }
	f := []string{"1", "vroot", "none", "ro", "1,0", u(t.ImageSize / 512), "verity", "1", dev, dev, "4096", "4096",
		u(t.ImageSize / 4096), u(t.TreeOffset / 4096), t.Alg, hex.EncodeToString(t.Root), hex.EncodeToString(salt),
		"2", "$(ANDROID_VERITY_MODE)", "ignore_zero_blocks"}
	return `dm="` + strings.Join(f, " ") + `" root=/dev/dm-0`
}

// FirmwareOpt changes the test firmware.
type FirmwareOpt struct {
	Rollback               uint64
	PIKVersion, PSKVersion uint64 // 0: 42
	Signer                 *Key   // nil: PSK
	Seed                   int64
}

// Firmware is a set of covered images and their signed vbmeta.
type Firmware struct {
	Images      map[string][]byte // boot, dtbo, system, vendor, vbmeta
	Descriptors [][]byte
	Salt        []byte
}

// Covered sizes of the test firmware (image size, data size).
type Size struct{ Total, Data int }

// MakeFirmware builds the covered images from data and signs the vbmeta.
// sizes gives each image's total size and its covered data size (system
// and vendor data must be whole 4 KiB blocks).
func MakeFirmware(data map[string][]byte, sizes map[string]Size, o FirmwareOpt) *Firmware {
	salt := sha256.Sum256([]byte(fmt.Sprintf("salt %d", o.Seed)))
	fw := &Firmware{Images: map[string][]byte{}, Salt: salt[:]}
	var descs [][]byte
	var sysTree Tree
	for _, p := range []string{"vendor", "boot", "system", "dtbo"} {
		s := sizes[p]
		d := data[p][:s.Data]
		switch p {
		case "boot", "dtbo":
			img, desc := HashImage(p, d, s.Total, salt[:])
			fw.Images[p] = img
			descs = append(descs, desc)
		default:
			img, desc, t := HashtreeImage(p, d, s.Total, salt[:])
			fw.Images[p] = img
			descs = append(descs, desc)
			if p == "system" {
				sysTree = t
			}
		}
		if p == "system" {
			descs = append(descs, CmdlineDesc(1, DMCmdline(sysTree, salt[:])), CmdlineDesc(2, "root=PARTUUID=$(ANDROID_SYSTEM_PARTUUID)"))
		}
	}
	fw.Descriptors = descs
	pv, sv := o.PIKVersion, o.PSKVersion
	if pv == 0 {
		pv = 42
	}
	if sv == 0 {
		sv = 42
	}
	signer := o.Signer
	if signer == nil {
		signer = PSK()
	}
	fw.Images["vbmeta"] = Vbmeta(VbmetaOpt{Algorithm: 2, Rollback: o.Rollback, Signer: signer, Key: PSK().Blob(),
		Metadata: Metadata(PIKCert(pv), PSKCert(sv)), Descriptors: descs})
	return fw
}

// KeeperVbmeta is a vbmeta like the ones on a unit: the test chain with the
// given versions and rollback index, signed by the PSK, with no descriptors.
// key replaces the vbmeta key (nil: the PSK's).
func KeeperVbmeta(rollback, pikV, pskV uint64, key []byte) []byte {
	if key == nil {
		key = PSK().Blob()
	}
	return Vbmeta(VbmetaOpt{Algorithm: 2, Rollback: rollback, Signer: PSK(), Key: key,
		Metadata: Metadata(PIKCert(pikV), PSKCert(pskV))})
}
