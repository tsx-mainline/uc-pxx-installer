//go:build install && step2 && devtest

package step2_test

// MAINLINE-RC2-LS-REVIEW A-1, the mirror case of MINOR-4: a STOP set in a
// boot where /data did not mount is on the running root file system's own
// /var/lib/tsx/autoupdate/holds; in a later boot tsx-data binds
// /data/var/lib/tsx over /var/lib/tsx and hides it from step 2's two folders
// and from tsx-autoupdate. Quiet (HoldsClear) also reads that folder raw from
// the root partition (RootHolds): it refuses on a hold there, on anything but
// a folder on its path, and on every read error.

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/step2"
)

func writeMountinfo(t *testing.T, root, content string) {
	t.Helper()
	p := filepath.Join(root, strings.TrimPrefix(device.MountInfoPath, "/"))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRootHolds(t *testing.T) {
	for _, c := range []struct {
		name string
		img  []byte // system_a; nil = the fixture of the name
		ok   bool
		want string
	}{
		{"noholds", nil, true, ""},
		{"empty", nil, true, ""}, // holds/ with a dot file only
		{"novar", nil, true, ""}, // no /var at all
		{"step2", nil, false, "own folder (system_a:/var/lib/tsx/autoupdate/holds: step2)"},
		{"other", nil, false, "own folder (system_a:/var/lib/tsx/autoupdate/holds: other)"},
		{"holdsfile", nil, false, "is not a directory (a regular file)"},
		{"holdslink", nil, false, "is not a directory (a symbolic link)"},
		{"tsxlink", nil, false, "/var/lib/tsx is not a directory (a symbolic link)"},
		{"no ext4", make([]byte, 4096), false, "system_a (the running root)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			img := c.img
			if img == nil {
				img = rootImg(t, c.name)
			}
			e, d, root := diskEnv(t, map[string][]byte{"system_a": img})
			os.MkdirAll(filepath.Join(root, strings.TrimPrefix(filepath.Dir(step2.HoldsDir), "/")), 0o755)
			err := step2.RootHolds(e, d)
			if c.ok && err != nil {
				t.Fatalf("refused: %v", err)
			}
			if !c.ok && (err == nil || !strings.Contains(err.Error(), c.want)) {
				t.Fatalf("got %v, want a refusal with %q", err, c.want)
			}
			// HoldsClear and Quiet give the same answer (the live folders are
			// empty here)
			herr := step2.HoldsClear(e, d)
			if (herr == nil) != c.ok || (!c.ok && herr.Error() != err.Error()) {
				t.Fatalf("HoldsClear: %v, RootHolds: %v", herr, err)
			}
			if qerr := step2.LiveChecks(e, d).Quiet(); !c.ok && (qerr == nil || qerr.Error() != err.Error()) {
				t.Fatalf("Quiet: %v", qerr)
			}
		})
	}
	t.Run("the root is system_b", func(t *testing.T) {
		e, d, root := diskEnv(t, map[string][]byte{"system_b": rootImg(t, "step2")})
		os.MkdirAll(filepath.Join(root, strings.TrimPrefix(filepath.Dir(step2.HoldsDir), "/")), 0o755)
		if err := step2.RootHolds(e, d); err != nil {
			t.Fatalf("root system_a (no hold) refused: %v", err)
		}
		writeMountinfo(t, root, "20 1 179:23 / / rw,noatime - ext4 /dev/mmcblk0p23 rw\n21 20 0:5 / /proc rw - proc proc rw\n")
		if err := step2.RootHolds(e, d); err == nil || !strings.Contains(err.Error(), "system_b:/var/lib/tsx/autoupdate/holds: step2") {
			t.Fatalf("root system_b with a hold: %v", err)
		}
	})
	for _, c := range []struct{ name, mounts string }{
		{"the root on userdata", "20 1 179:28 / / rw - ext4 /dev/mmcblk0p28 rw\n"},
		{"the root not on the eMMC", "20 1 0:20 / / rw - tmpfs tmpfs rw\n"},
		{"no root line", "21 20 0:5 / /proc rw - proc proc rw\n"},
		{"two root lines", "20 1 179:22 / / rw - ext4 /dev/mmcblk0p22 rw\n25 20 179:23 / / rw - ext4 /dev/mmcblk0p23 rw\n"},
		{"the root on the whole disk", "20 1 179:0 / / rw - ext4 /dev/mmcblk0 rw\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, d, root := diskEnv(t, nil)
			writeMountinfo(t, root, c.mounts)
			if err := step2.RootHolds(e, d); err == nil || !strings.Contains(err.Error(), "not one system partition") {
				t.Fatalf("got %v", err)
			}
		})
	}
	t.Run("no eMMC view", func(t *testing.T) {
		e, _, _ := diskEnv(t, nil)
		if err := step2.RootHolds(e, nil); err == nil {
			t.Fatal("passed")
		}
	})
}

// copyTree copies the files, folders and links of src into dst.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, de fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		to := filepath.Join(dst, rel)
		st, err := os.Lstat(p)
		if err != nil {
			return err
		}
		switch {
		case st.Mode()&os.ModeSymlink != 0:
			l, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(l, to)
		case st.IsDir():
			return os.MkdirAll(to, st.Mode().Perm()|0o700)
		default:
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(to, b, st.Mode().Perm())
		}
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestHoldsRealAutoupdateHidden: no stub. The mirror case end to end with
// the real tsx-autoupdate (40c91350; UC_PXX_TSX_COMMON) and a real ext4 of
// the root file system made by mke2fs (UC_PXX_MKE2FS: a command that runs
// mke2fs; skipped without it): (1) a boot where /data did not mount: the
// real `stop-hold set step2` puts the hold on the root's own folder; Quiet
// refuses; (2) the root file system as on the eMMC after that boot (an ext4
// of the root's tree, in system_a); a new boot with /data mounted and
// tsx-data's bind over /var/lib/tsx: the two folders and the tool itself
// show no hold; Quiet refuses by the raw read (8c34a5d passed here);
// (3) a person removes the bind, runs the real `stop-hold clear step2`
// (on the root's folder), the root is written back, a new boot with the
// bind: Quiet ok.
func TestHoldsRealAutoupdateHidden(t *testing.T) {
	mk := os.Getenv("UC_PXX_MKE2FS")
	if mk == "" {
		t.Skip("UC_PXX_MKE2FS not set: this test makes the root file system with mke2fs")
	}
	e, d, root := diskEnv(t, map[string][]byte{"userdata": ext4Super("tsxdata")})
	tool := realAutoupdate(t, root)
	at := func(p string) string { return filepath.Join(root, strings.TrimPrefix(p, "/")) }
	live := step2.LiveChecks(e, d)
	varTsx, dataTsx := at("/var/lib/tsx"), at("/data/var/lib/tsx")
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(tool, args...)
		cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
		out, _ := cmd.CombinedOutput()
		return string(out)
	}
	// writeRoot: the root file system as it is on the eMMC: an ext4 of the
	// root's own /var/lib/tsx (the folder under any bind) in system_a
	rootTsx := filepath.Join(t.TempDir(), "root-var-lib-tsx")
	writeRoot := func() {
		t.Helper()
		st := filepath.Join(t.TempDir(), "tree")
		os.MkdirAll(filepath.Join(st, "etc"), 0o755)
		os.WriteFile(filepath.Join(st, "etc/hostname"), []byte("uc-pxx\n"), 0o644)
		os.MkdirAll(filepath.Join(st, "var/lib"), 0o755)
		copyTree(t, rootTsx, filepath.Join(st, "var/lib/tsx"))
		img := filepath.Join(t.TempDir(), "root.img")
		cmd := exec.Command(mk, "-q", "-F", "-t", "ext4", "-b", "1024", "-L", "tsxroot", "-d", st, img, "3072")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("mke2fs: %v\n%s", err, out)
		}
		b, err := os.ReadFile(img)
		if err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(at("/dev/mmcblk0p22"), os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteAt(b, 0); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	// (1) a boot where /data did not mount: /var/lib/tsx is the root's own
	// folder (the init made autoupdate/), /data the empty folder of the root
	os.RemoveAll(varTsx)
	os.MkdirAll(filepath.Join(varTsx, "autoupdate"), 0o755)
	os.MkdirAll(at("/data"), 0o755)
	setMounts(t, root, "")
	if rc := live.SetStopHold("STOP after step 2: test"); rc != 0 {
		t.Fatalf("(1) stop-hold set: exit %d", rc)
	}
	if _, err := os.Stat(filepath.Join(varTsx, "autoupdate/holds/step2")); err != nil {
		t.Fatalf("(1) the hold is not on the root's folder: %v", err)
	}
	if err := live.Quiet(); err == nil || !strings.Contains(err.Error(), "step2") {
		t.Fatalf("(1) Quiet with the hold: %v", err)
	}
	// (2) the root on the eMMC holds it; a new boot with /data mounted and the
	// bind over /var/lib/tsx (here: a link to the /data folder)
	if err := os.Rename(varTsx, rootTsx); err != nil {
		t.Fatal(err)
	}
	writeRoot()
	os.MkdirAll(filepath.Join(dataTsx, "autoupdate"), 0o755)
	if err := os.Symlink(dataTsx, varTsx); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(at("/run"))
	setMounts(t, root, userdataMount)
	if out := run("stop-hold", "list"); !strings.Contains(out, "no STOP hold") {
		t.Fatalf("(2) the tool sees a hold under the bind: %q", out)
	}
	if err := step2.HoldsClear(e, d); err == nil || !strings.Contains(err.Error(), "own folder (system_a:/var/lib/tsx/autoupdate/holds: step2)") {
		t.Fatalf("(2) A-1: the hold under the bind passed: %v", err)
	}
	if err := live.Quiet(); err == nil || !strings.Contains(err.Error(), "own folder") {
		t.Fatalf("(2) A-1: Quiet: %v", err)
	}
	// (3) a person removes the bind (the root's folder is /var/lib/tsx
	// again), clears with the real tool, the root is on the eMMC; a new
	// boot with the bind
	os.Remove(varTsx)
	if err := os.Rename(rootTsx, varTsx); err != nil {
		t.Fatal(err)
	}
	if out := run("stop-hold", "clear", "step2"); strings.Contains(out, "error") {
		t.Fatalf("(3) stop-hold clear: %q", out)
	}
	if _, err := os.Lstat(filepath.Join(varTsx, "autoupdate/holds/step2")); err == nil {
		t.Fatal("(3) the clear left the hold")
	}
	if err := os.Rename(varTsx, rootTsx); err != nil {
		t.Fatal(err)
	}
	writeRoot()
	if err := os.Symlink(dataTsx, varTsx); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(at("/run"))
	if err := live.Quiet(); err != nil {
		t.Fatalf("(3) Quiet after the clear: %v", err)
	}
}
