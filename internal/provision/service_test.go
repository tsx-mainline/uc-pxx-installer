package provision

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"uc-pxx-installer/internal/clone"
	"uc-pxx-installer/internal/provision/bundle"
	"uc-pxx-installer/internal/provision/bundle/bundletest"
	"uc-pxx-installer/internal/xfer/handover"
)

const testRelease = "0.2.0-rc10"

var testExclude = []string{"/run", "/tmp", "/var/log", "/var/cache", "/var/lib/*", "!/var/lib/tsx", "/root", "/data", "/lib/apk/db/lock",
	"/usr/share/uc-pxx-provision/profiles.json"}

// unit is a fake unit: a root folder with a base, the fakes of Env, and an
// event log.
type unit struct {
	t      *testing.T
	root   string
	b      *bundletest.Bundle
	gate   *clone.Gate
	rel    *clone.ReleaseData
	ps     *Profiles
	env    *Env
	mu     sync.Mutex
	now    time.Time
	events []string
	sleeps []time.Duration
	// behaviours
	marked     bool
	lockBusy   int // Locks refuses this many times
	apkFail    bool
	applyExtra bool // the profile script writes a file that ref_b does not have
	// the health gate (mainline-rc-10b): services that do not read started,
	// and a profile script that makes no runlevel link
	svcDown map[string]bool
	noLinks bool
	usb     *fakeUSB
	fetch   *fakeFetch
}

func (u *unit) ev(f string, a ...any) {
	u.mu.Lock()
	u.events = append(u.events, fmt.Sprintf(f, a...))
	u.mu.Unlock()
}

func (u *unit) has(prefix string) int {
	for i, e := range u.events {
		if strings.HasPrefix(e, prefix) {
			return i
		}
	}
	return -1
}

func writeFile(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// installedDB is an apk installed database of these name=version lines.
func installedDB(lines []string) []byte {
	sort.Strings(lines)
	var b bytes.Buffer
	for _, l := range lines {
		n, v, _ := strings.Cut(l, "=")
		fmt.Fprintf(&b, "C:Q1x=\nP:%s\nV:%s\nA:aarch64\n\n", n, v)
	}
	return b.Bytes()
}

// base writes the base tree of the fake unit into root.
func baseTree(t *testing.T, root string, b *bundletest.Bundle) {
	writeFile(t, filepath.Join(root, "etc/tsx/build-id"), []byte("0.2.0-rc10-base\n"))
	writeFile(t, filepath.Join(root, "bin/busybox"), []byte("busybox"))
	writeFile(t, filepath.Join(root, ApplyScript), []byte("#!/bin/sh\n"))
	writeFile(t, filepath.Join(root, InstalledDB), installedDB(append([]string(nil), b.DocV.Base...)))
	writeFile(t, filepath.Join(root, "etc/apk/world"), []byte("busybox\n"))
	writeFile(t, filepath.Join(root, ConfFile), []byte("UC_PXX_PROVISION_PROFILES="+ProfilesFile+"\nUC_PXX_PROVISION_APPLY="+ApplyScript+"\nUC_PXX_PROVISION_USB=yes\n"))
	os.MkdirAll(filepath.Join(root, "var/lib/tsx"), 0o755)
	os.MkdirAll(filepath.Join(root, "run"), 0o755)
}

// fakeInstall is what apk add and the profile script do on the fake unit.
func fakeInstall(t *testing.T, root string, b *bundletest.Bundle, extra bool) {
	lines := append([]string(nil), b.DocV.Base...)
	for _, p := range b.DocV.Packages {
		lines = append(lines, p.Name+"="+p.Version)
		writeFile(t, filepath.Join(root, "usr/bin", p.Name), []byte(p.Name+" "+p.Version))
	}
	writeFile(t, filepath.Join(root, InstalledDB), installedDB(lines))
	writeFile(t, filepath.Join(root, "etc/apk/world"), []byte(strings.Join(b.DocV.World, "\n")+"\n"))
	writeFile(t, filepath.Join(root, "lib/apk/db/lock"), nil)
	writeFile(t, filepath.Join(root, "var/log/apk.log"), []byte("a time\n"))
	writeFile(t, filepath.Join(root, "etc/tsx/profile"), []byte("kiosk\n"))
	if extra {
		writeFile(t, filepath.Join(root, "etc/extra"), []byte("x"))
	}
}

func newUnit(t *testing.T, mod func(o *bundletest.Options)) *unit {
	o := bundletest.Options{Release: testRelease, Profile: "kiosk", Base: []string{"busybox=1.37.0-r20", "musl=1.2.5-r10", "apk-tools=3.0.9-r0"},
		Pkgs: []bundletest.Pkg{
			{Name: "tsx-kiosk", Version: "0.3.13_pre20261113-r0", Repo: "common", Data: bytes.Repeat([]byte("k"), 3000), Installed: 40960},
			{Name: "chromium", Version: "142.0-r0", Repo: "community", Data: bytes.Repeat([]byte("c"), 2<<20), Installed: 400000},
			{Name: "uc-pxx-kiosk", Version: "0.2.0-r0", Repo: "uc-pxx", Data: bytes.Repeat([]byte("u"), 513), Installed: 4096},
		},
		Add: []string{"tsx-kiosk", "uc-pxx-kiosk"}, World: []string{"tsx-kiosk", "uc-pxx-kiosk", "busybox"}, RecordPad: true}
	if mod != nil {
		mod(&o)
	}
	b := bundletest.Make(o)
	u := &unit{t: t, root: t.TempDir(), b: b, now: time.Unix(1_800_000_000, 0), marked: true}
	baseTree(t, u.root, b)
	// ref_b: the base plus the fake install, in a twin root
	twin := t.TempDir()
	baseTree(t, twin, b)
	consoleRef, err := clone.LiveContent(twin, testExclude)
	if err != nil {
		t.Fatal(err)
	}
	_, consoleInstSHA, _ := readInstalled(filepath.Join(twin, InstalledDB))
	fakeInstall(t, twin, b, false)
	ref, err := clone.LiveContent(twin, testExclude)
	if err != nil {
		t.Fatal(err)
	}
	_, inst, _ := readInstalled(filepath.Join(twin, InstalledDB))
	g := clone.EmbeddedGate()
	g.Exclude = testExclude
	g.Profiles = []clone.Profile{{Name: "kiosk", BuildID: testRelease + "-kiosk\n", RefA: strings.Repeat("a", 64), RefB: ref.SHA256, BundleSHA256: b.Pins.SHA256},
		{Name: "console", BuildID: testRelease + "-console\n", RefA: strings.Repeat("c", 64), RefB: consoleRef.SHA256}}
	u.gate = g
	u.rel = &clone.ReleaseData{Kind: clone.ReleaseKind, Release: testRelease, Schema: 1, XferSHA256: strings.Repeat("9", 64), Profiles: g.Profiles}
	idx := b.Pins.IndexSHA256
	ps := Profiles{Schema: 1, Kind: ProfilesKind, Release: testRelease, BaseVersion: "0.2.0-rc10-base", Alpine: "v3.24", Arch: "aarch64",
		ContentExcludeSHA256: g.ExcludeSHA256(), CloneDeltaSHA256: g.DeltaSHA256(), URLs: []string{"https://example.invalid/r/", "http://mirror.example:8137/r23/"},
		Profiles: map[string]Entry{
			"kiosk": {Apply: "kiosk", BuildID: testRelease + "-kiosk\n", Bundle: &BundleRef{File: "uc-pxx-profile-kiosk-" + testRelease + ".tar",
				Size: b.Pins.Size, SHA256: b.Pins.SHA256}, IndexSHA256: &idx, Installed: inst, Packages: len(o.Pkgs), RefA: strings.Repeat("a", 64), RefB: ref.SHA256},
			"console": {Apply: "console", BuildID: testRelease + "-console\n", Installed: consoleInstSHA, RefA: strings.Repeat("c", 64), RefB: consoleRef.SHA256},
		}}
	pj, _ := json.MarshalIndent(ps, "", " ")
	writeFile(t, filepath.Join(u.root, ProfilesFile), pj) // excluded from ref_b (as in the release list)
	u.ps = &ps
	u.usb = &fakeUSB{u: u}
	u.fetch = &fakeFetch{u: u, data: b.Tar}
	u.env = &Env{Root: u.root, Logf: t.Logf,
		Now: func() time.Time { u.mu.Lock(); defer u.mu.Unlock(); return u.now },
		Sleep: func(d time.Duration) {
			u.mu.Lock()
			u.now = u.now.Add(d)
			u.sleeps = append(u.sleeps, d)
			u.mu.Unlock()
			u.usb.tick()
		},
		Marked: func() (bool, string, error) {
			if !u.marked {
				return false, "b", errors.New("not marked")
			}
			return true, "b", nil
		},
		Locks: func() (func(), error) {
			if u.lockBusy > 0 {
				u.lockBusy--
				return nil, errors.New("busy")
			}
			u.ev("lock")
			return func() { u.ev("unlock") }, nil
		},
		Guard: func(slot string) (func() error, error) {
			u.ev("guard " + slot)
			return func() error { u.ev("restore"); return nil }, nil
		},
		Exec: func(argv []string, line func(string)) error {
			u.ev("exec %s", strings.Join(argv, " "))
			switch argv[0] {
			case "/sbin/apk":
				if u.apkFail {
					return errors.New("exit status 1")
				}
				if argv[1] == "add" {
					for i := range b.DocV.Packages {
						line(fmt.Sprintf("(%d/%d) Installing %s", i+1, len(b.DocV.Packages), b.DocV.Packages[i].Name))
					}
					fakeInstall(t, u.root, b, false)
				}
			case "/bin/sh":
				u.mu.Lock()
				extra := u.applyExtra
				u.mu.Unlock()
				if extra {
					writeFile(t, filepath.Join(u.root, "etc/extra"), []byte("x"))
				} else {
					os.Remove(filepath.Join(u.root, "etc/extra"))
				}
				u.mu.Lock()
				links := !u.noLinks
				u.mu.Unlock()
				if links && len(argv) > 2 {
					d := filepath.Join(u.root, RunlevelDefault)
					os.MkdirAll(d, 0o755)
					for _, n := range ProfileServices(argv[2]) {
						os.Symlink("/etc/init.d/"+n, filepath.Join(d, n))
					}
				}
			case RCService:
				u.mu.Lock()
				down := u.svcDown[argv[1]]
				u.mu.Unlock()
				if argv[2] == "status" && down {
					return errors.New("exit status 3")
				}
			}
			return nil
		},
		Fetch: u.fetch, USB: u.usb,
		Panel: func(l []string) {}, Free: func(string) int64 { return 8 << 30 }, Mem: func() int64 { return 4 << 30 },
		ClockSynced: func() bool { return true }}
	return u
}

func (u *unit) run() (*Result, error) {
	return Run(u.env, Options{Gate: u.gate, Release: u.rel, DoneLinger: time.Second, ChoiceWait: time.Hour, MarkWait: time.Minute,
		ServiceWait: time.Minute})
}

func (u *unit) progress() handover.Progress {
	b, err := os.ReadFile(filepath.Join(u.root, handover.ProvisionProgressFile))
	if err != nil {
		u.t.Fatal(err)
	}
	var p handover.Progress
	json.Unmarshal(b, &p)
	return p
}

// fakeUSB is the receiver's side: choices are written as the receiver writes
// them; a cable bundle is handed over as the receiver hands it over.
type fakeUSB struct {
	u       *unit
	mu      sync.Mutex
	starts  []XferArgs
	queue   []handover.Choice // given one per tick
	cable   []byte            // handed over at the first tick after a cable choice
	pending bool
	seq     int
}

type fakeProc struct{ done chan struct{} }

func (p *fakeProc) Stop() error           { return nil }
func (p *fakeProc) Done() <-chan struct{} { return p.done }

func (f *fakeUSB) Take() error    { f.u.ev("usb take"); return nil }
func (f *fakeUSB) Give() error    { f.u.ev("usb give"); return nil }
func (f *fakeUSB) Cleanup() error { return nil }
func (f *fakeUSB) UID() (int, error) {
	return os.Getuid(), nil
}
func (f *fakeUSB) NoProcessOf(int) error { f.u.ev("noprocess"); return nil }
func (f *fakeUSB) Start(a XferArgs) (Proc, error) {
	f.mu.Lock()
	f.starts = append(f.starts, a)
	f.mu.Unlock()
	f.u.ev("usb start status-only=%v fixed=%s seq=%d", a.StatusOnly, a.Fixed, a.Seq)
	os.MkdirAll(filepath.Join(f.u.root, handover.ProvisionStage, handover.ProvisionXferDir), 0o700)
	return &fakeProc{done: make(chan struct{})}, nil
}

func (f *fakeUSB) dir() string {
	return filepath.Join(f.u.root, handover.ProvisionStage, handover.ProvisionXferDir)
}

// tick: one queued choice is written; a pending cable bundle is handed over.
func (f *fakeUSB) tick() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pending && f.cable != nil {
		f.pending = false
		f.handOver(f.cable)
		return
	}
	if len(f.queue) == 0 {
		return
	}
	c := f.queue[0]
	f.queue = f.queue[1:]
	f.seq++
	c.Version, c.Kind, c.Seq = 1, handover.RecordKindChoice, f.seq
	b, _ := json.Marshal(c)
	os.MkdirAll(f.dir(), 0o700)
	os.WriteFile(filepath.Join(f.dir(), handover.ChoiceRecord), b, 0o600)
	if c.Source == handover.SourceCable {
		f.pending = true
	}
}

// handOver writes what the receiver writes at the PUT_END of a cable bundle.
// Like the real receiver, it has no room while the root's RAM repo still holds
// a partial download (review MAJOR-2): it refuses then.
func (f *fakeUSB) handOver(tarb []byte) {
	if _, err := os.Stat(filepath.Join(f.u.root, handover.ProvisionStage, RepoName)); err == nil {
		f.u.ev("cable refused: the stage holds the root's repo")
		return
	}
	d := filepath.Join(f.dir(), "incoming.9")
	os.RemoveAll(d)
	os.MkdirAll(d, 0o700)
	u, err := bundle.NewUnpacker(bundle.Pins{Size: f.u.b.Pins.Size, SHA256: f.u.b.Pins.SHA256, IndexSHA256: f.u.b.Pins.IndexSHA256}, dirSink{d})
	if err == nil {
		_, err = u.Write(tarb)
	}
	if err == nil {
		err = u.Finish()
	}
	if err != nil {
		f.u.t.Logf("the fake receiver refuses the bundle: %v", err)
		os.RemoveAll(d)
		return
	}
	p := f.u.ps.Profiles["kiosk"]
	rec := handover.Record{Version: 1, Kind: handover.RecordKindProvision, Profile: "kiosk", ManifestSHA256: p.Bundle.SHA256,
		Files: []handover.ReadyFile{{Name: p.Bundle.File, Size: p.Bundle.Size, SHA256: p.Bundle.SHA256}}, Session: 1}
	b, _ := json.Marshal(rec)
	os.WriteFile(filepath.Join(f.dir(), handover.ReadyRecord), b, 0o600)
	os.Rename(d, filepath.Join(f.dir(), handover.ReadyDir))
}

func (f *fakeUSB) choose(c handover.Choice) {
	f.mu.Lock()
	f.queue = append(f.queue, c)
	f.mu.Unlock()
}

// fakeFetch serves the bundle from memory with the behaviours of a test.
type fakeFetch struct {
	u     *unit
	data  []byte
	calls []string // "url off"
	// per call (by index): "cut:N" (the body ends after N bytes), "fail",
	// "full" (a 200 to a Range), "bad" (one wrong byte of chromium)
	plan []string
}

func (f *fakeFetch) Get(ctx context.Context, url string, off, size int64) (io.ReadCloser, int64, error) {
	i := len(f.calls)
	f.calls = append(f.calls, url+" "+strconv.FormatInt(off, 10))
	how := ""
	if i < len(f.plan) {
		how = f.plan[i]
	}
	data := f.data
	switch {
	case how == "fail":
		return nil, 0, errors.New("dial tcp: no route to host")
	case how == "bad":
		data = append([]byte(nil), data...)
		j := bytes.Index(data, bytes.Repeat([]byte("c"), 1000))
		data[j+7] = 'q'
	case how == "full":
		off = 0
	}
	body := data[off:]
	if n, ok := strings.CutPrefix(how, "cut:"); ok {
		k, _ := strconv.Atoi(n)
		body = body[:k]
	}
	return io.NopCloser(bytes.NewReader(body)), off, nil
}

func (u *unit) checkDone(t *testing.T) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(u.root, clone.DoneRecordPath))
	if err != nil {
		t.Fatalf("no done record: %v", err)
	}
	d, err := clone.ParseDone(b)
	if err != nil || d.Profile != "kiosk" || d.RefBSHA256 != u.ps.Profiles["kiosk"].RefB || d.BundleSHA256 != u.b.Pins.SHA256 || d.Release != testRelease {
		t.Fatalf("done record %+v %v", d, err)
	}
	if _, err := os.Stat(filepath.Join(u.root, handover.ProvisionStage, RepoName)); err == nil {
		t.Fatal("the RAM repo is still there")
	}
}

// TestNetHappy: the page's choice (net); the download, the checks, the
// locks, the read-only flags around apk and the profile script only, the
// health gate (the profile's services started after the flags are cleared);
// the done record is put in place after the link was given back.
func TestNetHappy(t *testing.T) {
	u := newUnit(t, nil)
	u.usb.choose(handover.Choice{Profile: "kiosk", Source: "net", URL: "http://mirror.example:8137/r23/"})
	r, err := u.run()
	if err != nil || !r.Done {
		t.Fatalf("%+v %v\n%s", r, err, strings.Join(u.events, "\n"))
	}
	u.checkDone(t)
	ev := strings.Join(u.events, "\n")
	order := []string{"usb take", "usb start status-only=false", "lock", "guard b", "exec /sbin/apk add --no-network --no-cache --repositories-file",
		"exec /bin/sh " + ApplyScript + " kiosk /", "restore", "exec /sbin/rc-service seatd start", "exec /sbin/rc-service kiosk start",
		"exec /sbin/rc-service seatd status", "exec /sbin/rc-service kiosk status", "unlock", "usb give"}
	last := -1
	for _, o := range order {
		i := u.has(o)
		if i <= last {
			t.Fatalf("%q at %d (after %d):\n%s", o, i, last, ev)
		}
		last = i
	}
	if strings.Contains(ev, "apk fix") {
		t.Fatalf("an apk fix without a cut install:\n%s", ev)
	}
	if len(u.fetch.calls) != 1 || u.fetch.calls[0] != "http://mirror.example:8137/r23/uc-pxx-profile-kiosk-0.2.0-rc10.tar 0" {
		t.Fatalf("calls %v", u.fetch.calls)
	}
	f, err := readFixed(filepath.Join(u.root, FixedFile))
	if err != nil || f == nil || f.Profile != "kiosk" {
		t.Fatalf("fixed %+v %v", f, err)
	}
	if p := u.progress(); p.State != "done" {
		t.Fatalf("progress %+v", p)
	}
	if _, err := os.Stat(filepath.Join(u.root, ChoiceFile)); err == nil {
		t.Fatal("the choice record stays after done")
	}
	// the repositories file names only the bundle's folders
	rf, _ := os.ReadFile(filepath.Join(u.root, RepositoriesFile))
	want := ""
	for _, d := range []string{"main", "community", "common", "uc-pxx"} {
		want += filepath.Join(u.root, handover.ProvisionStage, RepoName, "v3.24", d) + "\n"
	}
	if string(rf) != want {
		t.Fatalf("repositories\n%s", rf)
	}
	// a second boot: nothing to do
	u.events = nil
	r, err = u.run()
	if err != nil || r.What != "done before: kiosk" || u.has("usb") >= 0 {
		t.Fatalf("second boot %+v %v %v", r, err, u.events)
	}
}

// TestResume: a cut download resumes at the start of the member that was
// not complete (an HTTP Range); a server that ignores the Range starts from 0.
func TestResume(t *testing.T) {
	u := newUnit(t, nil)
	cut := len(u.b.Doc) + 1024 + 100000
	u.fetch.plan = []string{"cut:" + strconv.Itoa(cut), "cut:1000", "full"}
	u.usb.choose(handover.Choice{Profile: "kiosk", Source: "net"})
	r, err := u.run()
	if err != nil || !r.Done {
		t.Fatalf("%+v %v", r, err)
	}
	u.checkDone(t)
	if len(u.fetch.calls) != 3 {
		t.Fatalf("calls %v", u.fetch.calls)
	}
	off := func(i int) int64 {
		n, _ := strconv.ParseInt(strings.Fields(u.fetch.calls[i])[1], 10, 64)
		return n
	}
	if off(0) != 0 || off(1) <= 0 || off(1) > int64(cut) || off(1)%512 != 0 || off(2) != off(1) {
		t.Fatalf("offsets %v", u.fetch.calls)
	}
	// the profiles.json URLs, the second after the first try failed
	if !strings.HasPrefix(u.fetch.calls[0], "https://example.invalid/r/") || !strings.HasPrefix(u.fetch.calls[1], "http://mirror.example:8137/") {
		t.Fatalf("urls %v", u.fetch.calls)
	}
}

// TestMismatch: a bundle with a wrong byte: one new fetch; twice: failed
// bundle, and nothing was installed.
func TestMismatch(t *testing.T) {
	u := newUnit(t, nil)
	u.fetch.plan = []string{"bad"}
	u.usb.choose(handover.Choice{Profile: "kiosk", Source: "net", URL: "http://h/"})
	if r, err := u.run(); err != nil || !r.Done {
		t.Fatalf("%+v %v", r, err)
	}
	u = newUnit(t, nil)
	u.fetch.plan = []string{"bad", "bad"}
	u.usb.choose(handover.Choice{Profile: "kiosk", Source: "net", URL: "http://h/"})
	r, err := u.run()
	if err == nil || r.Done {
		t.Fatalf("%+v", r)
	}
	if p := u.progress(); p.State != "failed" || p.Fail != "bundle" {
		t.Fatalf("progress %+v", p)
	}
	if u.has("lock") >= 0 || u.has("exec") >= 0 {
		t.Fatalf("something ran: %v", u.events)
	}
	if _, err := os.Stat(filepath.Join(u.root, FixedFile)); err == nil {
		t.Fatal("the profile was fixed")
	}
}

// TestRetriesThenCable: the network fails every time: 5 retries after the
// waits of the design, failed network; the page then sends the bundle over
// the cable.
func TestRetriesThenCable(t *testing.T) {
	u := newUnit(t, nil)
	u.fetch.plan = []string{"fail", "fail", "fail", "fail", "fail", "fail"}
	u.usb.choose(handover.Choice{Profile: "kiosk", Source: "net", URL: "http://h/"})
	u.usb.cable = u.b.Tar
	done := make(chan struct{})
	go func() {
		// the page's second choice after the failure
		for {
			if p, err := os.ReadFile(filepath.Join(u.root, handover.ProvisionProgressFile)); err == nil && bytes.Contains(p, []byte(`"fail":"network"`)) {
				u.usb.choose(handover.Choice{Profile: "kiosk", Source: "cable"})
				close(done)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	r, err := u.run()
	<-done
	if err != nil || !r.Done {
		t.Fatalf("%+v %v\n%s", r, err, strings.Join(u.events, "\n"))
	}
	u.checkDone(t)
	if len(u.fetch.calls) != 6 {
		t.Fatalf("%d calls", len(u.fetch.calls))
	}
	var waits []time.Duration
	for _, s := range u.sleeps {
		waits = append(waits, s)
	}
	total := time.Duration(0)
	for _, w := range waits {
		total += w
	}
	if total < 10*time.Second+30*time.Second+time.Minute+2*time.Minute+5*time.Minute {
		t.Fatalf("the waits %v", waits)
	}
	ev := strings.Join(u.events, "\n")
	if u.has("noprocess") < 0 || !strings.Contains(ev, "usb start status-only=true") {
		t.Fatalf("the cable hand-over:\n%s", ev)
	}
}

// TestPartialThenCable: the network fails after a part of the bundle
// arrived: failed network; the RAM stage is freed, so the cable bundle has
// its room (review MAJOR-2).
func TestPartialThenCable(t *testing.T) {
	u := newUnit(t, nil)
	cut := len(u.b.Doc) + 1024 + 100000
	u.fetch.plan = []string{"cut:" + strconv.Itoa(cut), "fail", "fail", "fail", "fail", "fail", "fail"}
	u.usb.choose(handover.Choice{Profile: "kiosk", Source: "net", URL: "http://h/"})
	u.usb.cable = u.b.Tar
	go func() {
		for {
			if p, err := os.ReadFile(filepath.Join(u.root, handover.ProvisionProgressFile)); err == nil && bytes.Contains(p, []byte(`"fail":"network"`)) {
				u.usb.choose(handover.Choice{Profile: "kiosk", Source: "cable"})
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	r, err := u.run()
	if err != nil || !r.Done || u.has("cable refused") >= 0 {
		t.Fatalf("%+v %v\n%s", r, err, strings.Join(u.events, "\n"))
	}
	u.checkDone(t)
	// the cut try counts as the first try after its progress, then 5 retries
	if len(u.fetch.calls) != 6 {
		t.Fatalf("%d calls", len(u.fetch.calls))
	}
}

// TestBaseMismatch: a unit whose packages are not the bundle's base: failed
// install before anything is fixed, guarded or run.
func TestBaseMismatch(t *testing.T) {
	u := newUnit(t, nil)
	writeFile(t, filepath.Join(u.root, InstalledDB), installedDB([]string{"busybox=1.37.0-r19", "musl=1.2.5-r10", "apk-tools=3.0.9-r0"}))
	u.usb.choose(handover.Choice{Profile: "kiosk", Source: "net", URL: "http://h/"})
	r, err := u.run()
	if err == nil || r.Done {
		t.Fatalf("%+v", r)
	}
	if p := u.progress(); p.Fail != "install" || !strings.Contains(p.Reason, "busybox") {
		t.Fatalf("progress %+v", p)
	}
	if u.has("guard") >= 0 || u.has("exec") >= 0 {
		t.Fatalf("%v", u.events)
	}
	if _, err := os.Stat(filepath.Join(u.root, FixedFile)); err == nil {
		t.Fatal("the profile was fixed")
	}
}

// TestServicesNotStarted (mainline-rc-10b, the health gate): the kiosk does
// not read started within the wait: failed check, no done record, the flags
// restored; a later choice of another profile is refused (the profile is
// fixed), the same one runs apk fix --reinstall after apk add, starts the
// services again and is done.
func TestServicesNotStarted(t *testing.T) {
	u := newUnit(t, nil)
	u.svcDown = map[string]bool{"kiosk": true}
	u.usb.choose(handover.Choice{Profile: "kiosk", Source: "net", URL: "http://h/"})
	u.usb.choose(handover.Choice{Profile: "console", Source: "net"})
	go func() {
		for {
			if p, err := os.ReadFile(filepath.Join(u.root, handover.ProvisionProgressFile)); err == nil && bytes.Contains(p, []byte(`"fail":"check"`)) {
				if _, err := os.Stat(filepath.Join(u.root, clone.DoneRecordPath)); err == nil {
					panic("a done record after a failed health gate")
				}
				u.mu.Lock()
				u.svcDown = nil
				u.mu.Unlock()
				u.usb.choose(handover.Choice{Profile: "kiosk", Source: "net", URL: "http://h/"})
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	r, err := u.run()
	if err != nil || !r.Done {
		t.Fatalf("%+v %v\n%s", r, err, strings.Join(u.events, "\n"))
	}
	ev := strings.Join(u.events, "\n")
	if strings.Count(ev, "restore") != 2 || !strings.Contains(ev, "exec /sbin/apk fix --no-network --no-cache --repositories-file") ||
		strings.Count(ev, "exec /sbin/rc-service kiosk start") != 2 {
		t.Fatalf("events:\n%s", ev)
	}
	// after the failure the receiver came back with the profile fixed (review
	// MINOR-1: it refuses another profile itself)
	if !strings.Contains(ev, "usb start status-only=false fixed=kiosk") {
		t.Fatalf("the receiver was not started again with the fixed profile:\n%s", ev)
	}
	u.checkDone(t)
}

// TestServicesTimeout: a service that never reads started ends in failed
// check after the bounded wait (no done record), with the service's name.
func TestServicesTimeout(t *testing.T) {
	u := newUnit(t, nil)
	u.svcDown = map[string]bool{"tsx-setupd": true}
	u.usb.choose(handover.Choice{Profile: "kiosk", Source: "net", URL: "http://h/"})
	go func() {
		for {
			if p, err := os.ReadFile(filepath.Join(u.root, handover.ProvisionProgressFile)); err == nil && bytes.Contains(p, []byte(`"fail":"check"`)) {
				u.mu.Lock()
				u.now = u.now.Add(10 * time.Hour) // the choice wait ends: the run returns
				u.mu.Unlock()
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	r, err := u.run()
	if err == nil || r.Done {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := os.Stat(filepath.Join(u.root, clone.DoneRecordPath)); err == nil {
		t.Fatal("a done record")
	}
	ev := strings.Join(u.events, "\n")
	if !strings.Contains(ev, "exec /sbin/rc-service tsx-setupd status") {
		t.Fatalf("events:\n%s", ev)
	}
	var waited time.Duration
	for _, d := range u.sleeps {
		if d == 5*time.Second {
			waited += d
		}
	}
	if waited < time.Minute || waited > time.Minute+10*time.Second {
		t.Fatalf("the gate waited %s (the bound is 1 min)", waited)
	}
}

// TestServicesNoLink: the profile script made no runlevel link: failed
// check at once, no service started, no done record.
func TestServicesNoLink(t *testing.T) {
	u := newUnit(t, nil)
	u.noLinks = true
	u.usb.choose(handover.Choice{Profile: "kiosk", Source: "net", URL: "http://h/"})
	go func() {
		for {
			if p, err := os.ReadFile(filepath.Join(u.root, handover.ProvisionProgressFile)); err == nil && bytes.Contains(p, []byte(`"fail":"check"`)) {
				if !bytes.Contains(p, []byte("default runlevel")) {
					panic(string(p))
				}
				u.mu.Lock()
				u.now = u.now.Add(10 * time.Hour)
				u.mu.Unlock()
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	r, err := u.run()
	if err == nil || r.Done || strings.Contains(strings.Join(u.events, "\n"), "rc-service kiosk") {
		t.Fatalf("%+v %v\n%s", r, err, strings.Join(u.events, "\n"))
	}
	if _, err := os.Stat(filepath.Join(u.root, clone.DoneRecordPath)); err == nil {
		t.Fatal("a done record")
	}
}

// TestApkFailure: apk add fails: failed install, the flags restored, no
// service started, no done record.
func TestApkFailure(t *testing.T) {
	u := newUnit(t, nil)
	u.apkFail = true
	u.usb.choose(handover.Choice{Profile: "kiosk", Source: "net", URL: "http://h/"})
	go func() {
		for {
			if p, err := os.ReadFile(filepath.Join(u.root, handover.ProvisionProgressFile)); err == nil && bytes.Contains(p, []byte(`"fail":"install"`)) {
				u.mu.Lock()
				u.now = u.now.Add(10 * time.Hour)
				u.mu.Unlock()
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	r, err := u.run()
	ev := strings.Join(u.events, "\n")
	if err == nil || r.Done || !strings.Contains(ev, "restore") || strings.Contains(ev, "rc-service") || strings.Contains(ev, "exec /bin/sh") {
		t.Fatalf("%+v %v\n%s", r, err, ev)
	}
	if _, err := os.Stat(filepath.Join(u.root, clone.DoneRecordPath)); err == nil {
		t.Fatal("a done record")
	}
}

// TestNoContentCheck (mainline-rc-10b): the tree after the profile steps is
// not ref_b (a file the reference does not have, as on unit 4): done, since
// the package set and the services are right.
func TestNoContentCheck(t *testing.T) {
	u := newUnit(t, nil)
	u.applyExtra = true
	u.usb.choose(handover.Choice{Profile: "kiosk", Source: "net", URL: "http://h/"})
	r, err := u.run()
	if err != nil || !r.Done {
		t.Fatalf("%+v %v\n%s", r, err, strings.Join(u.events, "\n"))
	}
	if c, err := clone.LiveContent(u.root, testExclude); err != nil || c.SHA256 == u.ps.Profiles["kiosk"].RefB {
		t.Fatalf("the tree should differ from ref_b: %+v %v", c, err)
	}
	u.checkDone(t)
}

// TestConsole: the console profile needs a seeded root key; it installs no
// package (the profile script only).
func TestConsole(t *testing.T) {
	u := newUnit(t, nil)
	u.usb.choose(handover.Choice{Profile: "console", Source: "net"})
	u.env.Sleep = func(d time.Duration) {
		u.mu.Lock()
		u.now = u.now.Add(d)
		u.mu.Unlock()
		u.usb.tick()
	}
	o := Options{Gate: u.gate, Release: u.rel, DoneLinger: time.Second, ChoiceWait: time.Minute, MarkWait: time.Minute}
	r, err := Run(u.env, o)
	if err == nil || r.Done {
		t.Fatalf("console without a key: %+v", r)
	}
	if a := u.usb.starts[0]; a.ConsoleKey {
		t.Fatal("the receiver was told of a key")
	}
	writeFile(t, filepath.Join(u.root, "data/tsx/panel.conf"), []byte(`SSH_AUTHORIZED_KEY="`+edKey()+`"`+"\n"))
	if !KeySeeded(u.root) {
		t.Fatal("the seed is not seen")
	}
	u.usb.choose(handover.Choice{Profile: "console", Source: "net"})
	r, err = Run(u.env, o)
	if err != nil || !r.Done || r.Profile != "console" {
		t.Fatalf("%+v %v\n%s", r, err, strings.Join(u.events, "\n"))
	}
	// the health gate of console: the package set only, no service started
	if ev := strings.Join(u.events, "\n"); strings.Contains(ev, "/sbin/apk") || strings.Contains(ev, "rc-service") ||
		u.has("exec /bin/sh "+ApplyScript+" console /") < 0 {
		t.Fatalf("%v", u.events)
	}
}

// TestRefusals: no profile offered (an empty gate); the slot not marked;
// tsx-autoupdate's locks busy longer than LockWait; profiles.json of another
// exclusion list.
func TestRefusals(t *testing.T) {
	u := newUnit(t, nil)
	g := *u.gate
	g.Profiles = nil
	if r, err := Run(u.env, Options{Gate: &g, Release: u.rel}); err == nil || r.Fail != "check" || u.has("usb") >= 0 {
		t.Fatalf("empty gate: %+v %v", r, err)
	}
	u.marked = false
	if r, err := u.run(); err == nil || r.Fail != "check" || !strings.Contains(err.Error(), "not successful") {
		t.Fatalf("not marked: %+v %v", r, err)
	}
	u.marked = true
	u.lockBusy = 1 << 20
	u.usb.choose(handover.Choice{Profile: "kiosk", Source: "net", URL: "http://h/"})
	r, err := Run(u.env, Options{Gate: u.gate, Release: u.rel, ChoiceWait: time.Minute, LockWait: time.Minute})
	if err == nil || u.progress().Fail != "install" || u.has("exec") >= 0 {
		t.Fatalf("locks: %+v %v %+v", r, err, u.progress())
	}
	g = *u.gate
	g.Exclude = append([]string{"/x"}, g.Exclude...)
	if _, err := Run(u.env, Options{Gate: &g, Release: u.rel}); err == nil || !strings.Contains(err.Error(), "exclusion list") {
		t.Fatalf("another list: %v", err)
	}
}

// TestRecordedChoice: a choice with the source net of an earlier boot runs
// again without the page.
func TestRecordedChoice(t *testing.T) {
	u := newUnit(t, nil)
	b, _ := json.Marshal(handover.Choice{Version: 1, Kind: handover.RecordKindChoice, Seq: 1, Profile: "kiosk", Source: "net", URL: "http://h/"})
	writeFile(t, filepath.Join(u.root, ChoiceFile), b)
	r, err := u.run()
	if err != nil || !r.Done {
		t.Fatalf("%+v %v", r, err)
	}
	u.checkDone(t)
}

// TestHTTPFetcher: the unit's HTTP client against a local server: the whole
// file, a Range, a server that answers a Range with 200, a wrong
// Content-Range, an error status.
func TestHTTPFetcher(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789"), 10000)
	mode := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode {
		case "200":
			w.Write(data)
		case "badrange":
			w.Header().Set("Content-Range", "bytes 5-99999/100000")
			w.WriteHeader(206)
		case "404":
			http.NotFound(w, r)
		default:
			http.ServeContent(w, r, "f", time.Unix(0, 0), bytes.NewReader(data))
		}
	}))
	defer srv.Close()
	f := HTTPFetcher()
	get := func(off int64) ([]byte, int64, error) {
		b, start, err := f.Get(context.Background(), srv.URL+"/f", off, int64(len(data)))
		if err != nil {
			return nil, 0, err
		}
		defer b.Close()
		x, err := io.ReadAll(b)
		return x, start, err
	}
	if b, s, err := get(0); err != nil || s != 0 || !bytes.Equal(b, data) {
		t.Fatalf("whole: %d %v", s, err)
	}
	if b, s, err := get(512); err != nil || s != 512 || !bytes.Equal(b, data[512:]) {
		t.Fatalf("range: %d %v", s, err)
	}
	mode = "200"
	if b, s, err := get(512); err != nil || s != 0 || !bytes.Equal(b, data) {
		t.Fatalf("200 to a range: %d %v", s, err)
	}
	mode = "badrange"
	if _, _, err := get(512); err == nil {
		t.Fatal("a wrong Content-Range")
	}
	mode = "404"
	if _, _, err := get(0); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("404: %v", err)
	}
}

// TestProfilesAndOffer: profiles.json's own rules and the offer against the
// gate.
func TestProfilesAndOffer(t *testing.T) {
	u := newUnit(t, nil)
	pj, _ := os.ReadFile(filepath.Join(u.root, ProfilesFile))
	ps, err := ParseProfiles(pj)
	if err != nil {
		t.Fatal(err)
	}
	l, notes, err := Offer(ps, u.gate, u.rel)
	if err != nil || len(l) != 2 || len(notes) != 0 {
		t.Fatalf("%v %v %v", l, notes, err)
	}
	g := *u.gate
	g.Profiles = append([]clone.Profile(nil), u.gate.Profiles...)
	g.Profiles[0].RefB = strings.Repeat("f", 64)
	if l, notes, err := Offer(ps, &g, u.rel); err != nil || len(l) != 1 || l[0].Name != "console" || len(notes) != 1 {
		t.Fatalf("%v %v %v", l, notes, err)
	}
	rel := *u.rel
	rel.Release = "other"
	if _, _, err := Offer(ps, u.gate, &rel); err == nil {
		t.Fatal("another release")
	}
	for _, edit := range []func(p *Profiles){
		func(p *Profiles) { e := p.Profiles["kiosk"]; e.Apply = "ha"; p.Profiles["kiosk"] = e },
		func(p *Profiles) { e := p.Profiles["kiosk"]; e.BuildID = "x\n"; p.Profiles["kiosk"] = e },
		func(p *Profiles) { e := p.Profiles["console"]; e.Packages = 1; p.Profiles["console"] = e },
		func(p *Profiles) { e := p.Profiles["kiosk"]; e.Bundle = nil; p.Profiles["kiosk"] = e },
		func(p *Profiles) { p.URLs = []string{"http://h/x"} },
		func(p *Profiles) { p.Profiles["other"] = p.Profiles["kiosk"] },
	} {
		var q Profiles
		json.Unmarshal(pj, &q)
		edit(&q)
		b, _ := json.Marshal(q)
		if _, err := ParseProfiles(b); err == nil {
			t.Fatalf("taken: %s", b)
		}
	}
	if _, err := ParseConf([]byte("UC_PXX_PROVISION_PROFILES=/tmp/p.json\nUC_PXX_PROVISION_APPLY=" + ApplyScript + "\n")); err == nil {
		t.Fatal("a conf naming another file")
	}
}

// edKey is an ssh-ed25519 public key line.
func edKey() string {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	str := func(b []byte) []byte {
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(b)))
		return append(l[:], b...)
	}
	blob := append(str([]byte("ssh-ed25519")), str(pub)...)
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob) + " t@h"
}
