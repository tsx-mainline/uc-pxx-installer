package provision

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uc-pxx-installer/internal/provision/bundle"
	"uc-pxx-installer/internal/provision/bundle/bundletest"
	"uc-pxx-installer/internal/xfer/handover"
)

// handed is an honest hand-over folder of b (as the receiver writes it).
func handed(t *testing.T, b *bundletest.Bundle) string {
	d := filepath.Join(t.TempDir(), "complete")
	os.MkdirAll(d, 0o700)
	u, _ := bundle.NewUnpacker(bundle.Pins{Size: b.Pins.Size, SHA256: b.Pins.SHA256, IndexSHA256: b.Pins.IndexSHA256}, dirSink{d})
	if _, err := u.Write(b.Tar); err != nil || u.Finish() != nil {
		t.Fatal(err)
	}
	return d
}

// TestTakeOverHostile: the root's take-over and check of a cable hand-over
// (review MINOR-6): an honest folder is taken; a changed member, a changed
// bundle.json, an extra file or folder, a member that is a symbolic link, a
// member with a second hard link, a folder that is a link: each refused.
func TestTakeOverHostile(t *testing.T) {
	b := bundletest.Simple("0.2.0-rc10", true)
	pins := b.Pins
	m := b.DocV.Packages[0].Path
	cases := []struct {
		name string
		mod  func(d string)
		want string
	}{
		{"honest", func(string) {}, ""},
		{"a changed member", func(d string) {
			os.WriteFile(filepath.Join(d, m), []byte(strings.Repeat("x", int(b.DocV.Packages[0].Size))), 0o600)
		}, "sha256"},
		{"a changed bundle.json", func(d string) { os.WriteFile(filepath.Join(d, bundle.DocName), append(b.Doc, ' '), 0o600) }, "bundle.json has sha256"},
		{"an extra file", func(d string) { os.WriteFile(filepath.Join(d, "v3.24/main/aarch64/evil.apk"), []byte("x"), 0o600) }, "does not name"},
		{"an extra folder", func(d string) { os.MkdirAll(filepath.Join(d, "etc/apk"), 0o700) }, "does not name"},
		{"a member that is a link", func(d string) {
			os.Remove(filepath.Join(d, m))
			os.Symlink("/etc/shadow", filepath.Join(d, m))
		}, "not a folder or a regular file"},
		{"a member with a second link", func(d string) { os.Link(filepath.Join(d, m), filepath.Join(t.TempDir(), "kept")) }, "one link"},
	}
	for _, c := range cases {
		d := handed(t, b)
		c.mod(d)
		repo := filepath.Join(t.TempDir(), "repo")
		err := takeOver(d, repo)
		if err == nil {
			_, _, err = verifyRepo(repo, pins)
		}
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
	// the folder itself a link
	d := handed(t, b)
	l := filepath.Join(t.TempDir(), "complete")
	os.Symlink(d, l)
	repo := filepath.Join(t.TempDir(), "repo")
	err := takeOver(l, repo)
	if err == nil {
		_, _, err = verifyRepo(repo, pins)
	}
	if err == nil {
		t.Fatal("a hand-over folder that is a link was taken")
	}
}

// TestLiveExecAllowList: the service runs only its pinned programs in their
// forms (nothing is started for a refused argv).
func TestLiveExecAllowList(t *testing.T) {
	for _, argv := range [][]string{
		{"/sbin/apk", "add", "--no-network", "--no-cache", "--repositories-file", "/etc/apk/repositories", "x"},
		{"/sbin/apk", "add", "x"},
		{"/sbin/apk", "upgrade", "--no-network", "--no-cache", "--repositories-file", RepositoriesFile, "x"},
		{"/bin/sh", "-c", "dd of=/dev/x", "x"},
		{"/sbin/rc-service", "sshd", "start"},
		{XferBin, "run", "--kind", "provision"},
		{"/usr/sbin/uc-pxx-bootctl", "status"},
		{},
	} {
		if err := liveExec(argv, nil); err == nil || !strings.Contains(err.Error(), "does not run") && len(argv) > 0 {
			t.Fatalf("%q: %v", argv, err)
		}
	}
	a := xferArgv(XferArgs{Profiles: []handover.ProvisionProfile{{Name: "console"}}, Fixed: "console", ConsoleKey: true, Seq: 3, StatusOnly: true}, true)
	want := XferBin + " run --kind provision --stage " + handover.ProvisionStage + " --info " + handover.ProvisionInfo + " --progress " +
		handover.ProvisionProgressFile + " --seq 3 --profile console:-:-:-:- --fixed console --console-key --status-only --net"
	if strings.Join(a, " ") != want {
		t.Fatalf("%q", a)
	}
}
