package flowio

// The drive form of a compressed bundle (mainline-rc-10, owner R21/R22: the
// drive fallback takes the same schema-3 bundle as the USB cable). The
// folder uc-pxx-mainline of the ejected drive holds manifest.json,
// boot-template.img and rootfs.ext4.zst. The read of the drive (readMainline)
// unpacks rootfs.ext4.zst once into a RAM folder of the run folder,
// DriveBundleDir: the manifest's bytes, the template and the unpacked
// rootfs.ext4, each checked against the manifest (size and sha256 of the
// compressed file, size and sha256 of the unpacked one). The plan and
// install children then take that folder (--mainline DIR, as for the sealed
// USB bundle: they bind its three files by their identity and check them
// again, pass 1). The compressed bytes stay on the drive; the RAM holds one
// unpacked copy. Nothing here touches the eMMC: the only writes are the
// files of DriveBundleDir (unpackDriveBundle, removeDriveBundle).

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"uc-pxx-installer/internal/fatfs"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/mainline/mldrive"
	"uc-pxx-installer/internal/zstd"
)

// DriveBundleDir is the RAM folder of an unpacked drive bundle.
const DriveBundleDir = RunDir + "/drive-bundle"

// UnpackReserve is the RAM that stays free after the unpack (the receiver's
// default, xfer.DefaultReserve).
const UnpackReserve = 256 << 20

// zstForm reads the names of the drive's folder: zst is true when it holds
// rootfs.ext4.zst. Then it must hold exactly manifest.json, boot-template.img
// and rootfs.ext4.zst (names without case, the entries a host adds by itself
// left out), and the manifest must be of schema 3. names maps each contract
// name to the name on the drive.
func zstForm(src mainline.Source, m *mainline.Manifest) (names map[string]string, zst bool, err error) {
	files, others, err := src.Names()
	if err != nil {
		return nil, false, err
	}
	all := []string{mainline.ManifestFile, mainline.TemplateFile, mainline.RootfsFile, mainline.RootfsZstFile}
	names = map[string]string{}
	var extra []string
	for _, n := range others {
		if !mainline.Ignored(n) {
			extra = append(extra, strconv.Quote(n))
		}
	}
	for _, n := range files {
		if mainline.Ignored(n) {
			continue
		}
		match := ""
		for _, w := range all {
			if strings.EqualFold(n, w) {
				match = w
			}
		}
		switch {
		case match == "":
			extra = append(extra, strconv.Quote(n))
		case names[match] != "":
			return nil, false, fmt.Errorf("the mainline folder holds %q and %q: only one of them may be there", names[match], n)
		default:
			names[match] = n
		}
	}
	if names[mainline.RootfsZstFile] == "" {
		return nil, false, nil // the uncompressed form: the plan child reads the drive in place
	}
	want := mainline.ManifestFile + ", " + mainline.TemplateFile + ", " + mainline.RootfsZstFile
	switch {
	case names[mainline.RootfsFile] != "":
		return nil, true, fmt.Errorf("the mainline folder holds %s and %s: copy only the folder of the release again", mainline.RootfsFile, mainline.RootfsZstFile)
	case len(extra) > 0:
		return nil, true, fmt.Errorf("the mainline folder holds other entries (%s): it must hold only %s", strings.Join(extra, ", "), want)
	case names[mainline.TemplateFile] == "":
		return nil, true, fmt.Errorf("the mainline folder has no %s", mainline.TemplateFile)
	case m.Schema != mainline.SchemaZst || m.RootfsZst == nil:
		return nil, true, fmt.Errorf("the mainline folder holds %s, but its manifest is of schema %d (only schema %d names it)", mainline.RootfsZstFile, m.Schema, mainline.SchemaZst)
	}
	return names, true, nil
}

// memAvailable is MemAvailable of the unit's meminfo in bytes (0: unknown).
func (r *Runner) memAvailable() int64 {
	f, err := os.Open(filepath.Join(r.c.Proc, "meminfo"))
	if err != nil {
		return 0
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		if v, ok := strings.CutPrefix(s.Text(), "MemAvailable:"); ok {
			kb, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
			if err != nil {
				return 0
			}
			return kb << 10
		}
	}
	return 0
}

// unpackDrive makes DriveBundleDir from the drive's compressed folder (the
// names of zstForm, the manifest's bytes b). It reports the folder or why
// not (the folder is then removed).
func (r *Runner) unpackDrive(image string, m *mainline.Manifest, b []byte, names map[string]string) (string, error) {
	dir := r.path(DriveBundleDir)
	need := m.BootTemplate.Size + m.Rootfs.Size + int64(len(b))
	if avail := r.memAvailable(); avail-UnpackReserve < need {
		return "", fmt.Errorf("the unit has %d MiB of RAM free, the unpacked bundle needs %d MiB and %d MiB stay free: it cannot be unpacked",
			avail>>20, need>>20, int64(UnpackReserve)>>20)
	}
	fs, err := fatfs.OpenImage(image)
	if err != nil {
		return "", err
	}
	defer fs.Close()
	src, err := mldrive.DriveSource(fs)
	if err != nil {
		return "", err
	}
	open := func(contract string) (io.Reader, int64, func(), error) {
		f, c, err := src.Open(names[contract])
		if err != nil {
			return nil, 0, nil, fmt.Errorf("%s: %v", contract, err)
		}
		return io.NewSectionReader(f, 0, f.Size()), f.Size(), func() { c.Close() }, nil
	}
	tr, tn, tc, err := open(mainline.TemplateFile)
	if err != nil {
		return "", err
	}
	defer tc()
	zr, zn, zc, err := open(mainline.RootfsZstFile)
	if err != nil {
		return "", err
	}
	defer zc()
	if tn != m.BootTemplate.Size {
		return "", fmt.Errorf("%s: %d bytes, the manifest says %d", mainline.TemplateFile, tn, m.BootTemplate.Size)
	}
	if zn != m.RootfsZst.Size {
		return "", fmt.Errorf("%s: %d bytes, the manifest says %d", mainline.RootfsZstFile, zn, m.RootfsZst.Size)
	}
	if err := unpackDriveBundle(r.path(RunDir), dir, b, tr, zr, m); err != nil {
		return "", err
	}
	return dir, nil
}

// errMore: a reader gave more bytes than the manifest's size.
var errMore = errors.New("more bytes than the manifest says")

// countReader hashes and counts what is read through it.
type countReader struct {
	r io.Reader
	h hash.Hash
	n int64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.h.Write(p[:n])
	c.n += int64(n)
	return n, err
}

// unpackDriveBundle writes dir (inside the run folder base): manifest.json
// (the bytes b), boot-template.img (tmpl) and rootfs.ext4 (zst unpacked),
// each file new (0400) in a new folder (0700), each checked against the
// manifest m: the template's size and sha256, the compressed stream's size
// and sha256 (every byte of it read) and the unpacked size and sha256. An
// earlier folder is removed first; on any error the folder is removed.
func unpackDriveBundle(base, dir string, b []byte, tmpl, zst io.Reader, m *mainline.Manifest) (err error) {
	if !inside(base, dir) || filepath.Clean(dir) == filepath.Clean(base) || underKernelFS(dir) ||
		!strings.Contains(filepath.Clean(base), "/run/") && !strings.HasSuffix(filepath.Clean(base), "/run") {
		return fmt.Errorf("%s is not a folder of the run folder %s", dir, base)
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(dir)
		}
	}()
	create := func(name string) (*os.File, error) {
		return os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o400)
	}
	write := func(name string, fill func(f *os.File) error) error {
		f, err := create(name)
		if err != nil {
			return err
		}
		werr := fill(f)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		return werr
	}
	hexOf := func(h hash.Hash) string { return hex.EncodeToString(h.Sum(nil)) }
	// copyTo copies src to f and hashes it with h: at most limit bytes
	// (errMore for more)
	copyTo := func(f *os.File, src io.Reader, limit int64, h hash.Hash) (int64, error) {
		buf := make([]byte, 1<<20)
		var n int64
		for {
			k, rerr := src.Read(buf)
			if k > 0 {
				if n+int64(k) > limit {
					return n, errMore
				}
				if _, err := f.Write(buf[:k]); err != nil {
					return n, err
				}
				h.Write(buf[:k])
				n += int64(k)
			}
			if rerr == io.EOF {
				return n, nil
			}
			if rerr != nil {
				return n, rerr
			}
		}
	}
	if err := write(mainline.ManifestFile, func(f *os.File) error { _, err := f.Write(b); return err }); err != nil {
		return err
	}
	if err := write(mainline.TemplateFile, func(f *os.File) error {
		h := sha256.New()
		n, err := copyTo(f, tmpl, m.BootTemplate.Size, h)
		if err != nil {
			return fmt.Errorf("%s: %v", mainline.TemplateFile, err)
		}
		if n != m.BootTemplate.Size || hexOf(h) != m.BootTemplate.SHA256 {
			return fmt.Errorf("%s: %d bytes, sha256 %s; the manifest says %d bytes, sha256 %s: copy the folder again",
				mainline.TemplateFile, n, hexOf(h), m.BootTemplate.Size, m.BootTemplate.SHA256)
		}
		return nil
	}); err != nil {
		return err
	}
	return write(mainline.RootfsFile, func(f *os.File) error {
		in := &countReader{r: io.LimitReader(zst, m.RootfsZst.Size+1), h: sha256.New()}
		h := sha256.New()
		n, uerr := copyTo(f, zstd.NewReader(in), m.Rootfs.Size, h)
		io.Copy(io.Discard, in) // every compressed byte is hashed, also after a stop
		switch {
		case in.n != m.RootfsZst.Size || hexOf(in.h) != m.RootfsZst.SHA256:
			return fmt.Errorf("%s: %d bytes, sha256 %s; the manifest says %d bytes, sha256 %s: copy the folder again",
				mainline.RootfsZstFile, in.n, hexOf(in.h), m.RootfsZst.Size, m.RootfsZst.SHA256)
		case uerr != nil:
			return fmt.Errorf("the unpack of %s stopped: %v", mainline.RootfsZstFile, uerr)
		case n != m.Rootfs.Size || hexOf(h) != m.Rootfs.SHA256:
			return fmt.Errorf("%s unpacks to %d bytes, sha256 %s; the manifest says %d bytes, sha256 %s",
				mainline.RootfsZstFile, n, hexOf(h), m.Rootfs.Size, m.Rootfs.SHA256)
		}
		return nil
	})
}

// removeDriveBundle removes DriveBundleDir (a new drive read, or a read that
// did not end well).
func removeDriveBundle(base, dir string) error {
	if !inside(base, dir) || filepath.Clean(dir) == filepath.Clean(base) || underKernelFS(dir) {
		return fmt.Errorf("%s is not a folder of the run folder %s", dir, base)
	}
	return os.RemoveAll(dir)
}
