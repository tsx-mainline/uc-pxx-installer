//go:build install && step2 && finish

package step2

// finishCmd is the command that runs the supervised finish in this build: the
// finish executor of a normal build, from /run/uc-pxx-finish.
const finishCmd = "uc-pxx-step2-finish"
