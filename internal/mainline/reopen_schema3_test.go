package mainline_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/mainline/mltest"
)

// planCopy is the plan's payload as the install child reads it: the JSON of
// the plan file (Payload in plan.json), decoded again.
func planCopy(t *testing.T, p *mainline.Payload) *mainline.Payload {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var q mainline.Payload
	if err := json.Unmarshal(b, &q); err != nil {
		t.Fatal(err)
	}
	return &q
}

// schema3 makes b a schema-3 bundle (rootfs_zst; the folder holds the
// unpacked rootfs.ext4, the form the step-1 receiver and the drive unpack
// give the children).
func schema3(b *mltest.Bundle) {
	b.Manifest["schema"] = mainline.SchemaZst
	b.Manifest["rootfs_zst"] = map[string]any{"file": mainline.RootfsZstFile, "size": 4321,
		"sha256": "4444444444444444444444444444444444444444444444444444444444444444"}
}

// TestReopenSchema3PlanJSON (mainline-rc-10a, unit 4 2026-11-16): the install
// child of a schema-3 bundle opens it again (Reopen) against the payload of
// the plan file it read. Manifest.RootfsZst is a pointer: the bundle's
// manifest and the plan's are two parses, so the old struct compare (==)
// compared two addresses and refused with equal sha256s ("manifest.json:
// sha256 3b2755c1…, the plan's is 3b2755c1…"). The compare is by value; any
// change of the manifest still refuses.
func TestReopenSchema3PlanJSON(t *testing.T) {
	b := newBundle()
	schema3(b)
	d, err := b.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := verifyDir(t, d)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if p.Manifest.Schema != mainline.SchemaZst || p.Manifest.RootfsZst == nil {
		t.Fatalf("not a schema-3 payload: %+v", p.Manifest)
	}
	src, _ := mainline.DirSource(d)
	// the same process (no JSON) and the plan file's JSON form
	for name, w := range map[string]*mainline.Payload{"pass 1": p, "plan.json": planCopy(t, p)} {
		q, err := mainline.Reopen(src, w)
		if err != nil {
			t.Fatalf("%s: reopen of the plan's own schema-3 bundle: %v", name, err)
		}
		if !q.Verified() || q.RootfsHashed() || q.ManifestSHA256 != p.ManifestSHA256 || q.Manifest.RootfsZst == nil ||
			*q.Manifest.RootfsZst != *p.Manifest.RootfsZst {
			t.Fatalf("%s: reopened %+v", name, q)
		}
		if a, b := mustJSON(t, p), mustJSON(t, q); a != b {
			t.Fatalf("%s: plan JSON differs:\n%s\n%s", name, a, b)
		}
		q.Close()
	}

	// any change of the manifest refuses: in the plan's copy (the sha256 kept)
	for _, c := range []struct {
		name string
		f    func(w *mainline.Payload)
	}{
		{"rootfs_zst.sha256", func(w *mainline.Payload) { w.Manifest.RootfsZst.SHA256 = strings.Repeat("5", 64) }},
		{"rootfs_zst.size", func(w *mainline.Payload) { w.Manifest.RootfsZst.Size++ }},
		{"rootfs_zst.file", func(w *mainline.Payload) { w.Manifest.RootfsZst.File = mainline.RootfsFile }},
		{"no rootfs_zst", func(w *mainline.Payload) { w.Manifest.RootfsZst = nil }},
		{"schema 2", func(w *mainline.Payload) { w.Manifest.Schema, w.Manifest.RootfsZst = mainline.Schema, nil }},
		{"rootfs.sha256", func(w *mainline.Payload) { w.Manifest.Rootfs.SHA256 = strings.Repeat("6", 64) }},
		{"version", func(w *mainline.Payload) { w.Manifest.Version += "x" }},
		{"manifest sha256", func(w *mainline.Payload) { w.ManifestSHA256 = strings.Repeat("0", 64) }},
	} {
		w := planCopy(t, p)
		c.f(w)
		if q, err := mainline.Reopen(src, w); err == nil || !strings.Contains(err.Error(), "manifest.json: ") {
			if q != nil {
				q.Close()
			}
			t.Errorf("%s changed in the plan: %v, want a manifest.json refusal", c.name, err)
		}
	}
	// a schema-2 plan of the same bundle the other way round: the plan has
	// rootfs_zst, the bundle on the drive not (another manifest: its sha256)
	w := planCopy(t, p)
	b2 := newBundle()
	d2, err := b2.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w.Source = "folder " + d2
	s2, _ := mainline.DirSource(d2)
	if _, err := mainline.Reopen(s2, w); err == nil || !strings.Contains(err.Error(), "manifest.json: sha256") {
		t.Errorf("a schema-2 bundle for a schema-3 plan: %v", err)
	}
	// a changed manifest file on the drive (rootfs_zst changed: a new sha256)
	sub := b.Sub("rootfs_zst")
	sub["size"] = 4322
	if err := os.WriteFile(filepath.Join(d, mainline.ManifestFile), b.ManifestJSON(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := mainline.Reopen(src, planCopy(t, p)); err == nil || !strings.Contains(err.Error(), "manifest.json: sha256") {
		t.Errorf("a changed manifest.json on the drive: %v", err)
	}
}

// TestReopenRealBasePlanJSON: the same on the release's R23 base
// (RC_RELEASE_BASE: its manifest.json 3b2755c1…, boot-template.img and the
// unpacked rootfs.ext4; not in the repo): pass 1, the plan's payload through
// JSON, Reopen passes; a changed rootfs_zst in the plan's copy refuses.
func TestReopenRealBasePlanJSON(t *testing.T) {
	p, _ := realPayload(t, "RC_RELEASE_BASE")
	if p.Manifest.Schema != mainline.SchemaZst || p.Manifest.RootfsZst == nil {
		t.Fatalf("the release base is not schema 3: %+v", p.Manifest)
	}
	src, err := mainline.DirSource(os.Getenv("RC_RELEASE_BASE"))
	if err != nil {
		t.Fatal(err)
	}
	q, err := mainline.Reopen(src, planCopy(t, p))
	if err != nil {
		t.Fatalf("reopen of the release base against its plan.json payload: %v", err)
	}
	q.Close()
	t.Logf("release base %s (schema %d, rootfs_zst %s): reopened against its plan JSON", p.ManifestSHA256, p.Manifest.Schema, p.Manifest.RootfsZst.SHA256)
	w := planCopy(t, p)
	w.Manifest.RootfsZst.SHA256 = strings.Repeat("5", 64)
	if _, err := mainline.Reopen(src, w); err == nil || !strings.Contains(err.Error(), "manifest.json: ") {
		t.Fatalf("a changed rootfs_zst in the plan: %v", err)
	}
}

// TestReopenSameAsVerify: Reopen's copy of verify's plan path (reopen.go)
// gives the same result as verify(src, plan) on schema-2 bundles (no
// pointer in the manifest, so verify's compare is right there): the same
// payload JSON, or the same refusal text, for the plan's own bundle and for
// every change of TestReopen's list and more. The one text that differs:
// a manifest whose fields are not the plan's with the plan's sha256.
func TestReopenSameAsVerify(t *testing.T) {
	b := newBundle()
	d, err := b.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := verifyDir(t, d)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	type change struct {
		name string
		f    func(d string, w *mainline.Payload)
	}
	write := func(d, name string, data []byte) {
		if err := os.WriteFile(filepath.Join(d, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	flip := func(x []byte, i int) []byte { c := append([]byte(nil), x...); c[i] ^= 1; return c }
	cases := []change{
		{"none", func(string, *mainline.Payload) {}},
		{"plan manifest sha256", func(_ string, w *mainline.Payload) { w.ManifestSHA256 = strings.Repeat("0", 64) }},
		{"plan manifest version", func(_ string, w *mainline.Payload) { w.Manifest.Version += "x" }},
		{"plan manifest rootfs", func(_ string, w *mainline.Payload) { w.Manifest.Rootfs.Size++ }},
		{"plan with rootfs_zst", func(_ string, w *mainline.Payload) {
			w.Manifest.RootfsZst = &mainline.FileRef{File: mainline.RootfsZstFile, Size: 1, SHA256: strings.Repeat("4", 64)}
		}},
		{"plan template", func(_ string, w *mainline.Payload) { w.Template.Size++ }},
		{"plan template token", func(_ string, w *mainline.Payload) { w.Template.Token += "x" }},
		{"plan source", func(_ string, w *mainline.Payload) { w.Source = "folder /elsewhere" }},
		{"manifest file", func(d string, _ *mainline.Payload) { write(d, mainline.ManifestFile, append(b.ManifestJSON(), ' ')) }},
		{"manifest not json", func(d string, _ *mainline.Payload) { write(d, mainline.ManifestFile, []byte("{")) }},
		{"manifest empty", func(d string, _ *mainline.Payload) { write(d, mainline.ManifestFile, nil) }},
		{"template changed", func(d string, _ *mainline.Payload) {
			write(d, mainline.TemplateFile, flip(b.Template, len(b.Template)-1))
		}},
		{"template short", func(d string, _ *mainline.Payload) { write(d, mainline.TemplateFile, b.Template[:len(b.Template)-1]) }},
		{"rootfs same size", func(d string, _ *mainline.Payload) { write(d, mainline.RootfsFile, flip(b.Rootfs, len(b.Rootfs)-1)) }},
		{"rootfs size", func(d string, _ *mainline.Payload) {
			write(d, mainline.RootfsFile, append(b.Rootfs, make([]byte, 512)...))
		}},
		{"rootfs superblock", func(d string, _ *mainline.Payload) { write(d, mainline.RootfsFile, flip(b.Rootfs, 1024+0x38)) }},
		{"rootfs label", func(d string, _ *mainline.Payload) {
			c := append([]byte(nil), b.Rootfs...)
			copy(c[1024+0x78:1024+0x88], "other\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00")
			write(d, mainline.RootfsFile, c)
		}},
		{"rootfs blocks", func(d string, _ *mainline.Payload) { write(d, mainline.RootfsFile, flip(b.Rootfs, 1024+0x04)) }},
		{"extra file", func(d string, _ *mainline.Payload) { write(d, "x", nil) }},
		{"extra folder", func(d string, _ *mainline.Payload) { os.Mkdir(filepath.Join(d, "more"), 0o755) }},
		{"ignored files", func(d string, _ *mainline.Payload) {
			write(d, "._rootfs.ext4", []byte("m"))
			write(d, ".DS_Store", nil)
		}},
		{"two manifests", func(d string, _ *mainline.Payload) { write(d, "MANIFEST.JSON", b.ManifestJSON()) }},
		{"no template", func(d string, _ *mainline.Payload) { os.Remove(filepath.Join(d, mainline.TemplateFile)) }},
		{"no rootfs", func(d string, _ *mainline.Payload) { os.Remove(filepath.Join(d, mainline.RootfsFile)) }},
		{"rootfs a symlink", func(d string, _ *mainline.Payload) {
			p := filepath.Join(d, mainline.RootfsFile)
			os.Rename(p, p+".real")
			os.Symlink(p+".real", p)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d2, err := b.Write(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			w := planCopy(t, p) // the plan's payload, with this copy's source
			w.Source = "folder " + d2
			c.f(d2, w)
			s2, _ := mainline.DirSource(d2)
			v, verr := mainline.VerifyPlanPath(s2, w)
			r, rerr := mainline.Reopen(s2, w)
			switch {
			case (verr == nil) != (rerr == nil):
				t.Fatalf("verify %v, Reopen %v", verr, rerr)
			case verr != nil && verr.Error() != rerr.Error() &&
				// the one new text: a field change with the plan's sha256
				!(verr.Error() == "manifest.json: sha256 "+w.ManifestSHA256+", the plan's is "+w.ManifestSHA256 &&
					rerr.Error() == "manifest.json: sha256 "+w.ManifestSHA256+" as the plan's, but its fields are not the plan's"):
				t.Fatalf("verify %q, Reopen %q", verr, rerr)
			case verr == nil:
				if a, b := mustJSON(t, v), mustJSON(t, r); a != b || v.RootfsHashed() || r.RootfsHashed() || !r.Verified() ||
					!bytes.Equal(v.TemplateBytes(), r.TemplateBytes()) {
					t.Fatalf("payloads differ:\n%s\n%s", a, b)
				}
				v.Close()
				r.Close()
			}
			if c.name == "none" && rerr != nil {
				t.Fatalf("the plan's own bundle: %v", rerr)
			}
		})
	}
}
