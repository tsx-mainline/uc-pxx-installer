package xfer

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// readyRig makes a real hand-over by a session and returns the folder.
func readyRig(t *testing.T) (string, fixture) {
	t.Helper()
	f := newFixture(1 << 20)
	g := newRig(t, Config{Pins: []string{f.pin}})
	h, wait := g.session()
	sendAll(t, h, f)
	if c := h.req(TypeCommit, Commit{}); c["ok"] != true {
		t.Fatalf("commit %v", c)
	}
	h.l.disable()
	wait()
	return g.dir, f
}

func TestCheckReadyRefusals(t *testing.T) {
	for name, change := range map[string]func(dir string, f fixture) []string{
		"unpinned": func(string, fixture) []string { return []string{strings.Repeat("a", 64)} },
		"no pins":  func(string, fixture) []string { return nil },
		"extra file": func(d string, f fixture) []string {
			os.WriteFile(filepath.Join(d, ReadyDir, "AUTO-STEP2"), nil, 0o600)
			return []string{f.pin}
		},
		"missing file": func(d string, f fixture) []string {
			os.Remove(filepath.Join(d, ReadyDir, TemplateName))
			return []string{f.pin}
		},
		"symlink file": func(d string, f fixture) []string {
			p := filepath.Join(d, ReadyDir, RootfsName)
			os.Rename(p, p+".x")
			os.Symlink(p+".x", p)
			return []string{f.pin}
		},
		"hard link": func(d string, f fixture) []string {
			os.Link(filepath.Join(d, ReadyDir, RootfsName), filepath.Join(d, "outside"))
			return []string{f.pin}
		},
		"size": func(d string, f fixture) []string {
			fh, _ := os.OpenFile(filepath.Join(d, ReadyDir, RootfsName), os.O_WRONLY|os.O_APPEND, 0)
			fh.Write([]byte("x"))
			fh.Close()
			return []string{f.pin}
		},
		"manifest changed": func(d string, f fixture) []string {
			b, _ := os.ReadFile(filepath.Join(d, ReadyDir, ManifestName))
			b[len(b)-1] = ' '
			os.WriteFile(filepath.Join(d, ReadyDir, ManifestName), b, 0o600)
			return []string{f.pin}
		},
		"record changed": func(d string, f fixture) []string {
			b, _ := os.ReadFile(filepath.Join(d, ReadyRecord))
			os.WriteFile(filepath.Join(d, ReadyRecord), []byte(strings.Replace(string(b), f.pin, strings.Repeat("b", 64), 1)), 0o600)
			return []string{f.pin, strings.Repeat("b", 64)}
		},
		"record extra key": func(d string, f fixture) []string {
			b, _ := os.ReadFile(filepath.Join(d, ReadyRecord))
			os.WriteFile(filepath.Join(d, ReadyRecord), []byte(strings.Replace(string(b), "{", `{"x":1,`, 1)), 0o600)
			return []string{f.pin}
		},
		"record is a link": func(d string, f fixture) []string {
			p := filepath.Join(d, ReadyRecord)
			os.Rename(p, p+".x")
			os.Symlink(p+".x", p)
			return []string{f.pin}
		},
		"folder is a link": func(d string, f fixture) []string {
			p := filepath.Join(d, ReadyDir)
			os.Rename(p, p+".x")
			os.Symlink(p+".x", p)
			return []string{f.pin}
		},
		"no record": func(d string, f fixture) []string {
			os.Remove(filepath.Join(d, ReadyRecord))
			return []string{f.pin}
		},
	} {
		t.Run(name, func(t *testing.T) {
			d, f := readyRig(t)
			pins := change(d, f)
			if _, err := CheckReady(d, -1, pins); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	d, f := readyRig(t)
	if _, err := CheckReady(d, os.Getuid()+1, []string{f.pin}); err == nil {
		t.Fatal("accepted a folder of another user")
	}
	if _, err := CheckReady(d, os.Getuid(), []string{f.pin}); err != nil {
		t.Fatal(err)
	}
}

func TestReadStateRefusesOddFiles(t *testing.T) {
	d := t.TempDir()
	if _, err := ReadState(d); err == nil {
		t.Fatal("no file")
	}
	os.WriteFile(filepath.Join(d, "x"), []byte(`{"state":"receiving"}`), 0o600)
	os.Symlink(filepath.Join(d, "x"), filepath.Join(d, StateFile))
	if _, err := ReadState(d); err == nil {
		t.Fatal("followed a link")
	}
	os.Remove(filepath.Join(d, StateFile))
	os.WriteFile(filepath.Join(d, StateFile), make([]byte, maxStateFile+1), 0o600)
	if _, err := ReadState(d); err == nil {
		t.Fatal("read a large file")
	}
	os.WriteFile(filepath.Join(d, StateFile), []byte(`{"state":"RECEIVING; rm"}`), 0o600)
	if _, err := ReadState(d); err == nil {
		t.Fatal("an odd state")
	}
	os.Remove(filepath.Join(d, StateFile))
	syscall.Mkfifo(filepath.Join(d, StateFile), 0o600)
	if _, err := ReadState(d); err == nil {
		t.Fatal("read a FIFO")
	}
}
