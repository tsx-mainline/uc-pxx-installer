package provision

// The service (DESIGN-R23 §3.1, §3.4, §3.6; record §6.10), in this order:
//
//  1. the conf and profiles.json; the profiles the unit offers (those that
//     this program's clone gate pins: Offer; none today, until the release
//     build fills the gate: nothing is offered, "failed: check");
//  2. a done record of an offered profile of this release: nothing to do;
//  3. the wait (at most MarkWait) until the running slot is successful and
//     the slot that boots;
//  4. the USB link (with UC_PXX_PROVISION_USB=yes): the gadget taken from
//     uc-pxx-usb, the receiver uc-pxx-xfer of the kind provision started;
//  5. the choice: the page's COMMIT {profile, source, url?} (the receiver's
//     provision.json), or the last choice with the source net of an earlier
//     boot (the next boot starts again by itself);
//  6. the work of the choice (work): the bundle into the RAM repo (net: the
//     fetch with resume and retries; cable: the receiver's hand-over) and
//     its check; the free space; tsx-autoupdate's two locks; the installed
//     packages against the bundle's base; fixed.json (the profile is fixed
//     from here on); the read-only flags of the eMMC; apk add (and apk fix
//     --reinstall after a cut install); the profile steps; the flags
//     cleared; the installed database and the content check (ref_b); the
//     done record (first as a temporary file);
//  7. done: the page reads "done", the link goes back to uc-pxx-usb, then
//     the done record is put in place (step 2 waits for it and takes the
//     link for its consent). A failure: "failed" with its word; the service
//     waits for a new choice (the page offers the cable after "network").
//
// At every point both slots stay bootable: provisioning writes no partition
// and no A/B sector; the running slot is marked before anything is done.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"uc-pxx-installer/internal/clone"
	"uc-pxx-installer/internal/provision/bundle"
	"uc-pxx-installer/internal/xfer/handover"
)

// Result says what the service did (for the log and the tests).
type Result struct {
	What    string
	Profile string
	Done    bool
	Fail    string
}

type outcome struct {
	ok     bool // the step passed (work: the profile is installed and checked)
	fail   string
	reason string
	next   *handover.Choice // a new choice that came while the service waited
}

// marshal is the JSON of v with a newline.
func marshal(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	return append(b, '\n'), err
}

func failed(word, f string, a ...any) outcome {
	return outcome{fail: word, reason: fmt.Sprintf(f, a...)}
}

type svc struct {
	e       *Env
	o       Options
	conf    Conf
	ps      *Profiles
	offered []Profile
	slot    string
	fixed   *Fixed
	key     bool
	profile string

	usb        Proc
	usbOn      bool
	usbStatus  bool
	usbFixed   string // the fixed profile the receiver was started with
	uid        int
	lastSeq    int
	stageMount bool
	lastPanel  string
}

// Run is the service (uc-pxx-provision service).
func Run(e *Env, o Options) (*Result, error) {
	o.defaults()
	s := &svc{e: e, o: o}
	defer s.close()
	cb, err := readRegular(e.path(ConfFile), 64<<10)
	if err == nil {
		s.conf, err = ParseConf(cb)
	}
	if err != nil {
		return s.stop(handover.FailCheck, "the conf: %v", err)
	}
	pb, err := readRegular(e.path(s.conf.Profiles), MaxProfiles)
	if err == nil {
		s.ps, err = ParseProfiles(pb)
	}
	if err != nil {
		return s.stop(handover.FailCheck, "%v", err)
	}
	offered, notes, err := Offer(s.ps, o.Gate, o.Release)
	for _, n := range notes {
		e.Logf("%s", n)
	}
	if err != nil {
		return s.stop(handover.FailCheck, "%v", err)
	}
	s.offered = offered
	e.Logf("profiles.json of release %s: %d profile(s) offered", s.ps.Release, len(offered))
	// 2. done before
	if b, err := readRegular(e.path(clone.DoneRecordPath), clone.MaxDoneRecord); err == nil {
		if d, derr := doneFor(b, s.ps, offered); derr == nil {
			s.profile = d.Profile
			s.say(handover.PStateDone, 0, 0, "", "The system ("+d.Profile+") is installed.")
			return &Result{What: "done before: " + d.Profile, Profile: d.Profile, Done: true}, nil
		} else {
			e.Logf("the done record does not count: %v", derr)
		}
	}
	if len(offered) == 0 {
		return s.stop(handover.FailCheck, "this unit offers no profile: nothing is installed")
	}
	// 3. the mark
	if err := s.waitMark(); err != nil {
		return s.stop(handover.FailCheck, "%v", err)
	}
	s.fixed, err = readFixed(e.path(FixedFile))
	if err != nil {
		return s.stop(handover.FailCheck, "%v", err)
	}
	if s.fixed != nil {
		if _, ok := find(offered, s.fixed.Profile); !ok || s.fixed.Release != s.ps.Release {
			return s.stop(handover.FailCheck, "the fixed profile %s (release %s) is not one this unit offers", s.fixed.Profile, s.fixed.Release)
		}
		s.profile = s.fixed.Profile
	}
	s.key = KeySeeded(e.Root)
	// 4. the link
	s.say(handover.PStateWaiting, 0, 0, "", "Connect the installer page. Keep the USB-C cable connected.")
	if s.conf.USB && e.USB != nil {
		if err := s.usbStart(false); err != nil {
			e.Logf("the USB link did not start (%v): the page cannot choose; only a recorded choice runs", err)
		}
	}
	// 5. the choice
	ch := s.recordedChoice()
	for {
		if ch == nil {
			ch = s.waitChoice(o.ChoiceWait)
			if ch == nil {
				return &Result{What: "no choice", Profile: s.profile}, errors.New("no choice of a profile arrived: nothing was installed (the next boot asks again)")
			}
		}
		e.Logf("choice %d: profile %s, source %s, url %q", ch.Seq, ch.Profile, ch.Source, ch.URL)
		out := s.work(*ch)
		if out.ok {
			return s.finish()
		}
		if out.next != nil {
			ch = out.next
			continue
		}
		// the RAM stage is freed: the page's next choice may be the cable,
		// whose receiver needs the room (review MAJOR-2)
		if err := os.RemoveAll(s.repo()); err != nil {
			e.Logf("the RAM repo: %v", err)
		}
		s.say(handover.PStateFailed, 0, 0, out.fail, out.reason)
		e.Logf("failed: %s: %s", out.fail, out.reason)
		// the receiver comes back in full mode, with the profile fixed when
		// apk add started in this run (review MINOR-1: another profile is
		// then refused by the receiver itself, ok false)
		if s.usbOn && (s.usbStatus || s.fixed != nil && s.usbFixed != s.fixed.Profile) {
			if err := s.usbRestart(false); err != nil {
				e.Logf("the USB link did not start again: %v", err)
			}
		}
		ch = nil
	}
}

// stop is the end with "failed" before any choice.
func (s *svc) stop(word, f string, a ...any) (*Result, error) {
	r := fmt.Sprintf(f, a...)
	s.say(handover.PStateFailed, 0, 0, word, r)
	return &Result{What: "failed: " + word, Profile: s.profile, Fail: word}, errors.New(r)
}

func (s *svc) close() {
	s.usbStop(true)
	if s.stageMount && s.e.UnmountStage != nil {
		if err := s.e.UnmountStage(s.e.path(handover.ProvisionStage)); err != nil {
			s.e.Logf("unmount the stage: %v", err)
		}
		s.stageMount = false
	}
}

var progressLine = regexp.MustCompile(`^\(\s*([0-9]+)/\s*([0-9]+)\) `)

// say writes the progress file, the info file and the panel, and logs.
func (s *svc) say(state string, done, total int64, fail, reason string, next ...int) {
	p := handover.Progress{State: state, Done: done, Total: total, Fail: fail, Reason: oneLine(reason), Profile: s.profile,
		Updated: s.e.Now().Unix()}
	if len(next) > 0 {
		p.NextTryS = next[0]
	}
	if err := handover.WriteProgress(s.e.path(handover.ProvisionProgressFile), p); err != nil {
		s.e.Logf("progress file: %v", err)
	}
	lines := panelLines(p)
	if err := handover.WriteInfo(s.e.path(handover.ProvisionInfo), handover.BaseInfo(s.e.path("/"), "", lines)); err != nil {
		s.e.Logf("info file: %v", err)
	}
	if t := strings.Join(lines, "\n"); t != s.lastPanel {
		s.lastPanel = t
		if s.e.Panel != nil {
			s.e.Panel(lines)
		}
		s.e.Logf("%s", strings.Join(lines, " | "))
	}
}

func oneLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	b := []byte(s)
	for i, c := range b {
		if c < 0x20 || c > 0x7e {
			b[i] = '?'
		}
	}
	if len(b) > 200 {
		b = b[:200]
	}
	return string(b)
}

const mb = 1000 * 1000

// panelLines are the unit's text of a progress (DESIGN-R23 §3.6).
func panelLines(p handover.Progress) []string {
	keep := "Do not disconnect the power."
	switch p.State {
	case handover.PStateDownloading:
		return []string{fmt.Sprintf("Installing the system: downloading %d of %d MB", p.Done/mb, (p.Total+mb-1)/mb), keep}
	case handover.PStateReceiving:
		return []string{fmt.Sprintf("Installing the system: receiving %d of %d MB", p.Done/mb, (p.Total+mb-1)/mb), keep}
	case handover.PStateInstalling:
		return []string{fmt.Sprintf("Installing the system: package %d of %d", p.Done, p.Total), keep}
	case handover.PStateApplying:
		return []string{"Installing the system: the last steps", keep}
	case handover.PStateChecking:
		return []string{"Installing the system: checking", keep}
	case handover.PStateDone:
		return []string{p.Reason}
	case handover.PStateRetryWait:
		return []string{"The unit cannot download the packages: " + p.Reason, fmt.Sprintf("It tries again in %d s.", p.NextTryS)}
	case handover.PStateFailed:
		return []string{"The system was not installed (" + p.Fail + "): " + p.Reason, "Slot B still starts. Nothing of slot A was written."}
	}
	if p.Reason != "" {
		return []string{p.Reason}
	}
	return []string{"Connect the installer page. Keep the USB-C cable connected."}
}

// waitMark waits until the running slot is marked.
func (s *svc) waitMark() error {
	end := s.e.Now().Add(s.o.MarkWait)
	for {
		ok, slot, err := s.e.Marked()
		if err == nil && ok && (slot == "a" || slot == "b") {
			s.slot = slot
			return nil
		}
		if !s.e.Now().Before(end) {
			return fmt.Errorf("the running slot was not successful and the slot that boots within %s (%v): nothing is installed", s.o.MarkWait, err)
		}
		s.e.Sleep(5 * time.Second)
	}
}

// --- the USB link ---------------------------------------------------------------------------------

func (s *svc) xferArgs(statusOnly bool) XferArgs {
	a := XferArgs{ConsoleKey: s.key, Seq: s.lastSeq, StatusOnly: statusOnly}
	for _, p := range s.offered {
		a.Profiles = append(a.Profiles, p.HO())
	}
	if s.fixed != nil {
		a.Fixed = s.fixed.Profile
	}
	return a
}

func (s *svc) usbStart(statusOnly bool) error {
	u := s.e.USB
	if err := s.ensureStage(); err != nil {
		return err
	}
	if err := u.Take(); err != nil {
		return fmt.Errorf("rc-service uc-pxx-usb stop: %v", err)
	}
	s.usbOn = true
	uid, err := u.UID()
	if err == nil {
		s.uid = uid
		err = u.Cleanup()
	}
	if err == nil {
		s.usb, err = u.Start(s.xferArgs(statusOnly))
	}
	if err != nil {
		s.usbStop(true)
		return err
	}
	s.usbStatus, s.usbFixed = statusOnly, s.xferArgs(statusOnly).Fixed
	return nil
}

func (s *svc) usbRestart(statusOnly bool) error {
	if s.usb != nil {
		if err := s.usb.Stop(); err != nil {
			s.e.Logf("uc-pxx-xfer: %v", err)
		}
		s.usb = nil
	}
	u := s.e.USB
	if err := u.Cleanup(); err != nil {
		return err
	}
	p, err := u.Start(s.xferArgs(statusOnly))
	if err != nil {
		return err
	}
	s.usb, s.usbStatus, s.usbFixed = p, statusOnly, s.xferArgs(statusOnly).Fixed
	return nil
}

// usbStop ends the receiver and, with give, gives the gadget back to
// uc-pxx-usb.
func (s *svc) usbStop(give bool) {
	if s.usb != nil {
		if err := s.usb.Stop(); err != nil {
			s.e.Logf("uc-pxx-xfer: %v", err)
		}
		s.usb = nil
	}
	if s.usbOn && give {
		if err := s.e.USB.Give(); err != nil {
			s.e.Logf("rc-service uc-pxx-usb start: %v", err)
		}
		s.usbOn = false
	}
}

func (s *svc) xferDir() string {
	return s.e.path(filepath.Join(handover.ProvisionStage, handover.ProvisionXferDir))
}

// newChoice is a choice of the receiver that the service has not taken yet
// and that it accepts (checked again as root); nil: none.
func (s *svc) newChoice() *handover.Choice {
	if s.usb == nil {
		return nil
	}
	select {
	case <-s.usb.Done():
		s.e.Logf("uc-pxx-xfer ended: it is started again")
		s.usb = nil
		if err := s.usbRestart(s.usbStatus); err != nil {
			s.e.Logf("uc-pxx-xfer did not start again: %v", err)
		}
		return nil
	default:
	}
	c, err := handover.ReadChoice(s.xferDir(), s.uid)
	if err != nil || c.Seq <= s.lastSeq {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			s.e.Logf("the receiver's choice: %v", err)
		}
		return nil
	}
	s.lastSeq = c.Seq
	if why := s.refuseChoice(c); why != "" {
		s.e.Logf("choice %d refused: %s", c.Seq, why)
		return nil
	}
	return c
}

// refuseChoice: the root's own check of a choice.
func (s *svc) refuseChoice(c *handover.Choice) string {
	p, ok := find(s.offered, c.Profile)
	switch {
	case !ok:
		return "the profile " + c.Profile + " is not offered"
	case s.fixed != nil && s.fixed.Profile != c.Profile:
		return "the profile is fixed: " + s.fixed.Profile
	case p.Bundle == nil && c.Source != handover.SourceNet:
		return "the console profile has no bundle"
	case p.Bundle == nil && !s.key:
		return "no root ssh key was seeded: the console profile has no other way in"
	}
	return ""
}

// waitChoice waits up to d for a new choice.
func (s *svc) waitChoice(d time.Duration) *handover.Choice {
	end := s.e.Now().Add(d)
	for s.e.Now().Before(end) {
		if c := s.newChoice(); c != nil {
			return c
		}
		s.e.Sleep(time.Second)
	}
	return nil
}

// recordedChoice is the last choice with the source net (an earlier boot):
// the service starts it again by itself.
func (s *svc) recordedChoice() *handover.Choice {
	b, err := readRegular(s.e.path(ChoiceFile), 4096)
	if err != nil {
		return nil
	}
	var c handover.Choice
	if err := handover.DecodeStrict([]byte(strings.TrimRight(string(b), "\n")), &c); err != nil || c.Check() != nil ||
		c.Source != handover.SourceNet || s.refuseChoice(&c) != "" {
		s.e.Logf("the recorded choice does not count (%v)", err)
		return nil
	}
	c.Seq = 0
	return &c
}

// --- the stage ------------------------------------------------------------------------------------

func (s *svc) stageSize() int64 {
	var n int64
	for _, p := range s.offered {
		if p.Bundle != nil && p.Bundle.Size > n {
			n = p.Bundle.Size
		}
	}
	n += StageSlack
	if n > MaxStage {
		n = MaxStage
	}
	return n
}

func (s *svc) ensureStage() error {
	if s.stageMount || s.e.MountStage == nil {
		return nil
	}
	if err := s.e.MountStage(s.e.path(handover.ProvisionStage), s.stageSize()); err != nil {
		return fmt.Errorf("the RAM stage: %v", err)
	}
	s.stageMount = true
	return nil
}

func (s *svc) repo() string { return s.e.path(filepath.Join(handover.ProvisionStage, RepoName)) }

// --- the work of a choice -------------------------------------------------------------------------

func (s *svc) work(ch handover.Choice) outcome {
	e := s.e
	if why := s.refuseChoice(&ch); why != "" {
		return failed(handover.FailCheck, "%s", why)
	}
	p, _ := find(s.offered, ch.Profile)
	s.profile = p.Name
	if ch.Source == handover.SourceCable {
		// the person chose the cable: the next boot does not try the network
		// by itself (review NIT-2)
		if err := os.Remove(e.path(ChoiceFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
			e.Logf("the choice record: %v", err)
		}
	}
	if ch.Source == handover.SourceNet {
		c := ch
		c.Seq, c.Session = 1, 0
		b, _ := marshal(c)
		if err := writeDurable(e.path(ChoiceFile), b, 0o644); err != nil {
			e.Logf("the choice record: %v", err)
		}
	}
	var doc *bundle.Doc
	if p.Bundle != nil {
		if free := e.Mem() - MemReserve; free < p.Bundle.Size {
			return failed(handover.FailSpace, "not enough free RAM for the bundle: %d bytes needed, %d free", p.Bundle.Size, free)
		}
		if p.Bundle.Size+StageSlack > MaxStage {
			return failed(handover.FailSpace, "the bundle of %d bytes is larger than the RAM stage", p.Bundle.Size)
		}
		if err := s.ensureStage(); err != nil {
			return failed(handover.FailSpace, "%v", err)
		}
		var out outcome
		if ch.Source == handover.SourceCable {
			out = s.cable(p)
		} else {
			out = s.download(p, ch)
		}
		if !out.ok {
			return out
		}
		d, _, err := verifyRepo(s.repo(), s.pins(p))
		if err != nil {
			return failed(handover.FailBundle, "the bundle in RAM: %v", err)
		}
		doc = d
		isz, err := bundle.InstalledSize(doc, openMember(s.repo()))
		if err != nil {
			return failed(handover.FailBundle, "the installed size: %v", err)
		}
		need := isz + isz*FreeMarginPct/100
		if free := e.Free(e.path("/")); free < need {
			return failed(handover.FailSpace, "the root file system has %d bytes free, the profile needs %d", free, need)
		}
	}
	release, err := s.lock()
	if err != nil {
		return failed(handover.FailInstall, "%v", err)
	}
	defer release()
	inst, _, err := readInstalled(e.path(InstalledDB))
	if err != nil {
		return failed(handover.FailInstall, "the installed packages: %v", err)
	}
	if doc != nil {
		if err := baseCheck(inst, doc); err != nil {
			return failed(handover.FailInstall, "%v", err)
		}
	}
	resumed := s.fixed != nil // apk add started in an earlier run: apk fix after it
	if s.fixed == nil {
		f := Fixed{Schema: 1, Kind: FixedKind, Release: s.ps.Release, Profile: p.Name, BundleSHA256: p.BundleSHA256(), Time: e.Now().Unix()}
		b, _ := marshal(f)
		if err := writeDurable(e.path(FixedFile), b, 0o644); err != nil {
			return failed(handover.FailInstall, "fixed.json: %v", err)
		}
		s.fixed = &f
		e.Logf("the profile %s is fixed for this unit", p.Name)
	}
	restore, err := e.Guard(s.slot)
	if err != nil {
		return failed(handover.FailInstall, "the kernel read-only flags of the eMMC could not be set: %v", err)
	}
	guarded := true
	defer func() {
		if guarded {
			if err := restore(); err != nil {
				e.Logf("the read-only flags: %v", err)
			}
		}
	}()
	if doc != nil {
		if out := s.apk(p, doc, resumed); !out.ok {
			return out
		}
	}
	s.say(handover.PStateApplying, 0, 0, "", "")
	argv := []string{"/bin/sh", s.conf.Apply, p.Apply, "/"}
	if p.Apply == "ha" {
		argv = append(argv, "--lva", filepath.Join(s.repo(), bundle.LVAName))
	}
	if err := e.Exec(argv, func(l string) { e.Logf("profile-apply: %s", l) }); err != nil {
		return failed(handover.FailInstall, "the profile steps (%s): %v", p.Apply, err)
	}
	guarded = false
	if err := restore(); err != nil {
		return failed(handover.FailInstall, "the kernel read-only flags of the eMMC could not be cleared (%v): restart the unit, then choose again", err)
	}
	s.say(handover.PStateChecking, 0, 0, "", "")
	inst, isha, err := readInstalled(e.path(InstalledDB))
	if err != nil {
		return failed(handover.FailCheck, "the installed packages: %v", err)
	}
	if doc != nil {
		if err := exactCheck(inst, doc); err != nil {
			return failed(handover.FailInstall, "%v", err)
		}
	}
	if isha != p.Installed {
		return failed(handover.FailCheck, "the installed database has sha256 %s, the profile's is %s", isha, p.Installed)
	}
	r, err := clone.LiveContent(e.path("/"), s.o.Gate.Exclude)
	if err != nil {
		return failed(handover.FailCheck, "the content check: %v", err)
	}
	if r.SHA256 != p.RefB {
		return failed(handover.FailCheck, "the system on the unit is not as expected: its content (%d entries) has sha256 %s, ref_b of %s is %s",
			r.Entries, r.SHA256, p.Name, p.RefB)
	}
	e.Logf("the content check passed: %d entries, ref_b %s", r.Entries, r.SHA256)
	if err := writeDurable(e.path(DoneTmp), doneRecord(s.ps, p), 0o644); err != nil {
		return failed(handover.FailCheck, "the done record: %v", err)
	}
	if err := os.RemoveAll(s.repo()); err != nil {
		e.Logf("the RAM repo: %v", err)
	}
	return outcome{ok: true}
}

func (s *svc) pins(p Profile) bundle.Pins {
	return bundle.Pins{Size: p.Bundle.Size, SHA256: p.Bundle.SHA256, IndexSHA256: *p.IndexSHA256, Expect: p.Expect(s.ps)}
}

// lock takes tsx-autoupdate's two locks, waiting (at most LockWait) while
// another run holds one.
func (s *svc) lock() (func(), error) {
	end := s.e.Now().Add(s.o.LockWait)
	for {
		rel, err := s.e.Locks()
		if err == nil {
			return rel, nil
		}
		if !s.e.Now().Before(end) {
			return nil, fmt.Errorf("tsx-autoupdate held its lock for %s (%v): try again later", s.o.LockWait, err)
		}
		s.e.Sleep(2 * time.Second)
	}
}

// apk installs the bundle: apk add of its names (and apk fix --reinstall of
// its installed packages after a cut install of an earlier run), with only
// the bundle's repositories and no network.
func (s *svc) apk(p Profile, doc *bundle.Doc, resumed bool) outcome {
	e := s.e
	rf := s.e.path(RepositoriesFile)
	if err := writeRunFile(rf, repositories(s.repo(), doc)); err != nil {
		return failed(handover.FailInstall, "the repositories file: %v", err)
	}
	n := int64(len(doc.Packages))
	s.say(handover.PStateInstalling, 0, n, "", "")
	last := e.Now()
	progress := func(l string) {
		e.Logf("apk: %s", l)
		if m := progressLine.FindStringSubmatch(l); m != nil {
			a, _ := strconv.ParseInt(m[1], 10, 64)
			b, _ := strconv.ParseInt(m[2], 10, 64)
			if now := e.Now(); now.Sub(last) >= time.Second || a == b {
				last = now
				s.say(handover.PStateInstalling, min(a, b), b, "", "")
			}
		}
	}
	argv := append([]string{"/sbin/apk", "add", "--no-network", "--no-cache", "--repositories-file", rf}, doc.Add...)
	if err := e.Exec(argv, progress); err != nil {
		return failed(handover.FailInstall, "apk add: %v", err)
	}
	if resumed {
		inst, _, err := readInstalled(e.path(InstalledDB))
		if err != nil {
			return failed(handover.FailInstall, "the installed packages: %v", err)
		}
		var names []string
		for _, q := range doc.Packages {
			if inst[q.Name] == q.Version {
				names = append(names, q.Name)
			}
		}
		e.Logf("an earlier run was cut: apk fix --reinstall of %d packages", len(names))
		argv := append([]string{"/sbin/apk", "fix", "--no-network", "--no-cache", "--repositories-file", rf, "--reinstall"}, names...)
		if err := e.Exec(argv, progress); err != nil {
			return failed(handover.FailInstall, "apk fix: %v", err)
		}
	}
	return outcome{ok: true}
}

// baseCheck: every package of the bundle's base is installed with its
// version, and every other installed package is one of the bundle's with
// its version (a cut install of an earlier run leaves some).
func baseCheck(inst map[string]string, doc *bundle.Doc) error {
	base, pk := doc.BaseSet(), doc.PackageSet()
	for n, v := range base {
		if inst[n] != v {
			return fmt.Errorf("the base package %s-%s is %q on the unit: the bundle is not for this base", n, v, inst[n])
		}
	}
	for n, v := range inst {
		if _, ok := base[n]; ok {
			continue
		}
		if pv, ok := pk[n]; !ok || pv != v {
			return fmt.Errorf("the unit has %s-%s, a package of neither the base nor the bundle", n, v)
		}
	}
	return nil
}

// exactCheck: the installed packages are exactly the base and the bundle's.
func exactCheck(inst map[string]string, doc *bundle.Doc) error {
	if err := baseCheck(inst, doc); err != nil {
		return err
	}
	for n, v := range doc.PackageSet() {
		if inst[n] != v {
			return fmt.Errorf("the bundle's %s-%s is %q on the unit after apk", n, v, inst[n])
		}
	}
	return nil
}

// --- the bundle over the network -------------------------------------------------------------------

func (s *svc) download(p Profile, ch handover.Choice) outcome {
	e := s.e
	if e.Fetch == nil {
		return failed(handover.FailNetwork, "this unit has no network fetch")
	}
	repo := s.repo()
	if err := freshRepo(repo); err != nil {
		return failed(handover.FailSpace, "the RAM repo: %v", err)
	}
	u, err := bundle.NewUnpacker(s.pins(p), dirSink{repo})
	if err != nil {
		return failed(handover.FailCheck, "%v", err)
	}
	urls := s.ps.URLs
	if ch.URL != "" {
		urls = []string{ch.URL}
	}
	buf := make([]byte, 1<<20)
	tries, mism := 0, 0
	for {
		url := urls[tries%len(urls)] + p.Bundle.File
		if strings.HasPrefix(url, "https:") && e.ClockSynced != nil && !e.ClockSynced() {
			s.say(handover.PStateDownloading, u.Offset(), p.Bundle.Size, "", "waiting for the clock")
			end := e.Now().Add(s.o.ClockWait)
			for !e.ClockSynced() && e.Now().Before(end) {
				e.Sleep(5 * time.Second)
			}
		}
		before, _ := u.Members()
		whole, err := s.fetchOnce(u, url, p.Bundle.Size, buf)
		if err == nil {
			return outcome{ok: true}
		}
		var be *bundle.Error
		switch {
		case errors.As(err, &be) && be.Sink:
			return failed(handover.FailSpace, "the RAM repo: %v", err)
		case bundle.IsBundle(err):
			e.Logf("from %s: %v", url, err)
			if mism++; mism > 1 {
				return failed(handover.FailBundle, "the downloaded bundle is not the pinned one (after a new fetch): %v", err)
			}
			if whole {
				if err := freshRepo(repo); err != nil {
					return failed(handover.FailSpace, "the RAM repo: %v", err)
				}
				u.Reset()
			} else {
				u.Resume()
			}
			continue
		}
		e.Logf("from %s: %v", url, err)
		u.Resume()
		if after, _ := u.Members(); after > before {
			tries = 0 // a try that brought members is not a failed try
		}
		if tries++; tries > s.o.Retries {
			return failed(handover.FailNetwork, "the unit cannot download the packages (%d tries): %v", tries, err)
		}
		w := s.o.RetryWaits[min(tries-1, len(s.o.RetryWaits)-1)]
		if c := s.retryWait(w, oneLine(err.Error())); c != nil {
			return outcome{next: c}
		}
	}
}

// retryWait waits d in retry-wait; a new choice of the page ends it.
func (s *svc) retryWait(d time.Duration, why string) *handover.Choice {
	end := s.e.Now().Add(d)
	for {
		left := end.Sub(s.e.Now())
		if left <= 0 {
			return nil
		}
		s.say(handover.PStateRetryWait, 0, 0, "", why, int((left+time.Second-1)/time.Second))
		if c := s.newChoice(); c != nil {
			return c
		}
		s.e.Sleep(min(time.Second, left))
	}
}

// fetchOnce is one try: a GET from the unpacker's offset (a Range when it is
// not 0) to the end; whole reports that every member was there (the error
// is then of the whole file).
func (s *svc) fetchOnce(u *bundle.Unpacker, url string, size int64, buf []byte) (whole bool, err error) {
	e := s.e
	off := u.Offset()
	ctx, cancel := ctxFor()
	defer cancel()
	body, start, err := e.Fetch.Get(ctx, url, off, size)
	if err != nil {
		return false, err
	}
	defer body.Close()
	if start == 0 && off > 0 {
		e.Logf("the server sent the whole file: the download starts again from 0")
		if err := freshRepo(s.repo()); err != nil {
			return false, &bundle.Error{Sink: true, Err: err}
		}
		u.Reset()
	}
	r, stop := watch(body, cancel, StallTimeout)
	defer stop()
	last := e.Now()
	s.say(handover.PStateDownloading, u.Offset(), size, "", "")
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			if _, err := u.Write(buf[:n]); err != nil {
				return false, err
			}
			if now := e.Now(); now.Sub(last) >= time.Second {
				last = now
				s.say(handover.PStateDownloading, u.Offset(), size, "", "")
			}
		}
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			return false, rerr
		}
	}
	if u.Offset() != size {
		return false, fmt.Errorf("the download ended after %d of %d bytes", u.Offset(), size)
	}
	s.say(handover.PStateDownloading, size, size, "", "")
	return true, u.Finish()
}

// --- the bundle over the cable --------------------------------------------------------------------

// cable waits for the receiver's hand-over of the bundle (a new choice of
// the page ends the wait), then takes it over as root: the receiver stopped,
// no process of its user left, the hand-over checked, the folder the root's
// repo; then the receiver comes back status only.
func (s *svc) cable(p Profile) outcome {
	e := s.e
	if s.usb == nil {
		return failed(handover.FailCheck, "no USB link: the cable cannot be used")
	}
	// a partial download of an earlier try leaves the RAM stage to the
	// receiver (review MAJOR-2)
	if err := os.RemoveAll(s.repo()); err != nil {
		return failed(handover.FailSpace, "the RAM repo: %v", err)
	}
	s.say(handover.PStateWaiting, 0, p.Bundle.Size, "", "Send the packages over the cable.")
	end := e.Now().Add(s.o.ChoiceWait)
	dir := s.xferDir()
	for {
		// the receiver writes complete.json, then renames its folder to
		// complete/: both (review NIT-2)
		_, e1 := os.Lstat(filepath.Join(dir, handover.ReadyRecord))
		_, e2 := os.Lstat(filepath.Join(dir, handover.ReadyDir))
		if e1 == nil && e2 == nil {
			break
		}
		if c := s.newChoice(); c != nil {
			return outcome{next: c}
		}
		if !e.Now().Before(end) {
			return failed(handover.FailNetwork, "no bundle arrived over the cable within %s", s.o.ChoiceWait)
		}
		e.Sleep(time.Second)
	}
	if err := s.usb.Stop(); err != nil {
		e.Logf("uc-pxx-xfer: %v", err)
	}
	s.usb = nil
	if err := e.USB.NoProcessOf(s.uid); err != nil {
		return failed(handover.FailCheck, "%v", err)
	}
	rd, err := handover.CheckProvisionReady(dir, s.uid, p.HO())
	if err == nil {
		err = takeOver(rd.Folder, s.repo())
	}
	os.Remove(filepath.Join(dir, handover.ReadyRecord))
	os.RemoveAll(filepath.Join(dir, handover.ReadyDir)) // a refused hand-over must not block the receiver's next start
	if rerr := s.usbRestart(true); rerr != nil {
		e.Logf("the USB link did not come back: %v", rerr)
	}
	if err != nil {
		if rd != nil {
			os.RemoveAll(rd.Folder)
		}
		os.RemoveAll(s.repo())
		return failed(handover.FailBundle, "the bundle from the cable: %v", err)
	}
	e.Logf("the bundle of %s from the cable was taken over", p.Name)
	return outcome{ok: true}
}

// --- done ------------------------------------------------------------------------------------------

func (s *svc) finish() (*Result, error) {
	e := s.e
	s.say(handover.PStateDone, 0, 0, "", "The system ("+s.profile+") is installed.")
	if s.usb != nil {
		e.Sleep(s.o.DoneLinger)
	}
	s.usbStop(true)
	if err := renameDurable(e.path(DoneTmp), e.path(clone.DoneRecordPath)); err != nil {
		return s.stop(handover.FailCheck, "the done record: %v", err)
	}
	if err := os.Remove(e.path(ChoiceFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		e.Logf("the choice record: %v", err)
	}
	e.Logf("done: %s", s.profile)
	return &Result{What: "done: " + s.profile, Profile: s.profile, Done: true}, nil
}
