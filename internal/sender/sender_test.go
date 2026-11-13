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
	"strconv"
	"strings"
	"sync"
	"syscall"
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

	set, err := sender.PrepareSet(sender.Payload, dir, pin)
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	d := sender.New(l, t.Logf)
	if err := d.Send(set); err != nil {
		l.Close()
		t.Fatalf("send: %v", err)
	}
	if set.First != pin {
		t.Fatalf("first sha %s, want %s", set.First, pin)
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
	if rd.Record.Auto {
		t.Fatal("the sender asked the unit to go on by itself (auto): it never does (RC-6 sender review MAJOR-1)")
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
	set, err := sender.PrepareSet(sender.Payload, dir, mltest.SHA(b.ManifestJSON()))
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	d := sender.New(l, t.Logf)
	err = d.Send(set)
	l.Close()
	if err == nil || !strings.Contains(err.Error(), "is not a set the unit pins") || !errors.Is(err, sender.ErrLocal) {
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

	set, err := sender.PrepareSet(sender.Finish, dir, pin)
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	d := sender.New(l, t.Logf)
	if err := d.Send(set); err != nil {
		l.Close()
		t.Fatalf("send finish: %v", err)
	}
	l.Close() // end the session so the receiver's Serve returns
	<-done
}

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

// testBundle writes a payload set and returns its folder and manifest sha.
func testBundle(t *testing.T, seed int64) (string, string) {
	t.Helper()
	res := mltest.Fill(seed, 5000)
	ch := mltest.Chain{Version: "1.0.0.94", BuildType: "userdebug", UBoot: strings.Repeat("1", 64), Trust: strings.Repeat("2", 64),
		Dtbo: strings.Repeat("3", 64), Vbmeta: strings.Repeat("4", 64), Resource: mltest.SHA(res)}
	b := mltest.NewBundle("tok-neg", 9000, 1<<20, ch, res, 38029312)
	dir, err := b.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir, mltest.SHA(b.ManifestJSON())
}

// TestPrepareSetRefuses (RC-6 sender review MINOR-1, MINOR-2): the set is
// refused on the host, before any device is opened, when its first file is
// not the expected sha, when a file is a link or not a regular file, when a
// file differs from the manifest, or when the folder is not a folder.
func TestPrepareSetRefuses(t *testing.T) {
	dir, pin := testBundle(t, 9)
	for _, c := range []struct {
		name   string
		edit   func(t *testing.T, dir string) string
		expect string
		want   string
	}{
		{"another expected sha", nil, strings.Repeat("b", 64), "not the expected"},
		{"an expected sha that is not hex", nil, "xyz", "64 lowercase hex"},
		{"the manifest is a link", func(t *testing.T, dir string) string {
			m := filepath.Join(dir, xfer.ManifestName)
			b, _ := os.ReadFile(m)
			o := filepath.Join(t.TempDir(), "real.json")
			os.WriteFile(o, b, 0o644)
			os.Remove(m)
			if err := os.Symlink(o, m); err != nil {
				t.Fatal(err)
			}
			return dir
		}, pin, "a link is not followed"},
		{"the manifest links to a device node", func(t *testing.T, dir string) string {
			m := filepath.Join(dir, xfer.ManifestName)
			os.Remove(m)
			if err := os.Symlink("/dev/zero", m); err != nil {
				t.Fatal(err)
			}
			return dir
		}, pin, "a link is not followed"},
		{"the root file system is a FIFO", func(t *testing.T, dir string) string {
			r := filepath.Join(dir, xfer.RootfsName)
			os.Remove(r)
			if err := syscall.Mkfifo(r, 0o644); err != nil {
				t.Skip("no mkfifo:", err)
			}
			return dir
		}, pin, "not a regular file"},
		{"the boot template changed", func(t *testing.T, dir string) string {
			p := filepath.Join(dir, xfer.TemplateName)
			b, _ := os.ReadFile(p)
			b[100] ^= 1
			os.WriteFile(p, b, 0o644)
			return dir
		}, pin, "a damaged download"},
		{"the folder is a file", func(t *testing.T, dir string) string { return filepath.Join(dir, xfer.ManifestName) }, pin, "is not a folder"},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := dir
			if c.edit != nil {
				src, _ := testBundle(t, 9)
				d = c.edit(t, src)
			}
			s, err := sender.PrepareSet(sender.Payload, d, c.expect)
			if err == nil || !errors.Is(err, sender.ErrLocal) || !strings.Contains(err.Error(), c.want) {
				if s != nil {
					s.Close()
				}
				t.Fatalf("want %q, got %v", c.want, err)
			}
		})
	}
}

// scripted is a Transport that answers HELLO with a given body and then
// PUT_BEGIN accept, PUT_END with a given sha (to test the sender's checks
// without a receiver).
type scripted struct {
	hello  string
	endSHA string
	outs   int
	data   int64
	last   []byte
}

func (s *scripted) Out(b []byte) error {
	s.outs++
	if len(b) >= xfer.HeaderSize && string(b[:4]) == xfer.Magic {
		s.last = append([]byte(nil), b...)
	} else {
		s.data += int64(len(b))
	}
	return nil
}

func (s *scripted) In() ([]byte, error) {
	typ := uint16(s.last[4]) | uint16(s.last[5])<<8
	seq := uint32(s.last[12]) | uint32(s.last[13])<<8 | uint32(s.last[14])<<16 | uint32(s.last[15])<<24
	var body string
	switch typ {
	case xfer.TypeHello:
		body = s.hello
	case xfer.TypePutBegin:
		if s.data == 0 {
			body = `{"accept":true,"name":"manifest.json"}`
			fr, _ := xfer.EncodeFrame(typ|xfer.TypeReply, seq, []byte(body))
			s.data = -1 // the next reply is PUT_END
			return fr, nil
		}
		fr, _ := xfer.EncodeFrame(xfer.TypePutEnd|xfer.TypeReply, seq, []byte(`{"ok":true,"name":"manifest.json","sha256":"`+s.endSHA+`"}`))
		return fr, nil
	}
	fr, _ := xfer.EncodeFrame(typ|xfer.TypeReply, seq, []byte(body))
	return fr, nil
}

func (s *scripted) Close() error { return nil }

// TestSendChecksHelloAndPutEnd (MINOR-1, NIT-3): a HELLO with another proto or
// USB id, or another mode, is refused before any byte of the set; a PUT_END
// whose sha256 is not the file's is a unit refusal.
func TestSendChecksHelloAndPutEnd(t *testing.T) {
	dir, pin := testBundle(t, 11)
	hello := func(proto int, usb, mode string) string {
		return `{"proto":` + strconv.Itoa(proto) + `,"usb":"` + usb + `","mode":"` + mode + `","state":"waiting","pins":["` + pin + `"],"room":0}`
	}
	for _, c := range []struct {
		name, hello string
		want        error
	}{
		{"proto 99", hello(99, sender.ReviewedUSB, "payload"), sender.ErrLocal},
		{"another usb id", hello(xfer.ProtoVersion, "dead:beef", "payload"), sender.ErrLocal},
		{"mode finish", hello(xfer.ProtoVersion, sender.ReviewedUSB, "finish"), sender.ErrLocal},
	} {
		t.Run(c.name, func(t *testing.T) {
			set, err := sender.PrepareSet(sender.Payload, dir, pin)
			if err != nil {
				t.Fatal(err)
			}
			defer set.Close()
			tr := &scripted{hello: c.hello}
			if err := sender.New(tr, t.Logf).Send(set); !errors.Is(err, c.want) || tr.outs != 1 || tr.data != 0 {
				t.Fatalf("err %v, %d outs, %d data bytes (want only the HELLO)", err, tr.outs, tr.data)
			}
		})
	}
	t.Run("a PUT_END sha that is not the file's", func(t *testing.T) {
		set, err := sender.PrepareSet(sender.Payload, dir, pin)
		if err != nil {
			t.Fatal(err)
		}
		defer set.Close()
		tr := &scripted{hello: hello(xfer.ProtoVersion, sender.ReviewedUSB, "payload"), endSHA: strings.Repeat("c", 64)}
		if err := sender.New(tr, t.Logf).Send(set); !errors.Is(err, sender.ErrUnit) {
			t.Fatalf("err %v, want a unit refusal", err)
		}
	})
}
