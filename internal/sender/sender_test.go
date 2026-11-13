package sender_test

// The host sender driven against a live internal/xfer.Receiver over an
// in-memory bulk link: a payload is pre-checked on the host, sent, and the
// receiver seals a ready folder. A finish set is sent the same way. The
// sender writes nothing but the transport; the unit (the receiver) is the
// authority and checks every file.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"uc-pxx-installer/internal/mainline/mltest"
	"uc-pxx-installer/internal/sender"
	"uc-pxx-installer/internal/xfer"
	"uc-pxx-installer/internal/xfer/handover"
)

// memLink is one in-memory session: the host end (Out/In, the Transport) and
// the device end (Read/Write/Halt/Close, the xfer.Conn). Each host Out is one
// transfer the receiver's next Read returns; each receiver Write is one
// transfer the host's next In returns.
type memLink struct {
	h2d     chan []byte // host -> device transfers
	d2h     chan []byte // device -> host transfers
	mu      sync.Mutex
	done    bool
	hClosed bool
}

func newMemLink() *memLink {
	return &memLink{h2d: make(chan []byte, 256), d2h: make(chan []byte, 256)}
}

// host side (Transport)
func (l *memLink) Out(b []byte) error {
	cp := append([]byte(nil), b...)
	select {
	case l.h2d <- cp:
		return nil
	default:
		return errors.New("host OUT: the device is not reading")
	}
}

func (l *memLink) In() ([]byte, error) {
	b, ok := <-l.d2h
	if !ok {
		return nil, errors.New("host IN: the device closed")
	}
	return b, nil
}

func (l *memLink) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.hClosed {
		l.hClosed = true
		close(l.h2d)
	}
	return nil
}

// device side (xfer.Conn)
func (l *memLink) Read(p []byte) (int, error) {
	b, ok := <-l.h2d
	if !ok {
		return 0, errors.New("device read: closed")
	}
	if len(b) > len(p) {
		return 0, errors.New("device read: transfer larger than the buffer (EOVERFLOW)")
	}
	return copy(p, b), nil
}

func (l *memLink) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.done {
		return 0, errors.New("device write: closed")
	}
	l.d2h <- append([]byte(nil), p...)
	return len(p), nil
}

func (l *memLink) Halt() {}

func (l *memLink) deviceClose() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.done {
		l.done = true
		close(l.d2h)
	}
	return nil
}

func receiverCfg(t *testing.T, dir string, pins []string, kind string) xfer.Config {
	return xfer.Config{
		Dir: dir, Pins: pins, Kind: kind,
		MemAvailable: func() int64 { return 4 << 30 },
		Free:         func(string) int64 { return 8 << 30 },
		Reconnect:    func() {},
		Logf:         t.Logf,
	}
}

func TestSendPayload(t *testing.T) {
	res := mltest.Fill(77, 5000)
	ch := mltest.Chain{Version: "1.0.0.94", BuildType: "userdebug", UBoot: strings.Repeat("1", 64), Trust: strings.Repeat("2", 64),
		Dtbo: strings.Repeat("3", 64), Vbmeta: strings.Repeat("4", 64), Resource: mltest.SHA(res)}
	b := mltest.NewBundle("tok-send", 9000, 3<<20+4096, ch, res, 38029312)
	pin := mltest.SHA(b.ManifestJSON())

	src := t.TempDir()
	dir, err := b.Write(src)
	if err != nil {
		t.Fatal(err)
	}

	stage := t.TempDir()
	r, err := xfer.NewReceiver(receiverCfg(t, stage, []string{pin}, xfer.KindPayload))
	if err != nil {
		t.Fatal(err)
	}
	l := newMemLink()
	done := make(chan struct{})
	go func() { r.Serve(l); l.deviceClose(); close(done) }()

	d := sender.New(l, t.Logf)
	first, err := d.Send(sender.Payload, dir, true)
	if err != nil {
		l.Close()
		t.Fatalf("send: %v", err)
	}
	if first != pin {
		t.Fatalf("first sha %s, want %s", first, pin)
	}
	l.Close() // end the session so the receiver's Serve returns
	<-done

	rd, err := xfer.CheckReady(stage, -1, []string{pin})
	if err != nil {
		t.Fatalf("the staged folder is not a ready payload: %v", err)
	}
	if rd.Record.ManifestSHA256 != pin || len(rd.Record.Files) != 3 {
		t.Fatalf("ready record %+v", rd.Record)
	}
	if !rd.Record.Auto {
		t.Fatal("the --auto commit was not recorded")
	}
}

func TestSendPayloadRefusedByPin(t *testing.T) {
	res := mltest.Fill(5, 5000)
	ch := mltest.Chain{Version: "1.0.0.94", BuildType: "userdebug", UBoot: strings.Repeat("1", 64), Trust: strings.Repeat("2", 64),
		Dtbo: strings.Repeat("3", 64), Vbmeta: strings.Repeat("4", 64), Resource: mltest.SHA(res)}
	b := mltest.NewBundle("tok-send2", 9000, 1<<20, ch, res, 38029312)
	src := t.TempDir()
	dir, _ := b.Write(src)

	// the unit pins a different manifest, so the host pre-check refuses
	stage := t.TempDir()
	r, err := xfer.NewReceiver(receiverCfg(t, stage, []string{strings.Repeat("a", 64)}, xfer.KindPayload))
	if err != nil {
		t.Fatal(err)
	}
	l := newMemLink()
	go func() { r.Serve(l); l.deviceClose() }()
	d := sender.New(l, t.Logf)
	_, err = d.Send(sender.Payload, dir, false)
	l.Close()
	if err == nil || !strings.Contains(err.Error(), "is not a payload the unit pins") {
		t.Fatalf("want a pin refusal, got %v", err)
	}
}

func TestSendFinish(t *testing.T) {
	bin := mltest.Fill(3, 4096)
	bootctl := mltest.Fill(4, 8192)
	list := handover.FinishList{Kind: handover.FinishKind, Version: handover.FinishVersion, Files: []handover.ReadyFile{
		{Name: handover.FinishBin, Size: int64(len(bin)), SHA256: mltest.SHA(bin)},
		{Name: handover.FinishBootctl, Size: int64(len(bootctl)), SHA256: mltest.SHA(bootctl)},
	}}
	lb, err := jsonMarshal(&list)
	if err != nil {
		t.Fatal(err)
	}
	pin := mltest.SHA(lb)

	src := t.TempDir()
	dir := filepath.Join(src, "uc-pxx-finish")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(n string, b []byte) {
		if err := os.WriteFile(filepath.Join(dir, n), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(handover.FinishListName, lb)
	write(handover.FinishBin, bin)
	write(handover.FinishBootctl, bootctl)

	stage := t.TempDir()
	r, err := xfer.NewReceiver(receiverCfg(t, stage, []string{pin}, xfer.KindFinish))
	if err != nil {
		t.Fatal(err)
	}
	l := newMemLink()
	done := make(chan struct{})
	go func() { r.Serve(l); l.deviceClose(); close(done) }()

	d := sender.New(l, t.Logf)
	first, err := d.Send(sender.Finish, dir, false)
	if err != nil {
		l.Close()
		t.Fatalf("send finish: %v", err)
	}
	if first != pin {
		t.Fatalf("first sha %s, want %s", first, pin)
	}
	l.Close() // end the session so the receiver's Serve returns
	<-done
}

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
