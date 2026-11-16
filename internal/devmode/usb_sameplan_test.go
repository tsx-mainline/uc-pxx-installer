//go:build install && devtest

package devmode_test

// I-6 (mainline-rc-10): the files of step 1 over the USB cable give the same
// plan as the same files on the drive. The USB form hands the children the
// sealed RAM files (--fw FILE --fw-expect ID for the firmware, --mainline DIR
// for the bundle) in place of the drive (--fw-drive DRIVE --fw-drive-expect
// ID). The plans are byte for byte the same but for the source identity
// (firmware_ident: "drive ..." against "file ..." or the folder's files),
// which the plan id covers on purpose (it binds the install child to the
// source that the plan child read); and --fw-expect binds that file.

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"uc-pxx-installer/internal/device/devicetest"
	"uc-pxx-installer/internal/devmode"
	"uc-pxx-installer/internal/drivestate"
	"uc-pxx-installer/internal/plan"
)

// sameBut compares two plans as JSON with the id and the source identity
// cleared.
// fold maps each per-source text (the folder name, the stash index of each
// unit) to one placeholder first.
func sameBut(t *testing.T, a, b any, fold ...*regexp.Regexp) {
	t.Helper()
	norm := func(v any) string {
		j, _ := json.Marshal(v)
		for _, re := range fold {
			j = re.ReplaceAll(j, []byte("<source>"))
		}
		var m map[string]any
		json.Unmarshal(j, &m)
		delete(m, "id")
		delete(m, "firmware_ident")
		j, _ = json.Marshal(m)
		return string(j)
	}
	if x, y := norm(a), norm(b); x != y {
		var ma, mb map[string]any
		json.Unmarshal([]byte(x), &ma)
		json.Unmarshal([]byte(y), &mb)
		for k := range ma {
			ja, _ := json.Marshal(ma[k])
			jb, _ := json.Marshal(mb[k])
			if sa, sb := string(ja), string(jb); sa != sb {
				i := 0
				for i < len(sa) && i < len(sb) && sa[i] == sb[i] {
					i++
				}
				lo := i - 120
				if lo < 0 {
					lo = 0
				}
				t.Errorf("key %q differs at %d:\n%s\n%s", k, i, sa[lo:min(len(sa), i+200)], sb[lo:min(len(sb), i+200)])
			}
		}
		t.Fatal("the plans differ beyond the id and the source identity")
	}
}

func TestUSBSamePlanOfficial(t *testing.T) {
	bin, exe := bootctlBin(t)
	u := newUnit(t, unitFwd, devicetest.Opts{}, bin, exe)
	d := newDrive(t, u, map[string][]byte{"UC-P10 1.0.0.94.bin": u.fx.FW})
	d.eject()
	var out strings.Builder
	pd, err := devmode.Plan(u.newEnv(), u.driveOpts("b", d), &out)
	if err != nil {
		t.Fatalf("%v %s", err, out.String())
	}
	// the USB form: the same bytes as a sealed file, with its identity
	id, err := drivestate.Stat(u.fw)
	if err != nil {
		t.Fatal(err)
	}
	o := u.opts("b")
	o.FWExpect = id.String()
	pu, err := devmode.Plan(u.newEnv(), o, &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pd.FirmwareIdent, "drive ") || pu.FirmwareIdent != "file "+id.String() || pd.ID == pu.ID {
		t.Fatalf("source identities: %q %q", pd.FirmwareIdent, pu.FirmwareIdent)
	}
	sameBut(t, pd, pu)
	// the plan child and the install child of the USB form: the same id;
	// a changed file refuses (nothing written)
	pu2, err := devmode.Plan(u.newEnv(), o, &strings.Builder{})
	if err != nil || pu2.ID != pu.ID {
		t.Fatalf("a second plan of the USB form: %v %s %s", err, pu2.ID, pu.ID)
	}
	os.Chtimes(u.fw, time.Unix(1, 0), time.Unix(1, 0))
	before := u.image()
	if _, err := devmode.Plan(u.newEnv(), o, &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "not the one that was handed over") {
		t.Fatalf("a changed firmware file: %v", err)
	}
	if string(u.image()) != string(before) {
		t.Fatal("the refusal wrote")
	}
	// --fw-expect only with --fw
	bad := u.driveOpts("b", d)
	bad.FWExpect = id.String()
	if _, err := devmode.Plan(u.newEnv(), bad, &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "only with --fw") {
		t.Fatalf("--fw-expect with the drive: %v", err)
	}
}

func TestUSBSamePlanDirect(t *testing.T) {
	bin, exe := bootctlBin(t)
	var plans []*plan.DirectPlan
	for _, onDrive := range []bool{true, false} {
		m := newMLU(t, dUnitReset, onDrive, bin, exe)
		p, out, err := m.dPlan(m.mlOpts())
		if err != nil {
			t.Fatalf("drive %v: %v\n%s", onDrive, err, out)
		}
		plans = append(plans, p)
	}
	if plans[0].FirmwareIdent == plans[1].FirmwareIdent || !strings.HasPrefix(plans[0].FirmwareIdent, "drive ") {
		t.Fatalf("source identities: %q %q", plans[0].FirmwareIdent, plans[1].FirmwareIdent)
	}
	// the two fake units are the same fixture: the plans agree outside the
	// id, the source identity (the folder's name in mainline.source and in
	// its check line) and the stash of each unit (its own RAM image token,
	// so its own stash index)
	a, b := *plans[0], *plans[1]
	if !strings.HasPrefix(b.Mainline.Source, "folder ") || !strings.HasSuffix(b.Mainline.Source, "/run/uc-pxx-inst/uc-pxx-mainline") {
		t.Fatalf("the USB folder source: %q", b.Mainline.Source)
	}
	a.RAMImage, b.RAMImage = nil, nil
	a.UnlockDirect.Reader, b.UnlockDirect.Reader = nil, nil
	a.Stash, b.Stash = nil, nil
	sameBut(t, &a, &b,
		regexp.MustCompile(`the folder uc-pxx-mainline of the UC-PXX-INST drive|folder /[^"):;]*/run/uc-pxx-inst/uc-pxx-mainline`),
		regexp.MustCompile(`stash index sha256 [0-9a-f]{64}`))
}
