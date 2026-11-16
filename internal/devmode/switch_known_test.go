//go:build install && devtest

package devmode_test

// I-2 (mainline-rc-10): the switch to a slot that is not tested yet, with the
// real uc-pxx-bootctl-inst r3 (UC_PXX_BOOTCTL_R3) on fake units.

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/device/devicetest"
	"uc-pxx-installer/internal/devmode"
	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/manifest"
)

func bootctlBinR3(t testing.TB) ([]byte, string) {
	p := os.Getenv("UC_PXX_BOOTCTL_R3")
	if p == "" {
		t.Skip("UC_PXX_BOOTCTL_R3 not set: this test runs the real uc-pxx-bootctl-inst r3")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if devicetest.SHA256(b) != device.RAMBootctlSHA256 {
		t.Fatalf("%s is not r3 %s", p, device.RAMBootctlSHA256)
	}
	return b, os.Getenv("UC_PXX_BOOTCTL_EXEC")
}

func r3Unit(t *testing.T) *unit {
	bin, exe := bootctlBinR3(t)
	// both slots hold the fixture firmware (a known firmware of the fixture's manifests)
	u := newUnit(t, fakeemmc.Unit{AB: [2][3]byte{{15, 6, 0}, {14, 7, 0}}, A: "new", B: "new"}, devicetest.Opts{}, bin, exe)
	u.bootctlSHA = device.RAMBootctlSHA256
	u.env = u.newEnv()
	return u
}

func TestSwitchUntested(t *testing.T) {
	s4 := func(p, tr, s byte) [4]byte { return [4]byte{p, tr, s, 0} }
	for _, c := range []struct {
		name   string
		a, b   [4]byte
		to     string
		poke   string // a partition whose first byte is changed (its chain is not known any more)
		code   int
		wa, wb [4]byte
		want   string
	}{
		{"the unit-3 case: both trying, to b", s4(15, 6, 0), s4(14, 7, 0), "b", "", 0, s4(14, 6, 0), s4(15, 7, 0), "RESULT: switched"},
		{"a successful fallback", s4(15, 0, 1), s4(14, 7, 0), "b", "", 0, s4(14, 0, 1), s4(15, 7, 0), "RESULT: switched"},
		{"to a: a starts next already", s4(15, 6, 0), s4(14, 7, 0), "a", "", 0, s4(15, 6, 0), s4(14, 7, 0), "RESULT: no switch needed"},
		{"the target's chain is not known", s4(15, 6, 0), s4(14, 7, 0), "b", "boot_b", 1, s4(15, 6, 0), s4(14, 7, 0), "is not one known firmware"},
		{"the target's U-Boot is not known", s4(15, 6, 0), s4(14, 7, 0), "b", "uboot_b", 1, s4(15, 6, 0), s4(14, 7, 0), "is not one known firmware"},
		{"a trying fallback that is not known", s4(15, 6, 0), s4(14, 7, 0), "b", "vbmeta_a", 1, s4(15, 6, 0), s4(14, 7, 0), "no proven fallback"},
		{"a successful fallback needs no chain", s4(15, 0, 1), s4(14, 7, 0), "b", "boot_a", 0, s4(14, 0, 1), s4(15, 7, 0), "RESULT: switched"},
		{"a target with no tries", s4(15, 0, 1), s4(14, 0, 0), "b", "", 1, s4(15, 0, 1), s4(14, 0, 0), "is not trying"},
		{"a disabled fallback", s4(0, 0, 0), s4(15, 7, 0), "b", "", 1, s4(0, 0, 0), s4(15, 7, 0), "would not be a fallback"},
		{"a successful target is not untested", s4(15, 6, 0), s4(14, 0, 1), "b", "", 1, s4(15, 6, 0), s4(14, 0, 1), "is not trying"},
	} {
		t.Run(c.name, func(t *testing.T) {
			u := r3Unit(t)
			if c.poke != "" {
				u.poke(c.poke, 0)
			}
			sec := abSector(u.start, c.a, c.b)
			u.setAB(sec)
			u.bc.Calls = nil
			_, dout, dcode := u.switchTo(c.to, func(o *devmode.SwitchOptions) { o.Yes, o.DryRun, o.Untested = false, true, true })
			if !bytes.Equal(u.ab(), sec) || len(u.writeCalls()) != 0 {
				t.Fatalf("the dry run wrote\n%s", dout)
			}
			writes := c.code == 0 && !bytes.Equal(abSector(u.start, c.wa, c.wb), sec)
			if want := map[bool]int{true: 5, false: c.code}[writes]; dcode != want {
				t.Fatalf("dry run exit %d, want %d\n%s", dcode, want, dout)
			}
			res, out, code := u.switchTo(c.to, func(o *devmode.SwitchOptions) { o.Untested = true })
			if code != c.code || !strings.Contains(out, c.want) || (res.Writes == 1) != writes {
				t.Fatalf("exit %d writes %d, want %d %v %q\n%s", code, res.Writes, c.code, writes, c.want, out)
			}
			if want := abSector(u.start, c.wa, c.wb); !bytes.Equal(u.ab(), want) {
				t.Fatalf("A/B %x, want %x\n%s", u.ab()[:32], want[:32], out)
			}
			a, _ := avbab.Parse(u.ab())
			if a.Validate() != nil || a.Pick() < 0 {
				t.Fatal("no bootable slot")
			}
			if writes {
				if !strings.Contains(out, "switch: slot B is not tested yet; its boot chain is ") || a.Slots[1].Successful != 0 ||
					a.Slots[1].Tries != c.b[1] || !a.Slots[0].Bootable() {
					t.Fatalf("after the switch: %x\n%s", u.ab()[:32], out)
				}
				for _, call := range u.bc.Calls {
					if strings.HasPrefix(call, "switch ") && !strings.HasSuffix(call, " --untested") && !strings.HasSuffix(call, " --untested --dry-run") {
						t.Fatalf("a switch without --untested: %q", call)
					}
				}
			}
		})
	}
	// RC-10 review I-2 MAJOR-1: a chain of a firmware the manifests refuse,
	// of a test build, or (for the target) a chain that never booted, is not
	// a known chain: nothing written
	for name, edit := range map[string]func(m *manifest.Manifest){
		"refuse":     func(m *manifest.Manifest) { m.Status, m.RefuseReason = manifest.StatusRefuse, "for an older UC-P10 board" },
		"test build": func(m *manifest.Manifest) { m.TestBuild = true },
		"no-go":      func(m *manifest.Manifest) { m.BootChain.ResourceStatus = manifest.ResourceNoGo },
		"not booted": func(m *manifest.Manifest) { m.BootChain.BootchainStatus = manifest.BootchainNotBooted },
	} {
		t.Run("a "+name+" chain is not known", func(t *testing.T) {
			u := r3Unit(t)
			fx := *u.fx
			fx.Manifests = append([]*manifest.Manifest(nil), u.fx.Manifests...)
			for i, m := range fx.Manifests {
				c := *m
				edit(&c)
				fx.Manifests[i] = &c
			}
			u.fx = &fx
			u.env = u.newEnv()
			sec := abSector(u.start, s4(15, 6, 0), s4(14, 7, 0))
			u.setAB(sec)
			_, out, code := u.switchTo("b", func(o *devmode.SwitchOptions) { o.Untested = true })
			if code != 1 || !bytes.Equal(u.ab(), sec) || len(u.writeCalls()) != 0 || !strings.Contains(out, "is not one known firmware") {
				t.Fatalf("exit %d\n%s", code, out)
			}
		})
	}
	t.Run("without --untested: r3 refuses a trying target as r2 does", func(t *testing.T) {
		u := r3Unit(t)
		sec := abSector(u.start, s4(15, 6, 0), s4(14, 7, 0))
		u.setAB(sec)
		_, out, code := u.switchTo("b", nil)
		if code != 1 || !bytes.Equal(u.ab(), sec) || !strings.Contains(out, "is not successful") {
			t.Fatalf("exit %d\n%s", code, out)
		}
	})
}

// TestSwitchUntestedMismatch: the tool misbehaves on the untested write:
// as the official switch, no second write, the read-back decides.
func TestSwitchUntestedMismatch(t *testing.T) {
	for _, c := range []struct {
		mode string
		code int
	}{
		// "wrong" clears the successful bytes: on a trying target they are 0 already, so its write is the prediction
		{"garbage", 3}, {"wrong", 0}, {"tail", 3}, {"lie", 0}, {"refuse", 1}, {"unknown", 1}, {"unknown-wrote", 0},
		{"signal", 1}, {"signal-wrote", 0}, {"torn-rc3:16", 3}, {"torn-rc3:32", 0},
	} {
		t.Run(c.mode, func(t *testing.T) {
			u := r3Unit(t)
			u.setAB(abSector(u.start, [4]byte{15, 6, 0, 0}, [4]byte{14, 7, 0, 0}))
			u.bc.Mode = c.mode
			res, out, code := u.switchTo("b", func(o *devmode.SwitchOptions) { o.Untested = true })
			if code != c.code || res.Writes != 1 || len(u.writeCalls()) != 1 {
				t.Fatalf("exit %d writes %d calls %v\n%s", code, res.Writes, u.writeCalls(), out)
			}
			a, _ := avbab.Parse(u.ab())
			if code == 0 && (a.Validate() != nil || a.Pick() != 1 || a.Slots[1].Successful != 0) {
				t.Fatalf("exit 0 but %x", u.ab()[:32])
			}
		})
	}
}
