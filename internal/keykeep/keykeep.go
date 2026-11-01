// Package keykeep is the firmware key record that the installer keeps on the
// unit (KEY-KEEP.md, the owner's answers of 2026-10-31): two 512-byte sectors
// of the backup partition, written before the disable when the install would
// remove the last copy of the key on the eMMC. It has no I/O.
//
// The record (version 1, every field fixed, deterministic):
//
//	0..15    magic "P10INST-KEYKEEP1"
//	16..19   version 1 (big-endian)
//	20..23   flags 0
//	24..39   the head key
//	40..55   the IV
//	56..507  zero
//	508..511 CRC32 (IEEE, big-endian) of bytes 0..507
//
// The record holds the key in plain (owner Q3: as the stock libraries do on
// every stock unit). So the record bytes are the key: nothing may print, log or
// bundle them. Only their sha256 goes into a plan.
package keykeep

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"strings"

	"uc-pxx-installer/internal/pcdfw"
)

const (
	// Magic starts every record.
	Magic   = "P10INST-KEYKEEP1"
	Version = 1
	// Partition holds the two records.
	Partition = "backup"
	// FromEnd are the places of copy 1 and copy 2: sectors from the end of
	// the partition (1 MiB before the end, and the last sector). On the
	// UC-P10 (backup = 446464 + 32768 sectors) they are LBA 477184 and 479231.
	FromEnd1, FromEnd2 = 2048, 1
	// MinSectors is the smallest backup partition that holds both copies.
	MinSectors = 4096
)

// UnitLBAs are the record sectors on the UC-P10 layout (KEY-KEEP.md §3.1).
var UnitLBAs = [2]int64{477184, 479231}

// LBAs are the two record sectors of a backup partition at start, size.
func LBAs(start, size int64) ([2]int64, error) {
	if size < MinSectors {
		return [2]int64{}, fmt.Errorf("%s has %d sectors, fewer than %d", Partition, size, MinSectors)
	}
	return [2]int64{start + size - FromEnd1, start + size - FromEnd2}, nil
}

// Encode makes the record of a key.
func Encode(k pcdfw.Keys) []byte {
	b := make([]byte, 512)
	copy(b, Magic)
	binary.BigEndian.PutUint32(b[16:], Version)
	copy(b[24:40], k.Head[:])
	copy(b[40:56], k.IV[:])
	binary.BigEndian.PutUint32(b[508:], crc32.ChecksumIEEE(b[:508]))
	return b
}

// Decode reads a record. It checks the magic, the version, the flags, the
// zero area and the CRC.
func Decode(sec []byte) (pcdfw.Keys, error) {
	var k pcdfw.Keys
	switch {
	case len(sec) != 512:
		return k, fmt.Errorf("a record sector has 512 bytes, not %d", len(sec))
	case string(sec[:16]) != Magic:
		return k, errors.New("no key record (magic)")
	case binary.BigEndian.Uint32(sec[16:]) != Version || binary.BigEndian.Uint32(sec[20:]) != 0:
		return k, errors.New("a key record of another version")
	case binary.BigEndian.Uint32(sec[508:]) != crc32.ChecksumIEEE(sec[:508]):
		return k, errors.New("a key record with a bad CRC (torn or damaged)")
	case !bytes.Equal(sec[56:508], make([]byte, 452)):
		return k, errors.New("a key record with data in its zero area")
	}
	copy(k.Head[:], sec[24:40])
	copy(k.IV[:], sec[40:56])
	return k, nil
}

// Present reports a valid record (for the status row: never the key).
func Present(sec []byte) bool {
	_, err := Decode(sec)
	return err == nil
}

// SHA256 of a record, hex: the only form of the record that a plan or a log
// may hold.
func SHA256(rec []byte) string {
	s := sha256.Sum256(rec)
	return hex.EncodeToString(s[:])
}

// Class is what a record sector holds before the write (KEY-KEEP.md §3.4).
type Class string

const (
	Empty   Class = "empty"        // all 0x00 or all 0xFF
	Same    Class = "this-record"  // exactly the record to keep
	Torn    Class = "torn-record"  // every byte 0x00, 0xFF or the record's byte there: an earlier write of it, cut
	Other   Class = "other-record" // a valid record of another key: never replaced (owner Q6, never remove)
	Foreign Class = "foreign-data" // anything else: never written (owner Q4: a plan note)
	Unread  Class = "unreadable"   // the read failed
)

// Writable: the keep-key step may write over it (or, Same, need not).
func (c Class) Writable() bool { return c == Empty || c == Same || c == Torn }

// Classify says what sec holds, for the record want.
func Classify(sec, want []byte) Class {
	if len(sec) != 512 || len(want) != 512 {
		return Foreign
	}
	if bytes.Equal(sec, want) {
		return Same
	}
	if allByte(sec, 0) || allByte(sec, 0xff) {
		return Empty
	}
	if Present(sec) {
		return Other
	}
	for i, b := range sec {
		if b != 0 && b != 0xff && b != want[i] {
			return Foreign
		}
	}
	return Torn
}

func allByte(b []byte, v byte) bool {
	for _, x := range b {
		if x != v {
			return false
		}
	}
	return true
}

//go:embed checks.json
var checksJSON []byte

// Checks are the read-only checks of KEY-KEEP.md §2.1 (checks.json), with
// their results as KEY-KEEP.md records them (§9: check 1; §10: checks 2, 3).
type Checks struct {
	Schema int    `json:"schema"`
	Kind   string `json:"kind"`
	About  string `json:"about"`
	Checks []struct {
		ID        string `json:"id"`
		What      string `json:"what"`
		Passed    bool   `json:"passed"`
		Source    string `json:"source"`    // the section of KEY-KEEP.md that records the result
		Evidence  string `json:"evidence"`  // what was found
		Condition string `json:"condition"` // a condition of the pass ("": none); it goes into the gate
	} `json:"checks"`
}

// CheckIDs are the three checks of KEY-KEEP.md §2.1, in order.
var CheckIDs = []string{"backup-content", "bootchain-no-backup", "android-no-backup"}

// SourcePrefix starts the source of every passed check.
const SourcePrefix = "KEY-KEEP.md section "

// Gate is the state of the keep-key write: open only when every check of
// the embedded checks.json passed. sha is the sha256 of that file (it goes
// into the plan), and so do the conditions of the passes.
type Gate struct {
	Open       bool     `json:"open"`
	Why        string   `json:"why"`
	SHA256     string   `json:"checks_sha256"`
	Conditions []string `json:"conditions,omitempty"` // "id: condition" of each pass with one
}

// TheGate reads the embedded checks.
func TheGate() Gate {
	return gateOf(checksJSON)
}

// checkFields are the fields that every check must have ("condition" too:
// "" says that the pass has no condition).
var checkFields = []string{"id", "what", "passed", "source", "evidence", "condition"}

// strictJSON: b is exactly one JSON value with no key twice in any object,
// and every check has every field of checkFields (review R3 I-1). The
// decoder alone takes the last of two equal keys, ignores data after the
// first value, and takes a missing field as "".
func strictJSON(b []byte) error {
	d := json.NewDecoder(bytes.NewReader(b))
	if err := noDuplicateKeys(d); err != nil {
		return err
	}
	if len(bytes.TrimSpace(b[d.InputOffset():])) != 0 {
		return errors.New("data after the JSON object")
	}
	var raw struct {
		Checks []map[string]json.RawMessage `json:"checks"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	for i, c := range raw.Checks {
		for _, f := range checkFields {
			if _, ok := c[f]; !ok {
				return fmt.Errorf("check %d has no field %q", i+1, f)
			}
		}
	}
	return nil
}

// noDuplicateKeys reads one JSON value and refuses a key that comes twice
// in one object.
func noDuplicateKeys(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return err
	}
	switch t {
	case json.Delim('{'):
		seen := map[string]bool{}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return err
			}
			key, _ := k.(string)
			if seen[key] {
				return fmt.Errorf("the key %q twice in one object", key)
			}
			seen[key] = true
			if err := noDuplicateKeys(d); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	case json.Delim('['):
		for d.More() {
			if err := noDuplicateKeys(d); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	}
	return nil
}

func gateOf(b []byte) Gate {
	g := Gate{SHA256: SHA256(b)}
	var c Checks
	err := strictJSON(b)
	if err == nil {
		d := json.NewDecoder(bytes.NewReader(b))
		d.DisallowUnknownFields()
		err = d.Decode(&c)
	}
	if err != nil || c.Schema != 1 || c.Kind != "p10inst-keykeep-checks" || len(c.Checks) != len(CheckIDs) {
		g.Why = fmt.Sprintf("the checks file does not read (%v)", err)
		return g
	}
	var open, conds []string
	for i, x := range c.Checks {
		if x.ID != CheckIDs[i] {
			g.Why = fmt.Sprintf("the checks file does not read (check %d is %q, not %q)", i+1, x.ID, CheckIDs[i])
			return g
		}
		if !x.Passed || x.Evidence == "" || !strings.HasPrefix(x.Source, SourcePrefix) || len(x.Source) == len(SourcePrefix) {
			open = append(open, x.ID)
			continue
		}
		if x.Condition != "" {
			conds = append(conds, x.ID+": "+x.Condition)
		}
	}
	if len(open) > 0 {
		g.Why = "the read-only checks of KEY-KEEP.md §2.1 have not all passed (open: " + strings.Join(open, ", ") + ")"
		return g
	}
	g.Open, g.Why, g.Conditions = true, "the three read-only checks of KEY-KEEP.md §2.1 passed", conds
	if len(conds) > 0 {
		g.Why += fmt.Sprintf(", %d with a condition", len(conds))
	}
	return g
}
