//go:build install && devtest

package devmode_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uc-pxx-installer/internal/device"
	"uc-pxx-installer/internal/device/devicetest"
	"uc-pxx-installer/internal/devmode"
	"uc-pxx-installer/internal/factory/factorytest"
	"uc-pxx-installer/internal/fakeemmc"
	"uc-pxx-installer/internal/plan"
)

// p8Opts is the RAM image of a UC-P8 (image/build.sh --board ucp8).
var p8Opts = devicetest.Opts{Model: "Crestron UC-P8", Compat: "crestron,uc-p8\x00rockchip,rk3399"}

func withCfg(u fakeemmc.Unit, cfg []byte) fakeemmc.Unit {
	u.Tweak = map[string][]byte{"cfg": cfg}
	return u
}

// TestUnitModelRefusals: the model gate (UC-P8.md §2.3, §11). Plan,
// install, repair and switch all refuse, before any read of the plan file
// or any bootctl run, when the factory data does not name the model of the
// RAM image by the rule of the stock U-Boot. Nothing is written.
func TestUnitModelRefusals(t *testing.T) {
	zero := make([]byte, 4096)
	cases := []struct {
		name string
		o    devicetest.Opts
		cfg  []byte // nil: the default (UC-P10-T-HS)
		want string
	}{
		{"a UC-P8 unit, the UC-P10 image", devicetest.Opts{}, factorytest.Unit2Like("UC-P8-T-HS"),
			`this unit's factory data says "UC-P8-T-HS": it is a UC-P8. This installer image is for the UC-P10. Nothing was written.`},
		{"a UC-P10 unit, the UC-P8 image", p8Opts, nil,
			`this unit's factory data says "UC-P10-T-HS": it is a UC-P10. This installer image is for the UC-P8. Nothing was written.`},
		{"the UC-P8 model with the UC-P10 compatible", devicetest.Opts{Model: "Crestron UC-P8"}, factorytest.Unit2Like("UC-P8"),
			"does not list crestron,uc-p8: this is not a UC-P8"},
		{"another board", devicetest.Opts{Model: "Crestron UC-P12", Compat: "crestron,uc-p12\x00rockchip,rk3399"}, nil,
			"this is not a UC-P10 or a UC-P8"},
		{"a model U-Boot maps to the 800x1280 panel", devicetest.Opts{}, factorytest.Unit2Like("UC-P12"),
			`names no UC-P10 or UC-P8: it says "UC-P12"`},
		{"the same, on the UC-P8 image", p8Opts, factorytest.Unit2Like("UC-P12"), `it says "UC-P12"`},
		{"lower case", devicetest.Opts{}, factorytest.Unit2Like("uc-p10"), `it says "uc-p10"`},
		{"a wrong checksum", devicetest.Opts{}, factorytest.Build(factorytest.Opt{BadSum: true}), "its checksum is wrong"},
		{"a zero checksum", devicetest.Opts{}, factorytest.Build(factorytest.Opt{ZeroSum: true}), "its checksum is wrong"},
		{"no model record", devicetest.Opts{}, factorytest.Build(factorytest.Opt{NoModel: true}), "no model record"},
		{"two model records", devicetest.Opts{}, factorytest.Build(factorytest.Opt{TwoModels: true}), "2 model records"},
		{"an empty cfg", devicetest.Opts{}, zero, "length 0 out of range"},
		{"the length past 4096", devicetest.Opts{}, factorytest.Build(factorytest.Opt{Length: 5000}), "length 5000 out of range"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			un := unitFwd
			if c.cfg != nil {
				un = withCfg(un, c.cfg)
			}
			u := newUnit(t, un, c.o, nil, "")
			check := func(what string, err error, out string) {
				t.Helper()
				if err == nil || !device.IsRefusal(err) || !strings.Contains(err.Error(), c.want) {
					t.Fatalf("%s: err = %v, want a refusal with %q\n%s", what, err, c.want, out)
				}
				if !strings.Contains(err.Error(), "nothing written") {
					t.Fatalf("%s: %v does not say that nothing was written", what, err)
				}
			}
			var out bytes.Buffer
			_, err := devmode.Plan(u.newEnv(), u.opts("a"), &out)
			check("plan", err, out.String())
			_, o2, err := u.install(u.opts("a"))
			check("install", err, o2)
			planFile := filepath.Join(t.TempDir(), "plan.json")
			os.WriteFile(planFile, []byte("{}"), 0o644)
			out.Reset()
			_, err = devmode.Repair(u.newEnv(), devmode.RepairOptions{Device: device.EMMCPath, PlanFile: planFile,
				LogDir: filepath.Join(t.TempDir(), "log"), Yes: true}, &out)
			check("repair", err, out.String())
			out.Reset()
			_, err = devmode.Switch(u.newEnv(), devmode.SwitchOptions{Device: device.EMMCPath, To: "b",
				LogDir: filepath.Join(t.TempDir(), "log"), Yes: true}, &out)
			check("switch", err, out.String())
			if len(u.bc.Calls) != 0 {
				t.Fatalf("uc-pxx-bootctl-inst ran: %q", u.bc.Calls)
			}
			u.untouched()
		})
	}
}

// TestUnitModelInstall: on a UC-P10 (unit 2's factory model) and on a UC-P8
// the gate passes, the plan records the unit model with its suffix, and
// the install is the same install (the same steps).
func TestUnitModelInstall(t *testing.T) {
	bin, exe := bootctlBin(t)
	var steps [2]string
	for i, c := range []struct {
		name, factory, base, suffix, dt string
		o                               devicetest.Opts
	}{
		{"UC-P10-T-HS", "UC-P10-T-HS", "UC-P10", "-T-HS", "Crestron UC-P10", devicetest.Opts{}},
		{"UC-P8-T-HS", "UC-P8-T-HS", "UC-P8", "-T-HS", "Crestron UC-P8", p8Opts},
	} {
		t.Run(c.name, func(t *testing.T) {
			u := newUnit(t, withCfg(unitFwd, factorytest.Unit2Like(c.factory)), c.o, bin, exe)
			p := u.plan("a")
			want := plan.UnitModel{DTModel: c.dt, FactoryModel: c.factory, Base: c.base, Suffix: c.suffix, HWRev: "V1.5A", ChecksumOK: true}
			if p.Device == nil || p.Device.UnitModel != want || p.Device.Model != c.dt {
				t.Fatalf("plan device %+v, want unit model %+v", p.Device, want)
			}
			js, _ := json.Marshal(p)
			if !strings.Contains(string(js), `"unit_model":{"dt_model":"`+c.dt+`","factory_model":"`+c.factory+`","base":"`+c.base+`","suffix":"`+c.suffix+`","hwrev":"V1.5A","checksum_ok":true}`) {
				t.Fatalf("plan JSON: %s", js)
			}
			var ok bool
			for _, ch := range p.Checks {
				ok = ok || ch.ID == "unit-model" && ch.OK
			}
			if !ok {
				t.Fatal("no unit-model check that passed")
			}
			_, out, err := u.install(u.opts("a"))
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if !strings.Contains(out, `unit: the factory data says "`+c.factory+`" (`+c.base+`, suffix "`+c.suffix+`", hardware V1.5A); this RAM image is for the `+c.base) {
				t.Fatalf("no unit line\n%s", out)
			}
			u.checkDone(p)
			var s []string
			for _, st := range p.Steps {
				w := ""
				if st.Write != nil {
					w = st.Write.Partition
				}
				s = append(s, st.Op+" "+w)
			}
			steps[i] = strings.Join(s, "|")
		})
	}
	if steps[0] == "" || steps[0] != steps[1] {
		t.Fatalf("the steps differ between the models:\n%s\n%s", steps[0], steps[1])
	}
}
