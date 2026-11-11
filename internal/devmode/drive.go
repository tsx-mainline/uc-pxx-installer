//go:build install

package devmode

// The firmware on the UC-PXX-INST drive (--fw-drive IMAGE): the on-screen
// flow gives the drive file, and the device mode reads the one .bin on it in
// place (package fatfs), with no copy and no mount.
//
// The drive rule (INSTALLER-END-USER-PLAN.md §2.6, FAT-READER-REVIEW.md F1,
// user 2026-10-29: required and checked by code): the lun of the drive is
// empty (the host does not have the drive) and the drive file keeps one
// identity (size, modification and change time, device, inode) from the
// first read of the firmware to the end of the install. The device mode
// checks it at the start, at every open of the firmware (each plan
// computation), and before every step of the install up to the arm. A
// difference before the first step refuses (nothing written); a difference
// later stops the install before that step, so the target stays disabled and
// is never armed, and the keeper boots. After the arm the install reads the
// drive no more; a difference there is only reported.
//
// Safety does not rest on this rule (the executor hashes every byte it
// writes and verifies the eMMC before the arm). The rule makes a changed
// drive a clean stop at a step boundary, and it makes the flow's promise
// ("the drive stays detached") a fact that the log shows.

import (
	"fmt"
	"io"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/drivestate"
	"uc-pxx-installer/internal/fatfs"
	"uc-pxx-installer/internal/plan"
)

// driveGuard is the drive rule of one run.
type driveGuard struct {
	s        *session
	lun      string
	image    string
	want     drivestate.Ident
	afterArm bool
	opened   []io.Closer
}

// setupDrive checks the drive rule once and makes FWSource read the drive.
// It runs in open, before the RAM image and the eMMC are checked, so a
// refusal here has not even locked anything.
func (s *session) setupDrive() error {
	o := &s.o
	if o.FWDrive == "" {
		return nil
	}
	if o.FWSource != nil || o.FW != "" {
		return &device.Refusal{Why: "give the firmware as a file (--fw) or on the drive (--fw-drive), not both"}
	}
	g := &driveGuard{s: s, lun: s.e.Path(drivestate.LunFile), image: o.FWDrive}
	var err error
	if o.FWDriveExpect != "" {
		g.want, err = drivestate.Parse(o.FWDriveExpect)
	} else {
		g.want, err = drivestate.Stat(g.image)
	}
	if err != nil {
		return &device.Refusal{Why: "drive: " + err.Error()}
	}
	if err := g.check(); err != nil {
		return &device.Refusal{Why: "drive: " + err.Error()}
	}
	o.FW = "the .bin on the UC-PXX-INST drive " + g.image
	o.FWSource = g.open
	s.drive = g
	return nil
}

func (g *driveGuard) check() error {
	return drivestate.Detached(g.lun, g.image, g.want)
}

// open is FWSource: the rule, then the firmware file of the drive, then the
// rule again (the open must not race with a change).
func (g *driveGuard) open() (FWFile, error) {
	if err := g.check(); err != nil {
		return nil, err
	}
	f, err := fatfs.OpenFirmware(g.image)
	if err != nil {
		return nil, err
	}
	if err := g.check(); err != nil {
		f.Close()
		return nil, err
	}
	g.opened = append(g.opened, f)
	return f, nil
}

// step is install.Options.Guard: the rule before every step up to the arm.
func (g *driveGuard) step(st plan.Step) error {
	err := g.check()
	if g.afterArm {
		if err != nil {
			g.s.say("drive: %v (after the arm: the install does not read the drive any more; the written slot was verified)", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("drive rule: %w", err)
	}
	if st.Op == "bootctl" && st.Bootctl != nil && len(st.Bootctl.Args) > 0 && st.Bootctl.Args[0] == "arm-trying" {
		g.afterArm = true
	}
	return nil
}

// final reports the rule once more at the end of the run.
func (g *driveGuard) final() {
	if err := g.check(); err != nil {
		g.s.say("drive: at the end of the run: %v", err)
		return
	}
	g.s.say("drive: detached and unchanged from the first read to the end (%s)", g.want)
}

func (g *driveGuard) close() {
	for _, c := range g.opened {
		c.Close()
	}
	g.opened = nil
}
