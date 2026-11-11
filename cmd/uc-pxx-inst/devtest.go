//go:build install && devtest

package main

// The test flags of the device mode. They exist only in a devtest build
// (go build -tags install,devtest), which is never put into a RAM image:
// ci/check-no-writer.py checks that a release install binary has none of it.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/device/devicetest"
	"uc-pxx-installer/internal/devmode"
	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/flowio"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/plan"
)

const devtestUsage = `devtest build only (never in a RAM image):
  --test-root DIR      the device mode on DIR/dev, DIR/sys, DIR/proc (loop devices, run as root;
                       uc-pxx-bootctl-inst runs with chroot DIR)
  --test-kill-after N  SIGKILL this process right after primitive write N
  --test-cut-after N   stop before primitive write N+1, as a power cut; --test-torn: half of it first
  --test-max-write N   bytes per data write
  --test-pause-after N sleep 5 s right after primitive write N (a slow install, for the flow tests)
  --test-fake-unit     with --test-root: a fake unit of package devicetest (device nodes are regular files,
                       the small test layout, the made-up test firmware); uc-pxx-bootctl-inst runs with
                       --file on a copy of the A/B sector, under $UC_PXX_BOOTCTL_EXEC (no root needed)
  --test-fixture-manifests  with --test-root (loop devices): the manifests of the made-up test firmware
                       (package fakeemmc) instead of the embedded ones, for a keeper that holds its images;
                       --test-fixture-boot-size N: the fixture with boot images of N bytes
  --test-review-bundle DIR  (plan/install of a mainline bundle) a TEST payload gate that lists the bundle
                       in DIR as reviewed (no kernel and no boot-control check)
  uc-pxx-inst test-fixture -o DIR [--boot-size N]   write the made-up test firmware's images (DIR/new, DIR/old),
                       its manifest and the resource of its boot image (the inputs of ci/devmode-loop.py
                       --mainline-fixture)
`

func init() {
	extraCmds["test-fixture"] = cmdTestFixture
}

// cmdTestFixture writes the made-up fixture of package fakeemmc (no vendor
// byte): DIR/new/NAME.img and DIR/old/NAME.img for the eight slot images,
// DIR/manifest.json and DIR/resource.bin (the second part of the new boot
// image).
func cmdTestFixture(args []string) {
	fs := flag.NewFlagSet("test-fixture", flag.ExitOnError)
	out := fs.String("o", "", "output folder (new)")
	bs := fs.Int("boot-size", 0, "the size of the boot images (0: the default 200 KiB)")
	fs.Parse(args)
	if *out == "" || fs.NArg() != 0 {
		usage()
	}
	fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{BootSize: *bs})
	for _, d := range []string{"new", "old"} {
		if err := os.MkdirAll(filepath.Join(*out, d), 0o755); err != nil {
			fail(2, "%v", err)
		}
	}
	put := func(p string, b []byte) {
		if err := os.WriteFile(filepath.Join(*out, p), b, 0o644); err != nil {
			fail(2, "%v", err)
		}
	}
	for n, b := range fx.New {
		put("new/"+n+".img", b)
	}
	for n, b := range fx.Old {
		put("old/"+n+".img", b)
	}
	put("manifest.json", fx.Manifest)
	res, err := mainline.ReadResource(bytes.NewReader(fx.New["boot"]), 0, int64(len(fx.New["boot"])))
	if err != nil {
		fail(2, "the fixture boot image: %v", err)
	}
	put("resource.bin", res)
	fmt.Printf("%s: the made-up fixture firmware %s (%d images)\n", *out, fx.Manifests[0].Firmware.Version, len(fx.New))
}

// devtestBoolFlag: the devtest flags of plan and install that take no value
// (boolFlags of mainline_install.go asks here; the release build's
// devtest_off.go says no to every name).
func devtestBoolFlag(name string) bool {
	switch name {
	case "test-fake-unit", "test-fixture-manifests":
		return true
	}
	return false
}

func devtestFlags(f *flag.FlagSet) func(*devmode.Options) (*device.Env, error) {
	root := f.String("test-root", "", "devtest: root of the test devices")
	kill := f.Int("test-kill-after", -1, "devtest: SIGKILL after primitive write N")
	cut := f.Int("test-cut-after", -1, "devtest: stop before primitive write N+1")
	torn := f.Bool("test-torn", false, "devtest: with --test-cut-after, write half of the cut write first")
	maxw := f.Int("test-max-write", 0, "devtest: bytes per data write")
	fake := f.Bool("test-fake-unit", false, "devtest: with --test-root, a fake unit of package devicetest (regular files)")
	pause := f.Int("test-pause-after", -1, "devtest: sleep 5 s after primitive write N")
	fixm := f.Bool("test-fixture-manifests", false, "devtest: with --test-root (loop devices), the made-up fixture firmware's manifests")
	fixBoot := f.Int("test-fixture-boot-size", 0, "devtest: with --test-fixture-manifests, the fixture with boot images of N bytes")
	return func(o *devmode.Options) (*device.Env, error) {
		if *kill == 0 {
			return nil, errors.New("--test-kill-after needs N >= 1 (use --test-cut-after 0)")
		}
		o.MaxWrite = *maxw
		if *kill > 0 || *cut >= 0 || *pause > 0 {
			k, pz := *kill, *pause
			o.Hooks = &install.Hooks{CutAfter: *cut, Torn: *torn, Observe: func(ev install.Event) error {
				if k > 0 && ev.Write == k {
					syscall.Kill(os.Getpid(), syscall.SIGKILL)
					select {}
				}
				if pz > 0 && ev.Write == pz {
					fmt.Fprintf(os.Stderr, "uc-pxx-inst: devtest: paused after write %d\n", pz)
					time.Sleep(5 * time.Second)
				}
				return nil
			}}
		}
		if *root == "" {
			if *fake {
				return nil, errors.New("--test-fake-unit needs --test-root")
			}
			return nil, nil
		}
		if *fake {
			// the same made-up firmware and layout as the device-mode tests
			fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
			small := fakeemmc.ParseSmallLayout(fakeemmc.SmallLayout)
			bc := &devicetest.Bootctl{Root: devicetest.Reopen(*root, small), Exec: os.Getenv("UC_PXX_BOOTCTL_EXEC"), Tmp: os.TempDir()}
			return device.NewForTest(device.TestOptions{Root: *root, RegularFiles: true, Layout: small, Runner: bc,
				Manifests: fx.Manifests})
		}
		if *fixm {
			fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{BootSize: *fixBoot})
			return device.NewForTest(device.TestOptions{Root: *root, ChrootBootctl: true, Manifests: fx.Manifests})
		}
		return device.NewForTest(device.TestOptions{Root: *root, ChrootBootctl: true})
	}
}

// mainlineTestGate is --test-review-bundle DIR (devtest only): a test gate
// (mainline.TestGate) that lists the bundle in DIR (its three files'
// sha256) as reviewed, with no kernel and no boot-control check (a made-up
// bundle has neither), for the mainline CI runs on made-up bundles.
func mainlineTestGate(f *flag.FlagSet) func() *mainline.Gate {
	dir := f.String("test-review-bundle", "", "devtest: a test payload gate that lists the bundle in DIR as reviewed")
	slots := f.String("test-review-slots", "", "devtest: with --test-review-bundle, the slots of the reviewed entry (\"\" or \"a\": slot A only; \"ab\": both, for the direct path)")
	return func() *mainline.Gate {
		if *dir == "" {
			return nil
		}
		sum := func(n string) string {
			b, err := os.ReadFile(filepath.Join(*dir, n))
			if err != nil {
				fail(2, "--test-review-bundle: %v", err)
			}
			s := sha256.Sum256(b)
			return hex.EncodeToString(s[:])
		}
		r := mainline.Reviewed{Version: "devtest", ManifestSHA256: sum(mainline.ManifestFile), TemplateSHA256: sum(mainline.TemplateFile),
			RootfsSHA256: sum(mainline.RootfsFile), Slots: *slots}
		fmt.Fprintf(os.Stderr, "uc-pxx-inst: devtest: a TEST payload gate lists the bundle %s as reviewed\n", *dir)
		return mainline.TestGate([]mainline.Reviewed{r}, nil, nil)
	}
}

// stashChildTestFlags (devtest only): the flow gives its plan children the
// test arguments of --test-child-args and --test-autoupdate-off
// (InstallChildArgs); the stash child of step 1 is a plan child, so it takes
// them too and uses none (it reads no auto-update state and no payload).
func stashChildTestFlags(f *flag.FlagSet) {
	f.Bool("autoupdate-unclear-confirmed", false, "devtest: the flow's plan-child argument; the stash does not use it")
	f.String("test-review-bundle", "", "devtest: the flow's plan-child argument; the stash does not use it")
	f.String("test-review-slots", "", "devtest: the flow's plan-child argument; the stash does not use it")
}

// flowTestFlags are the test flags of `uc-pxx-inst flow` (devtest only): the
// flow on a test root, with the device-mode children on a fake unit.
func flowTestFlags(f *flag.FlagSet) func(*flowio.Config) bool {
	root := f.String("test-root", "", "devtest: the root of the test unit (dev, sys, proc, run)")
	fake := f.Bool("test-fake-unit", false, "devtest: the children run the device mode on a fake unit (--test-fake-unit)")
	noReboot := f.Bool("no-reboot", false, "devtest: exit instead of the restart")
	childArgs := f.String("test-child-args", "", "devtest: more arguments of the device-mode children (space-separated)")
	auOff := f.Bool("test-autoupdate-off", false, "devtest: the flow reads Crestron auto-update as off and the children get --autoupdate-unclear-confirmed (the small fake layout has no nvram at its place)")
	return func(c *flowio.Config) bool {
		if *root != "" {
			c.Root = *root
			c.Proc = "/proc"
			if *fake {
				fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
				c.Manifests = fx.Manifests
				// (--panel-confirmed comes from the flow's question "Do you see
				// this screen on the unit's panel?", FIRMWARE-GENERAL §11.5)
				c.ChildArgs = []string{"--test-root", *root, "--test-fake-unit"}
				if v, ok := os.LookupEnv("UC_PXX_BOOTCTL_EXEC"); ok {
					c.ChildEnv = []string{"UC_PXX_BOOTCTL_EXEC=" + v}
				}
				if v, ok := os.LookupEnv("TMPDIR"); ok {
					c.ChildEnv = append(c.ChildEnv, "TMPDIR="+v)
				}
			}
			c.InstallChildArgs = strings.Fields(*childArgs)
			if *auOff {
				c.TestAutoUpdateOff = true
				c.InstallChildArgs = append(c.InstallChildArgs, "--autoupdate-unclear-confirmed")
			}
		}
		return *noReboot
	}
}

// lockTestFlag is --test-lock-state of the mainline commands (devtest only):
// a stub of the TEE part of the in-RAM lock_state reader of the direct path
// (devmode lockstate.go; MAINLINE-RC2-LS-2026-11-11.md). The device mode keeps
// everything else of the reader (once per boot, its record, the reads before
// and after). VALUE: 0xNN (the byte the client gives), fail (no byte), or
// changed (the byte 0x01, and the stub changes the BCB of the fake unit
// during the read). In the device mode on a test root the default is 0x01
// (the fake unit has no TEE); in the image mode there is no default (no
// read: the direct plan refuses the unlock).
func lockTestFlag(f *flag.FlagSet) (devF func(testEnv bool) devmode.LockReader, imgF func() *plan.LockStateRead) {
	v := f.String("test-lock-state", "", "devtest: a stub of the TEE part of the in-RAM lock_state reader: 0xNN, fail or changed (device mode on a test root: default 0x01)")
	parse := func() (b *byte, failNow, changed bool) {
		switch *v {
		case "fail":
			return nil, true, false
		case "changed":
			one := byte(1)
			return &one, false, true
		}
		var x byte
		if n, err := fmt.Sscanf(*v, "0x%02x", &x); n != 1 || err != nil || len(*v) != 4 {
			fail(2, "--test-lock-state: %q is not 0xNN, fail or changed", *v)
		}
		return &x, false, false
	}
	devF = func(testEnv bool) devmode.LockReader {
		if *v == "" && !testEnv {
			return nil
		}
		if *v == "" {
			*v = "0x01"
		}
		b, failNow, changed := parse()
		stub := "devtest --test-lock-state " + *v
		return func(e *device.Env, d *device.Disk, say func(string)) devmode.LockTEE {
			say("  " + stub + ": no module, supplicant or client runs")
			t := devmode.LockTEE{Stub: stub, Module: "stub", Supplicant: "stub", Client: "stub"}
			if failNow {
				t.Err = errors.New("the stub's read failed (as the client's exit 3: OPEN_SESSION failed)")
				return t
			}
			if changed && !e.IsTest() {
				t.Err = errors.New("--test-lock-state changed runs only on a test root (it changes the fake unit's BCB)")
				return t
			}
			if changed {
				// the fake unit's BCB changes during the "read" (test only; the
				// disk node and the misc node are separate files of the fake unit)
				misc, _ := d.Part("misc")
				for _, w := range []struct {
					node string
					off  int64
				}{{device.EMMCPath, 49184*512 + 64}, {"/dev/" + misc.Dev, (49184-misc.Start)*512 + 64}} {
					f, err := os.OpenFile(e.Path(w.node), os.O_WRONLY, 0)
					if err != nil {
						t.Err = err
						return t
					}
					f.WriteAt([]byte("devtest-changed-bcb"), w.off)
					f.Close()
				}
			}
			t.Byte = b
			return t
		}
	}
	imgF = func() *plan.LockStateRead {
		if *v == "" {
			return nil
		}
		if *v == "changed" {
			return &plan.LockStateRead{Stub: "devtest --test-lock-state changed (image mode)", LockState: "0x01", Changed: true}
		}
		b, failNow, _ := parse()
		r := &plan.LockStateRead{Stub: "devtest --test-lock-state " + *v + " (image mode: no TEE, no reads around it)"}
		if failNow {
			r.Error = "the stub's read failed"
		} else {
			r.LockState = fmt.Sprintf("0x%02x", *b)
		}
		return r
	}
	return devF, imgF
}
