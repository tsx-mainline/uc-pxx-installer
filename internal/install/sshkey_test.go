package install_test

// mainline-rc-8 (owner 2026-11-13, D1/D2): the direct path seeds the person's
// ssh key into slot B after the verify and before the arm. Here with a real
// ext4 root file system (mke2fs, UC_PXX_MKE2FS) and a test seeder in place of
// the read-write mount (no root in a unit test; the mount itself is tested as
// root in ci/seed-root-test.sh): the plan's step and record, the order
// (verify -> ssh-key -> arm), the refusals before any write (no key, another
// key, a medium that cannot seed, a payload with panel.conf), and the power
// cut sweep with cuts and torn cuts in the seed: the keeper A boots at every
// cut and a new plan resumes to the end state with the key.

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/ext4"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/keywrite"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/mainline/mltest"
	"uc-pxx-installer/internal/plan"
	"uc-pxx-installer/internal/sshkey"
)

const testKeyLine = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBbrhXWbO19v2SgtcPAMqpGOt8V5hUqtgQyUCJxvGtTp test-ed25519\n"

func testKey(t testing.TB) *sshkey.Key {
	k, err := sshkey.Parse([]byte(testKeyLine))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// mkfs makes an ext4 (1 KiB blocks, no journal) of the tree; root lists the
// paths whose inodes the test makes root's (a unit test runs as a user).
func mkfs(t testing.TB, tree string, kib int, root ...string) []byte {
	t.Helper()
	mk := os.Getenv("UC_PXX_MKE2FS")
	if mk == "" {
		t.Skip("UC_PXX_MKE2FS not set: this test makes a root file system with mke2fs")
	}
	img := filepath.Join(t.TempDir(), "root.img")
	out, err := exec.Command(mk, "-q", "-F", "-t", "ext4", "-b", "1024", "-O", "^has_journal,^metadata_csum", "-E", "root_owner=0:0",
		"-U", "0b5d3a1e-0000-4000-8000-000000000001", "-L", mainline.RootfsLabel, "-d", tree, img, fmt.Sprint(kib)).CombinedOutput()
	if err != nil {
		t.Fatalf("mke2fs: %v %s", err, out)
	}
	b, err := os.ReadFile(img)
	if err != nil {
		t.Fatal(err)
	}
	fs, err := ext4.Open(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range root {
		off, err := ext4.InodeOffset(fs, p)
		if err != nil {
			t.Fatal(err)
		}
		le := binary.LittleEndian
		le.PutUint16(b[off+0x2:], 0)
		le.PutUint16(b[off+0x18:], 0)
		le.PutUint16(b[off+0x78:], 0)
		le.PutUint16(b[off+0x7A:], 0)
	}
	return b
}

// rootTree is a small root file system tree with /data (and /data/tsx).
func rootTree(t testing.TB, tsx bool, extra map[string]string) string {
	d := t.TempDir()
	os.MkdirAll(filepath.Join(d, "etc"), 0o755)
	os.WriteFile(filepath.Join(d, "etc/hostname"), []byte("uc-pxx\n"), 0o644)
	os.MkdirAll(filepath.Join(d, "data"), 0o755)
	if extra["-no-setup-rule"] == "" {
		os.MkdirAll(filepath.Join(d, "usr/local/lib/tsx"), 0o755)
		os.WriteFile(filepath.Join(d, "usr/local/lib/tsx/setup-state.sh"), []byte("# the setup-done rule\n"), 0o644)
	}
	delete(extra, "-no-setup-rule")
	if tsx {
		os.MkdirAll(filepath.Join(d, "data/tsx"), 0o700)
		os.Chmod(filepath.Join(d, "data/tsx"), 0o700)
	}
	for p, c := range extra {
		os.MkdirAll(filepath.Dir(filepath.Join(d, p)), 0o700)
		os.WriteFile(filepath.Join(d, p), []byte(c), 0o600)
	}
	return d
}

const rootKiB = 1024

// keyRig is a direct rig whose payload has a real ext4 root file system.
func keyRig(t *testing.T, tsx bool, extra map[string]string) (*dRig, string) {
	r := newDRig(t, dUnit)
	tree := rootTree(t, tsx, extra)
	rf := mkfs(t, tree, rootKiB)
	r.bundle.Rootfs = rf
	r.bundle.Manifest["rootfs"] = map[string]any{"file": mainline.RootfsFile, "size": len(rf), "sha256": mltest.SHA(rf), "label": mainline.RootfsLabel}
	r.ml = mlPayloadOf(t, r.bundle)
	return r, tree
}

// fakeSeeder stands for the read-write mount: it writes the image that the
// mount gives (an ext4 of the tree with the seed's files, made root's) into
// the range. A torn cut (stop) writes half of it, then stops at the rename.
func fakeSeeder(t *testing.T, tree string) func(path string, off, length int64, files []keywrite.File, there bool, stop keywrite.Hook) (bool, error) {
	return func(path string, off, length int64, files []keywrite.File, there bool, stop keywrite.Hook) (bool, error) {
		seeded := t.TempDir()
		cp := exec.Command("cp", "-a", tree+"/.", seeded)
		if out, err := cp.CombinedOutput(); err != nil {
			return false, fmt.Errorf("cp: %v %s", err, out)
		}
		os.MkdirAll(filepath.Join(seeded, "data/tsx"), 0o700)
		os.Chmod(filepath.Join(seeded, "data/tsx"), 0o700)
		root := []string{"/data/tsx"}
		for _, f := range files {
			os.WriteFile(filepath.Join(seeded, f.Path), f.Data, os.FileMode(f.Mode))
			os.Chmod(filepath.Join(seeded, f.Path), os.FileMode(f.Mode))
			root = append(root, f.Path)
		}
		img := mkfs(t, seeded, rootKiB, root...)
		if int64(len(img)) != length {
			return false, fmt.Errorf("the seeded image has %d bytes, the range %d", len(img), length)
		}
		fh, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			return false, err
		}
		defer fh.Close()
		if stop != nil {
			fh.WriteAt(img[:len(img)/2], off)
			if err := stop(keywrite.StageRename, files[0].Path); err != nil {
				return false, err
			}
		}
		_, err = fh.WriteAt(img, off)
		return !there, err
	}
}

func withSeeder(t *testing.T, f func(string, int64, int64, []keywrite.File, bool, keywrite.Hook) (bool, error)) {
	old := install.ImageSeeder
	install.ImageSeeder = f
	t.Cleanup(func() { install.ImageSeeder = old })
}

func (r *dRig) makeKeyPlan(im *install.Image, k *sshkey.Key) (*plan.DirectPlan, error) {
	r.key = k
	return r.makePlan(im)
}

// keyDone: as dDone, but system_b holds the root file system with the seed
// (keywrite.Check), not the image byte for byte.
func (r *dRig) keyDone(p *plan.DirectPlan, k *sshkey.Key) {
	t := r.t
	t.Helper()
	after, err := os.ReadFile(r.path)
	if err != nil {
		t.Fatal(err)
	}
	if !sameOutside(after, r.before, &p.Plan) {
		t.Fatal("a byte outside B's write ranges, the key-record sectors and the A/B sector changed")
	}
	a, _ := avbab.Parse(after[plan.ABLBA*512:])
	if a.Validate() != nil || a.Pick() != 1 || a.Slots[1] != (avbab.Slot{Priority: 15, Tries: 7}) || a.Slots[0] != (avbab.Slot{Priority: 14, Successful: 1}) {
		t.Fatalf("final A/B %v %v", a.Slots[0], a.Slots[1])
	}
	tb, _ := gpt.Parse(bytes.NewReader(after), int64(len(after)))
	e, _ := tb.Find("system_b")
	sys := after[e.Start*512 : e.Start*512+int64(len(r.bundle.Rootfs))]
	if err := keywrite.Check(bytes.NewReader(sys), int64(len(sys)), keywrite.Seed(k), true); err != nil {
		t.Fatalf("system_b after the install: %v", err)
	}
	fs, _ := ext4.Open(bytes.NewReader(sys), int64(len(sys)))
	if b, _ := fs.ReadFile("/etc/hostname", 100); string(b) != "uc-pxx\n" {
		t.Fatalf("system_b's other files: %q", b)
	}
}

func TestDirectSSHKeyPlanAndRun(t *testing.T) {
	r, tree := keyRig(t, false, nil)
	withSeeder(t, fakeSeeder(t, tree))
	k := testKey(t)
	im := r.open()
	defer im.Close()
	p, err := r.makeKeyPlan(im, k)
	if err != nil {
		var sb strings.Builder
		p.Render(&sb)
		t.Fatalf("%v\n%s", err, sb.String())
	}
	var ops []string
	var ks *plan.SSHKeyStep
	for _, s := range p.Steps {
		ops = append(ops, s.Op)
		if s.Op == "ssh-key" {
			ks = s.SSHKey
		}
	}
	tail := strings.Join(ops[len(ops)-5:], " ")
	if tail != "sync verify ssh-key bootctl check-ab" || ks == nil || p.SSHKey == nil || p.SSHKey.Fingerprint != k.Fingerprint ||
		ks.Fingerprint != k.Fingerprint || !ks.FolderMade || ks.Partition != "system_b" || len(ks.Files) != 2 ||
		ks.Files[0].Path != "/data/tsx/panel.conf" || ks.Files[0].Mode != 0o600 || ks.Files[1].Path != "/data/tsx/setup-v2" ||
		ks.Files[0].SHA256 != mltest.SHA(k.PanelConf()) || ks.RootfsSHA256 != mltest.SHA(r.bundle.Rootfs) {
		t.Fatalf("steps %v, key step %+v, plan key %+v", ops, ks, p.SSHKey)
	}
	if err := install.DirectStructure(p); err != nil {
		t.Fatalf("structure: %v", err)
	}
	// the plan has the fingerprint and the files' sha256, never the key
	if pj := planJSON(t, p); strings.Contains(pj, "AAAAC3NzaC1lZDI1NTE5AAAAI") {
		t.Fatal("the plan JSON holds the key's text")
	}
	j := &journal{}
	o := r.opts(install.Options{Hooks: &install.Hooks{CutAfter: -1, Observe: j.observe}})
	o.SSHKey = k
	if _, err := install.ExecuteMainlineDirect(p, r.ml, im, o); err != nil {
		t.Fatal(err)
	}
	im.Close()
	r.keyDone(p, k)
	var evs []string
	for _, e := range j.ev {
		if len(evs) == 0 || evs[len(evs)-1] != e.Op {
			evs = append(evs, e.Op)
		}
	}
	if strings.Join(evs, " ") != "key ab data ssh-key ab" {
		t.Fatalf("write order %v", evs)
	}
}

func TestDirectSSHKeyRefusals(t *testing.T) {
	k := testKey(t)
	other, err := sshkey.Parse([]byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMcC4tUC7gadx+uXZrB0t3iBDXIMQAdId8iEmLb+8Z5N\n"))
	if err != nil {
		t.Fatal("the second test key: " + err.Error())
	}
	for _, c := range []struct {
		name    string
		key     *sshkey.Key // the run's key
		seeder  bool
		want    string
		planKey *sshkey.Key
	}{
		{"no key in the run", nil, true, "this run has no key", k},
		{"another key in the run", other, true, "this run has its key", k},
		{"a medium that cannot seed", k, false, "cannot seed", k},
		{"a key for a plan without the step", k, true, "no ssh-key step", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, tree := keyRig(t, false, nil)
			if c.seeder {
				withSeeder(t, fakeSeeder(t, tree))
			} else {
				withSeeder(t, nil)
			}
			im := r.open()
			defer im.Close()
			p, err := r.makeKeyPlan(im, c.planKey)
			if err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(r.path)
			o := r.opts(install.Options{})
			o.SSHKey = c.key
			_, err = install.ExecuteMainlineDirect(p, r.ml, im, o)
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "nothing was written") {
				t.Fatalf("err %v, want %q", err, c.want)
			}
			if after, _ := os.ReadFile(r.path); !bytes.Equal(before, after) {
				t.Fatal("a refusal wrote to the image")
			}
		})
	}
	t.Run("a payload without the setup-done rule (an older tsx-linux-common): the plan refuses", func(t *testing.T) {
		r, _ := keyRig(t, false, map[string]string{"-no-setup-rule": "1"})
		im := r.open()
		defer im.Close()
		p, err := r.makeKeyPlan(im, k)
		if err == nil || !strings.Contains(err.Error(), "ssh-key-rootfs") || len(p.Steps) != 0 {
			t.Fatalf("%v", err)
		}
	})
	t.Run("a payload whose root file system has panel.conf already: the plan refuses", func(t *testing.T) {
		r, _ := keyRig(t, true, map[string]string{"data/tsx/panel.conf": "TZ_NAME=\"UTC\"\n"})
		im := r.open()
		defer im.Close()
		p, err := r.makeKeyPlan(im, k)
		if err == nil || !strings.Contains(err.Error(), "ssh-key-rootfs") || len(p.Steps) != 0 {
			t.Fatalf("%v", err)
		}
	})
}

// The cut sweep with the seed: every primitive write, and the seed clean and
// torn (half of the seeded image, cut at the first rename).
func TestDirectSSHKeyCutSweep(t *testing.T) {
	k := testKey(t)
	r0, tree := keyRig(t, false, nil)
	withSeeder(t, fakeSeeder(t, tree))
	im := r0.open()
	p0, err := r0.makeKeyPlan(im, k)
	if err != nil {
		t.Fatal(err)
	}
	j := &journal{}
	o := r0.opts(install.Options{MaxWrite: sweepWrite, Hooks: &install.Hooks{CutAfter: -1, Observe: j.observe}})
	o.SSHKey = k
	if _, err := install.ExecuteMainlineDirect(p0, r0.ml, im, o); err != nil {
		t.Fatal(err)
	}
	im.Close()
	n := len(j.ev)
	seedAt := -1
	for i, e := range j.ev {
		if e.Op == "ssh-key" {
			seedAt = i
		}
	}
	if seedAt < 0 {
		t.Fatal("no seed event")
	}
	pts := map[int]bool{0: true, n: true, seedAt: true, seedAt - 1: true, seedAt + 1: true}
	for _, c := range sweepPoints(n, j.ev) {
		pts[c] = true
	}
	checked := 0
	for c := range pts {
		for _, torn := range []bool{false, true} {
			c, torn := c, torn
			t.Run(fmt.Sprintf("cut %d torn %v", c, torn), func(t *testing.T) {
				r := &dRig{rig: newRig(t, r0.fx, r0.tool, dUnit), ml: r0.ml, bundle: r0.bundle, res: r0.res, stash: r0.stash}
				im := r.open()
				p, err := r.makeKeyPlan(im, k)
				if err != nil {
					t.Fatal(err)
				}
				o := r.opts(install.Options{MaxWrite: sweepWrite, Hooks: &install.Hooks{CutAfter: c, Torn: torn}})
				o.SSHKey = k
				_, err = install.ExecuteMainlineDirect(p, r.ml, im, o)
				if c < n && !errors.Is(err, install.ErrPowerCut) {
					t.Fatalf("cut %d: err %v, want a power cut", c, err)
				}
				im.Close()
				cut, _ := os.ReadFile(r.path)
				boots := reestablishCutOK(t, cut, &p.Plan)
				if !sameOutside(cut, r.before, &p.Plan) {
					t.Fatalf("cut %d: a byte outside the plan's ranges changed", c)
				}
				if c < n && boots != "keeper" {
					t.Fatalf("cut %d: boots %s; only the arm may make B boot next", c, boots)
				}
				if c >= n {
					r.keyDone(p, k)
					return
				}
				im2 := r.open()
				defer im2.Close()
				p2, err := r.makeKeyPlan(im2, k)
				if err != nil {
					var sb strings.Builder
					p2.Render(&sb)
					t.Fatalf("resume refused: %v\n%s", err, sb.String())
				}
				o2 := r.opts(install.Options{})
				o2.SSHKey = k
				if _, err := install.ExecuteMainlineDirect(p2, r.ml, im2, o2); err != nil {
					t.Fatalf("resume: %v", err)
				}
				im2.Close()
				r.keyDone(p, k) // the first plan's ranges (the resume plan has no keep-key step)
			})
			checked++
		}
	}
	t.Logf("ssh key cut sweep: %d cut points (the seed at write %d of %d)", checked, seedAt, n)
}

func planJSON(t testing.TB, p *plan.DirectPlan) string {
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
