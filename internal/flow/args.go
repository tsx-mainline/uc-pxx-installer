package flow

import (
	"strings"

	"uc-pxx-installer/internal/plan"
)

// Paths are the files of one flow run (in /run/p10inst/flow on the unit).
type Paths struct {
	Exe        string // the p10inst binary of the image (/usr/bin/p10inst)
	Drive      string // the drive file
	DriveIdent string // its identity when the firmware was read (drivestate.Ident)
	PlanJSON   string // the plan of the plan child, shown on the plan screen
	Log        string // the JSON-lines log of the install; the transcript is Log with .out for .jsonl
	// Keys is the key file that the flow copied from the drive
	// (p10-keys.txt, §1.2), "" when the drive has none: the device mode then
	// searches the libraries in system_a and system_b of the eMMC.
	Keys string
}

// PlanArgs is the command line of the plan child. It writes nothing to the
// eMMC (devmode.Plan).
func PlanArgs(p Paths, keeper string, prereqs, unclear, panel bool) []string {
	a := []string{p.Exe, "plan", "--device", "/dev/mmcblk0", "--fw-drive", p.Drive, "--fw-drive-expect", p.DriveIdent,
		"--json", p.PlanJSON}
	return append(a, common(p, keeper, prereqs, unclear, panel)...)
}

// InstallArgs is the command line of the install child: the device mode with
// the plan that was shown (--plan: it runs only when the plan computed again
// has the same id), the confirmation given by the hold of OK (--yes), and the
// log. The keeper and the confirmations are the ones of the plan child, so
// the same plan id comes out.
func InstallArgs(p Paths, keeper string, prereqs, unclear, panel bool) []string {
	a := []string{p.Exe, "install", "--device", "/dev/mmcblk0", "--fw-drive", p.Drive, "--fw-drive-expect", p.DriveIdent,
		"--plan", p.PlanJSON, "--yes", "--log", p.Log}
	return append(a, common(p, keeper, prereqs, unclear, panel)...)
}

func common(p Paths, keeper string, prereqs, unclear, panel bool) []string {
	var a []string
	if p.Keys != "" {
		a = append(a, "--keys", p.Keys)
	}
	if keeper == "a" || keeper == "b" {
		a = append(a, "--keeper", keeper)
	}
	if prereqs {
		a = append(a, "--prereqs-confirmed")
	}
	if panel {
		a = append(a, "--panel-confirmed") // the user sees this screen on the unit's panel
	}
	if unclear {
		a = append(a, "--autoupdate-unclear-confirmed")
	}
	return a
}

// RepairArgs is the command line of the repair of the hold (devmode-r4,
// FLOW.md §14): the plan of this install, the run folder for its log, and
// --dry-run (reads only) or --yes (after the 3 s hold of OK).
func RepairArgs(exe, planJSON, logDir string, dry bool) []string {
	mode := "--yes"
	if dry {
		mode = "--dry-run"
	}
	return []string{exe, "repair", "--device", "/dev/mmcblk0", "--plan", planJSON, "--log", logDir, mode}
}

// SwitchArgs is the command line of the slot switch: the slot that is to
// start next, the run folder, --dry-run or --yes.
func SwitchArgs(exe, to, logDir string, dry bool) []string {
	mode := "--yes"
	if dry {
		mode = "--dry-run"
	}
	return []string{exe, "switch", "--device", "/dev/mmcblk0", "--to", to, "--log", logDir, mode}
}

// Transcript is the transcript file of an install log (devmode: the log
// name with .out for .jsonl). The flow never writes to it.
func Transcript(log string) string {
	if len(log) > 6 && log[len(log)-6:] == ".jsonl" {
		return log[:len(log)-6] + ".out"
	}
	return log + ".out"
}

// StepsOf are the steps of a plan for the progress view, in plain words.
func StepsOf(p *plan.Plan) []StepView {
	var out []StepView
	t := slotWord(p.Target)
	for _, s := range p.Steps {
		v := StepView{N: s.N, Op: s.Op}
		switch s.Op {
		case "bootctl":
			switch {
			case len(s.Bootctl.Args) > 0 && s.Bootctl.Args[0] == "disable":
				v.Label = "Mark slot " + t + " as not bootable"
			case len(s.Bootctl.Args) > 0 && s.Bootctl.Args[0] == "arm-trying":
				v.Label = "Make slot " + t + " start next"
			default:
				v.Label = "Change the slot table"
			}
		case "write":
			v.Label, v.Bytes = "Write "+partWord(s.Write.Partition), s.Write.Length
		case "skip":
			v.Label = "Write " + partWord(s.Write.Partition) + " (already up to date)"
		case "sync":
			v.Label = "Finish the writes"
		case "verify":
			// the on-screen label is just "Verify"; the "re-read uboot_b …"
			// detail stays in the plan render and the log (R4 finding 5)
			v.Label = "Verify"
		case "check-ab":
			v.Op, v.Label = "check", "Check the slot table"
		case "keep-key":
			v.Label = "Keep the firmware key on the unit"
		default:
			v.Label = s.Op
		}
		out = append(out, v)
	}
	return out
}

// partWord is a partition of a slot in plain words.
func partWord(name string) string {
	base := name
	if i := strings.LastIndex(name, "_"); i > 0 {
		base = name[:i]
	}
	switch base {
	case "uboot":
		return "U-Boot"
	case "trust":
		return "the trust firmware"
	case "dtbo":
		return "the device tree"
	case "vbmeta":
		return "the boot check data"
	case "boot":
		return "the kernel"
	case "system":
		return "the system"
	case "vendor":
		return "the vendor files"
	case "oem":
		return "the Crestron files"
	}
	return name
}

func join(a []string) string {
	s := ""
	for i, x := range a {
		if i > 0 {
			s += " "
		}
		s += x
	}
	return s
}
