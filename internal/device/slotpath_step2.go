//go:build install && step2 && !finish

package device

// SlotBootctlPath is uc-pxx-bootctl-inst r2 in a step-2 root file system
// (uc-pxx-linux UC_PXX_STEP2_BOOTCTL_INST: root, 0700, folder 0700).
const SlotBootctlPath = "/usr/libexec/uc-pxx-step2/uc-pxx-bootctl-inst"
