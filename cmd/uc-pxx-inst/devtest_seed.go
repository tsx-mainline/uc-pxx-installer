//go:build install && devtest

package main

// uc-pxx-inst test-check-seed --image IMG --plan PLAN --rootfs ROOTFS (devtest;
// ci/dryrun-check.py): system_b of the dry-run image after the ssh key seed
// (mainline-rc-8): the plan's ssh-key step's files read back (install.Seeded:
// the file system clean, the two files root's with the plan's modes, sizes,
// sha256), and every other regular file of the image equal to the payload's
// root file system ROOTFS (the seed changed nothing else). Exit 0: ok; 1: not
// seeded or something else changed; 2: a usage or read error.

import (
	"bytes"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"os"

	"uc-pxx-installer/internal/ext4"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/install"
	"uc-pxx-installer/internal/keywrite"
	"uc-pxx-installer/internal/plan"
)

func init() { extraCmds["test-check-seed"] = cmdTestCheckSeed }

func cmdTestCheckSeed(args []string) {
	fs := flag.NewFlagSet("test-check-seed", flag.ExitOnError)
	img := fs.String("image", "", "the dry-run image")
	pf := fs.String("plan", "", "the direct plan JSON")
	rf := fs.String("rootfs", "", "the payload's rootfs.ext4")
	fs.Parse(args)
	if *img == "" || *pf == "" || *rf == "" {
		usage()
	}
	raw, err := os.ReadFile(*pf)
	if err != nil {
		fail(2, "%v", err)
	}
	p, err := plan.DirectPlanOf(raw)
	if err != nil {
		fail(2, "%v", err)
	}
	var w *plan.WriteStep
	var ks *plan.SSHKeyStep
	for _, s := range p.Steps {
		if s.Write != nil && s.Write.Partition == "system_b" {
			w = s.Write
		}
		if s.Op == "ssh-key" {
			ks = s.SSHKey
		}
	}
	if w == nil || ks == nil {
		fail(2, "the plan has no system_b write or no ssh-key step")
	}
	f, err := os.Open(*img)
	if err != nil {
		fail(2, "%v", err)
	}
	defer f.Close()
	if !install.Seeded(f, &p.Plan, w) {
		fmt.Println("seed: NOT seeded (keywrite.Check of the plan's files fails)")
		os.Exit(1)
	}
	sys := io.NewSectionReader(f, w.StartLBA*gpt.Sector, w.Length)
	got, err := files(sys, w.Length)
	if err != nil {
		fail(2, "system_b: %v", err)
	}
	rb, err := os.ReadFile(*rf)
	if err != nil {
		fail(2, "%v", err)
	}
	want, err := files(bytes.NewReader(rb), int64(len(rb)))
	if err != nil {
		fail(2, "rootfs: %v", err)
	}
	seed := map[string]bool{}
	for _, sf := range ks.Files {
		seed[sf.Path] = true
	}
	bad := 0
	for p, h := range want {
		if got[p] != h {
			fmt.Printf("seed: %s differs from the payload's rootfs\n", p)
			bad++
		}
	}
	for p := range got {
		if _, ok := want[p]; !ok && !seed[p] {
			fmt.Printf("seed: %s is new and not a seed file\n", p)
			bad++
		}
	}
	if bad > 0 {
		os.Exit(1)
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.NewSectionReader(f, w.StartLBA*gpt.Sector, w.Length)); err != nil {
		fail(2, "%v", err)
	}
	fmt.Printf("seed: ok: %s and %s read back (%s, folder made %v); the other %d files equal the payload's rootfs\n",
		ks.Files[0].Path, ks.Files[1].Path, ks.Fingerprint, ks.FolderMade, len(want))
	fmt.Printf("seeded-sha256 %x\n", h.Sum(nil))
	_ = keywrite.Folder
}

func files(r io.ReaderAt, n int64) (map[string]string, error) {
	fs, err := ext4.Open(r, n)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	err = fs.Walk(500000, func(p string) {
		if b, err := fs.ReadFile(p, 256<<20); err == nil {
			s := sha256.Sum256(b)
			out[p] = fmt.Sprintf("%x", s)
		}
	})
	return out, err
}
