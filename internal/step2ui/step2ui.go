// Package step2ui is the confirm model of mainline step 2 on the panel
// (MAINLINE-STEP2-DESIGN.md §8, Q-S7; MAINLINE-INT-2026-11-10.md task 4). It is
// pure: no I/O, no time source of its own; the caller feeds keys and ticks.
//
//   - Default: the confirm screen. Hold OK (the Mic key) HoldOK to start;
//     Back (the Speaker key) is "Not now"; "Do not ask again" (Vol- then the
//     hold of OK on that choice) writes step2.never through the caller.
//   - Automatic (the file AUTO-STEP2 in the uc-pxx-mainline folder of the
//     install drive, carried in the stash): the same screen with an
//     AutoCountdown countdown that starts the run at its end; Back cancels.
//     At most one automatic attempt per install drive (the drive identity is
//     recorded before the start), and never again after a step-2 target that
//     failed (B {0,0,0} after a step-2 arm: "ask the user"). Supervised runs
//     never use it (each A/B write needs its rule-9 GO).
//
// Every precondition of the plan still holds after the start: the model only
// decides when the user (or the countdown) asked for the run.
package step2ui

import (
	"fmt"
	"strings"
	"time"
)

const (
	// HoldOK is the hold of OK that starts a confirmed run.
	HoldOK = 3 * time.Second
	// AutoCountdown is the countdown of an automatic run.
	AutoCountdown = 30 * time.Second
	// AutoFile is the drive file that asks for the automatic run.
	AutoFile = "AUTO-STEP2"
)

// Screen is what the panel shows.
type Screen string

const (
	ScreenAsk       Screen = "ask"       // the confirm screen (hold OK)
	ScreenCountdown Screen = "countdown" // the automatic countdown
	ScreenNever     Screen = "never"     // the "do not ask again" choice (hold OK to confirm)
	ScreenStart     Screen = "start"     // the run starts (the caller runs the plan)
	ScreenCancelled Screen = "cancelled" // not now: the kiosk comes back; the question returns at the next boot
	ScreenNeverSet  Screen = "never-set" // the caller writes step2.never; only `uc-pxx-step2 start` over ssh then
)

// Key is a panel key.
type Key int

const (
	KeyNone Key = iota
	KeyOKDown
	KeyOKUp
	KeyBack
	KeyVolDown
	KeyVolUp
)

// Model is the confirm model.
type Model struct {
	Screen Screen
	Auto   bool   // the automatic countdown runs (or ran out: ScreenStart by the countdown); false once a key stopped it
	AutoNo string // why an AUTO-STEP2 request did not give the countdown
	held   time.Time
	ends   time.Time
}

// Attempt is one recorded automatic attempt: the drive identity, and whether
// its target failed afterwards.
type Attempt struct {
	Drive  string
	Failed bool
}

// ParseAttempts reads the attempt record: one LF-terminated line per
// attempt, "drive <ident>" or "failed <ident>". A read error, an unknown or
// empty-ident line, or a last line without its LF (a torn append) counts as
// a failure (fail closed: such a record never allows the countdown; review
// T4 MINOR-3). A missing record is (nil, os.ErrNotExist from the caller):
// pass readErr nil and b empty.
func ParseAttempts(b []byte, readErr error) []Attempt {
	if readErr != nil {
		return []Attempt{{Drive: "?", Failed: true}}
	}
	var out []Attempt
	if len(b) > 0 && b[len(b)-1] != '\n' {
		out = append(out, Attempt{Drive: "?", Failed: true})
	}
	for _, l := range strings.Split(string(b), "\n") {
		if l == "" {
			continue
		}
		k, v, _ := strings.Cut(l, " ")
		switch {
		case k == "drive" && v != "" && !strings.ContainsAny(v, " \t\r\x00"):
			out = append(out, Attempt{Drive: v})
		case k == "failed":
			out = append(out, Attempt{Drive: v, Failed: true})
		default:
			out = append(out, Attempt{Drive: "?", Failed: true})
		}
	}
	return out
}

// SupervisedMarker is the file whose presence makes every step-2 run of the
// unit supervised: no countdown, no automatic finish (each A/B write has its
// rule-9 GO). A person creates it by hand on a unit under supervision.
const SupervisedMarker = "/etc/uc-pxx-step2/supervised"

// FailedLine is the line the caller appends when a step-2 target failed (the
// target {0,0,0} after a step-2 arm, seen from the keeper).
func FailedLine(drive string) string { return "failed " + drive + "\n" }

// AttemptLine is the line the caller appends (durably) before an automatic run.
func AttemptLine(drive string) string { return "drive " + drive + "\n" }

// AutoAllowed decides whether an AUTO-STEP2 request gives the countdown: the
// drive asked for it, its identity is known, no attempt with this drive was
// made, no step-2 target ever failed, and the run is not supervised.
func AutoAllowed(requested bool, drive string, attempts []Attempt, supervised bool) (bool, string) {
	switch {
	case !requested:
		return false, "the drive has no " + AutoFile
	case supervised:
		return false, "a supervised run never starts by itself (each A/B write has its rule-9 GO)"
	case drive == "":
		return false, "the drive's identity is not known"
	}
	for _, a := range attempts {
		if a.Failed {
			return false, "a step-2 target failed before: ask the user"
		}
		if a.Drive == drive {
			return false, fmt.Sprintf("this drive (%s) started step 2 once already", drive)
		}
	}
	return true, ""
}

// New is the model at the start of the question. auto must be the result of
// AutoAllowed, and its supervised input a fixed marker file of the unit
// (SupervisedMarker), never a setting the panel service could get wrong
// (review T4 NIT-4). now must be time.Now() (monotonic), never an evdev
// time stamp.
func New(now time.Time, auto bool, autoNo string) *Model {
	m := &Model{Screen: ScreenAsk, AutoNo: autoNo}
	if auto {
		m.Screen, m.Auto, m.ends = ScreenCountdown, true, now.Add(AutoCountdown)
	}
	return m
}

// Left is the time left of the hold or of the countdown (0 when none).
func (m *Model) Left(now time.Time) time.Duration {
	switch {
	case m.Screen == ScreenCountdown:
		return max(0, m.ends.Sub(now))
	case !m.held.IsZero():
		return max(0, m.held.Add(HoldOK).Sub(now))
	}
	return 0
}

// Handle feeds one key (KeyNone: a tick) at time now.
func (m *Model) Handle(k Key, now time.Time) {
	switch m.Screen {
	case ScreenStart, ScreenCancelled, ScreenNeverSet:
		return // final
	case ScreenCountdown:
		switch {
		case k == KeyBack:
			m.Screen = ScreenCancelled
		case k == KeyOKDown || k == KeyVolDown || k == KeyVolUp:
			// any other key turns the countdown into the confirm screen; the
			// press that stopped the countdown does not start the hold (a
			// new press of OK does: review T4 NIT-1, as the RAM flow drops a
			// hold on every screen change). A start from there is a
			// confirmed start, not an automatic one.
			m.Screen, m.held, m.Auto = ScreenAsk, time.Time{}, false
		case !now.Before(m.ends):
			m.Screen = ScreenStart
		}
		return
	}
	switch k {
	case KeyBack:
		if m.Screen == ScreenNever {
			m.Screen, m.held = ScreenAsk, time.Time{}
			return
		}
		m.Screen = ScreenCancelled
	case KeyVolDown, KeyVolUp:
		m.held = time.Time{}
		if m.Screen == ScreenAsk {
			m.Screen = ScreenNever
		} else {
			m.Screen = ScreenAsk
		}
	case KeyOKDown:
		// every press starts the hold again (a lost key-up never lets a
		// short second press start the run: review T4 NIT-1)
		m.held = now
	case KeyOKUp:
		m.held = time.Time{}
	}
	if !m.held.IsZero() && !now.Before(m.held.Add(HoldOK)) {
		if m.Screen == ScreenNever {
			m.Screen = ScreenNeverSet
		} else {
			m.Screen = ScreenStart
		}
		m.held = time.Time{}
	}
}
