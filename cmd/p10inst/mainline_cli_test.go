//go:build install && devtest

package main

// The mainline commands of mainline_install.go through the devtest binary of
// flow_test.go (testBin): the dispatch of plan and install, the official
// commands untouched by it, and the device mode on a fake unit
// (--test-root --test-fake-unit: the fixture's manifests, the real
// uc-pxx-bootctl-inst r2) with the bundle in a folder and on the drive.

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/device/devicetest"
	"uc-pxx-installer/internal/drivestate"
	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/fatfs/fattest"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/mainline/mltest"
	"uc-pxx-installer/internal/plan"
)

// TestDispatchRegistered: plan and install are the dispatchers (the init of
// mainline_install.go runs after the one of install.go).
func TestDispatchRegistered(t *testing.T) {
	for n, f := range map[string]func([]string){"plan": cmdPlanDispatch, "install": cmdInstallDispatch} {
		if reflect.ValueOf(extraCmds[n]).Pointer() != reflect.ValueOf(f).Pointer() {
			t.Fatalf("extraCmds[%q] is not the dispatcher", n)
		}
	}
	if !strings.Contains(extraUsage, "install build, mainline") || !strings.Contains(extraUsage, "install build, dry run") {
		t.Fatal("the usage lacks a part")
	}
}

func TestFlagValue(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--fw-drive", "/run/d.img"}, "/run/d.img"},
		{[]string{"-fw-drive=/run/d.img"}, "/run/d.img"},
		{[]string{"--fw-drive", "a", "--fw-drive", "b"}, "b"},
		{[]string{"--fw-drive"}, ""},
		{[]string{"--", "--fw-drive", "x"}, ""},
		{[]string{"--fw-drive-expect", "x"}, ""},
		{[]string{"--device", "/dev/mmcblk0"}, ""},
	} {
		if got := flagValue(c.args, "fw-drive"); got != c.want {
			t.Fatalf("%q: %q, want %q", c.args, got, c.want)
		}
	}
}

// mlBundleFor is a made-up bundle for the fixture (its chain in slot B).
func mlBundleFor(t *testing.T) (*mltest.Bundle, []byte) {
	t.Helper()
	res, err := mainline.ReadResource(bytes.NewReader(fixture.New["boot"]), 0, int64(len(fixture.New["boot"])))
	if err != nil {
		t.Fatal(err)
	}
	m := fixture.Manifests[0]
	var cap int64
	for _, d := range m.AVB.Descriptors {
		if d.Partition == "boot" {
			cap = int64(d.ImageSize)
		}
	}
	ch := mltest.Chain{Version: m.Firmware.Version, BuildType: m.Build["ro.build.type"], UBoot: fixture.ManifestPartSHA("uboot"),
		Trust: fixture.ManifestPartSHA("trust"), Dtbo: fixture.ManifestPartSHA("dtbo"), Vbmeta: fixture.ManifestPartSHA("vbmeta"),
		Resource: mltest.SHA(res)}
	return mltest.NewBundle("mltest-cli", 30000, 1<<20, ch, res, cap), res
}

func mlDriveFile(t *testing.T, path string, b *mltest.Bundle, bin bool) {
	t.Helper()
	im := fattest.New(fattest.Spec{Type: 16, Sectors: 2*(len(b.Rootfs)+len(b.Template))/512 + 40000, SecPerClus: 4, Label: "P10-INSTALL"})
	d := im.Root.Mkdir(mainline.Dir)
	d.File(mainline.ManifestFile, b.ManifestJSON())
	d.File(mainline.TemplateFile, b.Template)
	d.File(mainline.RootfsFile, b.Rootfs)
	if bin {
		im.Root.File("fw.bin", []byte("x"))
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, im.B, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMainlineSelected(t *testing.T) {
	dir := t.TempDir()
	b, _ := mlBundleFor(t)
	ml := filepath.Join(dir, "ml.img")
	mlDriveFile(t, ml, b, false)
	fw := filepath.Join(dir, "fw.img")
	im := fattest.New(fattest.Spec{Type: 16, Sectors: 40000, SecPerClus: 4})
	im.Root.File("fw.bin", []byte("x"))
	os.WriteFile(fw, im.B, 0o644)
	junk := filepath.Join(dir, "junk.img")
	os.WriteFile(junk, []byte("not a FAT"), 0o644)
	mlPlan := filepath.Join(dir, "ml.json")
	os.WriteFile(mlPlan, []byte(`{"kind":"`+plan.MainlineKind+`","id":"x"}`), 0o644)
	offPlan := filepath.Join(dir, "off.json")
	os.WriteFile(offPlan, []byte(`{"schema":1,"id":"x"}`), 0o644)
	for _, c := range []struct {
		args []string
		want bool
	}{
		{[]string{"--image", "x", "--fw", "fw.bin"}, false},
		{[]string{"--image", "x", "--mainline", "d"}, true},
		{[]string{"--device", "/dev/mmcblk0", "--fw-drive", ml}, true},
		{[]string{"--device", "/dev/mmcblk0", "--fw-drive=" + ml}, true},
		{[]string{"--device", "/dev/mmcblk0", "--fw-drive", fw}, false},
		{[]string{"--device", "/dev/mmcblk0", "--fw-drive", junk}, false},
		{[]string{"--device", "/dev/mmcblk0", "--fw-drive", filepath.Join(dir, "none.img")}, false},
		{[]string{"--device", "/dev/mmcblk0", "--fw-drive", fw, "--plan", mlPlan}, true},
		{[]string{"--device", "/dev/mmcblk0", "--fw-drive", fw, "--plan", offPlan}, false},
		{[]string{"--device", "/dev/mmcblk0", "--fw-drive", fw, "--plan", dir}, false},
	} {
		if got := mainlineSelected(c.args); got != c.want {
			t.Fatalf("%q: %v, want %v", c.args, got, c.want)
		}
	}
}

func runBin(t *testing.T, env []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(testBin, args...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), code
}

// TestMainlineCLIDryRunRefused: a dry run of a mainline bundle on an image
// of the fixture's unit reaches the mainline planner, which refuses: the
// keeper's chain is not a firmware of the embedded manifests (the release's
// gate; the dry run has no test manifests). Nothing is written.
func TestMainlineCLIDryRunRefused(t *testing.T) {
	bc := os.Getenv("UC_PXX_BOOTCTL")
	if bc == "" {
		t.Skip("UC_PXX_BOOTCTL not set")
	}
	dir := t.TempDir()
	img, err := fixture.Image(dir, "emmc.img", fakeemmc.Unit{AB: [2][3]byte{{14, 0, 1}, {15, 0, 1}}, A: "old", B: "new"})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(img)
	b, _ := mlBundleFor(t)
	d, err := b.Write(dir)
	if err != nil {
		t.Fatal(err)
	}
	common := []string{"--image", img, "--mainline", d, "--bootctl", bc, "--prereqs-confirmed", "--unlock-confirmed",
		"--hash-keeper", "--tmp", dir}
	if e := os.Getenv("UC_PXX_BOOTCTL_EXEC"); e != "" {
		common = append(common, "--bootctl-exec", e)
	}
	jp := filepath.Join(dir, "plan.json")
	out, code := runBin(t, nil, append([]string{"plan", "--json", jp}, common...)...)
	if code != 1 || !strings.Contains(out, "FAIL chain-manifest") || !strings.Contains(out, "RESULT: refused, nothing would be written") {
		t.Fatalf("plan: exit %d\n%s", code, out)
	}
	var p plan.MainlinePlan
	raw, _ := os.ReadFile(jp)
	if err := json.Unmarshal(raw, &p); err != nil || p.Kind != plan.MainlineKind || p.ID != "" {
		t.Fatalf("the refused plan JSON: %v %s %q", err, p.Kind, p.ID)
	}
	out, code = runBin(t, nil, append([]string{"install", "--dry-run"}, common...)...)
	if code != 1 || !strings.Contains(out, "RESULT: refused, nothing written") {
		t.Fatalf("install: exit %d\n%s", code, out)
	}
	if after, _ := os.ReadFile(img); !bytes.Equal(after, before) {
		t.Fatal("the image changed")
	}
	// without --mainline, --fw and the rest: the official usage
	if out, code := runBin(t, nil, "plan", "--image", img, "--bootctl", bc); code != 2 || !strings.Contains(out, "usage:") {
		t.Fatalf("official plan without --fw: exit %d\n%s", code, out)
	}
	// --unlock-confirmed is not an official flag: the official parser refuses it as before
	if out, code := runBin(t, nil, "plan", "--image", img, "--fw", "x.bin", "--bootctl", bc, "--unlock-confirmed"); code != 2 ||
		!strings.Contains(out, "flag provided but not defined: -unlock-confirmed") {
		t.Fatalf("official plan with --unlock-confirmed: exit %d\n%s", code, out)
	}
}

// TestMainlineCLIDevice: the device mode on a fake unit through the CLI:
// plan with --json, then install --plan --yes, with the bundle in a folder
// and on the drive; a drive with a .bin too is refused (exit 1); without
// --unlock-confirmed refused.
func TestMainlineCLIDevice(t *testing.T) {
	bin := bootctlBin(t)
	for _, onDrive := range []bool{false, true} {
		t.Run(map[bool]string{false: "folder", true: "drive"}[onDrive], func(t *testing.T) {
			dir := t.TempDir()
			src, err := fixture.Image(dir, "src.img", fakeemmc.Unit{AB: [2][3]byte{{14, 0, 1}, {15, 0, 1}}, A: "old", B: "new"})
			if err != nil {
				t.Fatal(err)
			}
			r, err := devicetest.New(filepath.Join(dir, "root"), src, small, bin, devicetest.Opts{})
			if err != nil {
				t.Fatal(err)
			}
			os.Remove(src)
			b, _ := mlBundleFor(t)
			var bundle []string
			if onDrive {
				img := r.Path(drivestate.Image)
				mlDriveFile(t, img, b, false)
				lun := r.Path(drivestate.LunFile)
				os.MkdirAll(filepath.Dir(lun), 0o755)
				os.WriteFile(lun, []byte("\n"), 0o644)
				bundle = []string{"--fw-drive", img}
			} else {
				d, err := b.Write(r.Path("/run/p10inst"))
				if err != nil {
					t.Fatal(err)
				}
				bundle = []string{"--mainline", d}
			}
			env := []string{"TMPDIR=" + dir}
			common := append([]string{"--device", "/dev/mmcblk0", "--test-root", r.Dir, "--test-fake-unit", "--prereqs-confirmed",
				"--panel-confirmed"}, bundle...)
			before, _ := r.Image()
			// no --unlock-confirmed: refused
			out, code := runBin(t, env, append([]string{"plan"}, common...)...)
			if code != 1 || !strings.Contains(out, "FAIL unlock") {
				t.Fatalf("plan without --unlock-confirmed: exit %d\n%s", code, out)
			}
			jp := r.Path("/run/p10inst/ml-plan.json")
			out, code = runBin(t, env, append([]string{"plan", "--unlock-confirmed", "--json", jp}, common...)...)
			if code != 0 || !strings.Contains(out, "RESULT: plan OK (nothing written)") || !strings.Contains(out, "mainline  uc-pxx-mainline") {
				t.Fatalf("plan: exit %d\n%s", code, out)
			}
			if after, _ := r.Image(); !bytes.Equal(after, before) {
				t.Fatal("the plan run changed the unit")
			}
			// the official install with the mainline plan file goes to the mainline install (the plan's kind)
			out, code = runBin(t, env, append([]string{"install", "--unlock-confirmed", "--plan", jp, "--yes",
				"--log", r.Path("/run/p10inst/ml.jsonl")}, common...)...)
			if code != 0 || !strings.Contains(out, "RESULT: install done") {
				t.Fatalf("install: exit %d\n%s", code, out)
			}
			sec, _ := r.ABSector()
			a, _ := avbab.Parse(sec)
			if a.Pick() != 0 || a.Slots[0] != (avbab.Slot{Priority: 15, Tries: 7}) || a.Slots[1] != (avbab.Slot{Priority: 14, Successful: 1}) {
				t.Fatalf("A/B after: %v %v", a.Slots[0], a.Slots[1])
			}
		})
	}
	t.Run("drive with a .bin too", func(t *testing.T) {
		dir := t.TempDir()
		src, _ := fixture.Image(dir, "src.img", fakeemmc.Unit{AB: [2][3]byte{{14, 0, 1}, {15, 0, 1}}, A: "old", B: "new"})
		r, err := devicetest.New(filepath.Join(dir, "root"), src, small, bin, devicetest.Opts{})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := mlBundleFor(t)
		img := r.Path(drivestate.Image)
		mlDriveFile(t, img, b, true)
		lun := r.Path(drivestate.LunFile)
		os.MkdirAll(filepath.Dir(lun), 0o755)
		os.WriteFile(lun, []byte("\n"), 0o644)
		out, code := runBin(t, []string{"TMPDIR=" + dir}, "plan", "--device", "/dev/mmcblk0", "--test-root", r.Dir, "--test-fake-unit",
			"--prereqs-confirmed", "--panel-confirmed", "--unlock-confirmed", "--fw-drive", img)
		if code != 1 || !strings.Contains(out, "and a firmware file (fw.bin): leave only one of them there") {
			t.Fatalf("exit %d\n%s", code, out)
		}
	})
}
