package status

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uc-pxx-installer/internal/factory/factorytest"
	"uc-pxx-installer/internal/fakedev"
	"uc-pxx-installer/internal/manifest"
	"uc-pxx-installer/internal/nvram/nvramtest"
)

// TestUnitModel: the status shows the model of the factory data in the
// Model row (no row more), with the suffix and the revision, and says when
// the installer will not write.
func TestUnitModel(t *testing.T) {
	ab := abSector([3]byte{15, 0, 1}, [3]byte{14, 0, 1})
	for _, c := range []struct {
		name, dt string
		cfg      []byte
		unit     Unit
		row      string
	}{
		{"unit 2", "Crestron UC-P10", factorytest.Unit2Like("UC-P10-T-HS"),
			Unit{Value: "UC-P10-T-HS", Base: "UC-P10", Suffix: "-T-HS", HWRev: "V1.5A", ImageBase: "UC-P10"},
			"  Model      Crestron UC-P10, unit UC-P10-T-HS (factory data, hardware V1.5A)"},
		{"a UC-P8", "Crestron UC-P8", factorytest.Unit2Like("UC-P8-T-HS"),
			Unit{Value: "UC-P8-T-HS", Base: "UC-P8", Suffix: "-T-HS", HWRev: "V1.5A", ImageBase: "UC-P8"},
			"  Model      Crestron UC-P8, unit UC-P8-T-HS (factory data, hardware V1.5A)"},
		{"a UC-P8 in the UC-P10 image", "Crestron UC-P10", factorytest.Unit2Like("UC-P8-T-HS"),
			Unit{Value: "UC-P8-T-HS", Base: "UC-P8", Suffix: "-T-HS", HWRev: "V1.5A", ImageBase: "UC-P10"},
			"  Model      Crestron UC-P10, but the unit's factory data says UC-P8-T-HS (a UC-P8). The installer will not write"},
		{"another model", "Crestron UC-P10", factorytest.Unit2Like("UC-P12"),
			Unit{Value: "UC-P12", HWRev: "V1.5A", ImageBase: "UC-P10",
				Err: `the factory data names no UC-P10 or UC-P8: it says "UC-P12"`},
			`  Model      Crestron UC-P10; the unit's factory data: the factory data names no UC-P10 or UC-P8: it says "UC-P12". The installer will not write`},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, m := deviceWith(t, ab, nvramtest.Off(), func(p []fakedev.Part) []fakedev.Part {
				p[fakedev.PartNum(p, "cfg")-1].Data = c.cfg
				return p
			})
			os.WriteFile(filepath.Join(root, "proc/device-tree/model"), []byte(c.dt+"\x00"), 0o644)
			r := Collect(Source{Root: root, Manifests: []*manifest.Manifest{m},
				Cmdline: "uc-pxx.id=p10inst-test uc-pxx-inst.ab=" + hex.EncodeToString(ab[:32])})
			if r.Unit != c.unit {
				t.Fatalf("unit %+v, want %+v", r.Unit, c.unit)
			}
			if r.Unit.Match() != (c.unit.Err == "" && c.unit.Base == c.unit.ImageBase) {
				t.Fatal("Match")
			}
			lines := r.Lines()
			if lines[1].Text != c.row {
				t.Fatalf("row %q, want %q", lines[1].Text, c.row)
			}
			if r.MAC != "02:00:5e:10:00:01" {
				t.Fatalf("MAC %q, want the made-up one", r.MAC)
			}
			if strings.Contains(strings.Join(func() []string {
				var s []string
				for _, l := range lines {
					s = append(s, l.Text)
				}
				return s
			}(), "\n"), "FAKE") {
				t.Fatal("a factory record other than the model is shown")
			}
		})
	}
}
