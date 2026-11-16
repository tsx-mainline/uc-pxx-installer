package xfer

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uc-pxx-installer/internal/provision/bundle/bundletest"
	"uc-pxx-installer/internal/xfer/handover"
)

func provProfiles(b *bundletest.Bundle) []handover.ProvisionProfile {
	return []handover.ProvisionProfile{
		{Name: "console"},
		{Name: "kiosk", File: "uc-pxx-profile-kiosk-0.2.0-rc10.tar", Size: b.Pins.Size, SHA256: b.Pins.SHA256, IndexSHA256: b.Pins.IndexSHA256},
	}
}

func provRig(t *testing.T, b *bundletest.Bundle, mod func(*Config)) (*rig, string) {
	t.Helper()
	prog := filepath.Join(t.TempDir(), "progress.json")
	c := Config{Kind: KindProvision, ProvProfiles: provProfiles(b), ProgressFile: prog, Seq: 4}
	if mod != nil {
		mod(&c)
	}
	return newRig(t, c), prog
}

// TestProvisionHello: HELLO of the kind provision (record §6.10): mode
// provision, the state from the root's progress file, the profiles with
// null file, size and sha256 for console, the profile "" until a COMMIT.
func TestProvisionHello(t *testing.T) {
	b := bundletest.Simple("0.2.0-rc10", true)
	g, prog := provRig(t, b, nil)
	h, wait := g.session()
	hl := h.req(TypeHello, struct{}{})
	ps := hl["profiles"].([]any)
	c, k := ps[0].(map[string]any), ps[1].(map[string]any)
	if hl["mode"] != "provision" || hl["state"] != "waiting" || hl["profile"] != "" || len(ps) != 2 || len(hl["pins"].([]any)) != 0 {
		t.Fatalf("hello %v", hl)
	}
	if c["name"] != "console" || c["file"] != nil || c["size"] != nil || c["sha256"] != nil {
		t.Fatalf("console %v", c)
	}
	if k["name"] != "kiosk" || k["size"] != float64(b.Pins.Size) || k["sha256"] != b.Pins.SHA256 || k["file"] != "uc-pxx-profile-kiosk-0.2.0-rc10.tar" {
		t.Fatalf("kiosk %v", k)
	}
	// the root's progress, filtered
	handover.WriteProgress(prog, handover.Progress{State: "downloading", Done: 10, Total: 100, Fail: "network", Reason: "x\x01y", Profile: "kiosk"})
	st := h.req(TypeStatus, struct{}{})
	if st["state"] != "downloading" || st["done"] != float64(10) || st["total"] != float64(100) || st["fail"] != nil || st["reason"] != nil ||
		st["profile"] != "kiosk" || st["mode"] != "provision" {
		t.Fatalf("status %v", st)
	}
	handover.WriteProgress(prog, handover.Progress{State: "failed", Fail: "network", Reason: "no route", Total: 5})
	if st := h.req(TypeStatus, struct{}{}); st["state"] != "failed" || st["fail"] != "network" || st["reason"] != "no route" {
		t.Fatalf("status %v", st)
	}
	handover.WriteProgress(prog, handover.Progress{State: "retry-wait", NextTryS: 30, Fail: "network"})
	if st := h.req(TypeStatus, struct{}{}); st["state"] != "retry-wait" || st["next_try_s"] != float64(30) || st["fail"] != nil {
		t.Fatalf("status %v", st)
	}
	handover.WriteProgress(prog, handover.Progress{State: "hacked"})
	if st := h.req(TypeStatus, struct{}{}); st["state"] != "waiting" {
		t.Fatalf("status %v", st)
	}
	h.l.disable()
	wait()
}

// TestProvisionCommit: the choice: each refusal of §6.10, then the record
// provision.json with the next sequence number, read by the root.
func TestProvisionCommit(t *testing.T) {
	b := bundletest.Simple("0.2.0-rc10", true)
	g, prog := provRig(t, b, nil)
	h, wait := g.session()
	refuse := func(body map[string]any, want string) {
		t.Helper()
		c := h.req(TypeCommit, body)
		if c["ok"] != false || !strings.Contains(c["reason"].(string), want) {
			t.Fatalf("%v: %v (want %q)", body, c, want)
		}
		if _, err := handover.ReadChoice(g.dir, os.Getuid()); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("a record after a refusal: %v", err)
		}
	}
	refuse(map[string]any{"profile": "kiosk-ha", "source": "net"}, "not one of this unit's profiles")
	refuse(map[string]any{"profile": "kiosk", "source": "usb"}, "net or cable")
	refuse(map[string]any{"profile": "kiosk", "source": "cable", "url": "http://x/"}, "only with the source net")
	refuse(map[string]any{"profile": "kiosk", "source": "net", "url": ""}, "a url of 0 bytes")
	for _, u := range []string{"http://mirror.example:8137", "http://h", "ftp://h/", "http:///x/", "http://u:p@h/", "http://h/x?y", "http://h/x#y", "http://h/file.tar", "http://h/ x/",
		"http://h/" + strings.Repeat("a", 600) + "/"} {
		refuse(map[string]any{"profile": "kiosk", "source": "net", "url": u}, "url")
	}
	refuse(map[string]any{"profile": "console", "source": "cable"}, "no bundle")
	refuse(map[string]any{"profile": "console", "source": "net"}, "no root ssh key")
	handover.WriteProgress(prog, handover.Progress{State: "installing"})
	refuse(map[string]any{"profile": "kiosk", "source": "net"}, "the unit works now (installing)")
	handover.WriteProgress(prog, handover.Progress{State: "failed", Fail: "network"})
	if c := h.req(TypeCommit, map[string]any{"profile": "kiosk", "source": "net", "url": "http://mirror.example:8137/r23/"}); c["ok"] != true || len(c) != 1 {
		t.Fatalf("commit %v", c)
	}
	ch, err := handover.ReadChoice(g.dir, os.Getuid())
	if err != nil || ch.Seq != 5 || ch.Profile != "kiosk" || ch.Source != "net" || ch.URL != "http://mirror.example:8137/r23/" {
		t.Fatalf("choice %+v %v", ch, err)
	}
	if _, err := handover.ReadChoice(g.dir, os.Getuid()+1); err == nil {
		t.Fatal("the record was taken of another user")
	}
	if hl := h.req(TypeHello, struct{}{}); hl["profile"] != "kiosk" {
		t.Fatalf("hello %v", hl)
	}
	if c := h.req(TypeCommit, map[string]any{"profile": "kiosk", "source": "cable"}); c["ok"] != true {
		t.Fatalf("commit %v", c)
	}
	if ch, _ := handover.ReadChoice(g.dir, os.Getuid()); ch.Seq != 6 || ch.Source != "cable" || ch.URL != "" {
		t.Fatalf("second choice %+v", ch)
	}
	// an unknown key is a protocol violation of this kind
	h.l.hostOut(h.frame(TypeCommit, map[string]any{"profile": "kiosk", "source": "net", "auto": true}))
	h.noReply()
	wait()
	// the fixed profile and the console with a seeded key
	g2, _ := provRig(t, b, func(c *Config) { c.Fixed = "kiosk"; c.ConsoleKey = true })
	h2, wait2 := g2.session()
	if hl := h2.req(TypeHello, struct{}{}); hl["profile"] != "kiosk" {
		t.Fatalf("hello %v", hl)
	}
	if c := h2.req(TypeCommit, map[string]any{"profile": "console", "source": "net"}); c["ok"] != false || !strings.Contains(c["reason"].(string), "fixed: kiosk") {
		t.Fatalf("commit %v", c)
	}
	h2.l.disable()
	wait2()
	g3, _ := provRig(t, b, func(c *Config) { c.ConsoleKey = true })
	h3, wait3 := g3.session()
	if c := h3.req(TypeCommit, map[string]any{"profile": "console", "source": "net"}); c["ok"] != true {
		t.Fatalf("console commit %v", c)
	}
	h3.l.disable()
	wait3()
	// status only takes no choice
	g4, _ := provRig(t, b, func(c *Config) { c.StatusOnly = true })
	h4, wait4 := g4.session()
	if hl := h4.req(TypeHello, struct{}{}); hl["status_only"] != true || hl["mode"] != "provision" {
		t.Fatalf("hello %v", hl)
	}
	if c := h4.req(TypeCommit, map[string]any{"profile": "kiosk", "source": "net"}); c["ok"] != false || !strings.Contains(c["reason"].(string), "status only") {
		t.Fatalf("commit %v", c)
	}
	h4.l.disable()
	wait4()
	for _, bad := range []Config{{Kind: KindProvision, Fixed: "kiosk"}, {Kind: KindProvision, Pins: []string{strings.Repeat("a", 64)}},
		{Kind: KindProvision, ProvProfiles: []handover.ProvisionProfile{{Name: "console", File: "x"}}},
		{Kind: KindProvision, ProvProfiles: []handover.ProvisionProfile{{Name: "console"}, {Name: "console"}}},
		{Kind: KindPayload, ProgressFile: "x"}, {Kind: KindConsent, PlanID: "0123456789ab", Fixed: "kiosk"}} {
		bad.Dir = t.TempDir()
		if _, err := NewReceiver(bad); err == nil {
			t.Fatalf("receiver %+v taken", bad)
		}
	}
}

// TestProvisionCable: the bundle over the cable: no PUT before a cable
// COMMIT of this session; only the pinned file and size; the bundle is
// unpacked into the session folder; PUT_END ok is the hand-over (complete.json,
// complete/ with exactly the members); a bundle that is not the pin leaves
// nothing and may be sent again.
func TestProvisionCable(t *testing.T) {
	b := bundletest.Simple("0.2.0-rc10", true)
	g, _ := provRig(t, b, nil)
	h, wait := g.session()
	file := "uc-pxx-profile-kiosk-0.2.0-rc10.tar"
	if a, _ := h.put(file, b.Tar, b.Pins.Size, DataChunk); a["accept"] != false || !strings.Contains(a["reason"].(string), "COMMIT with the source cable") {
		t.Fatalf("PUT before the commit %v", a)
	}
	if c := h.req(TypeCommit, map[string]any{"profile": "kiosk", "source": "cable"}); c["ok"] != true {
		t.Fatalf("commit %v", c)
	}
	for _, x := range []struct {
		n string
		s int64
	}{{"other.tar", b.Pins.Size}, {file, b.Pins.Size - 1}} {
		if a := h.req(TypePutBegin, PutBegin{Name: x.n, Size: x.s}); a["accept"] != false || !strings.Contains(a["reason"].(string), "not the pinned bundle") {
			t.Fatalf("PUT %v: %v", x, a)
		}
	}
	// one wrong byte in a package: refused, nothing kept
	bad := append([]byte(nil), b.Tar...)
	i := bytes.Index(bad, bytes.Repeat([]byte("c"), 1000))
	bad[i+10] = 'z'
	a, e := h.put(file, bad, b.Pins.Size, DataChunk)
	if a["accept"] != true || e["ok"] != false || !strings.Contains(e["reason"].(string), "chromium") {
		t.Fatalf("a wrong bundle: %v %v", a, e)
	}
	if got := g.entries(); strings.Join(got, ",") != "provision.json,state.json" {
		t.Fatalf("after a refused bundle: %v", got)
	}
	// the right one, in the same session
	a, e = h.put(file, b.Tar, b.Pins.Size, DataChunk)
	if a["accept"] != true || e["ok"] != true || e["sha256"] != b.Pins.SHA256 {
		t.Fatalf("the bundle: %v %v", a, e)
	}
	rd, err := handover.CheckProvisionReady(g.dir, os.Getuid(), provProfiles(b)[1])
	if err != nil {
		t.Fatal(err)
	}
	for p, want := range b.Members {
		got, err := os.ReadFile(filepath.Join(rd.Folder, filepath.FromSlash(p)))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s in the hand-over: %v", p, err)
		}
	}
	n := 0
	filepath.Walk(rd.Folder, func(p string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() {
			n++
		}
		return nil
	})
	if n != len(b.Members) {
		t.Fatalf("%d files in the hand-over, the bundle has %d", n, len(b.Members))
	}
	if st := h.req(TypeStatus, struct{}{}); st["state"] != "receiving" || st["done"] != st["total"] || st["total"] != float64(b.Pins.Size) {
		t.Fatalf("status %v", st)
	}
	if a, _ := h.put(file, b.Tar, b.Pins.Size, DataChunk); a["accept"] != false {
		t.Fatalf("a second PUT %v", a)
	}
	if c := h.req(TypeCommit, map[string]any{"profile": "kiosk", "source": "net"}); c["ok"] != false {
		t.Fatalf("a COMMIT after the hand-over %v", c)
	}
	h.l.disable()
	wait()
	// another profile's pin does not take this hand-over
	other := provProfiles(b)[1]
	other.SHA256 = strings.Repeat("e", 64)
	if _, err := handover.CheckProvisionReady(g.dir, os.Getuid(), other); err == nil {
		t.Fatal("the hand-over was taken for another pin")
	}
	// a new session after the link went: a cable PUT needs a new COMMIT
	g2, _ := provRig(t, b, nil)
	h2, wait2 := g2.session()
	h2.req(TypeCommit, map[string]any{"profile": "kiosk", "source": "cable"})
	h2.l.disable()
	wait2()
	h3, wait3 := g2.session()
	if a, _ := h3.put(file, b.Tar, b.Pins.Size, DataChunk); a["accept"] != false {
		t.Fatalf("a PUT in a session without the COMMIT %v", a)
	}
	h3.l.disable()
	wait3()
}
