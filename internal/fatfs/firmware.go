package fatfs

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Reason says why FindFirmware refused the drive.
type Reason int

const (
	NotFAT     Reason = iota + 1 // no FAT file system that this package reads
	NoFirmware                   // no .bin in the top folder
	TwoOrMore                    // two or more .bin files in the top folder
	Dirty                        // the volume was not unmounted cleanly
	Damaged                      // the file system has errors
	Empty                        // the .bin is empty
)

func (r Reason) String() string {
	switch r {
	case NotFAT:
		return "not FAT"
	case NoFirmware:
		return "no firmware file"
	case TwoOrMore:
		return "two or more firmware files"
	case Dirty:
		return "not ejected cleanly"
	case Damaged:
		return "damaged"
	case Empty:
		return "empty firmware file"
	}
	return "unknown"
}

// Refusal is a drive that FindFirmware does not use. Why is a sentence for
// the user (the names in it are quoted, so a control character in a file name
// cannot reach the screen); Detail lists what the check found.
type Refusal struct {
	Reason Reason
	Why    string
	Detail []string
}

func (r *Refusal) Error() string {
	if len(r.Detail) == 0 {
		return r.Why
	}
	return r.Why + " (" + strings.Join(r.Detail, "; ") + ")"
}

// Quote is a file name as the messages show it: in double quotes, with
// control and non-printable characters escaped.
func Quote(name string) string { return strconv.Quote(name) }

// IsFirmwareName reports whether a top-folder file counts as a firmware file:
// a name that ends in ".bin" (any case). The AppleDouble files that macOS
// writes next to a copied file ("._NAME.bin") do not count.
func IsFirmwareName(name string) bool {
	return len(name) > 4 && strings.EqualFold(name[len(name)-4:], ".bin") && !strings.HasPrefix(name, "._")
}

// FindFirmware applies the rules of the drive (INSTALLER-END-USER-PLAN.md
// §2.6): exactly one *.bin file in the top folder (not none, not two or
// more), a clean volume (the dirty bits, and a read-only check of every
// directory and chain), and a file that is not empty. It returns the opened
// file, which reads the clusters in place.
func FindFirmware(fs *FS) (*File, error) {
	es, err := fs.readDir(0)
	if err != nil {
		return nil, &Refusal{Reason: Damaged, Why: "the top folder of the UC-PXX-INST drive cannot be read: copy the firmware again",
			Detail: []string{err.Error()}}
	}
	var bins []Entry
	for _, e := range es {
		if !e.Dir && IsFirmwareName(e.Name) {
			bins = append(bins, e)
		}
	}
	switch len(bins) {
	case 0:
		return nil, &Refusal{Reason: NoFirmware,
			Why: "there is no firmware file (*.bin) in the top folder of the UC-PXX-INST drive: copy the firmware .bin there, then eject the drive"}
	case 1:
	default:
		var names []string
		for _, e := range bins {
			names = append(names, Quote(e.Name))
		}
		sort.Strings(names)
		return nil, &Refusal{Reason: TwoOrMore, Detail: names,
			Why: fmt.Sprintf("there are %d firmware files (*.bin) in the top folder of the UC-PXX-INST drive: leave only one there, then eject the drive", len(bins))}
	}
	if d := fs.Dirty(); len(d) > 0 {
		return nil, &Refusal{Reason: Dirty, Detail: d,
			Why: "the UC-PXX-INST drive was not ejected cleanly, so the copy can be incomplete: copy the firmware again, then eject the drive on the computer"}
	}
	if p := fs.Check(); len(p) > 0 {
		return nil, &Refusal{Reason: Damaged, Detail: p,
			Why: "the file system of the UC-PXX-INST drive has errors: copy the firmware again"}
	}
	f, err := fs.openEntry(bins[0])
	if err != nil {
		return nil, &Refusal{Reason: Damaged, Detail: []string{err.Error()},
			Why: "the firmware file " + Quote(bins[0].Name) + " cannot be read: copy the firmware again"}
	}
	if f.size == 0 {
		return nil, &Refusal{Reason: Empty, Why: "the firmware file " + Quote(f.name) + " is empty: copy the firmware again"}
	}
	return f, nil
}

// OpenFirmware opens the drive image read-only and finds the firmware file
// (FindFirmware). Close the file to close the image.
func OpenFirmware(image string) (*File, error) {
	fs, err := OpenImage(image)
	if err != nil {
		switch {
		case errors.Is(err, ErrNotFAT):
			return nil, &Refusal{Reason: NotFAT, Detail: []string{err.Error()},
				Why: "the UC-PXX-INST drive has no FAT file system that the installer can read: do not format the drive; restart the installer and copy the firmware onto the new drive"}
		case errors.Is(err, ErrDamaged):
			return nil, &Refusal{Reason: Damaged, Detail: []string{err.Error()},
				Why: "the file system of the UC-PXX-INST drive has errors: copy the firmware again"}
		}
		return nil, err
	}
	f, err := FindFirmware(fs)
	if err != nil {
		fs.Close()
		return nil, err
	}
	f.owner = true
	return f, nil
}
