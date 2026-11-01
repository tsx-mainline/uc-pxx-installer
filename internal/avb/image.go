package avb

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"hash"
)

// footerWindow is how far after the data (the hashed bytes, or the hash tree
// and FEC) the vbmeta of the AVB footer may start and end. avbtool puts it
// at the next 4 KiB boundary; in the 37 public files it is at most 4096
// bytes after the data and 1024 bytes long.
const footerWindow = 64 << 10

// ImageCheck checks one covered partition image while it streams past
// (Write), with nothing stored but the hashes of level 0 of a hash tree
// (system: 14.4 MB) and a little of the tail. It is set up from the
// descriptor that the manifest lists, so the order of the images in the
// firmware does not matter (dtbo comes before vbmeta). Done compares that
// descriptor with the one of the signed vbmeta and the computed digests with
// the signed ones.
type ImageCheck struct {
	part string
	d    Descriptor
	size int64
	pos  int64
	err  error

	salt []byte
	h    hash.Hash

	// hashtree
	dsize, pad     int
	sizes, offsets []int64 // per level, level 0 first; offsets in the stored tree (top level first)
	level0         []byte  // computed, padded to sizes[0]
	blk            []byte  // the data block being filled
	nblk           int64
	upper          []byte // the stored levels above level 0
	firstBad       int64  // first data block whose stored hash differs, -1 none

	dataEnd int64
	win     []byte // the bytes [dataEnd, dataEnd+len(win))
	foot    []byte // the last FooterSize bytes
}

// treeLevels: the size of each level (level 0 first) and its offset in the
// stored tree, where the top level comes first (the avbtool layout).
func treeLevels(imageSize int64, pad int) (sizes, offsets []int64) {
	for size := imageSize; size > Block; {
		blocks := (size + Block - 1) / Block
		size = (blocks*int64(pad) + Block - 1) / Block * Block
		sizes = append(sizes, size)
	}
	offsets = make([]int64, len(sizes))
	for i := range sizes {
		for _, s := range sizes[i+1:] {
			offsets[i] += s
		}
	}
	return sizes, offsets
}

// NewImageCheck sets up the check of the image of part (size bytes) for the
// descriptor d. The layout is checked here, before any byte streams.
func NewImageCheck(part string, d Descriptor, size int64) (*ImageCheck, error) {
	c := &ImageCheck{part: part, d: d, size: size, firstBad: -1}
	w := part + ": " + d.Type + " descriptor"
	if d.Partition != part || Covered[part] != d.Type {
		return nil, failf("%s: the manifest lists a %s descriptor for %s", part, d.Type, d.Partition)
	}
	alg := d.HashAlgorithm
	if newHash(alg) == nil {
		return nil, failf("%s: hash algorithm %q is not supported", w, alg)
	}
	salt, err := hex.DecodeString(d.Salt)
	if err != nil {
		return nil, failf("%s: salt is not hex", w)
	}
	c.salt = salt
	c.h = newHash(alg)
	if size < FooterSize || d.ImageSize > uint64(size) {
		return nil, failf("%s: image size %d is larger than the image (%d)", w, d.ImageSize, size)
	}
	switch d.Type {
	case "hash":
		c.h.Write(salt)
		c.dataEnd = int64(d.ImageSize)
	case "hashtree":
		if d.DataBlockSize != Block || d.HashBlockSize != Block {
			return nil, failf("%s: block sizes %d/%d, not %d", w, d.DataBlockSize, d.HashBlockSize, Block)
		}
		is := int64(d.ImageSize)
		if is <= Block || is%Block != 0 {
			return nil, failf("%s: image size %d is not a multiple of %d above one block", w, is, Block)
		}
		if d.TreeOffset != d.ImageSize {
			return nil, failf("%s: tree offset %d is not right after the data (%d)", w, d.TreeOffset, d.ImageSize)
		}
		c.dsize = hashSizes[alg]
		c.pad = 1
		for c.pad < c.dsize {
			c.pad <<= 1
		}
		c.sizes, c.offsets = treeLevels(is, c.pad)
		var sum int64
		for _, s := range c.sizes {
			sum += s
		}
		if uint64(sum) != d.TreeSize {
			return nil, failf("%s: tree size %d, the image size gives %d", w, d.TreeSize, sum)
		}
		end := int64(d.TreeOffset + d.TreeSize)
		if r := int64(d.FECNumRoots); r != 0 {
			rounds := (end/Block + (255 - r) - 1) / (255 - r)
			if r < 2 || r > 24 || int64(d.FECOffset) != end || int64(d.FECSize) != rounds*r*Block {
				return nil, failf("%s: FEC (%d roots, %d bytes at %d) is not where and how large it must be",
					w, r, d.FECSize, d.FECOffset)
			}
			end += int64(d.FECSize)
		} else if d.FECOffset != 0 || d.FECSize != 0 {
			return nil, failf("%s: FEC offset or size without FEC roots", w)
		}
		if end > size-FooterSize {
			return nil, failf("%s: tree and FEC end at %d, after the image (%d) and its footer", w, end, size)
		}
		c.level0 = make([]byte, c.sizes[0])
		c.upper = make([]byte, c.offsets[0])
		c.blk = make([]byte, 0, Block)
		c.dataEnd = end
	default:
		return nil, failf("%s: not a hash or hashtree descriptor", w)
	}
	return c, nil
}

// span returns the part of p (at stream position at) inside [lo, hi), and
// its offset from lo.
func span(p []byte, at, lo, hi int64) ([]byte, int64) {
	a, b := max(at, lo), min(at+int64(len(p)), hi)
	if a >= b {
		return nil, 0
	}
	return p[a-at : b-at], a - lo
}

func (c *ImageCheck) block(b []byte) {
	c.h.Reset()
	c.h.Write(c.salt)
	c.h.Write(b)
	copy(c.level0[c.nblk*int64(c.pad):], c.h.Sum(nil))
	c.nblk++
}

// Write takes the next bytes of the image. It never fails; Done reports.
func (c *ImageCheck) Write(p []byte) (int, error) {
	n := len(p)
	at := c.pos
	c.pos += int64(n)
	if c.err != nil {
		return n, nil
	}
	if c.pos > c.size {
		c.err = failf("%s: image is longer than %d bytes", c.part, c.size)
		return n, nil
	}
	is := int64(c.d.ImageSize)
	if b, _ := span(p, at, 0, is); b != nil {
		if c.d.Type == "hash" {
			c.h.Write(b)
		} else {
			for len(b) > 0 {
				if len(c.blk) == 0 && len(b) >= Block {
					c.block(b[:Block])
					b = b[Block:]
					continue
				}
				k := min(Block-len(c.blk), len(b))
				c.blk = append(c.blk, b[:k]...)
				b = b[k:]
				if len(c.blk) == Block {
					c.block(c.blk)
					c.blk = c.blk[:0]
				}
			}
		}
	}
	if c.d.Type == "hashtree" {
		to := int64(c.d.TreeOffset)
		if b, off := span(p, at, to, to+c.offsets[0]); b != nil {
			copy(c.upper[off:], b)
		}
		lo := to + c.offsets[0]
		if b, off := span(p, at, lo, lo+c.sizes[0]); b != nil && c.firstBad < 0 {
			if !bytes.Equal(b, c.level0[off:off+int64(len(b))]) {
				for i := range b {
					if b[i] != c.level0[off+int64(i)] {
						c.firstBad = (off + int64(i)) / int64(c.pad)
						break
					}
				}
			}
		}
	}
	if b, off := span(p, at, c.dataEnd, min(c.dataEnd+footerWindow, c.size)); b != nil {
		if int64(len(c.win)) != off {
			c.win = append(c.win, make([]byte, off-int64(len(c.win)))...)
		}
		c.win = append(c.win, b...)
	}
	if b, _ := span(p, at, c.size-FooterSize, c.size); b != nil {
		c.foot = append(c.foot, b...)
	}
	return n, nil
}

// Done ends the check. signed is the vbmeta that Verify accepted.
func (c *ImageCheck) Done(signed *Vbmeta) error {
	w := c.part + ": " + c.d.Type + " descriptor"
	if c.err != nil {
		return c.err
	}
	if c.pos != c.size {
		return failf("%s: image is %d bytes, %d expected", c.part, c.pos, c.size)
	}
	sd, ok := signed.Find(c.part)
	if !ok || sd != c.d {
		return failf("%s: the signed vbmeta's descriptor differs from the manifest's", w)
	}
	switch c.d.Type {
	case "hash":
		if hex.EncodeToString(c.h.Sum(nil)) != c.d.Digest {
			return failf("%s: digest of the image does not match the vbmeta", w)
		}
	case "hashtree":
		if c.nblk*int64(c.pad) > c.sizes[0] || len(c.blk) != 0 {
			return failf("%s: the data does not fill whole blocks", w)
		}
		if c.firstBad >= 0 {
			if c.firstBad < c.nblk {
				return failf("%s: data block %d does not match the hash tree in the image", w, c.firstBad)
			}
			return failf("%s: level 0 of the stored tree has data after the last hash", w)
		}
		zpad := make([]byte, c.pad-c.dsize)
		prev := c.level0
		for lv := 1; lv < len(c.sizes); lv++ {
			out := make([]byte, 0, c.sizes[lv])
			for i := 0; i < len(prev); i += Block {
				c.h.Reset()
				c.h.Write(c.salt)
				c.h.Write(prev[i : i+Block])
				out = append(c.h.Sum(out), zpad...)
			}
			out = append(out, make([]byte, c.sizes[lv]-int64(len(out)))...)
			if !bytes.Equal(c.upper[c.offsets[lv]:c.offsets[lv]+c.sizes[lv]], out) {
				return failf("%s: level %d of the stored tree does not match", w, lv)
			}
			prev = out
		}
		c.h.Reset()
		c.h.Write(c.salt)
		c.h.Write(prev)
		if hex.EncodeToString(c.h.Sum(nil)) != c.d.RootDigest {
			return failf("%s: root digest does not match the vbmeta", w)
		}
	}
	return c.footer(signed)
}

// footer checks the AVB footer at the end of the image and its unsigned
// vbmeta: every descriptor in it must be one of the signed vbmeta.
func (c *ImageCheck) footer(signed *Vbmeta) error {
	w := c.part + ": AVB footer"
	f := c.foot
	if len(f) != FooterSize || string(f[:4]) != "AVBf" {
		return failf("%s: no AVBf magic", w)
	}
	be := binary.BigEndian
	major, minor := be.Uint32(f[4:]), be.Uint32(f[8:])
	orig, voff, vsize := be.Uint64(f[12:]), be.Uint64(f[20:]), be.Uint64(f[28:])
	if major != 1 || !allZero(f[36:]) {
		return failf("%s: version %d.%d or reserved bytes not zero", w, major, minor)
	}
	if orig != c.d.ImageSize {
		return failf("%s: original image size %d, the descriptor says %d", w, orig, c.d.ImageSize)
	}
	if voff < uint64(c.dataEnd) || vsize > uint64(c.size) || voff > uint64(c.size-FooterSize)-vsize {
		return failf("%s: its vbmeta (%d bytes at %d) overlaps the data or the footer", w, vsize, voff)
	}
	rel := voff - uint64(c.dataEnd)
	if rel+vsize > uint64(len(c.win)) {
		return failf("%s: its vbmeta (%d bytes at %d) is not within %d bytes after the data", w, vsize, voff, footerWindow)
	}
	blob := c.win[rel : rel+vsize]
	v, err := Parse(blob, w+" vbmeta")
	if err != nil {
		return err
	}
	if !allZero(blob[v.Total:]) {
		return failf("%s: bytes after its vbmeta are not zero", w)
	}
	if v.Algorithm != 0 || v.RollbackIndex != 0 || v.Flags != 0 || v.RollbackIndexLocation != 0 {
		return failf("%s: its vbmeta is not the plain unsigned one (algorithm 0, rollback 0, flags 0)", w)
	}
	for _, r := range v.Raw {
		found := false
		for _, s := range signed.Raw {
			found = found || bytes.Equal(r, s)
		}
		if !found {
			return failf("%s: its vbmeta has a descriptor that the signed vbmeta does not have", w)
		}
	}
	return nil
}
