// SPDX-License-Identifier: GPL-2.0-or-later

// Package sender is the host side of the step-2 USB transport (uc-pxx-xfer on
// the running mainline slot): a command-line sender that drives the same
// PXX1 frame protocol as the installer page (web/site/js/step2usb.js and
// finishusb.js), so a step-2 payload or a finish set can be sent from a host
// with no browser (owner 2026-11-12: a host-side CLI sender, drivable from a
// build host).
//
// The unit is the authority, exactly as for the page. The sender runs the
// cheap page-side pre-checks (the manifest or list parses, its sha256 is one
// the slot's HELLO pins, and the two files have the sizes and sha256 the
// manifest/list names), then sends the first file, the two files and COMMIT.
// The unit takes a first file only when its sha256 is pinned, then only the
// two files it names with their exact sizes, and checks their sha256 itself.
//
// The sender opens NO block device and writes NOTHING to the unit's storage.
// Its only side effect on the unit is RAM: the receiver stages the files in a
// tmpfs folder. Step 2 (and the finish) start only on the unit, by the slot's
// own reviewed rules. The transport is an interface (Transport); the real one
// is a usbfs bulk transport (transport_usbfs.go, linux), and the tests pair
// the driver with a live internal/xfer.Receiver over an in-memory link.
package sender

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/xfer"
	"uc-pxx-installer/internal/xfer/handover"
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

// Logf logs a progress line.
type Logf func(format string, a ...any)

// file is one file to send: its name in the set and its path on the host.
type file struct {
	name string
	path string
	size int64
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
		return fmt.Errorf("sending the request: %w", err)
	}
	return d.readReply(typ, seq, out)
}

// readReply reads one reply frame and checks its type and seq.
func (d *Driver) readReply(typ uint16, seq uint32, out any) error {
	rb, err := d.t.In()
	if err != nil {
		return fmt.Errorf("reading the reply: %w", err)
	}
	// parse a reply frame (xfer.ParseFrame is the device side and refuses a
	// reply-typed frame, so the sender parses the header itself)
	if len(rb) < xfer.HeaderSize || string(rb[:4]) != xfer.Magic {
		return fmt.Errorf("the reply is not a PXX1 frame (%d bytes)", len(rb))
	}
	rtyp := binary.LittleEndian.Uint16(rb[4:])
	flags := binary.LittleEndian.Uint16(rb[6:])
	rlen := binary.LittleEndian.Uint32(rb[8:])
	rseq := binary.LittleEndian.Uint32(rb[12:])
	if flags != 0 {
		return fmt.Errorf("reply flags %#x", flags)
	}
	if uint64(len(rb)) != uint64(xfer.HeaderSize)+uint64(rlen) || rlen > xfer.MaxBody {
		return fmt.Errorf("reply of %d bytes for a body of %d", len(rb), rlen)
	}
	if rtyp != (typ|xfer.TypeReply) || rseq != seq {
		return fmt.Errorf("reply type %#x seq %d, expected %#x seq %d", rtyp, rseq, typ|xfer.TypeReply, seq)
	}
	if out != nil {
		if err := json.Unmarshal(rb[xfer.HeaderSize:], out); err != nil {
			return fmt.Errorf("the reply body is not the expected JSON: %w", err)
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

// putFile sends one file: PUT_BEGIN, then the bytes in DataChunk transfers,
// then it reads PUT_END. The unit answers PUT_BEGIN before any byte is sent; a
// refusal sends nothing.
func (d *Driver) putFile(f file) error {
	d.seq++
	seq := d.seq
	fr, err := xfer.EncodeFrame(xfer.TypePutBegin, seq, mustJSON(xfer.PutBegin{Name: f.name, Size: f.size}))
	if err != nil {
		return err
	}
	if err := d.t.Out(fr); err != nil {
		return fmt.Errorf("%s: sending PUT_BEGIN: %w", f.name, err)
	}
	var a putAnswer
	if err := d.readReply(xfer.TypePutBegin, seq, &a); err != nil {
		return err
	}
	if !a.Accept {
		return fmt.Errorf("the unit refused %s: %s", f.name, a.Reason)
	}
	fh, err := os.Open(f.path)
	if err != nil {
		return err
	}
	defer fh.Close()
	t0 := time.Now()
	buf := make([]byte, DataChunk)
	var sent int64
	for sent < f.size {
		n := int64(DataChunk)
		if left := f.size - sent; left < n {
			n = left
		}
		if _, err := io.ReadFull(fh, buf[:n]); err != nil {
			return fmt.Errorf("%s: reading at %d of %d bytes: %w", f.name, sent, f.size, err)
		}
		if err := d.t.Out(buf[:n]); err != nil {
			return fmt.Errorf("%s: sending at %d of %d bytes: %w", f.name, sent, f.size, err)
		}
		sent += n
	}
	var e putEnd
	if err := d.readReply(xfer.TypePutEnd, seq, &e); err != nil {
		return err
	}
	if !e.OK {
		return fmt.Errorf("the unit did not take %s: %s", f.name, e.Reason)
	}
	secs := time.Since(t0).Seconds()
	if secs <= 0 {
		secs = 0.001
	}
	d.log("%s sent (%d bytes, %.1f MB/s), unit sha256 %s", f.name, f.size, float64(f.size)/1e6/secs, e.SHA256)
	return nil
}

// Commit tells the unit that the set is complete. auto asks it to continue by
// itself where the slot allows it (the default is the hold).
func (d *Driver) Commit(auto bool) error {
	var c commitAnswer
	if err := d.frame(xfer.TypeCommit, xfer.Commit{Auto: auto}, &c); err != nil {
		return err
	}
	if !c.OK {
		return fmt.Errorf("the unit did not take the set: %s", c.Reason)
	}
	return nil
}

// Send runs the whole session: HELLO, the cheap pre-checks against the unit's
// pins, the first file, the two files it names, and COMMIT. dir is the folder
// that holds the set (uc-pxx-mainline for a payload, uc-pxx-finish for a
// finish set). It returns the set's first-file sha256.
func (d *Driver) Send(kind Kind, dir string, auto bool) (string, error) {
	h, err := d.Hello()
	if err != nil {
		return "", err
	}
	d.log("unit: proto %d, usb %s, mode %q, state %q, %d pin(s), room %d bytes", h.Proto, h.USB, h.Mode, h.State, len(h.Pins), h.Room)
	wantMode := map[Kind]string{Payload: "payload", Finish: "finish"}[kind]
	if h.Mode != wantMode {
		return "", fmt.Errorf("the unit is in mode %q, not %q: it does not want this set now (use the matching section)", h.Mode, wantMode)
	}
	if len(h.Pins) == 0 {
		return "", errors.New("the unit pins nothing: it refuses every set now (not a step-2 keeper, or already done)")
	}
	first, files, err := checkSet(kind, dir, h.Pins)
	if err != nil {
		return "", err
	}
	total := int64(0)
	for _, f := range files {
		total += f.size
	}
	if h.Room > 0 && total > h.Room {
		return "", fmt.Errorf("the set is %d bytes, the unit has room for %d", total, h.Room)
	}
	for _, f := range files {
		if err := d.putFile(f); err != nil {
			return first, err
		}
	}
	if err := d.Commit(auto); err != nil {
		return first, err
	}
	d.log("the unit took the %s (first-file sha256 %s)", wantMode, first)
	return first, nil
}

// checkSet does the page's cheap pre-check of a set on the host, before any
// byte is sent: the first file (manifest.json or finish.json) parses, its
// sha256 is one of the unit's pins, and the two files it names have those
// sizes and sha256. The unit checks all of it again.
func checkSet(kind Kind, dir string, pins []string) (first string, files []file, err error) {
	switch kind {
	case Payload:
		mPath := filepath.Join(dir, xfer.ManifestName)
		mb, err := readSmall(mPath, mainline.MaxManifest)
		if err != nil {
			return "", nil, err
		}
		first = shaHex(mb)
		if !pinned(first, pins) {
			return "", nil, fmt.Errorf("%s sha256 %s is not a payload the unit pins", xfer.ManifestName, first)
		}
		m, err := mainline.ParseManifest(mb)
		if err != nil {
			return "", nil, fmt.Errorf("%s: %w", xfer.ManifestName, err)
		}
		fl := []struct {
			name string
			size int64
			sha  string
		}{
			{xfer.TemplateName, m.BootTemplate.Size, m.BootTemplate.SHA256},
			{xfer.RootfsName, m.Rootfs.Size, m.Rootfs.SHA256},
		}
		files = append(files, file{name: xfer.ManifestName, path: mPath, size: int64(len(mb))})
		for _, x := range fl {
			p := filepath.Join(dir, x.name)
			if err := checkFile(p, x.size, x.sha); err != nil {
				return "", nil, err
			}
			files = append(files, file{name: x.name, path: p, size: x.size})
		}
		return first, files, nil
	case Finish:
		lPath := filepath.Join(dir, handover.FinishListName)
		lb, err := readSmall(lPath, handover.MaxFinishList)
		if err != nil {
			return "", nil, err
		}
		first = shaHex(lb)
		if !pinned(first, pins) {
			return "", nil, fmt.Errorf("%s sha256 %s is not a finish set the unit pins", handover.FinishListName, first)
		}
		l, err := handover.ParseFinishList(lb)
		if err != nil {
			return "", nil, fmt.Errorf("%s: %w", handover.FinishListName, err)
		}
		files = append(files, file{name: handover.FinishListName, path: lPath, size: int64(len(lb))})
		for _, x := range l.Files {
			p := filepath.Join(dir, x.Name)
			if err := checkFile(p, x.Size, x.SHA256); err != nil {
				return "", nil, err
			}
			files = append(files, file{name: x.Name, path: p, size: x.Size})
		}
		return first, files, nil
	}
	return "", nil, errors.New("unknown kind")
}

func checkFile(path string, size int64, sha string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if fi.Size() != size {
		return fmt.Errorf("%s is %d bytes, the manifest/list says %d (an incomplete download?)", filepath.Base(path), fi.Size(), size)
	}
	h, err := shaFile(path)
	if err != nil {
		return err
	}
	if h != sha {
		return fmt.Errorf("%s has sha256 %s, the manifest/list says %s (a damaged download?)", filepath.Base(path), h, sha)
	}
	return nil
}

func readSmall(path string, max int) ([]byte, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	b, err := io.ReadAll(io.LimitReader(fh, int64(max)+1))
	if err != nil {
		return nil, err
	}
	if len(b) == 0 || len(b) > max {
		return nil, fmt.Errorf("%s is %d bytes (1 to %d)", filepath.Base(path), len(b), max)
	}
	return b, nil
}

func shaFile(path string) (string, error) {
	fh, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer fh.Close()
	h := sha256.New()
	if _, err := io.Copy(h, fh); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
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
