// Package xfer is the receiver of the step-2 USB transport T-A
// (STEP2-USB-TRANSPORT-2026-11-11.md §3; owner answers of 2026-11-11: T-A,
// RAM staging, dev USB id 1d6b:0105). The running mainline slot K presents one
// vendor-class USB interface (FunctionFS); the installer page talks to it with
// WebUSB and sends the three files of a normal mainline payload into a RAM
// folder. The receiver accepts the manifest first, and only when its sha256 is
// one of the manifest sha256s that the step-2 gate pins (uc-pxx-step2 pins);
// then only the two files that the manifest names, with the manifest's exact
// sizes, and it computes their sha256 while the bytes arrive. A complete,
// checked folder is handed to the root step-2 service as one ready folder
// (CheckReady; the seal itself is the service's).
//
// The package opens no block device and links no eMMC code: it writes only
// into its own folder of the tmpfs stage (ci/check-no-writer.py --xfer).
//
// Protocol (one USB transfer per frame; the fastboot shape: command, size,
// data, answer):
//
//	frame  = "PXX1", u16 type, u16 flags (0), u32 length, u32 seq, length bytes of JSON
//	         (little endian; length <= MaxBody; a frame is never a multiple of 512 bytes:
//	         the JSON is padded with spaces, so its last packet is short)
//	reply  = the same header with type|0x80 and the seq of the request
//
//	HELLO      -> state, the pins, mem_available, the service's info
//	PUT_BEGIN  {name, size} -> {accept, reason}: before any byte of the file
//	(data)     exactly size raw bytes in transfers of DataChunk bytes (the last one shorter)
//	           -> PUT_END reply {ok, sha256, reason}
//	COMMIT     {auto} -> {ok}: the three files are there and checked; the folder is ready
//	STATUS     -> the state and the service's progress lines
package xfer

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// The USB identity of the step-2 transfer device: ONE place (owner Q-T4: the
// dev id 1d6b:0105 now, an own pid.codes PID for the release later). The page
// has the same values in config.js STEP2_USB.
const (
	USBVendorID  = 0x1d6b // Linux Foundation
	USBProductID = 0x0105 // "FunctionFS Gadget" (dev id; Q-T4)
	USBBCDDevice = 0x0201 // bump it when the descriptors change (Windows caches the MS OS answer per VID+PID+bcdDevice)
	USBSerial    = "UC-PXX-STEP2"
	USBProduct   = "UC-P10 mainline step 2"
	USBMaker     = "uc-pxx"
	// the interface class triple (vendor class)
	IfaceClass    = 0xff
	IfaceSubclass = 0x50
	IfaceProtocol = 0x01
	// MS OS 1.0 vendor code and the interface GUID Windows registers for WinUSB
	MSOSVendorCode = 0xcd
	InterfaceGUID  = "{6c1f7a0e-5d2b-4e8a-9f31-0b7c2e4d8a15}"
	WebUSBCode     = 0x02
	LandingPage    = "https://tsx-mainline.github.io/uc-pxx-installer/"
)

// Frame types. A reply has the request's type | TypeReply.
const (
	TypeHello    = 1
	TypePutBegin = 2
	TypePutEnd   = 4 // the reply after the data of a PUT
	TypeCommit   = 5
	TypeStatus   = 6
	TypeReply    = 0x80
)

// Limits.
const (
	Magic      = "PXX1"
	HeaderSize = 16
	MaxBody    = 64 << 10
	// FrameRead is the size of one read of a frame: more than the largest
	// frame, so a frame (one USB transfer) is always read whole. FunctionFS
	// fails a read shorter than the host's transfer with EOVERFLOW on a UDC
	// without the aligned-size quirk (prototype finding 1, §9).
	FrameRead = HeaderSize + MaxBody + 512
	// DataChunk is the transfer size of the file data: the host sends DataChunk
	// bytes per transfer (the last one shorter) and the receiver reads exactly
	// min(DataChunk, bytes left).
	DataChunk = 1 << 20
	// MaxManifest is the largest manifest.json (mainline.MaxManifest).
	MaxManifest = 64 << 10
	// ProtoVersion is the "proto" of HELLO.
	ProtoVersion = 1
)

// Header is the 16-byte frame header.
type Header struct {
	Type  uint16
	Flags uint16
	Len   uint32
	Seq   uint32
}

// ErrFrame is a frame that breaks the protocol: the session ends.
var ErrFrame = errors.New("bad frame")

// ParseFrame parses one whole transfer as a frame: the header, then exactly
// Len bytes of body, nothing after it.
func ParseFrame(b []byte) (Header, []byte, error) {
	var h Header
	if len(b) < HeaderSize {
		return h, nil, fmt.Errorf("%w: %d bytes, shorter than the header", ErrFrame, len(b))
	}
	if string(b[:4]) != Magic {
		return h, nil, fmt.Errorf("%w: no magic", ErrFrame)
	}
	h.Type = binary.LittleEndian.Uint16(b[4:])
	h.Flags = binary.LittleEndian.Uint16(b[6:])
	h.Len = binary.LittleEndian.Uint32(b[8:])
	h.Seq = binary.LittleEndian.Uint32(b[12:])
	switch {
	case h.Flags != 0:
		return h, nil, fmt.Errorf("%w: flags %#x", ErrFrame, h.Flags)
	case h.Len > MaxBody:
		return h, nil, fmt.Errorf("%w: body of %d bytes (at most %d)", ErrFrame, h.Len, MaxBody)
	case uint64(len(b)) != HeaderSize+uint64(h.Len):
		return h, nil, fmt.Errorf("%w: transfer of %d bytes for a body of %d", ErrFrame, len(b), h.Len)
	case h.Type&TypeReply != 0:
		return h, nil, fmt.Errorf("%w: a reply type %#x from the host", ErrFrame, h.Type)
	}
	return h, b[HeaderSize:], nil
}

// EncodeFrame makes a frame of typ, seq and the JSON body j. It pads j with
// spaces until the frame is not a multiple of 512 bytes (its last packet is
// then short, which ends the transfer on both sides).
func EncodeFrame(typ uint16, seq uint32, j []byte) ([]byte, error) {
	n := len(j)
	for (HeaderSize+n)%512 == 0 {
		n++
	}
	if n > MaxBody {
		return nil, fmt.Errorf("frame body of %d bytes (at most %d)", n, MaxBody)
	}
	b := make([]byte, HeaderSize+n)
	copy(b, Magic)
	binary.LittleEndian.PutUint16(b[4:], typ)
	binary.LittleEndian.PutUint32(b[8:], uint32(n))
	binary.LittleEndian.PutUint32(b[12:], seq)
	copy(b[HeaderSize:], j)
	for i := HeaderSize + len(j); i < len(b); i++ {
		b[i] = ' '
	}
	return b, nil
}

// File names of the payload (mainline.ManifestFile, TemplateFile, RootfsFile).
const (
	ManifestName = "manifest.json"
	TemplateName = "boot-template.img"
	RootfsName   = "rootfs.ext4"
)

// PutBegin is the body of PUT_BEGIN.
type PutBegin struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// Commit is the body of COMMIT.
type Commit struct {
	// Auto: the person chose "continue by itself" on the page (the panel's
	// 30 s countdown that Back cancels, AUTO-STEP2). The default is the hold.
	Auto bool `json:"auto"`
}
