package step2ui

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestCopyCode: the code the page reads (R32): the run's "started" word, the
// plan id, whole MiB; within the info file's code form.
func TestCopyCode(t *testing.T) {
	form := regexp.MustCompile(`^[a-z0-9][a-z0-9 :._-]{0,63}$`) // internal/step2 infoCode, the receiver's infoStep2
	for _, c := range []struct {
		done, total int64
		want        string
	}{
		{0, 1792 * MiB, "started 286b4c515994 copy 0 of 1792"},
		{512*MiB + 5, 1792 * MiB, "started 286b4c515994 copy 512 of 1792"},
		{1792 * MiB, 1792 * MiB, "started 286b4c515994 copy 1792 of 1792"},
		{10, 1792*MiB + 1, "started 286b4c515994 copy 0 of 1793"},
	} {
		got := CopyCode("286b4c515994", c.done, c.total)
		if got != c.want || !form.MatchString(got) || !strings.HasPrefix(got, "started ") {
			t.Errorf("%d/%d: %q, want %q", c.done, c.total, got, c.want)
		}
	}
}

func TestCopyLines(t *testing.T) {
	l := CopyLines("a", 448*MiB, 1792*MiB, 30*time.Second)
	if l[0] != "Copying this system into slot A: 448 of 1792 MiB" {
		t.Fatalf("%q", l[0])
	}
	if !strings.HasPrefix(l[1], "[##########..............................]  25 %") || !strings.HasSuffix(l[1], "1 min 30 s left") {
		t.Fatalf("%q", l[1])
	}
	// no time left before the speed is known
	if l := CopyLines("a", MiB, 1792*MiB, time.Second); strings.Contains(l[1], "left") {
		t.Fatalf("%q", l[1])
	}
	for _, c := range []struct {
		done, total int64
		el          time.Duration
		want        string
		ok          bool
	}{
		{50, 100, 40 * time.Second, "40 s", true},
		{25, 100, 30 * time.Second, "1 min 30 s", true},
		{4, 100, 9 * time.Second, "", false},
		{4, 100, 10 * time.Second, "4 min 00 s", true},
		{100, 100, time.Second, "", false},
		{0, 100, time.Minute, "", false},
	} {
		got, ok := TimeLeft(c.done, c.total, c.el)
		if got != c.want || ok != c.ok {
			t.Errorf("TimeLeft(%d, %d, %v) = %q %v", c.done, c.total, c.el, got, ok)
		}
	}
	if Bar(-1, 4) != "[....]   0 %" || Bar(2, 4) != "[####] 100 %" {
		t.Fatalf("%q %q", Bar(-1, 4), Bar(2, 4))
	}
}
