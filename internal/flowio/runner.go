// Package flowio runs the on-screen flow (package flow) on the unit: the
// reads of the status, the drive and the A/B sector, the children, and the
// log bundle. It writes no byte of the eMMC: it does not import the install
// side (ci/goallow), it opens the eMMC only through package blk (O_RDONLY,
// O_DIRECT for the A/B sector and the BCB), and its only writes are its own
// files in /run, the RAM drive file while the host does not have it, the
// lun of the gadget, and the LEDs. The device mode runs as a child process
// (`p10inst install --device /dev/mmcblk0 ...`) under a supervisor, with
// exactly the arguments of flow.InstallArgs.
//
// It is in the install build only (check-no-writer.py: install-only).
package flowio

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/blk"
	"uc-pxx-installer/internal/drivestate"
	"uc-pxx-installer/internal/fatfs"
	"uc-pxx-installer/internal/fatmk"
	"uc-pxx-installer/internal/flow"
	"uc-pxx-installer/internal/gadget"
	"uc-pxx-installer/internal/manifest"
	"uc-pxx-installer/internal/plan"
	"uc-pxx-installer/internal/status"
)

// Config is where the runner works. On the unit every path is the real one
// (Root "/"); the tests give a test root.
type Config struct {
	Root      string // "/" on the unit
	Exe       string // the p10inst of the image, for the children
	Manifests []*manifest.Manifest
	// ChildArgs are added to the device-mode children: the test root and
	// the fake unit, set only by the flags of a devtest build. Empty in a
	// release build.
	ChildArgs []string
	ChildEnv  []string // devtest only (UC_PXX_BOOTCTL_EXEC)
	// Readonly runs `p10inst readonly` (nil: the child with Exe).
	Readonly func() (string, error)
	Restart  func()
	LED      func(color string)
	Log      func(string)
	Now      func() time.Time
	Proc     string // the process table, "/proc"
}

// The paths of the unit (inside Root).
const (
	RunDir    = "/run/p10inst"
	FlowDir   = "/run/p10inst/flow"
	GadgetDir = "/sys/kernel/config/usb_gadget/p10inst"
	ABLock    = "/run/uc-pxx-abdata.lock"
)

// Runner is the flow on the unit.
type Runner struct {
	c       Config
	M       *flow.Model
	ev      chan func()
	child   *child
	n       int
	st      runState
	report  *status.Report
	statusB *status.Report // before the install
	ending  bool
	log     []string
	screen  flow.Screen
	// the drive file at the last poll (the copy progress)
	lastDrive drivestate.Ident
}

// runState is DIR/state.json: what a new UI process needs.
type runState struct {
	Persisted flow.Persisted `json:"flow"`
	ChildDir  string         `json:"child_dir,omitempty"`
	PlanJSON  string         `json:"plan_json,omitempty"`
	Log       string         `json:"log,omitempty"`
	Ident     string         `json:"drive_ident,omitempty"`
	Before    string         `json:"ab_before_hex,omitempty"` // the 512 bytes before the install
	BCBBefore string         `json:"bcb_before_hex,omitempty"`
	Keeper    string         `json:"keeper,omitempty"`
	Prereqs   bool           `json:"prereqs_confirmed"`
	Unclear   bool           `json:"autoupdate_unclear_confirmed"`
	Started   time.Time      `json:"install_started,omitempty"`
	PlanStart time.Time      `json:"plan_started,omitempty"`
	PlanChild string         `json:"plan_child,omitempty"`
	PlanEnd   time.Time      `json:"plan_ended,omitempty"`
}

// New makes the runner.
func New(c Config, m *flow.Model) *Runner {
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Proc == "" {
		c.Proc = "/proc"
	}
	if c.Log == nil {
		c.Log = func(string) {}
	}
	return &Runner{c: c, M: m, ev: make(chan func(), 64)}
}

// Events are the results of the background work, to run on the main loop.
func (r *Runner) Events() <-chan func() { return r.ev }

func (r *Runner) path(p string) string {
	if r.c.Root == "" || r.c.Root == "/" {
		return p
	}
	return filepath.Join(r.c.Root, p)
}

func (r *Runner) say(f string, a ...any) {
	l := r.c.Now().UTC().Format("15:04:05") + " " + fmt.Sprintf(f, a...)
	r.log = append(r.log, l)
	r.c.Log(fmt.Sprintf(f, a...))
	writeRunFile(r.path(RunDir), r.path(FlowDir+"/flow.log"), []byte(strings.Join(r.log, "\n")+"\n"), 0o644)
}

func (r *Runner) post(f func()) { r.ev <- f }

func (r *Runner) save() {
	r.st.Persisted = r.M.Persist()
	b, _ := json.MarshalIndent(r.st, "", "  ")
	if err := writeRunFile(r.path(RunDir), r.path(FlowDir+"/state.json"), append(b, '\n'), 0o644); err != nil {
		r.say("state: %v", err)
	}
}

// Start reads the state of an earlier UI process and starts the status read.
func (r *Runner) Start() {
	if b, err := os.ReadFile(r.path(FlowDir + "/state.json")); err == nil && json.Unmarshal(b, &r.st) == nil {
		var p *plan.Plan
		if r.st.PlanJSON != "" {
			p, _ = readPlan(r.st.PlanJSON)
		}
		r.M.Resume(r.st.Persisted, p)
		if r.st.Persisted.Running == "install" && r.st.ChildDir != "" {
			r.child = &child{kind: "install", dir: r.st.ChildDir}
			pr := flow.NewProgress(true, r.st.Started)
			if p != nil {
				pr.SetPlan(flow.StepsOf(p))
			}
			r.M.ChildStarted(pr)
			r.say("UI started again: an install runs (%s); following it", r.st.ChildDir)
		} else {
			r.say("UI started again: %s", r.M.Screen)
		}
	} else {
		r.say("flow started")
	}
	r.readStatus(false)
}

func (r *Runner) source() status.Source {
	root := r.c.Root
	if root == "" {
		root = "/"
	}
	return status.Source{Root: root, Manifests: r.c.Manifests}
}

func (r *Runner) readStatus(after bool) {
	go func() {
		rep := status.Collect(r.source())
		r.post(func() {
			r.report = rep
			u := r.unit(rep)
			r.M.SetUnit(u)
			r.say("status: A/B valid %v, next %d, auto-update %s", u.ABValid, u.Next, rep.AutoUpdate.State)
		})
	}()
}

// unit is the status for the flow, with the A/B state from the flow's own
// O_DIRECT read of the sector (the status reads it through the page cache
// of the whole disk): the keeper screen and the restart text rest on it.
func (r *Runner) unit(rep *status.Report) flow.Unit {
	u := flow.UnitFrom(rep)
	sec, err := r.readAB()
	if err != nil {
		u.ABValid, u.ABErr, u.Next = false, "the A/B sector cannot be read: "+err.Error(), -1
		return u
	}
	a, _ := avbab.Parse(sec)
	if err := a.Validate(); err != nil {
		u.ABValid, u.ABErr, u.Next = false, err.Error(), -1
		return u
	}
	u.ABValid, u.ABErr, u.Next = true, "", a.Pick()
	for i := range u.Slots {
		u.Slots[i].AB = a.Slots[i]
	}
	return u
}

// Poll runs every few hundred ms on the main loop: the drive state and the
// output and end of a child.
func (r *Runner) Poll() {
	empty, _, err := drivestate.LunEmpty(r.path(GadgetDir + "/functions/mass_storage.usb0/lun.0/file"))
	r.M.DriveEjected(err == nil && empty)
	if id, err := drivestate.Stat(r.drive()); err == nil && !(err == nil && empty) {
		if r.lastDrive != (drivestate.Ident{}) && id.MtimeNS != r.lastDrive.MtimeNS {
			r.M.DriveWrites(r.c.Now())
		}
		r.lastDrive = id
	}
	if r.child == nil || r.ending {
		return
	}
	now := r.c.Now()
	for _, l := range r.child.lines() {
		r.M.ChildLine(l, now)
	}
	end, ok := r.child.end()
	if !ok && !r.child.alive(r.c.Proc) {
		end, ok = flow.End{Lost: true}, true
	}
	if !ok {
		return
	}
	for _, l := range r.child.lines() {
		r.M.ChildLine(l, now)
	}
	ch := r.child
	r.ending = true
	switch ch.kind {
	case "plan":
		r.planEnded(ch, end)
	case "install":
		r.say("install child ended: %s", end)
		go r.installEnded(ch, end)
	}
}

// Do runs the actions of the model.
func (r *Runner) Do(acts []flow.Action) {
	for _, a := range acts {
		r.say("action %s%s", a.Kind, map[bool]string{true: " (make the drive new)"}[a.Remake])
		switch a.Kind {
		case flow.ActEject:
			if err := ejectDrive(r.path(GadgetDir)); err != nil {
				r.say("eject: %v", err)
			}
		case flow.ActReadDrive:
			go r.readDrive()
		case flow.ActGiveBackDrive:
			r.giveBack(a.Remake, nil)
		case flow.ActStartPlan:
			r.startPlan(a)
		case flow.ActCancelPlan:
			if err := cancelPlan(r.child); err != nil {
				r.say("cancel: %v", err)
			}
		case flow.ActStartInstall:
			r.startInstall()
		case flow.ActReadAB:
			go func() {
				before, _ := hex.DecodeString(r.st.Before)
				after, err := r.readAB()
				p, _ := readPlan(r.st.PlanJSON)
				st, why := flow.Recheck(p, before, after, err)
				r.post(func() {
					r.say("hold: the A/B sector read again: %s (%s)", st, why)
					r.M.Rechecked(st, why)
				})
			}()
		case flow.ActRestart:
			if why := r.M.RestartBlocked(); why != "" {
				r.say("restart refused: %s", why)
				continue
			}
			r.say("restart")
			if r.c.Restart != nil {
				r.c.Restart()
			}
		}
	}
	if r.M.Screen != r.screen {
		r.screen = r.M.Screen
		r.say("screen %s", r.screen)
	}
	r.save()
	if r.c.LED != nil {
		r.c.LED(r.M.View(r.c.Now()).LED)
	}
}

// KeyFileName is the key file on the drive (INSTALLER-END-USER-PLAN.md §1.2).
const KeyFileName = "p10-keys.txt"

func (r *Runner) keyFile() string { return r.path(FlowDir + "/keys.txt") }

func (r *Runner) removeKeyFile() { removeRunFile(r.path(RunDir), r.keyFile()) }

func (r *Runner) drive() string { return r.path(drivestate.Image) }
func (r *Runner) lun() string   { return r.path(drivestate.LunFile) }

func (r *Runner) readDrive() {
	res := flow.DriveResult{}
	empty, v, err := drivestate.LunEmpty(r.lun())
	switch {
	case err != nil || !empty:
		res.Refusal = fmt.Sprintf("the drive is with the computer again (%q %v)", v, err)
	default:
		id, ierr := drivestate.Stat(r.drive())
		f, err := fatfs.OpenFirmware(r.drive())
		if err != nil {
			var rf *fatfs.Refusal
			res.Refusal = err.Error()
			if errors.As(err, &rf) {
				res.Refusal = rf.Why
				res.Remake = rf.Reason == fatfs.Dirty || rf.Reason == fatfs.Damaged || rf.Reason == fatfs.NotFAT
			}
			break
		}
		res.Name, res.Size = fatfs.Quote(f.Name()), f.Size()
		f.Close()
		// the key file of §1.2, if the drive has one: copied (mode 0600) to
		// the run folder for --keys; it is never shown and never in a bundle
		r.removeKeyFile()
		if v, err := fatfs.OpenImage(r.drive()); err == nil {
			kb, kerr := v.ReadFile("/"+KeyFileName, 4096)
			v.Close()
			if kerr == nil {
				if err := writeRunFile(r.path(RunDir), r.keyFile(), kb, 0o600); err == nil {
					res.KeyFile = true
				}
			}
		}
		id2, ierr2 := drivestate.Stat(r.drive())
		empty2, _, lerr := drivestate.LunEmpty(r.lun())
		if ierr != nil || ierr2 != nil || id != id2 || lerr != nil || !empty2 {
			res = flow.DriveResult{Refusal: "the drive changed while it was read: eject it again"}
			break
		}
		res.OK, res.Ident = true, id.String()
	}
	r.post(func() {
		if res.OK {
			r.st.Ident = res.Ident
			r.say("drive: firmware %s, %d bytes, %s", res.Name, res.Size, res.Ident)
		} else {
			r.say("drive refused: %s", res.Refusal)
		}
		r.M.DriveRead(res)
		r.Do(r.M.Take())
	})
}

// giveBack gives the drive to the host: made new first if asked (with the
// files of a bundle, or empty).
func (r *Runner) giveBack(remake bool, files []fatmk.File) error {
	r.removeKeyFile()
	if remake {
		if err := makeDriveFile(r.lun(), r.drive(), files, r.c.Now()); err != nil {
			r.say("drive: cannot make it new: %v", err)
			return err
		}
	}
	if err := attachDrive(r.path(GadgetDir), r.drive()); err != nil {
		r.say("drive: cannot give it back: %v", err)
		return err
	}
	r.say("drive given back to the computer")
	return nil
}

func (r *Runner) paths(id string) flow.Paths {
	ps := flow.Paths{Exe: r.c.Exe, Drive: r.drive(), DriveIdent: r.st.Ident, PlanJSON: r.path(FlowDir + "/plan.json"),
		Log: r.path(FlowDir + "/install-" + id + ".jsonl")}
	if _, err := os.Lstat(r.keyFile()); err == nil {
		ps.Keys = r.keyFile()
	}
	return ps
}

func (r *Runner) childDir(kind string) string {
	r.n++
	return r.path(fmt.Sprintf("%s/%s-%d-%d", FlowDir, kind, r.c.Now().Unix(), r.n))
}

func (r *Runner) startPlan(a flow.Action) {
	r.st.Keeper, r.st.Prereqs, r.st.Unclear = a.Keeper, r.M.PrereqsConfirmed(), a.Unclear
	ps := r.paths("x")
	removeRunFile(r.path(RunDir), ps.PlanJSON) // the plan of an earlier run never counts
	argv := append(flow.PlanArgs(ps, a.Keeper, r.st.Prereqs, a.Unclear), r.c.ChildArgs...)
	dir := r.childDir("plan")
	ch, err := startSupervised(&r.c, "plan", dir, argv)
	if err != nil {
		r.say("plan child: %v", err)
		r.M.PlanEnded(flow.PlanResult{Refusal: "the check could not start: " + err.Error(), End: flow.End{Exited: true, Code: 2}})
		return
	}
	r.child, r.ending = ch, false
	r.st.PlanStart, r.st.PlanChild = r.c.Now(), dir
	r.M.ChildStarted(flow.NewProgress(false, r.c.Now()))
	r.say("plan child started: %s", strings.Join(argv, " "))
}

func lastResult(out []byte) string {
	res := ""
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(l, "RESULT: ") {
			res = strings.TrimPrefix(l, "RESULT: ")
		}
	}
	return res
}

func (r *Runner) planEnded(ch *child, end flow.End) {
	r.child, r.ending = nil, false
	r.st.PlanEnd = r.c.Now()
	res := flow.PlanResult{End: end}
	if end.Exited && end.Code == 0 {
		p, err := readPlan(r.path(FlowDir + "/plan.json"))
		if err == nil {
			res.Plan = p
			r.st.PlanJSON = r.path(FlowDir + "/plan.json")
		} else {
			res.Refusal = "the plan file: " + err.Error()
		}
	} else {
		res.Refusal = lastResult(ch.output())
		res.Canceled = !end.Exited && end.Signal == "terminated" // SIGTERM: only cancelPlan sends it
	}
	r.say("plan child ended: %s; plan %v; %s", end, res.Plan != nil, res.Refusal)
	r.M.PlanEnded(res)
	r.Do(r.M.Take())
}

// readPlan reads a plan JSON and checks its id.
func readPlan(p string) (*plan.Plan, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var pl plan.Plan
	if err := json.Unmarshal(b, &pl); err != nil {
		return nil, err
	}
	if pl.ID == "" || !pl.VerifyID() {
		return nil, errors.New("the plan does not match its id")
	}
	return &pl, nil
}

func (r *Runner) disk() (*blk.Disk, error) {
	root := r.c.Root
	if root == "" {
		root = "/"
	}
	return blk.Find(root)
}

// readAB reads the A/B sector (misc + 4 sectors) with O_DIRECT.
func (r *Runner) readAB() ([]byte, error) {
	d, err := r.disk()
	if err != nil {
		return nil, err
	}
	return d.ReadPartDirect("misc", (avbab.LBA-plan.MiscStart)*512, 512)
}

// readBCB reads the BCB (misc LBA 49184, 4 sectors) with O_DIRECT.
func (r *Runner) readBCB() ([]byte, error) {
	d, err := r.disk()
	if err != nil {
		return nil, err
	}
	return d.ReadPartDirect("misc", 32*512, 2048)
}

func (r *Runner) startInstall() {
	p, err := readPlan(r.path(FlowDir + "/plan.json"))
	refuse := func(why string) {
		r.say("install not started: %s", why)
		r.M.InstallRefused(why)
		r.Do(r.M.Take())
	}
	if err != nil || r.M.Plan == nil || p.ID != r.M.Plan.ID {
		refuse(fmt.Sprintf("the plan file is not the plan on the screen (%v)", err))
		return
	}
	want, err := drivestate.Parse(r.st.Ident)
	if err == nil {
		err = drivestate.Detached(r.lun(), r.drive(), want)
	}
	if err != nil {
		refuse("drive rule: " + err.Error())
		return
	}
	before, err := r.readAB()
	if err != nil {
		refuse("the A/B sector cannot be read: " + err.Error())
		return
	}
	sum := sha256.Sum256(before)
	if hex.EncodeToString(sum[:]) != p.AB.SectorSHA256 {
		refuse("the A/B sector is not the one of the plan any more")
		return
	}
	bcb, _ := r.readBCB()
	r.statusB = r.report
	id := p.ID[:12]
	ps := r.paths(id)
	argv := append(flow.InstallArgs(ps, r.st.Keeper, r.st.Prereqs, r.st.Unclear), r.c.ChildArgs...)
	dir := r.childDir("install")
	r.st.Before, r.st.BCBBefore, r.st.ChildDir, r.st.Log, r.st.PlanJSON = hex.EncodeToString(before), hex.EncodeToString(bcb), dir, ps.Log, ps.PlanJSON
	r.st.Started = r.c.Now()
	r.st.Persisted.Running = "install"
	b, _ := json.MarshalIndent(r.st, "", "  ")
	writeRunFile(r.path(RunDir), r.path(FlowDir+"/state.json"), append(b, '\n'), 0o644) // before the start: a new UI must find it
	ch, err := startSupervised(&r.c, "install", dir, argv)
	if err != nil {
		r.st.Persisted.Running = ""
		refuse("the install could not start: " + err.Error())
		return
	}
	r.child, r.ending = ch, false
	pr := flow.NewProgress(true, r.c.Now())
	pr.SetPlan(flow.StepsOf(p))
	r.M.ChildStarted(pr)
	r.say("install child started: %s", strings.Join(argv, " "))
}

// waitQuiet waits until no uc-pxx-bootctl-inst runs and the A/B lock is
// free: after a kill of p10inst, the tool may still finish its write in
// its own session (DEVICE-MODE-REVIEW.md §3.4).
func (r *Runner) waitQuiet() {
	for i := 0; ; i++ {
		busy := r.bootctlRuns()
		if !busy {
			busy = r.lockHeld()
		}
		if !busy {
			return
		}
		if i%40 == 0 {
			r.post(func() { r.say("waiting: uc-pxx-bootctl-inst or the A/B lock is still busy") })
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func (r *Runner) bootctlRuns() bool {
	ents, _ := os.ReadDir(r.c.Proc)
	for _, e := range ents {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join(r.c.Proc, e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		for i, a := range strings.Split(string(b), "\x00") {
			if i > 1 {
				break
			}
			if filepath.Base(a) == "uc-pxx-bootctl-inst" {
				return true
			}
		}
	}
	return false
}

func (r *Runner) lockHeld() bool {
	f, err := os.Open(r.path(ABLock))
	if err != nil {
		return false // no lock file: nobody took it
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return true
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

func (r *Runner) readonly() (string, error) {
	if r.c.Readonly != nil {
		return r.c.Readonly()
	}
	return runReadonly(r.c.Exe, r.c.Root)
}

// runReadonly runs `p10inst readonly`: after a SIGKILL inside a write the
// kernel read-only flags of the disk and of that partition stay off until
// the next run (DEVICE-MODE-REVIEW.md R2.4 item 2).
func runReadonly(exe, root string) (string, error) {
	args := []string{"readonly"}
	if root != "" && root != "/" {
		args = append(args, "--root", root)
	}
	out, err := exec.Command(exe, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// installEnded runs off the main loop: it waits for the A/B writers to be
// gone, sets the read-only flags again, reads the sector, and classifies.
func (r *Runner) installEnded(ch *child, end flow.End) {
	r.waitQuiet()
	r.removeKeyFile()
	ro, roErr := r.readonly()
	after, abErr := r.readAB()
	bcb, _ := r.readBCB()
	before, _ := hex.DecodeString(r.st.Before)
	p, perr := readPlan(r.st.PlanJSON)
	var o flow.Outcome
	if perr != nil {
		o = flow.Classify(nil, before, after, abErr, end)
	} else {
		o = flow.Classify(p, before, after, abErr, end)
	}
	driveNote := "detached and unchanged to the end"
	if want, err := drivestate.Parse(r.st.Ident); err != nil {
		driveNote = err.Error()
	} else if err := drivestate.Detached(r.lun(), r.drive(), want); err != nil {
		driveNote = err.Error()
	}
	rep := status.Collect(r.source())
	r.post(func() {
		r.say("after the install: readonly %q (%v); A/B %s; %s; drive %s", ro, roErr, o.State, o.Why, driveNote)
		r.child, r.ending = nil, false
		r.report = rep
		r.M.SetUnit(r.unit(rep))
		r.M.InstallEnded(o)
		r.st.Persisted.Running = ""
		r.save()
		if r.c.LED != nil {
			r.c.LED(r.M.View(r.c.Now()).LED)
		}
		b := r.bundle(ch, p, o, before, after, abErr, bcb, ro, roErr, driveNote)
		r.Do(nil)
		go r.writeBundle(b)
	})
}

// ejectDrive takes the drive from the host (OK on the firmware screen).
func ejectDrive(g string) error { return gadget.Eject(g) }

// attachDrive gives the drive file to the host again.
func attachDrive(g, drive string) error { return gadget.Attach(g, drive) }

func fileSHA(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return "unreadable (" + err.Error() + ")"
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
