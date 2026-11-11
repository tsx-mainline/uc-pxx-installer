//go:build install

package devmode

// The in-RAM lock_state reader of the direct path (MAINLINE-RC-2026-11-11.md
// §10 items 1-4; MAINLINE-RC2-LS-2026-11-11.md). The direct planner takes the
// unlock only from it (plan.LockStateRead): lock_state 0x01, no error,
// nothing changed.
//
//   - Once per boot: the first direct plan of a boot runs it; its record
//     (/run/uc-pxx-inst/optee/lockstate.json, this boot's id and the image's
//     token) answers every later call of the boot (the re-check of the
//     install sees the same record, so the same plan id). A start marker is
//     made first (O_EXCL): a boot whose read started and left no record (a
//     killed run) reads "did not end" and is never read again. The
//     userdebug-first path never runs it.
//   - Around the TEE part the reader reads itself, O_DIRECT through the
//     eMMC view: the sha256 of the partition security, of the first 32 bytes
//     of the A/B sector and of the BCB (LBA 49184, 4 sectors), of LBA 0..2047,
//     the backup GPT and the whole misc (review B-3), and the kernel
//     read-only flag of the disk and of every partition (BLKROGET). The TEE
//     part runs only when every flag is set. Any after-read that differs or
//     fails, or a flag that is off after it, is Changed: the plan refuses
//     with the A/B state unknown (exit 3: keep the unit powered), and every
//     later mainline plan or install of this boot refuses the same way.
//   - The TEE part (teeRead): the three files of the image at OpteeDir, each
//     checked (a regular file of root, not writable by group or others, its
//     sha256 the pin) and run from a private copy in /run whose bytes are
//     read back: insmod of optee_legacy.ko (its vermagic release must be the
//     running kernel's), the TEE char device, tee-supplicant-ro (--disk
//     /dev/mmcblk0: its own read-only GPT parse; security opened O_RDONLY;
//     its RPC log on fd 3, never a data byte), tee_legacy_read (the PTA READ,
//     command 0 only), then the supplicant is stopped; the module stays
//     loaded (it has no clean unload). Nothing here writes the eMMC.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/plan"
)

const (
	// OpteeDir holds the reader's three files in the install image (root,
	// 0500; image/mkinitramfs.sh pins them too).
	OpteeDir        = "/usr/lib/uc-pxx-inst/optee"
	OpteeModule     = OpteeDir + "/optee_legacy.ko"
	OpteeSupplicant = OpteeDir + "/tee-supplicant-ro"
	OpteeClient     = OpteeDir + "/tee_legacy_read"
	// The pins: the module of the reviewed port 26dd90965 built against the
	// d02e9263 kbuild (vermagic 6.18.54-gd02e92631f28); the READ-only client
	// (PTA 1b484ea5..., command 0); the round-4 read-only supplicant (an FS
	// OPEN served only with flags exactly RDONLY or RDWR).
	OpteeModuleSHA256     = "5870e0fd31816310518e6576e30aeaf38e3c11a21a5cec2cfad4ec1176b2e02d"
	OpteeClientSHA256     = "1424ac1e34f913e2068d579afa87cafb5fc4e0bd2360f8d4fe0284999500c220"
	OpteeSupplicantSHA256 = "64c139f6a062776e00a7810d4a06e9e96eaa1fc787754efc5df6acc9427e2e0c"

	// LockRunDir holds the reader's record, start marker, copies and logs.
	LockRunDir      = "/run/uc-pxx-inst/optee"
	LockStateRecord = LockRunDir + "/lockstate.json"
	LockStarted     = LockRunDir + "/started"
	LockRPCLog      = LockRunDir + "/supplicant-rpc.log"
	lockSuppLog     = LockRunDir + "/supplicant.log"

	// TEEDevice is the legacy TEE char device of optee_legacy.ko.
	TEEDevice  = "/dev/opteearmtz00"
	teeSysDev  = "/sys/class/misc/opteearmtz00/dev"
	teeModule  = "/sys/module/optee_legacy"
	insmodPath = "/sbin/insmod"
	osRelease  = "/proc/sys/kernel/osrelease"
	// SecurityDev is the node of the partition security on the UC-P10
	// (RULES hard rule 3: SECPART is that partition only).
	SecurityDev = "mmcblk0p16"
	bcbLBA      = 49184
	maxOptee    = 4 << 20
)

// LockTEE is what the TEE part of the reader gives: the byte the client
// printed (nil: none), why it failed, the sha256 of the three files it ran
// and of the RPC log, and the name of a test stub ("" for the real one).
type LockTEE struct {
	Byte                       *byte
	Err                        error
	Module, Supplicant, Client string
	RPCLog                     string
	Stub                       string
}

// LockReader is the TEE part of the reader (MainlineOptions.LockReader; nil:
// teeRead, the image's). Only a devtest flag gives another (a stub).
type LockReader func(e *device.Env, d *device.Disk, say func(string)) LockTEE

// errLockChanged is the reader's STOP: the A/B state is not known (exit 3).
func errLockChanged(r *plan.LockStateRead) error {
	return fmt.Errorf("%w: STOP: the in-RAM lock_state reader saw security, the A/B sector or the BCB change around its read (before %+v, after %+v): keep the unit powered, do not restart, write nothing; ask for help",
		device.ErrABUnknown, r.Before, r.After)
}

// LockStateHeld says why every mainline plan and install of this boot
// refuses: the reader's record of this boot says Changed ("" = it does not).
func LockStateHeld(e *device.Env) string {
	b, err := os.ReadFile(e.Path(LockStateRecord))
	if err != nil {
		return ""
	}
	var r plan.LockStateRead
	if json.Unmarshal(b, &r) != nil || r.Changed {
		return "the in-RAM lock_state reader of this boot saw security, the A/B sector or the BCB change (or its record cannot be read): keep the unit powered, do not restart, write nothing; ask for help"
	}
	return ""
}

// lockState is the reader's run of this boot (see the file comment).
func (s *mlSession) lockState(say func(string)) (*plan.LockStateRead, error) {
	e := s.e
	if b, err := os.ReadFile(e.Path(LockStateRecord)); err == nil {
		var r plan.LockStateRead
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, fmt.Errorf("%w: the in-RAM lock_state record %s cannot be read (%v): keep the unit powered, ask for help", device.ErrABUnknown, LockStateRecord, err)
		}
		if r.BootID != s.bootID || r.Token != s.token {
			return nil, &device.Refusal{Why: fmt.Sprintf("the in-RAM lock_state record %s is of boot %s / token %s, not this boot %s / token %s: no second read",
				LockStateRecord, r.BootID, r.Token, s.bootID, s.token)}
		}
		say(fmt.Sprintf("lock_state: the in-RAM reader ran in this boot already (%s): lock_state %s%s", LockStateRecord, orNoneS(r.LockState), errNote(r.Error)))
		if r.Changed {
			return &r, errLockChanged(&r)
		}
		return &r, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, &device.Refusal{Why: fmt.Sprintf("the in-RAM lock_state record %s: %v", LockStateRecord, err)}
	}
	dir := e.Path(LockRunDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, &device.Refusal{Why: fmt.Sprintf("%s: %v", LockRunDir, err)}
	}
	r := &plan.LockStateRead{BootID: s.bootID, Token: s.token}
	f, err := os.OpenFile(e.Path(LockStarted), os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		// a read of this boot started before and left no record: never again
		r.Error = "an earlier lock_state read of this boot started and did not end (" + LockStarted + "): no second read in this boot"
		return r, s.saveLockState(r)
	}
	fmt.Fprintf(f, "boot %s token %s %s\n", s.bootID, s.token, time.Now().UTC().Format(time.RFC3339))
	f.Close()
	say("lock_state: the in-RAM reader (once in this boot): " + OpteeDir + " (module, supplicant, client), reads before and after")
	if r.Before, err = s.lockSums(); err != nil {
		r.Error = "the reads before the TEE read: " + err.Error()
		return r, s.saveLockState(r)
	}
	say(fmt.Sprintf("  before: security %s, A/B first 32 %s, BCB %s, eMMC read-only %v", short(r.Before.Security), short(r.Before.AB32), short(r.Before.BCB), r.Before.ReadOnly))
	if !r.Before.ReadOnly {
		r.Error = "the kernel read-only flag is not set on every eMMC node: no TEE read"
		r.After = r.Before
		return r, s.saveLockState(r)
	}
	read := s.o.LockReader
	if read == nil {
		read = teeRead
	}
	t := read(e, s.d, say)
	r.Module, r.Supplicant, r.Client, r.RPCLog, r.Stub = t.Module, t.Supplicant, t.Client, t.RPCLog, t.Stub
	switch {
	case t.Err != nil:
		r.Error = t.Err.Error()
	case t.Byte == nil:
		r.Error = "the client printed no lock_state"
	default:
		r.LockState = fmt.Sprintf("0x%02x", *t.Byte)
	}
	var aerr error
	r.After, aerr = s.lockSums()
	r.Changed = aerr != nil || r.After != r.Before || !r.After.ReadOnly
	if aerr != nil {
		r.Error = strings.TrimPrefix(r.Error+"; ", "; ") + "the reads after the TEE read: " + aerr.Error()
	}
	say(fmt.Sprintf("  after:  security %s, A/B first 32 %s, BCB %s, eMMC read-only %v: %s", short(r.After.Security), short(r.After.AB32), short(r.After.BCB), r.After.ReadOnly,
		map[bool]string{false: "SAME", true: "CHANGED"}[r.Changed]))
	say(fmt.Sprintf("  lock_state %s%s", orNoneS(r.LockState), errNote(r.Error)))
	if err := s.saveLockState(r); err != nil {
		return r, err
	}
	if r.Changed {
		return r, errLockChanged(r)
	}
	return r, nil
}

// saveLockState writes the record (a new file, then a rename, then sync).
func (s *mlSession) saveLockState(r *plan.LockStateRead) error {
	b, _ := json.MarshalIndent(r, "", "  ")
	tmp := s.e.Path(LockStateRecord + ".new")
	os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err == nil {
		_, err = f.Write(append(b, '\n'))
		if serr := f.Sync(); err == nil {
			err = serr
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err == nil {
		err = os.Rename(tmp, s.e.Path(LockStateRecord))
	}
	if err != nil {
		return fmt.Errorf("%w: the in-RAM lock_state record could not be written (%v): keep the unit powered, ask for help", device.ErrABUnknown, err)
	}
	return nil
}

// lockSums: the reader's own reads (O_DIRECT through the eMMC view).
func (s *mlSession) lockSums() (plan.ReadSums, error) {
	var z plan.ReadSums
	sec, ok := s.d.Part("security")
	if !ok {
		return z, errors.New("no partition security")
	}
	var err error
	if z.Security, err = s.d.HashRange(sec.Start*gpt.Sector, sec.Bytes()); err != nil {
		return z, fmt.Errorf("security: %w", err)
	}
	ab, err := s.d.ReadAB()
	if err != nil {
		return z, fmt.Errorf("the A/B sector: %w", err)
	}
	h := sha256.Sum256(ab[:32])
	z.AB32 = hex.EncodeToString(h[:])
	if z.BCB, err = s.d.HashRange(bcbLBA*gpt.Sector, 4*gpt.Sector); err != nil {
		return z, fmt.Errorf("the BCB: %w", err)
	}
	// review B-3: the loader area with the primary GPT, the backup GPT, misc
	if z.Boot, err = s.d.HashRange(0, 2048*gpt.Sector); err != nil {
		return z, fmt.Errorf("LBA 0..2047: %w", err)
	}
	if z.GPTBackup, err = s.d.HashRange((s.d.Sectors-33)*gpt.Sector, 33*gpt.Sector); err != nil {
		return z, fmt.Errorf("the backup GPT: %w", err)
	}
	misc, ok := s.d.Part("misc")
	if !ok {
		return z, errors.New("no partition misc")
	}
	if z.Misc, err = s.d.HashRange(misc.Start*gpt.Sector, misc.Bytes()); err != nil {
		return z, fmt.Errorf("misc: %w", err)
	}
	z.ReadOnly = len(s.d.ROOff()) == 0
	return z, nil
}

func short(h string) string {
	if len(h) > 16 {
		return h[:16] + "…"
	}
	return orNoneS(h)
}

func orNoneS(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func errNote(e string) string {
	if e == "" {
		return ""
	}
	return " (" + e + ")"
}

// pinnedCopy checks a file of the image (a regular file of root, not
// writable by group or others, at most maxOptee bytes, its sha256 the pin)
// and writes a private copy to dir (O_EXCL, mode), read back. It returns the
// copy's path and the bytes.
func pinnedCopy(e *device.Env, path, pin, dir string, mode os.FileMode) (string, []byte, error) {
	p := e.Path(path)
	st, err := os.Lstat(p)
	if err != nil {
		return "", nil, err
	}
	sys, _ := st.Sys().(*syscall.Stat_t)
	if !st.Mode().IsRegular() || st.Size() > maxOptee || sys == nil || sys.Uid != 0 || st.Mode().Perm()&0o022 != 0 {
		return "", nil, fmt.Errorf("%s must be a regular file of root of at most %d bytes, not writable by group or others", path, maxOptee)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", nil, err
	}
	h := sha256.Sum256(b)
	if got := hex.EncodeToString(h[:]); got != pin {
		return "", nil, fmt.Errorf("%s has sha256 %s, not the pinned %s", path, got, pin)
	}
	cp := filepath.Join(dir, filepath.Base(path))
	f, err := os.OpenFile(cp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, mode)
	if err != nil {
		return "", nil, err
	}
	_, werr := f.Write(b)
	if err := errors.Join(werr, f.Close()); err != nil {
		return "", nil, err
	}
	if back, err := os.ReadFile(cp); err != nil || !bytes.Equal(back, b) {
		return "", nil, fmt.Errorf("the private copy of %s differs from the checked bytes", path)
	}
	return cp, b, nil
}

// vermagicRelease is the kernel release of a module's vermagic= string.
func vermagicRelease(ko []byte) string {
	i := bytes.Index(ko, []byte("\x00vermagic="))
	if i < 0 {
		return ""
	}
	v := ko[i+len("\x00vermagic="):]
	if j := bytes.IndexByte(v, 0); j >= 0 {
		v = v[:j]
	}
	if f := strings.Fields(string(v)); len(f) > 0 {
		return f[0]
	}
	return ""
}

var lockLine = regexp.MustCompile(`(?m)^lock_state = (0x[0-9a-f]{2}) `)

// runFixed runs a fixed argv with PATH and LC_ALL only, for at most d; on
// the limit it is killed and not waited for (it may stay in the kernel).
func runFixed(d time.Duration, argv ...string) (int, string, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	cmd.Dir = "/"
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		return -1, "", err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var x *exec.ExitError
		if errors.As(err, &x) {
			return x.ExitCode(), out.String(), nil
		}
		if err != nil {
			return -1, out.String(), err
		}
		return 0, out.String(), nil
	case <-time.After(d):
		cmd.Process.Kill()
		return -1, "", fmt.Errorf("%s did not end in %s (killed)", filepath.Base(argv[0]), d)
	}
}

// teeRead is the image's TEE part (see the file comment). It runs only in
// the installer RAM image on the unit (the device mode checked the image).
func teeRead(e *device.Env, d *device.Disk, say func(string)) LockTEE {
	var t LockTEE
	fail := func(f string, a ...any) LockTEE { t.Err = fmt.Errorf(f, a...); return t }
	if _, err := os.Lstat(e.Path(teeModule)); err == nil {
		return fail("the module optee_legacy is loaded already (not by this reader): no read")
	}
	if pids := procsNamed(e, "tee-supplicant"); len(pids) > 0 {
		return fail("a tee-supplicant runs already (pid %v): no read", pids)
	}
	sec, ok := d.Part("security")
	if !ok || sec.Dev != SecurityDev || sec.Index != 16 {
		return fail("the partition security is not %s (GPT index 16): no read", SecurityDev)
	}
	bin := e.Path(LockRunDir + "/bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		return fail("%s/bin: %v", LockRunDir, err)
	}
	ko, kob, err := pinnedCopy(e, OpteeModule, OpteeModuleSHA256, bin, 0o400)
	if err != nil {
		return fail("the module: %v", err)
	}
	t.Module = OpteeModuleSHA256
	supp, _, err := pinnedCopy(e, OpteeSupplicant, OpteeSupplicantSHA256, bin, 0o500)
	if err != nil {
		return fail("the supplicant: %v", err)
	}
	t.Supplicant = OpteeSupplicantSHA256
	cli, _, err := pinnedCopy(e, OpteeClient, OpteeClientSHA256, bin, 0o500)
	if err != nil {
		return fail("the client: %v", err)
	}
	t.Client = OpteeClientSHA256
	rel, err := os.ReadFile(e.Path(osRelease))
	vm := vermagicRelease(kob)
	if err != nil || vm == "" || vm != strings.TrimSpace(string(rel)) {
		return fail("the module's vermagic release %q is not the running kernel's %q (%v)", vm, strings.TrimSpace(string(rel)), err)
	}
	say("  module vermagic " + vm + " = the running kernel; insmod " + OpteeModule + " (its checked copy)")
	if rc, out, err := runFixed(30*time.Second, insmodPath, ko); err != nil || rc != 0 {
		return fail("insmod: exit %d (%v): %s", rc, err, strings.TrimSpace(out))
	}
	// the TEE char device: a misc device (major 10) whose node is sysfs's
	var node string
	for i := 0; i < 40; i++ {
		if st, err := os.Stat(e.Path(TEEDevice)); err == nil && st.Mode()&os.ModeCharDevice != 0 {
			sys := st.Sys().(*syscall.Stat_t)
			node = fmt.Sprintf("%d:%d", (sys.Rdev>>8)&0xfff, (sys.Rdev&0xff)|((sys.Rdev>>12)&0xfff00))
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	sysdev, _ := os.ReadFile(e.Path(teeSysDev))
	if node == "" || strings.TrimSpace(string(sysdev)) != node || !strings.HasPrefix(node, "10:") {
		return fail("no TEE char device %s after 20 s, or not the misc device of sysfs (node %q, sysfs %q): the module bound no OP-TEE", TEEDevice, node, strings.TrimSpace(string(sysdev)))
	}
	say("  " + TEEDevice + ": char device " + node)
	lf, err := os.OpenFile(e.Path(LockRPCLog), os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fail("the RPC log: %v", err)
	}
	defer lf.Close()
	sl, err := os.OpenFile(e.Path(lockSuppLog), os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fail("the supplicant log: %v", err)
	}
	defer sl.Close()
	cmd := exec.Command(supp, "--disk", device.EMMCPath, "--rpc-log-fd", "3")
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	cmd.Dir = "/"
	cmd.Stdout, cmd.Stderr = sl, sl
	cmd.ExtraFiles = []*os.File{lf} // fd 3
	if err := cmd.Start(); err != nil {
		return fail("the supplicant: %v", err)
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	stop := func() {
		cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
			select {
			case <-exited:
			case <-time.After(5 * time.Second):
			}
		}
	}
	defer func() {
		lf.Sync()
		if b, err := os.ReadFile(e.Path(LockRPCLog)); err == nil {
			h := sha256.Sum256(b)
			t.RPCLog = hex.EncodeToString(h[:])
		}
	}()
	want := fmt.Sprintf("security = /dev/%s (partition %d, start LBA %d), O_RDONLY", sec.Dev, sec.Index, sec.Start)
	up := false
	for i := 0; i < 20 && !up; i++ {
		select {
		case <-exited:
			i = 20
			continue
		default:
		}
		fds, _ := os.ReadDir(e.Path(fmt.Sprintf("/proc/%d/fd", cmd.Process.Pid)))
		dev := false
		for _, fd := range fds {
			if l, err := os.Readlink(e.Path(fmt.Sprintf("/proc/%d/fd/%s", cmd.Process.Pid, fd.Name()))); err == nil && l == TEEDevice {
				dev = true
			}
		}
		if b, _ := os.ReadFile(e.Path(lockSuppLog)); dev && hasLine(b, want) {
			up = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !up {
		stop()
		return fail("the supplicant did not come up on /dev/%s (want its line %q and the TEE device open)", sec.Dev, want)
	}
	say("  supplicant up (pid " + fmt.Sprint(cmd.Process.Pid) + "): " + want)
	rc, out, rerr := runFixed(60*time.Second, cli)
	stop()
	m := lockLine.FindStringSubmatch(out)
	say(fmt.Sprintf("  client: exit %d %s", rc, strings.TrimSpace(firstLines(out, 3))))
	switch {
	case rerr != nil:
		return fail("the client: %v", rerr)
	case rc != 0:
		return fail("the client: exit %d (%s)", rc, strings.TrimSpace(firstLines(out, 3)))
	case m == nil:
		return fail("the client printed no lock_state line")
	}
	var v byte
	fmt.Sscanf(m[1], "0x%02x", &v)
	t.Byte = &v
	return t
}

func hasLine(b []byte, want string) bool {
	for _, l := range strings.Split(string(b), "\n") {
		if l == want {
			return true
		}
	}
	return false
}

func firstLines(s string, n int) string {
	l := strings.SplitN(s, "\n", n+1)
	if len(l) > n {
		l = l[:n]
	}
	return strings.Join(l, " | ")
}

// procsNamed: the pids whose /proc/N/comm starts with name (comm is cut at
// 15 bytes: tee-supplicant-ro is "tee-supplicant-").
func procsNamed(e *device.Env, name string) []string {
	var out []string
	ents, _ := os.ReadDir(e.Path("/proc"))
	for _, d := range ents {
		if d.Name() == "" || d.Name()[0] < '0' || d.Name()[0] > '9' {
			continue
		}
		if b, err := os.ReadFile(e.Path("/proc/" + d.Name() + "/comm")); err == nil && strings.HasPrefix(strings.TrimSpace(string(b)), name) {
			out = append(out, d.Name())
		}
	}
	return out
}
