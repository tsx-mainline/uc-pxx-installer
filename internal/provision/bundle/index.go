package bundle

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// MaxIndex caps the unpacked APKINDEX text that InstalledSize reads.
const MaxIndex = 64 << 20

// InstalledSize is the installed size (the I: fields) of the bundle's
// packages, read from the bundle's own four indexes (open gives the reader
// of a member path). Each package of bundle.json must be in the indexes with
// an installed size; the lva.tar member counts with its own size. It reads
// only: the signatures of the indexes are apk's to check (apk add).
func InstalledSize(d *Doc, open func(path string) (io.ReadCloser, error)) (int64, error) {
	want := map[string]bool{}
	for _, p := range d.Packages {
		want[p.Name+"\x00"+p.Version] = true
	}
	got := map[string]int64{}
	for _, x := range d.Indexes {
		r, err := open(x.Path)
		if err != nil {
			return 0, err
		}
		err = readIndex(r, want, got)
		r.Close()
		if err != nil {
			return 0, fmt.Errorf("%s: %v", x.Path, err)
		}
	}
	var n int64
	for _, p := range d.Packages {
		v, ok := got[p.Name+"\x00"+p.Version]
		if !ok {
			return 0, fmt.Errorf("%s-%s is in none of the bundle's indexes", p.Name, p.Version)
		}
		n += v
	}
	if d.LVA != nil {
		n += d.LVA.Size
	}
	return n, nil
}

// readIndex reads the APKINDEX of one APKINDEX.tar.gz (the signature and the
// index are gzip members of one stream).
func readIndex(r io.Reader, want map[string]bool, got map[string]int64) error {
	z, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer z.Close()
	tr := tar.NewReader(z)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return errors.New("no APKINDEX in it")
		}
		if err != nil {
			return err
		}
		if h.Name != "APKINDEX" {
			continue
		}
		sc := bufio.NewScanner(io.LimitReader(tr, MaxIndex))
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		var p, v, i string
		flush := func() error {
			if p != "" && want[p+"\x00"+v] {
				n, err := strconv.ParseInt(i, 10, 64)
				if err != nil || n < 0 {
					return fmt.Errorf("%s-%s: installed size %q", p, v, i)
				}
				got[p+"\x00"+v] = n
			}
			p, v, i = "", "", ""
			return nil
		}
		for sc.Scan() {
			l := sc.Text()
			switch {
			case l == "":
				if err := flush(); err != nil {
					return err
				}
			case strings.HasPrefix(l, "P:") && p == "":
				p = l[2:]
			case strings.HasPrefix(l, "V:") && v == "":
				v = l[2:]
			case strings.HasPrefix(l, "I:") && i == "":
				i = l[2:]
			}
		}
		if err := sc.Err(); err != nil {
			return err
		}
		return flush()
	}
}
