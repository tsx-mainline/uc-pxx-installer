// Package gpt reads the GUID partition table of the UC-P10 eMMC. It only
// reads: Parse takes an io.ReaderAt. Build makes the bytes of a new table in
// memory (for test images) and writes nothing itself.
//
// The installer uses the table in two places. In a dry run it is the only
// source of the partition layout of the image file. On the device, the
// kernel's view (package blk) is the source, and the table is a cross-check.
//
// Sector size is 512 bytes. The primary header is at LBA 1 with its entries
// at LBA 2. The backup header is at the last LBA, with its entries in the 32
// sectors before it.
package gpt

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"sort"
	"unicode/utf16"
)

const (
	Sector     = 512
	HeaderSize = 92
	EntrySize  = 128
	NumEntries = 128
	// EntrySectors is the size of the entry array (128 * 128 bytes).
	EntrySectors = NumEntries * EntrySize / Sector
)

var signature = []byte("EFI PART")

// Entry is one used partition entry.
type Entry struct {
	Index int    // 1-based, as the kernel numbers it (mmcblk0pN)
	Name  string // GPT name, for example "boot_a"
	Start int64  // first LBA
	End   int64  // last LBA (inclusive), as in the GPT
	Type  [16]byte
	GUID  [16]byte
	Attrs uint64
}

// Sectors is the size in sectors.
func (e Entry) Sectors() int64 { return e.End - e.Start + 1 }

// Bytes is the size in bytes.
func (e Entry) Bytes() int64 { return e.Sectors() * Sector }

// Contains reports that LBA lba is inside the partition.
func (e Entry) Contains(lba int64) bool { return lba >= e.Start && lba <= e.End }

// Table is a parsed and checked partition table.
type Table struct {
	DiskSectors int64
	FirstUsable int64
	LastUsable  int64
	DiskGUID    [16]byte
	Entries     []Entry // sorted by Start
}

type header struct {
	myLBA, altLBA, firstUsable, lastUsable, entriesLBA int64
	diskGUID                                           [16]byte
	numEntries, entrySize, entriesCRC                  uint32
}

func readAt(r io.ReaderAt, lba int64, n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := r.ReadAt(b, lba*Sector); err != nil {
		return nil, fmt.Errorf("read LBA %d: %w", lba, err)
	}
	return b, nil
}

func parseHeader(b []byte, where string) (header, error) {
	var h header
	le := binary.LittleEndian
	if !bytes.Equal(b[:8], signature) {
		return h, fmt.Errorf("%s GPT header: no signature", where)
	}
	if le.Uint32(b[8:]) != 0x00010000 {
		return h, fmt.Errorf("%s GPT header: revision %08x", where, le.Uint32(b[8:]))
	}
	if le.Uint32(b[12:]) != HeaderSize {
		return h, fmt.Errorf("%s GPT header: header size %d", where, le.Uint32(b[12:]))
	}
	hdr := append([]byte(nil), b[:HeaderSize]...)
	stored := le.Uint32(hdr[16:])
	binary.LittleEndian.PutUint32(hdr[16:], 0)
	if crc32.ChecksumIEEE(hdr) != stored {
		return h, fmt.Errorf("%s GPT header: header CRC mismatch", where)
	}
	h.myLBA = int64(le.Uint64(b[24:]))
	h.altLBA = int64(le.Uint64(b[32:]))
	h.firstUsable = int64(le.Uint64(b[40:]))
	h.lastUsable = int64(le.Uint64(b[48:]))
	copy(h.diskGUID[:], b[56:72])
	h.entriesLBA = int64(le.Uint64(b[72:]))
	h.numEntries = le.Uint32(b[80:])
	h.entrySize = le.Uint32(b[84:])
	h.entriesCRC = le.Uint32(b[88:])
	if h.numEntries != NumEntries || h.entrySize != EntrySize {
		return h, fmt.Errorf("%s GPT header: %d entries of %d bytes (want %d of %d)",
			where, h.numEntries, h.entrySize, NumEntries, EntrySize)
	}
	return h, nil
}

func decodeName(b []byte) string {
	u := make([]uint16, 0, 36)
	for i := 0; i+1 < len(b); i += 2 {
		c := binary.LittleEndian.Uint16(b[i:])
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return string(utf16.Decode(u))
}

var zeroGUID [16]byte

// Parse reads and checks both tables of a disk of size bytes. It refuses a
// table that it cannot fully trust: a bad CRC in either copy, the two copies
// not agreeing, entries outside the usable range, overlapping entries, or
// two entries with one name.
func Parse(r io.ReaderAt, size int64) (*Table, error) {
	if size%Sector != 0 || size < 64*Sector {
		return nil, fmt.Errorf("disk size %d is not a whole number of sectors", size)
	}
	last := size/Sector - 1
	pb, err := readAt(r, 1, Sector)
	if err != nil {
		return nil, err
	}
	ph, err := parseHeader(pb, "primary")
	if err != nil {
		return nil, err
	}
	if ph.myLBA != 1 || ph.altLBA != last || ph.entriesLBA != 2 {
		return nil, fmt.Errorf("primary GPT header: my %d alt %d entries %d (want 1, %d, 2)",
			ph.myLBA, ph.altLBA, ph.entriesLBA, last)
	}
	bb, err := readAt(r, last, Sector)
	if err != nil {
		return nil, err
	}
	bh, err := parseHeader(bb, "backup")
	if err != nil {
		return nil, err
	}
	if bh.myLBA != last || bh.altLBA != 1 || bh.entriesLBA != last-EntrySectors {
		return nil, fmt.Errorf("backup GPT header: my %d alt %d entries %d", bh.myLBA, bh.altLBA, bh.entriesLBA)
	}
	if bh.firstUsable != ph.firstUsable || bh.lastUsable != ph.lastUsable || bh.diskGUID != ph.diskGUID ||
		bh.entriesCRC != ph.entriesCRC {
		return nil, errors.New("the primary and backup GPT headers do not agree")
	}
	if ph.firstUsable < 2+EntrySectors || ph.lastUsable >= last-EntrySectors || ph.firstUsable > ph.lastUsable {
		return nil, fmt.Errorf("GPT usable range %d..%d is not inside the disk", ph.firstUsable, ph.lastUsable)
	}
	pe, err := readAt(r, 2, EntrySectors*Sector)
	if err != nil {
		return nil, err
	}
	be, err := readAt(r, bh.entriesLBA, EntrySectors*Sector)
	if err != nil {
		return nil, err
	}
	if crc32.ChecksumIEEE(pe) != ph.entriesCRC {
		return nil, errors.New("primary GPT entries: CRC mismatch")
	}
	if !bytes.Equal(pe, be) {
		return nil, errors.New("the primary and backup GPT entries differ")
	}
	t := &Table{DiskSectors: last + 1, FirstUsable: ph.firstUsable, LastUsable: ph.lastUsable, DiskGUID: ph.diskGUID}
	names := map[string]int{}
	for i := 0; i < NumEntries; i++ {
		b := pe[i*EntrySize : (i+1)*EntrySize]
		var e Entry
		copy(e.Type[:], b[0:16])
		if e.Type == zeroGUID {
			continue
		}
		copy(e.GUID[:], b[16:32])
		e.Index = i + 1
		e.Start = int64(binary.LittleEndian.Uint64(b[32:]))
		e.End = int64(binary.LittleEndian.Uint64(b[40:]))
		e.Attrs = binary.LittleEndian.Uint64(b[48:])
		e.Name = decodeName(b[56:128])
		if e.Name == "" {
			return nil, fmt.Errorf("GPT entry %d has no name", e.Index)
		}
		if e.Start < t.FirstUsable || e.End > t.LastUsable || e.End < e.Start {
			return nil, fmt.Errorf("GPT entry %d (%s) %d..%d is outside the usable range", e.Index, e.Name, e.Start, e.End)
		}
		if j, dup := names[e.Name]; dup {
			return nil, fmt.Errorf("GPT entries %d and %d both have the name %s", j, e.Index, e.Name)
		}
		names[e.Name] = e.Index
		t.Entries = append(t.Entries, e)
	}
	if len(t.Entries) == 0 {
		return nil, errors.New("GPT has no partition")
	}
	sort.Slice(t.Entries, func(i, j int) bool { return t.Entries[i].Start < t.Entries[j].Start })
	for i := 1; i < len(t.Entries); i++ {
		a, b := t.Entries[i-1], t.Entries[i]
		if b.Start <= a.End {
			return nil, fmt.Errorf("GPT entries %s and %s overlap", a.Name, b.Name)
		}
	}
	return t, nil
}

// Find returns the entry with that name.
func (t *Table) Find(name string) (Entry, bool) {
	for _, e := range t.Entries {
		if e.Name == name {
			return e, true
		}
	}
	return Entry{}, false
}

// At returns the entry that holds lba.
func (t *Table) At(lba int64) (Entry, bool) {
	for _, e := range t.Entries {
		if e.Contains(lba) {
			return e, true
		}
	}
	return Entry{}, false
}

// BackupStart is the first LBA of the backup entry array.
func (t *Table) BackupStart() int64 { return t.DiskSectors - 1 - EntrySectors }

// LayoutSHA256 is a fingerprint of the layout: index, name, start and size of
// every entry, plus the disk size. It holds no GUID and no raw GPT byte, so
// it can go into a plan and a log.
func (t *Table) LayoutSHA256() string {
	h := sha256.New()
	fmt.Fprintf(h, "disk %d usable %d %d\n", t.DiskSectors, t.FirstUsable, t.LastUsable)
	for _, e := range t.Entries {
		fmt.Fprintf(h, "%d %s %d %d\n", e.Index, e.Name, e.Start, e.Sectors())
	}
	return hex.EncodeToString(h.Sum(nil))
}
