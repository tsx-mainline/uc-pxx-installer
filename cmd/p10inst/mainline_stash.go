//go:build install

package main

// The first drive phase of a mainline install (MAINLINE-INT-2026-11-10.md tasks
// 2 and 3): the stash of the 1.0.0.94 userdebug firmware's boot chain and of
// the firmware key's record, in RAM (package mlstash).
//
//	p10inst mainline-stash --device /dev/mmcblk0 (--fw-drive DRIVE.img [--fw-drive-expect ID] | --fw FILE) [--keys FILE]
//	p10inst mainline-stash --image FILE --fw FILE --out DIR [--keys FILE]   (a dry run on an image file)
//
// Neither form writes the eMMC (or the image): only the stash folder.

import (
	"flag"
	"fmt"
	"os"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/devmode"
	"uc-pxx-installer/internal/drivestate"
	"uc-pxx-installer/internal/keykeep"
	"uc-pxx-installer/internal/manifest"
	"uc-pxx-installer/internal/mlstash"
	"uc-pxx-installer/internal/pcdfw"
	"uc-pxx-installer/internal/plan"
)

const mainlineStashUsage = `install build, mainline first drive phase (the 1.0.0.94 userdebug .bin on the drive; nothing is written
to the eMMC, only the RAM stash ` + mlstash.Dir + `):
  p10inst mainline-stash --device /dev/mmcblk0 (--fw-drive DRIVE.img [--fw-drive-expect ID] | --fw FILE) [--keys FILE]
  p10inst mainline-stash --image FILE --fw FILE --out DIR [--keys FILE]   (dry run on an image file)
`

func init() {
	extraCmds["mainline-stash"] = cmdMainlineStash
	extraUsage += mainlineStashUsage
}

func cmdMainlineStash(args []string) {
	fs := flag.NewFlagSet("mainline-stash", flag.ExitOnError)
	var o devmode.Options
	var image, out string
	fs.StringVar(&o.Device, "device", "", "the eMMC: must be /dev/mmcblk0")
	fs.StringVar(&o.FW, "fw", "", "the firmware file (.bin)")
	fs.StringVar(&o.FWDrive, "fw-drive", "", "the P10-INSTALL drive file: the firmware is its one .bin, read in place")
	fs.StringVar(&o.FWDriveExpect, "fw-drive-expect", "", "with --fw-drive: the identity of the drive file")
	fs.StringVar(&o.Keys, "keys", "", "the key file (else the key search of the official device mode)")
	fs.StringVar(&image, "image", "", "dry run: the image file that stands for the eMMC (read only)")
	fs.StringVar(&out, "out", "", "dry run: the stash folder to make")
	envf := devtestFlags(fs)
	fs.Parse(args)
	if fs.NArg() != 0 || (o.Device == "") == (image == "") {
		usage()
	}
	if image != "" {
		mainlineStashDry(image, o.FW, o.Keys, out)
		return
	}
	if out != "" || (o.FW == "") == (o.FWDrive == "") {
		usage()
	}
	e := device.Live()
	if envf != nil {
		te, err := envf(&o)
		if err != nil {
			fail(2, "%v", err)
		}
		if te != nil {
			e = te
		}
	}
	if _, err := devmode.MainlineStash(e, o, os.Stdout); err != nil {
		fmt.Printf("\nRESULT: %v\n", err)
		os.Exit(devmode.ExitCode(err))
	}
	fmt.Println("\nRESULT: stash done (nothing written to the eMMC); now give the drive the folder uc-pxx-mainline")
}

// mainlineStashDry is the stash of a dry run: the firmware file, the key of
// --keys or of the image's systems, the stash folder out.
func mainlineStashDry(image, fwPath, keys, out string) {
	if fwPath == "" || out == "" {
		usage()
	}
	if err := devmode.CheckOutputPath(out); err != nil {
		fail(2, "%v", err)
	}
	ms, err := manifest.Embedded()
	if err != nil {
		fail(2, "manifests: %v", err)
	}
	disk, err := os.Open(image)
	if err != nil {
		fail(2, "%v", err)
	}
	defer disk.Close()
	dst, err := disk.Stat()
	if err != nil || !dst.Mode().IsRegular() {
		fail(2, "%s is not a regular file", image)
	}
	fw, err := os.Open(fwPath)
	if err != nil {
		fail(2, "%v", err)
	}
	defer fw.Close()
	fst, err := fw.Stat()
	if err != nil || !fst.Mode().IsRegular() {
		fail(2, "%s is not a regular file", fwPath)
	}
	identNow := func() (string, error) {
		id, err := drivestate.Stat(fwPath)
		if err != nil {
			return "", err
		}
		return "file " + id.String(), nil
	}
	ident, err := identNow()
	if err != nil {
		fail(2, "%v", err)
	}
	say := func(l string) { fmt.Println(l) }
	var k pcdfw.Keys
	var slots []string
	src := ""
	if keys != "" {
		if k, err = pcdfw.LoadKeyFile(keys); err != nil {
			fail(2, "%v", err)
		}
		src = "key file"
	} else if k, slots, err = devmode.KeysFromDisk(disk, dst.Size(), fw, fst.Size(), say); err != nil {
		fmt.Printf("\nRESULT: refused: %v\n", err)
		os.Exit(1)
	}
	l, err := devmode.BuildStash(devmode.StashInput{FW: fw, FWSize: fst.Size(), Ident: ident, IdentNow: identNow, Key: k, KeySlots: slots,
		KeySource: src, Manifests: ms, Disk: disk, DiskBytes: dst.Size(), BootID: "dry-run", RAMToken: "dry-run", Dir: out}, say)
	if err != nil {
		fmt.Printf("\nRESULT: refused: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("stash: %s (index sha256 %s): %s, key record sha256 %s… (key from %s)\n", out, l.IndexSHA256[:16], l.Firmware.Name,
		l.Key.RecordSHA256[:16], l.Key.Source)
	fmt.Println("\nRESULT: stash done (nothing written to the image)")
}

// mainlineKeyOf is the keep-key input of a dry-run mainline plan from the
// stash folder dir ("" = none: the plan refuses).
func mainlineKeyOf(dir string, r interface {
	ReadAt([]byte, int64) (int, error)
}, size int64) (*plan.MainlineKeyInput, *mlstash.Loaded) {
	if dir == "" {
		return nil, nil
	}
	l, err := mlstash.Load(dir)
	if err != nil {
		fail(2, "--stash: %v", err)
	}
	return plan.ReadMainlineKey(r, size, l.Record(), keykeep.TheGate(), devmode.StashProof(l)), l
}
