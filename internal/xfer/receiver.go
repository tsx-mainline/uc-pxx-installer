package xfer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"uc-pxx-installer/internal/mainline"
)

// Conn is the link of one session: the bulk OUT endpoint (Read: one read(2)
// of len(p), which FunctionFS ends at the end of the host's transfer) and the
// bulk IN endpoint (Write: one transfer).
type Conn interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	// Halt stalls both endpoints after a protocol violation, so that a
	// host's transfer in flight fails at once (it never hangs); the host
	// starts again with SET_INTERFACE. Best effort.
	Halt()
	Close() error
}

// Config of a Receiver.
type Config struct {
	Dir        string        // the receiver's folder in the tmpfs stage (owned by its user)
	Pins       []string      // the manifest sha256s of the step-2 gate (uc-pxx-step2 pins)
	Reserve    int64         // RAM kept free: a file is accepted only if size <= MemAvailable - Reserve
	Idle       time.Duration // a session that holds a partial folder and gets no frame or byte for this long ends
	StatusOnly bool          // after the seal: HELLO and STATUS only
	InfoFile   string        // the root service's info for HELLO/STATUS ("" = none)

	MemAvailable func() int64           // nil: /proc/meminfo
	Free         func(dir string) int64 // nil: statfs
	Now          func() time.Time       // nil: time.Now
	Reconnect    func()                 // ask the root supervisor to drop the USB link (the idle end)
	Logf         func(string, ...any)
}

// DefaultReserve and DefaultIdle (STEP2-USB-TRANSPORT §2, §3.6).
const (
	DefaultReserve = 256 << 20
	DefaultIdle    = 30 * time.Second
	maxZeroReads   = 16 // zero-length transfers in a row before the session ends
)

// Receiver is the state of the transport across sessions.
type Receiver struct {
	cfg      Config
	pins     []string
	mu       sync.Mutex
	st       State
	sessions int
	complete bool
	cur      *session
	carry    []byte // a transfer that a stale session read for the next one

	// linkGen counts the link events (ENABLE, DISABLE, UNBIND) of the
	// FunctionFS loop. A session belongs to one value: a read that starts or
	// ends after a change belongs to the next session (review F6).
	linkGen atomic.Int64
}

// LinkEvent records an ENABLE, DISABLE or UNBIND of the function.
func (r *Receiver) LinkEvent() { r.linkGen.Add(1) }

// NewReceiver checks cfg and prepares the receiver's folder: it removes what a
// former run left (incoming.*, a record without its ready folder) and refuses
// when a ready folder still waits for the seal.
func NewReceiver(cfg Config) (*Receiver, error) {
	if cfg.Reserve <= 0 {
		cfg.Reserve = DefaultReserve
	}
	if cfg.Idle <= 0 {
		cfg.Idle = DefaultIdle
	}
	if cfg.MemAvailable == nil {
		cfg.MemAvailable = MemAvailable
	}
	if cfg.Free == nil {
		cfg.Free = FreeBytes
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Reconnect == nil {
		cfg.Reconnect = func() {}
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	r := &Receiver{cfg: cfg}
	seen := map[string]bool{}
	for _, p := range cfg.Pins {
		if !mainline.IsSHA256(p) {
			return nil, fmt.Errorf("pin %q is not a sha256", p)
		}
		if !seen[p] {
			seen[p] = true
			r.pins = append(r.pins, p)
		}
	}
	if cfg.StatusOnly {
		r.st = State{State: StateStatusOnly, Detail: "the payload is sealed"}
		r.publish()
		return r, nil
	}
	fi, err := os.Lstat(cfg.Dir)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", cfg.Dir)
	}
	if _, err := os.Lstat(filepath.Join(cfg.Dir, ReadyDir)); err == nil {
		return nil, fmt.Errorf("%s: a ready payload waits for the seal", cfg.Dir)
	}
	ents, err := os.ReadDir(cfg.Dir)
	if err != nil {
		return nil, err
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), incomingStem) || e.Name() == ReadyRecord || strings.HasPrefix(e.Name(), ".") {
			if err := os.RemoveAll(filepath.Join(cfg.Dir, e.Name())); err != nil {
				return nil, err
			}
		}
	}
	r.st = State{State: StateWaiting}
	if len(r.pins) == 0 {
		r.st.Detail = "no payload is pinned: every manifest is refused"
	}
	r.publish()
	return r, nil
}

// publish writes the state file (r.mu held or not yet shared).
func (r *Receiver) publish() {
	r.st.Updated = r.cfg.Now().Unix()
	if r.cfg.Dir == "" {
		return
	}
	b, _ := json.Marshal(r.st)
	if err := writeAtomic(r.cfg.Dir, StateFile, append(b, '\n')); err != nil {
		r.cfg.Logf("state file: %v", err)
	}
}

func (r *Receiver) setState(f func(*State)) {
	r.mu.Lock()
	f(&r.st)
	r.publish()
	st := r.st
	r.mu.Unlock()
	r.cfg.Logf("state %s: %s", st.State, st.Detail)
}

// Snapshot is the current state.
func (r *Receiver) Snapshot() State {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.st
}

// Complete reports a COMMIT: the ready folder is there.
func (r *Receiver) Complete() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.complete
}

// session is one host session: from an ENABLE of the function to the end of
// its link. Each session stages into its own incoming.<n> folder, and its
// clean-up removes only that folder (prototype finding 2, §9).
type session struct {
	r    *Receiver
	n    int
	dir  string
	gen  int64        // the link generation of the session
	last atomic.Int64 // unix nano of the last frame or data byte

	mu    sync.Mutex // guards the fields below against the idle timer
	dead  bool       // ended (idle or link)
	made  bool       // dir exists
	f     *os.File   // the file being received
	inPut bool

	// only the session goroutine
	manifestSHA string
	want        map[string]ReadyFile
	got         map[string]bool
	committed   bool
}

var (
	errEnded = errors.New("the session was ended")
	errZero  = errors.New("zero-length transfers in a row")
	errStale = errors.New("the link changed (a new configuration or SET_INTERFACE)")
	// errStaleData: bytes read across a link change that are not a whole
	// frame (the old host's file data): not carried; the session ends as a
	// violation, so the endpoints halt and the new host resyncs (re-look R1)
	errStaleData = errors.New("the link changed and the read held no frame")
)

// carryConn hands a transfer that the former session read after a link
// change to the new session as its first read.
type carryConn struct {
	Conn
	first []byte
}

func (c *carryConn) Read(p []byte) (int, error) {
	if b := c.first; b != nil {
		c.first = nil
		if len(b) > len(p) {
			return 0, errors.New("a carried transfer larger than the read")
		}
		return copy(p, b), nil
	}
	return c.Conn.Read(p)
}

// read is one read of the session's link. A read that would start after a
// link change does not start; bytes read across a change belong to the next
// session (they are carried to it), and this one ends.
func (s *session) read(c Conn, p []byte) (int, error) {
	if s.r.linkGen.Load() != s.gen {
		return 0, errStale
	}
	n, err := c.Read(p)
	if err == nil && n > 0 && s.r.linkGen.Load() != s.gen {
		if n > FrameRead || n < HeaderSize || string(p[:4]) != Magic {
			return 0, errStaleData
		}
		s.r.mu.Lock()
		s.r.carry = append([]byte(nil), p[:n]...)
		s.r.mu.Unlock()
		return 0, errStale
	}
	return n, err
}

func (s *session) touch() { s.last.Store(s.r.cfg.Now().UnixNano()) }

// Serve runs one session on c until its link ends. Sessions never overlap:
// the caller starts the next one only after Serve returned.
func (r *Receiver) Serve(c Conn) {
	r.mu.Lock()
	if r.cur != nil {
		r.mu.Unlock()
		panic("xfer: a session while another one runs")
	}
	r.sessions++
	s := &session{r: r, n: r.sessions, got: map[string]bool{}, gen: r.linkGen.Load()}
	if r.carry != nil {
		c = &carryConn{Conn: c, first: r.carry}
		r.carry = nil
	}
	s.dir = filepath.Join(r.cfg.Dir, incomingStem+strconv.Itoa(s.n))
	r.cur = s
	r.st.Session = s.n
	r.mu.Unlock()
	s.touch()
	why, k := s.run(c)
	if k == endViolation {
		c.Halt()
	}
	c.Close()
	s.end(why, k)
}

// How a session ends.
type endKind int

const (
	endLink      endKind = iota // the link ended (DISABLE, UNBIND, a cable pull, a host reset)
	endIdle                     // the idle timer ended it
	endViolation                // the host broke the protocol: the endpoints are halted
)

// end closes the session: its open file, its own incoming folder (unless it
// was committed), and the state.
func (s *session) end(why string, k endKind) {
	s.mu.Lock()
	s.dead = true
	if s.f != nil {
		s.f.Close()
		s.f = nil
	}
	if s.made && !s.committed {
		os.RemoveAll(s.dir)
		s.made = false
	}
	s.mu.Unlock()
	r := s.r
	r.mu.Lock()
	cur := r.cur == s
	if cur {
		r.cur = nil
	}
	r.mu.Unlock()
	if !cur {
		return // a session that is not the current one never changes the state
	}
	r.setState(func(st *State) {
		if st.State == StateReceiving || st.State == StateWaiting {
			st.State, st.Detail, st.File, st.Bytes, st.Total, st.ManifestSHA = StateWaiting, "session "+strconv.Itoa(s.n)+" ended: "+why, "", 0, 0, ""
			if k == endIdle {
				st.Detail += " (idle: the partial folder was removed)"
			}
		}
	})
}

// Watch runs the idle timer until stop is closed: a session that holds a
// partial folder (or is in a PUT) and got no frame or byte for Idle loses its
// folder at once (the RAM is freed: the open file is truncated), and the
// supervisor is asked to drop the USB link, so that a blocked read ends and a
// host that comes back starts again from the manifest.
func (r *Receiver) Watch(stop <-chan struct{}) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			r.idleCheck()
		}
	}
}

func (r *Receiver) idleCheck() {
	r.mu.Lock()
	s := r.cur
	r.mu.Unlock()
	if s == nil {
		return
	}
	idle := r.cfg.Now().Sub(time.Unix(0, s.last.Load()))
	if idle < r.cfg.Idle {
		return
	}
	s.mu.Lock()
	if s.dead || s.committed || !(s.made || s.inPut) {
		s.mu.Unlock()
		return
	}
	s.dead = true
	if s.f != nil {
		s.f.Truncate(0)
		s.f.Close()
		s.f = nil
	}
	os.RemoveAll(s.dir)
	s.made = false
	s.mu.Unlock()
	r.setState(func(st *State) {
		st.State, st.Detail, st.File, st.Bytes, st.Total, st.ManifestSHA = StateWaiting,
			fmt.Sprintf("session %d idle for %s: the partial folder was removed", s.n, idle.Round(time.Second)), "", 0, 0, ""
	})
	r.cfg.Reconnect()
}

func (s *session) isDead() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dead
}

// readFrame reads one whole transfer as a frame (zero-length transfers are
// skipped, a few in a row).
func (s *session) readFrame(c Conn, buf []byte) (int, error) {
	for z := 0; ; z++ {
		n, err := s.read(c, buf)
		if err != nil {
			return 0, err
		}
		if n > 0 {
			return n, nil
		}
		if z >= maxZeroReads {
			return 0, errZero
		}
	}
}

func (s *session) run(c Conn) (string, endKind) {
	buf := make([]byte, FrameRead)
	for {
		n, err := s.readFrame(c, buf)
		if s.isDead() {
			return "ended by the idle timer", endIdle
		}
		if errors.Is(err, errZero) || errors.Is(err, errStaleData) {
			return err.Error(), endViolation
		}
		if err != nil {
			return "link: " + err.Error(), endLink
		}
		s.touch()
		h, body, err := ParseFrame(buf[:n])
		if err != nil {
			return err.Error(), endViolation
		}
		var v any
		switch h.Type {
		case TypeHello:
			if err := decodeStrict(body, &struct{}{}); err != nil {
				return "HELLO: " + err.Error(), endViolation
			}
			v = s.r.hello()
		case TypeStatus:
			if err := decodeStrict(body, &struct{}{}); err != nil {
				return "STATUS: " + err.Error(), endViolation
			}
			v = s.r.status()
		case TypePutBegin:
			var p PutBegin
			if err := decodeStrict(body, &p); err != nil {
				return "PUT_BEGIN: " + err.Error(), endViolation
			}
			if why, k, ok := s.put(c, h, p); !ok {
				return why, k
			}
			continue
		case TypeCommit:
			var cm Commit
			if err := decodeStrict(body, &cm); err != nil {
				return "COMMIT: " + err.Error(), endViolation
			}
			v = s.commit(cm)
		default:
			return fmt.Sprintf("unknown frame type %#x", h.Type), endViolation
		}
		if err := s.reply(c, h.Type|TypeReply, h.Seq, v); err != nil {
			return "reply: " + err.Error(), endLink
		}
	}
}

func (s *session) reply(c Conn, typ uint16, seq uint32, v any) error {
	j, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := EncodeFrame(typ, seq, j)
	if err != nil {
		return err
	}
	n, err := c.Write(f)
	if err == nil && n != len(f) {
		err = fmt.Errorf("short write %d of %d", n, len(f))
	}
	return err
}

// Reply bodies.
type putAnswer struct {
	Accept bool   `json:"accept"`
	Name   string `json:"name"`
	Reason string `json:"reason,omitempty"`
}

type putEnd struct {
	OK     bool        `json:"ok"`
	Name   string      `json:"name"`
	SHA256 string      `json:"sha256,omitempty"`
	Reason string      `json:"reason,omitempty"`
	Next   []ReadyFile `json:"next,omitempty"`
}

type commitAnswer struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
	Auto   bool   `json:"auto"`
}

// room is the largest file the receiver takes now.
func (r *Receiver) room() int64 {
	m := r.cfg.MemAvailable() - r.cfg.Reserve
	if f := r.cfg.Free(r.cfg.Dir); f < m {
		m = f
	}
	if m < 0 {
		m = 0
	}
	return m
}

// decide says whether a file is accepted, before any byte of it is sent:
// the manifest first; then only the two files it names, once each, with its
// sizes; never more than the free RAM.
func (s *session) decide(p PutBegin) string {
	r := s.r
	if r.cfg.StatusOnly {
		return "status only: the payload is already sealed"
	}
	if r.Complete() {
		return "the payload is complete and waits for the seal"
	}
	switch p.Name {
	case ManifestName:
		if s.want != nil {
			return "the manifest of this session is already received"
		}
		if len(r.pins) == 0 {
			return "no payload is pinned by the step-2 gate: every manifest is refused"
		}
		if p.Size <= 0 || p.Size > MaxManifest {
			return fmt.Sprintf("manifest of %d bytes (1 to %d)", p.Size, MaxManifest)
		}
	case TemplateName, RootfsName:
		if s.want == nil {
			return "manifest first"
		}
		if s.got[p.Name] {
			return p.Name + " is already received"
		}
		if w := s.want[p.Name]; p.Size != w.Size {
			return fmt.Sprintf("%s: %d bytes, the manifest says %d", p.Name, p.Size, w.Size)
		}
	default:
		return "not a file of the payload"
	}
	if room := r.room(); p.Size > room {
		return fmt.Sprintf("not enough free RAM: %d bytes needed, %d free", p.Size, room)
	}
	return ""
}

// put answers a PUT_BEGIN and, when it accepts it, receives exactly p.Size
// bytes and answers PUT_END. ok false: the session ends with why.
func (s *session) put(c Conn, h Header, p PutBegin) (why string, k endKind, ok bool) {
	if reason := s.decide(p); reason != "" {
		if err := s.reply(c, TypePutBegin|TypeReply, h.Seq, putAnswer{Name: p.Name, Reason: reason}); err != nil {
			return "reply: " + err.Error(), endLink, false
		}
		return "", 0, true
	}
	if err := s.reply(c, TypePutBegin|TypeReply, h.Seq, putAnswer{Accept: true, Name: p.Name}); err != nil {
		return "reply: " + err.Error(), endLink, false
	}
	res, err := s.receive(c, p)
	switch {
	case errors.Is(err, errEnded):
		return "ended by the idle timer", endIdle, false
	case errors.Is(err, errZero), errors.Is(err, errStaleData):
		return err.Error(), endViolation, false
	case err != nil:
		return err.Error(), endLink, false
	}
	if err := s.reply(c, TypePutEnd|TypeReply, h.Seq, res); err != nil {
		return "reply: " + err.Error(), endLink, false
	}
	return "", 0, true
}

// open makes the session's folder (once) and the file name in it.
func (s *session) open(name string) (*os.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dead {
		return nil, errEnded
	}
	if !s.made {
		if err := os.Mkdir(s.dir, 0o700); err != nil {
			return nil, err
		}
		s.made = true
	}
	p := filepath.Join(s.dir, name)
	os.Remove(p)
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	s.f = f
	return f, nil
}

// closeFile closes the file being received; remove drops it.
func (s *session) closeFile(name string, remove bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	if s.f != nil {
		err = s.f.Close()
		s.f = nil
	}
	if remove && s.made {
		os.Remove(filepath.Join(s.dir, name))
	}
	return err
}

// write writes b into the file being received unless the session was ended.
func (s *session) write(b []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dead {
		return errEnded
	}
	if s.f == nil {
		return errors.New("no open file")
	}
	_, err := s.f.Write(b)
	return err
}

func (s *session) setInPut(v bool) {
	s.mu.Lock()
	s.inPut = v
	s.mu.Unlock()
}

// receive reads the data of an accepted PUT: reads of exactly min(DataChunk,
// bytes left) (prototype finding 1), the sha256 while the bytes arrive. A link
// error is returned as an error (the session ends); everything else is a
// PUT_END answer, after all size bytes were read, so the link stays in step.
func (s *session) receive(c Conn, p PutBegin) (putEnd, error) {
	r := s.r
	res := putEnd{Name: p.Name}
	s.setInPut(true)
	defer s.setInPut(false)
	var (
		hsum = sha256.New()
		mbuf []byte // the manifest in memory
		werr error
	)
	if p.Name == ManifestName {
		mbuf = make([]byte, 0, p.Size)
	} else if _, err := s.open(p.Name); err != nil {
		if errors.Is(err, errEnded) {
			return res, err
		}
		werr = err
	}
	r.setState(func(st *State) {
		if st.State == StateWaiting || st.State == StateReceiving {
			st.State, st.File, st.Bytes, st.Total = StateReceiving, p.Name, 0, p.Size
			if s.want == nil {
				st.Detail = "receiving the manifest"
			}
		}
	})
	buf := make([]byte, DataChunk)
	left, zeros, nextPub := p.Size, 0, int64(32<<20)
	for left > 0 {
		want := int64(DataChunk)
		if left < want {
			want = left
		}
		k, err := s.read(c, buf[:want])
		if s.isDead() {
			return res, errEnded
		}
		if err != nil {
			s.closeFile(p.Name, true)
			return res, fmt.Errorf("link during %s: %w", p.Name, err)
		}
		if k == 0 {
			if zeros++; zeros > maxZeroReads {
				s.closeFile(p.Name, true)
				return res, errZero
			}
			continue
		}
		zeros = 0
		s.touch()
		d := buf[:k]
		left -= int64(k)
		hsum.Write(d)
		switch {
		case mbuf != nil:
			mbuf = append(mbuf, d...)
		case werr == nil:
			if err := s.write(d); err != nil {
				if errors.Is(err, errEnded) {
					return res, err
				}
				werr = err
			}
		}
		if done := p.Size - left; done >= nextPub || left == 0 {
			nextPub = done + 32<<20
			r.setState(func(st *State) {
				if st.State == StateReceiving && st.File == p.Name {
					st.Bytes = done
				}
			})
		}
	}
	sum := hex.EncodeToString(hsum.Sum(nil))
	res.SHA256 = sum
	if p.Name == ManifestName {
		return s.takeManifest(res, mbuf, sum)
	}
	cerr := s.closeFile(p.Name, false)
	if werr == nil {
		werr = cerr
	}
	if werr != nil {
		s.closeFile(p.Name, true)
		res.Reason = "the RAM folder: " + werr.Error()
		return res, nil
	}
	if w := s.want[p.Name]; sum != w.SHA256 {
		s.closeFile(p.Name, true)
		res.Reason = fmt.Sprintf("sha256 %s is not the manifest's %s", sum, w.SHA256)
		return res, nil
	}
	s.got[p.Name] = true
	res.OK = true
	s.r.setState(func(st *State) {
		if st.State == StateReceiving {
			st.Detail = fmt.Sprintf("%d of 3 files received", len(s.got))
		}
	})
	return res, nil
}

// takeManifest checks a received manifest: its sha256 is pinned, it parses
// (mainline.ParseManifest, the parser of pass 1); then it is written to the
// folder and names the two files the session takes.
func (s *session) takeManifest(res putEnd, b []byte, sum string) (putEnd, error) {
	if !pinned(s.r.pins, sum) {
		res.Reason = fmt.Sprintf("manifest sha256 %s is not one of the %d payloads that the step-2 gate pins", sum, len(s.r.pins))
		return res, nil
	}
	m, err := mainline.ParseManifest(b)
	if err != nil {
		res.Reason = err.Error()
		return res, nil
	}
	if _, err := s.open(ManifestName); err != nil {
		if errors.Is(err, errEnded) {
			return res, err
		}
		res.Reason = "the RAM folder: " + err.Error()
		return res, nil
	}
	werr := s.write(b)
	if errors.Is(werr, errEnded) {
		return res, werr
	}
	if cerr := s.closeFile(ManifestName, false); werr == nil {
		werr = cerr
	}
	if werr != nil {
		s.closeFile(ManifestName, true)
		res.Reason = "the RAM folder: " + werr.Error()
		return res, nil
	}
	s.manifestSHA = sum
	s.want = map[string]ReadyFile{
		TemplateName: {Name: TemplateName, Size: m.BootTemplate.Size, SHA256: m.BootTemplate.SHA256},
		RootfsName:   {Name: RootfsName, Size: m.Rootfs.Size, SHA256: m.Rootfs.SHA256},
	}
	s.got[ManifestName] = true
	res.OK = true
	res.Next = []ReadyFile{s.want[TemplateName], s.want[RootfsName]}
	s.r.setState(func(st *State) {
		st.State, st.ManifestSHA, st.Detail = StateReceiving, sum, "1 of 3 files received"
	})
	return res, nil
}

// commit hands the complete, checked folder over: the record first, then one
// rename of incoming.<n> to the ready folder.
func (s *session) commit(cm Commit) commitAnswer {
	r := s.r
	a := commitAnswer{Auto: cm.Auto}
	switch {
	case r.cfg.StatusOnly:
		a.Reason = "status only: the payload is already sealed"
		return a
	case r.Complete():
		a.Reason = "the payload is already complete"
		return a
	case s.want == nil || !s.got[ManifestName] || !s.got[TemplateName] || !s.got[RootfsName]:
		a.Reason = fmt.Sprintf("incomplete: %d of 3 files received", len(s.got))
		return a
	}
	rec := Record{Version: RecordVersion, ManifestSHA256: s.manifestSHA, Auto: cm.Auto, Session: s.n, Time: r.cfg.Now().Unix(),
		Files: []ReadyFile{{Name: ManifestName, SHA256: s.manifestSHA}, s.want[TemplateName], s.want[RootfsName]}}
	if fi, err := os.Lstat(filepath.Join(s.dir, ManifestName)); err == nil {
		rec.Files[0].Size = fi.Size()
	}
	b, _ := json.MarshalIndent(rec, "", " ")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dead {
		a.Reason = "the session was ended"
		return a
	}
	if err := writeAtomic(r.cfg.Dir, ReadyRecord, append(b, '\n')); err != nil {
		a.Reason = "record: " + err.Error()
		return a
	}
	if err := os.Rename(s.dir, filepath.Join(r.cfg.Dir, ReadyDir)); err != nil {
		os.Remove(filepath.Join(r.cfg.Dir, ReadyRecord))
		a.Reason = "ready folder: " + err.Error()
		return a
	}
	s.committed, s.made = true, false
	r.mu.Lock()
	r.complete = true
	r.st = State{State: StateComplete, Session: s.n, ManifestSHA: s.manifestSHA, Detail: "complete: the step-2 service seals the payload"}
	if cm.Auto {
		r.st.Detail += " (the person chose: continue by itself)"
	}
	r.publish()
	r.mu.Unlock()
	r.cfg.Logf("COMMIT: session %d, manifest %s, auto %v", s.n, s.manifestSHA, cm.Auto)
	a.OK = true
	return a
}

// Hello is the HELLO reply. It never holds a unit serial, a MAC, a key byte or
// a factory byte (STEP2-USB-TRANSPORT §3.3): only the receiver's state and the
// filtered fields of the service's info.
type Hello struct {
	Proto        int      `json:"proto"`
	USB          string   `json:"usb"`
	State        string   `json:"state"`
	Detail       string   `json:"detail,omitempty"`
	StatusOnly   bool     `json:"status_only"`
	Session      int      `json:"session"`
	Pins         []string `json:"pins"`
	MemAvailable int64    `json:"mem_available"`
	Room         int64    `json:"room"`
	Info         *Info    `json:"info,omitempty"`
}

// Status is the STATUS reply.
type Status struct {
	State
	StatusOnly bool  `json:"status_only"`
	Info       *Info `json:"info,omitempty"`
}

func (r *Receiver) hello() Hello {
	st := r.Snapshot()
	h := Hello{Proto: ProtoVersion, USB: fmt.Sprintf("%04x:%04x", USBVendorID, USBProductID), State: st.State, Detail: st.Detail,
		StatusOnly: r.cfg.StatusOnly, Session: st.Session, Pins: append([]string{}, r.pins...),
		MemAvailable: r.cfg.MemAvailable(), Info: ReadInfo(r.cfg.InfoFile)}
	if !r.cfg.StatusOnly {
		h.Room = r.room()
	}
	return h
}

func (r *Receiver) status() Status {
	return Status{State: r.Snapshot(), StatusOnly: r.cfg.StatusOnly, Info: ReadInfo(r.cfg.InfoFile)}
}

// Info is what the root step-2 service tells the page through the receiver
// (the file Config.InfoFile, root-written). Each field is filtered: only these
// keys, each with a strict form.
type Info struct {
	Model  string   `json:"model,omitempty"`
	Slot   string   `json:"slot,omitempty"`
	Token  string   `json:"token,omitempty"`
	Step2  string   `json:"step2,omitempty"`
	PlanID string   `json:"plan_id,omitempty"`
	Lines  []string `json:"lines,omitempty"`
}

var (
	infoModel = regexp.MustCompile(`^[A-Z0-9-]{1,16}$`)
	infoSlot  = regexp.MustCompile(`^[ab]$`)
	infoToken = regexp.MustCompile(`^[A-Za-z0-9._-]{1,15}$`)
	infoStep2 = regexp.MustCompile(`^[a-z0-9][a-z0-9 :._-]{0,63}$`)
	infoPlan  = regexp.MustCompile(`^[0-9a-f]{12}$`)
	infoLine  = regexp.MustCompile(`^[\x20-\x7e]{0,200}$`)
)

const maxInfo = 16 << 10

// ReadInfo reads and filters the service's info file ("" or a bad file = nil).
func ReadInfo(p string) *Info {
	if p == "" {
		return nil
	}
	b, err := readSmall(p, maxInfo)
	if err != nil {
		return nil
	}
	var in Info
	if json.Unmarshal(b, &in) != nil {
		return nil
	}
	keep := func(s string, rx *regexp.Regexp) string {
		if rx.MatchString(s) {
			return s
		}
		return ""
	}
	out := &Info{Model: keep(in.Model, infoModel), Slot: keep(in.Slot, infoSlot), Token: keep(in.Token, infoToken),
		Step2: keep(in.Step2, infoStep2), PlanID: keep(in.PlanID, infoPlan)}
	for i, l := range in.Lines {
		if i >= 40 {
			break
		}
		if infoLine.MatchString(l) {
			out.Lines = append(out.Lines, l)
		}
	}
	return out
}

// MemAvailable is MemAvailable of /proc/meminfo in bytes (0 when unknown, so
// every file is refused).
func MemAvailable() int64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "MemAvailable:") {
			f := strings.Fields(l)
			if len(f) >= 2 {
				n, err := strconv.ParseInt(f[1], 10, 64)
				if err == nil && len(f) == 3 && f[2] == "kB" {
					return n * 1024
				}
			}
		}
	}
	return 0
}

// FreeBytes is the free space of the file system of dir (0 when unknown).
func FreeBytes(dir string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0
	}
	return int64(st.Bavail) * int64(st.Bsize)
}
