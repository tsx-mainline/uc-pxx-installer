package pcdfw

import (
	"bytes"
	"compress/flate"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"hash/crc32"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tests build their own container with made-up keys. They need no vendor
// file and no vendor key.
func testKeys() Keys {
	var k Keys
	copy(k.Head[:], "test-head-key-01")
	copy(k.IV[:], "test-iv-value-02")
	return k
}

func encHead(block cipher.Block, iv, plain []byte) []byte {
	out := make([]byte, len(plain))
	for i := 0; i+32 <= len(plain); i += 32 {
		cipher.NewCBCEncrypter(block, iv).CryptBlocks(out[i:i+32], plain[i:i+32])
	}
	return out
}

func encBody(block cipher.Block, iv, buf []byte) {
	for i := 0; i < len(buf); i += BodyWindow {
		end := i + BodyWindow
		if end > len(buf) {
			end = len(buf)
		}
		full := (end - i) - (end-i)%16
		if full > 0 {
			cipher.NewCBCEncrypter(block, iv).CryptBlocks(buf[i:i+full], buf[i:i+full])
		}
	}
}

func zipRecord(t *testing.T, data []byte) []byte {
	var comp bytes.Buffer
	w, err := flate.NewWriter(&comp, flate.BestSpeed)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(data)
	w.Close()
	name := "tmp/tmp.tmp"
	h := make([]byte, 30)
	copy(h, "PK\x03\x04")
	binary.LittleEndian.PutUint16(h[4:], 20)
	binary.LittleEndian.PutUint16(h[8:], 8)
	binary.LittleEndian.PutUint32(h[14:], crc32.ChecksumIEEE(data))
	binary.LittleEndian.PutUint32(h[18:], uint32(comp.Len()))
	binary.LittleEndian.PutUint32(h[22:], uint32(len(data)))
	binary.LittleEndian.PutUint16(h[26:], uint16(len(name)))
	return append(append(h, name...), comp.Bytes()...)
}

type spec struct {
	id      uint16
	img     []byte
	chunked bool
}

// build makes a container and returns it with the plain images.
func build(t *testing.T, k Keys, specs []spec) []byte {
	head, _ := aes.NewCipher(k.Head[:])
	out := make([]byte, FirstSeg)
	copy(out, sigLE)
	for i, s := range specs {
		var key [16]byte
		copy(key[:], sigLE)
		binary.LittleEndian.PutUint32(key[4:], 0x01000600+uint32(i)) // version
		binary.LittleEndian.PutUint32(key[8:], 0x20261024+uint32(i)) // date
		binary.LittleEndian.PutUint32(key[12:], 0x00010002)          // oem, hw
		block, _ := aes.NewCipher(key[:])
		var body []byte
		idtLen := 0
		if !s.chunked {
			body = append([]byte(nil), s.img...)
			encBody(block, k.IV[:], body)
		} else {
			var chunks [][]byte
			for off := 0; off < len(s.img); off += 1 << 20 {
				end := off + 1<<20
				if end > len(s.img) {
					end = len(s.img)
				}
				rec := zipRecord(t, s.img[off:end])
				encBody(block, k.IV[:], rec)
				chunks = append(chunks, rec)
			}
			n := len(chunks) + 2 // two unused slots at the end
			idt := make([]byte, n*IdtEntry)
			pos := 0
			for j, c := range chunks {
				e := idt[j*IdtEntry:]
				copy(e, sigLE)
				binary.LittleEndian.PutUint32(e[0x08:], uint32(j))
				binary.LittleEndian.PutUint32(e[0x30:], uint32(pos))
				binary.LittleEndian.PutUint32(e[0x34:], uint32(len(c)))
				pos += len(c)
			}
			encBody(block, k.IV[:], idt)
			idtLen = len(idt)
			body = idt
			for _, c := range chunks {
				body = append(body, c...)
			}
		}
		h := make([]byte, SegHdrLen)
		binary.LittleEndian.PutUint32(h[0x00:], uint32(len(body)))
		binary.LittleEndian.PutUint32(h[0x04:], uint32(len(body))+HdrRegion)
		binary.LittleEndian.PutUint16(h[0x0a:], s.id)
		binary.LittleEndian.PutUint32(h[0x0c:], uint32(idtLen))
		copy(h[0x10:], key[:])
		region := make([]byte, HdrRegion)
		copy(region, encHead(head, k.IV[:], h))
		out = append(append(out, region...), body...)
	}
	return out
}

func images() []spec {
	rnd := rand.New(rand.NewSource(1))
	mk := func(magic string, n int) []byte {
		b := make([]byte, n)
		rnd.Read(b)
		copy(b, magic)
		return b
	}
	sys := make([]byte, 2<<20+12345) // 3 chunks, the last one short
	for i := range sys {
		sys[i] = byte(i / 977)
	}
	return []spec{
		{id: 0xfd02, img: mk("LOADER  ", 64<<10)},
		{id: 0xfd03, img: mk("BL3X", 32<<10)},
		{id: 0xfe02, img: mk("AVB0", 8192+7)}, // odd size: a plain-text tail
		{id: 0xfe04, img: mk("ANDROID!", 100<<10)},
		{id: 0xfe10, img: sys, chunked: true},
	}
}

func open(t *testing.T, data []byte, k Keys) *File {
	f, err := Open(bytes.NewReader(data), int64(len(data)), k)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestRoundTrip(t *testing.T) {
	k := testKeys()
	specs := images()
	data := build(t, k, specs)
	f := open(t, data, k)
	segs, err := f.Check()
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != len(specs) {
		t.Fatalf("got %d segments, want %d", len(segs), len(specs))
	}
	for i, s := range segs {
		var buf bytes.Buffer
		n, err := f.WriteSegment(s, &buf)
		if err != nil {
			t.Fatalf("%s: %v", s.Name(), err)
		}
		if n != int64(len(specs[i].img)) || !bytes.Equal(buf.Bytes(), specs[i].img) {
			t.Fatalf("%s: image differs (%d bytes, want %d)", s.Name(), n, len(specs[i].img))
		}
		if s.Name() != Partition[specs[i].id] {
			t.Fatalf("segment %d named %q", i, s.Name())
		}
	}
	if v := segs[0].VersionString(); v != "1.0.6.0" {
		t.Fatalf("version %s", v)
	}
}

func TestWrongKeys(t *testing.T) {
	k := testKeys()
	data := build(t, k, images())
	bad := k
	bad.Head[0] ^= 1
	if _, err := open(t, data, bad).Check(); err == nil {
		t.Fatal("wrong head key accepted")
	}
	bad = k
	bad.IV[3] ^= 1
	if _, err := open(t, data, bad).Check(); err == nil {
		t.Fatal("wrong IV accepted")
	}
}

func TestCorruptChunk(t *testing.T) {
	k := testKeys()
	data := build(t, k, images())
	f := open(t, data, k)
	segs, err := f.Check()
	if err != nil {
		t.Fatal(err)
	}
	sys := segs[len(segs)-1]
	pos := sys.Offset + HdrRegion + int64(sys.IdtLen) + 100 // inside the first chunk
	data[pos] ^= 0xff
	if _, err := f.WriteSegment(sys, &bytes.Buffer{}); err == nil {
		t.Fatal("corrupt chunk accepted")
	}
}

func TestTruncated(t *testing.T) {
	k := testKeys()
	data := build(t, k, images())
	cut := data[:len(data)-1]
	if _, err := open(t, cut, k).Segments(); err == nil || !strings.Contains(err.Error(), "past the end") {
		t.Fatalf("truncated file: err = %v", err)
	}
	extra := append(append([]byte(nil), data...), make([]byte, 100)...)
	if _, err := open(t, extra, k).Segments(); err == nil || !strings.Contains(err.Error(), "walk ended") {
		t.Fatalf("file with trailing bytes: err = %v", err)
	}
	if _, err := Open(bytes.NewReader(make([]byte, 200)), 200, k); err == nil {
		t.Fatal("no pack signature accepted")
	}
}

func fakeLib(rnd *rand.Rand, parts ...string) []byte {
	var b []byte
	for _, p := range parts {
		noise := make([]byte, 300)
		rnd.Read(noise)
		b = append(append(append(b, noise...), p...), 0)
	}
	return b
}

func TestFindKeys(t *testing.T) {
	k := testKeys()
	data := build(t, k, images())
	rnd := rand.New(rand.NewSource(2))
	libs := []Lib{
		// the key is the start of a longer string, as in the vendor library
		{"libwebupdate.so", fakeLib(rnd, "some printable text that is long", string(k.Head[:])+"0123456789abcdef",
			"another string of text", string(k.IV[:]))},
		{"libprovdetect.so", fakeLib(rnd, "/proc/provision/partition/%s", "more strings here, sixteen+")},
	}
	for _, raw := range []bool{false, true} {
		got, err := FindKeys(bytes.NewReader(data), int64(len(data)), libs, raw)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Pairs) != 1 || got.Pairs[0] != k {
			t.Fatalf("raw=%v: %d pairs (%v), want exactly the test keys", raw, len(got.Pairs), got.Notes)
		}
	}
	none := []Lib{{"other.so", fakeLib(rnd, "no key in this file at all, sorry")}}
	got, err := FindKeys(bytes.NewReader(data), int64(len(data)), none, true)
	if err != nil || len(got.Pairs) != 0 {
		t.Fatalf("found %d pairs in a file without the key (err %v)", len(got.Pairs), err)
	}
}

func TestKeyFile(t *testing.T) {
	k := testKeys()
	p := filepath.Join(t.TempDir(), "keys.txt")
	if err := WriteKeyFile(p, k); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	got, err := LoadKeyFile(p)
	if err != nil || got != k {
		t.Fatalf("round trip: %v", err)
	}
	for _, bad := range []string{"head_key=00\niv=00\n", "head_key=" + strings.Repeat("0", 32) + "\n", "garbage"} {
		os.WriteFile(p, []byte(bad), 0o600)
		if _, err := LoadKeyFile(p); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}
