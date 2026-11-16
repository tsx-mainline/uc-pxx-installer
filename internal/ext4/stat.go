package ext4

// The checks of the ssh key seed (mainline-rc-8, package keywrite): the
// superblock state of a root file system before and after the installer
// mounts it, and the owner, mode and content of a file, read without
// following any link. Read only, like the rest of the package.
//
// They are functions, not methods of *FS: a new method changes the method
// table of *FS's type descriptor in every binary that links the package, and
// uc-pxx-xfer (pinned 62421c48…, ci/build-xfer.sh) links it but none of these.

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// Super is the part of the superblock that tells whether a file system is
// clean: s_state (1 = cleanly unmounted, 2 = errors), the incompatible
// features (0x4 = the journal needs recovery), the head of the orphan list.
type Super struct {
	State      uint16
	Incompat   uint32
	ROCompat   uint32
	LastOrphan uint32
	ErrorCount uint32
	HasJournal bool
}

// Clean: unmounted cleanly, no errors recorded, no journal to replay, no
// orphan inode.
func (s Super) Clean() bool {
	// RO_COMPAT_ORPHAN_PRESENT 0x10000: an orphan file with entries
	return s.State&1 == 1 && s.State&2 == 0 && s.Incompat&0x4 == 0 && s.LastOrphan == 0 && s.ROCompat&0x10000 == 0 && s.ErrorCount == 0
}

func (s Super) String() string {
	return fmt.Sprintf("state 0x%x, incompat 0x%x (recover %v), ro_compat 0x%x, last orphan %d, errors %d, journal %v", s.State, s.Incompat,
		s.Incompat&0x4 != 0, s.ROCompat, s.LastOrphan, s.ErrorCount, s.HasJournal)
}

// ReadSuper reads the superblock's state fields.
func ReadSuper(r io.ReaderAt) (Super, error) {
	sb := make([]byte, 1024)
	if _, err := r.ReadAt(sb, sbOffset); err != nil {
		return Super{}, err
	}
	le := binary.LittleEndian
	if le.Uint16(sb[0x38:]) != magic {
		return Super{}, ErrNotExt4
	}
	return Super{State: le.Uint16(sb[0x3A:]), Incompat: le.Uint32(sb[0x60:]), ROCompat: le.Uint32(sb[0x64:]), LastOrphan: le.Uint32(sb[0xE8:]),
		ErrorCount: le.Uint32(sb[0x194:]), HasJournal: le.Uint32(sb[0x5C:])&0x4 != 0}, nil
}

// Stat is a file's type, mode, owner and size.
type Stat struct {
	Type string // "file", "dir", "link", …
	Perm uint16 // the low 12 bits of i_mode
	UID  uint32
	GID  uint32
	Size int64
}

// ErrNotFound: a component of the path is not in its directory.
var ErrNotFound = errors.New("not found")

// LstatNoFollow is the stat of p. It never follows a symbolic link: every
// component before the last must be a directory (strictly parsed); the last
// one is reported as it is. A missing component is ErrNotFound.
func LstatNoFollow(fs *FS, p string) (Stat, error) {
	ino, err := walkNoFollow(fs, p)
	if err != nil {
		return Stat{}, err
	}
	return statOf(fs, ino)
}

// ReadFileNoFollow reads the regular file p (no link followed anywhere),
// at most max bytes, with its stat.
func ReadFileNoFollow(fs *FS, p string, max int64) ([]byte, Stat, error) {
	ino, err := walkNoFollow(fs, p)
	if err != nil {
		return nil, Stat{}, err
	}
	st, err := statOf(fs, ino)
	if err != nil {
		return nil, Stat{}, err
	}
	if st.Type != "file" {
		return nil, st, fmt.Errorf("%s is not a regular file (%s)", p, st.Type)
	}
	if st.Size > max {
		return nil, st, fmt.Errorf("%s: %d bytes, more than %d", p, st.Size, max)
	}
	in, err := fs.inode(ino)
	if err != nil {
		return nil, st, err
	}
	b, err := fs.data(in, in.size)
	return b, st, err
}

func walkNoFollow(fs *FS, p string) (uint32, error) {
	cur := uint32(rootIno)
	at := "/"
	parts := strings.Split(strings.Trim(path.Clean("/"+p), "/"), "/")
	for _, part := range parts {
		if part == "" {
			continue
		}
		dir, err := fs.inode(cur)
		if err != nil {
			return 0, fmt.Errorf("%s: %v", at, err)
		}
		if t := dir.mode & modeTypeMask; t != modeDir {
			return 0, fmt.Errorf("%s is not a directory (%s)", at, typeName(t))
		}
		ents, err := fs.strictEntries(dir)
		if err != nil {
			return 0, fmt.Errorf("%s: %v", at, err)
		}
		next := uint32(0)
		for _, e := range ents {
			if e.name == part {
				next = e.ino
				break
			}
		}
		at = path.Join(at, part)
		if next == 0 {
			return 0, fmt.Errorf("%s: %w", at, ErrNotFound)
		}
		cur = next
	}
	return cur, nil
}

func statOf(fs *FS, ino uint32) (Stat, error) {
	in, err := fs.inode(ino)
	if err != nil {
		return Stat{}, err
	}
	// the owner: i_uid (0x2), i_gid (0x18), and the high halves of osd2
	// (Linux: l_i_uid_high 0x78, l_i_gid_high 0x7A)
	group := (ino - 1) / fs.inodesPer
	index := int64((ino - 1) % fs.inodesPer)
	desc := make([]byte, fs.descSize)
	if _, err := fs.r.ReadAt(desc, fs.gdtBlock*fs.blockSize+int64(group)*fs.descSize); err != nil {
		return Stat{}, err
	}
	le := binary.LittleEndian
	table := int64(le.Uint32(desc[0x8:]))
	if fs.descSize >= 64 {
		table |= int64(le.Uint32(desc[0x28:])) << 32
	}
	raw := make([]byte, fs.inodeSize)
	if _, err := fs.r.ReadAt(raw, table*fs.blockSize+index*fs.inodeSize); err != nil {
		return Stat{}, err
	}
	return Stat{Type: kindOf(in.mode & modeTypeMask), Perm: in.mode & 0o7777,
		UID:  uint32(le.Uint16(raw[0x2:])) | uint32(le.Uint16(raw[0x78:]))<<16,
		GID:  uint32(le.Uint16(raw[0x18:])) | uint32(le.Uint16(raw[0x7A:]))<<16,
		Size: in.size}, nil
}

func kindOf(t uint16) string {
	switch t {
	case modeFile:
		return "file"
	case modeDir:
		return "dir"
	case modeLink:
		return "link"
	}
	return fmt.Sprintf("type-%#o", t)
}

// InodeOffset is the byte offset of the inode of p in the file system (no
// link followed). Read only; tests use it to make an image's files root's.
func InodeOffset(fs *FS, p string) (int64, error) {
	ino, err := walkNoFollow(fs, p)
	if err != nil {
		return 0, err
	}
	group := (ino - 1) / fs.inodesPer
	index := int64((ino - 1) % fs.inodesPer)
	desc := make([]byte, fs.descSize)
	if _, err := fs.r.ReadAt(desc, fs.gdtBlock*fs.blockSize+int64(group)*fs.descSize); err != nil {
		return 0, err
	}
	le := binary.LittleEndian
	table := int64(le.Uint32(desc[0x8:]))
	if fs.descSize >= 64 {
		table |= int64(le.Uint32(desc[0x28:])) << 32
	}
	return table*fs.blockSize + index*fs.inodeSize, nil
}

// Digest is the sha256 of every entry of the tree, by path, without
// following links: a regular file "file MODE UID:GID sha256", a directory
// "dir MODE UID:GID", a symbolic link "link TARGET", anything else its type.
// File data is hashed block by block (no file is held in memory). Read only.
func Digest(fs *FS, max int) (map[string]string, error) {
	out := map[string]string{}
	var ferr error
	err := fs.Walk(max, func(p string) {
		if ferr != nil {
			return
		}
		ino, err := walkNoFollow(fs, p)
		if err != nil {
			ferr = err
			return
		}
		st, err := statOf(fs, ino)
		if err != nil {
			ferr = err
			return
		}
		head := fmt.Sprintf("%s %o %d:%d", st.Type, st.Perm, st.UID, st.GID)
		switch st.Type {
		case "file":
			in, err := fs.inode(ino)
			if err != nil {
				ferr = err
				return
			}
			h, err := hashData(fs, in)
			if err != nil {
				ferr = fmt.Errorf("%s: %v", p, err)
				return
			}
			out[p] = head + " " + h
		case "link":
			in, err := fs.inode(ino)
			if err != nil {
				ferr = err
				return
			}
			t, err := fs.linkTarget(in)
			if err != nil {
				ferr = fmt.Errorf("%s: %v", p, err)
				return
			}
			out[p] = "link " + t
		default:
			out[p] = head
		}
	})
	if err == nil {
		err = ferr
	}
	return out, err
}

func hashData(fs *FS, in *inode) (string, error) {
	nb := (in.size + fs.blockSize - 1) / fs.blockSize
	phys, err := fs.blocks(in, nb)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	left := in.size
	zero := make([]byte, fs.blockSize)
	for _, p := range phys {
		b := zero
		if p != 0 {
			if b, err = fs.readBlock(p); err != nil {
				return "", err
			}
		}
		k := int64(len(b))
		if left < k {
			k = left
		}
		h.Write(b[:k])
		left -= k
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
