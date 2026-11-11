package xfer

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func isTmpfs(p string) bool {
	var sf syscall.Statfs_t
	return syscall.Statfs(p, &sf) == nil && int64(sf.Type) == tmpfsMagic
}

// CheckStage refuses a stage that is not a tmpfs, and a tmpfs folder that is
// not root's (review F11). The happy path needs root (the guest test).
func TestCheckStageRefusals(t *testing.T) {
	id := Ident{UID: os.Getuid() + 1000, GID: os.Getgid() + 1000}
	d := t.TempDir()
	if !isTmpfs(d) {
		if _, err := CheckStage(d, id); err == nil || !strings.Contains(err.Error(), "not a tmpfs") {
			t.Fatalf("a stage on a disk: %v", err)
		}
	}
	if os.Getuid() == 0 || !isTmpfs("/dev/shm") {
		t.Skip("needs a non-root user and /dev/shm on a tmpfs")
	}
	s, err := os.MkdirTemp("/dev/shm", "stage-test-")
	if err != nil {
		t.Skip(err)
	}
	defer os.RemoveAll(s)
	if _, err := CheckStage(s, id); err == nil || !strings.Contains(err.Error(), "a folder of root") {
		t.Fatalf("a tmpfs folder of another user: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s, XferDir)); err == nil {
		t.Fatal("the refused stage got a receiver folder")
	}
}

func TestSwapOff(t *testing.T) {
	defer func(s string) { SwapsFile = s }(SwapsFile)
	sw := filepath.Join(t.TempDir(), "swaps")
	SwapsFile = sw
	os.WriteFile(sw, []byte("Filename\t\t\t\tType\t\tSize\t\tUsed\t\tPriority\n"), 0o644)
	if err := swapOff(); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(sw, []byte("Filename Type Size Used Priority\n/dev/mmcblk0p9 partition 1024 0 -2\n"), 0o644)
	if err := swapOff(); err == nil || !strings.Contains(err.Error(), "/dev/mmcblk0p9") {
		t.Fatalf("swap on: %v", err)
	}
	os.Remove(sw)
	if err := swapOff(); err == nil {
		t.Fatal("no swaps file was taken")
	}
}
