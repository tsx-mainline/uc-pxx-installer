package plan

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash/crc32"
	"testing"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/manifest"
)

// TestPredictSwitchKnownUnit3: the DESIGN §9.2 vector of unit 3 after the
// BCB-route reset (A stock 1.0.4.22 trying {15,6,0}, B userdebug 1.0.0.94
// trying {14,7,0}): the switch to B writes bytes 8 and 12 and the CRC only.
func TestPredictSwitchKnownUnit3(t *testing.T) {
	cur := make([]byte, 512)
	before, _ := hex.DecodeString("00414230010000000f0600000e070000000000000000000000000000ae1365e7")
	copy(cur, before)
	for i := 32; i < 512; i++ {
		cur[i] = byte(i*7 + 3)
	}
	if s := sha256.Sum256(cur[:32]); hex.EncodeToString(s[:]) != "dcf29ffc740a09517a362b96992738c4fa36d5260d0f6bbb4a4cfc8ebc6aceb0" {
		t.Fatalf("before sha %x", s)
	}
	got, err := PredictSwitchKnown(cur, 1)
	if err != nil || got == nil {
		t.Fatal(got, err)
	}
	if h := hex.EncodeToString(got[:32]); h != "00414230010000000e0600000f070000000000000000000000000000651ccf30" {
		t.Fatalf("after %s", h)
	}
	if s := sha256.Sum256(got[:32]); hex.EncodeToString(s[:]) != "3d52f91a8770a63e597fdccaf499036dffc673573510c0801470de0f2d13cda6" {
		t.Fatalf("after sha %x", s)
	}
	if !bytes.Equal(got[32:], cur[32:]) {
		t.Fatal("bytes 32..511 changed")
	}
	// the official switch refuses it (B is not successful); r3 without --untested too
	if _, err := PredictSwitch(cur, 1); err == nil {
		t.Fatal("PredictSwitch took a trying target")
	}
	// the switch to A: A starts next already
	if n, err := PredictSwitchKnown(cur, 0); err != nil || n != nil {
		t.Fatalf("to A: %v %v", n, err)
	}
}

// TestPredictSwitchKnownAll: every legal pair of slot states (priority
// 0..15, tries 0..7, successful 0/1, the legal combos): a prediction is
// made exactly for a trying T and a bootable other slot; it changes only
// bytes 8 + 4T, 8 + 4O and the big-endian CRC (28..31), keeps T trying with
// its tries, makes T the one that starts next, and keeps the other slot
// bootable.
func TestPredictSwitchKnownAll(t *testing.T) {
	type sl struct{ p, tr, s byte }
	var states []sl
	for p := byte(0); p <= 15; p++ {
		for tr := byte(0); tr <= 7; tr++ {
			states = append(states, sl{p, tr, 0})
		}
		states = append(states, sl{p, 0, 1})
	}
	n, made := 0, 0
	for _, a := range states {
		for _, b := range states {
			cur := make([]byte, 512)
			copy(cur, []byte{0, 'A', 'B', '0', 1, 0})
			cur[8], cur[9], cur[10] = a.p, a.tr, a.s
			cur[12], cur[13], cur[14] = b.p, b.tr, b.s
			binary.BigEndian.PutUint32(cur[28:], crc32.ChecksumIEEE(cur[:28]))
			ab, _ := avbab.Parse(cur)
			if ab.Validate() != nil {
				continue
			}
			for to := 0; to < 2; to++ {
				n++
				tt, oo := ab.Slots[to], ab.Slots[1-to]
				got, err := PredictSwitchKnown(cur, to)
				want := tt.Priority >= 1 && tt.Tries >= 1 && tt.Successful == 0 && oo.Bootable()
				if !want {
					if err == nil {
						t.Fatalf("%x to %d: not refused", cur[:16], to)
					}
					continue
				}
				if err != nil {
					t.Fatalf("%x to %d: %v", cur[:16], to, err)
				}
				if got == nil {
					if ab.Pick() != to || tt.Priority <= oo.Priority {
						t.Fatalf("%x to %d: nothing to do, but %d starts", cur[:16], to, ab.Pick())
					}
					continue
				}
				made++
				for i := range got {
					if got[i] != cur[i] && i != 8+4*to && i != 8+4*(1-to) && (i < 28 || i > 31) {
						t.Fatalf("%x to %d: byte %d changed", cur[:16], to, i)
					}
				}
				if binary.BigEndian.Uint32(got[28:]) != crc32.ChecksumIEEE(got[:28]) {
					t.Fatal("the CRC is not big-endian over bytes 0..27")
				}
				g, _ := avbab.Parse(got)
				if g.Validate() != nil || g.Pick() != to || g.Slots[to].Tries != tt.Tries || g.Slots[to].Successful != 0 ||
					!g.Slots[1-to].Bootable() || g.Slots[to].Priority != 15 || g.Slots[1-to].Priority > 14 {
					t.Fatalf("%x to %d: %x", cur[:16], to, got[:16])
				}
			}
		}
	}
	if made < 1000 {
		t.Fatalf("only %d predictions of %d cases", made, n)
	}
	t.Logf("%d cases, %d predicted writes", n, made)
}

// TestSafeChainManifests (RC-10 review I-2 MAJOR-1): of the installer's own
// manifests, a refused firmware (older board), a test build and a no-go
// resource are never a known chain; a target needs a boot chain that booted
// on a unit (1.0.0.94 userdebug yes, the stock 1.0.4.22 no), the trying
// fallback does not (the unit-3 case: A stock 1.0.4.22, B 1.0.0.94 userdebug).
func TestSafeChainManifests(t *testing.T) {
	ms, err := manifest.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, m := range ms {
		tgt, fb := SafeChain(m, true), SafeChain(m, false)
		switch {
		case m.Status != manifest.StatusInstall || m.TestBuild || m.BootChain.ResourceStatus == manifest.ResourceNoGo:
			if tgt == "" || fb == "" {
				t.Errorf("%s (%s, test %v, resource %s): taken (%q %q)", m.Name(), m.Status, m.TestBuild, m.BootChain.ResourceStatus, tgt, fb)
			}
			seen["bad"] = true
		case m.BootChain.BootchainStatus == manifest.BootchainBooted:
			if tgt != "" || fb != "" {
				t.Errorf("%s: refused %q %q", m.Name(), tgt, fb)
			}
		default:
			if tgt == "" || fb != "" {
				t.Errorf("%s (not booted): target %q, fallback %q", m.Name(), tgt, fb)
			}
		}
		switch m.Firmware.File {
		case "pcdfw-1.0.0.94-userdebug.bin":
			seen["userdebug"] = tgt == ""
		case "pcdfw-1.0.4.22.bin":
			seen["stock"] = tgt != "" && fb == ""
		}
	}
	if !seen["bad"] {
		t.Fatal("no refused manifest among the installer's: the test checks nothing")
	}
	for _, k := range []string{"userdebug", "stock"} {
		if !seen[k] {
			t.Errorf("%s: not as the unit-3 case needs (or its file name changed)", k)
		}
	}
}
