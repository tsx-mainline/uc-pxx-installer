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
// first and marks itself, which is already the end state. The finish refuses
// it.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// Step2FinishKind is the kind of a finish plan.
const Step2FinishKind = "p10inst-mainline-step2-finish"

// FinishInput is what ComputeStep2Finish needs.
type FinishInput struct {
	Running RunningSlot
	// Sector is the A/B sector read now (512 bytes).
	Sector []byte
	// Status is uc-pxx-bootctl status of that sector (nil: not run).
	Status *BootctlStatus
	// Step2 is the step-2 plan that armed this slot (its RKSS record, its
	// target, its payload token).
	Step2 *OtherSlotPlan
	// RKSSNow is ReadRKSS of the unit now.
	RKSSNow map[string]string
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

// ComputeStep2Finish makes the finish plan, or refuses (ErrRefused, no switch).
func ComputeStep2Finish(in FinishInput) (*FinishPlan, error) {
	c := &checker{}
	p := &FinishPlan{Schema: Schema, Kind: Step2FinishKind, Running: in.Running}
	refuse := func() (*FinishPlan, error) {
		p.Checks, p.Switch = c.list, nil
		return p, fmt.Errorf("%w: %s", ErrRefused, strings.Join(c.failed(), "; "))
	}
	s2 := in.Step2
	if !c.add("step2-plan", s2 != nil && s2.Kind == OtherSlotKind && s2.VerifyID() && s2.Keeper == "a" && s2.Target == "b",
		"the step-2 plan that armed this slot must be a sealed other-slot plan of keeper a, target b (the K = b finish needs no switch)") {
		return refuse()
	}
	p.Step2ID = s2.ID
	ru := in.Running
	c.add("running-slot", ru.UBootArgs && !ru.RAMImage && ru.SlotWords == 1 && ru.Slot == "b",
		"the finish runs from a slot boot of B (u-boot,bootargs %v, %d slot words, slot %q, RAM image %v)", ru.UBootArgs, ru.SlotWords, ru.Slot, ru.RAMImage)
	c.add("running-token", ru.Token != "" && s2.Mainline != nil && ru.Token == s2.Mainline.Manifest.Token,
		"the running kernel is the step-2 payload's (token %q, the payload's %q)", ru.Token, tokenOf(s2))
	c.add("running-root", ru.Root == "system_b", "the root file system is %s, want system_b", orNone(ru.Root))
	if len(c.failed()) > 0 {
		return refuse()
	}
	if len(in.Sector) != gpt.Sector {
		c.add("ab-read", false, "the A/B sector was not read")
		return refuse()
	}
	cur, _ := avbab.Parse(in.Sector)
	var now [32]byte
	copy(now[:], in.Sector)
	if err := cur.Validate(); !c.add("ab-valid", err == nil, "%s", errText(err, "valid; both slots legal")) {
		return refuse()
	}
	if in.Status == nil {
		c.add("bootctl-status", false, "uc-pxx-bootctl status was not run")
	} else if err := in.Status.Agrees(cur); err != nil {
		c.add("bootctl-status", false, "%v", err)
	} else {
		c.add("bootctl-status", true, "uc-pxx-bootctl status agrees with the installer's decoder")
	}
	// B marked itself and boots; A is the successful fallback (r2's switch
	// itself accepts a trying other slot: the finish does not)
	c.add("b-successful", cur.Slots[1].Keeper() && !cur.Tie() && cur.Pick() == 1, "slot B must be successful and the slot that boots: %s", State(now))
	c.add("a-successful", cur.Slots[0].Keeper(), "slot A must be successful (the fallback): %s", State(now))
	// F1: detect and stop
	var bad, keys []string
	for k := range s2.RKSS {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, rg := range RKSSRanges {
		k := fmt.Sprintf("%d-%d", rg[0], rg[1])
		if s2.RKSS[k] == "" || in.RKSSNow[k] != s2.RKSS[k] {
			bad = append(bad, k)
		}
	}
	p.RKSS = keys
	c.add("rkss", len(s2.RKSS) == len(RKSSRanges) && len(bad) == 0,
		"LBA 0-8, 64-392, 1024-1279 and 1088-1416 must read as before step 2 (UBOOT-RKSS-1024 §6.6, detect and stop)%s", listText(": changed or not recorded: ", bad, ""))
	if len(c.failed()) > 0 {
		return refuse()
	}
	after, err := PredictSwitch(in.Sector, 0)
	if err != nil {
		c.add("switch-predict", false, "%v", err)
		return refuse()
	}
	var a32 [32]byte
	copy(a32[:], after)
	aa, _ := avbab.Parse(after)
	c.add("switch-predict", aa.Validate() == nil && aa.Pick() == 0 && aa.Slots[0].Keeper() && aa.Slots[1].Keeper() &&
		aa.Slots[0].Priority == 15 && aa.Slots[1].Priority == 14,
		"after the switch A{15,0,1} B{14,0,1}, A first, both successful: %s", State(a32))
	if len(c.failed()) > 0 {
		return refuse()
	}
	p.Switch = &BootctlStep{Args: []string{"switch", "--to", "a", "--expect-sector", hex.EncodeToString(in.Sector)}, Before: State(now), After: State(a32)}
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
