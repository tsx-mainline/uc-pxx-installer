package provision

// The health gate of the end of provisioning (mainline-rc-10b, owner
// 2026-11-16: "I dont think it needs to be that strict if the system starts
// its good"). It replaces the content check of the running root against
// ref_b of RC-10. After the pinned package set is checked (apk add exit 0,
// the installed set = the base + the bundle exactly, the sha256 of
// /lib/apk/db/installed = installed_sha256), the services that the profile
// adds to the default runlevel (profile-apply.sh step 10) are started by
// rc-service and must read "started" (`rc-service NAME status`, exit 0)
// within ServiceWait. Each rc-service call is bounded itself (liveExec:
// RCCallLimit for a start, RCStatusLimit for a status, then the output pipe
// is closed): a call that hangs is killed and counts as "not started", so the
// gate always ends. The console profile adds none: its check is the
// package set alone (the base's own services are running: the slot was
// marked by its hook after the default runlevel finished starting).
//
// It writes nothing itself: rc-service starts the services of the profile
// as the next boot of the slot would, and each service writes what its own
// start writes. No eMMC node, no A/B sector, no partition: both slots stay
// as they were.

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"uc-pxx-installer/internal/xfer/handover"
)

// ServiceWait is the longest wait for the profile's services to read
// "started" (the kiosk's start waits up to 20 s for the clock itself).
const ServiceWait = 3 * time.Minute

// RunlevelDefault is the folder of the default runlevel's links.
const RunlevelDefault = "/etc/runlevels/default"

// The services that the profiles add to the default runlevel, in the order
// they are started (uc-pxx-linux rootfs/profiles/kiosk.list and ha.list,
// their "svc default" lines; the console list is the base's own).
var (
	kioskServices = []string{"seatd", "tsx-setup-helper", "tsx-setupd", "kiosk"}
	haServices    = []string{"tsx-sendspin", "tsx-bt", "tsx-esphome", "tsx-mqtt"}
)

// ProfileServices are the services of the default runlevel whose start the
// health gate checks for a profile's apply name: kiosk the kiosk's, ha the
// kiosk's and the Home Assistant ones, console none.
func ProfileServices(apply string) []string {
	switch apply {
	case "kiosk":
		return append([]string(nil), kioskServices...)
	case "ha":
		return append(append([]string(nil), kioskServices...), haServices...)
	}
	return nil
}

// serviceName: a name the service may give rc-service (a fixed name of the
// lists above; never a path or an option).
func serviceName(n string) bool {
	for _, s := range ProfileServices("ha") {
		if n == s {
			return true
		}
	}
	return false
}

// services is the health gate of profile p: every service of the profile is
// a link of the default runlevel (profile-apply.sh made it), is started, and
// reads "started" within the wait.
func (s *svc) services(p Profile) outcome {
	e := s.e
	names := ProfileServices(p.Apply)
	if len(names) == 0 {
		e.Logf("the health gate: the %s profile adds no service (the package set is its check)", p.Name)
		return outcome{ok: true}
	}
	var missing []string
	for _, n := range names {
		fi, err := os.Lstat(e.path(filepath.Join(RunlevelDefault, n)))
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return failed(handover.FailCheck, "the profile steps did not put %s into the default runlevel", strings.Join(missing, ", "))
	}
	wait := s.o.ServiceWait
	if wait == 0 {
		wait = ServiceWait
	}
	s.say(handover.PStateChecking, 0, int64(len(names)), "", "")
	for _, n := range names {
		if err := e.Exec([]string{RCService, n, "start"}, func(l string) { e.Logf("rc-service %s start: %s", n, l) }); err != nil {
			e.Logf("rc-service %s start: %v", n, err)
		}
	}
	end := e.Now().Add(wait)
	for {
		var down []string
		for _, n := range names {
			if err := e.Exec([]string{RCService, n, "status"}, nil); err != nil {
				down = append(down, n)
			}
		}
		s.say(handover.PStateChecking, int64(len(names)-len(down)), int64(len(names)), "", "")
		if len(down) == 0 {
			e.Logf("the health gate passed: the services of %s are started: %s", p.Name, strings.Join(names, ", "))
			return outcome{ok: true}
		}
		if !e.Now().Before(end) {
			return failed(handover.FailCheck, "the services of %s did not start within %s: %s is not started (the next choice tries again)",
				p.Name, wait, strings.Join(down, ", "))
		}
		e.Sleep(5 * time.Second)
	}
}
