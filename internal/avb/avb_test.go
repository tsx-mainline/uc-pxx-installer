package avb_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math/rand"
	"os"
	"strings"
	"testing"

	"uc-pxx-installer/internal/avb"
	"uc-pxx-installer/internal/avb/avbtest"
)

var sizes = map[string]avbtest.Size{
	"boot":   {Total: 64 << 10, Data: 3*4096 + 100},
	"dtbo":   {Total: 16 << 10, Data: 136},
	"system": {Total: 1 << 20, Data: 200 * 4096}, // two tree levels
	"vendor": {Total: 64 << 10, Data: 10 * 4096},
}

func data(seed int64) map[string][]byte {
	m := map[string][]byte{}
	for i, p := range []string{"boot", "dtbo", "system", "vendor"} {
		b := make([]byte, sizes[p].Data)
		rand.New(rand.NewSource(seed + int64(i))).Read(b)
		m[p] = b
	}
	return m
}

func testPins() avb.Pins {
	pik, sub := avbtest.Pins()
	return avb.Pins{PIKCertSHA256: pik, PSKSubject: sub}
}

func firmware(o avbtest.FirmwareOpt) *avbtest.Firmware {
	return avbtest.MakeFirmware(data(1), sizes, o)
}

// check runs the whole verification as pass 1 does: the vbmeta, then each
// covered image in odd-sized chunks, set up from the signed descriptor.
func check(t *testing.T, images map[string][]byte, pins avb.Pins) error {
	t.Helper()
	v, err := avb.Verify(images["vbmeta"], pins)
	if err != nil {
		return err
	}
	for _, p := range []string{"dtbo", "boot", "system", "vendor"} {
		d, _ := v.Find(p)
		if err := stream(images[p], p, d, v); err != nil {
			return err
		}
	}
	return nil
}

func stream(img []byte, p string, d avb.Descriptor, v *avb.Vbmeta) error {
	c, err := avb.NewImageCheck(p, d, int64(len(img)))
	if err != nil {
		return err
	}
	for i := 0; i < len(img); i += 777 {
		c.Write(img[i:min(i+777, len(img))])
	}
	return c.Done(v)
}

func clone(m map[string][]byte) map[string][]byte {
	out := map[string][]byte{}
	for k, v := range m {
		out[k] = append([]byte(nil), v...)
	}
	return out
}

func refused(t *testing.T, name string, err error, want string) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: accepted, want a refusal with %q", name, want)
	} else if !strings.Contains(err.Error(), want) {
		t.Errorf("%s: %v, want %q", name, err, want)
	}
}

func TestPositive(t *testing.T) {
	fw := firmware(avbtest.FirmwareOpt{})
	if err := check(t, fw.Images, testPins()); err != nil {
		t.Fatal(err)
	}
	v, _ := avb.Verify(fw.Images["vbmeta"], testPins())
	if v.ATX.PIKKeyVersion() != 42 || v.ATX.PSKKeyVersion() != 42 || v.RollbackIndex != 0 {
		t.Fatalf("versions %d/%d/%d", v.RollbackIndex, v.ATX.PIKKeyVersion(), v.ATX.PSKKeyVersion())
	}
	if len(v.Descriptors) != 6 {
		t.Fatalf("%d descriptors", len(v.Descriptors))
	}
	f, err := avb.ReadFacts(fw.Images["vbmeta"])
	if err != nil || f.PIKKeyVersion != 42 || f.PSKKeyVersion != 42 || f.KeySHA1 != v.KeySHA1() {
		t.Fatalf("facts %+v %v", f, err)
	}
	// The real pins refuse the test chain.
	refused(t, "real pins", check(t, fw.Images, avb.CrestronPins()), "is not the pinned one")
}

func TestVersions(t *testing.T) {
	fw := firmware(avbtest.FirmwareOpt{Rollback: 3, PIKVersion: 43, PSKVersion: 44})
	// Another PIK key version is another PIK certificate: the pin refuses it.
	_, err := avb.Verify(fw.Images["vbmeta"], testPins())
	refused(t, "PIK 43 under the PIK-42 pin", err, "is not the pinned one")
	pins := testPins()
	s := sha256.Sum256(avbtest.PIKCert(43))
	pins.PIKCertSHA256 = hex.EncodeToString(s[:])
	v, err := avb.Verify(fw.Images["vbmeta"], pins)
	if err != nil {
		t.Fatal(err)
	}
	if v.RollbackIndex != 3 || v.ATX.PIKKeyVersion() != 43 || v.ATX.PSKKeyVersion() != 44 {
		t.Fatalf("versions %d/%d/%d", v.RollbackIndex, v.ATX.PIKKeyVersion(), v.ATX.PSKKeyVersion())
	}
}

func vb(o avbtest.VbmetaOpt, fw *avbtest.Firmware) []byte {
	if o.Algorithm == 0 {
		o.Algorithm = 2
	}
	if o.Signer == nil {
		o.Signer = avbtest.PSK()
	}
	if o.Metadata == nil {
		o.Metadata = avbtest.Metadata(avbtest.PIKCert(42), avbtest.PSKCert(42))
	}
	if o.Descriptors == nil {
		o.Descriptors = fw.Descriptors
	}
	return avbtest.Vbmeta(o)
}

func TestVbmetaRefusals(t *testing.T) {
	fw := firmware(avbtest.FirmwareOpt{})
	pins := testPins()
	other := avbtest.Other()
	subj := sha256.Sum256([]byte("another product"))
	desc := func(f func([][]byte) [][]byte) [][]byte {
		return f(append([][]byte(nil), fw.Descriptors...))
	}
	cases := []struct {
		name string
		img  []byte
		want string
	}{
		{"hash wrong", vb(avbtest.VbmetaOpt{CorruptHash: true}, fw), "hash over the header"},
		{"bad signature", vb(avbtest.VbmetaOpt{CorruptSigned: true}, fw), "signature does not verify"},
		{"signed by another key", vb(avbtest.VbmetaOpt{Signer: other, Key: avbtest.PSK().Blob()}, fw), "signature does not verify"},
		{"vbmeta key not the PSK key", vb(avbtest.VbmetaOpt{Signer: other}, fw), "not the PSK key"},
		{"PSK cert not signed by the PIK", vb(avbtest.VbmetaOpt{Metadata: avbtest.Metadata(avbtest.PIKCert(42),
			avbtest.Cert(avbtest.PSK().Blob(), avbtest.Subject, avbtest.UsagePSK, 42, other, 1))}, fw), "PSK certificate (by the PIK key)"},
		{"PIK cert not the pin", vb(avbtest.VbmetaOpt{Metadata: avbtest.Metadata(avbtest.PIKCert(41), avbtest.PSKCert(42))}, fw), "PIK certificate"},
		{"PIK usage", vb(avbtest.VbmetaOpt{Metadata: avbtest.Metadata(avbtest.Cert(avbtest.PIK().Blob(), avbtest.Subject, avbtest.UsagePSK, 42, avbtest.PIK(), 1),
			avbtest.PSKCert(42))}, fw), "is not the pinned one"},
		{"PSK usage", vb(avbtest.VbmetaOpt{Metadata: avbtest.Metadata(avbtest.PIKCert(42),
			avbtest.Cert(avbtest.PSK().Blob(), avbtest.Subject, avbtest.UsagePIK, 42, avbtest.PIK(), 1))}, fw), "usage is not vboot"},
		{"PSK subject", vb(avbtest.VbmetaOpt{Metadata: avbtest.Metadata(avbtest.PIKCert(42),
			avbtest.Cert(avbtest.PSK().Blob(), subj, avbtest.UsagePSK, 42, avbtest.PIK(), 1))}, fw), "PSK subject"},
		{"cert version", vb(avbtest.VbmetaOpt{Metadata: avbtest.Metadata(avbtest.PIKCert(42),
			avbtest.Cert(avbtest.PSK().Blob(), avbtest.Subject, avbtest.UsagePSK, 42, avbtest.PIK(), 2))}, fw), "certificate version"},
		{"metadata missing", vb(avbtest.VbmetaOpt{Metadata: []byte{}}, fw), "public key metadata is 0 bytes"},
		{"flags", vb(avbtest.VbmetaOpt{Flags: 2}, fw), "flags 0x2"},
		{"algorithm", vb(avbtest.VbmetaOpt{Algorithm: 5}, fw), "algorithm 5"},
		{"location", vb(avbtest.VbmetaOpt{Location: 1}, fw), "rollback index location 1"},
		{"libavb minor", vb(avbtest.VbmetaOpt{Minor: 1}, fw), "needs libavb 1.1"},
		{"chain descriptor", vb(avbtest.VbmetaOpt{Descriptors: desc(func(d [][]byte) [][]byte {
			return append(d, avbtest.ChainDesc("vendor", 1, avbtest.PSK().Blob()))
		})}, fw), "chain partition descriptor"},
		{"property descriptor", vb(avbtest.VbmetaOpt{Descriptors: desc(func(d [][]byte) [][]byte {
			return append(d, avbtest.PropertyDesc("k", "v"))
		})}, fw), "property descriptor"},
		{"unknown tag", vb(avbtest.VbmetaOpt{Descriptors: desc(func(d [][]byte) [][]byte {
			return append(d, avbtest.Raw(9, make([]byte, 8)))
		})}, fw), "unknown descriptor tag 9"},
		{"descriptor for oem", vb(avbtest.VbmetaOpt{Descriptors: desc(func(d [][]byte) [][]byte {
			return append(d, avbtest.HashDesc("oem", 136, "sha256", fw.Salt, make([]byte, 32), 0))
		})}, fw), "partition oem"},
		{"descriptor missing", vb(avbtest.VbmetaOpt{Descriptors: desc(func(d [][]byte) [][]byte { return d[:len(d)-1] })}, fw),
			"no descriptor for partition dtbo"},
		{"descriptor twice", vb(avbtest.VbmetaOpt{Descriptors: desc(func(d [][]byte) [][]byte { return append(d, d[len(d)-1]) })}, fw),
			"two descriptors for partition dtbo"},
		{"wrong kind", vb(avbtest.VbmetaOpt{Descriptors: desc(func(d [][]byte) [][]byte {
			d[len(d)-1] = avbtest.HashtreeDesc("dtbo", avbtest.Tree{ImageSize: 8192, TreeOffset: 8192, TreeSize: 4096, Alg: "sha1",
				Root: make([]byte, 20)}, fw.Salt, 1, 0)
			return d
		})}, fw), "dtbo has a hashtree descriptor"},
		{"hash flags", vb(avbtest.VbmetaOpt{Descriptors: desc(func(d [][]byte) [][]byte {
			d[len(d)-1] = avbtest.HashDesc("dtbo", 136, "sha256", fw.Salt, make([]byte, 32), 1)
			return d
		})}, fw), "flags 0x1"},
		{"dm table", vb(avbtest.VbmetaOpt{Descriptors: desc(func(d [][]byte) [][]byte {
			for i, x := range d {
				if bytes.Contains(x, []byte("dm=")) {
					d[i] = avbtest.CmdlineDesc(1, strings.Replace(avbtest.DMCmdline(avbtest.Tree{ImageSize: 4096 * 199, TreeOffset: 4096 * 199,
						Alg: "sha1", Root: make([]byte, 20)}, fw.Salt), "x", "x", 1))
				}
			}
			return d
		})}, fw), "dm= table"},
		{"cmdline flags 3", vb(avbtest.VbmetaOpt{Descriptors: desc(func(d [][]byte) [][]byte {
			return append(d, avbtest.CmdlineDesc(3, "x"))
		})}, fw), "kernel cmdline flags 0x3"},
	}
	for _, c := range cases {
		im := clone(fw.Images)
		im["vbmeta"] = c.img
		refused(t, c.name, check(t, im, pins), c.want)
	}
	// Bytes after the blocks.
	b := append([]byte(nil), fw.Images["vbmeta"]...)
	b[len(b)-1] = 1
	_, err := avb.Verify(b, pins)
	refused(t, "trailing byte", err, "after the vbmeta blocks")
	// A truncated image.
	_, err = avb.Verify(fw.Images["vbmeta"][:1000], pins)
	refused(t, "truncated", err, "truncated")
}

// TestEveryVbmetaByte: a change of any byte of the used vbmeta refuses.
func TestEveryVbmetaByte(t *testing.T) {
	fw := firmware(avbtest.FirmwareOpt{})
	h, _ := avb.ParseHeader(fw.Images["vbmeta"], "vbmeta")
	used := int(h.Total)
	step := 1
	if testing.Short() {
		step = 7
	}
	for i := 0; i < used+64; i += step {
		b := append([]byte(nil), fw.Images["vbmeta"]...)
		b[i] ^= 0x01
		if _, err := avb.Verify(b, testPins()); err == nil {
			t.Errorf("byte %d flipped: accepted", i)
		}
	}
}

func TestImageRefusals(t *testing.T) {
	fw := firmware(avbtest.FirmwareOpt{})
	pins := testPins()
	v, err := avb.Verify(fw.Images["vbmeta"], pins)
	if err != nil {
		t.Fatal(err)
	}
	flip := func(p string, off int) error {
		im := clone(fw.Images)
		im[p][off] ^= 0x10
		return check(t, im, pins)
	}
	refused(t, "boot data", flip("boot", 5000), "boot: hash descriptor: digest")
	refused(t, "dtbo data", flip("dtbo", 135), "dtbo: hash descriptor: digest")
	refused(t, "system data", flip("system", 4096*17+3), "data block 17 does not match")
	refused(t, "vendor data", flip("vendor", 4096*9), "data block 9 does not match")
	// The stored tree of system: level 1 is first (one block), then level 0.
	refused(t, "system stored level 1", flip("system", 200*4096+5), "level 1 of the stored tree")
	refused(t, "system stored level 0", flip("system", 200*4096+4096+40), "data block 1 does not match")
	refused(t, "system level 0 padding", flip("system", 200*4096+4096+200*32+1), "data after the last hash")
	refused(t, "boot footer", flip("boot", 64<<10-50), "boot: AVB footer")
	refused(t, "system footer vbmeta", flip("system", 200*4096+3*4096+300), "AVB footer")
	// Not covered: the gap between the data and the footer vbmeta of boot.
	if err := flip("boot", 3*4096+200); err != nil {
		t.Errorf("a byte outside the AVB data and footer: %v", err)
	}
	// Truncated and longer images.
	d, _ := v.Find("system")
	refused(t, "short", stream(fw.Images["system"][:len(fw.Images["system"])-4096], "system", d, v), "AVBf")
	c, _ := avb.NewImageCheck("vendor", func() avb.Descriptor { d, _ := v.Find("vendor"); return d }(), 64<<10)
	c.Write(append(append([]byte(nil), fw.Images["vendor"]...), 0))
	refused(t, "long", c.Done(v), "longer than")
	// The manifest's descriptor (the set-up) differs from the signed one.
	d2 := d
	d2.Salt = strings.Repeat("00", 32)
	refused(t, "salt of the set-up", stream(fw.Images["system"], "system", d2, v), "differs from the manifest")
	d3 := d
	d3.ImageSize = 2 << 20
	_, err = avb.NewImageCheck("system", d3, 1<<20)
	refused(t, "image size larger than the image", err, "larger than the image")
	d4 := d
	d4.FECNumRoots = 2
	_, err = avb.NewImageCheck("system", d4, 1<<20)
	refused(t, "FEC without place", err, "FEC")
	_, err = avb.NewImageCheck("oem", d, 1<<20)
	refused(t, "descriptor for another partition", err, "the manifest lists")
}

func TestReadFacts(t *testing.T) {
	b := avbtest.KeeperVbmeta(2, 43, 44, nil)
	f, err := avb.ReadFacts(b)
	if err != nil || f.RollbackIndex != 2 || f.PIKKeyVersion != 43 || f.PSKKeyVersion != 44 || f.RollbackIndexLocation != 0 {
		t.Fatalf("%+v %v", f, err)
	}
	// A patched keeper vbmeta (the signature no longer fits) still gives its facts.
	b[300] ^= 1
	if _, err := avb.ReadFacts(b); err != nil {
		t.Fatal(err)
	}
	// No ATX metadata.
	nb := avbtest.Vbmeta(avbtest.VbmetaOpt{Algorithm: 2, Signer: avbtest.PSK()})
	_, err = avb.ReadFacts(nb)
	refused(t, "no metadata", err, "public key metadata")
	nb = append([]byte(nil), b...)
	binary.BigEndian.PutUint32(nb[124:], 1)
	if f, err := avb.ReadFacts(nb); err != nil || f.RollbackIndexLocation != 1 {
		t.Fatalf("location: %+v %v", f, err)
	}
}

// TestRealVbmeta: the vbmeta of a real firmware (UC_PXX_VBMETA, not in the
// repo; the CI run passes the 1.0.0.94 one) verifies with the real pins.
func TestRealVbmeta(t *testing.T) {
	p := os.Getenv("UC_PXX_VBMETA")
	if p == "" {
		t.Skip("UC_PXX_VBMETA not set")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	v, err := avb.Verify(b, avb.CrestronPins())
	if err != nil {
		t.Fatal(err)
	}
	if want := os.Getenv("UC_PXX_VBMETA_SHA1"); want != "" && v.KeySHA1() != want {
		t.Fatalf("key sha1 %s, want %s", v.KeySHA1(), want)
	}
	f, err := avb.ReadFacts(b)
	if err != nil || f.RollbackIndex != 0 || f.PIKKeyVersion != 42 || f.PSKKeyVersion != 42 || f.PIKCertSHA256 != avb.CrestronPIKCertSHA256 {
		t.Fatalf("%+v %v", f, err)
	}
}
