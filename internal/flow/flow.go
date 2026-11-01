// Package flow is the on-screen install flow of the installer RAM image
// (INSTALLER-END-USER-PLAN.md §2): the screens, the keys, the choice of the
// target slot, how the end of the install child is read, and what the
// screens say. It is a state machine with no I/O at all: it imports no
// package that opens a file, runs a program or makes a system call
// (ci/check-no-writer.py checks its imports). The runner (package flowio)
// does every read and every start of a child, and gives the results back as
// events; the model answers with Actions.
//
// The flow never writes the eMMC. The only program that does is the device
// mode, `p10inst install --device /dev/mmcblk0`, which the runner starts as
// a child after the hold of OK on the plan screen (InstallArgs). The flow
// adds no write path: it chooses the arguments, follows the output, and
// reads the A/B sector after the end.
//
// Screens: Status (with the menu), Firmware, Target (which slot to install
// to; only when the planner asks), Planning (the plan child), Plan,
// Progress, Done, Stopped, Hold, Refused (nothing written), Log, Restart.
// The checklist of earlier rounds is gone (owner, 2026-10-31): the
// preparation is "Before you start" in the install guide; the auto-update
// gate is on the Install item of the status screen.
package flow

import (
	"fmt"
	"strings"
	"time"

	"uc-pxx-installer/internal/avbab"
	"uc-pxx-installer/internal/plan"
	"uc-pxx-installer/internal/status"
)

// Key is one input. The runner maps the touch keys (Vol+ Prev, Vol- Next,
// Mic OK, Speaker Back) and the terminal keys to it.
type Key int

const (
	KeyNone Key = iota
	KeyPrev
	KeyNext
	KeyOK
	KeyBack
	KeyYes // a terminal only: y confirms where the device keys hold OK
)

// Event is a key press (Down) or release.
type Event struct {
	Key  Key
	Down bool
}

// Screen is one screen of the flow.
type Screen int

const (
	ScreenStatus Screen = iota + 1
	ScreenFirmware
	ScreenTarget
	ScreenPlanning
	ScreenPlan
	ScreenProgress
	ScreenDone
	ScreenStopped
	ScreenHold
	ScreenRefused
	ScreenLog
	ScreenRestart
	ScreenRepair // the repair of the slot table runs (from the hold)
	ScreenSafe   // after the hold: the slot state was checked and is safe
	ScreenSwitch // "Switch which slot starts" (the status menu)
	ScreenPanel  // "Do you see this screen on the unit's panel?" (before the plan)
)

var screenNames = map[Screen]string{ScreenStatus: "status", ScreenFirmware: "firmware",
	ScreenTarget: "target", ScreenPlanning: "planning", ScreenPlan: "plan", ScreenProgress: "progress",
	ScreenDone: "done", ScreenStopped: "stopped", ScreenHold: "hold", ScreenRefused: "refused", ScreenLog: "log",
	ScreenRestart: "restart", ScreenRepair: "repair", ScreenSafe: "safe", ScreenSwitch: "switch", ScreenPanel: "panel"}

func (s Screen) String() string { return screenNames[s] }

// HoldOK is how long OK must be held to start the install or to restart.
const HoldOK = 3 * time.Second

// ActionKind is what the model asks the runner to do.
type ActionKind int

const (
	ActEject         ActionKind = iota + 1 // eject the drive from the host now (OK on the firmware screen)
	ActReadDrive                           // the drive is ejected: read the firmware file (read-only)
	ActGiveBackDrive                       // give the drive back to the host (Remake: a new empty volume first)
	ActStartPlan                           // run the plan child (Keeper, Unclear)
	ActCancelPlan                          // stop the plan child (it writes nothing)
	ActStartInstall                        // run the install child with the plan that was shown
	ActReadAB                              // the hold screen: read the A/B sector again (U1)
	ActRestart                             // restart the unit (only when allowed)
	ActRepairCheck                         // the hold: p10inst repair --dry-run (reads only)
	ActRepair                              // the hold: p10inst repair --yes (after the 3 s hold)
	ActSwitch                              // the status menu: p10inst switch --to Slot (dry run, then --yes; after the 3 s hold)
)

func (k ActionKind) String() string {
	return [...]string{"", "eject", "read-drive", "give-back-drive", "start-plan", "cancel-plan", "start-install",
		"read-ab", "restart", "repair-check", "repair", "switch"}[k]
}

// Action is one request to the runner.
type Action struct {
	Kind    ActionKind
	Keeper  string // ActStartPlan: "", "a" or "b"
	Unclear bool   // ActStartPlan: the user confirmed a not clear auto-update state
	Remake  bool   // ActGiveBackDrive: make a new, empty volume first (after a dirty or damaged drive)
	Slot    string // ActSwitch: the slot that is to start next ("a" or "b")
}

// ToolResult is the end of a repair or switch child (p10inst repair,
// p10inst switch), as the runner read it: the exit, the lines it follows,
// and the flow's own read of the A/B sector afterwards.
type ToolResult struct {
	Tool   string // "repair" or "switch"
	DryRun bool
	End    End
	Safe   bool   // a REPAIR-SAFE: yes / SWITCH-SAFE: yes line (exit 0)
	Class  string // REPAIR-CLASS
	Result string // the RESULT line
	Stop   string // a STOP line
	After  []byte // the A/B sector read by the flow after the end (nil: not readable)
}

// Exit is the exit status (-1 for a signal or a lost end).
func (r ToolResult) Exit() int {
	if !r.End.Exited {
		return -1
	}
	return r.End.Code
}

// AutoUpdateView is the auto-update setting as the status read it.
type AutoUpdateView struct {
	State  string // off, on, unknown
	URL    string
	Detail string // the part and counter, or why it is not clear
}

// SlotView is one slot for the screens.
type SlotView struct {
	Name    string // "a" or "b"
	AB      avbab.Slot
	Summary string
}

// Unit is what the flow needs from the status.
type Unit struct {
	ABValid    bool
	ABErr      string
	Next       int // -1 none
	Slots      [2]SlotView
	AutoUpdate AutoUpdateView
	Lines      []status.Line // the status screen (statusLines)
}

// UnitFrom takes the parts of a status report that the flow uses.
func UnitFrom(r *status.Report) Unit {
	u := Unit{ABValid: r.ABValid, ABErr: r.ABErr, Next: r.Next}
	for i := range r.Slots {
		u.Slots[i] = SlotView{Name: r.Slots[i].Name, AB: r.AB.Slots[i], Summary: r.Slots[i].Summary}
	}
	au := r.AutoUpdate
	u.AutoUpdate = AutoUpdateView{State: au.State, URL: au.URL, Detail: au.Why}
	if au.Part >= 0 && au.State != "unknown" {
		u.AutoUpdate.Detail = fmt.Sprintf("nvram part %d, write counter %d", au.Part, au.Counter)
	}
	u.Lines = statusLines(r, u)
	return u
}

// keptWord is the status's key record state in plain words: yes or no
// (anything it could not read is "not known").
func keptWord(k string) string {
	switch {
	case k == "yes":
		return "yes"
	case strings.HasPrefix(k, "no"):
		return "no"
	}
	return "not known"
}

// statusLines is the status for the user of the install image: what the
// unit is, what each slot holds and does, the bootloader, Crestron
// auto-update, the eMMC read-only flag, and the problems last. Each part
// has a fixed number of rows; only the problems at the end vary. (The
// status image's own text, status.Report.Lines, is for the status image.)
func statusLines(r *status.Report, u Unit) []status.Line {
	var out []status.Line
	add := func(level int, f string, a ...any) {
		out = append(out, status.Line{Level: level, Text: fmt.Sprintf(f, a...)})
	}
	q := func(s string) string {
		if s == "" {
			return "unknown"
		}
		return s
	}
	add(levelHeading, "Unit")
	add(levelNormal, "  Model          %s", q(r.Model))
	add(levelNormal, "  Serial         %s", q(r.Serial))
	add(levelNormal, "  MAC            %s", q(r.MAC))
	add(levelNormal, "  Installer      image %s, kernel %s", q(r.ImageToken), q(r.Kernel))
	add(levelNormal, "")
	add(levelHeading, "Slots")
	var problems []string
	for i, s := range r.Slots {
		mark, role := "  ", ""
		switch {
		case i == r.Next:
			mark, role = "> ", "starts next"
		case i == r.Fallback:
			role = "fallback"
		}
		st, lvl := plainState(s.AB), levelGood
		if s.AB.State() != "successful" {
			lvl = levelWarn
		}
		if !r.ABValid {
			st, role, lvl = "unknown", "", levelBad
		}
		add(lvl, "%sSlot %s  %-12s %-30s %s", mark, slotWord(s.Name), role, st, orQ(s.Summary))
		for _, e := range s.Errs {
			problems = append(problems, "slot "+slotWord(s.Name)+": "+e)
		}
	}
	if !r.ABValid {
		problems = append(problems, "the slot table cannot be read: "+r.ABErr)
	}
	add(levelNormal, "")
	lvl := levelNormal
	if r.Unlock == "unlocked" {
		lvl = levelGood
	}
	add(lvl, "%-30s %s", "Bootloader", q(r.Unlock))
	switch u.AutoUpdate.State {
	case "off":
		add(levelGood, "%-30s off", "Crestron auto-update")
	case "on":
		add(levelBad, "%-30s ON: turn it off in the Crestron web UI, then start the installer again", "Crestron auto-update")
	default:
		add(levelBad, "%-30s could not be read: check in the Crestron web UI that it is off", "Crestron auto-update")
	}
	add(levelNormal, "%-30s %s", "Firmware key kept on the unit", keptWord(r.KeyKept))
	add(levelNormal, "%-30s %s", "eMMC read-only flag", q(r.ReadOnly))
	problems = append(problems, r.Errs...)
	if len(problems) > 0 {
		add(levelNormal, "")
		add(levelHeading, "Problems")
		for _, p := range problems {
			add(levelBad, "  ! %s", p)
		}
	}
	return out
}

// DriveResult is the read of the firmware file on the ejected drive.
type DriveResult struct {
	OK      bool
	Name    string // the file name, quoted for the screen
	Size    int64
	Ident   string // drivestate.Ident of the drive file at the read
	Refusal string // why the drive is refused (fatfs.Refusal.Why)
	Remake  bool   // the drive must be made again before it goes back (dirty or damaged)
	KeyFile bool   // the drive has the key file p10-keys.txt (its content is never shown)
}

// PlanResult is the end of the plan child.
type PlanResult struct {
	Plan     *plan.Plan // nil on a refusal
	Refusal  string     // the RESULT text of a refusal
	Canceled bool
	End      End
}

// Model is the state of the flow.
type Model struct {
	Title    string
	Terminal bool // keys from a terminal: Enter, then y, in place of a hold

	Screen Screen
	prev   Screen // the screen to go back to from the log
	Sel    int
	Scroll int
	Msg    string

	Unit *Unit

	// installOK: Install was chosen with auto-update read as off; the
	// status screen says that Install means "Before you start" is done
	// (--prereqs-confirmed)
	installOK bool
	// panelOK: the user answered yes to "Do you see this screen on the
	// unit's panel?" (--panel-confirmed, FIRMWARE-GENERAL §11.5); kept for
	// the whole run, also by a UI that is started again
	panelOK bool

	// the drive and the firmware
	Ejected       bool
	reading       bool
	Drive         *DriveResult
	refused       string // the last refusal of the drive or the plan, in plain words
	refusedDetail string // the device mode's own text of a refusal (the details row)

	// the keeper
	keeperAsked bool   // the planner asked for the keeper
	Keeper      string // the user's choice ("" = the planner's rule)
	keeperWhy   string

	// the plan and the install
	Plan    *plan.Plan
	Prog    *Progress
	running string // "", "plan" or "install": a child runs
	Outcome *Outcome
	recheck string // the result of the last "read again" on the hold screen
	// recheckKnown: that read was a known state (S0, D1, D2)
	recheckKnown bool
	// the repair of the hold (devmode-r4): its state, the reason of a
	// refusal, the tries (at most 2: one more after a refusal)
	repair      repairState
	repairWhy   string
	repairTries int
	// after the hold: the slot state that the repair check read as safe
	safeText string
	// the switch: the slot it would make start; its result
	switchTo   string
	recheckWhy string // the detail of that read, for the log row
	Bundle     string // where the log bundle is, for the end screens
	everHeld   bool   // a hold happened in this RAM image: restart stays blocked

	copyStart, copyLast time.Time // the host writes the drive

	okDown  time.Time
	okFor   ActionKind
	confirm ActionKind // terminal: waiting for y

	actions []Action
}

// New starts the flow on the status screen.
func New(title string, terminal bool) *Model {
	return &Model{Title: title, Terminal: terminal, Screen: ScreenStatus}
}

// Take returns the actions asked since the last call.
func (m *Model) Take() []Action {
	a := m.actions
	m.actions = nil
	return a
}

func (m *Model) ask(a Action) {
	if a.Kind == ActGiveBackDrive {
		m.Ejected = false // until the runner reports the next eject
	}
	m.actions = append(m.actions, a)
}

// Running is "plan" or "install" while a child runs.
func (m *Model) Running() string { return m.running }

// RestartBlocked says why the restart is not allowed now, or "".
func (m *Model) RestartBlocked() string {
	switch {
	case m.running == "install":
		return "an install runs"
	case m.running == "repair":
		return "the repair runs"
	case m.running == "switch":
		return "the switch runs"
	case m.everHeld || m.Screen == ScreenHold:
		return "the install did not end normally: keep the unit powered"
	case m.running == "plan":
		return "the check runs: wait for it, or cancel it"
	}
	return ""
}

// SetTerminal switches the keys between the device keys (the 3 s hold of
// OK) and a terminal (Enter, then y): the flow UI does it while a
// take-over terminal sends the keys. A hold or a confirmation that was
// started is dropped.
func (m *Model) SetTerminal(t bool) {
	if m.Terminal != t {
		m.Terminal, m.okDown, m.okFor, m.confirm = t, time.Time{}, 0, 0
	}
}

// SetUnit gives the status of the unit (at the start and after each end).
func (m *Model) SetUnit(u Unit) { m.Unit = &u }

var statusMenu = []string{"Status", "Install", "Switch", "Log", "Restart"}

func (m *Model) go2(s Screen) {
	m.Screen, m.Sel, m.Scroll, m.okDown, m.okFor, m.confirm = s, 0, 0, time.Time{}, 0, 0
}

// Handle applies one key event.
func (m *Model) Handle(e Event, now time.Time) {
	if e.Key == KeyOK && !e.Down {
		m.okDown, m.okFor = time.Time{}, 0
		return
	}
	if !e.Down {
		return
	}
	m.Msg = ""
	if e.Key == KeyYes && m.Screen != ScreenPanel {
		m.yes()
		return
	}
	if e.Key != KeyOK {
		m.confirm = 0
	}
	switch m.Screen {
	case ScreenStatus:
		m.keyStatus(e.Key)
	case ScreenFirmware:
		m.keyFirmware(e.Key)
	case ScreenTarget:
		m.keyTarget(e.Key)
	case ScreenPlanning:
		m.keyScroll(e.Key)
		if e.Key == KeyBack && m.running == "plan" {
			m.ask(Action{Kind: ActCancelPlan})
			m.Msg = "Stopping the check ... (nothing was written)"
		}
	case ScreenPlan:
		switch e.Key {
		case KeyBack:
			m.cancel()
		case KeyOK:
			m.startHold(ActStartInstall, now)
		default:
			m.keyScroll(e.Key)
		}
	case ScreenProgress:
		// no scrolling: the live safety line stays on the screen
		if e.Key == KeyBack || e.Key == KeyOK {
			m.Msg = txtProgressKey
		}
	case ScreenDone:
		switch e.Key {
		case KeyOK:
			m.startHold(ActRestart, now)
		case KeyBack:
			m.go2(ScreenStatus)
		default:
			m.keyScroll(e.Key)
		}
	case ScreenStopped, ScreenRefused:
		switch e.Key {
		case KeyOK:
			m.tryAgain()
		case KeyBack:
			m.go2(ScreenStatus)
		default:
			m.keyScroll(e.Key)
		}
	case ScreenHold:
		switch {
		case e.Key == KeyBack:
			m.prev = ScreenHold
			m.go2(ScreenLog)
		case m.running != "":
			m.Msg = txtToolRuns
		case e.Key == KeyOK && m.repair == repairOffer:
			m.startHold(ActRepair, now) // a write: the 3 s hold
		case e.Key == KeyOK, e.Key == KeyNext && m.repair == repairOffer:
			m.ask(Action{Kind: ActReadAB})
			m.Msg = txtCheckingAgain
		}
	case ScreenRepair:
		if e.Key == KeyBack || e.Key == KeyOK {
			m.Msg = txtRepairKey
		}
	case ScreenSafe:
		switch e.Key {
		case KeyOK:
			m.tryAgain()
		case KeyBack:
			m.go2(ScreenStatus)
		}
	case ScreenSwitch:
		m.keySwitch(e.Key, now)
	case ScreenPanel:
		m.keyPanel(e.Key)
	case ScreenLog:
		switch e.Key {
		case KeyBack, KeyOK:
			back := m.prev
			if back == 0 {
				back = ScreenStatus
			}
			m.go2(back)
		case KeyPrev: // the log opens at its end: up is back in time
			m.Scroll++
		case KeyNext:
			if m.Scroll > 0 {
				m.Scroll--
			}
		}
	case ScreenRestart:
		switch e.Key {
		case KeyBack:
			m.go2(ScreenStatus)
		case KeyOK:
			if why := m.RestartBlocked(); why != "" {
				m.Msg = "Restart is blocked: " + why + "."
				return
			}
			m.startHold(ActRestart, now)
		}
	}
}

func (m *Model) keyScroll(k Key) {
	switch k {
	case KeyPrev:
		if m.Scroll > 0 {
			m.Scroll--
		}
	case KeyNext:
		m.Scroll++
	}
}

func (m *Model) keyStatus(k Key) {
	switch k {
	case KeyPrev:
		m.Sel = (m.Sel + len(statusMenu) - 1) % len(statusMenu)
	case KeyNext:
		m.Sel = (m.Sel + 1) % len(statusMenu)
	case KeyOK:
		switch statusMenu[m.Sel] {
		case "Status":
		case "Install":
			m.openInstall()
		case "Log":
			m.prev = ScreenStatus
			m.go2(ScreenLog)
		case "Restart":
			m.go2(ScreenRestart)
		case "Switch":
			m.openSwitch()
		}
	}
}

// openInstall: the Install menu item. A running child or an end screen
// comes back first; a hold is never left. Then the gates: the slot state
// must be readable; Crestron auto-update must be off (on: refused; not
// clear or not readable: refused too; owner 2026-10-31: the installer only
// verifies that it is off, with no confirmation).
func (m *Model) openInstall() {
	switch {
	case m.running == "install":
		m.go2(ScreenProgress)
	case m.running == "plan":
		m.go2(ScreenPlanning)
	case m.everHeld:
		m.go2(ScreenHold)
	case m.installGate() != "":
		m.Msg = m.installGate()
	default:
		m.installOK = true
		m.Drive, m.refused, m.reading = nil, "", false
		m.keeperAsked, m.Keeper, m.keeperWhy = false, "", ""
		m.go2(ScreenFirmware)
		if m.Ejected {
			m.reading = true
			m.ask(Action{Kind: ActReadDrive})
		}
	}
}

// installGate is why an install cannot start now, or "": the slot table
// must be readable and Crestron auto-update must read as off. Install on
// the status screen and "try again" on an end screen both pass it.
func (m *Model) installGate() string {
	switch {
	case m.Unit == nil:
		return txtNotRead
	case !m.Unit.ABValid:
		return txtABInvalid
	case m.Unit.AutoUpdate.State == "on":
		return txtAutoUpdateOn
	case m.Unit.AutoUpdate.State != "off":
		return txtAutoUpdateUnclear
	}
	return ""
}

// PrereqsConfirmed: the user chose Install. The status screen says that
// Install means the steps of "Before you start" in the install guide are
// done (Crestron auto-update off, the update URL, stable power). It gives
// --prereqs-confirmed to the device mode.
func (m *Model) PrereqsConfirmed() bool { return m.installOK }

// UnclearConfirmed is always false since flow-r3 (owner, 2026-10-31): a not
// clear auto-update state is refused, never confirmed, so the flow never
// gives --autoupdate-unclear-confirmed.
func (m *Model) UnclearConfirmed() bool { return false }

func (m *Model) keyFirmware(k Key) {
	switch k {
	case KeyBack:
		m.cancel()
	case KeyOK:
		if !m.Ejected && !m.reading {
			m.ask(Action{Kind: ActEject})
			m.Msg = "Ejecting the drive ..."
		}
	}
}

// cancel is Back on the screens before the install: nothing is written, the
// drive goes back to the host, and it is not a failure (R2.4 item 4).
func (m *Model) cancel() {
	if m.running == "plan" {
		m.ask(Action{Kind: ActCancelPlan})
		return
	}
	if m.Ejected {
		m.ask(Action{Kind: ActGiveBackDrive})
	}
	m.Plan, m.Drive = nil, nil
	m.go2(ScreenStatus)
	m.Msg = "Canceled. Nothing was written."
}

// tryAgain: OK on the stopped or refused screen goes back to the firmware
// screen (the drive was given back with the log bundle).
func (m *Model) tryAgain() {
	if why := m.installGate(); why != "" {
		m.Msg = why
		return
	}
	// "try again" continues the install the user chose (also in a UI that
	// was started again after the stop): --prereqs-confirmed as before
	m.installOK = true
	m.Plan, m.Drive, m.Outcome, m.refused, m.refusedDetail, m.reading = nil, nil, nil, "", "", false
	m.keeperAsked, m.Keeper, m.keeperWhy = false, "", ""
	m.go2(ScreenFirmware)
	if m.Ejected {
		m.reading = true
		m.ask(Action{Kind: ActReadDrive})
	}
}

// DriveEjected reports the lun state (the runner polls it).
func (m *Model) DriveEjected(ejected bool) {
	was := m.Ejected
	m.Ejected = ejected
	if ejected && !was && m.Screen == ScreenFirmware && m.Drive == nil && !m.reading && m.running == "" {
		m.reading = true
		m.Msg = ""
		m.ask(Action{Kind: ActReadDrive})
	}
}

// DriveWrites reports that the host wrote the drive file since the last
// poll (its modification time moved): the copy runs. The firmware screen
// shows it against the measured copy time.
func (m *Model) DriveWrites(now time.Time) {
	if m.Screen != ScreenFirmware || m.Ejected {
		return
	}
	if m.copyStart.IsZero() || now.Sub(m.copyLast) > 30*time.Second {
		m.copyStart = now
	}
	m.copyLast = now
}

// DriveRead is the result of ActReadDrive.
func (m *Model) DriveRead(r DriveResult) {
	m.reading = false
	if m.Screen != ScreenFirmware {
		return
	}
	if !r.OK {
		m.refused = r.Refusal
		m.Drive = nil
		m.ask(Action{Kind: ActGiveBackDrive, Remake: r.Remake})
		return
	}
	m.Drive = &r
	if !m.panelOK {
		m.go2(ScreenPanel) // the question comes before the plan
		return
	}
	m.startPlan()
}

// PanelConfirmed: the user sees this screen on the unit's panel.
func (m *Model) PanelConfirmed() bool { return m.panelOK }

func (m *Model) keyPanel(k Key) {
	switch k {
	case KeyOK, KeyYes:
		m.panelOK = true
		m.startPlan()
	case KeyBack:
		// no: the installer works only on the panel; nothing was written
		m.refused, m.refusedDetail = txtPanelNo, ""
		m.ask(Action{Kind: ActGiveBackDrive})
		m.go2(ScreenRefused)
	}
}

func (m *Model) startPlan() {
	m.Msg = "" // ("Ejecting the drive ..." is over)
	m.running = "plan"
	m.Prog = nil
	m.ask(Action{Kind: ActStartPlan, Keeper: m.Keeper, Unclear: m.UnclearConfirmed()})
	m.go2(ScreenPlanning)
}

// ChildStarted tells that a child runs (Progress follows its lines).
func (m *Model) ChildStarted(p *Progress) { m.Prog = p }

// ChildLine is one output line of the running child.
func (m *Model) ChildLine(l string, now time.Time) {
	if m.Prog != nil {
		m.Prog.Line(l, now)
	}
}

// keeperRefusal reports a refusal of the planner that the keeper screen
// answers: it asked for the keeper, or the slot that boots next is not
// successful (a re-install over a trying slot is offered only behind the
// keeper screen, DEVICE-MODE-REVIEW.md §6).
func keeperRefusal(r string) (bool, string) {
	// the refusal is "install refused: ID: DETAIL; ID: DETAIL ..." and a
	// detail may hold "; " itself: find the check id "keeper" at the start
	// of an item
	for _, lead := range []string{"install refused: ", "; "} {
		for i := 0; ; {
			j := strings.Index(r[i:], lead+"keeper: ")
			if j < 0 {
				break
			}
			d := r[i+j+len(lead)+len("keeper: "):]
			if k := strings.Index(d, "; keeper-"); k >= 0 {
				d = d[:k]
			}
			if strings.Contains(d, "choose the keeper") || strings.Contains(d, "must be successful") {
				return true, d
			}
			i += j + len(lead)
		}
	}
	return false, ""
}

// PlanEnded is the end of the plan child.
func (m *Model) PlanEnded(r PlanResult) {
	m.running = ""
	m.Msg = "" // a message of the planning screen is not one of the next screen
	switch {
	case r.Canceled:
		if m.Ejected {
			m.ask(Action{Kind: ActGiveBackDrive})
		}
		m.Plan, m.Drive = nil, nil
		m.go2(ScreenStatus)
		m.Msg = "Canceled. Nothing was written."
	case r.Plan != nil:
		m.Plan = r.Plan
		m.go2(ScreenPlan)
	default:
		if ask, why := keeperRefusal(r.Refusal); ask && !m.keeperAsked && m.Keeper == "" {
			m.keeperAsked, m.keeperWhy = true, why
			m.go2(ScreenTarget)
			if m.Unit != nil && m.Unit.Next >= 0 {
				m.Sel = 1 - m.Unit.Next // U7: keep the slot that starts next, so install to the other one
			}
			return
		}
		raw := r.Refusal
		if raw == "" {
			raw = "the plan check ended (" + r.End.String() + ") without a plan"
		}
		m.refused, m.refusedDetail = plainReason(raw), raw
		m.ask(Action{Kind: ActGiveBackDrive})
		m.go2(ScreenRefused)
	}
}

// keyTarget: the screen asks which slot to install to (the planner asked:
// both slots are successful, or the slot that starts next is not). Sel is
// the target; the other slot is kept and must be successful.
func (m *Model) keyTarget(k Key) {
	switch k {
	case KeyPrev, KeyNext:
		m.Sel = 1 - m.Sel
	case KeyBack:
		m.cancel()
	case KeyOK:
		keep := m.Unit.Slots[1-m.Sel]
		if !keep.AB.Keeper() {
			m.Msg = fmt.Sprintf(txtTargetRefused, slotWord(m.Unit.Slots[m.Sel].Name), slotWord(keep.Name), plainState(keep.AB))
			return
		}
		m.Keeper = keep.Name
		m.startPlan()
	}
}

func (m *Model) startHold(a ActionKind, now time.Time) {
	if m.Terminal {
		m.confirm = a
		return
	}
	m.okDown, m.okFor = now, a
}

func (m *Model) yes() {
	a := m.confirm
	m.confirm = 0
	if a == 0 {
		return
	}
	m.fire(a)
}

// Tick checks the OK hold. It returns true when the screen must be drawn.
func (m *Model) Tick(now time.Time) bool {
	if m.okDown.IsZero() {
		return m.running != ""
	}
	if now.Sub(m.okDown) >= HoldOK {
		a := m.okFor
		m.okDown, m.okFor = time.Time{}, 0
		m.fire(a)
	}
	return true
}

func (m *Model) fire(a ActionKind) {
	switch a {
	case ActStartInstall:
		if m.Screen != ScreenPlan || m.Plan == nil || m.running != "" {
			return
		}
		m.running = "install"
		m.Prog = nil
		m.ask(Action{Kind: ActStartInstall})
		m.go2(ScreenProgress)
	case ActRepair:
		if m.Screen != ScreenHold || m.repair != repairOffer || m.running != "" {
			return
		}
		m.repairTries++
		m.running, m.repair = "repair", repairRunning
		m.Prog = NewProgress(false, time.Time{})
		m.ask(Action{Kind: ActRepair})
		m.go2(ScreenRepair)
	case ActSwitch:
		if m.Screen != ScreenSwitch || m.running != "" || m.switchTo == "" || m.switchGate(m.switchTo) != "" {
			return
		}
		m.running = "switch"
		m.ask(Action{Kind: ActSwitch, Slot: m.switchTo})
		m.Msg = txtSwitching
	case ActRestart:
		if why := m.RestartBlocked(); why != "" {
			m.Msg = "Restart is blocked: " + why + "."
			return
		}
		if m.Screen != ScreenRestart && m.Screen != ScreenDone {
			return
		}
		m.ask(Action{Kind: ActRestart})
		m.Msg = "Restarting ..."
	}
}

// InstallEnded is the end of the install child, classified by the runner
// with Classify after `p10inst readonly` and its own read of the sector.
func (m *Model) InstallEnded(o Outcome) {
	m.running = ""
	m.Msg = "" // a key pressed during the install: its message is not one of the end screen
	m.Outcome = &o
	if o.Screen == ScreenHold {
		m.everHeld = true
	}
	m.go2(o.Screen)
	if o.Screen == ScreenHold {
		m.startRepairCheck()
	}
}

// InstallRefused: the runner did not start the install child (the drive
// rule, the plan file or the A/B sector changed before the start). Nothing
// was written.
func (m *Model) InstallRefused(why string) {
	m.running = ""
	m.Msg = ""
	m.refused, m.refusedDetail = why, ""
	m.ask(Action{Kind: ActGiveBackDrive})
	m.go2(ScreenRefused)
}

// Rechecked is the result of ActReadAB on the hold screen.
func (m *Model) Rechecked(state, why string) {
	m.Msg = ""
	m.recheckKnown = state == StateS0 || state == StateD1 || state == StateD2
	switch state {
	case StateS0, StateD1, StateD2:
		k, t := "?", "?"
		if m.Plan != nil {
			k, t = m.Plan.Keeper, m.Plan.Target
		}
		m.recheck = "Checked again: " + stateSentence(state, k, t)
		m.recheckWhy = why
	default:
		m.recheck = txtRecheckUnknown
		m.recheckWhy = why
	}
}

// BundleDone tells where the log bundle went.
func (m *Model) BundleDone(where string) { m.Bundle = where }

// Persisted is what the runner keeps in /run across a restart of the UI
// process (busybox respawns it). It holds no secret.
type Persisted struct {
	Running  string   `json:"running"` // "", "install"
	Screen   string   `json:"screen"`  // the end screen, if any
	Outcome  *Outcome `json:"outcome,omitempty"`
	EverHeld bool     `json:"ever_held"`
	Bundle   string   `json:"bundle,omitempty"`
	Panel    bool     `json:"panel_confirmed,omitempty"`
}

// Persist is the state to keep.
func (m *Model) Persist() Persisted {
	p := Persisted{Running: "", EverHeld: m.everHeld, Outcome: m.Outcome, Bundle: m.Bundle, Panel: m.panelOK}
	if m.running == "install" || m.running == "repair" && m.repair == repairRunning || m.running == "switch" {
		p.Running = m.running
	}
	switch m.Screen {
	case ScreenDone, ScreenStopped, ScreenHold:
		p.Screen = m.Screen.String()
	}
	return p
}

// Resume restores a persisted state in a new UI process: an install that
// still runs goes to the progress screen (and its end is read when the
// supervisor records it), a hold stays a hold.
func (m *Model) Resume(p Persisted, pl *plan.Plan) {
	m.everHeld = p.EverHeld
	m.panelOK = p.Panel
	m.Plan = pl
	m.Bundle = p.Bundle
	if p.Running == "install" {
		m.running = "install"
		m.go2(ScreenProgress)
		return
	}
	switch p.Running {
	case "repair":
		m.Outcome = p.Outcome
		m.running, m.repair = "repair", repairRunning
		m.Prog = NewProgress(false, time.Time{})
		m.go2(ScreenRepair)
		return
	case "switch":
		m.running = "switch"
		m.go2(ScreenSwitch)
		return
	}
	if p.Outcome != nil {
		m.Outcome = p.Outcome
		m.go2(p.Outcome.Screen)
	}
	if m.everHeld {
		m.go2(ScreenHold)
		m.startRepairCheck()
	}
}

// repairState is where the repair of the hold is.
type repairState int

const (
	repairNone     repairState = iota // no check (no plan: a hold after a switch)
	repairChecking                    // the dry run runs
	repairOffer                       // the dry run: a repair is needed and possible (exit 5)
	repairRefused                     // the dry run or a repair refused (exit 1): nothing written
	repairRunning                     // repair --yes runs
	repairDead                        // a repair ended with the state not known (exit 3): no more repair
)

// startRepairCheck runs the repair's dry run when the hold starts (owner
// Q4, DEVMODE-R4 §11): it reads only. Without the plan of this install
// there is nothing to repair with.
func (m *Model) startRepairCheck() {
	if m.Plan == nil || m.running != "" || m.repair == repairDead {
		return
	}
	m.running, m.repair = "repair", repairChecking
	m.ask(Action{Kind: ActRepairCheck})
}

// ToolEnded is the end of a repair or switch child.
func (m *Model) ToolEnded(r ToolResult) {
	m.running = ""
	m.Msg = ""
	switch r.Tool {
	case "switch":
		m.switchEnded(r)
		return
	}
	exit := r.Exit()
	switch {
	case exit == 0 && r.Safe:
		// the slot state is valid and the kept slot starts: the hold ends
		// (owner Q3)
		m.everHeld, m.repair = false, repairNone
		m.safeText = plainAB(r.After)
		m.go2(ScreenSafe)
	case r.DryRun && exit == 5:
		m.repair = repairOffer
		m.go2(ScreenHold)
	case !r.DryRun && exit == 1 && m.repairTries < 2:
		// nothing was written: one more try (as the tool, at most once)
		m.repair, m.repairWhy = repairOffer, plainReason(r.Result)
		m.go2(ScreenHold)
	case !r.DryRun && exit != 1:
		// exit 3 (or any other end of a write): the state is not known
		m.repair, m.repairWhy = repairDead, plainReason(r.Stop+" "+r.Result)
		m.everHeld = true
		m.go2(ScreenHold)
	default:
		m.repair, m.repairWhy = repairRefused, plainReason(r.Result)
		m.go2(ScreenHold)
	}
}

// plainAB says which slot starts, from an A/B sector, for the safe screen.
func plainAB(sec []byte) string {
	a, err := avbab.Parse(sec)
	if err != nil || a.Validate() != nil {
		return "The slot state is not known."
	}
	n := a.Pick()
	if n < 0 {
		return "The slot state is not known."
	}
	next, other := slotWord(avbab.SlotName(n)), slotWord(avbab.SlotName(1-n))
	if a.Slots[1-n].Bootable() {
		return fmt.Sprintf("Slot %s starts next. Slot %s is the fallback.", next, other)
	}
	return fmt.Sprintf("Slot %s starts next. Slot %s will not start until it is installed again.", next, other)
}

// openSwitch: the Switch item of the status menu ("Switch which slot
// starts", owner Q5): only to a slot that the status shows as working,
// while the other slot stays bootable; never during a child or a hold.
func (m *Model) openSwitch() {
	switch {
	case m.running != "":
		m.Msg = txtToolRuns
	case m.everHeld:
		m.go2(ScreenHold)
	case m.Unit == nil:
		m.Msg = txtNotRead
	case !m.Unit.ABValid:
		m.Msg = txtABInvalid
	default:
		m.go2(ScreenSwitch)
		m.Sel = 0
		if m.Unit.Next == 0 {
			m.Sel = 1 // the slot that does not start now
		}
		m.switchTo = m.Unit.Slots[m.Sel].Name
	}
}

// switchGate is why the switch to slot s is not possible, or "".
func (m *Model) switchGate(s string) string {
	if m.Unit == nil || !m.Unit.ABValid {
		return txtABInvalid
	}
	i := 0
	if s == "b" {
		i = 1
	}
	to, other := m.Unit.Slots[i], m.Unit.Slots[1-i]
	switch {
	case m.Unit.Next == i:
		return fmt.Sprintf(txtSwitchAlready, slotWord(to.Name))
	case !to.AB.Keeper():
		return fmt.Sprintf(txtSwitchNotWorking, slotWord(to.Name), plainState(to.AB))
	case !other.AB.Bootable():
		return fmt.Sprintf(txtSwitchNoFallback, slotWord(other.Name))
	}
	return ""
}

func (m *Model) keySwitch(k Key, now time.Time) {
	switch k {
	case KeyPrev, KeyNext:
		m.Sel = 1 - m.Sel
		m.switchTo = m.Unit.Slots[m.Sel].Name
	case KeyBack:
		if m.running == "" {
			m.go2(ScreenStatus)
		}
	case KeyOK:
		if m.running != "" {
			m.Msg = txtToolRuns
			return
		}
		if why := m.switchGate(m.switchTo); why != "" {
			m.Msg = why
			return
		}
		m.startHold(ActSwitch, now)
	}
}

func (m *Model) switchEnded(r ToolResult) {
	switch exit := r.Exit(); {
	case exit == 0:
		m.go2(ScreenStatus)
		m.Msg = "Done. " + plainAB(r.After)
	case exit == 1:
		m.go2(ScreenSwitch)
		m.Msg = "Not switched, nothing was written: " + plainReason(r.Result)
	default:
		// the state is not known after the write: keep the unit powered
		m.everHeld, m.repair = true, repairDead
		m.repairWhy = plainReason(r.Stop + " " + r.Result)
		m.go2(ScreenHold)
	}
}
