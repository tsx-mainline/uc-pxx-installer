// Package bootctl runs uc-pxx-bootctl, the only program that changes the A/B
// metadata. It is part of the install side: the status-only RAM image does
// not import it (ci/check-no-writer.py checks that).
//
// uc-pxx-bootctl --file PATH works on a 512-byte sector file at offset 0, not
// on a whole disk image. So in a dry run this package copies the A/B sector
// of the image into a private temporary file, runs the tool on it, and gives
// the result back. The executor then puts it into the image with a guarded
// write. On a real device the tool writes LBA 49156 itself: that mode is not
// built (see INSTALLER-INSTALL-PATH.md for what it needs).
package bootctl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"uc-pxx-installer/internal/plan"
)

// Tool is one uc-pxx-bootctl binary.
type Tool struct {
	Path   string // the binary
	Exec   string // optional runner, for example qemu-aarch64-static (dry runs on a PC)
	TmpDir string // where the sector copies go ("" = the system default)
}

// Exit codes of uc-pxx-bootctl.
const (
	RCOK        = 0
	RCRefused   = 1
	RCIOSame    = 2
	RCUnknown   = 3
	timeoutSecs = 60
)

// SHA256 hashes the binary.
func (t Tool) SHA256() (string, error) {
	f, err := os.Open(t.Path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Info is what a plan records about the tool.
func (t Tool) Info() (plan.Bootctl, error) {
	s, err := t.SHA256()
	return plan.Bootctl{Path: t.Path, SHA256: s, Exec: t.Exec}, err
}

// Result is one run.
type Result struct {
	RC     int
	Stdout string
	Stderr string
	Sector []byte // the sector file after the run
}

func (t Tool) run(sector []byte, args []string) (*Result, error) {
	if len(sector) != 512 {
		return nil, fmt.Errorf("sector of %d bytes", len(sector))
	}
	dir, err := os.MkdirTemp(t.TmpDir, "p10inst-ab-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "sector.bin")
	if err := os.WriteFile(path, sector, 0o600); err != nil {
		return nil, err
	}
	full := append(append([]string(nil), args...), "--file", path)
	ctx, cancel := context.WithTimeout(context.Background(), timeoutSecs*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if t.Exec != "" {
		cmd = exec.CommandContext(ctx, t.Exec, append([]string{t.Path}, full...)...)
	} else {
		cmd = exec.CommandContext(ctx, t.Path, full...)
	}
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	r := &Result{RC: -1, Stdout: out.String(), Stderr: errb.String()}
	if ee, ok := err.(*exec.ExitError); ok {
		r.RC = ee.ExitCode()
	} else if err != nil {
		return r, fmt.Errorf("run %s: %w", t.Path, err)
	} else {
		r.RC = 0
	}
	if r.Sector, err = os.ReadFile(path); err != nil {
		return r, err
	}
	if len(r.Sector) != 512 {
		return r, fmt.Errorf("the sector file has %d bytes after the run", len(r.Sector))
	}
	return r, nil
}

// Status runs `uc-pxx-bootctl status` on a copy of the sector. The copy
// must not change.
func (t Tool) Status(sector []byte) (*plan.BootctlStatus, *Result, error) {
	r, err := t.run(sector, []string{"status"})
	if err != nil {
		return nil, r, err
	}
	if r.RC != RCOK {
		return nil, r, fmt.Errorf("uc-pxx-bootctl status: exit %d: %s", r.RC, r.Stderr)
	}
	if !bytes.Equal(r.Sector, sector) {
		return nil, r, fmt.Errorf("uc-pxx-bootctl status changed the sector")
	}
	st, err := plan.ParseBootctlStatus(r.Stderr, r.Stdout)
	return st, r, err
}

// Apply runs one of the two write commands that the installer uses,
// `disable --slot X` or `arm-trying --slot X --lower-keeper`, on a copy of
// the sector, and returns the result. It refuses any other command.
func (t Tool) Apply(sector []byte, args []string) (*Result, error) {
	ok := len(args) == 3 && args[0] == "disable" && args[1] == "--slot" && (args[2] == "a" || args[2] == "b")
	ok = ok || len(args) == 4 && args[0] == "arm-trying" && args[1] == "--slot" && (args[2] == "a" || args[2] == "b") &&
		args[3] == "--lower-keeper"
	if !ok {
		return nil, fmt.Errorf("bootctl command %q is not one the installer uses", args)
	}
	return t.run(sector, args)
}
