package clone_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uc-pxx-installer/internal/clone"
)

// TestReleaseHooks: the release build's data of the clone gate. The
// embedded gate-profiles.json is the RC-10d release's (mainline-rc-10d release
// chain: ci/r23-gate-data.py from the stage-1 refs.txt 79ae8ca1 of uc-pxx-linux
// uc-pxx-rc10d-int and uc-pxx-xfer 3262ce29; release 0.2.0-rc10d, base
// version 0.2.0-rc10d-base, the RC-10d packages (uc-pxx-board 0.2.2, tsx-base
// 0.3.13_pre20261118), mode auto, finish list a1e2d88c, new bundles): the three profiles of 0.2.0-rc10d, and a gate with them and a keeper pin passes Gate.Check (a
// clone plan does not refuse for an empty gate); the output of
// ci/r23-gate-data.py (testdata, made from a made-up refs.txt) parses; the
// strict rules refuse a wrong build id, a console with a bundle, an unknown
// profile, a release without its uc-pxx-xfer pin, the keeper pin of another
// path.
func TestReleaseHooks(t *testing.T) {
	r := clone.Release()
	want := map[string][2]string{
		"console":  {"093e416b2bd15176ec8add4cffcfa33186e179f559f749d37fb742efc50721b8", "99cd4d13c463191c2f500a2fea608cbb357fc4c78274320d65e3122f7db947ea"},
		"kiosk":    {"83eceee6cc2391478e60f0a34923ab80f40a91f2ca5b5161872915de0be6fe33", "596ca4ea3253360ac6f110306e3fc3e031154f184d60ecb8839038e49755e8e5"},
		"kiosk-ha": {"51e67d03a445e6b6fb55b14fb1d8ae606cde65549c085924b38984a6453e1b69", "38c0fd09ebdb5f85caac6976f8a9372bf622cda13e9a7a7d8c0efd5bffd1ded0"},
	}
	if len(r.Profiles) != 3 || r.Release != "0.2.0-rc10d" || r.XferSHA256 != "3262ce2964e932dda2195d2466fae346cea490ce7e602822bdf676736b71fc75" ||
		len(clone.EmbeddedGate().Profiles) != 3 {
		t.Fatalf("the embedded release data: %+v", r)
	}
	for _, p := range r.Profiles {
		if w, ok := want[p.Name]; !ok || p.RefA != w[0] || p.RefB != w[1] || p.BuildID != "0.2.0-rc10d-"+p.Name+"\n" {
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
