package provision

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"uc-pxx-installer/internal/provision/bundle"
)

// RepoName is the root's repo folder in the stage: the bundle's members.
const RepoName = "repo"

// dirSink writes members into a root folder (the net download).
type dirSink struct{ dir string }

func memberFile(dir, p string) (string, error) {
	if p == "" || strings.HasPrefix(p, "/") || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") {
		return "", fmt.Errorf("member path %q", p)
	}
	return filepath.Join(dir, filepath.FromSlash(p)), nil
}

func (s dirSink) Create(p string, size int64) (io.WriteCloser, error) {
	full, err := memberFile(s.dir, p)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o644)
}

func (s dirSink) Remove(p string) error {
	full, err := memberFile(s.dir, p)
	if err != nil {
		return err
	}
	return os.Remove(full)
}

// freshRepo removes dir and makes it again (root, 0700).
func freshRepo(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.Mkdir(dir, 0o700)
}

// takeOver makes the receiver's handed-over folder the root's repo: it
// renames it into place (one tmpfs), then, without following a link, makes
// every entry the service's own (root: uc-pxx-provision runs only as root;
// folders 0755, files 0644); any entry that is not a folder or a regular
// file refuses. The receiver is stopped before (no process of its user is
// left).
func takeOver(from, repo string) error {
	uid, gid := os.Geteuid(), os.Getegid()
	if err := os.RemoveAll(repo); err != nil {
		return err
	}
	if err := os.Rename(from, repo); err != nil {
		return err
	}
	return filepath.WalkDir(repo, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		switch {
		case fi.IsDir():
			mode = 0o755
			if p == repo {
				mode = 0o700
			}
		case !fi.Mode().IsRegular():
			return fmt.Errorf("%s: not a folder or a regular file", p)
		}
		if err := os.Lchown(p, uid, gid); err != nil {
			return err
		}
		return os.Chmod(p, mode)
	})
}

// verifyRepo checks the repo folder as the root reads it, before any member
// is used: bundle.json with the pinned sha256 (index_sha256), parsed against
// profiles.json; then exactly its members and their folders, each member a
// regular file with one link, its size and its sha256; nothing else.
func verifyRepo(repo string, pins bundle.Pins) (*bundle.Doc, []byte, error) {
	db, err := readRegular(filepath.Join(repo, bundle.DocName), bundle.MaxDoc)
	if err != nil {
		return nil, nil, err
	}
	if s := sha256.Sum256(db); hex.EncodeToString(s[:]) != pins.IndexSHA256 {
		return nil, nil, fmt.Errorf("bundle.json has sha256 %x, profiles.json pins %s", s, pins.IndexSHA256)
	}
	doc, err := bundle.ParseDoc(db, pins.Expect)
	if err != nil {
		return nil, nil, err
	}
	want := map[string]bundle.Member{bundle.DocName: {Path: bundle.DocName, Size: int64(len(db)), SHA256: pins.IndexSHA256}}
	dirs := map[string]bool{".": true}
	for _, m := range doc.Members() {
		want[m.Path] = m
		for d := path.Dir(m.Path); d != "."; d = path.Dir(d) {
			dirs[d] = true
		}
	}
	seen := 0
	err = filepath.WalkDir(repo, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(repo, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if fi.IsDir() {
			if !dirs[rel] {
				return fmt.Errorf("the repo holds the folder %s, which bundle.json does not name", rel)
			}
			return nil
		}
		m, ok := want[rel]
		st, sok := fi.Sys().(*syscall.Stat_t)
		switch {
		case !ok:
			return fmt.Errorf("the repo holds %s, which bundle.json does not name", rel)
		case !fi.Mode().IsRegular() || !sok || st.Nlink != 1:
			return fmt.Errorf("%s: not a regular file with one link", rel)
		case fi.Size() != m.Size:
			return fmt.Errorf("%s: %d bytes, bundle.json says %d", rel, fi.Size(), m.Size)
		}
		f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}
		h := sha256.New()
		n, err := io.Copy(h, f)
		f.Close()
		if err != nil {
			return err
		}
		if n != m.Size || hex.EncodeToString(h.Sum(nil)) != m.SHA256 {
			return fmt.Errorf("%s: sha256 %x of %d bytes, bundle.json says %s", rel, h.Sum(nil), n, m.SHA256)
		}
		seen++
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if seen != len(want) {
		return nil, nil, fmt.Errorf("the repo holds %d of the %d files of bundle.json", seen, len(want))
	}
	return doc, db, nil
}

// repositories is the unit's repositories file: the bundle's four folders
// under repo, in the order of its indexes (never /etc/apk/repositories).
func repositories(repo string, doc *bundle.Doc) []byte {
	var b strings.Builder
	for _, d := range doc.RepoDirs() {
		b.WriteString(filepath.Join(repo, filepath.FromSlash(d)) + "\n")
	}
	return []byte(b.String())
}

// openMember opens a member of the repo (InstalledSize).
func openMember(repo string) func(string) (io.ReadCloser, error) {
	return func(p string) (io.ReadCloser, error) {
		full, err := memberFile(repo, p)
		if err != nil {
			return nil, err
		}
		return os.OpenFile(full, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	}
}
