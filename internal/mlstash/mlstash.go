// Package mlstash is the RAM stash of a mainline install (MAINLINE-INT-2026-11-10.md,
// tasks 2 and 3): what the first drive phase of a mainline install takes from
// the reviewed 1.0.0.94 userdebug firmware (.bin) before the UC-PXX-INST drive is
// given back for the uc-pxx-mainline folder. The drive is 1200 MiB; the .bin
// (928 MiB) and the mainline folder (about 1117 MiB) do not fit together
// (PAGE-DRIVEN-INSTALL-DESIGN.md §2.2, Q3).
//
// The stash holds, in one folder of the RAM image's /run (tmpfs, root, 0700):
//
//   - the five boot-chain parts of that firmware: uboot.img, trust.img,
//     dtbo.img, vbmeta.img (the signed vbmeta, never the patch) and
//     resource.bin (the resource, "second", of its boot image), each with its
//     size and sha256 in stash.json; the mainline planner checks each against
//     the reviewed firmware manifest and the payload's requires_bootchain;
//   - key.rec: the key record of the firmware key (package keykeep, 512
//     bytes) that the first phase proved against the whole firmware (pcdfw:
//     the key decrypts the container and passes pass 1). The record bytes are
//     the key: they are never printed or logged, and they never leave /run;
//     only their sha256 is in stash.json and in a plan;
//   - stash.json: the firmware (file name, size, sha256, version, build type,
//     U-Boot build, the drive identity it was read from), the boot id and the
//     RAM image token of the run that made it, the files, and the key source.
//
// A stash is valid only in the boot that made it (the boot id): it is RAM
// only. Load checks every file against stash.json, refuses any other file,
// a symlink, a file that is not regular or not 0600, and a folder that is
// not 0700. Nothing here writes the eMMC.
package mlstash

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

	"uc-pxx-installer/internal/keykeep"
)

const (
	// Kind is the kind of stash.json.
	Kind = "p10inst-mainline-stash"
	// Schema is the version of stash.json.
	Schema = 1
	// Dir is the stash folder of the RAM image.
	Dir = "/run/uc-pxx-inst/mlstash"
	// Index is the name of the index file.
	Index = "stash.json"
	// KeyFile is the name of the key record file.
	KeyFile = "key.rec"
	// maxPart caps one stashed part (the resource and U-Boot are a few MiB).
	maxPart = 64 << 20
)

// Parts are the stashed boot-chain parts, in order, and their file names.
var Parts = []string{"uboot", "trust", "dtbo", "vbmeta", "resource"}

// FileName is the file of a part.
func FileName(part string) string {
	if part == "resource" {
		return "resource.bin"
	}
	return part + ".img"
}

// File is one stashed file.
type File struct {
	Part   string `json:"part"`
	Name   string `json:"file"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Firmware is the firmware the stash was made from.
type Firmware struct {
	File       string `json:"file"` // the manifest's file name
	Name       string `json:"name"` // the manifest's name ("1.0.0.94 (userdebug)")
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	Version    string `json:"version"`
	BuildType  string `json:"build_type"`
	UBootBuild string `json:"uboot_build"`
	Ident      string `json:"source_ident"` // the drive (or file) identity it was read from
}

// Key is what the stash says of the key record (never the key).
type Key struct {
	// Source is where the first phase found the key: "updater libraries in
	// system_a and system_b", "key file", "the key kept on the unit (backup
	// partition)".
	Source string `json:"source"`
	// SystemCopies are the slots whose system held the key at the first phase.
	SystemCopies []string `json:"system_copies"`
	RecordSHA256 string   `json:"record_sha256"`
	File         string   `json:"file"`
}

// Stash is stash.json.
type Stash struct {
	Schema   int      `json:"schema"`
	Kind     string   `json:"kind"`
	BootID   string   `json:"boot_id"`
	RAMToken string   `json:"ram_token"`
	Firmware Firmware `json:"firmware"`
	Files    []File   `json:"files"`
	Key      Key      `json:"key"`
}

// Loaded is a checked stash: the index, its sha256 and the bytes.
type Loaded struct {
	Stash
	IndexSHA256 string
	data        map[string][]byte
	record      []byte
}

// Part is the bytes of a stashed part (nil if none).
func (l *Loaded) Part(part string) []byte {
	if l == nil {
		return nil
	}
	return l.data[part]
}

// Record is the key record (512 bytes). Never print or log it.
func (l *Loaded) Record() []byte {
	if l == nil {
		return nil
	}
	return l.record
}

// FileOf is the index entry of a part.
func (s *Stash) FileOf(part string) (File, bool) {
	for _, f := range s.Files {
		if f.Part == part {
			return f, true
		}
	}
	return File{}, false
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Write makes the stash in dir: a new folder (it must not exist), written
// as dir+".new" and renamed when every file is written and synced. parts
// holds the bytes of every part of Parts; record is the key record.
func Write(dir string, st Stash, parts map[string][]byte, record []byte) (*Loaded, error) {
	if _, err := os.Lstat(dir); err == nil {
		return nil, fmt.Errorf("%s exists already: a stash is made once per boot", dir)
	}
	if _, err := keykeep.Decode(record); err != nil || len(record) != 512 {
		return nil, fmt.Errorf("the key record does not decode: %v", err)
	}
	st.Schema, st.Kind, st.Files = Schema, Kind, nil
	for _, p := range Parts {
		b := parts[p]
		if len(b) == 0 || len(b) > maxPart {
			return nil, fmt.Errorf("the part %s has %d bytes", p, len(b))
		}
		st.Files = append(st.Files, File{Part: p, Name: FileName(p), Size: int64(len(b)), SHA256: sha(b)})
	}
	if len(parts) != len(Parts) {
		return nil, fmt.Errorf("%d parts, want %d", len(parts), len(Parts))
	}
	st.Key.File, st.Key.RecordSHA256 = KeyFile, keykeep.SHA256(record)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return nil, err
	}
	tmp := dir + ".new"
	os.RemoveAll(tmp)
	if err := os.Mkdir(tmp, 0o700); err != nil {
		return nil, err
	}
	put := func(name string, b []byte) error {
		f, err := os.OpenFile(filepath.Join(tmp, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
		if err != nil {
			return err
		}
		_, err = f.Write(b)
		if serr := f.Sync(); err == nil {
			err = serr
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		return err
	}
	for _, f := range st.Files {
		if err := put(f.Name, parts[f.Part]); err != nil {
			os.RemoveAll(tmp)
			return nil, err
		}
	}
	if err := put(KeyFile, record); err != nil {
		os.RemoveAll(tmp)
		return nil, err
	}
	idx, _ := json.MarshalIndent(st, "", "  ")
	if err := put(Index, append(idx, '\n')); err != nil {
		os.RemoveAll(tmp)
		return nil, err
	}
	if err := os.Rename(tmp, dir); err != nil {
		os.RemoveAll(tmp)
		return nil, err
	}
	return Load(dir)
}

// strictUnmarshal: b is exactly one JSON value of v's shape with no unknown key.
func strictUnmarshal(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("more than one JSON value")
	}
	return nil
}

func readFile(dir, name string, max int64) ([]byte, error) {
	p := filepath.Join(dir, name)
	// O_NONBLOCK: a FIFO in the stash does not block the open (review T2
	// MINOR-4); the Stat below refuses it
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm() != 0o600 || st.Size() > max {
		return nil, fmt.Errorf("%s: not a regular 0600 file of at most %d bytes (%v, %d bytes)", name, max, st.Mode(), st.Size())
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) != st.Size() {
		return nil, fmt.Errorf("%s changed while it was read", name)
	}
	return b, nil
}

// Load reads and checks a stash. It does not check the boot id (the caller
// compares it with this boot's).
func Load(dir string) (*Loaded, error) {
	st, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() || st.Mode().Perm() != 0o700 {
		return nil, fmt.Errorf("%s is not a 0700 folder (%v)", dir, st.Mode())
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	want := []string{Index, KeyFile}
	for _, p := range Parts {
		want = append(want, FileName(p))
	}
	var got []string
	for _, e := range ents {
		if !e.Type().IsRegular() {
			return nil, fmt.Errorf("%s in the stash is not a regular file (%v)", e.Name(), e.Type())
		}
		got = append(got, e.Name())
	}
	sort.Strings(want)
	sort.Strings(got)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		return nil, fmt.Errorf("the stash holds %v, want exactly %v", got, want)
	}
	idx, err := readFile(dir, Index, 1<<20)
	if err != nil {
		return nil, err
	}
	l := &Loaded{IndexSHA256: sha(idx), data: map[string][]byte{}}
	if err := strictUnmarshal(idx, &l.Stash); err != nil {
		return nil, fmt.Errorf("%s: %v", Index, err)
	}
	s := &l.Stash
	if s.Schema != Schema || s.Kind != Kind || len(s.Files) != len(Parts) || s.BootID == "" {
		return nil, fmt.Errorf("%s: schema %d kind %q, %d files, boot id %q: not a stash of this installer", Index, s.Schema, s.Kind,
			len(s.Files), s.BootID)
	}
	for i, p := range Parts {
		f := s.Files[i]
		if f.Part != p || f.Name != FileName(p) {
			return nil, fmt.Errorf("%s: file %d is %s %s, want %s %s", Index, i, f.Part, f.Name, p, FileName(p))
		}
		b, err := readFile(dir, f.Name, maxPart)
		if err != nil {
			return nil, err
		}
		if int64(len(b)) != f.Size || sha(b) != f.SHA256 {
			return nil, fmt.Errorf("%s: %d bytes sha256 %s, the index says %d bytes %s", f.Name, len(b), sha(b), f.Size, f.SHA256)
		}
		l.data[p] = b
	}
	rec, err := readFile(dir, KeyFile, 512)
	if err != nil {
		return nil, err
	}
	if _, err := keykeep.Decode(rec); err != nil || s.Key.File != KeyFile || keykeep.SHA256(rec) != s.Key.RecordSHA256 {
		return nil, fmt.Errorf("%s: not the key record of the index (%v)", KeyFile, err)
	}
	l.record = rec
	return l, nil
}
