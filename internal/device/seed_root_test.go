//go:build seedroot && install && devtest

package device

// The device seed's mount on a real kernel with the kernel read-only flags set
// as the RAM image sets them (RC-8 key code review MAJOR-1/MAJOR-2;
// ci/seed-root-test.sh: as root in a privileged container, loop devices,
// never on a unit). SEED_ROOTFS is a real payload root file system.

import (
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"uc-pxx-installer/internal/keywrite"
	"uc-pxx-installer/internal/keywrite/looptest"
	kwmount "uc-pxx-installer/internal/keywrite/mount"
	"uc-pxx-installer/internal/sshkey"
)

// blockNode attaches a loop device to img and gives its node and numbers.
func blockNode(t *testing.T, img string) (string, uint32, uint32) {
	st, err := os.Stat(img)
	if err != nil {
		t.Fatal(err)
	}
	dev, detach, err := looptest.Attach(img, 0, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(detach)
	fi, err := os.Lstat(dev)
	if err != nil {
		t.Fatal(err)
	}
	rd := fi.Sys().(*syscall.Stat_t).Rdev
	return dev, major64(rd), minor64(rd)
}

// roOf reads the kernel read-only flag (BLKROGET).
func (e *Env) roOf(p string, maj, min uint32) (bool, error) {
	f, err := e.openRead(p, maj, min)
	if err != nil {
		return false, err
	}
	defer f.Close()
	var v int32
	err = ioctlInt(f, blkROGet, &v)
	return v != 0, err
}

func TestSeedNodeReadOnlyFlags(t *testing.T) {
	rootfs := os.Getenv("SEED_ROOTFS")
	if rootfs == "" || os.Geteuid() != 0 {
		t.Skip("SEED_ROOTFS and root needed (ci/seed-root-test.sh)")
	}
	tmp := t.TempDir()
	part := filepath.Join(tmp, "system_b.img")
	in, _ := os.Open(rootfs)
	out, _ := os.Create(part)
	io.Copy(out, in)
	in.Close()
	out.Close()
	disk := filepath.Join(tmp, "disk.img")
	os.WriteFile(disk, make([]byte, 1<<20), 0o600)
	e := &Env{root: "/"}
	SeedMount = func(node, mnt string, files []keywrite.File, there bool) (bool, error) {
		return kwmount.MountWrite(node, mnt, files, there, nil)
	}
	pn, pmaj, pmin := blockNode(t, part)
	dn, dmaj, dmin := blockNode(t, disk)
	n := seedNodes{disk: dn, dmaj: dmaj, dmin: dmin, part: pn, pmaj: pmaj, pmin: pmin}
	// as the RAM image leaves them: both read-only
	for _, x := range []struct {
		p        string
		maj, min uint32
	}{{dn, dmaj, dmin}, {pn, pmaj, pmin}} {
		if err := e.setRO(x.p, x.maj, x.min, true); err != nil {
			t.Fatal(err)
		}
	}
	k, _ := sshkey.Parse([]byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBbrhXWbO19v2SgtcPAMqpGOt8V5hUqtgQyUCJxvGtTp test-ed25519\n"))
	files := keywrite.Seed(k)
	f, _ := os.Open(part)
	st, _ := f.Stat()
	folder, err := keywrite.Before(f, st.Size(), files)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	// the mount alone on the read-only node fails (what MAJOR-1 found)
	if _, err := kwmount.MountWrite(pn, filepath.Join(tmp, "m0"), files, folder, nil); err == nil {
		t.Fatal("a read-write mount of a read-only node worked: the test does not show the flag")
	} else {
		t.Logf("the mount on the read-only node: %v (as on a unit before the fix)", err)
	}
	made, err := e.seedNode(n, filepath.Join(tmp, "m1"), files, folder)
	if err != nil {
		t.Fatalf("seedNode: %v", err)
	}
	for _, x := range []struct {
		p        string
		maj, min uint32
	}{{dn, dmaj, dmin}, {pn, pmaj, pmin}} {
		g, err := e.roOf(x.p, x.maj, x.min)
		if err != nil || !g {
			t.Fatalf("%s: read-only after the seed %v %v", x.p, g, err)
		}
	}
	f, _ = os.Open(part)
	defer f.Close()
	if err := keywrite.Check(f, st.Size(), files, made); err != nil {
		t.Fatalf("check: %v", err)
	}
	t.Logf("seedNode: the flags cleared for the mount and set again; the seed reads back (folder made %v)", made)
}
