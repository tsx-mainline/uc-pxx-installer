package mainline_test

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"uc-pxx-installer/internal/mainline"
)

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// TestEmbeddedGate: the release's gate lists no reviewed payload yet, and
// its boot-control pins are the RULES.md values. A new entry here is a
// reviewed change of this test too.
func TestEmbeddedGate(t *testing.T) {
	if n := mainline.EmbeddedReviewedCount(); n != 0 {
		t.Fatalf("%d reviewed mainline payloads: none is reviewed yet", n)
	}
	want := map[string]string{
		"/usr/sbin/uc-pxx-bootctl":     "38fea6d32412e7977ebc5845b138dc1c8c2df8555dae0b64b47f8a851b45e2d7",
		"/etc/uc-pxx-mark-boot.sh":     "b60c9ef3aa29a7d873e890875217e78ce9bb9826818265d7bf3e22152bb69a5d",
		"/etc/init.d/uc-pxx-boot-ok":   "15d7588c6f6dd9bea3bb8fbccf4e4d4c8986c7e20973f806c71ba530e78f59f4",
		"/usr/sbin/uc-pxx-flash-boot":  "400d218b6fb83943974d6dadbb62df20cb8f75c470d7cca0f1910e8d5b46878a",
		"/usr/sbin/uc-pxx-arm-slot":    "f5f60db87d32149ac10c83e63852b2f78019fd5140cd8991579df1e8ed4d8dcd",
		"/usr/sbin/uc-pxx-post-update": "a512a311af2ddfa8a08be74cb19c7aebee8d4de773f59e5c98885dc3924836f3",
	}
	pins := mainline.EmbeddedBootControlPins()
	if len(pins) != len(want) {
		t.Fatalf("%d pins", len(pins))
	}
	for _, p := range pins {
		if want[p.Path] != p.SHA256 {
			t.Fatalf("%s: %s", p.Path, p.SHA256)
		}
	}
	// it refuses a self-consistent bundle: not reviewed, no reviewed kernel, rootfs not an ext4 with the tools
	b := newBundle()
	d, _ := b.Write(t.TempDir())
	p, err := verifyDir(t, d)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	r := mainline.EmbeddedGate().Check(p)
	if r.OK() || r.Gate != "embedded" || len(r.Checks) != 3 {
		t.Fatalf("%+v", r)
	}
	for _, c := range r.Checks {
		if c.OK {
			t.Fatalf("%s passed", c.Name)
		}
	}
	var zero *mainline.Gate
	if zr := zero.Check(p); zr.OK() {
		t.Fatal("a nil gate passed")
	}
}

func TestTestGate(t *testing.T) {
	b := newBundle()
	d, _ := b.Write(t.TempDir())
	p, err := verifyDir(t, d)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	rev := mainline.Reviewed{Version: "t", ManifestSHA256: p.ManifestSHA256, TemplateSHA256: sum(b.Template), RootfsSHA256: sum(b.Rootfs)}
	if r := mainline.TestGate([]mainline.Reviewed{rev}, nil, nil).Check(p); !r.OK() || r.Reviewed == nil || len(r.Checks) != 1 {
		t.Fatalf("%+v", r)
	}
	for _, edit := range []func(*mainline.Reviewed){
		func(r *mainline.Reviewed) { r.ManifestSHA256 = strings.Repeat("0", 64) },
		func(r *mainline.Reviewed) { r.TemplateSHA256 = strings.Repeat("0", 64) },
		func(r *mainline.Reviewed) { r.RootfsSHA256 = strings.Repeat("0", 64) },
	} {
		e := rev
		edit(&e)
		if r := mainline.TestGate([]mainline.Reviewed{e}, nil, nil).Check(p); r.OK() {
			t.Fatal("a payload with one other sha passed")
		}
	}
	// a made-up template has no Image at 2 MiB: no reviewed kernel can match
	if r := mainline.TestGate([]mainline.Reviewed{rev}, map[string]string{mainline.ReviewedKernelRev: strings.Repeat("1", 64)}, nil).Check(p); r.OK() {
		t.Fatal("the kernel check passed without an Image")
	}
}

// TestLinuxImage: the Image of a template whose kernel part reaches past
// ImageOffset is the kernel part from there on.
func TestLinuxImage(t *testing.T) {
	k := make([]byte, mainline.ImageOffset+5000)
	for i := range k {
		k[i] = byte(i)
	}
	tmpl := append(make([]byte, mainline.PageSize), k...)
	img := mainline.LinuxImage(tmpl, mainline.TemplateInfo{KernelSize: int64(len(k))})
	if len(img) != 5000 || img[0] != byte(mainline.ImageOffset%256) {
		t.Fatalf("%d bytes", len(img))
	}
	if mainline.LinuxImage(tmpl[:4096], mainline.TemplateInfo{KernelSize: 2048}) != nil {
		t.Fatal("an Image in a short kernel part")
	}
}

// TestBootControlFiles: a real ext4 (mke2fs -d) with made-up boot-control
// files: the pins of a test gate must match each file; a missing or
// changed file refuses. Skipped without mke2fs.
func TestBootControlFiles(t *testing.T) {
	mk, err := exec.LookPath("mke2fs")
	if err != nil {
		for _, c := range []string{"/sbin/mke2fs", "/usr/sbin/mke2fs"} {
			if _, e := os.Stat(c); e == nil {
				mk, err = c, nil
			}
		}
	}
	if err != nil {
		t.Skip("mke2fs not installed")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "root")
	files := map[string][]byte{"/usr/sbin/uc-pxx-bootctl": []byte("made-up bootctl"), "/etc/uc-pxx-mark-boot.sh": []byte("#!/bin/sh\n")}
	var pins []mainline.PinnedFile
	for p, c := range files {
		os.MkdirAll(filepath.Join(src, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(src, p), c, 0o755)
		pins = append(pins, mainline.PinnedFile{Path: p, SHA256: sum(c)})
	}
	img := filepath.Join(dir, "rootfs.ext4")
	if out, err := exec.Command(mk, "-q", "-F", "-t", "ext4", "-b", "4096", "-L", mainline.RootfsLabel, "-d", src, img, "8M").CombinedOutput(); err != nil {
		t.Fatalf("mke2fs: %v\n%s", err, out)
	}
	rf, _ := os.ReadFile(img)
	if n := binary.LittleEndian.Uint32(rf[1024+4:]); int64(n)*4096 != int64(len(rf)) {
		t.Fatalf("mke2fs made %d blocks for %d bytes", n, len(rf))
	}
	b := newBundle()
	b.Rootfs = rf
	b.Sub("rootfs")["size"], b.Sub("rootfs")["sha256"] = len(rf), sum(rf)
	d, _ := b.Write(t.TempDir())
	p, err := verifyDir(t, d)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if r := mainline.TestGate(nil, nil, pins).Check(p); r.Checks[1].Name != "boot-control-files" || !r.Checks[1].OK || len(r.Files) != 2 {
		t.Fatalf("%+v", r)
	}
	bad := append([]mainline.PinnedFile(nil), pins...)
	bad[0].SHA256 = strings.Repeat("0", 64)
	if r := mainline.TestGate(nil, nil, bad).Check(p); r.Checks[1].OK {
		t.Fatal("a changed pin passed")
	}
	missing := append(pins, mainline.PinnedFile{Path: "/usr/sbin/uc-pxx-post-update", SHA256: strings.Repeat("0", 64)})
	if r := mainline.TestGate(nil, nil, missing).Check(p); r.Checks[1].OK || !strings.Contains(r.Checks[1].Why, "uc-pxx-post-update") {
		t.Fatalf("a missing file passed: %+v", r.Checks[1])
	}
	// the release pins: these made-up files are not the RULES ones
	if r := mainline.EmbeddedGate().Check(p); r.OK() {
		t.Fatal("the embedded gate passed made-up files")
	}
}
