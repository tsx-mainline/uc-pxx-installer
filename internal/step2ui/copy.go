package step2ui

import (
	"fmt"
	"strings"
	"time"
)

// The progress of the clone's frozen block copy (R32): the unit's screen and
// the step code of the info file, from the bytes copied so far. The time
// left is computed from the speed so far (no estimate in text).

// MiB is the unit of the copy's numbers on the screen and on the link.
const MiB = 1 << 20

// CopyCode is the info file's step code while the copy runs:
// "started <id12> copy <D> of <T>" (whole MiB; the page shows a bar). It
// keeps the "started" word of the run, so a reader of the older codes sees
// the run.
func CopyCode(id12 string, done, total int64) string {
	return fmt.Sprintf("started %s copy %d of %d", id12, done/MiB, (total+MiB-1)/MiB)
}

// CopyLines is the unit's screen while the copy runs. elapsed is the time
// since the copy started; the time left is shown once 5 % or 10 s of the
// copy are done.
func CopyLines(target string, done, total int64, elapsed time.Duration) []string {
	f := 0.0
	if total > 0 {
		f = float64(done) / float64(total)
	}
	bar := Bar(f, 40)
	if left, ok := TimeLeft(done, total, elapsed); ok {
		bar += "  " + left + " left"
	}
	return []string{
		fmt.Sprintf("Copying this system into slot %s: %d of %d MiB", strings.ToUpper(target), done/MiB, (total+MiB-1)/MiB),
		bar,
		"",
		"Do not disconnect the power. The unit restarts by itself when step 2 is done.",
	}
}

// Bar is a text bar of w cells with the percent.
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

// TimeLeft is the time left at the speed so far ("1 min 05 s", "40 s"):
// only after 5 % or 10 s of the work (the speed is not known before).
func TimeLeft(done, total int64, elapsed time.Duration) (string, bool) {
	if done <= 0 || total <= 0 || done >= total || elapsed <= 0 || (done*20 < total && elapsed < 10*time.Second) {
		return "", false
	}
	left := time.Duration(float64(elapsed) * float64(total-done) / float64(done))
	s := int(left.Seconds() + 0.5)
	if s >= 60 {
		return fmt.Sprintf("%d min %02d s", s/60, s%60), true
	}
	return fmt.Sprintf("%d s", s), true
}
