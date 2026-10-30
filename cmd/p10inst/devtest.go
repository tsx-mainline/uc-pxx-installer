//go:build install && devtest

package main

// The test flags of the device mode. They exist only in a devtest build
// (go build -tags install,devtest), which is never put into a RAM image:
// ci/check-no-writer.py checks that a release install binary has none of it.

import (
	"errors"
	"flag"
	"os"
	"syscall"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/device/devicetest"
	"uc-pxx-installer/internal/devmode"
	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/install"
)

const devtestUsage = `devtest build only (never in a RAM image):
  --test-root DIR      the device mode on DIR/dev, DIR/sys, DIR/proc (loop devices, run as root;
                       uc-pxx-bootctl-inst runs with chroot DIR)
  --test-kill-after N  SIGKILL this process right after primitive write N
  --test-cut-after N   stop before primitive write N+1, as a power cut; --test-torn: half of it first
  --test-max-write N   bytes per data write
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
	return func(o *devmode.Options) (*device.Env, error) {
		if *kill == 0 {
			return nil, errors.New("--test-kill-after needs N >= 1 (use --test-cut-after 0)")
		}
		o.MaxWrite = *maxw
		if *kill > 0 || *cut >= 0 {
			k := *kill
			o.Hooks = &install.Hooks{CutAfter: *cut, Torn: *torn, Observe: func(ev install.Event) error {
				if k > 0 && ev.Write == k {
					syscall.Kill(os.Getpid(), syscall.SIGKILL)
					select {}
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
				Manifests: fx.Manifests, ApprovedResources: []string{fx.Resource}})
		}
		return device.NewForTest(device.TestOptions{Root: *root, ChrootBootctl: true})
	}
}
