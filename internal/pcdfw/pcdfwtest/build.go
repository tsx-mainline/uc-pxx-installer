// Package pcdfwtest builds made-up firmware containers for tests, with
// made-up keys. It is the encrypting side of package pcdfw. Only tests use
// it. It holds no vendor key and reads no vendor file.
package pcdfwtest

import (
	"bytes"
	"compress/flate"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"hash/crc32"

	"uc-pxx-installer/internal/pcdfw"
)

var sigLE = []byte{0x39, 0x8f, 0xa7, 0x43}

// Keys are made-up test keys.
func Keys() pcdfw.Keys {
	var k pcdfw.Keys
	copy(k.Head[:], "test-head-key-01")
	copy(k.IV[:], "test-iv-value-02")
	return k
}

// Spec is one segment: its seg_id and plain image. Chunked segments are
// stored as 1 MiB ZIP chunks, like system, vendor and oem.
type Spec struct {
	ID      uint16
	Image   []byte
	Chunked bool
}

func encHead(block cipher.Block, iv, plain []byte) []byte {
	out := make([]byte, len(plain))
	for i := 0; i+32 <= len(plain); i += 32 {
		cipher.NewCBCEncrypter(block, iv).CryptBlocks(out[i:i+32], plain[i:i+32])
	}
	return out
}

func encBody(block cipher.Block, iv, buf []byte) {
	for i := 0; i < len(buf); i += pcdfw.BodyWindow {
		end := i + pcdfw.BodyWindow
		if end > len(buf) {
			end = len(buf)
		}
		full := (end - i) - (end-i)%16
		if full > 0 {
			cipher.NewCBCEncrypter(block, iv).CryptBlocks(buf[i:i+full], buf[i:i+full])
		}
	}
}

func zipRecord(data []byte) []byte {
	var comp bytes.Buffer
	w, _ := flate.NewWriter(&comp, flate.BestSpeed)
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

// Build makes a container from specs, in order.
func Build(k pcdfw.Keys, specs []Spec) []byte {
	head, _ := aes.NewCipher(k.Head[:])
	out := make([]byte, pcdfw.FirstSeg)
	copy(out, sigLE)
	for i, s := range specs {
		var key [16]byte
		copy(key[:], sigLE)
		binary.LittleEndian.PutUint32(key[4:], 0x01000600+uint32(i))
		binary.LittleEndian.PutUint32(key[8:], 0x20261024+uint32(i))
		binary.LittleEndian.PutUint32(key[12:], 0x00010002)
		block, _ := aes.NewCipher(key[:])
		var body []byte
		idtLen := 0
		if !s.Chunked {
			body = append([]byte(nil), s.Image...)
			encBody(block, k.IV[:], body)
		} else {
			var chunks [][]byte
			for off := 0; off < len(s.Image); off += 1 << 20 {
				end := off + 1<<20
				if end > len(s.Image) {
					end = len(s.Image)
				}
				rec := zipRecord(s.Image[off:end])
				encBody(block, k.IV[:], rec)
				chunks = append(chunks, rec)
			}
			idt := make([]byte, (len(chunks)+2)*pcdfw.IdtEntry)
			pos := 0
			for j, c := range chunks {
				e := idt[j*pcdfw.IdtEntry:]
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
		h := make([]byte, pcdfw.SegHdrLen)
		binary.LittleEndian.PutUint32(h[0x00:], uint32(len(body)))
		binary.LittleEndian.PutUint32(h[0x04:], uint32(len(body))+pcdfw.HdrRegion)
		binary.LittleEndian.PutUint16(h[0x0a:], s.ID)
		binary.LittleEndian.PutUint32(h[0x0c:], uint32(idtLen))
		copy(h[0x10:], key[:])
		region := make([]byte, pcdfw.HdrRegion)
		copy(region, encHead(head, k.IV[:], h))
		out = append(append(out, region...), body...)
	}
	return out
}

// Vbmeta makes a minimal vbmeta image of n bytes (n >= 1024) with a made-up
// public key, the rollback index and the release string. Package vbmeta can
// parse it. It is not signed.
func Vbmeta(n int, key []byte, rollback uint64) []byte {
	b := make([]byte, n)
	copy(b, "AVB0")
	be := binary.BigEndian
	be.PutUint32(b[4:], 1)
	auth := uint64(64)
	aux := uint64(((len(key) + 63) / 64) * 64)
	be.PutUint64(b[12:], auth)
	be.PutUint64(b[20:], aux)
	be.PutUint32(b[28:], 2)
	be.PutUint64(b[64:], 0)
	be.PutUint64(b[72:], uint64(len(key)))
	be.PutUint64(b[112:], rollback)
	copy(b[128:], "avbtool test")
	copy(b[256+auth:], key)
	return b
}
