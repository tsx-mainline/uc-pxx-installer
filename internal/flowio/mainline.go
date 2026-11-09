package flowio

// The mainline install in the runner (MAINLINE-INSTALL-PLAN.md, phase 5; the
// flow side is internal/flow/mainline.go). It adds no write of the eMMC and
// no new kind of child: the children are the device mode, `p10inst plan` and
// `p10inst install --device /dev/mmcblk0 --fw-drive ...`, which choose the
// mainline mode themselves from the drive (its top folder holds
// uc-pxx-mainline) or the plan file (its kind). What it adds:
//
//   - the read of a mainline drive (readMainline, the first part of every
//     drive read): the top folder, and the bundle's manifest read and checked
//     strictly, in place, read-only (the plan child checks the whole bundle,
//     pass 1); a drive with the folder and a .bin is refused, as the device
//     mode refuses it; every other drive read leaves the mainline mode first;
//   - the children's arguments (planArgv, installArgv): the official ones, or
//     flow.MainlinePlanArgs / flow.MainlineInstallArgs (--unlock-confirmed
//     after the yes of the unlock screen; no --keys, no --keeper);
//   - the read of a mainline plan file (mainlinePlanOf, from readPlan): its
//     own id check (plan.MainlinePlan.VerifyID); the flow gets the embedded
//     Plan (its Kind is plan.MainlineKind, its ID the mainline plan's);
//   - the unlock answer in the flow log, in mainline.json of the run folder
//     (for a UI that is started again) and in result.txt of the log bundle.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"uc-pxx-installer/internal/drivestate"
	"uc-pxx-installer/internal/fatfs"
	"uc-pxx-installer/internal/flow"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/mainline/mldrive"
	"uc-pxx-installer/internal/plan"
)

// mlRun is the unlock answer that the mainline plan child got (the install
// child gets the same, so the same plan id comes out). It is kept in
// FlowDir/mainline.json too. It holds no secret.
type mlRun struct {
	Unlock         bool `json:"unlock_confirmed"`
	UnlockTerminal bool `json:"unlock_from_terminal"`
}

const mlRunFile = FlowDir + "/mainline.json"

// readMainline is the first part of every drive read (readDrive). When the
// top folder of the ejected drive holds the folder uc-pxx-mainline, it reads
// the bundle's manifest and gives the result to the flow (MainlineDriveRead)
// and reports true. Otherwise it tells the flow to leave the mainline mode
// and reports false: the official read follows (also for a drive that is
// with the computer or has no readable FAT: that read says why).
func (r *Runner) readMainline() bool {
	leave := func() bool {
		r.post(func() { r.M.LeaveMainline() }) // before the official result of this read
		return false
	}
	if empty, _, err := drivestate.LunEmpty(r.lun()); err != nil || !empty {
		return leave()
	}
	id, ierr := drivestate.Stat(r.drive())
	top, err := mldrive.TopOfImage(r.drive())
	if ierr != nil || err != nil || !top.Bundle {
		return leave()
	}
	r.removeKeyFile() // a mainline install takes no key: a key file on the drive is not copied
	res := mlReadDrive(r.drive(), top)
	id2, ierr2 := drivestate.Stat(r.drive())
	empty2, _, lerr := drivestate.LunEmpty(r.lun())
	if ierr2 != nil || id != id2 || lerr != nil || !empty2 {
		res = flow.MainlineDrive{Refusal: "the drive changed while it was read: eject it again"}
	}
	if res.OK {
		res.Ident = id.String()
	}
	r.post(func() {
		if res.OK {
			r.st.Ident = res.Ident
			r.say("drive: mainline bundle %s: version %s, token %s, kernel %s, slot B must hold %s %s; manifest sha256 %s; %d bytes; %s",
				mainline.Dir, res.Version, res.Token, res.Kernel, res.ChainVersion, res.ChainBuild, res.ManifestSHA256, res.Size, res.Ident)
		} else {
			r.say("drive refused (mainline bundle): %s", res.Refusal)
		}
		r.M.MainlineDriveRead(res)
		r.Do(r.M.Take())
	})
	return true
}

// mlReadDrive reads the folder uc-pxx-mainline of the drive file: the
// volume must be clean and pass the read-only check (mldrive.DriveSource, as
// for a firmware file), the folder must hold manifest.json, and the
// manifest must pass the strict parser of payload contract v1
// (mainline.ParseManifest). The boot template and the root file system are
// not read here: the plan child hashes them (pass 1).
func mlReadDrive(image string, top mldrive.TopFolder) flow.MainlineDrive {
	refuse := func(remake bool, f string, a ...any) flow.MainlineDrive {
		return flow.MainlineDrive{Refusal: fmt.Sprintf(f, a...), Remake: remake}
	}
	if len(top.Bins) > 0 {
		return refuse(false, "it holds the folder %s and a firmware file (%s): leave only one of them there",
			mainline.Dir, strings.Join(top.Bins, ", "))
	}
	fs, err := fatfs.OpenImage(image)
	if err != nil {
		return refuse(true, "the file system of the P10-INSTALL drive cannot be read (%v): copy the folder %s again", err, mainline.Dir)
	}
	defer fs.Close()
	src, err := mldrive.DriveSource(fs)
	if err != nil {
		return refuse(true, "%v", err)
	}
	files, _, err := src.Names()
	if err != nil {
		return refuse(false, "%v", err)
	}
	name := ""
	for _, n := range files {
		if strings.EqualFold(n, mainline.ManifestFile) {
			name = n
		}
	}
	if name == "" {
		return refuse(false, "the folder %s has no %s: copy the whole folder again", mainline.Dir, mainline.ManifestFile)
	}
	f, c, err := src.Open(name)
	if err != nil {
		return refuse(false, "%s: %v", mainline.ManifestFile, err)
	}
	defer c.Close()
	if f.Size() <= 0 || f.Size() > mainline.MaxManifest {
		return refuse(false, "%s of the folder %s has %d bytes (1 to %d)", mainline.ManifestFile, mainline.Dir, f.Size(), mainline.MaxManifest)
	}
	b := make([]byte, f.Size())
	if _, err := io.ReadFull(io.NewSectionReader(f, 0, f.Size()), b); err != nil {
		return refuse(false, "%s: %v", mainline.ManifestFile, err)
	}
	m, err := mainline.ParseManifest(b)
	if err != nil {
		return refuse(false, "the folder %s is not a mainline build this installer can read (%v): copy the folder again", mainline.Dir, err)
	}
	sum := sha256.Sum256(b)
	rb := m.RequiresBootchain
	return flow.MainlineDrive{OK: true, Size: m.BootTemplate.Size + m.Rootfs.Size, Version: m.Version, Token: m.Token,
		Kernel: m.Kernel.Rev, ChainVersion: rb.Version, ChainBuild: rb.BuildType, ManifestSHA256: hex.EncodeToString(sum[:])}
}

// planArgv is the command line of the plan child (startPlan): the official
// flow.PlanArgs, or after the read of a mainline drive flow.MainlinePlanArgs
// with the unlock answer of the flow, which goes into the flow log and
// mainline.json.
func (r *Runner) planArgv(ps flow.Paths, a flow.Action) []string {
	if !r.M.Mainline() {
		r.ml = nil
		removeRunFile(r.path(RunDir), r.path(mlRunFile))
		return flow.PlanArgs(ps, a.Keeper, r.st.Prereqs, a.Unclear, r.st.Panel)
	}
	r.ml = &mlRun{Unlock: r.M.UnlockConfirmed(), UnlockTerminal: r.M.UnlockFromTerminal()}
	r.say("unlock: \"Is the unit unlocked?\": %s", r.ml.answer())
	b, _ := json.MarshalIndent(r.ml, "", "  ")
	if err := writeRunFile(r.path(RunDir), r.path(mlRunFile), append(b, '\n'), 0o644); err != nil {
		r.say("mainline: %v", err)
	}
	return flow.MainlinePlanArgs(ps, r.st.Prereqs, r.st.Panel, r.ml.Unlock)
}

// installArgv is the command line of the install child (startInstall), by
// the kind of the plan that was shown: the official flow.InstallArgs, or
// flow.MainlineInstallArgs with the unlock answer of its plan child.
func (r *Runner) installArgv(ps flow.Paths, p *plan.Plan) []string {
	if p.Kind != plan.MainlineKind {
		return flow.InstallArgs(ps, r.st.Keeper, r.st.Prereqs, r.st.Unclear, r.st.Panel)
	}
	return flow.MainlineInstallArgs(ps, r.st.Prereqs, r.st.Panel, r.ml != nil && r.ml.Unlock)
}

func (ml *mlRun) answer() string {
	switch {
	case ml == nil:
		return "not recorded"
	case ml.Unlock && ml.UnlockTerminal:
		return "yes, from a terminal, after its warning"
	case ml.Unlock:
		return "yes, on the panel"
	}
	return "no"
}

// mainlinePlanOf reads a plan file of the mainline kind (readPlan): its
// content must match its id (plan.MainlinePlan.VerifyID; the embedded Plan's
// own VerifyID fails by design). The flow gets the embedded Plan: its Kind
// is plan.MainlineKind and its ID the mainline plan's id.
func mainlinePlanOf(b []byte) (*plan.Plan, error) {
	mp, err := parseMainlinePlan(b)
	if err != nil {
		return nil, err
	}
	return &mp.Plan, nil
}

func parseMainlinePlan(b []byte) (*plan.MainlinePlan, error) {
	var mp plan.MainlinePlan
	if err := json.Unmarshal(b, &mp); err != nil {
		return nil, err
	}
	if mp.Kind != plan.MainlineKind || mp.ID == "" || !mp.VerifyID() {
		return nil, errors.New("the mainline plan does not match its id")
	}
	return &mp, nil
}

// mainlineResult is the part of result.txt (bundle) of a mainline install:
// the bundle of the plan, the unlock gate of the plan, and where the yes of
// the unlock screen came from. "" for an official plan.
func (r *Runner) mainlineResult(p *plan.Plan) string {
	if p == nil || p.Kind != plan.MainlineKind {
		return ""
	}
	var b strings.Builder
	if raw, err := os.ReadFile(r.st.PlanJSON); err == nil {
		if mp, err := parseMainlinePlan(raw); err == nil && mp.Mainline != nil {
			m := mp.Mainline.Manifest
			fmt.Fprintf(&b, "mainline: %s %s, token %s, kernel %s, manifest sha256 %s\n", m.Kind, m.Version, m.Token, m.Kernel.Rev,
				mp.Mainline.ManifestSHA256)
			if mp.Chain != nil {
				fmt.Fprintf(&b, "slot %s keeps Android %s %s (%s)\n", mp.Keeper, mp.Chain.BuildType, mp.Chain.Version, mp.Chain.Firmware)
			}
			fmt.Fprintf(&b, "unlock in the plan: confirmed %v (%s)\n", mp.Unlock.Confirmed, mp.Unlock.Flag)
		}
	}
	ml := r.ml
	if ml == nil { // a UI that was started again: the answer of the plan's UI
		if raw, err := os.ReadFile(r.path(mlRunFile)); err == nil {
			var x mlRun
			if json.Unmarshal(raw, &x) == nil {
				ml = &x
			}
		}
	}
	fmt.Fprintf(&b, "\"Is the unit unlocked?\" (the TEE unlock tool read back UNLOCKED): %s\n", ml.answer())
	return b.String()
}
