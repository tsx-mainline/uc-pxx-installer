package xfer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"uc-pxx-installer/internal/xfer/handover"
)

func readCfs(t *testing.T, root, p string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, p))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestInstGadget: the RAM installer's gadget has the step-2 transport's
// identity with the installer's bcdDevice values, the vendor interface first,
// then ACM and NCM; the drive only by AddInstDrive.
func TestInstGadget(t *testing.T) {
	root := filepath.Join(t.TempDir(), "uc-pxx-inst")
	os.MkdirAll(filepath.Join(root, "webusb"), 0o755) // configfs makes os_desc and webusb itself
	os.MkdirAll(filepath.Join(root, "os_desc"), 0o755)
	if err := NewInstGadget(root); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{"idVendor": "0x1d6b", "idProduct": "0x0105", "bcdDevice": "0x0301", "bDeviceClass": "0xef",
		"bDeviceSubClass": "0x02", "bDeviceProtocol": "0x01", "strings/0x409/serialnumber": "UC-PXX-STEP2",
		"strings/0x409/product": USBProduct, "os_desc/use": "1", "os_desc/b_vendor_code": "0xcd",
		"functions/ncm.usb0/host_addr": instNCMHost, "functions/ncm.usb0/dev_addr": instNCMDev, "webusb/use": "1",
		"webusb/landingPage": LandingPage} {
		if got := readCfs(t, root, p); got != want {
			t.Errorf("%s = %q, want %q", p, got, want)
		}
	}
	fns, _ := Functions(root)
	if strings.Join(fns, ",") != "acm.usb0,ffs.uc-pxx-inst,ncm.usb0" {
		t.Fatalf("functions %v (no drive before the drive mode)", fns)
	}
	if err := LinkInst(root); err != nil {
		t.Fatal(err)
	}
	if err := LinkInst(root); err != nil { // idempotent
		t.Fatal(err)
	}
	if l, _ := InstFunctions(root); strings.Join(l, ",") != "acm.usb0,ffs.uc-pxx-inst,ncm.usb0" {
		t.Fatalf("links %v", l)
	}
	dir := t.TempDir()
	os.Symlink("/proc/self/exe", filepath.Join(dir, "link"))
	if err := AddInstDrive(root, filepath.Join(dir, "link")); err == nil {
		t.Fatal("a drive behind a symbolic link was taken")
	}
	if err := AddInstDrive(root, dir); err == nil {
		t.Fatal("a folder was taken as the drive")
	}
	drive := filepath.Join(dir, "drive.img")
	os.WriteFile(drive, nil, 0o600)
	var sf syscall.Statfs_t
	syscall.Statfs(dir, &sf)
	if int64(sf.Type) != tmpfsMagic {
		if err := AddInstDrive(root, drive); err == nil || !strings.Contains(err.Error(), "tmpfs") {
			t.Fatalf("a drive file that is not on a tmpfs: %v", err)
		}
		old := driveInRAM
		driveInRAM = func(string) error { return nil }
		defer func() { driveInRAM = old }()
	}
	if err := AddInstDrive(root, drive); err != nil {
		t.Fatal(err)
	}
	if got := readCfs(t, root, "functions/mass_storage.usb0/lun.0/file"); got != drive {
		t.Fatalf("lun file %q", got)
	}
	if got := readCfs(t, root, "bcdDevice"); got != "0x0302" {
		t.Fatalf("bcdDevice in the drive mode %q", got)
	}
	if l, _ := InstFunctions(root); !strings.Contains(strings.Join(l, ","), "mass_storage.usb0") {
		t.Fatalf("links %v", l)
	}
	// the interface order: the configuration links are made in order (vendor first)
	if InstConfigFS != filepath.Dir(filepath.Dir(filepath.Dir("/sys/kernel/config/usb_gadget/uc-pxx-inst/functions/mass_storage.usb0/lun.0"))) {
		t.Fatal("the gadget folder is not the one of drivestate.LunFile")
	}
}

// instRig is an inst on temporary folders, with fake process and owner
// steps, and a real step-1 receiver in its folder.
func instRig(t *testing.T, fw []FWPin, bundle []string) *inst {
	t.Helper()
	stage := t.TempDir()
	t.Cleanup(func() { // the sealed folders are 0500 (root would write in them)
		for _, d := range []string{handover.InstFWDir, handover.InstBundleDir} {
			os.Chmod(filepath.Join(stage, d), 0o700)
		}
	})
	dir := filepath.Join(stage, XferDir)
	os.Mkdir(dir, 0o700)
	run := t.TempDir()
	s := &inst{c: InstConfig{Stage: stage, RunDir: run, ConfigFS: filepath.Join(t.TempDir(), "g"), FFS: "/ffs", Idle: DefaultIdle, Reserve: DefaultReserve},
		id: Ident{UID: os.Getuid(), GID: os.Getgid()}, pins: &handover.InstPins{FW: fw, Bundle: bundle}, dir: dir, logf: t.Logf,
		noProc: func(int) error { return nil }, chown: func(string) error { return nil }}
	s.res = handover.InstResult{Mode: handover.InstModeUSB, Stage: handover.InstStageFirmware}
	return s
}

// receive runs one receiver session of the stage on s.dir.
func (s *inst) testReceiver(t *testing.T, cfg Config) *rig {
	t.Helper()
	cfg.Kind = KindStep1
	g := newRig(t, cfg)
	g.dir = s.dir
	cfg.Dir = s.dir
	cfg.MemAvailable = func() int64 { return 4 << 30 }
	cfg.Free = func(string) int64 { return 8 << 30 }
	cfg.Logf = t.Logf
	r, err := NewReceiver(cfg)
	if err != nil {
		t.Fatal(err)
	}
	g.r = r
	return g
}

func TestInstFirmwareSealThenBundle(t *testing.T) {
	fw, pin := fwFixture(1 << 20)
	z := newZstFixture(t, nil)
	s := instRig(t, []FWPin{pin}, []string{z.pin})
	if a := s.args(); !strings.Contains(strings.Join(a, " "), "--kind step1") || !strings.Contains(strings.Join(a, " "), "--stage firmware") ||
		!strings.Contains(strings.Join(a, " "), "--fw-pin 1048576:"+pin.SHA256) || strings.Contains(strings.Join(a, " "), "--pin ") {
		t.Fatalf("firmware args %v", a)
	}
	g := s.testReceiver(t, Config{Stage: Step1Firmware, FWPins: []FWPin{pin}})
	h, wait := g.session()
	h.put(FirmwareName, fw, int64(len(fw)), DataChunk)
	h.put(SSHKeyName, []byte(testKey), int64(len(testKey)), DataChunk)
	if c := h.req(TypeCommit, Commit{Mode: "mainline"}); c["ok"] != true {
		t.Fatal(c)
	}
	h.l.disable()
	wait()
	if err := s.seal(); err != nil {
		t.Fatal(err)
	}
	fwDir := filepath.Join(s.c.Stage, handover.InstFWDir)
	if fi, err := os.Lstat(fwDir); err != nil || fi.Mode().Perm() != 0o500 {
		t.Fatalf("the sealed folder %v %v", fi, err)
	}
	for _, n := range []string{FirmwareName, SSHKeyName} {
		if fi, err := os.Lstat(filepath.Join(fwDir, n)); err != nil || fi.Mode().Perm() != 0o400 {
			t.Fatalf("%s %v %v", n, fi, err)
		}
	}
	if s.res.Firmware == nil || s.res.Firmware.Record.Mode != "mainline" || s.res.Firmware.Dir != fwDir {
		t.Fatalf("result %+v", s.res)
	}
	if _, err := os.Lstat(filepath.Join(s.dir, ReadyDir)); err == nil {
		t.Fatal("the ready folder is still with the receiver")
	}
	// the flow asks for the bundle: the firmware goes first (root writes
	// in its 0500 folder; this test runs as a user)
	os.Chmod(fwDir, 0o700)
	s.res.Stage = handover.InstStageBusy
	if r, err := s.act(handover.WantBundle); err != nil || !r {
		t.Fatal(r, err)
	}
	if _, err := os.Lstat(filepath.Join(fwDir, FirmwareName)); !os.IsNotExist(err) {
		t.Fatalf("firmware.bin is still there: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(fwDir, SSHKeyName)); err != nil {
		t.Fatal("the key went with the firmware")
	}
	if a := strings.Join(s.args(), " "); !strings.Contains(a, "--stage bundle") || !strings.Contains(a, "--pin "+z.pin) || strings.Contains(a, "--fw-pin") {
		t.Fatalf("bundle args %s", a)
	}
	if _, err := s.act(handover.WantBundle); err == nil {
		t.Fatal("a second bundle stage")
	}
	g = s.testReceiver(t, Config{Stage: Step1Bundle, Pins: []string{z.pin}})
	h, wait = g.session()
	h.put(ManifestName, z.manifest, int64(len(z.manifest)), DataChunk)
	h.put(TemplateName, z.template, int64(len(z.template)), DataChunk)
	h.put(RootfsZstName, z.packed, int64(len(z.packed)), DataChunk)
	if c := h.req(TypeCommit, Commit{}); c["ok"] != true {
		t.Fatal(c)
	}
	h.l.disable()
	wait()
	if err := s.seal(); err != nil {
		t.Fatal(err)
	}
	bd := filepath.Join(s.c.Stage, handover.InstBundleDir)
	if s.res.Bundle == nil || s.res.Bundle.Dir != bd || s.res.Bundle.Record.ManifestSHA256 != z.pin {
		t.Fatalf("bundle %+v", s.res.Bundle)
	}
	if b, _ := os.ReadFile(filepath.Join(bd, RootfsName)); sum(b) != sum(z.rootfs) {
		t.Fatal("the sealed rootfs is not the unpacked one")
	}
	// start again: both sealed folders go (root writes in them)
	os.Chmod(bd, 0o700)
	if _, err := s.act(handover.WantFirmware); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{fwDir, bd} {
		if _, err := os.Lstat(d); !os.IsNotExist(err) {
			t.Fatalf("%s is still there", d)
		}
	}
	if s.res.Firmware != nil || s.res.Bundle != nil || s.res.Stage != handover.InstStageFirmware {
		t.Fatalf("result after the start again %+v", s.res)
	}
}

func TestInstRefusals(t *testing.T) {
	fw, pin := fwFixture(1 << 20)
	s := instRig(t, []FWPin{pin}, nil)
	// an official firmware: no bundle stage
	g := s.testReceiver(t, Config{Stage: Step1Firmware, FWPins: []FWPin{pin}})
	h, wait := g.session()
	h.put(FirmwareName, fw, int64(len(fw)), DataChunk)
	h.req(TypeCommit, Commit{Mode: "official"})
	h.l.disable()
	wait()
	// a process of the receiver's user is left: no seal
	s.noProc = func(int) error { return os.ErrPermission }
	if err := s.seal(); err == nil {
		t.Fatal("sealed with a process of the receiver's user")
	}
	s.noProc = func(int) error { return nil }
	// a file added to the ready folder: no seal
	os.WriteFile(filepath.Join(s.dir, ReadyDir, "extra"), []byte("x"), 0o600)
	if err := s.seal(); err == nil || !strings.Contains(err.Error(), "not exactly") {
		t.Fatalf("sealed a folder with another file: %v", err)
	}
	os.Remove(filepath.Join(s.dir, ReadyDir, "extra"))
	if err := s.seal(); err != nil {
		t.Fatal(err)
	}
	s.res.Stage = handover.InstStageBusy
	if _, err := s.act(handover.WantBundle); err == nil {
		t.Fatal("a bundle stage after an official firmware")
	}
	if _, err := s.act(handover.WantDrive); err == nil || !strings.Contains(err.Error(), "no RAM drive") {
		t.Fatalf("the drive mode without a drive: %v", err)
	}
	if _, err := s.act("format"); err == nil {
		t.Fatal("an unknown request was taken")
	}
	s.c.Drive = filepath.Join(t.TempDir(), "drive.img")
	os.WriteFile(s.c.Drive, nil, 0o600)
	old := driveInRAM
	driveInRAM = func(string) error { return nil }
	defer func() { driveInRAM = old }()
	os.MkdirAll(filepath.Join(s.c.ConfigFS, "os_desc"), 0o755)
	if err := NewInstGadget(s.c.ConfigFS); err != nil {
		t.Fatal(err)
	}
	if r, err := s.act(handover.WantDrive); err != nil || !r || s.res.Mode != handover.InstModeDrive || s.res.Stage != handover.InstStageDrive {
		t.Fatalf("drive %v %v %+v", r, err, s.res)
	}
	if a := strings.Join(s.args(), " "); !strings.Contains(a, "--status-only --status-state drive") || strings.Contains(a, "--dir") {
		t.Fatalf("drive args %s", a)
	}
	if _, err := s.act(handover.WantFirmware); err == nil {
		t.Fatal("the drive mode was left in the same boot")
	}
}

func TestInstWantAndPins(t *testing.T) {
	s := instRig(t, nil, nil)
	for _, c := range []struct {
		text string
		n    int
		want string
	}{{"3 bundle\n", 3, "bundle"}, {"x bundle", 0, ""}, {"0 drive", 0, ""}, {"4", 0, ""}, {"5 drive now", 0, ""}} {
		os.WriteFile(filepath.Join(s.c.RunDir, handover.InstWantFile), []byte(c.text), 0o644)
		if n, w := s.readWant(); n != c.n || w != c.want {
			t.Errorf("%q: %d %q", c.text, n, w)
		}
	}
	p := filepath.Join(t.TempDir(), "pins.json")
	_, pin := fwFixture(10)
	good, _ := json.Marshal(handover.InstPins{FW: []FWPin{pin}, Bundle: []string{pin.SHA256}})
	os.WriteFile(p, good, 0o644)
	if ps, err := ReadInstPins(p); err != nil || len(ps.FW) != 1 || len(ps.Bundle) != 1 {
		t.Fatalf("%+v %v", ps, err)
	}
	for _, bad := range []string{`{"fw":[]}`, `{"fw":[{"size":0,"sha256":"` + pin.SHA256 + `"}]}`, `{"fw":[{"size":1,"sha256":"x"}]}`,
		`{"fw":[{"size":1,"sha256":"` + pin.SHA256 + `"}],"bundle":["y"]}`, `{"fw":[{"size":1,"sha256":"` + pin.SHA256 + `"}],"x":1}`} {
		os.WriteFile(p, []byte(bad), 0o644)
		if _, err := ReadInstPins(p); err == nil {
			t.Errorf("taken: %s", bad)
		}
	}
	root := t.TempDir()
	if InRAMImage(root) == nil {
		t.Fatal("no marker taken")
	}
	os.MkdirAll(filepath.Join(root, "etc/uc-pxx-inst"), 0o755)
	os.WriteFile(filepath.Join(root, RAMImageMarker), nil, 0o644)
	os.MkdirAll(filepath.Join(root, "proc"), 0o755)
	for cl, ok := range map[string]bool{"console=x uc-pxx-inst.ram=1 uc-pxx.id=pxx-inst-abcdef": true, "uc-pxx-inst.ram=1 uc-pxx-inst.ram=1": false, "uc-pxx-inst.ram=0": false} {
		os.WriteFile(filepath.Join(root, "proc/cmdline"), []byte(cl), 0o644)
		if (InRAMImage(root) == nil) != ok {
			t.Errorf("%q: %v", cl, InRAMImage(root))
		}
	}
}

// TestInstDropsUnsealed (RC-10 review I-6 MAJOR-2): a request that comes
// between the receiver's COMMIT (its ready folder and record) and the
// "complete" leaves no ready folder: a new receiver starts (NewReceiver
// refuses while one is there), for every request kind.
func TestInstDropsUnsealed(t *testing.T) {
	for _, want := range []string{handover.WantFirmware, handover.WantBusy, handover.WantDrive} {
		s := instRig(t, nil, nil)
		s.c.Drive = filepath.Join(t.TempDir(), "drive.img")
		os.WriteFile(s.c.Drive, nil, 0o600)
		os.MkdirAll(filepath.Join(s.dir, ReadyDir), 0o700)
		os.WriteFile(filepath.Join(s.dir, ReadyDir, FirmwareName), []byte("x"), 0o600)
		os.WriteFile(filepath.Join(s.dir, ReadyRecord), []byte("{}"), 0o600)
		os.MkdirAll(filepath.Join(s.dir, "incoming.3"), 0o700)
		s.act(want) // the drive's gadget change fails in the test folder: the drop came first
		for _, p := range []string{ReadyDir, ReadyRecord, "incoming.3"} {
			if _, err := os.Lstat(filepath.Join(s.dir, p)); !os.IsNotExist(err) {
				t.Errorf("%s: %s is still there", want, p)
			}
		}
		if _, err := NewReceiver(Config{Kind: KindStep1, Stage: Step1Firmware, Dir: s.dir, FWPins: []FWPin{{Size: 1, SHA256: strings.Repeat("a", 64)}},
			MemAvailable: func() int64 { return 4 << 30 }, Free: func(string) int64 { return 8 << 30 }}); err != nil {
			t.Errorf("%s: a new receiver: %v", want, err)
		}
	}
}

// TestInstFailedStart: a service that cannot start says so in step1.json
// (mode failed, the error): the flow shows the drive, rcS starts usbd.
func TestInstFailedStart(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("runs as a user: RunInst refuses without root")
	}
	run := t.TempDir()
	err := RunInst(InstConfig{RunDir: run}, nil)
	if err == nil {
		t.Fatal("RunInst ran without root")
	}
	r, rerr := handover.ReadInstResult(run)
	if rerr != nil || r.Mode != handover.InstModeFailed || !strings.Contains(r.Error, "root") {
		t.Fatalf("step1.json: %+v %v", r, rerr)
	}
}

// TestInstBackoff: 1 s, 2 s, 4 s ... at most 30 s; never a stop.
func TestInstBackoff(t *testing.T) {
	for n, want := range map[int]string{1: "2s", 2: "4s", 4: "16s", 5: "30s", 100: "30s"} {
		if got := instBackoff(n).String(); got != want {
			t.Errorf("%d: %s, want %s", n, got, want)
		}
	}
}
