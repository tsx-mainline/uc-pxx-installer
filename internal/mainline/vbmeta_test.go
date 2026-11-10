package mainline_test

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"

	"uc-pxx-installer/internal/avb"
	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/mainline/mltest"
)

// TestPatchVbmeta: on the fixture's signed vbmeta, PatchVbmeta changes only
// the 8-byte image_size of the boot hash descriptor; the result parses to
// the same header and descriptors with the new image_size, no longer
// verifies (its header hash covers the field), keeps the rollback and ATX
// facts, and is deterministic. Bad inputs are refused.
func TestPatchVbmeta(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	signed := fx.New["vbmeta"]
	v, err := avb.Parse(signed, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	bd, _ := v.Find("boot")
	const size = 1 << 20
	out, pf, err := mainline.PatchVbmeta(signed, size)
	if err != nil {
		t.Fatal(err)
	}
	if pf.OldImageSize != bd.ImageSize || pf.NewImageSize != size || len(out) != len(signed) || pf.SHA256 != mltest.SHA(out) ||
		pf.SourceSHA256 != mltest.SHA(signed) || binary.BigEndian.Uint64(out[pf.FieldOffset:]) != size {
		t.Fatalf("%+v", pf)
	}
	for i := range out {
		if (out[i] != signed[i]) != containsInt(pf.Changed, i) || out[i] != signed[i] && (int64(i) < pf.FieldOffset || int64(i) >= pf.FieldOffset+8) {
			t.Fatalf("byte %d", i)
		}
	}
	w, err := avb.Parse(out, "patched")
	if err != nil || w.Header != v.Header {
		t.Fatal(err)
	}
	if d, _ := w.Find("boot"); d.ImageSize != size {
		t.Fatalf("boot image_size %d", d.ImageSize)
	}
	if _, err := avb.Verify(out, fx.Manifests[0].Pins()); err == nil {
		t.Fatal("the patched vbmeta still verifies")
	}
	f0, e0 := avb.ReadFacts(signed)
	f1, e1 := avb.ReadFacts(out)
	if e0 != nil || e1 != nil || f0 != f1 {
		t.Fatalf("facts %+v %+v %v %v", f0, f1, e0, e1)
	}
	again, pf2, _ := mainline.PatchVbmeta(signed, size)
	if !bytes.Equal(again, out) || pf2.SHA256 != pf.SHA256 {
		t.Fatal("not deterministic")
	}
	if !bytes.Equal(signed, fx.New["vbmeta"]) {
		t.Fatal("the input was changed")
	}
	for name, in := range map[string][]byte{"empty": nil, "no magic": make([]byte, 8192), "truncated": signed[:300]} {
		if _, _, err := mainline.PatchVbmeta(in, size); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, _, err := mainline.PatchVbmeta(signed, 0); err == nil {
		t.Error("size 0 accepted")
	}
	if _, _, err := mainline.PatchVbmeta(signed, mainline.MaxBootImage+1); err == nil {
		t.Error("a size over the boot partition accepted")
	}
}

func containsInt(l []int, x int) bool {
	for _, v := range l {
		if v == x {
			return true
		}
	}
	return false
}

// TestPatchVbmetaReal: UC_PXX_VBMETA_094=FILE (the signed 1.0.0.94 userdebug
// vbmeta, a vendor file on the build host only) gives exactly the release's
// reviewed patch: bytes 1092-1094 change, sha256 cebd4d8a....
func TestPatchVbmetaReal(t *testing.T) {
	path := os.Getenv("UC_PXX_VBMETA_094")
	if path == "" {
		t.Skip("UC_PXX_VBMETA_094 is not set")
	}
	signed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := avb.Verify(signed, avb.CrestronPins()); err != nil {
		t.Fatalf("not the signed vbmeta: %v", err)
	}
	r := mainline.ReviewedVbmetaPatches()
	out, pf, err := mainline.PatchVbmeta(signed, mainline.PatchedBootImageSize)
	if err != nil || len(r) != 1 || !r[0].Matches(pf, len(out)) || pf.SHA256 != mainline.PatchedVbmetaSHA256 ||
		pf.FieldOffset != 1088 || len(pf.Changed) != 3 || pf.Changed[0] != 1092 || pf.Changed[2] != 1094 {
		t.Fatalf("%+v %v", pf, err)
	}
	t.Logf("real patch: %+v", pf)
}
