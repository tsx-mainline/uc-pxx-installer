package manifest_test

import (
	"encoding/json"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"uc-pxx-installer/internal/avb"
	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/manifest"
	"uc-pxx-installer/manifests"
)

// The reviewed decisions of the embedded set (FIRMWARE-ALL37, DTB-REVIEW,
// FIRMWARE-GENERAL §10, §11). A change of a status or a resource status
// must change this test in the same reviewed commit.
var (
	resourceStatus = map[string]string{
		"290ef40577d434d702c3544076dbebd6ddd17e16087eff0e84c92549d67bb363": "booted", // 1.0.0.80-1.0.0.105, booted on unit 2 (1.0.0.94 userdebug)
		"0ffbc829700ac51c8883d72711214ae1d3981b7b1c75ab2c3f1ba9c3a7f6e2e8": "booted", // 1.0.6.20, booted on unit 2
		"2086380a": "reviewed", // 1.0.0.106, 1.0.0.107: DTB-REVIEW GO for switch-2 boards; never booted
		"6e4bd38b": "reviewed", // 1.0.4.22-1.0.5.20: the same
		"dbef6308": "no-go",    // 1.0.0.46-1.0.0.67: panel supply on PMIC switch 1
		"aefe9efb": "no-go",    // 1.0.0.72-1.0.0.75: the same
	}
	// The nvram driver of all 37 kernels is the reviewed code (FIRMWARE-GENERAL §11.12).
	nvramAll = true
)

func TestEmbedded(t *testing.T) {
	ms, err := manifest.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 37 {
		t.Fatalf("%d manifests, want the 37 public files", len(ms))
	}
	ud := manifest.ByFingerprint(ms, "rockchip/pcd/pcd:9/PQ3A.190505.002/bxpan04012107:userdebug/test-keys")
	st := manifest.ByFingerprint(ms, "rockchip/pcd/pcd:9/PQ3A.190505.002/bxpan03172311:user/release-keys")
	if ud == nil || st == nil || ud.Name() != "1.0.0.94 (userdebug)" || st.Name() != "1.0.6.20 (user)" {
		t.Fatal("1.0.0.94 userdebug / 1.0.6.20 not found by their fingerprint")
	}
	if manifest.ByFirmwareSHA(ms, st.Firmware.SHA256) != st || manifest.ByFingerprint(ms, "") != nil {
		t.Fatal("lookup")
	}
	names, tests, refused := map[string]bool{}, 0, 0
	trust := ud.BootChain.TrustSHA256
	for _, m := range ms {
		n := m.Name()
		if names[n] {
			t.Errorf("two manifests named %s", n)
		}
		names[n] = true
		a := m.AVB
		if a.PIKCertSHA256 != avb.CrestronPIKCertSHA256 || a.PSKSubject != avb.CrestronPSKSubject ||
			a.RollbackIndex != 0 || a.PIKKeyVersion != 42 || a.PSKKeyVersion != 42 || m.Vbmeta.PublicKeySHA256 != ud.Vbmeta.PublicKeySHA256 {
			t.Errorf("%s: AVB facts %s %d/%d/%d", n, a.PIKCertSHA256, a.RollbackIndex, a.PIKKeyVersion, a.PSKKeyVersion)
		}
		if m.BootChain.TrustSHA256 != trust {
			t.Errorf("%s: trust %s", n, m.BootChain.TrustSHA256)
		}
		if p, _ := m.Part("trust"); p.Action != "write-if-different" {
			t.Errorf("%s: trust is written only if different", n)
		}
		want := ""
		for k, v := range resourceStatus {
			if strings.HasPrefix(m.BootChain.BootResourceSHA256, k) {
				want = v
			}
		}
		if want == "" || m.BootChain.ResourceStatus != want {
			t.Errorf("%s: resource %s status %q, want %q", n, m.BootChain.BootResourceSHA256[:8], m.BootChain.ResourceStatus, want)
		}
		switch {
		case want == "no-go":
			refused++
			if m.Status != manifest.StatusRefuse || !strings.Contains(m.RefuseReason, "older UC-P10 board") {
				t.Errorf("%s: a no-go resource must be refused with the board reason: %s %q", n, m.Status, m.RefuseReason)
			}
		case m.Status != manifest.StatusInstall:
			t.Errorf("%s: status %s, want install", n, m.Status)
		}
		if m.NvramChecked() != nvramAll {
			t.Errorf("%s: nvram model %q", n, m.NvramModel)
		}
		if m.TestBuild != strings.Contains(m.Firmware.File, "_based_") {
			t.Errorf("%s: test_build %v", n, m.TestBuild)
		}
		if m.TestBuild {
			tests++
		}
	}
	booted := 0
	for _, m := range ms {
		if m.BootchainProven() {
			booted++
			if n := m.Name(); n != "1.0.0.94 (userdebug)" && n != "1.0.6.20 (user)" {
				t.Errorf("%s: bootchain booted; only the two pairs that booted on unit 2 are (FWGEN-REVIEW F-1)", n)
			}
			if m.BootChain.BootchainBooted.FirmwareSHA256 != m.Firmware.SHA256 {
				t.Errorf("%s: not its own first-hand record", m.Name())
			}
		} else if m.BootChain.UBootBuild == "user" && strings.Contains(m.BootChain.Evidence, "booted on unit 2 (slot B, 1.0.0.94 userdebug") {
			t.Errorf("%s: the evidence implies that the user U-Boot booted with 290ef405 (F-1a)", m.Name())
		}
	}
	if booted != 2 {
		t.Errorf("%d booted pairs, want 2", booted)
	}
	if tests != 4 || refused != 12 {
		t.Errorf("%d test builds (want 4), %d refused (want 12)", tests, refused)
	}
	// Two files have the version 1.0.0.94: the names and the fingerprints differ.
	if !names["1.0.0.94 (user)"] || !names["1.0.0.94 (userdebug)"] {
		t.Error("the two 1.0.0.94 files")
	}
}

func fixtureDoc(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(fakeemmc.NewFixture(fakeemmc.FixtureOpt{}).Manifest, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func load(m map[string]any) error {
	b, _ := json.Marshal(m)
	_, err := manifest.Load(fstest.MapFS{"m.json": {Data: b}})
	return err
}

func descs(m map[string]any) []any { return m["avb"].(map[string]any)["descriptors"].([]any) }

// TestBootRecords: a manifest that claims its boot chain booted must name a
// manifest of the set with the same pair and its own first-hand record.
func TestBootRecords(t *testing.T) {
	ents, _ := fs.ReadDir(manifests.FS, ".")
	set := fstest.MapFS{}
	docs := map[string]map[string]any{}
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".json") {
			b, _ := fs.ReadFile(manifests.FS, e.Name())
			set[e.Name()] = &fstest.MapFile{Data: b}
			var d map[string]any
			json.Unmarshal(b, &d)
			docs[e.Name()] = d
		}
	}
	if _, err := manifest.Load(set); err != nil {
		t.Fatal(err)
	}
	bc := func(n string) map[string]any { return docs[n]["boot_chain"].(map[string]any) }
	rec620 := bc("manifest-1.0.6.20.json")["bootchain_booted"]
	rec094 := bc("manifest-1.0.0.94-userdebug.json")["bootchain_booted"]
	for name, c := range map[string]struct {
		status string
		rec    any
	}{
		"booted without a record":                {"booted", nil},
		"a record without booted":                {"not-booted", rec620},
		"the record of another pair (1.0.6.20)":  {"booted", rec620},
		"the record of another pair (userdebug)": {"booted", rec094},
		"a record naming no manifest of the set": {"booted", map[string]any{"firmware_sha256": strings.Repeat("ab", 32), "unit": "u", "slot": "B", "date": "d", "record": "r"}},
		"a status that is neither":               {"proven", nil},
	} {
		d := docs["manifest-1.0.0.103.json"]
		old := map[string]any{"s": bc("manifest-1.0.0.103.json")["bootchain_status"], "r": bc("manifest-1.0.0.103.json")["bootchain_booted"]}
		bc("manifest-1.0.0.103.json")["bootchain_status"], bc("manifest-1.0.0.103.json")["bootchain_booted"] = c.status, c.rec
		b, _ := json.Marshal(d)
		bad := fstest.MapFS{}
		for k, v := range set {
			bad[k] = v
		}
		bad["manifest-1.0.0.103.json"] = &fstest.MapFile{Data: b}
		if _, err := manifest.Load(bad); err == nil {
			t.Errorf("%s: loaded", name)
		}
		bc("manifest-1.0.0.103.json")["bootchain_status"], bc("manifest-1.0.0.103.json")["bootchain_booted"] = old["s"], old["r"]
	}
}

func TestRejects(t *testing.T) {
	if err := load(fixtureDoc(t)); err != nil {
		t.Fatalf("the fixture's manifest: %v", err)
	}
	sub := func(m map[string]any, k string) map[string]any { return m[k].(map[string]any) }
	setDesc := func(m map[string]any, part, k string, v any) {
		for _, d := range descs(m) {
			if d := d.(map[string]any); d["partition"] == part {
				d[k] = v
			}
		}
	}
	addDesc := func(m map[string]any, d any) { sub(m, "avb")["descriptors"] = append(descs(m), d) }
	cases := map[string]func(m map[string]any){
		"schema 1":                  func(m map[string]any) { m["schema"] = 1 },
		"unknown field":             func(m map[string]any) { m["extra"] = 2 },
		"unknown status":            func(m map[string]any) { m["status"] = "maybe" },
		"refuse without a reason":   func(m map[string]any) { m["status"] = "refuse" },
		"install with a reason":     func(m map[string]any) { m["refuse_reason"] = "x" },
		"install without evidence":  func(m map[string]any) { sub(m, "boot_chain")["evidence"] = nil },
		"install with a no-go":      func(m map[string]any) { sub(m, "boot_chain")["resource_status"] = "no-go" },
		"unknown resource status":   func(m map[string]any) { sub(m, "boot_chain")["resource_status"] = "ok" },
		"unknown nvram model":       func(m map[string]any) { m["nvram_model"] = "v2" },
		"uboot sha not the part's":  func(m map[string]any) { sub(m, "boot_chain")["uboot_sha256"] = strings.Repeat("c", 64) },
		"trust sha not the part's":  func(m map[string]any) { sub(m, "boot_chain")["trust_sha256"] = strings.Repeat("c", 64) },
		"bad resource sha":          func(m map[string]any) { sub(m, "boot_chain")["boot_resource_sha256"] = "abc" },
		"user U-Boot, userdebug OS": func(m map[string]any) { sub(m, "boot_chain")["uboot_build"] = "user" },
		"no fingerprint":            func(m map[string]any) { delete(sub(m, "build"), "ro.build.fingerprint") },
		"bad version":               func(m map[string]any) { sub(m, "firmware")["version"] = "1.0" },
		"avb flags":                 func(m map[string]any) { sub(m, "avb")["flags"] = 2 },
		"avb location":              func(m map[string]any) { sub(m, "avb")["rollback_index_location"] = 1 },
		"avb algorithm":             func(m map[string]any) { sub(m, "avb")["algorithm"] = "SHA512_RSA4096" },
		"avb chain partition":       func(m map[string]any) { sub(m, "avb")["chain_partitions"] = []string{"vendor"} },
		"rollback summary differs":  func(m map[string]any) { sub(m, "vbmeta")["rollback_index"] = 3 },
		"descriptor for oem": func(m map[string]any) {
			addDesc(m, map[string]any{"type": "hash", "partition": "oem", "image_size": 1})
		},
		"descriptor twice":            func(m map[string]any) { d := descs(m); addDesc(m, d[len(d)-1]) },
		"descriptor missing":          func(m map[string]any) { d := descs(m); sub(m, "avb")["descriptors"] = d[:len(d)-1] },
		"descriptor larger than part": func(m map[string]any) { setDesc(m, "dtbo", "image_size", 1<<30) },
		"no footer":                   func(m map[string]any) { delete(sub(m, "avb")["footers"].(map[string]any), "boot") },
		"idbloader partition":         func(m map[string]any) { m["partitions"].([]any)[0].(map[string]any)["partition"] = "idbloader" },
	}
	for name, f := range cases {
		m := fixtureDoc(t)
		f(m)
		if err := load(m); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// refuse (with a reason) and recognise are valid manifests, not installable
	m := fixtureDoc(t)
	m["status"], m["refuse_reason"] = "refuse", "For another board."
	if err := load(m); err != nil {
		t.Fatalf("refuse: %v", err)
	}
	// two manifests of one file
	b, _ := json.Marshal(fixtureDoc(t))
	if _, err := manifest.Load(fstest.MapFS{"a.json": {Data: b}, "b.json": {Data: b}}); err == nil {
		t.Error("two manifests of one file accepted")
	}
}
