package xfer

// The kind provision (mainline-rc-10 R23; MAINLINE-RC10-2026-11-15 §6.10):
// the slot's USB interface while the root provision service (uc-pxx-provision)
// installs the chosen profile on an R23 base slot.
//
//	HELLO   -> mode "provision", the merged state, profiles [{name, file, size,
//	           sha256}] (null file, size, sha256 for console), profile
//	COMMIT  {profile, source: "net"|"cable", url?} -> {ok, reason?}: the choice;
//	           taken in waiting, retry-wait and failed; written as provision.json
//	           for the root service, which acts on it
//	PUT     after a COMMIT with source "cable", in the same session: the
//	           pinned bundle file (its name and size), unpacked into the session
//	           folder as it arrives (internal/provision/bundle); its PUT_END
//	           {ok, sha256} is the hand-over point (complete.json, complete/)
//	STATUS  -> state, done, total, fail, reason, next_try_s, profile
//
// The receiver runs as its own user (no root, no eMMC code): it writes only
// into its folder of the stage. The root service stops it before it takes a
// bundle over and checks every member again as root.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"uc-pxx-installer/internal/provision/bundle"
	"uc-pxx-installer/internal/xfer/handover"
)

// KindProvision is the kind of the provisioning (Config.Kind).
const KindProvision = "provision"

// stateReceived is the receiver's own state after the hand-over of a cable
// bundle (STATUS says "receiving" with done = total until the root takes it).
const stateReceived = "received"

func provisionSet() set {
	return set{kind: KindProvision, waiting: handover.PStateWaiting, complete: stateReceived}
}

// checkProvisionConfig checks the provision fields of cfg (NewReceiver).
func checkProvisionConfig(cfg Config) error {
	seen := map[string]bool{}
	for _, p := range cfg.ProvProfiles {
		if err := p.Check(); err != nil {
			return err
		}
		if seen[p.Name] {
			return fmt.Errorf("profile %s twice", p.Name)
		}
		seen[p.Name] = true
	}
	switch {
	case cfg.Fixed != "" && !seen[cfg.Fixed]:
		return fmt.Errorf("the fixed profile %q is not one of the unit's profiles", cfg.Fixed)
	case len(cfg.Pins) > 0:
		return errors.New("a provision receiver takes no pin (its profiles are its pins)")
	case cfg.Seq < 0:
		return fmt.Errorf("seq %d", cfg.Seq)
	}
	return nil
}

func (r *Receiver) provProfile(name string) (handover.ProvisionProfile, bool) {
	for _, p := range r.cfg.ProvProfiles {
		if p.Name == name {
			return p, true
		}
	}
	return handover.ProvisionProfile{}, false
}

// provState is the state of HELLO and STATUS: the receiver's own while it
// receives (or holds a received bundle), else the root's progress file.
func (r *Receiver) provState() handover.Progress {
	st := r.Snapshot()
	switch {
	case st.State == StateReceiving:
		return handover.Progress{State: handover.PStateReceiving, Done: st.Bytes, Total: st.Total, Updated: st.Updated}
	case r.Complete():
		return handover.Progress{State: handover.PStateReceiving, Done: st.Total, Total: st.Total, Updated: st.Updated,
			Reason: "the bundle was received: the unit checks it"}
	}
	if p := handover.ReadProgress(r.cfg.ProgressFile); p != nil {
		return *p
	}
	return handover.Progress{State: handover.PStateWaiting, Updated: st.Updated}
}

// provProfileNow is HELLO's "profile": the fixed one, else the profile of
// the last COMMIT this receiver took, else the root's.
func (r *Receiver) provProfileNow(pr handover.Progress) string {
	r.mu.Lock()
	last := r.lastProfile
	r.mu.Unlock()
	switch {
	case r.cfg.Fixed != "":
		return r.cfg.Fixed
	case last != "":
		return last
	}
	return pr.Profile
}

// ProfileView is one profile of HELLO (null file, size and sha256: console).
type ProfileView struct {
	Name   string  `json:"name"`
	File   *string `json:"file"`
	Size   *int64  `json:"size"`
	SHA256 *string `json:"sha256"`
}

// ProvisionHello is the HELLO reply of the kind provision.
type ProvisionHello struct {
	Proto        int           `json:"proto"`
	USB          string        `json:"usb"`
	Mode         string        `json:"mode"`
	State        string        `json:"state"`
	Detail       string        `json:"detail,omitempty"`
	StatusOnly   bool          `json:"status_only"`
	Session      int           `json:"session"`
	Pins         []string      `json:"pins"`
	MemAvailable int64         `json:"mem_available"`
	Room         int64         `json:"room"`
	Info         *Info         `json:"info,omitempty"`
	Profiles     []ProfileView `json:"profiles"`
	Profile      string        `json:"profile"`
}

// ProvisionStatus is the STATUS reply of the kind provision.
type ProvisionStatus struct {
	State      string `json:"state"`
	Detail     string `json:"detail,omitempty"`
	Session    int    `json:"session"`
	Done       int64  `json:"done"`
	Total      int64  `json:"total"`
	Fail       string `json:"fail,omitempty"`
	Reason     string `json:"reason,omitempty"`
	NextTryS   int    `json:"next_try_s,omitempty"`
	Profile    string `json:"profile"`
	Updated    int64  `json:"updated"`
	Mode       string `json:"mode"`
	StatusOnly bool   `json:"status_only"`
	Info       *Info  `json:"info,omitempty"`
}

func (r *Receiver) provHello() ProvisionHello {
	st := r.Snapshot()
	pr := r.provState()
	h := ProvisionHello{Proto: ProtoVersion, USB: fmt.Sprintf("%04x:%04x", USBVendorID, USBProductID), Mode: KindProvision,
		State: pr.State, Detail: st.Detail, StatusOnly: r.cfg.StatusOnly, Session: st.Session, Pins: []string{},
		MemAvailable: r.cfg.MemAvailable(), Info: ReadInfo(r.cfg.InfoFile), Profiles: []ProfileView{}, Profile: r.provProfileNow(pr)}
	if !r.cfg.StatusOnly {
		h.Room = r.room()
	}
	for _, p := range r.cfg.ProvProfiles {
		v := ProfileView{Name: p.Name}
		if p.Name != handover.ProfileConsole {
			f, n, s := p.File, p.Size, p.SHA256
			v.File, v.Size, v.SHA256 = &f, &n, &s
		}
		h.Profiles = append(h.Profiles, v)
	}
	return h
}

func (r *Receiver) provStatus() ProvisionStatus {
	st := r.Snapshot()
	pr := r.provState()
	return ProvisionStatus{State: pr.State, Detail: st.Detail, Session: st.Session, Done: pr.Done, Total: pr.Total, Fail: pr.Fail,
		Reason: pr.Reason, NextTryS: pr.NextTryS, Profile: r.provProfileNow(pr), Updated: pr.Updated, Mode: KindProvision,
		StatusOnly: r.cfg.StatusOnly, Info: ReadInfo(r.cfg.InfoFile)}
}

// provisionAnswer is the COMMIT reply of the kind provision.
type provisionAnswer struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

// provisionCommit takes the choice of the page: the checks of §6.10, then
// provision.json (the root acts on it). A cable choice lets this session
// take one PUT of the profile's bundle.
func (s *session) provisionCommit(body []byte) (any, string) {
	r := s.r
	var c struct {
		Profile string  `json:"profile"`
		Source  string  `json:"source"`
		URL     *string `json:"url"`
	}
	if err := decodeStrict(body, &c); err != nil {
		return nil, "COMMIT: " + err.Error()
	}
	a := provisionAnswer{}
	refuse := func(f string, x ...any) (any, string) {
		a.Reason = fmt.Sprintf(f, x...)
		r.cfg.Logf("COMMIT refused: %s", a.Reason)
		return a, ""
	}
	pr := r.provState()
	p, known := r.provProfile(c.Profile)
	switch {
	case r.cfg.StatusOnly:
		return refuse("status only: the unit takes no choice now (%s)", pr.State)
	case r.Complete():
		return refuse("the bundle was received: the unit checks it")
	case pr.State != handover.PStateWaiting && pr.State != handover.PStateRetryWait && pr.State != handover.PStateFailed:
		return refuse("the unit works now (%s): read STATUS", pr.State)
	case !known:
		return refuse("profile %q is not one of this unit's profiles", c.Profile)
	case r.cfg.Fixed != "" && c.Profile != r.cfg.Fixed:
		return refuse("the profile is fixed: %s", r.cfg.Fixed)
	case c.Source != handover.SourceNet && c.Source != handover.SourceCable:
		return refuse("source %q (net or cable)", c.Source)
	case c.URL != nil && c.Source != handover.SourceNet:
		return refuse("a url only with the source net")
	case c.Profile == handover.ProfileConsole && c.Source != handover.SourceNet:
		return refuse("the console profile has no bundle: source net (nothing is fetched)")
	case c.Profile == handover.ProfileConsole && !r.cfg.ConsoleKey:
		return refuse("no root ssh key was seeded in step 1: the console profile has no other way in")
	}
	url := ""
	if c.URL != nil {
		if err := handover.CheckBaseURL(*c.URL); err != nil {
			return refuse("%v", err)
		}
		url = *c.URL
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dead {
		return refuse("the session was ended")
	}
	r.mu.Lock()
	seq := r.seq + 1
	r.mu.Unlock()
	rec := handover.Choice{Version: RecordVersion, Kind: handover.RecordKindChoice, Seq: seq, Profile: p.Name, Source: c.Source, URL: url,
		Session: s.n, Time: r.cfg.Now().Unix()}
	b, _ := json.MarshalIndent(rec, "", " ")
	if err := writeAtomic(r.cfg.Dir, handover.ChoiceRecord, append(b, '\n')); err != nil {
		return refuse("record: %v", err)
	}
	r.mu.Lock()
	r.seq, r.lastProfile = seq, p.Name
	r.mu.Unlock()
	s.provCable = ""
	if c.Source == handover.SourceCable {
		s.provCable = p.Name
	}
	r.cfg.Logf("COMMIT: session %d, choice %d: profile %s, source %s, url %q", s.n, seq, p.Name, c.Source, url)
	a.OK = true
	return a, ""
}

// decideProvision says whether the PUT of a cable bundle is taken, before
// any byte of it is sent.
func (s *session) decideProvision(p PutBegin) string {
	r := s.r
	switch {
	case r.cfg.StatusOnly:
		return "status only: nothing is received now"
	case r.Complete():
		return "the bundle was received already: the unit checks it"
	case s.provCable == "":
		return "a PUT needs a COMMIT with the source cable in this session"
	case s.provGot:
		return "the bundle of this session was sent already"
	}
	pp, _ := r.provProfile(s.provCable)
	if p.Name != pp.File || p.Size != pp.Size {
		return fmt.Sprintf("%s of %d bytes is not the pinned bundle of %s: %s of %d bytes", p.Name, p.Size, pp.Name, pp.File, pp.Size)
	}
	if room := r.room(); p.Size > room {
		return fmt.Sprintf("not enough free RAM: %d bytes needed, %d free", p.Size, room)
	}
	return ""
}

// putProvision answers a PUT_BEGIN of the kind provision and receives the
// bundle when it is taken.
func (s *session) putProvision(c Conn, h Header, p PutBegin) (why string, k endKind, ok bool) {
	if reason := s.decideProvision(p); reason != "" {
		if err := s.reply(c, TypePutBegin|TypeReply, h.Seq, putAnswer{Name: p.Name, Reason: reason}); err != nil {
			return "reply: " + err.Error(), endLink, false
		}
		return "", 0, true
	}
	if err := s.reply(c, TypePutBegin|TypeReply, h.Seq, putAnswer{Accept: true, Name: p.Name}); err != nil {
		return "reply: " + err.Error(), endLink, false
	}
	res, err := s.receiveProvision(c, p)
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
	if res.OK && s.r.cfg.Committed != nil {
		s.r.cfg.Committed() // after the PUT_END reply: the root takes the bundle over
	}
	return "", 0, true
}

// sessionSink writes the members into the session folder (the idle timer
// can end the session: every write goes through the session's checks).
type sessionSink struct{ s *session }

type sessionFile struct{ s *session }

func (f sessionFile) Write(b []byte) (int, error) {
	if err := f.s.write(b); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (f sessionFile) Close() error { return f.s.closeFile("", false) }

func memberPath(dir, p string) (string, error) {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return "", fmt.Errorf("member path %q", p)
	}
	for _, c := range strings.Split(p, "/") {
		if c == "" || c == "." || c == ".." {
			return "", fmt.Errorf("member path %q", p)
		}
	}
	return filepath.Join(dir, filepath.FromSlash(p)), nil
}

func (k sessionSink) Create(p string, size int64) (io.WriteCloser, error) {
	s := k.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dead {
		return nil, errEnded
	}
	full, err := memberPath(s.dir, p)
	if err != nil {
		return nil, err
	}
	if !s.made {
		if err := os.Mkdir(s.dir, 0o700); err != nil {
			return nil, err
		}
		s.made = true
	}
	// the folders of the path, each a new folder of this session (no link)
	d := s.dir
	for _, c := range strings.Split(filepath.Dir(filepath.FromSlash(p)), string(filepath.Separator)) {
		if c == "." {
			break
		}
		d = filepath.Join(d, c)
		if err := os.Mkdir(d, 0o700); err != nil && !os.IsExist(err) {
			return nil, err
		}
		if fi, err := os.Lstat(d); err != nil || !fi.IsDir() {
			return nil, fmt.Errorf("%s is not a folder", d)
		}
	}
	f, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	s.f = f
	return sessionFile{s}, nil
}

func (k sessionSink) Remove(p string) error {
	full, err := memberPath(k.s.dir, p)
	if err != nil {
		return err
	}
	return os.Remove(full)
}

// receiveProvision reads the data of the accepted bundle PUT (reads of
// exactly min(DataChunk, bytes left)), feeds the unpacker and hashes the
// stream; after all bytes it answers PUT_END. A bundle that is not the pin
// leaves nothing: the session folder is removed.
func (s *session) receiveProvision(c Conn, p PutBegin) (putEnd, error) {
	r := s.r
	res := putEnd{Name: p.Name}
	s.setInPut(true)
	defer s.setInPut(false)
	pp, _ := r.provProfile(s.provCable)
	s.provGot = true
	// bundle.json is pinned by its sha256 (index_sha256): the receiver needs
	// no more of profiles.json; the root checks it against profiles.json
	// again (bundle.Expect) after the hand-over
	u, uerr := bundle.NewUnpacker(bundle.Pins{Size: pp.Size, SHA256: pp.SHA256, IndexSHA256: pp.IndexSHA256}, sessionSink{s})
	hsum := sha256.New()
	r.setState(func(st *State) {
		st.State, st.File, st.Bytes, st.Total, st.Detail = StateReceiving, p.Name, 0, p.Size, "receiving the bundle of "+pp.Name
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
			s.dropSessionDir()
			return res, fmt.Errorf("link during %s: %w", p.Name, err)
		}
		if k == 0 {
			if zeros++; zeros > maxZeroReads {
				s.dropSessionDir()
				return res, errZero
			}
			continue
		}
		zeros = 0
		s.touch()
		left -= int64(k)
		hsum.Write(buf[:k])
		if uerr == nil {
			if _, err := u.Write(buf[:k]); err != nil {
				if errors.Is(err, errEnded) {
					return res, errEnded
				}
				uerr = err
			}
		}
		if done := p.Size - left; done >= nextPub || left == 0 {
			nextPub = done + 32<<20
			r.setState(func(st *State) {
				if st.State == StateReceiving {
					st.Bytes = done
				}
			})
		}
	}
	res.SHA256 = hex.EncodeToString(hsum.Sum(nil))
	if uerr == nil {
		uerr = u.Finish()
	}
	if uerr == nil && res.SHA256 != pp.SHA256 {
		uerr = fmt.Errorf("sha256 %s is not the pin %s", res.SHA256, pp.SHA256)
	}
	if uerr != nil {
		s.dropSessionDir()
		res.Reason = fmt.Sprintf("%s: %v", p.Name, uerr)
		r.setState(func(st *State) {
			st.State, st.File, st.Bytes, st.Total, st.Detail = r.set.waiting, "", 0, 0, "the bundle was refused: "+uerr.Error()
		})
		s.provGot = false // the page may send it again in this session
		return res, nil
	}
	if why := s.handOver(pp); why != "" {
		s.dropSessionDir()
		res.Reason = why
		s.provGot = false
		return res, nil
	}
	res.OK = true
	return res, nil
}

// dropSessionDir removes the session folder (a refused or cut bundle).
func (s *session) dropSessionDir() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f != nil {
		s.f.Close()
		s.f = nil
	}
	if s.made {
		os.RemoveAll(s.dir)
		s.made = false
	}
}

// handOver writes complete.json, then renames the session folder to
// complete/: the hand-over point of a cable bundle.
func (s *session) handOver(pp handover.ProvisionProfile) string {
	r := s.r
	rec := Record{Version: RecordVersion, Kind: handover.RecordKindProvision, Profile: pp.Name, ManifestSHA256: pp.SHA256,
		Files: []ReadyFile{{Name: pp.File, Size: pp.Size, SHA256: pp.SHA256}}, Session: s.n, Time: r.cfg.Now().Unix()}
	b, _ := json.MarshalIndent(rec, "", " ")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dead {
		return "the session was ended"
	}
	if err := writeAtomic(r.cfg.Dir, ReadyRecord, append(b, '\n')); err != nil {
		return "record: " + err.Error()
	}
	if err := os.Rename(s.dir, filepath.Join(r.cfg.Dir, ReadyDir)); err != nil {
		os.Remove(filepath.Join(r.cfg.Dir, ReadyRecord))
		return "ready folder: " + err.Error()
	}
	s.committed, s.made = true, false
	r.mu.Lock()
	r.complete = true
	r.st = State{State: stateReceived, Session: s.n, ManifestSHA: pp.SHA256, File: pp.File, Bytes: pp.Size, Total: pp.Size,
		Detail: "the bundle of " + pp.Name + " was received: the root service checks it"}
	r.publish()
	r.mu.Unlock()
	r.cfg.Logf("PUT_END: session %d, the bundle of %s (%s) was received and handed over", s.n, pp.Name, pp.SHA256)
	return ""
}
