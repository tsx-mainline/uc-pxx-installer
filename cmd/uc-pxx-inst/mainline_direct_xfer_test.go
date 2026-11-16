//go:build install && devtest

package main

// mainline-rc-10a (unit 4, 2026-11-16): the children of the direct path with
// step 1 over the USB cable, as the flow runs them, through the devtest binary
// on a fake factory-stock unit after the BCB-route entry: the stash child with
// the sealed firmware (--fw FILE --fw-expect ID), then a schema-3 bundle
// sealed in /run/uc-pxx-inst/xfer-stage/bundle (rootfs.ext4 unpacked, the
// files 0400, as `uc-pxx-xfer inst` seals it), the plan child (--json
// flow/plan.json --direct) and the install child (--plan flow/plan.json
// --direct --replace-target-confirmed): RC-10 refused there at once ("the
// plan's mainline bundle: manifest.json: sha256 X, the plan's is X").

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/device/devicetest"
	"uc-pxx-installer/internal/drivestate"
	"uc-pxx-installer/internal/flowio"
	"uc-pxx-installer/internal/mainline"
	"uc-pxx-installer/internal/pcdfw"
	"uc-pxx-installer/internal/plan"
	"uc-pxx-installer/internal/xfer/handover"
)

func TestMainlineCLIDirectXferStageSchema3(t *testing.T) {
	bin := bootctlBin(t)
	dir := t.TempDir()
	src, err := fixture.Image(dir, "src.img", dUnitReset)
	if err != nil {
		t.Fatal(err)
	}
	r, err := devicetest.New(filepath.Join(dir, "root"), src, small, bin, devicetest.Opts{})
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(src)
	env := []string{"TMPDIR=" + dir}
	dev := []string{"--device", "/dev/mmcblk0", "--test-root", r.Dir, "--test-fake-unit"}
	before, _ := r.Image()

	// step 1, the firmware stage: the sealed firmware.bin; the stash child
	fwDir := r.Path(filepath.Join(handover.InstStage, handover.InstFWDir))
	os.MkdirAll(fwDir, 0o755)
	fw := filepath.Join(fwDir, handover.FirmwareName)
	if err := os.WriteFile(fw, fixture.FW, 0o400); err != nil {
		t.Fatal(err)
	}
	id, err := drivestate.Stat(fw)
	if err != nil {
		t.Fatal(err)
	}
	kp := filepath.Join(dir, "p10-keys.txt") // the fake unit's systems have no updater library
	if err := pcdfw.WriteKeyFile(kp, fixture.Keys); err != nil {
		t.Fatal(err)
	}
	out, code := runBin(t, env, append(append([]string{"plan", "--mainline-stash"}, dev...), "--fw", fw, "--fw-expect", id.String(), "--keys", kp)...)
	if code != 0 || !strings.Contains(out, "RESULT: stash done") {
		t.Fatalf("the stash child: exit %d\n%s", code, out)
	}
	os.Remove(fw) // the service removes the firmware before the bundle stage

	// the bundle stage: a schema-3 bundle sealed by the root side
	b, _ := mlBundleFor(t)
	b.Manifest["schema"] = mainline.SchemaZst
	b.Manifest["rootfs_zst"] = map[string]any{"file": mainline.RootfsZstFile, "size": 4321,
		"sha256": "4444444444444444444444444444444444444444444444444444444444444444"}
	host, err := b.Write(filepath.Join(dir, "host")) // --test-review-bundle: the TEST gate lists it
	if err != nil {
		t.Fatal(err)
	}
	bd := r.Path(filepath.Join(handover.InstStage, handover.InstBundleDir))
	os.MkdirAll(bd, 0o755)
	for n, data := range map[string][]byte{mainline.ManifestFile: b.ManifestJSON(), mainline.TemplateFile: b.Template,
		mainline.RootfsFile: b.Rootfs} {
		if err := os.WriteFile(filepath.Join(bd, n), data, 0o400); err != nil {
			t.Fatal(err)
		}
	}
	os.MkdirAll(r.Path(flowio.FlowDir), 0o755)
	pj := r.Path(flowio.FlowDir + "/plan.json")
	common := append(append([]string{}, dev...), "--mainline", bd, "--prereqs-confirmed", "--panel-confirmed", "--unlock-confirmed",
		"--test-review-bundle", host, "--test-review-slots", "ab")

	// the plan child
	out, code = runBin(t, env, append(append([]string{"plan"}, common...), "--json", pj, "--direct")...)
	if code != 0 || !strings.Contains(out, "RESULT: plan OK (nothing written)") {
		t.Fatalf("the plan child: exit %d\n%s", code, out)
	}
	raw, err := os.ReadFile(pj)
	if err != nil {
		t.Fatal(err)
	}
	p, err := plan.DirectPlanOf(raw)
	if err != nil || p.Mainline == nil || p.Mainline.Manifest.Schema != mainline.SchemaZst || p.Mainline.Manifest.RootfsZst == nil ||
		!strings.Contains(string(raw), `"rootfs_zst"`) {
		t.Fatalf("plan.json: %v %+v", err, p)
	}
	if after, _ := r.Image(); !bytes.Equal(after, before) {
		t.Fatal("the plan child changed the unit")
	}

	// a plan.json whose rootfs_zst was changed (the manifest sha256 kept, the
	// id sealed again): the install child refuses before any write
	bad := *p
	ml := *p.Mainline
	z := *ml.Manifest.RootfsZst
	z.SHA256 = strings.Repeat("5", 64)
	ml.Manifest.RootfsZst = &z
	bad.Mainline = &ml
	bad.Seal()
	bj := r.Path(flowio.FlowDir + "/plan-bad.json")
	if jb, err := json.MarshalIndent(&bad, "", "  "); err != nil || os.WriteFile(bj, append(jb, '\n'), 0o644) != nil {
		t.Fatal(err)
	}
	inst := func(planFile, log string) (string, int) {
		return runBin(t, env, append(append([]string{"install"}, common...), "--plan", planFile, "--yes", "--log", r.Path(log),
			"--direct", plan.ReplaceFlag)...)
	}
	out, code = inst(bj, "/run/uc-pxx-inst/bad.jsonl")
	if code != 1 || !strings.Contains(out, "manifest.json: sha256 "+p.Mainline.ManifestSHA256+" as the plan's, but its fields are not the plan's") {
		t.Fatalf("the install child with a changed plan: exit %d\n%s", code, out)
	}
	if after, _ := r.Image(); !bytes.Equal(after, before) {
		t.Fatal("the refused install changed the unit")
	}

	// the install child of the saved plan: to the arm and the end state
	out, code = inst(pj, "/run/uc-pxx-inst/mainline.jsonl")
	if code != 0 || !strings.Contains(out, "RESULT: install done") {
		t.Fatalf("the install child: exit %d\n%s", code, out)
	}
	t.Logf("install child:\n%s", out)
	after, _ := r.Image()
	a, err := avbab.Parse(after[plan.ABLBA*512:])
	if err != nil || a.Validate() != nil || a.Pick() != 1 || a.Slots[1] != (avbab.Slot{Priority: 15, Tries: 7}) ||
		a.Slots[0] != (avbab.Slot{Priority: 14, Successful: 1}) {
		t.Fatalf("A/B after the install: %v %v (%v)", a.Slots[0], a.Slots[1], err)
	}
}
