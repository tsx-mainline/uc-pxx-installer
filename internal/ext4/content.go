package ext4

// The reads of the clone's content check (step 2 plan kind clone,
// DESIGN-R23 §4.6): the link target, the data hash and the device number of
// one entry, none following a symbolic link. They are functions, not methods
// of *FS (as stat.go: a new method changes the method set of every binary
// that holds the type). Read only.

import (
	"encoding/binary"
	"fmt"
)

const (
	modeChar   = 0x2000
	modeBlock  = 0x6000
	modeFIFO   = 0x1000
	modeSocket = 0xC000
)

// KindNoFollow is the type of p in the letters of the content manifest:
// "f" (regular), "d", "l", "c", "b", "p" (FIFO), "s" (socket). No link is
// followed.
func KindNoFollow(fs *FS, p string) (string, error) {
	ino, err := walkNoFollow(fs, p)
	if err != nil {
		return "", err
	}
	in, err := fs.inode(ino)
	if err != nil {
		return "", err
	}
	switch in.mode & modeTypeMask {
	case modeFile:
		return "f", nil
	case modeDir:
		return "d", nil
	case modeLink:
		return "l", nil
	case modeChar:
		return "c", nil
	case modeBlock:
		return "b", nil
	case modeFIFO:
		return "p", nil
	case modeSocket:
		return "s", nil
	}
	return "", fmt.Errorf("%s: unknown file type %#o", p, in.mode&modeTypeMask)
}

// ReadlinkNoFollow is the target of the symbolic link p (its parents are
// never followed).
func ReadlinkNoFollow(fs *FS, p string) (string, error) {
	ino, err := walkNoFollow(fs, p)
	if err != nil {
		return "", err
	}
	in, err := fs.inode(ino)
	if err != nil {
		return "", err
	}
	if in.mode&modeTypeMask != modeLink {
		return "", fmt.Errorf("%s is not a symbolic link", p)
	}
	return fs.linkTarget(in)
}

// HashFileNoFollow is the size and the sha256 of the regular file p (block
// by block: no file is held in memory).
func HashFileNoFollow(fs *FS, p string) (int64, string, error) {
	ino, err := walkNoFollow(fs, p)
	if err != nil {
		return 0, "", err
	}
	in, err := fs.inode(ino)
	if err != nil {
		return 0, "", err
	}
	if in.mode&modeTypeMask != modeFile {
		return 0, "", fmt.Errorf("%s is not a regular file", p)
	}
	if in.flags&(flagInline|flagEncrypt) != 0 {
		return 0, "", fmt.Errorf("%s: inline or encrypted data is not read", p)
	}
	h, err := hashData(fs, in)
	return in.size, h, err
}

// DeviceNoFollow is the major and minor number of the character or block
// device node p (the kernel's encoding in i_block: the old 16-bit form in
// word 0 when it is not zero, else the new form in word 1).
func DeviceNoFollow(fs *FS, p string) (uint32, uint32, error) {
	ino, err := walkNoFollow(fs, p)
	if err != nil {
		return 0, 0, err
	}
	in, err := fs.inode(ino)
	if err != nil {
		return 0, 0, err
	}
	if t := in.mode & modeTypeMask; t != modeChar && t != modeBlock {
		return 0, 0, fmt.Errorf("%s is not a device node", p)
	}
	le := binary.LittleEndian
	if old := le.Uint32(in.block[0:]); old != 0 {
		return (old >> 8) & 0xff, old & 0xff, nil
	}
	n := le.Uint32(in.block[4:])
	return (n & 0xfff00) >> 8, (n & 0xff) | ((n >> 12) & 0xfff00), nil
}
