// uc-pxx-provision: the first-boot provisioning of an R23 base slot
// (DESIGN-R23 §3; MAINLINE-RC10-2026-11-15 §6.10). A separate binary of
// uc-pxx-installer:
//
//	go build -trimpath -ldflags "-s -w -buildid=" ./cmd/uc-pxx-provision   (ci/build-provision.sh)
//
// It links no eMMC writer (ci/check-no-writer.py --provision): no device
// layer, no install engine, no A/B tool, no step-2 engine, no plan. Its eMMC
// reads are the A/B sector (read only, for the mark) and the kernel
// read-only flag of the eMMC nodes (BLKROSET while apk add and the profile
// steps run; internal/blk). It installs the chosen profile from its pinned
// bundle with the base's apk, runs the profile script, checks the tree
// against ref_b and writes the done record of step 2's clone.
//
//	uc-pxx-provision service     the OpenRC service uc-pxx-provision (once per boot)
//	uc-pxx-provision check       read only: the offered profiles, the done record, the content of /
//
// Exit: 0 done (or nothing to do); 1 failed or refused (the log says why;
// both slots stay as they were: provisioning writes no partition); 2 usage.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"uc-pxx-installer/internal/clone"
	"uc-pxx-installer/internal/provision"
)

func logf(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "%s uc-pxx-provision: %s\n", time.Now().UTC().Format("2006-01-02T15:04:05Z"), fmt.Sprintf(f, a...))
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  uc-pxx-provision service
  uc-pxx-provision check`)
	os.Exit(2)
}

func main() {
	if len(os.Args) != 2 {
		usage()
	}
	switch os.Args[1] {
	case "service":
		if os.Geteuid() != 0 {
			logf("the service needs root")
			os.Exit(1)
		}
		// an apk add is never cut short by a signal: the service ends by
		// itself. The signals are caught (and logged), not ignored: an ignored
		// signal stays ignored across exec, and apk, the package scripts and
		// profile-apply.sh must run with the default dispositions, as in the
		// reference build (review MINOR-4)
		sig := make(chan os.Signal, 4)
		signal.Notify(sig, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT, syscall.SIGPIPE)
		go func() {
			for s := range sig {
				logf("signal %v: the service goes on (it ends by itself)", s)
			}
		}()
		syscall.Umask(0o022)
		e := provision.LiveEnv(clone.Release(), logf)
		r, err := provision.Run(e, provision.Options{})
		if r != nil {
			logf("%s", r.What)
		}
		if err != nil {
			logf("%v", err)
			os.Exit(1)
		}
	case "check":
		if err := provision.Check(os.Stdout, "/"); err != nil {
			logf("%v", err)
			os.Exit(1)
		}
	default:
		usage()
	}
}
