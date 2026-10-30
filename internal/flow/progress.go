package flow

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Progress follows the output of a plan or an install child line by line.
// It only reads the lines: what the screen shows is what the device mode
// printed, and the estimate is from the measured times of §4.5.
type Progress struct {
	Install    bool // an install child (else a plan child)
	Start      time.Time
	Computes   int // plan computations started (an install does two)
	Steps      []StepView
	Cur        int // index into Steps of the running step, -1 = none yet
	stepStart  time.Time
	BytesTotal int64
	bytesDone  int64
	writeSecs  float64 // seconds of the finished write steps
	Result     string  // the RESULT line, if seen
	Stop       string  // the last STOP line, if seen
	Agree      bool    // the check step said that the decoder and bootctl agree
	disabled   bool
	armed      bool
	inBootctl  bool
	Lines      []string // the last lines, for the log view
}

// StepView is one step as the child printed it.
type StepView struct {
	N     int
	Op    string
	Label string
	Bytes int64
	Done  bool
	Secs  float64
}

var stepRx = regexp.MustCompile(`^\s*(\d+)/(\d+) (bootctl|write|skip|sync|verify|check)\b(.*)$`)
var bytesRx = regexp.MustCompile(`\((\d+) bytes\)`)

// NewProgress starts following a child.
func NewProgress(install bool, now time.Time) *Progress {
	return &Progress{Install: install, Start: now, Cur: -1}
}

// SetPlan gives the steps of the plan (the install child prints the same
// steps), so the bar knows the bytes before the first step.
func (p *Progress) SetPlan(steps []StepView) {
	p.Steps = append([]StepView(nil), steps...)
	p.BytesTotal = 0
	for _, s := range p.Steps {
		p.BytesTotal += s.Bytes
	}
}

// Line takes one output line of the child.
func (p *Progress) Line(l string, now time.Time) {
	l = strings.TrimRight(l, "\r\n")
	p.Lines = append(p.Lines, l)
	if len(p.Lines) > 400 {
		p.Lines = p.Lines[len(p.Lines)-400:]
	}
	t := strings.TrimSpace(l)
	switch {
	case strings.HasPrefix(t, "pass 1:"):
		p.Computes++
	case strings.HasPrefix(t, "RESULT:"):
		p.Result = strings.TrimSpace(strings.TrimPrefix(t, "RESULT:"))
		p.finishCur(now)
		p.inBootctl = false
	case strings.HasPrefix(t, "STOP"):
		p.Stop = t
		p.inBootctl = false
	case strings.Contains(t, "(decoder and bootctl agree)"):
		p.Agree = true
	}
	m := stepRx.FindStringSubmatch(l)
	if m == nil {
		return
	}
	n, _ := strconv.Atoi(m[1])
	p.finishCur(now)
	idx := -1
	for i := range p.Steps {
		if p.Steps[i].N == n {
			idx = i
		}
	}
	if idx < 0 {
		sv := StepView{N: n, Op: m[3], Label: strings.TrimSpace(m[3] + m[4])}
		if b := bytesRx.FindStringSubmatch(m[4]); b != nil {
			sv.Bytes, _ = strconv.ParseInt(b[1], 10, 64)
			p.BytesTotal += sv.Bytes
		}
		p.Steps = append(p.Steps, sv)
		idx = len(p.Steps) - 1
	}
	p.Cur, p.stepStart = idx, now
	op, rest := m[3], m[4]
	p.inBootctl = op == "bootctl"
	if op == "bootctl" && strings.Contains(rest, "disable") {
		p.disabled = true
	}
	if op == "bootctl" && strings.Contains(rest, "arm-trying") {
		p.armed = true // from the start of the arm step; InBootctl wins while it runs
	}
}

func (p *Progress) finishCur(now time.Time) {
	if p.Cur < 0 || p.Steps[p.Cur].Done {
		return
	}
	s := &p.Steps[p.Cur]
	s.Done = true
	s.Secs = now.Sub(p.stepStart).Seconds()
	if s.Op == "write" {
		p.bytesDone += s.Bytes
		p.writeSecs += s.Secs
	}
	if s.Op == "bootctl" {
		p.inBootctl = false
	}
}

// InBootctl: a bootctl step is running (the A/B sector may be written now).
func (p *Progress) InBootctl() bool { return p.inBootctl }

// Disabled: the disable step has started (the target may be disabled).
func (p *Progress) Disabled() bool { return p.disabled }

// Armed: the arm step has run (and is not running any more).
func (p *Progress) Armed() bool { return p.armed && !p.inBootctl }

// MBps is the write speed of the finished write steps.
func (p *Progress) MBps() float64 {
	if p.writeSecs <= 0 {
		return 0
	}
	return float64(p.bytesDone) / 1e6 / p.writeSecs
}

// Estimate is the expected total time of the child, from the measured times.
func (p *Progress) Estimate() time.Duration {
	if !p.Install {
		return measuredPlanSecs * time.Second
	}
	return (2*measuredPlanSecs + measuredStepsSecs) * time.Second
}

// Fraction is the progress from 0 to 1. Before the steps it follows the
// time of the plan computations; in the steps, the bytes written, and the
// time inside the running step at the speed measured so far.
func (p *Progress) Fraction(now time.Time) float64 {
	el := now.Sub(p.Start).Seconds()
	total := p.Estimate().Seconds()
	plan := float64(measuredPlanSecs)
	if p.Install {
		plan = 2 * measuredPlanSecs
	}
	if p.Cur < 0 {
		f := el / total
		if max := plan / total; f > max*0.98 {
			f = max * 0.98
		}
		return f
	}
	if !p.Install {
		return 1
	}
	done := p.bytesDone
	if p.Cur >= 0 && !p.Steps[p.Cur].Done && p.Steps[p.Cur].Op == "write" {
		rate := float64(p.BytesTotal) / measuredStepsSecs
		if mb := p.MBps(); mb > 0 {
			rate = mb * 1e6
		}
		in := int64(now.Sub(p.stepStart).Seconds() * rate)
		if in > p.Steps[p.Cur].Bytes {
			in = p.Steps[p.Cur].Bytes
		}
		done += in
	}
	var f float64
	if p.BytesTotal > 0 {
		f = float64(done) / float64(p.BytesTotal)
	}
	// the steps after the writes (sync, verify, arm, check) take their share
	stepsShare := float64(measuredStepsSecs) / total
	writeShare := 0.8
	if p.Result != "" {
		return 1
	}
	after := 0.0
	if p.Cur >= 0 {
		switch p.Steps[p.Cur].Op {
		case "sync":
			after = 0.05
		case "verify":
			after = 0.1
		case "bootctl":
			if p.armed {
				after = 0.18
			}
		case "check":
			after = 0.19
		}
	}
	return plan/total + stepsShare*(writeShare*f+after)
}

// Bar is a text bar of w characters.
func Bar(f float64, w int) string {
	if f < 0 {
		f = 0
	}
	if f > 1 {
		f = 1
	}
	n := int(f*float64(w) + 0.5)
	return "[" + strings.Repeat("#", n) + strings.Repeat(".", w-n) + "]" + fmt.Sprintf(" %3d %%", int(f*100+0.5))
}
