// Package pcdfw reads the "pack" firmware container of the Crestron UC-P10
// (a Grandstream design) and streams out its partition images.
//
// Container layout (little-endian):
//
//	0x00 .. 0x58  plaintext pack header; +0x00 u32 signature 0x43A78F39
//	0x58 .. EOF   a chain of segments: [0x200-byte header region][body]
//
// Each segment header (seghdr, 0x60 bytes at the start of the header region)
// is AES-128-CBC with the head key, in independent 32-byte chunks (the IV is
// reset for each chunk):
//
//	+0x00 u32 size_used   body length
//	+0x04 u32 size        size_used + 0x200 (the stride to the next segment)
//	+0x08 u16 fw_vmask
//	+0x0a u16 seg_id      which partition (see Partition)
//	+0x0c u32 idt_len     0 = raw body, else the length of the chunk index
//	+0x10 [16] body key   its first 4 bytes are the signature again
//	+0x14 u32 version, +0x18 u32 date, +0x1c u16 oem, +0x1e u16 hw
//
// A body is AES-128-CBC with the segment's body key, with the IV reset every
// 0x1000 bytes. A trailing run of less than 16 bytes in the last window is
// stored as plain text. A raw body is the partition image. A chunked body
// (system, vendor, oem) starts with an index of 0x48-byte entries
// (+0x00 signature, +0x30 u32 offset after the index, +0x34 u32 stored
// length). Each chunk, decrypted the same way, is a small ZIP with one entry
// that inflates to 1 MiB of the image (the last one may be shorter).
//
// The head key and the IV are not in this repository. See FindKeys.
package pcdfw

import (
	"bytes"
	"compress/flate"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"strings"
)

const (
	Sig        = 0x43A78F39
	FirstSeg   = 0x58
	HdrRegion  = 0x200
	SegHdrLen  = 0x60
	BodyWindow = 0x1000
	IdtEntry   = 0x48
)

var sigLE = []byte{0x39, 0x8f, 0xa7, 0x43}

// Partition maps a seg_id to the A/B partition the image is written to,
// without the _a/_b suffix. The device's own names for 0xfd02 and 0xfd03 are
// "loader" and "uboot". Byte comparison on a unit proved that 0xfd02 is
// uboot_X and 0xfd03 is trust_X. Neither is the shared idbloader, which is
// not in the container.
var Partition = map[uint16]string{
	0xfd02: "uboot",
	0xfd03: "trust",
	0xfe01: "dtbo",
	0xfe02: "vbmeta",
	0xfe04: "boot",
	0xfe06: "vendor",
	0xfe08: "oem",
	0xfe10: "system",
}

// rawMagic is the expected start of each raw segment body.
var rawMagic = map[uint16][]byte{
	0xfd02: []byte("LOADER"),
	0xfd03: []byte("BL3X"),
	0xfe01: {0xd7, 0xb7, 0xab, 0x1e},
	0xfe02: []byte("AVB0"),
	0xfe04: []byte("ANDROID!"),
}

// Keys are the two values that come from the device, not from the container.
type Keys struct {
	Head [16]byte
	IV   [16]byte
}

// LoadKeyFile reads "head_key=<hex>" and "iv=<hex>" lines.
func LoadKeyFile(path string) (Keys, error) {
	var k Keys
	b, err := os.ReadFile(path)
	if err != nil {
		return k, err
	}
	got := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		name, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		var dst *[16]byte
		switch strings.TrimSpace(name) {
		case "head_key":
			dst = &k.Head
		case "iv":
			dst = &k.IV
		default:
			continue
		}
		raw, err := hex.DecodeString(strings.TrimSpace(val))
		if err != nil || len(raw) != 16 {
			return k, fmt.Errorf("%s: %s is not 16 bytes of hex", path, name)
		}
		copy(dst[:], raw)
		got[name] = true
	}
	if !got["head_key"] || !got["iv"] {
		return k, fmt.Errorf("%s: needs head_key= and iv= lines", path)
	}
	return k, nil
}

// WriteKeyFile saves the keys in the LoadKeyFile format, mode 0600.
func WriteKeyFile(path string, k Keys) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "head_key=%x\niv=%x\n", k.Head[:], k.IV[:])
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(path, 0o600)
	}
	return err
}

// Segment is one decrypted segment header.
type Segment struct {
	Index    int
	Offset   int64 // of the header region
	SizeUsed uint32
	Size     uint32
	FwVmask  uint16
	SegID    uint16
	IdtLen   uint32
	BodyKey  [16]byte
	Version  uint32
	Date     uint32
	OEM, HW  uint16
}

// Name is the partition name, or segN_XXXX for an unknown seg_id.
func (s Segment) Name() string {
	if n, ok := Partition[s.SegID]; ok {
		return n
	}
	return fmt.Sprintf("seg%d_%04x", s.Index, s.SegID)
}

// VersionString shows the segment version as d.d.d.d.
func (s Segment) VersionString() string {
	v := s.Version
	return fmt.Sprintf("%d.%d.%d.%d", v>>24&0xff, v>>16&0xff, v>>8&0xff, v&0xff)
}

func parseSegHdr(h []byte) Segment {
	var s Segment
	s.SizeUsed = binary.LittleEndian.Uint32(h[0x00:])
	s.Size = binary.LittleEndian.Uint32(h[0x04:])
	s.FwVmask = binary.LittleEndian.Uint16(h[0x08:])
	s.SegID = binary.LittleEndian.Uint16(h[0x0a:])
	s.IdtLen = binary.LittleEndian.Uint32(h[0x0c:])
	copy(s.BodyKey[:], h[0x10:0x20])
	s.Version = binary.LittleEndian.Uint32(h[0x14:])
	s.Date = binary.LittleEndian.Uint32(h[0x18:])
	s.OEM = binary.LittleEndian.Uint16(h[0x1c:])
	s.HW = binary.LittleEndian.Uint16(h[0x1e:])
	return s
}

// decHead decrypts in independent 32-byte chunks (prov_dec_header).
func decHead(block cipher.Block, iv []byte, buf []byte) []byte {
	out := make([]byte, len(buf)-len(buf)%32)
	for i := 0; i+32 <= len(buf); i += 32 {
		cipher.NewCBCDecrypter(block, iv).CryptBlocks(out[i:i+32], buf[i:i+32])
	}
	return out
}

// decBody decrypts in place, with the IV reset every BodyWindow bytes from the
// start of buf (prov_dec_body). A trailing run of less than 16 bytes stays.
func decBody(block cipher.Block, iv []byte, buf []byte) {
	for i := 0; i < len(buf); i += BodyWindow {
		end := i + BodyWindow
		if end > len(buf) {
			end = len(buf)
		}
		full := (end - i) - (end-i)%16
		if full > 0 {
			cipher.NewCBCDecrypter(block, iv).CryptBlocks(buf[i:i+full], buf[i:i+full])
		}
	}
}

// File is an opened container.
type File struct {
	r    io.ReaderAt
	size int64
	keys Keys
	head cipher.Block
}

// Open checks the pack signature. It does not walk the segments.
func Open(r io.ReaderAt, size int64, k Keys) (*File, error) {
	var sig [4]byte
	if _, err := r.ReadAt(sig[:], 0); err != nil {
		return nil, err
	}
	if !bytes.Equal(sig[:], sigLE) {
		return nil, errors.New("not a pcdfw container (bad pack signature)")
	}
	head, err := aes.NewCipher(k.Head[:])
	if err != nil {
		return nil, err
	}
	return &File{r: r, size: size, keys: k, head: head}, nil
}

func (f *File) read(off int64, n int) ([]byte, error) {
	if off < 0 || n < 0 || off+int64(n) > f.size {
		return nil, fmt.Errorf("read 0x%x+0x%x is outside the file (0x%x)", off, n, f.size)
	}
	b := make([]byte, n)
	_, err := f.r.ReadAt(b, off)
	return b, err
}

// Segments walks the whole chain. It fails unless the walk ends exactly at
// the end of the file, and unless every header is consistent.
func (f *File) Segments() ([]Segment, error) {
	var segs []Segment
	off := int64(FirstSeg)
	for off+SegHdrLen <= f.size {
		raw, err := f.read(off, SegHdrLen)
		if err != nil {
			return nil, err
		}
		h := decHead(f.head, f.keys.IV[:], raw)
		if !bytes.Equal(h[0x10:0x14], sigLE) {
			break
		}
		s := parseSegHdr(h)
		s.Index = len(segs)
		s.Offset = off
		if s.Size != s.SizeUsed+HdrRegion {
			return nil, fmt.Errorf("segment %d: size 0x%x is not size_used + 0x200", s.Index, s.Size)
		}
		if _, ok := Partition[s.SegID]; !ok {
			return nil, fmt.Errorf("segment %d: unknown seg_id 0x%04x", s.Index, s.SegID)
		}
		if off+int64(s.Size) > f.size {
			return nil, fmt.Errorf("segment %d runs past the end of the file", s.Index)
		}
		segs = append(segs, s)
		off += int64(s.Size)
	}
	if len(segs) == 0 {
		return nil, errors.New("no segment (wrong key?)")
	}
	if off != f.size {
		return nil, fmt.Errorf("walk ended at 0x%x, the file is 0x%x", off, f.size)
	}
	return segs, nil
}

// Check walks the chain and decrypts the first block of each body: a raw body
// must start with its magic, a chunked body with an index signature. It is the
// test that tells a right key/IV pair from a wrong one.
func (f *File) Check() ([]Segment, error) {
	segs, err := f.Segments()
	if err != nil {
		return nil, err
	}
	for _, s := range segs {
		first, err := f.read(s.Offset+HdrRegion, 16)
		if err != nil {
			return nil, err
		}
		block, err := aes.NewCipher(s.BodyKey[:])
		if err != nil {
			return nil, err
		}
		cipher.NewCBCDecrypter(block, f.keys.IV[:]).CryptBlocks(first, first)
		if s.IdtLen == 0 {
			if m := rawMagic[s.SegID]; m != nil && !bytes.HasPrefix(first, m) {
				return nil, fmt.Errorf("segment %d (%s): body magic mismatch", s.Index, s.Name())
			}
		} else if !bytes.Equal(first[:4], sigLE) {
			return nil, fmt.Errorf("segment %d (%s): index signature mismatch", s.Index, s.Name())
		}
	}
	return segs, nil
}

// WriteSegment streams the decrypted partition image of s to w and returns
// the number of bytes written. A chunked segment is checked chunk by chunk
// (ZIP CRC32).
func (f *File) WriteSegment(s Segment, w io.Writer) (int64, error) {
	block, err := aes.NewCipher(s.BodyKey[:])
	if err != nil {
		return 0, err
	}
	iv := f.keys.IV[:]
	data := s.Offset + HdrRegion
	if s.IdtLen == 0 {
		var n int64
		const step = 256 * BodyWindow // 1 MiB, a whole number of windows
		for done := int64(0); done < int64(s.SizeUsed); done += step {
			ln := int64(s.SizeUsed) - done
			if ln > step {
				ln = step
			}
			buf, err := f.read(data+done, int(ln))
			if err != nil {
				return n, err
			}
			decBody(block, iv, buf)
			m, err := w.Write(buf)
			n += int64(m)
			if err != nil {
				return n, err
			}
		}
		return n, nil
	}
	idt, err := f.read(data, int(s.IdtLen))
	if err != nil {
		return 0, err
	}
	decBody(block, iv, idt)
	pay := data + int64(s.IdtLen)
	var n int64
	for e := 0; e+IdtEntry <= len(idt); e += IdtEntry {
		ent := idt[e : e+IdtEntry]
		if !bytes.Equal(ent[:4], sigLE) {
			break // unused slots at the end of the index
		}
		dst := binary.LittleEndian.Uint32(ent[0x30:])
		ln := binary.LittleEndian.Uint32(ent[0x34:])
		cb, err := f.read(pay+int64(dst), int(ln))
		if err != nil {
			return n, fmt.Errorf("%s chunk %d: %w", s.Name(), e/IdtEntry, err)
		}
		decBody(block, iv, cb)
		raw, err := inflateChunk(cb)
		if err != nil {
			return n, fmt.Errorf("%s chunk %d: %w", s.Name(), e/IdtEntry, err)
		}
		m, err := w.Write(raw)
		n += int64(m)
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// inflateChunk unpacks the one entry of a ZIP local file record.
func inflateChunk(cb []byte) ([]byte, error) {
	if len(cb) < 30 || !bytes.Equal(cb[:4], []byte("PK\x03\x04")) {
		return nil, errors.New("chunk is not a ZIP record (wrong key?)")
	}
	method := binary.LittleEndian.Uint16(cb[8:])
	crc := binary.LittleEndian.Uint32(cb[14:])
	csize := binary.LittleEndian.Uint32(cb[18:])
	nlen := binary.LittleEndian.Uint16(cb[26:])
	elen := binary.LittleEndian.Uint16(cb[28:])
	start := 30 + int(nlen) + int(elen)
	if start+int(csize) > len(cb) {
		return nil, errors.New("chunk ZIP entry runs past the chunk")
	}
	data := cb[start : start+int(csize)]
	var out []byte
	switch method {
	case 0:
		out = data
	case 8:
		r := flate.NewReader(bytes.NewReader(data))
		var err error
		out, err = io.ReadAll(r)
		if err != nil {
			return nil, fmt.Errorf("inflate: %w", err)
		}
	default:
		return nil, fmt.Errorf("unknown ZIP method %d", method)
	}
	if crc32.ChecksumIEEE(out) != crc {
		return nil, errors.New("chunk CRC mismatch (wrong key?)")
	}
	return out, nil
}
