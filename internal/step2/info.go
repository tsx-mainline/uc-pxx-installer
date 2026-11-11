//go:build install && step2

package step2

// The info file of the USB interface (mainline-rc-4; review STEP2-USB-REVIEW
// F14): what the step-2 service and the finish executor tell the page
// through the receiver's HELLO/STATUS. Root writes it (0644 in a 0755
// folder: the receiver's user reads it) from a fixed set of this package's
// own strings: the step code (handover.Info.Step2: lowercase, digits, space,
// ":._-", at most 64 characters; anything else is dropped by the receiver)
// and short lines. Never the engine's output verbatim, never a serial, a MAC,
// a key or a factory byte: the lines below are fixed texts, a plan id, a slot
// letter, or the first line of a refusal of this package (whose texts name
// partitions, slots and states only).

import (
	"regexp"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/xfer/handover"
)

var infoCode = regexp.MustCompile(`^[a-z0-9][a-z0-9 :._-]{0,63}$`)

// writeInfo writes the info file of o (none: nothing). A code that the
// receiver would drop is a programming error: it is written as "busy".
func writeInfo(e *device.Env, o Options, code string, lines ...string) {
	if o.InfoFile == "" {
		return
	}
	if !infoCode.MatchString(code) {
		code = "busy"
	}
	var keep []string
	for _, l := range lines {
		if len(l) > 200 {
			l = l[:200]
		}
		keep = append(keep, printable(l))
	}
	in := handover.BaseInfo(e.Path("/"), code, keep)
	handover.WriteInfo(e.Path(o.InfoFile), in)
}

func printable(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c < 0x20 || c > 0x7e {
			b[i] = '?'
		}
	}
	return string(b)
}
