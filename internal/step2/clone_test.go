//go:build install && step2 && devtest

package step2_test

// The step-2 plan kind clone on a fake unit (DESIGN-R23 §4) with the real
// uc-pxx-bootctl-inst r2 (UC_PXX_BOOTCTL) and a real ext4 of B's root in
// system_b (mke2fs, UC_PXX_MKE2FS; e2fsck next to it): plan --clone, start,
// the restart into A, its mark, the finish; every stop before the arm (no
// done record, a done record changed after the plan, the difference that
// does not apply, e2fsck not clean, a content mismatch, a forbidden file
// left in A, the freeze that hits the guard); the power-cut sweep (a cut
// before every primitive write, clean and torn, inside the data steps and
// inside the write-back of the difference), each point checked with
// install.CheckCutState and resumed by a new boot of B.
//
// The mount of the difference is made up (no root in the test): Mount
// extracts the ext4 of the node into the folder (package ext4), Unmount
// makes an ext4 of the folder again (mke2fs -d) and writes it to the node.
// The difference itself (clonedelta.Apply), the content check (package ext4
// on system_a), e2fsck and r2 are the real ones.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/clone"
	"uc-pxx-installer/internal/clonedelta"
	"uc-pxx-installer/internal/ext4"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/mainline/mltest"
	"uc-pxx-installer/internal/plan"
	"uc-pxx-installer/internal/step2"
)

func e2tools(t testing.TB) (mk, fsck string) {
	mk = os.Getenv("UC_PXX_MKE2FS")
	if mk == "" {
		t.Skip("UC_PXX_MKE2FS not set: the clone tests make B's root file system with mke2fs")
	}
	if f := os.Getenv("UC_PXX_E2FSCK"); f != "" {
		return mk, f
	}
	b, err := os.ReadFile(mk)
	if err == nil && bytes.HasPrefix(b, []byte("#!")) && bytes.Contains(b, []byte("/sbin/mke2fs")) {
		// the e2fsprogs wrapper of run.sh: the same with e2fsck
		w := filepath.Join(t.TempDir(), "e2fsck-wrap")
		if err := os.WriteFile(w, bytes.ReplaceAll(b, []byte("/sbin/mke2fs"), []byte("/sbin/e2fsck")), 0o755); err != nil {
			t.Fatal(err)
		}
		return mk, w
	}
	if f := filepath.Join(filepath.Dir(mk), "e2fsck"); fileExists(f) {
		return mk, f
	}
	t.Skip("no e2fsck next to UC_PXX_MKE2FS (set UC_PXX_E2FSCK)")
	return
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// bTree is B's root file system after provisioning: the step-2 and
// provisioning sets, the per-slot state, the seed in /data, the logs.
func bTree(t testing.TB, r2 []byte, done []byte) string {
	d := t.TempDir()
	w := func(p string, b []byte, m os.FileMode) {
		t.Helper()
		f := filepath.Join(d, p)
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, b, m); err != nil {
			t.Fatal(err)
		}
		os.Chmod(f, m)
	}
	w("etc/tsx/build-id", []byte("0.2.0-rc10-eu-step2\n"), 0o644)
	w("usr/libexec/uc-pxx-step2/uc-pxx-step2", []byte("the step-2 binary (test)"), 0o700)
	w("usr/libexec/uc-pxx-step2/uc-pxx-bootctl-inst", r2, 0o700)
	w("etc/init.d/uc-pxx-step2", []byte("#!/sbin/openrc-run\n"), 0o755)
	// the base's conf (in ref_b): the clone, the consent over the cable
	w("etc/conf.d/uc-pxx-step2", []byte("UC_PXX_STEP2_KIND=clone\nUC_PXX_STEP2_USB=yes\n"), 0o644)
	w("etc/tsx/step2-bootctl-inst", []byte("r2 (end user)\n"), 0o644)
	w("usr/libexec/uc-pxx-provision/uc-pxx-provision", []byte("the provision binary (test)"), 0o700)
	w("usr/share/uc-pxx-provision/profiles.json", []byte("{}\n"), 0o644)
	w("var/lib/uc-pxx-provision/done", done, 0o644)
	w("var/log/messages", []byte("boot\n"), 0o644)
	w("data/tsx/panel.conf", []byte("SSH_AUTHORIZED_KEY=\"ssh-ed25519 AAAA test\"\n"), 0o600)
	w("var/lib/tsx/autoupdate/mode", []byte("manual\n"), 0o644)
	w("etc/machine-id", []byte("0123456789abcdef0123456789abcdef\n"), 0o444)
	w("etc/ssh/ssh_host_ed25519_key", []byte("host key\n"), 0o600)
	w("usr/bin/kiosk", bytes.Repeat([]byte("kiosk "), 3000), 0o755)
	w("usr/lib/libx.so.1.2", []byte("lib"), 0o755)
	os.Symlink("libx.so.1.2", filepath.Join(d, "usr/lib/libx.so.1"))
	os.MkdirAll(filepath.Join(d, "etc/runlevels/default"), 0o755)
	os.Symlink("/etc/init.d/uc-pxx-step2", filepath.Join(d, "etc/runlevels/default/uc-pxx-step2"))
	for _, p := range []string{"dev", "proc", "sys", "run", "tmp", "root"} {
		os.MkdirAll(filepath.Join(d, p), 0o755)
	}
	filepath.Walk(d, func(p string, fi os.FileInfo, err error) error {
		if err == nil && fi.IsDir() {
			os.Chmod(p, 0o755)
		}
		return nil
	})
	return d
}

// mkfs makes an ext4 of the tree of exactly size bytes.
func mkfs(t testing.TB, mk, tree string, size int64) []byte {
	t.Helper()
	img := filepath.Join(t.TempDir(), "fs.img")
	own := strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
	out, err := exec.Command(mk, "-q", "-F", "-t", "ext4", "-b", "1024", "-E", "root_owner="+own, "-d", tree, img,
		strconv.FormatInt(size/1024, 10)).CombinedOutput()
	if err != nil {
		t.Fatalf("mke2fs: %v\n%s", err, out)
	}
	b, err := os.ReadFile(img)
	if err != nil || int64(len(b)) != size {
		t.Fatalf("mke2fs gave %d bytes (%v), want %d", len(b), err, size)
	}
	return b
}

// manifestOf builds the content manifest of a tree.
func manifestOf(t testing.TB, dir string, g *clone.Gate) *clone.Result {
	t.Helper()
	x, err := clone.NewExcluder(g.Exclude)
	if err != nil {
		t.Fatal(err)
	}
	src, err := clone.NewOSSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	r, err := clone.Build(src, x, clone.Options{OneFS: true})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// copyMode copies a tree with its modes.
func copyMode(t testing.TB, src, dst string) {
	t.Helper()
	var dirs []string
	err := filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		to := filepath.Join(dst, rel)
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			l, _ := os.Readlink(p)
			return os.Symlink(l, to)
		case fi.IsDir():
			dirs = append(dirs, to)
			return os.MkdirAll(to, 0o755)
		default:
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if err := os.WriteFile(to, b, 0o600); err != nil {
				return err
			}
			return os.Chmod(to, fi.Mode())
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		fi, _ := os.Lstat(filepath.Join(src, strings.TrimPrefix(d, dst)))
		if fi != nil {
			os.Chmod(d, fi.Mode().Perm())
		}
	}
}

// fakeFS is the made-up mount of the difference (see the file comment).
type fakeFS struct {
	t      testing.TB
	mk     string
	fsck   string
	mu     sync.Mutex
	nodes  map[string]string // mount folder -> node
	cut    string            // "delta-mid": the write-back stops half way (a power cut)
	fsckRC int               // > 0: e2fsck says so (made up)
	mounts int
}

func (f *fakeFS) Mount(node, dir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	fh, err := os.Open(node)
	if err != nil {
		return err
	}
	defer fh.Close()
	fi, _ := fh.Stat()
	fs, err := ext4.Open(fh, fi.Size())
	if err != nil {
		return err
	}
	var dirs [][2]string
	var walk func(p string) error
	walk = func(p string) error {
		names, _, err := fs.ReadDirNoFollow(p, 1<<20)
		if err != nil {
			return err
		}
		for _, n := range names {
			c := path.Join(p, n)
			to := filepath.Join(dir, c)
			k, err := ext4.KindNoFollow(fs, c)
			if err != nil {
				return err
			}
			st, err := ext4.LstatNoFollow(fs, c)
			if err != nil {
				return err
			}
			switch k {
			case "d":
				if err := os.Mkdir(to, 0o755); err != nil {
					return err
				}
				dirs = append(dirs, [2]string{to, strconv.FormatUint(uint64(st.Perm), 8)})
				if err := walk(c); err != nil {
					return err
				}
			case "l":
				l, err := ext4.ReadlinkNoFollow(fs, c)
				if err != nil {
					return err
				}
				if err := os.Symlink(l, to); err != nil {
					return err
				}
			case "f":
				b, _, err := ext4.ReadFileNoFollow(fs, c, 64<<20)
				if err != nil {
					return err
				}
				if err := os.WriteFile(to, b, 0o600); err != nil {
					return err
				}
				m := os.FileMode(st.Perm & 0o777)
				if st.Perm&0o4000 != 0 {
					m |= os.ModeSetuid
				}
				os.Chmod(to, m)
			default:
				return fmt.Errorf("%s: type %s in the test tree", c, k)
			}
		}
		return nil
	}
	if err := walk("/"); err != nil {
		return err
	}
	if st, err := ext4.LstatNoFollow(fs, "/"); err == nil {
		os.Chmod(dir, os.FileMode(st.Perm&0o777)) // the mounted root has the file system's mode
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		m, _ := strconv.ParseUint(dirs[i][1], 8, 32)
		os.Chmod(dirs[i][0], os.FileMode(m))
	}
	os.RemoveAll(filepath.Join(dir, "lost+found"))
	if f.nodes == nil {
		f.nodes = map[string]string{}
	}
	f.nodes[dir] = node
	f.mounts++
	return nil
}

func (f *fakeFS) Unmount(dir string) error {
	f.mu.Lock()
	node := f.nodes[dir]
	delete(f.nodes, dir)
	f.mu.Unlock()
	fi, err := os.Stat(node)
	if err != nil {
		return err
	}
	img := mkfs(f.t, f.mk, dir, fi.Size())
	// the folder is empty again after the unmount
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && fi.IsDir() {
			os.Chmod(p, 0o755)
		}
		return nil
	})
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		os.RemoveAll(filepath.Join(dir, e.Name()))
	}
	fh, err := os.OpenFile(node, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer fh.Close()
	if f.cut == "delta-mid" {
		fh.WriteAt(img[:len(img)/2], 0)
		return errCut
	}
	_, err = fh.WriteAt(img, 0)
	return err
}

func (f *fakeFS) Fsck(node string) (int, string, error) {
	if f.fsckRC > 0 {
		return f.fsckRC, "made up: the file system has errors", nil
	}
	out, err := exec.Command(f.fsck, "-fn", node).CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), string(out), nil
	}
	return 0, string(out), err
}

// freezeRec is the made-up freeze of B's root.
type freezeRec struct {
	mu             sync.Mutex
	frozen         bool
	freezes, thaws int
	wroteFrozen    int
}

func (f *freezeRec) freeze() (func() error, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.frozen, f.freezes = true, f.freezes+1
	return func() error { f.mu.Lock(); f.frozen, f.thaws = false, f.thaws+1; f.mu.Unlock(); return nil }, nil
}

// out counts what the run writes to its output while frozen.
type frozenOut struct {
	f *freezeRec
	b *bytes.Buffer
}

func (o frozenOut) Write(p []byte) (int, error) {
	o.f.mu.Lock()
	if o.f.frozen {
		o.f.wroteFrozen++
	}
	o.f.mu.Unlock()
	return o.b.Write(p)
}

// cloneUnit is a unit whose B runs the provisioned base and whose A holds
// the old images.
type cloneUnit struct {
	*unit
	gate   *clone.Gate
	tree   string // B's root file system (also in system_b)
	refA   *clone.Result
	fs     *fakeFS
	fr     *freezeRec
	sysLen int64
}

func cloneTestGate(res []byte) *clone.Gate {
	g := clone.EmbeddedGate()
	g.Name = "test"
	pin := func(part string, b []byte) clone.ChainPin {
		return clone.ChainPin{Part: part, Size: int64(len(b)), SHA256: clone.SHA256(b)}
	}
	g.Chain = []clone.ChainPin{pin("uboot", fx.New["uboot"]), pin("trust", fx.New["trust"]), pin("dtbo", fx.New["dtbo"]),
		pin("vbmeta", patched()), pin("boot", mltest.Expected(tok, linux, 11, res))}
	g.FreezeBound = 20 * time.Second
	// the test does not run as root: the difference gives the test's ids
	d := *g.Delta
	d.Empty = append([]clone.Dir(nil), d.Empty...)
	d.Write = append([]clone.File(nil), d.Write...)
	for i := range d.Empty {
		d.Empty[i].UID, d.Empty[i].GID = uint32(os.Getuid()), uint32(os.Getgid())
	}
	for i := range d.Write {
		d.Write[i].UID, d.Write[i].GID = uint32(os.Getuid()), uint32(os.Getgid())
	}
	g.Delta, g.DeltaJSON = &d, mustJSON(&d)
	return g
}

const sysBytes = 6144 * 512

func newCloneUnit(t testing.TB, bin []byte, exe string, mod func(g *clone.Gate)) *cloneUnit {
	mk, fsck := e2tools(t)
	_, res := bundle()
	g := cloneTestGate(res)
	if mod != nil {
		mod(g)
	}
	// ref_b: the root of the test unit; ref_a: B's tree with the difference
	refB := "" // set below, after the root holds the tree
	bundleSHA := clone.SHA256([]byte("the kiosk-ha bundle (test)"))
	g.Profiles = []clone.Profile{{Name: "kiosk-ha", BuildID: "0.2.0-rc10-kiosk-ha\n", BundleSHA256: bundleSHA}}
	prov := []byte("the provision binary (test)")
	g.KeeperFiles = []clone.PathPin{{Path: clone.ProvisionBinary, Size: int64(len(prov)), SHA256: clone.SHA256(prov)}}
	doneOf := func(refB string) []byte {
		return []byte(fmt.Sprintf(`{"schema":1,"kind":"uc-pxx-provision-done","release":"0.2.0-rc10","profile":"kiosk-ha","bundle_sha256":%q,"ref_b_sha256":%q,"installed_sha256":%q}`,
			bundleSHA, refB, clone.SHA256([]byte("installed"))))
	}
	// the tree first with a placeholder record (the record is excluded from
	// the manifest: /var/lib/*), to learn ref_b
	tree := bTree(t, bin, doneOf(strings.Repeat("0", 64)))
	ab := [2][3]byte{{14, 0, 1}, {15, 0, 1}}
	fu := unitOf(res, ab, "b", "old")
	fu.Tweak["system_b"] = mkfs(t, mk, tree, sysBytes)
	u := newUnit(t, fu, "b", bin, exe)
	u.payload = ""
	copyMode(t, tree, u.root.Dir)
	refB = manifestOf(t, u.root.Dir, g).SHA256
	g.Profiles[0].RefB = refB
	done := doneOf(refB)
	os.WriteFile(u.root.Path(clone.DoneRecordPath), done, 0o644)
	os.WriteFile(filepath.Join(tree, strings.TrimPrefix(clone.DoneRecordPath, "/")), done, 0o644)
	// system_b holds the same tree (with the real record)
	fh, _ := os.OpenFile(u.root.Node("system_b"), os.O_WRONLY, 0)
	fh.WriteAt(mkfs(t, mk, tree, sysBytes), 0)
	fh.Close()
	ra := t.TempDir()
	copyMode(t, tree, ra)
	if err := clonedelta.Apply(ra, g.Delta, g.Profiles[0].BuildID); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(filepath.Join(ra, "lost+found"))
	refA := manifestOf(t, ra, g)
	g.Profiles[0].RefA = refA.SHA256
	cu := &cloneUnit{unit: u, gate: g, tree: tree, refA: refA, fs: &fakeFS{t: t, mk: mk, fsck: fsck}, fr: &freezeRec{}, sysLen: sysBytes}
	u.before = u.image()
	u.start, _ = u.root.ABSector()
	return cu
}

func (u *cloneUnit) opts(out *bytes.Buffer) step2.Options {
	o := u.unit.opts(out)
	o.Payload, o.Clone, o.CloneGate, o.CloneFS, o.Freezer = "", true, u.gate, u.fs, u.fr.freeze
	if out != nil {
		o.Out = frozenOut{u.fr, out}
	}
	return o
}

// startClone is plan --clone + start.
func (u *cloneUnit) startClone(o step2.Options) (*plan.OtherSlotPlan, *step2.Result, *bytes.Buffer, error) {
	var out bytes.Buffer
	o.Out = frozenOut{u.fr, &out}
	p, f, err := u.plan(o)
	if err != nil {
		return p, nil, &out, fmt.Errorf("plan: %w", err)
	}
	o.PlanFile = f
	r, err := step2.Start(u.env(), o)
	return p, r, &out, err
}

// cloneEnd: the finish in A, then A{15,0,1} B{14,0,1}; A's chain and system
// as planned (its content manifest = ref_a); B unchanged; userdata /data.
func (u *cloneUnit) cloneEnd(p *plan.OtherSlotPlan) {
	t := u.t
	u.boot(p.Target)
	u.mark(p.Target)
	var out bytes.Buffer
	o := u.opts(&out)
	o.DryRun = true
	fr, err := step2.Finish(u.env(), o)
	if err != nil {
		t.Fatalf("finish --dry-run: %v\n%s", err, out.String())
	}
	o.DryRun, o.ExpectID = false, fr.Plan.ID
	fr2, err := step2.Finish(u.env(), o)
	if err != nil || fr2.Switched || !fr2.Plan.Done {
		t.Fatalf("finish (K = b: done, no switch): %v %+v\n%s", err, fr2, out.String())
	}
	a := u.ab()
	if a.Validate() != nil || a.Slots[0] != (avbab.Slot{Priority: 15, Successful: 1}) || a.Slots[1] != (avbab.Slot{Priority: 14, Successful: 1}) || a.Pick() != 0 {
		t.Fatalf("end state %v %v", a.Slots[0], a.Slots[1])
	}
	img := u.image()
	part := func(name string) []byte {
		for _, e := range small.Entries {
			if e.Name == name {
				return img[e.Start*512 : (e.Start+e.Size)*512]
			}
		}
		t.Fatalf("no %s", name)
		return nil
	}
	for _, c := range u.gate.Chain {
		if !bytes.Equal(part(c.Part + "_a")[:c.Size], part(c.Part + "_b")[:c.Size]) {
			t.Fatalf("%s_a is not %s_b", c.Part, c.Part)
		}
	}
	es, err := clone.NewExt4Source(bytes.NewReader(part("system_a")), u.sysLen)
	if err != nil {
		t.Fatal(err)
	}
	x, _ := clone.NewExcluder(u.gate.Exclude)
	got, err := clone.Build(es, x, clone.Options{Full: true})
	if err != nil || got.SHA256 != u.refA.SHA256 {
		t.Fatalf("system_a is not ref_a: %v %v", err, clone.Diff(u.refA.Manifest, got.Manifest, 10))
	}
	for _, e := range small.Entries {
		if strings.HasSuffix(e.Name, "_b") || e.Name == "backup" {
			s, n := e.Start*512, e.Size*512
			if !bytes.Equal(img[s:s+n], u.before[s:s+n]) {
				t.Fatalf("%s changed", e.Name)
			}
		}
	}
	if got := u.userdata(); got != "ext4 label "+plan.DataLabel {
		t.Fatalf("userdata holds %s", got)
	}
}

func TestStep2CloneFull(t *testing.T) {
	bin, exe := bootctlBin(t)
	u := newCloneUnit(t, bin, exe, nil)
	p, r, out, err := u.startClone(u.opts(nil))
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if p.Kind != plan.CloneKind || p.Clone.AttemptKey != "clone "+u.refA.SHA256 || p.Clone.Profile != "kiosk-ha" {
		t.Fatalf("plan %s %+v", p.Kind, p.Clone)
	}
	if u.fr.freezes != 1 || u.fr.thaws != 1 || u.fr.wroteFrozen != 0 {
		t.Fatalf("the freeze: %+v (the run wrote to its output while frozen?)", u.fr)
	}
	if r.Clone == nil || r.Clone.Guard || r.Clone.Frozen <= 0 || !strings.Contains(out.String(), "was frozen for") {
		t.Fatalf("the clone result %+v\n%s", r.Clone, out.String())
	}
	t.Logf("frozen %s for the copy of %d bytes (made-up freeze, files as nodes)", r.Clone.Frozen, u.sysLen)
	if strings.Join(u.data.ops, " ") != "format migrate" || u.fs.mounts != 1 {
		t.Fatalf("data %v mounts %d", u.data.ops, u.fs.mounts)
	}
	st, _ := os.ReadFile(u.root.Path(step2.StateDir + "/" + step2.StateFile))
	if !strings.Contains(string(st), "started "+p.ID+" supervised=false") || !strings.Contains(string(st), "armed "+p.ID) {
		t.Fatalf("state %q", st)
	}
	a := u.ab()
	if a.Slots[0] != (avbab.Slot{Priority: 15, Tries: 7}) || a.Slots[1] != (avbab.Slot{Priority: 14, Successful: 1}) || a.Pick() != 0 {
		t.Fatalf("after start %v %v", a.Slots[0], a.Slots[1])
	}
	u.cloneEnd(p)
}

// TestStep2CloneStopsBeforeArm: every refusal or mismatch stops before the
// arm: A {0,0,0} (or as before), B boots and keeps everything.
func TestStep2CloneStopsBeforeArm(t *testing.T) {
	bin, exe := bootctlBin(t)
	cases := []struct {
		name, want string
		gate       func(g *clone.Gate)
		mod        func(u *cloneUnit, o *step2.Options)
		planFails  bool
	}{
		{name: "no done record", want: "clone-done", planFails: true, mod: func(u *cloneUnit, o *step2.Options) {
			os.Remove(u.root.Path(clone.DoneRecordPath))
		}},
		{name: "the done record changes after the plan", want: "make a new plan", mod: func(u *cloneUnit, o *step2.Options) {
			o.Hooks = nil
		}},
		{name: "e2fsck not clean", want: "is not clean", mod: func(u *cloneUnit, o *step2.Options) { u.fs.fsckRC = 4 }},
		{name: "a content mismatch", want: "content-check", gate: func(g *clone.Gate) {}, mod: func(u *cloneUnit, o *step2.Options) {
			u.gate.Profiles[0].RefA = clone.SHA256([]byte("another ref_a"))
		}},
		{name: "a forbidden file left in A", want: "must not be in this tree", gate: func(g *clone.Gate) {
			// a difference that keeps the step-2 folder (with r2); its ref_a
			// is made with it, so only the forbidden search finds it
			d := *g.Delta
			d.Remove = append([]string(nil), d.Remove[1:]...)
			g.Delta = &d
			g.DeltaJSON = mustJSON(&d)
		}},
		{name: "the difference does not apply", want: "the difference", mod: func(u *cloneUnit, o *step2.Options) {}},
		{name: "the freeze hits the guard", want: "the freeze reached its bound", mod: func(u *cloneUnit, o *step2.Options) {
			u.gate.FreezeBound = 200 * time.Millisecond
			o.Hooks = &install.Hooks{CutAfter: -1, Observe: func(ev install.Event) error {
				if ev.Partition == "system_a" {
					time.Sleep(15 * time.Millisecond)
				}
				return nil
			}}
		}},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			u := newCloneUnit(t, bin, exe, c.gate)
			if c.name == "the difference does not apply" {
				// B's tree with /var/log a link: remove_children refuses it
				os.RemoveAll(filepath.Join(u.tree, "var/log"))
				os.Symlink("/etc", filepath.Join(u.tree, "var/log"))
				fh, _ := os.OpenFile(u.root.Node("system_b"), os.O_WRONLY, 0)
				fh.WriteAt(mkfs(t, u.fs.mk, u.tree, sysBytes), 0)
				fh.Close()
				u.before = u.image()
			}
			o := u.opts(nil)
			if c.mod != nil {
				c.mod(u, &o)
			}
			var out bytes.Buffer
			o.Out = frozenOut{u.fr, &out}
			p, f, err := u.plan(o)
			if c.planFails {
				if err == nil || !strings.Contains(err.Error(), c.want) {
					t.Fatalf("the plan did not refuse with %q: %v", c.want, err)
				}
				if !bytes.Equal(u.image(), u.before) {
					t.Fatal("the disk changed")
				}
				return
			}
			if err != nil {
				t.Fatalf("plan: %v\n%s", err, out.String())
			}
			if c.name == "the done record changes after the plan" {
				b, _ := os.ReadFile(u.root.Path(clone.DoneRecordPath))
				os.WriteFile(u.root.Path(clone.DoneRecordPath), append(b, ' '), 0o644)
			}
			o.PlanFile = f
			_, err = step2.Start(u.env(), o)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want a stop with %q, got %v\n%s", c.want, err, out.String())
			}
			cs, cerr := install.CheckCutState(bytes.NewReader(u.image()), &p.Plan, u.start)
			a := u.ab()
			if cerr != nil || cs.Boots != "keeper" || a.Slots[0].Bootable() && a.Slots[0] != (avbab.Slot{Priority: 14, Successful: 1}) || !a.Slots[1].Keeper() || a.Pick() != 1 {
				t.Fatalf("after the stop: %v %+v A %v B %v", cerr, cs, a.Slots[0], a.Slots[1])
			}
			if len(u.data.ops) != 0 {
				t.Fatalf("data steps ran: %v", u.data.ops)
			}
			if u.fr.freezes != u.fr.thaws || u.fr.wroteFrozen != 0 {
				t.Fatalf("the freeze: %+v", u.fr)
			}
		})
	}
}

func mustJSON(d *clone.Delta) []byte {
	b, err := json.Marshal(d)
	if err != nil {
		panic(err)
	}
	return b
}

// TestStep2ClonePowerCutSweep: a cut before every primitive write of the
// clone (clean and torn), inside the data steps and inside the write-back
// of the difference: B stays the bootable keeper, A is never armed, and a
// new plan of the next boot of B runs to the end.
func TestStep2ClonePowerCutSweep(t *testing.T) {
	bin, exe := bootctlBin(t)
	u0 := newCloneUnit(t, bin, exe, nil)
	n := 0
	o := u0.opts(nil)
	o.Hooks = &install.Hooks{CutAfter: -1, Observe: func(install.Event) error { n++; return nil }}
	if _, _, out, err := u0.startClone(o); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	points := []string{}
	for k := 0; k < n; k++ {
		points = append(points, fmt.Sprintf("cut:%d", k), fmt.Sprintf("torn:%d", k))
	}
	points = append(points, "fs:delta-mid", "data:format-mid", "data:format-end", "data:migrate-mid", "data:migrate-end")
	if testing.Short() {
		var s []string
		for i, p := range points {
			if i%7 == 0 || i >= len(points)-6 {
				s = append(s, p)
			}
		}
		points = s
	}
	var total atomic.Int64
	t.Cleanup(func() {
		t.Logf("clone power-cut sweep: %d primitive writes, %d cut points, each checked and resumed", n, total.Load())
	})
	for _, pt := range points {
		pt := pt
		t.Run(pt, func(t *testing.T) {
			t.Parallel()
			total.Add(1)
			u := newCloneUnit(t, bin, exe, nil)
			o := u.opts(nil)
			kind, arg, _ := strings.Cut(pt, ":")
			switch kind {
			case "data":
				u.data.cut = arg
			case "fs":
				u.fs.cut = arg
			default:
				k, _ := strconv.Atoi(arg)
				o.Hooks = &install.Hooks{CutAfter: k, Torn: kind == "torn"}
			}
			p0, _, out, err := u.startClone(o)
			if err == nil || !(errors.Is(err, install.ErrPowerCut) || strings.Contains(err.Error(), errCut.Error())) {
				t.Fatalf("%s: the start did not stop at the cut: %v\n%s", pt, err, out.String())
			}
			cs, err := install.CheckCutState(bytes.NewReader(u.image()), &p0.Plan, u.start)
			if err != nil {
				t.Fatalf("%s: the cardinal rule: %v (%+v)", pt, err, cs)
			}
			if cs.Boots != "keeper" || cs.Target == "armed" {
				t.Fatalf("%s: a cut before the end boots the %s (target %s)", pt, cs.Boots, cs.Target)
			}
			// the power comes back: B boots again; a new plan resumes
			u.boot("b")
			u.data.cut, u.fs.cut, u.fr = "", "", &freezeRec{}
			p1, _, out, err := u.startClone(u.opts(nil))
			if err != nil {
				t.Fatalf("%s: the resume: %v\n%s", pt, err, out.String())
			}
			if p1.ID == p0.ID {
				t.Fatalf("%s: the resume ran the plan of the boot before", pt)
			}
			u.cloneEnd(p1)
		})
	}
}
