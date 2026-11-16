package provision

import (
	"fmt"
	"io"
	"path/filepath"

	"uc-pxx-installer/internal/clone"
)

// Check is `uc-pxx-provision check` (read only): the profiles this program
// offers on the unit at root, the done record, and the content manifest of
// root (the check of the end of provisioning).
func Check(w io.Writer, root string) error {
	g, rel := clone.EmbeddedGate(), clone.Release()
	fmt.Fprintf(w, "release data: release %q, %d profile(s), uc-pxx-xfer %q\n", rel.Release, len(rel.Profiles), rel.XferSHA256)
	fmt.Fprintf(w, "exclusion list %s (%d rules), difference %s\n", g.ExcludeSHA256(), len(g.Exclude), g.DeltaSHA256())
	b, err := readRegular(filepath.Join(root, ProfilesFile), MaxProfiles)
	if err != nil {
		return err
	}
	ps, err := ParseProfiles(b)
	if err != nil {
		return err
	}
	offered, notes, err := Offer(ps, g, rel)
	for _, n := range notes {
		fmt.Fprintln(w, n)
	}
	if err != nil {
		return err
	}
	for _, p := range offered {
		fmt.Fprintf(w, "offered: %s (apply %s, bundle %s, ref_b %s)\n", p.Name, p.Apply, p.BundleSHA256(), p.RefB)
	}
	if d, err := readRegular(filepath.Join(root, clone.DoneRecordPath), clone.MaxDoneRecord); err == nil {
		if r, err := doneFor(d, ps, offered); err != nil {
			fmt.Fprintf(w, "done record: does not count: %v\n", err)
		} else {
			fmt.Fprintf(w, "done record: %s, ref_b %s\n", r.Profile, r.RefBSHA256)
		}
	} else {
		fmt.Fprintf(w, "done record: none (%v)\n", err)
	}
	r, err := clone.LiveContent(root, g.Exclude)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "content of %s: %d entries, sha256 %s\n", root, r.Entries, r.SHA256)
	return nil
}
