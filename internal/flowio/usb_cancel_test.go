package flowio

// CANCEL-GADGET-2026-11-16: Back and Vol- while the receiver receives write
// no request to the step-1 service (the want file stays as it is); the screen
// with its line fits the panel; after the file Back asks for the firmware
// stage again, as before.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"uc-pxx-installer/internal/flow"
	"uc-pxx-installer/internal/xfer/handover"
)

func TestUSBKeysDuringSend(t *testing.T) {
	g := newRig(t, s0, "exit 0")
	s := newUSBService(g)
	g.r.Start()
	g.pump(func() bool { return g.m.Unit != nil }, 20*time.Second)
	g.key(flow.KeyNext)
	g.key(flow.KeyOK) // Install
	sf := filepath.Join(g.root, handover.InstStage, "xfer", handover.StateFile)
	st, _ := json.Marshal(handover.State{State: "receiving", File: "firmware.bin", Bytes: 300e6, Total: 972963471})
	os.WriteFile(sf, st, 0o644)
	g.pump(func() bool { return strings.Contains(bodyText(g.m), "Receiving the firmware file: 300 of 972 MB") }, 5*time.Second)
	for _, k := range []flow.Key{flow.KeyBack, flow.KeyPrev} {
		g.key(k) // g.key checks that the screen fits the panel
		if n, w := s.want(); n != 0 || g.m.Screen != flow.ScreenFirmware ||
			!strings.Contains(g.m.View(time.Now()).Msg, "Press Cancel or Drive again when it is done") {
			t.Fatalf("%v during the send: request %d %q, %s, %q", k, n, w, g.m.Screen, g.m.View(time.Now()).Msg)
		}
	}
	// the file is done: the receiver waits again
	st, _ = json.Marshal(handover.State{State: "waiting-firmware"})
	os.WriteFile(sf, st, 0o644)
	g.pump(func() bool { return !strings.Contains(bodyText(g.m), "Receiving the firmware file") }, 5*time.Second)
	g.key(flow.KeyBack)
	if n, w := s.want(); n != 1 || w != handover.WantFirmware || g.m.Screen != flow.ScreenStatus {
		t.Fatalf("Back after the file: request %d %q, %s", n, w, g.m.Screen)
	}
}
