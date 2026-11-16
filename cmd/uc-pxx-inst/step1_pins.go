//go:build install

package main

// uc-pxx-inst step1-pins [--out FILE] (mainline-rc-10, I-6): the pins of
// the step-1 USB service of the RAM installer (`uc-pxx-xfer inst`), from the
// installer's own data: the firmware files that the firmware gate offers
// (manifests/*.json, install_status open: their exact size and sha256) and
// the manifest sha256s of every mainline payload that the RAM installer's
// gate takes (mainline.GateManifests of step2set.Gate). The receiver takes
// only these, as a drive's .bin and bundle pass the same gates. The install
// image's rcS runs it once, as root, before `uc-pxx-xfer inst`. It reads only.

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/mainline/step2set"
	"uc-pxx-installer/internal/manifest"
	"uc-pxx-installer/internal/xfer/handover"
)

func init() {
	extraCmds["step1-pins"] = cmdStep1Pins
	extraUsage += "  uc-pxx-inst step1-pins [--out FILE]   the pins of the step-1 USB receiver (reads only)\n"
}

// step1Pins is the pins of the embedded data.
func step1Pins() (*handover.InstPins, error) {
	ms, err := manifest.Embedded()
	if err != nil {
		return nil, err
	}
	p := &handover.InstPins{Bundle: mainline.GateManifests(step2set.Gate(mainline.EmbeddedGate()))}
	for _, m := range ms {
		if m.Offered() {
			p.FW = append(p.FW, handover.FWPin{Size: m.Firmware.Size, SHA256: m.Firmware.SHA256})
		}
	}
	if len(p.FW) == 0 {
		return nil, fmt.Errorf("no firmware is offered by the manifests")
	}
	if p.Bundle == nil {
		p.Bundle = []string{}
	}
	return p, nil
}

func cmdStep1Pins(args []string) {
	fs := flag.NewFlagSet("step1-pins", flag.ExitOnError)
	out := fs.String("out", "", "write the pins file here (root 0644); default: stdout")
	fs.Parse(args)
	if fs.NArg() != 0 {
		usage()
	}
	p, err := step1Pins()
	if err != nil {
		fail(1, "%v", err)
	}
	b, _ := json.MarshalIndent(p, "", " ")
	b = append(b, '\n')
	if *out == "" {
		os.Stdout.Write(b)
		return
	}
	tmp := filepath.Join(filepath.Dir(*out), "."+filepath.Base(*out)+".tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		fail(1, "%v", err)
	}
	if err := os.Rename(tmp, *out); err != nil {
		fail(1, "%v", err)
	}
}
