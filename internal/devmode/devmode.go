//go:build install

// Package devmode is the flow of the device mode: `p10inst install --device
// /dev/mmcblk0` in the installer RAM image. It joins the checks of package
// device, the planner, the confirmation and the executor:
//
//  1. CheckRAMImage: the installer RAM image, not a booted system. Its
//     token (uc-pxx.id=p10inst-XXXXXX) is printed, and it goes into the plan
//     (and so into the plan id) and into the JSON-lines log.
//  2. OpenEMMC: exactly /dev/mmcblk0, and only the eMMC of a UC-P10 with the
//     expected layout in sysfs and in both GPT copies.
//  3. The A/B lock for the whole run.
//  4. The plan, with pass 1 over the whole firmware and the keeper hashed.
//     It is printed with its id. A refused plan ends here.
//  5. With --plan FILE: the computed id must be the id in the file.
//  6. The confirmation: --yes, or the word INSTALL typed by a person.
//  7. The plan is computed again from scratch. Its id must be the same.
//  8. The executor runs it (package install, the same steps as a dry run).
//
// Nothing is written before step 8.
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
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/pcdfw"
	"uc-pxx-installer/internal/plan"
)

// Version is the tool version that goes into the plan.
const Version = "p10inst device mode 2026-10-28"

// Options are the options of the device mode.
type Options struct {
	Device           string   // must be /dev/mmcblk0
	FW               string   // the firmware file
	Keys             string   // a key file (optional)
	Libs             []string // libraries to find the key in (optional)
	Keeper           string   // "", "a" or "b"
	PrereqsConfirmed bool
	// AutoUpdateUnclearConfirmed: go on although the Android auto-update
	// state is not clear (plan.Input.AutoUpdateUnclearConfirmed).
	AutoUpdateUnclearConfirmed bool
	PlanFile                   string // the plan JSON that a person approved (optional)
	JSONOut                    string // write the plan JSON here (optional)
	Yes                        bool   // the confirmation is given
	Confirm                    io.Reader
	LogPath                    string // JSON lines (default /run/p10inst/install-ID.jsonl)
	// Tests only:
	Hooks        *install.Hooks
	MaxWrite     int
	AfterConfirm func()
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
	token string
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
	tok, err := e.CheckRAMImage()
	if err != nil {
		return nil, err
	}
	s.token = tok
	s.say("environment: the installer RAM image, token %s (%s, %s, uc-pxx.id=%s), no eMMC file system mounted",
		tok, device.RAMImageMarker, device.RAMCmdline, tok)
	d, err := e.OpenEMMC(o.Device)
	if err != nil {
		return nil, err
	}
	s.say("eMMC: %s is the eMMC of a %s on %s; sysfs and both GPT copies have the UC-P10 layout (%d partitions)",
		device.EMMCPath, d.ID.Model, d.ID.Host, len(d.Table.Entries))
	if off := d.ROWasOff(); len(off) > 0 {
		s.say("eMMC: the kernel read-only flag was OFF on %d device(s) (%s), probably after a run that was killed; it is set again now",
			len(off), strings.Join(off, " "))
	}
	s.say("eMMC: kernel read-only flag set on the disk and on every partition")
	if err := d.Lock(); err != nil {
		return nil, &device.Refusal{Why: err.Error()}
	}
	s.d = d
	return s, nil
}

func (s *session) close() {
	if s.d != nil {
		s.d.Unlock()
	}
}

// compute makes the plan from scratch: the key, pass 1 over the whole
// firmware, the bootctl binary and its status, and every check.
func (s *session) compute(quiet bool) (*plan.Plan, *plan.Payload, error) {
	say := func(l string) {
		if !quiet {
			s.say("%s", l)
		}
	}
	e, d := s.e, s.d
	ms, err := e.Manifests()
	if err != nil {
		return nil, nil, err
	}
	fw, err := os.Open(s.o.FW)
	if err != nil {
		return nil, nil, &device.Refusal{Why: err.Error()}
	}
	st, err := fw.Stat()
	if err != nil || !st.Mode().IsRegular() {
		fw.Close()
		return nil, nil, &device.Refusal{Why: s.o.FW + " is not a regular file"}
	}
	view := d.View()
	defer view.Close()
	var k pcdfw.Keys
	var keySlots []string
	keySource := ""
	switch {
	case s.o.Keys != "" && len(s.o.Libs) > 0:
		return nil, nil, &device.Refusal{Why: "give a key file or libraries, not both"}
	case s.o.Keys != "":
		if k, err = pcdfw.LoadKeyFile(s.o.Keys); err != nil {
			return nil, nil, &device.Refusal{Why: err.Error()}
		}
		keySource = "key file"
	case len(s.o.Libs) > 0:
		var libs []pcdfw.Lib
		for _, p := range s.o.Libs {
			b, err := os.ReadFile(p)
			if err != nil {
				return nil, nil, &device.Refusal{Why: err.Error()}
			}
			libs = append(libs, pcdfw.Lib{Name: filepath.Base(p), Data: b})
		}
		res, err := pcdfw.FindKeys(fw, st.Size(), libs, false)
		if err != nil || len(res.Pairs) != 1 {
			return nil, nil, &device.Refusal{Why: fmt.Sprintf("key search in the libraries: %d pairs (%v)", len(res.Pairs), err)}
		}
		k = res.Pairs[0]
		keySource = "libraries given with --lib"
	default:
		if k, keySlots, err = KeysFromDisk(view, d.Bytes(), fw, st.Size(), say); err != nil {
			return nil, nil, &device.Refusal{Why: err.Error()}
		}
	}
	say("pass 1: check the whole firmware against the manifests (nothing is written)")
	pl, err := plan.VerifyOfficial(fw, st.Size(), k, ms, func(l string) { say("  " + l) })
	if err != nil {
		return nil, nil, &device.Refusal{Why: "pass 1: " + err.Error()}
	}
	_, sha, berr := e.BootctlBinary()
	if berr != nil {
		say("uc-pxx-bootctl-inst: " + berr.Error())
	}
	var status *plan.BootctlStatus
	if berr == nil {
		if status, err = d.BootctlStatus(); err != nil {
			say("uc-pxx-bootctl-inst status: " + err.Error())
		}
	}
	id := d.ID
	p, err := plan.Compute(plan.Input{Disk: view, DiskBytes: d.Bytes(), Source: "device:" + device.EMMCPath, Mode: "device",
		Tool: Version, Keeper: s.o.Keeper, Payload: pl, Bootctl: plan.Bootctl{Path: device.BootctlPath, SHA256: sha},
		BootctlStatus: status, PrereqsConfirmed: s.o.PrereqsConfirmed, AutoUpdateUnclearConfirmed: s.o.AutoUpdateUnclearConfirmed, HashKeeper: true, KeySlots: keySlots,
		KeySource: keySource, Device: &id, RAMImage: &plan.RAMImage{Token: s.token}, RequireBootctlSHA256: e.BootctlSHA256(),
		ApprovedResources: e.ApprovedResources()})
	if err != nil {
		if !quiet {
			s.say("")
			p.Render(s.out)
		}
		return p, pl, &device.Refusal{Why: err.Error()}
	}
	return p, pl, nil
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
	for _, p := range append([]string{o.FW, o.Keys, o.PlanFile}, o.Libs...) {
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
	p1, _, err := s.compute(false)
	if err != nil {
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
	if o.PlanFile != "" {
		raw, err := os.ReadFile(o.PlanFile)
		if err != nil {
			return nil, &device.Refusal{Why: err.Error()}
		}
		var want plan.Plan
		if err := json.Unmarshal(raw, &want); err != nil {
			return nil, &device.Refusal{Why: fmt.Sprintf("%s: %v", o.PlanFile, err)}
		}
		if want.ID == "" || !want.VerifyID() {
			return nil, &device.Refusal{Why: fmt.Sprintf("%s: its content does not match its id %s (an edited plan)", o.PlanFile, want.ID)}
		}
		if want.ID != p1.ID {
			return nil, &device.Refusal{Why: fmt.Sprintf("the plan now has id %s, %s has %s", p1.ID, o.PlanFile, want.ID)}
		}
		s.say("the plan is the approved plan %s", o.PlanFile)
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
	s.say("computing the plan again from the unit and the firmware (nothing is written yet)")
	p2, pl2, err := s.compute(true)
	if err != nil {
		return nil, err
	}
	if p2.ID != p1.ID {
		return nil, &device.Refusal{Why: fmt.Sprintf("the plan changed after it was shown: id %s, shown %s", p2.ID, p1.ID)}
	}
	s.say("the new plan has the same id %s", p2.ID)

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
	res, err := install.Run(p2, pl2, m, install.Options{Say: func(l string) { s.say("%s", l) }, Log: lf, Hooks: o.Hooks,
		MaxWrite: o.MaxWrite})
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

func first32(b []byte) [32]byte {
	var r [32]byte
	copy(r[:], b)
	return r
}
