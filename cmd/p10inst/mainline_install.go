//go:build install

package main

// The mainline install (MAINLINE-INSTALL-PLAN.md, phase 4): p10inst plan and
// p10inst install with a mainline bundle (payload contract v1) in place of an
// official firmware.
//
//	p10inst plan    --image FILE --mainline DIR --bootctl PATH [--bootctl-exec RUNNER] [--pre-ab HEX]
//	                [--prereqs-confirmed] [--autoupdate-unclear-confirmed] [--unlock-confirmed]
//	                [--hash-keeper] [--json OUT] [--tmp DIR]
//	p10inst install --dry-run --image FILE --mainline DIR (the same options) [--plan PLAN.json] [--log LOG]
//	p10inst plan    --device /dev/mmcblk0 (--fw-drive DRIVE.img [--fw-drive-expect ID] | --mainline DIR)
//	                [--prereqs-confirmed] [--panel-confirmed] [--autoupdate-unclear-confirmed]
//	                [--unlock-confirmed] [--json OUT]
//	p10inst install --device /dev/mmcblk0 (the same options) [--plan PLAN.json] [--yes] [--log LOG]
//
// The mode is chosen by what is given, before any flag is parsed
// (mainlineSelected): --mainline DIR; or --fw-drive DRIVE.img whose top
// folder holds the folder uc-pxx-mainline; or --plan FILE of the kind
// p10inst-mainline-plan. Everything else runs the official commands of
// install.go and device_install.go exactly as before: this file registers
// "plan" and "install" again (its init runs after the one of install.go, the
// files are initialized in name order) with a dispatcher that calls cmdPlan
// and cmdInstall unchanged.

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"uc-pxx-installer/internal/bootctl"
	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/devmode"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/mainline/mldrive"
	"uc-pxx-installer/internal/manifest"
	"uc-pxx-installer/internal/plan"
)

const mainlineToolVersion = "p10inst install-path dry-run, mainline 2026-11-09"

const mainlineUsage = `install build, mainline (a uc-pxx-mainline bundle in place of a firmware; slot A gets mainline Linux,
slot B keeps Android userdebug as the fallback):
  p10inst plan      --image FILE --mainline DIR --bootctl PATH [--bootctl-exec RUNNER] [--pre-ab HEX]
                    [--prereqs-confirmed] [--autoupdate-unclear-confirmed] [--unlock-confirmed]
                    [--hash-keeper] [--json OUT]
  p10inst install   --dry-run --image FILE --mainline DIR (same options) [--plan PLAN.json] [--log LOG.jsonl]
  p10inst plan      --device /dev/mmcblk0 (--fw-drive DRIVE.img [--fw-drive-expect ID] | --mainline DIR)
                    [--prereqs-confirmed] [--panel-confirmed] [--autoupdate-unclear-confirmed]
                    --unlock-confirmed [--json OUT]
  p10inst install   --device /dev/mmcblk0 (the same options) [--plan PLAN.json] [--yes] [--log LOG.jsonl]
                    (the mainline mode is chosen by --mainline, by a drive whose top folder holds
                    uc-pxx-mainline/, or by a mainline plan file; --unlock-confirmed: the unit is unlocked)
`

func init() {
	extraCmds["plan"] = cmdPlanDispatch
	extraCmds["install"] = cmdInstallDispatch
	extraUsage += mainlineUsage
}

// boolFlags are the flags of plan and install (the official, the mainline
// and the devtest ones) that take no value: package flag never takes the
// next argument as their value. Every other flag takes the next argument
// when it is not given as --name=value. TestScanFlagsBools holds this list
// to the flag sets. (A switch, not a map: no package variable, so main.init
// stays as it is.)
func boolFlags(name string) bool {
	switch name {
	case "prereqs-confirmed", "panel-confirmed", "autoupdate-unclear-confirmed", "unlock-confirmed", "hash-keeper", "dry-run", "yes",
		"test-torn", "test-fake-unit", "test-fixture-manifests":
		return true
	}
	return false
}

// scanFlags reads the flags of args as package flag does: one or two
// leading dashes, --name=value or --name value (a bool flag takes no
// value), and it stops at "--", at "-" and at the first argument that is not
// a flag. It gives each flag's last value ("" for a bool flag without one).
// A flag's value is never read as a flag, nor an argument after the flags
// (review of 01b3841, finding 2: hasFlag of install.go also matched a value,
// as in --log mainline).
func scanFlags(args []string) map[string]string {
	out := map[string]string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) < 2 || a[0] != '-' {
			break
		}
		name := a[1:]
		if name[0] == '-' {
			name = name[1:]
		}
		if name == "" || name[0] == '-' || name[0] == '=' {
			break // "--", or a bad flag (package flag refuses it)
		}
		v, eq := "", false
		if j := strings.IndexByte(name, '='); j >= 0 {
			name, v, eq = name[:j], name[j+1:], true
		}
		if !eq && !boolFlags(name) && i+1 < len(args) {
			i++
			v = args[i]
		}
		out[name] = v
	}
	return out
}

// mlHasFlag reports the flag -name or --name in args (scanFlags).
func mlHasFlag(args []string, name string) bool {
	_, ok := scanFlags(args)[name]
	return ok
}

// flagValue is the value of -name/--name in args (scanFlags): the last one
// wins; "" if not given.
func flagValue(args []string, name string) string { return scanFlags(args)[name] }

// mainlineSelected reports whether args ask for the mainline mode: --mainline
// DIR; a drive (--fw-drive) whose top folder holds the folder uc-pxx-mainline
// (read-only; a drive that cannot be read goes to the official command, which
// reports it); or a plan file (--plan) of the mainline kind.
func mainlineSelected(args []string) bool {
	if mlHasFlag(args, "mainline") {
		return true
	}
	if d := flagValue(args, "fw-drive"); d != "" {
		if top, err := mldrive.TopOfImage(d); err == nil && top.Bundle {
			return true
		}
	}
	if p := flagValue(args, "plan"); p != "" {
		if k, err := devmode.PlanKind(p); err == nil && k == plan.MainlineKind {
			return true
		}
	}
	return false
}

func cmdPlanDispatch(args []string) {
	if mainlineSelected(args) {
		cmdMainlinePlan(args)
		return
	}
	cmdPlan(args)
}

func cmdInstallDispatch(args []string) {
	if mainlineSelected(args) {
		cmdMainlineInstall(args)
		return
	}
	cmdInstall(args)
}

func cmdMainlinePlan(args []string) {
	if mlHasFlag(args, "device") {
		cmdMainlineDevice(args, false)
		return
	}
	a := newMLPlanArgs("plan", false)
	a.parse(args)
	f, err := os.Open(a.image)
	if err != nil {
		fail(2, "%v", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		fail(2, "%s is not a regular file: plan works on image files only in this build", a.image)
	}
	if _, err := a.build(f, st.Size()); err != nil {
		fmt.Println("\nRESULT: refused, nothing would be written")
		os.Exit(1)
	}
	fmt.Println("\nRESULT: plan OK")
}

func cmdMainlineInstall(args []string) {
	if mlHasFlag(args, "device") {
		if mlHasFlag(args, "dry-run") || mlHasFlag(args, "image") {
			fail(2, "give --device or --dry-run --image, not both")
		}
		cmdMainlineDevice(args, true)
		return
	}
	a := newMLPlanArgs("install", true)
	a.parse(args)
	if !a.dry {
		fail(2, "install needs --dry-run --image FILE, or --device /dev/mmcblk0 (see p10inst help)")
	}
	im, err := install.OpenImage(a.image)
	if err != nil {
		fail(2, "%v", err)
	}
	defer im.Close()
	b, err := a.build(im.Reader(), im.Size())
	if err != nil {
		fmt.Println("\nRESULT: refused, nothing written")
		os.Exit(1)
	}
	defer b.ml.Close()
	if a.planIn != "" {
		raw, err := os.ReadFile(a.planIn)
		if err != nil {
			fail(2, "%v", err)
		}
		var want plan.MainlinePlan
		if err := json.Unmarshal(raw, &want); err != nil {
			fail(2, "%s: %v", a.planIn, err)
		}
		if want.Kind != plan.MainlineKind || want.ID == "" || !want.VerifyID() {
			fmt.Printf("\nRESULT: refused: %s is not a mainline plan whose content matches its id %s (an edited plan?); nothing written\n", a.planIn, want.ID)
			os.Exit(1)
		}
		if want.ID != b.p.ID {
			fmt.Printf("\nRESULT: refused: the plan now has id %s, %s has %s; nothing written\n", b.p.ID, a.planIn, want.ID)
			os.Exit(1)
		}
		fmt.Printf("\nthe plan matches %s (id %s)\n", a.planIn, want.ID)
	}
	var logw io.Writer
	if a.logO != "" {
		lf, err := os.OpenFile(a.logO, os.O_WRONLY|os.O_CREATE|os.O_APPEND|syscall.O_NOFOLLOW, 0o644)
		if err != nil {
			fail(2, "%v", err)
		}
		defer lf.Close()
		logw = lf
	}
	var hooks *install.Hooks
	if a.cutAfter >= 0 {
		hooks = &install.Hooks{CutAfter: a.cutAfter, Torn: a.torn}
	}
	fmt.Printf("\ndry run: executing the mainline plan on %s\n", a.image)
	res, err := install.ExecuteMainline(b.p, b.ml, im, install.Options{Bootctl: b.bt, Log: logw, Hooks: hooks,
		Say: func(s string) { fmt.Println(s) }})
	if err != nil {
		fmt.Printf("\nSTOP: %v\n", err)
		fmt.Printf("A/B now: %s\n", install.SafeState(im, &b.p.Plan))
		if res != nil {
			fmt.Printf("writes done: %d\n", res.Writes)
		}
		os.Exit(1)
	}
	fmt.Printf("\nRESULT: dry-run install done, %d writes, every write read back and verified\n", res.Writes)
	fmt.Printf("A/B now: %s\n", install.SafeState(im, &b.p.Plan))
}

// mlPlanArgs are the flags of the mainline dry run: those of the official
// dry run without the firmware and key flags, with --mainline and
// --unlock-confirmed.
type mlPlanArgs struct {
	fs                                                 *flag.FlagSet
	image, dir, bc, bcExec, keeper, preAB, jsonO, keys string
	tmp, planIn, logO                                  string
	prereqs, auUnclear, unlock, hashKeeper, dry, torn  bool
	cutAfter                                           int
	gate                                               func() *mainline.Gate // devtest only (nil in a release build)
}

func newMLPlanArgs(name string, inst bool) *mlPlanArgs {
	a := &mlPlanArgs{fs: flag.NewFlagSet(name, flag.ExitOnError)}
	f := a.fs
	f.StringVar(&a.image, "image", "", "image file that stands for the eMMC")
	f.StringVar(&a.dir, "mainline", "", "the mainline bundle folder (uc-pxx-mainline/)")
	f.StringVar(&a.bc, "bootctl", "", "uc-pxx-bootctl binary")
	f.StringVar(&a.bcExec, "bootctl-exec", "", "runner for the bootctl binary, for example qemu-aarch64-static")
	f.StringVar(&a.keeper, "keeper", "", "the keeper slot: only b (a mainline install always keeps slot B)")
	f.StringVar(&a.keys, "keys", "", "not for a mainline install: refused")
	f.StringVar(&a.preAB, "pre-ab", "", "the A/B sector before fastboot entry, hex")
	f.BoolVar(&a.prereqs, "prereqs-confirmed", false, "the user turned off automatic firmware update")
	f.BoolVar(&a.auUnclear, "autoupdate-unclear-confirmed", false, "the Android auto-update setting is not clear, and the user confirmed to go on")
	f.BoolVar(&a.unlock, "unlock-confirmed", false, "the user confirmed that the unit is unlocked (the unlock tool's read-back)")
	f.BoolVar(&a.hashKeeper, "hash-keeper", false, "record the sha256 of every keeper partition in the plan")
	f.StringVar(&a.jsonO, "json", "", "write the plan as JSON to this file (- = stdout)")
	f.StringVar(&a.tmp, "tmp", "", "directory for the A/B sector copies")
	if inst {
		f.BoolVar(&a.dry, "dry-run", false, "required: write to the image file")
		f.StringVar(&a.planIn, "plan", "", "run only if the computed plan has the id of this mainline plan file")
		f.StringVar(&a.logO, "log", "", "write a JSON-lines log here")
		f.IntVar(&a.cutAfter, "test-cut-after", -1, "test: stop before primitive write N+1, as a power cut")
		f.BoolVar(&a.torn, "test-torn", false, "test: with --test-cut-after, write half of the cut write first")
	}
	a.gate = mainlineTestGate(f)
	return a
}

// payloadGate is the release's gate (mainline.EmbeddedGate), or in a devtest
// build the test gate of --test-review-bundle.
func payloadGate(f func() *mainline.Gate) *mainline.Gate {
	if f != nil {
		if g := f(); g != nil {
			return g
		}
	}
	return mainline.EmbeddedGate()
}

func (a *mlPlanArgs) parse(args []string) {
	a.fs.Parse(args)
	if a.fs.NArg() != 0 || a.image == "" || a.dir == "" || a.bc == "" {
		usage()
	}
	if a.keys != "" {
		fmt.Println("\nRESULT: refused: a mainline install takes no firmware key (--keys): it reads no firmware container; nothing written")
		os.Exit(1)
	}
	if a.planIn != "" {
		if err := devmode.CheckInputFile(a.planIn); err != nil {
			fail(2, "%v", err)
		}
	}
	for _, p := range []string{a.jsonO, a.logO} {
		if p != "" && p != "-" {
			if err := devmode.CheckOutputPath(p); err != nil {
				fail(2, "%v", err)
			}
		}
	}
}

type mlBuilt struct {
	p  *plan.MainlinePlan
	ml *mainline.Payload
	bt bootctl.Tool
}

func (a *mlPlanArgs) build(r io.ReaderAt, size int64) (*mlBuilt, error) {
	ms, err := manifest.Embedded()
	if err != nil {
		fail(2, "manifests: %v", err)
	}
	src, err := mainline.DirSource(a.dir)
	if err != nil {
		fail(2, "--mainline: %v", err)
	}
	fmt.Println("pass 1: check the mainline bundle (" + src.Describe() + "), hashed in place (nothing is written)")
	ml, err := mainline.Verify(src)
	if err != nil {
		fail(1, "pass 1: %v", err)
	}
	fmt.Println("  " + ml.Pass1)
	bt := bootctl.Tool{Path: a.bc, Exec: a.bcExec, TmpDir: a.tmp}
	info, err := bt.Info()
	if err != nil {
		fail(2, "bootctl: %v", err)
	}
	sector := make([]byte, gpt.Sector)
	if _, err := r.ReadAt(sector, plan.ABLBA*gpt.Sector); err != nil {
		fail(2, "read the A/B sector: %v", err)
	}
	st, res, err := bt.Status(sector)
	if err != nil {
		msg := ""
		if res != nil {
			msg = strings.TrimSpace(res.Stderr)
		}
		fmt.Printf("uc-pxx-bootctl status: %v %s\n", err, msg)
	}
	var pre []byte
	if a.preAB != "" {
		if pre, err = hexBytes(a.preAB); err != nil {
			fail(2, "--pre-ab: %v", err)
		}
	}
	p, err := plan.ComputeMainline(plan.MainlineInput{Input: plan.Input{Disk: r, DiskBytes: size, Source: "image:" + a.image,
		Mode: "dry-run", Tool: mainlineToolVersion, PreFastboot: pre, Keeper: a.keeper, Bootctl: info, BootctlStatus: st,
		PrereqsConfirmed: a.prereqs, AutoUpdateUnclearConfirmed: a.auUnclear, HashKeeper: a.hashKeeper},
		Mainline: ml, Manifests: ms, UnlockConfirmed: a.unlock, Gate: payloadGate(a.gate)})
	fmt.Println()
	p.Render(os.Stdout)
	if a.jsonO != "" {
		b, _ := json.MarshalIndent(p, "", "  ")
		b = append(b, '\n')
		if a.jsonO == "-" {
			os.Stdout.Write(b)
		} else if werr := writeFileNoFollow(a.jsonO, b); werr != nil {
			fail(2, "%v", werr)
		}
	}
	if err != nil {
		ml.Close()
		return nil, err
	}
	return &mlBuilt{p: p, ml: ml, bt: bt}, nil
}

// cmdMainlineDevice is the mainline device mode: devmode.PlanMainline or
// devmode.InstallMainline. The devtest flags are those of the official
// device mode (devtestFlags).
func cmdMainlineDevice(args []string, inst bool) {
	name := map[bool]string{false: "plan", true: "install"}[inst]
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	var o devmode.MainlineOptions
	fs.StringVar(&o.Device, "device", "", "the eMMC: must be /dev/mmcblk0")
	fs.StringVar(&o.Mainline, "mainline", "", "the mainline bundle folder (instead of --fw-drive)")
	fs.StringVar(&o.FWDrive, "fw-drive", "", "the P10-INSTALL drive file: the bundle is its folder uc-pxx-mainline, read in place; the drive must stay detached and unchanged to the end")
	fs.StringVar(&o.FWDriveExpect, "fw-drive-expect", "", "with --fw-drive: the identity of the drive file when the bundle was read (size=…,mtime=…,ctime=…,dev=…,ino=…)")
	fs.StringVar(&o.Keeper, "keeper", "", "the keeper slot: only b (a mainline install always keeps slot B)")
	// --keys is not an input of a mainline install: it is defined only to be
	// refused with a RESULT line (checkMainlineOptions, exit 1), not with the
	// flag parser's exit 2 (review of 01b3841, INFO 6)
	fs.StringVar(&o.Keys, "keys", "", "not for a mainline install: refused")
	fs.BoolVar(&o.PrereqsConfirmed, "prereqs-confirmed", false, "the user turned off automatic firmware update")
	fs.BoolVar(&o.PanelConfirmed, "panel-confirmed", false, "the user sees the installer's screen on the unit's panel")
	fs.BoolVar(&o.AutoUpdateUnclearConfirmed, "autoupdate-unclear-confirmed", false, "the Android auto-update setting is not clear, and the user confirmed to go on")
	fs.BoolVar(&o.UnlockConfirmed, "unlock-confirmed", false, "the user confirmed that the unit is unlocked (the unlock tool's read-back)")
	fs.StringVar(&o.JSONOut, "json", "", "write the plan as JSON to this file")
	if inst {
		fs.StringVar(&o.PlanFile, "plan", "", "run only if the computed plan has the id of this mainline plan file")
		fs.BoolVar(&o.Yes, "yes", false, "the confirmation: do not ask for INSTALL")
		fs.StringVar(&o.LogPath, "log", "", "JSON-lines log (default /run/p10inst/install-ID.jsonl); the transcript goes next to it")
	}
	envf := devtestFlags(fs)
	gatef := mainlineTestGate(fs)
	fs.Parse(args)
	o.Gate = payloadGate(gatef)
	if fs.NArg() != 0 || o.Device == "" || (o.Mainline == "") == (o.FWDrive == "") || (o.FWDriveExpect != "" && o.FWDrive == "") {
		usage()
	}
	e := device.Live()
	if envf != nil {
		te, err := envf(&o.Options)
		if err != nil {
			fail(2, "%v", err)
		}
		if te != nil {
			e = te
		}
	}
	var err error
	if inst {
		o.Confirm = os.Stdin
		_, err = devmode.InstallMainline(e, o, os.Stdout)
	} else {
		_, err = devmode.PlanMainline(e, o, os.Stdout)
	}
	if err != nil {
		fmt.Printf("\nRESULT: %v\n", err)
		os.Exit(devmode.ExitCode(err))
	}
	if inst {
		fmt.Println("\nRESULT: install done")
	} else {
		fmt.Println("\nRESULT: plan OK (nothing written)")
	}
}
