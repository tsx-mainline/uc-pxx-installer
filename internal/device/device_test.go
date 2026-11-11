//go:build install && devtest

package device_test

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/device/devicetest"
	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/nvram"
	"uc-pxx-installer/internal/plan"
)

var small = fakeemmc.ParseSmallLayout(fakeemmc.SmallLayout)

func TestLayoutIsTheUnitLayout(t *testing.T) {
	a, err := os.ReadFile("ucp10-layout.txt")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("../gpt/testdata/ucp10-layout.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("internal/device/ucp10-layout.txt differs from internal/gpt/testdata/ucp10-layout.txt")
	}
	l := device.Layout()
	if l.DiskSectors != 30535680 || len(l.Entries) != 28 {
		t.Fatalf("layout: %d sectors, %d entries", l.DiskSectors, len(l.Entries))
	}
	// The Android nvram driver reads absolute sectors: the device mode reads
	// the store in others only because the layout check pins others where
	// the driver reads it.
	n := 0
	for _, e := range l.Entries {
		if e.Name == nvram.PartName {
			n++
			if err := nvram.CheckPartition(e.Start, e.Size); err != nil {
				t.Fatal(err)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d partitions %s in the layout", n, nvram.PartName)
	}
}

func TestBootctlForms(t *testing.T) {
	for _, c := range []struct {
		args   string
		ok, wr bool
	}{
		{"status", true, false},
		{"disable --slot a", true, true},
		{"disable --slot b", true, true},
		{"arm-trying --slot a --lower-keeper", true, true},
		{"arm-trying --slot b --lower-keeper", true, true},
		{"arm-trying --slot b", false, false},
		{"mark-successful --expect a --token x", false, false},
		{"status --file /tmp/x", false, false},
		{"disable --slot b --dry-run", false, false},
		{"disable --slot c", false, false},
		{"arm-trying --lower-keeper --slot b", false, false},
		{"", false, false},
	} {
		ok, wr := device.BootctlFormForTest(strings.Fields(c.args))
		if ok != c.ok || wr != c.wr {
			t.Errorf("%q: ok %v writes %v", c.args, ok, wr)
		}
	}
}

type fakeUnit struct {
	root *devicetest.Root
	env  *device.Env
	d    *device.Disk
	img  []byte
}

func newFake(t *testing.T, bin []byte, sha string, r device.BootctlRunner) *fakeUnit {
	dir := t.TempDir()
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
	src, err := fx.Image(dir, "src.img", fakeemmc.Unit{AB: [2][3]byte{{15, 0, 1}, {14, 0, 1}}, A: "old", B: "old"})
	if err != nil {
		t.Fatal(err)
	}
	img, _ := os.ReadFile(src)
	root, err := devicetest.New(filepath.Join(dir, "root"), src, small, bin, devicetest.Opts{})
	if err != nil {
		t.Fatal(err)
	}
	e, err := device.NewForTest(device.TestOptions{Root: root.Dir, RegularFiles: true, Layout: small, BootctlSHA256: sha, Runner: r})
	if err != nil {
		t.Fatal(err)
	}
	d, err := e.OpenEMMC(device.EMMCPath)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeUnit{root: root, env: e, d: d, img: img}
}

// The view reads every range as the disk holds it, across partition
// boundaries, through the partition files and the disk file.
func TestViewReads(t *testing.T) {
	u := newFake(t, []byte("x"), "", nil)
	v := u.d.View()
	defer v.Close()
	rnd := rand.New(rand.NewSource(1))
	for i := 0; i < 400; i++ {
		off := rnd.Int63n(int64(len(u.img)))
		n := rnd.Intn(300000) + 1
		if off+int64(n) > int64(len(u.img)) {
			n = int(int64(len(u.img)) - off)
		}
		b := make([]byte, n)
		if _, err := v.ReadAt(b, off); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(b, u.img[off:off+int64(n)]) {
			t.Fatalf("read %d+%d differs", off, n)
		}
	}
	ab, err := u.d.ReadAB()
	if err != nil || !bytes.Equal(ab, u.img[plan.ABLBA*512:(plan.ABLBA+1)*512]) {
		t.Fatalf("ReadAB: %v", err)
	}
}

// The write layer checks the range rule itself, whatever the caller does.
func TestWriteLayerRefuses(t *testing.T) {
	u := newFake(t, []byte("x"), "", nil)
	step := func(name string, off, n int64) *plan.WriteStep {
		e, _ := u.d.Table.Find(name)
		return &plan.WriteStep{Partition: name, GPTIndex: e.Index, StartLBA: e.Start, PartSectors: e.Sectors(), Offset: off, Length: n}
	}
	if _, err := device.OpenWriteForTest(u.d, "b", step("boot_b", 0, 4096), 4096); err == nil || !strings.Contains(err.Error(), "A/B lock") {
		t.Fatalf("without the lock: %v", err)
	}
	if err := u.d.Lock(); err != nil {
		t.Fatal(err)
	}
	defer u.d.Unlock()
	for _, c := range []struct {
		target string
		w      *plan.WriteStep
		want   string
	}{
		{"b", step("boot_a", 0, 4096), "not a partition of slot b"},
		{"b", step("misc", 0, 512), "not a partition of slot b"},
		{"b", step("userdata", 0, 512), "not a partition of slot b"},
		{"b", step("cfg", 0, 512), "not a partition of slot b"},
		{"b", step("boot_b", 0, 512*513), "larger than the partition"},
		{"b", step("boot_b", 100, 512), "not whole sectors"},
		{"b", &plan.WriteStep{Partition: "boot_b", GPTIndex: 13, StartLBA: 58496, PartSectors: 512, Length: 512}, "the plan says p13"},
	} {
		if _, err := device.OpenWriteForTest(u.d, c.target, c.w, 4096); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.w.Partition, err, c.want)
		}
	}
	// a write that runs past the planned range is refused at the write
	w, err := device.OpenWriteForTest(u.d, "b", step("dtbo_b", 0, 4096), 4096)
	if err != nil {
		t.Fatal(err)
	}
	big := bytes.Repeat([]byte{0xa5}, 256*512+4096)
	_, werr := w.Write(big)
	w.Close()
	if werr == nil || !strings.Contains(werr.Error(), "larger than the partition") {
		t.Fatalf("past the end: %v", werr)
	}
	after, _ := u.root.Image()
	e, _ := u.d.Table.Find("dtbo_b")
	if !bytes.Equal(after[:e.Start*512], u.img[:e.Start*512]) || !bytes.Equal(after[(e.Start+e.Sectors())*512:], u.img[(e.Start+e.Sectors())*512:]) {
		t.Fatal("a byte outside dtbo_b changed")
	}
}

type countRunner struct{ calls []string }

func (c *countRunner) Run(e *device.Env, bin []byte, args []string) (*device.BootctlResult, error) {
	c.calls = append(c.calls, strings.Join(args, " "))
	return &device.BootctlResult{}, nil
}

// uc-pxx-bootctl-inst runs only with its sha256, only in the three forms, and
// a write form only with the A/B lock.
func TestBootctlGuards(t *testing.T) {
	bin := []byte("#!/bin/sh\necho \"uc-pxx-bootctl: args $*\" >&2\nexit 0\n")
	sha := devicetest.SHA256(bin)
	cr := &countRunner{}
	u := newFake(t, bin, sha, cr)
	if _, err := u.d.Bootctl([]string{"mark-successful", "--expect", "a"}); err == nil {
		t.Fatal("a fourth form ran")
	}
	if _, err := u.d.Bootctl([]string{"disable", "--slot", "b"}); err == nil || !strings.Contains(err.Error(), "A/B lock") {
		t.Fatalf("a write form without the lock: %v", err)
	}
	if _, err := u.d.Bootctl([]string{"status"}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(u.root.Path(device.BootctlPath), append(bin, '#'), 0o755)
	if _, err := u.d.Bootctl([]string{"status"}); err == nil || !strings.Contains(err.Error(), "not the reviewed binary") {
		t.Fatalf("a changed binary: %v", err)
	}
	os.Remove(u.root.Path(device.BootctlPath))
	os.Symlink("/bin/true", u.root.Path(device.BootctlPath))
	if _, err := u.d.Bootctl([]string{"status"}); err == nil {
		t.Fatal("a symlink ran")
	}
	if strings.Join(cr.calls, "|") != "status" {
		t.Fatalf("runner calls %q", cr.calls)
	}
}

// The default runner runs a private copy of the checked bytes, and removes it.
func TestExecRunner(t *testing.T) {
	bin := []byte("#!/bin/sh\necho \"uc-pxx-bootctl: current: A{prio 15 tries 0 succ 1} B{prio 14 tries 0 succ 1} crc 00000000\" >&2\necho \"self $0\"\necho libavb-would-boot: a\nexit 0\n")
	u := newFake(t, bin, devicetest.SHA256(bin), nil)
	r, err := u.d.Bootctl([]string{"status"})
	if err != nil || r.RC != 0 || !strings.Contains(r.Stdout, "libavb-would-boot: a") {
		t.Fatalf("%+v %v", r, err)
	}
	self := strings.TrimPrefix(strings.Fields(r.Stdout)[1], "")
	if !strings.HasPrefix(self, u.root.Path("/run/uc-pxx-inst/bootctl-")) || !strings.HasSuffix(self, "/uc-pxx-bootctl-inst") {
		t.Fatalf("ran %s", self)
	}
	if _, err := os.Stat(filepath.Dir(self)); !os.IsNotExist(err) {
		t.Fatal("the private copy was not removed")
	}
	// a non-zero exit is reported, not hidden
	bin3 := []byte("#!/bin/sh\nexit 3\n")
	u3 := newFake(t, bin3, devicetest.SHA256(bin3), nil)
	if r, err := u3.d.Bootctl([]string{"status"}); err != nil || r.RC != 3 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestOpenEMMCSetsNothingOnRegularFiles(t *testing.T) {
	u := newFake(t, []byte("x"), "", nil)
	after, err := u.root.Image()
	if err != nil || !bytes.Equal(after, u.img) {
		t.Fatalf("OpenEMMC changed the disk: %v", err)
	}
	if u.d.ID.Model != device.Model || u.d.ID.Host != device.EMMCHost || len(u.d.ID.CIDSHA256) != 64 {
		t.Fatalf("identity %+v", u.d.ID)
	}
	_ = gpt.Sector
}

// The kernel command line of the real RAM image is accepted: it has the
// image's own token uc-pxx.id=p10inst-XXXXXX (kernel/mkimage.sh), and
// CheckRAMImage returns that token. Every other uc-pxx.id= word, a second
// one, no token, or the token without uc-pxx-inst.ram=1 is not.
func TestCheckRAMImageCmdline(t *testing.T) {
	u := newFake(t, []byte("x"), "", nil)
	real := devicetest.RAMCmdline
	tok := "uc-pxx.id=pxx-inst-4f3f5e"
	if !strings.HasSuffix(real, " uc-pxx-inst.ram=1 "+tok) {
		t.Fatalf("the fixture is not the real command line: %q", real)
	}
	noTok := strings.TrimSuffix(real, " "+tok)
	for _, c := range []struct {
		name, cmdline, want string // want "" = accepted
	}{
		{"the real installer RAM image", real, ""},
		{"the real one, the token first", tok + " " + noTok, ""},
		{"another uc-pxx-inst token", noTok + " uc-pxx.id=pxx-inst-00a9ff", ""},
		// owner Q-R1 (2026-11-11): the old prefix of the released images is still accepted
		{"an old p10inst- token", noTok + " uc-pxx.id=p10inst-00a9ff", ""},
		{"an old p10inst- token first", "uc-pxx.id=p10inst-4f3f5e " + noTok, ""},
		{"a new and an old token", real + " uc-pxx.id=p10inst-4f3f5e", "second uc-pxx.id="},
		{"an old and a new token", noTok + " uc-pxx.id=p10inst-4f3f5e " + tok, "second uc-pxx.id="},
		{"new prefix, too short", noTok + " uc-pxx.id=pxx-inst-4f3f5", "booted system"},
		{"new prefix in uppercase hex", noTok + " uc-pxx.id=pxx-inst-4F3F5E", "booted system"},
		{"new prefix without the hex", noTok + " uc-pxx.id=pxx-inst-", "booted system"},
		{"a mixed prefix", noTok + " uc-pxx.id=pxx-p10inst-4f3f5e", "booted system"},
		{"the prefix of the rename", noTok + " uc-pxx.id=uc-pxx-inst-4f3f5e", "booted system"},
		{"the old prefix without the hyphen", noTok + " uc-pxx.id=p10inst4f3f5e", "booted system"},
		{"no token", noTok, "no token word"},
		{"no token, a mainline token removed", strings.Replace(real, " "+tok, "", 1), "no token word"},
		{"a mainline token", noTok + " uc-pxx.id=ucpxx-dfdcdfd8", "booted system"},
		{"a mainline token instead", strings.Replace(real, tok, "uc-pxx.id=ucpxx-003ba796", 1), "booted system"},
		{"token too short", noTok + " uc-pxx.id=p10inst-4f3f5", "booted system"},
		{"token too long", noTok + " uc-pxx.id=pxx-inst-4f3f5e0", "booted system"},
		{"token in uppercase", noTok + " uc-pxx.id=p10inst-4F3F5E", "booted system"},
		{"token with a suffix", noTok + " uc-pxx.id=pxx-inst-4f3f5e-x", "booted system"},
		{"token not hex", noTok + " uc-pxx.id=p10inst-4f3g5e", "booted system"},
		{"token without the hex", noTok + " uc-pxx.id=p10inst-", "booted system"},
		{"another prefix", noTok + " uc-pxx.id=P10inst-4f3f5e", "booted system"},
		{"an empty token", noTok + " uc-pxx.id=", "booted system"},
		{"two uc-pxx-inst tokens", real + " uc-pxx.id=p10inst-000000", "second uc-pxx.id="},
		{"the same token twice", real + " " + tok, "second uc-pxx.id="},
		{"a uc-pxx-inst and a mainline token", real + " uc-pxx.id=ucpxx-dfdcdfd8", "second uc-pxx.id="},
		{"a mainline and a uc-pxx-inst token", noTok + " uc-pxx.id=ucpxx-dfdcdfd8 " + tok, "booted system"},
		{"the token without uc-pxx-inst.ram=1", strings.Replace(real, " uc-pxx-inst.ram=1", "", 1), "no uc-pxx-inst.ram=1"},
		{"the token and uc-pxx-inst.ram=0", strings.Replace(real, "uc-pxx-inst.ram=1", "uc-pxx-inst.ram=0", 1), "no uc-pxx-inst.ram=1"},
		{"the token and androidboot.", real + " androidboot.slot_suffix=_a", "booted system"},
		{"the token and root=", real + " root=/dev/mmcblk0p22", "booted system"},
		{"the token and skip_initramfs", real + " skip_initramfs", "booted system"},
		{"the token and init=", real + " init=/sbin/init", "booted system"},
	} {
		if err := os.WriteFile(u.root.Path("/proc/cmdline"), []byte(c.cmdline+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := u.env.CheckRAMImage()
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: refused: %v", c.name, err)
		case c.want == "" && "uc-pxx.id="+got != tokenWord(c.cmdline):
			t.Errorf("%s: token %q, the line has %q", c.name, got, tokenWord(c.cmdline))
		case c.want != "" && (err == nil || !device.IsRefusal(err) || !strings.Contains(err.Error(), c.want) || got != ""):
			t.Errorf("%s: %q %v, want a refusal with %q", c.name, got, err, c.want)
		}
	}
	// the mounts that image/overlay/init and rcS make, with the real line
	os.WriteFile(u.root.Path("/proc/cmdline"), []byte(real+"\n"), 0o644)
	os.WriteFile(u.root.Path("/proc/mounts"), []byte("rootfs / rootfs rw,size=1951304k,nr_inodes=487826 0 0\n"+
		"proc /proc proc rw,relatime 0 0\nsysfs /sys sysfs rw,relatime 0 0\n"+
		"devtmpfs /dev devtmpfs rw,relatime,size=1951304k,nr_inodes=487826,mode=755 0 0\n"+
		"devpts /dev/pts devpts rw,relatime,mode=600,ptmxmode=000 0 0\n"+
		"run /run tmpfs rw,nosuid,nodev,relatime,mode=755 0 0\ntmp /tmp tmpfs rw,nosuid,nodev,relatime 0 0\n"+
		"configfs /sys/kernel/config configfs rw,relatime 0 0\n"+
		"drive /run/uc-pxx-inst/drive tmpfs rw,relatime,size=1236992k,mode=700 0 0\n"), 0o644)
	if got, err := u.env.CheckRAMImage(); err != nil || got != "pxx-inst-4f3f5e" {
		t.Errorf("the mounts of the RAM image: %q %v", got, err)
	}
}

// tokenWord is the one uc-pxx.id= word of a line ("" if none).
func tokenWord(cl string) string {
	for _, w := range strings.Fields(cl) {
		if strings.HasPrefix(w, "uc-pxx.id=") {
			return w
		}
	}
	return ""
}

// The bytes of the command line (review R3-D3): the kernel splits at more
// bytes than strings.Fields (0xA0) and joins quoted words, so only printable
// ASCII and tab pass, and no '"'. The one newline at the end is the kernel's.
func TestCheckRAMImageCmdlineBytes(t *testing.T) {
	u := newFake(t, []byte("x"), "", nil)
	real := devicetest.RAMCmdline
	for _, c := range []struct {
		name string
		raw  string // the whole file
		want string // "" = accepted
	}{
		{"the real line", real + "\n", ""},
		{"the real line without the newline", real, ""},
		{"the real line with tabs", strings.ReplaceAll(real, " ", "\t") + "\n", ""},
		{"the real line with blanks around it", "  \t" + real + " \t \n", ""},
		{"the real line, root= after a tab", real + "\troot=/dev/mmcblk0p22\n", "booted system"},
		// the reviewer's cases: a word that only the kernel sees
		{"root= behind a 0xA0 byte", real + "\xa0root=/dev/mmcblk0p22\n", "byte 0xa0"},
		{"root= behind a 0xA0 byte, no space", strings.Replace(real, " uc-pxx-inst.ram=1", "\xa0root=/dev/mmcblk0p22 uc-pxx-inst.ram=1", 1) + "\n", "byte 0xa0"},
		{"init= behind a 0xA0 byte", real + "\xa0init=/bin/sh\n", "byte 0xa0"},
		{"root= behind a UTF-8 no-break space", real + "\u00a0root=/dev/mmcblk0p22\n", "byte 0xc2"},
		{"root= behind U+0085", real + "\u0085root=/dev/mmcblk0p22\n", "byte 0xc2"},
		{"a quoted root=", real + " \"root=/dev/mmcblk0p22\"\n", `'"'`},
		{"root= in a quoted value", real + " x=\"a root=/dev/mmcblk0p22\"\n", `'"'`},
		{"a quoted token", strings.Replace(real, "uc-pxx.id=pxx-inst-4f3f5e", "uc-pxx.id=\"pxx-inst-4f3f5e\"", 1) + "\n", `'"'`},
		{"a quote alone", real + " \"\n", `'"'`},
		{"root= behind a CR", real + "\rroot=/dev/mmcblk0p22\n", "byte 0x0d"},
		{"root= behind a newline", real + "\nroot=/dev/mmcblk0p22\n", "byte 0x0a"},
		{"root= behind VT", real + "\vroot=/dev/mmcblk0p22\n", "byte 0x0b"},
		{"root= behind FF", real + "\froot=/dev/mmcblk0p22\n", "byte 0x0c"},
		{"two newlines at the end", real + "\n\n", "byte 0x0a"},
		{"a CR at the end", real + "\r\n", "byte 0x0d"},
		{"a NUL byte", real + "\x00\n", "byte 0x00"},
		{"a NUL at the end", real + "\n\x00", "byte 0x0a"},
		{"DEL", real + "\x7f\n", "byte 0x7f"},
		{"0x80", real + "\x80\n", "byte 0x80"},
		{"0xff", real + "\xff\n", "byte 0xff"},
		{"an escape byte", real + " \x1b[0m\n", "byte 0x1b"},
	} {
		if err := os.WriteFile(u.root.Path("/proc/cmdline"), []byte(c.raw), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := u.env.CheckRAMImage()
		switch {
		case c.want == "" && (err != nil || got != "pxx-inst-4f3f5e"):
			t.Errorf("%s: %q %v, want accepted", c.name, got, err)
		case c.want != "" && (err == nil || !device.IsRefusal(err) || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: %v, want a refusal with %q", c.name, err, c.want)
		}
	}
}

// kernelWords splits a command line as the kernel does: parse_args skips
// spaces and next_arg (lib/cmdline.c) ends a word at an isspace byte
// (lib/ctype.c: tab, LF, VT, FF, CR, space, 0xA0) outside quotes, and drops
// the quotes around a word and around a value. Each word comes back as
// param or param=value.
func kernelWords(s string) []string {
	isspace := func(c byte) bool { return c == ' ' || (c >= '\t' && c <= '\r') || c == 0xa0 }
	var out []string
	b := []byte(s)
	for {
		for len(b) > 0 && isspace(b[0]) {
			b = b[1:]
		}
		if len(b) == 0 {
			return out
		}
		quoted, inQuote := false, false
		if b[0] == '"' {
			b, quoted, inQuote = b[1:], true, true
		}
		i, equals := 0, 0
		for ; i < len(b); i++ {
			if isspace(b[i]) && !inQuote {
				break
			}
			if equals == 0 && b[i] == '=' {
				equals = i
			}
			if b[i] == '"' {
				inQuote = !inQuote
			}
		}
		// as next_arg does it: it puts NULs into the word, the C strings end there
		w := append([]byte(nil), b[:i]...)
		cstr := func(x []byte) string {
			if k := bytes.IndexByte(x, 0); k >= 0 {
				return string(x[:k])
			}
			return string(x)
		}
		valStart := -1
		if equals != 0 {
			valStart = equals + 1
			if valStart < len(w) && w[valStart] == '"' {
				valStart++
				if w[i-1] == '"' {
					w[i-1] = 0
				}
			}
		}
		if quoted && i > 0 && w[i-1] == '"' {
			w[i-1] = 0
		}
		word := cstr(w)
		if valStart >= 0 {
			word = cstr(w[:equals]) + "=" + cstr(w[valStart:])
		}
		out = append(out, word)
		b = b[i:]
	}
}

// The words that CheckRAMImage checks are the words that the kernel sees:
// whenever a random line passes, the kernel's own split of it (kernelWords)
// has uc-pxx-inst.ram=1, exactly one uc-pxx.id= word, the RAM token, and no word
// of a booted system. The lines are made of tricky words and separators,
// 0xA0 and quotes among them.
func TestCheckRAMImageCmdlineAsTheKernelSplits(t *testing.T) {
	u := newFake(t, []byte("x"), "", nil)
	words := []string{"uc-pxx-inst.ram=1", "uc-pxx.id=pxx-inst-4f3f5e", "uc-pxx.id=p10inst-00a9ff", "uc-pxx.id=ucpxx-dfdcdfd8",
		"root=/dev/mmcblk0p22", "init=/bin/sh", "androidboot.slot_suffix=_a", "skip_initramfs", "rdinit=/init",
		"console=ttyS2,115200n8", "x=\"a", "b\"", "\"root=/dev/x\"", "y=\"root=/dev/x\"", "\"", "\"uc-pxx-inst.ram=1", "loglevel=7"}
	seps := []string{"\xa0", "\u00a0", "\r", "\n", "\v", "\f", " \xa0 ", "\u0085"}
	rng := rand.New(rand.NewSource(20261028))
	accepted, refusedByBytes := 0, 0
	for n := 0; n < 50000; n++ {
		// the two words of the RAM image and up to 4 more, in any order; most
		// separators are a space or a tab, some are tricky
		ws := []string{"uc-pxx-inst.ram=1", "uc-pxx.id=pxx-inst-4f3f5e"}
		for k := rng.Intn(5); k > 0; k-- {
			ws = append(ws, words[rng.Intn(len(words))])
		}
		rng.Shuffle(len(ws), func(i, j int) { ws[i], ws[j] = ws[j], ws[i] })
		var sb strings.Builder
		for _, w := range ws {
			sb.WriteString(w)
			switch r := rng.Intn(8); {
			case r < 4:
				sb.WriteString(" ")
			case r < 6:
				sb.WriteString("\t")
			default:
				sb.WriteString(seps[rng.Intn(len(seps))])
			}
		}
		line := sb.String()
		if err := os.WriteFile(u.root.Path("/proc/cmdline"), []byte(line+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := u.env.CheckRAMImage()
		if err != nil {
			if strings.Contains(err.Error(), "byte") || strings.Contains(err.Error(), `'"'`) {
				refusedByBytes++
			}
			continue
		}
		accepted++
		ram, ids, booted := false, 0, ""
		for _, w := range kernelWords(line) {
			ram = ram || w == "uc-pxx-inst.ram=1"
			if strings.HasPrefix(w, "uc-pxx.id=") {
				ids++
				if w == "uc-pxx.id="+got {
					continue
				}
			}
			for _, b := range []string{"uc-pxx.id=", "androidboot.", "root=", "skip_initramfs", "init="} {
				if strings.HasPrefix(w, b) {
					booted = w
				}
			}
		}
		if !ram || ids != 1 || booted != "" {
			t.Fatalf("accepted %q (token %q), but the kernel sees: ram %v, %d uc-pxx.id= words, %q", line, got, ram, ids, booted)
		}
	}
	if accepted == 0 || refusedByBytes == 0 {
		t.Fatalf("the random lines test nothing: %d accepted, %d refused for their bytes", accepted, refusedByBytes)
	}
	t.Logf("%d accepted, %d refused for their bytes, of 50000", accepted, refusedByBytes)
}

// The default runner starts the tool in its own session and process group,
// with stdout and stderr in files, not pipes (review F2): a ^C, a hangup or a
// closed pipe of uc-pxx-inst cannot reach it.
func TestExecRunnerSession(t *testing.T) {
	bin := []byte("#!/bin/sh\nset -- $(cut -d' ' -f1,5,6 /proc/$$/stat)\necho \"pid $1 pgrp $2 sid $3\"\necho \"out $(readlink /proc/$$/fd/1)\"\necho \"err $(readlink /proc/$$/fd/2)\" >&2\nexit 0\n")
	u := newFake(t, bin, devicetest.SHA256(bin), nil)
	r, err := u.d.Bootctl([]string{"status"})
	if err != nil || r.RC != 0 {
		t.Fatalf("%+v %v", r, err)
	}
	var pid, pgrp, sid int
	if _, err := fmt.Sscanf(r.Stdout, "pid %d pgrp %d sid %d", &pid, &pgrp, &sid); err != nil {
		t.Fatalf("%q: %v", r.Stdout, err)
	}
	if pgrp != pid || sid != pid || pgrp == syscall.Getpgrp() {
		t.Fatalf("pid %d pgrp %d sid %d (ours: pgrp %d): not its own session", pid, pgrp, sid, syscall.Getpgrp())
	}
	if !strings.Contains(r.Stdout, "out "+u.root.Path("/run/uc-pxx-inst/bootctl-")) || !strings.HasSuffix(strings.TrimSpace(r.Stdout), "/stdout") ||
		!strings.Contains(r.Stderr, "/stderr") || strings.Contains(r.Stdout+r.Stderr, "pipe:") {
		t.Fatalf("output is not in files: %q %q", r.Stdout, r.Stderr)
	}
	// a tool that a signal ends is reported as such, not as a plain exit
	bin2 := []byte("#!/bin/sh\nkill -HUP $$\nsleep 5\n")
	u2 := newFake(t, bin2, devicetest.SHA256(bin2), nil)
	r2, err := u2.d.Bootctl([]string{"status"})
	if err != nil || r2.RC != -1 || r2.Signal != "hangup" {
		t.Fatalf("%+v %v", r2, err)
	}
}
