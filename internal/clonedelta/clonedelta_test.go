package clonedelta_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uc-pxx-installer/internal/clone"
	"uc-pxx-installer/internal/clonedelta"
)

func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	d := t.TempDir()
	for p, c := range files {
		f := filepath.Join(d, p)
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(p, "/") {
			continue
		}
		if err := os.WriteFile(f, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

// TestApplyDraft: the draft clone-delta.json on a tree like B's.
func TestApplyDraft(t *testing.T) {
	d := tree(t, map[string]string{
		"usr/libexec/uc-pxx-step2/uc-pxx-step2": "step2", "usr/libexec/uc-pxx-step2/uc-pxx-bootctl-inst": "r2",
		"etc/init.d/uc-pxx-step2": "init", "etc/conf.d/uc-pxx-step2": "conf", "etc/tsx/step2-bootctl-inst": "marker",
		"var/lib/uc-pxx-step2/state": "started", "usr/libexec/uc-pxx-provision/uc-pxx-provision": "prov",
		"usr/share/uc-pxx-provision/profiles.json": "{}", "var/lib/uc-pxx-provision/done": "{}",
		"var/log/messages": "x", "var/log/chrony/a": "y", "data/tsx/panel.conf": "seed", "var/lib/tsx/autoupdate/mode": "manual",
		"etc/tsx/build-id": "0.2.0-rc10-step2\n", "usr/bin/keep": "keep",
	})
	os.Symlink("/etc/init.d/uc-pxx-step2", filepath.Join(d, "etc/runlevels/default/uc-pxx-step2"))
	os.MkdirAll(filepath.Join(d, "etc/runlevels/default"), 0o755)
	os.Symlink("/etc/init.d/uc-pxx-step2", filepath.Join(d, "etc/runlevels/default/uc-pxx-step2"))
	g := clone.EmbeddedGate()
	uid, gid := uint32(os.Getuid()), uint32(os.Getgid())
	for i := range g.Delta.Empty {
		g.Delta.Empty[i].UID, g.Delta.Empty[i].GID = uid, gid
	}
	for i := range g.Delta.Write {
		g.Delta.Write[i].UID, g.Delta.Write[i].GID = uid, gid
	}
	if err := clonedelta.Apply(d, g.Delta, "0.2.0-rc10-kiosk-ha\n"); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"usr/libexec/uc-pxx-step2", "etc/init.d/uc-pxx-step2", "etc/conf.d/uc-pxx-step2", "etc/tsx/step2-bootctl-inst",
		"etc/runlevels/default/uc-pxx-step2", "var/lib/uc-pxx-step2", "usr/libexec/uc-pxx-provision", "usr/share/uc-pxx-provision",
		"var/lib/uc-pxx-provision", "var/log/messages", "var/log/chrony", "data/tsx", "var/lib/tsx/autoupdate"} {
		if _, err := os.Lstat(filepath.Join(d, gone)); err == nil {
			t.Errorf("%s is still there", gone)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(d, "etc/tsx/build-id")); string(b) != "0.2.0-rc10-kiosk-ha\n" {
		t.Errorf("build-id %q", b)
	}
	for _, kept := range []string{"usr/bin/keep", "var/log", "data", "var/lib/tsx", "etc/runlevels/default"} {
		if _, err := os.Lstat(filepath.Join(d, kept)); err != nil {
			t.Errorf("%s is gone", kept)
		}
	}
	if fi, _ := os.Stat(filepath.Join(d, "etc/tsx/build-id")); fi.Mode().Perm() != 0o644 {
		t.Errorf("build-id mode %v", fi.Mode())
	}
}

// TestApplyNoLinkFollowed: a link where a folder of the difference is never
// followed; the target outside is untouched.
func TestApplyNoLinkFollowed(t *testing.T) {
	out := tree(t, map[string]string{"victim/file": "keep me", "victim/tsx/x": "keep"})
	for _, c := range []struct{ name, link, delta string }{
		{"a parent of remove", "var", `{"schema":1,"remove":["/var/lib/uc-pxx-step2"]}`},
		{"the folder of remove_children", "var/log", `{"schema":1,"remove_children":["/var/log"]}`},
		{"the folder of empty", "data", `{"schema":1,"empty":[{"path":"/data","mode":"0755","uid":0,"gid":0}]}`},
		{"a parent of write", "etc", `{"schema":1,"write":[{"path":"/etc/tsx/build-id","mode":"0644","uid":0,"gid":0,"from":"build-id"}]}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := tree(t, map[string]string{"usr/": ""})
			os.MkdirAll(filepath.Dir(filepath.Join(d, c.link)), 0o755)
			if err := os.Symlink(filepath.Join(out, "victim"), filepath.Join(d, c.link)); err != nil {
				t.Fatal(err)
			}
			dl, err := clone.ParseDelta([]byte(c.delta))
			if err != nil {
				t.Fatal(err)
			}
			err = clonedelta.Apply(d, dl, "x")
			if err == nil {
				t.Fatal("applied through a link")
			}
			if b, _ := os.ReadFile(filepath.Join(out, "victim/file")); string(b) != "keep me" {
				t.Fatal("the file outside changed")
			}
			if _, err := os.Lstat(filepath.Join(out, "victim/tsx/x")); err != nil {
				t.Fatal("the folder outside changed")
			}
		})
	}
}
