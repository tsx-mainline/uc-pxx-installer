package plan_test

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"strings"
	"testing"

	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/pcdfw"
	"uc-pxx-installer/internal/plan"
)

// The repair rule against a model written here from the state table of
// DEVMODE-R4.md §2.2 (not from package avbab).
type mslot struct{ p, t, s, r byte }

func (s mslot) legal() bool {
	if s.p > 15 || s.t > 7 || s.r != 0 {
		return false
	}
	if s.s == 1 {
		return s.t == 0
	}
	return s.s == 0
}
func (s mslot) bootable() bool   { return s.p > 0 && (s.s == 1 || s.t > 0) }
func (s mslot) successful() bool { return s.s == 1 && s.t == 0 && s.p >= 1 }

func sector(a, b mslot, tail byte) []byte {
	sec := make([]byte, 512)
	copy(sec, "\x00AB0")
	sec[4] = 1
	copy(sec[8:], []byte{a.p, a.t, a.s, a.r, b.p, b.t, b.s, b.r})
	binary.BigEndian.PutUint32(sec[28:], crc32.ChecksumIEEE(sec[:28]))
	for i := 32; i < 512; i++ {
		sec[i] = tail + byte(i)
	}
	return sec
}

func modelClass(a, b mslot, k int) plan.RepairClass {
	sl := [2]mslot{a, b}
	if !a.legal() || !b.legal() || !a.bootable() && !b.bootable() {
		return plan.RepairInvalid
	}
	if sl[k].successful() {
		return plan.RepairKeeperOK
	}
	pick := 0
	if !a.bootable() || b.bootable() && b.p > a.p {
		pick = 1
	}
	if pick == k {
		return plan.RepairKeeperNext
	}
	return plan.RepairTargetNext
}

func TestClassifyRepairAll(t *testing.T) {
	n := 0
	for ai := 0; ai < 17*9*3*2; ai++ {
		a := mslot{byte(ai % 17), byte(ai / 17 % 9), byte(ai / 153 % 3), byte(ai / 459)}
		for bi := 0; bi < 17*9*3*2; bi += 7 {
			b := mslot{byte(bi % 17), byte(bi / 17 % 9), byte(bi / 153 % 3), byte(bi / 459)}
			sec := sector(a, b, byte(ai))
			for k := 0; k < 2; k++ {
				got, why := plan.ClassifyRepair(sec, nil, k)
				if want := modelClass(a, b, k); got != want || why == "" {
					t.Fatalf("A%v B%v keeper %d: %s (%s), want %s", a, b, k, got, why, want)
				}
				n++
			}
		}
	}
	t.Logf("%d sectors x keepers classified as the model says", n)
}

func TestClassifyRepairBroken(t *testing.T) {
	good := sector(mslot{15, 0, 1, 0}, mslot{14, 0, 1, 0}, 0)
	le := append([]byte(nil), good...)
	binary.LittleEndian.PutUint32(le[28:], crc32.ChecksumIEEE(le[:28]))
	cases := map[string][]byte{
		"bad magic": append([]byte("\x00AX0"), good[4:]...),
		"LE crc":    le,
		"crc":       append(append([]byte(nil), good[:31]...), append([]byte{good[31] ^ 1}, good[32:]...)...),
		"all zero":  make([]byte, 512),
		"all ff":    bytes.Repeat([]byte{0xff}, 512),
		"version 2": func() []byte {
			b := append([]byte(nil), good...)
			b[4] = 2
			binary.BigEndian.PutUint32(b[28:], crc32.ChecksumIEEE(b[:28]))
			return b
		}(),
		"reserved 6": func() []byte {
			b := append([]byte(nil), good...)
			b[6] = 1
			binary.BigEndian.PutUint32(b[28:], crc32.ChecksumIEEE(b[:28]))
			return b
		}(),
	}
	for name, sec := range cases {
		for k := 0; k < 2; k++ {
			if c, _ := plan.ClassifyRepair(sec, nil, k); c != plan.RepairInvalid || !c.NeedsWrite() || c.Safe() {
				t.Fatalf("%s keeper %d: %s", name, k, c)
			}
		}
	}
	if c, why := plan.ClassifyRepair(nil, errors.New("EIO"), 0); c != plan.RepairUnreadable || c.NeedsWrite() || c.Safe() || !strings.Contains(why, "EIO") {
		t.Fatalf("unreadable: %s %s", c, why)
	}
	if c, _ := plan.ClassifyRepair(good[:511], nil, 0); c != plan.RepairUnreadable {
		t.Fatalf("short sector: %s", c)
	}
}

func TestPredictRepair(t *testing.T) {
	cur := bytes.Repeat([]byte{0x5a}, 512)
	for k, want := range []string{
		"0041423001000000" + "0f000100" + "00000000" + "000000000000000000000000",
		"0041423001000000" + "00000000" + "0f000100" + "000000000000000000000000",
	} {
		r, err := plan.PredictRepair(cur, k)
		if err != nil {
			t.Fatal(err)
		}
		raw := make([]byte, 28)
		copy(raw, r[:28])
		crc := make([]byte, 4)
		binary.BigEndian.PutUint32(crc, crc32.ChecksumIEEE(raw))
		if got := hexs(r[:28]); got != want || !bytes.Equal(r[28:32], crc) || !bytes.Equal(r[32:], cur[32:]) {
			t.Fatalf("keeper %d: R = %x", k, r[:32])
		}
		if c, _ := plan.ClassifyRepair(r, nil, k); c != plan.RepairKeeperOK {
			t.Fatalf("R(%d) is %s for its keeper", k, c)
		}
		// for the other slot as keeper, R is a state to repair (only the
		// first keeper boots): the repair never flips a keeper
		if c, _ := plan.ClassifyRepair(r, nil, 1-k); c != plan.RepairTargetNext {
			t.Fatalf("R(%d) is %s for the other keeper", k, c)
		}
	}
	if _, err := plan.PredictRepair(cur[:100], 0); err == nil {
		t.Fatal("a short sector")
	}
	if _, err := plan.PredictRepair(cur, 2); err == nil {
		t.Fatal("keeper 2")
	}
}

func hexs(b []byte) string {
	const h = "0123456789abcdef"
	var s strings.Builder
	for _, c := range b {
		s.WriteByte(h[c>>4])
		s.WriteByte(h[c&15])
	}
	return s.String()
}

// TestReopen: the payload of a plan file, reopened without a hash, is the
// payload of pass 1, and a plan made from it has the same id. Any recorded
// fact that is not the manifest's is refused.
func TestReopen(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	w := newWorld(t, fx, unit2)
	deviceMode(w)
	p1, err := plan.Compute(w.in)
	if err != nil {
		t.Fatal(err)
	}
	// the plan file round trip
	b, _ := json.Marshal(p1)
	var pf plan.Plan
	if err := json.Unmarshal(b, &pf); err != nil || !pf.VerifyID() {
		t.Fatalf("plan file: %v", err)
	}
	pl, err := plan.Reopen(bytes.NewReader(fx.FW), int64(len(fx.FW)), fx.Keys, fx.Manifests, pf.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if pl.File() == nil || len(pl.Segments()) != len(pf.Payload.Parts) {
		t.Fatal("no container")
	}
	in := w.in
	in.Payload = pl
	in.KnownBefore = plan.KnownBeforeOf(&pf)
	p2, err := plan.Compute(in)
	if err != nil || p2.ID != p1.ID {
		t.Fatalf("the plan from the reopened payload: id %s, want %s: %v", p2.ID, p1.ID, err)
	}
	edits := map[string]func(p *plan.Payload){
		"part sha":       func(p *plan.Payload) { p.Parts[1].SHA256 = strings.Repeat("0", 64) },
		"part size":      func(p *plan.Payload) { p.Parts[2].Size++ },
		"part action":    func(p *plan.Payload) { p.Parts[0].Action = "write-if-different" },
		"segment":        func(p *plan.Payload) { p.Parts[3].Segment = 9 },
		"a part less":    func(p *plan.Payload) { p.Parts = p.Parts[1:] },
		"firmware sha":   func(p *plan.Payload) { p.FirmwareSHA256 = strings.Repeat("1", 64) },
		"firmware size":  func(p *plan.Payload) { p.FirmwareSize-- },
		"version":        func(p *plan.Payload) { p.Version = "9.9" },
		"vbmeta key":     func(p *plan.Payload) { p.Vbmeta.KeySHA1 = "x" },
		"rollback":       func(p *plan.Payload) { p.Vbmeta.RollbackIndex++ },
		"no resource":    func(p *plan.Payload) { p.BootResource = "" },
		"pass 1 partial": func(p *plan.Payload) { p.Pass1 = "3 of 8 partition images match the manifest" },
		"kind":           func(p *plan.Payload) { p.Kind = "unofficial" },
	}
	for name, edit := range edits {
		var q plan.Plan
		json.Unmarshal(b, &q)
		edit(q.Payload)
		if _, err := plan.Reopen(bytes.NewReader(fx.FW), int64(len(fx.FW)), fx.Keys, fx.Manifests, q.Payload); err == nil {
			t.Errorf("%s: reopened", name)
		}
	}
	// another file of the same size: the segment walk or the bodies fail
	bad := append([]byte(nil), fx.FW...)
	for i := pcdfw.FirstSeg; i < pcdfw.FirstSeg+pcdfw.SegHdrLen; i++ {
		bad[i] ^= 0xa5 // the first segment header
	}
	if _, err := plan.Reopen(bytes.NewReader(bad), int64(len(bad)), fx.Keys, fx.Manifests, pf.Payload); err == nil {
		t.Error("a damaged container reopened")
	}
}

// TestKnownBefore: the before-hash of an always-written part comes from the
// earlier plan (not read again); a write-if-different part is hashed again.
func TestKnownBefore(t *testing.T) {
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	w := newWorld(t, fx, unit2)
	deviceMode(w)
	p1, err := plan.Compute(w.in)
	if err != nil {
		t.Fatal(err)
	}
	kb := plan.KnownBeforeOf(p1)
	for _, s := range p1.Steps {
		if s.Write != nil && w.in.Payload.Part(strings.TrimSuffix(s.Write.Partition, "_b")).Action != "write" && kb[s.Write.Partition] != "" {
			t.Fatalf("KnownBeforeOf gives %s, a write-if-different part", s.Write.Partition)
		}
	}
	for name := range kb {
		in := w.in
		in.KnownBefore = map[string]string{}
		for k, v := range kb {
			in.KnownBefore[k] = v
		}
		in.KnownBefore[name] = strings.Repeat("7", 64)
		p2, err := plan.Compute(in)
		if err != nil || p2.ID == p1.ID {
			t.Fatalf("%s: a known hash is not used (id %s): %v", name, p2.ID, err)
		}
	}
	// a write-if-different part (trust): a known hash is ignored
	in := w.in
	in.KnownBefore = map[string]string{"trust_b": strings.Repeat("7", 64)}
	if p3, err := plan.Compute(in); err != nil || p3.ID != p1.ID {
		t.Fatalf("trust_b: the known hash was used: %v", err)
	}
}
