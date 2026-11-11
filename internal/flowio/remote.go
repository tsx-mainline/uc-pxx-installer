package flowio

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"uc-pxx-installer/internal/flow"
)

// One flow runs on the unit: the UI that holds UILockFile (on the install
// image the screen UI on tty1). It does all the work: the children, the end
// handling (readonly, Classify, the bundle) and the screens. Any other UI
// only shows what that UI publishes, and may send keys to it (FLOW.md
// §2.3, review M-1 and M-2):
//
//   - ScreenFile: the flow UI writes its frame (not laid out) after every
//     draw, with its pid, terminal and time. A waiting screen UI and a
//     take-over terminal draw it at their own size. It holds what the
//     screen shows (no key, no serial beyond the status screen's rows).
//   - InputLockFile and RemoteInFile: `uc-pxx-inst flow --stdin --take-over`
//     holds the input lock (one at a time) and appends its keys and a
//     heartbeat to RemoteInFile. While the lock is held and the heartbeat
//     is fresh, the flow UI takes its keys from there (the touch keys are
//     off) and shows that on the screen. The flow UI never waits for the
//     other terminal: it only reads the file, without blocking.

// The files of the published screen and of the remote input (in Root).
const (
	ScreenFile    = FlowDir + "/screen.json"
	InputLockFile = FlowDir + "/input.lock"
	RemoteInFile  = FlowDir + "/remote.in"
)

// RemoteFresh: the take-over terminal writes a heartbeat every second; a
// remote input older than this is not active.
const RemoteFresh = 5 * time.Second

// Published is the screen of the flow UI.
type Published struct {
	PID   int        `json:"pid"`
	TTY   string     `json:"tty"`
	At    time.Time  `json:"at"`
	Name  string     `json:"screen"`
	Frame flow.Frame `json:"frame"`
}

func rootPath(root, p string) (string, string) {
	if root == "" || root == "/" {
		return RunDir, p
	}
	return filepath.Join(root, RunDir), filepath.Join(root, p)
}

// Publish writes the frame of the flow UI (ScreenFile).
func Publish(root string, p Published) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	base, path := rootPath(root, ScreenFile)
	return writeRunFile(base, path, append(b, '\n'), 0o644)
}

// ReadPublished reads the frame that the flow UI published.
func ReadPublished(root string) (Published, error) {
	var p Published
	_, path := rootPath(root, ScreenFile)
	b, err := os.ReadFile(path)
	if err != nil {
		return p, err
	}
	err = json.Unmarshal(b, &p)
	return p, err
}

// AppendRemote appends one line of the take-over terminal to RemoteInFile:
// "key K D" (a flow.Key, D 1 = down, 0 = up) or "beat".
func AppendRemote(root, line string) error {
	base, path := rootPath(root, RemoteInFile)
	return appendRemote(base, path, line)
}

// appendRemote is the only function that writes RemoteInFile (ci/goallow).
func appendRemote(base, path, line string) error {
	if !inside(base, path) || underKernelFS(path) || !strings.Contains(filepath.Clean(base), "/run/") && !strings.HasSuffix(filepath.Clean(base), "/run") {
		return fmt.Errorf("%s is not a file of the run folder %s", path, base)
	}
	if strings.ContainsAny(line, "\n\r") {
		return errors.New("one line only")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write([]byte(line + "\n"))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// RemoteKey is one key of the take-over terminal.
type RemoteKey struct {
	Key  flow.Key
	Down bool
}

// RemoteIn is the flow UI's side of the remote input: it reads
// RemoteInFile from where it stopped (never what was there before this UI
// started) and says whether a take-over terminal is active.
type RemoteIn struct {
	lock, in string
	off      int64
	part     string
}

// NewRemoteIn starts reading the remote input at its present end.
func NewRemoteIn(root string) *RemoteIn {
	_, lock := rootPath(root, InputLockFile)
	_, in := rootPath(root, RemoteInFile)
	r := &RemoteIn{lock: lock, in: in}
	if st, err := os.Stat(in); err == nil {
		r.off = st.Size()
	}
	return r
}

// Poll returns whether a take-over terminal is active (it holds the input
// lock and its heartbeat is fresh), who it is, and its keys since the last
// poll (none when it is not active). It never blocks.
func (r *RemoteIn) Poll(now time.Time) (bool, Holder, []RemoteKey) {
	var lines []string
	st, err := os.Stat(r.in)
	if err == nil {
		if st.Size() < r.off { // a new file
			r.off, r.part = 0, ""
		}
		if st.Size() > r.off {
			if f, err := os.Open(r.in); err == nil {
				buf := make([]byte, st.Size()-r.off)
				n, _ := f.ReadAt(buf, r.off)
				f.Close()
				r.off += int64(n)
				all := strings.Split(r.part+string(buf[:n]), "\n")
				r.part = all[len(all)-1]
				lines = all[:len(all)-1]
			}
		}
	}
	active := err == nil && now.Sub(st.ModTime()) < RemoteFresh && lockHeld(r.lock)
	if !active {
		return false, Holder{}, nil
	}
	var keys []RemoteKey
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) != 3 || f[0] != "key" {
			continue
		}
		k, err1 := strconv.Atoi(f[1])
		d, err2 := strconv.Atoi(f[2])
		if err1 != nil || err2 != nil || k <= int(flow.KeyNone) || k > int(flow.KeyYes) {
			continue
		}
		keys = append(keys, RemoteKey{Key: flow.Key(k), Down: d == 1})
	}
	return true, ReadUIHolder(r.lock), keys
}

// lockHeld: another process holds the flock of path (an O_RDONLY open; the
// lock is taken and given back at once when it is free).
func lockHeld(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return true
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}
