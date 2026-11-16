package flow

// Step 1 over the USB cable (mainline-rc-10, I-6; owner 2026-11-15): the
// installer page sends the firmware .bin (and for mainline the person's ssh
// key, then the bundle) to the RAM installer's step-1 USB service
// (`uc-pxx-xfer inst`), which seals each part into a root-only RAM folder. The
// runner reads the service's state and gives the flow the same events as a
// drive read: a sealed firmware of mode official is an official firmware
// drive (DriveRead), one of mode mainline is the drive of step 1 (the stash),
// a sealed bundle is the bundle drive (MainlineDriveRead). Everything after it
// is the drive flow unchanged: the same children with the sealed files in
// place of the drive (--fw FILE --fw-expect ID, --mainline DIR), the same
// checks, the same screens.
//
// What changes on the unit's screen: the firmware screen waits for the
// installer page (usbWaitView); Vol- asks for the drive instead (ActUSB
// "drive": the service makes the gadget again with the drive, for the rest
// of the boot, and the drive screens follow); where the drive flow gives the
// drive back, the USB flow asks the service for the next files (ActUSB
// "firmware": start again, or "bundle" after the stash).

import (
	"fmt"
	"strings"
	"time"
)

// usbState is the USB side of the model.
type usbState struct {
	drive bool        // the drive mode (the page's DRIVE or Vol-): the drive flow from now on
	recv  USBProgress // the receiver's progress, while it receives
}

// USBProgress is the receiver's state while a file arrives.
type USBProgress struct {
	File         string
	Bytes, Total int64
}

// USBFirmware is a sealed firmware stage of the USB service.
type USBFirmware struct {
	Mode  string // "official" or "mainline"
	Size  int64
	Ident string // the identity of the sealed firmware.bin (--fw-expect)
	Key   bool   // SSH-KEY.PUB came with it (mainline only)
}

// SetUSB: the image has the step-1 USB service (the runner found it).
func (m *Model) SetUSB() {
	if m.usb == nil {
		m.usb = &usbState{}
	}
}

// USBOn reports the image's step-1 USB service.
func (m *Model) USBOn() bool { return m.usb != nil }

// USBDriveMode reports the drive mode of the USB service.
func (m *Model) USBDriveMode() bool { return m.usb != nil && m.usb.drive }

// usbActive: the files come over the USB cable (the service is there and
// not in the drive mode).
func (m *Model) usbActive() bool { return m.usb != nil && !m.usb.drive }

// USBDrive: the service is in the drive mode now (it made the gadget again
// with the drive UC-PXX-INST): the drive flow from now on.
func (m *Model) USBDrive() {
	if m.usb == nil || m.usb.drive {
		return
	}
	m.usb.drive = true
	m.usb.recv = USBProgress{}
	if m.Screen == ScreenFirmware {
		m.Msg = txtUSBDriveOn
	}
}

// USBWaiting is what the flow waits for over the cable now: "firmware",
// "bundle" (the stash is done) or "" (nothing: another screen, a child
// runs, the drive mode).
func (m *Model) USBWaiting() string {
	switch {
	case !m.usbActive() || m.Screen != ScreenFirmware || m.running != "" || m.reading:
		return ""
	case m.ml != nil && m.ml.swap:
		return "bundle"
	case m.Drive == nil && m.ml == nil:
		return "firmware"
	}
	return ""
}

// USBReceiving gives the receiver's progress (the firmware screen shows it).
func (m *Model) USBReceiving(p USBProgress) {
	if m.usb != nil {
		m.usb.recv = p
	}
}

// USBFirmwareSealed: the service sealed a firmware stage while the flow
// waits for it. Official: the firmware read of the official flow (the panel
// question, the plan). Mainline: step 1, the stash child.
func (m *Model) USBFirmwareSealed(f USBFirmware) {
	if m.USBWaiting() != "firmware" {
		return
	}
	m.usb.recv = USBProgress{}
	m.refused, m.refusedDetail = "", ""
	if f.Mode == "mainline" {
		m.stashDrive(MainlineDrive{OK: true, Stash: true, FWName: `"firmware.bin"`, FWSize: f.Size, Ident: f.Ident, USB: true, KeyFile: false})
		return
	}
	m.ml = nil
	m.DriveRead(DriveResult{OK: true, Name: `"firmware.bin"`, Size: f.Size, Ident: f.Ident, USB: true})
}

// USBBundleSealed: the service sealed the bundle (after the stash): the
// bundle read of the drive flow (MainlineDriveRead).
func (m *Model) USBBundleSealed(d MainlineDrive) {
	if m.USBWaiting() != "bundle" {
		return
	}
	m.usb.recv = USBProgress{}
	d.USB = true
	m.MainlineDriveRead(d)
}

// usbGiveBack is the USB form of a give-back of the drive: after the stash
// (the swap view) the bundle stage; else the firmware stage again (the
// sealed files are removed: the page sends them again).
func (m *Model) usbGiveBack(a Action) Action {
	if a.Remake && m.ml != nil && m.ml.swap {
		return Action{Kind: ActUSB, Want: "bundle"}
	}
	return Action{Kind: ActUSB, Want: "firmware"}
}

// keyUSBWait: the firmware screen while the files come over the cable.
// Vol- (Prev) asks for the drive; Back cancels (nothing written).
func (m *Model) keyUSBWait(k Key) {
	switch k {
	case KeyBack:
		m.cancel()
	case KeyPrev:
		if m.running == "" && !m.reading {
			m.ask(Action{Kind: ActUSB, Want: "drive"})
			m.Msg = txtUSBDriveAsk
		}
	case KeyOK:
		m.Msg = txtUSBOK
	}
}

const (
	txtUSBTitle      = "Waiting for the installer page"
	txtUSBCable      = "Keep the USB-C cable connected to the computer."
	txtUSBPage       = "The installer page sends the files to the unit."
	txtUSBDriveKey   = "Vol- = use the drive instead."
	txtUSBPart1      = "Step 1 is done. Waiting for mainline Linux, part 1, from the installer page."
	txtUSBDriveAsk   = "Making the drive UC-PXX-INST ..."
	txtUSBDriveOn    = "The drive UC-PXX-INST is on the computer now."
	txtUSBOK         = "The installer page sends the files. Vol- = use the drive instead."
	txtUSBReceiveFW  = "Receiving the firmware file: %d of %d MB"
	txtUSBReceiveKey = "Receiving the SSH key"
	txtUSBReceiveML  = "Receiving mainline Linux, part 1: %d of %d MB"
)

// usbWaitView is the firmware screen while the files come over the cable.
func (m *Model) usbWaitView(f *Frame, add func(level int, format string, a ...any), now time.Time) {
	f.Title += "  -  " + txtUSBTitle
	if m.ml != nil && m.ml.swap {
		add(levelNormal, "%s", txtUSBPart1)
	} else {
		add(levelNormal, "%s", txtUSBPage)
	}
	add(levelNormal, "%s", txtUSBCable)
	add(levelNormal, "%s", txtUSBDriveKey)
	add(levelNormal, "")
	switch r := m.usb.recv; {
	case m.refused != "":
		add(levelBad, "The files were refused: %s", m.refused)
		add(levelNormal, "The installer page can send them again.")
	case r.File == "firmware.bin" && r.Total > 0:
		add(levelNormal, txtUSBReceiveFW, r.Bytes/1e6, r.Total/1e6)
	case r.File == "SSH-KEY.PUB":
		add(levelNormal, "%s", txtUSBReceiveKey)
	case r.File != "" && r.Total > 0:
		add(levelNormal, txtUSBReceiveML, r.Bytes/1e6, r.Total/1e6)
	default:
		add(levelNormal, "")
	}
	m.setKeys(f, keys{back: "Cancel", prev: "Drive"})
}

// PhaseWords is the closed set of the words of Phase (the "Page interface"
// of MAINLINE-RC10-2026-11-15.md): the page maps each of them, and treats
// any other word as unknown (it waits).
var PhaseWords = []string{
	// a child runs
	"stash", "check", "install", "switch-running", "repair-running",
	// the files screen: the cable stages, and the drive phases of the drive mode
	"waiting-firmware", "waiting-bundle", "drive: waiting-firmware", "drive: waiting-bundle", "drive: reading",
	// the other screens
	"status", "canceled", "question", "planning", "plan", "progress", "done", "stopped", "hold", "refused",
	"log", "restart", "repair", "safe", "switch", "switching", "switch-confirm",
}

// Phase is where the flow is, for the installer page (the service's info
// file, HELLO/STATUS "info.phase"): a word of PhaseWords ("drive: …" for the
// drive phases of the drive mode).
func (m *Model) Phase() string {
	drive := m.usb != nil && m.usb.drive
	switch {
	case m.running == "plan" && m.MainlineStashing():
		return "stash"
	case m.running == "plan":
		return "check"
	case m.running == "install":
		return "install"
	case m.running == "switch" || m.running == "repair":
		// a child of the A/B sector runs (its dry run or its write): not the
		// menu screens of the same name (review I-6 MINOR-5)
		return m.running + "-running"
	case m.running != "":
		return m.running
	}
	switch m.Screen {
	case ScreenFirmware:
		swap := m.ml != nil && m.ml.swap
		switch {
		case drive && m.reading:
			return "drive: reading"
		case drive && swap:
			return "drive: waiting-bundle"
		case drive:
			return "drive: waiting-firmware"
		case swap:
			return "waiting-bundle"
		}
		return "waiting-firmware"
	case ScreenPlan:
		return "plan"
	case ScreenStatus:
		if strings.HasPrefix(m.Msg, "Canceled") {
			return "canceled"
		}
		return "status"
	case ScreenUnlock, ScreenPanel, ScreenTarget:
		return "question"
	}
	return fmt.Sprint(m.Screen)
}
