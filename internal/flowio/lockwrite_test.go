//go:build install && devtest

package flowio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/devmode"
	"uc-pxx-installer/internal/flow"
)

// TestLockWriteState (flow review MINOR-B, mainline-rc-7): the runner's read
// of the lock-state record agrees with the device mode's own rule
// (devmode.LockWriteHeld) on every shape of the record, and the paths are the
// device mode's.
func TestLockWriteState(t *testing.T) {
	if LockWriteRecord != devmode.LockWriteRecord || LockWriteStarted != devmode.LockWriteStarted {
		t.Fatalf("the paths %s %s are not the device mode's %s %s", LockWriteRecord, LockWriteStarted, devmode.LockWriteRecord, devmode.LockWriteStarted)
	}
	for _, c := range []struct {
		name    string
		rec     string // "" = no record
		started bool
		done    bool
	}{
		{"nothing", "", false, false},
		{"a start marker, no record", "", true, false},
		{"verified", `{"verified":true,"read_back":"0x01","backup_sha256":"ab","restored":false,"changed":false}`, true, true},
		{"restored after a failure", `{"verified":false,"read_back":"0x02","backup_sha256":"ab","restored":true,"changed":false}`, true, false},
		{"not restored", `{"verified":false,"read_back":"0x02","backup_sha256":"ab","restored":false,"changed":false}`, true, false},
		{"no backup (failed before it)", `{"verified":false,"backup_sha256":"","restored":false,"changed":false}`, true, false},
		{"changed", `{"verified":false,"backup_sha256":"ab","restored":true,"changed":true}`, true, false},
		{"unreadable", `{"verified":`, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			d := filepath.Join(root, filepath.Dir(LockWriteRecord))
			os.MkdirAll(d, 0o700)
			if c.rec != "" {
				os.WriteFile(filepath.Join(root, LockWriteRecord), []byte(c.rec), 0o600)
			}
			if c.started {
				os.WriteFile(filepath.Join(root, LockWriteStarted), []byte("x\n"), 0o600)
			}
			r := New(Config{Root: root}, flow.New("t", false))
			stop, done, raw := r.lockWriteState()
			e, err := device.NewForTest(device.TestOptions{Root: root, RegularFiles: true})
			if err != nil {
				t.Fatal(err)
			}
			held := devmode.LockWriteHeld(e)
			if (stop != "") != (held != "") || done != c.done || (c.rec != "") != (raw != nil) {
				t.Fatalf("runner: stop %q done %v raw %v; device mode: held %q", stop, done, raw != nil, held)
			}
			if stop != "" && strings.Contains(strings.ToLower(stop), "nothing on the unit") {
				t.Fatalf("stop %q", stop)
			}
		})
	}
}
