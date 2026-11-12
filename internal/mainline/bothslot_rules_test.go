package mainline_test

// The both-slot ("ab") rules of the release gate (BOTH-SLOT-PACKAGING-2026-11-10
// §6 with the review's m2; mainline-rc, PROPOSED data): the data, and, on the
// real DEV payloads of the packaging (RC_BOTHSLOT_NORMAL / RC_BOTHSLOT_STEP2 =
// their uc-pxx-mainline folders; the lint does not have them), every rule.

import (
	"os"
	"sort"
	"strings"
	"testing"

	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/mainline/step2set"
)

// TestEmbeddedBothSlotData: the both-slot pins are the packaging's §6 set,
// the list names exactly the eight boot tools, d02e9263's both-slot Image is
// 85f5a417 (never the slot-A 593ccb89), the key set and DEV markers.
func TestEmbeddedBothSlotData(t *testing.T) {
	b := mainline.EmbeddedBothSlot()
	want := map[string]string{
		"/usr/sbin/uc-pxx-bootctl":                      "11c2afcd0272f9c989f9825074186a2341d0a33a34e0fe9ebf92cf0081f9efa7",
		"/etc/uc-pxx-mark-boot.sh":                      "726d567556b1386bee4d60a5052e28fec02676f4c044f717ea096a8ce1e7e916",
		"/etc/init.d/uc-pxx-boot-ok":                    "15d7588c6f6dd9bea3bb8fbccf4e4d4c8986c7e20973f806c71ba530e78f59f4",
		"/usr/sbin/uc-pxx-flash-boot":                   "2fad02b3d825706affabac71c45a2a04d2143970dcf014c1586593c11dfd6fee",
		"/usr/sbin/uc-pxx-arm-slot":                     "26c3f1c0a375674f8a80b2536ed6765f85ae6b13724770767039a271650f786e",
		"/usr/sbin/uc-pxx-update-boot":                  "4de04b78b8a48a4cf4d79f22f18f2f69a6932a36398e4ba6ec0ac175483b3703",
		"/usr/sbin/uc-pxx-post-update":                  "a512a311af2ddfa8a08be74cb19c7aebee8d4de773f59e5c98885dc3924836f3",
		"/usr/sbin/uc-pxx-slot":                         "cfec605c6d4042b36aa9882efa3bf8ee1f5a186ad3a06b07a7cc351b796abe2e",
		"/usr/local/sbin/tsx-autoupdate":                "40c913500d29e26601fbb6d14ac9feb2b8b200b9b3b2a2c2d5d69c42180391ab",
		"/usr/local/lib/tsx/conf.sh":                    "22f631e199a6232b9e9f54f6ac35c42f1ded260e9121b0c708e4deef66785692",
		"/usr/local/share/tsx/defaults/autoupdate.conf": "24b59e0f82833de669cd1edea5aebf8e04611473a41838e4a3638cdeafc84757",
		"/usr/local/sbin/tsx-panelctl":                  "c4ebec627795ac9a374cd992b52147bd0543cd608224b2395a504b649aece3b5",
		"/usr/local/lib/tsx/board.sh":                   "15a610e7079ddad643bdacc0f744f348bef0ee965d2fb4e6d912e848f6e1ced2",
		"/usr/share/uc-pxx/boot-control.sha256":         "bca98ff6ebcf2ca55bc183a64216e61b73ee5a7030366e11848faf3a8a25ad3e",
		"/usr/sbin/uc-pxx-data-migrate":                 "9162c65df08ac5577e38a2fe124d8cb1434b9b2bcfe787d6fba76055b8006791",
		"/usr/sbin/uc-pxx-data-release":                 "0e94edad81775245c704153abf7bb0469ed88e9638324b9d798a2312539d853b",
		"/etc/init.d/uc-pxx-lvm":                        "8f8632da42376defd3eabc5b11b5dd1d9121c97451e05feaa40b593c761eb4aa",
		"/etc/fstab":                                    "d4d8367497849ba3c7773fe229b422a1eb7b42d5799389fcbd75796b83a1ad2e",
	}
	// The USB set (mainline-rc-4): the permanent USB interface is pinned in
	// every both-slot payload (gate.go bothSlotUSB; uc-pxx-linux rc-payload-4).
	for p, h := range map[string]string{
		"/usr/libexec/uc-pxx-xfer/uc-pxx-xfer":   "42d764e0da7b5871de9980e1205c66769e0092f8a63d53a8b9353a93831c952f",
		"/etc/init.d/uc-pxx-usb":                 "3bd80e76654359ef605f01ad7cbb24e687cc95da2df1285581e32427667cc9da",
		"/etc/conf.d/uc-pxx-usb":                 "66eddca6f6c58b3839ec57c45f8895dc217810a853b73288de03186618cce2ca",
		"/usr/share/uc-pxx-usb/finish.json":      "05414bbf5a7e3e71b7acd3403f13bbc8071e6b64217e211f363bf04272b48ce1",
		"/etc/ssh/sshd_config.d/uc-pxx-usb.conf": "ad2fcc1ba531c2cbc72bedd2bffd72160c292484f817c5b81d3900616eaa83c9",
	} {
		want[p] = h
	}
	if len(b.Pins) != len(want) {
		t.Fatalf("%d both-slot pins, want %d", len(b.Pins), len(want))
	}
	for _, p := range b.Pins {
		if w, ok := want[p.Path]; !ok || w != p.SHA256 {
			t.Fatalf("%s: %s (want %q)", p.Path, p.SHA256, w)
		}
	}
	uo := append([]string(nil), b.USBOther...)
	sort.Strings(uo)
	if strings.Join(uo, " ") != "/etc/runlevels/default/uc-pxx-usb /usr/libexec/uc-pxx-xfer /usr/share/uc-pxx-usb" {
		t.Fatalf("USB other %v", uo)
	}
	l := append([]string(nil), b.ListPaths...)
	sort.Strings(l)
	if strings.Join(l, " ") != "/etc/init.d/uc-pxx-boot-ok /etc/uc-pxx-mark-boot.sh /usr/sbin/uc-pxx-arm-slot /usr/sbin/uc-pxx-bootctl "+
		"/usr/sbin/uc-pxx-flash-boot /usr/sbin/uc-pxx-post-update /usr/sbin/uc-pxx-slot /usr/sbin/uc-pxx-update-boot" {
		t.Fatalf("list paths %v", l)
	}
	if len(b.Kernels) != 1 || b.Kernels["d02e92631f2810a4a43fb6f09824afb9b0044ee2"] != "85f5a417e12feabb3c8a52b246ce8b89d6f9c70d2e6994525f69ff307f13f3d7" {
		t.Fatalf("both-slot kernels %v", b.Kernels)
	}
	if len(b.APKKeys) != 3 || len(b.DevMarkers) != 2 || len(b.Step2) != 0 {
		t.Fatalf("keys %d markers %d step-2 %d (the release gate of this package has no step-2 set)", len(b.APKKeys), len(b.DevMarkers), len(b.Step2))
	}
	if len(step2set.Files) != 6 || len(step2set.Other) != 3 {
		t.Fatalf("the step-2 set: %d files, %d others", len(step2set.Files), len(step2set.Other))
	}
}

func realPayload(t *testing.T, env string) (*mainline.Payload, mainline.Reviewed) {
	d := os.Getenv(env)
	if d == "" {
		t.Skip(env + " not set (the real both-slot DEV payload is not in the repo)")
	}
	p, err := verifyDir(t, d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	tb := p.TemplateBytes()
	return p, mainline.Reviewed{Version: p.Manifest.Version, ManifestSHA256: p.ManifestSHA256, TemplateSHA256: sum(tb),
		RootfsSHA256: p.Manifest.Rootfs.SHA256, Slots: "ab"}
}

func failedChecks(r *mainline.GateRecord) []string {
	var bad []string
	for _, c := range r.Checks {
		if !c.OK {
			bad = append(bad, c.Name+": "+c.Why)
		}
	}
	return bad
}

// TestBothSlotRulesReal: on the real DEV payloads (the rebuilt ones of
// RC-PAYLOAD-REBUILD-2026-11-11.md: normal 0ca9e172…, STEP-2 d140d012…), a gate with the release's
// both-slot rules but no DEV-marker rule passes them (normal as "ab", STEP-2
// as "ab" + Step2); the release's rules refuse them only for the DEV marker;
// and each rule bites: the step-2 set in a payload not marked Step2, a Step2
// mark on the normal payload, a changed pin, a changed key set, a list path
// set that differs, the slot-A kernel map, an "a" STEP-2 payload.
func TestBothSlotRulesReal(t *testing.T) {
	type c struct {
		name, env string
		step2     bool
		edit      func(*mainline.BothSlot)
		slots     string
		ok        bool
		want      string
	}
	noDev := func(b *mainline.BothSlot) { b.DevMarkers = nil }
	cases := []c{
		{"normal ab, no DEV rule", "RC_BOTHSLOT_NORMAL", false, noDev, "ab", true, ""},
		{"normal ab, the release's rules: DEV marker", "RC_BOTHSLOT_NORMAL", false, nil, "ab", false, "/etc/tsx/dev-apk-key is in the tree"},
		{"step-2 ab Step2, no DEV rule", "RC_BOTHSLOT_STEP2", true, noDev, "ab", true, ""},
		{"step-2 ab Step2, the release's rules: DEV marker", "RC_BOTHSLOT_STEP2", true, nil, "ab", false, "dev-apk-key"},
		{"step-2 payload not marked Step2", "RC_BOTHSLOT_STEP2", false, noDev, "ab", false, "the step-2 set is allowed only in a STEP-2 payload"},
		{"normal payload marked Step2", "RC_BOTHSLOT_NORMAL", true, noDev, "ab", false, "uc-pxx-step2"},
		{"step-2 payload, another uc-pxx-step2 pin", "RC_BOTHSLOT_STEP2", true, func(b *mainline.BothSlot) {
			noDev(b)
			b.Step2[1].SHA256 = strings.Repeat("0", 64)
		}, "ab", false, "the step-2 pin is"},
		{"a pin changed (tsx-autoupdate)", "RC_BOTHSLOT_NORMAL", false, func(b *mainline.BothSlot) {
			noDev(b)
			for i := range b.Pins {
				if b.Pins[i].Path == "/usr/local/sbin/tsx-autoupdate" {
					b.Pins[i].SHA256 = strings.Repeat("1", 64)
				}
			}
		}, "ab", false, "tsx-autoupdate has sha256"},
		{"a key missing from the set", "RC_BOTHSLOT_NORMAL", false, func(b *mainline.BothSlot) { noDev(b); b.APKKeys = b.APKKeys[:2] }, "ab", false,
			"is not a key of the both-slot key set"},
		{"a key added to the set", "RC_BOTHSLOT_NORMAL", false, func(b *mainline.BothSlot) {
			noDev(b)
			b.APKKeys = append(b.APKKeys, mainline.PinnedFile{Path: "/etc/apk/keys/x.rsa.pub", SHA256: strings.Repeat("2", 64)})
		}, "ab", false, "x.rsa.pub is not in the tree"},
		{"the list must name a ninth tool", "RC_BOTHSLOT_NORMAL", false, func(b *mainline.BothSlot) {
			noDev(b)
			b.ListPaths = append(b.ListPaths, "/usr/sbin/uc-pxx-reboot")
		}, "ab", false, "does not name /usr/sbin/uc-pxx-reboot"},
		{"the list names a tool not in the set", "RC_BOTHSLOT_NORMAL", false, func(b *mainline.BothSlot) { noDev(b); b.ListPaths = b.ListPaths[:7] },
			"ab", false, "which is not a both-slot boot tool"},
		{"the slot-A Image only", "RC_BOTHSLOT_NORMAL", false, func(b *mainline.BothSlot) {
			noDev(b)
			b.Kernels = map[string]string{"d02e92631f2810a4a43fb6f09824afb9b0044ee2": "593ccb89" + strings.Repeat("0", 56)}
		}, "ab", false, "kernel-reviewed"},
		{"the both-slot mark hook with the slot-A bootctl (11c2afcd review (2))", "RC_BOTHSLOT_NORMAL", false, func(b *mainline.BothSlot) {
			noDev(b)
			for i := range b.Pins {
				if b.Pins[i].Path == "/usr/sbin/uc-pxx-bootctl" {
					b.Pins[i].SHA256 = "38fea6d32412e7977ebc5845b138dc1c8c2df8555dae0b64b47f8a851b45e2d7"
				}
			}
		}, "ab", false, "never ships with /usr/sbin/uc-pxx-bootctl"},
		{"a STEP-2 payload for slot A only", "RC_BOTHSLOT_STEP2", true, noDev, "a", false, "payload-step2"},
	}
	for _, cs := range cases {
		t.Run(cs.name, func(t *testing.T) {
			p, rev := realPayload(t, cs.env)
			rev.Step2, rev.Slots = cs.step2, cs.slots
			b := mainline.EmbeddedBothSlot()
			b.Step2 = append([]mainline.PinnedFile(nil), step2set.Files...)
			b.Step2Other = append([]string(nil), step2set.Other...)
			if cs.edit != nil {
				cs.edit(&b)
			}
			g := mainline.TestGate([]mainline.Reviewed{rev}, map[string]string{}, mainline.EmbeddedBootControlPins()).WithBothSlot(b).WithVbmetaPatches(mainline.ReviewedVbmetaPatches())
			r := g.Check(p)
			bad := failedChecks(r)
			if r.OK() != cs.ok || !cs.ok && !strings.Contains(strings.Join(bad, "\n"), cs.want) {
				t.Fatalf("ok %v, want %v (%q): %v", r.OK(), cs.ok, cs.want, bad)
			}
			if cs.ok && (r.ImageSHA256 != "85f5a417e12feabb3c8a52b246ce8b89d6f9c70d2e6994525f69ff307f13f3d7" || len(r.Files) != 23) { // 18 boot/autoupdate/board pins + the 5 of the USB set (mainline-rc-4; review MINOR-6)
				t.Fatalf("image %s, %d files", r.ImageSHA256, len(r.Files))
			}
			t.Logf("%s: ok %v %v", cs.name, r.OK(), bad)
		})
	}
	// the gate of uc-pxx-step2 (no step-2 set): a STEP-2 payload is refused
	t.Run("a STEP-2 payload with the gate of uc-pxx-step2", func(t *testing.T) {
		p, rev := realPayload(t, "RC_BOTHSLOT_STEP2")
		rev.Step2 = true
		b := mainline.EmbeddedBothSlot()
		b.DevMarkers = nil
		r := mainline.TestGate([]mainline.Reviewed{rev}, map[string]string{}, mainline.EmbeddedBootControlPins()).WithBothSlot(b).Check(p)
		if bad := failedChecks(r); r.OK() || !strings.Contains(strings.Join(bad, "\n"), "this gate takes no STEP-2 payload") {
			t.Fatalf("ok %v: %v", r.OK(), bad)
		}
	})
	// the release's gate: not listed (no reviewed payload)
	p, _ := realPayload(t, "RC_BOTHSLOT_NORMAL")
	if r := mainline.EmbeddedGate().Check(p); r.OK() {
		t.Fatal("the release's gate passed a DEV payload")
	}
}
