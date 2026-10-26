//go:build install && devtest

// Package devicetest builds fake UC-P10 roots for the device-mode tests: a
// directory with dev/, sys/, proc/, etc/ and run/ as the device mode reads
// them, where every device node is a regular file. No real disk is opened.
// It is in devtest builds only.
//
// The whole-disk file holds the GPT (both copies) and the regions outside
// the partitions. Each partition is its own file, as the kernel gives each
// partition its own node. The device mode reads partition data only through
// the partition nodes, and the GPT through the disk node, as on the unit.
package devicetest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"uc-pxx-installer/internal/bootctl"
	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/plan"
)

// Opts change the fake unit. The zero value is a good UC-P10 in the RAM image.
type Opts struct {
	CardType  string // device/type, default "MMC"
	Host      string // the controller in the sysfs path, default fe330000.mmc
	Model     string // the device-tree model, default "Crestron UC-P10"
	Compat    string // default "crestron,uc-p10\x00rockchip,rk3399"
	Cmdline   string // default: the RAM image command line
	Mounts    string // default: rootfs on /
	NoMarker  bool   // no /etc/p10inst/ram-image
	Android   bool   // an /init.rc
	SysfsEdit func(name string, p *gpt.LayoutEntry)
	Files     map[string]string // more files: path in the root -> content
	Links     map[string]string // symlinks: path in the root -> target
}

// Root is a fake unit.
type Root struct {
	Dir    string
	Layout *gpt.Layout
	nodes  map[string]string // partition name -> node file
}

const ramCmdline = "console=ttyS2,115200n8 loglevel=3 panic=30 rdinit=/init p10inst.ram=1"

func write(p string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}

func copyRange(dst string, src *os.File, off, n int64) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Truncate(n); err != nil {
		return err
	}
	buf := make([]byte, 1<<20)
	zero := make([]byte, len(buf))
	for done := int64(0); done < n; {
		k := int64(len(buf))
		if k > n-done {
			k = n - done
		}
		if _, err := src.ReadAt(buf[:k], off+done); err != nil && err != io.EOF {
			return err
		}
		if !bytes.Equal(buf[:k], zero[:k]) { // keep the file sparse
			if _, err := f.WriteAt(buf[:k], done); err != nil {
				return err
			}
		}
		done += k
	}
	return nil
}

// New makes a fake unit in dir from image, a whole-disk image file with the
// GPT of l (package fakeemmc). bootctlBin is the content of
// /usr/libexec/p10inst/uc-pxx-bootctl-inst.
func New(dir, image string, l *gpt.Layout, bootctlBin []byte, o Opts) (*Root, error) {
	r := &Root{Dir: dir, Layout: l, nodes: map[string]string{}}
	img, err := os.Open(image)
	if err != nil {
		return nil, err
	}
	defer img.Close()
	if err := copyRange(r.Path("/dev/mmcblk0"), img, 0, l.DiskSectors*gpt.Sector); err != nil {
		return nil, err
	}
	host := o.Host
	if host == "" {
		host = device.EMMCHost
	}
	card := "sys/devices/platform/" + host + "/mmc_host/mmc0/mmc0:0001"
	blk := card + "/block/mmcblk0"
	ct := o.CardType
	if ct == "" {
		ct = "MMC"
	}
	files := map[string]string{
		card + "/type":                    ct + "\n",
		card + "/cid":                     "150100424a54443452068c2f2e01d500\n",
		blk + "/dev":                      "179:0\n",
		blk + "/size":                     fmt.Sprintf("%d\n", l.DiskSectors),
		blk + "/ro":                       "0\n",
		blk + "/queue/logical_block_size": "512\n",
	}
	for k, v := range files {
		if err := write(filepath.Join(dir, k), []byte(v)); err != nil {
			return nil, err
		}
	}
	os.MkdirAll(filepath.Join(dir, blk, "holders"), 0o755)
	if err := os.Symlink("../../../mmc0:0001", filepath.Join(dir, blk, "device")); err != nil {
		return nil, err
	}
	os.MkdirAll(r.Path("/sys/class/block"), 0o755)
	if err := os.Symlink("../../../"+blk, r.Path("/sys/class/block/mmcblk0")); err != nil {
		return nil, err
	}
	for _, e := range l.Entries {
		dev := fmt.Sprintf("mmcblk0p%d", e.Index)
		node := r.Path("/dev/" + dev)
		if err := copyRange(node, img, e.Start*gpt.Sector, e.Size*gpt.Sector); err != nil {
			return nil, err
		}
		r.nodes[e.Name] = node
		se := e
		if o.SysfsEdit != nil {
			o.SysfsEdit(e.Name, &se)
		}
		pd := filepath.Join(dir, blk, dev)
		pf := map[string]string{
			"uevent":    fmt.Sprintf("MAJOR=179\nMINOR=%d\nDEVNAME=%s\nDEVTYPE=partition\nDISKSEQ=1\nPARTN=%d\nPARTNAME=%s\n", e.Index, dev, e.Index, se.Name),
			"partition": fmt.Sprintf("%d\n", e.Index),
			"start":     fmt.Sprintf("%d\n", se.Start),
			"size":      fmt.Sprintf("%d\n", se.Size),
			"dev":       fmt.Sprintf("179:%d\n", e.Index),
			"ro":        "0\n",
		}
		for k, v := range pf {
			if err := write(filepath.Join(pd, k), []byte(v)); err != nil {
				return nil, err
			}
		}
		os.MkdirAll(filepath.Join(pd, "holders"), 0o755)
		if err := os.Symlink("../../../"+blk+"/"+dev, r.Path("/sys/class/block/"+dev)); err != nil {
			return nil, err
		}
	}
	model, compat := o.Model, o.Compat
	if model == "" {
		model = device.Model
	}
	if compat == "" {
		compat = device.Compatible + "\x00rockchip,rk3399"
	}
	cl := o.Cmdline
	if cl == "" {
		cl = ramCmdline
	}
	mounts := o.Mounts
	if mounts == "" {
		mounts = "rootfs / rootfs rw,size=1951304k,nr_inodes=487826 0 0\nproc /proc proc rw,relatime 0 0\nsysfs /sys sysfs rw,relatime 0 0\nrun /run tmpfs rw,nosuid,nodev,mode=755 0 0\n"
	}
	more := map[string][]byte{
		"/proc/device-tree/model":      []byte(model + "\x00"),
		"/proc/device-tree/compatible": []byte(compat + "\x00"),
		"/proc/cmdline":                []byte(cl + "\n"),
		"/proc/mounts":                 []byte(mounts),
		device.BootctlPath:             bootctlBin,
	}
	if !o.NoMarker {
		more[device.RAMImageMarker] = []byte(device.RAMImageMagic + "\nbuild test\n")
	}
	if o.Android {
		more["/init.rc"] = []byte("on init\n")
	}
	for k, v := range more {
		if err := write(r.Path(k), v); err != nil {
			return nil, err
		}
	}
	for k, v := range o.Files {
		if err := write(r.Path(k), []byte(v)); err != nil {
			return nil, err
		}
	}
	for k, v := range o.Links {
		os.MkdirAll(filepath.Dir(r.Path(k)), 0o755)
		if err := os.Symlink(v, r.Path(k)); err != nil {
			return nil, err
		}
	}
	os.MkdirAll(r.Path("/run"), 0o755)
	return r, nil
}

// Reopen is the Root of a fake unit that New made in dir before (for a
// child process).
func Reopen(dir string, l *gpt.Layout) *Root {
	r := &Root{Dir: dir, Layout: l, nodes: map[string]string{}}
	for _, e := range l.Entries {
		r.nodes[e.Name] = r.Path(fmt.Sprintf("/dev/mmcblk0p%d", e.Index))
	}
	return r
}

// Path is p inside the root.
func (r *Root) Path(p string) string { return filepath.Join(r.Dir, p) }

// Node is the node file of a partition.
func (r *Root) Node(name string) string { return r.nodes[name] }

// Image puts the disk back together from the node files (the disk file for
// the GPT and the gaps, each partition file for its range): the bytes a power
// cut would leave on the eMMC. It shares no code with package device.
func (r *Root) Image() ([]byte, error) {
	img, err := os.ReadFile(r.Path("/dev/mmcblk0"))
	if err != nil {
		return nil, err
	}
	if int64(len(img)) != r.Layout.DiskSectors*gpt.Sector {
		return nil, errors.New("the disk file changed its size")
	}
	for _, e := range r.Layout.Entries {
		b, err := os.ReadFile(r.nodes[e.Name])
		if err != nil {
			return nil, err
		}
		if int64(len(b)) != e.Size*gpt.Sector {
			return nil, fmt.Errorf("%s changed its size", e.Name)
		}
		copy(img[e.Start*gpt.Sector:], b)
	}
	return img, nil
}

// ABSector reads the A/B sector from the misc node file.
func (r *Root) ABSector() ([]byte, error) {
	f, err := os.Open(r.nodes["misc"])
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b := make([]byte, gpt.Sector)
	_, err = f.ReadAt(b, (plan.ABLBA-plan.MiscStart)*gpt.Sector)
	return b, err
}

// Bootctl stands in for uc-pxx-bootctl-inst on a fake unit. It runs the
// checked bytes (the real binary, under Exec when it is an aarch64 binary on
// a PC) with --file on a copy of the A/B sector, and on success writes the
// result into the misc node file at offset 2048 with fsync: what the real
// tool does on the eMMC through /dev/mmcblk0. Mode makes it misbehave.
type Bootctl struct {
	Root *Root
	Exec string // runner, for example qemu-aarch64-static
	Tmp  string
	// Mode: "" (normal), "refuse" (exit 1), "unknown" (exit 3, no write),
	// "unknown-wrote" (exit 3 after the right write), "signal" (ended by a
	// signal before its write), "signal-wrote" (ended by a signal after the
	// right write), "garbage" (exit 3 after writing a sector with a bad CRC),
	// "lie" (exit 1 but writes), "wrong" (writes a keeper that is not
	// successful), "tail" (changes byte 300), "arm-refuse" (refuses arm-trying).
	Mode  string
	Calls []string
}

// Run implements device.BootctlRunner.
func (b *Bootctl) Run(e *device.Env, bin []byte, args []string) (*device.BootctlResult, error) {
	b.Calls = append(b.Calls, strings.Join(args, " "))
	dir, err := os.MkdirTemp(b.Tmp, "bc-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	exe := filepath.Join(dir, "uc-pxx-bootctl-inst")
	if err := os.WriteFile(exe, bin, 0o700); err != nil {
		return nil, err
	}
	tool := bootctl.Tool{Path: exe, Exec: b.Exec, TmpDir: dir}
	cur, err := b.Root.ABSector()
	if err != nil {
		return nil, err
	}
	if args[0] == "status" {
		_, res, err := tool.Status(cur)
		if res == nil {
			return nil, err
		}
		return &device.BootctlResult{RC: res.RC, Stdout: res.Stdout, Stderr: res.Stderr}, nil
	}
	switch {
	case b.Mode == "refuse", b.Mode == "arm-refuse" && args[0] == "arm-trying":
		return &device.BootctlResult{RC: 1, Stderr: "uc-pxx-bootctl: refusing (test)\n"}, nil
	case b.Mode == "unknown":
		return &device.BootctlResult{RC: 3, Stderr: "uc-pxx-bootctl: !!! ON-DISK A/B STATE UNKNOWN (test)\n"}, nil
	case b.Mode == "signal":
		return &device.BootctlResult{RC: -1, Signal: "hangup"}, nil
	}
	res, err := tool.Apply(cur, args)
	if err != nil {
		return nil, err
	}
	next := res.Sector
	rc := res.RC
	switch b.Mode {
	case "wrong":
		next[10], next[14] = 0, 0
	case "tail":
		next[300] ^= 0xff
	case "lie":
		rc = 1
	case "unknown-wrote":
		rc = 3
	case "garbage":
		next[29] ^= 0xff
		rc = 3
	}
	if (res.RC == 0 || b.Mode == "lie") && !bytes.Equal(next, cur) {
		f, err := os.OpenFile(b.Root.nodes["misc"], os.O_WRONLY, 0)
		if err != nil {
			return nil, err
		}
		_, werr := f.WriteAt(next, (plan.ABLBA-plan.MiscStart)*gpt.Sector)
		serr := f.Sync()
		f.Close()
		if werr != nil || serr != nil {
			return nil, errors.Join(werr, serr)
		}
	}
	if b.Mode == "signal-wrote" {
		return &device.BootctlResult{RC: -1, Signal: "interrupt"}, nil
	}
	return &device.BootctlResult{RC: rc, Stdout: res.Stdout, Stderr: res.Stderr}, nil
}

// SHA256 of a byte slice, hex.
func SHA256(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
