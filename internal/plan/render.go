package plan

import (
	"fmt"
	"io"
	"strings"

	"uc-pxx-installer/internal/nvram"
)

func shortSHA(s string) string {
	if len(s) > 16 {
		return s[:16] + "…"
	}
	return s
}

// Render prints the plan for a human.
func (p *Plan) Render(w io.Writer) {
	pf := func(f string, a ...any) { fmt.Fprintf(w, f, a...) }
	title := "uc-pxx-inst install plan"
	if p.ID != "" {
		title += "  id " + p.ID
	} else {
		title += "  (REFUSED)"
	}
	pf("%s\nmode      %s\n", title, p.Mode)
	if p.Disk.Sectors > 0 {
		pf("disk      %s, %d sectors, GPT layout %s\n", p.Disk.Source, p.Disk.Sectors, shortSHA(p.Disk.LayoutSHA256))
	}
	if p.RAMImage != nil {
		pf("RAM image token %s (uc-pxx.id= on the kernel command line)\n", p.RAMImage.Token)
	}
	if p.Payload != nil {
		pf("payload   %s %s (incremental %s), %s, sha256 %s\n", p.Payload.Version, p.Payload.BuildType,
			p.Payload.Incremental, p.Payload.FirmwareFile, shortSHA(p.Payload.FirmwareSHA256))
	}
	if p.KeySource != "" {
		pf("key       from %s (never shown, never logged)\n", p.KeySource)
	}
	if p.Bootctl.Path != "" {
		pf("bootctl   %s %s sha256 %s\n", p.Bootctl.Exec, p.Bootctl.Path, shortSHA(p.Bootctl.SHA256))
	}
	if p.AB.Now.Hex32 != "" {
		pf("A/B now   %s\n", p.AB.Now)
	}
	if p.AB.PreFastboot != nil {
		pf("A/B before fastboot entry  %s\n", p.AB.PreFastboot)
	}
	if p.Keeper != "" {
		pf("keeper    slot %s: %s. Never written; only its priority may drop 15 -> 14\n", strings.ToUpper(p.Keeper), p.KeeperWhy)
		pf("target    slot %s: all its partitions are replaced\n", strings.ToUpper(p.Target))
	}
	if a := p.AutoUpdate; a != nil {
		switch {
		case a.State == nvram.Off:
			pf("autoupdate Auto Update Firmware of stock Android: off (nvram part %d, write counter %d)\n", a.Part, a.Counter)
		case a.State == nvram.On:
			pf("autoupdate Auto Update Firmware of stock Android: ON: always refused\n")
		case a.UnclearConfirmed:
			pf("autoupdate Auto Update Firmware of stock Android: NOT CLEAR, the user confirmed to go on (%s)\n", UnclearFlag)
		default:
			pf("autoupdate Auto Update Firmware of stock Android: NOT CLEAR, not confirmed (%s): refused\n", UnclearFlag)
		}
	}
	pf("checks\n")
	for _, c := range p.Checks {
		mark := "ok  "
		if !c.OK {
			mark = "FAIL"
		}
		pf("  %s %-16s %s\n", mark, c.ID, c.Detail)
	}
	if len(p.Steps) > 0 {
		pf("steps\n")
	}
	for _, s := range p.Steps {
		switch s.Op {
		case "bootctl":
			b := s.Bootctl
			extra := ""
			if b.NoOp {
				extra = " (already so: no change)"
			}
			pf("  %2d bootctl %-28s -> %s%s\n", s.N, strings.Join(b.Args, " "), b.After, extra)
		case "write":
			w := s.Write
			pf("  %2d write   %-9s p%-2d LBA %-9d %11d bytes  sha256 %s\n", s.N, w.Partition, w.GPTIndex, w.StartLBA, w.Length, shortSHA(w.SHA256))
		case "skip":
			w := s.Write
			pf("  %2d skip    %-9s p%-2d already holds the image (sha256 %s)\n", s.N, w.Partition, w.GPTIndex, shortSHA(w.SHA256))
		case "sync":
			pf("  %2d sync\n", s.N)
		case "verify":
			pf("  %2d verify  re-read %s\n", s.N, strings.Join(s.Verify, " "))
		case "check-ab":
			pf("  %2d check   A/B read back must be %s\n", s.N, s.Expect)
		}
	}
	if len(p.Steps) > 0 {
		pf("never written\n")
		for _, s := range p.NeverWritten {
			pf("  - %s\n", s)
		}
	}
	for _, n := range p.Notes {
		pf("note: %s\n", n)
	}
}
