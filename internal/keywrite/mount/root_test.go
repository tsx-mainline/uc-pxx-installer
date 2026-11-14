//go:build seedroot && install

package mount_test

// The seed's read-write mount on a real kernel (ci/seed-root-test.sh: as root
// in a privileged test container, loop devices, never on a unit; mainline-rc-8
// D2). SEED_ROOTFS is a real payload root file system (rootfs.ext4 of a
// STEP-2 payload); SEED_E2FSCK runs e2fsck. It checks:
//   - the seed on a copy: Before, MountWrite, Check, e2fsck -fn clean, the
//     rest of the file system unchanged (every file of the image but the two
//     and /data/tsx reads the same before and after);
//   - a power cut at each stage (mount, mkdir, write, rename, unmount): the
//     image's bytes at that moment (read while it is still mounted) stand for
//     the disk after the cut; then the installer's resume, which writes the
//     image again from the payload (every byte) and seeds again: Check ok;
//   - a refusal: a file system that is not clean (needs_recovery set) is not
//     mounted.

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"uc-pxx-installer/internal/ext4"
	"uc-pxx-installer/internal/keywrite"
	"uc-pxx-installer/internal/keywrite/looptest"
	"uc-pxx-installer/internal/keywrite/mount"
	"uc-pxx-installer/internal/sshkey"
)

const rootKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBbrhXWbO19v2SgtcPAMqpGOt8V5hUqtgQyUCJxvGtTp test-ed25519\n"

func need(t *testing.T) (string, string) {
	r, f := os.Getenv("SEED_ROOTFS"), os.Getenv("SEED_E2FSCK")
	if r == "" || f == "" || os.Geteuid() != 0 {
		t.Skip("SEED_ROOTFS, SEED_E2FSCK and root needed (ci/seed-root-test.sh)")
	}
	return r, f
}

func loop(t *testing.T, img string) string {
	st, err := os.Stat(img)
	if err != nil {
		t.Fatal(err)
	}
	dev, detach, err := looptest.Attach(img, 0, st.Size())
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	t.Cleanup(detach)
	return dev
}

func copyFile(t *testing.T, src, dst string) {
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	out.Close()
}

// tree is the sha256 of every regular file the ext4 reader can walk, by path.
func tree(t *testing.T, img string) map[string]string {
	f, err := os.Open(img)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()
	fs, err := ext4.Open(f, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	fs.Walk(200000, func(p string) {
		if b, err := fs.ReadFile(p, 64<<20); err == nil {
			s := sha256.Sum256(b)
			out[p] = fmt.Sprintf("%x", s)
		}
	})
	return out
}

func fsck(t *testing.T, e2fsck, img string) string {
	out, err := exec.Command(e2fsck, "-fn", img).CombinedOutput()
	if err != nil {
		return fmt.Sprintf("e2fsck -fn: %v\n%s", err, out)
	}
	return ""
}

func seedOnce(t *testing.T, img string, hook mount.Hook) (bool, error) {
	k, _ := sshkey.Parse([]byte(rootKey))
	files := keywrite.Seed(k)
	f, _ := os.Open(img)
	st, _ := f.Stat()
	there, err := keywrite.Before(f, st.Size(), files)
	f.Close()
	if err != nil {
		return false, fmt.Errorf("before: %v", err)
	}
	dev := loop(t, img)
	return mount.MountWrite(dev, filepath.Join(t.TempDir(), "mnt"), files, there, hook)
}

func TestSeedRoot(t *testing.T) {
	rootfs, e2fsck := need(t)
	k, _ := sshkey.Parse([]byte(rootKey))
	files := keywrite.Seed(k)
	img := filepath.Join(t.TempDir(), "system_b.img")
	copyFile(t, rootfs, img)
	if p := fsck(t, e2fsck, img); p != "" {
		t.Fatalf("the payload's rootfs before: %s", p)
	}
	before := tree(t, img)
	made, err := seedOnce(t, img, nil)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(img)
	st, _ := f.Stat()
	if err := keywrite.Check(f, st.Size(), files, made); err != nil {
		t.Fatalf("check: %v", err)
	}
	f.Close()
	if p := fsck(t, e2fsck, img); p != "" {
		t.Fatalf("after the seed: %s", p)
	}
	after := tree(t, img)
	for p, h := range before {
		if after[p] != h {
			t.Errorf("%s changed in the seed", p)
		}
	}
	var added []string
	for p := range after {
		if _, ok := before[p]; !ok {
			added = append(added, p)
		}
	}
	if len(added) != 2 {
		t.Fatalf("the seed added %v, want exactly the two files", added)
	}
	t.Logf("seed ok (folder made %v): %v; the %d other files unchanged; e2fsck -fn clean", made, added, len(before))

	for _, stage := range []mount.Stage{mount.StageMount, mount.StageMkdir, mount.StageWrite, mount.StageRename, mount.StageUnmount} {
		t.Run("cut at "+string(stage), func(t *testing.T) {
			img := filepath.Join(t.TempDir(), "system_b.img")
			copyFile(t, rootfs, img)
			var snap []byte
			cut := errors.New("power cut (test)")
			_, err := seedOnce(t, img, func(s mount.Stage, _ string) error {
				if s != stage {
					return nil
				}
				var err error
				snap, err = os.ReadFile(img)
				if err != nil {
					return err
				}
				return cut
			})
			if !errors.Is(err, cut) || snap == nil {
				t.Fatalf("the seed did not stop at %s: %v", stage, err)
			}
			os.WriteFile(img, snap, 0o600) // the disk as the cut left it
			sb, _ := ext4.ReadSuper(bytes.NewReader(snap))
			t.Logf("at the cut: %s; e2fsck -fn: %s", sb, strings.ReplaceAll(fsck(t, e2fsck, img), "\n", " | "))
			// the resume: the image written again (every byte), then the seed
			copyFile(t, rootfs, img)
			made, err := seedOnce(t, img, nil)
			if err != nil {
				t.Fatalf("the resume's seed: %v", err)
			}
			f, _ := os.Open(img)
			st, _ := f.Stat()
			defer f.Close()
			if err := keywrite.Check(f, st.Size(), files, made); err != nil {
				t.Fatalf("the resume's check: %v", err)
			}
		})
	}
	t.Run("a file system that needs recovery is not mounted", func(t *testing.T) {
		img := filepath.Join(t.TempDir(), "system_b.img")
		copyFile(t, rootfs, img)
		fh, _ := os.OpenFile(img, os.O_RDWR, 0)
		var b [4]byte
		fh.ReadAt(b[:], 1024+0x60)
		binary.LittleEndian.PutUint32(b[:], binary.LittleEndian.Uint32(b[:])|0x4)
		fh.WriteAt(b[:], 1024+0x60)
		fh.Close()
		if _, err := seedOnce(t, img, nil); err == nil || !strings.Contains(err.Error(), "not clean") {
			t.Fatalf("%v", err)
		}
	})
}
