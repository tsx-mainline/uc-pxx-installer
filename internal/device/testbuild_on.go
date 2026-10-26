//go:build install && devtest

package device

// A test build (go build -tags install,devtest): NewForTest accepts a test
// root. ci/check-no-writer.py checks that a release install binary does not
// hold testBuildMarker, and that a devtest binary does.
const testBuild = true

const testBuildMarker = "p10inst devtest build, test roots allowed"
