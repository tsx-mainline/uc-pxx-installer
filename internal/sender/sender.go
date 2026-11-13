// SPDX-License-Identifier: GPL-2.0-or-later

// Package sender is the host side of the step-2 USB transport (uc-pxx-xfer on
// the running mainline slot): a command-line sender that drives the same
// PXX1 frame protocol as the installer page (web/site/js/step2usb.js and
// finishusb.js), so a step-2 payload or a finish set can be sent from a host
// with no browser (owner 2026-11-12: a host-side CLI sender, drivable from a
// build host).
//
// The unit is the authority, exactly as for the page. The sender runs the
// page-side pre-checks: the first file (manifest.json or finish.json) has the
// sha256 that the caller names (--expect-sha256, the set a RULES line names)
// and the unit's HELLO pins it; HELLO's proto and USB id are the reviewed
// ones; the two files have the sizes and sha256 the manifest/list names; every
// file is a regular file, opened once (no link followed) and sent from that
// open. Then it sends the first file, the two files and COMMIT, and compares
// each PUT_END sha256 with the file's. COMMIT never asks the unit to go on by
// itself (no AUTO-STEP2 from this tool: RC-6 sender review MAJOR-1; step 2
// waits for the panel's hold or, on a supervised unit, the supervised CLI).
// The unit takes a first file only when its sha256 is pinned, then only the
// two files it names with their exact sizes, and checks their sha256 itself.
//
// The sender opens NO block device and writes NOTHING to the unit's storage.
// Its only side effect on the unit is RAM: the receiver stages the files in a
// tmpfs folder. Step 2 (and the finish) start only on the unit, by the slot's
// own reviewed rules. The transport is an interface (Transport); the real one
// is a usbfs bulk transport (cmd/uc-pxx-send/usbfs_linux.go), and the tests pair
// the driver with a live internal/xfer.Receiver over an in-memory link.
package sender

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/xfer"
	"uc-pxx-installer/internal/xfer/handover"
)

// The three classes of a failure (the exit codes of cmd/uc-pxx-send).
var (
	// ErrLocal: refused on the host before any byte of the set was sent
	// (the set, the expected sha, or the unit's HELLO does not fit).
	ErrLocal = errors.New("refused before any byte of the set was sent")
	// ErrTransport: the USB link failed (a transfer, a reply, a cut).
	ErrTransport = errors.New("the USB link failed")
	// ErrUnit: the unit refused a file or the set.
	ErrUnit = errors.New("the unit refused")
)

// Transport is one session with the unit's vendor-class interface: a host
// bulk OUT transfer (Out) and a host bulk IN transfer (In), each one whole
// USB transfer, as the page's usbio.js does. The driver sends one frame or
// one data chunk per Out and reads one reply frame per In. A short or stalled
// transfer is an error; nothing is ever aborted mid-transfer.
type Transport interface {
	Out(b []byte) error
	In() ([]byte, error)
	Close() error
}

// Kind is which set this sender sends.
type Kind int

const (
	Payload Kind = iota // a step-2 payload: manifest.json + boot-template.img + rootfs.ext4
	Finish              // a finish set: finish.json + uc-pxx-step2-finish + uc-pxx-bootctl-inst
)

// Mode is the HELLO mode in which the unit takes the kind.
func (k Kind) Mode() string { return map[Kind]string{Payload: "payload", Finish: "finish"}[k] }

// Logf logs a progress line.
type Logf func(format string, a ...any)

// file is one file of a set: its name in the set, its one open (read only,
// no link followed, a regular file), its size and sha256. The first file's
// bytes are in mem.
type file struct {
	name string
	fh   *os.File
	mem  []byte
	size int64
	sha  string
}

// Set is a set checked on the host and held open (PrepareSet). Close it.
type Set struct {
	Kind  Kind
	First string // the first file's sha256 (= the caller's expected sha)
	files []file
}

// Close closes every file of the set.
func (s *Set) Close() {
	for _, f := range s.files {
		if f.fh != nil {
			f.fh.Close()
		}
	}
}

// Size is the bytes of the whole set.
func (s *Set) Size() int64 {
	var n int64
	for _, f := range s.files {
		n += f.size
	}
	return n
}

// HelloInfo is the part of the unit's HELLO the sender acts on.
type HelloInfo struct {
	Proto int      `json:"proto"`
	USB   string   `json:"usb"`
	Mode  string   `json:"mode"`
	State string   `json:"state"`
	Pins  []string `json:"pins"`
	Room  int64    `json:"room"`
}

type putAnswer struct {
	Accept bool   `json:"accept"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

type putEnd struct {
	OK     bool   `json:"ok"`
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Reason string `json:"reason"`
}

type commitAnswer struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason"`
	Auto   bool   `json:"auto"`
}

// Driver sends one set over a transport.
type Driver struct {
	t   Transport
	seq uint32
	log Logf
}

// New makes a Driver. log may be nil.
func New(t Transport, log Logf) *Driver {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &Driver{t: t, log: log}
}

// frame sends one request frame of typ and obj and reads the matching reply,
// decoding its body into out (out may be nil).
func (d *Driver) frame(typ uint16, obj any, out any) error {
	var j []byte
	if obj != nil {
		var err error
		if j, err = json.Marshal(obj); err != nil {
			return err
		}
	} else {
		j = []byte("{}")
	}
	d.seq++
	seq := d.seq
	fr, err := xfer.EncodeFrame(typ, seq, j)
	if err != nil {
		return err
	}
	if err := d.t.Out(fr); err != nil {
		return fmt.Errorf("%w: sending the request: %v", ErrTransport, err)
	}
	return d.readReply(typ, seq, out)
}

// readReply reads one reply frame and checks its type and seq.
func (d *Driver) readReply(typ uint16, seq uint32, out any) error {
	rb, err := d.t.In()
	if err != nil {
		return fmt.Errorf("%w: reading the reply: %v", ErrTransport, err)
	}
	// parse a reply frame (xfer.ParseFrame is the device side and refuses a
	// reply-typed frame, so the sender parses the header itself)
	if len(rb) < xfer.HeaderSize || string(rb[:4]) != xfer.Magic {
		return fmt.Errorf("%w: the reply is not a PXX1 frame (%d bytes)", ErrTransport, len(rb))
	}
	rtyp := binary.LittleEndian.Uint16(rb[4:])
	flags := binary.LittleEndian.Uint16(rb[6:])
	rlen := binary.LittleEndian.Uint32(rb[8:])
	rseq := binary.LittleEndian.Uint32(rb[12:])
	if flags != 0 {
		return fmt.Errorf("%w: reply flags %#x", ErrTransport, flags)
	}
	if uint64(len(rb)) != uint64(xfer.HeaderSize)+uint64(rlen) || rlen > xfer.MaxBody {
		return fmt.Errorf("%w: reply of %d bytes for a body of %d", ErrTransport, len(rb), rlen)
	}
	if rtyp != (typ|xfer.TypeReply) || rseq != seq {
		return fmt.Errorf("%w: reply type %#x seq %d, expected %#x seq %d", ErrTransport, rtyp, rseq, typ|xfer.TypeReply, seq)
	}
	if out != nil {
		if err := json.Unmarshal(rb[xfer.HeaderSize:], out); err != nil {
			return fmt.Errorf("%w: the reply body is not the expected JSON: %v", ErrTransport, err)
		}
	}
	return nil
}

// Hello reads the unit's state and pins.
func (d *Driver) Hello() (HelloInfo, error) {
	var h HelloInfo
	err := d.frame(xfer.TypeHello, struct{}{}, &h)
	return h, err
}

// DataChunk is the size of one data transfer (the page's DATA_CHUNK).
const DataChunk = xfer.DataChunk

// putFile sends one file: PUT_BEGIN, then the bytes in DataChunk transfers
// from its one open, then it reads PUT_END and compares its sha256. The unit
// answers PUT_BEGIN before any byte is sent; a refusal sends nothing.
func (d *Driver) putFile(f file) error {
	d.seq++
	seq := d.seq
	fr, err := xfer.EncodeFrame(xfer.TypePutBegin, seq, mustJSON(xfer.PutBegin{Name: f.name, Size: f.size}))
	if err != nil {
		return err
	}
	if err := d.t.Out(fr); err != nil {
		return fmt.Errorf("%w: %s: sending PUT_BEGIN: %v", ErrTransport, f.name, err)
	}
	var a putAnswer
	if err := d.readReply(xfer.TypePutBegin, seq, &a); err != nil {
		return err
	}
	if !a.Accept {
		return fmt.Errorf("%w %s: %s", ErrUnit, f.name, a.Reason)
	}
	var src io.Reader
	if f.mem != nil {
		src = bytes.NewReader(f.mem)
	} else {
		if _, err := f.fh.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrTransport, f.name, err)
		}
		src = f.fh
	}
	t0 := time.Now()
	buf := make([]byte, DataChunk)
	h := sha256.New()
	var sent int64
	for sent < f.size {
		n := int64(DataChunk)
		if left := f.size - sent; left < n {
			n = left
		}
		if _, err := io.ReadFull(src, buf[:n]); err != nil {
			return fmt.Errorf("%w: %s: reading at %d of %d bytes: %v", ErrTransport, f.name, sent, f.size, err)
		}
		h.Write(buf[:n])
		if err := d.t.Out(buf[:n]); err != nil {
			return fmt.Errorf("%w: %s: sending at %d of %d bytes: %v", ErrTransport, f.name, sent, f.size, err)
		}
		sent += n
		if f.size > 64<<20 && sent%(256<<20) < int64(DataChunk) {
			d.log("  %s: %d of %d MB", f.name, sent>>20, f.size>>20)
		}
	}
	var e putEnd
	if err := d.readReply(xfer.TypePutEnd, seq, &e); err != nil {
		return err
	}
	if !e.OK {
		return fmt.Errorf("%w %s: %s", ErrUnit, f.name, e.Reason)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != f.sha || e.SHA256 != f.sha || e.Name != f.name {
		return fmt.Errorf("%w: %s: sent sha256 %s, the unit says %q sha256 %s, the set names %s", ErrUnit, f.name, got, e.Name, e.SHA256, f.sha)
	}
	secs := time.Since(t0).Seconds()
	if secs <= 0 {
		secs = 0.001
	}
	d.log("%s sent (%d bytes, %.1f MB/s), the unit's sha256 %s = the set's", f.name, f.size, float64(f.size)/1e6/secs, e.SHA256)
	return nil
}

// commit tells the unit that the set is complete. It never asks the unit to
// go on by itself (auto false: the panel's hold, or the supervised CLI).
func (d *Driver) commit() error {
	var c commitAnswer
	if err := d.frame(xfer.TypeCommit, xfer.Commit{Auto: false}, &c); err != nil {
		return err
	}
	if !c.OK {
		return fmt.Errorf("%w the set: %s", ErrUnit, c.Reason)
	}
	if c.Auto {
		return fmt.Errorf("%w: the unit answered COMMIT with auto true for a commit without auto", ErrUnit)
	}
	return nil
}

// ReviewedUSB is the "usb" of the unit's HELLO (the dev id, owner Q-T4).
var ReviewedUSB = fmt.Sprintf("%04x:%04x", xfer.USBVendorID, xfer.USBProductID)

// CheckHello is the check of the unit's HELLO for a set: the reviewed proto
// and USB id, the mode of the set's kind, the set's first sha among the pins,
// and room for the set.
func CheckHello(h HelloInfo, s *Set) error {
	switch {
	case h.Proto != xfer.ProtoVersion:
		return fmt.Errorf("%w: the unit speaks proto %d, this sender %d", ErrLocal, h.Proto, xfer.ProtoVersion)
	case h.USB != ReviewedUSB:
		return fmt.Errorf("%w: the unit names the USB id %q, not %s", ErrLocal, h.USB, ReviewedUSB)
	case h.Mode != s.Kind.Mode():
		return fmt.Errorf("%w: the unit is in mode %q, not %q: it does not want this set now", ErrLocal, h.Mode, s.Kind.Mode())
	case len(h.Pins) == 0:
		return fmt.Errorf("%w: the unit pins nothing: it refuses every set now (not a step-2 keeper, or already done)", ErrLocal)
	case !pinned(s.First, h.Pins):
		return fmt.Errorf("%w: %s sha256 %s is not a set the unit pins (%v)", ErrLocal, s.files[0].name, s.First, h.Pins)
	case h.Room > 0 && s.Size() > h.Room:
		return fmt.Errorf("%w: the set is %d bytes, the unit has room for %d", ErrLocal, s.Size(), h.Room)
	}
	return nil
}

// Send runs the whole session for a prepared set: HELLO and its check, the
// first file, the two files it names, and COMMIT (no auto).
func (d *Driver) Send(s *Set) error {
	h, err := d.Hello()
	if err != nil {
		return err
	}
	d.log("unit: proto %d, usb %s, mode %q, state %q, %d pin(s), room %d bytes", h.Proto, h.USB, h.Mode, h.State, len(h.Pins), h.Room)
	if err := CheckHello(h, s); err != nil {
		return err
	}
	for _, f := range s.files {
		if err := d.putFile(f); err != nil {
			return err
		}
	}
	if err := d.commit(); err != nil {
		return err
	}
	d.log("the unit took the %s (first-file sha256 %s) into its RAM; step 2 or the finish starts only on the unit, by its own rules", s.Kind.Mode(), s.First)
	return nil
}

var shaRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// PrepareSet checks a set on the host before any device is opened: dir is a
// folder; the first file (manifest.json or finish.json) parses and has the
// sha256 expect (the set the caller names); the two files it names have
// those sizes and sha256. Each file is a regular file, opened once (read
// only, no link followed) and kept open for the send.
func PrepareSet(kind Kind, dir, expect string) (s *Set, err error) {
	if !shaRE.MatchString(expect) {
		return nil, fmt.Errorf("%w: the expected sha256 %q is not 64 lowercase hex", ErrLocal, expect)
	}
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("%w: %s is not a folder (%v)", ErrLocal, dir, err)
	}
	set := &Set{Kind: kind}
	defer func() {
		if err != nil {
			set.Close() // every file opened so far (the result is nil then)
		}
	}()
	s = set
	firstName, max := xfer.ManifestName, mainline.MaxManifest
	if kind == Finish {
		firstName, max = handover.FinishListName, handover.MaxFinishList
	} else if kind != Payload {
		return nil, fmt.Errorf("%w: unknown kind", ErrLocal)
	}
	ff, err := openRegular(filepath.Join(dir, firstName))
	if err != nil {
		return nil, err
	}
	s.files = append(s.files, file{name: firstName, fh: ff})
	b, err := io.ReadAll(io.LimitReader(ff, int64(max)+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrLocal, firstName, err)
	}
	if len(b) == 0 || len(b) > max {
		return nil, fmt.Errorf("%w: %s is %d bytes (1 to %d)", ErrLocal, firstName, len(b), max)
	}
	s.First = shaHex(b)
	s.files[0].mem, s.files[0].size, s.files[0].sha = b, int64(len(b)), s.First
	if s.First != expect {
		return nil, fmt.Errorf("%w: %s has sha256 %s, not the expected %s", ErrLocal, firstName, s.First, expect)
	}
	type want struct {
		name string
		size int64
		sha  string
	}
	var ws []want
	switch kind {
	case Payload:
		m, err := mainline.ParseManifest(b)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrLocal, firstName, err)
		}
		ws = []want{{xfer.TemplateName, m.BootTemplate.Size, m.BootTemplate.SHA256}, {xfer.RootfsName, m.Rootfs.Size, m.Rootfs.SHA256}}
	case Finish:
		l, err := handover.ParseFinishList(b)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrLocal, firstName, err)
		}
		for _, x := range l.Files {
			ws = append(ws, want{x.Name, x.Size, x.SHA256})
		}
	}
	for _, w := range ws {
		fh, err := openRegular(filepath.Join(dir, w.name))
		if err != nil {
			return nil, err
		}
		s.files = append(s.files, file{name: w.name, fh: fh, size: w.size, sha: w.sha})
		fi, err := fh.Stat()
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrLocal, w.name, err)
		}
		if fi.Size() != w.size {
			return nil, fmt.Errorf("%w: %s is %d bytes, the manifest/list says %d (an incomplete download?)", ErrLocal, w.name, fi.Size(), w.size)
		}
		h := sha256.New()
		if _, err := io.Copy(h, fh); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrLocal, w.name, err)
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != w.sha {
			return nil, fmt.Errorf("%w: %s has sha256 %s, the manifest/list says %s (a damaged download?)", ErrLocal, w.name, got, w.sha)
		}
	}
	return s, nil
}

// openRegular opens path once, read only, without following a link and
// without blocking on a FIFO, and refuses anything but a regular file (a
// device node, a FIFO, a socket, a folder): the sender opens no block device.
func openRegular(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v (a link is not followed)", ErrLocal, filepath.Base(path), err)
	}
	f := os.NewFile(uintptr(fd), path)
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("%w: %s is not a regular file (%v)", ErrLocal, filepath.Base(path), err)
	}
	return f, nil
}

func shaHex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func pinned(sha string, pins []string) bool {
	for _, p := range pins {
		if p == sha {
			return true
		}
	}
	return false
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
