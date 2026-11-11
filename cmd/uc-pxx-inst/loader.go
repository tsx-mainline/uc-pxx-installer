//go:build !install

package main

// The restart into the download mode (package rebootmode). It is in the
// status build only: the status RAM image is the way back for a unit with a
// stale BCB (DLMODE.md of the plan). The install build has none of it
// (loader_off.go; ci/check-no-writer.py --release-install): there a restart
// must first respect the flow's holds, which this file does not know.
//
//	uc-pxx-inst loader --check   the read-only checks, exit 0 when the restart would work
//	uc-pxx-inst loader --yes     the checks, then sync and reboot(2) RESTART2 "loader"
//
// The status UI has the same as its page "Download mode" (3 s hold of OK,
// or Enter, then y, on a terminal).

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"uc-pxx-installer/internal/rebootmode"
	"uc-pxx-installer/internal/ui"
)

func init() {
	extraCmds["loader"] = cmdLoader
	extraUsage += `status build (the status RAM image), the way back to the download mode:
  uc-pxx-inst loader --check   read only: would a restart enter the download mode (rockusb)?
  uc-pxx-inst loader --yes     restart into the download mode now (writes nothing to the eMMC;
                           a cold power cycle leaves it)
`
}

// loaderRestart is the restart. It runs only with the unit's own files: a
// test root (--root) is for the checks, so no test can restart the host.
func loaderRestart(root string) error {
	if root != "/" {
		return errors.New("a test root (--root): the checks only, no restart")
	}
	return rebootmode.Restart(root)
}

// loaderPage is the page of the status UI: the checks of rebootmode.Probe.
func loaderPage(root string) *ui.LoaderPage {
	r := rebootmode.Probe(root)
	return &ui.LoaderPage{OK: r.OK, Lines: r.Lines}
}

// loaderFromUI restarts into the download mode from the status UI. It
// returns only on a refusal or an error.
func loaderFromUI(root string) error {
	return loaderRestart(root)
}

// cmdLoader: exit 0 the check passed (--check), 1 refused, 2 usage or the
// system call failed.
func cmdLoader(args []string) {
	fs := flag.NewFlagSet("loader", flag.ExitOnError)
	check := fs.Bool("check", false, "read only: run the checks and say what the restart would do")
	yes := fs.Bool("yes", false, "restart into the download mode now")
	root := fs.String("root", "/", "root of /proc, /sys and /etc (tests)")
	fs.Parse(args)
	if fs.NArg() != 0 || *check == *yes {
		usage()
	}
	r := rebootmode.Probe(*root)
	for _, l := range r.Lines {
		fmt.Println("loader: " + l)
	}
	if !r.OK {
		fmt.Println("RESULT: refused: the unit does not restart. Nothing was written.")
		os.Exit(1)
	}
	fmt.Printf("RESULT: a restart writes %#08x (\"%s\") to the boot-mode register %#x, and U-Boot enters the download mode (rockusb). Nothing is written to the eMMC. A cold power cycle leaves the download mode.\n",
		rebootmode.Magic, rebootmode.Arg, rebootmode.RegAddr)
	if *check {
		return
	}
	logf("restart into the download mode (uc-pxx-inst loader --yes)")
	if err := loaderRestart(*root); err != nil {
		fail(2, "loader: %v", err)
	}
}
