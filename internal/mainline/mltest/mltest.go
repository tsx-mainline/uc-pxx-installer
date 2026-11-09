// Package mltest makes made-up mainline payloads for tests: a boot image
// built from its parts the way the AOSP mkbootimg builds one (an independent
// model, not package mainline's Assemble), a flat device tree, a root file
// system image with an ext4 superblock, and a bundle folder. It holds no
// vendor byte. Only test files import it.
package mltest

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"

	"uc-pxx-installer/internal/mainline"
)

// EmptyGzip is what `gzip -9n < /dev/null` writes: the 20-byte ramdisk of
// kernel/mkimage.sh.
var EmptyGzip = []byte{0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02, 0x03, 0x03, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}

// Cmdline is the header command line of kernel/mkimage.sh.
const Cmdline = "console=ttyS2,115200n8 rdinit=/init"

// Base is mkbootimg's default --base.
const Base = 0x10000000

// Fill is n pseudo-random bytes.
func Fill(seed int64, n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

func pad4(b []byte) []byte {
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

// FDT is a flat device tree (version 17) with the root node and /chosen,
// whose only property is bootargs = args. extra adds properties to /chosen
// (name -> value bytes, written as given).
func FDT(args string, extra ...[2]string) []byte {
	be := binary.BigEndian
	u32 := func(b []byte, v uint32) []byte { return be.AppendUint32(b, v) }
	strs := []byte("bootargs\x00")
	var st []byte
	st = u32(st, 1) // the root node
	st = pad4(append(st, 0))
	st = u32(st, 1)
	st = pad4(append(st, "chosen\x00"...))
	prop := func(nameOff int, v []byte) {
		st = u32(st, 3)
		st = u32(st, uint32(len(v)))
		st = u32(st, uint32(nameOff))
		st = pad4(append(st, v...))
	}
	prop(0, append([]byte(args), 0))
	for _, e := range extra {
		off := len(strs)
		strs = append(strs, e[0]+"\x00"...)
		prop(off, []byte(e[1]))
	}
	st = u32(st, 2)
	st = u32(st, 2)
	st = u32(st, 9)
	const hdr = 40
	rsv := make([]byte, 16) // the empty memory reservation map
	offRsv := hdr
	offStruct := offRsv + len(rsv)
	offStrings := offStruct + len(st)
	total := offStrings + len(strs)
	var b []byte
	for _, v := range []uint32{0xd00dfeed, uint32(total), uint32(offStruct), uint32(offStrings), uint32(offRsv), 17, 16, 0,
		uint32(len(strs)), uint32(len(st))} {
		b = u32(b, v)
	}
	b = append(b, rsv...)
	b = append(b, st...)
	b = append(b, strs...)
	return b
}

// Shim is a made-up kernel part of kernel/mkimage.sh: a stub of 64 KiB, the
// device tree with "Cmdline uc-pxx.id=TOKEN" at 64 KiB, then linux bytes.
func Shim(token string, linux int, seed int64) []byte {
	k := Fill(seed, mainline.ShimDTB)
	k = append(k, FDT(Cmdline+" uc-pxx.id="+token)...)
	return append(k, Fill(seed+1, linux)...)
}

// MkBootImg builds a header version 0 boot image with page size 2048 from
// its parts, as the AOSP mkbootimg does with its default offsets: an empty
// second gives second_size 0 and second_addr 0; the id field is sha1 over
// each part and its u32 size; every part is padded with zeros to a page.
func MkBootImg(kernel, ramdisk, second []byte, name, cmdline string) []byte {
	const pg = 2048
	le := binary.LittleEndian
	h := make([]byte, pg)
	copy(h, "ANDROID!")
	secAddr := uint32(0)
	if len(second) > 0 {
		secAddr = Base + 0x00f00000
	}
	rdAddr := uint32(0)
	if len(ramdisk) > 0 {
		rdAddr = Base + 0x01000000
	}
	for i, v := range []uint32{uint32(len(kernel)), Base + 0x8000, uint32(len(ramdisk)), rdAddr, uint32(len(second)), secAddr,
		Base + 0x100, pg, 0, 0} {
		le.PutUint32(h[8+4*i:], v)
	}
	copy(h[48:64], name)
	copy(h[64:576], cmdline)
	s := sha1.New()
	for _, p := range [][]byte{kernel, ramdisk, second} {
		s.Write(p)
		s.Write(le.AppendUint32(nil, uint32(len(p))))
	}
	copy(h[576:608], s.Sum(nil))
	out := h
	for _, p := range [][]byte{kernel, ramdisk, second} {
		out = append(out, p...)
		for len(out)%pg != 0 {
			out = append(out, 0)
		}
	}
	return out
}

// Template is a made-up boot template (no resource) with token.
func Template(token string, linux int, seed int64) []byte {
	return MkBootImg(Shim(token, linux, seed), EmptyGzip, nil, token, Cmdline)
}

// Expected is the made-up image of mkimage.sh --resource res with the same
// inputs as Template.
func Expected(token string, linux int, seed int64, res []byte) []byte {
	return MkBootImg(Shim(token, linux, seed), EmptyGzip, res, token, Cmdline)
}

// Rootfs is a made-up root file system image of n bytes with an ext4
// superblock magic, label and size (blocks of 4096 bytes when n is a
// multiple of 4096, else of 1024).
func Rootfs(n int, label string, seed int64) []byte {
	b := Fill(seed, n)
	sb := b[1024:2048]
	for i := range sb {
		sb[i] = 0
	}
	le := binary.LittleEndian
	le.PutUint16(sb[0x38:], 0xEF53)
	copy(sb[0x78:0x88], label)
	bs, lg := 1024, uint32(0)
	if n%4096 == 0 {
		bs, lg = 4096, 2
	}
	le.PutUint32(sb[0x18:], lg)
	le.PutUint32(sb[0x04:], uint32(n/bs))
	return b
}

// SHA is the sha256 in hex.
func SHA(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Chain is the boot chain a manifest requires.
type Chain struct {
	Version, BuildType                   string
	UBoot, Trust, Dtbo, Vbmeta, Resource string
}

// Bundle is a made-up payload: the manifest and the two images.
type Bundle struct {
	Manifest map[string]any
	Template []byte
	Rootfs   []byte
}

// NewBundle makes a consistent bundle: token, a template with linux bytes,
// a root file system of rootfs bytes, the chain, the resource the keeper
// holds (for boot_assembled) and the vbmeta boot cap.
func NewBundle(token string, linux, rootfs int, ch Chain, res []byte, cap int64) *Bundle {
	tmpl := Template(token, linux, 11)
	asm := Expected(token, linux, 11, res)
	rf := Rootfs(rootfs, mainline.RootfsLabel, 12)
	m := map[string]any{
		"schema": 1, "kind": mainline.Kind, "version": "v0.0.1-test", "model": mainline.Model,
		"kernel":         map[string]any{"rev": mainline.ReviewedKernelRev},
		"token":          token,
		"boot_template":  map[string]any{"file": mainline.TemplateFile, "size": len(tmpl), "sha256": SHA(tmpl)},
		"boot_assembled": map[string]any{"resource_sha256": ch.Resource, "size": len(asm), "sha256": SHA(asm)},
		"rootfs":         map[string]any{"file": mainline.RootfsFile, "size": len(rf), "sha256": SHA(rf), "label": mainline.RootfsLabel},
		"requires_bootchain": map[string]any{"version": ch.Version, "build_type": ch.BuildType, "uboot_sha256": ch.UBoot,
			"trust_sha256": ch.Trust, "dtbo_sha256": ch.Dtbo, "vbmeta_sha256": ch.Vbmeta, "resource_sha256": ch.Resource},
		"vbmeta_boot_cap": cap,
	}
	return &Bundle{Manifest: m, Template: tmpl, Rootfs: rf}
}

// ManifestJSON is the manifest as a file.
func (b *Bundle) ManifestJSON() []byte {
	j, err := json.MarshalIndent(b.Manifest, "", "  ")
	if err != nil {
		panic(err)
	}
	return j
}

// Write writes the bundle folder dir/uc-pxx-mainline and returns its path.
func (b *Bundle) Write(dir string) (string, error) {
	d := filepath.Join(dir, mainline.Dir)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return "", err
	}
	for n, data := range map[string][]byte{mainline.ManifestFile: b.ManifestJSON(), mainline.TemplateFile: b.Template,
		mainline.RootfsFile: b.Rootfs} {
		if err := os.WriteFile(filepath.Join(d, n), data, 0o644); err != nil {
			return "", err
		}
	}
	return d, nil
}

// Sub returns the object at key of the manifest map (for test edits).
func (b *Bundle) Sub(key string) map[string]any { return b.Manifest[key].(map[string]any) }

// Reviewed is the reviewed-payload entry of the bundle (its three files).
func (b *Bundle) Reviewed() mainline.Reviewed {
	return mainline.Reviewed{Version: "test", ManifestSHA256: SHA(b.ManifestJSON()), TemplateSHA256: SHA(b.Template),
		RootfsSHA256: SHA(b.Rootfs)}
}

// Gate is a test gate that lists this bundle as reviewed (no kernel and no
// boot-control check: a made-up template has no Image at 2 MiB, a made-up
// root file system no files).
func (b *Bundle) Gate() *mainline.Gate {
	return mainline.TestGate([]mainline.Reviewed{b.Reviewed()}, nil, nil)
}
