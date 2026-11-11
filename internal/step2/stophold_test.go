//go:build install && step2 && devtest

package step2_test

// The durable STOP hold (STEP2-STOP-HOLD-2026-11-11.md §4, items 1-5): the
// service records a finish STOP with `tsx-autoupdate stop-hold set step2
// REASON` (fixed argv, PATH, LC_ALL), never writes the restart block itself
// after its exit 0 and never sets again in that boot; on any other end it
// keeps the "hold 4" fallback and tries again until the first exit 0; it
// never clears. Quiet refuses while the hold folder has an entry or cannot
// be read (review MAJOR-1).

import (
	"compress/gzip"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/device/devicetest"
	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/step2"
	"uc-pxx-installer/internal/step2ui"
)

func holdEnv(t *testing.T) (*device.Env, string) {
	t.Helper()
	root := t.TempDir()
	e, err := device.NewForTest(device.TestOptions{Root: root, RegularFiles: true, Layout: small, Manifests: fx.Manifests})
	if err != nil {
		t.Fatal(err)
	}
	return e, root
}

// fakeTool puts a tsx-autoupdate into the root that logs its argv and
// environment and exits with the codes given, one per call (the last again).
func fakeTool(t *testing.T, root string, codes ...string) string {
	t.Helper()
	log := filepath.Join(root, "calls.log")
	n := filepath.Join(root, "n")
	p := filepath.Join(root, strings.TrimPrefix(step2.AutoupdateTool, "/"))
	os.MkdirAll(filepath.Dir(p), 0o755)
	q := func(s string) string { return "'" + s + "'" } // the test folders have spaces and parentheses
	script := "#!/bin/sh\necho \"argv[$#]: $*|PATH=$PATH|LC_ALL=$LC_ALL\" >> " + q(log) + "\n" +
		"i=$(cat " + q(n) + " 2>/dev/null || echo 0); echo $((i + 1)) > " + q(n) + "\n" +
		"set -- " + strings.Join(codes, " ") + "\nshift $i 2>/dev/null || true\n" +
		"c=$1; [ -n \"$c\" ] || c=" + codes[len(codes)-1] + "\nexit $c\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return log
}

func calls(log string) []string {
	b, _ := os.ReadFile(log)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// diskEnv is a fake unit with an eMMC (package devicetest) whose userdata,
// vendor_a and oem_a hold what tweak says; the running root is system_a and
// /proc/self/mountinfo has the extra lines mounts.
func diskEnv(t *testing.T, tweak map[string][]byte) (*device.Env, *device.Disk, string) {
	t.Helper()
	dir := t.TempDir()
	tw := map[string][]byte{"system_a": rootImg(t, "noholds")} // the running root: no hold in its own folder
	for k, v := range tweak {
		tw[k] = v
	}
	src, err := fx.Image(dir, "src.img", fakeemmc.Unit{AB: [2][3]byte{{15, 0, 1}, {14, 0, 1}}, Tweak: tw})
	if err != nil {
		t.Fatal(err)
	}
	root, err := devicetest.New(filepath.Join(dir, "root"), src, small, nil, devicetest.Opts{NoMarker: true})
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(src)
	e, err := device.NewForTest(device.TestOptions{Root: root.Dir, RegularFiles: true, Layout: small, Manifests: fx.Manifests})
	if err != nil {
		t.Fatal(err)
	}
	d, err := e.OpenEMMCSlot(device.EMMCPath)
	if err != nil {
		t.Fatal(err)
	}
	setMounts(t, root.Dir, "")
	return e, d, root.Dir
}

// rootImg is a root file system fixture of testdata/roots (mkroots.sh): an
// ext4 image whose own /var/lib/tsx/autoupdate/holds is in the state name.
func rootImg(t *testing.T, name string) []byte {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "roots", name+".img.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(z)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// setMounts writes the mountinfo of a boot of slot A with the extra lines.
func setMounts(t *testing.T, root, extra string) {
	t.Helper()
	p := filepath.Join(root, strings.TrimPrefix(device.MountInfoPath, "/"))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte("20 1 179:22 / / rw,noatime - ext4 /dev/mmcblk0p22 rw\n21 20 0:5 / /proc rw - proc proc rw\n"+extra), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ext4Super is the start of a partition with an ext4 superblock of the label.
func ext4Super(label string) []byte {
	b := make([]byte, 4096)
	b[1024+0x38], b[1024+0x39] = 0x53, 0xEF
	copy(b[1024+0x78:1024+0x88], label)
	return b
}

// lvmLabel is the start of a partition with an LVM2 physical volume label in
// sector 1 (where pvcreate puts it).
func lvmLabel() []byte {
	b := make([]byte, 4096)
	copy(b[512:], "LABELONE")
	copy(b[512+24:], "LVM2 001")
	return b
}

const (
	userdataMount = "30 20 179:28 / /data rw,noatime - ext4 /dev/mmcblk0p28 rw,commit=60\n"
	lvmMount      = "30 20 253:0 / /data rw,noatime - ext4 /dev/mapper/tsx-data rw,commit=60\n"
)

func TestHoldsClear(t *testing.T) {
	e, d, root := diskEnv(t, nil)
	h := filepath.Join(root, strings.TrimPrefix(step2.HoldsDir, "/"))
	if err := step2.HoldsClear(e, d); err == nil {
		t.Fatal("a missing hold folder with no tsx-autoupdate state folder passed (it must fail closed)")
	}
	// MAINLINE-RC-REVIEW MAJOR-1: the STOP-hold tsx-autoupdate makes holds/
	// only at the first set; its init makes /var/lib/tsx/autoupdate
	os.MkdirAll(filepath.Dir(h), 0o755)
	if err := step2.HoldsClear(e, d); err != nil {
		t.Fatalf("a unit that never had a hold (state folder, no holds/, no tsx data file system): %v", err)
	}
	if err := step2.HoldsClear(e, nil); err == nil {
		t.Fatal("no eMMC view passed")
	}
	// the two folders: the live one and tsx-data's place on /data
	for _, dir := range []string{step2.HoldsDir, step2.DataHoldsDir} {
		p := filepath.Join(root, strings.TrimPrefix(dir, "/"))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, nil, 0o644) // holds as a file
		if err := step2.HoldsClear(e, d); err == nil {
			t.Fatalf("%s: a file named holds passed", dir)
		}
		os.Remove(p)
		os.Symlink("/nonexistent", p) // a dangling link
		if err := step2.HoldsClear(e, d); err == nil {
			t.Fatalf("%s: a dangling link named holds passed", dir)
		}
		os.Remove(p)
		other := filepath.Join(root, "elsewhere")
		os.MkdirAll(other, 0o755)
		os.Symlink(other, p) // a link to an empty folder: not the folder tsx-autoupdate makes
		if err := step2.HoldsClear(e, d); err == nil {
			t.Fatalf("%s: a link named holds passed", dir)
		}
		os.Remove(p)
		os.MkdirAll(p, 0o755)
		if err := step2.HoldsClear(e, d); err != nil {
			t.Fatalf("%s: an empty hold folder: %v", dir, err)
		}
		os.WriteFile(filepath.Join(p, ".step2.123"), nil, 0o644)
		if err := step2.HoldsClear(e, d); err != nil {
			t.Fatalf("%s: a dot file only: %v", dir, err)
		}
		os.WriteFile(filepath.Join(p, "step2"), []byte("STOP\n"), 0o644)
		if err := step2.HoldsClear(e, d); err == nil || !strings.Contains(err.Error(), "step2") || !strings.Contains(err.Error(), dir) {
			t.Fatalf("%s: a step2 hold passed: %v", dir, err)
		}
		os.Remove(filepath.Join(p, "step2"))
		os.WriteFile(filepath.Join(p, "other"), nil, 0o644)
		if err := step2.HoldsClear(e, d); err == nil {
			t.Fatalf("%s: another owner's hold passed", dir)
		}
		os.Remove(filepath.Join(p, "other"))
		if os.Getuid() != 0 {
			os.Chmod(p, 0)
			if err := step2.HoldsClear(e, d); err == nil {
				t.Fatalf("%s: an unreadable hold folder passed", dir)
			}
			os.Chmod(p, 0o755)
		}
		os.RemoveAll(p)
	}
	// the live state folder a file
	os.RemoveAll(filepath.Dir(h))
	os.WriteFile(filepath.Dir(h), nil, 0o644)
	if err := step2.HoldsClear(e, d); err == nil {
		t.Fatal("a file in place of the state folder passed")
	}
	os.Remove(filepath.Dir(h))
	os.MkdirAll(filepath.Dir(h), 0o755)
	// a path of the /data place that is not a folder (/data/var a file): refuse
	os.RemoveAll(filepath.Join(root, "data"))
	os.MkdirAll(filepath.Join(root, "data"), 0o755)
	os.WriteFile(filepath.Join(root, "data/var"), nil, 0o644)
	if err := step2.HoldsClear(e, d); err == nil {
		t.Fatal("/data/var a file passed")
	}
	os.Remove(filepath.Join(root, "data/var"))
	if err := step2.HoldsClear(e, d); err != nil {
		t.Fatalf("back to a never-held unit: %v", err)
	}
	// LiveChecks' Quiet refuses with the hold before anything else
	os.MkdirAll(h, 0o755)
	os.WriteFile(filepath.Join(h, "step2"), nil, 0o644)
	if err := step2.LiveChecks(e, d).Quiet(); err == nil || !strings.Contains(err.Error(), "hold") {
		t.Fatalf("Quiet with a hold: %v", err)
	}
	// LiveChecks' Quiet on a unit that never had a hold (no holds/): past the
	// hold check (here it stops at tsx-autoupdate get mode: no tool in the root)
	os.RemoveAll(h)
	if err := step2.LiveChecks(e, d).Quiet(); err == nil || strings.Contains(err.Error(), "hold") {
		t.Fatalf("Quiet with no holds folder: %v (want past the hold check)", err)
	}
	if err := step2.LiveChecks(e, nil).Quiet(); err == nil || !strings.Contains(err.Error(), "eMMC") {
		t.Fatalf("Quiet with no eMMC view: %v", err)
	}
}

// TestHoldsDataVisible: a tsx data file system on the eMMC that is not this
// boot's /data refuses, whatever the hold folders show (MAINLINE-RC-REVIEW
// re-look MINOR-4).
func TestHoldsDataVisible(t *testing.T) {
	for _, c := range []struct {
		name   string
		tweak  map[string][]byte
		mounts string
		ok     bool
		want   string
	}{
		{"no tsx data file system, /data a folder", nil, "", true, ""},
		{"userdata Android's (ext4 label data), /data a folder", map[string][]byte{"userdata": ext4Super("data")}, "", true, ""},
		{"userdata tsxdata-new (a step 2 between format and migrate)", map[string][]byte{"userdata": ext4Super("tsxdata-new")}, "", true, ""},
		{"userdata tsxdata, mounted on /data", map[string][]byte{"userdata": ext4Super("tsxdata")}, userdataMount, true, ""},
		{"userdata tsxdata, /data NOT mounted", map[string][]byte{"userdata": ext4Super("tsxdata")}, "", false, "not mounted from it"},
		{"userdata tsxdata, /data from another device", map[string][]byte{"userdata": ext4Super("tsxdata")}, lvmMount, false, "not mounted from it"},
		{"userdata tsxdata, /data a bind of a folder of it", map[string][]byte{"userdata": ext4Super("tsxdata")},
			"30 20 179:28 /x /data rw - ext4 /dev/mmcblk0p28 rw\n", false, "not mounted from it"},
		{"userdata tsxdata, /data mounted, then a tmpfs over it", map[string][]byte{"userdata": ext4Super("tsxdata")},
			userdataMount + "31 30 0:40 / /data rw - tmpfs tmpfs rw\n", false, "not mounted from it"},
		{"LVM label on vendor_a, /data the LVM volume", map[string][]byte{"vendor_a": lvmLabel()}, lvmMount, true, ""},
		{"LVM label on oem_a, /data NOT mounted", map[string][]byte{"oem_a": lvmLabel()}, "", false, "LVM"},
		{"LVM label on vendor_b, /data NOT mounted", map[string][]byte{"vendor_b": lvmLabel()}, "", false, "LVM"},
		{"LVM label on vendor_a, /data a tmpfs", map[string][]byte{"vendor_a": lvmLabel()}, "30 20 0:40 / /data rw - tmpfs tmpfs rw\n", false, "LVM"},
		{"LVM on vendor_a and userdata tsxdata mounted (phase 2)", map[string][]byte{"vendor_a": lvmLabel(), "userdata": ext4Super("tsxdata")}, userdataMount, true, ""},
		{"LVM on vendor_a, userdata tsxdata, /data the old LVM volume", map[string][]byte{"vendor_a": lvmLabel(), "userdata": ext4Super("tsxdata")}, lvmMount, false, "not mounted from it"},
		{"a /data line that cannot be read", nil, "30 20 bad / /data rw - ext4 x rw\n", false, "cannot be read"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, d, root := diskEnv(t, c.tweak)
			os.MkdirAll(filepath.Join(root, strings.TrimPrefix(filepath.Dir(step2.HoldsDir), "/")), 0o755)
			setMounts(t, root, c.mounts)
			err := step2.HoldsClear(e, d)
			if c.ok && err != nil {
				t.Fatalf("refused: %v", err)
			}
			if !c.ok && (err == nil || !strings.Contains(err.Error(), c.want)) {
				t.Fatalf("got %v, want a refusal with %q", err, c.want)
			}
		})
	}
	t.Run("an unreadable mountinfo", func(t *testing.T) {
		e, d, root := diskEnv(t, nil)
		os.MkdirAll(filepath.Join(root, strings.TrimPrefix(filepath.Dir(step2.HoldsDir), "/")), 0o755)
		os.Remove(filepath.Join(root, strings.TrimPrefix(device.MountInfoPath, "/")))
		if err := step2.HoldsClear(e, d); err == nil {
			t.Fatal("passed")
		}
	})
}

// realAutoupdate puts the real STOP-hold tsx-autoupdate (40c91350,
// tsx-linux-common 7aafeb2, from UC_PXX_TSX_COMMON; skipped without it) at
// AutoupdateTool in root: a wrapper that maps its folders into root (its test
// hooks) and runs the real script with the real conf.sh and defaults; MODE
// manual. It returns the tool's path.
func realAutoupdate(t testing.TB, root string) string {
	t.Helper()
	common := os.Getenv("UC_PXX_TSX_COMMON")
	if common == "" {
		t.Skip("UC_PXX_TSX_COMMON not set: this test runs the real tsx-autoupdate of tsx-linux-common")
	}
	pin := map[string]string{
		"autoupdate/usr/local/sbin/tsx-autoupdate":                "40c913500d29e26601fbb6d14ac9feb2b8b200b9b3b2a2c2d5d69c42180391ab",
		"base/usr/local/lib/tsx/conf.sh":                          "22f631e199a6232b9e9f54f6ac35c42f1ded260e9121b0c708e4deef66785692",
		"autoupdate/usr/local/share/tsx/defaults/autoupdate.conf": "24b59e0f82833de669cd1edea5aebf8e04611473a41838e4a3638cdeafc84757",
	}
	for f, want := range pin {
		b, err := os.ReadFile(filepath.Join(common, f))
		if err != nil {
			t.Fatal(err)
		}
		if got := devicetest.SHA256(b); got != want {
			t.Fatalf("%s: sha256 %s, not the reviewed %s", f, got, want)
		}
	}
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	at := func(p string) string { return filepath.Join(root, strings.TrimPrefix(p, "/")) }
	for _, f := range []struct{ p, v string }{
		{"/etc/tsx/autoupdate.conf", "MODE=manual\n"},
		{"/usr/local/lib/tsx/board.sh", "TSX_FAMILY=uc-pxx\nTSX_APK_CATEGORY=uc-pxx\n"},
		{"/proc/sys/kernel/random/boot_id", "0f0e0d0c-0b0a-0908-0706-050403020100\n"},
		{"/etc/tsx/build-id", "dev-test\n"},
	} {
		os.MkdirAll(filepath.Dir(at(f.p)), 0o755)
		os.WriteFile(at(f.p), []byte(f.v), 0o644)
	}
	// the wrapper maps the tool's folders into the root (its test hooks) and
	// runs the real script; argv, PATH and LC_ALL are LiveChecks' own
	env := map[string]string{"TSX_BOARD_CONF": "/usr/local/lib/tsx/board.sh", "TSX_AUTOUPDATE_CONF": "/etc/tsx/autoupdate.conf",
		"TSX_RUN_DIR": "/run/tsx", "TSX_STATE_DIR": "/var/lib/tsx/autoupdate", "TSX_LOG": "/var/log/tsx-autoupdate.log",
		"TSX_BOOT_ID_FILE": "/proc/sys/kernel/random/boot_id", "TSX_BUILD_ID_FILE": "/etc/tsx/build-id", "TSX_INITD": "/etc/init.d",
		"TSX_RUNLEVELS": "/etc/runlevels", "TSX_IDLED_STATE": "/run/tsx-idled.state", "TSX_KIOSK_CONF": "/etc/kiosk.conf",
		"TSX_PANEL_APP_STATE": "/run/tsx/panel-app.state", "TSX_LEDBAR_FW_PENDING": "/data/tsx/ledbar-fw.pending",
		"TSX_BACKLIGHT_CLASS": "/sys/class/backlight"}
	script := "#!/bin/sh\n"
	for k, v := range env {
		script += "export " + k + "=" + q(at(v)) + "\n"
	}
	script += "export TSX_CONF_SH=" + q(filepath.Join(common, "base/usr/local/lib/tsx/conf.sh")) + "\n"
	script += "export TSX_DEFAULTS_DIR=" + q(filepath.Join(common, "autoupdate/usr/local/share/tsx/defaults")) + "\n"
	script += "exec /bin/sh " + q(filepath.Join(common, "autoupdate/usr/local/sbin/tsx-autoupdate")) + " \"$@\"\n"
	tool := at(step2.AutoupdateTool)
	os.MkdirAll(filepath.Dir(tool), 0o755)
	if err := os.WriteFile(tool, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return tool
}

// TestHoldsRealAutoupdate: no stub. The real STOP-hold tsx-autoupdate
// (40c91350, tsx-linux-common 7aafeb2) with its real conf.sh (22f631e1) and
// defaults (24b59e0f) runs `stop-hold set|clear` and `get mode` for
// LiveChecks' real SetStopHold and Quiet, its folders inside the test root.
// The MINOR-4 case: a STOP set while /var/lib/tsx is tsx-data's bind of
// /data/var/lib/tsx; in the next boot /data does not mount (userdata still
// holds the tsxdata file system), tsx-data leaves the empty /var/lib/tsx of
// the root file system and the tsx-autoupdate init makes autoupdate/:
// Quiet must refuse. Run with UC_PXX_TSX_COMMON = a checkout of
// tsx-linux-common 7aafeb2 (skipped without it).
func TestHoldsRealAutoupdate(t *testing.T) {
	e, d, root := diskEnv(t, map[string][]byte{"userdata": ext4Super("tsxdata")})
	tool := realAutoupdate(t, root)
	at := func(p string) string { return filepath.Join(root, strings.TrimPrefix(p, "/")) }
	live := step2.LiveChecks(e, d)
	varTsx, dataTsx := at("/var/lib/tsx"), at("/data/var/lib/tsx")
	// a boot with /data mounted from userdata: tsx-data bound
	// /data/var/lib/tsx onto /var/lib/tsx (here: a link, the same folder)
	bootWithData := func() {
		os.RemoveAll(varTsx)
		os.MkdirAll(filepath.Join(dataTsx, "autoupdate"), 0o755) // the tsx-autoupdate init: checkpath
		os.MkdirAll(filepath.Dir(varTsx), 0o755)
		if err := os.Symlink(dataTsx, varTsx); err != nil {
			t.Fatal(err)
		}
		os.RemoveAll(at("/run"))
		setMounts(t, root, userdataMount)
	}
	// a boot where /data did not mount: /data is the empty folder of the
	// root file system, /var/lib/tsx the root's (tsx-data emptied it when it
	// moved it), the tsx-autoupdate init made autoupdate/ there
	hidden := at("/data-unmounted")
	bootNoData := func() {
		os.RemoveAll(varTsx)
		os.RemoveAll(hidden)
		if err := os.Rename(at("/data"), hidden); err != nil {
			t.Fatal(err)
		}
		os.MkdirAll(at("/data"), 0o755)
		os.MkdirAll(filepath.Join(varTsx, "autoupdate"), 0o755)
		os.RemoveAll(at("/run"))
		setMounts(t, root, "")
	}
	remount := func() {
		os.RemoveAll(at("/data"))
		if err := os.Rename(hidden, at("/data")); err != nil {
			t.Fatal(err)
		}
	}
	// (1) a unit that never had a hold, /data mounted: Quiet ok (the real get mode: manual)
	bootWithData()
	if err := live.Quiet(); err != nil {
		t.Fatalf("(1) never held, /data mounted: %v", err)
	}
	// (2) the real stop-hold set step2: exit 0, the file on /data; Quiet refuses
	if rc := live.SetStopHold("STOP after step 2: test"); rc != 0 {
		t.Fatalf("(2) stop-hold set: exit %d", rc)
	}
	if _, err := os.Stat(filepath.Join(dataTsx, "autoupdate/holds/step2")); err != nil {
		t.Fatalf("(2) the hold is not on /data: %v", err)
	}
	if err := live.Quiet(); err == nil || !strings.Contains(err.Error(), "step2") {
		t.Fatalf("(2) Quiet with the hold: %v", err)
	}
	// (3) a new boot with /data mounted again: still refused (durable)
	bootWithData()
	if err := live.Quiet(); err == nil || !strings.Contains(err.Error(), "step2") {
		t.Fatalf("(3) Quiet after a restart: %v", err)
	}
	// (4) MINOR-4: a boot where /data did not mount: no holds/ in the live
	// folder, the state folder there; the old rule passed here
	bootNoData()
	if _, err := os.Lstat(at(step2.HoldsDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("(4) setup: holds/ is visible: %v", err)
	}
	if err := live.Quiet(); err == nil || !strings.Contains(err.Error(), "not mounted from it") {
		t.Fatalf("(4) MINOR-4: Quiet with the hold on an unmounted /data: %v", err)
	}
	// the tool itself does not see the hold in this boot (its own design)
	cmd := exec.Command(tool, "stop-hold", "list")
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "no STOP hold") {
		t.Fatalf("(4) stop-hold list on the root's folder: %v %q", err, out)
	}
	// (5) /data mounted, but tsx-data's bind of /var/lib/tsx missing: the
	// live folder is the root's (empty), the hold is at the /data place
	remount()
	setMounts(t, root, userdataMount)
	if err := live.Quiet(); err == nil || !strings.Contains(err.Error(), step2.DataHoldsDir) {
		t.Fatalf("(5) Quiet with the hold at the /data place and no bind: %v", err)
	}
	// (6) the bind again, then the real clear: Quiet ok
	bootWithData()
	cmd = exec.Command(tool, "stop-hold", "clear", "step2")
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("(6) stop-hold clear: %v %q", err, out)
	}
	bootWithData() // a new boot: /run is new (the restart block of the cleared STOP is gone)
	if err := live.Quiet(); err != nil {
		t.Fatalf("(6) Quiet after the clear: %v", err)
	}
	// (7) the cleared unit, /data not mounted: still refused (the file
	// system is there, the boot is not right), whatever the folders show
	bootNoData()
	if err := live.Quiet(); err == nil || !strings.Contains(err.Error(), "not mounted from it") {
		t.Fatalf("(7) Quiet with /data not mounted: %v", err)
	}
}

func block(root string) string {
	b, err := os.ReadFile(filepath.Join(root, strings.TrimPrefix(step2.RestartBlock, "/")))
	if err != nil {
		return "none"
	}
	return strings.TrimSpace(string(b))
}

type holdPanel struct{ lines [][]string }

func (p *holdPanel) Show(l []string)          { p.lines = append(p.lines, l) }
func (p *holdPanel) Keys() <-chan step2ui.Key { return nil }
func (p *holdPanel) Close(bool)               {}

func TestStopHoldDurable(t *testing.T) {
	stop := errors.New("the check after step 2: LBA 1024 changed\nsecond line")
	run := func(t *testing.T, codes []string, turns int) (string, *holdPanel, string, []string) {
		e, root := holdEnv(t)
		log := fakeTool(t, root, codes...)
		live := step2.LiveChecks(e, nil)
		live.LockAutoupdate = func() (func(), error) { return func() {}, nil }
		pn := &holdPanel{}
		n := 0
		se := step2.ServiceEnv{Panel: func() (step2.Panel, error) { return pn, nil },
			Turn: func() bool { n++; return n <= turns }}
		if _, err := step2.StopHoldForTest(e, step2.Options{Live: live}, se, stop); err != stop {
			t.Fatalf("the STOP's error %v", err)
		}
		return root, pn, block(root), calls(log)
	}
	t.Run("exit 0: recorded, no block of its own, set once", func(t *testing.T) {
		root, pn, b, c := run(t, []string{"0"}, 5)
		if len(c) != 1 || c[0] != "argv[4]: stop-hold set step2 STOP after step 2: the check after step 2: LBA 1024 changed|PATH=/usr/sbin:/usr/bin:/sbin:/bin|LC_ALL=C" {
			t.Fatalf("calls %q", c)
		}
		if b != "none" {
			t.Fatalf("step 2 wrote the restart block %q after exit 0", b)
		}
		if len(pn.lines) != 1 || strings.Contains(strings.Join(pn.lines[0], " "), "could not be recorded") {
			t.Fatalf("panel %q", pn.lines)
		}
		if _, err := os.Stat(filepath.Join(root, strings.TrimPrefix(step2.StopHoldSetMarker, "/"))); err != nil {
			t.Fatal("no marker of the set in this boot")
		}
	})
	t.Run("exit 2 (an older tsx-autoupdate): the fallback, set again until exit 0", func(t *testing.T) {
		_, pn, b, c := run(t, []string{"2", "1", "0"}, 6)
		if len(c) != 3 {
			t.Fatalf("calls %q (want 3: 2, 1, then 0 and no more)", c)
		}
		if b != "hold 4" {
			t.Fatalf("restart block %q", b)
		}
		if len(pn.lines) != 2 || !strings.Contains(strings.Join(pn.lines[0], " "), "could not be recorded") ||
			strings.Contains(strings.Join(pn.lines[1], " "), "could not be recorded") {
			t.Fatalf("panel %q", pn.lines)
		}
	})
	t.Run("never recorded: the block every turn, set every turn", func(t *testing.T) {
		_, _, b, c := run(t, []string{"2"}, 4)
		if len(c) != 5 || b != "hold 4" {
			t.Fatalf("calls %d, block %q", len(c), b)
		}
	})
	t.Run("set once in this boot already: never again", func(t *testing.T) {
		e, root := holdEnv(t)
		log := fakeTool(t, root, "0")
		m := filepath.Join(root, strings.TrimPrefix(step2.StopHoldSetMarker, "/"))
		os.MkdirAll(filepath.Dir(m), 0o755)
		os.WriteFile(m, []byte("set\n"), 0o644)
		live := step2.LiveChecks(e, nil)
		live.LockAutoupdate = func() (func(), error) { return func() {}, nil }
		step2.StopHoldForTest(e, step2.Options{Live: live}, step2.ServiceEnv{Turn: func() bool { return false }}, stop)
		if _, err := os.Stat(log); err == nil {
			t.Fatalf("set ran again: %q", calls(log))
		}
	})
	t.Run("never clear", func(t *testing.T) {
		_, _, _, c := run(t, []string{"2", "0"}, 5)
		for _, l := range c {
			if strings.Contains(l, "clear") {
				t.Fatalf("a clear: %q", l)
			}
		}
	})
}
