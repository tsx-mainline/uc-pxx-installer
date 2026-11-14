//go:build install

// Package ram keeps the person's ssh key of this boot in RAM (the first
// drive phase to the direct plan; package keywrite).
package ram

// The key of this boot in RAM: the first drive phase (the stash) reads
// SSH-KEY.PUB from the top folder of the UC-PXX-INST drive next to the .bin
// (the drive is made new and empty after it), checks it (package sshkey) and
// keeps it here; the direct plan of the second phase takes it from here.
// The folder holds exactly key.pub (the normalised line) and key.json (this
// boot, this RAM image, the fingerprint); it is valid in this boot only.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"

	"uc-pxx-installer/internal/sshkey"
)

// RAMDir is the folder of the key of this boot (root, 0700).
const RAMDir = "/run/uc-pxx-inst/sshkey"

type ramIndex struct {
	Kind        string `json:"kind"`
	BootID      string `json:"boot_id"`
	RAMToken    string `json:"ram_token"`
	Type        string `json:"type"`
	Bits        int    `json:"bits"`
	Fingerprint string `json:"fingerprint"`
	FileSHA256  string `json:"file_sha256"`
	Source      string `json:"source"`
}

const ramKind = "uc-pxx-inst-ssh-key"

// ClearRAM removes the key of this boot (a new first phase without a key).
func ClearRAM(dir string) error {
	for _, n := range []string{"key.pub", "key.json"} {
		if err := os.Remove(filepath.Join(dir, n)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.Remove(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// SaveRAM keeps k for this boot.
func SaveRAM(dir string, k *sshkey.Key, bootID, token, source string) error {
	if err := ClearRAM(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	f := k.File()
	s := sha256.Sum256(f)
	ix, _ := json.Marshal(ramIndex{Kind: ramKind, BootID: bootID, RAMToken: token, Type: k.Type, Bits: k.Bits, Fingerprint: k.Fingerprint,
		FileSHA256: hex.EncodeToString(s[:]), Source: source})
	for _, w := range []struct {
		n string
		b []byte
	}{{"key.pub", f}, {"key.json", append(ix, '\n')}} {
		if err := os.WriteFile(filepath.Join(dir, w.n), w.b, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// LoadRAM gives the key of this boot, or nil when there is none. A folder
// or file that is not exactly what SaveRAM makes, or a key of another boot
// or RAM image, is an error (the plan refuses; nothing is written).
func LoadRAM(dir, bootID, token string) (*sshkey.Key, string, error) {
	fi, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	if !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		return nil, "", fmt.Errorf("%s is not a folder 0700 (%v)", dir, fi.Mode())
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, "", err
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if len(names) != 2 || names[0] != "key.json" || names[1] != "key.pub" {
		return nil, "", fmt.Errorf("%s holds %v, not exactly key.json and key.pub", dir, names)
	}
	read := func(n string) ([]byte, error) {
		p := filepath.Join(dir, n)
		fi, err := os.Lstat(p)
		if err != nil {
			return nil, err
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 || fi.Size() > sshkey.MaxBytes || !ok || st.Uid != uint32(os.Geteuid()) {
			return nil, fmt.Errorf("%s is not a regular file 0600 of this user, at most %d bytes", p, sshkey.MaxBytes)
		}
		return os.ReadFile(p)
	}
	kb, err := read("key.pub")
	if err != nil {
		return nil, "", err
	}
	jb, err := read("key.json")
	if err != nil {
		return nil, "", err
	}
	var ix ramIndex
	dec := json.NewDecoder(bytesReader(jb))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ix); err != nil {
		return nil, "", fmt.Errorf("key.json: %v", err)
	}
	k, err := sshkey.Parse(kb)
	if err != nil {
		return nil, "", fmt.Errorf("key.pub: %v", err)
	}
	s := sha256.Sum256(kb)
	switch {
	case ix.Kind != ramKind || string(k.File()) != string(kb):
		return nil, "", errors.New("key.json or key.pub is not the installer's")
	case ix.BootID != bootID || ix.RAMToken != token:
		return nil, "", fmt.Errorf("the ssh key is of boot %s / RAM image %s, this is %s / %s", ix.BootID, ix.RAMToken, bootID, token)
	case ix.Fingerprint != k.Fingerprint || ix.Type != k.Type || ix.Bits != k.Bits || ix.FileSHA256 != hex.EncodeToString(s[:]):
		return nil, "", errors.New("key.json does not describe key.pub")
	}
	return k, ix.Source, nil
}

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
