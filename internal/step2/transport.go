//go:build install && step2

package step2

// The payload receive of step 2 over the USB cable (component C2 of
// STEP2-USB-TRANSPORT-2026-11-11 §6; STEP2-USB-BUILD-2026-11-11 §6 items 1-10;
// mainline-rc-4). In a STEP-2 build the uc-pxx-usb service gives the status
// for the page; while step 2 waits for its payload the step-2 service takes
// the gadget over (it stops uc-pxx-usb), runs uc-pxx-xfer in the receive
// mode with the pins of its own gate, and seals the complete folder itself:
//
//  1. only in the branch where the service would ask the question (this
//     slot successful and booting, no done or never record, a payload folder
//     configured and not there yet), and on a supervised unit only when a
//     supervised session allows it (USBReceiveMarker);
//  2. the stage: tmpfs StageDir (root, 0711, StageSize, nosuid, nodev,
//     noexec); `uc-pxx-xfer cleanup` before each run (review F9);
//  3. the pins: mainline.StepTwoManifests of this binary's gate (never a
//     second list); none: every manifest is refused (by design);
//  4. a run that ends by itself is started again, at most maxRestarts times
//     (review F9: an exit 1, an OOM kill); the panel shows the receive;
//     Back ends the wait;
//  5. the seal on a complete folder (reviews F5, F7): the receiver stopped
//     and waited for; no process of its user left (handover.NoProcessOf);
//     handover.CheckReady with the same pins; one rename into
//     StageDir/payload/uc-pxx-mainline; the owner and modes changed without
//     following a link (root, 0555/0444); checked again as root; AUTO-STEP2
//     only when the record says so; the record removed;
//  6. then uc-pxx-xfer in the status-only mode until the restart (or the end
//     of the service), the info file (root, 0644, fixed strings: review F14).
//
// Nothing here writes the eMMC: the step-2 write path (Plan, Start, the
// engine) is unchanged and runs on the sealed folder exactly as on a drive.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/step2ui"
	"uc-pxx-installer/internal/xfer/handover"
)

const (
	// StageDir is the tmpfs of the payload receive.
	StageDir = "/run/uc-pxx-step2/stage"
	// StagePayload is the sealed payload folder: the fixed
	// UC_PXX_STEP2_PAYLOAD of a STEP-2 build's conf file.
	StagePayload = StageDir + "/payload/" + mainline.Dir
	// XferInfo is the info file of the service's own transport.
	XferInfo = "/run/uc-pxx-step2/xfer-info.json"
	// USBReceiveMarker: on a supervised unit the receive runs only when a
	// supervised session made this file (in RAM, for this boot).
	USBReceiveMarker = "/run/uc-pxx-step2/usb-receive"
	// StageSize is the size of the stage tmpfs (the payload is 1118 MiB).
	StageSize = 1200 << 20
	// MaxReceiveWait is how long the service waits for a payload.
	MaxReceiveWait = 2 * time.Hour
	maxRestarts    = 3
)

// Transport is the USB transport of the service (LiveTransport on a unit).
type Transport interface {
	// Take stops the uc-pxx-usb service (step 2 owns the gadget now); Give
	// starts it again.
	Take() error
	Give() error
	MountStage() error
	UnmountStage() error
	// Cleanup is `uc-pxx-xfer cleanup --stage StageDir`.
	Cleanup() error
	// Start runs `uc-pxx-xfer run` in the receive mode with pins, or the
	// status-only mode.
	Start(statusOnly bool, pins []string) (Proc, error)
	// UID is the receiver's user id.
	UID() (int, error)
	// NoProcessOf is handover.NoProcessOf (a test replaces it).
	NoProcessOf(uid int) error
}

// Proc is a running uc-pxx-xfer.
type Proc interface {
	// Stop sends SIGTERM and waits for the end.
	Stop() error
	// Done is closed when the process ended (by itself or by Stop).
	Done() <-chan struct{}
}

func confUSB(e *device.Env) bool {
	b, err := readRegular(e.Path(ConfFile), 64<<10)
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), "=")
		if ok && k == "UC_PXX_STEP2_USB" && strings.Trim(v, "\"'") == "yes" {
			return true
		}
	}
	return false
}

// usbRun is the transport of one service run.
type usbRun struct {
	tr     Transport
	status Proc // the status-only transport after the seal
	given  bool
}

// end stops the status-only transport, frees the stage (unless keep) and
// gives the gadget back to uc-pxx-usb.
func (u *usbRun) end(keep bool) {
	if u == nil || u.given {
		return
	}
	u.given = true
	if u.status != nil {
		u.status.Stop()
	}
	if !keep {
		u.tr.UnmountStage()
	}
	u.tr.Give()
}

// receivePayload waits for the payload over the USB cable and seals it (see
// the file comment). sealed false: nothing was sealed (cancelled, timed out,
// or the transport failed); the stage is freed and the gadget given back.
func receivePayload(e *device.Env, o Options, se ServiceEnv, pn Panel, pins []string) (*usbRun, *handover.Record, error) {
	tr := se.Transport
	info := func(code string, lines ...string) { writeInfo(e, o, code, lines...) }
	if err := tr.Take(); err != nil {
		return nil, nil, fmt.Errorf("uc-pxx-usb did not stop: %w", err)
	}
	u := &usbRun{tr: tr}
	fail := func(err error) (*usbRun, *handover.Record, error) {
		u.end(false)
		return nil, nil, err
	}
	if err := tr.MountStage(); err != nil {
		return fail(fmt.Errorf("the stage: %w", err))
	}
	uid, err := tr.UID()
	if err != nil {
		return fail(err)
	}
	start := func() (Proc, error) {
		if err := tr.Cleanup(); err != nil {
			return nil, fmt.Errorf("uc-pxx-xfer cleanup: %w", err)
		}
		return tr.Start(false, pins)
	}
	p, err := start()
	if err != nil {
		return fail(err)
	}
	info("waiting-payload", "Step 2 waits for its payload: send it from the installer page.")
	if pn != nil {
		pn.Show([]string{"Step 2: connect the computer with the USB-C cable.", "The installer page sends the mainline payload to this unit.",
			"Nothing is written before you confirm on this screen. Back (Speaker) = not now."})
	}
	deadline := se.Now().Add(MaxReceiveWait)
	restarts := 0
	xd := e.Path(filepath.Join(StageDir, "xfer"))
	var keys <-chan step2ui.Key
	if pn != nil {
		keys = pn.Keys()
	}
	last := ""
	for {
		st, _ := handover.ReadState(xd)
		if st != nil && st.State == handover.StateComplete {
			break
		}
		if st != nil && st.State == handover.StateReceiving && pn != nil {
			l := fmt.Sprintf("Receiving step 2: %s %d of %d MiB. Do not disconnect.", st.File, st.Bytes>>20, st.Total>>20)
			if l != last {
				pn.Show([]string{l, "Back (Speaker) = not now."})
				info("receiving", l)
				last = l
			}
		}
		select {
		case <-p.Done():
			if restarts >= maxRestarts {
				info("transport failed", "The USB transport ended "+fmt.Sprint(restarts+1)+" times: step 2 does not wait any more.")
				return fail(errors.New("uc-pxx-xfer ended by itself too often"))
			}
			restarts++
			if p, err = start(); err != nil {
				return fail(err)
			}
			continue
		case k := <-keys:
			if k == step2ui.KeyBack {
				p.Stop()
				info("not now", "Step 2 does not wait for the payload any more (Back on the unit).")
				return fail(nil)
			}
		case <-time.After(time.Second):
		}
		if !se.Now().Before(deadline) {
			p.Stop()
			info("not now", "No payload came: step 2 does not wait any more.")
			return fail(nil)
		}
	}
	// the seal
	info("sealing", "The payload was received: the unit checks it.")
	if err := p.Stop(); err != nil {
		return fail(fmt.Errorf("stop the receiver: %w", err))
	}
	rec, err := sealPayload(e, tr, uid, pins)
	if err != nil {
		info("plan-refused: the seal", firstLine(err.Error()))
		return fail(fmt.Errorf("the seal: %w", err))
	}
	if u.status, err = func() (Proc, error) {
		if err := tr.Cleanup(); err != nil {
			return nil, err
		}
		return tr.Start(true, nil)
	}(); err != nil {
		u.status = nil // the status is only for the page: step 2 goes on without it
	}
	info("sealed", "The payload is checked and sealed in RAM.")
	return u, rec, nil
}

// sealPayload is the seal of item 5 of the file comment.
func sealPayload(e *device.Env, tr Transport, uid int, pins []string) (*handover.Record, error) {
	if err := tr.NoProcessOf(uid); err != nil {
		return nil, err
	}
	xd := e.Path(filepath.Join(StageDir, "xfer"))
	ready, err := handover.CheckReady(xd, uid, pins)
	if err != nil {
		return nil, err
	}
	pd := e.Path(filepath.Dir(StagePayload))
	if err := os.Mkdir(pd, 0o755); err != nil && !os.IsExist(err) {
		return nil, err
	}
	if fi, err := os.Lstat(pd); err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is not a folder", pd)
	}
	dst := e.Path(StagePayload)
	if _, err := os.Lstat(dst); err == nil {
		return nil, fmt.Errorf("%s exists: a payload was sealed before", dst)
	}
	if err := os.Rename(ready.Folder, dst); err != nil {
		return nil, err
	}
	names := []string{handover.ManifestName, handover.TemplateName, handover.RootfsName}
	for _, n := range names {
		if err := ownRoot(filepath.Join(dst, n), 0o444, e.IsTest()); err != nil {
			return nil, err
		}
	}
	if err := ownRoot(dst, 0o555, e.IsTest()); err != nil {
		return nil, err
	}
	if err := checkSealed(dst, ready.Record, e.IsTest()); err != nil {
		return nil, err
	}
	if ready.Record.Auto {
		if err := writeFile(filepath.Join(pd, step2ui.AutoFile), []byte("the person chose on the page: continue by itself\n"), true); err != nil {
			return nil, err
		}
	}
	os.Remove(filepath.Join(xd, handover.ReadyRecord))
	return &ready.Record, nil
}

// ownRoot gives p to root with mode m, without following a link: the file is
// opened with O_NOFOLLOW (a folder with O_DIRECTORY) and changed by its fd.
// In a test (not root) only the mode changes.
func ownRoot(p string, m os.FileMode, test bool) error {
	flags := os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	f, err := os.OpenFile(p, flags, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	if !test {
		if err := f.Chown(0, 0); err != nil {
			return err
		}
	}
	return f.Chmod(m)
}

// checkSealed: the sealed folder, as root (review F7): exactly the three
// files, each a regular file with one link, of root (on a unit), mode 0444,
// the record's size; the folder of root, 0555.
func checkSealed(dir string, r handover.Record, test bool) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() || fi.Mode().Perm() != 0o555 || (!test && !rootOwned(fi)) {
		return fmt.Errorf("%s: not a sealed folder (root, 0555)", dir)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var got []string
	for _, x := range ents {
		got = append(got, x.Name())
	}
	sort.Strings(got)
	want := []string{handover.TemplateName, handover.ManifestName, handover.RootfsName}
	sort.Strings(want)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		return fmt.Errorf("%s holds %v", dir, got)
	}
	for _, f := range r.Files {
		fi, err := os.Lstat(filepath.Join(dir, f.Name))
		if err != nil {
			return err
		}
		st, _ := fi.Sys().(*syscall.Stat_t)
		if !fi.Mode().IsRegular() || st == nil || st.Nlink != 1 || fi.Mode().Perm() != 0o444 || fi.Size() != f.Size || (!test && st.Uid != 0) {
			return fmt.Errorf("%s: not a sealed file (regular, one link, root, 0444, %d bytes)", f.Name, f.Size)
		}
	}
	return nil
}

func rootOwned(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Uid == 0
}
