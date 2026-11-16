package clone_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uc-pxx-installer/internal/clone"
)

// TestReleaseHooks: the release build's data of the clone gate. The
// embedded gate-profiles.json is the RC-10 release's (mainline-rc-10 release
// chain: ci/r23-gate-data.py from the stage-1 refs.txt of uc-pxx-linux
// r23-base 8baacf1 and uc-pxx-xfer 62421c48): the three profiles of
// 0.2.0-rc10, and a gate with them and a keeper pin passes Gate.Check (a
// clone plan does not refuse for an empty gate); the output of
// ci/r23-gate-data.py (testdata, made from a made-up refs.txt) parses; the
// strict rules refuse a wrong build id, a console with a bundle, an unknown
// profile, a release without its uc-pxx-xfer pin, the keeper pin of another
// path.
func TestReleaseHooks(t *testing.T) {
	r := clone.Release()
	want := map[string][2]string{
		"console":  {"0fb66c50af125235d2b3390525bff356d8fa6a2a552129a0e668d7bedc5bd422", "37aebdf1381ba7b8a6f93d3083acd5b45b5fa326b66b9d091f442f560632a6f1"},
		"kiosk":    {"ba87bd54f5592b9c7651476363f2a34b2e15fc2cdaa3a430896090c253bf5c70", "5f6f8d46a0a1f38f0d3b961146b436bff813fb46daab4721b67e96d900c3f594"},
		"kiosk-ha": {"0cd36e9918b042036cbde573402edd0934bf5ad689a11a760430fa4aec15f9d0", "f467e7a0a6107d852a7e0dde77ccc003b890a98548c6aa16823b1df3969ad55a"},
	}
	if len(r.Profiles) != 3 || r.Release != "0.2.0-rc10" || r.XferSHA256 != "62421c487cf0233d1cee2844f623bf4de120a3ec7daa599bfb6f6465d6fe5d60" ||
		len(clone.EmbeddedGate().Profiles) != 3 {
		t.Fatalf("the embedded release data: %+v", r)
	}
	for _, p := range r.Profiles {
		if w, ok := want[p.Name]; !ok || p.RefA != w[0] || p.RefB != w[1] || p.BuildID != "0.2.0-rc10-"+p.Name+"\n" {
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
