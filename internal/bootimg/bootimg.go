// Package bootimg decodes the header of an Android boot image (header
// versions 0 to 2), as U-Boot reads it from a boot_X partition. The installer
// uses it to tell what a slot holds: a uc-pxx image has its build token in
// the name field, a stock image has an empty name and an Android OS version.
package bootimg

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// HeaderSize is enough bytes for every field that this package reads.
const HeaderSize = 1632

// Header is the decoded part of the header.
type Header struct {
	KernelSize, RamdiskSize, SecondSize uint32
	PageSize                            uint32
	Version                             uint32
	OSVersion                           uint32
	Name                                string
	Cmdline                             string
}

// ErrNoMagic means that the data does not start with "ANDROID!".
var ErrNoMagic = errors.New("no ANDROID! magic")

func cstr(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

// Parse decodes a header from its first HeaderSize bytes.
func Parse(b []byte) (Header, error) {
	var h Header
	if len(b) < HeaderSize {
		return h, errors.New("short header")
	}
	if string(b[:8]) != "ANDROID!" {
		return h, ErrNoMagic
	}
	le := binary.LittleEndian
	h.KernelSize = le.Uint32(b[8:])
	h.RamdiskSize = le.Uint32(b[16:])
	h.SecondSize = le.Uint32(b[24:])
	h.PageSize = le.Uint32(b[36:])
	h.Version = le.Uint32(b[40:])
	h.OSVersion = le.Uint32(b[44:])
	h.Name = cstr(b[48:64])
	h.Cmdline = cstr(b[64:576]) + cstr(b[608:1632])
	if h.Version > 2 {
		return h, fmt.Errorf("header version %d not supported", h.Version)
	}
	return h, nil
}

// Size is the image size that the header describes (pages of the parts).
func (h Header) Size() int64 {
	if h.PageSize == 0 {
		return 0
	}
	p := int64(h.PageSize)
	pages := func(n uint32) int64 { return (int64(n) + p - 1) / p }
	return p * (1 + pages(h.KernelSize) + pages(h.RamdiskSize) + pages(h.SecondSize))
}

// AndroidVersion decodes os_version, for example "9.0.0, patch 2019-06".
// It is empty when the field is 0.
func (h Header) AndroidVersion() string {
	if h.OSVersion == 0 {
		return ""
	}
	v := h.OSVersion >> 11
	lvl := h.OSVersion & 0x7ff
	s := fmt.Sprintf("%d.%d.%d", v>>14, (v>>7)&0x7f, v&0x7f)
	if lvl != 0 {
		s += fmt.Sprintf(", patch %d-%02d", 2000+lvl>>4, lvl&0xf)
	}
	return s
}

// UcPxxToken returns the uc-pxx build token ("ucpxx-..."), or "".
func (h Header) UcPxxToken() string {
	if strings.HasPrefix(h.Name, "ucpxx-") {
		return h.Name
	}
	return ""
}
