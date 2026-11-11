//go:build install

package devmode

// The first drive phase of a mainline install (MAINLINE-INT-2026-11-10.md
// tasks 2 and 3): `uc-pxx-inst mainline-stash --device /dev/mmcblk0 (--fw-drive
// DRIVE.img | --fw FILE) [--keys FILE]`. It reads the reviewed 1.0.0.94
// userdebug firmware (.bin) as the official device mode does, and writes
// nothing to the eMMC: only the RAM stash (package mlstash) under /run.
//
//  1. open (the official checks before any plan: the RAM image, the eMMC
//     and the model, the A/B lock; the drive rule of drive.go with
//     --fw-drive: the drive detached and unchanged to the end).
//  2. The key, as the official device mode finds it (keyFor: the key file,
//     the updater libraries of system_a and system_b, the key record).
//  3. Pass 1 over the whole firmware (plan.VerifyOfficial: listed in a
//     reviewed manifest that this release opens, every segment and the AVB
//     chain checked). The firmware must be a userdebug build with a
//     userdebug U-Boot (a user U-Boot forces locked: no mainline).
//  4. The five boot-chain parts from the firmware: uboot, trust, dtbo and
//     vbmeta (each must be the manifest's image: size and sha256) and the
//     resource of its boot image (the manifest's boot resource).
//  5. The key record of the key (keykeep.Encode) and the systems that hold
//     the key now (read only).
//  6. mlstash.Write: the stash of this boot (boot id, RAM image token).
//
// The record bytes never go to the output, a log or a plan.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/keykeep"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/manifest"
	"uc-pxx-installer/internal/mlstash"
	"uc-pxx-installer/internal/pcdfw"
	"uc-pxx-installer/internal/plan"
)

// StashKeySource is the stash's key source when the systems held the key.
const StashKeySource = "updater libraries in system_%s"

// MainlineStash runs the first drive phase. o is the official options: the
// device, the firmware (--fw or --fw-drive), and --keys or --lib. It returns
// the stash it wrote.
func MainlineStash(e *device.Env, o Options, out io.Writer) (*mlstash.Loaded, error) {
	if o.Keeper != "" || o.PlanFile != "" || o.Yes {
		return nil, &device.Refusal{Why: "the mainline stash takes only the device, the firmware and its key (no keeper, no plan, no --yes)"}
	}
	s, err := open(e, o, out)
	if err != nil {
		return nil, err
	}
	defer s.close()
	dir := e.Path(mlstash.Dir)
	ms, err := e.Manifests()
	if err != nil {
		return nil, err
	}
	fw, fwSize, ident, err := s.openFW()
	if err != nil {
		return nil, err
	}
	view := s.d.View()
	defer view.Close()
	say := s.sayer(false)
	k, keySlots, keySource, err := s.keyFor(view, fw, fwSize, say)
	if err != nil {
		return nil, err
	}
	l, err := BuildStash(StashInput{FW: fw, FWSize: fwSize, Ident: ident, Key: k, KeySlots: keySlots, KeySource: keySource,
		Manifests: ms, Disk: view, DiskBytes: s.d.Bytes(), BootID: s.bootID, RAMToken: s.token, Dir: dir,
		IdentNow: s.fwIdentNow}, say)
	if err != nil {
		return nil, err
	}
	s.say("stash: %s (index sha256 %s): the boot chain of %s (uboot, trust, dtbo, vbmeta, resource) and the firmware key's record (sha256 %s…; key from %s); valid in this boot only",
		dir, l.IndexSHA256[:16], l.Firmware.Name, l.Key.RecordSHA256[:16], l.Key.Source)
	for _, f := range l.Files {
		s.say("  %-8s %10d bytes sha256 %s", f.Part, f.Size, f.SHA256[:16])
	}
	return l, nil
}

// StashInput is what BuildStash needs: the opened firmware and its identity
// now (IdentNow, required: it is called after pass 1 and after the parts), the key in use and where
// it was found, the reviewed manifests, the disk (read only: the systems that
// hold the key), this boot and the folder.
type StashInput struct {
	FW        io.ReaderAt
	FWSize    int64
	Ident     string
	IdentNow  func() (string, error)
	Key       pcdfw.Keys
	KeySlots  []string
	KeySource string
	Manifests []*manifest.Manifest
	Disk      io.ReaderAt
	DiskBytes int64
	BootID    string
	RAMToken  string
	Dir       string
}

// BuildStash is steps 3 to 6 of MainlineStash (also the dry run's).
func BuildStash(in StashInput, say func(string)) (*mlstash.Loaded, error) {
	fw, fwSize, ident, k, ms := in.FW, in.FWSize, in.Ident, in.Key, in.Manifests
	keySlots, keySource := in.KeySlots, in.KeySource
	say("pass 1: check the whole firmware against the manifests (nothing is written)")
	pl, err := plan.VerifyOfficial(fw, fwSize, k, ms, func(l string) { say("  " + l) })
	if err != nil {
		return nil, &device.Refusal{Why: "pass 1: " + err.Error()}
	}
	if now, err := in.IdentNow(); err != nil || now != ident {
		return nil, &device.Refusal{Why: fmt.Sprintf("the firmware changed while pass 1 read it: %s, then %s (%v)", ident, now, err)}
	}
	m := manifest.ByFirmwareSHA(ms, pl.FirmwareSHA256)
	if m == nil || pl.BuildType != mainline.BuildType || pl.BootChain.UBootBuild != mainline.BuildType {
		return nil, &device.Refusal{Why: fmt.Sprintf("the firmware %s is a %s build with a %s U-Boot: a mainline install needs the userdebug boot chain (a user U-Boot forces locked)",
			pl.Version, pl.BuildType, pl.BootChain.UBootBuild)}
	}
	parts := map[string][]byte{}
	var boot []byte
	for _, seg := range pl.Segments() {
		name := seg.Name()
		switch name {
		case "uboot", "trust", "dtbo", "vbmeta", "boot":
		default:
			continue
		}
		var buf bytes.Buffer
		if _, err := pl.File().WriteSegment(seg, &buf); err != nil {
			return nil, &device.Refusal{Why: fmt.Sprintf("the %s image of the firmware: %v", name, err)}
		}
		b := buf.Bytes()
		if name == "boot" {
			boot = b
			continue
		}
		mp, ok := m.Part(name)
		if !ok || int64(len(b)) != mp.Size || sha256Hex(b) != mp.SHA256 {
			return nil, &device.Refusal{Why: fmt.Sprintf("the %s image of the firmware (%d bytes, sha256 %s) is not the manifest's", name, len(b), sha256Hex(b))}
		}
		parts[name] = b
	}
	if boot == nil {
		return nil, &device.Refusal{Why: "the firmware has no boot image"}
	}
	res, err := mainline.ReadResource(bytes.NewReader(boot), 0, int64(len(boot)))
	if err != nil || sha256Hex(res) != pl.BootResource {
		return nil, &device.Refusal{Why: fmt.Sprintf("the resource of the firmware's boot image: %v (sha256 %s, the payload's %s)", err, sha256Hex(res), pl.BootResource)}
	}
	parts["resource"] = res
	for _, p := range mlstash.Parts {
		if parts[p] == nil {
			return nil, &device.Refusal{Why: "the firmware has no " + p + " image"}
		}
	}
	if fresh, err := in.IdentNow(); err != nil || fresh != ident {
		return nil, &device.Refusal{Why: fmt.Sprintf("the firmware changed while it was read: %s, then %s (%v)", ident, fresh, err)}
	}
	rec := keykeep.Encode(k)
	copies := SystemCopies(in.Disk, in.DiskBytes, fw, fwSize, k)
	if keySource == "" {
		keySource = fmt.Sprintf(StashKeySource, joinSlots(keySlots))
	}
	st := mlstash.Stash{BootID: in.BootID, RAMToken: in.RAMToken,
		Firmware: mlstash.Firmware{File: m.Firmware.File, Name: m.Name(), Size: fwSize, SHA256: pl.FirmwareSHA256, Version: pl.Version,
			BuildType: pl.BuildType, UBootBuild: pl.BootChain.UBootBuild, Ident: ident},
		Key: mlstash.Key{Source: keySource, SystemCopies: append([]string{}, copies...)}}
	l, err := mlstash.Write(in.Dir, st, parts, rec)
	if err != nil {
		return nil, &device.Refusal{Why: "the stash: " + err.Error()}
	}
	return l, nil
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func joinSlots(l []string) string {
	out := ""
	for i, s := range l {
		if i > 0 {
			out += " and system_"
		}
		out += s
	}
	return out
}

// LoadStash reads the stash of this boot (mlstash.Load), or nil with the
// reason. A stash of another boot or another RAM image token, or of a
// firmware that is not a userdebug build with a userdebug U-Boot, is
// refused (review T2 MINOR-4).
func LoadStash(e *device.Env, bootID, token string) (*mlstash.Loaded, string) {
	l, err := mlstash.Load(e.Path(mlstash.Dir))
	if err != nil {
		return nil, err.Error()
	}
	f := l.Firmware
	switch {
	case l.BootID != bootID:
		return nil, fmt.Sprintf("the stash is of boot %s, this boot is %s", l.BootID, bootID)
	case l.RAMToken != token:
		return nil, fmt.Sprintf("the stash is of the RAM image %s, this one is %s", l.RAMToken, token)
	case f.BuildType != mainline.BuildType || f.UBootBuild != mainline.BuildType || len(f.SHA256) != 64:
		return nil, fmt.Sprintf("the stash's firmware %s is a %s build with a %s U-Boot", f.Name, f.BuildType, f.UBootBuild)
	}
	// review T3 NIT: the stash's firmware is a firmware of the manifests that this release opens
	ms, err := e.Manifests()
	if err != nil {
		return nil, "manifests: " + err.Error()
	}
	if m := manifest.ByFirmwareSHA(ms, f.SHA256); m == nil || plan.Gate(m) != "" {
		return nil, fmt.Sprintf("the stash's firmware %s (sha256 %s) is not a firmware this release opens", f.Name, f.SHA256)
	}
	return l, ""
}

// keptBoth is the key record when both record sectors of backup hold the
// same valid record (read only).
func keptBoth(r io.ReaderAt, size int64) ([]byte, bool) {
	recs, _, err := KeptRecords(r, size)
	if err != nil {
		return nil, false
	}
	if _, e0 := keykeep.Decode(recs[0]); e0 != nil || !bytes.Equal(recs[0], recs[1]) {
		return nil, false
	}
	return recs[0], true
}

// StashProof is the plan's name for the key's proof.
func StashProof(l *mlstash.Loaded) string {
	return fmt.Sprintf("the firmware %s (sha256 %s) decrypted with this key and passed pass 1; stash index sha256 %s", l.Firmware.Name,
		l.Firmware.SHA256, l.IndexSHA256)
}
