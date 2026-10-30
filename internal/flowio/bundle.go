package flowio

// The log bundle (INSTALLER-END-USER-PLAN.md §2.6, W10): a folder
// p10-install-<the first 12 hex of the plan id>-<UTC time>, with no serial
// number in its name or its files (U13; the eMMC CID only as the sha256 the
// plan holds). It is built from an allowlist of files, each made here from
// what the flow and the device mode wrote. It never holds the key, anything
// of cfg, an nvram key other than the three of the status, or firmware
// data. The serial number, the MAC address and the raw CID of the unit are
// cut out of every file, and a file that still holds one is replaced by a
// note. It goes to /run/p10inst/<folder> and onto the drive P10-INSTALL on
// Done, Stopped and Hold.

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/fatmk"
	"uc-pxx-installer/internal/flow"
	"uc-pxx-installer/internal/plan"
	"uc-pxx-installer/internal/status"
)

// Bundle is a built bundle.
type Bundle struct {
	Name  string
	Files []fatmk.File // paths relative to the folder
}

// BundleFiles is the allowlist of the files of a bundle (ID: the first 12
// hex of the plan id).
var BundleFiles = []string{"README.txt", "result.txt", "plan.json", "install-ID.jsonl", "install-ID.out",
	"child-plan.txt", "child-install.txt", "ab-sector.txt", "ab-steps.txt", "status-before.txt", "status-after.txt",
	"nvram.txt", "emmc.txt", "binaries.txt", "cmdline.txt", "dmesg.txt", "flow.log"}

// Allowed reports whether name is a file of the allowlist for the plan id.
func Allowed(name, id string) bool {
	for _, a := range BundleFiles {
		if strings.ReplaceAll(a, "ID", id) == name {
			return true
		}
	}
	return false
}

func hexDump(b []byte) string {
	var s strings.Builder
	for i := 0; i < len(b); i += 32 {
		j := min(i+32, len(b))
		fmt.Fprintf(&s, "  %04x  %s\n", i, hex.EncodeToString(b[i:j]))
	}
	return s.String()
}

func decode(b []byte) string {
	if len(b) < 32 {
		return "unreadable"
	}
	a, _ := avbab.Parse(b)
	var raw [32]byte
	copy(raw[:], b)
	v := "valid"
	if err := a.Validate(); err != nil {
		v = "NOT VALID: " + err.Error()
	}
	return plan.State(raw).String() + " (" + v + ")"
}

// statusText is the status report without the serial number and the MAC.
func statusText(r *status.Report) string {
	if r == nil {
		return "not read\n"
	}
	var b strings.Builder
	for _, l := range r.Lines() {
		t := strings.TrimSpace(l.Text)
		if strings.HasPrefix(t, "Serial ") || strings.HasPrefix(t, "MAC ") {
			continue // U13: no serial number in the bundle; no MAC either
		}
		b.WriteString(l.Text + "\n")
	}
	return b.String()
}

// abSteps lists the A/B state before and after each step from the JSON-lines
// log of the device mode.
func abSteps(jsonl []byte) string {
	var b strings.Builder
	sc := bufio.NewScanner(bytes.NewReader(jsonl))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var ev map[string]any
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		show := func(k string) string {
			h, _ := ev[k].(string)
			raw, err := hex.DecodeString(h)
			if err != nil || len(raw) != 32 {
				return h
			}
			return h + "  " + decode(raw)
		}
		switch {
		case ev["event"] == "start":
			fmt.Fprintf(&b, "start          %s\n", show("ab"))
		case ev["step"] != nil && ev["ab_before"] != nil:
			fmt.Fprintf(&b, "step %v %-8v before %s\n", ev["step"], ev["op"], show("ab_before"))
			if ev["ab_after"] != nil {
				fmt.Fprintf(&b, "step %v %-8v after  %s\n", ev["step"], ev["op"], show("ab_after"))
			}
			if e, _ := ev["error"].(string); e != "" {
				fmt.Fprintf(&b, "step %v error: %s\n", ev["step"], e)
			}
			if g, _ := ev["guard"].(string); g != "" {
				fmt.Fprintf(&b, "step %v stopped before the step: %s\n", ev["step"], g)
			}
		}
	}
	return b.String()
}

// identifiers are the strings of the unit that must not be in the bundle.
func (r *Runner) identifiers() []string {
	var ids []string
	if r.report != nil {
		ids = append(ids, r.report.Serial, r.report.MAC)
	}
	if r.statusB != nil {
		ids = append(ids, r.statusB.Serial, r.statusB.MAC)
	}
	if b, err := os.ReadFile(r.path("/sys/class/block/mmcblk0/device/cid")); err == nil {
		ids = append(ids, strings.TrimSpace(string(b)))
	}
	var out []string
	for _, s := range ids {
		s = strings.TrimSpace(s)
		if len(s) >= 4 && !strings.HasPrefix(s, "unknown") && s != "not set" {
			out = append(out, s)
			if strings.Contains(s, ":") { // a MAC also without the colons
				out = append(out, strings.ReplaceAll(s, ":", ""))
			}
		}
	}
	return out
}

// scrub cuts the identifiers out of every file. It reports the files that
// still hold one after that (none is expected) and replaces them.
func scrub(files []fatmk.File, ids []string) []string {
	var bad []string
	for i := range files {
		d := files[i].Data
		for _, id := range ids {
			rx := regexp.MustCompile("(?i)" + regexp.QuoteMeta(id))
			d = rx.ReplaceAll(d, []byte("<removed>"))
		}
		for _, id := range ids {
			if bytes.Contains(bytes.ToLower(d), bytes.ToLower([]byte(id))) {
				bad = append(bad, files[i].Path)
				d = []byte("removed: this file held an identifier of the unit\n")
				break
			}
		}
		files[i].Data = d
	}
	return bad
}

func readDmesg() []byte {
	out, err := exec.Command("dmesg").Output()
	if err != nil {
		return []byte("dmesg: " + err.Error() + "\n")
	}
	return out
}

func (r *Runner) bundle(ch *child, p *plan.Plan, o flow.Outcome, before, after []byte, abErr error, bcbAfter []byte,
	ro string, roErr error, driveNote string) Bundle {
	now := r.c.Now().UTC()
	id := "noplan"
	if p != nil && len(p.ID) >= 12 {
		id = p.ID[:12]
	}
	b := Bundle{Name: fmt.Sprintf("p10-install-%s-%s", id, now.Format("20060102T150405Z"))}
	add := func(name string, data []byte) { b.Files = append(b.Files, fatmk.File{Path: name, Data: data}) }
	add("README.txt", []byte("The log of one install of the UC-P10 installer (RAM image).\n"+
		"result.txt says how it ended. It holds no serial number, no key and no firmware data.\n"+
		flow.Contact+"\n"))
	var res strings.Builder
	fmt.Fprintf(&res, "screen: %s\nA/B state: %s\nwhy: %s\nend of the install child: %s\n", o.Screen, o.State, o.Why, o.End)
	fmt.Fprintf(&res, "read-only flags set again: %q (%v)\ndrive: %s\n", ro, roErr, driveNote)
	fmt.Fprintf(&res, "check started %s, ended %s; install started %s, ended %s (UTC)\n",
		r.st.PlanStart.UTC().Format("15:04:05"), r.st.PlanEnd.UTC().Format("15:04:05"),
		r.st.Started.UTC().Format("15:04:05"), now.Format("15:04:05"))
	if p != nil {
		fmt.Fprintf(&res, "plan %s: keeper %s, target %s\n", p.ID, p.Keeper, p.Target)
	}
	add("result.txt", []byte(res.String()))
	if pj, err := os.ReadFile(r.st.PlanJSON); err == nil {
		add("plan.json", pj)
	}
	// the names of the device mode's own files: --log install-ID.jsonl, and
	// its transcript next to it, install-ID.out (§4.5); the flow never
	// writes into either
	jl, _ := os.ReadFile(r.st.Log)
	add(filepath.Base(r.st.Log), jl)
	if tr, err := os.ReadFile(flow.Transcript(r.st.Log)); err == nil {
		add(filepath.Base(flow.Transcript(r.st.Log)), tr)
	}
	if r.st.PlanChild != "" {
		add("child-plan.txt", (&child{dir: r.st.PlanChild}).output())
	}
	if ch != nil {
		add("child-install.txt", ch.output())
	}
	var ab strings.Builder
	bcbBefore, _ := hex.DecodeString(r.st.BCBBefore)
	fmt.Fprintf(&ab, "A/B sector (eMMC LBA %d), read by the flow with O_DIRECT\nbefore the install: %s\n%s", avbab.LBA, decode(before), hexDump(before))
	if abErr != nil {
		fmt.Fprintf(&ab, "after the install: unreadable: %v\n", abErr)
	} else {
		fmt.Fprintf(&ab, "after the install: %s\n%s", decode(after), hexDump(after))
	}
	fmt.Fprintf(&ab, "\nBCB (misc LBA 49184, 4 sectors)\nbefore the install:\n%safter the install:\n%s", hexDump(bcbBefore), hexDump(bcbAfter))
	add("ab-sector.txt", []byte(ab.String()))
	add("ab-steps.txt", []byte(abSteps(jl)))
	add("status-before.txt", []byte(statusText(r.statusB)))
	add("status-after.txt", []byte(statusText(r.report)))
	if r.report != nil {
		au := r.report.AutoUpdate
		add("nvram.txt", []byte(fmt.Sprintf("auto update firmware (72005): %s\nauto update APK (72012): %s\nupdate URL (72016): %s\nactive part %d, write counter %d, valid parts %d\n%s\n",
			au.State, au.APK, au.URL, au.Part, au.Counter, au.Valid, au.Why)))
	}
	if p != nil && p.Device != nil {
		add("emmc.txt", []byte(fmt.Sprintf("node %s\nmodel %s\ncontroller %s\nCID sha256 %s (the CID itself is not in the bundle)\nsysfs layout sha256 %s\nGPT layout sha256 %s, %d sectors\n",
			p.Device.Node, p.Device.Model, p.Device.Host, p.Device.CIDSHA256, p.Device.SysfsLayoutSHA256, p.Disk.LayoutSHA256, p.Disk.Sectors)))
	}
	marker, _ := os.ReadFile(r.path("/etc/p10inst/ram-image"))
	add("binaries.txt", []byte(fmt.Sprintf("p10inst %s  %s\nuc-pxx-bootctl-inst %s\nram-image marker:\n%s",
		fileSHA(r.c.Exe), r.c.Exe, fileSHA(r.path("/usr/libexec/p10inst/uc-pxx-bootctl-inst")), marker)))
	cl, _ := os.ReadFile(r.path("/proc/cmdline"))
	add("cmdline.txt", cl)
	add("dmesg.txt", readDmesg())
	add("flow.log", []byte(strings.Join(r.log, "\n")+"\n"))
	var kept []fatmk.File
	for _, f := range b.Files {
		if Allowed(f.Path, id) {
			kept = append(kept, f)
		} else {
			r.say("bundle: %s is not in the allowlist: left out", f.Path)
		}
	}
	b.Files = kept
	if bad := scrub(b.Files, r.identifiers()); len(bad) > 0 {
		r.say("bundle: %v held an identifier of the unit and were replaced", bad)
	}
	return b
}

// writeBundle puts the bundle into /run/p10inst/<name> and onto the drive,
// and gives the drive back.
func (r *Runner) writeBundle(b Bundle) {
	runCopy := r.path(RunDir + "/" + b.Name)
	var errs []string
	for _, f := range b.Files {
		if err := writeRunFile(r.path(RunDir), filepath.Join(runCopy, f.Path), f.Data, 0o644); err != nil {
			errs = append(errs, err.Error())
		}
	}
	var onDrive []fatmk.File
	for _, f := range b.Files {
		onDrive = append(onDrive, fatmk.File{Path: b.Name + "/" + f.Path, Data: f.Data})
	}
	derr := makeDriveFile(r.lun(), r.drive(), onDrive, r.c.Now())
	if derr == nil {
		derr = attachDrive(r.path(GadgetDir), r.drive())
	}
	where := "the drive P10-INSTALL, folder " + b.Name + " (also in " + RunDir + "/" + b.Name + ")"
	if derr != nil {
		where = RunDir + "/" + b.Name + " only (the drive: " + derr.Error() + ")"
	}
	r.post(func() {
		r.say("bundle %s: %s %v", b.Name, where, errs)
		r.M.BundleDone(where)
		r.save()
	})
}
