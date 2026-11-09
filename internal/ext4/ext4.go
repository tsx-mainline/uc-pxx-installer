// Package ext4 reads single small files from an ext4 file system, through an
// io.ReaderAt. It has no write path and never replays the journal, so it can
// read a partition of the eMMC without mounting it. The installer uses it for
// build.prop of an Android system_X and for the build id of a uc-pxx root
// file system.
//
// It supports what these file systems use: 32-bit and 64-bit group
// descriptors, extent trees and the old block maps, linear and hashed
// directories (a hashed directory also reads as a linear one), and symbolic
// links. It does not support inline data or encrypted files.
package ext4

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

const (
	sbOffset     = 1024
	magic        = 0xEF53
	rootIno      = 2
	extentMagic  = 0xF30A
	flagExtents  = 0x80000
	flagInline   = 0x10000000
	flagEncrypt  = 0x800
	incompat64   = 0x80
	maxSymlinks  = 8
	maxDepth     = 5
	modeTypeMask = 0xF000
	modeDir      = 0x4000
	modeFile     = 0x8000
	modeLink     = 0xA000
)

// FS is an opened file system.
type FS struct {
	r          io.ReaderAt
	size       int64
	blockSize  int64
	inodeSize  int64
	inodesPer  uint32
	inodeCount uint32
	descSize   int64
	gdtBlock   int64
	groups     uint32
	Label      string
	UUID       [16]byte
}

// ErrNotExt4 means that the superblock has no ext4 magic.
var ErrNotExt4 = errors.New("no ext2/3/4 superblock")

// Open reads the superblock. size is the size of the device or image.
func Open(r io.ReaderAt, size int64) (*FS, error) {
	sb := make([]byte, 1024)
	if _, err := r.ReadAt(sb, sbOffset); err != nil {
		return nil, err
	}
	le := binary.LittleEndian
	if le.Uint16(sb[0x38:]) != magic {
		return nil, ErrNotExt4
	}
	fs := &FS{r: r, size: size}
	logBlock := le.Uint32(sb[0x18:])
	if logBlock > 6 {
		return nil, fmt.Errorf("block size 2^(10+%d) not supported", logBlock)
	}
	fs.blockSize = 1024 << logBlock
	fs.inodeCount = le.Uint32(sb[0x0:])
	blocks := int64(le.Uint32(sb[0x4:]))
	firstData := int64(le.Uint32(sb[0x14:]))
	blocksPer := int64(le.Uint32(sb[0x20:]))
	fs.inodesPer = le.Uint32(sb[0x28:])
	rev := le.Uint32(sb[0x4C:])
	fs.inodeSize = 128
	if rev >= 1 {
		fs.inodeSize = int64(le.Uint16(sb[0x58:]))
	}
	incompat := le.Uint32(sb[0x60:])
	fs.descSize = 32
	if incompat&incompat64 != 0 {
		blocks |= int64(le.Uint32(sb[0x150:])) << 32
		if ds := int64(le.Uint16(sb[0xFE:])); ds >= 64 {
			fs.descSize = ds
		}
	}
	copy(fs.UUID[:], sb[0x68:0x78])
	fs.Label = strings.TrimRight(string(sb[0x78:0x88]), "\x00")
	if blocksPer == 0 || fs.inodesPer == 0 || fs.inodeSize < 128 || fs.inodeSize > fs.blockSize {
		return nil, errors.New("superblock values out of range")
	}
	fs.groups = uint32((blocks - firstData + blocksPer - 1) / blocksPer)
	fs.gdtBlock = firstData + 1
	return fs, nil
}

func (fs *FS) readBlock(n int64) ([]byte, error) {
	if n <= 0 || (fs.size > 0 && (n+1)*fs.blockSize > fs.size) {
		return nil, fmt.Errorf("block %d out of range", n)
	}
	b := make([]byte, fs.blockSize)
	_, err := fs.r.ReadAt(b, n*fs.blockSize)
	return b, err
}

type inode struct {
	mode  uint16
	flags uint32
	size  int64
	block [60]byte
}

func (fs *FS) inode(ino uint32) (*inode, error) {
	if ino == 0 || ino > fs.inodeCount {
		return nil, fmt.Errorf("inode %d out of range", ino)
	}
	group := (ino - 1) / fs.inodesPer
	index := int64((ino - 1) % fs.inodesPer)
	if group >= fs.groups {
		return nil, fmt.Errorf("inode %d: group %d out of range", ino, group)
	}
	desc := make([]byte, fs.descSize)
	if _, err := fs.r.ReadAt(desc, fs.gdtBlock*fs.blockSize+int64(group)*fs.descSize); err != nil {
		return nil, err
	}
	le := binary.LittleEndian
	table := int64(le.Uint32(desc[0x8:]))
	if fs.descSize >= 64 {
		table |= int64(le.Uint32(desc[0x28:])) << 32
	}
	raw := make([]byte, fs.inodeSize)
	if _, err := fs.r.ReadAt(raw, table*fs.blockSize+index*fs.inodeSize); err != nil {
		return nil, err
	}
	in := &inode{mode: le.Uint16(raw[0:]), flags: le.Uint32(raw[0x20:])}
	in.size = int64(le.Uint32(raw[0x4:])) | int64(le.Uint32(raw[0x6C:]))<<32
	// i_size_high with bit 31 set gives a negative size, and a size past the
	// image is no file of it: a hostile image must not reach make() or a
	// slice with such a size (data, linkTarget)
	if in.size < 0 || (fs.size > 0 && in.size > fs.size) {
		return nil, fmt.Errorf("inode %d: size %d out of range (the image has %d bytes)", ino, in.size, fs.size)
	}
	copy(in.block[:], raw[0x28:0x28+60])
	return in, nil
}

// blocks lists the physical block of each logical block 0..n-1 (0 = a hole).
func (fs *FS) blocks(in *inode, n int64) ([]int64, error) {
	if in.flags&flagInline != 0 {
		return nil, errors.New("inline data is not supported")
	}
	if in.flags&flagEncrypt != 0 {
		return nil, errors.New("encrypted file")
	}
	out := make([]int64, n)
	if in.flags&flagExtents != 0 {
		return out, fs.extents(in.block[:], out, 0)
	}
	return out, fs.blockMap(in.block[:], out)
}

func (fs *FS) extents(node []byte, out []int64, depth int) error {
	le := binary.LittleEndian
	if len(node) < 12 || le.Uint16(node[0:]) != extentMagic {
		return errors.New("bad extent header")
	}
	if depth > maxDepth {
		return errors.New("extent tree too deep")
	}
	entries := int(le.Uint16(node[2:]))
	treeDepth := le.Uint16(node[6:])
	if 12+entries*12 > len(node) {
		return errors.New("extent entries past the node")
	}
	for i := 0; i < entries; i++ {
		e := node[12+i*12:]
		if treeDepth == 0 {
			first := int64(le.Uint32(e[0:]))
			length := int64(le.Uint16(e[4:]))
			uninit := false
			if length > 32768 {
				length -= 32768
				uninit = true
			}
			start := int64(le.Uint16(e[6:]))<<32 | int64(le.Uint32(e[8:]))
			for j := int64(0); j < length; j++ {
				if first+j < int64(len(out)) && !uninit {
					out[first+j] = start + j
				}
			}
			continue
		}
		child := int64(le.Uint16(e[8:]))<<32 | int64(le.Uint32(e[4:]))
		b, err := fs.readBlock(child)
		if err != nil {
			return err
		}
		if err := fs.extents(b, out, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (fs *FS) blockMap(iblock []byte, out []int64) error {
	le := binary.LittleEndian
	i := 0
	for ; i < 12 && i < len(out); i++ {
		out[i] = int64(le.Uint32(iblock[i*4:]))
	}
	per := fs.blockSize / 4
	var walk func(blk int64, level int) error
	walk = func(blk int64, level int) error {
		if blk == 0 {
			// a hole: skip what this block would map
			span := int64(1)
			for l := 0; l <= level; l++ {
				span *= per
			}
			i += int(span)
			return nil
		}
		b, err := fs.readBlock(blk)
		if err != nil {
			return err
		}
		for k := int64(0); k < per && i < len(out); k++ {
			v := int64(le.Uint32(b[k*4:]))
			if level == 0 {
				out[i] = v
				i++
				continue
			}
			if err := walk(v, level-1); err != nil {
				return err
			}
		}
		return nil
	}
	for level := 0; level < 3 && i < len(out); level++ {
		if err := walk(int64(le.Uint32(iblock[(12+level)*4:])), level); err != nil {
			return err
		}
	}
	return nil
}

// data reads the first n bytes of an inode.
func (fs *FS) data(in *inode, n int64) ([]byte, error) {
	if n < 0 {
		return nil, fmt.Errorf("a read of %d bytes", n)
	}
	nb := (n + fs.blockSize - 1) / fs.blockSize
	phys, err := fs.blocks(in, nb)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, nb*fs.blockSize)
	for _, p := range phys {
		if p == 0 {
			out = append(out, make([]byte, fs.blockSize)...)
			continue
		}
		b, err := fs.readBlock(p)
		if err != nil {
			return nil, err
		}
		out = append(out, b...)
	}
	return out[:n], nil
}

func (fs *FS) lookup(dir *inode, name string) (uint32, error) {
	if dir.mode&modeTypeMask != modeDir {
		return 0, errors.New("not a directory")
	}
	if dir.size > 64<<20 {
		return 0, errors.New("directory too large")
	}
	b, err := fs.data(dir, dir.size)
	if err != nil {
		return 0, err
	}
	le := binary.LittleEndian
	for off := int64(0); off+8 <= int64(len(b)); {
		ino := le.Uint32(b[off:])
		rec := int64(le.Uint16(b[off+4:]))
		nl := int64(b[off+6])
		if rec < 8 || off+rec > int64(len(b)) {
			// a broken entry: go on with the next block
			off = (off/fs.blockSize + 1) * fs.blockSize
			continue
		}
		if ino != 0 && off+8+nl <= int64(len(b)) && string(b[off+8:off+8+nl]) == name {
			return ino, nil
		}
		off += rec
	}
	return 0, fmt.Errorf("%s: not found", name)
}

// resolve walks a path from the root and follows symbolic links.
func (fs *FS) resolve(p string, links int) (*inode, error) {
	if links > maxSymlinks {
		return nil, errors.New("too many symbolic links")
	}
	cur, err := fs.inode(rootIno)
	if err != nil {
		return nil, err
	}
	dirPath := "/"
	parts := strings.Split(strings.Trim(path.Clean("/"+p), "/"), "/")
	for i, part := range parts {
		if part == "" {
			continue
		}
		ino, err := fs.lookup(cur, part)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path.Join(dirPath, part), err)
		}
		next, err := fs.inode(ino)
		if err != nil {
			return nil, err
		}
		if next.mode&modeTypeMask == modeLink {
			target, err := fs.linkTarget(next)
			if err != nil {
				return nil, err
			}
			if !strings.HasPrefix(target, "/") {
				target = path.Join(dirPath, target)
			}
			rest := strings.Join(parts[i+1:], "/")
			return fs.resolve(path.Join(target, rest), links+1)
		}
		cur = next
		dirPath = path.Join(dirPath, part)
	}
	return cur, nil
}

func (fs *FS) linkTarget(in *inode) (string, error) {
	if in.size < 0 {
		return "", errors.New("symbolic link of a negative size")
	}
	if in.size < 60 && in.flags&flagExtents == 0 {
		return string(in.block[:in.size]), nil
	}
	if in.size > 4096 {
		return "", errors.New("symbolic link too long")
	}
	b, err := fs.data(in, in.size)
	return string(b), err
}

// ReadFile reads a regular file. A file larger than max is an error.
func (fs *FS) ReadFile(p string, max int64) ([]byte, error) {
	in, err := fs.resolve(p, 0)
	if err != nil {
		return nil, err
	}
	if in.mode&modeTypeMask != modeFile {
		return nil, fmt.Errorf("%s: not a regular file", p)
	}
	if in.size > max {
		return nil, fmt.Errorf("%s: %d bytes, more than %d", p, in.size, max)
	}
	return fs.data(in, in.size)
}

// Props parses a build.prop style file: key=value lines, # comments.
func Props(b []byte) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			m[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return m
}
