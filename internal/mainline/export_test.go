package mainline

import (
	"fmt"
	"os"

	"uc-pxx-installer/internal/ext4"
)

// CheckRootfsPins is the file part of the release gate on a root file system
// image, as report lines (tests only: TestRealRootfsPins).
func CheckRootfsPins(img string) ([]string, error) {
	f, err := os.Open(img)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	fs, err := ext4.Open(f, st.Size())
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range bootControlPins {
		b, err := fs.ReadFile(p.Path, maxPinnedFile)
		switch {
		case err != nil:
			out = append(out, fmt.Sprintf("MISSING  %s: %v", p.Path, err))
		case hexSHA(b) == p.SHA256:
			out = append(out, fmt.Sprintf("MATCH    %s %s", p.Path, p.SHA256))
		default:
			out = append(out, fmt.Sprintf("MISMATCH %s: %s, the pin is %s", p.Path, hexSHA(b), p.SHA256))
		}
	}
	for _, l := range checkList(fs, bootControlPins) {
		out = append(out, "LIST     "+l)
	}
	for _, l := range checkAbsent(fs) {
		out = append(out, "ABSENT   "+l)
	}
	if b, err := fs.ReadFile(bootControlList, 64<<10); err == nil {
		out = append(out, "list file:\n"+string(b))
	}
	return out, nil
}

// ReleaseGateListing is the release gate (EmbeddedGate: the boot-control
// pins, the reviewed kernels, the reviewed vbmeta patches) with p listed as
// a reviewed payload (tests only: TestRealPayloadGate shows what the release
// gate would still refuse once a payload is listed).
func ReleaseGateListing(p *Payload) *Gate {
	g := EmbeddedGate()
	g.name = "embedded+listed"
	g.reviewed = append(g.reviewed, Reviewed{Version: "listed by the test", ManifestSHA256: p.ManifestSHA256,
		TemplateSHA256: hexSHA(p.tmpl), RootfsSHA256: p.Manifest.Rootfs.SHA256})
	return g
}

// ReadRootfsFile reads one file of a payload's root file system (tests only).
func ReadRootfsFile(p *Payload, path string) ([]byte, error) {
	fs, err := ext4.Open(p.rootfs, p.rootfs.Size())
	if err != nil {
		return nil, err
	}
	return fs.ReadFile(path, maxPinnedFile)
}

// SetBothPins sets the both-slot pins for a test; the returned func restores
// them (review T4-2 NIT-2).
func SetBothPins(p []PinnedFile) func() {
	old := testBothPins
	c := append([]PinnedFile(nil), p...)
	testBothPins = &c
	return func() { testBothPins = old }
}
