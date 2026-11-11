//go:build install && step2

// Package step2 is mainline step 2 on the unit (MAINLINE-STEP2-DESIGN.md §3,
// §8; MAINLINE-STEP2-REST-2026-11-10.md): the running, successful mainline
// slot K installs mainline into the other slot T, with itself as the keeper,
// and the finish in T switches the next boot back to A. It is the session
// around the reviewed engine: plan.ComputeOtherSlot, install.RunOtherSlot,
// plan.ComputeStep2Finish, the slot medium of package device. It is in the
// step-2 build only (go build -tags install,step2 ./cmd/uc-pxx-step2); it
// never links the device mode (package devmode), pcdfw or the RAM flow.
//
// The order of a start (Start):
//
//  1. the slot environment: a slot boot (U-Boot's bootargs, no RAM image),
//     the eMMC of a UC-P10 with its exact layout (device.OpenEMMCSlot);
//  2. the locks for the whole run: the tsx-autoupdate lock
//     (/run/tsx/autoupdate.lock) and the A/B lock (/run/uc-pxx-abdata.lock);
//     tsx-autoupdate in mode manual with no restart block or hold, no
//     slot-changed marker (design P10);
//  3. the plan computed again from the live unit (every read again: the
//     A/B sector, the keeper's chain, the key records and their pin, the
//     RKSS ranges, userdata, the running facts and the boot id), which must
//     have the id of the plan file that was confirmed (design P17, review
//     F2);
//  4. the step-2 record (the plan) written durably on the keeper's root file
//     system, before the first write: the finish in T reads it from there;
//  5. install.RunOtherSlot on the slot medium: disable T, the six writes of
//     T, sync, verify, [data-format, data-migrate], arm-trying T
//     --lower-keeper, check-ab. In a supervised run every A/B write and the
//     data-format wait for a typed GO line of the plan and step;
//  6. the record of the arm; the restart (`sync; reboot`) is the person's or
//     the service's.
//
// Nothing runs by itself here: Start needs the confirmation (the panel's 3 s
// hold, or the countdown that step2ui.AutoAllowed allowed).
package step2

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/drivestate"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/manifest"
	"uc-pxx-installer/internal/plan"
	"uc-pxx-installer/internal/step2ui"
)

// Version is the tool name in the plan.
const Version = "uc-pxx-step2"

const (
	// StateDir holds the step-2 records on the root file system of the slot
	// that runs (not under a tsx-data folder: the migration does not move
	// it, and the finish in the other slot reads it from this partition).
	StateDir = "/var/lib/uc-pxx-step2"
	// RecordFile is the step-2 plan of the last start, written before its
	// first write.
	RecordFile = "step2-plan.json"
	// StateFile has one line per event of a run: "started ID", "armed ID".
	StateFile = "state"
	// AttemptsFile is the record of the automatic attempts (step2ui).
	AttemptsFile = "attempts"
	// NeverFile is "do not ask again".
	NeverFile = "never"
	// DoneFile: the finish completed (the end state is reached).
	DoneFile  = "done"
	maxRecord = 4 << 20
)

// Options of a session. The fields under "tests" are refused in a release
// build (testBuild false).
type Options struct {
	Device  string // /dev/mmcblk0
	Payload string // the uc-pxx-mainline folder (plan, start)
	// Data: "auto" (the data steps unless userdata is a finished /data),
	// "yes" or "no".
	Data       string
	Confirmed  bool // the user held OK 3 s on the panel (or typed it)
	Auto       bool // the countdown of an allowed AUTO-STEP2 ran out
	Supervised bool // a supervised run: every A/B write waits for its GO line
	PlanFile   string
	JSONOut    string
	DryRun     bool   // finish: plan only
	ExpectID   string // finish: run only the finish plan of this id (the reviewed dry run)
	MaxWrite   int
	// Confirm is where a supervised run reads its GO lines (stdin).
	Confirm io.Reader
	Out     io.Writer

	// tests
	Hooks      *install.Hooks
	Gate       *mainline.Gate
	KeyPins    []plan.KeyPin
	Manifests  []*manifest.Manifest
	DataRunner DataRunner
	Live       *Live // the checks of the running system (nil: LiveChecks)
	// RecordReader reads the step-2 record of a slot's root file system
	// (nil: package ext4 on system_<slot>).
	RecordReader func(d *device.Disk, slot, file string) ([]byte, error)
	FinishCut    *install.Cutter
	// Checkpoint replaces the journal checkpoint of the running root (nil:
	// FIFREEZE + FITHAW of /; never in a test root).
	Checkpoint func() error
}

func (o *Options) testOnly() string {
	switch {
	case o.Hooks != nil:
		return "Hooks"
	case o.Gate != nil:
		return "Gate"
	case o.KeyPins != nil:
		return "KeyPins"
	case o.Manifests != nil:
		return "Manifests"
	case o.DataRunner != nil:
		return "DataRunner"
	case o.Live != nil:
		return "Live"
	case o.RecordReader != nil:
		return "RecordReader"
	case o.FinishCut != nil:
		return "FinishCut"
	case o.Checkpoint != nil:
		return "Checkpoint"
	}
	return ""
}

type session struct {
	e    *device.Env
	o    Options
	d    *device.Disk
	ru   plan.RunningSlot
	live *Live
	out  io.Writer
}

func refuse(f string, a ...any) error { return &device.Refusal{Why: fmt.Sprintf(f, a...)} }

func (s *session) say(f string, a ...any) { fmt.Fprintf(s.out, f+"\n", a...) }

// open checks the environment: the options, a slot boot of a UC-P10 eMMC.
func open(e *device.Env, o Options) (*session, error) {
	if o.Out == nil {
		o.Out = io.Discard
	}
	if t := o.testOnly(); t != "" && !testBuild {
		return nil, refuse("the option %s exists only in a devtest build", t)
	}
	if o.Device != device.EMMCPath {
		return nil, refuse("--device must be %s", device.EMMCPath)
	}
	if o.Data == "" {
		o.Data = "auto"
	}
	if o.Data != "auto" && o.Data != "yes" && o.Data != "no" {
		return nil, refuse("--data must be auto, yes or no")
	}
	if o.Supervised && o.Auto {
		return nil, refuse("a supervised run never starts by itself (--auto)")
	}
	d, err := e.OpenEMMCSlot(o.Device)
	if err != nil {
		return nil, err
	}
	s := &session{e: e, o: o, d: d, out: o.Out, live: o.Live}
	if s.live == nil {
		s.live = LiveChecks(e)
	}
	if s.ru, err = e.RunningFacts(d); err != nil {
		return nil, err
	}
	if !s.ru.UBootArgs || s.ru.RAMImage || (s.ru.Slot != "a" && s.ru.Slot != "b") {
		return nil, refuse("not a slot boot of slot A or B (u-boot,bootargs %v, RAM image %v, slot %q): uc-pxx-step2 runs only in a booted mainline slot",
			s.ru.UBootArgs, s.ru.RAMImage, s.ru.Slot)
	}
	s.ru.NetworkUp = s.live.NetworkUp != nil && s.live.NetworkUp()
	if _, err := os.Lstat(e.Path(step2ui.SupervisedMarker)); err == nil {
		s.o.Supervised = true // the unit's fixed marker (review T4 NIT-4)
		if s.o.Auto {
			return nil, refuse("%s exists: a supervised unit never starts step 2 by itself", step2ui.SupervisedMarker)
		}
	}
	return s, nil
}

func (s *session) close() {
	if s.d != nil {
		s.d.Unlock()
	}
}

func other(slot string) string {
	if slot == "a" {
		return "b"
	}
	return "a"
}

// ident is the identity of the payload folder now.
func (s *session) ident() (string, error) {
	abs, err := filepath.Abs(s.o.Payload)
	if err != nil {
		return "", err
	}
	// the identity of each contract file (lstat: size, modification and
	// change time, device, inode), as mldrive.Ident gives it
	var parts []string
	for _, n := range []string{mainline.ManifestFile, mainline.TemplateFile, mainline.RootfsFile} {
		id, err := drivestate.Stat(filepath.Join(abs, n))
		if err != nil {
			return "", err
		}
		parts = append(parts, n+" "+id.String())
	}
	return "folder " + abs + ": " + strings.Join(parts, "; "), nil
}

// mounts: the keeper's system partition must be the root; userdata may be
// mounted only for a plan without data steps; nothing else of the eMMC.
func (s *session) mounts(data bool) error {
	ms, err := s.e.SlotMounts(s.d)
	if err != nil {
		return err
	}
	root := false
	for _, m := range ms {
		switch {
		case m.Part == "system_"+s.ru.Slot && m.Point == "/":
			root = true
		case m.Part == "system_"+s.ru.Slot:
			return refuse("system_%s is mounted on %s too", s.ru.Slot, m.Point)
		case m.Part == plan.DataPartition && !data:
		default:
			return refuse("%s (%s) is mounted on %s: step 2 needs no eMMC file system mounted but the running root%s", m.Dev,
				m.Part, m.Point, map[bool]string{true: "", false: " and /data"}[data])
		}
	}
	if !root {
		return refuse("system_%s is not the mounted root file system", s.ru.Slot)
	}
	return nil
}

// dataWanted decides the data steps: "auto" = unless userdata is a finished
// /data already (a resumed run after the relabel, or a repair).
func (s *session) dataWanted() (bool, error) {
	switch s.o.Data {
	case "yes":
		return true, nil
	case "no":
		return false, nil
	}
	ud, ok := s.d.Table.Find(plan.DataPartition)
	if !ok {
		return false, refuse("no %s partition", plan.DataPartition)
	}
	v := s.d.View()
	defer v.Close()
	st := plan.DataState(v, ud.Start*gpt.Sector)
	if strings.HasPrefix(st, "unreadable") {
		return false, refuse("userdata: %s", st)
	}
	return st != "ext4 label "+plan.DataLabel, nil
}

// compute makes the plan from the live unit. ml is the payload of pass 1
// (nil: pass 1 runs now).
func (s *session) compute(ml *mainline.Payload, confirm plan.ConfirmGate) (*plan.OtherSlotPlan, *mainline.Payload, error) {
	if s.o.Payload == "" {
		return nil, nil, refuse("no --payload folder")
	}
	data, err := s.dataWanted()
	if err != nil {
		return nil, nil, err
	}
	if err := s.mounts(data); err != nil {
		return nil, nil, err
	}
	ident, err := s.ident()
	if err != nil {
		return nil, nil, refuse("the payload folder: %v", err)
	}
	opened := false
	if ml == nil {
		src, err := mainline.DirSource(s.o.Payload)
		if err != nil {
			return nil, nil, refuse("the payload folder: %v", err)
		}
		if ml, err = mainline.Verify(src); err != nil {
			return nil, nil, refuse("pass 1: %v", err)
		}
		opened = true
	}
	fail := func(err error) (*plan.OtherSlotPlan, *mainline.Payload, error) {
		if opened {
			ml.Close()
		}
		return nil, nil, err
	}
	if now, err := s.ident(); err != nil || now != ident {
		return fail(refuse("the payload folder changed while pass 1 read it"))
	}
	info, err := s.d.SlotBootctlInfo()
	if err != nil {
		return fail(refuse("%v", err))
	}
	st, err := s.d.SlotBootctlStatus()
	if err != nil {
		return fail(refuse("%v", err))
	}
	ms := s.o.Manifests
	if ms == nil {
		if ms, err = manifest.Embedded(); err != nil {
			return fail(refuse("the embedded manifests: %v", err))
		}
	}
	gate := s.o.Gate
	if gate == nil {
		gate = mainline.EmbeddedGate()
	}
	view := s.d.View()
	defer view.Close()
	ru := s.ru
	in := plan.OtherSlotInput{Input: plan.Input{Disk: view, DiskBytes: s.d.Bytes(), Source: "device:" + device.EMMCPath, Mode: "slot",
		Tool: Version, Bootctl: info, BootctlStatus: st, HashKeeper: true, Device: &s.d.ID,
		RequireBootctlSHA256: s.e.BootctlSHA256(), FirmwareIdent: ident},
		Running: ru, Mainline: ml, Manifests: ms, Gate: gate, Confirm: confirm, Data: data}
	if s.o.KeyPins != nil {
		in.KeyPins, in.KeyPinSet = s.o.KeyPins, plan.KeyPinSetTest
	}
	p, err := plan.ComputeOtherSlot(in)
	if err != nil {
		if p != nil {
			p.Render(s.out)
		}
		return fail(refuse("%v", err))
	}
	return p, ml, nil
}

func (s *session) confirmGate() plan.ConfirmGate {
	return plan.ConfirmGate{Confirmed: s.o.Confirmed || s.o.Auto, Auto: s.o.Auto}
}

// Plan makes the plan (read only) and prints it; with JSONOut it writes the
// plan file that Start runs.
func Plan(e *device.Env, o Options) (*plan.OtherSlotPlan, error) {
	s, err := open(e, o)
	if err != nil {
		return nil, err
	}
	defer s.close()
	p, ml, err := s.compute(nil, s.confirmGate())
	if err != nil {
		return nil, err
	}
	defer ml.Close()
	p.Render(s.out)
	s.say("plan id: %s", p.ID)
	if o.JSONOut != "" {
		if err := writeJSON(o.JSONOut, p, false); err != nil {
			return p, err
		}
	}
	return p, nil
}

// ReadPlanFile reads an other-slot plan file and checks its id.
func ReadPlanFile(path string) (*plan.OtherSlotPlan, error) {
	b, err := readRegular(path, maxRecord)
	if err != nil {
		return nil, refuse("the plan file: %v", err)
	}
	var p plan.OtherSlotPlan
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, refuse("the plan file: %v", err)
	}
	if p.Kind != plan.OtherSlotKind || !p.VerifyID() {
		return nil, refuse("%s is not a sealed other-slot plan", path)
	}
	return &p, nil
}

// Result is the end of a start.
type Result struct {
	Plan *plan.OtherSlotPlan
	Run  *install.Result
}

// Start runs step 2. See the package comment for the order.
func Start(e *device.Env, o Options) (*Result, error) {
	s, err := open(e, o)
	if err != nil {
		return nil, err
	}
	defer s.close()
	if !o.Confirmed && !o.Auto {
		return nil, refuse("step 2 starts only after the confirmation on the panel (the 3 s hold of OK) or the allowed countdown")
	}
	var p1 *plan.OtherSlotPlan
	if o.PlanFile == "" {
		return nil, refuse("start runs the plan file of `uc-pxx-step2 plan` (--plan): the plan that was shown and confirmed")
	}
	if p1, err = ReadPlanFile(o.PlanFile); err != nil {
		return nil, err
	}
	// the locks for the whole run, before the plan is computed again
	unlock, err := s.live.lockAutoupdate()
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := s.d.Lock(); err != nil {
		return nil, refuse("the A/B lock: %v", err)
	}
	if err := s.live.quiet(); err != nil {
		return nil, err
	}
	s.say("re-check: the plan again from the live unit (pass 1 of the payload again; nothing is written yet)")
	p2, ml, err := s.compute(nil, s.confirmGate())
	if err != nil {
		return nil, err
	}
	defer ml.Close()
	if p2.ID != p1.ID {
		return nil, refuse("the plan now has id %s, %s has %s: make a new plan and confirm it again", p2.ID, o.PlanFile, p1.ID)
	}
	s.say("re-check: the same plan id %s", p2.ID)
	if err := s.d.CanWriteABSlot(); err != nil {
		return nil, refuse("the A/B steps cannot run: %v", err)
	}
	dir := e.Path(StateDir)
	if err := writeJSON(filepath.Join(dir, RecordFile), p2, true); err != nil {
		return nil, refuse("the step-2 record: %v", err)
	}
	if err := appendLine(filepath.Join(dir, StateFile), fmt.Sprintf("started %s supervised=%v auto=%v\n", p2.ID, s.o.Supervised, o.Auto)); err != nil {
		return nil, refuse("the step-2 state: %v", err)
	}
	// the finish in T reads the record with a raw ext4 reader, which does not
	// replay the journal: checkpoint it now, before the first write (review
	// STEP2-REST-1 MINOR-1); a failure refuses, nothing is written
	if err := s.checkpoint(); err != nil {
		return nil, refuse("the journal checkpoint of the root file system after the step-2 record: %v", err)
	}
	logPath := filepath.Join(dir, "run-"+p2.ID[:16]+".jsonl")
	lf, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, refuse("the log: %v", err)
	}
	defer lf.Close()
	dr := s.o.DataRunner
	if dr == nil {
		dr = PinnedDataTool(e)
	}
	guard := s.guard(p2)
	opt := install.OtherSlotOptions{Options: install.Options{Say: func(l string) { s.say("%s", l) }, Log: lf, Hooks: s.o.Hooks,
		MaxWrite: s.o.MaxWrite, Guard: guard},
		Data: s.dataHook(dr), Gate: s.o.Gate, BootID: s.ru.BootID}
	if opt.Gate == nil {
		opt.Gate = mainline.EmbeddedGate()
	}
	if s.o.KeyPins != nil {
		opt.KeyPins, opt.KeyPinSet = s.o.KeyPins, plan.KeyPinSetTest
	}
	m := s.d.SlotMedium(s.o.MaxWrite)
	s.say("")
	res, rerr := install.RunOtherSlot(p2, ml, m, opt)
	sec, aerr := s.d.ReadABFresh()
	s.say("")
	s.say("A/B now: %s", install.SafeStateOf(sec, aerr, &p2.Plan))
	if rerr != nil {
		if errors.Is(rerr, install.ErrABUnknown) {
			return &Result{Plan: p2, Run: res}, fmt.Errorf("%w: %v", install.ErrABUnknown, rerr)
		}
		return &Result{Plan: p2, Run: res}, rerr
	}
	if err := appendLine(filepath.Join(dir, StateFile), "armed "+p2.ID+"\n"); err != nil {
		return &Result{Plan: p2, Run: res}, fmt.Errorf("slot %s is armed, but the state line was not written: %v", strings.ToUpper(p2.Target), err)
	}
	if err := s.checkpoint(); err != nil {
		s.say("WARNING: the journal checkpoint after the arm failed (%v): restart only with `sync; reboot`, never by a power cut", err)
	}
	s.say("step 2 done: slot %s is armed (trying) and boots next; slot %s stays successful as the fallback. Restart with: sync; reboot",
		strings.ToUpper(p2.Target), strings.ToUpper(p2.Keeper))
	return &Result{Plan: p2, Run: res}, nil
}

// guard is install.Options.Guard: the payload folder must stay unchanged up
// to the arm; in a supervised run each A/B write and the data-format wait
// for the line "go N ID12" (the step number and the first 12 hex digits of
// the plan id) that the person types after the rule-9 GO; any other line,
// or none, stops before the step (the target stays as it is: untouched before
// the disable, disabled after it, so the keeper boots).
func (s *session) guard(p *plan.OtherSlotPlan) func(plan.Step) error {
	armed := false
	var rd *bufio.Reader
	if s.o.Confirm != nil {
		rd = bufio.NewReader(s.o.Confirm)
	}
	return func(st plan.Step) error {
		if !armed {
			if now, err := s.ident(); err != nil || now != p.FirmwareIdent {
				return fmt.Errorf("the payload folder changed: now %s (%v), the plan has %s", now, err, p.FirmwareIdent)
			}
		}
		pause := st.Op == "bootctl" || st.Op == plan.OpDataFormat
		if st.Op == "bootctl" && len(st.Bootctl.Args) > 0 && st.Bootctl.Args[0] == "arm-trying" {
			armed = true
		}
		if !s.o.Supervised || !pause {
			return nil
		}
		want := fmt.Sprintf("go %d %s", st.N, p.ID[:12])
		s.say("SUPERVISED: before step %d (%s): %s", st.N, st.Op, stepText(st))
		s.say("SUPERVISED: after the rule-9 GO type exactly %q and Enter; anything else stops here", want)
		if rd == nil {
			return errors.New("supervised: no input for the GO line")
		}
		line, _ := rd.ReadString('\n')
		if strings.TrimSpace(line) != want {
			return fmt.Errorf("supervised: the line %q is not %q: stopped before step %d", strings.TrimSpace(line), want, st.N)
		}
		return nil
	}
}

func stepText(st plan.Step) string {
	if st.Bootctl != nil {
		return fmt.Sprintf("uc-pxx-bootctl-inst %s: A/B %s -> %s", strings.Join(st.Bootctl.Args, " "), st.Bootctl.Before, st.Bootctl.After)
	}
	return st.Why
}

// checkpoint writes the journal of the running root file system to its home
// blocks: FIFREEZE then FITHAW of / (ext4_freeze flushes the jbd2 journal).
// Every writer of / waits for the few milliseconds in between. In a test
// root it is the test's Checkpoint, never a freeze of the host.
func (s *session) checkpoint() error {
	if s.o.Checkpoint != nil {
		return s.o.Checkpoint()
	}
	if s.e.IsTest() {
		return errors.New("no checkpoint in a test root (set Options.Checkpoint)")
	}
	f, err := os.Open("/")
	if err != nil {
		return err
	}
	defer f.Close()
	const fifreeze, fithaw = 0xC0045877, 0xC0045878 // _IOWR('X', 119/120, int)
	if _, _, en := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), fifreeze, 0); en != 0 {
		return fmt.Errorf("FIFREEZE /: %v", en)
	}
	if _, _, en := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), fithaw, 0); en != 0 {
		// try again: a frozen root stops the system
		if _, _, en2 := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), fithaw, 0); en2 != 0 {
			return fmt.Errorf("FITHAW /: %v, %v", en, en2)
		}
	}
	return nil
}

// writeJSON writes v as JSON: a new temporary file, fsync, rename, fsync of
// the folder (durable: the finish reads it after the restart).
func writeJSON(path string, v any, durable bool) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, append(b, '\n'), durable)
}

func writeFile(path string, b []byte, durable bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if st, err := os.Lstat(path); err == nil && !st.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	tmp := fmt.Sprintf("%s.tmp-%d", path, os.Getpid())
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	if durable {
		return syncDir(dir)
	}
	return nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// appendLine appends one line and makes it durable (fsync of the file and of
// the folder) before it returns.
func appendLine(path, line string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(line)
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

// readRegular reads a regular file (no symlink, no device) of at most max bytes.
func readRegular(path string, max int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > max {
		return nil, fmt.Errorf("%s is not a regular file of at most %d bytes", path, max)
	}
	return io.ReadAll(io.LimitReader(f, max+1))
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
