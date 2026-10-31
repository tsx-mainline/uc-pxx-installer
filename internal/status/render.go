package status

import (
	"fmt"
	"strings"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/nvram"
)

// Line is one line of the status text. Level: 0 normal, 1 heading, 2 good,
// 3 warning, 4 error. The screen gives each level a color.
type Line struct {
	Level int
	Text  string
}

const (
	Normal = iota
	Heading
	Good
	Warn
	Bad
)

func slotState(s avbab.Slot) (string, int) {
	switch s.State() {
	case "successful":
		return fmt.Sprintf("successful (priority %d)", s.Priority), Good
	case "trying":
		return fmt.Sprintf("trying (priority %d, %d tries left)", s.Priority, s.Tries), Warn
	case "unbootable":
		return "unbootable", Warn
	}
	return "ILLEGAL " + s.String(), Bad
}

// Lines renders the report as text.
func (r *Report) Lines() []Line {
	var out []Line
	add := func(level int, f string, a ...any) { out = append(out, Line{level, fmt.Sprintf(f, a...)}) }
	add(Heading, "Device")
	add(Normal, "  Model      %s", orUnknown(r.Model))
	add(Normal, "  Serial     %s", orUnknown(r.Serial))
	add(Normal, "  MAC        %s", orUnknown(r.MAC))
	add(Normal, "  Installer  kernel %s, image %s", orUnknown(r.Kernel), orUnknown(r.ImageToken))
	add(Normal, "")
	add(Heading, "Slots (A/B metadata, eMMC sector %d)", avbab.LBA)
	if !r.ABValid {
		add(Bad, "  The A/B metadata is not valid: %s", r.ABErr)
	}
	for i, s := range r.Slots {
		mark := "  "
		if i == r.Next {
			mark = "> "
		}
		st, lvl := slotState(s.AB)
		if !r.ABValid {
			st, lvl = "?", Bad
		}
		role := ""
		switch i {
		case r.Next:
			role = "  boots next"
		case r.Fallback:
			role = "  fallback"
		}
		add(lvl, "%sSlot %s  %s%s", mark, strings.ToUpper(s.Name), st, role)
		add(Normal, "      Holds     %s", orUnknown(s.Summary))
		add(Normal, "      U-Boot    %s", orUnknown(s.UBoot))
		add(Normal, "      boot_%s    %s", s.Name, orUnknown(s.Boot))
		add(Normal, "      system_%s  %s", s.Name, orUnknown(s.System))
		for _, e := range s.Errs {
			add(Warn, "      ! %s", e)
		}
	}
	if r.ABValid {
		if r.Next >= 0 {
			add(Normal, "  Next boot: slot %s.", strings.ToUpper(avbab.SlotName(r.Next)))
		}
		if r.Fallback < 0 {
			add(Warn, "  No fallback: only one slot is bootable.")
		}
		if r.AB.Tie() {
			add(Warn, "  Both slots have the same priority.")
		}
	}
	if r.PreFastboot != nil {
		if r.PreFastboot.Raw == r.AB.Raw {
			add(Good, "  The A/B metadata is the same as before the fastboot entry.")
		} else {
			add(Warn, "  The A/B metadata differs from the copy made before the fastboot entry.")
		}
	} else if r.PreFastbootErr != "" {
		add(Warn, "  %s", r.PreFastbootErr)
	}
	add(Normal, "")
	add(Heading, "Unlock state")
	lvl := Normal
	if r.Unlock == "unlocked" {
		lvl = Good
	}
	add(lvl, "  %s: %s", r.Unlock, r.UnlockWhy)
	add(Normal, "")
	add(Heading, "Android auto-update (nvram in %s)", nvram.PartName)
	au := r.AutoUpdate
	switch au.State {
	case nvram.Off:
		add(Good, "  Firmware   off (%s=false)", nvram.KeyFirmware)
	case nvram.On:
		add(Bad, "  Firmware   ON (%s=true): stock Android may install a firmware by itself", nvram.KeyFirmware)
	default:
		add(Warn, "  Firmware   unknown")
	}
	add(Normal, "  APK        %s (the Crestron apps only)", flagText(au.APK))
	add(Normal, "  URL        %s", orUnknown(au.URL))
	add(Normal, "  %s", au.Why)
	switch au.State {
	case nvram.Off:
	case nvram.On:
		add(Bad, "  The installer refuses until Auto Update Firmware is off (web UI, Settings, Auto Update).")
	default:
		add(Warn, "  Not clear. The installer goes on only if you confirm that Auto Update Firmware")
		add(Warn, "  is off (web UI, Settings, Auto Update).")
	}
	add(Normal, "")
	add(Normal, "Firmware key kept on the unit: %s", orUnknown(r.KeyKept))
	add(Normal, "")
	add(Heading, "Safety")
	add(Normal, "  This image only reads. eMMC read-only flag: %s", orUnknown(r.ReadOnly))
	for _, e := range r.Errs {
		add(Bad, "  ! %s", e)
	}
	return out
}

func flagText(v string) string {
	switch v {
	case "true":
		return "on"
	case "false":
		return "off"
	case "other":
		return "not true or false"
	}
	return "unknown"
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// Text renders the report as plain text.
func (r *Report) Text() string {
	var b strings.Builder
	for _, l := range r.Lines() {
		b.WriteString(l.Text)
		b.WriteByte('\n')
	}
	return b.String()
}
