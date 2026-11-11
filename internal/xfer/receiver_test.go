package xfer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// sendAll sends the fixture in the page's order and returns the PUT_END answers.
func sendAll(t *testing.T, h *host, f fixture) {
	t.Helper()
	for _, x := range []struct {
		name string
		data []byte
	}{{ManifestName, f.manifest}, {TemplateName, f.template}, {RootfsName, f.rootfs}} {
		b, e := h.put(x.name, x.data, int64(len(x.data)), DataChunk)
		if b["accept"] != true || e["ok"] != true {
			t.Fatalf("%s: %v %v", x.name, b, e)
		}
		if e["sha256"] != sum(x.data) {
			t.Fatalf("%s: sha256 %v", x.name, e["sha256"])
		}
	}
}

func TestHappyPathToReadyFolder(t *testing.T) {
	f := newFixture(3<<20 + 4096)
	g := newRig(t, Config{Pins: []string{f.pin}})
	h, wait := g.session()
	hello := h.req(TypeHello, struct{}{})
	if hello["state"] != StateWaiting || hello["proto"] != float64(1) || hello["usb"] != "1d6b:0105" {
		t.Fatalf("hello %v", hello)
	}
	if p := hello["pins"].([]any); len(p) != 1 || p[0] != f.pin {
		t.Fatalf("pins %v", hello["pins"])
	}
	for _, k := range []string{"serial", "mac", "key", "factory"} {
		if _, ok := hello[k]; ok {
			t.Fatalf("HELLO holds %q", k)
		}
	}
	sendAll(t, h, f)
	// a HELLO in the middle is fine
	if s := h.req(TypeStatus, struct{}{}); s["state"] != StateReceiving || s["manifest_sha256"] != f.pin {
		t.Fatalf("status %v", s)
	}
	c := h.req(TypeCommit, Commit{Auto: true})
	if c["ok"] != true || c["auto"] != true {
		t.Fatalf("commit %v", c)
	}
	if st := g.state(); st.State != StateComplete || st.ManifestSHA != f.pin {
		t.Fatalf("state %+v", st)
	}
	rd, err := CheckReady(g.dir, -1, []string{f.pin})
	if err != nil {
		t.Fatal(err)
	}
	if !rd.Record.Auto || rd.Record.Files[2].Size != int64(len(f.rootfs)) || rd.Record.Files[0].Size != int64(len(f.manifest)) {
		t.Fatalf("record %+v", rd.Record)
	}
	for _, x := range []struct {
		n string
		d []byte
	}{{ManifestName, f.manifest}, {TemplateName, f.template}, {RootfsName, f.rootfs}} {
		b, err := os.ReadFile(filepath.Join(g.dir, ReadyDir, x.n))
		if err != nil || !bytes.Equal(b, x.d) {
			t.Fatalf("%s in the ready folder: %v", x.n, err)
		}
	}
	// after the commit: everything is refused, the folder stays
	b, _ := h.put(ManifestName, f.manifest, int64(len(f.manifest)), DataChunk)
	if b["accept"] != false || !strings.Contains(b["reason"].(string), "complete") {
		t.Fatalf("PUT after the commit: %v", b)
	}
	if c := h.req(TypeCommit, Commit{}); c["ok"] != false {
		t.Fatalf("second commit %v", c)
	}
	h.l.disable()
	wait()
	// a new session cannot change it either
	h2, wait2 := g.session()
	if hl := h2.req(TypeHello, struct{}{}); hl["state"] != StateComplete {
		t.Fatalf("hello after the commit %v", hl)
	}
	h2.l.disable()
	wait2()
	if _, err := CheckReady(g.dir, -1, []string{f.pin}); err != nil {
		t.Fatal(err)
	}
	got := g.entries()
	sort.Strings(got)
	if strings.Join(got, ",") != "complete,complete.json,state.json" {
		t.Fatalf("folder %v", got)
	}
}

func TestOrderNamesSizesDuplicates(t *testing.T) {
	f := newFixture(1 << 20)
	g := newRig(t, Config{Pins: []string{f.pin}})
	h, wait := g.session()
	refuse := func(name string, size int64, want string) {
		t.Helper()
		a := h.req(TypePutBegin, PutBegin{Name: name, Size: size})
		if a["accept"] != false || !strings.Contains(a["reason"].(string), want) {
			t.Fatalf("PUT %q %d: %v (want %q)", name, size, a, want)
		}
	}
	refuse(RootfsName, int64(len(f.rootfs)), "manifest first")
	refuse(TemplateName, int64(len(f.template)), "manifest first")
	for _, n := range []string{"../../etc/passwd", "manifest.json/../x", "/manifest.json", "uc-pxx-mainline/manifest.json", "", "state.json", "complete"} {
		refuse(n, 10, "not a file of the payload")
	}
	refuse(ManifestName, 0, "1 to")
	refuse(ManifestName, MaxManifest+1, "1 to")
	refuse(ManifestName, -5, "1 to")
	if b, e := h.put(ManifestName, f.manifest, int64(len(f.manifest)), DataChunk); b["accept"] != true || e["ok"] != true {
		t.Fatalf("manifest %v %v", b, e)
	}
	refuse(ManifestName, int64(len(f.manifest)), "already received")
	refuse(RootfsName, int64(len(f.rootfs))+1, "the manifest says")
	refuse(RootfsName, int64(len(f.rootfs))-512, "the manifest says")
	if b, e := h.put(TemplateName, f.template, int64(len(f.template)), DataChunk); e["ok"] != true {
		t.Fatalf("template %v %v", b, e)
	}
	refuse(TemplateName, int64(len(f.template)), "already received")
	if c := h.req(TypeCommit, Commit{}); c["ok"] != false || !strings.Contains(c["reason"].(string), "2 of 3") {
		t.Fatalf("early commit %v", c)
	}
	h.l.disable()
	wait()
	// the cut session left nothing
	if e := g.entries(); len(e) != 1 || e[0] != StateFile {
		t.Fatalf("after the cut: %v", e)
	}
}

func TestManifestRefusals(t *testing.T) {
	f := newFixture(1 << 20)
	bad := append([]byte(nil), f.manifest...)
	bad = bytes.Replace(bad, []byte("v0.0.1-test"), []byte("v0.0.2-test"), 1)
	broken := []byte(`{"schema": 2}`)
	g := newRig(t, Config{Pins: []string{f.pin, sum(broken)}})
	h, wait := g.session()
	_, e := h.put(ManifestName, bad, int64(len(bad)), DataChunk)
	if e["ok"] != false || !strings.Contains(e["reason"].(string), "not one of the 2 payloads") {
		t.Fatalf("unpinned manifest: %v", e)
	}
	// pinned, but not a valid manifest: the parser of pass 1 refuses it
	_, e = h.put(ManifestName, broken, int64(len(broken)), DataChunk)
	if e["ok"] != false || !strings.Contains(e["reason"].(string), "mainline manifest") {
		t.Fatalf("broken pinned manifest: %v", e)
	}
	if a := h.req(TypePutBegin, PutBegin{Name: RootfsName, Size: int64(len(f.rootfs))}); a["accept"] != false {
		t.Fatalf("rootfs after refused manifests: %v", a)
	}
	// the right one is still taken in the same session
	if _, e := h.put(ManifestName, f.manifest, int64(len(f.manifest)), DataChunk); e["ok"] != true {
		t.Fatalf("manifest %v", e)
	}
	h.l.disable()
	wait()
}

func TestNoPins(t *testing.T) {
	f := newFixture(1 << 20)
	g := newRig(t, Config{})
	h, wait := g.session()
	if hl := h.req(TypeHello, struct{}{}); !strings.Contains(hl["detail"].(string), "no payload is pinned") {
		t.Fatalf("hello %v", hl)
	}
	a := h.req(TypePutBegin, PutBegin{Name: ManifestName, Size: int64(len(f.manifest))})
	if a["accept"] != false || !strings.Contains(a["reason"].(string), "no payload is pinned") {
		t.Fatalf("PUT without pins: %v", a)
	}
	h.l.disable()
	wait()
	if _, err := NewReceiver(Config{Dir: t.TempDir(), Pins: []string{"abc"}}); err == nil {
		t.Fatal("a pin that is not a sha256 was taken")
	}
}

func TestWrongBytesRefusedAndResent(t *testing.T) {
	f := newFixture(2 << 20)
	g := newRig(t, Config{Pins: []string{f.pin}})
	h, wait := g.session()
	h.put(ManifestName, f.manifest, int64(len(f.manifest)), DataChunk)
	evil := append([]byte(nil), f.rootfs...)
	evil[len(evil)/2] ^= 1
	_, e := h.put(RootfsName, evil, int64(len(evil)), DataChunk)
	if e["ok"] != false || !strings.Contains(e["reason"].(string), "is not the manifest's") {
		t.Fatalf("changed rootfs: %v", e)
	}
	if _, err := os.Stat(filepath.Join(g.dir, "incoming.1", RootfsName)); !os.IsNotExist(err) {
		t.Fatalf("the refused file stays: %v", err)
	}
	if _, e := h.put(RootfsName, f.rootfs, int64(len(f.rootfs)), DataChunk); e["ok"] != true {
		t.Fatalf("resend %v", e)
	}
	h.put(TemplateName, f.template, int64(len(f.template)), DataChunk)
	if c := h.req(TypeCommit, Commit{}); c["ok"] != true || c["auto"] != false {
		t.Fatalf("commit %v", c)
	}
	rd, err := CheckReady(g.dir, -1, []string{f.pin})
	if err != nil || rd.Record.Auto {
		t.Fatalf("ready %v %+v", err, rd)
	}
	h.l.disable()
	wait()
}

func TestRAMRefusalBeforeAnyByte(t *testing.T) {
	f := newFixture(4 << 20)
	g := newRig(t, Config{Pins: []string{f.pin}, Reserve: 1 << 20})
	h, wait := g.session()
	h.put(ManifestName, f.manifest, int64(len(f.manifest)), DataChunk)
	g.mu.Lock()
	g.mem = 3 << 20 // 2 MiB room
	g.mu.Unlock()
	a := h.req(TypePutBegin, PutBegin{Name: RootfsName, Size: int64(len(f.rootfs))})
	if a["accept"] != false || !strings.Contains(a["reason"].(string), "not enough free RAM") {
		t.Fatalf("RAM: %v", a)
	}
	h.noReply()
	h.l.disable()
	wait()
}

// The desync cases: the host sends more bytes than it announced (the rest is
// read as a frame: no magic, the session ends and halts its endpoints), a
// frame in place of data (taken as data: the sha256 refuses it), and a bad
// frame (the session ends, its folder is removed, the endpoints halt).
func TestDesyncAndBadFrames(t *testing.T) {
	f := newFixture(1 << 20)
	t.Run("overrun", func(t *testing.T) {
		g := newRig(t, Config{Pins: []string{f.pin}})
		h, wait := g.session()
		h.put(ManifestName, f.manifest, int64(len(f.manifest)), DataChunk)
		h.req(TypePutBegin, PutBegin{Name: TemplateName, Size: int64(len(f.template))})
		h.l.hostOut(append(append([]byte(nil), f.template...), []byte("more bytes than announced")...))
		e := h.reply(TypePutEnd | TypeReply)
		if e["ok"] != true {
			t.Fatalf("template %v", e)
		}
		wait()
		if !h.l.halted {
			t.Fatal("the endpoints were not halted after a bad frame")
		}
		if e := g.entries(); len(e) != 1 {
			t.Fatalf("after the bad frame: %v", e)
		}
		if st := g.state(); st.State != StateWaiting || !strings.Contains(st.Detail, "no magic") {
			t.Fatalf("state %+v", st)
		}
	})
	t.Run("overrun-last-transfer", func(t *testing.T) {
		g := newRig(t, Config{Pins: []string{f.pin}})
		h, wait := g.session()
		h.put(ManifestName, f.manifest, int64(len(f.manifest)), DataChunk)
		odd := append([]byte(nil), f.template[:700]...)
		h.req(TypePutBegin, PutBegin{Name: TemplateName, Size: int64(len(f.template))})
		// the template in transfers of 1000 bytes (each read ends at the short
		// packet), but the rest and 700 more bytes in one transfer: the last read
		// (of what is left) meets a packet that does not fit: EOVERFLOW, the
		// session ends
		r := len(f.template) % 1000
		for o := 0; o < len(f.template)-r; o += 1000 {
			h.l.hostOut(f.template[o : o+1000])
		}
		h.l.hostOut(append(append([]byte(nil), f.template[len(f.template)-r:]...), odd...))
		wait()
		if e := g.entries(); len(e) != 1 {
			t.Fatalf("after EOVERFLOW: %v", e)
		}
	})
	t.Run("frame-as-data", func(t *testing.T) {
		g := newRig(t, Config{Pins: []string{f.pin}})
		h, wait := g.session()
		h.put(ManifestName, f.manifest, int64(len(f.manifest)), DataChunk)
		h.req(TypePutBegin, PutBegin{Name: TemplateName, Size: int64(len(f.template))})
		fr := h.frame(TypeCommit, Commit{Auto: true})
		junk := bytes.Repeat(fr, len(f.template)/len(fr)+1)[:len(f.template)]
		h.l.hostOut(junk)
		if e := h.reply(TypePutEnd | TypeReply); e["ok"] != false {
			t.Fatalf("a frame taken as data was accepted: %v", e)
		}
		h.l.disable()
		wait()
	})
	for name, b := range map[string][]byte{
		"magic":   []byte("XXX1\x01\x00\x00\x00\x02\x00\x00\x00\x01\x00\x00\x00{}"),
		"flags":   []byte("PXX1\x01\x00\x01\x00\x02\x00\x00\x00\x01\x00\x00\x00{}"),
		"length":  []byte("PXX1\x01\x00\x00\x00\x09\x00\x00\x00\x01\x00\x00\x00{}"),
		"type":    []byte("PXX1\x07\x00\x00\x00\x02\x00\x00\x00\x01\x00\x00\x00{}"),
		"reply":   []byte("PXX1\x81\x00\x00\x00\x02\x00\x00\x00\x01\x00\x00\x00{}"),
		"json":    []byte("PXX1\x02\x00\x00\x00\x02\x00\x00\x00\x01\x00\x00\x00{]"),
		"unknown": []byte("PXX1\x02\x00\x00\x00\x0c\x00\x00\x00\x01\x00\x00\x00{\"evil\":1}  "),
		"short":   []byte("PXX1\x01"),
	} {
		t.Run(name, func(t *testing.T) {
			g := newRig(t, Config{Pins: []string{f.pin}})
			h, wait := g.session()
			h.put(ManifestName, f.manifest, int64(len(f.manifest)), DataChunk)
			h.l.hostOut(b)
			wait()
			if !h.l.halted {
				t.Fatal("not halted")
			}
			if e := g.entries(); len(e) != 1 {
				t.Fatalf("left %v", e)
			}
		})
	}
}

// The idle timer: a session that holds a partial folder and gets nothing for
// Idle loses the folder at once and asks for a reconnect; its blocked read
// then ends (the reconnect disables the endpoints) and the session ends.
func TestIdleRemovesPartialFolder(t *testing.T) {
	f := newFixture(4 << 20)
	g := newRig(t, Config{Pins: []string{f.pin}, Idle: 30 * time.Second})
	h, wait := g.session()
	h.put(ManifestName, f.manifest, int64(len(f.manifest)), DataChunk)
	h.req(TypePutBegin, PutBegin{Name: RootfsName, Size: int64(len(f.rootfs))})
	h.l.hostOut(f.rootfs[:2<<20])
	part := filepath.Join(g.dir, "incoming.1", RootfsName)
	waitSize(t, part, 2<<20) // the receiver writes the 2 MiB
	g.advance(29 * time.Second)
	g.r.idleCheck()
	if _, err := os.Stat(part); err != nil {
		t.Fatal("removed before the idle time")
	}
	g.advance(2 * time.Second)
	g.r.idleCheck()
	if _, err := os.Stat(filepath.Join(g.dir, "incoming.1")); !os.IsNotExist(err) {
		t.Fatalf("the idle folder stays: %v", err)
	}
	if g.reconnects() != 1 {
		t.Fatalf("reconnects %d", g.reconnects())
	}
	if st := g.state(); st.State != StateWaiting || !strings.Contains(st.Detail, "idle") {
		t.Fatalf("state %+v", st)
	}
	// the supervisor's reconnect disables the endpoints: the read ends
	h.l.disable()
	wait()
	if e := g.entries(); len(e) != 1 {
		t.Fatalf("left %v", e)
	}
	// a session with no folder is never ended by the timer
	h2, wait2 := g.session()
	h2.req(TypeHello, struct{}{})
	g.advance(time.Hour)
	g.r.idleCheck()
	if g.reconnects() != 1 {
		t.Fatal("an idle session without a folder was ended")
	}
	h2.req(TypeHello, struct{}{})
	h2.l.disable()
	wait2()
}

// Bytes that come after the idle end are never written: the session is dead.
func TestIdleThenLateBytes(t *testing.T) {
	f := newFixture(4 << 20)
	g := newRig(t, Config{Pins: []string{f.pin}})
	h, wait := g.session()
	h.put(ManifestName, f.manifest, int64(len(f.manifest)), DataChunk)
	h.req(TypePutBegin, PutBegin{Name: RootfsName, Size: int64(len(f.rootfs))})
	h.l.hostOut(f.rootfs[:1<<20])
	waitSize(t, filepath.Join(g.dir, "incoming.1", RootfsName), 1<<20)
	g.advance(time.Minute)
	g.r.idleCheck()
	h.l.hostOut(f.rootfs[1<<20 : 2<<20])
	wait()
	if e := g.entries(); len(e) != 1 {
		t.Fatalf("left %v", e)
	}
}

// Each session stages into its own folder, and a session's clean-up removes
// only its own folder (the prototype's race, §9 finding 2).
func TestSessionsOwnFolders(t *testing.T) {
	f := newFixture(2 << 20)
	g := newRig(t, Config{Pins: []string{f.pin}})
	h1, wait1 := g.session()
	h1.put(ManifestName, f.manifest, int64(len(f.manifest)), DataChunk)
	h1.req(TypePutBegin, PutBegin{Name: RootfsName, Size: int64(len(f.rootfs))})
	h1.l.hostOut(f.rootfs[:1<<20])
	waitSize(t, filepath.Join(g.dir, "incoming.1", RootfsName), 1<<20)
	h1.l.disable() // SET_INTERFACE of a new page
	wait1()
	h2, wait2 := g.session()
	h2.put(ManifestName, f.manifest, int64(len(f.manifest)), DataChunk)
	if _, err := os.Stat(filepath.Join(g.dir, "incoming.2", ManifestName)); err != nil {
		t.Fatal(err)
	}
	// a late end of session 1 (as the prototype's stale goroutine) touches only incoming.1
	old := &session{r: g.r, n: 1, dir: filepath.Join(g.dir, "incoming.1"), made: true}
	old.end("late clean-up", endLink)
	if _, err := os.Stat(filepath.Join(g.dir, "incoming.2", ManifestName)); err != nil {
		t.Fatalf("a late clean-up of session 1 removed session 2's file: %v", err)
	}
	h2.put(TemplateName, f.template, int64(len(f.template)), DataChunk)
	h2.put(RootfsName, f.rootfs, int64(len(f.rootfs)), DataChunk)
	if c := h2.req(TypeCommit, Commit{}); c["ok"] != true {
		t.Fatalf("commit %v", c)
	}
	h2.l.disable()
	wait2()
	if rd, err := CheckReady(g.dir, -1, []string{f.pin}); err != nil || rd.Record.Session != 2 {
		t.Fatalf("ready %v %+v", err, rd)
	}
}

func TestServeRefusesOverlap(t *testing.T) {
	g := newRig(t, Config{})
	h, wait := g.session()
	h.req(TypeHello, struct{}{})
	defer func() {
		if recover() == nil {
			t.Fatal("a second session while one runs")
		}
		h.l.disable()
		wait()
	}()
	g.r.Serve(newLink())
}

func TestStatusOnly(t *testing.T) {
	f := newFixture(1 << 20)
	info := filepath.Join(t.TempDir(), "info.json")
	os.WriteFile(info, []byte(`{"model":"UC-P10","slot":"b","token":"pxx-inst-abc123","step2":"started 0123456789ab",
		"plan_id":"0123456789ab","lines":["writing system_b 40%","\u0007bell"],"serial":"BBE4ZZDT24","mac":"00:11"}`), 0o644)
	g := newRig(t, Config{StatusOnly: true, InfoFile: info, Pins: []string{f.pin}})
	h, wait := g.session()
	hl := h.req(TypeHello, struct{}{})
	if hl["state"] != StateStatusOnly || hl["status_only"] != true {
		t.Fatalf("hello %v", hl)
	}
	in := hl["info"].(map[string]any)
	if in["slot"] != "b" || in["step2"] != "started 0123456789ab" || in["plan_id"] != "0123456789ab" {
		t.Fatalf("info %v", in)
	}
	if l := in["lines"].([]any); len(l) != 1 || l[0] != "writing system_b 40%" {
		t.Fatalf("lines %v", l)
	}
	for _, k := range []string{"serial", "mac"} {
		if _, ok := in[k]; ok {
			t.Fatalf("info passes %q", k)
		}
	}
	if a := h.req(TypePutBegin, PutBegin{Name: ManifestName, Size: int64(len(f.manifest))}); a["accept"] != false ||
		!strings.Contains(a["reason"].(string), "status only") {
		t.Fatalf("PUT in status-only: %v", a)
	}
	if c := h.req(TypeCommit, Commit{}); c["ok"] != false {
		t.Fatalf("commit %v", c)
	}
	st := h.req(TypeStatus, struct{}{})
	if st["state"] != StateStatusOnly || st["info"].(map[string]any)["step2"] != "started 0123456789ab" {
		t.Fatalf("status %v", st)
	}
	// a bad info file: no info at all
	os.WriteFile(info, bytes.Repeat([]byte("x"), maxInfo+1), 0o644)
	if hl := h.req(TypeHello, struct{}{}); hl["info"] != nil {
		t.Fatalf("info of a bad file %v", hl["info"])
	}
	os.Remove(info)
	os.Symlink("/etc/passwd", info)
	if hl := h.req(TypeHello, struct{}{}); hl["info"] != nil {
		t.Fatalf("info through a link %v", hl["info"])
	}
	h.l.disable()
	wait()
}

func TestStartCleansAndRefusesReady(t *testing.T) {
	d := t.TempDir()
	os.MkdirAll(filepath.Join(d, "incoming.7"), 0o700)
	os.WriteFile(filepath.Join(d, "incoming.7", RootfsName), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(d, ReadyRecord), []byte("{}"), 0o600)
	os.WriteFile(filepath.Join(d, ".state.json.tmp"), []byte("{}"), 0o600)
	if _, err := NewReceiver(Config{Dir: d}); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(d)
	if len(ents) != 1 || ents[0].Name() != StateFile {
		t.Fatalf("left %v", ents)
	}
	os.Mkdir(filepath.Join(d, ReadyDir), 0o700)
	if _, err := NewReceiver(Config{Dir: d}); err == nil || !strings.Contains(err.Error(), "waits for the seal") {
		t.Fatalf("a ready folder at the start: %v", err)
	}
}

func TestZeroLengthTransfers(t *testing.T) {
	f := newFixture(1 << 20)
	g := newRig(t, Config{Pins: []string{f.pin}})
	h, wait := g.session()
	h.l.hostOut(nil) // one zero-length transfer is skipped
	if hl := h.req(TypeHello, struct{}{}); hl["state"] != StateWaiting {
		t.Fatalf("hello %v", hl)
	}
	for i := 0; i <= maxZeroReads+1; i++ {
		h.l.hostOut(nil)
	}
	wait()
	if !h.l.halted {
		t.Fatal("not halted after a run of zero-length transfers")
	}
}

// Every frame read is one read of FrameRead bytes: a frame of the largest
// body is read whole (no EOVERFLOW).
func TestFrameReadsAreWhole(t *testing.T) {
	g := newRig(t, Config{})
	h, wait := g.session()
	j := append([]byte("{"), bytes.Repeat([]byte(" "), MaxBody-3)...)
	j = append(j, '}')
	fr, err := EncodeFrame(TypeHello, 9, j)
	if err != nil {
		t.Fatal(err)
	}
	h.l.hostOut(fr)
	h.reply(TypeHello | TypeReply)
	h.l.disable()
	wait()
	for _, n := range h.l.reads {
		if n != FrameRead {
			t.Fatalf("a frame read of %d bytes", n)
		}
	}
}

// waitSize waits (at most 10 s) until the file p has n bytes (the receiver
// writes in its own goroutine; no fixed sleep: review F12).
func waitSize(t *testing.T, p string, n int64) {
	t.Helper()
	end := time.Now().Add(10 * time.Second)
	for {
		if fi, err := os.Stat(p); err == nil && fi.Size() == n {
			return
		}
		if time.Now().After(end) {
			fi, err := os.Stat(p)
			t.Fatalf("%s: not %d bytes in 10 s (%v %v)", p, n, fi, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A session that reads across a link change (the new page's first frame
// after a SET_INTERFACE, read by the old session's read) hands the bytes to
// the next session, which answers them (review F6).
func TestStaleReadIsCarried(t *testing.T) {
	g := newRig(t, Config{})
	l := newLink()
	done := make(chan struct{})
	go func() { g.r.Serve(l); close(done) }()
	h := &host{t: t, l: l}
	h.req(TypeHello, struct{}{})
	g.r.LinkEvent() // ENABLE of a new page while session 1 waits in its read
	l.hostOut(h.frame(TypeHello, struct{}{}))
	<-done
	h.noReply() // session 1 did not answer the new page's frame
	done2 := make(chan struct{})
	go func() { g.r.Serve(l); close(done2) }() // the next session (the same endpoints)
	if hl := h.reply(TypeHello | TypeReply); hl["session"] != float64(2) {
		t.Fatalf("the carried HELLO: %v", hl)
	}
	l.disable()
	<-done2
	// a read that would start after a change does not start
	g2 := newRig(t, Config{})
	l2 := newLink()
	g2.r.LinkEvent()
	s := &session{r: g2.r, gen: 0}
	if _, err := s.read(l2, make([]byte, 16)); err != errStale {
		t.Fatalf("a read after a link change: %v", err)
	}
}
