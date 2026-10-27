// Package nvram reads the auto-update settings of stock Android from its
// nvram store (the Grandstream nvram driver of the Android kernel) in the
// partition "others". It only reads.
//
// The store also holds secrets: the admin password, password hashes and
// tokens. This package returns the values of three keys only (72005, 72012,
// 72016). No other key or value leaves it, also not in an error text.
//
// The format and the rule are from the stock kernels of 1.0.0.94 and 1.0.6.20
// (the same driver code; UC-P10 notes, docs/android-update-settings.md):
//
//   - The driver reads absolute LBA 0x381400 + 0x80*n, n = 0..31: a ring of
//     32 parts of 64 KiB at byte 0x200000 of others (others starts at LBA
//     0x380400 on the unit).
//
//   - Each part: a 0x28-byte header, then a zlib stream of NUL-separated
//     key=value records. An empty record ends the list.
//
//     0x00 "FLSH"   0x04 u32 text length   0x08 u32 fill percentage
//     0x0c u32 zlib length   0x10 u16 flags   0x12 u16 checksum (big-endian)
//     0x14 not written   0x18 u64 write counter   0x20 not written
//
//   - Checksum: the 16-bit sum (no end-around carry) of the big-endian words
//     of the zlib stream, padded to an even length; the stored value is
//     (-sum) & 0xffff.
//
//   - The active part is the valid part (magic and checksum) with the highest
//     write counter. On a tie the driver takes the later part; this package
//     calls a tie unknown. A save writes the next part (active+1, after part
//     31 part 0) with the counter + 1.
package nvram

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
)

const (
	// PartName is the GPT name of the partition that holds the store.
	PartName = "others"
	// PartStart is the first sector of others on a UC-P10. The driver reads
	// absolute sectors, so the store is only where it expects it when
	// others starts here.
	PartStart = 3671040
	// RingOffset is the byte offset of part 0 in others.
	RingOffset = 0x200000
	// PartSize is the size of one part.
	PartSize = 0x10000
	// Parts is the number of parts in the ring.
	Parts = 32
	// RingSize is the size of the whole ring.
	RingSize = Parts * PartSize
	// HeaderSize is the size of a part header.
	HeaderSize = 0x28
	// Magic starts a valid part.
	Magic = "FLSH"
)

// The keys this package reads (nvram P-values of the web UI "Auto Update").
const (
	KeyFirmware = "72005" // Auto Update Firmware: "true" or "false"
	KeyAPK      = "72012" // Auto Update APK: "true" or "false"
	KeyURL      = "72016" // the firmware manifest URL
)

// Firmware states.
const (
	Off     = "off"
	On      = "on"
	Unknown = "unknown"
)

// AutoUpdate is what the active part says. Firmware and APK are "true",
// "false", "other" (any other value), or "" (no such key or no active part).
// URL is the value of 72016, printable ASCII only.
type AutoUpdate struct {
	// State is Off only when the active part is certain and 72005 is
	// "false", On when it is "true", Unknown otherwise.
	State         string
	Firmware, APK string
	URL           string
	// Part is the active part (0..31), -1 when none. Counter is its write
	// counter. Valid is the number of valid parts.
	Part    int
	Counter uint64
	Valid   int
	// Why says why the state is unknown, or how the part was chosen.
	Why string
}

// CheckPartition checks that others is where the driver reads the store:
// start sector and size (in 512-byte sectors).
func CheckPartition(start, sectors int64) error {
	if start != PartStart {
		return fmt.Errorf("%s starts at sector %d, not %d: the Android driver reads another place", PartName, start, PartStart)
	}
	if sectors*512 < RingOffset+RingSize {
		return fmt.Errorf("%s has %d sectors, too small for the nvram ring", PartName, sectors)
	}
	return nil
}

// Checksum is the driver's checksum of the zlib stream of a part.
func Checksum(data []byte, n int) uint16 {
	m := (n + 1) &^ 1
	var sum uint16
	for i := 0; i < m; i += 2 {
		hi, lo := byteAt(data, i), byteAt(data, i+1)
		sum += uint16(hi)<<8 | uint16(lo)
	}
	return -sum
}

func byteAt(b []byte, i int) byte {
	if i < len(b) {
		return b[i]
	}
	return 0
}

// header is the part header fields that the driver uses.
type header struct {
	textLen, zlibLen uint32
	sum              uint16
	counter          uint64
}

// The part kinds of the scan.
const (
	invalid  = iota // no magic, or a wrong checksum: the driver skips it
	accepted        // magic and checksum: the driver may make it active
	doubtful        // magic, but a zlib length past the part: the driver
	// computes its checksum over bytes after the part, which this package
	// cannot see
)

// scan reads a part header as the driver's scan does.
func scan(p []byte) (header, int) {
	var h header
	if len(p) < PartSize || string(p[:4]) != Magic {
		return h, invalid
	}
	le := binary.LittleEndian
	h.textLen = le.Uint32(p[4:])
	h.zlibLen = le.Uint32(p[12:])
	h.sum = binary.BigEndian.Uint16(p[0x12:])
	h.counter = le.Uint64(p[0x18:])
	if int64(h.zlibLen) > PartSize-HeaderSize {
		return h, doubtful
	}
	if Checksum(p[HeaderSize:PartSize], int(h.zlibLen)) != h.sum {
		return h, invalid
	}
	return h, accepted
}

// Parse reads the ring (RingSize bytes from RingOffset of others) and returns
// the auto-update state of the active part.
func Parse(ring []byte) AutoUpdate {
	au := AutoUpdate{State: Unknown, Part: -1}
	if len(ring) < RingSize {
		au.Why = fmt.Sprintf("the nvram ring is %d bytes, not %d", len(ring), RingSize)
		return au
	}
	best, tie, odd := -1, -1, -1
	var bh header
	var oddCounter uint64
	for i := 0; i < Parts; i++ {
		h, kind := scan(ring[i*PartSize : (i+1)*PartSize])
		if kind == doubtful && (odd < 0 || h.counter >= oddCounter) {
			odd, oddCounter = i, h.counter
		}
		if kind != accepted {
			continue
		}
		au.Valid++
		switch {
		case best < 0 || h.counter > bh.counter:
			best, bh, tie = i, h, -1
		case h.counter == bh.counter:
			tie = i
		}
	}
	if best < 0 {
		au.Why = "no valid nvram part (magic FLSH and checksum)"
		return au
	}
	au.Counter = bh.counter
	if odd >= 0 && oddCounter >= bh.counter {
		au.Why = fmt.Sprintf("part %d has the write counter %d and a zlib length past the part", odd, oddCounter)
		return au
	}
	if tie >= 0 {
		au.Why = fmt.Sprintf("parts %d and %d have the same highest write counter %d", best, tie, bh.counter)
		return au
	}
	au.Part = best
	p := ring[best*PartSize : (best+1)*PartSize]
	text, err := inflate(p[HeaderSize:HeaderSize+int(bh.zlibLen)], int(bh.textLen))
	if err != nil {
		au.Why = fmt.Sprintf("active part %d (write counter %d): %v", best, bh.counter, err)
		return au
	}
	vals, found, err := lookup(text, KeyFirmware, KeyAPK, KeyURL)
	if err != nil {
		au.Why = fmt.Sprintf("active part %d (write counter %d): %v", best, bh.counter, err)
		return au
	}
	au.Firmware, au.APK, au.URL = flag(vals[0], found[0]), flag(vals[1], found[1]), vals[2]
	switch {
	case !found[0]:
		au.Why = fmt.Sprintf("active part %d (write counter %d) has no key %s", best, bh.counter, KeyFirmware)
		return au
	case au.Firmware == "false":
		au.State = Off
	case au.Firmware == "true":
		au.State = On
	default:
		au.Why = fmt.Sprintf("active part %d (write counter %d): %s is neither true nor false", best, bh.counter, KeyFirmware)
		return au
	}
	au.Why = fmt.Sprintf("active part %d of %d (the highest write counter, %d; %d valid parts)", best, Parts, bh.counter, au.Valid)
	return au
}

// maxText is the output room of the driver's inflate (0x1ffd8 bytes).
const maxText = 0x1ffd8

// inflate decompresses the text of a part. The driver clears the store when
// this fails, so the state is then not known.
func inflate(z []byte, n int) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(z))
	if err != nil {
		return nil, fmt.Errorf("the zlib stream does not open")
	}
	text, err := io.ReadAll(io.LimitReader(r, maxText+1))
	if err != nil {
		return nil, fmt.Errorf("the zlib stream is corrupt")
	}
	if len(text) > maxText {
		return nil, fmt.Errorf("the text is longer than the driver's buffer")
	}
	if len(text) != n {
		return nil, fmt.Errorf("the text is %d bytes, the header says %d", len(text), n)
	}
	return text, nil
}

// lookup returns the values of the keys and which keys it found, as the
// driver splits the text: records end with NUL, an empty record ends the
// list, the key ends at the first "=". A key may carry one attribute byte
// (1..3) in front. A key that is there twice is an error. Only the asked keys
// are looked at.
func lookup(text []byte, keys ...string) ([]string, []bool, error) {
	out := make([]string, len(keys))
	seen := make([]bool, len(keys))
	for _, rec := range bytes.Split(text, []byte{0}) {
		if len(rec) == 0 {
			break
		}
		k, v, ok := bytes.Cut(rec, []byte("="))
		if !ok {
			continue
		}
		if len(k) > 0 && k[0] >= 1 && k[0] <= 3 {
			k = k[1:]
		}
		for i, want := range keys {
			if string(k) != want {
				continue
			}
			if seen[i] {
				return nil, nil, fmt.Errorf("key %s is there twice", want)
			}
			seen[i], out[i] = true, printable(v)
		}
	}
	return out, seen, nil
}

// flag keeps a value of a true/false key only when it is true or false, so
// that no other text comes out of a flag key.
func flag(v string, found bool) string {
	switch {
	case !found:
		return ""
	case v == "true" || v == "false":
		return v
	}
	return "other"
}

// printable keeps printable ASCII and cuts at 200 bytes, for the screen.
func printable(v []byte) string {
	var b strings.Builder
	for _, c := range v {
		if b.Len() >= 200 {
			b.WriteString("...")
			break
		}
		if c >= 0x20 && c < 0x7f {
			b.WriteByte(c)
		} else {
			b.WriteByte('?')
		}
	}
	return b.String()
}
