package mainline

// The patched vbmeta of the mainline slot (payload contract v2). vbmeta_a is
// the keeper's signed vbmeta (vbmeta_b, the 1.0.0.94 userdebug vbmeta) with
// only the image_size of its boot hash descriptor changed: libavb loads only
// image_size bytes of boot_a and U-Boot starts that buffer, and the signed
// value (38,029,312) is smaller than the mainline boot image. The new value
// is the size of boot_a, 67,108,864: unit 2's vbmeta_a holds it since
// 2026-10-24 (ucp10/docs/mainline-slotA-bringup-2026-10-24.md, v11).
//
// The change breaks the vbmeta signature. That adds no new dependency: the
// boot hash of a mainline image never matches the signed digest, so a
// mainline slot starts only on an unlocked unit (orange) anyway; on a locked
// unit slot A fails its verification and the keeper B starts.
//
// The payload does not carry the patched file (it is a vendor file). The
// manifest names it by sha256; the installer makes it again here from the
// bytes of vbmeta_b, and the payload gate allows only a reviewed patch
// (ReviewedVbmetaPatches: the source sha256, the field offset, the new
// image_size and the result sha256).

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"uc-pxx-installer/internal/avb"
)

// The reviewed patch of contract v2: uc-pxx-linux
// tools/mainline-payload/vbmeta-patch.py makes the same bytes.
const (
	// PatchedVbmetaSource is the sha256 of the signed 1.0.0.94 userdebug
	// vbmeta image (8192 bytes; manifest-1.0.0.94-userdebug.json).
	PatchedVbmetaSource = "7958c09b4fb6fb3d54a2382682074b4812e92cc1d81ca3183b20f03ff52f5f5c"
	// PatchedVbmetaSHA256 is the sha256 of the patch of that image.
	PatchedVbmetaSHA256 = "cebd4d8a68c8861b9801c4d83a14ab3d06f1d85b015a01686fae2d305a69f35e"
	// PatchedVbmetaSize is the size of both images.
	PatchedVbmetaSize = 8192
	// PatchedFieldOffset is the file offset of the image_size field of the
	// boot hash descriptor in that image.
	PatchedFieldOffset = 1088
	// PatchedSignedBootSize is the signed image_size, PatchedBootImageSize
	// the patched one (the size of boot_a).
	PatchedSignedBootSize = 38029312
	PatchedBootImageSize  = 67108864
)

// VbmetaPatch is one reviewed patch: the source image, the field and the
// new value, and the result.
type VbmetaPatch struct {
	SourceSHA256  string `json:"source_sha256"`
	Size          int64  `json:"size"`
	FieldOffset   int64  `json:"field_offset"`
	OldImageSize  uint64 `json:"old_boot_image_size"`
	BootImageSize uint64 `json:"boot_image_size"`
	SHA256        string `json:"sha256"`
}

// reviewedVbmetaPatches are the reviewed patches of this release (the
// payload gate of a release build). A new one needs an Opus review.
var reviewedVbmetaPatches = []VbmetaPatch{{
	SourceSHA256: PatchedVbmetaSource, Size: PatchedVbmetaSize, FieldOffset: PatchedFieldOffset,
	OldImageSize: PatchedSignedBootSize, BootImageSize: PatchedBootImageSize, SHA256: PatchedVbmetaSHA256,
}}

// ReviewedVbmetaPatches is a copy of the release's list.
func ReviewedVbmetaPatches() []VbmetaPatch {
	return append([]VbmetaPatch(nil), reviewedVbmetaPatches...)
}

// PatchFacts is what PatchVbmeta did.
type PatchFacts struct {
	FieldOffset  int64  `json:"field_offset"`
	OldImageSize uint64 `json:"old_boot_image_size"`
	NewImageSize uint64 `json:"boot_image_size"`
	Changed      []int  `json:"changed_bytes"` // the file offsets of the bytes that differ
	SourceSHA256 string `json:"source_sha256"`
	SHA256       string `json:"sha256"`
}

// PatchVbmeta returns a copy of the vbmeta image signed with only the
// image_size of its one hash descriptor of "boot" set to newSize. It parses
// the image with the installer's AVB reader (avb.Parse: the header, the
// blocks, every descriptor; a chain, property or unknown descriptor refuses),
// finds the descriptor by walking the descriptor block, and checks that the
// result parses to the same header and descriptors with only that
// image_size changed, and that it differs from signed only inside the 8-byte
// field. It does not check a signature: the caller verifies signed first
// (avb.Verify) and compares the result's sha256 with a reviewed patch.
func PatchVbmeta(signed []byte, newSize uint64) ([]byte, PatchFacts, error) {
	var pf PatchFacts
	v, err := avb.Parse(signed, "vbmeta_b")
	if err != nil {
		return nil, pf, err
	}
	// the descriptors are contiguous from the descriptor offset of the
	// auxiliary block (avb.Parse checked that they fill it exactly)
	pos := int64(avb.HeaderSize) + int64(v.AuthSize) + int64(v.DescOffset)
	field, n := int64(-1), 0
	for i, d := range v.Descriptors {
		if d.Type == "hash" && d.Partition == "boot" {
			field, n = pos+16, n+1 // the image_size is the first field after the tag and the length
			pf.OldImageSize = d.ImageSize
		}
		pos += int64(len(v.Raw[i]))
	}
	if n != 1 {
		return nil, pf, fmt.Errorf("vbmeta_b has %d hash descriptors of boot, not exactly one", n)
	}
	if field+8 > int64(len(signed)) || binary.BigEndian.Uint64(signed[field:]) != pf.OldImageSize {
		return nil, pf, fmt.Errorf("the boot image_size field at %d does not hold the parsed value %d", field, pf.OldImageSize)
	}
	if newSize == 0 || newSize > MaxBootImage {
		return nil, pf, fmt.Errorf("a boot image_size of %d (1 to %d)", newSize, MaxBootImage)
	}
	out := append([]byte(nil), signed...)
	binary.BigEndian.PutUint64(out[field:], newSize)
	for i := range out {
		if out[i] != signed[i] {
			pf.Changed = append(pf.Changed, i)
		}
	}
	for _, i := range pf.Changed {
		if int64(i) < field || int64(i) >= field+8 {
			return nil, pf, fmt.Errorf("the patch changed byte %d, outside the field %d..%d", i, field, field+7)
		}
	}
	w, err := avb.Parse(out, "patched vbmeta")
	if err != nil {
		return nil, pf, fmt.Errorf("the patched vbmeta does not parse: %w", err)
	}
	if w.Header != v.Header || len(w.Descriptors) != len(v.Descriptors) || !bytes.Equal(w.PublicKey, v.PublicKey) ||
		!bytes.Equal(w.PKMetadata, v.PKMetadata) {
		return nil, pf, fmt.Errorf("the patched vbmeta has another header, key or descriptor count")
	}
	for i := range v.Descriptors {
		want := v.Descriptors[i]
		if want.Type == "hash" && want.Partition == "boot" {
			want.ImageSize = newSize
		}
		if w.Descriptors[i] != want {
			return nil, pf, fmt.Errorf("descriptor %d of the patched vbmeta is not the signed one with the new boot image_size", i)
		}
	}
	pf.FieldOffset, pf.NewImageSize = field, newSize
	pf.SourceSHA256, pf.SHA256 = hexSHA(signed), hexSHA(out)
	return out, pf, nil
}

// FindVbmetaPatch is the reviewed patch of a list for a source sha256 and a
// result sha256 (nil if none).
func FindVbmetaPatch(list []VbmetaPatch, source, sha string) *VbmetaPatch {
	for i := range list {
		if list[i].SourceSHA256 == source && list[i].SHA256 == sha {
			p := list[i]
			return &p
		}
	}
	return nil
}

// Matches reports a PatchVbmeta result that is exactly this reviewed patch.
func (r *VbmetaPatch) Matches(pf PatchFacts, size int) bool {
	return r != nil && pf.SourceSHA256 == r.SourceSHA256 && pf.SHA256 == r.SHA256 && int64(size) == r.Size &&
		pf.FieldOffset == r.FieldOffset && pf.OldImageSize == r.OldImageSize && pf.NewImageSize == r.BootImageSize
}
