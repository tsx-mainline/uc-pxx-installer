package handover

// The provisioning of an R23 base slot (MAINLINE-RC10-2026-11-15 §6.10;
// DESIGN-R23 §3): the contract between the root provision service
// (internal/provision, uc-pxx-provision) and the uc-pxx-xfer receiver of the
// kind provision (uid 990):
//
//   - the root service starts `uc-pxx-xfer run --kind provision` with the
//     unit's pinned profiles (ProvisionProfile.Arg), the fixed profile, the
//     "a root key was seeded" flag and its progress file;
//   - the receiver answers HELLO/STATUS from its own state and the root's
//     progress file (Progress, filtered); a COMMIT {profile, source, url?}
//     that it takes is written as provision.json (Choice, with a sequence
//     number) into its folder: the root reads it (ReadChoice) and acts;
//   - with source "cable" the same session takes one PUT of the pinned
//     bundle file, unpacked into its session folder as it arrives
//     (internal/provision/bundle); its PUT_END is the hand-over point: the
//     receiver writes complete.json (Record kind provision) and renames the
//     folder to complete/; the root stops the receiver, checks the hand-over
//     (CheckProvisionReady), takes the folder over as root and checks every
//     member again against bundle.json before it uses any.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// The places of the provision service (root).
const (
	// ProvisionRunDir holds the service's RAM files (root, 0755: the
	// receiver's user reads the info and the progress file).
	ProvisionRunDir = "/run/uc-pxx-provision"
	// ProvisionStage is the tmpfs of the bundle (root, 0711): the receiver's
	// folder xfer/ and the root's repo/.
	ProvisionStage = ProvisionRunDir + "/stage"
	// ProvisionInfo is the info file of HELLO/STATUS (Info).
	ProvisionInfo = ProvisionRunDir + "/info.json"
	// ProvisionProgressFile is the root's progress (Progress).
	ProvisionProgressFile = ProvisionRunDir + "/progress.json"
	// ProvisionXferDir is the receiver's folder in the stage.
	ProvisionXferDir = "xfer"
	// ChoiceRecord is the receiver's record of a taken COMMIT.
	ChoiceRecord = "provision.json"
	// RecordKindProvision is the kind of complete.json of a cable bundle.
	RecordKindProvision = "provision"
	// RecordKindChoice is the kind of provision.json.
	RecordKindChoice = "provision-choice"
	// MaxBaseURL is the longest url of a COMMIT.
	MaxBaseURL = 512
)

// The states of the provision kind (HELLO/STATUS "state", §6.10).
const (
	PStateWaiting     = "waiting"
	PStateDownloading = "downloading"
	PStateReceiving   = "receiving"
	PStateInstalling  = "installing"
	PStateApplying    = "applying"
	PStateChecking    = "checking"
	PStateDone        = "done"
	PStateRetryWait   = "retry-wait"
	PStateFailed      = "failed"
)

// ProvisionStates is the closed set of the states.
var ProvisionStates = []string{PStateWaiting, PStateDownloading, PStateReceiving, PStateInstalling, PStateApplying, PStateChecking,
	PStateDone, PStateRetryWait, PStateFailed}

// The words of "fail" (only in the state failed; the page decides on them).
const (
	FailNetwork = "network" // the retries of the download are spent: offer the cable
	FailBundle  = "bundle"  // the bytes are not the pinned bundle (also after one new fetch)
	FailSpace   = "space"   // not enough RAM or room on the root file system
	FailInstall = "install" // apk or a profile step failed, or the unit's packages are not the expected ones
	FailCheck   = "check"   // the content check of the system (ref_b) failed, or the unit cannot be provisioned
)

// FailWords is the closed set of the fail words.
var FailWords = []string{FailNetwork, FailBundle, FailSpace, FailInstall, FailCheck}

// The sources of a COMMIT.
const (
	SourceNet   = "net"
	SourceCable = "cable"
)

// ProfileConsole has no bundle.
const ProfileConsole = "console"

func oneOf(s string, l []string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// ProvisionProfile is one pinned profile of the unit (profiles.json): the
// bundle file, its size and sha256, and the sha256 of its bundle.json. The
// console profile has no bundle (File "", Size 0, both sha256 "").
type ProvisionProfile struct {
	Name        string
	File        string
	Size        int64
	SHA256      string
	IndexSHA256 string
}

var (
	profileName = regexp.MustCompile(`^(console|kiosk|kiosk-ha)$`)
	bundleFile  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)
	sha256Form  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Check: a console profile without a bundle, or another profile with one.
func (p ProvisionProfile) Check() error {
	switch {
	case !profileName.MatchString(p.Name):
		return fmt.Errorf("profile %q", p.Name)
	case p.Name == ProfileConsole && (p.File != "" || p.Size != 0 || p.SHA256 != "" || p.IndexSHA256 != ""):
		return errors.New("the console profile has no bundle")
	case p.Name != ProfileConsole && (!bundleFile.MatchString(p.File) || p.Size <= 0 || !sha256Form.MatchString(p.SHA256) ||
		!sha256Form.MatchString(p.IndexSHA256)):
		return fmt.Errorf("profile %s: the bundle %q of %d bytes, sha256 %q, index %q", p.Name, p.File, p.Size, p.SHA256, p.IndexSHA256)
	}
	return nil
}

// Arg is the --profile argument of uc-pxx-xfer: NAME:FILE:SIZE:SHA256:INDEX,
// "console:-:-:-:-" for the console profile.
func (p ProvisionProfile) Arg() string {
	if p.Name == ProfileConsole {
		return "console:-:-:-:-"
	}
	return fmt.Sprintf("%s:%s:%d:%s:%s", p.Name, p.File, p.Size, p.SHA256, p.IndexSHA256)
}

// ParseProvisionProfile parses an Arg.
func ParseProvisionProfile(s string) (ProvisionProfile, error) {
	f := strings.Split(s, ":")
	if len(f) != 5 {
		return ProvisionProfile{}, fmt.Errorf("--profile %q: NAME:FILE:SIZE:SHA256:INDEX", s)
	}
	p := ProvisionProfile{Name: f[0]}
	if f[0] == ProfileConsole {
		if f[1] != "-" || f[2] != "-" || f[3] != "-" || f[4] != "-" {
			return p, fmt.Errorf("--profile %q: console:-:-:-:-", s)
		}
		return p, nil
	}
	n, err := strconv.ParseInt(f[2], 10, 64)
	if err != nil {
		return p, fmt.Errorf("--profile %q: size", s)
	}
	p.File, p.Size, p.SHA256, p.IndexSHA256 = f[1], n, f[3], f[4]
	return p, p.Check()
}

// CheckBaseURL is the rule of a COMMIT's url: a base URL, http or https, a
// host, no user info, no query or fragment, the path ending in "/" (at
// least "/"; the pinned file name is appended), at most MaxBaseURL bytes of
// printable ASCII. The pins stay the authority: a url changes only where
// the bytes come from.
func CheckBaseURL(s string) error {
	if s == "" || len(s) > MaxBaseURL {
		return fmt.Errorf("a url of %d bytes (1 to %d)", len(s), MaxBaseURL)
	}
	for _, c := range []byte(s) {
		if c <= ' ' || c > '~' {
			return errors.New("the url has a byte that is not printable ASCII")
		}
	}
	u, err := url.Parse(s)
	switch {
	case err != nil:
		return fmt.Errorf("the url: %v", err)
	case u.Scheme != "http" && u.Scheme != "https":
		return fmt.Errorf("the url scheme %q (http or https)", u.Scheme)
	case u.Host == "" || u.Hostname() == "" || u.Opaque != "":
		return errors.New("the url has no host")
	case u.User != nil:
		return errors.New("the url has user info")
	case u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(s, "?#"):
		return errors.New("the url has a query or a fragment")
	case !strings.HasSuffix(u.Path, "/"):
		// at least "/" (review MINOR-3: the file name is appended as it is)
		return errors.New("the url's path must end with / (at least \"/\"; the pinned file name is appended)")
	}
	return nil
}

// Choice is provision.json: a COMMIT that the receiver took.
type Choice struct {
	Version int    `json:"version"`
	Kind    string `json:"kind"`
	Seq     int    `json:"seq"`
	Profile string `json:"profile"`
	Source  string `json:"source"`
	URL     string `json:"url,omitempty"`
	Session int    `json:"session"`
	Time    int64  `json:"time"`
}

// Check: the record's own form.
func (c *Choice) Check() error {
	switch {
	case c.Version != RecordVersion || c.Kind != RecordKindChoice || c.Seq <= 0:
		return fmt.Errorf("version %d kind %q seq %d", c.Version, c.Kind, c.Seq)
	case !profileName.MatchString(c.Profile):
		return fmt.Errorf("profile %q", c.Profile)
	case c.Source != SourceNet && c.Source != SourceCable:
		return fmt.Errorf("source %q", c.Source)
	case c.URL != "" && (c.Source != SourceNet || CheckBaseURL(c.URL) != nil):
		return fmt.Errorf("url %q", c.URL)
	case c.Profile == ProfileConsole && c.Source != SourceNet:
		return errors.New("console with the cable")
	}
	return nil
}

// ReadChoice reads provision.json of the receiver's folder dir: a regular
// file of the receiver's user uid (not a link, at most MaxStateFile bytes),
// strictly parsed and checked. No record: os.ErrNotExist.
func ReadChoice(dir string, uid int) (*Choice, error) {
	p := filepath.Join(dir, ChoiceRecord)
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.Mode().IsRegular() || !ok || uid >= 0 && int(st.Uid) != uid {
		return nil, fmt.Errorf("%s: not a regular file of the receiver's user", p)
	}
	b, err := ReadSmall(p, MaxStateFile)
	if err != nil {
		return nil, err
	}
	var c Choice
	if err := DecodeStrict(bytes.TrimRight(b, "\n"), &c); err != nil {
		return nil, fmt.Errorf("%s: %v", p, err)
	}
	if err := c.Check(); err != nil {
		return nil, fmt.Errorf("%s: %v", p, err)
	}
	return &c, nil
}

// Progress is the root's progress file: what HELLO/STATUS of the provision
// kind say while the receiver itself receives nothing.
type Progress struct {
	State    string `json:"state"`
	Done     int64  `json:"done"`
	Total    int64  `json:"total"`
	Fail     string `json:"fail,omitempty"`
	Reason   string `json:"reason,omitempty"`
	NextTryS int    `json:"next_try_s,omitempty"`
	Profile  string `json:"profile,omitempty"`
	Updated  int64  `json:"updated"`
}

var reasonForm = regexp.MustCompile(`^[\x20-\x7e]{0,200}$`)

// Filter keeps each field only in its strict form (the receiver reads a
// root file, but it never passes on a field it does not know the form of);
// a state outside the set is "waiting".
func (p Progress) Filter() Progress {
	out := Progress{State: PStateWaiting, Updated: p.Updated}
	if oneOf(p.State, ProvisionStates) {
		out.State = p.State
	}
	if p.Done >= 0 && p.Total >= 0 && p.Done <= p.Total {
		out.Done, out.Total = p.Done, p.Total
	}
	if out.State == PStateFailed && oneOf(p.Fail, FailWords) {
		out.Fail = p.Fail
	}
	if reasonForm.MatchString(p.Reason) {
		out.Reason = p.Reason
	}
	if out.State == PStateRetryWait && p.NextTryS > 0 && p.NextTryS <= 3600 {
		out.NextTryS = p.NextTryS
	}
	if profileName.MatchString(p.Profile) {
		out.Profile = p.Profile
	}
	return out
}

// WriteProgress writes the progress file (root, 0644 in a 0755 folder),
// atomically.
func WriteProgress(path string, p Progress) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadProgress reads and filters a progress file ("" or a bad file: nil).
func ReadProgress(path string) *Progress {
	if path == "" {
		return nil
	}
	b, err := ReadSmall(path, MaxStateFile)
	if err != nil {
		return nil
	}
	var p Progress
	if json.Unmarshal(b, &p) != nil {
		return nil
	}
	f := p.Filter()
	return &f
}

// CheckProvisionReady checks the receiver's hand-over of a cable bundle in
// dir: complete.json (version 1, kind provision, the profile's bundle by
// name, size and sha256) and the folder complete/ of the receiver's user uid
// (uid < 0: tests only). The members are checked by the root afterwards
// (internal/provision: every member against bundle.json, after the folder is
// root's).
func CheckProvisionReady(dir string, uid int, p ProvisionProfile) (*Ready, error) {
	if err := p.Check(); err != nil || p.Name == ProfileConsole {
		return nil, fmt.Errorf("profile %s has no bundle (%v)", p.Name, err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() || (uid >= 0 && !ownedBy(fi, uid)) {
		return nil, fmt.Errorf("%s: not the receiver's folder", dir)
	}
	b, err := ReadSmall(filepath.Join(dir, ReadyRecord), MaxStateFile)
	if err != nil {
		return nil, err
	}
	var r Record
	if err := DecodeStrict(bytes.TrimRight(b, "\n"), &r); err != nil {
		return nil, fmt.Errorf("%s: %v", ReadyRecord, err)
	}
	switch {
	case r.Version != RecordVersion || r.Kind != RecordKindProvision || r.Profile != p.Name:
		return nil, fmt.Errorf("%s: version %d kind %q profile %q (not the bundle of %s)", ReadyRecord, r.Version, r.Kind, r.Profile, p.Name)
	case r.ManifestSHA256 != p.SHA256 || len(r.Files) != 1 || r.Files[0] != (ReadyFile{Name: p.File, Size: p.Size, SHA256: p.SHA256}):
		return nil, fmt.Errorf("%s: not the pinned bundle %s (%d bytes, sha256 %s)", ReadyRecord, p.File, p.Size, p.SHA256)
	case r.Auto || r.Mode != "":
		return nil, fmt.Errorf("%s: auto or a mode", ReadyRecord)
	}
	folder := filepath.Join(dir, ReadyDir)
	fi, err = os.Lstat(folder)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() || (uid >= 0 && !ownedBy(fi, uid)) {
		return nil, fmt.Errorf("%s: not a folder of the receiver", folder)
	}
	return &Ready{Folder: folder, Record: r}, nil
}
