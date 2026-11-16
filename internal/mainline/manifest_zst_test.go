package mainline_test

import (
	"encoding/json"
	"strings"
	"testing"

	"uc-pxx-installer/internal/mainline"
)

// zst is the schema-3 form of the contract example: schema 3 and rootfs_zst.
func zst(t *testing.T, f func(m map[string]any)) []byte {
	return edit(t, func(m map[string]any) {
		m["schema"] = 3
		m["rootfs_zst"] = map[string]any{"file": "rootfs.ext4.zst", "size": 187654321,
			"sha256": "4444444444444444444444444444444444444444444444444444444444444444"}
		if f != nil {
			f(m)
		}
	})
}

// TestManifestSchema3 (mainline-rc-10, owner R21): schema 3 is schema 2 with
// rootfs_zst; the unpacked rootfs stays the manifest's rootfs.
func TestManifestSchema3(t *testing.T) {
	m, err := mainline.ParseManifest(zst(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if m.Schema != mainline.SchemaZst || m.RootfsZst == nil || m.RootfsZst.File != mainline.RootfsZstFile ||
		m.RootfsZst.Size != 187654321 || m.Rootfs.Size != 536870912 {
		t.Fatalf("%+v %+v", m, m.RootfsZst)
	}
	// a schema-2 manifest marshals as before: no rootfs_zst key
	m2, err := mainline.ParseManifest([]byte(contract))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(m2)
	if strings.Contains(string(b), "rootfs_zst") || m2.RootfsZst != nil {
		t.Fatalf("schema 2 marshals with rootfs_zst: %s", b)
	}
	cases := []struct {
		name string
		b    []byte
		want string
	}{
		{"schema 3 without rootfs_zst", edit(t, func(m map[string]any) { m["schema"] = 3 }), "needs rootfs_zst"},
		{"schema 2 with rootfs_zst", zst(t, func(m map[string]any) { m["schema"] = 2 }), "rootfs_zst in a schema 2"},
		{"schema 4", zst(t, func(m map[string]any) { m["schema"] = 4 }), "schema 4"},
		{"wrong file", zst(t, func(m map[string]any) { sub(m, "rootfs_zst")["file"] = "rootfs.ext4" }), "rootfs_zst.file"},
		{"bad sha", zst(t, func(m map[string]any) { sub(m, "rootfs_zst")["sha256"] = "ABC" }), "rootfs_zst.sha256"},
		{"size 0", zst(t, func(m map[string]any) { sub(m, "rootfs_zst")["size"] = 0 }), "rootfs_zst.size"},
		{"size too large", zst(t, func(m map[string]any) { sub(m, "rootfs_zst")["size"] = int64(mainline.MaxRootfs) + 1 }), "rootfs_zst.size"},
		{"missing key", zst(t, func(m map[string]any) { delete(sub(m, "rootfs_zst"), "sha256") }), `missing key "rootfs_zst.sha256"`},
		{"extra key", zst(t, func(m map[string]any) { sub(m, "rootfs_zst")["label"] = "x" }), `unknown key "rootfs_zst.label"`},
		{"not an object", zst(t, func(m map[string]any) { m["rootfs_zst"] = "rootfs.ext4.zst" }), "must be an object"},
		{"null", zst(t, func(m map[string]any) { m["rootfs_zst"] = nil }), "null"},
		{"nested optional key", zst(t, func(m map[string]any) { sub(m, "rootfs")["rootfs_zst"] = map[string]any{} }), "unknown key"},
	}
	for _, c := range cases {
		_, err := mainline.ParseManifest(c.b)
		if err == nil || !strings.Contains(err.Error(), c.want) || !mainline.IsManifestError(err) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	dup := strings.Replace(string(zst(t, nil)), `"rootfs_zst":`, `"rootfs_zst":{"file":"rootfs.ext4.zst","size":1,"sha256":"4444444444444444444444444444444444444444444444444444444444444444"},"rootfs_zst":`, 1)
	if _, err := mainline.ParseManifest([]byte(dup)); err == nil || !strings.Contains(err.Error(), "given twice") {
		t.Errorf("rootfs_zst twice: %v", err)
	}
}
