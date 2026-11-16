package flowio

// The step-1 USB service in the runner (mainline-rc-10, I-6; the flow side is
// internal/flow/usb.go). The service (`uc-pxx-xfer inst`, root, no eMMC code)
// seals what the installer page sends into root-only RAM folders and says so
// in step1.json; the runner reads that file and the receiver's state file
// (both read only), gives the flow the sealed parts as it gives a drive read,
// writes the flow's requests (the want file) and the phase for the page (the
// info file), both its own files in /run. The children get the sealed files
// (--fw FILE --fw-expect ID, --ssh-key, --mainline DIR) in place of the drive.
// It adds no write of the eMMC and no new kind of child.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"uc-pxx-installer/internal/drivestate"
	"uc-pxx-installer/internal/flow"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/plan"
	"uc-pxx-installer/internal/xfer/handover"
)

// XferBinary is uc-pxx-xfer in the install image: the image has the step-1
// USB service when it holds it (image/mkinitramfs.sh).
const XferBinary = "/usr/libexec/uc-pxx-xfer/uc-pxx-xfer"

// usbRun is the runner's USB state. The last field of Runner.
type usbRun struct {
	seq       int    // the last step1.json seq read
	want      int    // the last request written
	delivered string // the record of the last sealed part given to the flow
	recv      flow.USBProgress
	info      string // the last info file written
}

// usbStart: the image has the USB service (its binary), so the flow waits
// for the files over the cable; the request counter goes on from the
// service's want file (a UI started again).
func (r *Runner) usbStart() {
	if _, err := os.Lstat(r.path(XferBinary)); err != nil {
		return
	}
	r.usb = &usbRun{}
	if n, _ := readWant(r.path(filepath.Join(handover.InstRunDir, handover.InstWantFile))); n > 0 {
		r.usb.want = n
	}
	r.M.SetUSB()
	r.say("usb: the image has the step-1 USB service (%s): the installer page sends the files over the cable", XferBinary)
}

func readWant(p string) (int, string) {
	b, err := os.ReadFile(p)
	if err != nil {
		return 0, ""
	}
	f := strings.Fields(string(b))
	if len(f) != 2 {
		return 0, ""
	}
	n, err := strconv.Atoi(f[0])
	if err != nil {
		return 0, ""
	}
	return n, f[1]
}

// pollUSB is the USB part of Poll.
func (r *Runner) pollUSB() {
	if r.usb == nil {
		return
	}
	res, err := handover.ReadInstResult(r.path(handover.InstRunDir))
	if err != nil {
		return // the service has not written it yet
	}
	if res.Seq != r.usb.seq {
		r.usb.seq = res.Seq
		r.say("usb: the service: mode %s, stage %s, request %d done; firmware sealed %v, bundle sealed %v%s", res.Mode, res.Stage,
			res.WantSeq, res.Firmware != nil, res.Bundle != nil, map[bool]string{true: "; error: " + res.Error}[res.Error != ""])
	}
	if res.Mode == handover.InstModeDrive && !r.M.USBDriveMode() {
		r.M.USBDrive()
		r.say("usb: the drive mode: the drive UC-PXX-INST is on the computer for the rest of this start")
	}
	if res.Mode == handover.InstModeFailed && !r.M.USBDriveMode() {
		// the service could not start: rcS starts the drive gadget in its
		// place (uc-pxx-inst usbd); the drive is the way in this start
		r.M.USBDrive()
		r.say("usb: the step-1 USB service could not start (%s): the drive UC-PXX-INST (uc-pxx-inst usbd) for the rest of this start", res.Error)
	}
	waiting := r.M.USBWaiting()
	if waiting == "" || res.WantSeq < r.usb.want {
		r.usbProgress(flow.USBProgress{})
		return // the service has not done the last request yet: an old sealed part never counts
	}
	switch {
	case waiting == "firmware" && res.Firmware != nil && res.Stage == handover.InstStageBusy:
		if k := sealedKey(res.Firmware); k != r.usb.delivered {
			r.usb.delivered = k
			r.usbFirmware(res.Firmware)
			return
		}
	case waiting == "bundle" && res.Bundle != nil && res.Stage == handover.InstStageBusy:
		if k := sealedKey(res.Bundle); k != r.usb.delivered {
			r.usb.delivered = k
			r.usbBundle(res.Bundle)
			return
		}
	}
	st, err := handover.ReadState(r.path(filepath.Join(handover.InstStage, "xfer")))
	if err == nil && st.State == "receiving" {
		r.usbProgress(flow.USBProgress{File: st.File, Bytes: st.Bytes, Total: st.Total})
	} else {
		r.usbProgress(flow.USBProgress{})
	}
}

func (r *Runner) usbProgress(p flow.USBProgress) {
	if p != r.usb.recv {
		r.usb.recv = p
		r.M.USBReceiving(p)
	}
}

func sealedKey(s *handover.InstSealed) string {
	return fmt.Sprintf("%s %d %d %s", s.Dir, s.Record.Session, s.Record.Time, s.Record.ManifestSHA256)
}

// usbFirmware gives the flow a sealed firmware stage: the identity of the
// sealed firmware.bin now (the children's --fw-expect).
func (r *Runner) usbFirmware(s *handover.InstSealed) {
	fw := r.path(filepath.Join(s.Dir, handover.FirmwareName))
	id, err := drivestate.Stat(fw)
	if err != nil || id.Size != s.Record.Files[0].Size {
		r.say("usb: the sealed firmware %s: %v (size %d, the record %d): asked again", fw, err, id.Size, s.Record.Files[0].Size)
		r.usbWant("firmware")
		return
	}
	r.st.USBFW, r.st.Ident, r.st.USBKey, r.st.USBBundle = fw, id.String(), "", ""
	key := false
	if len(s.Record.Files) == 2 {
		r.st.USBKey, key = r.path(filepath.Join(s.Dir, handover.SSHKeyName)), true
	}
	r.say("usb: firmware sealed: %s, %d bytes, sha256 %s, mode %s, ssh key %v; %s", fw, id.Size, s.Record.Files[0].SHA256, s.Record.Mode, key, id)
	r.M.USBFirmwareSealed(flow.USBFirmware{Mode: s.Record.Mode, Size: id.Size, Ident: id.String(), Key: key})
	r.Do(r.M.Take())
}

// usbBundle gives the flow the sealed bundle (its manifest read strictly; the
// plan child checks the whole bundle, pass 1).
func (r *Runner) usbBundle(s *handover.InstSealed) {
	dir := r.path(s.Dir)
	res := mlReadDir(dir)
	if res.OK {
		r.st.USBBundle, res.Ident = dir, "folder "+dir
		r.say("usb: bundle sealed: %s: version %s, token %s, kernel %s, slot chain %s %s; manifest sha256 %s; %d bytes", dir, res.Version,
			res.Token, res.Kernel, res.ChainVersion, res.ChainBuild, res.ManifestSHA256, res.Size)
		res.StashDone = r.stashThere()
	} else {
		r.say("usb: the sealed bundle %s refused: %s", dir, res.Refusal)
	}
	r.M.USBBundleSealed(res)
	if res.OK {
		r.say("mainline: the path: %s", map[bool]string{true: "direct (slot A keeps the Crestron firmware, slot B gets mainline; --direct)",
			false: "userdebug-first (slot B keeps Android userdebug, slot A gets mainline)"}[r.M.MainlineDirect()])
	}
	r.Do(r.M.Take())
}

// mlReadDir reads the manifest of a sealed bundle folder as mlReadDrive
// reads the drive's (strict parser, its sha256).
func mlReadDir(dir string) flow.MainlineDrive {
	b, err := handover.ReadSmall(filepath.Join(dir, mainline.ManifestFile), mainline.MaxManifest)
	if err != nil {
		return flow.MainlineDrive{Refusal: "the sealed bundle: " + err.Error()}
	}
	m, err := mainline.ParseManifest(b)
	if err != nil {
		return flow.MainlineDrive{Refusal: "the sealed bundle is not a mainline build this installer can read: " + err.Error()}
	}
	rb := m.RequiresBootchain
	return flow.MainlineDrive{OK: true, Size: m.BootTemplate.Size + m.Rootfs.Size, Version: m.Version, Token: m.Token,
		Kernel: m.Kernel.Rev, ChainVersion: rb.Version, ChainBuild: rb.BuildType, ManifestSHA256: hexSHA(b), USB: true}
}

func hexSHA(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// usbWant writes a request to the service: "SEQ WANT".
func (r *Runner) usbWant(w string) {
	if r.usb == nil {
		return
	}
	r.usb.want++
	p := r.path(filepath.Join(handover.InstRunDir, handover.InstWantFile))
	if err := writeRunFile(r.path(RunDir), p, []byte(fmt.Sprintf("%d %s\n", r.usb.want, w)), 0o644); err != nil {
		r.say("usb: request %s: %v", w, err)
		return
	}
	switch w {
	case handover.WantFirmware:
		r.st.USBFW, r.st.USBKey, r.st.USBBundle = "", "", ""
		r.usb.delivered = ""
	case handover.WantBundle:
		r.st.USBFW = "" // the service removes firmware.bin for the bundle (review I-6 MINOR-3)
	}
	r.say("usb: request %d: %s", r.usb.want, w)
}

// usbInfo writes the info file for the page (HELLO/STATUS): the phase, the
// plan id, the model and the token; nothing secret.
func (r *Runner) usbInfo() {
	if r.usb == nil {
		return
	}
	in := handover.Info{Phase: r.M.Phase(), Model: "UC-P10"}
	if r.report != nil {
		in.Model, in.Token = r.report.Model, r.report.ImageToken
	}
	if r.M.Plan != nil && len(r.M.Plan.ID) >= 12 {
		in.PlanID = r.M.Plan.ID[:12]
	}
	in.Unlock = r.unlockWord()
	b, _ := json.Marshal(in)
	if string(b) == r.usb.info {
		return
	}
	r.usb.info = string(b)
	if err := writeRunFile(r.path(RunDir), r.path(filepath.Join(handover.InstRunDir, handover.InstInfoFile)), append(b, '\n'), 0o644); err != nil {
		r.say("usb: the info file: %v", err)
	}
}

// usbSource reports a plan whose files came over the USB cable: an official
// plan of the sealed firmware.bin, or a mainline plan of the sealed bundle.
func (r *Runner) usbSource(p *plan.Plan) bool {
	if p != nil && (p.Kind == plan.MainlineKind || p.Kind == plan.MainlineDirectKind) {
		return r.st.USBBundle != ""
	}
	return r.st.USBFW != ""
}

// ramBundle is the bundle folder in RAM that the mainline children take
// (--mainline DIR): the sealed USB bundle, or the unpacked compressed drive
// bundle (unpack.go); "" for the drive in place.
func (r *Runner) ramBundle() string {
	if r.st.USBBundle != "" {
		return r.st.USBBundle
	}
	return r.st.DriveBundle
}

// sourceRule is the rule of the files before the install child starts and
// after it ended: the drive rule (detached, the file unchanged), or for the
// sealed USB files: the firmware.bin has the identity it had when it was
// handed over (the child's --fw-expect checks it again at every step), and a
// sealed bundle is bound by the child itself (--mainline: its three files'
// identities, pass 1 again).
func (r *Runner) sourceRule(p *plan.Plan) error {
	switch {
	case p != nil && (p.Kind == plan.MainlineKind || p.Kind == plan.MainlineDirectKind) && r.ramBundle() != "":
		_, err := os.Lstat(filepath.Join(r.ramBundle(), mainline.RootfsFile))
		return err
	case (p == nil || p.Kind != plan.MainlineKind && p.Kind != plan.MainlineDirectKind) && r.st.USBFW != "":
		want, err := drivestate.Parse(r.st.Ident)
		if err != nil {
			return err
		}
		got, err := drivestate.Stat(r.st.USBFW)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("the sealed firmware file changed: it is %s, it was %s", got, want)
		}
		return nil
	}
	want, err := drivestate.Parse(r.st.Ident)
	if err != nil {
		return err
	}
	return drivestate.Detached(r.lun(), r.drive(), want)
}
