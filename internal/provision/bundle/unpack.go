package bundle

import (
	"bytes"
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"
)

// Pins are the profile's pins of its bundle (profiles.json): the size and
// sha256 of the whole file, the sha256 of bundle.json (index_sha256), and
// what bundle.json must say (nil: only its own rules; the receiver of the
// cable, whose bundle.json is pinned by its sha256 and checked against
// profiles.json by the root again).
type Pins struct {
	Size        int64
	SHA256      string
	IndexSHA256 string
	Expect      *Expect
}

// Sink is where an Unpacker writes the members (a folder: the root's RAM
// repo, or the receiver's session folder).
type Sink interface {
	// Create makes the file at the member path (relative, "/"-separated;
	// its folders made as needed) for size bytes. It must not exist.
	Create(path string, size int64) (io.WriteCloser, error)
	// Remove removes a file that Create made (a member that did not
	// complete, or whose sha256 is not its line's).
	Remove(path string) error
}

// Error is an error of the bundle stream. Sink is true when the Sink failed
// (a full RAM folder, a closed session), false when the bytes are not the
// pinned bundle.
type Error struct {
	Sink bool
	Err  error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

func bad(f string, a ...any) error { return &Error{Err: fmt.Errorf(f, a...)} }

// IsBundle: err says that the bytes are not the pinned bundle.
func IsBundle(err error) bool {
	var e *Error
	return errors.As(err, &e) && !e.Sink
}

type phase int

const (
	phHeader phase = iota
	phData
	phPad
	phTrailer
	phBroken
)

// checkpoint is the state at the start of a member header: a resume of the
// stream starts there.
type checkpoint struct {
	off   int64
	state []byte // the marshalled sha256 of the stream so far
	idx   int
	doc   *Doc
}

// Unpacker reads a bundle stream (Write) and writes its members through a
// Sink. Nothing of a member is written before its header matched its line,
// and bundle.json is written only after its sha256 is the pin and it parsed.
// After an error the Unpacker takes no byte until Resume or Reset.
type Unpacker struct {
	pins Pins
	sink Sink
	sum  hash.Hash // the whole stream
	off  int64

	ph   phase
	hdr  [512]byte
	hn   int
	idx  int // -1: bundle.json next; then the index into members
	doc  *Doc
	mem  []Member
	cur  Member
	left int64
	pad  int64
	csum hash.Hash
	w    io.WriteCloser
	open bool // w is open (a member in progress)
	full bool // the current member's data is complete and in the sink (its padding is not read yet)
	dbuf []byte
	zero int64 // trailer bytes
	err  error
	cp   checkpoint
}

// NewUnpacker checks the pins and starts at offset 0.
func NewUnpacker(p Pins, s Sink) (*Unpacker, error) {
	if p.Size < 3*512 || !IsSHA256(p.SHA256) || !IsSHA256(p.IndexSHA256) || s == nil {
		return nil, fmt.Errorf("bundle pins: size %d sha256 %q index %q", p.Size, p.SHA256, p.IndexSHA256)
	}
	u := &Unpacker{pins: p, sink: s}
	u.Reset()
	return u, nil
}

// Reset starts again at offset 0 (the caller empties the Sink first).
func (u *Unpacker) Reset() {
	u.closeCur(false)
	u.sum = sha256.New()
	u.off, u.ph, u.hn, u.idx, u.doc, u.mem, u.zero, u.err, u.dbuf, u.full = 0, phHeader, 0, -1, nil, nil, 0, nil, nil, false
	u.cp = checkpoint{off: 0, state: u.marshal(), idx: -1}
}

func (u *Unpacker) marshal() []byte {
	b, err := u.sum.(encoding.BinaryMarshaler).MarshalBinary()
	if err != nil {
		panic(err) // crypto/sha256 always marshals
	}
	return b
}

// Offset is the number of stream bytes taken.
func (u *Unpacker) Offset() int64 { return u.off }

// Doc is bundle.json once it was checked (nil before).
func (u *Unpacker) Doc() *Doc { return u.doc }

// DocBytes are the bytes of bundle.json once it was checked.
func (u *Unpacker) DocBytes() []byte { return u.dbuf }

// Members is the number of members done (bundle.json counted) and the
// number there are (0 until bundle.json was read).
func (u *Unpacker) Members() (done, total int) {
	if u.doc == nil {
		return 0, 0
	}
	return u.idx + 1, len(u.mem) + 1
}

// Resume drops the member in progress (its file is removed) and goes back
// to the start of its header, the last checkpoint: the caller sends the
// stream again from the offset it returns (an HTTP Range).
func (u *Unpacker) Resume() int64 {
	u.closeCur(true)
	if u.full {
		// a cut in the padding after a complete member: the checkpoint is
		// still at its header, so its file goes too (review MINOR-2)
		u.sink.Remove(u.cur.Path)
		u.full = false
	}
	c := u.cp
	s := sha256.New()
	if err := s.(encoding.BinaryUnmarshaler).UnmarshalBinary(c.state); err != nil {
		panic(err)
	}
	u.sum, u.off, u.idx, u.doc, u.ph, u.hn, u.zero, u.err = s, c.off, c.idx, c.doc, phHeader, 0, 0, nil
	if c.doc == nil {
		u.mem, u.dbuf = nil, nil
	}
	return c.off
}

func (u *Unpacker) closeCur(remove bool) {
	if u.open {
		u.w.Close()
		if remove {
			u.sink.Remove(u.cur.Path)
		}
	}
	u.w, u.open = nil, false
}

func (u *Unpacker) fail(err error) (int, error) {
	u.err, u.ph = err, phBroken
	return 0, err
}

// Write takes the next bytes of the stream (io.Writer: it takes all of b or
// fails).
func (u *Unpacker) Write(b []byte) (int, error) {
	if u.ph == phBroken {
		return 0, u.err
	}
	if int64(len(b)) > u.pins.Size-u.off {
		return u.fail(bad("the stream is longer than the pinned %d bytes", u.pins.Size))
	}
	n := len(b)
	for len(b) > 0 {
		var k int
		switch u.ph {
		case phHeader:
			k = 512 - u.hn
		case phData:
			k = int(min(u.left, int64(len(b))))
		case phPad:
			k = int(min(u.pad, int64(len(b))))
		default:
			k = len(b)
		}
		k = min(k, len(b))
		seg := b[:k]
		b = b[k:]
		// the stream's sha256 and offset first: a checkpoint (boundary) in
		// the step below holds exactly the bytes up to it
		u.sum.Write(seg)
		u.off += int64(k)
		var err error
		switch u.ph {
		case phHeader:
			u.hn += copy(u.hdr[u.hn:], seg)
			if u.hn == 512 {
				err = u.header()
			}
		case phData:
			err = u.data(seg)
		case phPad:
			if !allZero(seg) {
				err = bad("%s: the padding after it is not zero", u.cur.Path)
			}
			u.pad -= int64(k)
			if err == nil && u.pad == 0 {
				u.boundary()
			}
		case phTrailer:
			if !allZero(seg) {
				err = bad("a member after the last line of bundle.json (or bytes after the end of the archive)")
			}
			u.zero += int64(k)
		}
		if err != nil {
			return u.fail(err)
		}
	}
	return n, nil
}

// boundary: the member and its padding are done; a checkpoint at the start
// of the next header.
func (u *Unpacker) boundary() {
	u.full = false
	u.idx++
	u.ph, u.hn = phHeader, 0
	u.cp = checkpoint{off: u.off, state: u.marshal(), idx: u.idx, doc: u.doc}
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

func (u *Unpacker) header() error {
	h := u.hdr[:]
	if allZero(h) {
		if u.doc == nil || u.idx < len(u.mem) {
			want := DocName
			if u.doc != nil {
				want = u.mem[u.idx].Path
			}
			return bad("the archive ends before %s", want)
		}
		u.ph, u.zero = phTrailer, 512
		return nil
	}
	name, size, err := parseHeader(h)
	if err != nil {
		return err
	}
	switch {
	case u.doc == nil:
		if name != DocName || size <= 0 || size > MaxDoc {
			return bad("the first member is %q of %d bytes, not %s (1 to %d bytes)", name, size, DocName, MaxDoc)
		}
		u.cur = Member{Path: DocName, Size: size, SHA256: u.pins.IndexSHA256}
	case u.idx >= len(u.mem):
		return bad("a member %q after the last line of bundle.json", name)
	default:
		m := u.mem[u.idx]
		if name != m.Path || size != m.Size {
			return bad("member %q of %d bytes is not the next line of bundle.json, %s of %d bytes", name, size, m.Path, m.Size)
		}
		u.cur = m
		w, err := u.sink.Create(m.Path, m.Size)
		if err != nil {
			return &Error{Sink: true, Err: fmt.Errorf("%s: %v", m.Path, err)}
		}
		u.w, u.open = w, true
	}
	u.left, u.pad, u.csum, u.ph = size, (512-size%512)%512, sha256.New(), phData
	if u.doc == nil {
		u.dbuf = make([]byte, 0, size)
	}
	return nil
}

func (u *Unpacker) data(b []byte) error {
	u.csum.Write(b)
	u.left -= int64(len(b))
	if u.doc == nil {
		u.dbuf = append(u.dbuf, b...)
	} else if _, err := u.w.Write(b); err != nil {
		return &Error{Sink: true, Err: fmt.Errorf("%s: %v", u.cur.Path, err)}
	}
	if u.left > 0 {
		return nil
	}
	sum := hex.EncodeToString(u.csum.Sum(nil))
	if u.doc == nil {
		if sum != u.pins.IndexSHA256 {
			return bad("bundle.json has sha256 %s, profiles.json pins %s", sum, u.pins.IndexSHA256)
		}
		d, err := ParseDoc(u.dbuf, u.pins.Expect)
		if err != nil {
			return bad("%v", err)
		}
		mem := d.Members()
		var need int64 = 512 + roundUp(int64(len(u.dbuf))) + 1024
		for _, m := range mem {
			need += 512 + roundUp(m.Size)
		}
		if need > u.pins.Size {
			return bad("bundle.json names %d bytes of members, more than the pinned %d bytes", need, u.pins.Size)
		}
		w, err := u.sink.Create(DocName, int64(len(u.dbuf)))
		if err != nil {
			return &Error{Sink: true, Err: fmt.Errorf("%s: %v", DocName, err)}
		}
		_, err = w.Write(u.dbuf)
		if cerr := w.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			u.sink.Remove(DocName)
			return &Error{Sink: true, Err: fmt.Errorf("%s: %v", DocName, err)}
		}
		u.doc, u.mem = d, mem
	} else {
		err := u.w.Close()
		u.w, u.open = nil, false
		if err != nil {
			u.sink.Remove(u.cur.Path)
			return &Error{Sink: true, Err: fmt.Errorf("%s: %v", u.cur.Path, err)}
		}
		if sum != u.cur.SHA256 {
			u.sink.Remove(u.cur.Path)
			return bad("%s has sha256 %s, bundle.json says %s", u.cur.Path, sum, u.cur.SHA256)
		}
	}
	u.ph, u.full = phPad, true
	if u.pad == 0 {
		u.boundary()
	}
	return nil
}

func roundUp(n int64) int64 { return (n + 511) / 512 * 512 }

// Finish checks the end of the stream: every member, the end of the archive
// (at least two zero blocks, then only zero bytes), exactly the pinned size
// and the pinned sha256.
func (u *Unpacker) Finish() error {
	switch {
	case u.ph == phBroken:
		return u.err
	case u.off != u.pins.Size:
		return &Error{Err: fmt.Errorf("the stream ended after %d of %d bytes", u.off, u.pins.Size)}
	case u.ph != phTrailer || u.zero < 1024:
		return bad("the archive has no end (two zero blocks) within the pinned size")
	}
	if s := hex.EncodeToString(u.sum.Sum(nil)); s != u.pins.SHA256 {
		u.err, u.ph = bad("the bundle has sha256 %s, profiles.json pins %s", s, u.pins.SHA256), phBroken
		return u.err
	}
	return nil
}

// parseHeader reads a ustar header of a bundle member: the checksum, the
// magic "ustar\0" "00", a regular file (type '0'), mode 0644, uid and gid 0,
// mtime 0, no link name, no user or group name, device numbers 0, the
// padding zero; the name is prefix "/" name.
func parseHeader(h []byte) (string, int64, error) {
	stored, err := octal(h[148:156])
	if err != nil {
		return "", 0, bad("a header checksum %q", h[148:156])
	}
	var s int64
	for i, c := range h {
		if i >= 148 && i < 156 {
			c = ' '
		}
		s += int64(c)
	}
	if s != stored {
		return "", 0, bad("a header with the checksum %d, its bytes sum to %d", stored, s)
	}
	if string(h[257:263]) != "ustar\x00" || string(h[263:265]) != "00" {
		return "", 0, bad("not a ustar header (magic %q %q)", h[257:263], h[263:265])
	}
	name := cstr(h[0:100])
	if p := cstr(h[345:500]); p != "" {
		name = p + "/" + name
	}
	var nums [6]int64
	for i, f := range [][2]int{{100, 108}, {108, 116}, {116, 124}, {136, 148}, {329, 337}, {337, 345}} {
		if nums[i], err = octal(h[f[0]:f[1]]); err != nil {
			return "", 0, bad("%s: a header field %q", name, h[f[0]:f[1]])
		}
	}
	size, err := octal(h[124:136])
	if err != nil {
		return "", 0, bad("%s: the size field %q", name, h[124:136])
	}
	switch {
	case h[156] != '0' && h[156] != 0:
		return "", 0, bad("%s: type %q, not a regular file", name, h[156])
	case !allZero(h[157:257]) || !allZero(h[265:297]) || !allZero(h[297:329]) || !allZero(h[500:512]):
		return "", 0, bad("%s: a link name, a user or group name, or padding in the header", name)
	case nums[0] != 0o644 || nums[1] != 0 || nums[2] != 0 || nums[3] != 0 || nums[4] != 0 || nums[5] != 0:
		return "", 0, bad("%s: mode %#o uid %d gid %d mtime %d dev %d:%d (want 0644, 0, 0, 0, 0:0)", name, nums[0], nums[1], nums[2],
			nums[3], nums[4], nums[5])
	case name == "" || strings.ContainsRune(name, 0):
		return "", 0, bad("a member without a name")
	}
	return name, size, nil
}

// cstr is a NUL-terminated field.
func cstr(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

// octal reads a numeric field: spaces, octal digits, then NUL or space to
// its end (empty: 0).
func octal(b []byte) (int64, error) {
	i := 0
	for i < len(b) && b[i] == ' ' {
		i++
	}
	var v int64
	n := 0
	for ; i < len(b) && b[i] >= '0' && b[i] <= '7'; i++ {
		if n++; n > 20 {
			return 0, errors.New("too long")
		}
		v = v*8 + int64(b[i]-'0')
	}
	for ; i < len(b); i++ {
		if b[i] != 0 && b[i] != ' ' {
			return 0, fmt.Errorf("byte %q", b[i])
		}
	}
	return v, nil
}
