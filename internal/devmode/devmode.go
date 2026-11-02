//go:build install

// Package devmode is the flow of the device mode: `p10inst install --device
// /dev/mmcblk0` in the installer RAM image. It joins the checks of package
// device, the planner, the confirmation and the executor:
//
//  1. CheckRAMImage: the installer RAM image, not a booted system. Its
//     token (uc-pxx.id=p10inst-XXXXXX) is printed, and it goes into the plan
//     (and so into the plan id) and into the JSON-lines log, with the
//     kernel's boot id.
//  2. OpenEMMC: exactly /dev/mmcblk0, and only the eMMC of a UC-P10 or a
//     UC-P8 with the expected layout in sysfs and in both GPT copies, whose
//     factory data names the model of this RAM image (the model gate).
//  3. The A/B lock for the whole run.
//  4. The plan: with --plan FILE the plan of the plan run (its content must
//     match its id); without it, the plan computed here, with pass 1 over
//     the whole firmware and the keeper hashed. It is printed with its id.
//     A refused plan ends here.
//  5. The confirmation: --yes, or the word INSTALL typed by a person.
//  6. The re-check (DEVMODE-R4.md §3): the plan is computed again from the
//     unit, but the firmware is NOT hashed again (pass 1 ran once, in step 4
//     or in the plan run of this boot): the firmware source must have the
//     identity that pass 1 read, the plan's payload must be the manifest's,
//     the container is opened with the key, and every fact of the unit is
//     read again (the keeper hashed). Its id must be the plan's id.
//  7. The executor runs it (package install, the same steps as a dry run).
//     It hashes every partition image as it writes it and reads it back
//     before the target is armed.
//
// Nothing is written before step 7.
package devmode

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
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/keykeep"
	"uc-pxx-installer/internal/pcdfw"
	"uc-pxx-installer/internal/plan"
)

// Version is the tool version that goes into the plan.
const Version = "p10inst device mode 2026-10-31"

// Options are the options of the device mode.
type Options struct {
	Device           string   // must be /dev/mmcblk0
	FW               string   // the firmware file
	Keys             string   // a key file (optional)
	Libs             []string // libraries to find the key in (optional)
	Keeper           string   // "", "a" or "b"
	PrereqsConfirmed bool
	// PanelConfirmed: the user sees this screen on the unit's panel
	// (plan.Input.PanelConfirmed; required).
	PanelConfirmed bool
	// AutoUpdateUnclearConfirmed: go on although the Android auto-update
	// state is not clear (plan.Input.AutoUpdateUnclearConfirmed).
	AutoUpdateUnclearConfirmed bool
	PlanFile                   string // the plan JSON that a person approved (optional)
	JSONOut                    string // write the plan JSON here (optional)
	Yes                        bool   // the confirmation is given
	Confirm                    io.Reader
	LogPath                    string // JSON lines (default /run/p10inst/install-ID.jsonl)
	// FWSource, if set, opens the firmware in place of FW: the on-screen
	// flow reads the .bin on the P10-INSTALL drive where it is
	// (fatfs.OpenFirmware), without a copy. FW is then only its name, and
	// it is not checked as a path. It has no flag.
	FWSource func() (FWFile, error)
	// FWDrive is the P10-INSTALL drive file (--fw-drive): the firmware is
	// the one .bin on it, read in place, and the drive rule holds for the
	// whole run (drive.go). FW must then be empty. FWDriveExpect
	// (--fw-drive-expect, optional) is the identity of the drive file that
	// the flow saw when it read the firmware (drivestate.Ident.String); it
	// binds the plan run and the install run to the same file.
	FWDrive       string
	FWDriveExpect string
	// Tests only:
	Hooks        *install.Hooks
	MaxWrite     int
	AfterConfirm func()
}

// FWFile is an opened firmware file, as the planner reads it.
type FWFile interface {
	io.ReaderAt
	Size() int64
}

// Stop is an install that stopped after the first write. Safe tells
// whether the A/B state is known.
type Stop struct {
	Err  error
	Safe string // the A/B state, as SafeStateOf says it
}

func (s *Stop) Error() string { return s.Err.Error() }
func (s *Stop) Unwrap() error { return s.Err }

// ExitCode maps an error of Plan or Install to the exit status of p10inst:
// 0 done, 1 refused or stopped with a known A/B state, 3 A/B state unknown
// (do not power off).
func ExitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, device.ErrABUnknown):
		return 3
	}
	return 1
}

type session struct {
	e   *device.Env
	o   Options
	out io.Writer
	d   *device.Disk
	tr  *os.File // the transcript of the run: every line also goes here
	// token is the RAM image token of the kernel command line
	// (p10inst-XXXXXX), from CheckRAMImage.
	token  string
	bootID string      // the kernel's boot id (plan.RAMImage.BootID)
	drive  *driveGuard // --fw-drive: the drive rule (drive.go)
	// the key source of this run's own pass 1 (Install without --plan)
	keySlots  []string
	keySource string
	keyKeep   *plan.KeyKeepInput // of this run's own pass 1
	record    []byte             // the key record of the key in use (never logged)
	key       pcdfw.Keys         // the key of this run's own pass 1 (never logged)
	fwR       io.ReaderAt        // and its firmware
	fwSize    int64
	fwArmed   bool // the file guard: the arm step has started
}

// say writes one line to the output and to the transcript. A write error of
// the output (a closed terminal) does not stop anything: the transcript and
// the JSON-lines log keep the lines.
func (s *session) say(f string, a ...any) {
	line := fmt.Sprintf(f+"\n", a...)
	io.WriteString(s.out, line)
	if s.tr != nil {
		s.tr.WriteString(line)
	}
}

// open runs the checks that come before any plan: the RAM image, the eMMC
// and the A/B lock.
func open(e *device.Env, o Options, out io.Writer) (*session, error) {
	s := &session{e: e, o: o, out: out}
	if err := checkPaths(o); err != nil {
		return nil, err
	}
	if err := s.setupDrive(); err != nil {
		return nil, err
	}
	tok, err := e.CheckRAMImage()
	if err != nil {
		return nil, err
	}
	s.token = tok
	if s.bootID, err = e.BootID(); err != nil {
		return nil, err
	}
	s.say("environment: the installer RAM image, token %s (%s, %s, uc-pxx.id=%s), no eMMC file system mounted",
		tok, device.RAMImageMarker, device.RAMCmdline, tok)
	s.say("environment: boot id %s", s.bootID)
	d, err := e.OpenEMMC(o.Device)
	if err != nil {
		return nil, err
	}
	s.say("eMMC: %s is the eMMC of a %s on %s; sysfs and both GPT copies have the unit layout (the same on the UC-P10 and the UC-P8, %d partitions)",
		device.EMMCPath, d.ID.Model, d.ID.Host, len(d.Table.Entries))
	um := d.ID.UnitModel
	s.say("unit: the factory data says %q (%s, suffix %q, hardware %s); this RAM image is for the %s",
		um.FactoryModel, um.Base, um.Suffix, um.HWRev, um.Base)
	if off := d.ROWasOff(); len(off) > 0 {
		s.say("eMMC: the kernel read-only flag was OFF on %d device(s) (%s), probably after a run that was killed; it is set again now",
			len(off), strings.Join(off, " "))
	}
	s.say("eMMC: kernel read-only flag set on the disk and on every partition")
	if err := d.Lock(); err != nil {
		return nil, &device.Refusal{Why: err.Error()}
	}
	s.d = d
	if s.drive != nil {
		s.say("drive: the firmware is the .bin on the P10-INSTALL drive file %s (%s); the drive is detached from the computer and must stay so, unchanged, to the end", s.drive.image, s.drive.want)
	}
	return s, nil
}

func (s *session) close() {
	if s.drive != nil {
		s.drive.close()
	}
	if s.d != nil {
		s.d.Unlock()
	}
}

// compute makes the plan from scratch: the key, pass 1 over the whole
// firmware, the bootctl binary and its status, and every check. This is the
// one full check of the firmware against the manifests (the plan run, or an
// install without --plan).
func (s *session) compute(quiet bool) (*plan.Plan, *plan.Payload, error) {
	say := s.sayer(quiet)
	ms, err := s.e.Manifests()
	if err != nil {
		return nil, nil, err
	}
	fw, fwSize, ident, err := s.openFW()
	if err != nil {
		return nil, nil, err
	}
	view := s.d.View()
	defer view.Close()
	k, keySlots, keySource, err := s.keyFor(view, fw, fwSize, say)
	if err != nil {
		return nil, nil, err
	}
	say("pass 1: check the whole firmware against the manifests (nothing is written)")
	pl, err := plan.VerifyOfficial(fw, fwSize, k, ms, func(l string) { say("  " + l) })
	if err != nil {
		return nil, nil, &device.Refusal{Why: "pass 1: " + err.Error()}
	}
	// the source must not have changed while pass 1 read it
	if now, err := s.fwIdentNow(); err != nil || now != ident {
		return nil, nil, &device.Refusal{Why: fmt.Sprintf("the firmware changed while pass 1 read it: %s, then %s (%v)", ident, now, err)}
	}
	say("firmware source: " + ident)
	s.keySlots, s.keySource = keySlots, keySource
	s.keyKeep = s.readKeyKeep(view, fw, fwSize, k)
	s.key, s.fwR, s.fwSize = k, fw, fwSize
	in, err := s.input(view, pl, keySlots, keySource, ident, nil, say)
	if err != nil {
		return nil, nil, err
	}
	p, err := plan.Compute(in)
	if err != nil {
		if !quiet {
			s.say("")
			p.Render(s.out)
		}
		return p, pl, &device.Refusal{Why: err.Error()}
	}
	return p, pl, nil
}

// recheck is the check of step 6: the plan p0 computed again from the unit
// without a second hash of the firmware. pl is the payload of this run's own
// pass 1 (nil with --plan: the payload of the plan file is then checked
// against the manifests and its container opened, plan.Reopen). The firmware
// source must have the identity that pass 1 read (p0.FirmwareIdent). The
// before-hashes of the always-written target parts come from p0 (they are
// recorded only; those bytes are replaced). Everything else is read again:
// the GPT, the A/B sector, bootctl status, the nvram auto-update state, both
// vbmeta, the trust_T skip hash and the keeper (all eight partitions hashed).
func (s *session) recheck(p0 *plan.Plan, pl *plan.Payload) (*plan.Plan, *plan.Payload, error) {
	say := s.sayer(true)
	if p0.FirmwareIdent == "" || p0.Payload == nil {
		return nil, nil, &device.Refusal{Why: "the plan records no firmware identity or payload (a plan of an older p10inst?)"}
	}
	if now, err := s.fwIdentNow(); err != nil || now != p0.FirmwareIdent {
		return nil, nil, &device.Refusal{Why: fmt.Sprintf("the firmware is not the one that pass 1 checked: now %s (%v), the plan has %s", now, err, p0.FirmwareIdent)}
	}
	view := s.d.View()
	defer view.Close()
	keySlots, keySource := s.keySlots, s.keySource
	var kk *plan.KeyKeepInput
	if pl != nil {
		// the key and the firmware of this run's pass 1: the record
		// sectors and the system copies are read again now
		kk = s.readKeyKeep(view, s.fwR, s.fwSize, s.key)
	}
	if pl == nil {
		ms, err := s.e.Manifests()
		if err != nil {
			return nil, nil, err
		}
		fw, fwSize, ident, err := s.openFW()
		if err != nil {
			return nil, nil, err
		}
		if ident != p0.FirmwareIdent {
			return nil, nil, &device.Refusal{Why: fmt.Sprintf("the firmware is not the one that pass 1 checked: %s, the plan has %s", ident, p0.FirmwareIdent)}
		}
		k, slots, src, err := s.keyFor(view, fw, fwSize, say)
		if err != nil {
			return nil, nil, err
		}
		if pl, err = plan.Reopen(fw, fwSize, k, ms, p0.Payload); err != nil {
			return nil, nil, &device.Refusal{Why: "the plan's payload: " + err.Error()}
		}
		keySlots, keySource = slots, src
		kk = s.readKeyKeep(view, fw, fwSize, k)
	}
	s.keyKeep = kk
	in, err := s.input(view, pl, keySlots, keySource, p0.FirmwareIdent, plan.KnownBeforeOf(p0), say)
	if err != nil {
		return nil, nil, err
	}
	p, err := plan.Compute(in)
	if err != nil {
		return p, pl, &device.Refusal{Why: err.Error()}
	}
	return p, pl, nil
}

func (s *session) sayer(quiet bool) func(string) {
	return func(l string) {
		if !quiet {
			s.say("%s", l)
		}
	}
}

// keyFor finds the key: the key file, the libraries given, or the updater
// libraries in system_a and system_b of the eMMC.
func (s *session) keyFor(view io.ReaderAt, fw io.ReaderAt, fwSize int64, say func(string)) (pcdfw.Keys, []string, string, error) {
	var k pcdfw.Keys
	var err error
	switch {
	case s.o.Keys != "" && len(s.o.Libs) > 0:
		return k, nil, "", &device.Refusal{Why: "give a key file or libraries, not both"}
	case s.o.Keys != "":
		if k, err = pcdfw.LoadKeyFile(s.o.Keys); err != nil {
			return k, nil, "", &device.Refusal{Why: err.Error()}
		}
		return k, nil, "key file", nil
	case len(s.o.Libs) > 0:
		var libs []pcdfw.Lib
		for _, p := range s.o.Libs {
			b, err := os.ReadFile(p)
			if err != nil {
				return k, nil, "", &device.Refusal{Why: err.Error()}
			}
			libs = append(libs, pcdfw.Lib{Name: filepath.Base(p), Data: b})
		}
		res, err := pcdfw.FindKeys(fw, fwSize, libs, false)
		if err != nil || len(res.Pairs) != 1 {
			return k, nil, "", &device.Refusal{Why: fmt.Sprintf("key search in the libraries: %d pairs (%v)", len(res.Pairs), err)}
		}
		return res.Pairs[0], nil, "libraries given with --lib", nil
	}
	k, slots, err := KeysFromDisk(view, s.d.Bytes(), fw, fwSize, say)
	if err == nil {
		return k, slots, "", nil
	}
	// the key kept on the unit (KEY-KEEP.md §4.3)
	if kk, kerr := KeyFromRecord(view, s.d.Bytes(), fw, fwSize); kerr == nil {
		say("key search: the key kept on the unit (backup) decrypts this firmware")
		return kk, nil, KeySourceRecord, nil
	}
	return k, nil, "", &device.Refusal{Why: err.Error()}
}

// KeySourceRecord is the plan's key_source for the key kept on the unit.
const KeySourceRecord = "the key kept on the unit (backup partition)"

// readKeyKeep reads what the keep-key step needs: the record of the key in use,
// the two record sectors now, whether the key is kept already, and the
// systems that hold the key (read-only). The record stays in the session
// (s.record) for the executor; it never goes into the plan or a log.
func (s *session) readKeyKeep(view io.ReaderAt, fw io.ReaderAt, fwSize int64, k pcdfw.Keys) *plan.KeyKeepInput {
	rec := keykeep.Encode(k)
	s.record = rec
	in := &plan.KeyKeepInput{Gate: s.e.KeyKeepGate(), RecordSHA256: keykeep.SHA256(rec),
		Copies: SystemCopies(view, s.d.Bytes(), fw, fwSize, k)}
	recs, _, err := KeptRecords(view, s.d.Bytes())
	for i := range in.Classes {
		in.Classes[i] = keykeep.Unread
		if err == nil {
			in.Classes[i] = keykeep.Classify(recs[i], rec)
			if got, derr := keykeep.Decode(recs[i]); derr == nil && got == k {
				in.Kept = true
			}
		}
	}
	return in
}

// input is the planner's input of this run: the unit read now (the bootctl
// binary and its status included), the payload and the key source given.
func (s *session) input(view io.ReaderAt, pl *plan.Payload, keySlots []string, keySource, ident string,
	known map[string]string, say func(string)) (plan.Input, error) {
	e, d := s.e, s.d
	_, sha, berr := e.BootctlBinary()
	if berr != nil {
		say("uc-pxx-bootctl-inst: " + berr.Error())
	}
	var status *plan.BootctlStatus
	if berr == nil {
		var err error
		if status, err = d.BootctlStatus(); err != nil {
			say("uc-pxx-bootctl-inst status: " + err.Error())
		}
	}
	id := d.ID
	return plan.Input{Disk: view, DiskBytes: d.Bytes(), Source: "device:" + device.EMMCPath, Mode: "device",
		Tool: Version, Keeper: s.o.Keeper, Payload: pl, Bootctl: plan.Bootctl{Path: device.BootctlPath, SHA256: sha},
		BootctlStatus: status, PrereqsConfirmed: s.o.PrereqsConfirmed, PanelConfirmed: s.o.PanelConfirmed, AutoUpdateUnclearConfirmed: s.o.AutoUpdateUnclearConfirmed,
		HashKeeper: true, KeySlots: keySlots, KeySource: keySource, Device: &id,
		RAMImage: &plan.RAMImage{Token: s.token, BootID: s.bootID}, RequireBootctlSHA256: e.BootctlSHA256(),
		FirmwareIdent: ident, KnownBefore: known, KeyKeep: s.keyKeep}, nil
}

// openFW opens the firmware (FWSource, or the file FW) and gives the
// identity of its source: the drive file (--fw-drive), or the file itself.
func (s *session) openFW() (io.ReaderAt, int64, string, error) {
	if s.o.FWSource != nil {
		f, err := s.o.FWSource()
		if err != nil {
			return nil, 0, "", &device.Refusal{Why: "firmware: " + err.Error()}
		}
		ident, err := s.fwIdentNow()
		if err != nil {
			return nil, 0, "", &device.Refusal{Why: "firmware: " + err.Error()}
		}
		return f, f.Size(), ident, nil
	}
	fw, err := os.Open(s.o.FW)
	if err != nil {
		return nil, 0, "", &device.Refusal{Why: err.Error()}
	}
	st, err := fw.Stat()
	if err != nil || !st.Mode().IsRegular() {
		fw.Close()
		return nil, 0, "", &device.Refusal{Why: s.o.FW + " is not a regular file"}
	}
	ident, err := s.fwIdentNow()
	if err != nil || !sameFile(st, s.o.FW) {
		fw.Close()
		return nil, 0, "", &device.Refusal{Why: fmt.Sprintf("%s: the open file is not the file of the path (%v)", s.o.FW, err)}
	}
	return fw, st.Size(), ident, nil
}

// fwIdentNow is the identity of the firmware source now, without an open:
// "drive " + the drive file identity (the drive rule checks it), "file " +
// the identity of the file of --fw (lstat: size, mtime, ctime, device,
// inode), or "source NAME" for a test source.
func (s *session) fwIdentNow() (string, error) {
	switch {
	case s.drive != nil:
		if err := s.drive.check(); err != nil {
			return "", err
		}
		return "drive " + s.drive.want.String(), nil
	case s.o.FWSource != nil:
		return "source " + s.o.FW, nil
	}
	id, err := drivestate.Stat(s.o.FW)
	if err != nil {
		return "", err
	}
	return "file " + id.String(), nil
}

func sameFile(st os.FileInfo, path string) bool {
	l, err := os.Lstat(path)
	return err == nil && os.SameFile(st, l)
}

// fwGuard is install.Options.Guard: the drive rule (drive.go), or for a
// firmware file its identity, before every step up to the arm. A change stops
// the install before that step: the target stays disabled, the keeper boots.
func (s *session) fwGuard(p *plan.Plan) func(plan.Step) error {
	if s.drive != nil {
		return s.drive.step
	}
	return func(st plan.Step) error {
		if s.fwArmed || s.o.FWSource != nil {
			return nil
		}
		if now, err := s.fwIdentNow(); err != nil || now != p.FirmwareIdent {
			return fmt.Errorf("firmware rule: the firmware file changed: now %s (%v), the plan has %s", now, err, p.FirmwareIdent)
		}
		if st.Op == "bootctl" && st.Bootctl != nil && len(st.Bootctl.Args) > 0 && st.Bootctl.Args[0] == "arm-trying" {
			s.fwArmed = true
		}
		return nil
	}
}

func writeJSON(path string, p *plan.Plan) error {
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

// CheckOutputPath refuses an output file under /dev, /proc or /sys, and an
// existing path that is not a regular file (a device node, a symlink, a
// directory). So a plan or a log can never go onto a disk.
func CheckOutputPath(p string) error {
	if err := outsideKernelFS(p); err != nil {
		return err
	}
	st, err := os.Lstat(p)
	if err == nil && !st.Mode().IsRegular() {
		return &device.Refusal{Why: fmt.Sprintf("%s exists and is not a regular file (%v): not an output file", p, st.Mode())}
	}
	return nil
}

// CheckInputFile requires a regular file outside /dev, /proc and /sys (not a
// device node, not a symlink): the firmware, a key file, a library, a plan.
func CheckInputFile(p string) error {
	if err := outsideKernelFS(p); err != nil {
		return err
	}
	st, err := os.Lstat(p)
	if err != nil {
		return &device.Refusal{Why: err.Error()}
	}
	if !st.Mode().IsRegular() {
		return &device.Refusal{Why: fmt.Sprintf("%s is not a regular file (%v)", p, st.Mode())}
	}
	return nil
}

func outsideKernelFS(p string) error {
	abs, err := filepath.Abs(p)
	if err != nil {
		return &device.Refusal{Why: err.Error()}
	}
	for _, bad := range []string{"/dev", "/proc", "/sys"} {
		if abs == bad || strings.HasPrefix(abs, bad+"/") {
			return &device.Refusal{Why: fmt.Sprintf("%s is under %s: give a file in /run or on the drive", p, bad)}
		}
	}
	return nil
}

// checkPaths checks every file path of the options.
func checkPaths(o Options) error {
	fw := o.FW
	if o.FWSource != nil {
		fw = "" // a name only, not a path
	}
	for _, p := range append([]string{fw, o.FWDrive, o.Keys, o.PlanFile}, o.Libs...) {
		if p != "" {
			if err := CheckInputFile(p); err != nil {
				return err
			}
		}
	}
	for _, p := range []string{o.JSONOut, o.LogPath} {
		if p != "" {
			if err := CheckOutputPath(p); err != nil {
				return err
			}
		}
	}
	return nil
}

// Plan checks the unit and prints the plan. It writes nothing to the eMMC
// (it sets the kernel read-only flags and runs `uc-pxx-bootctl-inst status`).
func Plan(e *device.Env, o Options, out io.Writer) (*plan.Plan, error) {
	s, err := open(e, o, out)
	if err != nil {
		return nil, err
	}
	defer s.close()
	p, _, err := s.compute(false)
	if err != nil {
		return p, err
	}
	s.say("")
	p.Render(out)
	s.say("plan id: %s", p.ID)
	if o.JSONOut != "" {
		if err := writeJSON(o.JSONOut, p); err != nil {
			return p, err
		}
	}
	return p, nil
}

// Install runs the device mode. See the package comment for the order.
func Install(e *device.Env, o Options, out io.Writer) (*install.Result, error) {
	s, err := open(e, o, out)
	if err != nil {
		return nil, err
	}
	defer s.close()
	var p1 *plan.Plan
	var pl1 *plan.Payload
	if o.PlanFile != "" {
		// the plan of the plan run: its pass 1 checked the whole firmware;
		// it is not hashed again (the re-check below)
		if p1, err = ReadPlanFile(o.PlanFile); err != nil {
			return nil, err
		}
		s.say("the plan of %s: its pass 1 checked the whole firmware against the manifests; the firmware is not hashed again", o.PlanFile)
	} else if p1, pl1, err = s.compute(false); err != nil {
		return nil, err
	}
	s.say("")
	p1.Render(out)
	s.say("")
	s.say("plan id: %s", p1.ID)
	if o.JSONOut != "" {
		if err := writeJSON(o.JSONOut, p1); err != nil {
			return nil, err
		}
	}
	if !o.Yes {
		s.say("")
		s.say("This writes slot %s of this unit. Slot %s stays as it is and is the fallback.", strings.ToUpper(p1.Target), strings.ToUpper(p1.Keeper))
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
	s.say("re-check: the plan again from the unit, the firmware not hashed again (its identity, the manifest, the container; nothing is written yet)")
	p2, pl2, err := s.recheck(p1, pl1)
	if err != nil {
		return nil, err
	}
	if p2.ID != p1.ID {
		if o.PlanFile != "" {
			return nil, &device.Refusal{Why: fmt.Sprintf("the plan now has id %s, %s has %s", p2.ID, o.PlanFile, p1.ID)}
		}
		return nil, &device.Refusal{Why: fmt.Sprintf("the plan changed after it was shown: id %s, shown %s", p2.ID, p1.ID)}
	}
	s.say("re-check: the plan has the same id %s; firmware source %s", p2.ID, p2.FirmwareIdent)
	// The A/B steps need what the tool needs (a queue that takes FUA, write
	// back; the reviewed binary): it refuses the disable otherwise. Check it
	// before the first step, so that a unit whose disable would be refused
	// gets no write at all (the keep-key step comes before the disable).
	if err := s.d.CanWriteAB(); err != nil {
		return nil, &device.Refusal{Why: "the A/B steps cannot run: " + err.Error()}
	}

	logPath := o.LogPath
	if logPath == "" {
		logPath = e.Path("/run/p10inst/install-" + p2.ID[:12] + ".jsonl")
		os.MkdirAll(filepath.Dir(logPath), 0o755)
		writeJSON(strings.TrimSuffix(logPath, ".jsonl")+".plan.json", p2)
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
	// the RAM image of this run: the first line of the run in the log (the
	// plan file holds it too)
	if b, err := json.Marshal(map[string]any{"event": "environment", "plan": p2.ID, "ram_token": s.token,
		"boot_id": s.bootID, "firmware_ident": p2.FirmwareIdent,
		"time": time.Now().UTC().Format(time.RFC3339Nano)}); err == nil {
		lf.Write(append(b, '\n'))
	}
	if s.tr, err = os.OpenFile(trPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND|syscall.O_NOFOLLOW, 0o644); err != nil {
		return nil, &device.Refusal{Why: err.Error()}
	}
	defer func() { s.tr.Close(); s.tr = nil }()
	s.say("log: %s, transcript: %s", logPath, trPath)
	s.say("RAM image token %s (in the plan and in the log)", s.token)
	// From here to the last read-back, a ^C, a terminal hangup, SIGTERM or a
	// closed output pipe must not stop p10inst between two steps of the A/B
	// sector. Only SIGKILL and a power cut can. uc-pxx-bootctl-inst runs in its
	// own session (bootctl.go), so these signals do not reach it either.
	sigs := []os.Signal{syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGPIPE}
	signal.Ignore(sigs...)
	defer signal.Reset(sigs...)
	s.say("signals: ^C, hangup and SIGTERM are ignored until the install ends")
	s.say("")
	start := time.Now()
	m := s.d.Medium(o.MaxWrite)
	runOpt := install.Options{Say: func(l string) { s.say("%s", l) }, Log: lf, Hooks: o.Hooks, MaxWrite: o.MaxWrite,
		Guard: s.fwGuard(p2), KeyRecord: s.record}
	res, err := install.Run(p2, pl2, m, runOpt)
	if s.drive != nil {
		s.drive.final()
	}
	ab, abErr := s.d.ReadAB()
	if err != nil {
		if res == nil {
			// the precheck stopped it: nothing was written
			return nil, &device.Refusal{Why: err.Error()}
		}
		s.say("")
		s.say("STOP: %v", err)
		if errors.Is(err, device.ErrABUnknown) {
			// Only the hold text: never "safe to power off" here.
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
		state := install.SafeStateOf(ab, abErr, p2)
		s.say("A/B now: %s", state)
		s.say("writes done: %d", res.Writes)
		return res, &Stop{Err: err, Safe: state}
	}
	state := install.SafeStateOf(ab, abErr, p2)
	s.say("")
	s.say("RESULT: install done in %s, %d writes, every write read back and verified", time.Since(start).Round(time.Second), res.Writes)
	s.say("A/B now: %s", state)
	return res, nil
}

// ReadPlanFile reads a plan JSON whose content matches its id.
func ReadPlanFile(path string) (*plan.Plan, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, &device.Refusal{Why: err.Error()}
	}
	var p plan.Plan
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &device.Refusal{Why: fmt.Sprintf("%s: %v", path, err)}
	}
	if p.ID == "" || !p.VerifyID() {
		return nil, &device.Refusal{Why: fmt.Sprintf("%s: its content does not match its id %s (an edited plan)", path, p.ID)}
	}
	if p.Mode != "device" {
		return nil, &device.Refusal{Why: fmt.Sprintf("%s: a %q plan, not a device plan", path, p.Mode)}
	}
	return &p, nil
}

func first32(b []byte) [32]byte {
	var r [32]byte
	copy(r[:], b)
	return r
}
