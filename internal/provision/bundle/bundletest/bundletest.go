// Package bundletest makes made-up profile bundles for the tests (the
// layout of uc-pxx-linux bundle.py make: ustar, mode 0644, uid and gid 0,
// mtime 0; bundle.json, the four indexes, the .apk files sorted by path,
// lva.tar). Test files only (ci/check-no-writer.py TEST_ONLY).
package bundletest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"uc-pxx-installer/internal/provision/bundle"
)

// Pkg is a made-up package of a bundle.
type Pkg struct {
	Name, Version, Repo string
	Data                []byte
	Installed           int64 // the I: field
}

// Bundle is a made-up bundle.
type Bundle struct {
	Tar     []byte
	Doc     []byte
	DocV    bundle.Doc
	Members map[string][]byte // every member by path, bundle.json too
	Pins    bundle.Pins
}

// SHA is the hex sha256 of b.
func SHA(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Index is an APKINDEX.tar.gz of the packages (a signature part as a first
// gzip member, as Alpine's).
func Index(pk []Pkg) []byte {
	var txt bytes.Buffer
	for _, p := range pk {
		fmt.Fprintf(&txt, "C:Q1fake%s=\nP:%s\nV:%s\nA:aarch64\nS:%d\nI:%d\n\n", p.Name, p.Name, p.Version, len(p.Data), p.Installed)
	}
	var out bytes.Buffer
	for i, members := range [][2][]byte{{[]byte(".SIGN.RSA.fake.rsa.pub"), []byte("sig")}, {[]byte("APKINDEX"), txt.Bytes()}} {
		var t bytes.Buffer
		tw := tar.NewWriter(&t)
		tw.WriteHeader(&tar.Header{Name: string(members[0]), Mode: 0o644, Size: int64(len(members[1])), ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR})
		tw.Write(members[1])
		if i == 1 {
			tw.Close()
		} else {
			tw.Flush() // Alpine's signature part has no end of archive
		}
		z := gzip.NewWriter(&out)
		z.Write(t.Bytes())
		z.Close()
	}
	return out.Bytes()
}

// Options of Make.
type Options struct {
	Release, Profile, Alpine string
	Base                     []string // "NAME=VERSION"
	Pkgs                     []Pkg
	LVA                      []byte // nil: none
	Add, World               []string
	// RecordPad: pad the archive to a multiple of 10240 bytes (Python's
	// tarfile); otherwise only the two zero blocks (Go's)
	RecordPad bool
}

// Make makes the bundle.
func Make(o Options) *Bundle {
	if o.Alpine == "" {
		o.Alpine = "v3.24"
	}
	byRepo := map[string][]Pkg{}
	for _, p := range o.Pkgs {
		byRepo[p.Repo] = append(byRepo[p.Repo], p)
	}
	b := &Bundle{Members: map[string][]byte{}}
	d := bundle.Doc{Schema: 1, Kind: bundle.Kind, Release: o.Release, Profile: o.Profile, Alpine: o.Alpine, Arch: bundle.Arch,
		Add: o.Add, World: append([]string(nil), o.World...), Base: append([]string(nil), o.Base...)}
	sort.Strings(d.World)
	sort.Strings(d.Base)
	for _, r := range bundle.Repos {
		ix := Index(byRepo[r])
		p := o.Alpine + "/" + r + "/aarch64/APKINDEX.tar.gz"
		b.Members[p] = ix
		d.Indexes = append(d.Indexes, bundle.Index{Repo: r, Path: p, Size: int64(len(ix)), SHA256: SHA(ix)})
	}
	for _, p := range o.Pkgs {
		path := o.Alpine + "/" + p.Repo + "/aarch64/" + p.Name + "-" + p.Version + ".apk"
		b.Members[path] = p.Data
		d.Packages = append(d.Packages, bundle.Package{Name: p.Name, Version: p.Version, Repo: p.Repo, Path: path, Size: int64(len(p.Data)),
			SHA256: SHA(p.Data), Checksum: "Q1fake" + p.Name + "=", Scripts: []string{}, Triggers: []string{}})
	}
	sort.Slice(d.Packages, func(i, j int) bool { return d.Packages[i].Path < d.Packages[j].Path })
	if o.LVA != nil {
		b.Members[bundle.LVAName] = o.LVA
		d.LVA = &bundle.File{Path: bundle.LVAName, Size: int64(len(o.LVA)), SHA256: SHA(o.LVA)}
	}
	js, _ := json.MarshalIndent(d, "", " ")
	js = append(js, '\n')
	b.Doc, b.DocV = js, d
	b.Members[bundle.DocName] = js
	var t bytes.Buffer
	tw := tar.NewWriter(&t)
	add := func(name string, data []byte) {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR,
			Typeflag: tar.TypeReg})
		tw.Write(data)
	}
	add(bundle.DocName, js)
	for _, m := range d.Members() {
		add(m.Path, b.Members[m.Path])
	}
	tw.Close()
	if o.RecordPad {
		for t.Len()%10240 != 0 {
			t.WriteByte(0)
		}
	}
	b.Tar = t.Bytes()
	lva := o.LVA != nil
	b.Pins = bundle.Pins{Size: int64(len(b.Tar)), SHA256: SHA(b.Tar), IndexSHA256: SHA(js),
		Expect: &bundle.Expect{Release: o.Release, Profile: o.Profile, Alpine: o.Alpine, Packages: len(o.Pkgs), LVA: lva}}
	return b
}

// Simple is a small kiosk bundle of three packages.
func Simple(release string, pad bool) *Bundle {
	return Make(Options{Release: release, Profile: "kiosk", Base: []string{"busybox=1.37.0-r20", "musl=1.2.5-r10", "apk-tools=3.0.9-r0"},
		Pkgs: []Pkg{
			{Name: "tsx-kiosk", Version: "0.3.13_pre20261113-r0", Repo: "common", Data: bytes.Repeat([]byte("k"), 3000), Installed: 40960},
			{Name: "chromium", Version: "142.0-r0", Repo: "community", Data: bytes.Repeat([]byte("c"), 70000), Installed: 400000},
			{Name: "uc-pxx-kiosk", Version: "0.2.0-r0", Repo: "uc-pxx", Data: bytes.Repeat([]byte("u"), 513), Installed: 4096},
		},
		Add: []string{"tsx-kiosk", "uc-pxx-kiosk"}, World: []string{"tsx-kiosk", "uc-pxx-kiosk", "busybox"}, RecordPad: pad})
}
