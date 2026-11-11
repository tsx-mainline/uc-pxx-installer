package rebootmode

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func cell(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

// fakeRoot is a status RAM image on a UC-P10 as the release image sees it:
// the marker, the command line of image/build.sh, the live device tree of
// rk3399-crestron-uc-pxx.dtsi and the bound driver.
func fakeRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	w := func(rel string, b []byte) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w(RAMMarker, []byte(RAMMagic+"\n# This file marks the installer RAM image.\n"))
	w("proc/cmdline", []byte("earlycon=uart8250,mmio32,0xff1a0000 console=ttyS2,115200n8 uc-pxx-inst.ram=1 uc-pxx.id=p10inst-e13d74\n"))
	w("proc/mounts", []byte("rootfs / rootfs rw 0 0\nproc /proc proc rw 0 0\nrun /run tmpfs rw,nosuid,nodev 0 0\n"))
	dt := "proc/device-tree/"
	w(dt+"compatible", []byte("crestron,uc-p10\x00rockchip,rk3399\x00"))
	w(dt+"model", []byte("Crestron UC-P10\x00"))
	w(dt+"syscon@ff320000/compatible", []byte("rockchip,rk3399-pmugrf\x00syscon\x00simple-mfd\x00"))
	n := dt + "syscon@ff320000/reboot-mode/"
	w(n+"compatible", []byte("syscon-reboot-mode\x00"))
	w(n+"offset", cell(0x300))
	w(n+"mode-normal", cell(0x5242c300))
	w(n+"mode-recovery", cell(0x5242c303))
	w(n+"mode-bootloader", cell(0x5242c301))
	w(n+"mode-loader", cell(0x5242c301))
	w(n+"mode-fastboot", cell(0x5242c309))
	dev := filepath.Join(root, "sys/devices/platform/ff320000.syscon/ff320000.syscon:reboot-mode")
	os.MkdirAll(dev, 0o755)
	os.Symlink("../../../../firmware/devicetree/base/syscon@ff320000/reboot-mode", filepath.Join(dev, "of_node"))
	drv := filepath.Join(root, driverDir)
	os.MkdirAll(drv, 0o755)
	os.WriteFile(filepath.Join(drv, "bind"), nil, 0o200)
	os.Symlink("../../../../devices/platform/ff320000.syscon/ff320000.syscon:reboot-mode", filepath.Join(drv, "ff320000.syscon:reboot-mode"))
	return root
}

func TestProbeOK(t *testing.T) {
	for _, model := range []string{"Crestron UC-P10", "Crestron UC-P8"} {
		root := fakeRoot(t)
		os.WriteFile(filepath.Join(root, "proc/device-tree/model"), []byte(model+"\x00"), 0o644)
		r := Probe(root)
		if !r.OK || r.Why != "" {
			t.Fatalf("%s: refused: %s\n%s", model, r.Why, strings.Join(r.Lines, "\n"))
		}
		if r.Model != model || r.Token != "p10inst-e13d74" || len(r.Lines) != 4 {
			t.Errorf("%s: %+v", model, r)
		}
		if !strings.Contains(strings.Join(r.Lines, "\n"), "mode-loader = 0x5242c301") {
			t.Errorf("no magic line: %v", r.Lines)
		}
	}
}

// Each change that would make the restart do something else, or run where
// it must not, is refused, and nothing restarts.
func TestProbeRefuses(t *testing.T) {
	n := "proc/device-tree/syscon@ff320000/reboot-mode/"
	cases := []struct {
		name, want string
		change     func(root string)
	}{
		{"no marker", "no marker file", func(r string) { os.Remove(filepath.Join(r, RAMMarker)) }},
		{"another marker", "no marker file", func(r string) { os.WriteFile(filepath.Join(r, RAMMarker), []byte("x\n"), 0o644) }},
		{"mainline command line", "uc-pxx.id=ucpxx-", func(r string) {
			os.WriteFile(filepath.Join(r, "proc/cmdline"), []byte("root=/dev/mmcblk0p20 uc-pxx-inst.ram=1 uc-pxx.id=ucpxx-003ba796\n"), 0o644)
		}},
		{"no uc-pxx-inst.ram=1", "no uc-pxx-inst.ram=1", func(r string) {
			os.WriteFile(filepath.Join(r, "proc/cmdline"), []byte("uc-pxx.id=p10inst-e13d74\n"), 0o644)
		}},
		{"two tokens", "not exactly one token", func(r string) {
			os.WriteFile(filepath.Join(r, "proc/cmdline"), []byte("uc-pxx-inst.ram=1 uc-pxx.id=p10inst-e13d74 uc-pxx.id=p10inst-000000\n"), 0o644)
		}},
		{"an eMMC file system mounted", "an eMMC file system is mounted", func(r string) {
			os.WriteFile(filepath.Join(r, "proc/mounts"), []byte("rootfs / rootfs rw 0 0\n/dev/mmcblk0p20 /mnt ext4 ro 0 0\n"), 0o644)
		}},
		{"not an RK3399", "not a UC-P10 or UC-P8", func(r string) {
			os.WriteFile(filepath.Join(r, "proc/device-tree/compatible"), []byte("crestron,uc-p10\x00"), 0o644)
		}},
		{"another model", "not a UC-P10 or UC-P8", func(r string) {
			os.WriteFile(filepath.Join(r, "proc/device-tree/model"), []byte("Pine64 RockPro64\x00"), 0o644)
		}},
		{"no PMU GRF", "no PMU GRF", func(r string) { os.Remove(filepath.Join(r, "proc/device-tree/syscon@ff320000/compatible")) }},
		{"no reboot-mode node", "no reboot-mode node", func(r string) { os.Remove(filepath.Join(r, n+"compatible")) }},
		{"node disabled", "status \"disabled\"", func(r string) { os.WriteFile(filepath.Join(r, n+"status"), []byte("disabled\x00"), 0o644) }},
		{"another offset", "offset 0x304", func(r string) { os.WriteFile(filepath.Join(r, n+"offset"), cell(0x304), 0o644) }},
		{"a mask", "has a mask", func(r string) { os.WriteFile(filepath.Join(r, n+"mask"), cell(0xff), 0o644) }},
		{"no mode-loader", "mode-loader", func(r string) { os.Remove(filepath.Join(r, n+"mode-loader")) }},
		{"mode-loader is fastboot", "0x5242c309", func(r string) { os.WriteFile(filepath.Join(r, n+"mode-loader"), cell(0x5242c309), 0o644) }},
		{"mode-loader is the BootROM magic", "0xef08a53c", func(r string) { os.WriteFile(filepath.Join(r, n+"mode-loader"), cell(0xef08a53c), 0o644) }},
		{"mode-loader of two cells", "not one cell", func(r string) {
			os.WriteFile(filepath.Join(r, n+"mode-loader"), append(cell(0x5242c301), cell(0)...), 0o644)
		}},
		{"driver not bound", "not bound", func(r string) {
			os.Remove(filepath.Join(r, driverDir, "ff320000.syscon:reboot-mode"))
		}},
		{"driver bound to another node", "not bound", func(r string) {
			l := filepath.Join(r, "sys/devices/platform/ff320000.syscon/ff320000.syscon:reboot-mode/of_node")
			os.Remove(l)
			os.Symlink("../../../../firmware/devicetree/base/syscon@ff770000/reboot-mode", l)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := fakeRoot(t)
			c.change(root)
			r := Probe(root)
			if r.OK || !strings.Contains(r.Why, c.want) {
				t.Fatalf("OK %v, why %q, want %q", r.OK, r.Why, c.want)
			}
			if !strings.HasPrefix(r.Lines[len(r.Lines)-1], "refused: ") {
				t.Errorf("last line %q", r.Lines[len(r.Lines)-1])
			}
			calls := 0
			old := restart2
			restart2 = func(string) error { calls++; return nil }
			defer func() { restart2 = old }()
			if err := Restart(root); err == nil || calls != 0 {
				t.Errorf("Restart: %v, %d calls", err, calls)
			}
		})
	}
}

func TestRestartPassesOnlyLoader(t *testing.T) {
	root := fakeRoot(t)
	var got []string
	old := restart2
	restart2 = func(a string) error { got = append(got, a); return os.ErrPermission }
	defer func() { restart2 = old }()
	if err := Restart(root); err != os.ErrPermission {
		t.Fatalf("the error of the system call is not returned: %v", err)
	}
	if len(got) != 1 || got[0] != "loader" || Arg != "loader" {
		t.Fatalf("calls %q", got)
	}
}

// The source has exactly one system call that is not a read: the reboot(2)
// of restart2, with RESTART2 and the constant argument. go/types (ci/goallow)
// allows the package syscall.Syscall6 by name only; this test holds the
// arguments.
func TestOneRebootCall(t *testing.T) {
	src, err := os.ReadFile("rebootmode.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	if n := strings.Count(s, "syscall.Syscall"); n != 1 {
		t.Fatalf("%d syscall.Syscall* calls, want 1", n)
	}
	re := regexp.MustCompile(`syscall\.Syscall6\(syscall\.SYS_REBOOT, syscall\.LINUX_REBOOT_MAGIC1, syscall\.LINUX_REBOOT_MAGIC2,\s+syscall\.LINUX_REBOOT_CMD_RESTART2, uintptr\(unsafe\.Pointer\(p\)\), 0, 0\)`)
	if !re.MatchString(s) {
		t.Fatal("the reboot call is not reboot(2) RESTART2 with the argument")
	}
	if strings.Count(s, "restart2(") != 1 || !strings.Contains(s, "return restart2(Arg)") {
		t.Fatal("restart2 is called other than with Arg in Restart")
	}
	for _, bad := range []string{"RawSyscall", "LINUX_REBOOT_CMD_RESTART)", "CMD_POWER_OFF", "CMD_HALT", "CMD_KEXEC", "os.OpenFile", "os.Create", "WriteFile"} {
		if strings.Contains(s, bad) {
			t.Errorf("the source has %s", bad)
		}
	}
}
