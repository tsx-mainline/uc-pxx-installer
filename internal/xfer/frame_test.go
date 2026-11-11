package xfer

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEncodeFrameNeverAMultipleOf64(t *testing.T) {
	for n := 0; n < 4096; n++ {
		f, err := EncodeFrame(TypeHello|TypeReply, 3, bytes.Repeat([]byte("a"), n))
		if err != nil {
			t.Fatal(err)
		}
		if len(f)%64 == 0 {
			t.Fatalf("body %d: a frame of %d bytes", n, len(f))
		}
	}
	if _, err := EncodeFrame(1, 1, make([]byte, MaxBody+1)); err == nil {
		t.Fatal("an oversize body was encoded")
	}
	// 16 + 496 = 512: one space of padding
	f, _ := EncodeFrame(1, 1, bytes.Repeat([]byte("b"), 496))
	if len(f) != 513 || f[512] != ' ' {
		t.Fatalf("padding %d", len(f))
	}
}

func TestParseFrame(t *testing.T) {
	good, _ := EncodeFrame(TypePutBegin, 77, []byte(`{"name":"rootfs.ext4","size":5}`))
	h, body, err := ParseFrame(good)
	if err != nil || h.Type != TypePutBegin || h.Seq != 77 || !bytes.HasPrefix(body, []byte(`{"name"`)) {
		t.Fatalf("%+v %q %v", h, body, err)
	}
	set := func(off int, v uint32, w int) []byte {
		b := append([]byte(nil), good...)
		if w == 2 {
			binary.LittleEndian.PutUint16(b[off:], uint16(v))
		} else {
			binary.LittleEndian.PutUint32(b[off:], v)
		}
		return b
	}
	for name, b := range map[string][]byte{
		"short":      good[:15],
		"magic":      append([]byte("PXX2"), good[4:]...),
		"flags":      set(6, 1, 2),
		"len+1":      set(8, uint32(len(good)-16+1), 4),
		"len-1":      set(8, uint32(len(good)-16-1), 4),
		"oversize":   set(8, MaxBody+1, 4),
		"huge":       set(8, 0xffffffff, 4),
		"reply type": set(4, TypeHello|TypeReply, 2),
		"trailing":   append(append([]byte(nil), good...), 'x'),
	} {
		if _, _, err := ParseFrame(b); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func FuzzParseFrame(f *testing.F) {
	for _, typ := range []uint16{TypeHello, TypePutBegin, TypeCommit, TypeStatus, 9} {
		b, _ := EncodeFrame(typ, 1, []byte(`{"name":"manifest.json","size":1597}`))
		f.Add(b)
	}
	f.Add([]byte("PXX1"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		h, body, err := ParseFrame(b)
		if err != nil {
			return
		}
		if len(b) != HeaderSize+int(h.Len) || h.Len > MaxBody || len(body) != int(h.Len) || h.Flags != 0 || h.Type&TypeReply != 0 {
			t.Fatalf("accepted a bad frame: %+v len %d", h, len(b))
		}
		if len(b)%64 != 0 {
			e, err := EncodeFrame(h.Type, h.Seq, body)
			if err != nil || !bytes.Equal(e, b) {
				t.Fatalf("no round trip")
			}
		}
		// the body decoders never panic
		var p PutBegin
		decodeStrict(body, &p)
		var c Commit
		decodeStrict(body, &c)
		decodeStrict(body, &struct{}{})
	})
}

// FuzzSession feeds a session with arbitrary transfers (the fuzz input cut at
// 0xff bytes, plus one good HELLO). Whatever the host sends, no panic, no
// file outside the receiver's folder, and nothing is left but the state file
// (no pinned manifest can be made by chance).
func FuzzSession(f *testing.F) {
	fx := newFixture(1 << 20)
	hello, _ := EncodeFrame(TypeHello, 1, nil)
	pb, _ := EncodeFrame(TypePutBegin, 2, []byte(`{"name":"manifest.json","size":40}`))
	f.Add(append(append(append([]byte(nil), hello...), 0xff), pb...))
	f.Add([]byte("PXX1\x05\x00\x00\x00\x02\x00\x00\x00\x01\x00\x00\x00{}"))
	f.Fuzz(func(t *testing.T, in []byte) {
		dir := t.TempDir()
		r, err := NewReceiver(Config{Dir: dir, Pins: []string{fx.pin}, MemAvailable: func() int64 { return 1 << 30 },
			Free: func(string) int64 { return 1 << 30 }})
		if err != nil {
			t.Fatal(err)
		}
		l := newLink()
		done := make(chan struct{})
		go func() { r.Serve(l); close(done) }()
		for _, part := range bytes.Split(in, []byte{0xff}) {
			l.hostOut(part)
		}
		go func() {
			for range l.replies {
			}
		}()
		for i := 0; i < 200; i++ { // until the session took every packet or ended
			l.mu.Lock()
			n := len(l.pkts)
			l.mu.Unlock()
			if n == 0 {
				break
			}
			select {
			case <-done:
				i = 200
			case <-time.After(time.Millisecond):
			}
		}
		l.disable()
		<-done
		close(l.replies)
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if e.Name() != StateFile {
				t.Fatalf("left %s", e.Name())
			}
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "complete")); err == nil {
			t.Fatal("a file outside the folder")
		}
	})
}

func TestDescriptorsBlob(t *testing.T) {
	d := Descriptors()
	if binary.LittleEndian.Uint32(d[0:]) != ffsDescriptorsMagicV2 || int(binary.LittleEndian.Uint32(d[4:])) != len(d) {
		t.Fatal("header")
	}
	if fl := binary.LittleEndian.Uint32(d[8:]); fl != 15 {
		t.Fatalf("flags %#x", fl)
	}
	// counts fs 3, hs 3, ss 5, os 2; then 23 + 23 + 35 bytes of descriptors
	want := []uint32{3, 3, 5, 2}
	for i, w := range want {
		if c := binary.LittleEndian.Uint32(d[12+4*i:]); c != w {
			t.Fatalf("count %d = %d", i, c)
		}
	}
	desc := d[28:]
	iface := desc[:9]
	if iface[1] != 4 || iface[4] != 2 || iface[5] != IfaceClass || iface[6] != IfaceSubclass || iface[7] != IfaceProtocol {
		t.Fatalf("interface %x", iface)
	}
	if desc[9+2] != EPOut || desc[16+2] != EPIn {
		t.Fatalf("endpoints %x", desc[:23])
	}
	if !bytes.Contains(d, []byte("WINUSB\x00\x00")) {
		t.Fatal("no WINUSB compat id")
	}
	// the extended property: REG_SZ (the kernel serves no REG_MULTI_SZ), 8-bit
	// name and data with the NUL (FunctionFS sends them as UTF-16)
	prop := append(append([]byte{1, 0, 0, 0, byte(len(ExtPropName) + 1), 0}, ExtPropName+"\x00"...), byte(len(InterfaceGUID)+1), 0, 0, 0)
	prop = append(prop, InterfaceGUID+"\x00"...)
	if !bytes.Contains(d, prop) {
		t.Fatal("no REG_SZ DeviceInterfaceGUID property")
	}
	if bytes.Contains(d, utf16z(ExtPropName)) {
		t.Fatal("a UTF-16 property name: FunctionFS wants 8-bit strings")
	}
	s := Strings()
	if binary.LittleEndian.Uint32(s) != ffsStringsMagic || int(binary.LittleEndian.Uint32(s[4:])) != len(s) ||
		!strings.Contains(string(s), InterfaceString+"\x00") {
		t.Fatalf("strings %q", s)
	}
}
