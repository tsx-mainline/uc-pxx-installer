// Package sshkey checks the person's optional ssh public key for a new
// mainline slot (mainline-rc-8; owner 2026-11-13 ~19:30, D1): the one line of
// the file SSH-KEY.PUB in the top folder of the UC-PXX-INST drive. It is pure
// (no I/O). The rule equals the installer page's (web/site/js/sshkey.js; the
// shared vectors are web/test/unit/sshkey-vectors.json):
//
//   - exactly one line, an optional single trailing LF or CRLF, at most
//     MaxBytes bytes in all;
//   - `TYPE BASE64[ COMMENT]`, single spaces, no options before TYPE;
//   - TYPE ssh-ed25519: the blob is string "ssh-ed25519" + string of 32
//     bytes, nothing after; TYPE ssh-rsa: string "ssh-rsa" + mpint e + mpint
//     n, nothing after, n of 3072 to 16384 bits, e odd and at least 3, both
//     canonical positive mpints;
//   - BASE64 canonical standard base64 with padding;
//   - COMMENT optional, 1 to 64 characters of A-Z a-z 0-9 . _ @ + = : -.
//
// The fingerprint is OpenSSH's: "SHA256:" + base64 without padding of the
// sha256 of the blob. A plan records the fingerprint, never the key.
package sshkey

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
)

const (
	// FileName is the drive file (top folder of UC-PXX-INST).
	FileName = "SSH-KEY.PUB"
	// MaxBytes is the largest file.
	MaxBytes = 8192
	// RSAMinBits and RSAMaxBits bound the rsa modulus.
	RSAMinBits = 3072
	RSAMaxBits = 16384
)

var commentRe = regexp.MustCompile(`^[A-Za-z0-9._@+=:-]{1,64}$`)

// Key is a checked key.
type Key struct {
	Type        string // ssh-ed25519 or ssh-rsa
	Bits        int    // 256 for ed25519, the modulus length for rsa
	Comment     string
	Fingerprint string // SHA256:...
	// Line is the normalised line `TYPE BASE64[ COMMENT]` (no LF).
	Line string
}

// Parse checks the content of SSH-KEY.PUB.
func Parse(b []byte) (*Key, error) {
	if len(b) == 0 {
		return nil, errors.New("the file is empty")
	}
	if len(b) > MaxBytes {
		return nil, fmt.Errorf("the file has %d bytes, at most %d", len(b), MaxBytes)
	}
	s := string(b)
	switch {
	case strings.HasSuffix(s, "\r\n"):
		s = s[:len(s)-2]
	case strings.HasSuffix(s, "\n"):
		s = s[:len(s)-1]
	}
	if strings.ContainsAny(s, "\r\n") {
		return nil, errors.New("the file has more than one line")
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return nil, fmt.Errorf("the line has a byte 0x%02x that is not printable ASCII", s[i])
		}
	}
	f := strings.Split(s, " ")
	if len(f) < 2 || len(f) > 3 {
		return nil, errors.New("the line is not `TYPE BASE64[ COMMENT]` with single spaces")
	}
	for _, x := range f {
		if x == "" {
			return nil, errors.New("the line is not `TYPE BASE64[ COMMENT]` with single spaces")
		}
	}
	typ, b64 := f[0], f[1]
	if typ != "ssh-ed25519" && typ != "ssh-rsa" {
		return nil, fmt.Errorf("the key type %q is not ssh-ed25519 or ssh-rsa (no options before the type)", short(typ))
	}
	blob, err := base64.StdEncoding.Strict().DecodeString(b64)
	if err != nil || base64.StdEncoding.EncodeToString(blob) != b64 {
		return nil, errors.New("the key is not canonical base64")
	}
	k := &Key{Type: typ}
	if len(f) == 3 {
		if !commentRe.MatchString(f[2]) {
			return nil, errors.New("the comment has a character outside A-Z a-z 0-9 . _ @ + = : - or more than 64 characters")
		}
		k.Comment = f[2]
	}
	r := bytes.NewReader(blob)
	name, err := str(r)
	if err != nil || string(name) != typ {
		return nil, errors.New("the key's blob does not start with its type")
	}
	switch typ {
	case "ssh-ed25519":
		pk, err := str(r)
		if err != nil || len(pk) != 32 {
			return nil, errors.New("the ed25519 key is not 32 bytes")
		}
		k.Bits = 256
	case "ssh-rsa":
		e, err := mpint(r)
		if err != nil {
			return nil, fmt.Errorf("the rsa exponent: %v", err)
		}
		n, err := mpint(r)
		if err != nil {
			return nil, fmt.Errorf("the rsa modulus: %v", err)
		}
		if e.Bit(0) == 0 || e.Cmp(big.NewInt(3)) < 0 {
			return nil, errors.New("the rsa exponent is not odd and at least 3")
		}
		k.Bits = n.BitLen()
		if k.Bits < RSAMinBits || k.Bits > RSAMaxBits {
			return nil, fmt.Errorf("the rsa key has %d bits: %d to %d", k.Bits, RSAMinBits, RSAMaxBits)
		}
	}
	if r.Len() != 0 {
		return nil, errors.New("the key's blob has bytes after the key")
	}
	sum := sha256.Sum256(blob)
	k.Fingerprint = "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
	k.Line = typ + " " + b64
	if k.Comment != "" {
		k.Line += " " + k.Comment
	}
	return k, nil
}

// File is the normalised content of SSH-KEY.PUB (the line and one LF).
func (k *Key) File() []byte { return []byte(k.Line + "\n") }

// PanelConf is the content of /data/tsx/panel.conf that the installer writes
// into the new slot: the one key line in tsx-config's store form (the key
// has no backslash or double quote: the rule above).
func (k *Key) PanelConf() []byte { return []byte(`SSH_AUTHORIZED_KEY="` + k.Line + "\"\n") }

// SetupStamp is the content of /data/tsx/setup-v2 (tsx-linux-common
// setup-done: the stamp that stops the old "configured" rule's migration, so
// the panel boots unconfigured and its pairing window opens).
func SetupStamp() []byte { return []byte("2\n") }

func str(r *bytes.Reader) ([]byte, error) {
	var n uint32
	if err := binary.Read(r, binary.BigEndian, &n); err != nil {
		return nil, errors.New("short")
	}
	if int64(n) > int64(r.Len()) {
		return nil, errors.New("short")
	}
	b := make([]byte, n)
	if _, err := r.Read(b); err != nil && n > 0 {
		return nil, err
	}
	return b, nil
}

// mpint: a positive mpint in canonical form (RFC 4251): no superfluous
// leading zero byte, not negative, not zero.
func mpint(r *bytes.Reader) (*big.Int, error) {
	b, err := str(r)
	switch {
	case err != nil:
		return nil, errors.New("short")
	case len(b) == 0:
		return nil, errors.New("zero")
	case b[0]&0x80 != 0:
		return nil, errors.New("negative")
	case b[0] == 0 && (len(b) == 1 || b[1]&0x80 == 0):
		return nil, errors.New("not canonical (a superfluous leading zero byte)")
	}
	return new(big.Int).SetBytes(b), nil
}

func short(s string) string {
	if len(s) > 24 {
		return s[:24] + "…"
	}
	return s
}
