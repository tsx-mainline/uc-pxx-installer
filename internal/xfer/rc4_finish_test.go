package xfer

// The finish set of the permanent USB interface (mainline-rc-4, owner Q-T8 =
// option 5): the receiver takes the slot's own finish list first (its one
// pin), then exactly the two files it names; the root check and the seal.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uc-pxx-installer/internal/xfer/handover"
)

type finishFixture struct {
	list, bin, bootctl []byte
	pin                string
	l                  *handover.FinishList
}

func newFinishFixture(t *testing.T) finishFixture {
	bin := bytes.Repeat([]byte("finish-executor "), 70000) // > 1 MiB: two data transfers
	bc := bytes.Repeat([]byte("r2 "), 5000)
	l := handover.FinishList{Kind: handover.FinishKind, Version: handover.FinishVersion, Files: []handover.ReadyFile{
		{Name: handover.FinishBin, Size: int64(len(bin)), SHA256: sum(bin)},
		{Name: handover.FinishBootctl, Size: int64(len(bc)), SHA256: sum(bc)}}}
	b, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	pl, err := handover.ParseFinishList(b)
	if err != nil {
		t.Fatal(err)
	}
	return finishFixture{list: b, bin: bin, bootctl: bc, pin: sum(b), l: pl}
}

func TestFinishSetHappyPathAndSeal(t *testing.T) {
	f := newFinishFixture(t)
	g := newRig(t, Config{Kind: KindFinish, Pins: []string{f.pin}})
	h, wait := g.session()
	hello := h.req(TypeHello, struct{}{})
	if hello["mode"] != KindFinish || hello["state"] != StateWaitingFinish {
		t.Fatalf("hello %v", hello)
	}
	// a payload file is not a file of the finish set
	b, _ := h.put(ManifestName, []byte("{}"), 2, DataChunk)
	if b["accept"] != false || !strings.Contains(b["reason"].(string), "not a file of the finish") {
		t.Fatalf("a manifest in the finish mode: %v", b)
	}
	// the files before the list
	if b, _ := h.put(handover.FinishBin, f.bin, int64(len(f.bin)), DataChunk); b["accept"] != false ||
		!strings.Contains(b["reason"].(string), "finish.json first") {
		t.Fatalf("the executor before the list: %v", b)
	}
	for _, x := range []struct {
		n string
		d []byte
	}{{handover.FinishListName, f.list}, {handover.FinishBin, f.bin}, {handover.FinishBootctl, f.bootctl}} {
		b, e := h.put(x.n, x.d, int64(len(x.d)), DataChunk)
		if b["accept"] != true || e["ok"] != true || e["sha256"] != sum(x.d) {
			t.Fatalf("%s: %v %v", x.n, b, e)
		}
	}
	// a finish set has no automatic start
	if c := h.req(TypeCommit, Commit{Auto: true}); c["ok"] != false || !strings.Contains(c["reason"].(string), "no automatic start") {
		t.Fatalf("commit auto %v", c)
	}
	if c := h.req(TypeCommit, Commit{}); c["ok"] != true {
		t.Fatalf("commit %v", c)
	}
	if st := g.state(); st.State != StateFinishComplete || st.ManifestSHA != f.pin {
		t.Fatalf("state %+v", st)
	}
	h.l.disable()
	wait()
	// the payload check never takes a finish record, and the finish check
	// never another list
	if _, err := CheckReady(g.dir, -1, []string{f.pin}); err == nil {
		t.Fatal("the payload check took a finish set")
	}
	if _, _, err := handover.CheckReadyFinish(g.dir, -1, strings.Repeat("0", 64)); err == nil {
		t.Fatal("another list was taken")
	}
	rd, l, err := handover.CheckReadyFinish(g.dir, -1, f.pin)
	if err != nil {
		t.Fatal(err)
	}
	if rd.Record.Kind != handover.RecordKindFinish || l.Files[0] != f.l.Files[0] || l.Files[1] != f.l.Files[1] {
		t.Fatalf("record %+v list %+v", rd.Record, l)
	}
	dst := filepath.Join(t.TempDir(), "run-finish")
	if err := handover.SealFinish(rd.Folder, f.l, dst); err != nil {
		t.Fatal(err)
	}
	for _, x := range []struct {
		n string
		d []byte
	}{{handover.FinishBin, f.bin}, {handover.FinishBootctl, f.bootctl}} {
		p := filepath.Join(dst, x.n)
		b, err := os.ReadFile(p)
		fi, _ := os.Lstat(p)
		if err != nil || !bytes.Equal(b, x.d) || fi.Mode().Perm() != 0o700 {
			t.Fatalf("sealed %s: %v mode %v", x.n, err, fi.Mode())
		}
	}
	if fi, _ := os.Lstat(dst); fi.Mode().Perm() != 0o700 {
		t.Fatalf("the sealed folder is %v", fi.Mode())
	}
	ents, _ := os.ReadDir(dst)
	if len(ents) != 2 {
		t.Fatalf("the sealed folder holds %d entries", len(ents))
	}
	// a second seal into the same place is refused (once per boot)
	if err := handover.SealFinish(rd.Folder, f.l, dst); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Fatalf("a second seal: %v", err)
	}
}

func TestFinishSetRefusals(t *testing.T) {
	f := newFinishFixture(t)
	// the unit's own list is the one pin: another list (another executor) is
	// refused before any file byte
	other := newFinishFixture(t)
	other.l.Files[0].SHA256 = strings.Repeat("9", 64)
	ob, _ := json.Marshal(other.l)
	g := newRig(t, Config{Kind: KindFinish, Pins: []string{f.pin}})
	h, wait := g.session()
	_, e := h.put(handover.FinishListName, ob, int64(len(ob)), DataChunk)
	if e["ok"] != false || !strings.Contains(e["reason"].(string), "not this slot's own finish list") {
		t.Fatalf("another list: %v", e)
	}
	if b, _ := h.put(handover.FinishBin, f.bin, int64(len(f.bin)), DataChunk); b["accept"] != false {
		t.Fatalf("the executor after a refused list: %v", b)
	}
	// the own list; then a changed executor of the right size
	if _, e := h.put(handover.FinishListName, f.list, int64(len(f.list)), DataChunk); e["ok"] != true {
		t.Fatalf("list %v", e)
	}
	evil := append([]byte(nil), f.bin...)
	evil[1000] ^= 1
	if b, e := h.put(handover.FinishBin, evil, int64(len(evil)), DataChunk); b["accept"] != true || e["ok"] != false ||
		!strings.Contains(e["reason"].(string), "is not the one finish.json names") {
		t.Fatalf("a changed executor: %v %v", b, e)
	}
	if b, _ := h.put(handover.FinishBin, f.bin, int64(len(f.bin))+1, DataChunk); b["accept"] != false {
		t.Fatalf("a size that is not the list's: %v", b)
	}
	if c := h.req(TypeCommit, Commit{}); c["ok"] != false || !strings.Contains(c["reason"].(string), "incomplete") {
		t.Fatalf("commit without the files: %v", c)
	}
	h.l.disable()
	wait()
	// more than one pin: refused at the start
	if _, err := NewReceiver(Config{Kind: KindFinish, Dir: t.TempDir(), Pins: []string{f.pin, other.pin}}); err == nil {
		t.Fatal("a finish receiver with two pins")
	}
	// no pin (a slot without a list): every list refused
	g2 := newRig(t, Config{Kind: KindFinish})
	h2, wait2 := g2.session()
	if b, _ := h2.put(handover.FinishListName, f.list, int64(len(f.list)), DataChunk); b["accept"] != false ||
		!strings.Contains(b["reason"].(string), "names no finish executor") {
		t.Fatalf("no pin: %v", b)
	}
	h2.l.disable()
	wait2()
}

func TestParseFinishListStrict(t *testing.T) {
	f := newFinishFixture(t)
	good := string(f.list)
	for name, s := range map[string]string{
		"another kind":       strings.Replace(good, `"uc-pxx-finish"`, `"uc-pxx-mainline"`, 1),
		"version 2":          strings.Replace(good, `"version":1`, `"version":2`, 1),
		"an unknown key":     strings.Replace(good, `{"kind"`, `{"x":1,"kind"`, 1),
		"the files swapped":  strings.Replace(strings.Replace(good, handover.FinishBin, "@@", 1), handover.FinishBootctl, handover.FinishBin, 1),
		"a third file":       strings.Replace(good, `]}`, `,{"name":"x","size":1,"sha256":"`+strings.Repeat("a", 64)+`"}]}`, 1),
		"a size of 0":        strings.Replace(good, `"size":`+jsonInt(len(f.bootctl)), `"size":0`, 1),
		"data after":         good + "{}",
		"not a sha256":       strings.Replace(good, f.l.Files[1].SHA256, "xyz", 1),
		"a path in the name": strings.Replace(good, `"`+handover.FinishBootctl+`"`, `"../`+handover.FinishBootctl+`"`, 1),
	} {
		if _, err := handover.ParseFinishList([]byte(s)); err == nil {
			t.Errorf("%s: taken", name)
		}
	}
	if _, err := handover.ParseFinishList(f.list); err != nil {
		t.Fatal(err)
	}
}

func jsonInt(n int) string { b, _ := json.Marshal(n); return string(b) }

// TestSealFinishRefuses: the seal copies only regular one-link files of the
// list's size and sha256, never through a link; any difference leaves nothing.
func TestSealFinishRefuses(t *testing.T) {
	f := newFinishFixture(t)
	mk := func(t *testing.T, edit func(dir string)) string {
		d := filepath.Join(t.TempDir(), "complete")
		os.Mkdir(d, 0o700)
		os.WriteFile(filepath.Join(d, handover.FinishListName), f.list, 0o600)
		os.WriteFile(filepath.Join(d, handover.FinishBin), f.bin, 0o600)
		os.WriteFile(filepath.Join(d, handover.FinishBootctl), f.bootctl, 0o600)
		if edit != nil {
			edit(d)
		}
		return d
	}
	for name, edit := range map[string]func(string){
		"a changed byte": func(d string) {
			b := append([]byte(nil), f.bootctl...)
			b[3] ^= 1
			os.WriteFile(filepath.Join(d, handover.FinishBootctl), b, 0o600)
		},
		"a link to another file": func(d string) {
			o := filepath.Join(filepath.Dir(d), "other")
			os.WriteFile(o, f.bin, 0o600)
			os.Remove(filepath.Join(d, handover.FinishBin))
			os.Symlink(o, filepath.Join(d, handover.FinishBin))
		},
		"a hard link": func(d string) {
			os.Link(filepath.Join(d, handover.FinishBin), filepath.Join(filepath.Dir(d), "hl"))
		},
		"a shorter file": func(d string) {
			os.WriteFile(filepath.Join(d, handover.FinishBin), f.bin[:len(f.bin)-1], 0o600)
		},
	} {
		t.Run(name, func(t *testing.T) {
			src := mk(t, edit)
			dst := filepath.Join(t.TempDir(), "run-finish")
			if err := handover.SealFinish(src, f.l, dst); err == nil {
				t.Fatal("sealed")
			}
			if _, err := os.Lstat(dst); !os.IsNotExist(err) {
				t.Fatalf("the refused seal left %s (%v)", dst, err)
			}
		})
	}
	src := mk(t, nil)
	dst := filepath.Join(t.TempDir(), "run-finish")
	if err := handover.SealFinish(src, f.l, dst); err != nil {
		t.Fatal(err)
	}
}

// TestDecideModeFinish: a normal build with its own root-owned finish list
// and no marker is a finish receiver with that list's sha256 as the one pin;
// a list that others can write, or a link, is refused (status only).
func TestDecideModeFinish(t *testing.T) {
	if os.Geteuid() != 0 {
		// ReadFinishList requires a list of root (the test runs as a user
		// in the lint container): check the refusal, the root case in the guest
		root := t.TempDir()
		f := newFinishFixture(t)
		p := filepath.Join(root, handover.FinishListPath)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, f.list, 0o644)
		if m, why, _, _ := DecideMode(root); m != ModeStatus || !strings.Contains(why, "not a regular file of root") {
			t.Fatalf("a list of a user: %s %s", m, why)
		}
		return
	}
	root := t.TempDir()
	f := newFinishFixture(t)
	p := filepath.Join(root, handover.FinishListPath)
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, f.list, 0o644)
	m, _, l, pin := DecideMode(root)
	if m != ModeFinish || pin != f.pin || l == nil || l.Files[1].SHA256 != f.l.Files[1].SHA256 {
		t.Fatalf("mode %s pin %s", m, pin)
	}
	os.Chmod(p, 0o666)
	if m, _, _, _ := DecideMode(root); m != ModeStatus {
		t.Fatal("a list that others can write was taken")
	}
}

func TestBaseInfoNoIdentity(t *testing.T) {
	root := t.TempDir()
	w := func(p, s string) {
		os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte(s), 0o644)
	}
	w("/proc/device-tree/compatible", "crestron,uc-p10\x00rockchip,rk3399\x00")
	w("/proc/device-tree/chosen/u-boot,bootargs", "storagemedia=emmc androidboot.serialno=BBE4ZZDT24 androidboot.slot_suffix=_b console=ttyFIQ0\x00")
	w("/proc/cmdline", "console=ttyS2 uc-pxx.id=pxx-inst-abc123 ethaddr=02:11:22:33:44:55\n")
	in := handover.BaseInfo(root, "idle", []string{"x"})
	if in.Model != "UC-P10" || in.Slot != "b" || in.Token != "pxx-inst-abc123" {
		t.Fatalf("%+v", in)
	}
	b, _ := json.Marshal(in)
	if strings.Contains(string(b), "BBE4ZZDT24") || strings.Contains(string(b), "02:11:22") {
		t.Fatalf("the info holds identifying data: %s", b)
	}
	p := filepath.Join(root, "run/info.json")
	if err := handover.WriteInfo(p, in); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o644 {
		t.Fatalf("info mode %v", fi.Mode())
	}
	if got := ReadInfo(p); got == nil || got.Slot != "b" || got.Step2 != "idle" {
		t.Fatalf("read back %+v", got)
	}
}

func TestProcessesOf(t *testing.T) {
	proc := t.TempDir()
	w := func(pid, uid string) {
		os.MkdirAll(filepath.Join(proc, pid), 0o755)
		os.WriteFile(filepath.Join(proc, pid, "status"), []byte("Name:\tx\nUid:\t"+uid+"\nGid:\t0\t0\t0\t0\n"), 0o644)
	}
	w("10", "0\t0\t0\t0")
	w("11", "990\t990\t990\t990")
	w("12", "1000\t990\t1000\t1000") // the effective uid
	w("self", "990\t990\t990\t990")  // not a pid
	l, err := handover.ProcessesOf(proc, 990)
	if err != nil || len(l) != 2 || l[0]+l[1] != 23 {
		t.Fatalf("%v %v", l, err)
	}
}
