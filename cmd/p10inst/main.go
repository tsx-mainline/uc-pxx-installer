// p10inst is the UC-P10 firmware installer. The default build has no write
// path to the eMMC: it finds the decryption key, checks a firmware file
// against the manifests, unpacks it, decodes A/B sectors, and on the device
// shows the status screen and runs the USB gadget. The install build
// (-tags install, install.go) adds plan, install --dry-run and fake-emmc,
// which work on image files only.
//
//	p10inst findkey [--raw] [--write KEYFILE] FW.bin LIB...
//	p10inst verify  [--keys KEYFILE | --lib LIB ...] FW.bin
//	p10inst unpack  --keys KEYFILE -o DIR FW.bin
//	p10inst ab      [--offset N] SECTOR.bin
//	p10inst status  [--root DIR]
//	p10inst readonly
//	p10inst ui      [--tty DEV | --stdin] [--leds]
//	p10inst usbd    [--drive FILE] [--once]
//	p10inst drive   [FILE]
//	p10inst loader  --check | --yes   (status build only, loader.go)
//
// Exit status: 0 OK, 1 a check failed, 2 usage or I/O error.
// The key is never printed.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/fatfs"
	"uc-pxx-installer/internal/manifest"
	"uc-pxx-installer/internal/pcdfw"
)

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  p10inst findkey [--raw] [--write KEYFILE] FW.bin LIB...
  p10inst verify  [--keys KEYFILE | --lib LIB ...] FW.bin
  p10inst unpack  --keys KEYFILE -o DIR FW.bin
  p10inst ab      [--offset N] SECTOR.bin
on the device (RAM image):
  p10inst status  [--root DIR]
  p10inst readonly
  p10inst ui      [--tty DEV | --stdin] [--leds]
  p10inst usbd    [--drive FILE] [--once]
  p10inst drive   [FILE]   (read-only: the firmware file on the P10-INSTALL drive)
  p10inst keytest [--seconds N]   (read-only: press the six touch keys from the left)
`+extraUsage)
	os.Exit(2)
}

// extraCmds and extraUsage are filled by the files of the install build
// (go build -tags install). The default build, which goes into the
// status-only RAM image, has only loader (loader.go: the restart into the
// download mode, which writes nothing): it holds no write path.
var (
	extraCmds  = map[string]func([]string){}
	extraUsage = ""
)

func fail(code int, format string, a ...any) {
	fmt.Fprintf(os.Stderr, "p10inst: "+format+"\n", a...)
	os.Exit(code)
}

type libList []string

func (l *libList) String() string     { return strings.Join(*l, ",") }
func (l *libList) Set(v string) error { *l = append(*l, v); return nil }

func readLibs(paths []string) []pcdfw.Lib {
	var libs []pcdfw.Lib
	for _, p := range paths {
		d, err := os.ReadFile(p)
		if err != nil {
			fail(2, "%v", err)
		}
		libs = append(libs, pcdfw.Lib{Name: filepath.Base(p), Data: d})
	}
	return libs
}

func openFW(path string) (*os.File, int64) {
	f, err := os.Open(path)
	if err != nil {
		fail(2, "%v", err)
	}
	st, err := f.Stat()
	if err != nil {
		fail(2, "%v", err)
	}
	return f, st.Size()
}

// findOne runs the key search and accepts only exactly one pair.
func findOne(fw *os.File, size int64, libs []pcdfw.Lib, raw bool) pcdfw.Keys {
	res, err := pcdfw.FindKeys(fw, size, libs, raw)
	if err != nil {
		fail(2, "%v", err)
	}
	for _, n := range res.Notes {
		fmt.Println("  " + n)
	}
	fmt.Printf("candidates %d, keys passing the signature test %d, pairs %d\n",
		res.Candidates, res.KeysPassed, len(res.Pairs))
	switch len(res.Pairs) {
	case 1:
		return res.Pairs[0]
	case 0:
		fail(1, "no key found")
	default:
		fail(1, "ambiguous: %d pairs, using none", len(res.Pairs))
	}
	return pcdfw.Keys{}
}

func fileSHA(f *os.File) string {
	h := sha256.New()
	if _, err := io.Copy(h, io.NewSectionReader(f, 0, 1<<62)); err != nil {
		fail(2, "%v", err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

type counter struct {
	h hash.Hash
	n int64
}

func (c *counter) Write(p []byte) (int, error) { c.n += int64(len(p)); return c.h.Write(p) }

func cmdFindkey(args []string) {
	fs := flag.NewFlagSet("findkey", flag.ExitOnError)
	raw := fs.Bool("raw", false, "test every 16-byte window, not only strings")
	write := fs.String("write", "", "save the key file here (mode 0600)")
	fs.Parse(args)
	if fs.NArg() < 2 {
		usage()
	}
	fw, size := openFW(fs.Arg(0))
	defer fw.Close()
	k := findOne(fw, size, readLibs(fs.Args()[1:]), *raw)
	fmt.Println("RESULT: exactly one key/IV pair found")
	if *write != "" {
		if err := pcdfw.WriteKeyFile(*write, k); err != nil {
			fail(2, "%v", err)
		}
		fmt.Println("key file written:", *write)
	}
}

func cmdVerify(args []string) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	keyfile := fs.String("keys", "", "key file")
	var libs libList
	fs.Var(&libs, "lib", "library to find the key in (repeat)")
	fs.Parse(args)
	if fs.NArg() != 1 || (*keyfile == "") == (len(libs) == 0) {
		usage()
	}
	ms, err := manifest.Embedded()
	if err != nil {
		fail(2, "manifests: %v", err)
	}
	fw, size := openFW(fs.Arg(0))
	defer fw.Close()
	sha := fileSHA(fw)
	m := manifest.ByFirmwareSHA(ms, sha)
	if m == nil {
		fail(1, "not a supported firmware (sha256 %s)", sha)
	}
	fmt.Printf("firmware: %s, %s\n", m.Name(), m.Firmware.File)
	var k pcdfw.Keys
	if *keyfile != "" {
		if k, err = pcdfw.LoadKeyFile(*keyfile); err != nil {
			fail(2, "%v", err)
		}
	} else {
		k = findOne(fw, size, readLibs(libs), false)
	}
	f, err := pcdfw.Open(fw, size, k)
	if err != nil {
		fail(1, "%v", err)
	}
	segs, err := f.Check()
	if err != nil {
		fail(1, "%v", err)
	}
	if len(segs) != len(m.Partitions) {
		fail(1, "%d segments, the manifest lists %d", len(segs), len(m.Partitions))
	}
	bad := 0
	for _, s := range segs {
		p, ok := m.Part(s.Name())
		if !ok {
			fail(1, "segment %s is not in the manifest", s.Name())
		}
		c := &counter{h: sha256.New()}
		if _, err := f.WriteSegment(s, c); err != nil {
			fail(1, "%s: %v", s.Name(), err)
		}
		got := hex.EncodeToString(c.h.Sum(nil))
		ok = got == p.SHA256 && c.n == p.Size
		if !ok {
			bad++
		}
		fmt.Printf("%-4s %-7s %11d  %s  v%s\n", map[bool]string{true: "OK", false: "BAD"}[ok],
			s.Name(), c.n, got, s.VersionString())
	}
	if bad > 0 {
		fail(1, "%d partition(s) do not match the manifest", bad)
	}
	fmt.Println("RESULT: every partition matches the manifest")
}

func cmdUnpack(args []string) {
	fs := flag.NewFlagSet("unpack", flag.ExitOnError)
	keyfile := fs.String("keys", "", "key file")
	out := fs.String("o", "", "output directory")
	fs.Parse(args)
	if fs.NArg() != 1 || *keyfile == "" || *out == "" {
		usage()
	}
	k, err := pcdfw.LoadKeyFile(*keyfile)
	if err != nil {
		fail(2, "%v", err)
	}
	fw, size := openFW(fs.Arg(0))
	defer fw.Close()
	f, err := pcdfw.Open(fw, size, k)
	if err != nil {
		fail(1, "%v", err)
	}
	segs, err := f.Check()
	if err != nil {
		fail(1, "%v", err)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fail(2, "%v", err)
	}
	for _, s := range segs {
		name := filepath.Join(*out, fmt.Sprintf("%02d_%s_%04x.img", s.Index, s.Name(), s.SegID))
		o, err := os.Create(name)
		if err != nil {
			fail(2, "%v", err)
		}
		c := &counter{h: sha256.New()}
		if _, err := f.WriteSegment(s, io.MultiWriter(o, c)); err != nil {
			fail(1, "%s: %v", s.Name(), err)
		}
		if err := o.Close(); err != nil {
			fail(2, "%v", err)
		}
		fmt.Printf("%-7s %11d  %x  %s\n", s.Name(), c.n, c.h.Sum(nil), filepath.Base(name))
	}
}

func cmdAB(args []string) {
	fs := flag.NewFlagSet("ab", flag.ExitOnError)
	off := fs.Int64("offset", 0, "byte offset of the sector in the file")
	fs.Parse(args)
	if fs.NArg() != 1 {
		usage()
	}
	f, err := os.Open(fs.Arg(0))
	if err != nil {
		fail(2, "%v", err)
	}
	defer f.Close()
	buf := make([]byte, 512)
	if _, err := f.ReadAt(buf, *off); err != nil {
		fail(2, "%v", err)
	}
	a, err := avbab.Parse(buf)
	if err != nil {
		fail(2, "%v", err)
	}
	for i, s := range a.Slots {
		fmt.Printf("slot %s: %v%s\n", avbab.SlotName(i), s, map[bool]string{true: ", can be the fallback"}[s.Keeper()])
	}
	fmt.Printf("crc: stored %08x, computed %08x\n", a.CRCStored, a.CRCComputed)
	if err := a.Validate(); err != nil {
		fmt.Println("INVALID:", err)
		os.Exit(1)
	}
	fmt.Printf("valid; boots next: slot %s%s\n", avbab.SlotName(a.Pick()),
		map[bool]string{true: " (priority tie: the installer never relies on a tie)"}[a.Tie()])
}

// DefaultDrive is the backing file of the P10-INSTALL drive (image/overlay/etc/init.d/rcS).
const DefaultDrive = "/run/p10inst/drive/drive.img"

// cmdDrive reads the drive image read-only with package fatfs and says which
// firmware file the installer would use, or why it refuses the drive. While
// the host has the drive, the answer can be out of date.
func cmdDrive(args []string) {
	fs := flag.NewFlagSet("drive", flag.ExitOnError)
	fs.Parse(args)
	path := DefaultDrive
	switch fs.NArg() {
	case 0:
	case 1:
		path = fs.Arg(0)
	default:
		usage()
	}
	fmt.Println("drive:", path)
	v, err := fatfs.OpenImage(path)
	if err != nil {
		if errors.Is(err, fatfs.ErrNotFAT) || errors.Is(err, fatfs.ErrDamaged) {
			fmt.Println("RESULT: refused:", err)
			os.Exit(1)
		}
		fail(2, "%v", err)
	}
	defer v.Close()
	in := v.Info()
	fmt.Printf("volume: %s, label %s, %d bytes at offset %d, %d-byte clusters, %d clusters\n", in.Type,
		fatfs.Quote(in.Label), in.Bytes, in.Offset, in.ClusterSize, in.Clusters)
	if es, err := v.ReadDir("/"); err == nil {
		fmt.Println("top folder:")
		for _, e := range es {
			kind, size := "file", fmt.Sprint(e.Size)
			if e.Dir {
				kind, size = "dir", "-"
			}
			fmt.Printf("  %-4s %10s  %s\n", kind, size, fatfs.Quote(e.Name))
		}
	}
	f, err := fatfs.FindFirmware(v)
	if err != nil {
		if r, ok := err.(*fatfs.Refusal); ok {
			fmt.Printf("RESULT: refused (%s): %s\n", r.Reason, r.Why)
			for _, d := range r.Detail {
				fmt.Println("  " + d)
			}
			os.Exit(1)
		}
		fail(2, "%v", err)
	}
	fmt.Printf("RESULT: firmware %s, %d bytes, %d fragment(s)\n", fatfs.Quote(f.Name()), f.Size(), f.Fragments())
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "findkey":
		cmdFindkey(os.Args[2:])
	case "verify":
		cmdVerify(os.Args[2:])
	case "unpack":
		cmdUnpack(os.Args[2:])
	case "ab":
		cmdAB(os.Args[2:])
	case "status":
		cmdStatus(os.Args[2:])
	case "readonly":
		cmdReadonly(os.Args[2:])
	case "ui":
		cmdUI(os.Args[2:])
	case "usbd":
		cmdUsbd(os.Args[2:])
	case "drive":
		cmdDrive(os.Args[2:])
	case "keytest":
		cmdKeytest(os.Args[2:])
	default:
		if f, ok := extraCmds[os.Args[1]]; ok {
			f(os.Args[2:])
			return
		}
		usage()
	}
}
