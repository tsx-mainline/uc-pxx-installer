package plan_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uc-pxx-installer/internal/manifest"
	"uc-pxx-installer/internal/pcdfw"
	"uc-pxx-installer/internal/plan"
)

// TestRealFirmware runs pass 1 (the manifest match and the AVB verifier,
// with the real pins) over real firmware files. It is skipped unless
//
//	UC_PXX_FW_DIR   a folder with pcdfw files (vendor data, not in the repo)
//	UC_PXX_KEYLIBS  a comma list of stock updater libraries: the key is
//	                found in them in memory, as the installer does on a unit
//	UC_PXX_FW_ALL=1 every *.bin of the folder (default: the three known files)
//
// The gate of each embedded manifest is checked as it is; then the AVB path
// runs with the gate opened in memory (status, resource, nvram), so that a
// refused firmware is shown to be Crestron-signed too: its refusal is by the
// review, not by accident. The key is never printed or written.
func TestRealFirmware(t *testing.T) {
	dir, libs := os.Getenv("UC_PXX_FW_DIR"), os.Getenv("UC_PXX_KEYLIBS")
	if dir == "" || libs == "" {
		t.Skip("UC_PXX_FW_DIR / UC_PXX_KEYLIBS not set")
	}
	var ls []pcdfw.Lib
	for _, p := range strings.Split(libs, ",") {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		ls = append(ls, pcdfw.Lib{Name: filepath.Base(p), Data: b})
	}
	ms, err := manifest.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	files := []string{"pcdfw-1.0.0.94-userdebug.bin", "pcdfw-1.0.6.20.bin", "pcdfw_1.0.0.98_based_94_dummy.bin"}
	if os.Getenv("UC_PXX_FW_ALL") == "1" {
		files, _ = filepath.Glob(filepath.Join(dir, "*.bin"))
		for i := range files {
			files[i] = filepath.Base(files[i])
		}
	}
	for _, name := range files {
		t.Run(name, func(t *testing.T) {
			f, err := os.Open(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			st, _ := f.Stat()
			found, err := pcdfw.FindKeys(f, st.Size(), ls, false)
			if err != nil || len(found.Pairs) != 1 {
				t.Fatalf("key search: %d pairs, %v", len(found.Pairs), err)
			}
			k := found.Pairs[0]
			var m *manifest.Manifest
			for _, x := range ms {
				if x.Firmware.File == name {
					m = x
				}
			}
			if m == nil {
				t.Fatal("no embedded manifest")
			}
			// open only for the two files whose pair of U-Boot and resource booted on unit 2 (F-1)
			if (plan.Gate(m) == "") != (name == "pcdfw-1.0.0.94-userdebug.bin" || name == "pcdfw-1.0.6.20.bin") {
				t.Fatalf("gate %q", plan.Gate(m))
			}
			// The AVB path with the gate opened in memory (this test only).
			o := *m
			o.Status, o.RefuseReason, o.NvramModel = manifest.StatusInstall, "", manifest.NvramModels[0]
			o.BootChain.ResourceStatus = manifest.ResourceBooted
			o.BootChain.BootchainStatus = manifest.BootchainBooted
			pl, err := plan.VerifyOfficial(f, st.Size(), k, []*manifest.Manifest{&o}, nil)
			if err != nil {
				t.Fatal(err)
			}
			a := pl.AVB
			if !a.Verified || len(a.Checked) != 4 || a.RollbackIndex != 0 || a.PIKKeyVersion != 42 || a.PSKKeyVersion != 42 ||
				pl.BootResource != m.BootChain.BootResourceSHA256 || pl.BootChain.UBootBuild != m.Build["ro.build.type"] {
				t.Fatalf("payload %+v", pl)
			}
			t.Logf("%s: Crestron-signed, %s, resource %s, gate %q", m.Name(), strings.Join(a.Checked, " "),
				pl.BootResource[:8], plan.Gate(m))
		})
	}
}
