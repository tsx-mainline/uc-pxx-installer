//go:build seedroot && install

package mount_test

// The first boots after the seed (ci/seed-root-test.sh with FIRSTBOOT_B and
// FIRSTBOOT_A: real payload root file systems built with tsx-linux-common's
// setup-done rule; as root in a privileged container, the aarch64 tools by
// the host's qemu binfmt; never on a unit; mainline-rc-8 D1-D3):
//   - B: the real seed (MountWrite) into a copy of B's root file system, then
//     tsx-config apply as the slot's boot runs it (chroot): root's
//     authorized_keys holds exactly the key, sshd is in the default runlevel,
//     and tsx-config setup-state says unconfigured (the pairing window opens);
//   - A after step 2: /data/tsx of the seeded B copied as the data migrate does
//     (cp -a; rootfs/tests/test-data-migrate.sh runs the real tool) into A's
//     /data, then the same apply: the same key, sshd on, unconfigured;
//   - no key: B with no seed: no authorized_keys, no sshd link, unconfigured.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"uc-pxx-installer/internal/keywrite"
	"uc-pxx-installer/internal/keywrite/mount"
	"uc-pxx-installer/internal/sshkey"
)

func sh(t *testing.T, cmd string) string {
	t.Helper()
	out, err := exec.Command("sh", "-c", cmd).CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", cmd, err, out)
	}
	return string(out)
}

// boot mounts img at a folder and runs tsx-config apply and setup-state in a
// chroot; it returns authorized_keys, whether sshd is in the default
// runlevel, and setup-state's answer.
func boot(t *testing.T, img string, prep func(root string)) (string, bool, string) {
	dev := loop(t, img)
	m := filepath.Join(t.TempDir(), "root")
	os.Mkdir(m, 0o755)
	sh(t, "mount -t ext4 "+dev+" "+m)
	defer exec.Command("umount", m).Run()
	if prep != nil {
		prep(m)
	}
	sh(t, "mount -t proc proc "+m+"/proc && mount -t tmpfs tmpfs "+m+"/run")
	defer exec.Command("umount", m+"/proc").Run()
	defer exec.Command("umount", m+"/run").Run()
	out := sh(t, "chroot "+m+" /bin/sh -c '/usr/local/sbin/tsx-config apply >/dev/null 2>&1; echo apply=$?; /usr/local/sbin/tsx-config setup-state; true'")
	if !strings.Contains(out, "apply=0") {
		t.Fatalf("tsx-config apply: %s", out)
	}
	ak, _ := os.ReadFile(filepath.Join(m, "root/.ssh/authorized_keys"))
	_, lerr := os.Lstat(filepath.Join(m, "etc/runlevels/default/sshd"))
	st := "?"
	for _, l := range strings.Split(out, "\n") {
		if l == "configured" || l == "unconfigured" {
			st = l
		}
	}
	return string(ak), lerr == nil, st
}

func TestSeedFirstBoot(t *testing.T) {
	b, a := os.Getenv("FIRSTBOOT_B"), os.Getenv("FIRSTBOOT_A")
	if b == "" || a == "" || os.Geteuid() != 0 {
		t.Skip("FIRSTBOOT_B, FIRSTBOOT_A and root needed (ci/seed-root-test.sh)")
	}
	k, err := sshkey.Parse([]byte(rootKey))
	if err != nil {
		t.Fatal(err)
	}
	files := keywrite.Seed(k)
	imgB := filepath.Join(t.TempDir(), "system_b.img")
	copyFile(t, b, imgB)
	f, _ := os.Open(imgB)
	st, _ := f.Stat()
	there, err := keywrite.Before(f, st.Size(), files)
	f.Close()
	if err != nil {
		t.Fatalf("before: %v", err)
	}
	if _, err := mount.MountWrite(loop(t, imgB), filepath.Join(t.TempDir(), "mnt"), files, there, nil); err != nil {
		t.Fatal(err)
	}
	ak, sshd, state := boot(t, imgB, nil)
	if strings.TrimSpace(ak) != k.Line || !sshd || state != "unconfigured" {
		t.Fatalf("B's first boot: authorized_keys %q, sshd %v, setup %s", ak, sshd, state)
	}
	t.Logf("B's first boot: authorized_keys = the key (%s), sshd in the default runlevel, setup-state %s", k.Fingerprint, state)
	// the data of B as the migrate carries it
	dataB := filepath.Join(t.TempDir(), "data-b")
	os.Mkdir(dataB, 0o755)
	m := filepath.Join(t.TempDir(), "b")
	os.Mkdir(m, 0o755)
	sh(t, "mount -o ro -t ext4 "+loop(t, imgB)+" "+m+" && cp -a "+m+"/data/tsx "+dataB+"/ && umount "+m)
	imgA := filepath.Join(t.TempDir(), "system_a.img")
	copyFile(t, a, imgA)
	ak, sshd, state = boot(t, imgA, func(root string) {
		sh(t, "rm -rf "+root+"/data/tsx && cp -a "+dataB+"/tsx "+root+"/data/")
	})
	if strings.TrimSpace(ak) != k.Line || !sshd || state != "unconfigured" {
		t.Fatalf("A's first boot after the migrate: authorized_keys %q, sshd %v, setup %s", ak, sshd, state)
	}
	t.Logf("A's first boot (the migrated /data/tsx): the same key, sshd on, setup-state %s", state)
	imgN := filepath.Join(t.TempDir(), "nokey.img")
	copyFile(t, b, imgN)
	ak, sshd, state = boot(t, imgN, nil)
	if ak != "" || sshd || state != "unconfigured" {
		t.Fatalf("no key: authorized_keys %q, sshd %v, setup %s", ak, sshd, state)
	}
	t.Log("no key: no authorized_keys, sshd off, unconfigured (as today)")
}
