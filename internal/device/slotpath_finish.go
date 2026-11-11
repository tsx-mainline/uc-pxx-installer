//go:build install && step2 && finish

package device

// SlotBootctlPath of the finish build (uc-pxx-step2-finish; mainline-rc-4,
// owner Q-T8 = option 5): r2 as the uc-pxx-usb service of a normal build
// sealed it in RAM (internal/xfer/handover FinishDir + FinishBootctl: root,
// 0700, folder 0700). The same reviewed sha256 20aa6bc5… is required there.
const SlotBootctlPath = "/run/uc-pxx-finish/uc-pxx-bootctl-inst"
