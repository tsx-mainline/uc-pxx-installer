// Package nvram reads the auto-update settings of stock Android from its
// nvram store (the Grandstream nvram driver of the Android kernel) in the
// partition "others". It only reads.
//
// The store also holds secrets: the admin password, password hashes and
// tokens. This package returns the values of three keys only (72005, 72012,
// 72016). No other key or value leaves it, also not in an error text.
//
// The format and the rule are from the stock kernels of 1.0.0.94 and 1.0.6.20
// (the same driver code; UC-P10 notes, docs/android-update-settings.md). This
// package follows the driver step by step (file offsets of the 1.0.0.94
// Image):
//
//   - scan (0x7ce024): part n = 0..31 at absolute LBA 0x381400 + 0x80*n, that
//     is byte 0x200000 + n*64 KiB of others. A part counts when it starts with
//     "FLSH" and its checksum matches. The active part is the last counted
//     part with the highest u64 write counter (at 0x18; "counter >= max",
//     max starts at 0). With no counted part the driver keeps its static
//     default, part 0.
//
//   - checksum (0x7cc33c): m = (zlib length + 1) & ~1 as a signed 32-bit
//     number; the 16-bit sum (no end-around carry) of the big-endian words of
//     the m bytes after the 0x28-byte header; the stored value (big-endian at
//     0x12) is -sum. m <= 0 sums nothing (a zlib length >= 0x7fffffff). An m
//     past the 64 KiB part reads memory after the driver's buffer: such a
//     part may or may not count.
//
//   - load (0x7cd1d0): the active part again. No magic: an empty store. Flags
//     (u16 at 0x10) not 0: the checksum again (a failure is an empty store).
//     Flags 0: no check, and the zlib length becomes 0xffd8. The driver
//     inflates the zlib length bytes from 0x28 (more than the part reads past
//     the buffer) into 0x1ffd8 bytes, and keeps the text only when the stream
//     ends there (adler checked). The text length at 0x04 is not used. Any
//     other result clears the text: an empty store.
//
//   - parse (0x7cd914): the bytes 0x1ffd6 and 0x1ffd7 of the text area are
//     set to NUL; the memory after the inflated text is not cleared. Records
//     end with NUL; an empty record ends the parse, and so does a record
//     without "=". set (0x7cd5bc): a first byte ':' or 1 is removed (the
//     "persistent" mark), 2 or 3 is removed too; a key that holds a lock
//     (bit 1 or 2) only takes a value from a record with 2 or 3 (and not from
//     2 after a 3); otherwise the later record wins. lock (0x7cd7e0): the
//     first record with 2 or 3 sets lock bit 1 or 2. get (0x7cd4e8): a key
//     with lock bit 2 is not returned to a plain query.
//
// Android reads 72005 through com.gs.nvram.NvramManager.getBool("72005"): the
// value "true" or "1" is true, anything else false (NvramManager.parser,
// boot-gsframework). The web UI shows "on" only for "true". So "1" is on.
package nvram

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
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

// AutoUpdate is what the store says. Firmware and APK are "true", "false",
// "other" (any other value), or "" (no such key or no store). URL is the
// value of 72016, printable ASCII only.
type AutoUpdate struct {
	// State is On when a part that Android may load gives 72005 a value that
	// Android reads as true ("true" or "1"). It is Off only when the driver
	// certainly loads one part, as the driver itself writes parts, and that
	// part gives 72005 the value "false". Unknown (not clear) otherwise.
	State         string
	Firmware, APK string
	URL           string
	// Part is the active part (0..31), -1 when none is certain. Counter is
	// the highest write counter. Valid is the number of valid parts.
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

// Checksum is the driver's checksum of the zlib stream of a part, for a zlib
// length n with 0 <= n <= DataSize.
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

const (
	// DataSize is the room after the header in a part.
	DataSize = PartSize - HeaderSize // 0xffd8
	// maxText is the output room of the driver's inflate.
	maxText = 0x1ffd8
	// forced0 and forced1 are the text offsets that parse sets to NUL.
	forced0, forced1 = maxText - 2, maxText - 1
)

// header is the part header fields that the driver uses.
type header struct {
	magic            bool
	flags            uint16
	textLen, zlibLen uint32
	sum              uint16
	counter          uint64
}

func readHeader(p []byte) header {
	le := binary.LittleEndian
	return header{magic: string(p[:4]) == Magic, flags: le.Uint16(p[0x10:]), textLen: le.Uint32(p[4:]),
		zlibLen: le.Uint32(p[12:]), sum: binary.BigEndian.Uint16(p[0x12:]), counter: le.Uint64(p[0x18:])}
}

// The results of the driver's checksum check.
const (
	rejected = iota // no magic, or a wrong checksum: the driver skips it
	accepted        // magic and checksum
	maybe           // magic, and a checksum over bytes past the part
)

// check is the driver's header check (0x7cc998 with 0x7cc33c).
func check(p []byte, h header) int {
	if !h.magic {
		return rejected
	}
	m := int32(h.zlibLen+1) &^ 1
	switch {
	case m <= 0:
		if h.sum == 0 {
			return accepted
		}
		return rejected
	case m > DataSize:
		return maybe
	case Checksum(p[HeaderSize:], int(m)) == h.sum:
		return accepted
	}
	return rejected
}

// What the driver's get gives for a key.
type value struct {
	set bool // false: no value (no key, or hidden by lock bit 2)
	v   string
}

// reading is what one load of one part gives.
type reading struct {
	part     int
	nondet   bool     // the result depends on memory that this package cannot see
	vals     [3]value // 72005, 72012, 72016 as get returns them
	fwRecs   []string // every 72005 record that the parse applied
	nRecs    [3]int   // the number of records of each key
	locked   bool     // a 72005 record had the prefix 2 or 3
	odd      []string // why the part is not as the driver's own commit writes it
	textLen  int      // the inflated text length; -1: none
	headerTL uint32
	why      string // why there is no text
}

func (r *reading) say(f string, a ...any) { r.odd = append(r.odd, fmt.Sprintf(f, a...)) }

// load is the driver's load of part i in a world where the scan accepted it
// (acc) or not.
func load(ring []byte, i int, acc bool) reading {
	p := ring[i*PartSize : (i+1)*PartSize]
	h := readHeader(p)
	r := reading{part: i, textLen: -1, headerTL: h.textLen}
	if !h.magic {
		r.why = "no magic FLSH"
		return r
	}
	n := uint64(h.zlibLen)
	if h.flags == 0 {
		r.say("flags 0 (no checksum check)")
		n = DataSize
	} else {
		if !acc {
			r.why = "a wrong checksum"
			return r
		}
		if h.flags != 1 {
			r.say("flags %d", h.flags)
		}
	}
	more := false
	if n > DataSize {
		r.say("the zlib length %#x is past the part", h.zlibLen)
		n, more = DataSize, true
	}
	text, err := inflate(p[HeaderSize:HeaderSize+int(n)], more)
	switch {
	case errors.Is(err, errPast):
		r.nondet, r.why = true, err.Error()
		return r
	case err != nil:
		r.why = err.Error()
		return r
	}
	r.textLen = len(text)
	if uint32(len(text)) != h.textLen {
		r.say("the text is %d bytes, the header says %d", len(text), h.textLen)
	}
	parse(&r, text)
	return r
}

var errPast = errors.New("the zlib stream needs bytes past the part")

// inflate decompresses as the driver does: success only when the stream ends
// with its adler check within maxText bytes of output. more: the driver's
// input goes on past these bytes (into memory this package cannot see).
func inflate(z []byte, more bool) ([]byte, error) {
	short := func() error {
		if more {
			return errPast
		}
		return errors.New("the zlib stream is incomplete")
	}
	zr, err := zlib.NewReader(bytes.NewReader(z))
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			return nil, short()
		}
		return nil, errors.New("the zlib stream does not open")
	}
	text, err := io.ReadAll(io.LimitReader(zr, maxText+1))
	switch {
	case len(text) > maxText:
		return nil, errors.New("the text is longer than the driver's buffer")
	case errors.Is(err, io.ErrUnexpectedEOF):
		return nil, short()
	case err != nil:
		return nil, errors.New("the zlib stream is corrupt")
	}
	return text, nil
}

var keys = [3]string{KeyFirmware, KeyAPK, KeyURL}

type tuple struct {
	v     string
	flags int // bit 0 persistent, bit 1 lock (2), bit 2 lock and hidden (3)
}

// parse runs the driver's record parse over the text and keeps the three keys.
func parse(r *reading, text []byte) {
	at := func(i int) (byte, bool) {
		switch {
		case i == forced0 || i == forced1:
			return 0, true
		case i < len(text):
			return text[i], true
		}
		return 0, false
	}
	tab := map[string]*tuple{}
	defer func() {
		for k, key := range keys {
			if t := tab[key]; t != nil && t.flags&4 == 0 {
				r.vals[k] = value{set: true, v: t.v}
			}
		}
	}()
	for pos := 0; ; {
		b, ok := at(pos)
		if !ok {
			r.nondet = true
			r.say("the record list has no end in the text: the driver reads on into memory that is not cleared")
			return
		}
		if b == 0 {
			return
		}
		eq := pos
		for ; ; eq++ {
			if b, ok = at(eq); !ok || b == 0 || b == '=' {
				break
			}
		}
		if !ok {
			r.nondet = true
			r.say("a record runs past the text into memory that is not cleared")
			return
		}
		if b == 0 {
			r.say("a record without \"=\" ends the driver's parse")
			return
		}
		end := eq + 1
		for ; ; end++ {
			if b, ok = at(end); !ok || b == 0 {
				break
			}
		}
		if !ok {
			r.nondet = true
			r.say("a record runs past the text into memory that is not cleared")
			return
		}
		set(r, tab, text[pos:eq], text[eq+1:end])
		pos = end + 1
	}
}

// set is the driver's set and lock of one record, for the three keys only.
func set(r *reading, tab map[string]*tuple, name, val []byte) {
	var pre byte
	if len(name) > 0 {
		pre = name[0]
	}
	key := name
	persist, attr := pre == ':' || pre == 1, pre == 2 || pre == 3
	if persist || attr {
		key = name[1:]
	}
	k := -1
	for i, want := range keys {
		if string(key) == want {
			k = i
		}
	}
	if k < 0 {
		return
	}
	v := printable(val)
	r.nRecs[k]++
	if k == 0 {
		r.fwRecs = append(r.fwRecs, v)
		r.locked = r.locked || attr
	}
	t := tab[keys[k]]
	refused := t != nil && t.flags&6 != 0 && (!attr || (pre == 2 && t.flags&4 != 0))
	if !refused {
		if t == nil {
			t = &tuple{}
			tab[keys[k]] = t
		} else if !persist {
			t.flags &^= 1
		}
		t.v = v
		if persist {
			t.flags |= 1
		}
	}
	if attr {
		if t == nil {
			t = &tuple{}
			tab[keys[k]] = t
		}
		if t.flags&6 == 0 {
			t.flags |= 2 << (pre - 2)
		}
	}
}

// isTrue is NvramManager.getBool: "true" and "1" are true.
func isTrue(v string) bool { return v == "true" || v == "1" }

// trueIn says why the reading may give Android a true 72005, or "".
func (r *reading) trueIn() string {
	switch {
	case r.vals[0].set && isTrue(r.vals[0].v):
		return fmt.Sprintf("%s=%s", KeyFirmware, r.vals[0].v)
	}
	for _, v := range r.fwRecs {
		if isTrue(v) {
			if r.nondet {
				return fmt.Sprintf("a %s=%s record before memory that is not cleared", KeyFirmware, v)
			}
			return fmt.Sprintf("%s records, one of them %s", KeyFirmware, v)
		}
	}
	return ""
}

// Parse reads the ring (RingSize bytes from RingOffset of others) and returns
// the auto-update state of the store as the Android driver loads it.
func Parse(ring []byte) AutoUpdate {
	au := AutoUpdate{State: Unknown, Part: -1}
	if len(ring) < RingSize {
		au.Why = fmt.Sprintf("the nvram ring is %d bytes, not %d", len(ring), RingSize)
		return au
	}
	var heads [Parts]header
	var kind [Parts]int
	var maybes []int
	for i := range heads {
		heads[i] = readHeader(ring[i*PartSize:])
		kind[i] = check(ring[i*PartSize:(i+1)*PartSize], heads[i])
		switch kind[i] {
		case accepted:
			au.Valid++
		case maybe:
			maybes = append(maybes, i)
		}
	}
	// active is the scan's result when the parts in kind are accepted, plus
	// extra (-1: none). found: some part counted.
	active := func(extra int) (act int, max uint64, found bool) {
		for i := range heads {
			if (kind[i] == accepted || i == extra) && heads[i].counter >= max {
				act, max, found = i, heads[i].counter, true
			}
		}
		return
	}
	act, max, found := active(-1)
	au.Counter = max
	var top []int // the accepted parts with the highest counter
	for i := range heads {
		if found && kind[i] == accepted && heads[i].counter == max {
			top = append(top, i)
		}
	}
	// Every part that Android may load: the scan's active part (or the
	// default part 0), each tied part, and each part whose checksum reads past
	// the part when it would be the active one.
	reads := []reading{load(ring, act, found)}
	for _, i := range top {
		if i != act {
			reads = append(reads, load(ring, i, true))
		}
	}
	var past []int
	for _, i := range maybes {
		if a, _, _ := active(i); a == i {
			past = append(past, i)
			reads = append(reads, load(ring, i, true))
		}
	}
	for _, r := range reads {
		if why := r.trueIn(); why != "" {
			au.State, au.Part = On, r.part
			au.Firmware, au.APK, au.URL = "true", flag(r.vals[1]), r.vals[2].v
			switch {
			case !found:
				au.Why = fmt.Sprintf("no valid nvram part; the driver loads part 0: %s", why)
			case r.part == act && len(top) == 1 && len(past) == 0:
				au.Why = fmt.Sprintf("active part %d of %d (the highest write counter, %d; %d valid parts): %s", act, Parts, max, au.Valid, why)
			default:
				au.Why = fmt.Sprintf("part %d (write counter %d): %s", r.part, heads[r.part].counter, why)
				if len(top) > 1 {
					au.Why += fmt.Sprintf("; parts %s have this highest write counter", intList(top))
				}
				switch {
				case slices.Contains(past, r.part):
					au.Why += "; its zlib length is past the part, so the driver may load it"
				case len(past) > 0:
					au.Why += fmt.Sprintf("; part %d has the write counter %d and a zlib length past the part", past[0], heads[past[0]].counter)
				}
			}
			return au
		}
	}
	r := reads[0]
	switch {
	case !found:
		au.Why = "no valid nvram part (magic FLSH and checksum)"
		if r.textLen >= 0 {
			au.Why += "; the driver loads part 0 with flags 0"
		}
		return au
	case len(past) > 0:
		au.Why = fmt.Sprintf("part %d has the write counter %d and a zlib length past the part", past[0], heads[past[0]].counter)
		return au
	case len(top) > 1:
		au.Why = fmt.Sprintf("parts %s have the same highest write counter %d", intList(top), max)
		return au
	}
	au.Part = act
	at := fmt.Sprintf("active part %d (write counter %d)", act, max)
	switch {
	case r.textLen < 0:
		au.Why = fmt.Sprintf("%s: %s (the driver loads an empty store)", at, r.why)
		return au
	case len(r.odd) > 0:
		au.Why = fmt.Sprintf("%s: %s", at, r.odd[0])
		return au
	}
	for k, key := range keys {
		if r.nRecs[k] > 1 {
			au.Why = fmt.Sprintf("%s: key %s is there twice", at, key)
			return au
		}
	}
	au.Firmware, au.APK, au.URL = flag(r.vals[0]), flag(r.vals[1]), r.vals[2].v
	switch {
	case r.locked && len(r.fwRecs) > 0:
		au.Why = fmt.Sprintf("%s: %s has a lock prefix (2 or 3)", at, KeyFirmware)
	case !r.vals[0].set:
		au.Why = fmt.Sprintf("%s has no key %s", at, KeyFirmware)
	case r.vals[0].v == "false":
		au.State = Off
		au.Why = fmt.Sprintf("active part %d of %d (the highest write counter, %d; %d valid parts)", act, Parts, max, au.Valid)
	default:
		au.Why = fmt.Sprintf("%s: %s is neither true nor false", at, KeyFirmware)
	}
	return au
}

// flag keeps the value of a true/false key only when it is true or false, so
// that no other text comes out of a flag key. "" means no value.
func flag(v value) string {
	switch {
	case !v.set:
		return ""
	case v.v == "true" || v.v == "false":
		return v.v
	}
	return "other"
}

func intList(l []int) string {
	var s []string
	for _, i := range l {
		s = append(s, fmt.Sprint(i))
	}
	if len(s) == 2 {
		return s[0] + " and " + s[1]
	}
	return strings.Join(s, ", ")
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
