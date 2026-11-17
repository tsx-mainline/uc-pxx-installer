package xfer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBindWatch (CANCEL-GADGET §5 follow-up 2): a failed bind is logged once
// per text and the bind is tried again; a bound gadget with the PHY active
// whose UDC stays "not attached" gets one reconnect after notAttachedReset,
// at most every reconnectEvery; any other state resets the watch.
func TestBindWatch(t *testing.T) {
	sys := t.TempDir()
	udcDir := filepath.Join(sys, "sys/class/udc/fe800000.usb")
	os.MkdirAll(udcDir, 0o755)
	state := func(v string) { os.WriteFile(filepath.Join(udcDir, "state"), []byte(v+"\n"), 0o644) }
	soft := filepath.Join(udcDir, "soft_connect")
	var log []string
	cfs := filepath.Join(t.TempDir(), "g")
	os.MkdirAll(cfs, 0o755)
	s := &inst{c: InstConfig{ConfigFS: cfs}, logf: func(f string, a ...any) { log = append(log, fmt.Sprintf(f, a...)) }}
	t0 := time.Unix(1800000000, 0)

	// the bind fails (UDC is a folder): logged once, tried each tick
	os.Mkdir(filepath.Join(cfs, "UDC"), 0o755)
	for i := 0; i < 3; i++ {
		s.udcStep(sys, "fe800000.usb", true, true, t0.Add(time.Duration(i)*time.Second))
	}
	if len(log) != 1 || !strings.Contains(log[0], "could not be bound to fe800000.usb") {
		t.Fatalf("log %q", log)
	}
	// it binds: the log names the earlier failure
	os.Remove(filepath.Join(cfs, "UDC"))
	s.udcStep(sys, "fe800000.usb", true, true, t0.Add(3*time.Second))
	if len(log) != 2 || !strings.Contains(log[1], "gadget bound to fe800000.usb (after: ") || s.bw.err != "" {
		t.Fatalf("log %q", log)
	}
	// bound, PHY active, "not attached": a reconnect after 5 s, then not
	// again within 30 s
	state("not attached")
	at := t0.Add(10 * time.Second)
	for i := 0; i <= 40; i++ {
		s.udcStep(sys, "fe800000.usb", true, true, at.Add(time.Duration(i)*time.Second))
	}
	n := 0
	for _, l := range log {
		if strings.Contains(l, "is not attached for") {
			n++
		}
	}
	if b, _ := os.ReadFile(soft); n != 2 || string(b) != "connect" {
		t.Fatalf("%d reconnects (want 2 in 40 s: at 5 s and at 35 s), soft_connect %q\n%q", n, b, log)
	}
	// "configured" resets the watch; a short "not attached" does nothing
	log = nil
	state("configured")
	s.udcStep(sys, "fe800000.usb", true, true, at.Add(100*time.Second))
	state("not attached")
	for i := 101; i < 105; i++ {
		s.udcStep(sys, "fe800000.usb", true, true, at.Add(time.Duration(i)*time.Second))
	}
	// the PHY not active: no reconnect at all
	for i := 105; i < 120; i++ {
		s.udcStep(sys, "fe800000.usb", false, false, at.Add(time.Duration(i)*time.Second))
	}
	if len(log) != 0 {
		t.Fatalf("log %q", log)
	}
}
