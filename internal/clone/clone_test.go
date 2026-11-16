package clone_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"uc-pxx-installer/internal/clone"
)

// TestManifestFormat: a fixed tree gives exactly these lines (the shared
// vector of the format; the release build's contentmanifest.py must give
// the same bytes for the same tree).
func TestManifestFormat(t *testing.T) {
	d := t.TempDir()
	uid, gid := os.Getuid(), os.Getgid()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(d, "etc/tsx"), 0o755))
	must(os.WriteFile(filepath.Join(d, "etc/tsx/build-id"), []byte("0.2.0-rc10-kiosk-ha\n"), 0o644))
	must(os.Symlink("../usr/bin/sh x", filepath.Join(d, "etc/sh link")))
	must(os.MkdirAll(filepath.Join(d, "usr/bin"), 0o755))
	must(os.WriteFile(filepath.Join(d, "usr/bin/su"), []byte("su"), 0o755))
	must(os.Chmod(filepath.Join(d, "usr/bin/su"), 0o755|os.ModeSetuid))
	must(os.Link(filepath.Join(d, "usr/bin/su"), filepath.Join(d, "usr/bin/su2")))
	must(os.MkdirAll(filepath.Join(d, "var/log"), 0o755))
	must(os.WriteFile(filepath.Join(d, "var/log/messages"), []byte("boot\n"), 0o644))
	must(os.MkdirAll(filepath.Join(d, "var/lib/tsx/autoupdate"), 0o755))
	must(os.MkdirAll(filepath.Join(d, "var/lib/other"), 0o755))
	must(os.MkdirAll(filepath.Join(d, "usr/lib/py/__pycache__"), 0o755))
	must(os.WriteFile(filepath.Join(d, "usr/lib/py/__pycache__/a.pyc"), []byte("x"), 0o644))
	must(syscall.Mkfifo(filepath.Join(d, "etc/fifo"), 0o600))
	for _, p := range []string{".", "etc", "etc/tsx", "usr", "usr/bin", "var", "var/log", "var/lib", "var/lib/tsx", "usr/lib", "usr/lib/py"} {
		must(os.Chmod(filepath.Join(d, p), 0o755))
	}
	x, err := clone.NewExcluder([]string{"/var/log", "/var/lib/*", "!/var/lib/tsx", "**/__pycache__", "/etc/ss*"})
	must(err)
	src, err := clone.NewOSSource(d)
	must(err)
	r, err := clone.Build(src, x, clone.Options{OneFS: true})
	must(err)
	id := func(s string) string {
		return strings.NewReplacer("U", strconv.Itoa(uid), "G", strconv.Itoa(gid)).Replace(s)
	}
	want := id(`/ d 0755 U G 0 -
/etc d 0755 U G 0 -
/etc/fifo p 0600 U G 0 -
/etc/sh\x20link l 0777 U G 15 ../usr/bin/sh\x20x
/etc/tsx d 0755 U G 0 -
/etc/tsx/build-id f 0644 U G 20 0b6f8f4d0c8da0ff3b1fd2d18eb5de1aeb4e5fa3c67bc7f9cd5ec1ebfb8f6bd6
/usr d 0755 U G 0 -
/usr/bin d 0755 U G 0 -
/usr/bin/su f 4755 U G 2 3f3fe0da6c9c94d7b3d5a6e56c1fb1a1c2d4bd8ec2ce7dd8a0e4cbdff1d97c80
/usr/bin/su2 f 4755 U G 2 3f3fe0da6c9c94d7b3d5a6e56c1fb1a1c2d4bd8ec2ce7dd8a0e4cbdff1d97c80
/usr/lib d 0755 U G 0 -
/usr/lib/py d 0755 U G 0 -
/var d 0755 U G 0 -
/var/lib d 0755 U G 0 -
/var/lib/tsx d 0755 U G 0 -
`)
	// the sha256s of the two contents, computed independently
	want = strings.Replace(want, "0b6f8f4d0c8da0ff3b1fd2d18eb5de1aeb4e5fa3c67bc7f9cd5ec1ebfb8f6bd6", clone.SHA256([]byte("0.2.0-rc10-kiosk-ha\n")), 1)
	want = strings.ReplaceAll(want, "3f3fe0da6c9c94d7b3d5a6e56c1fb1a1c2d4bd8ec2ce7dd8a0e4cbdff1d97c80", clone.SHA256([]byte("su")))
	if string(r.Manifest) != want {
		t.Fatalf("manifest:\n%s\nwant:\n%s\ndiff: %v", r.Manifest, want, clone.Diff([]byte(want), r.Manifest, 10))
	}
	if r.SHA256 != clone.SHA256([]byte(want)) || r.Entries != 15 {
		t.Fatalf("sha256 %s, %d entries", r.SHA256, r.Entries)
	}
	// a forbidden file anywhere, also where the manifest leaves it out
	must(os.WriteFile(filepath.Join(d, "var/log/r2"), []byte("r2 bytes"), 0o644))
	_, err = clone.Build(src, x, clone.Options{Full: true, Forbidden: []clone.Pin{{What: "r2", Size: 8, SHA256: clone.SHA256([]byte("r2 bytes"))}}})
	if err == nil || !strings.Contains(err.Error(), "/var/log/r2 is r2") {
		t.Fatalf("the forbidden file was not found: %v", err)
	}
	// without Full the excluded folder is not entered
	if _, err := clone.Build(src, x, clone.Options{Forbidden: []clone.Pin{{What: "r2", Size: 8, SHA256: clone.SHA256([]byte("r2 bytes"))}}}); err != nil {
		t.Fatal(err)
	}
}

func TestExcluderRules(t *testing.T) {
	x, err := clone.NewExcluder([]string{"/dev", "/var/lib/*", "!/var/lib/tsx", "**/__pycache__", "/etc/ssh/ssh_host_*"})
	if err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]bool{
		"/dev": true, "/dev/x": true, "/devx": false, "/var/lib": false, "/var/lib/a": true, "/var/lib/tsx": false,
		"/var/lib/tsx/autoupdate": true, "/usr/__pycache__/a": true, "/usr/x__pycache__": false, "/etc/ssh/ssh_host_rsa_key": true,
		"/etc/ssh/sshd_config": false, "/etc/ssh": false,
	} {
		if got := x.Excluded(p); got != want {
			t.Errorf("%s: excluded %v, want %v", p, got, want)
		}
	}
	for _, bad := range []string{"var", "/a/../b", "/a*b/c", "**/a/b", "!rel", "/"} {
		if _, err := clone.NewExcluder([]string{bad}); err == nil {
			t.Errorf("rule %q taken", bad)
		}
	}
}

func TestDeltaAndDoneParse(t *testing.T) {
	g := clone.EmbeddedGate()
	if err := g.Check(); err != nil {
		t.Fatalf("the embedded gate: %v", err)
	}
	if g.DeltaSHA256() != clone.DeltaPin || len(g.Profiles) != 0 {
		t.Fatalf("the embedded gate: delta %s, %d profiles (the release build supplies them)", g.DeltaSHA256(), len(g.Profiles))
	}
	for _, bad := range []string{
		`{"schema":2}`,
		`{"schema":1,"remove":["rel"]}`,
		`{"schema":1,"remove":["/"]}`,
		`{"schema":1,"remove":["/a","/a/b"]}`,
		`{"schema":1,"write":[{"path":"/a","mode":"644","uid":0,"gid":0,"from":"build-id"}]}`,
		`{"schema":1,"write":[{"path":"/a","mode":"0644","uid":0,"gid":0,"from":"file"}]}`,
		`{"schema":1,"other":1}`,
		`{"schema":1}{}`,
	} {
		if _, err := clone.ParseDelta([]byte(bad)); err == nil {
			t.Errorf("delta %s taken", bad)
		}
	}
	ok := `{"schema":1,"kind":"uc-pxx-provision-done","release":"0.2.0-rc10","profile":"kiosk","bundle_sha256":"` +
		strings.Repeat("a", 64) + `","ref_b_sha256":"` + strings.Repeat("b", 64) + `","installed_sha256":"` + strings.Repeat("c", 64) + `"}`
	if r, err := clone.ParseDone([]byte(ok)); err != nil || r.Profile != "kiosk" {
		t.Fatalf("done: %v", err)
	}
	for _, bad := range []string{strings.Replace(ok, `"schema":1`, `"schema":2`, 1), strings.Replace(ok, "uc-pxx-provision-done", "x", 1),
		strings.Replace(ok, strings.Repeat("b", 64), "b", 1), ok + "{}", strings.Replace(ok, `"profile":"kiosk",`, `"profile":"kiosk","x":1,`, 1), ""} {
		if _, err := clone.ParseDone([]byte(bad)); err == nil {
			t.Errorf("done %s taken", bad)
		}
	}
}

// TestExt4SameAsTree: the content manifest of a tree read live (OSSource)
// and of an ext4 made of it by mke2fs -d (Ext4Source, package ext4) are the
// same bytes (UC_PXX_MKE2FS; skipped without it).
func TestExt4SameAsTree(t *testing.T) {
	mk := os.Getenv("UC_PXX_MKE2FS")
	if mk == "" {
		t.Skip("UC_PXX_MKE2FS not set")
	}
	d := t.TempDir()
	os.MkdirAll(filepath.Join(d, "a/b/c"), 0o755)
	os.WriteFile(filepath.Join(d, "a/b/c/f"), bytes.Repeat([]byte("data"), 5000), 0o640)
	os.WriteFile(filepath.Join(d, "a/empty"), nil, 0o600)
	os.Symlink("/very/"+strings.Repeat("long/", 20)+"target", filepath.Join(d, "a/long"))
	os.Symlink("short", filepath.Join(d, "a/short"))
	os.Link(filepath.Join(d, "a/b/c/f"), filepath.Join(d, "a/hard"))
	os.WriteFile(filepath.Join(d, "big"), bytes.Repeat([]byte{1, 2, 3}, 400000), 0o755)
	for i := 0; i < 300; i++ {
		os.WriteFile(filepath.Join(d, "a/b", "n"+strconv.Itoa(i)), []byte(strconv.Itoa(i)), 0o644)
	}
	os.Chmod(d, 0o755)
	x, _ := clone.NewExcluder([]string{"/lost+found"})
	src, _ := clone.NewOSSource(d)
	live, err := clone.Build(src, x, clone.Options{})
	if err != nil {
		t.Fatal(err)
	}
	img := filepath.Join(t.TempDir(), "fs.img")
	if out, err := exec.Command(mk, "-q", "-F", "-t", "ext4", "-b", "1024", "-E", "root_owner="+strconv.Itoa(os.Getuid())+":"+strconv.Itoa(os.Getgid()), "-d", d, img, "8192").CombinedOutput(); err != nil {
		t.Fatalf("mke2fs: %v\n%s", err, out)
	}
	f, err := os.Open(img)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fi, _ := f.Stat()
	es, err := clone.NewExt4Source(f, fi.Size())
	if err != nil {
		t.Fatal(err)
	}
	got, err := clone.Build(es, x, clone.Options{Full: true})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Manifest, live.Manifest) {
		t.Fatalf("ext4 and tree differ: %v", clone.Diff(live.Manifest, got.Manifest, 10))
	}
	t.Logf("%d entries, sha256 %s, the same from the tree and from the ext4", got.Entries, got.SHA256)
	// one byte of a file changed in the image: another manifest
	b, _ := os.ReadFile(img)
	i := bytes.Index(b, bytes.Repeat([]byte{1, 2, 3}, 1000))
	if i < 0 {
		t.Fatal("no data block")
	}
	b[i] ^= 1
	es2, _ := clone.NewExt4Source(bytes.NewReader(b), int64(len(b)))
	if got2, err := clone.Build(es2, x, clone.Options{}); err != nil || got2.SHA256 == got.SHA256 {
		t.Fatalf("a changed byte is not seen: %v", err)
	}
}

// TestExcluderBadRules (clone review MINOR-5): a malformed rule is an error,
// never a panic.
func TestExcluderBadRules(t *testing.T) {
	for _, r := range []string{"*", "x*", "", "/a/*/b", "**/", "!x"} {
		if _, err := clone.NewExcluder([]string{r}); err == nil {
			t.Errorf("rule %q taken", r)
		}
	}
}

// TestGateBuildIDAndKeeperFiles (R23-FORMATS §9.1, §9.2): the build id is
// "VERSION\n" exactly; a gate with a profile pins uc-pxx-provision.
func TestGateBuildIDAndKeeperFiles(t *testing.T) {
	for s, ok := range map[string]bool{"0.2.0-rc10-kiosk-ha\n": true, "0.2.0-rc10-kiosk-ha": false, "0.2.0-rc10-kiosk-ha\n\n": false,
		"0.2.0 rc10\n": false, "\n": false, "a\r\n": false} {
		if clone.BuildIDOK(s) != ok {
			t.Errorf("%q: %v", s, !ok)
		}
	}
	prov := clone.PathPin{Path: clone.ProvisionBinary, Size: 3, SHA256: clone.SHA256([]byte("abc"))}
	base := func() *clone.Gate {
		g := clone.EmbeddedGate()
		g.Name = "test"
		g.Profiles = []clone.Profile{{Name: "kiosk", BuildID: "0.2.0-rc10-kiosk\n", RefA: clone.SHA256([]byte("a")), RefB: clone.SHA256([]byte("b")),
			BundleSHA256: clone.SHA256([]byte("c"))}}
		g.KeeperFiles = []clone.PathPin{prov}
		return g
	}
	if err := base().Check(); err != nil {
		t.Fatalf("a good gate: %v", err)
	}
	g := base()
	g.Profiles[0].BuildID = "0.2.0-rc10-kiosk"
	if g.Check() == nil {
		t.Error("a build id without its newline taken")
	}
	g = base()
	g.KeeperFiles = nil
	if err := g.Check(); err == nil || !strings.Contains(err.Error(), clone.ProvisionBinary) {
		t.Errorf("no provision pin: %v", err)
	}
	if err := clone.EmbeddedGate().Check(); err != nil {
		t.Errorf("the embedded gate (no profile yet): %v", err)
	}
}

// TestExcludeListPin: the exclusion list that uc-pxx-linux copies
// (r23/content-exclude.txt) byte for byte (R23-FORMATS Part 3).
func TestExcludeListPin(t *testing.T) {
	g := clone.EmbeddedGate()
	if s := g.ExcludeSHA256(); s != "dcf1591e6bdface31d0a62754b4e4b8fdf64029a3cbfda96eb9c82acde043134" || len(g.Exclude) != 35 {
		t.Fatalf("the exclusion list: %d rules, sha256 %s", len(g.Exclude), s)
	}
	for _, p := range g.Exclude {
		if p == "/etc/tsx/autoupdate.conf" || p == "/etc/kiosk.conf" || strings.HasPrefix(p, "/etc/tsx/") && strings.HasSuffix(p, ".conf") {
			t.Fatalf("%s is excluded: the package defaults stay in the content check", p)
		}
	}
}
