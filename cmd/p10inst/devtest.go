//go:build install && devtest

package main

// The test flags of the device mode. They exist only in a devtest build
// (go build -tags install,devtest), which is never put into a RAM image:
// ci/check-no-writer.py checks that a release install binary has none of it.

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/device/devicetest"
	"uc-pxx-installer/internal/devmode"
	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/flowio"
	"uc-pxx-installer/internal/install"
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
`

func devtestFlags(f *flag.FlagSet) func(*devmode.Options) (*device.Env, error) {
	root := f.String("test-root", "", "devtest: root of the test devices")
	kill := f.Int("test-kill-after", -1, "devtest: SIGKILL after primitive write N")
	cut := f.Int("test-cut-after", -1, "devtest: stop before primitive write N+1")
	torn := f.Bool("test-torn", false, "devtest: with --test-cut-after, write half of the cut write first")
	maxw := f.Int("test-max-write", 0, "devtest: bytes per data write")
	fake := f.Bool("test-fake-unit", false, "devtest: with --test-root, a fake unit of package devicetest (regular files)")
	pause := f.Int("test-pause-after", -1, "devtest: sleep 5 s after primitive write N")
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
					fmt.Fprintf(os.Stderr, "p10inst: devtest: paused after write %d\n", pz)
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
		return device.NewForTest(device.TestOptions{Root: *root, ChrootBootctl: true})
	}
}

// flowTestFlags are the test flags of `p10inst flow` (devtest only): the
// flow on a test root, with the device-mode children on a fake unit.
func flowTestFlags(f *flag.FlagSet) func(*flowio.Config) bool {
	root := f.String("test-root", "", "devtest: the root of the test unit (dev, sys, proc, run)")
	fake := f.Bool("test-fake-unit", false, "devtest: the children run the device mode on a fake unit (--test-fake-unit)")
	noReboot := f.Bool("no-reboot", false, "devtest: exit instead of the restart")
	childArgs := f.String("test-child-args", "", "devtest: more arguments of the device-mode children (space-separated)")
	return func(c *flowio.Config) bool {
		if *root != "" {
			c.Root = *root
			c.Proc = "/proc"
			if *fake {
				fx := fakeemmc.NewFixture(fakeemmc.FixtureOpt{})
				c.Manifests = fx.Manifests
				// --panel-confirmed: the fake unit stands for a lit panel. The
				// flow does not ask "Do you see this screen on the unit's
				// panel?" yet (FIRMWARE-GENERAL §11.5, the flow agent): until it
				// passes the answer, a release flow's install is refused (check
				// "panel") before any write.
				c.ChildArgs = []string{"--test-root", *root, "--test-fake-unit", "--panel-confirmed"}
				if v, ok := os.LookupEnv("UC_PXX_BOOTCTL_EXEC"); ok {
					c.ChildEnv = []string{"UC_PXX_BOOTCTL_EXEC=" + v}
				}
				if v, ok := os.LookupEnv("TMPDIR"); ok {
					c.ChildEnv = append(c.ChildEnv, "TMPDIR="+v)
				}
			}
			c.ChildArgs = append(c.ChildArgs, strings.Fields(*childArgs)...)
		}
		return *noReboot
	}
}
