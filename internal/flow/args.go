package flow

import "uc-pxx-installer/internal/plan"

// Paths are the files of one flow run (in /run/p10inst/flow on the unit).
type Paths struct {
	Exe        string // the p10inst binary of the image (/usr/bin/p10inst)
	Drive      string // the drive file
	DriveIdent string // its identity when the firmware was read (drivestate.Ident)
	PlanJSON   string // the plan of the plan child, shown on the plan screen
	Log        string // the JSON-lines log of the install; the transcript is Log with .out for .jsonl
}

// PlanArgs is the command line of the plan child. It writes nothing to the
// eMMC (devmode.Plan).
func PlanArgs(p Paths, keeper string, prereqs, unclear bool) []string {
	a := []string{p.Exe, "plan", "--device", "/dev/mmcblk0", "--fw-drive", p.Drive, "--fw-drive-expect", p.DriveIdent,
		"--json", p.PlanJSON}
	return append(a, common(keeper, prereqs, unclear)...)
}

// InstallArgs is the command line of the install child: the device mode with
// the plan that was shown (--plan: it runs only when the plan computed again
// has the same id), the confirmation given by the hold of OK (--yes), and the
// log. The keeper and the confirmations are the ones of the plan child, so
// the same plan id comes out.
func InstallArgs(p Paths, keeper string, prereqs, unclear bool) []string {
	a := []string{p.Exe, "install", "--device", "/dev/mmcblk0", "--fw-drive", p.Drive, "--fw-drive-expect", p.DriveIdent,
		"--plan", p.PlanJSON, "--yes", "--log", p.Log}
	return append(a, common(keeper, prereqs, unclear)...)
}

func common(keeper string, prereqs, unclear bool) []string {
	var a []string
	if keeper == "a" || keeper == "b" {
		a = append(a, "--keeper", keeper)
	}
	if prereqs {
		a = append(a, "--prereqs-confirmed")
	}
	if unclear {
		a = append(a, "--autoupdate-unclear-confirmed")
	}
	return a
}

// Transcript is the transcript file of an install log (devmode: the log
// name with .out for .jsonl). The flow never writes to it.
func Transcript(log string) string {
	if len(log) > 6 && log[len(log)-6:] == ".jsonl" {
		return log[:len(log)-6] + ".out"
	}
	return log + ".out"
}

// StepsOf are the steps of a plan for the progress view.
func StepsOf(p *plan.Plan) []StepView {
	var out []StepView
	for _, s := range p.Steps {
		v := StepView{N: s.N, Op: s.Op}
		switch s.Op {
		case "bootctl":
			v.Label = "bootctl " + join(s.Bootctl.Args)
		case "write":
			v.Label, v.Bytes = "write "+s.Write.Partition, s.Write.Length
		case "skip":
			v.Label = "skip " + s.Write.Partition + " (it holds the image already)"
		case "check-ab":
			v.Op, v.Label = "check", "check the A/B sector"
		default:
			v.Label = s.Op
		}
		out = append(out, v)
	}
	return out
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
