package flow

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"uc-pxx-installer/internal/plan"
)

// Progress follows the output of a plan or an install child line by line.
// It only reads the lines: what the screen shows is what the device mode
// printed, and the bar moves by real progress only (Fraction).
type Progress struct {
	Install      bool // an install child (else a plan child)
	Start        time.Time
	Computes     int  // plan computations started (an install does two)
	Checked      int  // partition images of the firmware checked (sha256) in this computation
	KeySearch    bool // the key search ran
	RepairKept   int  // the repair: partitions of the kept slot proven (sha256)
	RepairWrites int  // the repair or switch: the A/B writes started
	Steps        []StepView
	Cur          int // index into Steps of the running step, -1 = none yet
	stepStart    time.Time
	BytesTotal   int64
	bytesDone    int64
	writeSecs    float64 // seconds of the finished write steps
	Result       string  // the RESULT line, if seen
	Stop         string  // the last STOP line, if seen
	Agree        bool    // the check step said that the decoder and bootctl agree
	disabled     bool
	armed        bool
	inBootctl    bool
	Lines        []string // the last lines, for the log view
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

// stepRx is a step line of the install child, as it prints it (the
// numbers are never assumed: a keep-key step may come before the disable).
var stepRx = regexp.MustCompile(`^\s*(\d+)/(\d+) (keep-key|bootctl|write|skip|sync|verify|check)\b(.*)$`)
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
	// One plan computation: the device mode prints "pass 1: check ..."
	// (not indented) at its start, then one indented "  pass 1: <part>"
	// row per partition, which is not a new computation. The second
	// computation of an install is quiet: it starts with "computing the
	// plan again".
	case strings.HasPrefix(l, "pass 1:"), strings.HasPrefix(l, "computing the plan again"):
		p.Computes++
		p.Checked = 0
	case strings.HasPrefix(t, "pass 1: ") && strings.HasSuffix(t, " matches"):
		p.Checked++ // one partition image of the firmware: its sha256 matches
	case strings.HasPrefix(t, "key search:"):
		p.KeySearch = true
	case strings.HasPrefix(t, "repair: keeper ") && strings.Contains(t, " ok"):
		p.RepairKept++ // the repair proves the kept slot, one partition at a time
	case strings.HasPrefix(t, "REPAIR-WRITE:"), strings.HasPrefix(t, "SWITCH-WRITE:"):
		p.RepairWrites++
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

// Fraction is the progress from 0 to 1, by real progress only (no time
// estimate, owner 2026-10-31). A plan child: the partition images whose
// sha256 was checked, of the 8 of a slot. An install: the steps that are
// done, of all the steps of the plan (each step counts the same, as the
// list on the screen shows them); nothing during the check(s) before the
// first step.
func (p *Progress) Fraction(now time.Time) float64 {
	if p.Result != "" && p.Stop == "" {
		return 1
	}
	if !p.Install {
		return float64(min(p.Checked, len(plan.SlotParts))) / float64(len(plan.SlotParts))
	}
	if len(p.Steps) == 0 {
		return 0
	}
	done := 0
	for _, s := range p.Steps {
		if s.Done {
			done++
		}
	}
	return float64(done) / float64(len(p.Steps))
}

// StepEstimate is the time a write step should take at the speed of the
// earlier writes of this install (0 when there is none yet).
func (p *Progress) StepEstimate(s StepView) time.Duration {
	mb := p.MBps()
	if s.Op != "write" || mb <= 0 {
		return 0
	}
	return time.Duration(float64(s.Bytes) / (mb * 1e6) * float64(time.Second))
}

// Pulse is an indeterminate progress indicator w cells wide: a short block
// that moves back and forth with the elapsed time. It is for a step that
// reports no sub-progress and can take minutes (the unit check before the
// plan, the firmware read-back): it shows no percentage, so it never reads
// as a frozen full bar (R4 finding 3). The position is a function of the
// elapsed time only, so a render is deterministic.
func Pulse(elapsed time.Duration, w int) string {
	if w < 1 {
		w = 1
	}
	inner := w // the fill area inside the brackets, as wide as Bar's
	block := inner / 5
	if block < 1 {
		block = 1
	}
	span := inner - block
	if span < 1 {
		span = 1
	}
	if elapsed < 0 {
		elapsed = 0
	}
	// one cell per 200 ms, bouncing at each end
	step := int(elapsed / (200 * time.Millisecond))
	pos := step % (2 * span)
	if pos > span {
		pos = 2*span - pos
	}
	row := []byte(strings.Repeat(".", inner))
	for i := 0; i < block; i++ {
		row[pos+i] = '#'
	}
	return "[" + string(row) + "]"
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
