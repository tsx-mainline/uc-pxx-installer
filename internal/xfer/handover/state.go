package xfer

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
)

// The states of the receiver (HELLO/STATUS "state"; STEP2-USB-TRANSPORT §3.4).
const (
	StateStarting   = "starting"
	StateWaiting    = "waiting-payload"
	StateReceiving  = "receiving"
	StateComplete   = "payload-complete" // COMMIT done: the ready folder waits for the seal
	StateStatusOnly = "status-only"      // after the seal: every PUT is refused
)

// The files of the receiver's folder (Config.Dir, owned by its user).
const (
	StateFile    = "state.json"    // the receiver's state, for the root service
	ReadyDir     = "complete"      // the ready folder: exactly the three payload files
	ReadyRecord  = "complete.json" // its record (written before the folder is renamed to ReadyDir)
	incomingStem = "incoming."     // incoming.<session>: one per session
)

// State is the receiver's state file (and the core of HELLO/STATUS).
type State struct {
	State       string `json:"state"`
	Detail      string `json:"detail,omitempty"`
	Session     int    `json:"session"`
	File        string `json:"file,omitempty"`
	Bytes       int64  `json:"bytes"`
	Total       int64  `json:"total"`
	ManifestSHA string `json:"manifest_sha256,omitempty"`
	Updated     int64  `json:"updated"`
}

// maxStateFile caps the read of a state file or a record by the root service.
const maxStateFile = 64 << 10

// readSmall reads a regular file of at most max bytes without following a
// symbolic link (the root service reads files that an unprivileged user wrote).
func readSmall(p string, max int64) ([]byte, error) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", p)
	}
	if fi.Size() > max {
		return nil, fmt.Errorf("%s: %d bytes (at most %d)", p, fi.Size(), max)
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s: more than %d bytes", p, max)
	}
	return b, nil
}

var stateName = regexp.MustCompile(`^[a-z-]{1,32}$`)

// ReadState reads the receiver's state file in dir (for the root service: the
// "receiving" screens). It never follows a link and refuses an odd file.
func ReadState(dir string) (*State, error) {
	b, err := readSmall(filepath.Join(dir, StateFile), maxStateFile)
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("%s: %v", StateFile, err)
	}
	if !stateName.MatchString(s.State) {
		return nil, errors.New("state file: no state")
	}
	return &s, nil
}

// writeAtomic writes b to dir/name by a temporary file and a rename.
func writeAtomic(dir, name string, b []byte) error {
	tmp := filepath.Join(dir, "."+name+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, name))
}
