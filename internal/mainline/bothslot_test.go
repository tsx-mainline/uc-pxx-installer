package mainline_test

// Review T4 MINOR-1 / T4-2 NIT-2: a payload reviewed for slot B ("ab") is
// checked against the both-slot pins, a payload for slot A only against the
// slot-A pins, on real ext4 images (testdata/bothslot, checked in: the lint
// image has no mke2fs).

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"

	"uc-pxx-installer/internal/mainline"
)

func gunzipFile(t *testing.T, p string) []byte {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(z)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBothSlotPins(t *testing.T) {
	aPins := []mainline.PinnedFile{{Path: "/usr/sbin/uc-pxx-bootctl", SHA256: sum([]byte("slot-A bootctl\n"))},
		{Path: "/etc/uc-pxx-mark-boot.sh", SHA256: sum([]byte("slot-A hook\n"))}}
	bothPins := []mainline.PinnedFile{{Path: "/usr/sbin/uc-pxx-bootctl", SHA256: sum([]byte("both-slot bootctl\n"))},
		{Path: "/usr/sbin/uc-pxx-slot", SHA256: sum([]byte("both-slot slot tool\n"))}}
	type pl struct {
		p          *mainline.Payload
		tmpl, root string
	}
	load := func(name string) pl {
		rf := gunzipFile(t, "testdata/bothslot/"+name)
		if !bytes.Equal(rf[1024+0x38:1024+0x3a], []byte{0x53, 0xef}) {
			t.Fatalf("%s is not an ext4", name)
		}
		b := newBundle()
		b.Rootfs = rf
		b.Sub("rootfs")["size"], b.Sub("rootfs")["sha256"] = len(rf), sum(rf)
		d, _ := b.Write(t.TempDir())
		p, err := verifyDir(t, d)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { p.Close() })
		return pl{p, sum(b.Template), sum(rf)}
	}
	pa, pb := load("slot-a.ext4.gz"), load("both.ext4.gz")
	for _, c := range []struct {
		name  string
		p     pl
		slots string
		both  []mainline.PinnedFile
		ok    bool
	}{
		{"a payload, slot-A files", pa, "a", bothPins, true},
		{"a payload, both-slot files", pb, "a", bothPins, false},
		{"ab payload, both-slot files", pb, "ab", bothPins, true},
		{"ab payload, slot-A files (the T3 MAJOR-1 scenario)", pa, "ab", bothPins, false},
		{"ab payload, both-slot files, no both-slot pins", pb, "ab", nil, false},
		{"ab payload, a both-slot pin absent from the rootfs", pb, "ab",
			append(append([]mainline.PinnedFile(nil), bothPins...), mainline.PinnedFile{Path: "/usr/sbin/uc-pxx-arm-slot", SHA256: sum([]byte("x"))}), false},
		{"ab payload, a both-slot pin with another sha", pb, "ab",
			[]mainline.PinnedFile{bothPins[0], {Path: bothPins[1].Path, SHA256: strings.Repeat("0", 64)}}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			restore := mainline.SetBothPins(c.both)
			defer restore()
			rev := []mainline.Reviewed{{Version: "t", ManifestSHA256: c.p.p.ManifestSHA256, TemplateSHA256: c.p.tmpl, RootfsSHA256: c.p.root, Slots: c.slots}}
			r := mainline.TestGate(rev, nil, aPins).Check(c.p.p)
			var bad []string
			for _, ch := range r.Checks {
				if !ch.OK {
					bad = append(bad, ch.Name+": "+ch.Why)
				}
				if ch.Name == "boot-control-files" && c.slots == "ab" && len(c.both) > 0 && !strings.Contains(ch.Why, "the "+itoa(len(c.both))+" boot-control files") {
					t.Fatalf("the message counts the slot-A pins (review T4-2 NIT-5): %s", ch.Why)
				}
			}
			if r.OK() != c.ok || !c.ok && len(bad) == 0 {
				t.Fatalf("ok %v, want %v: %v", r.OK(), c.ok, bad)
			}
		})
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// TestForbiddenPair (BOOTCTL-11c2afcd-REVIEW (2)): a gate whose pins name the
// both-slot mark hook with the slot-A bootctl refuses, whatever the tree.
func TestForbiddenPair(t *testing.T) {
	rf := gunzipFile(t, "testdata/bothslot/both.ext4.gz")
	b := newBundle()
	b.Rootfs = rf
	b.Sub("rootfs")["size"], b.Sub("rootfs")["sha256"] = len(rf), sum(rf)
	d, _ := b.Write(t.TempDir())
	p, err := verifyDir(t, d)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	// both both-slot hooks: fe581cf2 (0.2.0-r0) and 726d5675 (0.2.0-r1, owner Q-T7)
	for _, mark := range []string{"fe581cf2212d8bc01ff54d1b88dc7d03601af075f56e74fb0d3edf2553a08056",
		"726d567556b1386bee4d60a5052e28fec02676f4c044f717ea096a8ce1e7e916"} {
		bad := []mainline.PinnedFile{{Path: "/etc/uc-pxx-mark-boot.sh", SHA256: mark},
			{Path: "/usr/sbin/uc-pxx-bootctl", SHA256: "38fea6d32412e7977ebc5845b138dc1c8c2df8555dae0b64b47f8a851b45e2d7"}}
		rev := []mainline.Reviewed{{Version: "t", ManifestSHA256: p.ManifestSHA256, TemplateSHA256: sum(b.Template), RootfsSHA256: sum(rf), Slots: "a"}}
		r := mainline.TestGate(rev, nil, bad).Check(p)
		refused := false
		for _, c := range r.Checks {
			if c.Name == "boot-control-files" && !c.OK && strings.Contains(c.Why, "never ships with /usr/sbin/uc-pxx-bootctl") {
				refused = true
			}
		}
		if !refused {
			t.Fatalf("the forbidden pair (mark %s) was not refused: %+v", mark[:8], r.Checks)
		}
	}
}
