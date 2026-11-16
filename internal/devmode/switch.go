//go:build install

package devmode

// The slot switch (owner decision 2026-10-31, DEVMODE-R4.md §4):
//
//	uc-pxx-inst switch --device /dev/mmcblk0 --to a|b --log DIR (--yes | --dry-run)
//
// With --untested (mainline-rc-10 I-2, uc-pxx-bootctl-inst r3) the slot T
// may be trying instead (not tested on this unit yet) when it holds exactly
// one known firmware's boot chain and the other slot is successful or holds
// one too (knownSwitch); the same write, T stays trying.
//
// It makes the other slot boot next, with no install. Only through
// uc-pxx-bootctl-inst `switch --to T --expect-sector HEX`, and only when the
// sector is valid (both decoders), the slot T is successful (it booted and was
// marked), and the slot it switches from stays bootable as the fallback. T
// gets priority 15, the other slot min(p, 14): two priority bytes and the CRC
// change, nothing else. One write, a fresh read-back compared byte for byte
// with the installer's prediction, and bootctl status again. No second write:
// the sector before was a bootable state, so a blind retry could lose it.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/plan"
)

// SwitchOptions are the options of the slot switch.
type SwitchOptions struct {
	Device  string // must be /dev/mmcblk0
	To      string // "a" or "b": the slot that boots next afterwards
	LogDir  string
	Yes     bool
	Confirm io.Reader // else the word SWITCH typed
	DryRun  bool
	// Untested (--untested, mainline-rc-10 I-2): the slot To is trying,
	// not successful; it must hold exactly one known firmware's boot chain,
	// and the other slot must be successful or hold one too (knownSwitch).
	Untested bool
	Hooks    *install.Hooks // tests only
}

// Switch runs the slot switch. The result and the exit codes are those of
// the repair (RepairExitCode): 0 safe (switched, or nothing to do), 1
// refused (nothing written), 3 the state is not known after the write, 5 a
// dry run that would switch.
func Switch(e *device.Env, o SwitchOptions, out io.Writer) (*RepairResult, error) {
	res := &RepairResult{}
	to := map[string]int{"a": 0, "b": 1}
	t, ok := to[o.To]
	if !ok || o.LogDir == "" {
		return res, &device.Refusal{Why: "switch needs --to a|b and --log DIR"}
	}
	r, done, err := startABRun(e, o.Device, o.LogDir, "switch", out, res)
	if err != nil {
		return res, err
	}
	r.o = RepairOptions{Yes: o.Yes, Confirm: o.Confirm, DryRun: o.DryRun}
	r.cut.Hooks = o.Hooks
	r.log(map[string]any{"event": "environment", "ram_token": r.s.token, "boot_id": r.s.bootID, "to": o.To, "dry_run": o.DryRun})
	return res, done(r.runSwitch(t, o.Untested))
}

// knownSwitch is the gate of the switch to a slot that is not tested yet
// (I-2): slot t holds exactly one known firmware's uboot, trust, dtbo,
// vbmeta and boot (a mainline or an unknown chain never does), and the other
// slot is successful or holds one too. It reads only.
func (r *repairRun) knownSwitch(cur []byte, t int) error {
	s, d := r.s, r.s.d
	a, _ := avbab.Parse(cur)
	ms, err := s.e.Manifests()
	if err != nil {
		return fmt.Errorf("the installer's manifests: %v", err)
	}
	view := d.View()
	defer view.Close()
	name, other := avbab.SlotName(t), avbab.SlotName(1-t)
	m, why := plan.KnownChain(view, d.Table, name, ms, true)
	if m == nil {
		return fmt.Errorf("slot %s is not tested on this unit, and its boot chain is not one known firmware (%s): no switch to it", strings.ToUpper(name), why)
	}
	s.say("switch: slot %s is not tested yet; its boot chain is %s (uboot, trust, dtbo, vbmeta, boot as its manifest)", strings.ToUpper(name), m.Name())
	r.log(map[string]any{"event": "known-chain", "slot": name, "firmware": m.Name()})
	if !a.Slots[1-t].Keeper() {
		om, owhy := plan.KnownChain(view, d.Table, other, ms, false)
		if om == nil {
			return fmt.Errorf("slot %s is trying and its boot chain is not one known firmware (%s): it would be no proven fallback", strings.ToUpper(other), owhy)
		}
		s.say("switch: the fallback slot %s is trying; its boot chain is %s", strings.ToUpper(other), om.Name())
		r.log(map[string]any{"event": "known-chain", "slot": other, "firmware": om.Name()})
	}
	return nil
}

func (r *repairRun) runSwitch(t int, untested bool) error {
	s, d, res := r.s, r.s.d, r.res
	name, from := avbab.SlotName(t), strings.ToUpper(avbab.SlotName(1-t))
	up := strings.ToUpper(name)
	if pids, err := s.e.BootctlRuns(); err != nil || len(pids) > 0 {
		return r.refuse("uc-pxx-bootctl-inst still runs (pid %s; %v): wait until it ends", strings.Join(pids, " "), err)
	}
	cur, rerr := d.ReadABFresh()
	if rerr != nil {
		return r.refuse("the A/B sector cannot be read (%v): an unreadable sector is never written", rerr)
	}
	res.Before = cur
	r.saveSector("ab-before", cur)
	r.log(map[string]any{"event": "sector", "sector": hexOrNil(cur), "sha256": sha256hex(cur)})
	s.say("switch: A/B sector now %x (%s)", cur[:32], plan.State(first32(cur)))
	want, perr := plan.PredictSwitch(cur, t)
	if untested {
		want, perr = plan.PredictSwitchKnown(cur, t)
	}
	if err := r.statusAgrees(cur); err != nil {
		return r.refuse("%v", err)
	}
	if untested && perr == nil && want != nil {
		if err := r.knownSwitch(cur, t); err != nil {
			s.say("SWITCH: refused: %v", err)
			return r.refuse("%v", err)
		}
	}
	if perr != nil {
		s.say("SWITCH: refused: %v", perr)
		return r.refuse("%v", perr)
	}
	if want == nil {
		res.After = cur
		s.say("SWITCH: not-needed")
		s.say("SWITCH-SAFE: yes: %s", plan.State(first32(cur)))
		s.say("")
		s.say("RESULT: no switch needed: slot %s boots next already, slot %s is the fallback; it is safe to power off.", up, from)
		return nil
	}
	s.say("SWITCH: to slot %s: would write %x (%s)", up, want[:32], plan.State(first32(want)))
	// the tool's dry run must be the prediction
	br, err := d.Bootctl(device.SwitchArgsKnown(name, cur, true, untested))
	if err != nil {
		return r.refuse("uc-pxx-bootctl-inst switch --dry-run: %v", err)
	}
	r.log(map[string]any{"event": "bootctl", "args": "switch --to " + name + map[bool]string{true: " --untested"}[untested] + " --dry-run",
		"rc": br.RC, "stdout": br.Stdout, "stderr": br.Stderr})
	if line := fmt.Sprintf("switch: would-write %x\n", want[:32]); br.RC != 0 || br.Stdout != line {
		return r.refuse("the tool's dry run (exit %d, %q) is not the installer's prediction %x", br.RC, strings.TrimSpace(br.Stdout), want[:32])
	}
	if r.o.DryRun {
		res.Would, res.After = want, cur
		s.say("")
		s.say("RESULT: switch possible (dry run, nothing written): slot %s would boot next, slot %s the fallback", up, from)
		return nil
	}
	if !r.o.Yes {
		s.say("")
		s.say("This writes the A/B sector: slot %s boots next, slot %s is the fallback.", up, from)
		fmt.Fprintf(s.out, "Type SWITCH and press Enter to write, anything else to stop: ")
		if !confirmed(r.o.Confirm, "SWITCH") {
			return r.refuse("not confirmed")
		}
	}
	r.o.Yes = true
	if err := r.confirm(); err != nil { // the signals are ignored from here
		return err
	}
	if again, err := d.ReadABFresh(); err != nil || !bytes.Equal(again, cur) {
		return r.refuse("the A/B sector changed after it was checked (%v)", err)
	}
	if err := d.CanWriteAB(); err != nil {
		return r.refuse("the tool cannot write: %v", err)
	}
	if err := r.cut.Before(nil); err != nil {
		return err
	}
	s.say("SWITCH-WRITE: 1: uc-pxx-bootctl-inst switch --to %s (the A/B sector is written now: do not power off)", name)
	br, rerr = d.Bootctl(device.SwitchArgsKnown(name, cur, false, untested))
	res.Writes++
	if errors.Is(rerr, install.ErrPowerCut) {
		return rerr
	}
	if cerr := r.cut.After(install.Event{Op: "ab", Partition: "misc", Off: plan.ABLBA * gpt.Sector, Len: gpt.Sector}); cerr != nil {
		return cerr
	}
	rc := -1
	if rerr == nil {
		rc = br.RC
	}
	back, berr := d.ReadABFresh()
	res.After = back
	r.log(map[string]any{"event": "write", "attempt": 1, "rc": rc, "want": hexOrNil(want), "read_back": hexOrNil(back), "read_error": errString(berr)})
	switch {
	case berr != nil:
		return r.unknown("the read-back after the write failed (%v)", berr)
	case bytes.Equal(back, want):
		if err := r.statusAgrees(back); err != nil {
			return r.unknown("the sector is the prediction, but %v", err)
		}
		s.say("SWITCH-WRITE: 1: read back %x = the prediction (eMMC cache flushed, page caches dropped, O_DIRECT)", back[:32])
		s.say("SWITCH-SAFE: yes: %s", plan.State(first32(back)))
		s.say("")
		s.say("RESULT: switched: A/B now %s: slot %s boots next, slot %s is the fallback; it is safe to power off.",
			plan.State(first32(back)), up, from)
		return nil
	case bytes.Equal(back, cur):
		// the first sector reads back byte for byte after a cache flush
		return r.refuse("the write did not take (the tool's exit %d); the sector is the one read first", rc)
	}
	return r.unknown("the read-back %x is not the prediction %x (the tool's exit %d); no second write", back[:32], want[:32], rc)
}
