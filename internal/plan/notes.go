package plan

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"uc-pxx-installer/internal/ext4"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/manifest"
)

// The plan notes of a payload, for the plan screen (FIRMWARE-GENERAL §10:
// plain words for end users). They warn; they never refuse.
const (
	// NoteTestBuild: the manifest marks the file as a Crestron test build.
	NoteTestBuild = "This is a Crestron test build (its file name says so), not a normal release."
	// noteUserTarget: a user U-Boot in the target (a stock Crestron build).
	noteUserTarget = "After the install, the unit starts Crestron firmware %s by default. It runs only software that Crestron signed. To start the system in slot %s again, use the installer's switch to slot %s."
	// noteDowngrade: the payload's Android is older than an Android on the unit.
	noteDowngrade = "This firmware is older than the Android now on the unit (%s). When it starts, Android may ask to erase its data (a factory reset). The installer never touches that data."
)

// androidOf reads the build of the stock Android in system_<slot>, if there
// is one: its build date (ro.build.date.utc) and a name for the screen. It
// only reads; a slot without an Android build.prop (mainline, empty, not
// ext4) gives ok false.
func androidOf(in Input, t *gpt.Table, slot string, ms []*manifest.Manifest) (date int64, name string, ok bool) {
	e, found := t.Find("system_" + slot)
	if !found {
		return 0, "", false
	}
	fs, err := ext4.Open(io.NewSectionReader(in.Disk, e.Start*gpt.Sector, e.Bytes()), e.Bytes())
	if err != nil || fs.Label == "tsxroot" {
		return 0, "", false
	}
	var props map[string]string
	for _, p := range []string{"/system/build.prop", "/build.prop"} {
		if b, err := fs.ReadFile(p, 1<<20); err == nil {
			props = ext4.Props(b)
			break
		}
	}
	date, err = strconv.ParseInt(props["ro.build.date.utc"], 10, 64)
	if props == nil || err != nil || date <= 0 {
		return 0, "", false
	}
	if m := manifest.ByFingerprint(ms, props["ro.build.fingerprint"]); m != nil {
		return date, "Crestron " + m.Name() + " in slot " + strings.ToUpper(slot), true
	}
	return date, "Android build " + props["ro.build.version.incremental"] + " in slot " + strings.ToUpper(slot), true
}

func payloadNotes(in Input, t *gpt.Table, p *Plan) []string {
	pl := p.Payload
	var notes []string
	if pl.TestBuild {
		notes = append(notes, NoteTestBuild)
	}
	if pl.BootChain.UBootBuild == "user" {
		k := strings.ToUpper(p.Keeper)
		notes = append(notes, fmt.Sprintf(noteUserTarget, pl.Version, k, k))
	}
	// The newest Android on the unit (keeper or the target's old content):
	// the userdata partition is shared and may be encrypted for it.
	var newest int64
	var newer string
	for _, s := range []string{p.Keeper, p.Target} {
		if d, n, ok := androidOf(in, t, s, pl.ms); ok && d > newest {
			newest, newer = d, n
		}
	}
	if pl.BuildDateUTC > 0 && newest > pl.BuildDateUTC {
		notes = append(notes, fmt.Sprintf(noteDowngrade, newer))
	}
	return notes
}
