// Package clonedelta applies the clone's pinned difference (clone.Delta,
// clone-delta.json) to the tree under a folder: step 2 runs it on system_a
// mounted at clone.MountPoint after the block copy (DESIGN-R23 §4.3 item 6,
// §4.4). It writes only under that folder and never follows a symbolic
// link: every component before the last must be a folder (checked with
// lstat; nothing else writes the private mount while it runs), the last one
// is removed or changed as what lstat found (os.RemoveAll does not follow a
// link).
package clonedelta

import (
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"syscall"

	"uc-pxx-installer/internal/clone"
)

// Apply applies d to the tree under dir in the order of clone.Delta. buildID
// is the content of every Write entry with from "build-id".
func Apply(dir string, d *clone.Delta, buildID string) error {
	if d == nil {
		return errors.New("no difference")
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a folder", dir)
	}
	r := root(dir)
	for _, p := range d.Remove {
		if err := remove(r, p); err != nil {
			return fmt.Errorf("remove %s: %w", p, err)
		}
	}
	for _, p := range d.RemoveChildren {
		if err := children(r, p, false); err != nil {
			return fmt.Errorf("remove under %s: %w", p, err)
		}
	}
	for _, e := range d.Empty {
		if err := empty(r, e); err != nil {
			return fmt.Errorf("empty %s: %w", e.Path, err)
		}
	}
	for _, f := range d.Write {
		if f.From != "build-id" {
			return fmt.Errorf("write %s: from %q", f.Path, f.From)
		}
		if err := write(r, f, []byte(buildID)); err != nil {
			return fmt.Errorf("write %s: %w", f.Path, err)
		}
	}
	return nil
}

// root is the folder the difference is applied in; its methods take a path
// relative to it.
type root string

func (r root) at(rel string) string {
	if rel == "." || rel == "" {
		return string(r)
	}
	return string(r) + "/" + rel
}
func (r root) Lstat(rel string) (os.FileInfo, error) { return os.Lstat(r.at(rel)) }
func (r root) Remove(rel string) error               { return os.Remove(r.at(rel)) }
func (r root) RemoveAll(rel string) error            { return os.RemoveAll(r.at(rel)) }
func (r root) Mkdir(rel string, m os.FileMode) error { return os.Mkdir(r.at(rel), m) }
func (r root) Lchown(rel string, u, g int) error     { return os.Lchown(r.at(rel), u, g) }
func (r root) Rename(a, b string) error              { return os.Rename(r.at(a), r.at(b)) }
func (r root) OpenFile(rel string, flag int, m os.FileMode) (*os.File, error) {
	return os.OpenFile(r.at(rel), flag|syscall.O_NOFOLLOW, m)
}

// Chmod changes the mode of what lstat finds there, refusing a link.
func (r root) Chmod(rel string, m os.FileMode) error {
	fi, err := os.Lstat(r.at(rel))
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symbolic link", rel)
	}
	return os.Chmod(r.at(rel), m)
}

// parents checks that every component of p before the last is a folder (no
// link) and returns p relative to the root.
func parents(r root, p string) (string, error) {
	if !strings.HasPrefix(p, "/") || p == "/" || path.Clean(p) != p {
		return "", fmt.Errorf("path %q", p)
	}
	rel := strings.TrimPrefix(p, "/")
	parts := strings.Split(rel, "/")
	for i := 1; i < len(parts); i++ {
		q := strings.Join(parts[:i], "/")
		fi, err := r.Lstat(q)
		if err != nil {
			return "", err
		}
		if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("/%s is not a folder (%v)", q, fi.Mode())
		}
	}
	return rel, nil
}

func remove(r root, p string) error {
	rel, err := parents(r, p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	fi, err := r.Lstat(rel)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return r.RemoveAll(rel)
	}
	return r.Remove(rel)
}

// children removes every entry under the folder p (it must be a folder; a
// missing one is an error unless missingOK).
func children(r root, p string, missingOK bool) error {
	rel, err := parents(r, p)
	if err != nil {
		return err
	}
	fi, err := r.Lstat(rel)
	if errors.Is(err, os.ErrNotExist) && missingOK {
		return nil
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a folder (%v)", p, fi.Mode())
	}
	f, err := r.OpenFile(rel, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	names, err := f.Readdirnames(-1)
	f.Close()
	if err != nil {
		return err
	}
	for _, n := range names {
		if err := r.RemoveAll(rel + "/" + n); err != nil {
			return err
		}
	}
	return nil
}

func fileMode(m uint32) os.FileMode {
	fm := os.FileMode(m & 0o777)
	if m&0o4000 != 0 {
		fm |= os.ModeSetuid
	}
	if m&0o2000 != 0 {
		fm |= os.ModeSetgid
	}
	if m&0o1000 != 0 {
		fm |= os.ModeSticky
	}
	return fm
}

// own sets the owner when it differs (a test that does not run as root
// gives its own ids).
func own(r root, rel string, uid, gid uint32) error {
	fi, err := r.Lstat(rel)
	if err != nil {
		return err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Uid == uid && st.Gid == gid {
		return nil
	}
	return r.Lchown(rel, int(uid), int(gid))
}

func empty(r root, e clone.Dir) error {
	m, err := clone.ParseMode(e.Mode)
	if err != nil {
		return err
	}
	rel, err := parents(r, e.Path)
	if err != nil {
		return err
	}
	if _, err := r.Lstat(rel); errors.Is(err, os.ErrNotExist) {
		if err := r.Mkdir(rel, 0o700); err != nil {
			return err
		}
	}
	if err := children(r, e.Path, false); err != nil {
		return err
	}
	if err := own(r, rel, e.UID, e.GID); err != nil {
		return err
	}
	return r.Chmod(rel, fileMode(m))
}

func write(r root, f clone.File, b []byte) error {
	m, err := clone.ParseMode(f.Mode)
	if err != nil {
		return err
	}
	rel, err := parents(r, f.Path)
	if err != nil {
		return err
	}
	if fi, err := r.Lstat(rel); err == nil && !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is there and not a regular file (%v)", f.Path, fi.Mode())
	}
	tmp := rel + ".clone-tmp"
	r.Remove(tmp)
	w, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	_, werr := w.Write(b)
	serr := w.Sync()
	cerr := w.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		r.Remove(tmp)
		return err
	}
	if err := own(r, tmp, f.UID, f.GID); err != nil {
		r.Remove(tmp)
		return err
	}
	if err := r.Chmod(tmp, fileMode(m)); err != nil {
		r.Remove(tmp)
		return err
	}
	if err := r.Rename(tmp, rel); err != nil {
		r.Remove(tmp)
		return err
	}
	dir := path.Dir(rel)
	if dir == "" {
		dir = "."
	}
	d, err := r.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
