package provision

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"uc-pxx-installer/internal/clone"
	"uc-pxx-installer/internal/sshkey"
	"uc-pxx-installer/internal/xfer/handover"
)

// The records of the service on the running root file system (its own
// folder; DESIGN-R23 §3.4 item 7, R23-FORMATS §3).
const (
	StateDir = "/var/lib/uc-pxx-provision"
	// FixedFile: the profile is fixed for this unit (written durably before
	// apk add starts the first time; a later choice must name it).
	FixedFile = StateDir + "/fixed.json"
	// ChoiceFile: the last choice with the source net (the next boot starts
	// it again without the page; DESIGN-R23 §3.6).
	ChoiceFile = StateDir + "/choice.json"
	// DoneTmp is the done record before the service gives the USB link back
	// (step 2 reads clone.DoneRecordPath only once the link is free).
	DoneTmp = StateDir + "/.done.tmp"
	// InstalledDB is apk's installed database.
	InstalledDB = "/lib/apk/db/installed"
)

// FixedKind is the "kind" of fixed.json.
const FixedKind = "uc-pxx-provision-fixed"

// Fixed is fixed.json.
type Fixed struct {
	Schema       int    `json:"schema"`
	Kind         string `json:"kind"`
	Release      string `json:"release"`
	Profile      string `json:"profile"`
	BundleSHA256 string `json:"bundle_sha256"`
	Time         int64  `json:"time"`
}

// readRegular reads a regular file of at most max bytes, without following
// a link.
func readRegular(p string, max int64) ([]byte, error) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", p)
	}
	if fi.Size() > max {
		return nil, fmt.Errorf("%s: %d bytes (at most %d)", p, fi.Size(), max)
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s: more than %d bytes", p, max)
	}
	return b, nil
}

// writeDurable writes b to p (root, mode) by a new temporary file in the same
// folder, fsync, rename, and an fsync of the folder: the file is on the eMMC
// when it returns. The folder is made (0755) when missing.
func writeDurable(p string, b []byte, mode os.FileMode) error {
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+filepath.Base(p)+".new")
	os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	return syncDir(dir)
}

// writeRepositories writes the apk repositories file (RAM; mode 0644). It
// takes only that file (the env root + RepositoriesFile: the path must end in
// it) and follows no link (release review MINOR-1: no general writer).
func writeRepositories(p string, b []byte) error {
	if !strings.HasSuffix(p, RepositoriesFile) {
		return fmt.Errorf("%s is not the repositories file %s", p, RepositoriesFile)
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// renameDurable renames a written file into place and syncs its folder.
func renameDurable(from, to string) error {
	if err := os.Rename(from, to); err != nil {
		return err
	}
	return syncDir(filepath.Dir(to))
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// readFixed reads fixed.json (nil, nil: none).
func readFixed(p string) (*Fixed, error) {
	b, err := readRegular(p, 4096)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f Fixed
	if err := handover.DecodeStrict(bytes.TrimRight(b, "\n"), &f); err != nil {
		return nil, fmt.Errorf("%s: %v", FixedFile, err)
	}
	if f.Schema != 1 || f.Kind != FixedKind || f.Profile == "" {
		return nil, fmt.Errorf("%s: schema %d kind %q profile %q", FixedFile, f.Schema, f.Kind, f.Profile)
	}
	return &f, nil
}

// doneFor parses a done record and says whether it is the done record of an
// offered profile of this release (its ref_b and bundle).
func doneFor(b []byte, ps *Profiles, offered []Profile) (*clone.DoneRecord, error) {
	d, err := clone.ParseDone(b)
	if err != nil {
		return nil, err
	}
	p, ok := find(offered, d.Profile)
	switch {
	case !ok:
		return d, fmt.Errorf("the done record names the profile %q, which this unit does not offer", d.Profile)
	case d.Release != ps.Release || d.RefBSHA256 != p.RefB || d.BundleSHA256 != p.BundleSHA256() || d.InstalledSHA256 != p.Installed:
		return d, fmt.Errorf("the done record (release %s, ref_b %s) is not the one of %s of this release", d.Release, d.RefBSHA256, p.Name)
	}
	return d, nil
}

// doneRecord is the done record of p (R23-FORMATS §3).
func doneRecord(ps *Profiles, p Profile) []byte {
	b, _ := json.Marshal(clone.DoneRecord{Schema: 1, Kind: clone.DoneRecordKind, Release: ps.Release, Profile: p.Name,
		BundleSHA256: p.BundleSHA256(), RefBSHA256: p.RefB, InstalledSHA256: p.Installed})
	return append(b, '\n')
}

// KeySeeded: a root ssh key was seeded in step 1 (DESIGN-R23 §2 item 1: the
// console profile needs one): /root/.ssh/authorized_keys holds a key line
// that the installer's key rule takes, or the seed /data/tsx/panel.conf has
// SSH_AUTHORIZED_KEY with such a key (tsx-config writes authorized_keys from
// it at the boot).
func KeySeeded(root string) bool {
	if b, err := readRegular(filepath.Join(root, "/root/.ssh/authorized_keys"), 64<<10); err == nil {
		sc := bufio.NewScanner(bytes.NewReader(b))
		for sc.Scan() {
			if l := strings.TrimSpace(sc.Text()); l != "" && !strings.HasPrefix(l, "#") {
				if _, err := sshkey.Parse([]byte(l + "\n")); err == nil {
					return true
				}
			}
		}
	}
	if b, err := readRegular(filepath.Join(root, "/data/tsx/panel.conf"), 64<<10); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(l), "SSH_AUTHORIZED_KEY="); ok {
				v = strings.Trim(v, "\"'")
				if _, err := sshkey.Parse([]byte(v + "\n")); v != "" && err == nil {
					return true
				}
			}
		}
	}
	return false
}

// readInstalled reads apk's installed database: name -> version, and the
// sha256 of the file (installed_sha256).
func readInstalled(p string) (map[string]string, string, error) {
	b, err := readRegular(p, 64<<20)
	if err != nil {
		return nil, "", err
	}
	m := map[string]string{}
	cur := ""
	for _, l := range strings.Split(string(b), "\n") {
		switch {
		case l == "":
			cur = ""
		case strings.HasPrefix(l, "P:"):
			cur = l[2:]
			if _, dup := m[cur]; dup {
				return nil, "", fmt.Errorf("%s lists %s twice", InstalledDB, cur)
			}
			m[cur] = ""
		case strings.HasPrefix(l, "V:") && cur != "":
			m[cur] = l[2:]
		}
	}
	for n, v := range m {
		if v == "" {
			return nil, "", fmt.Errorf("%s: %s has no version", InstalledDB, n)
		}
	}
	return m, clone.SHA256(b), nil
}
