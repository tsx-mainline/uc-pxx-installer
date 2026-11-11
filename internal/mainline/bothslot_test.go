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
