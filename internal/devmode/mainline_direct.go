//go:build install

package devmode

// The direct path in the device mode (MAINLINE-INT-2026-11-10.md task 3):
// `uc-pxx-inst plan|install --device /dev/mmcblk0 --direct (--fw-drive DRIVE.img
// | --mainline DIR) --unlock-confirmed …` on a factory-stock unit after the
// BCB-route entry. It is the order of InstallMainline, step for step, with
// the direct planner (plan.ComputeMainlineDirect) and executor
// (install.RunMainlineDirect): open (the RAM image, the eMMC and the model,
// the A/B lock, the drive rule) → the stash of this boot (the first drive
// phase: the boot chain and the key record) → pass 1 of the bundle → the plan
// → the confirmation → the re-check (the plan again, the same id) → the run.
// The cache marker of the page's unlock one-liner is read read-only (a
// claim, recorded in the plan). Nothing is written before the run.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/ext4"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/mainline/step2set"
	"uc-pxx-installer/internal/mlstash"
	"uc-pxx-installer/internal/plan"
)

// ReadUnlockMarker reads the unlock marker of the page's adb one-liner from
// the ext4 file system of the partition `cache` (by GPT name; read only, the
// installer's ext4 reader, never a mount). A missing partition, file system
// or file gives Found false with the reason.
func ReadUnlockMarker(r io.ReaderAt, size int64) *plan.UnlockMarker {
	t, err := gpt.Parse(r, size)
	if err != nil {
		return &plan.UnlockMarker{Why: "GPT: " + err.Error()}
	}
	e, ok := t.Find("cache")
	if !ok {
		return &plan.UnlockMarker{Why: "no partition cache"}
	}
	fs, err := ext4.Open(io.NewSectionReader(r, e.Start*gpt.Sector, e.Bytes()), e.Bytes())
	if err != nil {
		return &plan.UnlockMarker{Why: "cache: no ext4 file system (" + err.Error() + ")"}
	}
	b, err := fs.ReadFile(plan.UnlockMarkerPath, 4096)
	if err != nil {
		return &plan.UnlockMarker{Why: "cache" + plan.UnlockMarkerPath + ": " + err.Error()}
	}
	return markerPtr(plan.ParseUnlockMarker(b))
}

func markerPtr(m plan.UnlockMarker) *plan.UnlockMarker { return &m }

// directInput is the direct planner's input: the unit read now, the
// payload, the manifests, the gate, the stash of this boot, the key record's
// sectors, the unlock evidence.
func (s *mlSession) directInput(view io.ReaderAt, ml *mainline.Payload, ident string, say func(string)) (plan.MainlineDirectInput, error) {
	in, err := s.input(view, nil, nil, "", ident, nil, say)
	if err != nil {
		return plan.MainlineDirectInput{}, err
	}
	in.Tool = MainlineVersion + ", direct path"
	in.KeyKeep = nil
	ms, err := s.e.Manifests()
	if err != nil {
		return plan.MainlineDirectInput{}, err
	}
	gate := s.o.Gate
	if gate == nil {
		gate = step2set.Gate(mainline.EmbeddedGate())
	}
	di := plan.MainlineDirectInput{Input: in, Mainline: ml, Manifests: ms, Gate: gate,
		Unlock: plan.DirectUnlockInput{Confirmed: s.o.UnlockConfirmed, LockState: s.o.LockState, Marker: ReadUnlockMarker(view, s.d.Bytes())}}
	st, why := LoadStash(s.e, s.bootID, s.token)
	if st == nil {
		say("stash: none of this boot (" + why + "): the direct path needs the boot chain and the key of the first drive phase")
		return di, nil
	}
	s.stash = st
	di.Stash = &plan.StashInput{IndexSHA256: st.IndexSHA256, FirmwareName: st.Firmware.Name, FirmwareSHA256: st.Firmware.SHA256,
		BuildType: st.Firmware.BuildType, UBootBuild: st.Firmware.UBootBuild, Parts: map[string][]byte{}}
	for _, p := range mlstash.Parts {
		di.Stash.Parts[p] = st.Part(p)
	}
	di.MainlineKey = plan.ReadMainlineKey(view, s.d.Bytes(), st.Record(), s.e.KeyKeepGate(), StashProof(st))
	di.MainlineKey.Pin = gate.KeyRecordPin() // owner O3: the release's pin of the record
	say(fmt.Sprintf("stash: %s of %s, key record sha256 %s…", mlstash.Dir, st.Firmware.Name, st.Key.RecordSHA256[:16]))
	return di, nil
}

// computeDirect makes the direct plan from scratch (pass 1 over the bundle).
func (s *mlSession) computeDirect(quiet bool) (*plan.DirectPlan, error) {
	say := s.sayer(quiet)
	ident, err := s.identNow()
	if err != nil {
		return nil, &device.Refusal{Why: "the mainline bundle: " + err.Error()}
	}
	src, err := s.source()
	if err != nil {
		return nil, err
	}
	say("pass 1: check the mainline bundle (" + src.Describe() + "): the manifest, the boot template and the root file system, hashed in place (nothing is written)")
	ml, err := mainline.Verify(src)
	if err != nil {
		return nil, &device.Refusal{Why: "pass 1: " + err.Error()}
	}
	if now, err := s.identNow(); err != nil || now != ident {
		ml.Close()
		return nil, &device.Refusal{Why: fmt.Sprintf("the mainline bundle changed while pass 1 read it: %s, then %s (%v)", ident, now, err)}
	}
	say("  " + ml.Pass1)
	s.pass1 = ml
	view := s.d.View()
	defer view.Close()
	in, err := s.directInput(view, ml, ident, say)
	if err != nil {
		return nil, err
	}
	p, err := plan.ComputeMainlineDirect(in)
	if err != nil {
		if !quiet {
			s.say("")
			p.Render(s.out)
		}
		return p, &device.Refusal{Why: err.Error()}
	}
	return p, nil
}

// recheckDirect computes the plan p0 again from the unit (the bundle opened
// again by identity with --plan, mainline.Reopen).
func (s *mlSession) recheckDirect(p0 *plan.DirectPlan, ml *mainline.Payload) (*plan.DirectPlan, *mainline.Payload, error) {
	say := s.sayer(true)
	if p0.FirmwareIdent == "" || p0.Mainline == nil {
		return nil, nil, &device.Refusal{Why: "the plan records no bundle identity or mainline payload"}
	}
	if now, err := s.identNow(); err != nil || now != p0.FirmwareIdent {
		return nil, nil, &device.Refusal{Why: fmt.Sprintf("the mainline bundle is not the one that pass 1 checked: now %s (%v), the plan has %s", now, err, p0.FirmwareIdent)}
	}
	if ml == nil {
		src, err := s.source()
		if err != nil {
			return nil, nil, err
		}
		if ml, err = mainline.Reopen(src, p0.Mainline); err != nil {
			return nil, nil, &device.Refusal{Why: "the plan's mainline bundle: " + err.Error()}
		}
	}
	view := s.d.View()
	defer view.Close()
	in, err := s.directInput(view, ml, p0.FirmwareIdent, say)
	if err != nil {
		return nil, nil, err
	}
	p, err := plan.ComputeMainlineDirect(in)
	if err != nil {
		return p, ml, &device.Refusal{Why: err.Error()}
	}
	return p, ml, nil
}

// ReadDirectPlanFile reads a direct plan JSON whose content matches its id.
func ReadDirectPlanFile(path string) (*plan.DirectPlan, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, &device.Refusal{Why: err.Error()}
	}
	p, err := plan.DirectPlanOf(raw)
	if err != nil {
		return nil, &device.Refusal{Why: fmt.Sprintf("%s: %v", path, err)}
	}
	if p.Mode != "device" {
		return nil, &device.Refusal{Why: fmt.Sprintf("%s: a %q plan, not a device plan", path, p.Mode)}
	}
	return p, nil
}

func writeDirectJSON(path string, p *plan.DirectPlan) error {
	if err := CheckOutputPath(path); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(p, "", "  ")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// PlanMainlineDirect checks the unit and prints the direct plan. It writes
// nothing to the eMMC.
func PlanMainlineDirect(e *device.Env, o MainlineOptions, out io.Writer) (*plan.DirectPlan, error) {
	o.Direct = true
	s, err := openMainline(e, o, out)
	if err != nil {
		return nil, err
	}
	defer s.close()
	p, err := s.computeDirect(false)
	if err != nil {
		return p, err
	}
	s.say("")
	p.Render(out)
	s.say("plan id: %s", p.ID)
	if o.JSONOut != "" {
		if err := writeDirectJSON(o.JSONOut, p); err != nil {
			return p, err
		}
	}
	return p, nil
}

// InstallMainlineDirect runs the direct path. See the file comment.
func InstallMainlineDirect(e *device.Env, o MainlineOptions, out io.Writer) (*install.Result, error) {
	o.Direct = true
	s, err := openMainline(e, o, out)
	if err != nil {
		return nil, err
	}
	defer s.close()
	var p1 *plan.DirectPlan
	var ml1 *mainline.Payload
	if o.PlanFile != "" {
		if p1, err = ReadDirectPlanFile(o.PlanFile); err != nil {
			return nil, err
		}
	} else {
		if p1, err = s.computeDirect(false); err != nil {
			return nil, err
		}
		ml1 = s.pass1
	}
	s.say("")
	p1.Render(out)
	s.say("")
	s.say("plan id: %s", p1.ID)
	if o.JSONOut != "" {
		if err := writeDirectJSON(o.JSONOut, p1); err != nil {
			return nil, err
		}
	}
	// owner F1: what slot B holds now is replaced only with the person's
	// confirmation of it (after the plan was shown); nothing is written
	// without it
	if p1.TargetNow == nil {
		return nil, &device.Refusal{Why: "the plan does not say what slot B holds now (target_now)"}
	}
	s.say("")
	s.say("Slot B now: %s.", p1.TargetNow.Text)
	if !o.ReplaceConfirmed {
		return nil, &device.Refusal{Why: "slot B would be replaced without the confirmation of what it holds now (" + plan.ReplaceFlag + "): " + p1.TargetNow.Text + "; nothing written"}
	}
	s.say("confirmed (%s): slot B, as it is now, is replaced", plan.ReplaceFlag)
	if !o.Yes {
		s.say("")
		s.say("This keeps slot A (stock) and writes slot B of this unit with mainline Linux %s and the %s %s boot chain.",
			p1.Mainline.Manifest.Version, p1.Chain.Version, p1.Chain.BuildType)
		fmt.Fprintf(out, "Type INSTALL and press Enter to start, anything else to stop: ")
		line := ""
		if o.Confirm != nil {
			line, _ = bufio.NewReader(o.Confirm).ReadString('\n')
		}
		if strings.TrimSpace(line) != "INSTALL" {
			return nil, &device.Refusal{Why: "not confirmed"}
		}
	}
	if o.AfterConfirm != nil {
		o.AfterConfirm()
	}
	s.say("")
	s.say("re-check: the plan again from the unit, the bundle not hashed again (nothing is written yet)")
	p2, ml2, err := s.recheckDirect(p1, ml1)
	if ml2 != nil && ml2 != ml1 {
		defer ml2.Close()
	}
	if err != nil {
		return nil, err
	}
	if p2.ID != p1.ID {
		return nil, &device.Refusal{Why: fmt.Sprintf("the plan changed after it was shown: id %s, shown %s", p2.ID, p1.ID)}
	}
	if s.stash == nil {
		return nil, &device.Refusal{Why: "no stash of this boot"}
	}
	s.say("re-check: the plan has the same id %s; bundle source %s", p2.ID, p2.FirmwareIdent)
	if err := s.d.CanWriteAB(); err != nil {
		return nil, &device.Refusal{Why: "the A/B steps cannot run: " + err.Error()}
	}
	logPath := o.LogPath
	if logPath == "" {
		logPath = e.Path("/run/uc-pxx-inst/install-" + p2.ID[:12] + ".jsonl")
		os.MkdirAll(filepath.Dir(logPath), 0o755)
		writeDirectJSON(strings.TrimSuffix(logPath, ".jsonl")+".plan.json", p2)
	}
	trPath := strings.TrimSuffix(logPath, ".jsonl") + ".out"
	for _, p := range []string{logPath, trPath} {
		if err := CheckOutputPath(p); err != nil {
			return nil, err
		}
	}
	lf, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return nil, &device.Refusal{Why: err.Error()}
	}
	defer lf.Close()
	if b, err := json.Marshal(map[string]any{"event": "environment", "plan": p2.ID, "kind": p2.Kind, "ram_token": s.token,
		"boot_id": s.bootID, "firmware_ident": p2.FirmwareIdent, "mainline_manifest_sha256": p2.Mainline.ManifestSHA256,
		"unlock_basis": p2.UnlockDirect.Basis, "stash_index_sha256": p2.Stash.IndexSHA256,
		"time": time.Now().UTC().Format(time.RFC3339Nano)}); err == nil {
		lf.Write(append(b, '\n'))
	}
	if s.tr, err = os.OpenFile(trPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND|syscall.O_NOFOLLOW, 0o644); err != nil {
		return nil, &device.Refusal{Why: err.Error()}
	}
	defer func() { s.tr.Close(); s.tr = nil }()
	s.say("log: %s, transcript: %s", logPath, trPath)
	s.say("RAM image token %s (in the plan and in the log)", s.token)
	sigs := []os.Signal{syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGPIPE}
	signal.Ignore(sigs...)
	defer signal.Reset(sigs...)
	s.say("signals: ^C, hangup and SIGTERM are ignored until the install ends")
	s.say("")
	start := time.Now()
	m := s.d.Medium(o.MaxWrite)
	runOpt := install.DirectOptions{Options: install.Options{Say: func(l string) { s.say("%s", l) }, Log: lf, Hooks: o.Hooks,
		MaxWrite: o.MaxWrite, Guard: s.guard(&p2.MainlinePlan), KeyRecord: s.stash.Record()}, Stash: s.stash, Gate: o.Gate}
	if runOpt.Gate == nil {
		runOpt.Gate = step2set.Gate(mainline.EmbeddedGate())
	}
	res, err := install.RunMainlineDirect(p2, ml2, m, runOpt)
	if s.drive != nil {
		s.drive.final()
	}
	ab, abErr := s.d.ReadAB()
	if err != nil {
		if res == nil {
			return nil, &device.Refusal{Why: err.Error()}
		}
		s.say("")
		s.say("STOP: %v", err)
		if errors.Is(err, device.ErrABUnknown) {
			raw := "unreadable"
			if abErr == nil {
				raw = fmt.Sprintf("%x (decoded, not trusted: %s)", ab[:32], plan.State(first32(ab)))
			}
			s.say("!!! DO NOT POWER OFF AND DO NOT RESTART. The A/B state is not known.")
			s.say("!!! Keep the unit powered, read the A/B state with: %s status, and ask for help.", device.BootctlPath)
			s.say("A/B sector now: %s", raw)
			s.say("writes done: %d, plus perhaps the A/B write that was running", res.Writes)
			return res, &Stop{Err: err, Safe: "unknown"}
		}
		state := install.SafeStateOf(ab, abErr, &p2.Plan)
		s.say("A/B now: %s", state)
		s.say("writes done: %d", res.Writes)
		return res, &Stop{Err: err, Safe: state}
	}
	state := install.SafeStateOf(ab, abErr, &p2.Plan)
	s.say("")
	s.say("RESULT: install done in %s, %d writes, every write read back and verified", time.Since(start).Round(time.Second), res.Writes)
	s.say("A/B now: %s", state)
	s.say("Slot B starts mainline Linux at the next restart. It marks itself successful only with a network link (eth0 or usb0) and sshd within 300 s; otherwise its tries run out and slot A starts again.")
	return res, nil
}
