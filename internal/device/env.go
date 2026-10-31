//go:build install

// Package device is the device mode of the installer: the eMMC of a UC-P10,
// written from the installer RAM image. It is the only code in p10inst that
// writes to a block device. It is in the install build only (go build -tags
// install), and the status-only RAM image does not contain it
// (ci/check-no-writer.py).
//
// The safety layers, in the order they act:
//
//  1. The environment (CheckRAMImage): the program runs inside the installer
//     RAM image (the marker file, p10inst.ram=1 and the image's own token
//     on a command line of printable ASCII, the root file system in RAM),
//     not on a booted mainline or Android, and no eMMC file system is
//     mounted.
//  2. The disk (OpenEMMC): only /dev/mmcblk0, and only when it is the eMMC
//     of a UC-P10: the device-tree model and compatible, an MMC card (not
//     SD) on the eMMC controller, 512-byte sectors, the expected size, and
//     the exact expected partition layout, the same in sysfs and in both GPT
//     copies. Every device node must be the block device that sysfs names.
//  3. The writes (OpenWrite, writer.go): one partition node at a time, never
//     the whole disk; O_RDWR only for its own write; the range rule of the
//     plan again before every write; O_DIRECT; fsync and BLKFLSBUF at the
//     end; the kernel read-only flag cleared only on that node and on the
//     disk, and only while the write runs.
//  4. The A/B sector (bootctl.go): only through uc-pxx-bootctl-inst, the
//     reviewed binary, with its sha256 checked before each call, and only
//     with the commands status, disable --slot T and arm-trying --slot T
//     --lower-keeper, and for the repair of the hold repair --keeper K
//     --expect-sector HEX. p10inst never writes the A/B sector itself.
//  5. Every read that checks a write is a fresh read from the device
//     (O_DIRECT), so the page cache cannot hide a bad write.
package device

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/keykeep"
	"uc-pxx-installer/internal/manifest"
)

// The facts of a UC-P10 that the device mode checks. They are not secret.
const (
	// EMMCPath is the only device that the device mode accepts.
	EMMCPath = "/dev/mmcblk0"
	emmcDev  = "mmcblk0"
	// Model and Compatible are in the device tree of the uc-pxx kernel
	// (rk3399-crestron-ucp10.dts).
	Model      = "Crestron UC-P10"
	Compatible = "crestron,uc-p10"
	// EMMCHost is the RK3399 eMMC controller (sdhci, rk3399-base.dtsi). The
	// SD card slot (sdmmc) is fe320000.mmc.
	EMMCHost = "fe330000.mmc"
	// RAMImageMarker is a file of the installer RAM image
	// (image/overlay/etc/p10inst/ram-image). Its first line is RAMImageMagic.
	RAMImageMarker = "/etc/p10inst/ram-image"
	RAMImageMagic  = "p10inst installer RAM image"
	// RAMCmdline is on the kernel command line of the RAM image only
	// (image/build.sh puts it into the bootargs of its device tree).
	RAMCmdline = "p10inst.ram=1"
	// RAMTokenPrefix starts the token word of the RAM image: image/build.sh
	// gives kernel/mkimage.sh --board p10inst-XXXXXX (6 lowercase hex
	// digits), and mkimage.sh adds uc-pxx.id=TOKEN to the command line.
	// CheckRAMImage requires exactly one uc-pxx.id= word, of this form, and
	// returns its token. The plan holds the token (plan.RAMImage).
	RAMTokenPrefix = "uc-pxx.id=p10inst-"
	// BootctlPath is uc-pxx-bootctl-inst in the installer RAM image.
	BootctlPath = "/usr/libexec/p10inst/uc-pxx-bootctl-inst"
	// BootctlSHA256 is the only binary that runs. abbffc39… is
	// uc-pxx-linux branch bootctl-repair: the reviewed 5d83215b… (RULES.md
	// rule 9, two-slot installer tool, BOOTCTL-DEPLOY-REVIEW.md) plus the
	// commands `repair` and `switch` (DEVMODE-R4.md §2, §4). NOT REVIEWED YET: it needs an
	// Opus review and a RULES.md entry before it runs on a unit.
	BootctlSHA256 = "abbffc392615b1db708884262d7e18d5a5e480716e1d2fec4f26b32dd137f1c3"
	// ABLockPath is the lock of every A/B writer on the unit.
	ABLockPath = "/run/uc-pxx-abdata.lock"
	// runDir holds the private copies of the bootctl binary.
	runDir = "/run/p10inst"
)

//go:embed ucp10-layout.txt
var layoutText []byte

// Layout is the expected eMMC layout of a UC-P10 (index, name, start, size;
// read from the kernel of a unit). It is a copy of
// internal/gpt/testdata/ucp10-layout.txt (a test checks that they are equal).
func Layout() *gpt.Layout {
	l, err := gpt.ReadLayout(bytes.NewReader(layoutText))
	if err != nil {
		panic("device: the embedded layout does not parse: " + err.Error())
	}
	return l
}

// BootctlRunner runs uc-pxx-bootctl-inst. bin is the binary that was read
// and checked (sha256) for this call. The default runs a private copy of
// bin. Tests give their own.
type BootctlRunner interface {
	Run(e *Env, bin []byte, args []string) (*BootctlResult, error)
}

// BootctlResult is one run of the tool.
type BootctlResult struct {
	RC             int
	Stdout, Stderr string
	Signal         string // the signal that ended it, if one did (RC is then -1)
}

// Env is where the device mode looks. On the device it is Live(): the real
// /dev, /sys, /proc and /etc, block devices only, the pinned bootctl.
type Env struct {
	root       string
	regular    bool // test: device nodes are regular files: no O_DIRECT, no block ioctls
	chroot     bool // test: run bootctl inside root with chroot
	layout     *gpt.Layout
	bootctlSHA string
	runner     BootctlRunner
	manifests  []*manifest.Manifest // test: instead of the embedded manifests
	resources  []string             // test: approved boot resources
	abReadErr  func() error         // test: the A/B sector read fails (an unreadable sector)
	gate       *keykeep.Gate        // test: the keep-key gate instead of the embedded checks
}

// Live is the environment of the device: the root of the RAM image.
func Live() *Env {
	return &Env{root: "/", layout: Layout(), bootctlSHA: BootctlSHA256, runner: execRunner{}}
}

// TestOptions change the environment for tests. NewForTest accepts them only
// in a test build (go build -tags install,devtest). A release build cannot
// use them: the code behind the check is not compiled in.
type TestOptions struct {
	Root          string        // instead of "/"
	RegularFiles  bool          // device nodes are regular files (no root needed)
	ChrootBootctl bool          // run the real bootctl with chroot Root (loop devices, as root)
	Layout        *gpt.Layout   // instead of the UC-P10 layout
	BootctlSHA256 string        // instead of the pinned sha256
	Runner        BootctlRunner // instead of running the binary
	// Manifests and ApprovedResources replace the embedded manifests and the
	// stock resource, for a made-up test firmware.
	Manifests         []*manifest.Manifest
	ApprovedResources []string
	// ABReadError, if set, is called before each read of the A/B sector; an
	// error makes the read fail (an unreadable sector, for the repair tests).
	ABReadError func() error
	// KeyKeepGate replaces the gate of the embedded checks.json (the keep-key
	// write is tested with an open gate; a release build has the file's).
	KeyKeepGate *keykeep.Gate
}

// ErrNotTestBuild is returned by NewForTest in a release build.
var ErrNotTestBuild = errors.New("test environments exist only in a devtest build")

// NewForTest makes a test environment. It fails in a release build.
func NewForTest(o TestOptions) (*Env, error) {
	if !testBuild {
		return nil, ErrNotTestBuild
	}
	if o.Root == "" || o.Root == "/" {
		return nil, errors.New("a test environment needs its own root")
	}
	root, err := filepath.Abs(o.Root)
	if err != nil {
		return nil, err
	}
	e := &Env{root: root, regular: o.RegularFiles, chroot: o.ChrootBootctl, layout: o.Layout,
		bootctlSHA: o.BootctlSHA256, runner: o.Runner, manifests: o.Manifests, resources: o.ApprovedResources,
		abReadErr: o.ABReadError, gate: o.KeyKeepGate}
	if e.layout == nil {
		e.layout = Layout()
	}
	if e.bootctlSHA == "" {
		e.bootctlSHA = BootctlSHA256
	}
	if e.runner == nil {
		e.runner = execRunner{}
	}
	fmt.Fprintln(os.Stderr, "p10inst: "+testBuildMarker+": root "+root)
	return e, nil
}

// IsTest reports a test environment.
func (e *Env) IsTest() bool { return e.root != "/" }

// path is a path of the device, inside the root.
func (e *Env) path(p string) string {
	if e.root == "/" {
		return p
	}
	return filepath.Join(e.root, p)
}

// Path is p inside the root (for messages and tests).
func (e *Env) Path(p string) string { return e.path(p) }

// Manifests are the firmware manifests: the embedded ones on the device.
func (e *Env) Manifests() ([]*manifest.Manifest, error) {
	if e.manifests != nil {
		return e.manifests, nil
	}
	return manifest.Embedded()
}

// ApprovedResources is nil on the device (the planner then accepts only the
// stock resource of RULES.md).
func (e *Env) ApprovedResources() []string { return e.resources }

// KeyKeepGate is the gate of the keep-key write: the embedded checks.json
// (package keykeep) on the device.
func (e *Env) KeyKeepGate() keykeep.Gate {
	if e.gate != nil {
		return *e.gate
	}
	return keykeep.TheGate()
}

// BootctlSHA256 is the only bootctl sha256 that runs: the reviewed one.
func (e *Env) BootctlSHA256() string { return e.bootctlSHA }
