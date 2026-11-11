//go:build install && step2 && !devtest

package step2

// A release build: every test option of Options is refused.
const testBuild = false
