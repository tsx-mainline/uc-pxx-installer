package flow

import (
	"strings"
	"testing"
	"time"

	"uc-pxx-installer/internal/plan"
	"uc-pxx-installer/internal/status"
)

// TestUnlockStatusRow (I-1, owner R7): the Status screen's row says the
// unlock status in plain words; the hex value is never on the screen; a
// change seen by the read is the hold.
func TestUnlockStatusRow(t *testing.T) {
	for _, c := range []struct {
		value   string
		changed bool
		word    string
	}{{"0x01", false, "unlocked"}, {"0x00", false, "locked"}, {"", false, "could not read"}, {"0x03", false, "could not read"}, {"0x01", true, "changed"}} {
		if w := UnlockOf(c.value, c.changed); w != c.word {
			t.Errorf("%q %v: %q", c.value, c.changed, w)
		}
	}
	m := New("t", false)
	m.SetUnit(Unit{ABValid: true, Lines: []status.Line{{Text: "Unit"}, {Text: "Bootloader                     unlocked (heuristic)"}}})
	body := func() string {
		var s []string
		for _, l := range m.View(time.Now()).Body {
			s = append(s, l.Text)
		}
		return strings.Join(s, "\n")
	}
	if b := body(); !strings.Contains(b, "Unlock status                  reading …") || strings.Contains(b, "Bootloader") {
		t.Fatalf("before the read:\n%s", b)
	}
	for _, w := range []string{UnlockReading, UnlockUnlocked, UnlockLocked, UnlockUnread} {
		m.SetUnlock(w, "")
		want := map[string]string{UnlockReading: "reading …"}[w]
		if want == "" {
			want = w
		}
		if b := body(); !strings.Contains(b, "Unlock status                  "+want) || strings.Contains(b, "0x0") {
			t.Fatalf("%s:\n%s", w, b)
		}
	}
	m.SetUnlock(UnlockChanged, "STOP: the reader saw the BCB change")
	if m.Screen != ScreenHold || m.RestartBlocked() == "" {
		t.Fatalf("a changed read: screen %s", m.Screen)
	}
}

// TestPhaseWordsClosed: every screen, with and without each child, with and
// without the drive mode, gives a word of PhaseWords (the page interface).
func TestPhaseWordsClosed(t *testing.T) {
	words := map[string]bool{}
	for _, w := range PhaseWords {
		words[w] = true
	}
	seen := map[string]bool{}
	for s := ScreenStatus; s <= ScreenUnlock; s++ {
		for _, run := range []string{"", "plan", "install", "switch", "repair"} {
			for _, drive := range []bool{false, true} {
				for _, reading := range []bool{false, true} {
					for _, swap := range []int{0, 1, 2} { // no ml state, the swap view, the stash child
						m := New("test", true)
						m.Screen, m.running, m.reading = s, run, reading
						m.usb = &usbState{drive: drive}
						switch swap {
						case 1:
							m.ml = &mlState{swap: true}
						case 2:
							m.ml = &mlState{stashing: true}
						}
						for _, msg := range []string{"", "Canceled."} {
							m.Msg = msg
							for _, unlocking := range []bool{false, true} {
								m.Prog = nil
								if unlocking { // the install child's lock-state write (R26)
									m.Prog = NewProgress(true, time.Unix(0, 0))
									m.Prog.Line("   lock-state write: lock_state 0x00 -> 0x01 on mmcblk0p16", time.Unix(1, 0))
								}
								if p := m.Phase(); !words[p] {
									t.Fatalf("screen %v run %q drive %v: phase %q is not in PhaseWords", s, run, drive, p)
								} else {
									seen[p] = true
								}
							}
						}
					}
				}
			}
		}
	}
	for _, w := range PhaseWords {
		if !seen[w] {
			t.Errorf("the word %q of PhaseWords is never given", w)
		}
	}
}

// TestUnlockStep (R26): the lock-state write of a direct plan is its own row
// of the progress view, after keep-key and R(K) and before the disable of B;
// it runs between the install child's start and done lines, and the phase
// for the page is "unlock" while it runs.
func TestUnlockStep(t *testing.T) {
	bc := func(n int, a ...string) plan.Step {
		return plan.Step{N: n, Op: "bootctl", Bootctl: &plan.BootctlStep{Args: a}}
	}
	p := &plan.Plan{Target: "b", Keeper: "a", Steps: []plan.Step{{N: 1, Op: "keep-key"}, bc(2, "repair", "--keeper", "a"),
		bc(3, "disable", "--slot", "b"), {N: 4, Op: "sync"}, bc(5, "arm-trying", "--slot", "b")}}
	labels := func(v []StepView) string {
		var l []string
		for _, s := range v {
			l = append(l, s.Op)
		}
		return strings.Join(l, " ")
	}
	if got := labels(StepsOf(p)); got != "keep-key bootctl bootctl sync bootctl" {
		t.Fatalf("no lock-state write: %s", got)
	}
	p.Checks = []plan.Check{{ID: LockWriteCheck, OK: false}}
	if got := labels(StepsOf(p)); got != "keep-key bootctl bootctl sync bootctl" {
		t.Fatalf("the check not ok: %s", got)
	}
	p.Checks = []plan.Check{{ID: LockWriteCheck, OK: true}}
	steps := StepsOf(p)
	if got := labels(steps); got != "keep-key bootctl unlock bootctl sync bootctl" || steps[2].Label != "Unlock the bootloader" || steps[2].N != 0 {
		t.Fatalf("with the lock-state write: %s %+v", got, steps[2])
	}
	t0 := time.Unix(1800000000, 0)
	pr := NewProgress(true, t0)
	pr.SetPlan(steps)
	m := New("test", true)
	m.running, m.Prog = "install", pr
	for i, l := range []string{" 1/5 keep-key: the firmware key on the unit (backup, 2 sectors)", " 2/5 bootctl repair --keeper a"} {
		pr.Line(l, t0.Add(time.Duration(i)*time.Second))
	}
	if pr.Unlocking() || m.Phase() != "install" || pr.Cur != 1 {
		t.Fatalf("before the write: %v %q cur %d", pr.Unlocking(), m.Phase(), pr.Cur)
	}
	pr.Line("   lock-state write: lock_state 0x00 -> 0x01 on /dev/mmcblk0p16 (A/B A{15,0,1} B{0,0,0}: keeper A successful; the target B not written yet)", t0.Add(3*time.Second))
	if !pr.Unlocking() || m.Phase() != "unlock" || pr.Cur != 2 || !pr.Steps[1].Done {
		t.Fatalf("in the write: %v %q cur %d", pr.Unlocking(), m.Phase(), pr.Cur)
	}
	view := strings.Join(progressText(m), "\n")
	if !strings.Contains(view, ">> Unlock the bootloader") || !strings.Contains(view, "ok Keep the firmware key") || strings.Contains(view, "0x0") {
		t.Fatalf("the screen:\n%s", view)
	}
	pr.Line("   lock-state write: done, lock_state 0x01 read back; the A/B sector unchanged", t0.Add(5*time.Second))
	if pr.Unlocking() || m.Phase() != "install" || !pr.Steps[2].Done {
		t.Fatalf("after the write: %v %q", pr.Unlocking(), m.Phase())
	}
	pr.Line(" 3/5 bootctl disable --slot b", t0.Add(6*time.Second))
	if pr.Cur != 3 || !strings.Contains(strings.Join(progressText(m), "\n"), "ok Unlock the bootloader") {
		t.Fatalf("the next step: cur %d", pr.Cur)
	}
	// a STOP in the write ends "unlock"; a plan child never has the row
	pr2 := NewProgress(true, t0)
	pr2.SetPlan(steps)
	pr2.Line("   lock-state write: lock_state 0x00 -> 0x01 on /dev/mmcblk0p16", t0)
	pr2.Line("STOP: the lock-state write: the read-back is 0x00", t0)
	if pr2.Unlocking() {
		t.Fatal("unlocking after a STOP")
	}
	pc := NewProgress(false, t0)
	pc.Line("   lock-state write: lock_state 0x00 -> 0x01 on /dev/mmcblk0p16", t0)
	if pc.Unlocking() || len(pc.Steps) != 0 {
		t.Fatal("a plan child took the line")
	}
	// a view with no plan steps gets the row from the line
	pr3 := NewProgress(true, t0)
	pr3.Line("   lock-state write: lock_state 0x00 -> 0x01 on /dev/mmcblk0p16", t0)
	if !pr3.Unlocking() || len(pr3.Steps) != 1 || pr3.Steps[0].Op != StepUnlock {
		t.Fatalf("%+v", pr3.Steps)
	}
}

// progressText is the text of the progress rows of m.
func progressText(m *Model) []string {
	var body []status.Line
	m.progressRows(&body, time.Unix(1800000010, 0))
	var out []string
	for _, l := range body {
		out = append(out, l.Text)
	}
	return out
}
