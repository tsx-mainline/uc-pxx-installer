package flowio

// The unlock status at the start (mainline-rc-10, I-1; owner R7): the runner
// runs `uc-pxx-inst lockstate --device /dev/mmcblk0` once, right after the
// start reads: the in-RAM lock_state reader's one read of this boot
// (devmode.ReadLockStateAtStart: reads only, no TEE write, nothing written to
// the eMMC; its record answers every later read of the boot, so the direct
// plan reuses it). The plain word goes to the flow (the Status row, the
// page's info), the value and the status's heuristic to the flow log. A plan
// asked for while the read runs starts when it ends, and so does every other
// child that takes the A/B lock (the install, the repair and the switch,
// their dry runs too): the reader holds the A/B lock for its reads, and a
// child that finds it held refuses.

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"uc-pxx-installer/internal/flow"
)

// lockReadRun is the read of this UI.
type lockReadRun struct {
	ended   bool
	pending []flow.Action // the actions that wait for the end (lockReadWait)
}

// lockReadWait keeps an action whose child takes the A/B lock until the read
// of the start ended (it reports true: the action is kept, Do skips it).
func (r *Runner) lockReadWait(a flow.Action) bool {
	if r.lockRead == nil || r.lockRead.ended {
		return false
	}
	switch a.Kind {
	case flow.ActStartPlan, flow.ActStartInstall, flow.ActRepairCheck, flow.ActRepair, flow.ActSwitchCheck, flow.ActSwitch:
	default:
		return false
	}
	r.lockRead.pending = append(r.lockRead.pending, a)
	r.say("%s waits for the lock-state read of this start (it holds the A/B lock)", a.Kind)
	return true
}

// LockReadTimeout bounds the read child (the TEE read takes a few seconds).
const LockReadTimeout = 3 * time.Minute

func (r *Runner) startLockRead() {
	if r.lockRead != nil {
		return
	}
	r.lockRead = &lockReadRun{}
	r.M.SetUnlock(flow.UnlockReading, "")
	argv := append(append([]string{"lockstate", "--device", "/dev/mmcblk0"}, r.c.ChildArgs...), lockTestArgs(r.c.InstallChildArgs)...)
	r.say("unlock status: the in-RAM lock_state read of this boot: %s %s", r.c.Exe, strings.Join(argv, " "))
	go func() {
		out, code, err := readLockState(r.c.Exe, argv, r.c.ChildEnv)
		value, changed, why := "", false, ""
		for _, l := range strings.Split(out, "\n") {
			switch {
			case strings.HasPrefix(l, "LOCKSTATE: "):
				value = strings.TrimSpace(strings.TrimPrefix(l, "LOCKSTATE: "))
			case strings.HasPrefix(l, "LOCKSTATE-CHANGED: yes"):
				changed = true
			case strings.HasPrefix(l, "RESULT: "):
				why = strings.TrimPrefix(l, "RESULT: ")
			}
		}
		if code == 3 {
			changed = true
		}
		r.post(func() {
			word := flow.UnlockOf(value, changed)
			heur := ""
			if r.report != nil {
				heur = r.report.Unlock
			}
			r.say("unlock status: %s (lock_state %s, exit %d %v; %s); the status's own reading: %q", word, value, code, err, why, heur)
			r.lockRead.ended = true
			r.M.SetUnlock(word, why)
			p := r.lockRead.pending
			r.lockRead.pending = nil
			if len(p) > 0 && word == flow.UnlockChanged {
				// the hold of a changed read stays on the screen: nothing
				// that waited starts (review I-6 MINOR-1)
				r.say("unlock status changed: the %d waiting actions do not start", len(p))
				p = nil
			}
			if len(p) > 0 {
				r.Do(p)
			} else {
				r.Do(r.M.Take())
			}
		})
	}()
}

// readLockState runs the read child (bounded) and gives its output and exit.
func readLockState(exe string, argv, env []string) (string, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), LockReadTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, argv...)
	if len(env) > 0 {
		cmd.Env = env
	}
	b, err := cmd.CombinedOutput()
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	return string(b), code, err
}

// lockTestArgs: the reader's stub of a devtest build (--test-lock-state V in
// the plan children's test arguments) for the read at the start too, so
// that the plan of the same boot reuses a read of the same stub. Empty in a
// release build (InstallChildArgs is empty there).
func lockTestArgs(a []string) []string {
	var out []string
	for i := 0; i+1 < len(a); i++ {
		if a[i] == "--test-lock-state" {
			out = append(out, a[i], a[i+1])
		}
	}
	return out
}

// unlockWord is the unlock status for the page.
func (r *Runner) unlockWord() string {
	w := r.M.Unlock()
	if w == "" {
		return ""
	}
	return w
}
