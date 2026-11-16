package xfer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"uc-pxx-installer/internal/sshkey"
	"uc-pxx-installer/internal/xfer/handover"
)

// The step-1 kind of the RAM installer (mainline-rc-10, I-6; owner R21 for
// the compressed root file system).

const testKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBbrhXWbO19v2SgtcPAMqpGOt8V5hUqtgQyUCJxvGtTp test-ed25519\n"

// fwFixture is a made-up firmware file and its pin.
func fwFixture(n int) ([]byte, FWPin) {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + i/251)
	}
	return b, FWPin{Size: int64(n), SHA256: sum(b)}
}

// zstText is the text of testdata/rootfs-text-3m.zst (zstd -19
// --zstd=wlog=23 of it): 3 MiB of numbered lines.
func zstText(t *testing.T) (plain, packed []byte) {
	t.Helper()
	var b bytes.Buffer
	for i := 0; b.Len() < 3<<20; i++ {
		fmt.Fprintf(&b, "rootfs line %d: uc-pxx step-1 compressed fixture %x %d\n", i, (uint64(i)*2654435761)&0xffffffff, i%97)
	}
	plain = b.Bytes()[:3<<20]
	if sum(plain) != "ce5b8ee9cc3a8a4ff0388bb0089a51dff93dd54b56dbcf8d75f8eba583192f68" {
		t.Fatalf("the plain text is not the fixture's: %s", sum(plain))
	}
	packed, err := os.ReadFile("testdata/rootfs-text-3m.zst")
	if err != nil {
		t.Fatal(err)
	}
	return plain, packed
}

// zstFixture is a schema-3 bundle whose root file system is the 3 MiB text
// and whose rootfs_zst is the packed file (or other values: edit).
type zfix struct {
	manifest, template, rootfs, packed []byte
	pin                                string
}

func newZstFixture(t *testing.T, edit func(m map[string]any)) zfix {
	t.Helper()
	plain, packed := zstText(t)
	f := newFixture(4096)
	var m map[string]any
	if err := json.Unmarshal(f.manifest, &m); err != nil {
		t.Fatal(err)
	}
	m["schema"] = 3
	m["rootfs"].(map[string]any)["size"] = len(plain)
	m["rootfs"].(map[string]any)["sha256"] = sum(plain)
	m["rootfs_zst"] = map[string]any{"file": RootfsZstName, "size": len(packed), "sha256": sum(packed)}
	if edit != nil {
		edit(m)
	}
	j, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return zfix{manifest: j, template: f.template, rootfs: plain, packed: packed, pin: sum(j)}
}

func step1Rig(t *testing.T, cfg Config) *rig {
	cfg.Kind = KindStep1
	return newRig(t, cfg)
}

func TestStep1FirmwareMainlineWithKey(t *testing.T) {
	fw, pin := fwFixture(3<<20 + 123)
	g := step1Rig(t, Config{Stage: Step1Firmware, FWPins: []FWPin{pin}})
	h, wait := g.session()
	hl := h.req(TypeHello, struct{}{})
	if hl["mode"] != "step1" || hl["state"] != StateWaitingFirmware || hl["stage"] != Step1Firmware || hl["usb"] != "1d6b:0105" {
		t.Fatalf("hello %v", hl)
	}
	if p := hl["fw_pins"].([]any); len(p) != 1 || p[0].(map[string]any)["sha256"] != pin.SHA256 || p[0].(map[string]any)["size"] != float64(pin.Size) {
		t.Fatalf("fw_pins %v", hl["fw_pins"])
	}
	if b, e := h.put(FirmwareName, fw, int64(len(fw)), DataChunk); b["accept"] != true || e["ok"] != true || e["sha256"] != pin.SHA256 {
		t.Fatalf("firmware %v %v", b, e)
	}
	if b, e := h.put(SSHKeyName, []byte(testKey), int64(len(testKey)), DataChunk); b["accept"] != true || e["ok"] != true {
		t.Fatalf("key %v %v", b, e)
	}
	c := h.req(TypeCommit, Commit{Mode: handover.ModeMainline})
	if c["ok"] != true || c["mode"] != "mainline" || c["auto"] != false {
		t.Fatalf("commit %v", c)
	}
	if st := g.state(); st.State != StateFirmwareComplete || st.ManifestSHA != pin.SHA256 {
		t.Fatalf("state %+v", st)
	}
	rd, err := handover.CheckReadyStep1FW(g.dir, -1, []FWPin{pin})
	if err != nil {
		t.Fatal(err)
	}
	if rd.Record.Mode != "mainline" || len(rd.Record.Files) != 2 || rd.Record.Files[1].Name != SSHKeyName ||
		rd.Record.Files[1].SHA256 != sum([]byte(testKey)) {
		t.Fatalf("record %+v", rd.Record)
	}
	got, _ := os.ReadFile(filepath.Join(g.dir, ReadyDir, FirmwareName))
	if !bytes.Equal(got, fw) {
		t.Fatal("the firmware in the ready folder is not the one sent")
	}
	if a := h.req(TypePutBegin, PutBegin{Name: FirmwareName, Size: pin.Size}); a["accept"] != false {
		t.Fatalf("PUT after the commit: %v", a)
	}
	if d := h.req(TypeDrive, struct{}{}); d["ok"] != false {
		t.Fatalf("DRIVE after the commit: %v", d)
	}
	h.l.disable()
	wait()
	// another pin list does not take the folder
	if _, err := handover.CheckReadyStep1FW(g.dir, -1, []FWPin{{Size: pin.Size, SHA256: strings.Repeat("a", 64)}}); err == nil {
		t.Fatal("CheckReadyStep1FW took a firmware that no pin names")
	}
	if sshkey.MaxBytes != handover.MaxSSHKey || sshkey.FileName != SSHKeyName {
		t.Fatal("handover's key limits are not package sshkey's")
	}
}

func TestStep1FirmwareOfficial(t *testing.T) {
	fw, pin := fwFixture(1<<20 + 7)
	g := step1Rig(t, Config{Stage: Step1Firmware, FWPins: []FWPin{{Size: 99, SHA256: strings.Repeat("b", 64)}, pin}})
	h, wait := g.session()
	if c := h.req(TypeCommit, Commit{Mode: handover.ModeOfficial}); c["ok"] != false || !strings.Contains(c["reason"].(string), "incomplete") {
		t.Fatalf("commit before the firmware %v", c)
	}
	h.put(FirmwareName, fw, int64(len(fw)), DataChunk)
	for _, cm := range []Commit{{}, {Mode: "android"}, {Mode: "official", Auto: true}} {
		if c := h.req(TypeCommit, cm); c["ok"] != false {
			t.Fatalf("commit %+v: %v", cm, c)
		}
	}
	if c := h.req(TypeCommit, Commit{Mode: handover.ModeOfficial}); c["ok"] != true {
		t.Fatalf("commit %v", c)
	}
	h.l.disable()
	wait()
	rd, err := handover.CheckReadyStep1FW(g.dir, -1, []FWPin{pin})
	if err != nil || rd.Record.Mode != "official" || len(rd.Record.Files) != 1 {
		t.Fatalf("%+v %v", rd, err)
	}
}

func TestStep1FirmwareRefusals(t *testing.T) {
	fw, pin := fwFixture(1 << 20)
	g := step1Rig(t, Config{Stage: Step1Firmware, FWPins: []FWPin{pin}})
	h, wait := g.session()
	refuse := func(name string, size int64, want string) {
		t.Helper()
		a := h.req(TypePutBegin, PutBegin{Name: name, Size: size})
		if a["accept"] != false || !strings.Contains(a["reason"].(string), want) {
			t.Fatalf("PUT %q %d: %v (want %q)", name, size, a, want)
		}
	}
	refuse(FirmwareName, pin.Size+1, "no firmware that this installer takes has this size")
	refuse(SSHKeyName, int64(len(testKey)), "firmware.bin first")
	for _, n := range []string{ManifestName, RootfsName, RootfsZstName, "x.bin", "../firmware.bin", ""} {
		refuse(n, 10, "not a file of the step1")
	}
	evil := append([]byte(nil), fw...)
	evil[100] ^= 1
	if _, e := h.put(FirmwareName, evil, int64(len(evil)), DataChunk); e["ok"] != false || !strings.Contains(e["reason"].(string), "is not the firmware") {
		t.Fatalf("wrong bytes: %v", e)
	}
	if es, _ := os.ReadDir(filepath.Join(g.dir, incomingStem+"1")); len(es) != 0 {
		t.Fatalf("a refused firmware is left: %v", es)
	}
	h.put(FirmwareName, fw, int64(len(fw)), DataChunk)
	refuse(FirmwareName, pin.Size, "already received")
	refuse(SSHKeyName, handover.MaxSSHKey+1, "1 to")
	bad := "ssh-dss AAAAB3NzaC1kc3MAAACBAP test\n"
	if _, e := h.put(SSHKeyName, []byte(bad), int64(len(bad)), DataChunk); e["ok"] != false || !strings.Contains(e["reason"].(string), SSHKeyName) {
		t.Fatalf("bad key: %v", e)
	}
	h.put(SSHKeyName, []byte(testKey), int64(len(testKey)), DataChunk)
	refuse(SSHKeyName, int64(len(testKey)), "already received")
	if c := h.req(TypeCommit, Commit{Mode: handover.ModeOfficial}); c["ok"] != false || !strings.Contains(c["reason"].(string), "only for a mainline") {
		t.Fatalf("official with a key: %v", c)
	}
	// a cut: nothing is left
	h.l.disable()
	wait()
	if e := g.entries(); len(e) != 1 || e[0] != StateFile {
		t.Fatalf("after the cut: %v", e)
	}
	if st := g.state(); st.State != StateWaitingFirmware {
		t.Fatalf("state after the cut %+v", st)
	}
}

func TestStep1CutInsideFirmware(t *testing.T) {
	fw, pin := fwFixture(4 << 20)
	g := step1Rig(t, Config{Stage: Step1Firmware, FWPins: []FWPin{pin}})
	h, wait := g.session()
	if a := h.req(TypePutBegin, PutBegin{Name: FirmwareName, Size: pin.Size}); a["accept"] != true {
		t.Fatal(a)
	}
	h.l.hostOut(fw[:DataChunk])
	h.l.disable()
	wait()
	if e := g.entries(); len(e) != 1 || e[0] != StateFile {
		t.Fatalf("after the cut: %v", e)
	}
}

func TestStep1Drive(t *testing.T) {
	_, pin := fwFixture(1 << 20)
	var asked atomic.Int32
	g := step1Rig(t, Config{Stage: Step1Firmware, FWPins: []FWPin{pin}, Drive: func() { asked.Add(1) }})
	h, wait := g.session()
	if d := h.req(TypeDrive, struct{}{}); d["ok"] != true {
		t.Fatalf("DRIVE %v", d)
	}
	h.l.disable()
	wait()
	if asked.Load() != 1 {
		t.Fatalf("the root side was asked %d times", asked.Load())
	}
	// status only: no drive, no file
	g2 := step1Rig(t, Config{StatusOnly: true, StatusState: StateBusy})
	h2, wait2 := g2.session()
	if hl := h2.req(TypeHello, struct{}{}); hl["mode"] != "step1" || hl["state"] != StateBusy || hl["status_only"] != true {
		t.Fatalf("hello %v", hl)
	}
	if d := h2.req(TypeDrive, struct{}{}); d["ok"] != false {
		t.Fatalf("DRIVE when busy %v", d)
	}
	if a := h2.req(TypePutBegin, PutBegin{Name: FirmwareName, Size: pin.Size}); a["accept"] != false {
		t.Fatalf("PUT when busy %v", a)
	}
	h2.l.disable()
	wait2()
}

func TestStep1BundleCompressed(t *testing.T) {
	z := newZstFixture(t, nil)
	g := step1Rig(t, Config{Stage: Step1Bundle, Pins: []string{z.pin}})
	h, wait := g.session()
	if hl := h.req(TypeHello, struct{}{}); hl["state"] != StateWaitingBundle || hl["stage"] != Step1Bundle || hl["fw_pins"] != nil {
		t.Fatalf("hello %v", hl)
	}
	_, e := h.put(ManifestName, z.manifest, int64(len(z.manifest)), DataChunk)
	if e["ok"] != true {
		t.Fatal(e)
	}
	next := e["next"].([]any)
	if len(next) != 3 || next[2].(map[string]any)["name"] != RootfsZstName {
		t.Fatalf("next %v", next)
	}
	h.put(TemplateName, z.template, int64(len(z.template)), DataChunk)
	// the RAM rule counts the unpacked size
	g.mu.Lock()
	g.mem = DefaultReserve + int64(len(z.rootfs)) - 1
	g.mu.Unlock()
	if a := h.req(TypePutBegin, PutBegin{Name: RootfsZstName, Size: int64(len(z.packed))}); a["accept"] != false ||
		!strings.Contains(a["reason"].(string), fmt.Sprint(len(z.rootfs))) {
		t.Fatalf("too little RAM for the unpacked size: %v", a)
	}
	g.mu.Lock()
	g.mem = 4 << 30
	g.mu.Unlock()
	b, e := h.put(RootfsZstName, z.packed, int64(len(z.packed)), 64<<10)
	if b["accept"] != true || e["ok"] != true || e["sha256"] != sum(z.packed) || e["unpacked_sha256"] != sum(z.rootfs) ||
		e["unpacked"] != float64(len(z.rootfs)) {
		t.Fatalf("rootfs.ext4.zst %v %v", b, e)
	}
	if a := h.req(TypePutBegin, PutBegin{Name: RootfsName, Size: int64(len(z.rootfs))}); a["accept"] != false {
		t.Fatalf("rootfs.ext4 after the packed one: %v", a)
	}
	if c := h.req(TypeCommit, Commit{Mode: "mainline"}); c["ok"] != false {
		t.Fatalf("a mode on the bundle commit: %v", c)
	}
	if c := h.req(TypeCommit, Commit{}); c["ok"] != true {
		t.Fatalf("commit %v", c)
	}
	h.l.disable()
	wait()
	rd, err := CheckReady(g.dir, -1, []string{z.pin})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(rd.Folder, RootfsName))
	if !bytes.Equal(got, z.rootfs) {
		t.Fatal("the unpacked rootfs is not the plain text")
	}
	ents, _ := os.ReadDir(rd.Folder)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "boot-template.img,manifest.json,rootfs.ext4" {
		t.Fatalf("ready folder %v (the packed file is never kept)", names)
	}
}

func TestStep1BundleUncompressedOfSchema3(t *testing.T) {
	z := newZstFixture(t, nil)
	g := step1Rig(t, Config{Stage: Step1Bundle, Pins: []string{z.pin}})
	h, wait := g.session()
	h.put(ManifestName, z.manifest, int64(len(z.manifest)), DataChunk)
	h.put(TemplateName, z.template, int64(len(z.template)), DataChunk)
	if _, e := h.put(RootfsName, z.rootfs, int64(len(z.rootfs)), DataChunk); e["ok"] != true {
		t.Fatal(e)
	}
	if a := h.req(TypePutBegin, PutBegin{Name: RootfsZstName, Size: int64(len(z.packed))}); a["accept"] != false {
		t.Fatalf("packed after the plain one: %v", a)
	}
	if c := h.req(TypeCommit, Commit{}); c["ok"] != true {
		t.Fatal(c)
	}
	h.l.disable()
	wait()
	if _, err := CheckReady(g.dir, -1, []string{z.pin}); err != nil {
		t.Fatal(err)
	}
}

func TestStep1BundleCompressedRefusals(t *testing.T) {
	plain, packed := zstText(t)
	corrupt := append([]byte(nil), packed...)
	corrupt[len(corrupt)/2] ^= 0x55
	cases := []struct {
		name string
		edit func(m map[string]any)
		send []byte
		want string
	}{
		{"corrupt stream (pinned)", func(m map[string]any) {
			m["rootfs_zst"].(map[string]any)["sha256"] = sum(corrupt)
		}, corrupt, "unpack"},
		{"corrupt stream (not pinned)", nil, corrupt, "is not the one"},
		{"unpacks to more than rootfs.size", func(m map[string]any) {
			m["rootfs"].(map[string]any)["size"] = len(plain) - 512
		}, packed, "more bytes than"},
		{"unpacks to less than rootfs.size", func(m map[string]any) {
			m["rootfs"].(map[string]any)["size"] = len(plain) + 512
		}, packed, "the manifest's rootfs has"},
		{"unpacked bytes are not rootfs.sha256", func(m map[string]any) {
			m["rootfs"].(map[string]any)["sha256"] = strings.Repeat("c", 64)
		}, packed, "is not the one"},
		{"truncated stream (pinned)", func(m map[string]any) {
			m["rootfs_zst"].(map[string]any)["size"] = len(packed) - 1000
			m["rootfs_zst"].(map[string]any)["sha256"] = sum(packed[:len(packed)-1000])
		}, packed[:len(packed)-1000], "unpack"},
		{"trailing garbage (pinned)", func(m map[string]any) {
			bad := append(append([]byte(nil), packed...), []byte("garbage after the frame")...)
			m["rootfs_zst"].(map[string]any)["size"] = len(bad)
			m["rootfs_zst"].(map[string]any)["sha256"] = sum(bad)
		}, append(append([]byte(nil), packed...), []byte("garbage after the frame")...), "unpack"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			z := newZstFixture(t, c.edit)
			g := step1Rig(t, Config{Stage: Step1Bundle, Pins: []string{z.pin}})
			h, wait := g.session()
			h.put(ManifestName, z.manifest, int64(len(z.manifest)), DataChunk)
			b, e := h.put(RootfsZstName, c.send, int64(len(c.send)), DataChunk)
			if b["accept"] != true || e["ok"] != false || !strings.Contains(e["reason"].(string), c.want) {
				t.Fatalf("%v %v (want %q)", b, e, c.want)
			}
			if _, err := os.Lstat(filepath.Join(g.dir, incomingStem+"1", RootfsName)); err == nil {
				t.Fatal("a refused rootfs is left in the folder")
			}
			// the link is in step: the template is taken after it
			if _, e := h.put(TemplateName, z.template, int64(len(z.template)), DataChunk); e["ok"] != true {
				t.Fatalf("after the refusal: %v", e)
			}
			h.l.disable()
			wait()
		})
	}
	// schema 2: no packed file
	f := newFixture(1 << 20)
	g := step1Rig(t, Config{Stage: Step1Bundle, Pins: []string{f.pin}})
	h, wait := g.session()
	h.put(ManifestName, f.manifest, int64(len(f.manifest)), DataChunk)
	if a := h.req(TypePutBegin, PutBegin{Name: RootfsZstName, Size: 10}); a["accept"] != false || !strings.Contains(a["reason"].(string), "schema 2") {
		t.Fatalf("packed file of a schema-2 manifest: %v", a)
	}
	z := newZstFixture(t, nil)
	h.l.disable()
	wait()
	g = step1Rig(t, Config{Stage: Step1Bundle, Pins: []string{z.pin}})
	h, wait = g.session()
	if a := h.req(TypePutBegin, PutBegin{Name: RootfsZstName, Size: int64(len(z.packed))}); a["accept"] != false || !strings.Contains(a["reason"].(string), "manifest.json first") {
		t.Fatalf("packed before the manifest: %v", a)
	}
	h.put(ManifestName, z.manifest, int64(len(z.manifest)), DataChunk)
	if a := h.req(TypePutBegin, PutBegin{Name: RootfsZstName, Size: int64(len(z.packed)) + 1}); a["accept"] != false || !strings.Contains(a["reason"].(string), "says") {
		t.Fatalf("packed of another size: %v", a)
	}
	h.l.disable()
	wait()
}

// TestStep1CompressedCutAndIdle: a cut or an idle end inside the packed
// file leaves nothing, and the decoder ends with the session.
func TestStep1CompressedCutAndIdle(t *testing.T) {
	z := newZstFixture(t, nil)
	for _, idle := range []bool{false, true} {
		g := step1Rig(t, Config{Stage: Step1Bundle, Pins: []string{z.pin}, Idle: time.Second})
		h, wait := g.session()
		h.put(ManifestName, z.manifest, int64(len(z.manifest)), DataChunk)
		if a := h.req(TypePutBegin, PutBegin{Name: RootfsZstName, Size: int64(len(z.packed))}); a["accept"] != true {
			t.Fatal(a)
		}
		h.l.hostOut(z.packed[:100<<10])
		if idle {
			time.Sleep(50 * time.Millisecond)
			g.advance(2 * time.Second)
			g.r.idleCheck()
			if g.reconnects() != 1 {
				t.Fatalf("idle: %d reconnects", g.reconnects())
			}
		}
		h.l.disable()
		wait()
		if e := g.entries(); len(e) != 1 || e[0] != StateFile {
			t.Fatalf("idle %v: %v", idle, e)
		}
	}
}

// TestPayloadKindUnchangedByStep1: the step-2 receiver (kind payload) takes
// no packed file, no DRIVE frame and no COMMIT mode (the coordinator: the
// step-2 side is held for R23).
func TestPayloadKindUnchangedByStep1(t *testing.T) {
	z := newZstFixture(t, nil)
	g := newRig(t, Config{Pins: []string{z.pin}})
	h, wait := g.session()
	h.put(ManifestName, z.manifest, int64(len(z.manifest)), DataChunk)
	if a := h.req(TypePutBegin, PutBegin{Name: RootfsZstName, Size: int64(len(z.packed))}); a["accept"] != false || !strings.Contains(a["reason"].(string), "not a file of the payload") {
		t.Fatalf("packed file on step 2: %v", a)
	}
	h.l.hostOut(h.frame(TypeCommit, map[string]any{"auto": false, "mode": "mainline"}))
	wait()
	if !h.l.halted {
		t.Fatal("a COMMIT with a mode was not a violation on step 2")
	}
	for _, c := range []Config{{Stage: Step1Firmware}, {FWPins: []FWPin{{Size: 1, SHA256: strings.Repeat("a", 64)}}}, {StatusOnly: true, StatusState: StateBusy}} {
		c.Dir = t.TempDir()
		if _, err := NewReceiver(c); err == nil {
			t.Fatalf("a payload receiver took %+v", c)
		}
	}
}

func TestStep1Config(t *testing.T) {
	_, pin := fwFixture(100)
	for _, c := range []Config{
		{Kind: KindStep1},
		{Kind: KindStep1, Stage: "both"},
		{Kind: KindStep1, Stage: Step1Firmware, FWPins: []FWPin{{Size: 0, SHA256: pin.SHA256}}},
		{Kind: KindStep1, Stage: Step1Firmware, FWPins: []FWPin{{Size: 1, SHA256: "ABC"}}},
		{Kind: KindStep1, Stage: Step1Firmware, FWPins: []FWPin{pin}, Pins: []string{pin.SHA256}},
		{Kind: KindStep1, Stage: Step1Bundle, FWPins: []FWPin{pin}},
		{Kind: KindStep1, StatusOnly: true, StatusState: "idle"},
	} {
		if !c.StatusOnly {
			c.Dir = t.TempDir()
		}
		if _, err := NewReceiver(c); err == nil {
			t.Errorf("taken: %+v", c)
		}
	}
	g := step1Rig(t, Config{Stage: Step1Firmware})
	h, wait := g.session()
	if hl := h.req(TypeHello, struct{}{}); !strings.Contains(hl["detail"].(string), "no step1 is pinned") {
		t.Fatalf("hello without firmware pins %v", hl)
	}
	h.l.disable()
	wait()
}

func TestInfoPhaseUnlock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "info.json")
	for _, c := range []struct{ phase, unlock, wantPhase, wantUnlock string }{
		{"plan: hold", "locked", "plan: hold", "locked"},
		{"install", "could not read", "install", "could not read"},
		{"Plan", "0x01", "", ""},
		{"x\nhidden", "unlocked\n", "", ""},
	} {
		b, _ := json.Marshal(map[string]any{"phase": c.phase, "unlock": c.unlock, "serial": "BBE4ZZDT24"})
		os.WriteFile(p, b, 0o644)
		in := ReadInfo(p)
		if in.Phase != c.wantPhase || in.Unlock != c.wantUnlock {
			t.Errorf("%q %q: %+v", c.phase, c.unlock, in)
		}
		if j, _ := json.Marshal(in); strings.Contains(string(j), "BBE4") {
			t.Errorf("the info carries a serial: %s", j)
		}
	}
}
