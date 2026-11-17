//go:build install && step2

package step2

// The step-2 plan kind clone on the unit (DESIGN-R23 §4): slot A becomes a
// copy of the running, provisioned slot B. The session around
// plan.ComputeClone and install.RunClone:
//
//   - the plan reads, read only: the provisioning done record on the running
//     root (clone.DoneRecordPath), the files of the gate's ForbiddenFrom on
//     the running root, and everything ComputeOtherSlot reads
//     (mainline-rc-10b: no content manifest of the running root; the done
//     record is written only after provisioning's health gate passed);
//   - Start is the other-slot Start with these hooks of the executor: the
//     freeze (FIFREEZE of /; the thaw FITHAW), the difference (the kernel
//     read-only flags of the disk and of system_T cleared, the rw mount of
//     system_T at clone.MountPoint, clonedelta.Apply, syncfs, the unmount,
//     the flags set back, then e2fsck -fn of system_T: exit 0), and the
//     forbidden-file search (package ext4 on system_T, read only; its
//     content manifest is logged, not compared with ref_a since
//     mainline-rc-10b);
//   - the guard of a run: the done record stays the plan's until the arm.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"uc-pxx-installer/internal/clone"
	"uc-pxx-installer/internal/clonedelta"
	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/plan"
)

// CloneFS is the eMMC side of the difference: the rw mount of system_T,
// its unmount, and e2fsck -fn (a test gives its own; nil: liveCloneFS).
type CloneFS interface {
	Mount(node, dir string) error
	Unmount(dir string) error
	Fsck(node string) (rc int, out string, err error)
}

// cloneGate is the clone gate of the run: a test's, or the release's
// (clone.EmbeddedGate with the uc-pxx-provision pin of this build:
// gateKeeperFiles, gate-keeper.json).
func (s *session) cloneGate() *clone.Gate {
	if s.o.CloneGate != nil {
		return s.o.CloneGate
	}
	g := clone.EmbeddedGate()
	g.KeeperFiles = gateKeeperFiles()
	return g
}

// doneRecord reads the done record of the running root (nil, nil: none).
func (s *session) doneRecord() ([]byte, error) {
	b, err := readRegular(s.e.Path(clone.DoneRecordPath), clone.MaxDoneRecord)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

// doneIdent is the identity of a clone plan's source: the done record.
func (s *session) doneIdent() string {
	b, err := s.doneRecord()
	switch {
	case err != nil:
		return "provisioning done record: unreadable: " + err.Error()
	case b == nil:
		return "provisioning done record: none"
	}
	return "provisioning done record " + clone.DoneRecordPath + " sha256 " + clone.SHA256(b)
}

// keeperForbidden hashes the files of the gate's ForbiddenFrom on the
// running root (a missing one is skipped).
func (s *session) keeperForbidden(g *clone.Gate) ([]clone.Pin, error) {
	var out []clone.Pin
	for _, p := range g.ForbiddenFrom {
		b, err := readRegular(s.e.Path(p), 64<<20)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %v", p, err)
		}
		if len(b) > 0 {
			out = append(out, clone.Pin{What: p + " of slot " + strings.ToUpper(s.ru.Slot), Size: int64(len(b)), SHA256: clone.SHA256(b)})
		}
	}
	return out, nil
}

// keeperFiles checks the files of B that the content manifest leaves out
// by their own pins (the gate's KeeperFiles: uc-pxx-provision), and that
// this program is the installed uc-pxx-step2 (pinned by step 1 only: it
// holds the gate; R23-FORMATS §9.1).
func (s *session) keeperFiles(g *clone.Gate) error {
	for _, f := range g.KeeperFiles {
		b, err := readRegular(s.e.Path(f.Path), 256<<20)
		if err != nil {
			return fmt.Errorf("%s: %v", f.Path, err)
		}
		if int64(len(b)) != f.Size || clone.SHA256(b) != f.SHA256 {
			return fmt.Errorf("%s: %d bytes, sha256 %s; the clone gate pins %d bytes, sha256 %s", f.Path, len(b), clone.SHA256(b), f.Size, f.SHA256)
		}
	}
	if s.e.IsTest() {
		return nil // the test binary is not the installed program
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	a, err := readRegular(self, 256<<20)
	if err != nil {
		return fmt.Errorf("this program (%s): %v", self, err)
	}
	b, err := readRegular(s.e.Path(clone.Step2Binary), 256<<20)
	if err != nil {
		return fmt.Errorf("%s: %v", clone.Step2Binary, err)
	}
	if clone.SHA256(a) != clone.SHA256(b) {
		return fmt.Errorf("this program (sha256 %s) is not %s (sha256 %s)", clone.SHA256(a), clone.Step2Binary, clone.SHA256(b))
	}
	return markerNames(s.e.Path(clone.Step2Marker), clone.SHA256(a))
}

// markerNames: the step-2 marker is one line (at most 4096 bytes) that names
// sha (the running program's sha256) as a word (R23-BASE-REVIEW MAJOR-2).
func markerNames(path, sha string) error {
	b, err := readRegular(path, 4096)
	if err != nil {
		return fmt.Errorf("%s: %v", clone.Step2Marker, err)
	}
	line, ok := strings.CutSuffix(string(b), "\n")
	if !ok || strings.ContainsAny(line, "\n\r\x00") {
		return fmt.Errorf("%s is not one line", clone.Step2Marker)
	}
	for _, w := range strings.Fields(line) {
		if w == sha {
			return nil
		}
	}
	return fmt.Errorf("%s does not name this program's sha256 %s", clone.Step2Marker, sha)
}

// computeClone makes the clone plan from the live unit.
func (s *session) computeClone(confirm plan.ConfirmGate) (*plan.OtherSlotPlan, error) {
	data, err := s.dataWanted()
	if err != nil {
		return nil, err
	}
	if err := s.mounts(data); err != nil {
		return nil, err
	}
	g := s.cloneGate()
	done, derr := s.doneRecord()
	ident := s.doneIdent()
	forb, err := s.keeperForbidden(g)
	if err != nil {
		return nil, refuse("the files that must not be in the target: %v", err)
	}
	if err := s.keeperFiles(g); err != nil {
		return nil, refuse("the files of slot %s outside the content check: %v", strings.ToUpper(s.ru.Slot), err)
	}
	if now := s.doneIdent(); now != ident {
		return nil, refuse("the done record changed while the plan read the root")
	}
	info, err := s.d.SlotBootctlInfo()
	if err != nil {
		return nil, refuse("%v", err)
	}
	st, err := s.d.SlotBootctlStatus()
	if err != nil {
		return nil, refuse("%v", err)
	}
	view := s.d.View()
	defer view.Close()
	in := plan.CloneInput{Input: plan.Input{Disk: view, DiskBytes: s.d.Bytes(), Source: "device:" + device.EMMCPath, Mode: "slot",
		Tool: Version, Bootctl: info, BootctlStatus: st, HashKeeper: true, Device: &s.d.ID,
		RequireBootctlSHA256: s.e.BootctlSHA256(), FirmwareIdent: ident},
		Running: s.ru, Gate: g, Done: done, DoneErr: derr, Forbidden: forb, Confirm: confirm, Data: data}
	if s.o.KeyPins != nil {
		in.KeyPins, in.KeyPinSet = s.o.KeyPins, plan.KeyPinSetTest
	}
	p, err := plan.ComputeClone(in)
	if err != nil {
		if p != nil {
			p.Render(s.out)
		}
		return nil, refuse("%v", err)
	}
	return p, nil
}

// planClone is Plan for --clone.
func planClone(s *session, o Options) (*plan.OtherSlotPlan, error) {
	p, err := s.computeClone(s.confirmGate())
	if err != nil {
		return nil, err
	}
	p.Render(s.out)
	s.say("clone plan id: %s (profile %s, ref_a %.16s…, attempt key %q)", p.ID, p.Clone.Profile, p.Clone.RefA, p.Clone.AttemptKey)
	if o.JSONOut != "" {
		if err := writeJSON(o.JSONOut, p, false); err != nil {
			return p, err
		}
	}
	return p, nil
}

// startClone is Start for a clone plan p1 (the locks are held and Live.Quiet
// passed).
func (s *session) startClone(e *device.Env, o Options, p1 *plan.OtherSlotPlan) (*Result, error) {
	s.say("re-check: the clone plan again from the live unit (the done record, the keeper's chain; nothing is written yet)")
	p2, err := s.computeClone(s.confirmGate())
	if err != nil {
		return nil, err
	}
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
	con := s.abConsole(p2)
	opt := install.CloneOptions{Options: install.Options{Say: func(l string) { s.say("%s", l) }, Log: io.MultiWriter(con, lf), Hooks: s.o.Hooks,
		MaxWrite: s.o.MaxWrite, Guard: s.guard(p2)},
		Data: s.dataHook(dr), Gate: s.cloneGate(), BootID: s.ru.BootID, Freeze: s.freezeRoot,
		Delta: func(c *plan.ClonePart) error { return s.cloneDelta(p2, c) }, Content: func(c *plan.ClonePart) (string, error) { return s.cloneContent(p2, c) },
		Progress: o.CopyProgress}
	if s.o.KeyPins != nil {
		opt.KeyPins, opt.KeyPinSet = s.o.KeyPins, plan.KeyPinSetTest
	}
	m := s.d.SlotMedium(s.o.MaxWrite)
	s.say("")
	cr, rerr := install.RunClone(p2, m, opt)
	var res *install.Result
	if cr != nil {
		res = cr.Result
		s.say("the root file system of slot %s was frozen for %.1f s for the copy (thaw guard fired %v)", strings.ToUpper(p2.Keeper),
			cr.Frozen.Seconds(), cr.Guard)
	}
	sec, aerr := s.d.ReadABFresh()
	s.say("")
	s.say("A/B now: %s", install.SafeStateOf(sec, aerr, &p2.Plan))
	if aerr == nil {
		con.line(fmt.Sprintf("plan %.12s end: A/B first 32 read back %x (%s)", p2.ID, sec[:32], install.SafeStateOf(sec, aerr, &p2.Plan)))
	} else {
		con.line(fmt.Sprintf("plan %.12s end: A/B unreadable: %v", p2.ID, aerr))
	}
	if rerr != nil {
		if errors.Is(rerr, install.ErrABUnknown) {
			return &Result{Plan: p2, Run: res, Clone: cr}, fmt.Errorf("%w: %v", install.ErrABUnknown, rerr)
		}
		return &Result{Plan: p2, Run: res, Clone: cr}, rerr
	}
	if err := appendLine(filepath.Join(dir, StateFile), "armed "+p2.ID+"\n"); err != nil {
		return &Result{Plan: p2, Run: res, Clone: cr}, fmt.Errorf("slot %s is armed, but the state line was not written: %v", strings.ToUpper(p2.Target), err)
	}
	if err := s.checkpoint(); err != nil {
		s.say("WARNING: the journal checkpoint after the arm failed (%v): restart only with `sync; reboot`, never by a power cut", err)
	}
	s.say("step 2 done: slot %s is a copy of slot %s, armed (trying) and boots next; slot %s stays successful as the fallback. Restart with: sync; reboot",
		strings.ToUpper(p2.Target), strings.ToUpper(p2.Keeper), strings.ToUpper(p2.Keeper))
	return &Result{Plan: p2, Run: res, Clone: cr}, nil
}

const fifreeze, fithaw = 0xC0045877, 0xC0045878 // _IOWR('X', 119/120, int)

// freezeRoot is the freeze of the copy: FIFREEZE of / (the journal is
// written to its home blocks, every writer of / waits) and the thaw. In a
// test root it is the test's Freezer, never a freeze of the host.
func (s *session) freezeRoot() (func() error, error) {
	if s.o.Freezer != nil {
		return s.o.Freezer()
	}
	if s.e.IsTest() {
		return nil, errors.New("no freeze in a test root (set Options.Freezer)")
	}
	// the thaw guard as its own process first (clone review MINOR-2): it
	// thaws "/" when this process goes or the freeze lasts too long
	g, err := startThawGuard()
	if err != nil {
		return nil, fmt.Errorf("no freeze without the thaw guard: %v", err)
	}
	f, err := os.Open("/")
	if err != nil {
		g.end(false)
		return nil, err
	}
	if _, _, en := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), fifreeze, 0); en != 0 {
		f.Close()
		g.end(false) // not frozen: its thaw gets EINVAL, nothing done
		return nil, fmt.Errorf("FIFREEZE /: %v", en)
	}
	// nothing is written to "/" from here to the thaw (no say: the session's
	// output is a file on "/"). A failed "frozen" line means the helper is
	// gone: thaw at once and refuse (provision re-look MINOR-R2)
	if err := g.frozen(); err != nil {
		_, _, en := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), fithaw, 0)
		f.Close()
		g.end(false)
		return nil, fmt.Errorf("no freeze without the thaw guard: its frozen line failed (%v); FITHAW: %v", err, en)
	}
	return func() error {
		_, _, en := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), fithaw, 0)
		switch en {
		case 0:
			f.Close()
			s.say("the thaw guard ended (exit %d)", g.end(true))
			return nil
		case syscall.EINVAL:
			// not frozen at the thaw: someone else thawed / during the copy,
			// so the copy is not of a frozen file system (clone review
			// MINOR-4); the executor thaws once, a retry gets the same
			f.Close()
			s.say("the thaw guard ended (exit %d)", g.end(true))
			return fmt.Errorf("FITHAW /: %v: the root was not frozen any more (thawed by another program during the copy)", en)
		}
		// a failed thaw: the executor tries again (up to three times); after
		// the last, its error makes the run end, and the guard thaws when
		// this process goes or at its bound
		return fmt.Errorf("FITHAW /: %v", en)
	}, nil
}

func (s *session) cloneFS() CloneFS {
	if s.o.CloneFS != nil {
		return s.o.CloneFS
	}
	return liveCloneFS{}
}

// cloneDelta is the difference step: system_T mounted rw at
// clone.MountPoint (only this mount, only here), clonedelta.Apply, syncfs,
// the unmount, then e2fsck -fn (exit 0). Nothing of the eMMC may be mounted
// but the root (and /data) before and after.
func (s *session) cloneDelta(p *plan.OtherSlotPlan, c *plan.ClonePart) (err error) {
	if c == nil || c.MountPoint != clone.MountPoint || c.Delta == nil || c.DeltaSHA256 != s.cloneGate().DeltaSHA256() {
		return errors.New("the difference is not the clone gate's")
	}
	name := "system_" + p.Target
	if p.Target == s.ru.Slot || name == s.ru.Root {
		return fmt.Errorf("%s is the running root", name)
	}
	data := p.Data != nil
	if err := s.mounts(data); err != nil {
		return err
	}
	mnt := s.e.Path(c.MountPoint)
	if err := os.MkdirAll(filepath.Dir(mnt), 0o755); err != nil {
		return err
	}
	if err := os.Mkdir(mnt, 0o700); err != nil {
		return fmt.Errorf("the mount folder %s: %v", mnt, err)
	}
	defer os.Remove(mnt)
	fs := s.cloneFS()
	err = s.d.SlotPartRW(name, func(node string) error {
		if err := fs.Mount(node, mnt); err != nil {
			return fmt.Errorf("mount %s (ext4 rw,nosuid,nodev,noexec) at %s: %w", name, c.MountPoint, err)
		}
		aerr := clonedelta.Apply(mnt, c.Delta, c.BuildID)
		serr := syncDirFS(mnt)
		uerr := fs.Unmount(mnt)
		if aerr != nil {
			return fmt.Errorf("the difference: %w", aerr)
		}
		if serr != nil {
			return fmt.Errorf("syncfs %s: %w", c.MountPoint, serr)
		}
		if uerr != nil {
			return fmt.Errorf("unmount %s: %w", c.MountPoint, uerr)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := s.mounts(data); err != nil {
		return fmt.Errorf("after the difference: %w", err)
	}
	node, err := s.d.PartNode(name)
	if err != nil {
		return err
	}
	rc, out, ferr := fs.Fsck(node)
	s.say("     e2fsck -fn %s: exit %d", name, rc)
	for _, l := range lastLines(out, 4) {
		s.say("       | %s", l)
	}
	switch {
	case ferr != nil:
		return fmt.Errorf("e2fsck -fn %s: %v", name, ferr)
	case rc != 0:
		return fmt.Errorf("e2fsck -fn %s: exit %d: the file system of slot %s is not clean", name, rc, strings.ToUpper(p.Target))
	}
	return nil
}

// syncDirFS is syncfs(2) of the file system that holds dir.
func syncDirFS(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, _, en := syscall.Syscall(sysSyncfs, f.Fd(), 0, 0); en != 0 {
		return en
	}
	return nil
}

// cloneContent is the content-check step: the content manifest of system_T
// read through package ext4 (no mount), with the plan's exclusion list and
// forbidden files (a forbidden file is its error); the manifest is kept in
// RAM for the log (/run/uc-pxx-step2/content-<slot>.txt). Since
// mainline-rc-10b its sha256 is not compared with ref_a.
func (s *session) cloneContent(p *plan.OtherSlotPlan, c *plan.ClonePart) (string, error) {
	name := "system_" + p.Target
	r, size, closeFn, err := s.d.PartReader(name)
	if err != nil {
		return "", err
	}
	defer closeFn()
	src, err := clone.NewExt4Source(r, size)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	x, err := clone.NewExcluder(c.Exclude)
	if err != nil {
		return "", err
	}
	res, err := clone.Build(src, x, clone.Options{Full: true, Forbidden: c.Forbidden})
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	if d := s.e.Path(dataRunDir); os.MkdirAll(d, 0o700) == nil {
		writeFile(filepath.Join(d, "content-"+p.Target+".txt"), res.Manifest, false)
	}
	s.say("     the content manifest of %s: %d entries, sha256 %s", name, res.Entries, res.SHA256)
	if bytes.Count(res.Manifest, []byte("\n")) > 0 {
		s.say("     (its lines are in %s/content-%s.txt; not compared with ref_a %.16s since mainline-rc-10b)", dataRunDir, p.Target, c.RefA)
	}
	return res.SHA256, nil
}

// liveCloneFS is the unit's mount of the difference (clonemount.go) and
// e2fsck from the fixed system folders.
type liveCloneFS struct{}

func (liveCloneFS) Mount(node, dir string) error { return cloneMount(node, dir) }
func (liveCloneFS) Unmount(dir string) error     { return syscall.Unmount(dir, 0) }

func (liveCloneFS) Fsck(node string) (int, string, error) {
	var exe string
	for _, d := range []string{"/sbin", "/usr/sbin", "/bin", "/usr/bin"} {
		if st, err := os.Stat(d + "/e2fsck"); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
			exe = d + "/e2fsck"
			break
		}
	}
	if exe == "" {
		return -1, "", errors.New("no e2fsck in /sbin, /usr/sbin, /bin or /usr/bin")
	}
	cmd := exec.Command(exe, "-fn", node)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	cmd.Dir = "/"
	out, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), string(out), nil
	}
	if err != nil {
		return -1, string(out), err
	}
	return 0, string(out), nil
}
