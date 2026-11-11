package ext4

import (
	"encoding/binary"
	"fmt"
	"path"
	"strings"
)

// ReadDirNoFollow lists the names in the directory p ("." and ".." left
// out), at most max of them. It never follows a symbolic link: a component
// of p (the last one too) that is a link, a file or anything but a directory
// is an error. found is false, with no error, when a component of p is not
// in its directory. Every directory on the path is parsed strictly
// (strictEntries): a malformed record is an error, never skipped
// (MAINLINE-RC3-REVIEW C3-2). It only reads (uc-pxx-step2 reads the hold
// folder of the running root file system with it, beneath a bind mount:
// MAINLINE-RC2-LS review A-1).
func (fs *FS) ReadDirNoFollow(p string, max int) (names []string, found bool, err error) {
	cur, err := fs.inode(rootIno)
	if err != nil {
		return nil, false, err
	}
	if cur.mode&modeTypeMask != modeDir {
		return nil, false, fmt.Errorf("the root is not a directory")
	}
	at := "/"
	for _, part := range strings.Split(strings.Trim(path.Clean("/"+p), "/"), "/") {
		if part == "" {
			continue
		}
		ents, err := fs.strictEntries(cur)
		if err != nil {
			return nil, false, fmt.Errorf("%s: %v", at, err)
		}
		ino := uint32(0)
		for _, e := range ents {
			if e.name == part {
				ino = e.ino
				break
			}
		}
		at = path.Join(at, part)
		if ino == 0 {
			return nil, false, nil
		}
		if cur, err = fs.inode(ino); err != nil {
			return nil, false, fmt.Errorf("%s: %v", at, err)
		}
		if t := cur.mode & modeTypeMask; t != modeDir {
			return nil, false, fmt.Errorf("%s is not a directory (%s)", at, typeName(t))
		}
	}
	ents, err := fs.strictEntries(cur)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %v", at, err)
	}
	if len(ents) > max {
		return nil, false, fmt.Errorf("%s: more than %d entries", at, max)
	}
	for _, e := range ents {
		names = append(names, e.name)
	}
	return names, true, nil
}

func typeName(t uint16) string {
	switch t {
	case modeFile:
		return "a regular file"
	case modeLink:
		return "a symbolic link"
	}
	return fmt.Sprintf("file type %#o", t)
}

// strictEntries are the entries of a directory without "." and "..", like
// dirEntries, but a malformed record is an error: in each block the records
// must chain exactly to the block's end, each with rec_len >= 8, a multiple
// of 4, inside the block, name_len <= rec_len-8, and a used record (inode
// not 0) needs a name without '/' and NUL. The empty records of a hashed
// directory's index blocks and the checksum tail (inode 0) are records too.
func (fs *FS) strictEntries(dir *inode) ([]dirent, error) {
	if dir.mode&modeTypeMask != modeDir {
		return nil, fmt.Errorf("not a directory")
	}
	if dir.size > 64<<20 || dir.size%fs.blockSize != 0 {
		return nil, fmt.Errorf("directory size %d is not a whole number of blocks", dir.size)
	}
	b, err := fs.data(dir, dir.size)
	if err != nil {
		return nil, err
	}
	le := binary.LittleEndian
	var out []dirent
	for blk := int64(0); blk < int64(len(b)); blk += fs.blockSize {
		end := blk + fs.blockSize
		for off := blk; off < end; {
			if off+8 > end {
				return nil, fmt.Errorf("a record header past the block at byte %d", off)
			}
			ino := le.Uint32(b[off:])
			rec := int64(le.Uint16(b[off+4:]))
			nl := int64(b[off+6])
			if rec < 8 || rec%4 != 0 || off+rec > end || nl > rec-8 {
				return nil, fmt.Errorf("a malformed record at byte %d (rec_len %d, name_len %d)", off, rec, nl)
			}
			if ino != 0 {
				name := string(b[off+8 : off+8+nl])
				if nl == 0 || strings.ContainsAny(name, "/\x00") {
					return nil, fmt.Errorf("a record with the name %q at byte %d", name, off)
				}
				if name != "." && name != ".." {
					out = append(out, dirent{ino, name})
				}
			}
			off += rec
		}
	}
	return out, nil
}
