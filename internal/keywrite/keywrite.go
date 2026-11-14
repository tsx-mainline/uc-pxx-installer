// Package keywrite seeds the person's ssh public key into the root file
// system of a new mainline slot (mainline-rc-8; owner 2026-11-13 ~19:30, D2
// "W1, mount and write"). After system_b is written, synced and verified
// (slot B still {0,0,0}, the keeper A successful and the slot that boots),
// the RAM installer mounts system_b read-write, writes two files and reads
// them back:
//
//   - /data/tsx/panel.conf: one line SSH_AUTHORIZED_KEY="<the key line>"
//     (tsx-config's store form), mode 0600, root. tsx-config applies it at
//     the slot's first boot: root's authorized_keys, and the uc-pxx sshd
//     plugin runs sshd (key only);
//   - /data/tsx/setup-v2: "2", mode 0644, root: the stamp of tsx-linux-common's
//     setup-done rule, so the slot boots UNCONFIGURED (its setup page's
//     pairing window opens) although panel.conf has a key.
//
// /data/tsx is made (0700, root) when it is not in the image. Both files
// must not exist before (a reviewed payload has neither). The write keeps
// the file system change minimal: the superblock must be clean before the
// mount (no journal to replay, no orphan, no error), the mount has noatime,
// nodev, nosuid, noexec, data=ordered, errors=remount-ro, noinit_itable
// (no lazy inode-table thread) and nodiscard; each file is written as a new
// file (O_EXCL, no link followed: every open is relative to the folder's fd
// with O_NOFOLLOW), fsync'd, renamed into place, the folder fsync'd; then
// syncfs and the unmount; then the superblock must be clean again and the
// files read back by the package ext4 reader (no mount) must be exactly
// the plan's.
//
// A power cut at any point leaves slot B {0,0,0}: the keeper A boots, and a
// new install writes system_b again from the image (every byte) before it
// seeds the key again. The A/B sector is not touched here.
package keywrite

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"uc-pxx-installer/internal/ext4"
	"uc-pxx-installer/internal/sshkey"
)

// Folder is the settings folder of a tsx panel on its /data.
const Folder = "/data/tsx"

// File is one file of the seed.
type File struct {
	Path string `json:"path"`
	Mode uint16 `json:"mode"` // permission bits; root:root
	Data []byte `json:"-"`
	// SHA256 and Size are of Data (the plan records them: the key is public,
	// but the plan names it by its fingerprint and these only)
	SHA256 string `json:"sha256"`
	Size   int    `json:"size"`
}

// Seed is the two files for the key k.
func Seed(k *sshkey.Key) []File {
	return []File{mk(Folder+"/panel.conf", 0o600, k.PanelConf()), mk(Folder+"/setup-v2", 0o644, sshkey.SetupStamp())}
}

func mk(p string, mode uint16, b []byte) File {
	s := sha256.Sum256(b)
	return File{Path: p, Mode: mode, Data: b, SHA256: hex.EncodeToString(s[:]), Size: len(b)}
}

// SetupRuleFile is the helper of tsx-linux-common's setup-done rule (the
// setup-v2 stamp); a payload without it does not know the stamp.
const SetupRuleFile = "/usr/local/lib/tsx/setup-state.sh"

// FolderMode is the mode of /data/tsx when the seed makes it.
const FolderMode = 0o700

// MountOptions are the mount data of the seed's read-write mount.
const MountOptions = "data=ordered,errors=remount-ro,noinit_itable,nodiscard"

// Before is the root file system's state that the seed needs, read from the
// written image (or the payload's rootfs) without mounting: a clean ext4,
// /data a directory owned by root (not a link), /data/tsx missing or a
// directory owned by root, and neither file there. It returns whether
// /data/tsx exists.
func Before(r io.ReaderAt, size int64, files []File) (folder bool, err error) {
	sb, err := ext4.ReadSuper(r)
	if err != nil {
		return false, fmt.Errorf("the root file system: %v", err)
	}
	if !sb.Clean() {
		return false, fmt.Errorf("the root file system is not clean (%s): no mount", sb)
	}
	fs, err := ext4.Open(r, size)
	if err != nil {
		return false, err
	}
	// the owners are the reviewed payload's (root); the seed makes its own
	// files root's and Check reads that back
	st, err := ext4.LstatNoFollow(fs, "/data")
	if err != nil || st.Type != "dir" || st.Perm&0o022 != 0 {
		return false, fmt.Errorf("/data of the root file system is not a folder that only its owner may write (%+v, %v)", st, err)
	}
	st, err = ext4.LstatNoFollow(fs, Folder)
	switch {
	case errors.Is(err, ext4.ErrNotFound):
	case err != nil:
		return false, fmt.Errorf("%s: %v", Folder, err)
	case st.Type != "dir" || st.Perm&0o022 != 0:
		return false, fmt.Errorf("%s is not a folder that only its owner may write (%+v)", Folder, st)
	default:
		folder = true
	}
	// the payload must know the setup stamp (tsx-linux-common's setup-done
	// rule, package tsx-base): else a key in panel.conf makes the panel
	// "configured" and its pairing window would not open (key code review
	// MINOR-3)
	if st, err := ext4.LstatNoFollow(fs, SetupRuleFile); err != nil || st.Type != "file" {
		return folder, fmt.Errorf("the payload's root file system has no %s (the setup-done rule of tsx-linux-common): its setup page would count a seeded key as a finished setup (%+v, %v)", SetupRuleFile, st, err)
	}
	for _, f := range files {
		if path.Dir(f.Path) != Folder {
			return folder, fmt.Errorf("%s is not in %s", f.Path, Folder)
		}
		if _, err := ext4.LstatNoFollow(fs, f.Path); !errors.Is(err, ext4.ErrNotFound) {
			return folder, fmt.Errorf("%s is in the root file system already (%v): the seed never replaces a file", f.Path, err)
		}
	}
	return folder, nil
}

// Check reads the seed back from the partition (no mount): a clean file
// system, /data/tsx a root folder (0700 when the seed made it), and each
// file a regular file of root with exactly the plan's mode, size and sha256
// (and bytes, when the caller has them).
func Check(r io.ReaderAt, size int64, files []File, madeFolder bool) error {
	sb, err := ext4.ReadSuper(r)
	if err != nil {
		return err
	}
	if !sb.Clean() {
		return fmt.Errorf("after the unmount the root file system is not clean (%s)", sb)
	}
	fs, err := ext4.Open(r, size)
	if err != nil {
		return err
	}
	st, err := ext4.LstatNoFollow(fs, Folder)
	if err != nil || st.Type != "dir" || st.UID != 0 || st.GID != 0 || (madeFolder && st.Perm != FolderMode) {
		return fmt.Errorf("%s after the seed: %+v %v", Folder, st, err)
	}
	for _, f := range files {
		b, st, err := ext4.ReadFileNoFollow(fs, f.Path, 1<<16)
		if err != nil {
			return fmt.Errorf("read back %s: %v", f.Path, err)
		}
		s := sha256.Sum256(b)
		if st.UID != 0 || st.GID != 0 || st.Perm != f.Mode || len(b) != f.Size || hex.EncodeToString(s[:]) != f.SHA256 ||
			(f.Data != nil && !bytes.Equal(b, f.Data)) {
			return fmt.Errorf("read back %s: owner %d:%d mode %#o, %d bytes; the plan has root, %#o, %d bytes sha256 %s", f.Path,
				st.UID, st.GID, st.Perm, len(b), f.Mode, f.Size, f.SHA256)
		}
	}
	return nil
}

// Describe is the plan's words for the seed.
func Describe(k *sshkey.Key) string {
	return fmt.Sprintf("%s %d bits %s", k.Type, k.Bits, k.Fingerprint)
}

// ValidPath: a seed path is exactly one of the two.
func ValidPath(p string) bool {
	return !strings.Contains(p, "..") && (p == Folder+"/panel.conf" || p == Folder+"/setup-v2")
}

// Stage names a point of the seed, for the cut tests (Hook).
type Stage string

const (
	StageMount   Stage = "mount"
	StageMkdir   Stage = "mkdir"
	StageWrite   Stage = "write"  // before each file (Write names the file)
	StageRename  Stage = "rename" // before each rename
	StageUnmount Stage = "unmount"
)

// Hook is called before each stage; an error stops the seed there (a test's
// power cut: the caller then copies the partition as a power cut would leave
// it). file is the seed file of a write or rename stage.
type Hook func(st Stage, file string) error

// SameTree: the seeded root file system (seeded, n bytes) equals the payload's
// (rootfs, rn bytes) in every entry (type, mode, owner, file sha256, link
// target) except the seed: the two files, and /data/tsx when the seed made it
// (RC-8 key code review MINOR-4: nothing else changed).
func SameTree(seeded io.ReaderAt, n int64, rootfs io.ReaderAt, rn int64, files []File, madeFolder bool) error {
	a, err := ext4.Open(seeded, n)
	if err != nil {
		return err
	}
	b, err := ext4.Open(rootfs, rn)
	if err != nil {
		return err
	}
	da, err := ext4.Digest(a, 500000)
	if err != nil {
		return fmt.Errorf("the seeded root file system: %v", err)
	}
	db, err := ext4.Digest(b, 500000)
	if err != nil {
		return fmt.Errorf("the payload's root file system: %v", err)
	}
	seed := map[string]bool{}
	for _, f := range files {
		seed[f.Path] = true
	}
	if madeFolder {
		seed[Folder] = true
	}
	for p, h := range db {
		if da[p] != h {
			return fmt.Errorf("%s differs from the payload's root file system after the seed", p)
		}
	}
	for p := range da {
		if _, ok := db[p]; !ok && !seed[p] {
			return fmt.Errorf("%s is new after the seed and not a seed file", p)
		}
	}
	for p := range seed {
		if _, ok := da[p]; !ok {
			return fmt.Errorf("%s is not there after the seed", p)
		}
	}
	return nil
}
