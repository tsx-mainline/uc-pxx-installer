package install

import (
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/plan"
)

// slotKindMedium is the dry medium reporting the kind "slot", so a test
// reaches the slot branch of the other-slot precheck (the boot-id binding;
// review T4 NIT-2, T4-2 NIT-2).
type slotKindMedium struct{ *dryMedium }

func (slotKindMedium) Kind() string { return "slot" }

// ExecuteOtherSlotAsSlot runs a plan on the image through a medium of kind
// "slot".
func ExecuteOtherSlotAsSlot(p *plan.OtherSlotPlan, ml *mainline.Payload, im *Image, opt OtherSlotOptions) (*Result, error) {
	return RunOtherSlot(p, ml, slotKindMedium{&dryMedium{im: im, tool: opt.Bootctl}}, opt)
}
