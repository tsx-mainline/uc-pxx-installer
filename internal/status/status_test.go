package status

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash/crc32"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"uc-pxx-installer/internal/bootimg"
	"uc-pxx-installer/internal/fakedev"
	"uc-pxx-installer/internal/manifest"
	"uc-pxx-installer/internal/nvram"
	"uc-pxx-installer/internal/nvram/nvramtest"
	"uc-pxx-installer/internal/vendorstore"
)

func abSector(a, b [3]byte) []byte {
	s := make([]byte, 512)
	copy(s, "\x00AB0\x01")
	copy(s[8:], []byte{a[0], a[1], a[2], 0, b[0], b[1], b[2], 0})
	binary.BigEndian.PutUint32(s[28:], crc32.ChecksumIEEE(s[:28]))
	return s
}

func bootHeader(name string, osv uint32) []byte {
	b := make([]byte, bootimg.HeaderSize)
	copy(b, "ANDROID!")
	binary.LittleEndian.PutUint32(b[36:], 2048)
	binary.LittleEndian.PutUint32(b[44:], osv)
	copy(b[48:], name)
	return b
}

func mkext4(t *testing.T, label string, files map[string]string) []byte {
	t.Helper()
	mk, err := exec.LookPath("mke2fs")
	if err != nil {
		if _, e := os.Stat("/sbin/mke2fs"); e == nil {
			mk, err = "/sbin/mke2fs", nil
		}
	}
	if err != nil {
		t.Skip("mke2fs not installed")
	}
	dir := t.TempDir()
	for p, c := range files {
		os.MkdirAll(filepath.Join(dir, "src", filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(dir, "src", p), []byte(c), 0o644)
	}
	img := filepath.Join(dir, "fs.img")
	if out, err := exec.Command(mk, "-q", "-F", "-t", "ext4", "-L", label, "-d", filepath.Join(dir, "src"), img, "8M").CombinedOutput(); err != nil {
		t.Fatalf("mke2fs: %v %s", err, out)
	}
	b, _ := os.ReadFile(img)
	return b
}

func shaOf(b []byte, n int) string {
	h := sha256.Sum256(append(b, make([]byte, n-len(b))...)[:n])
	return hex.EncodeToString(h[:])
}

// The slot is recognised by its fingerprint, not by the incremental (a build time).
const testFingerprint = "rockchip/pcd/pcd:9/PQ3A.190505.002/bxpan04012107:userdebug/test-keys"

func testManifest(ubootData, bootData []byte) *manifest.Manifest {
	m := &manifest.Manifest{Schema: 2, Build: map[string]string{
		"ro.build.type": "userdebug", "ro.build.version.incremental": "210836", "ro.build.fingerprint": testFingerprint}}
	m.BootChain.UBootBuild = "userdebug"
	m.Firmware.Version = "1.0.0.94"
	m.BuildPropPath = "/system/build.prop"
	m.Partitions = []manifest.Part{
		{Partition: "uboot", Size: 4 << 20, SHA256: shaOf(ubootData, 4<<20)},
		{Partition: "boot", Size: 1 << 20, SHA256: shaOf(bootData, 1<<20)},
	}
	return m
}

func vendorArea(serial string) []byte {
	size := vendorstore.CopySectors * 512
	b := make([]byte, size)
	le := binary.LittleEndian
	le.PutUint32(b[0:], 0x524B5644)
	le.PutUint32(b[4:], 3)
	le.PutUint32(b[size-4:], 3)
	le.PutUint16(b[10:], 1)
	le.PutUint16(b[16:], 1)
	le.PutUint16(b[20:], uint16(len(serial)))
	copy(b[1024:], serial)
	return b
}

// device is unit 2 as it was on 2026-10-24: mainline in slot A, Android
// userdebug in slot B, both successful, the userdebug U-Boot in both slots.
func device(t *testing.T, ab []byte) (string, *manifest.Manifest) {
	return deviceWith(t, ab, nvramtest.Off(), nil)
}

// deviceWith is device with the given others partition. change edits the
// partition list before the tree is made.
func deviceWith(t *testing.T, ab, others []byte, change func([]fakedev.Part) []fakedev.Part) (string, *manifest.Manifest) {
	root := t.TempDir()
	parts := append([]fakedev.Part{}, fakedev.Layout...)
	uboot := []byte("userdebug u-boot")
	stockBoot := append(bootHeader("", 0x12000136), []byte("stock kernel")...)
	set := func(name string, data []byte) { parts[fakedev.PartNum(parts, name)-1].Data = data }
	set("uboot_a", uboot)
	set("uboot_b", uboot)
	set("boot_a", bootHeader("ucpxx-003ba796", 0))
	set("boot_b", stockBoot)
	set("cfg", []byte{0, 0, 0, 0, 0, 0, 0x02, 0x11, 0x22, 0x33, 0x44, 0x55})
	set("system_a", mkext4(t, "tsxroot", map[string]string{"etc/tsx/build-id": "v4-ha-1\n"}))
	set("system_b", mkext4(t, "/", map[string]string{"system/build.prop": "ro.build.version.incremental=210836\nro.build.type=userdebug\nro.build.version.release=9\nro.build.fingerprint=" + testFingerprint + "\n"}))
	set("others", others)
	if change != nil {
		parts = change(parts)
	}
	dev, err := fakedev.New(root, parts)
	if err != nil {
		t.Fatal(err)
	}
	dev.WriteDisk(49156, ab)
	dev.WriteDisk(vendorstore.Start, vendorArea("TESTSERIAL1"))
	os.MkdirAll(filepath.Join(root, "proc/device-tree"), 0o755)
	os.WriteFile(filepath.Join(root, "proc/device-tree/model"), []byte("Crestron UC-P10 \x00"), 0o644)
	return root, testManifest(uboot, stockBoot)
}

func TestCollectUnit2(t *testing.T) {
	ab := abSector([3]byte{15, 0, 1}, [3]byte{14, 0, 1})
	if crc := binary.BigEndian.Uint32(ab[28:]); crc != 0xda933679 {
		t.Fatalf("crc %08x, the unit has da933679", crc)
	}
	root, m := device(t, ab)
	r := Collect(Source{Root: root, Manifests: []*manifest.Manifest{m},
		Cmdline: "console=ttyS2 uc-pxx.id=p10inst-test p10inst.ab=" + hex.EncodeToString(ab[:32])})
	if !r.ABValid || r.Next != 0 || r.Fallback != 1 {
		t.Fatalf("ab %v %q next %d fallback %d", r.ABValid, r.ABErr, r.Next, r.Fallback)
	}
	a, b := r.Slots[0], r.Slots[1]
	checks := map[string]bool{
		"model":          r.Model == "Crestron UC-P10",
		"serial":         r.Serial == "TESTSERIAL1",
		"mac":            r.MAC == "02:11:22:33:44:55",
		"token":          r.ImageToken == "p10inst-test",
		"a token":        a.Token == "ucpxx-003ba796",
		"a uboot":        a.UBootUD && a.UBoot == "U-Boot of 1.0.0.94 (userdebug)",
		"a summary":      a.Summary == "uc-pxx mainline, build v4-ha-1, token ucpxx-003ba796",
		"b oem":          b.BootOEM && b.Boot == "Android boot of 1.0.0.94 (userdebug)",
		"b summary":      b.Summary == "Crestron 1.0.0.94 (userdebug)",
		"b system":       strings.HasPrefix(b.System, "Android 9 userdebug, build 210836"),
		"unlock":         r.Unlock == "unlocked" && strings.Contains(r.UnlockWhy, "slot a"),
		"pre-fastboot":   r.PreFastboot != nil && r.PreFastboot.Raw == r.AB.Raw,
		"no slot errors": len(a.Errs) == 0 && len(b.Errs) == 0,
		"read-only flag": r.ReadOnly == "no",
	}
	for k, ok := range checks {
		if !ok {
			t.Errorf("%s wrong", k)
		}
	}
	text := r.Text()
	for _, want := range []string{"> Slot A  successful (priority 15)  boots next", "Slot B  successful (priority 14)  fallback",
		"unlocked:", "same as before the fastboot entry"} {
		if !strings.Contains(text, want) {
			t.Errorf("text has no %q:\n%s", want, text)
		}
	}
	if t.Failed() {
		t.Log(text)
	}
}

func TestCollectBadAB(t *testing.T) {
	// the state that bricked unit 1: A dead, B successful with tries left
	ab := abSector([3]byte{15, 0, 0}, [3]byte{14, 1, 1})
	root, m := device(t, ab)
	r := Collect(Source{Root: root, Manifests: []*manifest.Manifest{m}, Cmdline: "x"})
	if r.ABValid || r.Next != -1 {
		t.Fatalf("illegal sector accepted: %+v", r.AB)
	}
	if r.Unlock != "unknown" {
		t.Errorf("unlock %q with an invalid A/B sector", r.Unlock)
	}
	if !strings.Contains(r.Text(), "not valid") {
		t.Error(r.Text())
	}
	// little-endian CRC
	le := abSector([3]byte{15, 0, 1}, [3]byte{14, 0, 1})
	crc := binary.BigEndian.Uint32(le[28:])
	binary.LittleEndian.PutUint32(le[28:], crc)
	root, m = device(t, le)
	r = Collect(Source{Root: root, Manifests: []*manifest.Manifest{m}, Cmdline: "x"})
	if r.ABValid || !strings.Contains(r.ABErr, "little-endian") {
		t.Errorf("little-endian crc: %v %q", r.ABValid, r.ABErr)
	}
}

func TestCollectFallbackAndDiff(t *testing.T) {
	ab := abSector([3]byte{15, 0, 1}, [3]byte{0, 0, 0})
	root, m := device(t, ab)
	pre := abSector([3]byte{15, 0, 1}, [3]byte{14, 0, 1})
	r := Collect(Source{Root: root, Manifests: []*manifest.Manifest{m}, Cmdline: "p10inst.ab=" + hex.EncodeToString(pre[:32])})
	if !r.ABValid || r.Next != 0 || r.Fallback != -1 {
		t.Fatalf("next %d fallback %d", r.Next, r.Fallback)
	}
	text := r.Text()
	if !strings.Contains(text, "No fallback") || !strings.Contains(text, "differs") {
		t.Error(text)
	}
	// no manifests: nothing is named, nothing is proven
	r = Collect(Source{Root: root, Cmdline: "x"})
	if r.Unlock != "unknown" || r.Slots[1].Summary != "Android build 210836 (unknown firmware)" {
		t.Errorf("%q %q", r.Unlock, r.Slots[1].Summary)
	}
}

func TestCmdlineValue(t *testing.T) {
	v, ok := CmdlineValue("a=1 uc-pxx.id=tok b", "uc-pxx.id")
	if !ok || v != "tok" {
		t.Error(v, ok)
	}
	if _, ok := CmdlineValue("uc-pxx.idx=1", "uc-pxx.id"); ok {
		t.Error("prefix matched")
	}
}

func TestCollectAutoUpdate(t *testing.T) {
	ab := abSector([3]byte{15, 0, 1}, [3]byte{14, 0, 1})
	secret := "2=made-up-admin-password-77f1"
	rec := func(fw string) []string { return append([]string{secret}, nvramtest.Records(fw)...) }
	type P = nvramtest.Part
	moved := func(ps []fakedev.Part) []fakedev.Part {
		ps[fakedev.PartNum(ps, "others")-1].Start++
		return ps
	}
	gone := func(ps []fakedev.Part) []fakedev.Part {
		i := fakedev.PartNum(ps, "others") - 1
		return append(ps[:i:i], ps[i+1:]...)
	}
	cases := []struct {
		name   string
		others []byte
		change func([]fakedev.Part) []fakedev.Part
		state  string
		text   []string
	}{
		{"off", nvramtest.Partition(P{Index: 30, Counter: 126, Records: rec("true")}, P{Index: 31, Counter: 127, Records: rec("false")}),
			nil, nvram.Off, []string{"Firmware   off (72005=false)", "APK        on", "URL        https://example.invalid/fw.txt", "active part 31 of 32"}},
		{"on", nvramtest.Partition(P{Index: 0, Counter: 128, Records: rec("true")}, P{Index: 31, Counter: 127, Records: rec("false")}),
			nil, nvram.On, []string{"Firmware   ON (72005=true)", "The installer refuses"}},
		{"corrupt", nvramtest.Partition(P{Index: 0, Counter: 1, Records: rec("false"), BadSum: true}),
			nil, nvram.Unknown, []string{"Firmware   unknown", "no valid nvram part", "Not clear. The installer goes on only if you confirm"}},
		{"tie", nvramtest.Partition(P{Index: 0, Counter: 9, Records: rec("false")}, P{Index: 1, Counter: 9, Records: rec("false")}),
			nil, nvram.Unknown, []string{"Firmware   unknown", "same highest write counter", "Not clear."}},
		{"tie, one part on", nvramtest.Partition(P{Index: 0, Counter: 9, Records: rec("false")}, P{Index: 1, Counter: 9, Records: rec("true")}),
			nil, nvram.On, []string{"Firmware   ON (72005=true)", "parts 0 and 1 have this highest write counter", "The installer refuses"}},
		{"empty others", nil, nil, nvram.Unknown, []string{"Firmware   unknown", "no valid nvram part", "Not clear."}},
		{"others moved", nvramtest.Off(), moved, nvram.Unknown, []string{"Firmware   unknown", "not 3671040", "Not clear."}},
		{"no others", nil, gone, nvram.Unknown, []string{"Firmware   unknown", "no partition others", "Not clear."}},
	}
	for _, c := range cases {
		root, m := deviceWith(t, ab, c.others, c.change)
		r := Collect(Source{Root: root, Manifests: []*manifest.Manifest{m}, Cmdline: "x"})
		text := r.Text()
		if r.AutoUpdate.State != c.state {
			t.Errorf("%s: state %s (%s)", c.name, r.AutoUpdate.State, r.AutoUpdate.Why)
		}
		for _, want := range c.text {
			if !strings.Contains(text, want) {
				t.Errorf("%s: text has no %q:\n%s", c.name, want, text)
			}
		}
		for _, bad := range []string{"made-up", "100=", "1=test"} {
			if strings.Contains(text, bad) {
				t.Errorf("%s: the status shows %q, a key that is not an auto-update key", c.name, bad)
			}
		}
		if c.state == nvram.Off && (strings.Contains(text, "The installer refuses") || strings.Contains(text, "Not clear")) {
			t.Errorf("%s: refusal text with auto update off", c.name)
		}
		// "on" is a plain refusal: no confirmation is offered for it
		if c.state == nvram.On && strings.Contains(text, "confirm") {
			t.Errorf("%s: a confirmation is offered while auto update is on:\n%s", c.name, text)
		}
		if c.state == nvram.Unknown && strings.Contains(text, "The installer refuses") {
			t.Errorf("%s: the unclear state shows the text of on", c.name)
		}
	}
}
