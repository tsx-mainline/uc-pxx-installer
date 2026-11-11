//go:build install && step2

package step2

// The checks of the running system that are not of the eMMC (design P10,
// P16): tsx-autoupdate is quiet and its lock is held for the whole run; the
// network is up for the target's mark after the restart.

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/ext4"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/plan"
)

const (
	// AutoupdateLock is the lock of tsx-autoupdate (tsx-linux-common: one run
	// at a time, a non-blocking flock of $RUN/autoupdate.lock).
	AutoupdateLock = "/run/tsx/autoupdate.lock"
	// RestartBlock is tsx-autoupdate's "busy" or "hold N" file.
	RestartBlock = "/run/tsx/restart-block"
	// AutoupdateTool answers `get mode` (auto or manual) and sets the STOP
	// hold (`stop-hold set step2 REASON`, tsx-linux-common step2-stop-hold).
	AutoupdateTool = "/usr/local/sbin/tsx-autoupdate"
	// HoldsDir is tsx-autoupdate's durable hold folder: one file per owner
	// (STEP2-STOP-HOLD-2026-11-11.md §4). It survives a restart; the restart
	// block does not exist until `tsx-autoupdate boot` ran.
	HoldsDir = "/var/lib/tsx/autoupdate/holds"
	// DataHoldsDir is the same folder at tsx-data's place on /data (tsx-data
	// moves /var/lib/tsx to /data/var/lib/tsx and binds it back at every boot
	// that has /data).
	DataHoldsDir = "/data" + HoldsDir
	// DataMountPoint is where uc-pxx-lvm mounts the tsx data file system.
	DataMountPoint = "/data"
	// StopHoldOwner is step 2's owner name of the hold.
	StopHoldOwner = "step2"
	// StopHoldSetMarker records in this boot (tmpfs) that `stop-hold set`
	// gave exit 0: it is never run again in this boot (a repeat would undo a
	// person's clear: STEP2-STOP-HOLD review MINOR-1).
	StopHoldSetMarker = "/run/uc-pxx-step2.stop-hold-set"
)

// SlotChanged are the markers of the slot-side updaters (an update of that
// slot is between its arm and its restart).
var SlotChanged = []string{"/run/uc-pxx-slot-a-changed", "/run/uc-pxx-slot-b-changed"}

// DataLVMParts are the partitions whose LVM2 label means a phase-1 tsx data
// volume may exist (uc-pxx-linux docs/rootfs.md: the volume group tsx over
// vendor_a and oem_a; the _b partitions too, to fail closed).
var DataLVMParts = []string{"vendor_a", "oem_a", "vendor_b", "oem_b"}

// Live are the checks of the running system. Tests give their own.
type Live struct {
	// NetworkUp: eth0 or usb0 has an IPv4 address and sshd runs (P16).
	NetworkUp func() bool
	// LockAutoupdate takes the tsx-autoupdate lock (non-blocking) and
	// returns its release.
	LockAutoupdate func() (func(), error)
	// Quiet: tsx-autoupdate is in mode manual, no restart block or hold (also
	// no durable hold in HoldsDir, DataHoldsDir or the running root's own
	// HoldsDir read raw, and no tsx data file system that is not this boot's
	// /data: HoldsClear), no slot-changed marker.
	Quiet func() error
	// SetStopHold runs `tsx-autoupdate stop-hold set step2 REASON` and gives
	// its exit code (-1: it did not run or end in 30 s). nil: not available
	// (the STOP keeps the lock and the "hold 4" block only).
	SetStopHold func(reason string) int
}

func (l *Live) setStopHold(reason string) int {
	if l.SetStopHold == nil {
		return -1
	}
	return l.SetStopHold(reason)
}

// HoldsClear fails closed (STEP2-STOP-HOLD review MAJOR-1; MAINLINE-RC-REVIEW
// MAJOR-1 and re-look MINOR-4). It refuses in each of these cases:
//
//  1. tsx-autoupdate's hold folder (HoldsDir, the folder it reads in this
//     boot) has any entry but a dot file, or is there and is not a readable
//     folder (a file, a link, no access). After a restart the restart block is
//     gone until `tsx-autoupdate boot` ran, so the block alone would let step
//     2 write while the last boot's STOP is set. A missing hold folder is "no
//     hold here" only when tsx-autoupdate's state folder
//     (/var/lib/tsx/autoupdate) is a folder: the STOP-hold tsx-autoupdate
//     (7aafeb2) makes `holds` only at the first `stop-hold set`.
//  2. The hold folder at tsx-data's place on /data (DataHoldsDir) has any
//     entry but a dot file, or is there and is not a readable folder. When
//     /var/lib/tsx is tsx-data's bind of /data/var/lib/tsx this is the same
//     folder as 1; when the bind is missing in this boot, it is where a STOP
//     of an earlier boot is.
//  3. The hold folder of the running root file system itself (RootHolds),
//     read raw from its partition beneath any mount: a STOP set in a boot
//     where /data did not mount is on the root file system's own
//     /var/lib/tsx, and a later boot's tsx-data bind of /data/var/lib/tsx
//     hides it from 1, 2 and tsx-autoupdate (MAINLINE-RC2-LS-REVIEW A-1, the
//     mirror case of MINOR-4).
//  4. A tsx data file system on the eMMC is not this boot's /data (see
//     DataVisible): a STOP set while /var/lib/tsx was on that file system
//     cannot be seen in 1 or 2 (re-look MINOR-4: "missing = no hold" passed
//     while the hold sat on a /data that did not mount).
//
// d is the eMMC of this boot (read only); nil refuses.
func HoldsClear(e *device.Env, d *device.Disk) error {
	if err := holdFolder(e, HoldsDir, true); err != nil {
		return err
	}
	if err := holdFolder(e, DataHoldsDir, false); err != nil {
		return err
	}
	if err := RootHolds(e, d); err != nil {
		return err
	}
	return DataVisible(e, d)
}

// RootHolds reads tsx-autoupdate's hold folder (HoldsDir) of the running
// root file system raw: the partition mounted on / (exactly one of system_a
// and system_b), through its own node (O_DIRECT), with package ext4, so a
// bind mount over /var/lib/tsx (tsx-data's of /data/var/lib/tsx) cannot
// hide it (MAINLINE-RC2-LS-REVIEW A-1). First a syncfs(2) of the root file
// system commits its journal, so the read sees this boot's own changes
// (O_DIRECT writes the node's dirty cache first). It refuses when the hold
// folder has any entry but a dot file, when the folder or a folder on its
// path is anything but a folder (a link too: it is not followed), and on
// every read error (no eMMC view, no single root partition, no ext4). A
// missing folder on the path is no hold. backup is never read.
func RootHolds(e *device.Env, d *device.Disk) error {
	if d == nil {
		return refuse("no eMMC view for the hold folder of the root file system: step 2 waits")
	}
	ms, err := e.SlotMounts(d)
	if err != nil {
		return err
	}
	var roots []string
	for _, m := range ms {
		if m.Point == "/" {
			roots = append(roots, m.Part)
		}
	}
	if len(roots) != 1 || (roots[0] != "system_a" && roots[0] != "system_b") {
		return refuse("the running root is not one system partition of the eMMC (%q): step 2 waits", roots)
	}
	part := roots[0]
	if err := syncfs(e.Path("/")); err != nil {
		return refuse("syncfs of the root file system: %v: step 2 waits", err)
	}
	r, size, closeFn, err := d.PartReader(part)
	if err != nil {
		return refuse("%s: %v: step 2 waits", part, err)
	}
	defer closeFn()
	fs, err := ext4.Open(r, size)
	if err != nil {
		return refuse("%s (the running root): %v: step 2 waits", part, err)
	}
	names, found, err := fs.ReadDirNoFollow(HoldsDir, 4096)
	if err != nil {
		return refuse("the hold folder of the running root file system (%s:%s) cannot be read: %v: step 2 waits", part, HoldsDir, err)
	}
	if !found {
		return nil
	}
	var held []string
	for _, n := range names {
		if !strings.HasPrefix(n, ".") {
			held = append(held, n)
		}
	}
	if len(held) > 0 {
		return refuse("tsx-autoupdate has a hold set on the running root file system's own folder (%s:%s: %s), which a bind mount of this boot may hide: step 2 waits until a person checked the unit and cleared it",
			part, HoldsDir, strings.Join(held, ", "))
	}
	return nil
}

// syncfs commits the file system of dir (syncfs(2)).
func syncfs(dir string) error {
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

// holdFolder checks one hold folder. live: the folder tsx-autoupdate reads in
// this boot (missing = no hold only with its state folder a folder); else
// the /data place (missing, as any part of its path, = nothing there).
func holdFolder(e *device.Env, dir string, live bool) error {
	p := e.Path(dir)
	st, err := os.Lstat(p)
	switch {
	case errors.Is(err, os.ErrNotExist) && !live:
		return nil
	case errors.Is(err, os.ErrNotExist):
		if pst, perr := os.Lstat(e.Path(filepath.Dir(dir))); perr == nil && pst.IsDir() {
			return nil // never a hold here: tsx-autoupdate makes the folder at the first set
		}
		return refuse("tsx-autoupdate's state folder %s is not a folder and %s is missing: step 2 waits", filepath.Dir(dir), dir)
	case err != nil:
		return refuse("the hold folder %s cannot be read (%v): step 2 waits", dir, err)
	case !st.IsDir():
		return refuse("the hold folder %s is not a folder (%s): step 2 waits", dir, st.Mode().Type())
	}
	ents, err := os.ReadDir(p)
	if err != nil {
		return refuse("the hold folder %s cannot be read (%v): step 2 waits", dir, err)
	}
	var held []string
	for _, d := range ents {
		if !strings.HasPrefix(d.Name(), ".") {
			held = append(held, d.Name())
		}
	}
	if len(held) > 0 {
		return refuse("tsx-autoupdate has a hold set (%s: %s): step 2 waits until a person checked the unit and cleared it",
			dir, strings.Join(held, ", "))
	}
	return nil
}

// DataVisible refuses when a tsx data file system on the eMMC is not this
// boot's /data, so that a STOP hold on it would be hidden (MAINLINE-RC-REVIEW
// re-look MINOR-4). uc-pxx-lvm mounts /data from userdata when it holds ext4
// with the label tsxdata, else from the LVM volume tsx/data (phase 1, over
// vendor_a and oem_a); tsx-data then binds /data/var/lib/tsx onto
// /var/lib/tsx. The rules, from the eMMC read now (O_DIRECT) and
// /proc/self/mountinfo (the last /data line is the one in use):
//
//   - userdata holds ext4 label tsxdata: /data must be mounted from userdata
//     (its device number), ext4, its whole file system (root "/");
//   - else, any of vendor_a, oem_a, vendor_b, oem_b holds an LVM2 physical
//     volume label ("LABELONE" in one of its first four sectors): /data must
//     be an ext4 mount of a whole file system (the LVM volume);
//   - else: no tsx data file system, /data is a folder of the root file
//     system: nothing more to check.
//
// Every read failure refuses. Only these partitions are read (not backup:
// its key records are never read here).
func DataVisible(e *device.Env, d *device.Disk) error {
	if d == nil {
		return refuse("no eMMC view for the /data check: step 2 waits")
	}
	ud, ok := d.Part(plan.DataPartition)
	if !ok {
		return refuse("no %s partition: step 2 waits", plan.DataPartition)
	}
	v := d.View()
	defer v.Close()
	state := plan.DataState(v, ud.Start*gpt.Sector)
	if strings.HasPrefix(state, "unreadable") {
		return refuse("%s: %s: step 2 waits", plan.DataPartition, state)
	}
	var lvm []string
	for _, n := range DataLVMParts {
		p, ok := d.Part(n)
		if !ok {
			continue
		}
		b := make([]byte, 4*gpt.Sector)
		if _, err := v.ReadAt(b, p.Start*gpt.Sector); err != nil {
			return refuse("%s: %v: step 2 waits", n, err)
		}
		for i := 0; i < 4; i++ {
			if bytes.Equal(b[i*gpt.Sector:i*gpt.Sector+8], []byte("LABELONE")) {
				lvm = append(lvm, n)
				break
			}
		}
	}
	m, err := dataMount(e)
	if err != nil {
		return err
	}
	switch {
	case state == "ext4 label "+plan.DataLabel:
		if m == nil || m.major != ud.Major || m.minor != ud.Minor || m.fstype != "ext4" || m.root != "/" {
			return refuse("%s holds the tsx data file system (ext4 %s), but /data is not mounted from it (%s): a STOP hold of an earlier boot may be on it; step 2 waits until a person checked the unit",
				plan.DataPartition, plan.DataLabel, m)
		}
	case len(lvm) > 0:
		if m == nil || m.fstype != "ext4" || m.root != "/" {
			return refuse("%s holds an LVM volume (the phase-1 tsx data volume), but /data is not mounted from an ext4 file system (%s): a STOP hold of an earlier boot may be on it; step 2 waits until a person checked the unit",
				strings.Join(lvm, ", "), m)
		}
	}
	return nil
}

// mount is one line of /proc/self/mountinfo.
type mount struct {
	major, minor uint32
	root, fstype string
}

func (m *mount) String() string {
	if m == nil {
		return "/data is not a mount point"
	}
	return fmt.Sprintf("/data is %d:%d root %s %s", m.major, m.minor, m.root, m.fstype)
}

// dataMount is the last mountinfo line whose mount point is /data (nil:
// none). An unreadable mountinfo or a /data line it cannot parse refuses.
func dataMount(e *device.Env) (*mount, error) {
	b, err := os.ReadFile(e.Path(device.MountInfoPath))
	if err != nil {
		return nil, refuse("cannot read %s: %v", device.MountInfoPath, err)
	}
	var out *mount
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) < 5 || f[4] != DataMountPoint {
			continue
		}
		sep := -1
		for i := 6; i < len(f); i++ {
			if f[i] == "-" {
				sep = i
				break
			}
		}
		a, c, ok := strings.Cut(f[2], ":")
		ma, e1 := strconv.ParseUint(a, 10, 32)
		mi, e2 := strconv.ParseUint(c, 10, 32)
		if sep < 0 || sep+1 >= len(f) || !ok || e1 != nil || e2 != nil {
			return nil, refuse("%s: a /data line that cannot be read: %q", device.MountInfoPath, l)
		}
		out = &mount{major: uint32(ma), minor: uint32(mi), root: f[3], fstype: f[sep+1]}
	}
	return out, nil
}

// runStopHoldSet is the default SetStopHold: the fixed argv, PATH and
// LC_ALL, 30 s. The reason is one line of at most 200 characters.
func runStopHoldSet(e *device.Env, reason string) int {
	cmd := exec.Command(e.Path(AutoupdateTool), "stop-hold", "set", StopHoldOwner, firstLine(reason))
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	if err := cmd.Start(); err != nil {
		return -1
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			return 0
		}
		var x *exec.ExitError
		if errors.As(err, &x) {
			return x.ExitCode()
		}
		return -1
	case <-time.After(30 * time.Second):
		cmd.Process.Kill()
		return -1
	}
}

func (l *Live) lockAutoupdate() (func(), error) {
	if l.LockAutoupdate == nil {
		return nil, refuse("no tsx-autoupdate lock check")
	}
	return l.LockAutoupdate()
}

func (l *Live) quiet() error {
	if l.Quiet == nil {
		return refuse("no tsx-autoupdate check")
	}
	return l.Quiet()
}

// LiveChecks are the checks of the unit, inside e's root. d is the eMMC of
// this boot (for Quiet's /data check, read only; nil: Quiet refuses).
func LiveChecks(e *device.Env, d *device.Disk) *Live {
	return &Live{
		NetworkUp: func() bool { return (ipv4Up("eth0") || ipv4Up("usb0")) && sshdRuns(e) },
		LockAutoupdate: func() (func(), error) {
			p := e.Path(AutoupdateLock)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return nil, refuse("%s: %v", AutoupdateLock, err)
			}
			f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_APPEND|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o644)
			if err != nil {
				return nil, refuse("%s: %v", AutoupdateLock, err)
			}
			if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
				f.Close()
				return nil, refuse("tsx-autoupdate runs now (%s is held): try again later", AutoupdateLock)
			}
			return func() { f.Close() }, nil
		},
		SetStopHold: func(reason string) int { return runStopHoldSet(e, reason) },
		Quiet: func() error {
			if err := HoldsClear(e, d); err != nil {
				return err
			}
			if _, err := os.Lstat(e.Path(RestartBlock)); err == nil {
				b, _ := os.ReadFile(e.Path(RestartBlock))
				return refuse("tsx-autoupdate has a restart block or a hold (%s: %q): step 2 waits until a person cleared it",
					RestartBlock, strings.TrimSpace(string(b)))
			}
			for _, m := range SlotChanged {
				if _, err := os.Lstat(e.Path(m)); err == nil {
					return refuse("%s exists: a slot update waits for its restart", m)
				}
			}
			cmd := exec.Command(e.Path(AutoupdateTool), "get", "mode")
			cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
			var out bytes.Buffer
			cmd.Stdout = &out
			done := make(chan error, 1)
			if err := cmd.Start(); err != nil {
				return refuse("tsx-autoupdate get mode: %v", err)
			}
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				if err != nil || strings.TrimSpace(out.String()) != "manual" {
					return refuse("tsx-autoupdate get mode gives %q (%v): step 2 runs only in mode manual", strings.TrimSpace(out.String()), err)
				}
			case <-time.After(30 * time.Second):
				cmd.Process.Kill()
				return refuse("tsx-autoupdate get mode did not answer in 30 s")
			}
			return nil
		},
	}
}

func ipv4Up(name string) bool {
	i, err := net.InterfaceByName(name)
	if err != nil || i.Flags&net.FlagUp == 0 {
		return false
	}
	as, err := i.Addrs()
	if err != nil {
		return false
	}
	for _, a := range as {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() {
			return true
		}
	}
	return false
}

// sshdRuns: a process named sshd (from /proc/N/comm).
func sshdRuns(e *device.Env) bool {
	ents, _ := os.ReadDir(e.Path("/proc"))
	for _, d := range ents {
		if _, err := strconv.Atoi(d.Name()); err != nil {
			continue
		}
		if b, err := os.ReadFile(e.Path(fmt.Sprintf("/proc/%s/comm", d.Name()))); err == nil && strings.TrimSpace(string(b)) == "sshd" {
			return true
		}
	}
	return false
}
