//go:build install

// The install build: go build -tags install ./cmd/p10inst
//
//	p10inst plan       --image FILE --fw FW.bin [key source] --bootctl PATH [options]
//	p10inst install    --dry-run --image FILE --fw FW.bin [key source] --bootctl PATH [options]
//	p10inst fake-emmc  --layout FILE -o IMAGE --ab a=P,T,S,b=P,T,S [--fill NAME=FILE ...] [--markers]
//
// Every command here works on an image file that stands for the eMMC. There
// is no code path to a block device: the image must be a regular file, and
// install refuses to run without --dry-run. The status-only RAM image is
// built without this file.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/bootctl"
	"uc-pxx-installer/internal/ext4"
	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/manifest"
	"uc-pxx-installer/internal/pcdfw"
	"uc-pxx-installer/internal/plan"
)

const toolVersion = "p10inst install-path dry-run 2026-10-25"

func init() {
	extraCmds["plan"] = cmdPlan
	extraCmds["install"] = cmdInstall
	extraCmds["fake-emmc"] = cmdFakeEmmc
	extraUsage = `install build (image files only):
  p10inst plan      --image FILE --fw FW.bin [--keys F | --lib L ...] --bootctl PATH [--bootctl-exec RUNNER]
                    [--keeper a|b] [--pre-ab HEX] [--prereqs-confirmed] [--hash-keeper] [--json OUT]
  p10inst install   --dry-run --image FILE (same options) [--plan PLAN.json] [--log LOG.jsonl]
  p10inst fake-emmc --layout FILE -o IMAGE --ab a=P,T,S,b=P,T,S [--fill NAME=FILE ...] [--markers]
`
}

type planArgs struct {
	fs                                                *flag.FlagSet
	image, fw, keys, bc, bcExec, keeper, preAB, jsonO string
	tmp, planIn, logO                                 string
	libs                                              libList
	prereqs, hashKeeper, dry                          bool
	cutAfter                                          int
	torn                                              bool
}

func newPlanArgs(name string, inst bool) *planArgs {
	a := &planArgs{fs: flag.NewFlagSet(name, flag.ExitOnError)}
	f := a.fs
	f.StringVar(&a.image, "image", "", "image file that stands for the eMMC")
	f.StringVar(&a.fw, "fw", "", "firmware file (pcdfw)")
	f.StringVar(&a.keys, "keys", "", "key file (default: search the libraries in system_a/system_b of the image)")
	f.Var(&a.libs, "lib", "library to find the key in (repeat)")
	f.StringVar(&a.bc, "bootctl", "", "uc-pxx-bootctl binary")
	f.StringVar(&a.bcExec, "bootctl-exec", "", "runner for the bootctl binary, for example qemu-aarch64-static")
	f.StringVar(&a.keeper, "keeper", "", "the keeper slot (a or b), when the rules cannot choose")
	f.StringVar(&a.preAB, "pre-ab", "", "the A/B sector before fastboot entry, hex (as p10inst.ab=)")
	f.BoolVar(&a.prereqs, "prereqs-confirmed", false, "the user turned off automatic firmware update")
	f.BoolVar(&a.hashKeeper, "hash-keeper", false, "record the sha256 of every keeper partition in the plan")
	f.StringVar(&a.jsonO, "json", "", "write the plan as JSON to this file (- = stdout)")
	f.StringVar(&a.tmp, "tmp", "", "directory for the A/B sector copies")
	if inst {
		f.BoolVar(&a.dry, "dry-run", false, "required: write to the image file")
		f.StringVar(&a.planIn, "plan", "", "run only if the computed plan has the id of this plan file")
		f.StringVar(&a.logO, "log", "", "write a JSON-lines log here")
		f.IntVar(&a.cutAfter, "test-cut-after", -1, "test: stop before primitive write N+1, as a power cut")
		f.BoolVar(&a.torn, "test-torn", false, "test: with --test-cut-after, write half of the cut write first")
	}
	return a
}

func (a *planArgs) parse(args []string) {
	a.fs.Parse(args)
	if a.fs.NArg() != 0 || a.image == "" || a.fw == "" || a.bc == "" {
		usage()
	}
	if a.keys != "" && len(a.libs) > 0 {
		fail(2, "give --keys or --lib, not both")
	}
}

// keysFromImage reads the updater libraries from system_a and system_b of
// the image, read-only with the ext4 reader (design §5.8). A slot without
// them is skipped. It returns the key and the slots whose libraries give it.
func keysFromImage(r io.ReaderAt, size int64, fw *os.File, fwSize int64) (pcdfw.Keys, []string) {
	t, err := gpt.Parse(r, size)
	if err != nil {
		fail(1, "GPT: %v", err)
	}
	var found []pcdfw.Keys
	var slots []string
	for _, s := range []string{"a", "b"} {
		e, ok := t.Find("system_" + s)
		if !ok {
			continue
		}
		fsys, err := ext4.Open(io.NewSectionReader(r, e.Start*gpt.Sector, e.Bytes()), e.Bytes())
		if err != nil {
			fmt.Printf("key search: system_%s: no ext4 file system (%v), skipped\n", s, err)
			continue
		}
		var libs []pcdfw.Lib
		for _, lib := range []string{"/system/lib64/libwebupdate.so", "/system/lib64/libprovdetect.so"} {
			if b, err := fsys.ReadFile(lib, 64<<20); err == nil {
				libs = append(libs, pcdfw.Lib{Name: "system_" + s + lib, Data: b})
			}
		}
		if len(libs) == 0 {
			fmt.Printf("key search: system_%s: no updater library\n", s)
			continue
		}
		res, err := pcdfw.FindKeys(fw, fwSize, libs, false)
		if err != nil || len(res.Pairs) != 1 {
			fmt.Printf("key search: system_%s: %d libraries, %d key pairs: not used\n", s, len(libs), len(res.Pairs))
			continue
		}
		fmt.Printf("key search: system_%s: exactly one key/IV pair\n", s)
		found = append(found, res.Pairs[0])
		slots = append(slots, s)
	}
	switch {
	case len(found) == 0:
		fail(1, "key search: no updater library with the key in system_a or system_b; give --keys")
	case len(found) == 2 && found[0] != found[1]:
		fail(1, "key search: system_a and system_b give different keys, using none")
	}
	return found[0], slots
}

type built struct {
	p  *plan.Plan
	pl *plan.Payload
	bt bootctl.Tool
}

func (a *planArgs) build(r io.ReaderAt, size int64) (*built, error) {
	ms, err := manifest.Embedded()
	if err != nil {
		fail(2, "manifests: %v", err)
	}
	fw, fwSize := openFW(a.fw)
	var k pcdfw.Keys
	var keySlots []string
	keySource := ""
	switch {
	case a.keys != "":
		if k, err = pcdfw.LoadKeyFile(a.keys); err != nil {
			fail(2, "%v", err)
		}
		keySource = "key file"
	case len(a.libs) > 0:
		k = findOne(fw, fwSize, readLibs(a.libs), false)
		keySource = "libraries given with --lib"
	default:
		k, keySlots = keysFromImage(r, size, fw, fwSize)
	}
	fmt.Println("pass 1: check the whole firmware against the manifests (nothing is written)")
	pl, err := plan.VerifyOfficial(fw, fwSize, k, ms, func(s string) { fmt.Println("  " + s) })
	if err != nil {
		fail(1, "pass 1: %v", err)
	}
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
	p, err := plan.Compute(plan.Input{Disk: r, DiskBytes: size, Source: "image:" + a.image, Mode: "dry-run",
		Tool: toolVersion, PreFastboot: pre, Keeper: a.keeper, Payload: pl, Bootctl: info, BootctlStatus: st,
		PrereqsConfirmed: a.prereqs, HashKeeper: a.hashKeeper, KeySlots: keySlots, KeySource: keySource})
	fmt.Println()
	p.Render(os.Stdout)
	if a.jsonO != "" {
		b, _ := json.MarshalIndent(p, "", "  ")
		b = append(b, '\n')
		if a.jsonO == "-" {
			os.Stdout.Write(b)
		} else if werr := os.WriteFile(a.jsonO, b, 0o644); werr != nil {
			fail(2, "%v", werr)
		}
	}
	return &built{p: p, pl: pl, bt: bt}, err
}

func hexBytes(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if len(s)%2 != 0 {
		return nil, fmt.Errorf("odd length")
	}
	b := make([]byte, len(s)/2)
	for i := range b {
		v, err := strconv.ParseUint(s[2*i:2*i+2], 16, 8)
		if err != nil {
			return nil, err
		}
		b[i] = byte(v)
	}
	return b, nil
}

func cmdPlan(args []string) {
	a := newPlanArgs("plan", false)
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

func cmdInstall(args []string) {
	a := newPlanArgs("install", true)
	a.parse(args)
	if !a.dry {
		fail(2, "install needs --dry-run: this build has no real-device mode")
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
	if a.planIn != "" {
		raw, err := os.ReadFile(a.planIn)
		if err != nil {
			fail(2, "%v", err)
		}
		var want plan.Plan
		if err := json.Unmarshal(raw, &want); err != nil {
			fail(2, "%s: %v", a.planIn, err)
		}
		if want.ID != b.p.ID {
			fmt.Printf("\nRESULT: refused: the plan now has id %s, %s has %s; nothing written\n", b.p.ID, a.planIn, want.ID)
			os.Exit(1)
		}
		fmt.Printf("\nthe plan matches %s (id %s)\n", a.planIn, want.ID)
	}
	var logw io.Writer
	if a.logO != "" {
		lf, err := os.OpenFile(a.logO, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
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
	fmt.Printf("\ndry run: executing the plan on %s\n", a.image)
	res, err := install.Execute(b.p, b.pl, im, install.Options{Bootctl: b.bt, Log: logw, Hooks: hooks,
		Say: func(s string) { fmt.Println(s) }})
	if err != nil {
		fmt.Printf("\nSTOP: %v\n", err)
		fmt.Printf("A/B now: %s\n", install.SafeState(im, b.p))
		if res != nil {
			fmt.Printf("writes done: %d\n", res.Writes)
		}
		os.Exit(1)
	}
	fmt.Printf("\nRESULT: dry-run install done, %d writes, every write read back and verified\n", res.Writes)
	fmt.Printf("A/B now: %s\n", install.SafeState(im, b.p))
}

type fillList map[string]string

func (l fillList) String() string { return fmt.Sprint(map[string]string(l)) }
func (l fillList) Set(v string) error {
	n, f, ok := strings.Cut(v, "=")
	if !ok {
		return fmt.Errorf("want NAME=FILE")
	}
	l[n] = f
	return nil
}

func parseAB(s string) ([2][3]byte, error) {
	var out [2][3]byte
	as, bs, ok := strings.Cut(s, ",b=")
	as, ok2 := strings.CutPrefix(as, "a=")
	if !ok || !ok2 {
		return out, fmt.Errorf("want a=P,T,S,b=P,T,S")
	}
	for i, v := range []string{as, bs} {
		f := strings.Split(v, ",")
		if len(f) != 3 {
			return out, fmt.Errorf("slot %c needs P,T,S", "ab"[i])
		}
		for j := 0; j < 3; j++ {
			n, err := strconv.Atoi(f[j])
			if err != nil || n < 0 || n > 255 {
				return out, fmt.Errorf("slot %c: bad number %q", "ab"[i], f[j])
			}
			out[i][j] = byte(n)
		}
	}
	return out, nil
}

func cmdFakeEmmc(args []string) {
	fs := flag.NewFlagSet("fake-emmc", flag.ExitOnError)
	layout := fs.String("layout", "", "layout file (disk SECTORS, then INDEX NAME START SIZE)")
	out := fs.String("o", "", "new image file")
	ab := fs.String("ab", "", "slot values, for example a=15,0,1,b=14,0,1")
	markers := fs.Bool("markers", false, "write marker bytes into the regions that must not change")
	fill := fillList{}
	fs.Var(fill, "fill", "NAME=FILE: copy FILE to the start of partition NAME (repeat)")
	fs.Parse(args)
	if *layout == "" || *out == "" || *ab == "" || fs.NArg() != 0 {
		usage()
	}
	if strings.HasPrefix(*out, "/dev/") {
		fail(2, "fake-emmc writes a new image file only")
	}
	lf, err := os.Open(*layout)
	if err != nil {
		fail(2, "%v", err)
	}
	l, err := gpt.ReadLayout(lf)
	lf.Close()
	if err != nil {
		fail(2, "%s: %v", *layout, err)
	}
	slots, err := parseAB(*ab)
	if err != nil {
		fail(2, "--ab: %v", err)
	}
	if err := fakeemmc.Create(*out, fakeemmc.Spec{Layout: l, AB: slots, Fill: fill, Markers: *markers}); err != nil {
		fail(2, "%v", err)
	}
	a, _ := avbab.Parse(fakeemmc.ABSector(slots, nil))
	fmt.Printf("%s: %d sectors, %d partitions, A %v, B %v\n", *out, l.DiskSectors, len(l.Entries), a.Slots[0], a.Slots[1])
}
