package fatfs

import (
	"encoding/binary"
	"io"
	"sync"
	"testing"

	"uc-pxx-installer/internal/fatfs/fattest"
)

// exercise runs every reader path on a volume, with a cap on the bytes read.
// It must not panic and must not hang, whatever the input.
func exercise(r io.ReaderAt, size int64) {
	fs, err := Open(r, size)
	if err != nil {
		return
	}
	_ = fs.Info()
	_ = fs.Dirty()
	_ = fs.Check()
	if f, err := FindFirmware(fs); err == nil {
		readSome(f)
	} else if r, ok := err.(*Refusal); ok {
		_ = r.Error()
	}
	budget := 64
	var walk func(p string, depth int)
	walk = func(p string, depth int) {
		es, err := fs.ReadDir(p)
		if err != nil || depth > 3 {
			return
		}
		for _, e := range es {
			if budget--; budget < 0 {
				return
			}
			q := p + "/" + e.Name
			if e.Dir {
				walk(q, depth+1)
			} else if f, err := fs.Open(q); err == nil {
				readSome(f)
			}
		}
	}
	walk("", 0)
}

// readSome reads up to 1 MiB of a file, in pieces, and its last byte.
func readSome(f *File) {
	buf := make([]byte, 64<<10)
	r := f.Reader()
	for n := 0; n < 1<<20; n += len(buf) {
		if _, err := r.Read(buf); err != nil {
			break
		}
	}
	if f.Size() > 0 {
		f.ReadAt(buf[:1], f.Size()-1)
		f.ReadAt(buf, f.Size()/2)
	}
}

// FuzzOpen: the input is the start of an image; the rest of the image (up
// to len + tailKiB KiB) reads as zero. So a small input can be the boot
// sector, the FAT and the root directory of a FAT16 or FAT32 volume.
func FuzzOpen(f *testing.F) {
	for _, typ := range []int{12, 16, 32} {
		im := fattest.New(small(typ))
		im.Root.File("fw.bin", pattern(1, 3000))
		sub := im.Root.Mkdir("A Directory")
		sub.File("long name file.txt", pattern(2, 700))
		cl := im.Alloc(5)
		cl[1], cl[3] = cl[3], cl[1]
		im.Root.FileAt("Frag.BIN", pattern(3, 5*im.ClusterSize()-1), cl)
		// the input: everything up to the end of the last cluster in use
		end := im.ClusterOff(cl[4]) + im.ClusterSize()
		f.Add(im.B[:end], uint32((len(im.B)-end)/1024))
	}
	f.Add(make([]byte, 512), uint32(0))
	f.Fuzz(func(t *testing.T, data []byte, tailKiB uint32) {
		size := int64(len(data)) + int64(tailKiB%(1<<20))*1024 // at most 1 GiB more
		exercise(zeroTail{data: data, size: size}, size)
	})
}

var (
	bases     []mutBase
	basesOnce sync.Once
)

type mutBase struct {
	r     io.ReaderAt
	size  int64
	limit int64 // the patches go below this offset: the metadata and the first data clusters
}

func loadBases(t testing.TB) []mutBase {
	basesOnce.Do(func() {
		for _, n := range []string{"busybox-1200m", "f16-lfn-frag", "f12", "linux-clean", "f32-none"} {
			s := loadSparse(t, n)
			fs, err := Open(s, s.size)
			if err != nil {
				t.Fatal(err)
			}
			lim := fs.dataOff + 64*fs.clusterSize
			bases = append(bases, mutBase{r: s, size: s.size, limit: lim})
		}
	})
	return bases
}

// FuzzMutate: the real images, with bytes changed. The input is a list of
// patches: offset (u32, taken modulo the metadata area), length (u8), bytes.
func FuzzMutate(f *testing.F) {
	f.Add(uint8(0), []byte{})
	f.Add(uint8(1), []byte{0x0b, 0, 0, 0, 2, 0, 1})                // bytes per sector
	f.Add(uint8(0), []byte{0x41, 0, 0, 0, 1, 1})                   // dirty bit
	f.Add(uint8(2), []byte{0x00, 0x08, 0, 0, 4, 0xff, 0xff, 0, 0}) // a FAT entry
	f.Add(uint8(3), []byte{0x00, 0x30, 0x25, 0, 4, 0xff, 0xff, 0xff, 0x0f})
	f.Fuzz(func(t *testing.T, which uint8, patches []byte) {
		bs := loadBases(t)
		b := bs[int(which)%len(bs)]
		o := newOverlay(b.r, b.size)
		for len(patches) >= 5 {
			off := int64(binary.LittleEndian.Uint32(patches)) % b.limit
			n := int(patches[4])
			patches = patches[5:]
			if n > len(patches) {
				n = len(patches)
			}
			o.set(off, patches[:n]...)
			patches = patches[n:]
		}
		exercise(o, b.size)
	})
}
