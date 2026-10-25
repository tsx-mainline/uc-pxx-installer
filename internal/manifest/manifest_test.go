package manifest

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestEmbedded(t *testing.T) {
	ms, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) < 2 {
		t.Fatalf("%d manifests", len(ms))
	}
	ud := ByIncremental(ms, "210836")
	st := ByIncremental(ms, "231157")
	if ud == nil || st == nil || ud.Firmware.Version != "1.0.0.94" || st.Firmware.Version != "1.0.6.20" {
		t.Fatal("1.0.0.94 / 1.0.6.20 not found by ro.build.version.incremental")
	}
	if ByFirmwareSHA(ms, st.Firmware.SHA256) != st {
		t.Fatal("ByFirmwareSHA")
	}
	// facts the design depends on (docs: fw-installer-design.md §5.2, §6)
	if ud.Vbmeta.PublicKeySHA256 != st.Vbmeta.PublicKeySHA256 {
		t.Error("userdebug and stock vbmeta keys differ")
	}
	a, _ := ud.Part("trust")
	b, _ := st.Part("trust")
	if a.SHA256 != b.SHA256 || a.Action != "write-if-different" {
		t.Error("trust is expected to be identical in both builds and written only if different")
	}
	if p, _ := ud.Part("uboot"); p.SegID != "0xfd02" {
		t.Errorf("uboot is segment %s", p.SegID)
	}
}

func TestRejects(t *testing.T) {
	base := `{"schema":1,"firmware":{"version":"x","file":"f","size":1,"sha256":"` + strings.Repeat("a", 64) +
		`"},"build":{"ro.build.version.incremental":"1"},"build_prop_path":"/system/build.prop",` +
		`"vbmeta":{},"partitions":[PARTS]}`
	part := func(name, sha, action string) string {
		return `{"segment":0,"seg_id":"0x0000","partition":"` + name + `","image":"i","size":1,"sha256":"` +
			sha + `","action":"` + action + `"}`
	}
	all := func(override string) string {
		var ps []string
		for _, n := range known {
			if n == "boot" && override != "" {
				ps = append(ps, override)
				continue
			}
			ps = append(ps, part(n, strings.Repeat("b", 64), "write"))
		}
		return strings.Replace(base, "PARTS", strings.Join(ps, ","), 1)
	}
	if _, err := Load(fstest.MapFS{"m.json": {Data: []byte(all(""))}}); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	bad := map[string]string{
		"idbloader partition": all(part("idbloader", strings.Repeat("b", 64), "write")),
		"short sha":           all(part("boot", "abcd", "write")),
		"unknown action":      all(part("boot", strings.Repeat("b", 64), "erase")),
		"unknown field":       strings.Replace(all(""), `"schema":1`, `"schema":1,"extra":2`, 1),
		"schema 2":            strings.Replace(all(""), `"schema":1`, `"schema":2`, 1),
	}
	for name, doc := range bad {
		if _, err := Load(fstest.MapFS{"m.json": {Data: []byte(doc)}}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
