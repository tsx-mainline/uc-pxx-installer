package xfer

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"uc-pxx-installer/internal/mainline/mltest"
)

// fakeLink is a packet-level model of one FunctionFS session on a UDC
// without the aligned-size quirk (dummy_hcd, the prototype's finding 1):
//   - a host transfer is cut into 512-byte packets; the last one is short
//     (none when the length is a multiple of 512: no zero-length packet, as
//     Chromium sends; a transfer of 0 bytes is one zero-length packet);
//   - a device read of len(p) takes packets until p is full or a short
//     packet ended the transfer; a packet that does not fit fails the read
//     with EOVERFLOW (the packet is lost);
//   - disable() ends every read and write with ESHUTDOWN.
type fakeLink struct {
	mu       sync.Mutex
	cond     *sync.Cond
	pkts     [][]byte
	replies  chan []byte
	disabled bool
	halted   bool
	closed   bool
	reads    []int // the sizes of the device reads
}

func newLink() *fakeLink {
	l := &fakeLink{replies: make(chan []byte, 64)}
	l.cond = sync.NewCond(&l.mu)
	return l
}

// hostOut is a host bulk OUT transfer.
func (l *fakeLink) hostOut(b []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.disabled {
		return syscall.ESHUTDOWN
	}
	if l.halted {
		return errors.New("stall")
	}
	if len(b) == 0 {
		l.pkts = append(l.pkts, []byte{})
	}
	for o := 0; o < len(b); o += 512 {
		e := o + 512
		if e > len(b) {
			e = len(b)
		}
		l.pkts = append(l.pkts, append([]byte(nil), b[o:e]...))
	}
	l.cond.Broadcast()
	return nil
}

func (l *fakeLink) disable() {
	l.mu.Lock()
	l.disabled = true
	l.cond.Broadcast()
	l.mu.Unlock()
}

func (l *fakeLink) Read(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reads = append(l.reads, len(p))
	n := 0
	for {
		for len(l.pkts) == 0 && !l.disabled {
			l.cond.Wait()
		}
		if l.disabled {
			return 0, syscall.ESHUTDOWN
		}
		pk := l.pkts[0]
		l.pkts = l.pkts[1:]
		if len(pk) > len(p)-n {
			return 0, syscall.EOVERFLOW
		}
		n += copy(p[n:], pk)
		if len(pk) < 512 || n == len(p) {
			return n, nil
		}
	}
}

func (l *fakeLink) Write(p []byte) (int, error) {
	l.mu.Lock()
	dis := l.disabled
	l.mu.Unlock()
	if dis {
		return 0, syscall.ESHUTDOWN
	}
	l.replies <- append([]byte(nil), p...)
	return len(p), nil
}

func (l *fakeLink) Halt() {
	l.mu.Lock()
	l.halted = true
	l.mu.Unlock()
}

func (l *fakeLink) Close() error {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
	return nil
}

// host is the page's side of a session.
type host struct {
	t   *testing.T
	l   *fakeLink
	seq uint32
}

func (h *host) frame(typ uint16, v any) []byte {
	j, err := json.Marshal(v)
	if err != nil {
		h.t.Fatal(err)
	}
	h.seq++
	f, err := EncodeFrame(typ, h.seq, j)
	if err != nil {
		h.t.Fatal(err)
	}
	return f
}

// reply waits for one reply and decodes it.
func (h *host) reply(wantType uint16) map[string]any {
	h.t.Helper()
	select {
	case b := <-h.l.replies:
		if len(b)%64 == 0 {
			h.t.Fatalf("a reply of %d bytes: a multiple of 64", len(b))
		}
		if string(b[:4]) != Magic {
			h.t.Fatalf("reply without magic")
		}
		typ := uint16(b[4]) | uint16(b[5])<<8
		if typ != wantType {
			h.t.Fatalf("reply type %#x, want %#x: %s", typ, wantType, b[16:])
		}
		var m map[string]any
		if err := json.Unmarshal(b[HeaderSize:], &m); err != nil {
			h.t.Fatalf("reply body: %v: %q", err, b[HeaderSize:])
		}
		return m
	case <-time.After(5 * time.Second):
		h.t.Fatalf("no reply (want %#x)", wantType)
	}
	return nil
}

func (h *host) noReply() {
	h.t.Helper()
	select {
	case b := <-h.l.replies:
		h.t.Fatalf("unexpected reply %q", b)
	case <-time.After(50 * time.Millisecond):
	}
}

func (h *host) req(typ uint16, v any) map[string]any {
	h.t.Helper()
	if err := h.l.hostOut(h.frame(typ, v)); err != nil {
		h.t.Fatal(err)
	}
	return h.reply(typ | TypeReply)
}

// put sends one file in transfers of chunk bytes; it returns the PUT_BEGIN
// answer and, when accepted, the PUT_END answer.
func (h *host) put(name string, data []byte, size int64, chunk int) (begin, end map[string]any) {
	h.t.Helper()
	begin = h.req(TypePutBegin, PutBegin{Name: name, Size: size})
	if begin["accept"] != true {
		return begin, nil
	}
	for o := 0; o < len(data); o += chunk {
		e := o + chunk
		if e > len(data) {
			e = len(data)
		}
		if err := h.l.hostOut(data[o:e]); err != nil {
			h.t.Fatal(err)
		}
	}
	return begin, h.reply(TypePutEnd | TypeReply)
}

// fixture is a made-up payload that ParseManifest accepts.
type fixture struct {
	manifest, template, rootfs []byte
	pin                        string
}

func newFixture(rootfs int) fixture {
	res := mltest.Fill(77, 5000)
	ch := mltest.Chain{Version: "1.0.0.94", BuildType: "userdebug", UBoot: strings.Repeat("1", 64), Trust: strings.Repeat("2", 64),
		Dtbo: strings.Repeat("3", 64), Vbmeta: strings.Repeat("4", 64), Resource: mltest.SHA(res)}
	b := mltest.NewBundle("tok-xfer", 9000, rootfs, ch, res, 38029312)
	m := b.ManifestJSON()
	return fixture{manifest: m, template: b.Template, rootfs: b.Rootfs, pin: mltest.SHA(m)}
}

// rig is a receiver in a temporary folder.
type rig struct {
	t      *testing.T
	dir    string
	r      *Receiver
	mem    int64
	reconn int
	mu     sync.Mutex
	now    time.Time
}

func newRig(t *testing.T, cfg Config) *rig {
	t.Helper()
	g := &rig{t: t, dir: t.TempDir(), mem: 4 << 30, now: time.Unix(1_800_000_000, 0)}
	if !cfg.StatusOnly {
		cfg.Dir = g.dir
	}
	cfg.MemAvailable = func() int64 { g.mu.Lock(); defer g.mu.Unlock(); return g.mem }
	cfg.Free = func(string) int64 { return 8 << 30 }
	cfg.Now = func() time.Time { g.mu.Lock(); defer g.mu.Unlock(); return g.now }
	cfg.Reconnect = func() { g.mu.Lock(); g.reconn++; g.mu.Unlock() }
	cfg.Logf = t.Logf
	r, err := NewReceiver(cfg)
	if err != nil {
		t.Fatal(err)
	}
	g.r = r
	return g
}

func (g *rig) reconnects() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.reconn
}

func (g *rig) advance(d time.Duration) {
	g.mu.Lock()
	g.now = g.now.Add(d)
	g.mu.Unlock()
}

// session starts one session; wait() waits for its end.
func (g *rig) session() (*host, func()) {
	l := newLink()
	done := make(chan struct{})
	go func() {
		g.r.Serve(l)
		close(done)
	}()
	return &host{t: g.t, l: l}, func() {
		g.t.Helper()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			g.t.Fatal("the session did not end")
		}
	}
}

func (g *rig) entries() []string {
	ents, err := os.ReadDir(g.dir)
	if err != nil {
		g.t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

func (g *rig) state() State {
	s, err := ReadState(g.dir)
	if err != nil {
		g.t.Fatal(err)
	}
	return *s
}
