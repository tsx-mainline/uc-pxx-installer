package mainline

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// File is one opened file of a bundle: it reads in place.
type File interface {
	io.ReaderAt
	Size() int64
}

// Source is where a bundle is: the folder uc-pxx-mainline/ of the
// UC-PXX-INST drive (DriveSource), or a folder on a file system (DirSource,
// the dry run).
type Source interface {
	// Names lists the folder: the names of its entries; dirs are the
	// entries that are not regular files.
	Names() (files, others []string, err error)
	// Open opens one regular file of the folder by its exact name.
	Open(name string) (File, io.Closer, error)
	// Describe says where the bundle is, for the plan.
	Describe() string
}

// ignored are the entries a host may add to a folder by itself: the
// AppleDouble files of macOS ("._NAME") and .DS_Store. Every other extra
// entry refuses the bundle.
func ignored(name string) bool { return strings.HasPrefix(name, "._") || name == ".DS_Store" }

// Payload is a mainline bundle that pass 1 checked: the manifest parsed
// strictly, the template and the root file system of the manifest's size
// and sha256, the template a kernel/mkimage.sh image with the manifest's
// token, and the root file system an ext4 with the manifest's label.
type Payload struct {
	Kind           string       `json:"kind"` // Kind ("uc-pxx-mainline")
	Source         string       `json:"source"`
	ManifestSHA256 string       `json:"manifest_sha256"`
	Manifest       Manifest     `json:"manifest"`
	Template       TemplateInfo `json:"template"`
	Pass1          string       `json:"pass1"`

	tmpl     []byte
	rootfs   File
	closers  []io.Closer
	reopened bool // Reopen: the root file system was not hashed by this process
}

// TemplateBytes is a copy of the checked boot template.
func (p *Payload) TemplateBytes() []byte { return append([]byte(nil), p.tmpl...) }

// RootfsReader reads the root file system image in place.
func (p *Payload) RootfsReader() io.ReaderAt { return p.rootfs }

// Verified reports a payload that came from Verify (not one decoded from a
// plan file).
func (p *Payload) Verified() bool { return p != nil && p.tmpl != nil && p.rootfs != nil }

// Close closes the files of the bundle.
func (p *Payload) Close() error {
	var first error
	for _, c := range p.closers {
		if err := c.Close(); err != nil && first == nil {
			first = err
		}
	}
	p.closers = nil
	return first
}

// Verify is pass 1 of a mainline bundle. The folder must hold exactly
// manifest.json, boot-template.img and rootfs.ext4 (the names compared
// without case, as FAT does; AppleDouble files and .DS_Store are ignored),
// and nothing else. Each image must have the manifest's size and sha256 (the
// sha256 is computed while the file is read, in place). The template must be
// a kernel/mkimage.sh image without a resource, with the manifest's token;
// the root file system an ext2/3/4 image with the manifest's label.
func Verify(src Source) (*Payload, error) { return verify(src, nil) }

// Reopen opens the bundle of a plan again for the install run of that plan,
// without a second sha256 of rootfs.ext4 (the one large file). Everything
// else is pass 1 again: the folder holds exactly the three files, the
// manifest is read and must be the plan's (its sha256), the template is read,
// hashed and parsed again and must be the plan's, the root file system has
// the manifest's size and label, and the source is the plan's. The pass 1
// text is the plan's (that pass 1 ran in the plan run).
//
// It is safe because the executor never trusts the root file system bytes:
// install.RunMainline hashes rootfs.ext4 while it writes system_a and stops
// on a sha256 or length other than the plan's (the manifest's), and the
// verify step reads system_a back from the eMMC and compares it with the
// plan's sha256, both before slot A is armed. A changed file so stops the
// install with slot A disabled: the keeper boots. The caller binds the files
// to the plan by their identity (the drive rule, or the folder's identity).
func Reopen(src Source, want *Payload) (*Payload, error) {
	if want == nil || want.Kind != Kind || !IsSHA256(want.ManifestSHA256) || want.Pass1 == "" {
		return nil, errors.New("reopen: the plan has no mainline payload")
	}
	return verify(src, want)
}

// RootfsHashed reports a payload whose root file system was hashed by this
// process (Verify); false for Reopen.
func (p *Payload) RootfsHashed() bool { return p != nil && p.Verified() && !p.reopened }

func verify(src Source, plan *Payload) (*Payload, error) {
	files, others, err := src.Names()
	if err != nil {
		return nil, err
	}
	want := []string{ManifestFile, TemplateFile, RootfsFile}
	got := map[string]string{} // the contract name -> the name on the drive
	var extra []string
	for _, n := range others {
		if !ignored(n) {
			extra = append(extra, n)
		}
	}
	for _, n := range files {
		if ignored(n) {
			continue
		}
		match := ""
		for _, w := range want {
			if strings.EqualFold(n, w) {
				match = w
			}
		}
		switch {
		case match == "":
			extra = append(extra, n)
		case got[match] != "":
			return nil, fmt.Errorf("the mainline folder holds %q and %q: only one of them may be there", got[match], n)
		default:
			got[match] = n
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		for i := range extra {
			extra[i] = strconv.Quote(extra[i])
		}
		return nil, fmt.Errorf("the mainline folder holds other entries (%s): it must hold only %s", strings.Join(extra, ", "), strings.Join(want, ", "))
	}
	for _, w := range want {
		if got[w] == "" {
			return nil, fmt.Errorf("the mainline folder has no %s", w)
		}
	}
	p := &Payload{Kind: Kind, Source: src.Describe()}
	if plan != nil && p.Source != plan.Source {
		return nil, fmt.Errorf("the mainline bundle is %s now, the plan's is %s", p.Source, plan.Source)
	}
	ok := false
	defer func() {
		if !ok {
			p.Close()
		}
	}()
	open := func(w string) (File, error) {
		f, c, err := src.Open(got[w])
		if err != nil {
			return nil, fmt.Errorf("%s: %v", w, err)
		}
		p.closers = append(p.closers, c)
		return f, nil
	}

	mf, err := open(ManifestFile)
	if err != nil {
		return nil, err
	}
	if mf.Size() <= 0 || mf.Size() > MaxManifest {
		return nil, fmt.Errorf("%s: %d bytes (1 to %d)", ManifestFile, mf.Size(), MaxManifest)
	}
	mb := make([]byte, mf.Size())
	if _, err := io.ReadFull(io.NewSectionReader(mf, 0, mf.Size()), mb); err != nil {
		return nil, fmt.Errorf("%s: %v", ManifestFile, err)
	}
	m, err := ParseManifest(mb)
	if err != nil {
		return nil, err
	}
	p.Manifest, p.ManifestSHA256 = *m, sha256Hex(mb)
	if plan != nil && (p.ManifestSHA256 != plan.ManifestSHA256 || p.Manifest != plan.Manifest) {
		return nil, fmt.Errorf("%s: sha256 %s, the plan's is %s", ManifestFile, p.ManifestSHA256, plan.ManifestSHA256)
	}

	tf, err := open(TemplateFile)
	if err != nil {
		return nil, err
	}
	if tf.Size() != m.BootTemplate.Size {
		return nil, fmt.Errorf("%s: %d bytes, the manifest says %d", TemplateFile, tf.Size(), m.BootTemplate.Size)
	}
	tb := make([]byte, tf.Size())
	if _, err := io.ReadFull(io.NewSectionReader(tf, 0, tf.Size()), tb); err != nil {
		return nil, fmt.Errorf("%s: %v", TemplateFile, err)
	}
	if got := sha256Hex(tb); got != m.BootTemplate.SHA256 {
		return nil, fmt.Errorf("%s: sha256 %s, the manifest says %s", TemplateFile, got, m.BootTemplate.SHA256)
	}
	ti, err := ParseTemplate(tb)
	if err != nil {
		return nil, err
	}
	if ti.Token != m.Token {
		return nil, fmt.Errorf("%s: token %q, the manifest says %q", TemplateFile, ti.Token, m.Token)
	}
	p.tmpl, p.Template = tb, *ti
	if plan != nil && p.Template != plan.Template {
		return nil, fmt.Errorf("%s: %+v, the plan's is %+v", TemplateFile, p.Template, plan.Template)
	}

	rf, err := open(RootfsFile)
	if err != nil {
		return nil, err
	}
	if rf.Size() != m.Rootfs.Size {
		return nil, fmt.Errorf("%s: %d bytes, the manifest says %d", RootfsFile, rf.Size(), m.Rootfs.Size)
	}
	if plan == nil {
		h := sha256.New()
		if _, err := io.CopyBuffer(h, io.NewSectionReader(rf, 0, rf.Size()), make([]byte, 1<<20)); err != nil {
			return nil, fmt.Errorf("%s: %v", RootfsFile, err)
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != m.Rootfs.SHA256 {
			return nil, fmt.Errorf("%s: sha256 %s, the manifest says %s", RootfsFile, got, m.Rootfs.SHA256)
		}
	}
	label, fsBytes, err := ext4Facts(rf)
	if err == nil && fsBytes != rf.Size() {
		err = fmt.Errorf("the superblock gives a file system of %d bytes (blocks count x block size), the file has %d", fsBytes, rf.Size())
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %v", RootfsFile, err)
	}
	if label != m.Rootfs.Label {
		return nil, fmt.Errorf("%s: the file system label is %q, the manifest says %q", RootfsFile, label, m.Rootfs.Label)
	}
	p.rootfs = rf
	if plan != nil {
		// the pass 1 of the plan run; this run hashes the root file system
		// while it writes it (RunMainline)
		p.Pass1, p.reopened = plan.Pass1, true
		ok = true
		return p, nil
	}
	p.Pass1 = fmt.Sprintf("manifest %s; %s %d bytes and %s %d bytes match the manifest (sha256); template token %s",
		ManifestFile, TemplateFile, tf.Size(), RootfsFile, rf.Size(), ti.Token)
	ok = true
	return p, nil
}

// ext4Facts reads the volume name and the size of an ext2/3/4 superblock (at
// byte 1024: the magic 0xEF53 at 0x38, the name at 0x78..0x87, the blocks
// count at 0x04 (and 0x150 with the 64-bit feature, 0x80 of the incompatible
// features at 0x60), the block size 1024 << s_log_block_size at 0x18). The
// size is blocks count x block size: the image must be exactly that long.
func ext4Facts(r io.ReaderAt) (string, int64, error) {
	sb := make([]byte, 1024)
	if _, err := r.ReadAt(sb, 1024); err != nil {
		return "", 0, fmt.Errorf("read the superblock: %v", err)
	}
	le := binary.LittleEndian
	if le.Uint16(sb[0x38:]) != 0xEF53 {
		return "", 0, fmt.Errorf("no ext2/3/4 superblock")
	}
	name := sb[0x78:0x88]
	n := 0
	for n < len(name) && name[n] != 0 {
		n++
	}
	if !allZero(name[n:]) {
		return "", 0, fmt.Errorf("the volume name is not NUL-terminated")
	}
	logBlock := le.Uint32(sb[0x18:])
	if logBlock > 6 {
		return "", 0, fmt.Errorf("block size 2^(10+%d)", logBlock)
	}
	blocks := uint64(le.Uint32(sb[0x04:]))
	if le.Uint32(sb[0x60:])&0x80 != 0 {
		blocks |= uint64(le.Uint32(sb[0x150:])) << 32
	}
	if blocks == 0 || blocks > MaxRootfs/1024 {
		return "", 0, fmt.Errorf("the superblock gives %d blocks", blocks)
	}
	return string(name[:n]), int64(blocks) << (10 + logBlock), nil
}

// --- a folder on a file system (dry run) ---------------------------------

type dirSource struct{ dir string }

// DirSource is a bundle folder on a file system (the dry run). Its entries
// must be regular files: a symlink, a device or a folder is refused, and a
// path under /dev, /proc or /sys is refused.
func DirSource(dir string) (Source, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	for _, p := range []string{"/dev/", "/proc/", "/sys/"} {
		if strings.HasPrefix(abs+"/", p) {
			return nil, fmt.Errorf("%s: a mainline bundle is never read under %s", dir, p)
		}
	}
	st, err := os.Lstat(abs)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", dir)
	}
	return &dirSource{dir: abs}, nil
}

func (d *dirSource) Describe() string { return "folder " + d.dir }

func (d *dirSource) Names() (files, others []string, err error) {
	es, err := os.ReadDir(d.dir)
	if err != nil {
		return nil, nil, err
	}
	for _, e := range es {
		if e.Type().IsRegular() {
			files = append(files, e.Name())
		} else {
			others = append(others, e.Name())
		}
	}
	return files, others, nil
}

type osFile struct {
	*os.File
	size int64
}

func (f osFile) Size() int64 { return f.size }

func (d *dirSource) Open(name string) (File, io.Closer, error) {
	if name == "" || strings.ContainsRune(name, '/') || name == "." || name == ".." {
		return nil, nil, fmt.Errorf("bad name %q", name)
	}
	p := filepath.Join(d.dir, name)
	lst, err := os.Lstat(p)
	if err != nil {
		return nil, nil, err
	}
	if !lst.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("%s is not a regular file", p)
	}
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || !os.SameFile(st, lst) {
		f.Close()
		return nil, nil, fmt.Errorf("%s changed while it was opened", p)
	}
	return osFile{File: f, size: st.Size()}, f, nil
}
