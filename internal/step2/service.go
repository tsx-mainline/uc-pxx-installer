//go:build install && step2

package step2

// The panel service of step 2 (design §8; review T4 §6 item 4): the OpenRC
// service uc-pxx-step2 runs `uc-pxx-step2 service` once per boot, after
// uc-pxx-boot-ok. What it does, in this order, and nothing else:
//
//  1. In a step-2 target (the other slot's root file system holds a step-2
//     record whose target is this slot, not finished here yet): the
//     read-only F1 compare of the finish at once, on every boot, marked or
//     not (review T4-2 MINOR-1); a STOP is shown on the panel (keep powered,
//     do not restart, ask) and nothing is written. Then it waits (at most
//     MarkWait) for this slot's mark, and runs the finish. The switch runs by
//     itself only when the start line of that run on the keeper's root says
//     supervised=false and this unit has no SupervisedMarker; a supervised
//     run (or an unreadable start line) never finishes by itself: the service
//     only says so.
//  2. Otherwise it waits (at most MarkWait) until the running slot is
//     successful and the slot that boots (its own mark hook ran).
//  3. In a step-2 keeper whose armed target came back {0,0,0}: the line
//     "failed" in the attempt record (no countdown ever again: ask the user).
//  4. With a payload folder configured (ConfFile) and no done or never
//     record: the confirm screen of package step2ui on the panel (the kiosk
//     stopped, the keys grabbed). Hold OK 3 s = start; Back = not now; Vol =
//     "do not ask again". AUTO-STEP2 next to the folder (in its parent, the
//     top folder of the drive; pass 1 refuses other files inside it) gives the 30 s countdown
//     only when step2ui.AutoAllowed allows it; the attempt line is written
//     durably before the countdown starts. A supervised unit never shows the
//     question (its runs use the CLI with the rule-9 GO lines).
//  5. Start runs the plan that was shown, computed again (the same id). On
//     exit 0 the panel says so and the unit restarts into the target.
//
// A refusal writes nothing and is logged; the kiosk comes back.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/plan"
	"uc-pxx-installer/internal/step2ui"
)

const (
	// ConfFile names the payload folder: UC_PXX_STEP2_PAYLOAD=/path. Without
	// it the service asks nothing.
	ConfFile = "/etc/conf.d/uc-pxx-step2"
	// MarkWait is how long the service waits for the mark of this boot.
	MarkWait = 10 * time.Minute
)

// Panel is the panel of the service: the screen and the keys.
type Panel interface {
	// Show draws the lines (the whole screen).
	Show(lines []string)
	// Keys gives the panel keys (step2ui keys; no autorepeat).
	Keys() <-chan step2ui.Key
	// Close gives the screen and the keys back (the kiosk starts again when
	// restart is true).
	Close(restart bool)
}

// ServiceEnv is what the service needs besides Options.
type ServiceEnv struct {
	// Hold is where a STOP waits (nil: for ever, holding the tsx-autoupdate
	// lock so that no update runs and the restart block stays).
	Hold func()
	// Turn, if set (tests), is one turn of a STOP's loop in place of the
	// 2 s sleep; false ends the loop.
	Turn  func() bool
	Panel func() (Panel, error) // opened only for the question (and the USB receive)
	// Transport is the USB transport of the payload receive (nil: none;
	// LiveTransport on a unit; C2, mainline-rc-4)
	Transport Transport
	Now       func() time.Time // time.Now (monotonic)
	Sleep     func(time.Duration)
	// Reboot restarts the unit (sync; reboot) after a successful start.
	Reboot func() error
}

// ServiceResult says what the service did (for the log and the tests).
type ServiceResult struct {
	What   string
	Start  *Result
	Finish *FinishResult
}

// confPayload reads UC_PXX_STEP2_PAYLOAD from the conf file.
func confPayload(e *device.Env) string {
	b, err := readRegular(e.Path(ConfFile), 64<<10)
	if err != nil {
		return ""
	}
	v := ""
	for _, l := range strings.Split(string(b), "\n") {
		k, val, ok := strings.Cut(strings.TrimSpace(l), "=")
		if ok && k == "UC_PXX_STEP2_PAYLOAD" {
			v = strings.Trim(val, "\"'")
		}
	}
	return v
}

// marked: the running slot is successful and the slot that boots.
func (s *session) marked() (bool, avbab.AB, error) {
	sec, err := s.d.ReadABFresh()
	if err != nil {
		return false, avbab.AB{}, err
	}
	a, _ := avbab.Parse(sec)
	i := 0
	if s.ru.Slot == "b" {
		i = 1
	}
	return a.Validate() == nil && a.Slots[i].Keeper() && !a.Tie() && a.Pick() == i, a, nil
}

// stateOf reads the state lines of a run from a state file's bytes.
func stateOf(b []byte, id string) (started, armed bool, supervised string) {
	supervised = "unknown"
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) < 2 || f[1] != id {
			continue
		}
		switch f[0] {
		case "started":
			started = true
			supervised = "unknown"
			for _, w := range f[2:] {
				if v, ok := strings.CutPrefix(w, "supervised="); ok && (v == "true" || v == "false") {
					supervised = v
				}
			}
		case "armed":
			armed = true
		}
	}
	return
}

// driveID is the attempt identity of a payload folder (no spaces).
func driveID(ident string) string { return sha([]byte(ident))[:32] }

// ErrNotTarget: this slot is not an unfinished step-2 target (no sealed
// step-2 record on the other slot's root whose target is this slot, or its
// finish is done here). The service then goes on with the keeper's side;
// the finish executor (uc-pxx-step2-finish finish-service) stops.
var ErrNotTarget = errors.New("this slot is not an unfinished step-2 target")

// Service runs the service once. See the file comment.
func Service(e *device.Env, o Options, se ServiceEnv) (*ServiceResult, error) {
	if se.Now == nil {
		se.Now = time.Now
	}
	if se.Sleep == nil {
		se.Sleep = time.Sleep
	}
	o.Payload = confPayload(e)
	// 1. a step-2 target: the finish (the same function the finish executor
	// of a normal build runs, mainline-rc-4)
	if r, err := FinishService(e, o, se); !errors.Is(err, ErrNotTarget) {
		return r, err
	}
	s, err := open(e, o)
	if err != nil {
		return &ServiceResult{What: "not a step-2 slot boot"}, err
	}
	dir := e.Path(StateDir)
	T := s.ru.Slot
	deadline := se.Now().Add(MarkWait)

	// the keeper side runs only on a marked boot
	for {
		ok, _, err := s.marked()
		if err != nil {
			s.close()
			return &ServiceResult{What: "A/B unreadable"}, err
		}
		if ok {
			break
		}
		if !se.Now().Before(deadline) {
			s.close()
			return &ServiceResult{What: "this boot is not marked: nothing to do"}, nil
		}
		se.Sleep(10 * time.Second)
	}

	// 3. a keeper whose armed target failed
	if b, err := readRegular(filepath.Join(dir, RecordFile), maxRecord); err == nil {
		var s2 plan.OtherSlotPlan
		if json2(b, &s2) == nil && s2.VerifyID() && s2.Keeper == T {
			st, _ := readRegular(filepath.Join(dir, StateFile), maxRecord)
			if _, armed, _ := stateOf(st, s2.ID); armed {
				_, a, _ := s.marked()
				ti := 1 - slotIdx(T)
				if a.Slots[ti] == (avbab.Slot{}) && !alreadyFailed(dir, s2.ID) {
					if err := appendLine(filepath.Join(dir, AttemptsFile), step2ui.FailedLine("plan-"+s2.ID[:32])); err != nil {
						s.close()
						return &ServiceResult{What: "failed target, record not written"}, err
					}
					s.say("step 2 of plan %s: slot %s did not start (it is {0,0,0} again): recorded; no automatic attempt any more", s2.ID, strings.ToUpper(other(T)))
				}
			}
		}
	}

	// 4. the question
	switch {
	case s.o.Payload == "":
		s.close()
		return &ServiceResult{What: "no payload folder configured"}, nil
	case exists(filepath.Join(dir, NeverFile)) || exists(filepath.Join(dir, DoneFile)):
		s.close()
		return &ServiceResult{What: "never or done"}, nil
	}
	// the payload over the USB cable (C2, mainline-rc-4): a STEP-2 build's
	// conf names the sealed folder in RAM (StagePayload) and UC_PXX_STEP2_USB=yes
	var usb *usbRun
	var pn Panel
	if se.Transport != nil && confUSB(e) && !exists(s.o.Payload) {
		supervised := s.o.Supervised
		if supervised && !exists(e.Path(USBReceiveMarker)) {
			s.close()
			return &ServiceResult{What: "a supervised unit: the question is never shown (use the CLI procedure)"}, nil
		}
		pins := mainline.StepTwoManifests(serviceGate(s.o))
		s.close()
		if !supervised && se.Panel != nil {
			if pn, err = se.Panel(); err != nil {
				pn = nil
			}
		}
		u, _, rerr := receivePayload(e, o, se, pn, pins)
		if u == nil {
			if pn != nil {
				pn.Close(true)
			}
			return &ServiceResult{What: "no payload came over the USB cable"}, rerr
		}
		usb = u
		if supervised {
			// the sealed payload stays in RAM with the status transport: the
			// person runs the CLI procedure (plan, the rule-9 GO, start)
			return &ServiceResult{What: "the payload is sealed in RAM (" + StagePayload + "): a supervised unit runs the CLI procedure"}, nil
		}
		if s, err = open(e, o); err != nil {
			usb.end(false)
			if pn != nil {
				pn.Close(true)
			}
			return &ServiceResult{What: "not a step-2 slot boot"}, err
		}
	}
	// endUSB: the end of the question without a write: the status transport
	// stops, the stage is freed, uc-pxx-usb gets the gadget back
	endUSB := func(code, line string) {
		if usb != nil {
			writeInfo(e, o, code, line)
			usb.end(false)
		}
	}
	if s.o.Supervised {
		s.close()
		return &ServiceResult{What: "a supervised unit: the question is never shown (use the CLI procedure)"}, nil
	}
	if done, why := s.otherRunsPayload(); done {
		s.close()
		endUSB("step2 done", "Step 2 is done with this payload.")
		if pn != nil {
			pn.Close(true)
		}
		return &ServiceResult{What: "step 2 is done with this payload: " + why}, nil
	}
	p, ml, err := s.compute(nil, plan.ConfirmGate{Confirmed: true})
	if err != nil {
		s.close()
		if usb != nil {
			// the page keeps its view of the refusal: the status transport
			// stays for this boot; the stage is freed
			writeInfo(e, o, "plan-refused: the plan refuses", "Nothing was written: "+firstLine(err.Error()))
			usb.tr.UnmountStage()
		}
		if pn != nil {
			pn.Close(true)
		}
		return &ServiceResult{What: "the plan refuses: nothing shown, nothing written"}, err
	}
	ml.Close()
	ident := p.FirmwareIdent
	ab, aerr := readRegular(filepath.Join(dir, AttemptsFile), 1<<20)
	if errors.Is(aerr, os.ErrNotExist) {
		ab, aerr = nil, nil
	}
	// AUTO-STEP2 is next to the payload folder (the top folder of its drive:
	// pass 1 refuses any other file inside uc-pxx-mainline/)
	auto, autoNo := step2ui.AutoAllowed(exists(filepath.Join(filepath.Dir(filepath.Clean(s.o.Payload)), step2ui.AutoFile)), driveID(ident),
		step2ui.ParseAttempts(ab, aerr), s.o.Supervised)
	s.close() // the question may take long: no lock, no open disk while it waits
	if auto {
		if err := appendLine(filepath.Join(dir, AttemptsFile), step2ui.AttemptLine(driveID(ident))); err != nil {
			auto, autoNo = false, "the attempt line could not be written: "+err.Error()
		}
	}
	if pn == nil {
		if se.Panel == nil {
			endUSB("not now", "The unit has no panel for the question.")
			return &ServiceResult{What: "no panel"}, errors.New("no panel")
		}
		if pn, err = se.Panel(); err != nil {
			endUSB("not now", "The unit has no panel for the question.")
			return &ServiceResult{What: "no panel"}, err
		}
	}
	writeInfo(e, o, "confirm", "Confirm step 2 on the unit's screen (hold OK 3 s).")
	m := step2ui.New(se.Now(), auto, autoNo)
	for !final(m.Screen) {
		pn.Show(screenLines(m, p, se.Now()))
		select {
		case k := <-pn.Keys():
			m.Handle(k, se.Now())
		case <-time.After(200 * time.Millisecond):
			m.Handle(step2ui.KeyNone, se.Now())
		}
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
	pn.Show([]string{"Installing mainline Linux into slot " + strings.ToUpper(p.Target) + " …", "Do not disconnect the power until the panel says so."})
	// the plan that was shown, made again with the confirmation the screen
	// gave (the 3 s hold, or the countdown); it must differ from the shown
	// one in the confirmation only. Start computes it again and needs its id.
	q, err := replan(e, o, plan.ConfirmGate{Confirmed: true, Auto: m.Auto})
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
	so.PlanFile, so.Confirmed, so.Auto = planFile, !m.Auto, m.Auto
	writeInfo(e, o, "started "+q.ID[:12], "Installing mainline Linux into slot "+strings.ToUpper(p.Target)+": do not cut the power.")
	res, err := Start(e, so)
	if err != nil {
		lines := []string{"Step 2 stopped: " + firstLine(err.Error())}
		if errors.Is(err, install.ErrABUnknown) {
			lines = append(lines, "DO NOT POWER OFF, DO NOT RESTART. Keep the unit powered and ask for help.")
			writeInfo(e, o, "hold: ab state not known", lines...)
		} else {
			lines = append(lines, "Slot "+strings.ToUpper(p.Keeper)+" (this system) still starts. Nothing more is written.")
			writeInfo(e, o, "stop: step 2 stopped", lines...)
		}
		pn.Show(lines)
		// the status transport stays for the page (KEEP THE UNIT POWERED);
		// the stage is freed
		if usb != nil {
			usb.tr.UnmountStage()
		}
		return &ServiceResult{What: "start failed", Start: res}, err
	}
	pn.Show([]string{"Slot " + strings.ToUpper(p.Target) + " is installed and starts next.", "The unit restarts now. Slot " +
		strings.ToUpper(p.Keeper) + " stays the fallback."})
	writeInfo(e, o, "armed "+q.ID[:12], "Slot "+strings.ToUpper(p.Target)+" is installed and starts next.")
	se.Sleep(5 * time.Second)
	writeInfo(e, o, "restarting", "The unit restarts into slot "+strings.ToUpper(p.Target)+".")
	se.Sleep(5 * time.Second)
	if usb != nil && usb.status != nil {
		usb.status.Stop() // before the restart (C2 item 9)
	}
	if se.Reboot != nil {
		return &ServiceResult{What: "started, restarting", Start: res}, se.Reboot()
	}
	return &ServiceResult{What: "started", Start: res}, nil
}

func final(s step2ui.Screen) bool {
	return s == step2ui.ScreenStart || s == step2ui.ScreenCancelled || s == step2ui.ScreenNeverSet
}

func screenLines(m *step2ui.Model, p *plan.OtherSlotPlan, now time.Time) []string {
	T, K := strings.ToUpper(p.Target), strings.ToUpper(p.Keeper)
	head := []string{
		"Install mainline Linux " + p.Mainline.Manifest.Version + " into slot " + T + ".",
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
	if m.AutoNo != "" && !m.Auto {
		head = append(head, "(No automatic start: "+m.AutoNo+".)")
	}
	return append(head, "Hold OK (Mic) 3 s to start. Back (Speaker) = not now. Vol = do not ask again.")
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	if len(l) > 200 {
		l = l[:200] + "…"
	}
	return l
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func slotIdx(s string) int {
	if s == "b" {
		return 1
	}
	return 0
}

func finishedHere(dir, id string) bool {
	b, err := readRegular(filepath.Join(dir, DoneFile), maxRecord)
	if err != nil {
		return false
	}
	var fp plan.FinishPlan
	return json2(b, &fp) == nil && fp.Step2ID == id
}

func alreadyFailed(dir, id string) bool {
	b, _ := readRegular(filepath.Join(dir, AttemptsFile), 1<<20)
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		if sc.Text() == strings.TrimSuffix(step2ui.FailedLine("plan-"+id[:32]), "\n") {
			return true
		}
	}
	return false
}

// readOtherState reads the state file of slot k's root file system.
func (s *session) readOtherState(k string) ([]byte, error) {
	rr := s.o.RecordReader
	if rr == nil {
		rr = readFileExt4
	}
	return rr(s.d, k, StateFile)
}

func json2(b []byte, v any) error { return json.Unmarshal(b, v) }

// replan makes the plan again with the confirmation gate g.
func replan(e *device.Env, o Options, g plan.ConfirmGate) (*plan.OtherSlotPlan, error) {
	s, err := open(e, o)
	if err != nil {
		return nil, err
	}
	defer s.close()
	p, ml, err := s.compute(nil, g)
	if err != nil {
		return nil, err
	}
	ml.Close()
	return p, nil
}

// sameBut: two plans that differ only in the confirmation (and so in the
// checks' texts and the id).
func sameBut(a, b *plan.OtherSlotPlan) bool {
	norm := func(p *plan.OtherSlotPlan) []byte {
		q := *p
		q.ID, q.Confirm, q.Checks = "", plan.ConfirmGate{}, nil
		j, _ := json.Marshal(&q)
		return j
	}
	return string(norm(a)) == string(norm(b))
}

// stopHold is a finish STOP in the service (review STEP2-REST-1 MINOR-2;
// STEP2-STOP-HOLD-2026-11-11.md §4). First, before the lock, it records the
// STOP durably: `tsx-autoupdate stop-hold set step2 REASON` (no lock taken by
// it). Exit 0: tsx-autoupdate itself holds every install and restart, also
// after a restart, and writes the restart block "hold stop step2"; step 2
// then never writes the block and never runs `set` again in this boot (a
// repeat would undo a person's clear). Any other end (an older tsx-autoupdate
// gives 2): the STOP is not durable; the fallback of before (the restart
// block "hold 4", written again every 2 s), the panel says so, and `set` is
// tried again on each turn until its first exit 0. It never runs `clear`.
// Then it takes the tsx-autoupdate lock (waiting for a run that holds it), so
// no update installs or restarts the unit, shows the STOP on the panel, and
// stays there holding the lock. Nothing is written to the eMMC.
func stopHold(e *device.Env, o Options, se ServiceEnv, fr *FinishResult, err error) (*ServiceResult, error) {
	live := o.Live
	if live == nil {
		live = LiveChecks(e, nil) // the STOP needs only the lock and the set
	}
	reason := "STOP after step 2: " + firstLine(err.Error())
	recorded := exists(e.Path(StopHoldSetMarker)) // set once in this boot already: never again
	trySet := func() {
		if recorded {
			return
		}
		if live.setStopHold(reason) == 0 {
			recorded = true
			writeFile(e.Path(StopHoldSetMarker), []byte("set\n"), false)
		}
	}
	trySet()
	var unlock func()
	for {
		var lerr error
		if unlock, lerr = live.lockAutoupdate(); lerr == nil {
			break
		}
		se.Sleep(10 * time.Second)
		trySet()
	}
	defer unlock()
	show := func() {
		if se.Panel == nil {
			return
		}
		pn, perr := se.Panel()
		if perr != nil {
			return
		}
		lines := []string{"STOP: the check after step 2 found a change (" + firstLine(err.Error()) + ").",
			"Keep the unit powered. Do not restart it. Ask for help. Nothing more is written."}
		if !recorded {
			bErr := writeRestartBlock(e)
			lines = append(lines, "(The hold could not be recorded for tsx-autoupdate: updates and the Restart button are blocked only until a restart. Do not press Restart.)")
			if bErr != nil {
				lines = append(lines, "(The restart block could not be written: "+bErr.Error()+".)")
			}
		}
		pn.Show(lines)
	}
	if !recorded {
		writeRestartBlock(e)
	}
	show()
	// the service stays: the lock and the screen stay until a person acts.
	// A sleep loop, not select{} (with no panel goroutine the runtime would
	// end a bare select as a deadlock: review STEP2-REST-1 re-look R-1);
	// while the STOP is not recorded the block is written again on every turn
	// (tsx-autoupdate's check, boot and refresh remove it: R-2) and set is
	// tried again
	for {
		if se.Hold != nil {
			se.Hold()
			break
		}
		if se.Turn != nil {
			if !se.Turn() {
				break
			}
		} else {
			time.Sleep(2 * time.Second)
		}
		if !recorded {
			if trySet(); recorded {
				show()
			} else {
				writeRestartBlock(e)
			}
		}
	}
	return &ServiceResult{What: "finish STOP", Finish: fr}, err
}

// writeRestartBlock writes "hold 4" to tsx-autoupdate's restart block (the
// form that tsx-panelctl reads: "busy" or "hold N").
func writeRestartBlock(e *device.Env) error {
	p := e.Path(RestartBlock)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := writeFile(p, []byte("hold 4\n"), false); err != nil {
		return err
	}
	return os.Chmod(p, 0o644)
}

// otherRunsPayload: the other slot is successful and its boot image is the
// payload's build (the boot header's name = the manifest's token): step 2
// was done with this payload (also when the done record is on the other
// slot's root only: review STEP2-REST-1 MAJOR-1). Nothing is asked then.
func (s *session) otherRunsPayload() (bool, string) {
	b, err := readRegular(filepath.Join(s.o.Payload, mainline.ManifestFile), 1<<20)
	if err != nil {
		return false, ""
	}
	var m struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(b, &m) != nil || m.Token == "" {
		return false, ""
	}
	T := other(s.ru.Slot)
	e, ok := s.d.Table.Find("boot_" + T)
	if !ok {
		return false, ""
	}
	v := s.d.View()
	defer v.Close()
	h := make([]byte, 64)
	if _, err := v.ReadAt(h, e.Start*512); err != nil || string(h[:8]) != "ANDROID!" {
		return false, ""
	}
	name := strings.TrimRight(string(h[48:64]), "\x00")
	_, a, err := s.marked()
	if err != nil || name != m.Token || !a.Slots[slotIdx(T)].Keeper() {
		return false, ""
	}
	return true, fmt.Sprintf("slot %s is successful and runs this payload's build (token %s)", strings.ToUpper(T), m.Token)
}

// FinishService is the target's part of the service (design §8 item 1; it
// was the first branch of Service): in a step-2 target (the other slot's root
// file system holds a sealed step-2 record whose target is this slot, not
// finished here), the read-only F1 compare at once, on every boot, marked or
// not (review T4-2 MINOR-1); a STOP is recorded, shown and kept (stopHold);
// then it waits (at most MarkWait) for this slot's mark and runs the finish:
// K = b (done) writes only the done record; the switch of K = a runs by
// itself only when the start line of that run on the keeper's root says
// supervised=false and this unit has no SupervisedMarker; otherwise it only
// says how to run the supervised finish. Not a target: ErrNotTarget, nothing
// done.
//
// mainline-rc-4 (owner Q-T8 = option 5): a normal build has no uc-pxx-step2;
// its uc-pxx-usb service receives the pinned finish executor
// (uc-pxx-step2-finish, the finish build of this package) into RAM and runs
// `finish-service`, which is this function, unchanged in its rules.
func FinishService(e *device.Env, o Options, se ServiceEnv) (*ServiceResult, error) {
	if se.Now == nil {
		se.Now = time.Now
	}
	if se.Sleep == nil {
		se.Sleep = time.Sleep
	}
	s, err := open(e, o)
	if err != nil {
		writeInfo(e, o, "finish: refused", firstLine(err.Error()))
		return &ServiceResult{What: "not a step-2 slot boot"}, err
	}
	dir := e.Path(StateDir)
	T, K := s.ru.Slot, other(s.ru.Slot)
	deadline := se.Now().Add(MarkWait)
	rr := s.o.RecordReader
	if rr == nil {
		rr = readFileExt4
	}
	b, rerr := rr(s.d, K, RecordFile)
	var s2 plan.OtherSlotPlan
	if rerr != nil || json2(b, &s2) != nil || s2.Kind != plan.OtherSlotKind || !s2.VerifyID() || s2.Target != T {
		s.close()
		writeInfo(e, o, "finish: not a target", "This slot is not a step-2 target: nothing to finish.")
		return &ServiceResult{What: "not a step-2 target"}, ErrNotTarget
	}
	if finishedHere(dir, s2.ID) {
		s.close()
		writeInfo(e, o, "finish: done", "Step 2 is finished on this slot (done before).")
		return &ServiceResult{What: "the finish is done here"}, ErrNotTarget
	}
	st, _ := s.readOtherState(K)
	_, armed, sup := stateOf(st, s2.ID)
	supervised := s.o.Supervised
	s.close()
	writeInfo(e, o, "finish: checking", "The check after step 2 (RKSS ranges, the keeper, the layout, the key) runs.")
	// F1 runs whatever the state lines say (a lost "armed" line never
	// hides a STOP: review STEP2-REST-1 MINOR-1); only the automatic
	// switch needs the lines
	for {
		fo := o
		fo.DryRun = true
		fr, err := Finish(e, fo)
		switch {
		case errors.Is(err, ErrStop):
			writeInfo(e, o, "stop: the check after step 2", "STOP: "+firstLine(err.Error()), "Keep the unit powered. Do not restart it. Ask for help.")
			return stopHold(e, o, se, fr, err)
		case errors.Is(err, ErrNotYet) && se.Now().Before(deadline):
			writeInfo(e, o, "finish: not-yet", "This slot has not marked itself yet: the finish waits.")
			se.Sleep(10 * time.Second)
			continue
		case errors.Is(err, ErrNotYet):
			writeInfo(e, o, "finish: later", "This slot did not mark itself: the finish checks again at the next boot.")
			return &ServiceResult{What: "the target has not marked itself: the finish checks again at the next boot", Finish: fr}, nil
		case err != nil:
			writeInfo(e, o, "finish: refused", firstLine(err.Error()))
			return &ServiceResult{What: "the finish refuses", Finish: fr}, err
		}
		if fr.Plan.Done {
			fo.DryRun = false
			fr, err = Finish(e, fo) // records done; writes nothing to the eMMC
			if err != nil {
				writeInfo(e, o, "finish: refused", firstLine(err.Error()))
			} else {
				writeInfo(e, o, "finish: done", "Step 2 is finished: slot A boots first, slot B is the fallback.")
			}
			return &ServiceResult{What: "finish: done", Finish: fr}, err
		}
		break
	}
	if !armed || sup != "false" || supervised {
		s.say("step 2 of plan %s: this slot %s is its target; the finish of a supervised run (or of a run whose start or arm line is not readable: %s, armed %v) never runs by itself: run `%s finish --dry-run`, then the rule-9 GO, then `%s finish --expect-id ID`",
			s2.ID, strings.ToUpper(T), sup, armed, finishCmd, finishCmd)
		writeInfo(e, o, "finish: supervised", "A supervised step 2: the finish runs only by the supervised procedure (dry run, rule-9 GO, --expect-id).")
		return &ServiceResult{What: "finish waits for the supervised procedure"}, nil
	}
	fo := o
	fo.Auto = true
	fr, err := Finish(e, fo)
	switch {
	case err == nil && fr != nil && fr.Switched:
		writeInfo(e, o, "finish: switched", "Step 2 is finished: slot A boots first, slot B is the fallback.")
	case errors.Is(err, install.ErrABUnknown):
		writeInfo(e, o, "hold: ab state not known", firstLine(err.Error()), "DO NOT POWER OFF, DO NOT RESTART. Keep the unit powered and ask for help.")
	case err != nil:
		writeInfo(e, o, "finish: refused", firstLine(err.Error()))
	default:
		writeInfo(e, o, "finish: done", "Step 2 is finished.")
	}
	return &ServiceResult{What: "finish", Finish: fr}, err
}

// serviceGate is the gate whose pins the USB receive takes: the session's
// (the release's, or a test's), the same that pass 1 of the plan uses.
func serviceGate(o Options) *mainline.Gate {
	if o.Gate != nil {
		return o.Gate
	}
	return mainline.EmbeddedGate()
}
