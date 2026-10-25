package plan

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"regexp"
	"strconv"
	"strings"

	"uc-pxx-installer/internal/avbab"
)

// The plan predicts each A/B transition that uc-pxx-bootctl makes, with its
// own code. The prediction is never written anywhere: the executor compares
// the sector that bootctl wrote with it, and stops on any difference. So a
// bootctl that does something else than its documentation says is caught,
// and a mistake in this code only stops an install.

func slotBytes(raw [32]byte, i int) [4]byte {
	var s [4]byte
	copy(s[:], raw[8+4*i:12+4*i])
	return s
}

func successful(s [4]byte) bool { return s[2] == 1 && s[1] == 0 && s[0] >= 1 }

func withCRC(raw [32]byte) [32]byte {
	binary.BigEndian.PutUint32(raw[28:], crc32.ChecksumIEEE(raw[:28]))
	return raw
}

// PredictDisable is what `uc-pxx-bootctl disable --slot t` writes: the target
// becomes {0,0,0}, the keeper does not change. It refuses when the keeper is
// not successful. A target that is already {0,0,0} gives the same sector.
func PredictDisable(cur [32]byte, t int) ([32]byte, error) {
	k := 1 - t
	if !successful(slotBytes(cur, k)) {
		return cur, fmt.Errorf("slot %s is not successful", avbab.SlotName(k))
	}
	ts := slotBytes(cur, t)
	if ts[0] == 0 && ts[1] == 0 && ts[2] == 0 {
		return cur, nil
	}
	next := cur
	next[8+4*t], next[9+4*t], next[10+4*t] = 0, 0, 0
	return withCRC(next), nil
}

// PredictArm is what `uc-pxx-bootctl arm-trying --slot t [--lower-keeper]`
// writes: the target becomes trying {15,7,0}; a keeper with priority 15
// drops to 14 (only with lower), nothing else of the keeper changes.
func PredictArm(cur [32]byte, t int, lower bool) ([32]byte, error) {
	k := 1 - t
	ks := slotBytes(cur, k)
	if !successful(ks) {
		return cur, fmt.Errorf("slot %s is not successful", avbab.SlotName(k))
	}
	if ks[0] == 15 && !lower {
		return cur, fmt.Errorf("slot %s has priority 15 and --lower-keeper is not given", avbab.SlotName(k))
	}
	ts := slotBytes(cur, t)
	if ts[0] == 15 && ts[1] == 7 && ts[2] == 0 && ks[0] <= 14 {
		return cur, nil
	}
	next := cur
	next[8+4*t], next[9+4*t], next[10+4*t] = 15, 7, 0
	if ks[0] == 15 {
		next[8+4*k] = 14
	}
	return withCRC(next), nil
}

// ABState is one A/B state as the plan shows it.
type ABState struct {
	Hex32 string `json:"hex32"` // the 32 bytes of AvbABData
	A     string `json:"a"`
	B     string `json:"b"`
	Next  string `json:"boots_next"`
}

func short(s avbab.Slot) string {
	return fmt.Sprintf("{%d,%d,%d} %s", s.Priority, s.Tries, s.Successful, s.State())
}

// State describes a sector.
func State(raw [32]byte) ABState {
	a, _ := avbab.Parse(raw[:])
	next := "-"
	if a.Validate() == nil {
		next = avbab.SlotName(a.Pick())
	}
	return ABState{Hex32: hex.EncodeToString(raw[:]), A: short(a.Slots[0]), B: short(a.Slots[1]), Next: next}
}

// Raw decodes the hex32 field.
func (s ABState) Raw() ([32]byte, error) {
	var r [32]byte
	b, err := hex.DecodeString(s.Hex32)
	if err != nil || len(b) != 32 {
		return r, fmt.Errorf("bad hex32 %q", s.Hex32)
	}
	copy(r[:], b)
	return r, nil
}

// String is a short line, for example "A{15,0,1} B{14,0,1} next A".
func (s ABState) String() string {
	cut := func(x string) string { return strings.SplitN(x, " ", 2)[0] }
	return fmt.Sprintf("A%s B%s, boots next: %s", cut(s.A), cut(s.B), strings.ToUpper(s.Next))
}

// BootctlStatus is what `uc-pxx-bootctl status` reports about a sector.
type BootctlStatus struct {
	Slots [2][3]byte // priority, tries, successful
	CRC   uint32
	Pick  int // -1 = none
}

var statusRx = regexp.MustCompile(`current: A\{prio (\d+) tries (\d+) succ (\d+)\} B\{prio (\d+) tries (\d+) succ (\d+)\} crc ([0-9a-f]{8})`)
var pickRx = regexp.MustCompile(`(?m)^libavb-would-boot: ([ab-])$`)

// ParseBootctlStatus reads the stderr and stdout of `uc-pxx-bootctl status`.
func ParseBootctlStatus(stderr, stdout string) (*BootctlStatus, error) {
	m := statusRx.FindStringSubmatch(stderr)
	p := pickRx.FindStringSubmatch(stdout)
	if m == nil || p == nil {
		return nil, fmt.Errorf("cannot read the output of uc-pxx-bootctl status")
	}
	st := &BootctlStatus{Pick: -1}
	for i := 0; i < 6; i++ {
		v, err := strconv.Atoi(m[1+i])
		if err != nil || v > 255 {
			return nil, fmt.Errorf("bad number in the bootctl status")
		}
		st.Slots[i/3][i%3] = byte(v)
	}
	c, _ := strconv.ParseUint(m[7], 16, 32)
	st.CRC = uint32(c)
	switch p[1] {
	case "a":
		st.Pick = 0
	case "b":
		st.Pick = 1
	}
	return st, nil
}

// Agrees reports that bootctl's view of a sector is the same as this
// program's own decoder.
func (st *BootctlStatus) Agrees(a avbab.AB) error {
	for i := 0; i < 2; i++ {
		s := a.Slots[i]
		if st.Slots[i] != [3]byte{s.Priority, s.Tries, s.Successful} {
			return fmt.Errorf("slot %s: bootctl says %v, the installer's decoder %v", avbab.SlotName(i), st.Slots[i], s)
		}
	}
	if st.CRC != a.CRCStored {
		return fmt.Errorf("crc: bootctl says %08x, the installer's decoder %08x", st.CRC, a.CRCStored)
	}
	if st.Pick != a.Pick() {
		return fmt.Errorf("next slot: bootctl says %s, the installer's decoder %s", avbab.SlotName(st.Pick), avbab.SlotName(a.Pick()))
	}
	return nil
}
