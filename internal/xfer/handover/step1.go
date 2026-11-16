package handover

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"uc-pxx-installer/internal/mainline"
)

// The step-1 hand-over of the RAM installer (mainline-rc-10, I-6): the
// receiver of kind step1 takes the firmware .bin (and, for mainline, the
// person's ssh key) in its firmware stage, and a mainline bundle in its
// bundle stage. A bundle stage's ready folder is a payload's (CheckReady,
// the step-1 gate's pins). A firmware stage's ready folder:
//
//	complete.json   the Record: kind RecordKindStep1FW, mode, firmware.bin first
//	complete/       firmware.bin, and SSH-KEY.PUB when the person gave a key
//
// The root side (`uc-pxx-xfer inst`) stops the receiver, checks the folder
// (CheckReadyStep1FW), and seals it into a root-only folder. Pass 1 of the
// device mode (the stash child, the official plan) is the authority over the
// firmware's content, as for a .bin of the drive.
const (
	RecordKindStep1FW = "step1-firmware"
	FirmwareName      = "firmware.bin"
	SSHKeyName        = "SSH-KEY.PUB" // sshkey.FileName
	ModeOfficial      = "official"
	ModeMainline      = "mainline"
	// MaxSSHKey is sshkey.MaxBytes (a test holds the two equal).
	MaxSSHKey = 8192
)

// FWPin is one firmware file the step-1 receiver takes: its exact size and
// sha256 (the installer's manifests, `uc-pxx-inst step1-pins`).
type FWPin struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// CheckReadyStep1FW checks a firmware stage's hand-over in dir: the record
// (kind, mode, the firmware first with a size and sha256 that one of pins
// names, the key only with mode mainline), and the ready folder with exactly
// the record's files (regular, one link, owned by uid, the record's sizes).
// uid < 0 skips the owner checks (tests only).
func CheckReadyStep1FW(dir string, uid int, pins []FWPin) (*Ready, error) {
	fi, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() || (uid >= 0 && !ownedBy(fi, uid)) {
		return nil, fmt.Errorf("%s: not the receiver's folder", dir)
	}
	b, err := ReadSmall(filepath.Join(dir, ReadyRecord), MaxStateFile)
	if err != nil {
		return nil, err
	}
	var r Record
	if err := DecodeStrict(b, &r); err != nil {
		return nil, fmt.Errorf("%s: %v", ReadyRecord, err)
	}
	switch {
	case r.Version != RecordVersion || r.Kind != RecordKindStep1FW || r.Auto:
		return nil, fmt.Errorf("%s: version %d kind %q auto %v: not a step-1 firmware stage", ReadyRecord, r.Version, r.Kind, r.Auto)
	case r.Mode != ModeOfficial && r.Mode != ModeMainline:
		return nil, fmt.Errorf("%s: mode %q (official or mainline)", ReadyRecord, r.Mode)
	case len(r.Files) < 1 || len(r.Files) > 2:
		return nil, fmt.Errorf("%s: %d files", ReadyRecord, len(r.Files))
	case r.Files[0].Name != FirmwareName || r.ManifestSHA256 != r.Files[0].SHA256:
		return nil, fmt.Errorf("%s: the first file is not %s with the record's sha256", ReadyRecord, FirmwareName)
	case len(r.Files) == 2 && (r.Files[1].Name != SSHKeyName || r.Mode != ModeMainline):
		return nil, fmt.Errorf("%s: a second file other than %s of a mainline step 1", ReadyRecord, SSHKeyName)
	case len(r.Files) == 2 && (r.Files[1].Size <= 0 || r.Files[1].Size > MaxSSHKey):
		return nil, fmt.Errorf("%s: %s of %d bytes", ReadyRecord, SSHKeyName, r.Files[1].Size)
	}
	ok := false
	for _, p := range pins {
		ok = ok || p.Size == r.Files[0].Size && p.SHA256 == r.Files[0].SHA256 && mainline.IsSHA256(p.SHA256)
	}
	if !ok {
		return nil, fmt.Errorf("%s: %s of %d bytes, sha256 %s is not a firmware of the installer", ReadyRecord, FirmwareName, r.Files[0].Size, r.Files[0].SHA256)
	}
	folder := filepath.Join(dir, ReadyDir)
	fi, err = os.Lstat(folder)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() || (uid >= 0 && !ownedBy(fi, uid)) {
		return nil, fmt.Errorf("%s: not a folder of the receiver", folder)
	}
	ents, err := os.ReadDir(folder)
	if err != nil {
		return nil, err
	}
	var names, want []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	for _, f := range r.Files {
		want = append(want, f.Name)
	}
	sort.Strings(names)
	sort.Strings(want)
	if fmt.Sprint(names) != fmt.Sprint(want) {
		return nil, fmt.Errorf("%s holds %v, not exactly %v", folder, names, want)
	}
	for _, f := range r.Files {
		p := filepath.Join(folder, f.Name)
		fi, err := os.Lstat(p)
		if err != nil {
			return nil, err
		}
		switch {
		case !fi.Mode().IsRegular():
			return nil, fmt.Errorf("%s: not a regular file", p)
		case nlink(fi) != 1:
			return nil, fmt.Errorf("%s: %d links", p, nlink(fi))
		case uid >= 0 && !ownedBy(fi, uid):
			return nil, fmt.Errorf("%s: not the receiver's file", p)
		case fi.Size() != f.Size:
			return nil, fmt.Errorf("%s: %d bytes, the record says %d", p, fi.Size(), f.Size)
		}
	}
	return &Ready{Folder: folder, Record: r}, nil
}

// The files of the step-1 USB service of the RAM installer (`uc-pxx-xfer
// inst`) and of the on-screen flow, in InstRunDir (root, 0755):
//
//	step1.json  the service's state and its sealed folders (InstResult), root-written
//	want        the flow's request: "SEQ WANT" (InstWant*), root-written
//	info.json   the flow's info for HELLO/STATUS (Info: phase, unlock, model, token), root-written
//
// The sealed folders are in InstStage (a tmpfs, root 0711): fw/ (firmware.bin,
// SSH-KEY.PUB) and bundle/ (manifest.json, boot-template.img, rootfs.ext4),
// root 0500 with files 0400. The receiver's own folder is InstStage/xfer.
const (
	InstRunDir     = "/run/uc-pxx-inst/xfer"
	InstStage      = "/run/uc-pxx-inst/xfer-stage"
	InstFFS        = "/run/uc-pxx-inst/xfer-ffs"
	InstResultFile = "step1.json"
	InstWantFile   = "want"
	InstInfoFile   = "info.json"
	InstFWDir      = "fw"
	InstBundleDir  = "bundle"
	// InstPinsFile: the pins of the service, written by `uc-pxx-inst
	// step1-pins` (root, at the start of the RAM image, from the installer's
	// manifests and its step-1 gate).
	InstPinsFile = "/run/uc-pxx-inst/step1-pins.json"
	// InstStageSize is the size of InstStage: the firmware (973 MB) or the
	// bundle (about 1.2 GB unpacked) with room.
	InstStageSize = 1536 << 20
)

// The requests of the flow (the WANT of the want file).
const (
	WantFirmware = "firmware" // the firmware stage again (the sealed folders are removed)
	WantBundle   = "bundle"   // after the stash: the bundle stage (firmware.bin is removed first)
	WantDrive    = "drive"    // the drive mode (for the rest of the boot)
	WantBusy     = "busy"     // nothing is received
)

// The service's stages (InstResult.Stage) and modes.
const (
	InstStageFirmware = "firmware"
	InstStageBundle   = "bundle"
	InstStageBusy     = "busy"
	InstStageDrive    = "drive"
	InstModeUSB       = "usb"
	InstModeDrive     = "drive"
	// InstModeFailed: the service could not start (the pins, the stage, the
	// gadget): the install image's rcS starts the drive gadget (uc-pxx-inst
	// usbd) in its place, and the flow shows the drive (RC-10 review I-6
	// MAJOR-2: the drive stays the fallback)
	InstModeFailed = "failed"
)

// InstPins is the pins file: the firmware files of the firmware stage and
// the manifest sha256s of the bundle stage.
type InstPins struct {
	FW     []FWPin  `json:"fw"`
	Bundle []string `json:"bundle"`
}

// InstSealed is one sealed folder: where, the record it was checked by.
type InstSealed struct {
	Dir    string `json:"dir"`
	Record Record `json:"record"`
}

// InstResult is step1.json: the service's state for the flow. Seq grows with
// every write; WantSeq is the last request the service acted on.
type InstResult struct {
	Seq      int         `json:"seq"`
	Mode     string      `json:"mode"`
	Stage    string      `json:"stage"`
	WantSeq  int         `json:"want_seq"`
	Firmware *InstSealed `json:"firmware,omitempty"`
	Bundle   *InstSealed `json:"bundle,omitempty"`
	Error    string      `json:"error,omitempty"`
}

// ReadInstResult reads step1.json of dir (root's own file, no link).
func ReadInstResult(dir string) (*InstResult, error) {
	b, err := ReadSmall(filepath.Join(dir, InstResultFile), MaxStateFile)
	if err != nil {
		return nil, err
	}
	var r InstResult
	if err := DecodeStrict(b, &r); err != nil {
		return nil, fmt.Errorf("%s: %v", InstResultFile, err)
	}
	return &r, nil
}
