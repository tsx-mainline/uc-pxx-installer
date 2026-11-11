package handover

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The places of the permanent USB interface of a running mainline slot
// (mainline-rc-4; owner decisions 2026-11-11). The root step-2 service
// (internal/step2) and the uc-pxx-usb service (internal/xfer RunUSB) share
// them here.
const (
	// StepTwoBinary exists only in a STEP-2 build (its step-2 set): there the
	// step-2 service owns the receive of the payload; the uc-pxx-usb service
	// gives only the status.
	StepTwoBinary = "/usr/libexec/uc-pxx-step2/uc-pxx-step2"
	// StepTwoDone is the done record of the finish on this slot's root
	// (internal/step2 StateDir + DoneFile; a test there checks they agree).
	StepTwoDone = "/var/lib/uc-pxx-step2/done"
	// USBRunDir holds the uc-pxx-usb service's files (root, 0755).
	USBRunDir = "/run/uc-pxx-usb"
	// USBInfo is the info file of HELLO/STATUS in a normal build (root, 0644):
	// the uc-pxx-usb service writes it, then the finish executor it started.
	USBInfo = "/run/uc-pxx-usb/info.json"
	// FinishStage is the tmpfs of the finish receive (root, 0711, 64 MiB).
	FinishStage = "/run/uc-pxx-usb/stage"
	// FinishStarted: the finish executor was started in this boot (never a
	// second time in the same boot).
	FinishStarted = "/run/uc-pxx-usb/finish-started"
	// FinishLog is the finish executor's log on this slot's root.
	FinishLog = "/var/log/uc-pxx-finish.log"
	// FinishStageSize is the size of FinishStage.
	FinishStageSize = 64 << 20
)

// ProcessesOf lists the pids whose real, effective, saved or file-system uid
// is uid (procfs at proc: "/proc"; tests another folder).
func ProcessesOf(proc string, uid int) ([]int, error) {
	ents, err := os.ReadDir(proc)
	if err != nil {
		return nil, err
	}
	var out []int
	want := strconv.Itoa(uid)
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		f, err := os.Open(filepath.Join(proc, e.Name(), "status"))
		if err != nil {
			continue // gone
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if v, ok := strings.CutPrefix(sc.Text(), "Uid:"); ok {
				for _, u := range strings.Fields(v) {
					if u == want {
						out = append(out, pid)
						break
					}
				}
				break
			}
		}
		f.Close()
	}
	return out, nil
}

// NoProcessOf makes sure that no process of uid is left before a seal (review
// F7): the receiver was stopped by its supervisor; any process of its user
// that is still there is killed (SIGKILL) and must be gone within 3 s.
func NoProcessOf(uid int) error {
	if uid <= 0 {
		return fmt.Errorf("uid %d", uid)
	}
	for i := 0; i < 30; i++ {
		l, err := ProcessesOf("/proc", uid)
		if err != nil {
			return err
		}
		if len(l) == 0 {
			return nil
		}
		for _, pid := range l {
			syscall.Kill(pid, syscall.SIGKILL)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("processes of uid %d are still there after SIGKILL: no seal", uid)
}

// Info is what a root service tells the page through the receiver (the info
// file, root-written, 0644; internal/xfer ReadInfo filters every field to a
// strict form). It never holds a unit serial, a MAC, a key or a factory byte.
type Info struct {
	Model  string   `json:"model,omitempty"`
	Slot   string   `json:"slot,omitempty"`
	Token  string   `json:"token,omitempty"`
	Step2  string   `json:"step2,omitempty"`
	PlanID string   `json:"plan_id,omitempty"`
	Lines  []string `json:"lines,omitempty"`
}

// BaseInfo is the info of the uc-pxx-usb service (model, slot and token from
// /proc; never a serial, a MAC or a factory byte).
func BaseInfo(root, step2 string, lines []string) Info {
	in := Info{Step2: step2, Lines: lines}
	if b, err := os.ReadFile(filepath.Join(root, "/proc/device-tree/compatible")); err == nil {
		c := string(b)
		switch {
		case strings.Contains(c, "crestron,uc-p8"):
			in.Model = "UC-P8"
		case strings.Contains(c, "crestron,uc-p10"):
			in.Model = "UC-P10"
		}
	}
	if b, err := os.ReadFile(filepath.Join(root, "/proc/device-tree/chosen/u-boot,bootargs")); err == nil {
		if m := slotWord.FindStringSubmatch(string(b)); m != nil {
			in.Slot = m[1]
		}
	}
	if b, err := os.ReadFile(filepath.Join(root, "/proc/cmdline")); err == nil {
		if m := tokenWord.FindStringSubmatch(string(b)); m != nil {
			in.Token = m[1]
		}
	}
	return in
}

var (
	slotWord  = regexp.MustCompile(`(?:^|\s)androidboot\.slot_suffix=_([ab])(?:\s|\x00|$)`)
	tokenWord = regexp.MustCompile(`(?:^|\s)uc-pxx\.id=([A-Za-z0-9._-]{1,15})(?:\s|$)`)
)

// WriteInfo writes the info file (root, 0644 in a 0755 folder: the receiver's
// user reads it), atomically.
func WriteInfo(path string, in Info) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
