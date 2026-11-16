package flowio

// The step-1 USB service in the runner (usb.go, mainline-rc-10 I-6) with the
// fake children of flowio_test.go: the test plays the service (step1.json
// with a sealed folder, as `uc-pxx-xfer inst` writes it) and checks what the
// runner asks for (the want file), what the flow shows, the info file for
// the page, and the children's arguments (--fw FILE --fw-expect ID,
// --ssh-key, --mainline DIR in place of the drive).

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"uc-pxx-installer/internal/drivestate"
	"uc-pxx-installer/internal/flow"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/xfer/handover"
)

// usbService is the test's stand-in for `uc-pxx-xfer inst`.
type usbService struct {
	g   *rig
	res handover.InstResult
}

func newUSBService(g *rig) *usbService {
	os.MkdirAll(filepath.Dir(filepath.Join(g.root, XferBinary)), 0o755)
	os.WriteFile(filepath.Join(g.root, XferBinary), []byte("xfer"), 0o755)
	os.MkdirAll(filepath.Join(g.root, handover.InstRunDir), 0o755)
	os.MkdirAll(filepath.Join(g.root, handover.InstStage, "xfer"), 0o755)
	// no drive in USB mode: the lun of the mass storage does not exist
	os.Remove(filepath.Join(g.root, drivestate.LunFile))
	s := &usbService{g: g, res: handover.InstResult{Mode: handover.InstModeUSB, Stage: handover.InstStageFirmware}}
	s.publish()
	return s
}

func (s *usbService) publish() {
	s.res.Seq++
	b, _ := json.Marshal(s.res)
	os.WriteFile(filepath.Join(s.g.root, handover.InstRunDir, handover.InstResultFile), b, 0o644)
}

func (s *usbService) want() (int, string) {
	return readWant(filepath.Join(s.g.root, handover.InstRunDir, handover.InstWantFile))
}

// sealFW makes the sealed firmware stage (fw/firmware.bin, and the key).
func (s *usbService) sealFW(mode string, key bool) string {
	dir := filepath.Join(handover.InstStage, handover.InstFWDir)
	os.MkdirAll(filepath.Join(s.g.root, dir), 0o755)
	fw := []byte(strings.Repeat("firmware", 1000))
	os.WriteFile(filepath.Join(s.g.root, dir, handover.FirmwareName), fw, 0o400)
	rec := handover.Record{Version: 1, Kind: handover.RecordKindStep1FW, Mode: mode, ManifestSHA256: hexSHA(fw), Session: 1, Time: time.Now().Unix(),
		Files: []handover.ReadyFile{{Name: handover.FirmwareName, Size: int64(len(fw)), SHA256: hexSHA(fw)}}}
	if key {
		k := []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBbrhXWbO19v2SgtcPAMqpGOt8V5hUqtgQyUCJxvGtTp test\n")
		os.WriteFile(filepath.Join(s.g.root, dir, handover.SSHKeyName), k, 0o400)
		rec.Files = append(rec.Files, handover.ReadyFile{Name: handover.SSHKeyName, Size: int64(len(k)), SHA256: hexSHA(k)})
	}
	s.res.Firmware, s.res.Stage = &handover.InstSealed{Dir: dir, Record: rec}, handover.InstStageBusy
	s.publish()
	return filepath.Join(s.g.root, dir, handover.FirmwareName)
}

// sealBundle makes the sealed bundle folder after the want "bundle".
func (s *usbService) sealBundle() string {
	n, w := s.want()
	if w != handover.WantBundle {
		s.g.t.Fatalf("the service was not asked for the bundle: %d %q", n, w)
	}
	os.Remove(filepath.Join(s.g.root, s.res.Firmware.Dir, handover.FirmwareName))
	b := mlBundle()
	dir := filepath.Join(handover.InstStage, handover.InstBundleDir)
	d := filepath.Join(s.g.root, dir)
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, mainline.ManifestFile), b.ManifestJSON(), 0o400)
	os.WriteFile(filepath.Join(d, mainline.TemplateFile), b.Template, 0o400)
	os.WriteFile(filepath.Join(d, mainline.RootfsFile), b.Rootfs, 0o400)
	s.res.Bundle = &handover.InstSealed{Dir: dir, Record: handover.Record{Version: 1, ManifestSHA256: hexSHA(b.ManifestJSON()), Session: 2, Time: 2}}
	s.res.Stage, s.res.WantSeq = handover.InstStageBusy, n
	s.publish()
	return d
}

func (g *rig) info() handover.Info {
	var in handover.Info
	b, _ := os.ReadFile(filepath.Join(g.root, handover.InstRunDir, handover.InstInfoFile))
	json.Unmarshal(b, &in)
	return in
}

// TestUSBMainlineStepOne: the firmware and the key over USB, the stash child
// with the sealed files, the request for the bundle, the bundle over USB,
// the plan and install children with --mainline of the sealed folder.
func TestUSBMainlineStepOne(t *testing.T) {
	g := newStashRig(t, mlS0, mlD2, "exit 0")
	s := newUSBService(g)
	g.r.Start()
	if !g.m.USBOn() {
		t.Fatal("the flow does not know the USB service")
	}
	g.pump(func() bool { return g.m.Unit != nil }, 20*time.Second)
	g.key(flow.KeyNext)
	g.key(flow.KeyOK) // Install
	if g.m.Screen != flow.ScreenFirmware || !strings.Contains(bodyText(g.m), "Waiting for the installer page") && !strings.Contains(g.m.View(time.Now()).Title, "Waiting for the installer page") {
		t.Fatalf("not the USB wait: %s\n%s", g.m.Screen, bodyText(g.m))
	}
	if p := g.info().Phase; p != "waiting-firmware" {
		t.Fatalf("info phase %q", p)
	}
	// a receive in progress shows on the screen
	st, _ := json.Marshal(handover.State{State: "receiving", File: "firmware.bin", Bytes: 400e6, Total: 972963471})
	os.WriteFile(filepath.Join(g.root, handover.InstStage, "xfer", handover.StateFile), st, 0o644)
	g.pump(func() bool { return strings.Contains(bodyText(g.m), "Receiving the firmware file: 400 of 972 MB") }, 5*time.Second)
	fw := s.sealFW("mainline", true)
	id, _ := drivestate.Stat(fw)
	g.pump(func() bool { return strings.Contains(strings.Join(g.r.log, "\n"), "stash child ended") }, 30*time.Second)
	args := g.mlArgs()
	want := fmt.Sprintf("plan --mainline-stash --device /dev/mmcblk0 --fw %s --fw-expect %s --ssh-key %s", fw, id,
		filepath.Join(g.root, handover.InstStage, handover.InstFWDir, handover.SSHKeyName))
	if len(args) != 1 || args[0] != want {
		t.Fatalf("the stash child\n%q\nwant\n%q", args, want)
	}
	g.pump(func() bool { n, w := s.want(); return n == 1 && w == handover.WantBundle }, 5*time.Second)
	if g.m.Screen != flow.ScreenFirmware || !strings.Contains(bodyText(g.m), "Waiting for mainline Linux, part 1") {
		t.Fatalf("after the stash: %s\n%s", g.m.Screen, bodyText(g.m))
	}
	if p := g.info().Phase; p != "waiting-bundle" {
		t.Fatalf("info phase %q", p)
	}
	// an old sealed bundle never counts before the service did the request
	dir := s.sealBundle()
	g.pump(func() bool { return g.m.Screen == flow.ScreenUnlock }, 30*time.Second)
	g.panelYes()
	g.panelYes()
	g.pump(func() bool { return g.m.Screen == flow.ScreenPlan }, 30*time.Second)
	if p := g.info().Phase; p != "plan" {
		t.Fatalf("info phase %q", p)
	}
	g.key(flow.KeyOK)
	g.key(flow.KeyYes)
	g.end()
	if g.m.Screen != flow.ScreenDone {
		t.Fatalf("screen %s", g.m.Screen)
	}
	args = g.mlArgs()
	if len(args) != 3 || !strings.HasPrefix(args[1], "plan --device /dev/mmcblk0 --mainline "+dir+" --json ") ||
		!strings.HasPrefix(args[2], "install --device /dev/mmcblk0 --mainline "+dir+" --plan ") || strings.Contains(args[2], "--fw-drive") {
		t.Fatalf("children %q", args)
	}
	if p := g.info().Phase; p != "done" {
		t.Fatalf("info phase %q", p)
	}
	log := strings.Join(g.r.log, "\n")
	for _, w := range []string{"usb: firmware sealed", "usb: request 1: bundle", "usb: bundle sealed", "the files came over the USB cable"} {
		if !strings.Contains(log, w) {
			t.Fatalf("no %q in the flow log:\n%s", w, log)
		}
	}
}

// TestUSBOfficial: an official firmware over USB: the plan and install
// children with --fw FILE --fw-expect ID; the firmware changed before the
// install refuses (nothing written).
func TestUSBOfficial(t *testing.T) {
	for _, changed := range []bool{false, true} {
		g := newRig(t, s0, "exit 0")
		s := newUSBService(g)
		g.r.Start()
		g.pump(func() bool { return g.m.Unit != nil }, 20*time.Second)
		g.key(flow.KeyNext)
		g.key(flow.KeyOK)
		fw := s.sealFW("official", false)
		id, _ := drivestate.Stat(fw)
		g.pump(func() bool { return g.m.Screen == flow.ScreenPanel || g.m.Screen == flow.ScreenPlanning }, 10*time.Second)
		if g.m.Screen == flow.ScreenPanel {
			g.panelYes()
		}
		g.pump(func() bool { return g.m.Screen == flow.ScreenPlan }, 30*time.Second)
		if changed {
			os.Chmod(fw, 0o600)
			os.WriteFile(fw, []byte("other"), 0o600)
		}
		g.key(flow.KeyOK)
		g.key(flow.KeyYes)
		if changed {
			g.pump(func() bool { return g.m.Screen == flow.ScreenRefused }, 10*time.Second)
			if g.m.Screen != flow.ScreenRefused {
				t.Fatalf("a changed firmware was installed: %s", g.m.Screen)
			}
			if n, w := s.want(); n != 1 || w != handover.WantFirmware {
				t.Fatalf("after the refusal the service was asked %d %q", n, w)
			}
			continue
		}
		g.end()
		b, _ := os.ReadFile(filepath.Join(filepath.Dir(g.root), "args.log"))
		_ = b
		log := strings.Join(g.r.log, "\n")
		if !strings.Contains(log, "plan child started: "+g.r.c.Exe+" plan --device /dev/mmcblk0 --fw "+fw+" --fw-expect "+id.String()) ||
			!strings.Contains(log, "install child started: "+g.r.c.Exe+" install --device /dev/mmcblk0 --fw "+fw+" --fw-expect "+id.String()) {
			t.Fatalf("children:\n%s", log)
		}
	}
}

// TestUSBDriveMode: Vol- on the wait asks for the drive; the service's drive
// mode gives the drive screens.
func TestUSBDriveMode(t *testing.T) {
	g := newRig(t, s0, "exit 0")
	s := newUSBService(g)
	g.r.Start()
	g.pump(func() bool { return g.m.Unit != nil }, 20*time.Second)
	g.key(flow.KeyNext)
	g.key(flow.KeyOK)
	g.key(flow.KeyPrev)
	if n, w := s.want(); n != 1 || w != handover.WantDrive {
		t.Fatalf("Vol- asked %d %q", n, w)
	}
	s.res.Mode, s.res.Stage, s.res.WantSeq = handover.InstModeDrive, handover.InstStageDrive, 1
	s.publish()
	img := filepath.Join(g.root, drivestate.Image)
	os.WriteFile(filepath.Join(g.root, drivestate.LunFile), []byte(img+"\n"), 0o644)
	g.pump(func() bool { return g.m.USBDriveMode() }, 5*time.Second)
	if !strings.Contains(g.m.View(time.Now()).Title, "drive") && !strings.Contains(bodyText(g.m), "drive") {
		t.Fatalf("not the drive screen:\n%s", bodyText(g.m))
	}
	if p := g.info().Phase; p != "drive: waiting-firmware" {
		t.Fatalf("info phase %q", p)
	}
}
