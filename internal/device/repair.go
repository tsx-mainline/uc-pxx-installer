//go:build install

package device

// The device side of the A/B repair of the hold (DEVMODE-R4.md §2): the
// repair form of uc-pxx-bootctl-inst, a fresh read of the sector with the page
// caches dropped, the kernel's boot id, and the check that no bootctl still
// runs. The rule (when to write, what to compare) is in package devmode; the
// state and its prediction are in package plan. p10inst never writes the A/B
// sector itself: the tool does, in its `repair` command.

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// BootIDPath is the kernel's id of this boot.
const BootIDPath = "/proc/sys/kernel/random/boot_id"

var bootIDRx = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// BootID is the kernel's boot id. It changes with every boot (also every
// `fastboot boot` of the RAM image), so a plan that records it belongs to
// this boot only.
func (e *Env) BootID() (string, error) {
	s, err := readTrim(e.path(BootIDPath))
	if err != nil {
		return "", refuse("cannot read the boot id %s: %v", BootIDPath, err)
	}
	if !bootIDRx.MatchString(s) {
		return "", refuse("the boot id %q is not a UUID", s)
	}
	return s, nil
}

// RepairArgs is the command line of the repair form of uc-pxx-bootctl-inst:
// `repair --keeper K --expect-sector HEX [--dry-run]`. HEX is the whole
// sector the caller read, so the tool writes only over exactly those bytes.
func RepairArgs(keeper string, sector []byte, dry bool) []string {
	a := []string{"repair", "--keeper", keeper, "--expect-sector", hex.EncodeToString(sector)}
	if dry {
		a = append(a, "--dry-run")
	}
	return a
}

// SwitchArgs is the command line of the switch form: `switch --to T
// --expect-sector HEX [--dry-run]`.
func SwitchArgs(to string, sector []byte, dry bool) []string {
	a := []string{"switch", "--to", to, "--expect-sector", hex.EncodeToString(sector)}
	if dry {
		a = append(a, "--dry-run")
	}
	return a
}

// repairForm reports whether args is the repair or the switch form, and
// whether it writes.
func repairForm(args []string) (ok, writes bool) {
	if len(args) != 5 && len(args) != 6 {
		return false, false
	}
	opt := map[string]string{"repair": "--keeper", "switch": "--to"}[args[0]]
	if opt == "" || args[1] != opt || (args[2] != "a" && args[2] != "b") || args[3] != "--expect-sector" {
		return false, false
	}
	b, err := hex.DecodeString(args[4])
	if err != nil || len(b) != 512 || hex.EncodeToString(b) != args[4] {
		return false, false
	}
	if len(args) == 6 {
		return args[5] == "--dry-run", false
	}
	return true, true
}

// ReadABFresh reads the A/B sector for a check after a write: it drops the
// page cache of the misc node and of the disk node (BLKFLSBUF), then reads
// with O_DIRECT through misc. bootctl writes through the disk node; neither
// cache can hide its write.
func (d *Disk) ReadABFresh() ([]byte, error) {
	misc, ok := d.byName["misc"]
	if !ok {
		return nil, errors.New("no misc partition")
	}
	if err := d.e.flushBuf(d.partNode(misc), misc.Major, misc.Minor); err != nil {
		return nil, err
	}
	if err := d.e.flushBuf(d.diskNode(), d.Major, d.Minor); err != nil {
		return nil, err
	}
	return d.ReadAB()
}

// BootctlRuns lists the processes that run uc-pxx-bootctl-inst (any copy:
// its name is the last part of argv[0] or argv[1]), from /proc. After a kill
// of an install, the tool may still finish its write in its own session; the
// repair waits for none and writes nothing while one runs.
func (e *Env) BootctlRuns() ([]string, error) {
	ents, err := os.ReadDir(e.path("/proc"))
	if err != nil {
		return nil, fmt.Errorf("read /proc: %w", err)
	}
	var out []string
	for _, ent := range ents {
		if _, err := strconv.Atoi(ent.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join(e.path("/proc"), ent.Name(), "cmdline"))
		if err != nil {
			continue // the process ended
		}
		for i, a := range strings.Split(string(b), "\x00") {
			if i > 1 {
				break
			}
			if filepath.Base(a) == "uc-pxx-bootctl-inst" {
				out = append(out, ent.Name())
				break
			}
		}
	}
	return out, nil
}
