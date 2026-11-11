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
	"uc-pxx-installer/internal/mainline/mltest"
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
		// review of 01b3841, finding 3: the other slot-side files of RULES.md
		"/usr/sbin/uc-pxx-update-boot":   "4de04b78b8a48a4cf4d79f22f18f2f69a6932a36398e4ba6ec0ac175483b3703",
		"/usr/local/lib/tsx/board.sh":    "15a610e7079ddad643bdacc0f744f348bef0ee965d2fb4e6d912e848f6e1ced2",
		"/usr/local/sbin/tsx-autoupdate": "11b50dd3a8fc2d40cf9a56339ee0dfd9af9ab3d4a112128ed454861c52eb92b0",
		"/usr/local/sbin/tsx-panelctl":   "fff41e69850f103b67d818cd321c2f880882f9908105ab25bbb8cc987eba7306",
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
	// one refusal: an unreviewed bundle's template and rootfs are not parsed
	// further (review of 01b3841, finding 1)
	r := mainline.EmbeddedGate().Check(p)
	if r.OK() || r.Gate != "embedded" || len(r.Checks) != 1 || r.Checks[0].Name != "payload-reviewed" || r.Files != nil {
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
	rev := []mainline.Reviewed{{Version: "t", ManifestSHA256: p.ManifestSHA256, TemplateSHA256: sum(b.Template), RootfsSHA256: sum(rf)}}
	if r := mainline.TestGate(rev, nil, pins).Check(p); r.Checks[1].Name != "boot-control-files" || !r.Checks[1].OK || len(r.Files) != 2 {
		t.Fatalf("%+v", r)
	}
	// not reviewed: the files are not read at all
	if r := mainline.TestGate(nil, nil, pins).Check(p); r.OK() || len(r.Checks) != 1 || r.Files != nil {
		t.Fatalf("an unreviewed bundle: %+v", r)
	}
	bad := append([]mainline.PinnedFile(nil), pins...)
	bad[0].SHA256 = strings.Repeat("0", 64)
	if r := mainline.TestGate(rev, nil, bad).Check(p); r.Checks[1].OK {
		t.Fatal("a changed pin passed")
	}
	missing := append(pins, mainline.PinnedFile{Path: "/usr/sbin/uc-pxx-post-update", SHA256: strings.Repeat("0", 64)})
	if r := mainline.TestGate(rev, nil, missing).Check(p); r.Checks[1].OK || !strings.Contains(r.Checks[1].Why, "uc-pxx-post-update") {
		t.Fatalf("a missing file passed: %+v", r.Checks[1])
	}
	// the release pins: these made-up files are not the RULES ones
	if r := mainline.EmbeddedGate().Check(p); r.OK() {
		t.Fatal("the embedded gate passed made-up files")
	}
}

// mkRootfs is an ext4 (mke2fs -d) of files (path -> content; a content
// "->T" is a symbolic link to T). Skipped without mke2fs.
func mkRootfs(t *testing.T, files map[string]string) []byte {
	t.Helper()
	mk := mke2fsPath()
	if mk == "" {
		t.Skip("mke2fs not installed")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "root")
	os.MkdirAll(src, 0o755)
	for p, c := range files {
		full := filepath.Join(src, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		if strings.HasPrefix(c, "->") {
			os.Symlink(c[2:], full)
		} else {
			os.WriteFile(full, []byte(c), 0o755)
		}
	}
	img := filepath.Join(dir, "rootfs.ext4")
	if out, err := exec.Command(mk, "-q", "-F", "-t", "ext4", "-b", "4096", "-L", mainline.RootfsLabel, "-d", src, img, "8M").CombinedOutput(); err != nil {
		t.Fatalf("mke2fs: %v\n%s", err, out)
	}
	b, _ := os.ReadFile(img)
	return b
}

// TestBootControlListAndAbsence (review of 01b3841, finding 3): a gate that
// pins uc-pxx-post-update also checks the list it runs sha256sum -c on; no
// file named uc-pxx-bootctl-inst may be anywhere in the tree.
func TestBootControlListAndAbsence(t *testing.T) {
	pu, bc := "#!/bin/sh\n# post-update\n", "made-up bootctl"
	pins := []mainline.PinnedFile{{Path: "/usr/sbin/uc-pxx-post-update", SHA256: sum([]byte(pu))}, {Path: "/usr/sbin/uc-pxx-bootctl", SHA256: sum([]byte(bc))}}
	list := sum([]byte(pu)) + "  /usr/sbin/uc-pxx-post-update\n" + sum([]byte(bc)) + " */usr/sbin/uc-pxx-bootctl\n" +
		strings.Repeat("7", 64) + "  /usr/sbin/not-pinned\n"
	base := map[string]string{"/usr/sbin/uc-pxx-post-update": pu, "/usr/sbin/uc-pxx-bootctl": bc,
		"/usr/share/uc-pxx/boot-control.sha256": list, "/etc/os-release": "->../usr/lib/os-release", "/usr/lib/os-release": "ID=alpine\n"}
	with := func(edit func(m map[string]string)) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		edit(m)
		return m
	}
	for _, c := range []struct {
		name  string
		files map[string]string
		why   string // "" = passes
	}{
		{"the list agrees", base, ""},
		{"no list", with(func(m map[string]string) { delete(m, "/usr/share/uc-pxx/boot-control.sha256") }), "boot-control.sha256: "},
		{"a pinned file with another sha256 in the list", with(func(m map[string]string) {
			m["/usr/share/uc-pxx/boot-control.sha256"] = strings.Replace(list, sum([]byte(bc)), strings.Repeat("0", 64), 1)
		}), "gives /usr/sbin/uc-pxx-bootctl sha256 0000"},
		{"the list does not name post-update", with(func(m map[string]string) {
			m["/usr/share/uc-pxx/boot-control.sha256"] = sum([]byte(bc)) + "  /usr/sbin/uc-pxx-bootctl\n"
		}), "does not name /usr/sbin/uc-pxx-post-update"},
		{"a line that is no sha256sum line", with(func(m map[string]string) {
			m["/usr/share/uc-pxx/boot-control.sha256"] = list + "garbage\n"
		}), "line 4 is not a sha256sum line"},
		{"bootctl-inst in /usr/libexec/uc-pxx-inst", with(func(m map[string]string) {
			m["/usr/libexec/uc-pxx-inst/uc-pxx-bootctl-inst"] = "x"
		}), "/usr/libexec/uc-pxx-inst/uc-pxx-bootctl-inst is in the tree"},
		{"bootctl-inst deep, under another suffix", with(func(m map[string]string) {
			m["/opt/a/b/c/uc-pxx-bootctl-inst-r2"] = "x"
		}), "/opt/a/b/c/uc-pxx-bootctl-inst-r2 is in the tree"},
		{"bootctl-inst as a symbolic link", with(func(m map[string]string) {
			m["/usr/bin/uc-pxx-bootctl-inst"] = "->/usr/sbin/uc-pxx-bootctl"
		}), "/usr/bin/uc-pxx-bootctl-inst is in the tree"},
		{"the step-2 USB transport in a slot-A payload", with(func(m map[string]string) {
			m["/usr/libexec/uc-pxx-step2/uc-pxx-xfer"] = "x"
		}), "/usr/libexec/uc-pxx-step2/uc-pxx-xfer is in the tree"},
		{"the transport elsewhere, renamed", with(func(m map[string]string) {
			m["/usr/bin/uc-pxx-xfer-r2"] = "x"
		}), "/usr/bin/uc-pxx-xfer-r2 is in the tree"},
	} {
		t.Run(c.name, func(t *testing.T) {
			rf := mkRootfs(t, c.files)
			b := newBundle()
			b.Rootfs = rf
			b.Sub("rootfs")["size"], b.Sub("rootfs")["sha256"] = len(rf), sum(rf)
			d, _ := b.Write(t.TempDir())
			p, err := verifyDir(t, d)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			rev := []mainline.Reviewed{{Version: "t", ManifestSHA256: p.ManifestSHA256, TemplateSHA256: sum(b.Template), RootfsSHA256: sum(rf)}}
			r := mainline.TestGate(rev, nil, pins).Check(p)
			ck := r.Checks[1]
			if c.why == "" && !ck.OK || c.why != "" && (ck.OK || !strings.Contains(ck.Why, c.why)) {
				t.Fatalf("%+v", ck)
			}
		})
	}
	// without a pin of post-update the list is not needed (a test gate of other files)
	rf := mkRootfs(t, map[string]string{"/usr/sbin/uc-pxx-bootctl": bc})
	b := newBundle()
	b.Rootfs = rf
	b.Sub("rootfs")["size"], b.Sub("rootfs")["sha256"] = len(rf), sum(rf)
	d, _ := b.Write(t.TempDir())
	p, err := verifyDir(t, d)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	rev := []mainline.Reviewed{{Version: "t", ManifestSHA256: p.ManifestSHA256, TemplateSHA256: sum(b.Template), RootfsSHA256: sum(rf)}}
	if r := mainline.TestGate(rev, nil, pins[1:]).Check(p); !r.Checks[1].OK {
		t.Fatalf("%+v", r.Checks[1])
	}
}

// TestRealRootfsPins reads the pins of the release gate in a real mainline
// root file system when UC_PXX_MAINLINE_ROOTFS names one (read-only): each
// pinned file, the list, the absence of bootctl-inst. It reports; it fails
// only when the file cannot be read as ext4.
func TestRealRootfsPins(t *testing.T) {
	img := os.Getenv("UC_PXX_MAINLINE_ROOTFS")
	if img == "" {
		t.Skip("UC_PXX_MAINLINE_ROOTFS not set")
	}
	r, err := mainline.CheckRootfsPins(img)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range r {
		t.Log(l)
	}
}

// TestRealPayloadGate: UC_PXX_MAINLINE_PAYLOAD=DIR (a real payload folder
// uc-pxx-mainline, read only) runs pass 1 and the release gate with the
// payload listed as reviewed (ReleaseGateListing), and reports every check
// and the root login facts of the root file system. It fails only when pass
// 1 refuses the payload.
func TestRealPayloadGate(t *testing.T) {
	dir := os.Getenv("UC_PXX_MAINLINE_PAYLOAD")
	if dir == "" {
		t.Skip("UC_PXX_MAINLINE_PAYLOAD not set")
	}
	src, err := mainline.DirSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := mainline.Verify(src)
	if err != nil {
		t.Fatalf("pass 1: %v", err)
	}
	defer p.Close()
	t.Logf("pass 1: %s (manifest %s, schema %d, cap %d, vbmeta_patched %s)", p.Pass1, p.ManifestSHA256, p.Manifest.Schema,
		p.Manifest.VbmetaBootCap, p.Manifest.VbmetaPatched.SHA256)
	r := mainline.ReleaseGateListing(p).Check(p)
	for _, c := range r.Checks {
		t.Logf("gate %-22s ok=%v: %s", c.Name, c.OK, c.Why)
	}
	t.Logf("gate OK: %v; Linux Image sha256 %s", r.OK(), r.ImageSHA256)
	for _, f := range r.Files {
		t.Logf("file %s %s", f.SHA256, f.Path)
	}
	for _, f := range []string{"/usr/local/lib/tsx/conf.sh", "/usr/local/share/tsx/defaults/autoupdate.conf"} {
		b, err := mainline.ReadRootfsFile(p, f)
		t.Logf("extra %s %s %v", mltest.SHA(b), f, err)
	}
	if b, err := mainline.ReadRootfsFile(p, "/root/.ssh/authorized_keys"); err != nil {
		t.Logf("login: no /root/.ssh/authorized_keys: %v", err)
	} else {
		t.Logf("login: /root/.ssh/authorized_keys sha256 %s, %d bytes, private key in it: %v", mltest.SHA(b), len(b),
			strings.Contains(string(b), "PRIVATE KEY"))
	}
	if b, err := mainline.ReadRootfsFile(p, "/etc/shadow"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if f := strings.SplitN(l, ":", 3); len(f) == 3 && f[0] == "root" {
				t.Logf("login: /etc/shadow root password field %q", f[1])
			}
		}
	}
	if b, err := mainline.ReadRootfsFile(p, "/etc/ssh/sshd_config"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "PermitRootLogin") || strings.HasPrefix(l, "PasswordAuthentication") {
				t.Logf("login: sshd_config %s", l)
			}
		}
	}
}

// TestXferOnlyInTheStep2Set (STEP2-USB-TRANSPORT §3.1, C4): an entry named
// uc-pxx-xfer* is refused in a normal both-slot payload, and in a STEP-2
// payload unless the step-2 set names it (each file with its sha256).
func TestXferOnlyInTheStep2Set(t *testing.T) {
	xf := "made-up uc-pxx-xfer"
	s2 := "made-up uc-pxx-step2"
	files := map[string]string{"/usr/libexec/uc-pxx-step2/uc-pxx-xfer": xf, "/usr/libexec/uc-pxx-step2/uc-pxx-step2": s2}
	rf := mkRootfs(t, files)
	b := newBundle()
	b.Rootfs = rf
	b.Sub("rootfs")["size"], b.Sub("rootfs")["sha256"] = len(rf), sum(rf)
	d, _ := b.Write(t.TempDir())
	p, err := verifyDir(t, d)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	setNo := []mainline.PinnedFile{{Path: "/usr/libexec/uc-pxx-step2/uc-pxx-step2", SHA256: sum([]byte(s2))}}
	setYes := append(append([]mainline.PinnedFile(nil), setNo...), mainline.PinnedFile{Path: "/usr/libexec/uc-pxx-step2/uc-pxx-xfer", SHA256: sum([]byte(xf))})
	setOther := append(append([]mainline.PinnedFile(nil), setNo...), mainline.PinnedFile{Path: "/usr/libexec/uc-pxx-step2/uc-pxx-xfer", SHA256: strings.Repeat("0", 64)})
	other := []string{"/usr/libexec/uc-pxx-step2"}
	for _, c := range []struct {
		name  string
		step2 bool
		set   []mainline.PinnedFile
		why   string
	}{
		{"a normal payload", false, setYes, "uc-pxx-xfer is in the tree"},
		{"a STEP-2 payload whose set does not name it", true, setNo, "uc-pxx-xfer is in the tree"},
		{"a STEP-2 payload whose set names another sha", true, setOther, "uc-pxx-xfer has sha256"},
		{"a STEP-2 payload whose set names it", true, setYes, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			rev := []mainline.Reviewed{{Version: "t", ManifestSHA256: p.ManifestSHA256, TemplateSHA256: sum(b.Template), RootfsSHA256: sum(rf),
				Slots: "ab", Step2: c.step2}}
			g := mainline.TestGate(rev, nil, []mainline.PinnedFile{}).WithBothSlot(mainline.BothSlot{Pins: []mainline.PinnedFile{},
				Step2: c.set, Step2Other: other})
			r := g.Check(p)
			var bad []string
			for _, ck := range r.Checks {
				if !ck.OK {
					bad = append(bad, ck.Name+": "+ck.Why)
				}
			}
			got := strings.Join(bad, "; ")
			if c.why == "" {
				if strings.Contains(got, "uc-pxx-xfer") {
					t.Fatalf("refused: %s", got)
				}
			} else if !strings.Contains(got, c.why) {
				t.Fatalf("want %q, got %s", c.why, got)
			}
		})
	}
}
