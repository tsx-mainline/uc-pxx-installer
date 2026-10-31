//go:build install

package devmode

import (
	"errors"
	"fmt"
	"io"

	"uc-pxx-installer/internal/ext4"
	"uc-pxx-installer/internal/gpt"
	"uc-pxx-installer/internal/keykeep"
	"uc-pxx-installer/internal/pcdfw"
)

// KeysFromDisk reads the updater libraries from system_a and system_b of a
// disk (an image file or the eMMC), read-only with the ext4 reader, never
// with a mount (design §5.8). A slot without them is skipped. It returns the
// key and the slots whose libraries gave it. The key is never printed.
func KeysFromDisk(r io.ReaderAt, size int64, fw io.ReaderAt, fwSize int64, say func(string)) (pcdfw.Keys, []string, error) {
	t, err := gpt.Parse(r, size)
	if err != nil {
		return pcdfw.Keys{}, nil, fmt.Errorf("GPT: %w", err)
	}
	var found []pcdfw.Keys
	var slots []string
	for _, s := range []string{"a", "b"} {
		e, ok := t.Find("system_" + s)
		if !ok {
			continue
		}
		fsys, err := ext4.Open(io.NewSectionReader(r, e.Start*gpt.Sector, e.Bytes()), e.Bytes())
		if err != nil {
			say(fmt.Sprintf("key search: system_%s: no ext4 file system (%v), skipped", s, err))
			continue
		}
		var libs []pcdfw.Lib
		for _, lib := range []string{"/system/lib64/libwebupdate.so", "/system/lib64/libprovdetect.so"} {
			if b, err := fsys.ReadFile(lib, 64<<20); err == nil {
				libs = append(libs, pcdfw.Lib{Name: "system_" + s + lib, Data: b})
			}
		}
		if len(libs) == 0 {
			say(fmt.Sprintf("key search: system_%s: no updater library", s))
			continue
		}
		res, err := pcdfw.FindKeys(fw, fwSize, libs, false)
		if err != nil || len(res.Pairs) != 1 {
			say(fmt.Sprintf("key search: system_%s: %d libraries, %d key pairs: not used", s, len(libs), len(res.Pairs)))
			continue
		}
		say(fmt.Sprintf("key search: system_%s: exactly one key/IV pair", s))
		found = append(found, res.Pairs[0])
		slots = append(slots, s)
	}
	switch {
	case len(found) == 0:
		return pcdfw.Keys{}, nil, errors.New("key search: no updater library with the key in system_a or system_b, and no key kept on the unit; give a key file")
	case len(found) == 2 && found[0] != found[1]:
		return pcdfw.Keys{}, nil, errors.New("key search: system_a and system_b give different keys, using none")
	}
	return found[0], slots, nil
}

// SystemCopies are the slots whose system holds the key k in its updater
// libraries (read-only, the ext4 reader). Errors are no copy.
func SystemCopies(r io.ReaderAt, size int64, fw io.ReaderAt, fwSize int64, k pcdfw.Keys) []string {
	got, slots, err := KeysFromDisk(r, size, fw, fwSize, func(string) {})
	if err != nil || got != k {
		return nil
	}
	return slots
}

// KeptRecords reads the two key-record sectors of backup (read-only) and
// returns them, with their LBAs. A unit without a fitting backup has none.
func KeptRecords(r io.ReaderAt, size int64) ([2][]byte, [2]int64, error) {
	var recs [2][]byte
	t, err := gpt.Parse(r, size)
	if err != nil {
		return recs, [2]int64{}, err
	}
	e, ok := t.Find(keykeep.Partition)
	if !ok {
		return recs, [2]int64{}, errors.New("no backup partition")
	}
	lbas, err := keykeep.LBAs(e.Start, e.Sectors())
	if err != nil {
		return recs, lbas, err
	}
	for i, l := range lbas {
		b := make([]byte, gpt.Sector)
		if _, err := r.ReadAt(b, l*gpt.Sector); err != nil {
			return recs, lbas, fmt.Errorf("read backup LBA %d: %w", l, err)
		}
		recs[i] = b
	}
	return recs, lbas, nil
}

// KeyFromRecord is the key kept on the unit: the first record of the two
// that decodes and decrypts this firmware (pcdfw Check).
func KeyFromRecord(r io.ReaderAt, size int64, fw io.ReaderAt, fwSize int64) (pcdfw.Keys, error) {
	recs, _, err := KeptRecords(r, size)
	if err != nil {
		return pcdfw.Keys{}, err
	}
	for _, rec := range recs {
		k, err := keykeep.Decode(rec)
		if err != nil {
			continue
		}
		f, err := pcdfw.Open(fw, fwSize, k)
		if err != nil {
			continue
		}
		if _, err := f.Check(); err == nil {
			return k, nil
		}
	}
	return pcdfw.Keys{}, errors.New("no key record on the unit that decrypts this firmware")
}
