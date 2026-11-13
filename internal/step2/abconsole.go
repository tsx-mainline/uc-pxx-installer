//go:build install && step2

package step2

// The A/B read-backs of a step-2 run on the console (mainline-rc-7; owner
// 2026-11-13: the end-user STEP-2 path records the before and after A/B
// read-backs so that the session can check them). install.RunOtherSlot writes
// one JSON line per step to the run log (/var/lib/uc-pxx-step2/run-ID.jsonl)
// with ab_before and ab_after: the first 32 bytes of the A/B sector, read
// back from the eMMC before and after the step. abConsole is a second reader
// of those lines: for the start, every bootctl step (the disable and the arm),
// the check-ab step and a guard stop it writes one plain line to the kernel
// log (/dev/kmsg, so the serial console shows it too) and to the run's
// output. It only reads the lines and writes text: a failed kernel-log write
// is not an error of the run (the run log keeps every line).

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"syscall"

	"uc-pxx-installer/internal/plan"
)

// Kmsg is the kernel log device.
const Kmsg = "/dev/kmsg"

type abConsole struct {
	s     *session
	p     *plan.OtherSlotPlan
	steps map[int]plan.Step
}

func (s *session) abConsole(p *plan.OtherSlotPlan) *abConsole {
	c := &abConsole{s: s, p: p, steps: map[int]plan.Step{}}
	for _, st := range p.Steps {
		c.steps[st.N] = st
	}
	return c
}

// Write takes the run log's lines (one JSON object per Write) and never fails.
func (c *abConsole) Write(b []byte) (int, error) {
	var ev struct {
		Event    string `json:"event"`
		Step     int    `json:"step"`
		Op       string `json:"op"`
		AB       string `json:"ab"`
		ABBefore string `json:"ab_before"`
		ABAfter  string `json:"ab_after"`
		Guard    string `json:"guard"`
		Error    string `json:"error"`
	}
	if json.Unmarshal(b, &ev) != nil {
		return len(b), nil
	}
	id := c.p.ID
	if len(id) > 12 {
		id = id[:12]
	}
	switch {
	case ev.Event == "start":
		c.line(fmt.Sprintf("plan %s start: keeper %s, target %s; A/B first 32 read back %s", id, c.p.Keeper, c.p.Target, ev.AB))
	case ev.Guard != "":
		c.line(fmt.Sprintf("plan %s step %d (%s) stopped before the step: %s; A/B first 32 read back %s", id, ev.Step, ev.Op, ev.Guard, ev.ABBefore))
	case (ev.Op == "bootctl" || ev.Op == "check-ab") && (ev.ABBefore != "" || ev.ABAfter != ""):
		what, plannedAfter := ev.Op, ""
		if st, ok := c.steps[ev.Step]; ok && st.Bootctl != nil {
			what = "uc-pxx-bootctl-inst " + strings.Join(st.Bootctl.Args, " ")
			plannedAfter = st.Bootctl.After.Hex32
		}
		end := "ok"
		switch {
		case ev.Error != "":
			end = "ERROR " + ev.Error
		case plannedAfter != "" && ev.ABAfter == plannedAfter:
			end = "ok, the plan's state after the step"
		case plannedAfter != "":
			end = "the plan's state after the step is " + plannedAfter
		}
		c.line(fmt.Sprintf("plan %s step %d %s: A/B first 32 read back before %s, after %s: %s", id, ev.Step, what, ev.ABBefore, ev.ABAfter, end))
	}
	return len(b), nil
}

// line writes one line to the output and to the kernel log (level 5,
// notice: the console prints it).
func (c *abConsole) line(l string) {
	l = strings.ReplaceAll(l, "\n", " ")
	c.s.say("A/B read-back: %s", l)
	f, err := os.OpenFile(c.s.e.Path(Kmsg), os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return
	}
	defer f.Close()
	// the real kernel log is a character device; a test root may give a
	// regular file (a devtest build only)
	if fi, err := f.Stat(); err != nil || (fi.Mode()&os.ModeCharDevice == 0 && !(testBuild && fi.Mode().IsRegular())) {
		return
	}
	if len(l) > 900 {
		l = l[:900]
	}
	f.Write([]byte("<5>uc-pxx-step2: " + l + "\n"))
}
