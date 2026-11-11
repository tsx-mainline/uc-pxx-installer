package handover

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"syscall"

	"uc-pxx-installer/internal/mainline"
)

// The finish set (mainline-rc-4, owner 2026-11-11: Q-T8 = option 5). In a
// normal mainline build that is an unfinished step-2 target, the USB
// transport receives the PINNED finish executor of step 2 into RAM: the
// finish build of uc-pxx-step2 (uc-pxx-step2-finish: the finish and the
// target's part of the service, nothing else) and uc-pxx-bootctl-inst r2. The
// slot's own root file system names both, by size and sha256, in its finish
// list (FinishListPath, a file of the USB set that the both-slot gate pins):
// the page sends that same list first, and the receiver takes it only when
// its sha256 is the sha256 of the slot's own list. Then only the two files it
// names, with its sizes; their sha256 while the bytes arrive.
//
// The root side (the uc-pxx-usb service, internal/xfer RunUSB) stops the
// receiver, checks the hand-over (CheckReadyFinish), and copies the two files
// as root into a fresh root-only folder (SealFinish: FinishDir), hashing
// them again against the slot's own list. Only then it runs the finish
// executor from there. Nothing is installed: FinishDir is in /run (RAM).

const (
	// FinishListName is the list's name in the receiver's folder (and in the
	// release folder the page sends from: uc-pxx-finish/finish.json).
	FinishListName = "finish.json"
	// FinishListPath is the slot's own list in its root file system.
	FinishListPath = "/usr/share/uc-pxx-usb/finish.json"
	// FinishKind and FinishVersion of a list.
	FinishKind    = "uc-pxx-finish"
	FinishVersion = 1
	// FinishBin and FinishBootctl are the two files of the set, in this order.
	FinishBin     = "uc-pxx-step2-finish"
	FinishBootctl = "uc-pxx-bootctl-inst"
	// FinishDir is where the root side seals the set (root, 0700) and runs it
	// from. The finish build of uc-pxx-step2 reads r2 at FinishDir/FinishBootctl
	// (internal/device SlotBootctlPath of the finish build).
	FinishDir = "/run/uc-pxx-finish"
	// MaxFinishFile is the largest file of the set.
	MaxFinishFile = 32 << 20
	// MaxFinishList is the largest list.
	MaxFinishList = 4 << 10
	// RecordKindFinish is Record.Kind of a finish set ("" is a payload).
	RecordKindFinish = "finish"
)

// FinishNames are the files of a ready finish folder.
var FinishNames = []string{FinishListName, FinishBin, FinishBootctl}

// FinishList is finish.json.
type FinishList struct {
	Kind    string      `json:"kind"`
	Version int         `json:"version"`
	Files   []ReadyFile `json:"files"` // FinishBin, FinishBootctl, in this order
}

// ParseFinishList parses a finish list strictly: the kind, the version,
// exactly the two files in their order, each with a size of 1 to
// MaxFinishFile bytes and a sha256.
func ParseFinishList(b []byte) (*FinishList, error) {
	if len(b) == 0 || len(b) > MaxFinishList {
		return nil, fmt.Errorf("a finish list of %d bytes (1 to %d)", len(b), MaxFinishList)
	}
	var l FinishList
	if err := DecodeStrict(b, &l); err != nil {
		return nil, fmt.Errorf("the finish list: %v", err)
	}
	if l.Kind != FinishKind || l.Version != FinishVersion {
		return nil, fmt.Errorf("the finish list is kind %q version %d, not %s version %d", l.Kind, l.Version, FinishKind, FinishVersion)
	}
	want := []string{FinishBin, FinishBootctl}
	if len(l.Files) != len(want) {
		return nil, fmt.Errorf("the finish list names %d files, not %d", len(l.Files), len(want))
	}
	for i, f := range l.Files {
		if f.Name != want[i] || f.Size <= 0 || f.Size > MaxFinishFile || !mainline.IsSHA256(f.SHA256) {
			return nil, fmt.Errorf("the finish list's file %d is not %s with a size of 1 to %d bytes and a sha256", i, want[i], MaxFinishFile)
		}
	}
	return &l, nil
}

// ReadFinishList reads the slot's own list (root-owned, a regular file, no
// link followed) and returns it with its sha256 (the receiver's one pin).
func ReadFinishList(p string) (*FinishList, string, error) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, "", err
	}
	if u, ok := ownerOf(fi); !fi.Mode().IsRegular() || !ok || u != 0 || fi.Mode().Perm()&0o022 != 0 {
		return nil, "", fmt.Errorf("%s: not a regular file of root that only root can write", p)
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxFinishList+1))
	if err != nil {
		return nil, "", err
	}
	l, err := ParseFinishList(b)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %v", p, err)
	}
	s := sha256.Sum256(b)
	return l, hex.EncodeToString(s[:]), nil
}

// CheckReadyFinish checks the receiver's hand-over of a finish set in dir:
// the record (kind finish), the ready folder with exactly the list and the two
// files (regular, one link, owned by uid, the record's sizes), the list's
// sha256 the pin and its content the record's. uid < 0 skips the owner checks
// (tests only). The two files' sha256 are checked again by SealFinish, as
// root, on the bytes it copies.
func CheckReadyFinish(dir string, uid int, pin string) (*Ready, *FinishList, error) {
	fi, err := os.Lstat(dir)
	if err != nil {
		return nil, nil, err
	}
	if !fi.IsDir() || (uid >= 0 && !ownedBy(fi, uid)) {
		return nil, nil, fmt.Errorf("%s: not the receiver's folder", dir)
	}
	b, err := ReadSmall(filepath.Join(dir, ReadyRecord), MaxStateFile)
	if err != nil {
		return nil, nil, err
	}
	var r Record
	if err := DecodeStrict(b, &r); err != nil {
		return nil, nil, fmt.Errorf("%s: %v", ReadyRecord, err)
	}
	if r.Version != RecordVersion || r.Kind != RecordKindFinish || r.Auto {
		return nil, nil, fmt.Errorf("%s: version %d kind %q auto %v: not a finish set", ReadyRecord, r.Version, r.Kind, r.Auto)
	}
	if len(r.Files) != len(FinishNames) {
		return nil, nil, fmt.Errorf("%s: %d files", ReadyRecord, len(r.Files))
	}
	for i, f := range r.Files {
		if f.Name != FinishNames[i] || f.Size <= 0 || !mainline.IsSHA256(f.SHA256) {
			return nil, nil, fmt.Errorf("%s: file %d is not %s with a size and a sha256", ReadyRecord, i, FinishNames[i])
		}
	}
	if !mainline.IsSHA256(pin) || r.ManifestSHA256 != pin || r.Files[0].SHA256 != pin {
		return nil, nil, fmt.Errorf("%s: the list sha256 %s is not the slot's own list %s", ReadyRecord, r.ManifestSHA256, pin)
	}
	folder := filepath.Join(dir, ReadyDir)
	if err := checkFolder(folder, uid, FinishNames, r.Files); err != nil {
		return nil, nil, err
	}
	lb, err := ReadSmall(filepath.Join(folder, FinishListName), MaxFinishList)
	if err != nil {
		return nil, nil, err
	}
	s := sha256.Sum256(lb)
	if hex.EncodeToString(s[:]) != pin {
		return nil, nil, errors.New("the finish list in the folder is not the slot's own list")
	}
	l, err := ParseFinishList(lb)
	if err != nil {
		return nil, nil, err
	}
	for i, f := range l.Files {
		if r.Files[i+1] != f {
			return nil, nil, fmt.Errorf("the record's %s is not the list's", f.Name)
		}
	}
	return &Ready{Folder: folder, Record: r}, l, nil
}

// checkFolder: folder holds exactly names, each a regular file with one link,
// owned by uid (uid >= 0), with the size of files.
func checkFolder(folder string, uid int, names []string, files []ReadyFile) error {
	fi, err := os.Lstat(folder)
	if err != nil {
		return err
	}
	if !fi.IsDir() || (uid >= 0 && !ownedBy(fi, uid)) {
		return fmt.Errorf("%s: not a folder of the receiver", folder)
	}
	ents, err := os.ReadDir(folder)
	if err != nil {
		return err
	}
	var got []string
	for _, e := range ents {
		got = append(got, e.Name())
	}
	sort.Strings(got)
	want := append([]string(nil), names...)
	sort.Strings(want)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		return fmt.Errorf("%s holds %v, not exactly %v", folder, got, want)
	}
	for _, f := range files {
		p := filepath.Join(folder, f.Name)
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		switch {
		case !fi.Mode().IsRegular():
			return fmt.Errorf("%s: not a regular file", p)
		case nlink(fi) != 1:
			return fmt.Errorf("%s: %d links", p, nlink(fi))
		case uid >= 0 && !ownedBy(fi, uid):
			return fmt.Errorf("%s: not the receiver's file", p)
		case fi.Size() != f.Size:
			return fmt.Errorf("%s: %d bytes, the record says %d", p, fi.Size(), f.Size)
		}
	}
	return nil
}

// SealFinish copies the two files of a checked finish folder (CheckReadyFinish)
// into dst, a NEW folder (root, 0700; it must not exist), as root-owned files
// of mode 0700, and checks the sha256 of the bytes it wrote against the
// slot's own list l. The receiver's files are opened without following a
// link and must be regular files of the list's size; the copy is read back
// and hashed again after an fsync. Any difference removes dst and fails:
// nothing is run. The receiver must be stopped before (the caller checks that
// no process of its user is left).
func SealFinish(folder string, l *FinishList, dst string) (err error) {
	if l == nil || len(l.Files) != 2 {
		return errors.New("no finish list")
	}
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("%s exists: a finish set was sealed before in this boot (or staged by hand)", dst)
	}
	if err := os.Mkdir(dst, 0o700); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(dst)
		}
	}()
	if err := os.Chmod(dst, 0o700); err != nil {
		return err
	}
	for _, f := range l.Files {
		if err := copyChecked(filepath.Join(folder, f.Name), filepath.Join(dst, f.Name), f); err != nil {
			return fmt.Errorf("%s: %w", f.Name, err)
		}
	}
	return syncDir(dst)
}

func copyChecked(src, dst string, f ReadyFile) error {
	in, err := os.OpenFile(src, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() || nlink(fi) != 1 || fi.Size() != f.Size {
		return fmt.Errorf("not a regular file of %d bytes with one link", f.Size)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o700)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, cerr := io.Copy(io.MultiWriter(out, h), io.LimitReader(in, f.Size+1))
	if cerr == nil {
		cerr = out.Chmod(0o700)
	}
	if cerr == nil {
		cerr = out.Sync()
	}
	if e := out.Close(); cerr == nil {
		cerr = e
	}
	if cerr != nil {
		return cerr
	}
	if n != f.Size || hex.EncodeToString(h.Sum(nil)) != f.SHA256 {
		return fmt.Errorf("%d bytes with sha256 %x, the slot's list says %d bytes %s", n, h.Sum(nil), f.Size, f.SHA256)
	}
	// read the copy back: the bytes that will run
	back, err := os.ReadFile(dst)
	if err != nil {
		return err
	}
	s := sha256.Sum256(back)
	if int64(len(back)) != f.Size || hex.EncodeToString(s[:]) != f.SHA256 || !bytes.Equal(s[:], h.Sum(nil)) {
		return errors.New("the copy read back is not the list's file")
	}
	return nil
}

func syncDir(d string) error {
	f, err := os.Open(d)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
