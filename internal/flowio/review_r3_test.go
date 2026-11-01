package flowio

// The runner side of the FLOW-R3-REVIEW findings, with the reviewer's
// probe scenarios RP1, RP3 and RP7: the real runner, supervisor and
// state.json, and a fake p10inst whose repair and switch log their argv
// and write the device mode's files into the log folder. Nothing here
// touches a disk.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"uc-pxx-installer/internal/drivestate"
	"uc-pxx-installer/internal/fatfs"
	"uc-pxx-installer/internal/flow"
)

// tools adds repair and switch to the fake p10inst of newRig. A dry run
// ends with dry, a --yes with yes (3: a write line, then the state not
// known). Each run logs its argv to calls.txt and writes TAG.jsonl,
// TAG.out and TAG-ab-before.bin into its --log folder.
func (g *rig) tools(dry, yes string) string {
	g.t.Helper()
	exe := g.r.c.Exe
	b, err := os.ReadFile(exe)
	if err != nil {
		g.t.Fatal(err)
	}
	calls := filepath.Join(filepath.Dir(exe), "calls.txt")
	add := `repair|switch)
	K=$1
	echo "$*" >> "` + calls + `"
	LOG=; DRY=
	for a in "$@"; do [ "$prev" = --log ] && LOG=$a; [ "$a" = --dry-run ] && DRY=1; prev=$a; done
	TAG=$K-$(date -u +%Y%m%dT%H%M%S).$(printf %03d $(($$ % 1000)))Z
	echo '{"event":"start"}' > "$LOG/$TAG.jsonl"
	echo "$K transcript" > "$LOG/$TAG.out"
	head -c 512 /dev/zero > "$LOG/$TAG-ab-before.bin"
	if [ -n "$DRY" ]; then
		echo "REPAIR-CLASS: invalid: the CRC is wrong"; echo "RESULT: dry run: a write is needed"; exit ` + dry + `
	fi
	echo "$(echo $K | tr a-z A-Z)-WRITE: 1: the A/B sector is written now"
	echo "STOP: the A/B sector is not the prediction"; echo "RESULT: not known"; exit ` + yes + ` ;;
esac`
	s := strings.Replace(string(b), "esac\nexit 9", add+"\nexit 9", 1)
	if s == string(b) {
		g.t.Fatal("cannot add the tools to the fake p10inst")
	}
	if err := os.WriteFile(exe, []byte(s), 0o755); err != nil {
		g.t.Fatal(err)
	}
	return calls
}

func calls(p string) []string {
	b, _ := os.ReadFile(p)
	if s := strings.TrimSpace(string(b)); s != "" {
		return strings.Split(s, "\n")
	}
	return nil
}

func bodyText(m *flow.Model) string {
	var b strings.Builder
	for _, l := range m.View(time.Now()).Body {
		b.WriteString(l.Text + "\n")
	}
	return b.String()
}

// newUI: the UI process is replaced (a crash, a kill, the respawn).
func (g *rig) newUI() *rig {
	m := flow.New("t", true)
	m.TerminalAnswersPanel = true
	g2 := &rig{t: g.t, root: g.root, misc: g.misc, m: m}
	g2.r = New(g.r.c, m)
	g2.r.Start()
	g2.t.Cleanup(func() {
		end := time.Now().Add(10 * time.Second)
		for (g2.m.Running() != "" || g2.r.child != nil) && time.Now().Before(end) {
			select {
			case f := <-g2.r.Events():
				f()
			case <-time.After(50 * time.Millisecond):
			}
			g2.r.Poll()
		}
	})
	return g2
}

// toDeadRepair: install exit 3 → the hold → the dry run (exit 5) → Enter,
// y → repair --yes exit 3.
func (g *rig) toDeadRepair() {
	g.t.Helper()
	g.toInstall()
	g.end()
	g.pump(func() bool { return g.m.Running() == "" && strings.Contains(bodyText(g.m), "can be repaired") }, 20*time.Second)
	g.key(flow.KeyOK)
	g.key(flow.KeyYes)
	g.pump(func() bool {
		return g.m.Running() == "" && strings.Contains(bodyText(g.m), "The repair did not finish")
	}, 20*time.Second)
}

// TestR3RepairDeadNewUI (M-1; the probe RP1): after repair --yes exit 3, a
// new UI runs no repair (not even the dry run), offers none, and shows the
// keep-powered screen; also when state.json is gone (the child folder of
// the write is found).
func TestR3RepairDeadNewUI(t *testing.T) {
	g := newRig(t, xx, "exit 3")
	cf := g.tools("5", "3")
	g.toDeadRepair()
	before := calls(cf)
	if len(before) != 2 || !strings.HasSuffix(before[0], "--dry-run") || !strings.HasSuffix(before[1], "--yes") {
		t.Fatalf("calls %q", before)
	}
	check := func(name string, g2 *rig) {
		t.Helper()
		g2.pump(func() bool { return g2.m.Unit != nil }, 10*time.Second)
		time.Sleep(time.Second)
		g2.pump(func() bool { return g2.m.Running() == "" }, 10*time.Second)
		body := bodyText(g2.m)
		if c := calls(cf); len(c) != len(before) {
			t.Fatalf("%s: the new UI ran %q", name, c[len(before):])
		}
		if g2.m.Screen != flow.ScreenHold || !strings.Contains(body, "KEEP THE UNIT POWERED") ||
			!strings.Contains(body, "The repair did not finish") || strings.Contains(body, "can be repaired") ||
			g2.m.RestartBlocked() == "" {
			t.Fatalf("%s: screen %s:\n%s", name, g2.m.Screen, body)
		}
		g2.key(flow.KeyOK) // only a read of the sector
		g2.key(flow.KeyYes)
		g2.pump(func() bool { return strings.Contains(bodyText(g2.m), "Checked again:") }, 10*time.Second)
		if c := calls(cf); len(c) != len(before) {
			t.Fatalf("%s: OK ran %q", name, c[len(before):])
		}
	}
	check("new UI", g.newUI())
	// state.json lost: the write's child folder says the end is not known
	os.Remove(filepath.Join(g.root, FlowDir, "state.json"))
	g3 := g.newUI()
	check("no state.json", g3)
	if !strings.Contains(strings.Join(g3.r.log, "\n"), "a repair write of this boot has no known end") {
		t.Fatal("the disk scan did not say it")
	}
}

// TestR3SwitchHoldStalePlan (H-1, M-2; the probes RP3, RP7): a plan shown
// and canceled (never installed), then Switch → the dry run (exit 5) →
// switch --yes exit 3: the hold says keep the unit powered; neither this UI
// nor a new one runs the repair (with that plan); the switch gets its own
// log bundle with its files.
func TestR3SwitchHoldStalePlan(t *testing.T) {
	g := newRig(t, d2, "exit 0")
	cf := g.tools("5", "3")
	g.r.Start()
	g.pump(func() bool { return g.m.Unit != nil }, 20*time.Second)
	g.key(flow.KeyNext)
	g.key(flow.KeyOK)
	g.key(flow.KeyOK) // eject
	g.pump(func() bool { return g.m.Screen == flow.ScreenPanel }, 30*time.Second)
	g.key(flow.KeyYes)
	g.pump(func() bool { return g.m.Screen == flow.ScreenPlan }, 30*time.Second)
	g.key(flow.KeyBack) // canceled: never installed
	g.pump(func() bool { return g.m.Screen == flow.ScreenStatus }, 10*time.Second)
	g.key(flow.KeyNext) // Status → Install → Switch
	g.key(flow.KeyNext)
	if g.m.Sel != 2 {
		t.Fatalf("menu %d", g.m.Sel)
	}
	g.key(flow.KeyOK)
	if g.m.Screen != flow.ScreenSwitch {
		t.Fatalf("screen %s (%s)", g.m.Screen, g.m.Msg)
	}
	g.key(flow.KeyOK)
	g.key(flow.KeyYes)
	if g.m.Screen != flow.ScreenSwitching {
		t.Fatalf("screen %s", g.m.Screen)
	}
	g.pump(func() bool { return g.m.Screen == flow.ScreenHold && g.m.Running() == "" && g.m.Bundle != "" }, 30*time.Second)
	body := bodyText(g.m)
	if !strings.HasPrefix(body, "!!! KEEP THE UNIT POWERED. DO NOT RESTART IT.\nThe switch did not finish") ||
		strings.Contains(body, "can be disconnected") || g.m.View(time.Now()).LED != "red" {
		t.Fatalf("the hold after the switch exit 3:\n%s", body)
	}
	c := calls(cf)
	if len(c) != 2 || !strings.HasPrefix(c[0], "switch ") || !strings.HasSuffix(c[0], "--dry-run") || !strings.HasSuffix(c[1], "--yes") {
		t.Fatalf("calls %q", c)
	}
	// OK: a read only; and the runner refuses a repair action here
	g.key(flow.KeyOK)
	g.key(flow.KeyYes)
	g.pump(func() bool { return strings.Contains(bodyText(g.m), "the slot state is still not known") }, 10*time.Second)
	g.r.Do([]flow.Action{{Kind: flow.ActRepairCheck}})
	g.pump(func() bool { return g.m.Running() == "" }, 10*time.Second)
	if !strings.Contains(strings.Join(g.r.log, "\n"), "repair not started: no plan of the install that ended on this hold") {
		t.Fatal("the runner started a repair in a switch hold")
	}
	// the switch's bundle, with its files
	g.checkDriveFolder("p10-switch-", regexp.MustCompile(`^switch-.*\.jsonl$`), "result.txt", "flow.log")
	// a new UI: the same hold, no repair with the old plan
	g2 := g.newUI()
	g2.pump(func() bool { return g2.m.Unit != nil }, 10*time.Second)
	time.Sleep(time.Second)
	g2.pump(func() bool { return g2.m.Running() == "" }, 10*time.Second)
	if c2 := calls(cf); len(c2) != len(c) {
		t.Fatalf("the new UI ran %q", c2[len(c):])
	}
	if b := bodyText(g2.m); g2.m.Screen != flow.ScreenHold || !strings.Contains(b, "The switch did not finish") {
		t.Fatalf("new UI: %s\n%s", g2.m.Screen, b)
	}
}

// checkDriveFolder: the drive is with the host and holds one folder with
// the prefix, with a file that matches rx and the named files.
func (g *rig) checkDriveFolder(prefix string, rx *regexp.Regexp, names ...string) {
	g.t.Helper()
	if v, _ := os.ReadFile(g.lunFile()); strings.TrimSpace(string(v)) != filepath.Join(g.root, drivestate.Image) {
		g.t.Fatalf("the drive is not with the host: lun %q", v)
	}
	fs, err := fatfs.OpenImage(filepath.Join(g.root, drivestate.Image))
	if err != nil {
		g.t.Fatal(err)
	}
	defer fs.Close()
	top, _ := fs.ReadDir("/")
	if len(top) != 1 || !strings.HasPrefix(top[0].Name, prefix) {
		g.t.Fatalf("drive: %+v", top)
	}
	es, _ := fs.ReadDir("/" + top[0].Name)
	have := map[string]bool{}
	match := false
	for _, e := range es {
		have[e.Name] = true
		match = match || rx.MatchString(e.Name)
	}
	for _, n := range names {
		if !have[n] {
			g.t.Fatalf("the folder %s has no %s: %v", top[0].Name, n, have)
		}
	}
	if !match {
		g.t.Fatalf("the folder %s has no file like %s: %v", top[0].Name, rx, have)
	}
}

// TestR3RepairBundle (L-7): after the repair, the install's bundle (in the
// run folder and on the drive) holds the repair's own files and the flow
// log of then.
func TestR3RepairBundle(t *testing.T) {
	g := newRig(t, xx, "exit 3")
	g.tools("5", "3")
	g.toDeadRepair()
	g.pump(func() bool { return strings.Count(strings.Join(g.r.log, "\n"), "bundle p10-install-") >= 3 }, 20*time.Second)
	for _, rx := range []string{`^repair-.*\.jsonl$`, `^repair-.*\.out$`, `^repair-.*-ab-before\.bin$`} {
		g.checkDriveFolder("p10-install-"+testPlan().ID[:12]+"-", regexp.MustCompile(rx), "result.txt", "flow.log", "ab-sector.txt")
	}
	ents, _ := os.ReadDir(filepath.Join(g.root, RunDir, g.r.st.BundleName))
	n := 0
	for _, e := range ents {
		if toolFile.MatchString(e.Name()) {
			n++
		}
	}
	if n != 6 { // 2 runs × (.jsonl, .out, -ab-before.bin)
		t.Fatalf("%d repair files in the run copy: %v", n, ents)
	}
	fl, _ := os.ReadFile(filepath.Join(g.root, RunDir, g.r.st.BundleName, "flow.log"))
	if !strings.Contains(string(fl), "after the repair (dry run false)") {
		t.Fatalf("the flow log of the bundle is the old one:\n%s", fl)
	}
	// a new UI, a check again: the bundle from the run copy, with the new files
	g2 := g.newUI()
	g2.pump(func() bool { return g2.m.Unit != nil && g2.m.Running() == "" }, 10*time.Second)
	if g2.r.st.BundleName != g.r.st.BundleName {
		t.Fatalf("bundle name %q", g2.r.st.BundleName)
	}
	if _, ok := g2.r.repairBundle(); !ok {
		t.Fatal("a new UI cannot build the repair bundle from the run copy")
	}
}
