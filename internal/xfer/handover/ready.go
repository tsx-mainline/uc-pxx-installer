package xfer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"syscall"

	"uc-pxx-installer/internal/mainline"
)

// The hand-over to the root step-2 service (the seal, component C2 of
// STEP2-USB-TRANSPORT §6; not in this package).
//
// After COMMIT the receiver's folder (Config.Dir) holds:
//
//	complete.json   the Record (written first)
//	complete/       exactly manifest.json, boot-template.img, rootfs.ext4
//
// and the receiver refuses every further PUT and COMMIT. The seal, as root:
//
//  1. stops uc-pxx-xfer (the supervisor ends its receiver child and waits for
//     it), so that no process of the receiver's user holds or changes a file;
//  2. calls CheckReady(dir, uid, pins) with the same pins the receiver got;
//  3. renames dir/complete to stage/payload/uc-pxx-mainline (one rename in the
//     same tmpfs), makes the folder and the three files root-owned and read-only
//     (0555 / 0444), and writes stage/payload/AUTO-STEP2 only when
//     Ready.Record.Auto;
//  4. starts uc-pxx-xfer again with --status-only.
//
// CheckReady does not hash the two large files again: the authority is pass 1
// of uc-pxx-step2 (the manifest's sha256s, the release gate), which reads the
// sealed, root-owned folder at plan and again at start.

// RecordVersion is the version of complete.json.
const RecordVersion = 1

// ReadyFile is one file of the ready folder.
type ReadyFile struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Record is complete.json.
type Record struct {
	Version        int         `json:"version"`
	ManifestSHA256 string      `json:"manifest_sha256"`
	Files          []ReadyFile `json:"files"` // manifest.json, boot-template.img, rootfs.ext4, in this order
	Auto           bool        `json:"auto"`
	Session        int         `json:"session"`
	Time           int64       `json:"time"`
}

// Ready is a checked hand-over.
type Ready struct {
	Folder string // dir/complete
	Record Record
}

var payloadNames = []string{ManifestName, TemplateName, RootfsName}

func ownedBy(fi os.FileInfo, uid int) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == uid
}

func nlink(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink)
	}
	return 0
}

// CheckReady checks the receiver's hand-over in dir: the record, and the ready
// folder with exactly the three files (regular, one link, owned by uid, the
// record's sizes), the manifest's sha256 one of pins and its content the
// record's. uid < 0 skips the owner checks (tests only).
func CheckReady(dir string, uid int, pins []string) (*Ready, error) {
	fi, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() || (uid >= 0 && !ownedBy(fi, uid)) {
		return nil, fmt.Errorf("%s: not the receiver's folder", dir)
	}
	b, err := readSmall(filepath.Join(dir, ReadyRecord), maxStateFile)
	if err != nil {
		return nil, err
	}
	var r Record
	if err := decodeStrict(b, &r); err != nil {
		return nil, fmt.Errorf("%s: %v", ReadyRecord, err)
	}
	if r.Version != RecordVersion {
		return nil, fmt.Errorf("%s: version %d", ReadyRecord, r.Version)
	}
	if len(r.Files) != len(payloadNames) {
		return nil, fmt.Errorf("%s: %d files", ReadyRecord, len(r.Files))
	}
	for i, f := range r.Files {
		if f.Name != payloadNames[i] || f.Size <= 0 || !mainline.IsSHA256(f.SHA256) {
			return nil, fmt.Errorf("%s: file %d is not %s with a size and a sha256", ReadyRecord, i, payloadNames[i])
		}
	}
	if r.ManifestSHA256 != r.Files[0].SHA256 || !pinned(pins, r.ManifestSHA256) {
		return nil, fmt.Errorf("%s: the manifest sha256 %s is not pinned", ReadyRecord, r.ManifestSHA256)
	}
	folder := filepath.Join(dir, ReadyDir)
	fi, err = os.Lstat(folder)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() || (uid >= 0 && !ownedBy(fi, uid)) {
		return nil, fmt.Errorf("%s: not a folder of the receiver", folder)
	}
	ents, err := os.ReadDir(folder)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	want := append([]string(nil), payloadNames...)
	sort.Strings(want)
	if fmt.Sprint(names) != fmt.Sprint(want) {
		return nil, fmt.Errorf("%s holds %v, not exactly %v", folder, names, want)
	}
	for _, f := range r.Files {
		p := filepath.Join(folder, f.Name)
		fi, err := os.Lstat(p)
		if err != nil {
			return nil, err
		}
		switch {
		case !fi.Mode().IsRegular():
			return nil, fmt.Errorf("%s: not a regular file", p)
		case nlink(fi) != 1:
			return nil, fmt.Errorf("%s: %d links", p, nlink(fi))
		case uid >= 0 && !ownedBy(fi, uid):
			return nil, fmt.Errorf("%s: not the receiver's file", p)
		case fi.Size() != f.Size:
			return nil, fmt.Errorf("%s: %d bytes, the record says %d", p, fi.Size(), f.Size)
		}
	}
	mb, err := readSmall(filepath.Join(folder, ManifestName), MaxManifest)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(mb)
	if hex.EncodeToString(sum[:]) != r.ManifestSHA256 {
		return nil, errors.New("the manifest is not the one of the record")
	}
	m, err := mainline.ParseManifest(mb)
	if err != nil {
		return nil, err
	}
	if m.BootTemplate.Size != r.Files[1].Size || m.BootTemplate.SHA256 != r.Files[1].SHA256 ||
		m.Rootfs.Size != r.Files[2].Size || m.Rootfs.SHA256 != r.Files[2].SHA256 {
		return nil, errors.New("the record's files are not the manifest's")
	}
	return &Ready{Folder: folder, Record: r}, nil
}

func pinned(pins []string, s string) bool {
	for _, p := range pins {
		if p == s && mainline.IsSHA256(p) {
			return true
		}
	}
	return false
}

// decodeStrict decodes one JSON object with only known fields and nothing
// after it (spaces of the frame padding are allowed).
func decodeStrict(b []byte, v any) error {
	b = bytes.TrimRight(b, " ")
	if len(b) == 0 {
		b = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("data after the object")
	}
	return nil
}
