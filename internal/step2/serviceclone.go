//go:build install && step2

package step2

// The service of an end-user base with the clone (DESIGN-R23 §4.5,
// MAINLINE-RC10 record §6.10): the conf says UC_PXX_STEP2_KIND=clone. There
// is no part-2 payload and no USB receive of one (the supervised CLI keeps
// the payload kind). In this order, after the steps of Service that come
// before the question (the finish of a target, the mark, the failed target,
// never/done):
//
//  1. a supervised unit never shows the question (the CLI: plan --clone, the
//     rule-9 GO, start --supervised);
//  2. a keeper whose own record armed a plan whose target is successful:
//     done;
//  3. it waits (at most ProvisionWait, holding no lock, no gadget, no panel)
//     for the provisioning done record; none: "failed: check", nothing
//     written;
//  4. the clone plan (read only); a refusal of the done record or of the
//     content of the running root is "failed: check", any other refusal
//     "plan-refused"; then Start's write-free live checks (ready);
//  5. the consent: uc-pxx-xfer of the kind consent with the plan's id (HELLO
//     mode "step2", state waiting-consent, info.plan_id). The panel asks at
//     once (the 3 s hold of OK, Back = not now, Vol = never). A COMMIT {auto:
//     true, plan_id} of the page gives the 30 s countdown only when
//     step2ui.AutoAllowed allows it for the attempt key "clone <ref_a>"; the
//     attempt line is written durably before the countdown starts. A COMMIT
//     {auto: false} changes nothing (the hold decides);
//  6. the plan again with the confirmation of the screen (it must differ in
//     the confirmation only), the plan file, Start (the clone), the restart.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"uc-pxx-installer/internal/clone"
	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/plan"
	"uc-pxx-installer/internal/step2ui"
	"uc-pxx-installer/internal/xfer/handover"
)

const (
	// ProvisionWait is how long the service waits for the done record of
	// uc-pxx-provision (the download and the install of a profile).
	ProvisionWait = 6 * time.Hour
	// MaxProvisionWait caps the wait while uc-pxx-provision of this boot
	// still runs (its pid file and its process).
	MaxProvisionWait = 24 * time.Hour
	// ProvisionPidFile is the pid file of the provision service (its init
	// script, once per boot, in /run).
	ProvisionPidFile = "/run/uc-pxx-provision.pid"
	// MaxConsentWait is how long the question waits with the consent
	// transport running (the panel's hold works the whole time).
	MaxConsentWait = 2 * time.Hour
	// KindClone is the conf's UC_PXX_STEP2_KIND of a base with the clone.
	KindClone = "clone"
)

// ConsentTransport is the consent receiver of the transport (LiveTransport
// has it; a transport without it gives no consent: the panel's hold only).
type ConsentTransport interface {
	StartConsent(planID string) (Proc, error)
}

// confKind is UC_PXX_STEP2_KIND of the conf ("" when none).
func confKind(e *device.Env) string {
	b, err := readRegular(e.Path(ConfFile), 64<<10)
	if err != nil {
		return ""
	}
	v := ""
	for _, l := range strings.Split(string(b), "\n") {
		k, val, ok := strings.Cut(strings.TrimSpace(l), "=")
		if ok && k == "UC_PXX_STEP2_KIND" {
			v = strings.Trim(val, "\"'")
		}
	}
	return v
}

// writeInfoPlan is writeInfo with the plan id (info.plan_id).
func writeInfoPlan(e *device.Env, o Options, code, planID string, lines ...string) {
	if o.InfoFile == "" {
		return
	}
	if !infoCode.MatchString(code) {
		code = "busy"
	}
	var keep []string
	for _, l := range lines {
		if len(l) > 200 {
			l = l[:200]
		}
		keep = append(keep, printable(l))
	}
	in := handover.BaseInfo(e.Path("/"), code, keep)
	if handover.PlanIDForm.MatchString(planID) {
		in.PlanID = planID
	}
	handover.WriteInfo(e.Path(o.InfoFile), in)
}

// checkFailure: a refusal of the clone's pass 1 (the done record, its
// profile, the content of the running root).
func checkFailure(err error) bool {
	s := err.Error()
	for _, id := range []string{"clone-done", "clone-profile", "keeper-content", "clone-gate"} {
		if strings.Contains(s, id+":") || strings.Contains(s, id+" ") {
			return true
		}
	}
	return false
}

func cloneScreenLines(m *step2ui.Model, p *plan.OtherSlotPlan, now time.Time) []string {
	T, K := strings.ToUpper(p.Target), strings.ToUpper(p.Keeper)
	head := []string{
		"Copy this system (" + p.Clone.Profile + ") into slot " + T + ".",
		"Slot " + T + " and all data of Android are erased. Slot " + K + " (this system) stays as it is and is the fallback.",
		"",
	}
	switch m.Screen {
	case step2ui.ScreenCountdown:
		return append(head, fmt.Sprintf("Starts by itself in %d s. Back (Speaker) cancels; any other key stops the countdown.", int(m.Left(now).Seconds()+0.999)))
	case step2ui.ScreenNever:
		return append(head, "Do not ask again? Hold OK (Mic) 3 s to confirm. Back returns.")
	}
	if l := m.Left(now); l > 0 {
		return append(head, fmt.Sprintf("Keep holding OK: %.0f s", l.Seconds()+0.499))
	}
	return append(head, "Hold OK (Mic) 3 s to start. Back (Speaker) = not now. Vol = do not ask again.")
}

// replanClone makes the clone plan again with the confirmation gate g.
func replanClone(e *device.Env, o Options, g plan.ConfirmGate) (*plan.OtherSlotPlan, error) {
	s, err := open(e, o)
	if err != nil {
		return nil, err
	}
	defer s.close()
	return s.computeClone(g)
}

// serviceClone is the clone branch of Service (see the file comment). s is
// open; it is closed here.
func serviceClone(e *device.Env, o Options, se ServiceEnv, s *session) (*ServiceResult, error) {
	o.Payload = ""
	dir := e.Path(StateDir)
	if s.o.Supervised {
		s.close()
		return &ServiceResult{What: "a supervised unit: the question is never shown (use the CLI procedure: plan --clone)"}, nil
	}
	if done, why := s.armedBefore(dir); done {
		s.close()
		return &ServiceResult{What: "step 2 was done before: " + why}, nil
	}
	s.close()
	// 3. the done record of the provisioning
	if !exists(e.Path(clone.DoneRecordPath)) {
		writeInfo(e, o, "waiting-provision", "Step 2 waits until the system is installed (provisioning).")
		start := se.Now()
		for end := start.Add(ProvisionWait); !exists(e.Path(clone.DoneRecordPath)); se.Sleep(10 * time.Second) {
			now := se.Now()
			// past ProvisionWait the wait goes on while uc-pxx-provision of
			// this boot still runs (it may still install), up to
			// MaxProvisionWait (provision review MINOR-5)
			running := provisionRunning(e)
			if !now.Before(end) && (!running || !now.Before(start.Add(MaxProvisionWait))) {
				why := fmt.Sprintf("after %s", ProvisionWait)
				if running {
					why = fmt.Sprintf("after %s, uc-pxx-provision still runs", MaxProvisionWait)
				}
				writeInfo(e, o, "failed: check", "The system install has no done record: step 2 does not start. Nothing was written.")
				return &ServiceResult{What: "no provisioning done record: step 2 refuses"}, refuse("no provisioning done record %s %s: step 2 refuses (failed: check)", clone.DoneRecordPath, why)
			}
		}
	}
	s, err := open(e, o)
	if err != nil {
		return &ServiceResult{What: "not a step-2 slot boot"}, err
	}
	// 4. the plan and the live checks
	p, err := s.computeClone(plan.ConfirmGate{Confirmed: true})
	if err != nil {
		s.close()
		if checkFailure(err) {
			writeInfo(e, o, "failed: check", "The system on the unit is not as expected: "+firstLine(err.Error()))
			return &ServiceResult{What: "the clone's check refuses: nothing shown, nothing written"}, err
		}
		writeInfo(e, o, "plan-refused: the plan refuses", "Nothing was written: "+firstLine(err.Error()))
		return &ServiceResult{What: "the plan refuses: nothing shown, nothing written"}, err
	}
	if err := s.ready(); err != nil {
		s.close()
		writeInfo(e, o, "plan-refused: the unit is not ready", "Nothing was written: "+firstLine(err.Error()))
		return &ServiceResult{What: "the unit is not ready for step 2: nothing shown, nothing written"}, err
	}
	s.close()
	id12 := p.ID[:12]
	// 5. the consent transport (none: the panel's hold only)
	var usb *usbRun
	var cproc Proc
	uid := -1
	xd := e.Path(filepath.Join(StageDir, "xfer"))
	if ct, ok := se.Transport.(ConsentTransport); ok && confUSB(e) {
		tr := se.Transport
		if err := tr.Take(); err == nil {
			usb = &usbRun{tr: tr}
			err = tr.MountStage()
			if err == nil {
				uid, err = tr.UID()
			}
			if err == nil {
				err = tr.Cleanup()
			}
			if err == nil {
				cproc, err = ct.StartConsent(id12)
			}
			if err != nil {
				s.say("the consent transport did not start (%v): the panel's hold only", err)
				usb.end(false)
				usb, cproc = nil, nil
			}
		}
	}
	endUSB := func(code, line string) {
		if usb != nil {
			if cproc != nil {
				cproc.Stop()
			}
			writeInfoPlan(e, o, code, id12, line)
			usb.end(false)
		}
	}
	ab, aerr := readRegular(filepath.Join(dir, AttemptsFile), 1<<20)
	if errors.Is(aerr, os.ErrNotExist) {
		ab, aerr = nil, nil
	}
	attempts := step2ui.ParseAttempts(ab, aerr)
	aid := p.Clone.AttemptKey
	if se.Panel == nil {
		endUSB("not now", "The unit has no panel for the question.")
		return &ServiceResult{What: "no panel"}, errors.New("no panel")
	}
	pn, err := se.Panel()
	if err != nil {
		endUSB("not now", "The unit has no panel for the question.")
		return &ServiceResult{What: "no panel"}, err
	}
	writeInfoPlan(e, o, "waiting-consent", id12, "Step 2 is ready: hold OK 3 s on the unit, or let the installer page start it.")
	m := step2ui.New(se.Now(), false, "the installer page has not asked to continue by itself")
	consented := false
	deadline := se.Now().Add(MaxConsentWait)
	for !final(m.Screen) {
		if cproc != nil && !consented {
			if c, err := handover.ReadConsent(xd, uid, id12); err == nil {
				consented = true
				if c.Auto && m.Screen == step2ui.ScreenAsk && m.Left(se.Now()) <= 0 {
					auto, autoNo := step2ui.AutoAllowed(true, aid, attempts, s.o.Supervised)
					if auto {
						if err := appendLine(filepath.Join(dir, AttemptsFile), step2ui.AttemptLine(aid)); err != nil {
							auto, autoNo = false, "the attempt line could not be written: "+err.Error()
						}
					}
					if auto {
						m = step2ui.New(se.Now(), true, "")
						writeInfoPlan(e, o, "confirm", id12, "The page asked to continue by itself: the unit counts down 30 s (Back on the unit stops it).")
					} else {
						m = step2ui.New(se.Now(), false, autoNo)
						writeInfoPlan(e, o, "confirm", id12, "No automatic start ("+autoNo+"): hold OK 3 s on the unit.")
					}
				} else {
					writeInfoPlan(e, o, "confirm", id12, "Confirm step 2 on the unit's screen (hold OK 3 s).")
				}
			}
		}
		if m.Screen == step2ui.ScreenAsk && !se.Now().Before(deadline) {
			m.Screen = step2ui.ScreenCancelled
			break
		}
		pn.Show(cloneScreenLines(m, p, se.Now()))
		select {
		case k := <-pn.Keys():
			m.Handle(k, se.Now())
		case <-time.After(200 * time.Millisecond):
			m.Handle(step2ui.KeyNone, se.Now())
		}
	}
	if cproc != nil {
		cproc.Stop()
	}
	switch m.Screen {
	case step2ui.ScreenCancelled:
		pn.Close(true)
		endUSB("not now", "Not now (on the unit): nothing was written.")
		return &ServiceResult{What: "not now"}, nil
	case step2ui.ScreenNeverSet:
		pn.Close(true)
		endUSB("never", "Do not ask again (on the unit): nothing was written.")
		return &ServiceResult{What: "never"}, writeFile(filepath.Join(dir, NeverFile), []byte("set on the panel\n"), true)
	}
	if usb != nil {
		// the page keeps a status view until the restart
		if err := usb.tr.Cleanup(); err == nil {
			if sp, err := usb.tr.Start(true, nil); err == nil {
				usb.status = sp
			}
		}
	}
	pn.Show([]string{"Copying this system into slot " + strings.ToUpper(p.Target) + " …", "Do not disconnect the power until the panel says so."})
	q, err := replanClone(e, o, plan.ConfirmGate{Confirmed: true, Auto: m.Auto})
	if err == nil && !sameBut(p, q) {
		err = refuse("the unit changed while the question was shown: nothing written")
	}
	if err != nil {
		pn.Show([]string{"Step 2 did not start: " + firstLine(err.Error()), "Nothing was written."})
		pn.Close(true)
		endUSB("plan-refused: the unit changed", "Step 2 did not start: nothing was written.")
		return &ServiceResult{What: "the plan changed"}, err
	}
	planFile := e.Path(dataRunDir + "/plan-" + q.ID[:16] + ".json")
	if err := writeJSON(planFile, q, false); err != nil {
		pn.Close(true)
		endUSB("plan-refused: the plan file", "Step 2 did not start: nothing was written.")
		return &ServiceResult{What: "the plan file"}, err
	}
	so := o
	so.PlanFile, so.Confirmed, so.Auto, so.Payload = planFile, !m.Auto, m.Auto, ""
	writeInfoPlan(e, o, "started "+q.ID[:12], q.ID[:12], "Copying this system into slot "+strings.ToUpper(p.Target)+": do not cut the power.")
	res, err := Start(e, so)
	if err != nil {
		lines := []string{"Step 2 stopped: " + firstLine(err.Error())}
		if errors.Is(err, install.ErrABUnknown) {
			lines = append(lines, "DO NOT POWER OFF, DO NOT RESTART. Keep the unit powered and ask for help.")
			writeInfoPlan(e, o, "hold: ab state not known", q.ID[:12], lines...)
		} else {
			lines = append(lines, "Slot "+strings.ToUpper(p.Keeper)+" (this system) still starts. Nothing more is written.")
			writeInfoPlan(e, o, "stop: step 2 stopped", q.ID[:12], lines...)
		}
		pn.Show(lines)
		if usb != nil {
			usb.tr.UnmountStage()
		}
		return &ServiceResult{What: "start failed", Start: res}, err
	}
	pn.Show([]string{"Slot " + strings.ToUpper(p.Target) + " is a copy of this system and starts next.", "The unit restarts now. Slot " +
		strings.ToUpper(p.Keeper) + " stays the fallback."})
	writeInfoPlan(e, o, "armed "+q.ID[:12], q.ID[:12], "Slot "+strings.ToUpper(p.Target)+" is installed and starts next.")
	se.Sleep(5 * time.Second)
	writeInfoPlan(e, o, "restarting", q.ID[:12], "The unit restarts into slot "+strings.ToUpper(p.Target)+".")
	se.Sleep(5 * time.Second)
	if usb != nil && usb.status != nil {
		usb.status.Stop()
	}
	if se.Reboot != nil {
		return &ServiceResult{What: "started, restarting", Start: res}, se.Reboot()
	}
	return &ServiceResult{What: "started", Start: res}, nil
}

// provisionRunning: the provision service of this boot runs: its pid file
// (tmpfs: this boot's) names a process whose comm is uc-pxx-provision (as
// the kernel cuts it: 15 characters).
func provisionRunning(e *device.Env) bool {
	b, err := readRegular(e.Path(ProvisionPidFile), 64)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 1 {
		return false
	}
	c, err := readRegular(e.Path("/proc/"+strconv.Itoa(pid)+"/comm"), 64)
	return err == nil && strings.TrimSpace(string(c)) == "uc-pxx-provisio"
}
