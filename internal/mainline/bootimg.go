package mainline

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"uc-pxx-installer/internal/bootimg"
)

// The boot image of uc-pxx-linux kernel/mkimage.sh (docs/boot.md "The boot
// image"): an Android boot image of header version 0, page size 2048, made by
// the AOSP mkbootimg with its default load addresses. kernel is the shim (a
// stub, the device tree at 64 KiB, the Linux Image at 2 MiB), ramdisk a dummy
// of 20 bytes, second the Rockchip resource, the name field the build token.
//
// The template of the payload is the same image with an empty second
// (mkimage.sh run with an empty resource file): mkbootimg then writes
// second_size 0 and second_addr 0. Assemble adds the resource as mkbootimg
// would have: second_size, second_addr = base + 0x00f00000, the resource
// padded with zeros to a whole page, and the id field again (sha1 over each
// part followed by its size as a little-endian u32, zero-padded to 32 bytes).
const (
	PageSize = 2048
	// MaxBootImage is the largest boot image (the boot partition, 64 MiB).
	MaxBootImage = 64 << 20
	// MaxResource caps the resource (as uc-pxx-update-boot does).
	MaxResource = 16 << 20
	// ShimDTB is where the shim keeps its device tree in the kernel part.
	ShimDTB = 0x10000

	// The default offsets of mkbootimg (from --base).
	offKernel  = 0x00008000
	offRamdisk = 0x01000000
	offSecond  = 0x00f00000
	offTags    = 0x00000100

	// The v0 header fields (bytes).
	hdrKernelSize  = 8
	hdrKernelAddr  = 12
	hdrRamdiskSize = 16
	hdrRamdiskAddr = 20
	hdrSecondSize  = 24
	hdrSecondAddr  = 28
	hdrTagsAddr    = 32
	hdrPageSize    = 36
	hdrVersion     = 40
	hdrOSVersion   = 44
	hdrName        = 48
	hdrID          = 576
	hdrEnd         = 1632 // header version 0 ends here (extra cmdline 608..1631)
)

// TemplateInfo is what ParseTemplate found.
type TemplateInfo struct {
	KernelSize  int64  `json:"kernel_size"`
	RamdiskSize int64  `json:"ramdisk_size"`
	Base        uint32 `json:"base"`
	Token       string `json:"token"`
	Size        int64  `json:"size"`
}

func pages(n int64) int64 { return (n + PageSize - 1) / PageSize }

func allZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

// layout is a decoded v0 header of a mkbootimg image with page size 2048.
type layout struct {
	kernel, ramdisk, second int64 // sizes
	secondAddr              uint32
	base                    uint32
	name                    string
	id                      []byte
}

func (l layout) kernelOff() int64  { return PageSize }
func (l layout) ramdiskOff() int64 { return PageSize * (1 + pages(l.kernel)) }
func (l layout) secondOff() int64  { return l.ramdiskOff() + PageSize*pages(l.ramdisk) }
func (l layout) end() int64        { return l.secondOff() + PageSize*pages(l.second) }

// parseLayout checks the header of an image of kernel/mkimage.sh: magic,
// header version 0, page size 2048, os_version 0, the default mkbootimg load
// addresses, a 20-byte-class ramdisk (not empty), a valid token in the name
// field (the rest of it zero), and the image size the header gives.
func parseLayout(b []byte) (layout, error) {
	var l layout
	if len(b) < PageSize {
		return l, fmt.Errorf("shorter than one page")
	}
	if string(b[:8]) != "ANDROID!" {
		return l, fmt.Errorf("no ANDROID! magic")
	}
	le := binary.LittleEndian
	switch {
	case le.Uint32(b[hdrVersion:]) != 0:
		return l, fmt.Errorf("header version %d, not 0", le.Uint32(b[hdrVersion:]))
	case le.Uint32(b[hdrPageSize:]) != PageSize:
		return l, fmt.Errorf("page size %d, not %d", le.Uint32(b[hdrPageSize:]), PageSize)
	case le.Uint32(b[hdrOSVersion:]) != 0:
		return l, fmt.Errorf("os_version is set (mkimage.sh sets none)")
	}
	l.kernel = int64(le.Uint32(b[hdrKernelSize:]))
	l.ramdisk = int64(le.Uint32(b[hdrRamdiskSize:]))
	l.second = int64(le.Uint32(b[hdrSecondSize:]))
	l.secondAddr = le.Uint32(b[hdrSecondAddr:])
	ka := le.Uint32(b[hdrKernelAddr:])
	if ka < offKernel {
		return l, fmt.Errorf("kernel_addr 0x%x is below the default kernel offset", ka)
	}
	l.base = ka - offKernel
	if l.kernel == 0 || l.ramdisk == 0 {
		return l, fmt.Errorf("kernel or ramdisk is empty")
	}
	if le.Uint32(b[hdrRamdiskAddr:]) != l.base+offRamdisk || le.Uint32(b[hdrTagsAddr:]) != l.base+offTags {
		return l, fmt.Errorf("not the default load addresses of mkbootimg")
	}
	name := b[hdrName : hdrName+16]
	n := bytes.IndexByte(name, 0)
	if n < 0 || !allZero(name[n:]) {
		return l, fmt.Errorf("the name field is not one NUL-terminated token")
	}
	l.name = string(name[:n])
	if !ValidToken(l.name) {
		return l, fmt.Errorf("the name field %q is not a build token", l.name)
	}
	l.id = b[hdrID : hdrID+32]
	return l, nil
}

// imageID is the id field that mkbootimg writes for header version 0: sha1
// over kernel, its size, ramdisk, its size, second, its size (u32 little
// endian each), zero-padded to 32 bytes. An absent second hashes as
// size 0, the same as an empty one.
func imageID(kernel, ramdisk, second []byte) []byte {
	h := sha1.New()
	var n [4]byte
	for _, part := range [][]byte{kernel, ramdisk, second} {
		h.Write(part)
		binary.LittleEndian.PutUint32(n[:], uint32(len(part)))
		h.Write(n[:])
	}
	id := make([]byte, 32)
	copy(id, h.Sum(nil))
	return id
}

// checkBody checks an image against its layout: the length, the zero
// padding of the header page and of every part, and the id field.
func checkBody(b []byte, l layout) error {
	if int64(len(b)) != l.end() {
		return fmt.Errorf("%d bytes, its header gives %d", len(b), l.end())
	}
	pad := func(off, n int64) bool { return allZero(b[off+n : off+PageSize*pages(n)]) }
	if !allZero(b[hdrEnd:PageSize]) {
		return fmt.Errorf("the header page is not zero after the header")
	}
	if !pad(l.kernelOff(), l.kernel) || !pad(l.ramdiskOff(), l.ramdisk) || !pad(l.secondOff(), l.second) {
		return fmt.Errorf("the padding of a part is not zero")
	}
	k := b[l.kernelOff() : l.kernelOff()+l.kernel]
	r := b[l.ramdiskOff() : l.ramdiskOff()+l.ramdisk]
	s := b[l.secondOff() : l.secondOff()+l.second]
	if !bytes.Equal(imageID(k, r, s), l.id) {
		return fmt.Errorf("the id field does not match the content")
	}
	return nil
}

// shimToken is the token of the one word uc-pxx.id=TOKEN in /chosen/bootargs
// of the device tree that the shim keeps at 64 KiB in the kernel part.
func shimToken(b []byte, l layout) (string, error) {
	if l.kernel < ShimDTB+fdtHeader {
		return "", fmt.Errorf("the kernel part (%d bytes) is too small for the shim device tree at 64 KiB", l.kernel)
	}
	dtb := b[l.kernelOff()+ShimDTB : l.kernelOff()+l.kernel]
	args, err := fdtChosenBootargs(dtb)
	if err != nil {
		return "", fmt.Errorf("the shim device tree: %v", err)
	}
	var ids []string
	for _, w := range strings.Fields(args) {
		if strings.HasPrefix(w, "uc-pxx.id=") {
			ids = append(ids, strings.TrimPrefix(w, "uc-pxx.id="))
		}
	}
	if len(ids) != 1 {
		return "", fmt.Errorf("/chosen/bootargs of the shim device tree has %d uc-pxx.id= words, not 1", len(ids))
	}
	return ids[0], nil
}

// ParseTemplate checks a boot template (kernel/mkimage.sh with an empty
// resource): the header of parseLayout, second_size 0 and second_addr 0, the
// size, the zero padding, the id field, and the token of the header name
// equal to the uc-pxx.id= word of the shim device tree.
func ParseTemplate(b []byte) (*TemplateInfo, error) {
	l, err := parseLayout(b)
	if err != nil {
		return nil, fmt.Errorf("boot template: %v", err)
	}
	if l.second != 0 || l.secondAddr != 0 {
		return nil, fmt.Errorf("boot template: it has a second part already (%d bytes at 0x%x)", l.second, l.secondAddr)
	}
	if len(b) > MaxBootImage {
		return nil, fmt.Errorf("boot template: %d bytes, more than %d", len(b), MaxBootImage)
	}
	if err := checkBody(b, l); err != nil {
		return nil, fmt.Errorf("boot template: %v", err)
	}
	tok, err := shimToken(b, l)
	if err != nil {
		return nil, fmt.Errorf("boot template: %v", err)
	}
	if tok != l.name {
		return nil, fmt.Errorf("boot template: the header name %q is not the token %q of the kernel command line", l.name, tok)
	}
	return &TemplateInfo{KernelSize: l.kernel, RamdiskSize: l.ramdisk, Base: l.base, Token: l.name, Size: int64(len(b))}, nil
}

// Assemble is the boot image of slot A: the template with res as its second
// part, byte for byte what kernel/mkimage.sh --resource RES makes from the
// same inputs. It checks the template first and the result again from its
// own bytes (CheckAssembled).
func Assemble(template, res []byte) ([]byte, error) {
	if _, err := ParseTemplate(template); err != nil {
		return nil, err
	}
	if len(res) == 0 || len(res) > MaxResource {
		return nil, fmt.Errorf("resource of %d bytes (1 to %d)", len(res), MaxResource)
	}
	l, _ := parseLayout(template)
	out := make([]byte, 0, int64(len(template))+PageSize*pages(int64(len(res))))
	out = append(out, template...)
	le := binary.LittleEndian
	le.PutUint32(out[hdrSecondSize:], uint32(len(res)))
	le.PutUint32(out[hdrSecondAddr:], l.base+offSecond)
	out = append(out, res...)
	out = append(out, make([]byte, PageSize*pages(int64(len(res)))-int64(len(res)))...)
	k := out[l.kernelOff() : l.kernelOff()+l.kernel]
	r := out[l.ramdiskOff() : l.ramdiskOff()+l.ramdisk]
	copy(out[hdrID:hdrID+32], imageID(k, r, res))
	if len(out) > MaxBootImage {
		return nil, fmt.Errorf("the assembled image has %d bytes, more than %d", len(out), MaxBootImage)
	}
	if _, err := CheckAssembled(out, l.name, sha256Hex(res)); err != nil {
		return nil, fmt.Errorf("internal error: %v", err)
	}
	return out, nil
}

// CheckAssembled checks a boot image for slot A from its own bytes: the
// header of parseLayout, a second part at base + 0x00f00000 whose sha256 is
// resSHA, the size, the padding, the id field, and the token in the header
// name and on the kernel command line. It returns the image's sha256.
func CheckAssembled(img []byte, token, resSHA string) (string, error) {
	l, err := parseLayout(img)
	if err != nil {
		return "", fmt.Errorf("boot image: %v", err)
	}
	switch {
	case len(img) > MaxBootImage:
		return "", fmt.Errorf("boot image: %d bytes, more than %d", len(img), MaxBootImage)
	case l.second == 0 || l.second > MaxResource || l.secondAddr != l.base+offSecond:
		return "", fmt.Errorf("boot image: second part of %d bytes at 0x%x", l.second, l.secondAddr)
	}
	if err := checkBody(img, l); err != nil {
		return "", fmt.Errorf("boot image: %v", err)
	}
	tok, err := shimToken(img, l)
	if err != nil {
		return "", fmt.Errorf("boot image: %v", err)
	}
	if tok != l.name || l.name != token {
		return "", fmt.Errorf("boot image: header name %q, kernel command line token %q, want %q", l.name, tok, token)
	}
	if got := sha256Hex(img[l.secondOff() : l.secondOff()+l.second]); got != resSHA {
		return "", fmt.Errorf("boot image: the resource has sha256 %s, want %s", got, resSHA)
	}
	return sha256Hex(img), nil
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// ReadResource reads the second part ("resource") of the boot image in a
// boot partition (the keeper's boot_b: a stock Android boot image, header
// version 0 to 2) at partStart of r, with partBytes the partition size. The
// caller checks its sha256.
func ReadResource(r io.ReaderAt, partStart, partBytes int64) ([]byte, error) {
	head := make([]byte, bootimg.HeaderSize)
	if partBytes < int64(len(head)) {
		return nil, fmt.Errorf("the boot partition is smaller than a header")
	}
	if _, err := r.ReadAt(head, partStart); err != nil {
		return nil, fmt.Errorf("read the boot header: %v", err)
	}
	h, err := bootimg.Parse(head)
	if err != nil {
		return nil, fmt.Errorf("the boot image: %v", err)
	}
	if h.PageSize < 512 || h.PageSize > 16384 || h.PageSize&(h.PageSize-1) != 0 {
		return nil, fmt.Errorf("the boot image has page size %d", h.PageSize)
	}
	pg := int64(h.PageSize)
	pg2 := func(x uint32) int64 { return (int64(x) + pg - 1) / pg }
	off := pg * (1 + pg2(h.KernelSize) + pg2(h.RamdiskSize))
	n := int64(h.SecondSize)
	if n == 0 || n > MaxResource {
		return nil, fmt.Errorf("the boot image has a second part of %d bytes (1 to %d)", n, MaxResource)
	}
	if off+n > partBytes {
		return nil, fmt.Errorf("the second part ends after the partition")
	}
	res := make([]byte, n)
	if _, err := r.ReadAt(res, partStart+off); err != nil {
		return nil, fmt.Errorf("read the resource: %v", err)
	}
	return res, nil
}

// --- a small flat device tree reader (only what shimToken needs) ---------

const fdtHeader = 40

// fdtChosenBootargs returns /chosen/bootargs of a flattened device tree
// (without its trailing NUL). Every offset and length is checked; no input
// makes it panic.
func fdtChosenBootargs(dtb []byte) (string, error) {
	be := binary.BigEndian
	if len(dtb) < fdtHeader || be.Uint32(dtb) != 0xd00dfeed {
		return "", fmt.Errorf("no device tree magic")
	}
	total := int64(be.Uint32(dtb[4:]))
	offStruct, offStrings := int64(be.Uint32(dtb[8:])), int64(be.Uint32(dtb[12:]))
	sizeStrings, sizeStruct := int64(be.Uint32(dtb[32:])), int64(be.Uint32(dtb[36:]))
	if total > int64(len(dtb)) || total < fdtHeader || offStruct+sizeStruct > total || offStrings+sizeStrings > total ||
		offStruct < fdtHeader || offStrings < fdtHeader || offStruct%4 != 0 {
		return "", fmt.Errorf("the device tree header is not plausible")
	}
	st := dtb[offStruct : offStruct+sizeStruct]
	strs := dtb[offStrings : offStrings+sizeStrings]
	var stack []string
	found, args := 0, ""
	for i := int64(0); i+4 <= int64(len(st)); {
		tok := be.Uint32(st[i:])
		i += 4
		switch tok {
		case 1: // FDT_BEGIN_NODE
			j := bytes.IndexByte(st[i:], 0)
			if j < 0 {
				return "", fmt.Errorf("a node name has no end")
			}
			if len(stack) >= 16 {
				return "", fmt.Errorf("the device tree is too deep")
			}
			stack = append(stack, string(st[i:i+int64(j)]))
			i = (i + int64(j) + 1 + 3) &^ 3
		case 2: // FDT_END_NODE
			if len(stack) == 0 {
				return "", fmt.Errorf("an end of a node that was not begun")
			}
			stack = stack[:len(stack)-1]
		case 3: // FDT_PROP
			if i+8 > int64(len(st)) {
				return "", fmt.Errorf("a property header ends after the structure")
			}
			ln, nameOff := int64(be.Uint32(st[i:])), int64(be.Uint32(st[i+4:]))
			i += 8
			if ln > int64(len(st))-i || nameOff >= int64(len(strs)) {
				return "", fmt.Errorf("a property ends after the structure")
			}
			k := bytes.IndexByte(strs[nameOff:], 0)
			if k < 0 {
				return "", fmt.Errorf("a property name has no end")
			}
			if len(stack) == 2 && stack[0] == "" && stack[1] == "chosen" && string(strs[nameOff:nameOff+int64(k)]) == "bootargs" {
				v := st[i : i+ln]
				if len(v) == 0 || v[len(v)-1] != 0 || bytes.IndexByte(v, 0) != len(v)-1 {
					return "", fmt.Errorf("/chosen/bootargs is not one string")
				}
				found++
				args = string(v[:len(v)-1])
			}
			i = (i + ln + 3) &^ 3
		case 4: // FDT_NOP
		case 9: // FDT_END
			if len(stack) != 0 {
				return "", fmt.Errorf("the structure ends inside a node")
			}
			if found != 1 {
				return "", fmt.Errorf("%d /chosen/bootargs properties, not 1", found)
			}
			return args, nil
		default:
			return "", fmt.Errorf("a bad token 0x%x", tok)
		}
	}
	return "", fmt.Errorf("the structure has no end token")
}
