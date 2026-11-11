//go:build install && step2

package step2

// The data hook of a step-2 run (install.OtherSlotOptions.Data): the plan
// steps data-format and data-migrate run uc-pxx-data-migrate of uc-pxx-linux
// (format --yes, migrate --yes), pinned by its sha256, from a private copy of
// the checked bytes. Around each run the session checks with its own reads:
//
//   - before: the target is disabled {0,0,0} in the A/B sector read fresh
//     (the tool checks it too, with uc-pxx-bootctl status), userdata is not
//     mounted, and holds what the plan saw (format) or ext4 tsxdata-new
//     (migrate);
//   - after: userdata holds ext4 tsxdata-new (format) or ext4 tsxdata
//     (migrate), read O_DIRECT from the eMMC.
//
// Any other result stops the run before the arm: the target stays disabled,
// the keeper boots and keeps its data on its own root file system.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/plan"
)

const (
	// DataToolPath and DataToolSHA256: uc-pxx-data-migrate of uc-pxx-linux
	// mainline-linux-int a544817 (MAINLINE-LINUX-INT-2026-11-10.md §5,
	// reviews REVIEW-2 and its re-looks GO).
	DataToolPath   = "/usr/sbin/uc-pxx-data-migrate"
	DataToolSHA256 = "9162c65df08ac5577e38a2fe124d8cb1434b9b2bcfe787d6fba76055b8006791"
	// dataRunDir holds the private copies.
	dataRunDir  = "/run/uc-pxx-step2"
	maxDataTool = 1 << 20
	// dataTimeout: a migrate copies the panel data; the tool is never killed
	// before this (a format of 6.7 GiB with discard takes seconds).
	dataTimeout = 60 * time.Minute
)

// DataRunner runs one op of the data tool ("format" or "migrate") and
// returns its exit code and output.
type DataRunner interface {
	Run(op string) (rc int, out string, err error)
}

type pinnedTool struct{ e *device.Env }

// PinnedDataTool is the release's data runner.
func PinnedDataTool(e *device.Env) DataRunner { return pinnedTool{e: e} }

func (t pinnedTool) Run(op string) (int, string, error) {
	if op != "format" && op != "migrate" {
		return -1, "", fmt.Errorf("the data tool has no op %q here", op)
	}
	p := t.e.Path(DataToolPath)
	st, err := os.Lstat(p)
	if err != nil {
		return -1, "", err
	}
	sys, _ := st.Sys().(*syscall.Stat_t)
	if !st.Mode().IsRegular() || st.Size() > maxDataTool || sys == nil || sys.Uid != 0 || st.Mode().Perm()&0o022 != 0 {
		return -1, "", fmt.Errorf("%s must be a regular file of at most %d bytes, owned by root, not writable by group or others", DataToolPath, maxDataTool)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return -1, "", err
	}
	s := sha256.Sum256(b)
	if got := hex.EncodeToString(s[:]); got != DataToolSHA256 {
		return -1, "", fmt.Errorf("%s has sha256 %s, not the reviewed %s: refusing to run it", DataToolPath, got, DataToolSHA256)
	}
	base := t.e.Path(dataRunDir)
	if err := os.MkdirAll(base, 0o700); err != nil {
		return -1, "", err
	}
	dir, err := os.MkdirTemp(base, "data-")
	if err != nil {
		return -1, "", err
	}
	defer os.RemoveAll(dir)
	exe := filepath.Join(dir, "uc-pxx-data-migrate")
	f, err := os.OpenFile(exe, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o700)
	if err != nil {
		return -1, "", err
	}
	_, werr := f.Write(b)
	if err := errors.Join(werr, f.Close()); err != nil {
		return -1, "", err
	}
	if back, err := os.ReadFile(exe); err != nil || !bytes.Equal(back, b) {
		return -1, "", errors.New("the private copy of the data tool differs from the checked bytes")
	}
	cmd := exec.Command("/bin/sh", exe, op, "--yes")
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	cmd.Dir = "/"
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		return -1, "", err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), out.String(), nil
		}
		if err != nil {
			return -1, out.String(), err
		}
		return 0, out.String(), nil
	case <-time.After(dataTimeout):
		return -1, out.String(), fmt.Errorf("the data tool %s still runs after %s: stopped waiting (it was not killed)", op, dataTimeout)
	}
}

// userdata reads what userdata holds now (O_DIRECT).
func (s *session) userdata() (string, error) {
	ud, ok := s.d.Table.Find(plan.DataPartition)
	if !ok {
		return "", errors.New("no userdata partition")
	}
	v := s.d.View()
	defer v.Close()
	return plan.DataState(v, ud.Start*gpt.Sector), nil
}

// dataHook is install.OtherSlotOptions.Data of a session.
func (s *session) dataHook(dr DataRunner) func(op string, d *plan.DataPlan) error {
	return func(op string, d *plan.DataPlan) error {
		tool := map[string]string{plan.OpDataFormat: "format", plan.OpDataMigrate: "migrate"}[op]
		if tool == "" || d == nil || d.Partition != plan.DataPartition || d.Label != plan.DataLabel || d.FormatAs != plan.DataFormatLabel {
			return fmt.Errorf("data step %q of %+v: not a step of the step-2 data rule", op, d)
		}
		sec, err := s.d.ReadABFresh()
		if err != nil {
			return fmt.Errorf("%s: read the A/B sector: %w", op, err)
		}
		a, _ := avbab.Parse(sec)
		ti := 1
		if s.ru.Slot == "b" {
			ti = 0
		}
		if a.Validate() != nil || a.Slots[ti] != (avbab.Slot{}) || !a.Slots[1-ti].Keeper() {
			return fmt.Errorf("%s: the target slot %s is not disabled {0,0,0} with the keeper successful: userdata could still be the data of a slot that boots",
				op, avbab.SlotName(ti))
		}
		if err := s.mounts(true); err != nil {
			return fmt.Errorf("%s: %w", op, err)
		}
		before, err := s.userdata()
		if err != nil {
			return err
		}
		want := map[string]string{"format": d.Before, "migrate": "ext4 label " + plan.DataFormatLabel}[tool]
		if before != want || before == "ext4 label "+plan.DataLabel {
			return fmt.Errorf("%s: userdata holds %s, want %s", op, before, want)
		}
		rc, out, rerr := dr.Run(tool)
		s.say("     uc-pxx-data-migrate %s --yes: exit %d", tool, rc)
		for _, l := range lastLines(out, 6) {
			s.say("       | %s", l)
		}
		after, err := s.userdata()
		if err != nil {
			return err
		}
		wantAfter := map[string]string{"format": "ext4 label " + plan.DataFormatLabel, "migrate": "ext4 label " + plan.DataLabel}[tool]
		switch {
		case rerr != nil:
			return fmt.Errorf("uc-pxx-data-migrate %s: %v (userdata now %s)", tool, rerr, after)
		case rc != 0:
			return fmt.Errorf("uc-pxx-data-migrate %s: exit %d (userdata now %s)", tool, rc, after)
		case after != wantAfter:
			return fmt.Errorf("uc-pxx-data-migrate %s ended with exit 0, but userdata holds %s, not %s", tool, after, wantAfter)
		}
		return nil
	}
}

func lastLines(s string, n int) []string {
	ls := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	if len(ls) == 1 && ls[0] == "" {
		return nil
	}
	return ls
}
