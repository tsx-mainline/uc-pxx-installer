package mlstash_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uc-pxx-installer/internal/keykeep"
	"uc-pxx-installer/internal/mlstash"
	"uc-pxx-installer/internal/pcdfw/pcdfwtest"
)

func parts() map[string][]byte {
	m := map[string][]byte{}
	for i, p := range mlstash.Parts {
		m[p] = bytes.Repeat([]byte{byte(i + 1)}, 4096*(i+1))
	}
	return m
}

func write(t *testing.T) (string, *mlstash.Loaded) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "mlstash")
	st := mlstash.Stash{BootID: "boot-1", RAMToken: "p10inst-test", Firmware: mlstash.Firmware{Name: "9.9.9.1 (userdebug)", SHA256: strings.Repeat("ab", 32)},
		Key: mlstash.Key{Source: "key file"}}
	l, err := mlstash.Write(dir, st, parts(), keykeep.Encode(pcdfwtest.Keys()))
	if err != nil {
		t.Fatal(err)
	}
	return dir, l
}

func TestRoundTrip(t *testing.T) {
	dir, l := write(t)
	for p, b := range parts() {
		if !bytes.Equal(l.Part(p), b) {
			t.Fatalf("%s differs", p)
		}
	}
	if !bytes.Equal(l.Record(), keykeep.Encode(pcdfwtest.Keys())) || l.Key.RecordSHA256 != keykeep.SHA256(l.Record()) || l.BootID != "boot-1" {
		t.Fatal("record or index")
	}
	// the index never holds the key
	idx, _ := os.ReadFile(filepath.Join(dir, mlstash.Index))
	k := pcdfwtest.Keys()
	if bytes.Contains(idx, k.Head[:]) || bytes.Contains(idx, k.IV[:]) || bytes.Contains(idx, []byte("P10INST-KEYKEEP1")) {
		t.Fatal("the index holds key bytes")
	}
	st, _ := os.Stat(dir)
	if st.Mode().Perm() != 0o700 {
		t.Fatalf("folder mode %v", st.Mode())
	}
	// a stash is made once
	if _, err := mlstash.Write(dir, l.Stash, parts(), l.Record()); err == nil || !strings.Contains(err.Error(), "once per boot") {
		t.Fatalf("second write: %v", err)
	}
}

func TestLoadRefuses(t *testing.T) {
	for _, c := range []struct {
		name string
		mut  func(dir string)
		want string
	}{
		{"a part changed", func(d string) { os.WriteFile(filepath.Join(d, "uboot.img"), []byte("x"), 0o600) }, "the index says"},
		{"another file", func(d string) { os.WriteFile(filepath.Join(d, "extra"), []byte("x"), 0o600) }, "want exactly"},
		{"a part missing", func(d string) { os.Remove(filepath.Join(d, "dtbo.img")) }, "want exactly"},
		{"a file 0644", func(d string) { os.Chmod(filepath.Join(d, "trust.img"), 0o644) }, "0600"},
		{"the folder 0755", func(d string) { os.Chmod(d, 0o755) }, "0700"},
		{"a symlink part", func(d string) {
			os.Rename(filepath.Join(d, "vbmeta.img"), filepath.Join(d, "..", "v"))
			os.Symlink(filepath.Join(d, "..", "v"), filepath.Join(d, "vbmeta.img"))
		}, ""},
		{"the record of another key", func(d string) {
			k := pcdfwtest.Keys()
			k.IV[0] ^= 1
			os.WriteFile(filepath.Join(d, "key.rec"), keykeep.Encode(k), 0o600)
		}, "not the key record"},
		{"a damaged record", func(d string) {
			b, _ := os.ReadFile(filepath.Join(d, "key.rec"))
			b[30] ^= 1
			os.WriteFile(filepath.Join(d, "key.rec"), b, 0o600)
		}, "not the key record"},
		{"an unknown index key", func(d string) {
			b, _ := os.ReadFile(filepath.Join(d, "stash.json"))
			os.WriteFile(filepath.Join(d, "stash.json"), bytes.Replace(b, []byte(`"schema"`), []byte(`"x": 1, "schema"`), 1), 0o600)
		}, "unknown field"},
		{"another kind", func(d string) {
			b, _ := os.ReadFile(filepath.Join(d, "stash.json"))
			os.WriteFile(filepath.Join(d, "stash.json"), bytes.Replace(b, []byte(mlstash.Kind), []byte("other"), 1), 0o600)
		}, "not a stash"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, _ := write(t)
			c.mut(dir)
			if _, err := mlstash.Load(dir); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v, want %q", err, c.want)
			}
		})
	}
}

func TestWriteRefuses(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	p := parts()
	delete(p, "resource")
	if _, err := mlstash.Write(dir, mlstash.Stash{BootID: "b"}, p, keykeep.Encode(pcdfwtest.Keys())); err == nil {
		t.Fatal("a stash without the resource")
	}
	if _, err := mlstash.Write(dir, mlstash.Stash{BootID: "b"}, parts(), make([]byte, 512)); err == nil {
		t.Fatal("a stash with a zero record")
	}
	if _, err := os.Lstat(dir); err == nil {
		t.Fatal("a refused write left the folder")
	}
}
