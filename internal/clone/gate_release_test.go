package clone_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uc-pxx-installer/internal/clone"
)

// TestReleaseHooks: the release build's data of the clone gate. The
// embedded gate-profiles.json is the RC-10c release's (mainline-rc-10c release
// chain: ci/r23-gate-data.py from the stage-1 refs.txt cac85a19 of uc-pxx-linux
// uc-pxx-rc10c-int 979c4b0 and uc-pxx-xfer 3262ce29; release 0.2.0-rc10c, base
// version 0.2.0-rc10c-base, the RC-10c packages and finish list a1e2d88c, new
// bundles): the three profiles of 0.2.0-rc10c, and a gate with them and a keeper pin passes Gate.Check (a
// clone plan does not refuse for an empty gate); the output of
// ci/r23-gate-data.py (testdata, made from a made-up refs.txt) parses; the
// strict rules refuse a wrong build id, a console with a bundle, an unknown
// profile, a release without its uc-pxx-xfer pin, the keeper pin of another
// path.
func TestReleaseHooks(t *testing.T) {
	r := clone.Release()
	want := map[string][2]string{
		"console":  {"22c7448f6dbd335b1d3ade3f8e01c22f94c314049f5ad4ea3ac5d200b47efebf", "a57d15caa66e2751e876a0244849c64c0d0fd3bb012f2499c4587e6d52a6e964"},
		"kiosk":    {"bf443d556f74121bdcae9f605083c56768b60c4b2b798868a4a6854ae15eb55a", "ad8885d938f64c998c96f12f23e0e5aa2c2847049ee87cc475d3e6f10f466d94"},
		"kiosk-ha": {"46954d4ba6b058b24d8b33f6dbc9e80ae8d44b3e787579ad30f04f9dee294c8f", "382de994768a6eaa50d28da27dcda9f276148a05dab6ae097bc9947a437d43a2"},
	}
	if len(r.Profiles) != 3 || r.Release != "0.2.0-rc10c" || r.XferSHA256 != "3262ce2964e932dda2195d2466fae346cea490ce7e602822bdf676736b71fc75" ||
		len(clone.EmbeddedGate().Profiles) != 3 {
		t.Fatalf("the embedded release data: %+v", r)
	}
	for _, p := range r.Profiles {
		if w, ok := want[p.Name]; !ok || p.RefA != w[0] || p.RefB != w[1] || p.BuildID != "0.2.0-rc10c-"+p.Name+"\n" {
			t.Fatalf("the embedded profile %+v", p)
		}
	}
	if eg := clone.EmbeddedGate(); eg.Check() == nil {
		t.Fatal("the embedded gate without the keeper pin passes Gate.Check")
	} else if eg.KeeperFiles = []clone.PathPin{{Path: clone.ProvisionBinary, Size: 4, SHA256: strings.Repeat("e", 64)}}; eg.Check() != nil {
		t.Fatalf("the embedded gate with a keeper pin: %v", eg.Check())
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
