package xfer

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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

// zram swap keeps its pages in RAM: it does not block a stage, unless it has a
// backing device (writeback) or the node is not the zram device (review NIT-1).
func TestSwapOffZram(t *testing.T) {
	defer func(s, b string, l func(string, *syscall.Stat_t) error) { SwapsFile, SysBlock, lstatNode = s, b, l }(SwapsFile, SysBlock, lstatNode)
	d := t.TempDir()
	SwapsFile, SysBlock = filepath.Join(d, "swaps"), filepath.Join(d, "block")
	os.MkdirAll(filepath.Join(SysBlock, "zram0"), 0o755)
	os.WriteFile(filepath.Join(SysBlock, "zram0", "dev"), []byte("252:0\n"), 0o644)
	node := map[string]syscall.Stat_t{
		"/n/zram0":  {Mode: syscall.S_IFBLK | 0o600, Rdev: 252<<8 | 0},
		"/n/zram1":  {Mode: syscall.S_IFBLK | 0o600, Rdev: 252<<8 | 1},
		"/x/zram0":  {Mode: syscall.S_IFREG | 0o600},
		"/n/mmc":    {Mode: syscall.S_IFBLK | 0o600, Rdev: 179<<8 | 9},
		"/y/zram0":  {Mode: syscall.S_IFBLK | 0o600, Rdev: 179<<8 | 9},
		"/n/zram00": {Mode: syscall.S_IFBLK | 0o600, Rdev: 252<<8 | 0},
	}
	lstatNode = func(p string, st *syscall.Stat_t) error {
		v, ok := node[p]
		if !ok {
			return os.ErrNotExist
		}
		*st = v
		return nil
	}
	hdr := "Filename Type Size Used Priority\n"
	try := func(lines string) error {
		os.WriteFile(SwapsFile, []byte(hdr+lines), 0o644)
		return swapOff()
	}
	if err := try("/n/zram0 partition 1048572 0 100\n"); err != nil {
		t.Fatalf("zram without backing_dev (no writeback in the kernel): %v", err)
	}
	os.WriteFile(filepath.Join(SysBlock, "zram0", "backing_dev"), []byte("none\n"), 0o644)
	if err := try("/n/zram0 partition 1048572 0 100\n"); err != nil {
		t.Fatalf("zram with backing_dev none: %v", err)
	}
	for name, l := range map[string]string{
		"a zram with writeback":        "/n/zram0 partition 1048572 0 100\n",
		"a zram and a disk partition":  "/n/zram0 partition 1048572 0 100\n/n/mmc partition 1024 0 -2\n",
		"a zram that sysfs lacks":      "/n/zram1 partition 1048572 0 100\n",
		"a file named zram0":           "/x/zram0 file 1048572 0 100\n",
		"a regular file typed as zram": "/x/zram0 partition 1048572 0 100\n",
		"another device named zram0":   "/y/zram0 partition 1048572 0 100\n",
		"a name that is not zramN":     "/n/zram00 partition 1048572 0 100\n",
		"a missing node":               "/q/zram0 partition 1048572 0 100\n",
		"a line without a type":        "/n/zram0\n",
	} {
		if name == "a zram with writeback" {
			os.WriteFile(filepath.Join(SysBlock, "zram0", "backing_dev"), []byte("/dev/mmcblk0p9\n"), 0o644)
		} else {
			os.WriteFile(filepath.Join(SysBlock, "zram0", "backing_dev"), []byte("none\n"), 0o644)
		}
		if err := try(l); err == nil {
			t.Errorf("%s: taken", name)
		}
	}
}

// The USB network address: ip(8) by its full path; an error that repeats every
// second is logged once (review NIT-4); a success logs once and records the
// interface instance, so the next tick runs nothing.
func TestNCMAddressLogsOnce(t *testing.T) {
	defer func(s string) { IPCmd = s }(IPCmd)
	d := t.TempDir()
	cfs, sys := filepath.Join(d, "cfs"), filepath.Join(d, "sys")
	os.MkdirAll(filepath.Join(cfs, "functions", "ncm."+NCMInstance), 0o755)
	os.WriteFile(filepath.Join(cfs, "functions", "ncm."+NCMInstance, "ifname"), []byte("usb0\n"), 0o644)
	os.MkdirAll(filepath.Join(sys, "sys/class/net/usb0"), 0o755)
	os.WriteFile(filepath.Join(sys, "sys/class/net/usb0/ifindex"), []byte("7\n"), 0o644)
	os.MkdirAll(filepath.Join(sys, "proc/sys/net/ipv6/conf/usb0"), 0o755)
	calls := filepath.Join(d, "calls")
	ip := filepath.Join(d, "ip")
	script := func(rc int) {
		os.WriteFile(ip, []byte("#!/bin/sh\necho \"$@\" >> "+calls+"\necho 'RTNETLINK answers: no'\nexit "+strconv.Itoa(rc)+"\n"), 0o755)
	}
	IPCmd = ip
	var logs []string
	logf := func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	var st ncmState
	script(2)
	for i := 0; i < 5; i++ {
		st.address(cfs, sys, logf)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], ip+" addr add") || st.done != "" {
		t.Fatalf("5 failing ticks: %q, done %q", logs, st.done)
	}
	if b, _ := os.ReadFile(filepath.Join(sys, "proc/sys/net/ipv6/conf/usb0/disable_ipv6")); string(b) != "1\n" {
		t.Fatalf("disable_ipv6 %q", b)
	}
	script(0)
	st.address(cfs, sys, logf)
	st.address(cfs, sys, logf)
	if len(logs) != 2 || !strings.Contains(logs[1], "usb0 has "+NCMUnitIP) || st.done != "usb0#7" || st.lastErr != "" {
		t.Fatalf("success: %q, %+v", logs, st)
	}
	b, _ := os.ReadFile(calls)
	if n := strings.Count(string(b), "\n"); n != 7 { // 5 failed adds, then one add and one link set
		t.Fatalf("ip calls %d:\n%s", n, b)
	}
	// a new interface (the gadget made again) gets the address again
	os.WriteFile(filepath.Join(sys, "sys/class/net/usb0/ifindex"), []byte("9\n"), 0o644)
	st.address(cfs, sys, logf)
	if st.done != "usb0#9" || len(logs) != 3 {
		t.Fatalf("new interface: %q, %+v", logs, st)
	}
}
