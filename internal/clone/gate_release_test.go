package clone_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uc-pxx-installer/internal/clone"
)

// TestReleaseHooks: the release build's data of the clone gate. The
// embedded gate-profiles.json is empty until the release build fills it (no
// profile: every clone plan and every provisioning refuses); the output of
// ci/r23-gate-data.py (testdata, made from a made-up refs.txt) parses; the
// strict rules refuse a wrong build id, a console with a bundle, an unknown
// profile, a release without its uc-pxx-xfer pin, the keeper pin of another
// path.
func TestReleaseHooks(t *testing.T) {
	r := clone.Release()
	if len(r.Profiles) != 0 || r.Release != "" || r.XferSHA256 != "" || len(clone.EmbeddedGate().Profiles) != 0 {
		t.Fatalf("the embedded release data is not empty: %+v", r)
	}
	b, err := os.ReadFile(filepath.Join("testdata", "gate-profiles-sample.json"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := clone.ParseRelease(b)
	if err != nil || len(d.Profiles) != 3 || d.Release != "0.2.0-rc10" || !clone.IsSHA256(d.XferSHA256) {
		t.Fatalf("%+v %v", d, err)
	}
	g := clone.EmbeddedGate()
	g.Profiles = d.Profiles
	g.KeeperFiles = []clone.PathPin{{Path: clone.ProvisionBinary, Size: 4, SHA256: strings.Repeat("d", 64)}}
	if err := g.Check(); err != nil {
		t.Fatalf("a gate with the sample's profiles: %v", err)
	}
	for _, x := range []struct{ from, to string }{
		{`"0.2.0-rc10-kiosk\n"`, `"0.2.0-rc10-kiosk"`},
		{`"0.2.0-rc10-kiosk\n"`, `"0.2.0-rc11-kiosk\n"`},
		{`"bundle_sha256": "",`, `"bundle_sha256": "` + strings.Repeat("1", 64) + `",`},
		{`"name": "kiosk-ha"`, `"name": "ha"`},
		{`"xfer_sha256": "127880a76de95de38af7504cc35e53b0dc53d9c4b233c51cf9d213b403b91a3c"`, `"xfer_sha256": ""`},
		{`"schema": 1`, `"schema": 2`},
		{`"release": "0.2.0-rc10"`, `"release": "0.2.0-rc10", "x": 1`},
	} {
		if !strings.Contains(string(b), x.from) {
			t.Fatalf("the sample has no %q", x.from)
		}
		if _, err := clone.ParseRelease([]byte(strings.Replace(string(b), x.from, x.to, 1))); err == nil {
			t.Fatalf("taken with %q", x.to)
		}
	}
	k, err := os.ReadFile(filepath.Join("testdata", "gate-keeper-sample.json"))
	if err != nil {
		t.Fatal(err)
	}
	l, err := clone.ParseKeeper(k)
	if err != nil || len(l) != 1 || l[0].Path != clone.ProvisionBinary || l[0].Size != 4 {
		t.Fatalf("%+v %v", l, err)
	}
	if _, err := clone.ParseKeeper([]byte(strings.Replace(string(k), "/usr/libexec/uc-pxx-provision/uc-pxx-provision", "/usr/bin/x", 1))); err == nil {
		t.Fatal("a keeper pin of another path")
	}
	if l, err := clone.ParseKeeper([]byte(`{"keeper_files": [], "kind": "uc-pxx-clone-gate-keeper", "schema": 1}`)); err != nil || len(l) != 0 {
		t.Fatalf("the empty keeper data: %v", err)
	}
}

// TestLiveContent: the shared content check is clone.Build of the tree with
// the list, one file system.
func TestLiveContent(t *testing.T) {
	d := t.TempDir()
	os.MkdirAll(filepath.Join(d, "etc"), 0o755)
	os.WriteFile(filepath.Join(d, "etc/a"), []byte("a"), 0o644)
	os.MkdirAll(filepath.Join(d, "var/log"), 0o755)
	os.WriteFile(filepath.Join(d, "var/log/x"), []byte("x"), 0o644)
	r, err := clone.LiveContent(d, []string{"/var/log"})
	if err != nil {
		t.Fatal(err)
	}
	x, _ := clone.NewExcluder([]string{"/var/log"})
	src, _ := clone.NewOSSource(d)
	r2, err := clone.Build(src, x, clone.Options{OneFS: true})
	if err != nil || r.SHA256 != r2.SHA256 || r.Entries != 4 {
		t.Fatalf("%+v %+v %v", r, r2, err)
	}
	if _, err := clone.LiveContent(d, []string{"*"}); err == nil {
		t.Fatal("a bad list")
	}
}
