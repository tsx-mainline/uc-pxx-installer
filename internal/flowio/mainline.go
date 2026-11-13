package flowio

// The mainline install in the runner (MAINLINE-INSTALL-PLAN.md, phase 5; the
// flow side is internal/flow/mainline.go). It adds no write of the eMMC and
// no new kind of child: the children are the device mode, `uc-pxx-inst plan` and
// `uc-pxx-inst install --device /dev/mmcblk0 --fw-drive ...`, which choose the
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
//     (for a UI that is started again) and in result.txt of the log bundle;
//   - step 1 of the two drive phases (owner O1/O8): the read of a drive with
//     one firmware .bin in the top folder and the folder uc-pxx-mainline
//     without the bundle's files (readStepOne: the official read of the .bin
//     and of the key file p10-keys.txt), the stash child in place of the plan
//     child (planArgv: flow.MainlineStashArgs, `uc-pxx-inst plan
//     --mainline-stash`, a child of the plan's kind under the supervisor;
//     it writes nothing to the eMMC, only the RAM stash of this boot), and
//     its end (stashEnded, from planEnded) for the flow (StashEnded). The
//     runner looks only whether the stash folder is there (stashThere); it
//     never reads the stash (the key record stays with the device mode);
//   - the direct path: --direct for its plan and install child
//     (flow.MainlineDirectPlanArgs, flow.MainlineDirectInstallArgs), and the
//     read of a direct plan file (plan.DirectPlanOf).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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
	// Direct: the direct path (its unlock screen is a statement: the
	// installer reads lock_state itself; the yes is "go on", recorded only)
	Direct bool `json:"direct,omitempty"`
	// LockWrite: the person agreed on the plan screen to the lock-state
	// write of a locked unit (owner MAJOR-3); the install child got
	// plan.LockWriteFlag. Set when the install child starts.
	LockWrite *flow.LockWriteAgreement `json:"lockwrite,omitempty"`
}

const mlRunFile = FlowDir + "/mainline.json"

// mlStashDir is the stash folder of the device mode's first drive phase
// (mlstash.Dir; flowio does not import the stash package, a test holds the
// two equal).
const mlStashDir = RunDir + "/mlstash"

// mlStashRun marks the plan child that runs as the stash child of step 1.
type mlStashRun struct{}

// stashThere reports the stash folder of this boot (read only: the stash
// itself is the device mode's; the plan checks it).
func (r *Runner) stashThere() bool {
	_, err := os.Lstat(r.path(mlStashDir))
	return err == nil
}

// stashRuns reports a stash child of an earlier UI of this boot that still
// runs (its command line: uc-pxx-inst plan --mainline-stash ... --fw-drive
// DRIVE ..., with this runner's drive file).
func (r *Runner) stashRuns() bool {
	ents, _ := os.ReadDir(r.c.Proc)
	for _, e := range ents {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join(r.c.Proc, e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		a := strings.Split(string(b), "\x00")
		if len(a) < 3 || filepath.Base(a[0]) != filepath.Base(r.c.Exe) || a[1] != "plan" || a[2] != "--mainline-stash" {
			continue
		}
		for i := 3; i+1 < len(a); i++ {
			if a[i] == "--fw-drive" && a[i+1] == r.drive() {
				return true
			}
		}
	}
	return false
}

// stashWait is how long the read of a step-1 drive waits for the stash
// child of an earlier UI (a UI that died while it ran) to end.
const stashWait = 30 * time.Minute

// readStepOne reads the drive of step 1 as the official read reads a
// firmware drive: the one .bin (fatfs.OpenFirmware: a clean volume, exactly
// one .bin), and the key file p10-keys.txt if the drive has one (copied,
// 0600, for --keys; never shown, never in a bundle). It waits for a stash
// child of an earlier UI that still runs, so that two never run at once.
func (r *Runner) readStepOne() flow.MainlineDrive {
	f, err := fatfs.OpenFirmware(r.drive())
	if err != nil {
		res := flow.MainlineDrive{Refusal: err.Error()}
		var rf *fatfs.Refusal
		if errors.As(err, &rf) {
			res.Refusal = rf.Why
			res.Remake = rf.Reason == fatfs.Dirty || rf.Reason == fatfs.Damaged || rf.Reason == fatfs.NotFAT
		}
		return res
	}
	res := flow.MainlineDrive{OK: true, Stash: true, FWName: strconv.QuoteToASCII(f.Name()), FWSize: f.Size()}
	f.Close()
	if v, err := fatfs.OpenImage(r.drive()); err == nil {
		kb, kerr := v.ReadFile("/"+KeyFileName, 4096)
		v.Close()
		if kerr == nil {
			if err := writeRunFile(r.path(RunDir), r.keyFile(), kb, 0o600); err == nil {
				res.KeyFile = true
			}
		}
	}
	for t0 := r.c.Now(); r.stashRuns(); {
		if r.c.Now().Sub(t0) > stashWait {
			r.removeKeyFile()
			return flow.MainlineDrive{Refusal: "step 1 of an earlier screen still runs: wait, then eject the drive again"}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return res
}

// stepOne reports a drive of step 1: exactly one .bin in the top folder and
// the folder uc-pxx-mainline with none of the bundle's three files.
func stepOne(image string, top mldrive.TopFolder) bool {
	if !top.Bundle || len(top.Bins) != 1 {
		return false
	}
	fs, err := fatfs.OpenImage(image)
	if err != nil {
		return false
	}
	defer fs.Close()
	es, err := fs.ReadDir("/" + mainline.Dir)
	if err != nil {
		return false
	}
	for _, e := range es {
		for _, n := range []string{mainline.ManifestFile, mainline.TemplateFile, mainline.RootfsFile} {
			if strings.EqualFold(e.Name, n) {
				return false
			}
		}
	}
	return true
}

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
	r.removeKeyFile() // a mainline install takes no key: a key file on the drive is not copied (step 1: readStepOne copies it)
	var res flow.MainlineDrive
	if stepOne(r.drive(), top) {
		res = r.readStepOne()
	} else {
		res = mlReadDrive(r.drive(), top)
	}
	id2, ierr2 := drivestate.Stat(r.drive())
	empty2, _, lerr := drivestate.LunEmpty(r.lun())
	if ierr2 != nil || id != id2 || lerr != nil || !empty2 {
		r.removeKeyFile()
		res = flow.MainlineDrive{Refusal: "the drive changed while it was read: eject it again"}
	}
	if res.OK {
		res.Ident = id.String()
		res.StashDone = r.stashThere()
	}
	r.post(func() {
		switch {
		case res.OK && res.Stash:
			r.st.Ident = res.Ident
			r.say("drive: mainline step 1: firmware %s, %d bytes, and the folder %s without the bundle; the stash of this boot is there: %v; %s",
				res.FWName, res.FWSize, mainline.Dir, res.StashDone, res.Ident)
		case res.OK:
			r.st.Ident = res.Ident
			r.say("drive: mainline bundle %s: version %s, token %s, kernel %s, slot B must hold %s %s; manifest sha256 %s; %d bytes; %s",
				mainline.Dir, res.Version, res.Token, res.Kernel, res.ChainVersion, res.ChainBuild, res.ManifestSHA256, res.Size, res.Ident)
			r.say("mainline: the stash of step 1 is there: %v", res.StashDone)
		default:
			r.say("drive refused (mainline bundle): %s", res.Refusal)
		}
		r.M.MainlineDriveRead(res)
		if res.OK && !res.Stash {
			r.say("mainline: the path: %s", map[bool]string{true: "direct (slot A keeps the Crestron firmware, slot B gets mainline; --direct)",
				false: "userdebug-first (slot B keeps Android userdebug, slot A gets mainline)"}[r.M.MainlineDirect()])
		}
		r.Do(r.M.Take())
	})
	return true
}

// stashEnded is the end of the plan child when it was the stash child of
// step 1 (planEnded calls it first; false: not a stash child). It gives the
// flow the end: exit 0 with the stash folder there, a refusal (the RESULT
// line), or a cancel (SIGTERM, from cancelPlan only). The child wrote
// nothing to the eMMC whatever its end.
func (r *Runner) stashEnded(ch *child, end flow.End) bool {
	if r.mlst == nil {
		return r.mlHeldEnded(ch, end)
	}
	r.mlst = nil
	r.child, r.ending = nil, false
	r.st.PlanEnd = r.c.Now()
	res := flow.StashResult{End: end, OK: end.Exited && end.Code == 0 && r.stashThere()}
	if !res.OK {
		res.Refusal = lastResult(ch.output())
		res.Canceled = !end.Exited && end.Signal == "terminated"
	}
	r.say("stash child ended: %s; stash %v; %s", end, res.OK, res.Refusal)
	r.M.StashEnded(res)
	r.Do(r.M.Take())
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
		return refuse(true, "the file system of the UC-PXX-INST drive cannot be read (%v): copy the folder %s again", err, mainline.Dir)
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
	r.mlst = nil
	if r.M.MainlineStashing() {
		// step 1: the stash child is this plan child (stashEnded reads its end)
		r.mlst = &mlStashRun{}
		r.say("mainline step 1: the stash child reads the .bin (it writes nothing to the eMMC, only the RAM stash of this boot)")
		return flow.MainlineStashArgs(ps)
	}
	if !r.M.Mainline() {
		r.ml = nil
		removeRunFile(r.path(RunDir), r.path(mlRunFile))
		return flow.PlanArgs(ps, a.Keeper, r.st.Prereqs, a.Unclear, r.st.Panel)
	}
	r.ml = &mlRun{Unlock: r.M.UnlockConfirmed(), UnlockTerminal: r.M.UnlockFromTerminal(), Direct: r.M.MainlineDirect()}
	if r.ml.Direct {
		r.say("unlock: the lock-state screen: %s", r.ml.answer())
	} else {
		r.say("unlock: \"Is the unit unlocked?\": %s", r.ml.answer())
	}
	b, _ := json.MarshalIndent(r.ml, "", "  ")
	if err := writeRunFile(r.path(RunDir), r.path(mlRunFile), append(b, '\n'), 0o644); err != nil {
		r.say("mainline: %v", err)
	}
	if r.M.MainlineDirect() {
		return flow.MainlineDirectPlanArgs(ps, r.st.Prereqs, r.st.Panel, r.ml.Unlock)
	}
	return flow.MainlinePlanArgs(ps, r.st.Prereqs, r.st.Panel, r.ml.Unlock)
}

// installArgv is the command line of the install child (startInstall), by
// the kind of the plan that was shown: the official flow.InstallArgs, or
// flow.MainlineInstallArgs with the unlock answer of its plan child.
func (r *Runner) installArgv(ps flow.Paths, p *plan.Plan) []string {
	switch p.Kind {
	case plan.MainlineKind:
		return flow.MainlineInstallArgs(ps, r.st.Prereqs, r.st.Panel, r.ml != nil && r.ml.Unlock)
	case plan.MainlineDirectKind:
		if r.M.ReplaceConfirmed() {
			r.say("mainline: the person confirmed that slot B is replaced (%s)", plan.ReplaceFlag)
		}
		lw, lt := r.M.LockWriteConfirmed(), r.M.LockWriteFromTerminal()
		if lw {
			r.say("mainline: the person agreed to the lock-state write, lock_state 0x00 -> 0x01 (%s), from a terminal: %v, plan %s", plan.LockWriteFlag, lt, p.ID)
			if r.ml != nil {
				r.ml.LockWrite = &flow.LockWriteAgreement{Confirmed: true, FromTerminal: lt, Plan: p.ID}
				b, _ := json.MarshalIndent(r.ml, "", "  ")
				if err := writeRunFile(r.path(RunDir), r.path(mlRunFile), append(b, '\n'), 0o644); err != nil {
					r.say("mainline: %v", err)
				}
			}
		}
		return flow.MainlineDirectInstallArgs(ps, r.st.Prereqs, r.st.Panel, r.ml != nil && r.ml.Unlock, r.M.ReplaceConfirmed(), lw, lt)
	}
	return flow.InstallArgs(ps, r.st.Keeper, r.st.Prereqs, r.st.Unclear, r.st.Panel)
}

func (ml *mlRun) answer() string {
	switch {
	case ml == nil:
		return "not recorded"
	case ml.Direct && ml.Unlock && ml.UnlockTerminal:
		return "go on (the direct path: the installer reads lock_state itself), from a terminal, after its warning"
	case ml.Direct && ml.Unlock:
		return "go on (the direct path: the installer reads lock_state itself), on the panel"
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
	var k struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(b, &k) == nil && k.Kind == plan.MainlineDirectKind {
		dp, err := plan.DirectPlanOf(b) // the direct path: its own id check
		if err != nil {
			return nil, err
		}
		return &dp.Plan, nil
	}
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
	if p == nil || p.Kind != plan.MainlineKind && p.Kind != plan.MainlineDirectKind {
		return ""
	}
	var b strings.Builder
	if raw, err := os.ReadFile(r.st.PlanJSON); err == nil && p.Kind == plan.MainlineDirectKind {
		if dp, err := plan.DirectPlanOf(raw); err == nil && dp.Mainline != nil {
			m := dp.Mainline.Manifest
			fmt.Fprintf(&b, "mainline (the direct path): %s %s, token %s, kernel %s, manifest sha256 %s\n", m.Kind, m.Version, m.Token, m.Kernel.Rev,
				dp.Mainline.ManifestSHA256)
			fmt.Fprintf(&b, "slot %s keeps its Crestron firmware; slot %s gets mainline", dp.Keeper, dp.Target)
			if dp.Chain != nil {
				fmt.Fprintf(&b, " and the boot chain of %s %s (%s)", dp.Chain.BuildType, dp.Chain.Version, dp.Chain.Firmware)
			}
			b.WriteString("\n")
			if dp.Stash != nil {
				fmt.Fprintf(&b, "step 1 (the stash): %s, index sha256 %s\n", dp.Stash.FirmwareName, dp.Stash.IndexSHA256)
			}
			fmt.Fprintf(&b, "unlock in the plan: confirmed %v (%s), lock_state %s, basis: %s\n", dp.UnlockDirect.Confirmed, dp.UnlockDirect.Flag,
				dp.UnlockDirect.LockState, dp.UnlockDirect.Basis)
			if wr := dp.UnlockDirect.Write; wr != nil {
				fmt.Fprintf(&b, "the lock-state write in the plan: lock_state %s -> %s on %s, before any target write\n", wr.From, wr.To, wr.Security)
			}
		}
	} else if err == nil {
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
	if ml != nil && ml.Direct {
		fmt.Fprintf(&b, "the lock-state screen: %s\n", ml.answer())
		if a := ml.LockWrite; a != nil && a.Confirmed {
			fmt.Fprintf(&b, "the lock-state write (lock_state 0x00 -> 0x01): agreed on the plan screen (%s), from a terminal %v, plan %s\n", plan.LockWriteFlag, a.FromTerminal, a.Plan)
		} else {
			b.WriteString("the lock-state write: not agreed in this run (no lock-state write)\n")
		}
		return b.String()
	}
	fmt.Fprintf(&b, "\"Is the unit unlocked?\" (the TEE unlock tool read back UNLOCKED): %s\n", ml.answer())
	return b.String()
}

// mlHeldEnded: a mainline plan child (not the stash child) that ended with
// exit 3 is the device mode's STOP (the A/B state not known; the in-RAM
// lock_state reader saw a change, MAINLINE-RC2-LS review B-1): the flow's
// hold (flow.MainlineHeld), not a refusal with "try again". Every other end
// goes on to planEnded unchanged.
func (r *Runner) mlHeldEnded(ch *child, end flow.End) bool {
	if r.ml == nil || !end.Exited || end.Code != 3 {
		return false
	}
	r.child, r.ending = nil, false
	r.st.PlanEnd = r.c.Now()
	why := lastResult(ch.output())
	r.say("mainline plan child ended: %s: a STOP before any write (exit 3, the A/B state is not known): the hold; %s", end, why)
	r.M.MainlineHeld(why)
	r.Do(r.M.Take())
	return true
}
