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
