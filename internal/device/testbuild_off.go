//go:build install && !devtest

package device

// A release build: NewForTest always fails, and no test option is compiled
// in (the compiler drops the code behind the constant).
const testBuild = false

const testBuildMarker = ""
