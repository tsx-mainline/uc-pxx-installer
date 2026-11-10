package step2ui_test

import (
	"errors"
	"testing"
	"time"

	"uc-pxx-installer/internal/step2ui"
)

var t0 = time.Unix(1700000000, 0)

func at(s float64) time.Time { return t0.Add(time.Duration(s * float64(time.Second))) }

func TestConfirmHold(t *testing.T) {
	m := step2ui.New(t0, false, "")
	m.Handle(step2ui.KeyOKDown, at(1))
	m.Handle(step2ui.KeyNone, at(3.9))
	if m.Screen != step2ui.ScreenAsk {
		t.Fatal("started before 3 s")
	}
	m.Handle(step2ui.KeyOKUp, at(3.95)) // released early: no start
	m.Handle(step2ui.KeyNone, at(10))
	if m.Screen != step2ui.ScreenAsk {
		t.Fatal("a released hold started the run")
	}
	m.Handle(step2ui.KeyOKDown, at(11))
	m.Handle(step2ui.KeyNone, at(14))
	if m.Screen != step2ui.ScreenStart {
		t.Fatalf("no start after the 3 s hold: %s", m.Screen)
	}
	m.Handle(step2ui.KeyBack, at(15))
	if m.Screen != step2ui.ScreenStart {
		t.Fatal("a final screen changed")
	}
}

func TestBackAndNever(t *testing.T) {
	m := step2ui.New(t0, false, "")
	m.Handle(step2ui.KeyBack, at(1))
	if m.Screen != step2ui.ScreenCancelled {
		t.Fatal("Back is not 'not now'")
	}
	m = step2ui.New(t0, false, "")
	m.Handle(step2ui.KeyVolDown, at(1))
	if m.Screen != step2ui.ScreenNever {
		t.Fatal("Vol- does not show the never choice")
	}
	m.Handle(step2ui.KeyBack, at(2))
	if m.Screen != step2ui.ScreenAsk {
		t.Fatal("Back on the never choice does not return")
	}
	m.Handle(step2ui.KeyVolUp, at(3))
	m.Handle(step2ui.KeyOKDown, at(4))
	m.Handle(step2ui.KeyNone, at(7))
	if m.Screen != step2ui.ScreenNeverSet {
		t.Fatalf("never not set: %s", m.Screen)
	}
}

func TestCountdown(t *testing.T) {
	m := step2ui.New(t0, true, "")
	if m.Screen != step2ui.ScreenCountdown || m.Left(at(10)) != 20*time.Second {
		t.Fatalf("%s %v", m.Screen, m.Left(at(10)))
	}
	m.Handle(step2ui.KeyNone, at(29.9))
	if m.Screen != step2ui.ScreenCountdown {
		t.Fatal("started early")
	}
	m.Handle(step2ui.KeyNone, at(30))
	if m.Screen != step2ui.ScreenStart {
		t.Fatal("no start at the end of the countdown")
	}
	m = step2ui.New(t0, true, "")
	m.Handle(step2ui.KeyBack, at(5))
	if m.Screen != step2ui.ScreenCancelled {
		t.Fatal("Back does not cancel the countdown")
	}
	m = step2ui.New(t0, true, "")
	m.Handle(step2ui.KeyVolUp, at(5))
	m.Handle(step2ui.KeyNone, at(40))
	if m.Screen != step2ui.ScreenAsk {
		t.Fatal("a key does not stop the countdown")
	}
}

func TestAutoAllowed(t *testing.T) {
	as := step2ui.ParseAttempts([]byte(step2ui.AttemptLine("size=1,ino=2")), nil)
	for _, c := range []struct {
		name       string
		req        bool
		drive      string
		attempts   []step2ui.Attempt
		supervised bool
		ok         bool
	}{
		{"asked, new drive", true, "size=9,ino=9", as, false, true},
		{"not asked", false, "size=9,ino=9", nil, false, false},
		{"the same drive again", true, "size=1,ino=2", as, false, false},
		{"a failed target before", true, "size=9,ino=9", step2ui.ParseAttempts([]byte("failed size=1,ino=2\n"), nil), false, false},
		{"an unreadable record line", true, "size=9,ino=9", step2ui.ParseAttempts([]byte("garbage\n"), nil), false, false},
		{"supervised", true, "size=9,ino=9", nil, true, false},
		{"a torn attempt line", true, "size=9,ino=9", step2ui.ParseAttempts([]byte("drive size=9,i"), nil), false, false},
		{"an empty drive line", true, "size=9,ino=9", step2ui.ParseAttempts([]byte("drive \n"), nil), false, false},
		{"a read error", true, "size=9,ino=9", step2ui.ParseAttempts(nil, errTest), false, false},
		{"no record yet", true, "size=9,ino=9", step2ui.ParseAttempts(nil, nil), false, true},
		{"no drive identity", true, "", nil, false, false},
	} {
		if ok, why := step2ui.AutoAllowed(c.req, c.drive, c.attempts, c.supervised); ok != c.ok || (!ok && why == "") {
			t.Fatalf("%s: %v %q", c.name, ok, why)
		}
	}
}

var errTest = errors.New("read error")
