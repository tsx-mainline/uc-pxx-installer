//go:build install

package devmode

// The mainline device mode (MAINLINE-INSTALL-PLAN.md, phase 4): `p10inst
// plan|install --device /dev/mmcblk0` with a mainline bundle (the folder
// uc-pxx-mainline/ of the P10-INSTALL drive, or --mainline DIR) in place of
// an official firmware. It is the order of Install, step for step, with the
// mainline planner (plan.ComputeMainline) and executor (install.RunMainline):
//
//  1. CheckRAMImage, OpenEMMC (the model gate), the A/B lock: open, unchanged.
//  2. The bundle: pass 1 (mainline.Verify: the manifest, the template and the
//     root file system hashed in place) in the plan run, or in an install
//     without --plan. On the drive the drive rule of drive.go holds (the lun
//     empty, the drive file of one identity), and the drive must not also
//     hold a firmware .bin. A folder is bound by the identity of its three
//     files.
//  3. The plan, printed with its id (kind p10inst-mainline-plan); refused
//     plans end here. The unlock confirmation (--unlock-confirmed) is a check
//     of the plan.
//  4. The confirmation: --yes, or the word INSTALL.
//  5. The re-check: the plan computed again from the unit; the bundle is not
//     hashed again (mainline.Reopen: the same identity, the same manifest and
//     template; RunMainline hashes the root file system as it writes it).
//     The before-hashes of the always-written slot A parts come from the
//     plan (plan.MainlineKnownBeforeOf); the GPT, the A/B sector, bootctl
//     status, the auto-update state, both vbmeta, the keeper's boot chain and
//     all eight keeper partitions are read again. Its id must be the plan's.
//  6. install.RunMainline: disable A, the six writes of slot A (each hashed
//     as written), sync, verify, arm, check-ab, the keeper check.
//
// Nothing is written before step 6. The official functions of this package
// (open, input, the drive guard, ExitCode) are called, never changed.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/drivestate"
	"uc-pxx-installer/internal/fatfs"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/mainline/mldrive"
	"uc-pxx-installer/internal/mlstash"
	"uc-pxx-installer/internal/plan"
)

// MainlineVersion is the tool version that goes into a mainline plan.
const MainlineVersion = "p10inst device mode, mainline 2026-11-09"

// MainlineOptions are the options of the mainline device mode: the options
// of the device mode (FW, Keys, Libs and FWSource must be empty; Keeper is
// "" or "b"), the bundle and the unlock confirmation.
type MainlineOptions struct {
	Options
	// Mainline is a bundle folder (--mainline DIR) in place of the drive
	// (Options.FWDrive). Exactly one of them is given.
	Mainline string
	// UnlockConfirmed: the user confirmed that the unit is unlocked
	// (--unlock-confirmed, plan.UnlockFlag).
	UnlockConfirmed bool
	// Gate is the payload gate; nil is the release's (mainline.EmbeddedGate).
	// Only tests and the devtest build give another (mainline.TestGate).
	Gate *mainline.Gate
	// Direct: the direct path (task 3; keeper stock A by R(K), target B,
	// the boot chain from the stash). PlanMainlineDirect and
	// InstallMainlineDirect set it.
	Direct bool
	// LockState is the hook of the in-RAM lock_state reader (nil: not built
	// yet; the plan then needs UnlockConfirmed). Only the direct path reads it.
	LockState *byte
}

type mlSession struct {
	*session
	o      MainlineOptions
	folder string // the bundle folder (--mainline), or ""
	pass1  *mainline.Payload
	armed  bool // the folder guard: the arm step has started
	// stash is the stash of the first drive phase of this boot (task 2:
	// the key record; nil when there is none: the plan then refuses)
	stash *mlstash.Loaded
}

// checkMainlineOptions refuses the inputs of the official device mode and a
// missing or double bundle source.
func checkMainlineOptions(o MainlineOptions) error {
	var wrong []string
	if o.FW != "" || o.FWSource != nil {
		wrong = append(wrong, "firmware file (--fw)")
	}
	if o.Keys != "" || len(o.Libs) > 0 {
		wrong = append(wrong, "firmware key (--keys, --lib): it reads no firmware container")
	}
	switch {
	case o.Direct && o.Keeper != "" && o.Keeper != plan.DirectKeeper:
		wrong = append(wrong, "keeper "+o.Keeper+" (the direct path always keeps slot A)")
	case !o.Direct && o.Keeper != "" && o.Keeper != plan.MainlineKeeper:
		wrong = append(wrong, "keeper "+o.Keeper+" (a mainline install always keeps slot B)")
	}
	if !o.Direct && o.LockState != nil {
		wrong = append(wrong, "a lock_state read (only the direct path takes one)")
	}
	if len(wrong) > 0 {
		return &device.Refusal{Why: "a mainline install takes no " + strings.Join(wrong, ", no ")}
	}
	switch {
	case (o.Mainline == "") == (o.FWDrive == ""):
		return &device.Refusal{Why: "give the mainline bundle on the drive (--fw-drive) or as a folder (--mainline), one of them"}
	case o.FWDriveExpect != "" && o.FWDrive == "":
		return &device.Refusal{Why: "--fw-drive-expect needs --fw-drive"}
	}
	if o.Mainline != "" {
		// a folder outside /dev, /proc and /sys, not a symlink
		if _, err := mainline.DirSource(o.Mainline); err != nil {
			return &device.Refusal{Why: "the mainline folder: " + err.Error()}
		}
	}
	return checkPaths(o.Options)
}

// openMainline runs the checks before any plan: the options, the drive rule
// (before anything else is opened, as setupDrive), the RAM image, the eMMC and
// the A/B lock (open).
func openMainline(e *device.Env, o MainlineOptions, out io.Writer) (*mlSession, error) {
	if err := checkMainlineOptions(o); err != nil {
		return nil, err
	}
	var g *driveGuard
	if o.FWDrive != "" {
		g = &driveGuard{lun: e.Path(drivestate.LunFile), image: o.FWDrive}
		var err error
		if o.FWDriveExpect != "" {
			g.want, err = drivestate.Parse(o.FWDriveExpect)
		} else {
			g.want, err = drivestate.Stat(g.image)
		}
		if err != nil {
			return nil, &device.Refusal{Why: "drive: " + err.Error()}
		}
		if err := g.check(); err != nil {
			return nil, &device.Refusal{Why: "drive: " + err.Error()}
		}
	}
	so := o.Options
	so.FWDrive, so.FWDriveExpect = "", "" // the drive guard of this file, not setupDrive's firmware open
	s, err := open(e, so, out)
	if err != nil {
		return nil, err
	}
	ms := &mlSession{session: s, o: o, folder: o.Mainline}
	if g != nil {
		g.s = s
		s.drive = g
		s.say("drive: the mainline bundle is the folder %s of the P10-INSTALL drive file %s (%s); the drive is detached from the computer and must stay so, unchanged, to the end",
			mainline.Dir, g.image, g.want)
	} else {
		s.say("bundle: the mainline folder %s; its files must stay where they are, unchanged, to the end", o.Mainline)
	}
	return ms, nil
}

// source opens the bundle: the folder of the drive (the drive rule before and
// after the open; the drive must not hold a firmware .bin too), or the
// folder of --mainline.
func (s *mlSession) source() (mainline.Source, error) {
	if g := s.drive; g != nil {
		if err := g.check(); err != nil {
			return nil, &device.Refusal{Why: "drive: " + err.Error()}
		}
		fs, err := fatfs.OpenImage(g.image)
		if err != nil {
			return nil, &device.Refusal{Why: "the P10-INSTALL drive: " + err.Error()}
		}
		g.opened = append(g.opened, fs)
		top, err := mldrive.Top(fs)
		switch {
		case err != nil:
			return nil, &device.Refusal{Why: "the top folder of the P10-INSTALL drive: " + err.Error()}
		case !top.Bundle:
			return nil, &device.Refusal{Why: "the P10-INSTALL drive has no folder " + mainline.Dir}
		case len(top.Bins) > 0:
			return nil, &device.Refusal{Why: fmt.Sprintf("the P10-INSTALL drive holds the folder %s and a firmware file (%s): leave only one of them there",
				mainline.Dir, strings.Join(top.Bins, ", "))}
		}
		src, err := mldrive.DriveSource(fs)
		if err != nil {
			return nil, &device.Refusal{Why: err.Error()}
		}
		if err := g.check(); err != nil {
			return nil, &device.Refusal{Why: "drive: " + err.Error()}
		}
		return src, nil
	}
	src, err := mainline.DirSource(s.folder)
	if err != nil {
		return nil, &device.Refusal{Why: "the mainline folder: " + err.Error()}
	}
	return src, nil
}

// identNow is the identity of the bundle now: "drive " + the drive file
// identity (the drive rule checks it), or "folder " + the identity of the
// three files of --mainline (mldrive.Ident).
func (s *mlSession) identNow() (string, error) {
	if s.drive != nil {
		return s.fwIdentNow()
	}
	abs, err := filepath.Abs(s.folder)
	if err != nil {
		return "", err
	}
	id, err := mldrive.Ident(abs)
	if err != nil {
		return "", err
	}
	return "folder " + abs + ": " + id, nil
}

// mlInput is the planner's input: the unit read now (input, the official
// reader of the device mode), the mainline payload, the embedded manifests and
// the unlock confirmation.
func (s *mlSession) mlInput(view io.ReaderAt, ml *mainline.Payload, ident string, known map[string]string, say func(string)) (plan.MainlineInput, error) {
	in, err := s.input(view, nil, nil, "", ident, nil, say)
	if err != nil {
		return plan.MainlineInput{}, err
	}
	in.Tool = MainlineVersion
	ms, err := s.e.Manifests()
	if err != nil {
		return plan.MainlineInput{}, err
	}
	gate := s.o.Gate
	if gate == nil {
		gate = mainline.EmbeddedGate()
	}
	mi := plan.MainlineInput{Input: in, Mainline: ml, Manifests: ms, UnlockConfirmed: s.o.UnlockConfirmed,
		RecheckBefore: known, Gate: gate}
	// task 2: the key record of the first drive phase (the stash of this
	// boot), and its two sectors read now
	st, why := LoadStash(s.e, s.bootID, s.token)
	if st == nil {
		// review T2 MINOR-2: a unit whose two record sectors hold the same
		// valid record (an earlier install kept the key) needs no stash: the
		// plan says "kept" and writes nothing there
		if rec, ok := keptBoth(view, s.d.Bytes()); ok {
			mi.MainlineKey = plan.ReadMainlineKey(view, s.d.Bytes(), rec, s.e.KeyKeepGate(),
				"the record kept on the unit (both copies equal; written by an earlier install), no stash of this boot")
			say("stash: none of this boot (" + why + "); the key record is kept on the unit already (both copies)")
		} else {
			say("stash: none of this boot (" + why + "): the key cannot be kept")
		}
	} else {
		s.stash = st
		mi.MainlineKey = plan.ReadMainlineKey(view, s.d.Bytes(), st.Record(), s.e.KeyKeepGate(), StashProof(st))
		say(fmt.Sprintf("stash: %s of %s, key record sha256 %s…", mlstash.Dir, st.Firmware.Name, st.Key.RecordSHA256[:16]))
	}
	if mi.MainlineKey != nil {
		// owner O3: the record (of the stash, or kept on the unit) must be
		// the one the release pins
		mi.MainlineKey.Pin = gate.KeyRecordPin()
	}
	return mi, nil
}

// compute makes the plan from scratch with pass 1 over the whole bundle.
func (s *mlSession) compute(quiet bool) (*plan.MainlinePlan, error) {
	say := s.sayer(quiet)
	ident, err := s.identNow()
	if err != nil {
		return nil, &device.Refusal{Why: "the mainline bundle: " + err.Error()}
	}
	src, err := s.source()
	if err != nil {
		return nil, err
	}
	say("pass 1: check the mainline bundle (" + src.Describe() + "): the manifest, the boot template and the root file system, hashed in place (nothing is written)")
	ml, err := mainline.Verify(src)
	if err != nil {
		return nil, &device.Refusal{Why: "pass 1: " + err.Error()}
	}
	if now, err := s.identNow(); err != nil || now != ident {
		ml.Close()
		return nil, &device.Refusal{Why: fmt.Sprintf("the mainline bundle changed while pass 1 read it: %s, then %s (%v)", ident, now, err)}
	}
	say("  " + ml.Pass1)
	say("bundle source: " + ident)
	s.pass1 = ml
	view := s.d.View()
	defer view.Close()
	in, err := s.mlInput(view, ml, ident, nil, say)
	if err != nil {
		return nil, err
	}
	p, err := plan.ComputeMainline(in)
	if err != nil {
		if !quiet {
			s.say("")
			p.Render(s.out)
		}
		return p, &device.Refusal{Why: err.Error()}
	}
	return p, nil
}

// recheck computes the plan p0 again from the unit without a second hash
// of the bundle. ml is this run's pass 1 (nil with --plan: the bundle is
// then opened again with mainline.Reopen and must be the plan's).
func (s *mlSession) recheck(p0 *plan.MainlinePlan, ml *mainline.Payload) (*plan.MainlinePlan, *mainline.Payload, error) {
	say := s.sayer(true)
	if p0.FirmwareIdent == "" || p0.Mainline == nil {
		return nil, nil, &device.Refusal{Why: "the plan records no bundle identity or mainline payload"}
	}
	if now, err := s.identNow(); err != nil || now != p0.FirmwareIdent {
		return nil, nil, &device.Refusal{Why: fmt.Sprintf("the mainline bundle is not the one that pass 1 checked: now %s (%v), the plan has %s", now, err, p0.FirmwareIdent)}
	}
	if ml == nil {
		src, err := s.source()
		if err != nil {
			return nil, nil, err
		}
		if ml, err = mainline.Reopen(src, p0.Mainline); err != nil {
			return nil, nil, &device.Refusal{Why: "the plan's mainline bundle: " + err.Error()}
		}
		if now, err := s.identNow(); err != nil || now != p0.FirmwareIdent {
			ml.Close()
			return nil, nil, &device.Refusal{Why: fmt.Sprintf("the mainline bundle changed while it was opened: now %s (%v), the plan has %s", now, err, p0.FirmwareIdent)}
		}
	}
	view := s.d.View()
	defer view.Close()
	in, err := s.mlInput(view, ml, p0.FirmwareIdent, plan.MainlineKnownBeforeOf(p0), say)
	if err != nil {
		return nil, nil, err
	}
	p, err := plan.ComputeMainline(in)
	if err != nil {
		return p, ml, &device.Refusal{Why: err.Error()}
	}
	return p, ml, nil
}

// guard is install.Options.Guard: the drive rule (drive.go), or the identity
// of the bundle folder, before every step up to the arm. A change stops the
// install before that step: slot A stays disabled, the keeper B boots.
func (s *mlSession) guard(p *plan.MainlinePlan) func(plan.Step) error {
	if s.drive != nil {
		return s.drive.step
	}
	return func(st plan.Step) error {
		if s.armed {
			return nil
		}
		if now, err := s.identNow(); err != nil || now != p.FirmwareIdent {
			return fmt.Errorf("bundle rule: the mainline folder changed: now %s (%v), the plan has %s", now, err, p.FirmwareIdent)
		}
		if st.Op == "bootctl" && st.Bootctl != nil && len(st.Bootctl.Args) > 0 && st.Bootctl.Args[0] == "arm-trying" {
			s.armed = true
		}
		return nil
	}
}

func (s *mlSession) close() {
	if s.pass1 != nil {
		s.pass1.Close()
	}
	s.session.close()
}

func writeMainlineJSON(path string, p *plan.MainlinePlan) error {
	if err := CheckOutputPath(path); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(p, "", "  ")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// PlanKind is the kind of a plan file ("" for an official plan of an
// older p10inst without the field). It reads only that field.
func PlanKind(path string) (string, error) {
	if err := CheckInputFile(path); err != nil {
		return "", err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var k struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &k); err != nil {
		return "", fmt.Errorf("%s: %v", path, err)
	}
	return k.Kind, nil
}

// ReadMainlinePlanFile reads a mainline plan JSON whose content matches its
// id. An official plan is refused here (and a mainline plan is refused by
// ReadPlanFile: its id is not the id of the plan inside it).
func ReadMainlinePlanFile(path string) (*plan.MainlinePlan, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, &device.Refusal{Why: err.Error()}
	}
	var p plan.MainlinePlan
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &device.Refusal{Why: fmt.Sprintf("%s: %v", path, err)}
	}
	if p.Kind != plan.MainlineKind {
		return nil, &device.Refusal{Why: fmt.Sprintf("%s: a plan of kind %q, not a mainline plan (%s)", path, p.Kind, plan.MainlineKind)}
	}
	if p.ID == "" || !p.VerifyID() {
		return nil, &device.Refusal{Why: fmt.Sprintf("%s: its content does not match its id %s (an edited plan)", path, p.ID)}
	}
	if p.Mode != "device" {
		return nil, &device.Refusal{Why: fmt.Sprintf("%s: a %q plan, not a device plan", path, p.Mode)}
	}
	return &p, nil
}

// PlanMainline checks the unit and the bundle and prints the mainline plan.
// It writes nothing to the eMMC.
func PlanMainline(e *device.Env, o MainlineOptions, out io.Writer) (*plan.MainlinePlan, error) {
	s, err := openMainline(e, o, out)
	if err != nil {
		return nil, err
	}
	defer s.close()
	p, err := s.compute(false)
	if err != nil {
		return p, err
	}
	s.say("")
	p.Render(out)
	s.say("plan id: %s", p.ID)
	if o.JSONOut != "" {
		if err := writeMainlineJSON(o.JSONOut, p); err != nil {
			return p, err
		}
	}
	return p, nil
}

// InstallMainline runs the mainline device mode. See the file comment for
// the order.
func InstallMainline(e *device.Env, o MainlineOptions, out io.Writer) (*install.Result, error) {
	s, err := openMainline(e, o, out)
	if err != nil {
		return nil, err
	}
	defer s.close()
	var p1 *plan.MainlinePlan
	var ml1 *mainline.Payload
	if o.PlanFile != "" {
		if p1, err = ReadMainlinePlanFile(o.PlanFile); err != nil {
			return nil, err
		}
		s.say("the plan of %s: its pass 1 checked the whole mainline bundle; the bundle is not hashed again (the root file system is hashed as it is written)", o.PlanFile)
	} else {
		if p1, err = s.compute(false); err != nil {
			return nil, err
		}
		ml1 = s.pass1
	}
	s.say("")
	p1.Render(out)
	s.say("")
	s.say("plan id: %s", p1.ID)
	if o.JSONOut != "" {
		if err := writeMainlineJSON(o.JSONOut, p1); err != nil {
			return nil, err
		}
	}
	if !o.Yes {
		s.say("")
		s.say("This writes slot A of this unit with mainline Linux %s. Slot B (Android %s %s) stays as it is and is the fallback.",
			p1.Mainline.Manifest.Version, p1.Chain.Version, p1.Chain.BuildType)
		fmt.Fprintf(out, "Type INSTALL and press Enter to start, anything else to stop: ")
		line := ""
		if o.Confirm != nil {
			line, _ = bufio.NewReader(o.Confirm).ReadString('\n')
		}
		if strings.TrimSpace(line) != "INSTALL" {
			return nil, &device.Refusal{Why: "not confirmed"}
		}
	}
	if o.AfterConfirm != nil {
		o.AfterConfirm()
	}
	s.say("")
	s.say("re-check: the plan again from the unit, the bundle not hashed again (its identity, the manifest, the template; nothing is written yet)")
	p2, ml2, err := s.recheck(p1, ml1)
	if ml2 != nil && ml2 != ml1 {
		defer ml2.Close()
	}
	if err != nil {
		return nil, err
	}
	if p2.ID != p1.ID {
		if o.PlanFile != "" {
			return nil, &device.Refusal{Why: fmt.Sprintf("the plan now has id %s, %s has %s", p2.ID, o.PlanFile, p1.ID)}
		}
		return nil, &device.Refusal{Why: fmt.Sprintf("the plan changed after it was shown: id %s, shown %s", p2.ID, p1.ID)}
	}
	s.say("re-check: the plan has the same id %s; bundle source %s", p2.ID, p2.FirmwareIdent)
	if err := s.d.CanWriteAB(); err != nil {
		return nil, &device.Refusal{Why: "the A/B steps cannot run: " + err.Error()}
	}

	logPath := o.LogPath
	if logPath == "" {
		logPath = e.Path("/run/p10inst/install-" + p2.ID[:12] + ".jsonl")
		os.MkdirAll(filepath.Dir(logPath), 0o755)
		writeMainlineJSON(strings.TrimSuffix(logPath, ".jsonl")+".plan.json", p2)
	}
	trPath := strings.TrimSuffix(logPath, ".jsonl") + ".out"
	for _, p := range []string{logPath, trPath} {
		if err := CheckOutputPath(p); err != nil {
			return nil, err
		}
	}
	lf, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return nil, &device.Refusal{Why: err.Error()}
	}
	defer lf.Close()
	if b, err := json.Marshal(map[string]any{"event": "environment", "plan": p2.ID, "kind": p2.Kind, "ram_token": s.token,
		"boot_id": s.bootID, "firmware_ident": p2.FirmwareIdent, "mainline_manifest_sha256": p2.Mainline.ManifestSHA256,
		"unlock_confirmed": p2.Unlock.Confirmed, "time": time.Now().UTC().Format(time.RFC3339Nano)}); err == nil {
		lf.Write(append(b, '\n'))
	}
	if s.tr, err = os.OpenFile(trPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND|syscall.O_NOFOLLOW, 0o644); err != nil {
		return nil, &device.Refusal{Why: err.Error()}
	}
	defer func() { s.tr.Close(); s.tr = nil }()
	s.say("log: %s, transcript: %s", logPath, trPath)
	s.say("RAM image token %s (in the plan and in the log)", s.token)
	sigs := []os.Signal{syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGPIPE}
	signal.Ignore(sigs...)
	defer signal.Reset(sigs...)
	s.say("signals: ^C, hangup and SIGTERM are ignored until the install ends")
	s.say("")
	start := time.Now()
	m := s.d.Medium(o.MaxWrite)
	runOpt := install.Options{Say: func(l string) { s.say("%s", l) }, Log: lf, Hooks: o.Hooks, MaxWrite: o.MaxWrite,
		Guard: s.guard(p2)}
	if s.stash != nil {
		runOpt.KeyRecord = s.stash.Record() // the plan's keep-key step (task 2); never logged
	}
	res, err := install.RunMainline(p2, ml2, m, runOpt)
	if s.drive != nil {
		s.drive.final()
	}
	ab, abErr := s.d.ReadAB()
	if err != nil {
		if res == nil {
			return nil, &device.Refusal{Why: err.Error()}
		}
		s.say("")
		s.say("STOP: %v", err)
		if errors.Is(err, device.ErrABUnknown) {
			raw := "unreadable"
			if abErr == nil {
				raw = fmt.Sprintf("%x (decoded, not trusted: %s)", ab[:32], plan.State(first32(ab)))
			}
			s.say("!!! DO NOT POWER OFF AND DO NOT RESTART. The A/B state is not known.")
			s.say("!!! Keep the unit powered, read the A/B state with: %s status, and ask for help.", device.BootctlPath)
			s.say("A/B sector now: %s", raw)
			s.say("writes done: %d, plus perhaps the A/B write that was running", res.Writes)
			return res, &Stop{Err: err, Safe: "unknown"}
		}
		state := install.SafeStateOf(ab, abErr, &p2.Plan)
		s.say("A/B now: %s", state)
		s.say("writes done: %d", res.Writes)
		return res, &Stop{Err: err, Safe: state}
	}
	state := install.SafeStateOf(ab, abErr, &p2.Plan)
	s.say("")
	s.say("RESULT: install done in %s, %d writes, every write read back and verified", time.Since(start).Round(time.Second), res.Writes)
	s.say("A/B now: %s", state)
	s.say("Slot A starts mainline Linux at the next restart. It marks itself successful only with a network link (eth0 or usb0) and sshd within 300 s; otherwise its tries run out and slot B starts again.")
	return res, nil
}
