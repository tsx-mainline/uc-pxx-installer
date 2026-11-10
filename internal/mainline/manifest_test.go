package mainline_test

import (
	"encoding/json"
	"strings"
	"testing"

	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/mainline/mltest"
)

var testChain = mltest.Chain{Version: "1.0.0.94", BuildType: "userdebug",
	UBoot:    "c6e80c1590a44c9abd5756b8553787b84e096563f3726118949c44d1fe8d798c",
	Trust:    "a5ab4d9c5756b5b7f3e0860269207119733b4bc84e8d64600be9671ebb84a562",
	Dtbo:     "83b3e6321c43960bc4215635550a405b297ed965d7fc6ec548bb2b02d9d93629",
	Vbmeta:   "7958c09b4fb6fb3d54a2382682074b4812e92cc1d81ca3183b20f03ff52f5f5c",
	Resource: mltest.SHA(testResource)}

// testResource is a made-up resource (not the stock one).
var testResource = mltest.Fill(77, 5000)

func newBundle() *mltest.Bundle {
	return mltest.NewBundle("tok-test.1", 9000, 64<<10, testChain, testResource, mainline.PatchedBootImageSize)
}

// contract is the manifest of payload contract v2 (MAINLINE-INSTALL-PLAN.md
// "Payload contract v1" plus vbmeta_patched, MAINLINE-PAYLOAD-2.md) with
// real-looking values: it must parse.
const contract = `{
  "schema": 2,
  "kind": "uc-pxx-mainline",
  "version": "v1.0.0",
  "model": "UC-P10",
  "kernel": { "rev": "d02e92631f2810a4a43fb6f09824afb9b0044ee2" },
  "token": "uc-pxx-1a2b3c",
  "boot_template": { "file": "boot-template.img", "size": 22528000, "sha256": "1111111111111111111111111111111111111111111111111111111111111111" },
  "boot_assembled": { "resource_sha256": "290ef40577d434d702c3544076dbebd6ddd17e16087eff0e84c92549d67bb363",
                      "size": 23052288, "sha256": "2222222222222222222222222222222222222222222222222222222222222222" },
  "rootfs": { "file": "rootfs.ext4", "size": 536870912, "sha256": "3333333333333333333333333333333333333333333333333333333333333333", "label": "tsxroot" },
  "requires_bootchain": {
    "version": "1.0.0.94", "build_type": "userdebug",
    "uboot_sha256":  "c6e80c1590a44c9abd5756b8553787b84e096563f3726118949c44d1fe8d798c",
    "trust_sha256":  "a5ab4d9c5756b5b7f3e0860269207119733b4bc84e8d64600be9671ebb84a562",
    "dtbo_sha256":   "83b3e6321c43960bc4215635550a405b297ed965d7fc6ec548bb2b02d9d93629",
    "vbmeta_sha256": "7958c09b4fb6fb3d54a2382682074b4812e92cc1d81ca3183b20f03ff52f5f5c",
    "resource_sha256": "290ef40577d434d702c3544076dbebd6ddd17e16087eff0e84c92549d67bb363"
  },
  "vbmeta_patched": { "source_sha256": "7958c09b4fb6fb3d54a2382682074b4812e92cc1d81ca3183b20f03ff52f5f5c", "size": 8192,
                      "sha256": "cebd4d8a68c8861b9801c4d83a14ab3d06f1d85b015a01686fae2d305a69f35e",
                      "field_offset": 1088, "boot_image_size": 67108864 },
  "vbmeta_boot_cap": 67108864
}`

func TestParseContractExample(t *testing.T) {
	m, err := mainline.ParseManifest([]byte(contract))
	if err != nil {
		t.Fatal(err)
	}
	if m.Token != "uc-pxx-1a2b3c" || m.VbmetaBootCap != 67108864 || m.Rootfs.Size != 536870912 ||
		m.RequiresBootchain.VbmetaSHA256 != testChain.Vbmeta || m.VbmetaPatched.SHA256 != mainline.PatchedVbmetaSHA256 ||
		m.VbmetaPatched.BootImageSize != mainline.PatchedBootImageSize || m.VbmetaPatched.FieldOffset != mainline.PatchedFieldOffset {
		t.Fatalf("%+v", m)
	}
	if _, err := mainline.ParseManifest(newBundle().ManifestJSON()); err != nil {
		t.Fatal(err)
	}
}

// edit applies a change to a fresh parse of contract and returns the JSON.
func edit(t *testing.T, f func(m map[string]any)) []byte {
	var m map[string]any
	if err := json.Unmarshal([]byte(contract), &m); err != nil {
		t.Fatal(err)
	}
	f(m)
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sub(m map[string]any, k string) map[string]any { return m[k].(map[string]any) }

func TestParseRefusals(t *testing.T) {
	raw := []struct {
		name, text, want string
	}{
		{"empty", "", "bytes"},
		{"not an object", `[1]`, "not a JSON object"},
		{"two objects", contract + contract, "data after"},
		{"trailing garbage", contract + " x", "data after"},
		{"byte order mark", "\xef\xbb\xbf" + contract, "byte order mark"},
		{"duplicate key", strings.Replace(contract, `"schema": 2,`, `"schema": 2, "schema": 2,`, 1), "given twice"},
		{"duplicate nested key", strings.Replace(contract, `"label": "tsxroot"`, `"label": "tsxroot", "label": "x"`, 1), "given twice"},
		{"too large", contract + strings.Repeat(" ", mainline.MaxManifest), "bytes"},
		{"not JSON", `{"schema": 1,`, "mainline manifest"},
	}
	for _, c := range raw {
		_, err := mainline.ParseManifest([]byte(c.text))
		if err == nil || !strings.Contains(err.Error(), c.want) || !mainline.IsManifestError(err) {
			t.Errorf("%s: err %v, want %q", c.name, err, c.want)
		}
	}
	sha := strings.Repeat("a", 64)
	cases := []struct {
		name string
		f    func(m map[string]any)
		want string
	}{
		{"unknown top key", func(m map[string]any) { m["extra"] = 1 }, "unknown key \"extra\""},
		{"unknown nested key", func(m map[string]any) { sub(m, "rootfs")["mode"] = "x" }, "unknown key \"rootfs.mode\""},
		{"missing key", func(m map[string]any) { delete(m, "token") }, "missing key \"token\""},
		{"missing nested key", func(m map[string]any) { delete(sub(m, "requires_bootchain"), "dtbo_sha256") }, "missing key \"requires_bootchain.dtbo_sha256\""},
		{"null value", func(m map[string]any) { m["version"] = nil }, "is null"},
		{"array value", func(m map[string]any) { m["model"] = []any{"UC-P10"} }, "object or array"},
		{"object where a value is", func(m map[string]any) { m["token"] = map[string]any{} }, "object or array"},
		{"value where an object is", func(m map[string]any) { m["kernel"] = "d02e9263" }, "must be an object"},
		{"schema 1 (contract v1: the signed vbmeta)", func(m map[string]any) { m["schema"] = 1 }, "schema 1"},
		{"schema 3", func(m map[string]any) { m["schema"] = 3 }, "schema 3"},
		{"schema as string", func(m map[string]any) { m["schema"] = "1" }, "mainline manifest"},
		{"schema fraction", func(m map[string]any) { m["schema"] = 1.5 }, "mainline manifest"},
		{"kind", func(m map[string]any) { m["kind"] = "official" }, "kind"},
		{"model UC-P8", func(m map[string]any) { m["model"] = "UC-P8" }, "only on a UC-P10"},
		{"model lower case", func(m map[string]any) { m["model"] = "uc-p10" }, "only on a UC-P10"},
		{"bad version", func(m map[string]any) { m["version"] = "v1 0" }, "version"},
		{"empty version", func(m map[string]any) { m["version"] = "" }, "version"},
		{"short kernel rev", func(m map[string]any) { sub(m, "kernel")["rev"] = "d02e9263" }, "full commit id"},
		{"other kernel rev", func(m map[string]any) { sub(m, "kernel")["rev"] = strings.Repeat("1", 40) }, "not the reviewed kernel"},
		{"token too long", func(m map[string]any) { m["token"] = "0123456789abcdef" }, "token"},
		{"token space", func(m map[string]any) { m["token"] = "a b" }, "token"},
		{"template name", func(m map[string]any) { sub(m, "boot_template")["file"] = "boot.img" }, "boot_template.file"},
		{"template sha upper case", func(m map[string]any) { sub(m, "boot_template")["sha256"] = strings.Repeat("A", 64) }, "boot_template.sha256"},
		{"template sha short", func(m map[string]any) { sub(m, "boot_template")["sha256"] = "abc" }, "boot_template.sha256"},
		{"template size not pages", func(m map[string]any) { sub(m, "boot_template")["size"] = 22528001 }, "boot_template.size"},
		{"template size negative", func(m map[string]any) { sub(m, "boot_template")["size"] = -2048 }, "boot_template.size"},
		{"template too large", func(m map[string]any) { sub(m, "boot_template")["size"] = 64<<20 + 2048 }, "boot_template.size"},
		{"assembled sha", func(m map[string]any) { sub(m, "boot_assembled")["sha256"] = "x" }, "boot_assembled"},
		{"assembled resource sha", func(m map[string]any) { sub(m, "boot_assembled")["resource_sha256"] = "" }, "boot_assembled"},
		{"assembled not larger", func(m map[string]any) { sub(m, "boot_assembled")["size"] = 22528000 }, "boot_assembled.size"},
		{"assembled not pages", func(m map[string]any) { sub(m, "boot_assembled")["size"] = 23052289 }, "boot_assembled.size"},
		{"assembled over the cap", func(m map[string]any) { m["vbmeta_boot_cap"] = 23050240 }, "larger than vbmeta_boot_cap"},
		{"cap zero", func(m map[string]any) { m["vbmeta_boot_cap"] = 0 }, "vbmeta_boot_cap"},
		{"cap too large", func(m map[string]any) { m["vbmeta_boot_cap"] = 64<<20 + 1 }, "vbmeta_boot_cap"},
		{"no vbmeta_patched", func(m map[string]any) { delete(m, "vbmeta_patched") }, "missing key \"vbmeta_patched\""},
		{"vbmeta_patched with a file", func(m map[string]any) { sub(m, "vbmeta_patched")["file"] = "vbmeta.img" }, "unknown key \"vbmeta_patched.file\""},
		{"vbmeta_patched sha", func(m map[string]any) { sub(m, "vbmeta_patched")["sha256"] = "cebd4d8a" }, "vbmeta_patched"},
		{"vbmeta_patched source sha", func(m map[string]any) { sub(m, "vbmeta_patched")["source_sha256"] = strings.Repeat("G", 64) }, "vbmeta_patched"},
		{"vbmeta_patched of another source", func(m map[string]any) { sub(m, "vbmeta_patched")["source_sha256"] = sha }, "not requires_bootchain.vbmeta_sha256"},
		{"vbmeta_patched is its source", func(m map[string]any) {
			sub(m, "vbmeta_patched")["sha256"] = sub(m, "vbmeta_patched")["source_sha256"]
		}, "not a patch"},
		{"vbmeta_patched size zero", func(m map[string]any) { sub(m, "vbmeta_patched")["size"] = 0 }, "vbmeta_patched.size"},
		{"vbmeta_patched size not sectors", func(m map[string]any) { sub(m, "vbmeta_patched")["size"] = 8193 }, "vbmeta_patched.size"},
		{"vbmeta_patched size too large", func(m map[string]any) { sub(m, "vbmeta_patched")["size"] = 2 << 20 }, "vbmeta_patched.size"},
		{"vbmeta_patched field outside", func(m map[string]any) { sub(m, "vbmeta_patched")["field_offset"] = 8188 }, "field_offset"},
		{"vbmeta_patched field negative", func(m map[string]any) { sub(m, "vbmeta_patched")["field_offset"] = -1 }, "field_offset"},
		{"cap is not the patched image_size", func(m map[string]any) { sub(m, "vbmeta_patched")["boot_image_size"] = 38029312 }, "is not vbmeta_patched.boot_image_size"},
		{"resource differs", func(m map[string]any) { sub(m, "boot_assembled")["resource_sha256"] = sha }, "resource_sha256"},
		{"rootfs name", func(m map[string]any) { sub(m, "rootfs")["file"] = "rootfs.img" }, "rootfs.file"},
		{"rootfs size not sectors", func(m map[string]any) { sub(m, "rootfs")["size"] = 536870913 }, "rootfs.size"},
		{"rootfs size zero", func(m map[string]any) { sub(m, "rootfs")["size"] = 0 }, "rootfs.size"},
		{"rootfs sha", func(m map[string]any) { sub(m, "rootfs")["sha256"] = strings.Repeat("g", 64) }, "rootfs.sha256"},
		{"rootfs label", func(m map[string]any) { sub(m, "rootfs")["label"] = "rootfs" }, "rootfs.label"},
		{"chain version", func(m map[string]any) { sub(m, "requires_bootchain")["version"] = "1.0.94" }, "requires_bootchain.version"},
		{"chain user build", func(m map[string]any) { sub(m, "requires_bootchain")["build_type"] = "user" }, "userdebug"},
		{"chain uboot sha", func(m map[string]any) { sub(m, "requires_bootchain")["uboot_sha256"] = "c6e80c15" }, "requires_bootchain"},
		{"chain vbmeta sha", func(m map[string]any) { sub(m, "requires_bootchain")["vbmeta_sha256"] = strings.Repeat("0", 63) }, "requires_bootchain"},
	}
	for _, c := range cases {
		_, err := mainline.ParseManifest(edit(t, c.f))
		if err == nil || !strings.Contains(err.Error(), c.want) || !mainline.IsManifestError(err) {
			t.Errorf("%s: err %v, want %q", c.name, err, c.want)
		}
	}
}

// FuzzParseManifest: no input panics, and an accepted manifest passes Check
// and has exactly the contract's keys when written again.
func FuzzParseManifest(f *testing.F) {
	f.Add([]byte(contract))
	f.Add(newBundle().ManifestJSON())
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"schema":2,"schema":2}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		m, err := mainline.ParseManifest(b)
		if err != nil {
			if !mainline.IsManifestError(err) {
				t.Fatalf("an error that is not a manifest error: %v", err)
			}
			return
		}
		if err := m.Check(); err != nil {
			t.Fatalf("accepted, but Check fails: %v", err)
		}
		again, _ := json.Marshal(m)
		if _, err := mainline.ParseManifest(again); err != nil {
			t.Fatalf("accepted, but its own JSON is refused: %v", err)
		}
	})
}
