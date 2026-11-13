//go:build install && step2 && devtest

package step2_test

import (
	"bytes"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"uc-pxx-installer/internal/step2"
	"uc-pxx-installer/internal/step2ui"
)

// TestStep2EndUserOnly (mainline-rc-7): on a unit without the supervised
// marker (the end-user STEP-2 payload) the plan refuses a payload that the
// gate does not list as an end-user payload, and writes nothing; with the
// supervised marker the same payload plans (a supervised unit takes every
// reviewed normal payload); the listed one plans on an end-user unit.
func TestStep2EndUserOnly(t *testing.T) {
	bin, exe := bootctlBin(t)
	_, res := bundle()
	for _, c := range []struct {
		name                       string
		notEndUser, superv, cliSup bool
		want                       string
	}{
		{"an end-user unit, a payload that is not an end-user one", true, false, false, "installs only an end-user payload"},
		{"an end-user unit, --supervised, a payload that is not an end-user one (code review MINOR-A)", true, false, true, "installs only an end-user payload"},
		{"a supervised unit, the same payload", true, true, false, ""},
		{"an end-user unit, an end-user payload", false, false, false, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			u := newUnit(t, unitOf(res, shapes[0].ab, "a", "new"), "a", bin, exe)
			u.notEndUser = c.notEndUser
			if c.superv {
				os.MkdirAll(u.root.Path("/etc/uc-pxx-step2"), 0o755)
				os.WriteFile(u.root.Path(step2ui.SupervisedMarker), nil, 0o644)
			}
			var out bytes.Buffer
			o := u.opts(&out)
			o.Supervised = c.cliSup
			_, _, err := u.plan(o)
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("plan: %v\n%s", err, out.String())
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Fatalf("plan: %v, want %q", err, c.want)
			}
			if !bytes.Equal(u.image(), u.before) {
				t.Fatal("a plan wrote something")
			}
		})
	}
}

// TestStep2ABConsole (mainline-rc-7): a start writes every A/B read-back of
// the run (the start, each bootctl step's before and after, the check-ab
// step, the end) to the kernel log and to its output; the after of the arm
// and the end line are the sector as read from the unit afterwards.
func TestStep2ABConsole(t *testing.T) {
	bin, exe := bootctlBin(t)
	_, res := bundle()
	u := newUnit(t, unitOf(res, shapes[0].ab, "a", "new"), "a", bin, exe)
	os.MkdirAll(u.root.Path("/dev"), 0o755)
	if err := os.WriteFile(u.root.Path(step2.Kmsg), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p, out, err := u.startRun(u.opts(nil))
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	k, _ := os.ReadFile(u.root.Path(step2.Kmsg))
	sec, _ := u.root.ABSector()
	final := hex.EncodeToString(sec[:32])
	start := hex.EncodeToString(u.start[:32])
	id := p.ID[:12]
	for _, want := range []string{
		"<5>uc-pxx-step2: plan " + id + " start: keeper a, target b; A/B first 32 read back " + start,
		"uc-pxx-bootctl-inst disable --slot b",
		"uc-pxx-bootctl-inst arm-trying --slot b --lower-keeper",
		", after " + final + ": ok, the plan's state after the step",
		"<5>uc-pxx-step2: plan " + id + " end: A/B first 32 read back " + final,
	} {
		if !strings.Contains(string(k), want) || !strings.Contains(out.String(), strings.TrimPrefix(want, "<5>uc-pxx-step2: ")) {
			t.Fatalf("the kernel log or the output has no %q\nkmsg:\n%s\nout:\n%s", want, k, out.String())
		}
	}
	n := 0
	for _, l := range strings.Split(string(k), "\n") {
		if strings.Contains(l, " step ") && strings.Contains(l, "read back before ") {
			n++
		}
	}
	bc := 0
	for _, s := range p.Steps {
		if s.Op == "bootctl" || s.Op == "check-ab" {
			bc++
		}
	}
	if n != bc {
		t.Fatalf("%d step lines in the kernel log, the plan has %d bootctl and check-ab steps\n%s", n, bc, k)
	}
}
