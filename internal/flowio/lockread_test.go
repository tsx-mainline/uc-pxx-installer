package flowio

// The unlock status read at the start (lockread.go, mainline-rc-10 I-1) with
// a fake `uc-pxx-inst lockstate`.

import (
	"os"
	"strings"
	"testing"
	"time"

	"uc-pxx-installer/internal/flow"
)

// withLockState makes the fake uc-pxx-inst answer `lockstate` with body.
func withLockState(g *rig, body string) {
	s, _ := os.ReadFile(g.r.c.Exe)
	os.WriteFile(g.r.c.Exe, []byte(strings.Replace(string(s), "plan)\n", "lockstate)\n"+body+"\n\t;;\nplan)\n", 1)), 0o755)
}

func TestLockReadAtStart(t *testing.T) {
	for _, c := range []struct {
		name, body, word string
		hold             bool
	}{
		{"unlocked", `echo "LOCKSTATE: 0x01"; echo "RESULT: lock state read (nothing written)"; exit 0`, "unlocked", false},
		{"locked", `echo "LOCKSTATE: 0x00"; exit 0`, "locked", false},
		{"failed read", `echo "LOCKSTATE: none"; echo "LOCKSTATE-ERROR: the client: exit 3"; exit 0`, "could not read", false},
		{"refused", `echo "RESULT: the A/B lock is held"; exit 1`, "could not read", false},
		{"changed", `echo "LOCKSTATE: 0x01"; echo "LOCKSTATE-CHANGED: yes"; echo "RESULT: STOP"; exit 3`, "changed", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := newRig(t, s0, "exit 0")
			withLockState(g, "\tsleep 0.3; "+c.body)
			g.r.Start()
			if g.m.Unlock() != flow.UnlockReading {
				t.Fatalf("at the start: %q", g.m.Unlock())
			}
			g.pump(func() bool { return g.m.Unlock() != flow.UnlockReading }, 10*time.Second)
			if g.m.Unlock() != c.word {
				t.Fatalf("%q, want %q", g.m.Unlock(), c.word)
			}
			if (g.m.Screen == flow.ScreenHold) != c.hold {
				t.Fatalf("screen %s", g.m.Screen)
			}
			if !c.hold && !strings.Contains(bodyText(g.m), "Unlock status") {
				t.Fatalf("no row:\n%s", bodyText(g.m))
			}
			if strings.Contains(bodyText(g.m), "0x0") {
				t.Fatalf("the hex value on the screen:\n%s", bodyText(g.m))
			}
			if log := strings.Join(g.r.log, "\n"); !strings.Contains(log, "unlock status: "+c.word) {
				t.Fatalf("flow log:\n%s", log)
			}
		})
	}
	t.Run("a plan waits for the read", func(t *testing.T) {
		g := newRig(t, s0, "exit 0")
		withLockState(g, "\tsleep 2; echo \"LOCKSTATE: 0x01\"; exit 0")
		g.r.Start()
		g.pump(func() bool { return g.m.Unit != nil }, 20*time.Second)
		g.toPlanQuick()
		log := strings.Join(g.r.log, "\n")
		if !strings.Contains(log, "start-plan waits for the lock-state read") ||
			strings.Index(log, "plan child started") < strings.Index(log, "unlock status: unlocked") {
			t.Fatalf("flow log:\n%s", log)
		}
	})
}

// TestLockReadSwitchWaits (RC-10 t6 TestFlowSwitch): a switch dry run asked
// for while the read of the start runs waits for its end (the reader holds
// the A/B lock: a switch child started then refused "the lock is held").
func TestLockReadSwitchWaits(t *testing.T) {
	g := newRig(t, s0, "exit 0")
	g.tools("5", "3")
	withLockState(g, "\tsleep 2; echo \"LOCKSTATE: 0x01\"; exit 0")
	g.r.Start()
	g.r.Do([]flow.Action{{Kind: flow.ActSwitchCheck, Slot: "b"}})
	g.pump(func() bool { return strings.Contains(strings.Join(g.r.log, "\n"), "switch child started") }, 20*time.Second)
	log := strings.Join(g.r.log, "\n")
	if !strings.Contains(log, "switch-check waits for the lock-state read") ||
		strings.Index(log, "switch child started") < strings.Index(log, "unlock status: unlocked") {
		t.Fatalf("flow log:\n%s", log)
	}
}

// toPlanQuick: Install, the eject, the panel's yes, the plan screen.
func (g *rig) toPlanQuick() {
	g.t.Helper()
	g.key(flow.KeyNext)
	g.key(flow.KeyOK)
	g.key(flow.KeyOK) // eject
	g.pump(func() bool { return g.m.Screen == flow.ScreenPanel }, 10*time.Second)
	g.panelYes()
	g.pump(func() bool { return g.m.Screen == flow.ScreenPlan }, 20*time.Second)
}
