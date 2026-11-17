package flow

// CANCEL-GADGET-2026-11-16 (unit 4, 2026-11-16 15:06): while a file comes over
// the cable, Back (Cancel) and Vol- (Drive) on the cable-wait screen ask the
// step-1 service for nothing (each request takes the gadget off the UDC; on
// the unit a gadget taken off during a bulk transfer did not come back). The
// screen says so. After the file, with no file, and for a stopped send (no
// bytes for usbSendQuiet) the keys act as before.

import (
	"testing"
	"time"
)

func usbWaitModel(t *testing.T, now time.Time) *Model {
	t.Helper()
	m := New("t", false)
	m.SetUSB()
	m.SetUnit(Unit{ABValid: true, AutoUpdate: AutoUpdateView{State: "off"}})
	if m.Screen != ScreenFirmware {
		m.Sel = 1
		pressAt(m, KeyOK, now)
	}
	m.Take()
	if m.Screen != ScreenFirmware || !m.usbActive() || m.USBWaiting() != "firmware" {
		t.Fatalf("not the cable wait: %s %q", m.Screen, m.USBWaiting())
	}
	return m
}

func pressAt(m *Model, k Key, now time.Time) []Action {
	m.Handle(Event{k, true}, now)
	if k == KeyOK {
		m.Handle(Event{k, false}, now)
	}
	return m.Take()
}

func TestCancelGadgetKeysHeldDuringSend(t *testing.T) {
	t0 := time.Unix(1800000000, 0)
	m := usbWaitModel(t, t0)
	m.USBReceiving(USBProgress{File: "firmware.bin", Bytes: 300e6, Total: 972963471}, t0)
	for _, k := range []Key{KeyBack, KeyPrev} {
		if a := pressAt(m, k, t0.Add(2*time.Second)); len(a) != 0 || m.Screen != ScreenFirmware || m.Msg != txtUSBSendRuns {
			t.Fatalf("%v during the send: %+v %s %q", k, a, m.Screen, m.Msg)
		}
	}
	// new bytes: still a send, counted from the last bytes
	m.USBReceiving(USBProgress{File: "firmware.bin", Bytes: 400e6, Total: 972963471}, t0.Add(8*time.Second))
	if a := pressAt(m, KeyBack, t0.Add(8*time.Second+usbSendQuiet-time.Second)); len(a) != 0 || m.Screen != ScreenFirmware {
		t.Fatalf("Back %v after the last bytes: %+v %s", usbSendQuiet-time.Second, a, m.Screen)
	}
	// the same progress again (the runner gives only changes; a test of the rule): not a move
	m.USBReceiving(USBProgress{File: "firmware.bin", Bytes: 400e6, Total: 972963471}, t0.Add(12*time.Second))
	// no bytes for usbSendQuiet: a stopped send; Back cancels as before
	a := pressAt(m, KeyBack, t0.Add(8*time.Second+usbSendQuiet))
	if len(a) != 1 || a[0].Kind != ActUSB || a[0].Want != "firmware" || m.Screen != ScreenStatus {
		t.Fatalf("Back after a stopped send: %+v %s", a, m.Screen)
	}
}

func TestCancelGadgetKeysAfterOrWithoutAFile(t *testing.T) {
	want := map[Key]string{KeyBack: "firmware", KeyPrev: "drive"}
	for _, done := range []bool{false, true} {
		for _, k := range []Key{KeyBack, KeyPrev} {
			t0 := time.Unix(1800000000, 0)
			m := usbWaitModel(t, t0)
			if done {
				// the file came in and is done: the runner gives an empty progress
				m.USBReceiving(USBProgress{File: "firmware.bin", Bytes: 972963471, Total: 972963471}, t0)
				m.USBReceiving(USBProgress{}, t0.Add(time.Second))
			}
			a := pressAt(m, k, t0.Add(2*time.Second))
			if len(a) != 1 || a[0].Kind != ActUSB || a[0].Want != want[k] {
				t.Fatalf("%v (file done %v): %+v", k, done, a)
			}
		}
	}
}

// the held keys in the bundle stage (after the stash, the swap view) too
func TestCancelGadgetKeysHeldDuringBundleSend(t *testing.T) {
	t0 := time.Unix(1800000000, 0)
	m := usbWaitModel(t, t0)
	m.USBFirmwareSealed(USBFirmware{Mode: "mainline", Size: 973, Ident: "size=1,mtime=1,ctime=1,dev=1,ino=1"})
	m.Take()
	m.StashEnded(StashResult{OK: true, End: End{Exited: true}})
	m.Take()
	if m.USBWaiting() != "bundle" {
		t.Fatalf("not the bundle wait: %s %q", m.Screen, m.USBWaiting())
	}
	m.USBReceiving(USBProgress{File: "rootfs.ext4.zst", Bytes: 50e6, Total: 300e6}, t0)
	if a := pressAt(m, KeyBack, t0.Add(time.Second)); len(a) != 0 || m.Msg != txtUSBSendRuns {
		t.Fatalf("Back during the bundle send: %+v %q", a, m.Msg)
	}
}
