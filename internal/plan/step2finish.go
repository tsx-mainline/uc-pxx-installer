package plan

// The finish of mainline step 2 (MAINLINE-STEP2-DESIGN.md §3 rows 11-12, §12
// item 4; MAINLINE-INT-2026-11-10.md task 4). After step 2 armed B (K = a, T = b)
// and B booted and marked itself successful, the finish runs in B:
//
//   - F1 (read only): the UBOOT-RKSS-1024 §6.6 re-read of LBA 0-8, 64-392,
//     1024-1279 and 1088-1416 must equal the record that the step-2 plan made
//     before its first write (OtherSlotPlan.RKSS); any difference stops
//     (detect and stop, no write);
//   - F2: `uc-pxx-bootctl-inst switch --to a --expect-sector <the 512 bytes
//     just read>` (the reviewed switch form), only when B is successful and
//     the slot that boots, and A successful: A{15,0,1} B{14,0,1} after it.
//
// K = b (the direct path's step 2: B installs A) needs no switch: A was armed
// first and marks itself, which is already the end state; its finish is F1.
//
// The result is one of four (review T4-2 MINOR-1): "switch" (F2 is planned),
// "done" (the end state already), "not-yet" (T is still trying: its own mark
// has not run; ask again at the next boot), and "STOP" (an RKSS range, the
// keeper's static parts, the layout or the kept key changed, or the A/B
// state is not one step 2 can leave: keep the unit powered, do not restart,
// write nothing more, ask the owner). F1 is decided first and on every boot
// of T, marked or not: a STOP is never hidden behind a "not yet".

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/gpt"
)

// RKSSRanges are the ranges of the detect-and-stop re-read (UBOOT-RKSS-1024
// §6.6), as "first-last" LBA (inclusive).
var RKSSRanges = [][2]int64{{0, 8}, {64, 392}, {1024, 1279}, {1088, 1416}}

// ReadRKSS hashes the four ranges (read only).
func ReadRKSS(r io.ReaderAt) (map[string]string, error) {
	out := map[string]string{}
	for _, rg := range RKSSRanges {
		h, err := hashRange(r, rg[0]*gpt.Sector, (rg[1]-rg[0]+1)*gpt.Sector)
		if err != nil {
			return nil, fmt.Errorf("LBA %d-%d: %w", rg[0], rg[1], err)
		}
		out[fmt.Sprintf("%d-%d", rg[0], rg[1])] = h
	}
	return out, nil
}

// RKSSMagic is the header magic of U-Boot's RKSS backup copy
// (UBOOT-RKSS-1024 §2: 0x55667788).
const RKSSMagic = 0x55667788

// RKSSMagicAt1024 reports whether the sector LBA 1024 holds the RKSS header
// magic, in either byte order, at any 4-byte-aligned offset (a conservative
// test: UBOOT-RKSS-1024 §6.6 "if LBA 1024 already holds the magic, STOP
// before the boot" of a new U-Boot).
func RKSSMagicAt1024(r io.ReaderAt) (bool, error) {
	sec := make([]byte, gpt.Sector)
	if _, err := r.ReadAt(sec, 1024*gpt.Sector); err != nil {
		return false, err
	}
	for i := 0; i+4 <= len(sec); i += 4 {
		le := uint32(sec[i]) | uint32(sec[i+1])<<8 | uint32(sec[i+2])<<16 | uint32(sec[i+3])<<24
		be := uint32(sec[i+3]) | uint32(sec[i+2])<<8 | uint32(sec[i+1])<<16 | uint32(sec[i])<<24
		if le == RKSSMagic || be == RKSSMagic {
			return true, nil
		}
	}
	return false, nil
}

// rkssMagicCheck is the precondition of a plan with a write step of
// uboot_T or trust_T (review T4-2 NIT-3: any write step, also of equal bytes
// in ComputeMainline/ComputeMainlineDirect, whose uboot action is the
// manifest's "write"; the other-slot planner skips an equal part, so there it
// fires only when the part differs). The first boot of that U-Boot in its
// slot is the RKSS case.
func rkssMagicCheck(c *checker, r io.ReaderAt, steps []Step, target string) {
	writes := false
	for _, s := range steps {
		writes = writes || s.Op == "write" && s.Write != nil && (s.Write.Partition == "uboot_"+target || s.Write.Partition == "trust_"+target)
	}
	if !writes {
		return
	}
	m, err := RKSSMagicAt1024(r)
	c.add("rkss-magic", err == nil && !m,
		"the plan writes uboot_%s/trust_%s: LBA 1024 must not hold the RKSS header magic 0x%08x before the first boot of that U-Boot (UBOOT-RKSS-1024 §6.6: stop): magic %v %s",
		target, target, RKSSMagic, m, errText(err, ""))
}

// Step2FinishKind is the kind of a finish plan.
const Step2FinishKind = "p10inst-mainline-step2-finish"

// The results of a finish.
const (
	FinishSwitch = "switch"
	FinishDone   = "done"
	FinishNotYet = "not-yet"
	FinishStop   = "STOP"
)

// ErrFinishStop is a finish that found a change it must not go past: keep
// the unit powered, do not restart, ask the owner. ErrFinishNotYet is a
// finish whose target has not marked itself yet. Both come wrapped in
// ErrRefused.
var (
	ErrFinishStop   = errors.New("STOP: keep the unit powered, do not restart, write nothing more, ask the owner")
	ErrFinishNotYet = errors.New("not yet: the step-2 target has not marked itself")
)

// FinishInput is what ComputeStep2Finish needs. The planner reads the disk
// itself (the GPT, the RKSS ranges, the keeper's static parts, the two
// key-record sectors and the A/B sector): no hash comes from the caller
// (review T4-2 NIT-6).
type FinishInput struct {
	Running RunningSlot
	// Disk is the whole eMMC (the slot side reads it O_DIRECT); DiskBytes
	// its size.
	Disk      io.ReaderAt
	DiskBytes int64
	// Sector is the A/B sector the caller read just before (512 bytes, with
	// the caches flushed): the switch's --expect-sector. It must be the
	// sector of Disk.
	Sector []byte
	// Status is uc-pxx-bootctl status of that sector (nil: not run).
	Status *BootctlStatus
	// Step2 is the step-2 plan that armed this slot (its RKSS record, its
	// target, its payload token, its keeper hashes, its key pin).
	Step2 *OtherSlotPlan
	// KeyPins are the key records accepted (nil: the release's); the set
	// must be the step-2 plan's.
	KeyPins   []KeyPin
	KeyPinSet string
	// allowDryRunPlan accepts a step-2 plan of a dry run: tests only, set
	// through export_test.go (review T4-2 NIT-4).
	allowDryRunPlan bool
}

// FinishPlan is the finish of step 2: the read-only checks and the one switch.
type FinishPlan struct {
	Schema  int          `json:"schema"`
	Kind    string       `json:"kind"`
	ID      string       `json:"id"`
	Step2ID string       `json:"step2_plan"`
	Running RunningSlot  `json:"running"`
	Checks  []Check      `json:"checks"`
	Switch  *BootctlStep `json:"switch,omitempty"`
	RKSS    []string     `json:"rkss_compared"`
	// Result is FinishSwitch, FinishDone, FinishNotYet or FinishStop.
	Result string `json:"result"`
	// Done: the end state already (Result FinishDone): nothing to write.
	Done bool `json:"done"`
	// SectorSHA256 is the sha256 of the 512-byte A/B sector the plan was
	// made from (the switch's --expect-sector).
	SectorSHA256 string `json:"sector_sha256"`
}

// Seal sets the ID (the sha256 of the plan without the ID).
func (p *FinishPlan) Seal() {
	p.ID = ""
	b, _ := json.Marshal(p)
	s := sha256.Sum256(b)
	p.ID = hex.EncodeToString(s[:])
}

// VerifyID recomputes the ID.
func (p *FinishPlan) VerifyID() bool {
	q := *p
	q.ID = ""
	b, _ := json.Marshal(&q)
	s := sha256.Sum256(b)
	return p.ID != "" && hex.EncodeToString(s[:]) == p.ID
}

// ComputeStep2Finish makes the finish plan. A refusal is ErrRefused, wrapped
// with ErrFinishStop or ErrFinishNotYet when it is one of those; the plan then
// has no switch and its Result says which.
func ComputeStep2Finish(in FinishInput) (*FinishPlan, error) {
	c := &checker{}
	p := &FinishPlan{Schema: Schema, Kind: Step2FinishKind, Running: in.Running}
	end := func(result string, why error) (*FinishPlan, error) {
		p.Checks, p.Switch, p.Result = c.list, nil, result
		if why == nil {
			return p, fmt.Errorf("%w: %s", ErrRefused, strings.Join(c.failed(), "; "))
		}
		return p, fmt.Errorf("%w: %w: %s", ErrRefused, why, strings.Join(c.failed(), "; "))
	}
	refuse := func() (*FinishPlan, error) { return end("", nil) }
	s2 := in.Step2
	if !c.add("step2-plan", s2 != nil && s2.Kind == OtherSlotKind && s2.VerifyID() && (s2.Keeper == "a" && s2.Target == "b" ||
		s2.Keeper == "b" && s2.Target == "a") && (s2.Mode == "slot" || in.allowDryRunPlan),
		"the step-2 plan that armed this slot must be a sealed other-slot plan of a slot run (keeper a target b, or keeper b target a)") {
		return refuse()
	}
	p.Step2ID = s2.ID
	T, K := s2.Target, s2.Keeper
	ti, ki := slotIndex(T), slotIndex(K)
	ru := in.Running
	c.add("running-slot", ru.UBootArgs && !ru.RAMImage && ru.SlotWords == 1 && ru.Slot == T,
		"the finish runs from a slot boot of the step-2 target %s (u-boot,bootargs %v, %d slot words, slot %q, RAM image %v)", strings.ToUpper(T),
		ru.UBootArgs, ru.SlotWords, ru.Slot, ru.RAMImage)
	c.add("running-token", ru.Token != "" && s2.Mainline != nil && ru.Token == s2.Mainline.Manifest.Token,
		"the running kernel is the step-2 payload's (token %q, the payload's %q)", ru.Token, tokenOf(s2))
	c.add("running-root", ru.Root == "system_"+T, "the root file system is %s, want system_%s", orNone(ru.Root), T)
	c.add("running-boot-id", ru.BootID != "", "the finish is bound to this boot (boot id %q)", ru.BootID)
	if len(c.failed()) > 0 || in.Disk == nil {
		c.add("disk", in.Disk != nil, "the eMMC is read by the planner")
		return refuse()
	}

	// F1, on every boot of T (marked or not): the layout, the keeper's
	// static parts, the kept key, the four RKSS ranges. Any difference is a
	// STOP.
	t, gerr := gpt.Parse(in.Disk, in.DiskBytes)
	c.add("layout", gerr == nil && t.LayoutSHA256() == s2.Disk.LayoutSHA256 && t.DiskSectors == s2.Disk.Sectors,
		"the GPT now (%s) is the step-2 plan's layout (%s)", errText(gerr, "read"), shortSHA(s2.Disk.LayoutSHA256))
	if gerr != nil {
		return end(FinishStop, ErrFinishStop)
	}
	var kbad []string
	for _, part := range KeeperStaticParts() {
		n := part + "_" + K
		e, ok := t.Find(n)
		h := ""
		if ok {
			h, _ = hashRange(in.Disk, e.Start*gpt.Sector, e.Bytes())
		}
		if s2.KeeperSHA256[n] == "" || h != s2.KeeperSHA256[n] {
			kbad = append(kbad, n)
		}
	}
	c.add("keeper-unchanged", len(kbad) == 0, "the step-2 keeper's uboot, trust, dtbo, vbmeta and boot are the plan's (read now)%s",
		listText(": changed or not hashed: ", kbad, ""))
	key := ReadKeyGate(in.Disk, t)
	pins, set := in.KeyPins, in.KeyPinSet
	if pins == nil {
		pins, set = ReleaseKeyPins(), KeyPinSetRelease
	}
	MatchKeyPin(&key, pins, set)
	kok, kwhy := key.KeyPinned()
	c.add("key-kept", kok && key.RecordSHA256 == s2.Key.RecordSHA256 && key.PinSet == s2.Key.PinSet,
		"after step 2 only the kept record holds the key: %s; the step-2 plan's record %s (%s pins)", kwhy, shortSHA(s2.Key.RecordSHA256), s2.Key.PinSet)
	rk, rerr := ReadRKSS(in.Disk)
	var bad, keys []string
	for k := range s2.RKSS {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, rg := range RKSSRanges {
		k := fmt.Sprintf("%d-%d", rg[0], rg[1])
		if s2.RKSS[k] == "" || rerr != nil || rk[k] != s2.RKSS[k] {
			bad = append(bad, k)
		}
	}
	p.RKSS = keys
	c.add("rkss", len(s2.RKSS) == len(RKSSRanges) && len(bad) == 0,
		"LBA 0-8, 64-392, 1024-1279 and 1088-1416 must read as before step 2 (UBOOT-RKSS-1024 §6.6, detect and stop)%s", listText(": changed or not recorded: ", bad, ""))
	if len(c.failed()) > 0 {
		return end(FinishStop, ErrFinishStop)
	}

	// The A/B state.
	disk := make([]byte, gpt.Sector)
	_, derr := in.Disk.ReadAt(disk, ABLBA*gpt.Sector)
	if !c.add("ab-read", len(in.Sector) == gpt.Sector && derr == nil && bytes.Equal(disk, in.Sector),
		"the A/B sector given (%d bytes) is the sector of the disk now (%s)", len(in.Sector), errText(derr, "read")) {
		return end(FinishStop, ErrFinishStop)
	}
	cur, _ := avbab.Parse(in.Sector)
	var now [32]byte
	copy(now[:], in.Sector)
	ss := sha256.Sum256(in.Sector)
	p.SectorSHA256 = hex.EncodeToString(ss[:])
	if err := cur.Validate(); !c.add("ab-valid", err == nil, "%s", errText(err, "valid; both slots legal")) {
		return end(FinishStop, ErrFinishStop)
	}
	if in.Status == nil {
		c.add("bootctl-status", false, "uc-pxx-bootctl status was not run")
	} else if err := in.Status.Agrees(cur); err != nil {
		c.add("bootctl-status", false, "%v", err)
	} else {
		c.add("bootctl-status", true, "uc-pxx-bootctl status agrees with the installer's decoder")
	}
	if len(c.failed()) > 0 {
		return end(FinishStop, ErrFinishStop)
	}
	// exactly the step-2 plan's final state with T marked by its own boot (T
	// {prio, 0, 1}, K as armed); the end state of a finish made before (K =
	// a); T still trying with K as armed (not yet); anything else stops
	// (review T4 MINOR-2, T4-2 NIT-6)
	fin, ferr := s2.AB.Final.Raw()
	fa, _ := avbab.Parse(fin[:])
	marked := fa.Slots
	marked[ti] = avbab.Slot{Priority: fa.Slots[ti].Priority, Tries: 0, Successful: 1}
	endS := marked
	endS[0], endS[1] = avbab.Slot{Priority: 15, Successful: 1}, avbab.Slot{Priority: 14, Successful: 1}
	isMarked := ferr == nil && cur.Slots == marked
	isEnd := ferr == nil && K == "a" && cur.Slots == endS
	ts := cur.Slots[ti]
	notYet := ferr == nil && cur.Slots[ki] == fa.Slots[ki] && ts.Priority == fa.Slots[ti].Priority && ts.Successful == 0 &&
		ts.Tries >= 1 && ts.Tries <= fa.Slots[ti].Tries
	c.add("ab-state", isMarked || isEnd,
		"the A/B state %s must be the step-2 plan's final state %s with slot %s marked successful by its own boot (%s %v, %s %v)%s",
		State(now), s2.AB.Final, strings.ToUpper(T), strings.ToUpper(T), marked[ti], strings.ToUpper(K), marked[ki],
		map[bool]string{true: ", or the end state A{15,0,1} B{14,0,1} of a finish made before", false: ""}[K == "a"])
	// T marked itself and boots; K is the successful fallback (r2's switch
	// itself accepts a trying other slot: the finish does not)
	c.add(T+"-successful", cur.Slots[ti].Keeper() && !cur.Tie() && (cur.Pick() == ti || isEnd),
		"slot %s (the step-2 target) must be successful and the slot that boots (or, after a finish made before, the fallback): %s",
		strings.ToUpper(T), State(now))
	c.add(K+"-successful", cur.Slots[ki].Keeper(), "slot %s must be successful (the fallback): %s", strings.ToUpper(K), State(now))
	switch {
	case notYet:
		return end(FinishNotYet, ErrFinishNotYet)
	case len(c.failed()) > 0:
		return end(FinishStop, ErrFinishStop)
	}
	if K == "b" || isEnd {
		// the direct path's step 2: A was armed first and marked itself: the
		// end state A first, B fallback; the finish is the read-only F1 check
		// after the first boot of the userdebug U-Boot in A (review T4
		// MAJOR-1). Or K = a after a finish that switched already.
		if !c.add("no-switch", cur.Pick() == 0 && !cur.Tie() && cur.Slots[0].Keeper() && cur.Slots[1].Keeper(),
			"slot A boots first already with slot B successful: no switch") {
			return end(FinishStop, ErrFinishStop)
		}
		p.Done, p.Result = true, FinishDone
		p.Checks = c.list
		p.Seal()
		return p, nil
	}
	after, err := PredictSwitch(in.Sector, 0)
	if err != nil || after == nil {
		c.add("switch-predict", false, "%v", err)
		return end(FinishStop, ErrFinishStop)
	}
	var a32 [32]byte
	copy(a32[:], after)
	aa, _ := avbab.Parse(after)
	if !c.add("switch-predict", aa.Validate() == nil && aa.Pick() == 0 && aa.Slots[0].Keeper() && aa.Slots[1].Keeper() &&
		aa.Slots[0].Priority == 15 && aa.Slots[1].Priority == 14 && bytes.Equal(after[32:], in.Sector[32:]),
		"after the switch A{15,0,1} B{14,0,1}, A first, both successful, bytes 32..511 unchanged: %s", State(a32)) {
		return end(FinishStop, ErrFinishStop)
	}
	p.Switch = &BootctlStep{Args: []string{"switch", "--to", "a", "--expect-sector", hex.EncodeToString(in.Sector)}, Before: State(now), After: State(a32)}
	p.Result = FinishSwitch
	p.Checks = c.list
	p.Seal()
	return p, nil
}

func tokenOf(p *OtherSlotPlan) string {
	if p == nil || p.Mainline == nil {
		return ""
	}
	return p.Mainline.Manifest.Token
}
