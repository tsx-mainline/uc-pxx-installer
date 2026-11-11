package xfer

import (
	"bytes"
	"encoding/binary"
	"unicode/utf16"
)

// FunctionFS blob values (include/uapi/linux/usb/functionfs.h).
const (
	ffsDescriptorsMagicV2 = 3
	ffsStringsMagic       = 2
	ffsHasFS              = 1
	ffsHasHS              = 2
	ffsHasSS              = 4
	ffsHasMSOS            = 8
)

// Endpoint addresses of the interface: ep1 OUT (host to unit), ep2 IN.
const (
	EPOut = 0x01
	EPIn  = 0x82
)

// InterfaceString is string 1 of the function (iInterface).
const InterfaceString = "UC-PXX step-2 transfer"

func le16(b *bytes.Buffer, v uint16) { _ = binary.Write(b, binary.LittleEndian, v) }
func le32(b *bytes.Buffer, v uint32) { _ = binary.Write(b, binary.LittleEndian, v) }

func utf16z(s string) []byte {
	var b bytes.Buffer
	for _, r := range utf16.Encode([]rune(s)) {
		le16(&b, r)
	}
	le16(&b, 0)
	return b.Bytes()
}

// osDesc is one FunctionFS MS OS descriptor: the 11-byte header (interface,
// dwLength, bcdVersion, wIndex, bCount/wCount) and its body.
func osDesc(b *bytes.Buffer, iface byte, wIndex, count uint16, body []byte) {
	b.WriteByte(iface)
	le32(b, uint32(11+len(body)))
	le16(b, 0x0100)
	le16(b, wIndex)
	le16(b, count) // wIndex 4: bCount = count, Reserved = 0; wIndex 5: wCount
	b.Write(body)
}

// Descriptors is the FunctionFS v2 descriptor blob of the function: one
// vendor-class interface (IfaceClass/Subclass/Protocol) with one bulk OUT and
// one bulk IN endpoint, at full, high and super speed, and the MS OS 1.0
// descriptors: the extended compat id WINUSB and the extended property
// DeviceInterfaceGUIDs, so that Windows binds WinUSB without Zadig
// (STEP2-USB-TRANSPORT §3.2; to be measured on a Windows host, A-2).
func Descriptors() []byte {
	iface := []byte{9, 4, 0, 0, 2, IfaceClass, IfaceSubclass, IfaceProtocol, 1}
	ep := func(b *bytes.Buffer, addr byte, mps uint16) {
		b.Write([]byte{7, 5, addr, 2})
		le16(b, mps)
		b.WriteByte(0)
	}
	ssComp := func(b *bytes.Buffer) { b.Write([]byte{6, 0x30, 0, 0, 0, 0}) }
	var fs, hs, ss bytes.Buffer
	fs.Write(iface)
	ep(&fs, EPOut, 64)
	ep(&fs, EPIn, 64)
	hs.Write(iface)
	ep(&hs, EPOut, 512)
	ep(&hs, EPIn, 512)
	ss.Write(iface)
	ep(&ss, EPOut, 1024)
	ssComp(&ss)
	ep(&ss, EPIn, 1024)
	ssComp(&ss)

	var msos, compat, prop bytes.Buffer
	compat.WriteByte(0) // bFirstInterfaceNumber
	compat.WriteByte(1) // Reserved1
	compat.Write([]byte("WINUSB\x00\x00"))
	compat.Write(make([]byte, 8)) // SubCompatibleID
	compat.Write(make([]byte, 6)) // Reserved2
	osDesc(&msos, 0, 4, 1, compat.Bytes())
	name := utf16z("DeviceInterfaceGUIDs")
	data := append(utf16z(InterfaceGUID), 0, 0) // REG_MULTI_SZ: a second NUL ends the list
	le32(&prop, uint32(14+len(name)+len(data)))
	le32(&prop, 7) // REG_MULTI_SZ
	le16(&prop, uint16(len(name)))
	prop.Write(name)
	le32(&prop, uint32(len(data)))
	prop.Write(data)
	osDesc(&msos, 0, 5, 1, prop.Bytes())

	var out bytes.Buffer
	le32(&out, ffsDescriptorsMagicV2)
	le32(&out, 0) // the length, set below
	le32(&out, ffsHasFS|ffsHasHS|ffsHasSS|ffsHasMSOS)
	le32(&out, 3) // fs_count
	le32(&out, 3) // hs_count
	le32(&out, 5) // ss_count (with the two endpoint companions)
	le32(&out, 2) // os_count
	out.Write(fs.Bytes())
	out.Write(hs.Bytes())
	out.Write(ss.Bytes())
	out.Write(msos.Bytes())
	b := out.Bytes()
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)))
	return b
}

// Strings is the FunctionFS strings blob: string 1 in US English.
func Strings() []byte {
	s := []byte(InterfaceString + "\x00")
	var b bytes.Buffer
	le32(&b, ffsStringsMagic)
	le32(&b, uint32(16+2+len(s)))
	le32(&b, 1) // str_count
	le32(&b, 1) // lang_count
	le16(&b, 0x0409)
	b.Write(s)
	return b.Bytes()
}
