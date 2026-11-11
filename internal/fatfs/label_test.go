package fatfs

import (
	"os"
	"path/filepath"
	"testing"

	"uc-pxx-installer/internal/fatfs/fattest"
)

// TestDriveLabelsAccepted (mainline-rc rename, owner 2026-11-11): the drive
// label changed from P10-INSTALL to UC-PXX-INST, and the old label must still
// be accepted. The reader never gates on the label (it only reports it), so a
// firmware drive opens with the new label, the old one, no label or another
// one; the label is read back as written.
func TestDriveLabelsAccepted(t *testing.T) {
	for _, label := range []string{"UC-PXX-INST", "P10-INSTALL", "", "OTHER"} {
		t.Run(label, func(t *testing.T) {
			im := fattest.New(fattest.Spec{Type: 32, Sectors: 70000, SecPerClus: 1, Label: label})
			im.Root.File("fw.bin", pattern(7, 5000))
			img := filepath.Join(t.TempDir(), "drive.img")
			if err := os.WriteFile(img, im.B, 0o600); err != nil {
				t.Fatal(err)
			}
			f, err := OpenFirmware(img)
			if err != nil {
				t.Fatalf("label %q: %v", label, err)
			}
			checkContent(t, f, pattern(7, 5000))
			if got := f.fs.Info().Label; label != "" && got != label {
				t.Fatalf("label %q read back as %q", label, got)
			}
			f.Close()
		})
	}
}
